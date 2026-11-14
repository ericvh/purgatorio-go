package main

// The rule file parser: Inferno's Plumbing module (appl/lib/plumbing.b),
// reading the rule language of plumbing(6).

import (
	"fmt"
	"strings"
	"syscall"

	"github.com/ericvh/purgatorio-go/sdk/regex"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

// Pattern is one line of a rule: field, verb, argument and, for
// "plumb start", the command's further words.
type Pattern struct {
	field  string
	pred   string
	arg    string
	extra  []string
	expand bool
	re     *regex.Re
}

// Rule is a rule's patterns followed by its actions.
type Rule struct {
	pattern []*Pattern
	action  []*Pattern
}

// loadRules reads each file in turn, or the user's default rules when
// there are none.
func loadRules(files []string) ([]*Rule, string) {
	if len(files) == 0 {
		user := readfile("/dev/user")
		if user == "" {
			return nil, "can't read /dev/user: " + sys.Errstr()
		}
		name := "/usr/" + user + "/plumbing"
		if _, err := sys.Stat(name); err != nil {
			name = "/usr/" + user + "/lib/plumbing"
		}
		files = []string{name}
	}
	var rules []*Rule
	for _, name := range files {
		file := readfile(name)
		if file == "" {
			return nil, "can't read " + name + ": " + sys.Errstr()
		}
		r, err := parseRules(name, file)
		if err != "" {
			return nil, err
		}
		rules = append(rules, r...)
	}
	return rules, ""
}

// readfile reads name in one read, as plumbing.b does.
func readfile(name string) string {
	fd, err := sys.Open(name, sys.OREAD)
	if err != nil {
		return ""
	}
	defer sys.Close(fd)
	d, err := sys.Fstat(fd)
	if err != nil {
		return ""
	}
	size := int(d.Length)
	if size == 0 {
		size = 1000
	}
	b := make([]byte, size)
	n, err := syscall.Read(fd, b)
	if err != nil || n <= 0 {
		return ""
	}
	return string(b[:n])
}

func parseRules(name, file string) ([]*Rule, string) {
	var rules []*Rule
	var pats []*Pattern // in file order
	lineno := 0
	for i := 0; i < len(file); {
		lineno++
		j := strings.IndexByte(file[i:], '\n')
		var line string
		if j < 0 {
			line, i = file[i:], len(file)
		} else {
			line, i = file[i:i+j], i+j+1
		}
		pat, err := pattern(line)
		if err != "" {
			return nil, fmt.Sprintf("%s:%d: %s", name, lineno, err)
		}
		if pat != nil {
			pats = append(pats, pat)
			continue
		}
		// A comment line does not end a rule; a blank one does.
		if pats == nil || !blank(line) {
			continue
		}
		r, err := rule(pats)
		if err != "" {
			return nil, fmt.Sprintf("%s:%d: %s", name, lineno-1, err)
		}
		rules = append(rules, r)
		pats = nil
	}
	if pats != nil {
		r, err := rule(pats)
		if err != "" {
			return nil, fmt.Sprintf("%s:%d: %s", name, lineno-1, err)
		}
		rules = append(rules, r)
	}
	return rules, ""
}

func blank(line string) bool {
	return strings.Trim(line, " \t") == ""
}

func pattern(line string) (*Pattern, string) {
	w := words(line)
	if w == nil {
		return nil, ""
	}
	if len(w) < 3 {
		return nil, "syntax error: too few words on line"
	}
	return &Pattern{field: w[0], pred: w[1], arg: w[2], extra: w[3:],
		expand: strings.Contains(line, "$")}, ""
}

func oneof(w string, ws ...string) bool {
	for _, s := range ws {
		if w == s {
			return true
		}
	}
	return false
}

// rule checks pats, which are in file order, from the last line back,
// so that it can tell an action that precedes a pattern.
func rule(pats []*Pattern) (*Rule, string) {
	npats, nacts := 0, 0
	haveto := false
	for k := len(pats) - 1; k >= 0; k-- {
		pat := pats[k]
		pred := pat.pred
		noextra := true
		switch pat.field {
		case "plumb":
			nacts++
			if !oneof(pred, "alwaysstart", "start", "to") {
				return nil, "illegal predicate " + pred + " in action"
			}
			switch pred {
			case "to", "alwaysstart":
				if pat.arg == "" {
					return nil, "\"plumb " + pred + "\" must have non-empty target"
				}
				haveto = true
			case "start":
				noextra = false
			}
			if npats != 0 {
				return nil, "actions must follow patterns in rule"
			}
		case "src", "dst", "dir", "kind", "attr", "data":
			if !oneof(pred, "is", "isdir", "isfile", "matches", "set") {
				return nil, "illegal predicate " + pred + " in pattern"
			}
			if pred == "matches" {
				pat.re, _ = regex.Compile(pat.arg, 1)
				if pat.re == nil {
					return nil, "error in regular expression '" + pat.arg + "'"
				}
			}
			npats++
		}
		if noextra && len(pat.extra) != 0 {
			return nil, "too many words in '" + pat.field + "' pattern"
		}
	}
	if !haveto {
		return nil, "rule must have \"plumb to\" action"
	}
	// The last nacts lines are the actions; the npats before them the
	// patterns.
	n := len(pats)
	r := &Rule{}
	r.action = append(r.action, pats[n-nacts:]...)
	r.pattern = append(r.pattern, pats[n-nacts-npats:n-nacts]...)
	return r, ""
}

// words splits a rule line into words: blanks separate them, # starts a
// comment, and single quotes protect text, a doubled quote standing for one.
func words(line string) []string {
	var ws []string
	i := 0
	for {
		for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
			i++
		}
		if i == len(line) || line[i] == '#' {
			break
		}
		if line[i] == '\'' {
			var w strings.Builder
			i++
			for i < len(line) {
				c := line[i]
				i++
				if c == '\'' {
					if i == len(line) || line[i] != '\'' {
						break
					}
					i++
				}
				w.WriteByte(c)
			}
			ws = append(ws, w.String())
			continue
		}
		start := i
		for i < len(line) && line[i] != ' ' && line[i] != '\t' {
			i++
		}
		ws = append(ws, line[start:i])
	}
	return ws
}
