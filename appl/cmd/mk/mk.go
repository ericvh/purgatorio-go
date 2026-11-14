package main

import (
	"fmt"
	"os"
	"strings"
)

func mk(target string) {
	did := false
	nproc()
	nrep()
	runerrs = 0
	node := graph(target)
	if debug&dGraph != 0 {
		dumpn("new target\n", node)
		bout.Flush()
	}
	clrmade(node)
	for node.flags&notmade != 0 {
		if work(node, nil, nil) {
			did = true
		} else if waitup(1, nil) > 0 {
			if node.flags&(notmade|beingmade) != 0 {
				assert("must be run errors", runerrs != 0)
				break
			}
		}
	}
	if node.flags&beingmade != 0 {
		waitup(-1, nil)
	}
	for len(jobs) > 0 {
		waitup(-2, nil)
	}
	assert("target didn't get done", runerrs != 0 || node.flags&made != 0)
	if !did {
		fmt.Fprintf(bout, "mk: '%s' is up to date\n", node.name)
	}
}

func clrmade(n *Node) {
	n.flags &^= canpretend | pretending
	if !strings.Contains(n.name, "(") || n.time != 0 {
		n.flags |= canpretend
	}
	n.setmade(notmade)
	for _, a := range n.prereqs {
		if a.n != nil {
			clrmade(a.n)
		}
	}
}

func unpretend(n *Node) {
	n.setmade(notmade)
	n.flags &^= canpretend | pretending
	n.time = 0
}

func work(node, p *Node, parc *Arc) bool {
	did := false
	if node.flags&beingmade != 0 {
		return did
	}
	if node.flags&made != 0 && node.flags&pretending != 0 && p != nil && outofdate(p, parc, false) {
		if explain {
			fmt.Fprintf(os.Stdout, "unpretending %s(%d) because %s is out of date(%d)\n", node.name, node.time, p.name, p.time)
		}
		unpretend(node)
	}
	// We may be pretending and someone unpretended a prerequisite.
	if node.flags&made != 0 {
		if node.flags&pretending != 0 {
			node.time = 0
		} else {
			return did
		}
	}
	if len(node.prereqs) == 0 {
		if node.time == 0 {
			fmt.Fprintf(os.Stderr, "mk: don't know how to make '%s'\n", node.name)
			if kflag {
				node.flags |= beingmade
				runerrs++
			} else {
				exit()
			}
		} else {
			node.setmade(made)
		}
		return did
	}
	isready := true
	weoutofdate := aflag
	var ra *Arc
	for _, a := range node.prereqs {
		if a.n != nil {
			did = work(a.n, node, a) || did
			if a.n.flags&(notmade|beingmade) != 0 {
				isready = false
			}
			if outofdate(node, a, false) {
				weoutofdate = true
				if ra == nil || ra.n == nil || ra.n.time < a.n.time {
					ra = a
				}
			}
		} else if node.time == 0 {
			if ra == nil {
				ra = a
			}
			weoutofdate = true
		}
	}
	if !isready {
		return did
	}
	if !weoutofdate {
		node.setmade(made)
		return did
	}
	// Can we pretend to be made?
	if !iflag && node.time == 0 && node.flags&(pretending|canpretend) != 0 && p != nil && ra != nil && ra.n != nil && !outofdate(p, ra, false) {
		node.flags &^= canpretend
		node.setmade(made)
		if explain && node.flags&pretending == 0 {
			fmt.Fprintf(os.Stdout, "pretending %s has time %d\n", node.name, node.time)
		}
		node.flags |= pretending
		return did
	}
	// Out of date: we must do something. Rescan for pretenders.
	for _, a := range node.prereqs {
		if a.n != nil && a.n.flags&pretending != 0 {
			if explain {
				if ra != nil && ra.n != nil {
					fmt.Fprintf(bout, "unpretending %s because of %s because of %s\n", a.n.name, node.name, ra.n.name)
				} else {
					fmt.Fprintf(bout, "unpretending %s because of %s because of %s\n", a.n.name, node.name, "rule with no prerequisites")
				}
			}
			unpretend(a.n)
			did = work(a.n, node, a) || did
			isready = false
		}
	}
	if !isready {
		return did || work(node, p, parc)
	}
	return dorecipe(node) || did
}

func update(fake bool, node *Node) {
	if fake {
		node.setmade(beingmade)
	} else {
		node.setmade(made)
	}
	if node.flags&virtual == 0 && access(node.name) {
		node.time = timeof(node.name, true)
		node.flags &^= canpretend | pretending
		for _, a := range node.prereqs {
			if a.hasprog {
				outofdate(node, a, true)
			}
		}
	} else {
		node.time = 1
		for _, a := range node.prereqs {
			if a.n != nil && outofdate(node, a, true) {
				node.time = a.n.time
			}
		}
	}
}

// pcmp runs a P attribute's program: 2 if p is out of date with
// respect to q, else 1.
func pcmp(prog, p, q string) int {
	bout.Flush()
	pid, _ := pipecmd(fmt.Sprintf("%s '%s' '%s'\n", prog, p, q), nil, false)
	if waitpid(pid) != 0 {
		return 2
	}
	return 1
}

func outofdate(node *Node, arc *Arc, eval bool) bool {
	if arc.hasprog {
		key := node.name + "\xff" + arc.n.name
		ret, ok := outofdat[key]
		if !ok || eval {
			ret = pcmp(arc.prog, node.name, arc.n.name)
			outofdat[key] = ret
		}
		return ret-1 != 0
	}
	if strings.Contains(arc.n.name, "(") && arc.n.time == 0 {
		// A missing archive member.
		return true
	}
	return node.time < arc.n.time
}

// dorecipe starts the recipe that makes node and the other targets of
// its rule that are out of date.
func dorecipe(node *Node) bool {
	var r *Rule
	var aa *Arc
	for _, a := range node.prereqs {
		if !emptyrecipe(a.r) {
			aa = a
			r = a.r
		}
	}
	if r == nil {
		if node.flags&virtual == 0 && node.flags&norecipe == 0 {
			fmt.Fprintf(os.Stderr, "mk: no recipe to make '%s'\n", node.name)
			exit()
		}
		if strings.Contains(node.name, "(") && node.time == 0 {
			node.setmade(made)
		} else {
			update(false, node)
		}
		if tflag {
			if node.flags&virtual == 0 {
				touch(node.name)
			} else if explain {
				fmt.Fprintf(bout, "no touch of virtual '%s'\n", node.name)
			}
		}
		return false
	}
	// Build the node list.
	node.next = nil
	var head, ahead []string
	if r.attr&regexpattr != 0 {
		head = append(head, node.name)
		ahead = append(ahead, node.name)
	} else {
		for _, w := range r.alltargets {
			buf := w
			if r.attr&meta != 0 {
				buf = subst(aa.stem, w)
			}
			ahead = append(ahead, buf)
			n, ok := nodes[buf]
			if !ok {
				continue
			}
			if !aflag && n.time != 0 {
				ood := false
				for _, a := range n.prereqs {
					if a.n != nil && outofdate(n, a, false) {
						ood = true
						break
					}
				}
				if !ood {
					continue
				}
			}
			head = append(head, buf)
			if n == node {
				continue
			}
			n.next = node.next
			node.next = n
		}
	}
	for n := node; n != nil; n = n.next {
		if n.flags&ready == 0 {
			return false
		}
	}
	// Gather the parameters for the job.
	var lp, ln []string
	for n := node; n != nil; n = n.next {
		for _, a := range n.prereqs {
			if a.n != nil {
				lp = addw(lp, a.n.name)
				if outofdate(n, a, false) {
					ln = addw(ln, a.n.name)
					if explain {
						fmt.Fprintf(os.Stdout, "%s(%d) < %s(%d)\n", n.name, n.time, a.n.name, a.n.time)
					}
				}
			} else if explain {
				fmt.Fprintf(os.Stdout, "%s has no prerequisites\n", n.name)
			}
		}
		n.setmade(beingmade)
	}
	run(&Job{r: r, n: node, stem: aa.stem, match: aa.match, p: lp, np: ln, t: head, at: ahead})
	return true
}

func addw(w []string, s string) []string {
	for _, x := range w {
		if x == s {
			return w
		}
	}
	return append(w, s)
}
