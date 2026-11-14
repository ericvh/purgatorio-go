package main

import (
	"fmt"
	"strings"
)

// Record types, the subset dns knows.
const (
	Ta     = 1
	Tns    = 2
	Tcname = 5
	Tsoa   = 6
	Tmb    = 7
	Tptr   = 12
	Thinfo = 13
	Tmx    = 15
	Tall   = 255
)

// Classes.
const (
	Cin  = 1
	Call = 255
)

// Opcodes, response codes and flags.
const (
	Oquery = 0 << 11

	Rok            = 0
	Rformat        = 1
	Rserver        = 2
	Rname          = 3
	Runimplemented = 4
	Rrefused       = 5
	Rmask          = 0xF

	Fresp       = 1 << 15
	Fauth       = 1 << 10
	Ftrunc      = 1 << 9
	Frecurse    = 1 << 8
	Fcanrecurse = 1 << 7
)

// QR is a question.
type QR struct {
	name  string
	rtype int
	class int
}

func (q *QR) text() string {
	s := q.name + " " + rrtypename(q.rtype)
	if q.class != Cin {
		s += fmt.Sprintf(" [c=%d]", q.class)
	}
	return s
}

// Kinds of RR, Limbo's pick.
const (
	kOther = iota
	kError
	kHost
	kHinfo
	kMx
	kSoa
	kA
)

// RR is a resource record; ttl is the time it expires.
type RR struct {
	name   string
	rtype  int
	class  int
	ttl    int64
	flags  int
	kind   int
	reason string // kError: a cached negative
	host   string // kHost, kMx
	cpu    string // kHinfo
	os     string
	pref   int  // kMx
	soa    *SOA // kSoa
	rdata  []byte
	port   int // kA of a dns= server: its port
}

// SOA is a start of authority.
type SOA struct {
	mname, rname                           string
	serial, refresh, retry, expire, minttl uint32
}

func (r *RR) islive() bool { return r.ttl >= now }

func (r *RR) text() string {
	s := r.name + " " + rrtypename(r.rtype)
	switch r.kind {
	case kHost:
		s += "\t" + r.host
	case kHinfo:
		s += "\t" + r.cpu + " " + r.os
	case kMx:
		s += fmt.Sprintf("\t%d %s", r.pref, r.host)
	case kSoa:
		o := r.soa
		s += fmt.Sprintf("\t%s %s %d %d %d %d %d", o.mname, o.rname, o.serial, o.refresh, o.retry, o.expire, o.minttl)
	case kA:
		if a := r.rdata; len(a) == 4 {
			s += fmt.Sprintf("\t%d.%d.%d.%d", a[0], a[1], a[2], a[3])
		}
	case kError:
		s += "\t!" + r.reason
	}
	return s
}

func rrsettext(rrl []*RR) string {
	s := ""
	for _, r := range rrl {
		s += " [" + r.text() + "]"
	}
	return s
}

//
// The cache: RR sets by name.
//

var cache = map[string][]*RR{}

// cacheput enters the RR sets in rrl, each whole or not at all
// (RFC 2181), with the given flags.
func cacheput(rrl []*RR, flags int) {
	for len(rrl) > 0 {
		var set, rest []*RR
		r0 := rrl[0]
		name := strings.ToLower(r0.name)
		for _, t := range rrl {
			if t.rtype == r0.rtype && t.class == r0.class && strings.ToLower(t.name) == name {
				set = append(set, t)
			} else {
				rest = append(rest, t)
			}
		}
		rrl = rest
		r0.flags = flags
		old, ok := cache[name]
		if debug {
			if ok {
				fmt.Printf("insertrr %s:%s\n", name, rrsettext(set))
			} else {
				fmt.Printf("newrr %s:%s\n", name, rrsettext(set))
			}
		}
		cache[name] = insertrrset(old, r0, set, name)
	}
}

// insertrrset puts the set nset, whose first is rr, among the sets of
// rrl, if it is better than what is there.
func insertrrset(rrl []*RR, rr *RR, nset []*RR, name string) []*RR {
	match := false
	for _, orr := range rrl {
		if orr.rtype != rr.rtype || orr.class != rr.class {
			continue
		}
		match = true
		if !orr.islive() {
			break // prefer new, unexpired data
		}
		if rr.kind == kError && orr.kind != kError {
			return rrl // prefer unexpired positive
		}
		if rr.flags&Fauth != 0 {
			break // prefer newly-arrived authoritative data
		}
		if orr.flags&Fauth != 0 {
			return rrl // prefer authoritative data
		}
		if orr.ttl > rr.ttl {
			return rrl // prefer longer-lived data
		}
	}
	if match {
		var keep []*RR
		for _, orr := range rrl {
			if (orr.rtype != rr.rtype || orr.class != rr.class) && orr.islive() {
				keep = append(keep, orr)
			}
		}
		rrl = keep
	}
	for _, n := range nset {
		n.name = name
		rrl = append(rrl, n)
	}
	return rrl
}

func lookcache(name string, rtype, rclass int) ([]*RR, string) {
	var results []*RR
	name = strings.ToLower(name)
	for _, r := range cache[name] {
		if (r.rtype == rtype || r.rtype == Tall || rtype == Tall) && r.class == rclass && r.islive() {
			if r.kind == kError {
				if rtype != Tall || r.reason != "resource does not exist" {
					if debug {
						fmt.Printf("lookcache: %s[%s]: !%s\n", name, rrtypename(rtype), r.reason)
					}
					return nil, r.reason
				}
				continue
			}
			c := *r
			results = append(results, &c)
		}
	}
	if debug {
		fmt.Printf("lookcache: %s[%s]: %s\n", name, rrtypename(rtype), rrsettext(results))
	}
	return results, ""
}

//
// The DNS message format (RFC 1035).
//

const udpdnslim = 512

// DNSmsg is a message.
type DNSmsg struct {
	id    int
	flags int
	qd    []*QR
	an    []*RR
	ns    []*RR
	ar    []*RR
	err   string
}

type labels map[string]int

func put2(a []byte, o, v int) int {
	if o < 0 {
		return o
	}
	if o+2 > len(a) {
		return -o
	}
	a[o], a[o+1] = byte(v>>8), byte(v)
	return o + 2
}

func put4(a []byte, o int, v uint32) int {
	if o < 0 {
		return o
	}
	if o+4 > len(a) {
		return -o
	}
	a[o], a[o+1], a[o+2], a[o+3] = byte(v>>24), byte(v>>16), byte(v>>8), byte(v)
	return o + 4
}

func puts(a []byte, o int, s string) int {
	if o < 0 {
		return o
	}
	l := len(s)
	if l > 255 || o+l+1 > len(a) {
		return -(o + l + 1)
	}
	a[o] = byte(l)
	copy(a[o+1:], s)
	return o + 1 + l
}

func get2(a []byte, o int) (int, int) {
	if o < 0 {
		return 0, o
	}
	if o+2 > len(a) {
		return 0, -o
	}
	return int(a[o])<<8 | int(a[o+1]), o + 2
}

func get4(a []byte, o int) (uint32, int) {
	if o < 0 {
		return 0, o
	}
	if o+4 > len(a) {
		return 0, -o
	}
	return uint32(a[o])<<24 | uint32(a[o+1])<<16 | uint32(a[o+2])<<8 | uint32(a[o+3]), o + 4
}

func gets(a []byte, o int) (string, int) {
	if o < 0 {
		return "", o
	}
	if o+1 > len(a) {
		return "", -o
	}
	l := int(a[o])
	o++
	if o+l > len(a) {
		return "", -o
	}
	return string(a[o : o+l]), o + l
}

func putdn(a []byte, o int, name string, labs labels) int {
	if o < 0 {
		return o
	}
	o0 := o
	for name != "" {
		if n, ok := labs[name]; ok {
			if o = put2(a, o, 0xC000|n); o < 0 {
				return -o0
			}
			return o
		}
		l := strings.IndexByte(name, '.')
		if l < 0 {
			l = len(name)
		}
		if o+l+1 > len(a) || l > 63 {
			return -o0
		}
		if o < 0x4000 {
			labs[name] = o
		}
		a[o] = byte(l)
		copy(a[o+1:], name[:l])
		o += 1 + l
		for l < len(name) && name[l] == '.' {
			l++
		}
		name = name[l:]
	}
	if o >= len(a) {
		return -o0
	}
	a[o] = 0
	return o + 1
}

func getdn(a []byte, o, depth int) (string, int) {
	if depth > 30 {
		return "", -o
	}
	if o < 0 {
		return "", o
	}
	name := ""
	for o < len(a) {
		l := int(a[o])
		o++
		if l == 0 {
			break
		}
		if l&0xC0 == 0xC0 { // pointer
			if o >= len(a) {
				return "", -o
			}
			po := (l&0x3F)<<8 | int(a[o])
			if po >= len(a) {
				return "", -o
			}
			o++
			pname, po := getdn(a, po, depth+1)
			if po < 1 {
				return "", -o
			}
			name += pname
			break
		}
		if l&0xC0 != 0 {
			return "", -o // format error
		}
		if o+l > len(a) {
			return "", -o
		}
		name += string(a[o : o+l])
		o += l
		if o < len(a) && a[o] != 0 {
			name += "."
		}
	}
	return strings.ToLower(name), o
}

func (m *DNSmsg) pack(hdrlen int) []byte {
	a := make([]byte, udpdnslim+hdrlen)
	l := hdrlen
	l = put2(a, l, m.id)
	l = put2(a, l, m.flags)
	l = put2(a, l, len(m.qd))
	l = put2(a, l, len(m.an))
	l = put2(a, l, len(m.ns))
	l = put2(a, l, len(m.ar))
	labs := labels{}
	for _, q := range m.qd {
		l = putdn(a, l, q.name, labs)
		l = put2(a, l, q.rtype)
		l = put2(a, l, q.class)
	}
	if l < 1 {
		return nil
	}
	return a[:l]
}

func unpack(a []byte) *DNSmsg {
	m := &DNSmsg{flags: Rformat}
	l := 0
	m.id, l = get2(a, l)
	m.flags, l = get2(a, l)
	if l < 0 || l > len(a) {
		m.err = "length error"
		return m
	}
	if l >= len(a) {
		return m
	}
	var nqd, nan, nns, nar int
	nqd, l = get2(a, l)
	nan, l = get2(a, l)
	nns, l = get2(a, l)
	nar, l = get2(a, l)
	if l >= len(a) || l < 0 {
		return m
	}
	for i := 0; i < nqd; i++ {
		q := &QR{}
		q.name, l = getdn(a, l, 0)
		q.rtype, l = get2(a, l)
		q.class, l = get2(a, l)
		if l < 1 {
			break
		}
		m.qd = append(m.qd, q)
	}
	m.an, l = getrrl(nan, a, l)
	m.ns, l = getrrl(nns, a, l)
	m.ar, l = getrrl(nar, a, l)
	if l < 1 {
		stderr.WriteString(fmt.Sprintf("l=%d format error\n", l))
		m.err = "format error"
	}
	return m
}

func getrrl(nr int, a []byte, o int) ([]*RR, int) {
	if o < 0 {
		return nil, o
	}
	var rrl []*RR
	for i := 0; i < nr; i++ {
		var name string
		var rtype, class, dlen int
		var ttl uint32
		name, o = getdn(a, o, 0)
		rtype, o = get2(a, o)
		class, o = get2(a, o)
		ttl, o = get4(a, o)
		t := int64(int32(ttl))
		if t < 0 {
			t = 0
		}
		dlen, o = get2(a, o)
		if o < 1 {
			return rrl, o
		}
		if o+dlen > len(a) {
			return rrl, -(o + dlen)
		}
		rr := &RR{name: name, rtype: rtype, class: class, ttl: t + now}
		switch rtype {
		case Tsoa:
			s := &SOA{}
			s.mname, o = getdn(a, o, 0)
			s.rname, o = getdn(a, o, 0)
			s.serial, o = get4(a, o)
			s.refresh, o = get4(a, o)
			s.retry, o = get4(a, o)
			s.expire, o = get4(a, o)
			s.minttl, o = get4(a, o)
			rr.kind, rr.soa = kSoa, s
		case Thinfo:
			rr.kind = kHinfo
			rr.cpu, o = gets(a, o)
			rr.os, o = gets(a, o)
		case Tmx:
			rr.kind = kMx
			rr.pref, o = get2(a, o)
			rr.host, o = getdn(a, o, 0)
		case Tcname, Tns, Tptr:
			rr.kind = kHost
			rr.host, o = getdn(a, o, 0)
		default:
			rr.kind = kOther
			if rtype == Ta {
				rr.kind = kA
			}
			rr.rdata = append([]byte(nil), a[o:o+dlen]...)
			o += dlen
		}
		rrl = append(rrl, rr)
	}
	return rrl, o
}

func (m *DNSmsg) text() string {
	s := fmt.Sprintf("id=%d flags=#%x[%s]\n", m.id, m.flags, flagtext(m.flags))
	s += "  QR:\n"
	for _, q := range m.qd {
		s += "\t" + q.text() + "\n"
	}
	for _, sec := range []struct {
		n string
		l []*RR
	}{{"AN", m.an}, {"NS", m.ns}, {"AR", m.ar}} {
		s += "  " + sec.n + ":\n"
		for _, r := range sec.l {
			s += "\t" + r.text() + "\n"
		}
	}
	return s
}

func flagtext(f int) string {
	s := ""
	for _, x := range []struct {
		f int
		c string
	}{{Fresp, "R"}, {Fauth, "A"}, {Ftrunc, "T"}, {Frecurse, "r"}, {Fcanrecurse, "c"}} {
		if f&x.f != 0 {
			s += x.c
		}
	}
	if f&Fresp == 0 {
		return s
	}
	if s != "" {
		s += ","
	}
	return s + reason(f&Rmask)
}

var rcodes = []string{
	Rok:            "no error",
	Rformat:        "format error",
	Rserver:        "server failure",
	Rname:          "name does not exist",
	Runimplemented: "unimplemented",
	Rrefused:       "refused",
}

func reason(n int) string {
	if n < 0 || n >= len(rcodes) {
		return fmt.Sprintf("error %d", n)
	}
	return rcodes[n]
}
