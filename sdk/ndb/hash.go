package ndb

// Hash file layout, as in Inferno's attrdb.m (and Plan 9's ndb.h): a
// header of the data base file's mtime and the table length, both
// little-endian 4 bytes, then a table of 3-byte file offsets, then the
// collision chains. A slot holding NAP is empty; one with the CHAIN bit
// points (relative to the table) at a pair of offsets: the entry's and
// the next link.
const (
	PLEN  = 3
	HLEN  = 4 + 4
	SPEC  = 1 << 23
	CHAIN = SPEC
	NAP   = SPEC | 1
)

// Hash is the hash of a value in a table of hlen slots, computed as
// Inferno's Attrhash does: the value's characters, but when one is not
// ASCII the UTF-8 bytes of the whole value are hashed on top of the
// characters before it.
func Hash(s string, hlen int) int {
	var h int32
	r := []rune(s)
	for i := 0; i < len(r); i++ {
		if r[i] >= 0x80 {
			for _, b := range []byte(s) {
				h = h*13 + int32(b) - 'a'
			}
			break
		}
		h = h*13 + r[i] - 'a'
	}
	return int(uint32(h) % uint32(hlen))
}

func get3(b []byte) int    { return int(b[2])<<16 | int(b[1])<<8 | int(b[0]) }
func put3(b []byte, v int) { b[0], b[1], b[2] = byte(v), byte(v>>8), byte(v>>16) }
func put4(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
}

// Mkhash returns the hash file of f for attribute attr, as ndb/mkhash
// writes it.
func Mkhash(f *File, attr string) []byte {
	type ent struct {
		val    string
		offset int
	}
	var ents []ent
	n := 0
	offset := 0
	for {
		e, _, next := f.ReadEntry(offset, "", "", false)
		if e == nil {
			break
		}
		ms := e.Find(attr)
		n += len(ms)
		for _, m := range ms {
			for _, a := range m.Attrs {
				ents = append(ents, ent{a.Val, offset})
			}
		}
		offset = next
	}
	hlen := 2*n + 1
	tab := make([]byte, hlen*PLEN, hlen*PLEN+len(ents)*2*PLEN)
	for i := 0; i < len(tab); i += PLEN {
		put3(tab[i:], NAP)
	}
	chain := hlen * PLEN
	for _, x := range ents {
		chain = enter(&tab, x.val, hlen, chain, x.offset)
	}
	file := make([]byte, HLEN, HLEN+chain)
	put4(file, f.Mtime)
	put4(file[4:], uint32(hlen))
	return append(file, tab[:chain]...)
}

func enter(tabp *[]byte, val string, hlen, chain, offset int) int {
	tab := *tabp
	o := Hash(val, hlen) * PLEN
	for p := get3(tab[o:]); p != NAP; p = get3(tab[o:]) {
		if p&CHAIN == 0 {
			if len(tab) < chain+2*PLEN {
				tab = append(tab, make([]byte, chain+2*PLEN-len(tab))...)
				*tabp = tab
			}
			put3(tab[o:], chain|CHAIN)
			put3(tab[chain:], p)
			put3(tab[chain+PLEN:], offset)
			return chain + 2*PLEN
		}
		o = p &^ CHAIN
	}
	put3(tab[o:], offset)
	return chain
}
