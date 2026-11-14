// Command chmod changes file permissions (Inferno chmod(1)).
package main

import (
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const (
	user  = 0o700
	group = 0o070
	other = 0o007
	all   = user | group | other
	read  = 0o444
	write = 0o222
	exec  = 0o111
	dmtmp = 0x04000000
)

func usage() {
	os.Stderr.WriteString("usage: chmod [8r]777 file ... or chmod [augo][+-=][rwxal] file ...\n")
	sys.Exits("fail:usage")
}

func main() {
	if len(os.Args) < 3 {
		usage()
	}
	m := os.Args[1]
	mask := uint32(all)
	m = strings.TrimPrefix(m, "8r")
	mode, ok := octal(m)
	if !ok {
		var good bool
		good, mask, mode = parsemode(m)
		if !good {
			os.Stderr.WriteString("chmod: bad mode '" + m + "'\n")
			usage()
		}
	}
	for _, f := range os.Args[2:] {
		d, err := sys.Stat(f)
		if err != nil {
			os.Stderr.WriteString("chmod: cannot stat " + f + ": " + sys.Errstr() + "\n")
			continue
		}
		nd := sys.NullDir()
		nd.Mode = d.Mode&^mask | mode&mask
		if err := sys.Wstat(f, &nd); err != nil {
			os.Stderr.WriteString("chmod: cannot wstat " + f + ": " + sys.Errstr() + "\n")
		}
	}
}

// octal parses all of s as an octal number, as String->toint(s, 8) with
// nothing left over.
func octal(s string) (uint32, bool) {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	neg := false
	if i < len(s) && (s[i] == '-' || s[i] == '+') {
		neg = s[i] == '-'
		i++
	}
	start := i
	n := uint32(0)
	for ; i < len(s) && s[i] >= '0' && s[i] <= '7'; i++ {
		n = n*8 + uint32(s[i]-'0')
	}
	if i == start || i != len(s) {
		return 0, false
	}
	if neg {
		n = -n
	}
	return n, true
}

// parsemode parses a symbolic mode such as "go-w" into a mask of the bits
// to change and their new values.
func parsemode(spec string) (bool, uint32, uint32) {
	mask := uint32(sys.DMAPPEND | sys.DMEXCL | dmtmp)
	i := 0
loop:
	for ; i < len(spec); i++ {
		switch spec[i] {
		case 'u':
			mask |= user
		case 'g':
			mask |= group
		case 'o':
			mask |= other
		case 'a':
			mask |= all
		default:
			break loop
		}
	}
	if i == len(spec) {
		return false, 0, 0
	}
	if i == 0 {
		mask |= all
	}
	op := spec[i]
	i++
	if op != '+' && op != '-' && op != '=' {
		return false, 0, 0
	}
	mode := uint32(0)
	for ; i < len(spec); i++ {
		switch spec[i] {
		case 'r':
			mode |= read
		case 'w':
			mode |= write
		case 'x':
			mode |= exec
		case 'a':
			mode |= sys.DMAPPEND
		case 'l':
			mode |= sys.DMEXCL
		case 't':
			mode |= dmtmp
		default:
			return false, 0, 0
		}
	}
	if op == '+' || op == '-' {
		mask &= mode
	}
	if op == '-' {
		mode = ^mode
	}
	return true, mask, mode
}
