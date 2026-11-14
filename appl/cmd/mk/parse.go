package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"unicode/utf8"
)

// Symbol tables, one per name space of mk's symtab.
var (
	vars      = map[string][]string{} // variable -> value
	targets   = map[string][]*Rule{}  // target -> rules, in mk's chain order
	times     = map[string]int{}      // file -> time
	nodes     = map[string]*Node{}    // target name -> node
	aggs      = map[string]int{}      // aggregate -> time
	bitched   = map[string]bool{}     // complained an aggregate is missing
	noexport  = map[string]bool{}
	override  = map[string]bool{}
	outofdat  = map[string]int{} // n1\377n2 -> 2 (out of date) or 1
	makevar   = map[string]bool{}
	bulked    = map[string]bool{}
	weset     = map[string]bool{} // variables set in the mkfile
	internals = map[string]bool{} // mk's own recipe variables
)

func setvar(name string, value []string) {
	vars[name] = value
	makevar[name] = true
}

// input is a mkfile being read, with one rune of push back.
type input struct {
	b    []byte
	p    int
	last int
}

func (in *input) getc() int {
	if in.p >= len(in.b) {
		in.last = 0
		return -1
	}
	r, n := utf8.DecodeRune(in.b[in.p:])
	in.p += n
	in.last = n
	return int(r)
}

func (in *input) getb() int {
	if in.p >= len(in.b) {
		in.last = 0
		return -1
	}
	in.p++
	in.last = 1
	return int(in.b[in.p-1])
}

func (in *input) ungetc() {
	in.p -= in.last
	in.last = 0
}

// The stack of files being parsed, for error positions.
type ipos struct {
	file string
	line int
}

var inputs []ipos

func parse(f string, b []byte, varoverride bool) {
	inputs = append(inputs, ipos{infile, mkinline})
	infile = f
	mkinline = 1
	in := &input{b: b}
	var buf strings.Builder
	for assline(in, &buf) {
		hline := mkinline
		v, head, tail, attr, prog, hasprog := rhead(buf.String())
		switch v {
		case '<':
			p := strings.Join(tail, " ")
			if p == "" {
				syntax(mkinline)
				fmt.Fprintf(os.Stderr, "missing include file name\n")
				exit()
			}
			nb, err := readfile(p)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: skipping missing include file: ")
				perror(p)
			} else {
				parse(p, nb, false)
			}
		case '|':
			p := strings.Join(tail, " ")
			if p == "" {
				syntax(mkinline)
				fmt.Fprintf(os.Stderr, "missing include program name\n")
				exit()
			}
			execinit()
			pid, out := pipecmd(p, envy, true)
			parse(p, out, false)
			if waitpid(pid) != 0 {
				fmt.Fprintf(os.Stderr, "bad include program status\n")
				exit()
			}
		case ':':
			body := rbody(in)
			addrules(head, tail, body, attr, hline, prog, hasprog)
		case '=':
			if len(head) > 1 {
				syntax(mkinline)
				fmt.Fprintf(os.Stderr, "multiple vars on left side of assignment\n")
				exit()
			}
			set := true
			if override[head[0]] {
				set = varoverride
			} else if varoverride {
				override[head[0]] = true
			}
			if set {
				setvar(head[0], tail)
				weset[head[0]] = true
			}
			if attr != 0 {
				noexport[head[0]] = true
			}
		default:
			syntax(hline)
			fmt.Fprintf(os.Stderr, "expected one of :<=\n")
			exit()
		}
	}
	top := inputs[len(inputs)-1]
	inputs = inputs[:len(inputs)-1]
	infile, mkinline = top.file, top.line
}

func addrules(head, tail []string, body *string, attr, hline int, prog string, hasprog bool) {
	// The first rule that is not a meta-rule names the default targets.
	if target1 == nil && attr&regexpattr == 0 {
		meta := false
		for _, w := range head {
			if charin(w, "%&") >= 0 {
				meta = true
				break
			}
		}
		if !meta {
			target1 = append([]string(nil), head...)
		}
	}
	for _, w := range head {
		addrule(w, tail, body, head, attr, hline, prog, hasprog)
	}
}

// rhead splits a line at its first unquoted :, = or < and decodes the
// attributes after it.
func rhead(line string) (int, []string, []string, int, string, bool) {
	i := charin(line, ":=<")
	if i < 0 {
		return '?', nil, nil, 0, "", false
	}
	sep := int(line[i])
	lhs := line[:i]
	p := line[i+1:]
	if sep == '<' && strings.HasPrefix(p, "|") {
		sep = '|'
		p = p[1:]
	}
	attr := 0
	prog := ""
	hasprog := false
	if sep == '=' {
		pp := charin(p, "= \t")
		if pp >= 0 && p[pp] == '=' {
			for _, r := range p[:pp] {
				if r != 'U' {
					syntax(mkinline)
					fmt.Fprintf(os.Stderr, "unknown attribute '%c'\n", p[0])
					exit()
				}
				attr = 1
			}
			p = p[pp+1:]
		}
	}
	if sep == ':' && p != "" && p[0] != ' ' && p[0] != '\t' {
		for p != "" {
			r, n := utf8.DecodeRuneInString(p)
			if r == ':' {
				break
			}
			ea := p[n-1]
			p = p[n:]
			switch r {
			case 'D':
				attr |= delattr
			case 'E':
				attr |= nominuse
			case 'n':
				attr |= novirt
			case 'N':
				attr |= norec
			case 'P':
				j := strings.IndexByte(p, ':')
				if j < 0 {
					syntax(mkinline)
					fmt.Fprintf(os.Stderr, "missing trailing :\n")
					exit()
				}
				prog = p[:j]
				hasprog = true
				p = p[j:]
			case 'Q':
				attr |= quiet
			case 'R':
				attr |= regexpattr
			case 'U':
				attr |= upd
			case 'V':
				attr |= vir
			default:
				syntax(mkinline)
				fmt.Fprintf(os.Stderr, "unknown attribute '%c'\n", ea)
				exit()
			}
		}
		if !strings.HasPrefix(p, ":") {
			syntax(mkinline)
			fmt.Fprintf(os.Stderr, "missing trailing :\n")
			exit()
		}
		p = p[1:]
	}
	h := stow(lhs)
	if h[0] == "" && sep != '<' && sep != '|' {
		syntax(mkinline - 1)
		fmt.Fprintf(os.Stderr, "no var on left side of assignment/rule\n")
		exit()
	}
	return sep, h, stow(p), attr, prog, hasprog
}

// rbody reads a recipe: the following lines that start with white
// space, less that first character. Lines starting with # are kept.
func rbody(in *input) *string {
	var b strings.Builder
	lastr := '\n'
	for {
		r := in.getc()
		if r < 0 {
			break
		}
		if lastr == '\n' {
			if r == '#' {
				b.WriteRune(rune(r))
			} else if r != ' ' && r != '\t' {
				in.ungetc()
				break
			}
		} else {
			b.WriteRune(rune(r))
		}
		lastr = rune(r)
		if r == '\n' {
			mkinline++
		}
	}
	s := b.String()
	return &s
}

// assline assembles a line, skipping blank lines and comments, eliding
// escaped newlines and running `{...} commands.
func assline(in *input, buf *strings.Builder) bool {
	buf.Reset()
	for {
		c := nextrune(in, true)
		if c < 0 {
			break
		}
		switch c {
		case '\r':
			continue
		case '\n':
			if buf.Len() != 0 {
				return true
			}
		case '\\', '\'', '"':
			buf.WriteRune(rune(c))
			if !escapetoken(in, buf, true, c) {
				exit()
			}
		case '`':
			if !bquote(in, buf) {
				exit()
			}
		case '#':
			lastc := '#'
			for {
				c = in.getb()
				if c == '\n' {
					break
				}
				if c < 0 {
					return buf.Len() != 0
				}
				if c != '\r' {
					lastc = rune(c)
				}
			}
			mkinline++
			if lastc == '\\' {
				break
			}
			if buf.Len() != 0 {
				return true
			}
		default:
			buf.WriteRune(rune(c))
		}
	}
	return buf.Len() != 0
}

// bquote runs a back-quoted command and puts its output in buf.
func bquote(in *input, buf *strings.Builder) bool {
	line := mkinline
	c := in.getc()
	for c == ' ' || c == '\t' {
		c = in.getc()
	}
	term := '`'
	if c == '{' {
		term = '}'
		c = in.getc()
		for c == ' ' || c == '\t' {
			c = in.getc()
		}
	}
	var cmd strings.Builder
	for ; c > 0; c = nextrune(in, false) {
		if rune(c) == term {
			cmd.WriteByte('\n')
			execinit()
			buf.WriteString(execsh("", cmd.String(), true, envy))
			return true
		}
		if c == '\n' {
			break
		}
		if c == '\'' || c == '"' || c == '\\' {
			cmd.WriteRune(rune(c))
			if !escapetoken(in, &cmd, true, c) {
				return false
			}
			continue
		}
		cmd.WriteRune(rune(c))
	}
	fmt.Fprintf(os.Stderr, "mk: %s:%d: syntax error; ", infile, line)
	fmt.Fprintf(os.Stderr, "missing closing %c after `\n", term)
	return false
}

var savec int

// nextrune returns the next rune, eliding escaped newlines or turning
// them into blanks.
func nextrune(in *input, elide bool) int {
	if savec != 0 {
		c := savec
		savec = 0
		return c
	}
	for {
		c := in.getc()
		if c == '\\' {
			c2 := in.getc()
			if c2 == '\r' {
				savec = c2
				c2 = in.getc()
			}
			if c2 == '\n' {
				savec = 0
				mkinline++
				if elide {
					continue
				}
				return ' '
			}
			in.ungetc()
		}
		if c == '\n' {
			mkinline++
		}
		return c
	}
}

// escapetoken copies a single-quoted token; other escapes are literal.
func escapetoken(in *input, buf *strings.Builder, preserve bool, esc int) bool {
	if esc != '\'' {
		return true
	}
	line := mkinline
	for {
		c := nextrune(in, false)
		if c <= 0 {
			break
		}
		if c == '\'' {
			if preserve {
				buf.WriteRune(rune(c))
			}
			c = in.getc()
			if c < 0 {
				break
			}
			if c != '\'' {
				in.ungetc()
				return true
			}
		}
		buf.WriteRune(rune(c))
	}
	fmt.Fprintf(os.Stderr, "mk: %s:%d: syntax error; ", infile, line)
	fmt.Fprintf(os.Stderr, "missing closing %c\n", esc)
	return false
}

// Variable substitution.

const varpunct = "!\"#$%&'()*+,-./:;<=>?@[\\]^`{|}~"

func isnamerune(r rune) bool { return r > ' ' && !strings.ContainsRune(varpunct, r) }

// shname returns the length of the variable name at the start of s.
func shname(s string) int {
	for i, r := range s {
		if !isnamerune(r) {
			return i
		}
	}
	return len(s)
}

// varsub expands the reference after a $ and returns the rest of s.
func varsub(s string) ([]string, string) {
	if strings.HasPrefix(s, "{") {
		return expandvar(s)
	}
	name, rest, ok := varname(s)
	if !ok {
		return nil, rest
	}
	return varmatch(name, rest)
}

func varname(s string) (string, string, bool) {
	n := shname(s)
	if n == 0 {
		syntax(mkinline)
		fmt.Fprintf(os.Stderr, "missing variable name <%s>\n", s)
		return "", s, false
	}
	return s[:n], s[n:], true
}

func varmatch(name, s string) ([]string, string) {
	if v, ok := vars[name]; ok {
		for _, w := range v {
			if w != "" {
				return append([]string(nil), v...), s
			}
		}
	}
	return nil, strings.TrimLeft(s, " \t")
}

func expandvar(s string) ([]string, string) {
	begin := s
	name, s, ok := varname(s[1:])
	if !ok {
		return nil, s
	}
	if strings.HasPrefix(s, "}") {
		return varmatch(name, s[1:])
	}
	if !strings.HasPrefix(s, ":") {
		syntax(mkinline)
		fmt.Fprintf(os.Stderr, "bad variable name <%s>\n", name)
		return nil, s
	}
	cp := s[1:]
	end := charin(cp, "}")
	if end < 0 {
		syntax(mkinline)
		fmt.Fprintf(os.Stderr, "missing '}': %s\n", begin)
		exit()
	}
	rest := cp[end+1:]
	v, ok := vars[name]
	if !ok {
		return []string{name}, rest
	}
	return subsub(v, cp[:end]), rest
}

// extractpat returns the words of s before the first character in
// term, and the rest of s from that character.
func extractpat(s, term string) ([]string, string) {
	i := charin(s, term)
	if i < 0 {
		return stow(s), ""
	}
	if i == 0 {
		return nil, s
	}
	return stow(s[:i]), s[i:]
}

// subsub applies the substitution A%B=C%D in s to each word of v.
func subsub(v []string, s string) []string {
	var b, c, d []string
	a, cp := extractpat(s, "=%&")
	if strings.HasPrefix(cp, "%") || strings.HasPrefix(cp, "&") {
		b, cp = extractpat(cp[1:], "=")
	}
	if strings.HasPrefix(cp, "=") {
		c, cp = extractpat(cp[1:], "&%")
	}
	pct := strings.HasPrefix(cp, "%") || strings.HasPrefix(cp, "&")
	if pct {
		d = stow(cp[1:])
	} else if cp != "" {
		d = stow(cp)
	}
	var out []string
	for _, vs := range v {
		var h []string
		ok, nmid, enda := submatch(vs, a, b)
		if ok {
			if c != nil {
				h = append(h, c...)
			}
			if pct && nmid > 0 {
				if h != nil {
					h[len(h)-1] += enda[:nmid]
				} else {
					h = []string{enda[:nmid]}
				}
			}
			if d != nil && d[0] != "" {
				if h != nil {
					h[len(h)-1] += d[0]
					h = append(h, d[1:]...)
				} else {
					h = append(h, d...)
				}
			}
		}
		if h == nil {
			h = []string{vs}
		}
		out = append(out, h...)
	}
	return out
}

func submatch(s string, a, b []string) (bool, int, string) {
	n := 0
	found := false
	for _, w := range a {
		n = len(w)
		if strings.HasPrefix(s, w) {
			found = true
			break
		}
	}
	if a != nil && !found {
		return false, 0, ""
	}
	enda := s[n:]
	nmid := len(enda)
	found = false
	for _, w := range b {
		if len(w) <= nmid && strings.HasSuffix(enda, w) {
			nmid -= len(w)
			found = true
			break
		}
	}
	if b != nil && !found {
		return false, nmid, enda
	}
	return true, nmid, enda
}

// Words.

// stow breaks s into words; an empty s is one empty word.
func stow(s string) []string {
	var head []string
	for s != "" {
		var w []string
		w, s = nextword(s)
		if w == nil {
			break
		}
		head = append(head, w...)
	}
	if head == nil {
		head = []string{""}
	}
	return head
}

// nextword breaks out a word, handling quotes and variable expansions.
func nextword(s string) ([]string, string) {
	orig := s
	var b strings.Builder
	var head []string
	cp := strings.TrimLeft(s, " \t")
loop:
	for cp != "" {
		r, n := utf8.DecodeRuneInString(cp)
		cp = cp[n:]
		switch r {
		case ' ', '\t', '\n':
			break loop
		case '\\', '"':
			b.WriteRune(r)
		case '\'':
			var ok bool
			cp, ok = expandquote(cp, &b)
			if !ok {
				fmt.Fprintf(os.Stderr, "missing closing quote: %s\n", orig)
				exit()
			}
		case '$':
			var w []string
			w, cp = varsub(cp)
			if w == nil {
				break
			}
			if b.Len() != 0 {
				w[0] = b.String() + w[0]
				b.Reset()
			}
			if head != nil {
				head[len(head)-1] += w[0]
				head = append(head, w[1:]...)
			} else {
				head = w
			}
		default:
			b.WriteRune(r)
		}
	}
	if b.Len() != 0 {
		if head != nil {
			head[len(head)-1] += b.String()
		} else {
			head = []string{b.String()}
		}
	}
	return head, cp
}

// expandquote copies a single-quoted string; s follows the quote.
func expandquote(s string, b *strings.Builder) (string, bool) {
	for s != "" {
		r, n := utf8.DecodeRuneInString(s)
		s = s[n:]
		if r == '\'' {
			if strings.HasPrefix(s, "'") {
				s = s[1:]
			} else {
				return s, true
			}
		}
		b.WriteRune(r)
	}
	return "", false
}

// charin returns the index of the first character of s in pat that is
// not quoted or inside a ${...}, or -1.
func charin(s, pat string) int {
	vargen := false
	i := 0
	for i < len(s) {
		r, n := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == '\'':
			j := squote(s, i+1)
			if j < 0 {
				return -1
			}
			i = j
		case r == '$':
			if i+1 < len(s) && s[i+1] == '{' {
				vargen = true
			}
		case r == '}':
			if vargen {
				vargen = false
			} else if strings.ContainsRune(pat, r) {
				return i
			}
		default:
			if !vargen && strings.ContainsRune(pat, r) {
				return i
			}
		}
		i += n
	}
	if vargen {
		syntax(mkinline)
		fmt.Fprintf(os.Stderr, "missing closing } in pattern generator\n")
	}
	return -1
}

// squote returns the index of the quote closing a token that starts at
// i, or -1.
func squote(s string, i int) int {
	for i < len(s) {
		if s[i] == '\'' {
			if i+1 < len(s) && s[i+1] == '\'' {
				i += 2
				continue
			}
			return i
		}
		i++
	}
	syntax(mkinline)
	fmt.Fprintf(os.Stderr, "missing closing '\n")
	return -1
}

// Debugging dumps.

var addrs = map[any]int{}

func addr(x any) int {
	switch v := x.(type) {
	case *Rule:
		if v == nil {
			return 0
		}
	case *Node:
		if v == nil {
			return 0
		}
	case *Arc:
		if v == nil {
			return 0
		}
	}
	if a, ok := addrs[x]; ok {
		return a
	}
	a := 0x1000 + 0x10*len(addrs)
	addrs[x] = a
	return a
}

func dumpw(s string, w []string) {
	bout.WriteString(s)
	for _, x := range w {
		fmt.Fprintf(bout, " '%s'", x)
	}
	bout.WriteByte('\n')
}

func dumpv(s string) {
	fmt.Fprintf(bout, "%s:\n", s)
	names := make([]string, 0, len(vars))
	for k := range vars {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		fmt.Fprintf(bout, "\t%s=", k)
		for _, w := range vars[k] {
			fmt.Fprintf(bout, "'%s'", w)
		}
		bout.WriteString("\n")
	}
}
