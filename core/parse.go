package core

import (
	"strings"

	"github.com/ericvh/purgatorio-go/abi"
)

// Cmdtab is one control message a device accepts, Inferno's Cmdtab:
// its name and its number of fields, the name included (0 for any).
type Cmdtab struct {
	Index int
	Cmd   string
	Narg  int
}

// ParseCmd splits a control message into fields as Inferno's parsecmd
// does: a trailing newline is dropped and fields may be quoted.
func ParseCmd(b []byte) []string {
	s := strings.TrimSuffix(string(b), "\n")
	var out []string
	for {
		s = strings.TrimLeft(s, " \t\r\n")
		if s == "" {
			return out
		}
		var f strings.Builder
		quoted := false
		i := 0
		for i < len(s) {
			c := s[i]
			if !quoted && (c == ' ' || c == '\t' || c == '\r' || c == '\n') {
				break
			}
			if c == '\'' {
				if quoted && i+1 < len(s) && s[i+1] == '\'' {
					f.WriteByte('\'')
					i += 2
					continue
				}
				quoted = !quoted
				i++
				continue
			}
			f.WriteByte(c)
			i++
		}
		out = append(out, f.String())
		s = s[i:]
	}
}

// LookupCmd finds the message f in tab, with Inferno's errors for an
// empty, unknown or wrongly sized message.
func LookupCmd(f []string, tab []Cmdtab) (Cmdtab, error) {
	if len(f) == 0 {
		return Cmdtab{}, Error("empty control message")
	}
	for _, ct := range tab {
		if ct.Cmd != "*" && ct.Cmd != f[0] {
			continue
		}
		if ct.Narg != 0 && ct.Narg != len(f) {
			return ct, CmdError(f, abi.Ecmdargs)
		}
		return ct, nil
	}
	return Cmdtab{}, CmdError(f, "unknown control message")
}

// CmdError is Inferno's cmderror: the error, then the message quoted.
func CmdError(f []string, e string) error {
	var b strings.Builder
	b.WriteString(e + " \"")
	for i, s := range f {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(quote(s))
	}
	s := b.String()
	if len(s) > ERRMAX-10 {
		s = s[:ERRMAX-10]
	}
	return Error(s + "\"")
}

// quote is Plan 9's %q.
func quote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\r\n'") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
