package main

import "strings"

// Url schemes, as Inferno's url(2) numbers them.
const (
	noscheme = iota
	schHTTP
	schHTTPS
	schFTP
	schFILE
	schGOPHER
	schMAILTO
	schNEWS
	schNNTP
	schTELNET
	schWAIS
	schPROSPERO
	schJAVASCRIPT
	schUNKNOWN
)

var schemes = []string{"", "http", "https", "ftp", "file", "gopher", "mailto", "news", "nntp",
	"telnet", "wais", "prospero", "javascript", "unknown"}

// parsedURL is url(2)'s ParsedUrl: only the pieces given are filled
// in, so a relative url can be made absolute against a base.
type parsedURL struct {
	scheme       int
	user, passwd string
	host, port   string
	pstart, path string // pstart is "/" or ""
	query, frag  string
}

// split cuts s at the first c, dropping c.
func split(s, c string) (string, string) {
	a, b, _ := strings.Cut(s, c)
	return a, b
}

func isschemechar(r rune) bool {
	return r == '-' || r == '.' || r == '+' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
}

func makeurl(surl string) *parsedURL {
	u := &parsedURL{}
	sch, rest := split(surl, ":")
	if rest == "" {
		rest = sch
		sch = ""
	} else if strings.IndexFunc(sch, func(r rune) bool { return !isschemechar(r) }) >= 0 {
		rest = surl
	} else {
		u.scheme = schUNKNOWN
		sch = strings.ToLower(sch)
		for i, s := range schemes {
			if s == sch {
				u.scheme = i
				break
			}
		}
	}
	if u.scheme == schMAILTO || u.scheme == schJAVASCRIPT {
		u.path = rest
		return u
	}
	if strings.HasPrefix(rest, "//") {
		netloc, path := rest[2:], ""
		if i := strings.IndexByte(netloc, '/'); i >= 0 {
			netloc, path = netloc[:i], netloc[i+1:]
		}
		u.path = path
		u.pstart = "/"
		if u.scheme == schFILE {
			u.host = netloc
		} else {
			up, hp := split(netloc, "@")
			if hp == "" {
				hp = up
			} else {
				u.user, u.passwd = split(up, ":")
			}
			u.host, u.port = split(hp, ":")
		}
	} else if strings.HasPrefix(rest, "/") {
		u.pstart = "/"
		u.path = rest[1:]
	} else {
		u.path = rest
	}
	if u.scheme == schFILE {
		if u.host == "" {
			u.host = "localhost"
		}
	} else {
		u.path, u.frag = split(u.path, "#")
		u.path, u.query = split(u.path, "?")
	}
	return u
}

func (u *parsedURL) String() string {
	s := ""
	if u.scheme > 0 && u.scheme < len(schemes) {
		s = schemes[u.scheme] + ":"
	}
	if u.host != "" {
		s += "//"
		if u.user != "" {
			s += u.user
			if u.passwd != "" {
				s += ":" + u.passwd
			}
			s += "@"
		}
		s += u.host
		if u.port != "" {
			s += ":" + u.port
		}
	}
	s += u.pstart + u.path
	if u.query != "" {
		s += "?" + u.query
	}
	if u.frag != "" {
		s += "#" + u.frag
	}
	return s
}

// makeabsolute fills in what u lacks from the base b.
func (u *parsedURL) makeabsolute(b *parsedURL) {
	if u.scheme != noscheme && u.scheme != schHTTP {
		return
	}
	if u.host == "" && u.path == "" && u.pstart == "" && u.query == "" && u.frag == "" {
		*u = *b
		return
	}
	if u.scheme == noscheme {
		u.scheme = b.scheme
	}
	if u.host != "" {
		return
	}
	u.user, u.passwd, u.host, u.port = b.user, b.passwd, b.host, b.port
	if u.pstart == "/" {
		return
	}
	u.pstart = "/"
	if u.path == "" {
		u.path = b.path
		if u.query == "" {
			u.query = b.query
		}
	} else {
		p1 := b.path[:strings.LastIndexByte(b.path, '/')+1]
		u.path = canonize(p1 + u.path)
	}
}

// canonize removes . and .. from s.
func canonize(s string) string {
	i := strings.LastIndexByte(s, '/') + 1
	base, file := s[:i], s[i:]
	var rev []string
	for _, p := range strings.FieldsFunc(base, func(r rune) bool { return r == '/' }) {
		switch p {
		case "..":
			if len(rev) > 0 {
				rev = rev[:len(rev)-1]
			}
		case ".":
		default:
			rev = append(rev, p)
		}
	}
	ans := strings.Join(rev, "/")
	if ans != "" {
		ans += "/"
	}
	return ans + file
}
