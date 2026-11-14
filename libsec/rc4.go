package libsec

// RC4 is an RC4 key stream, as libsec's RC4state: besides encrypting it
// can skip ahead and back up, which Keyring's rc4skip and rc4back need.
type RC4 struct {
	s    [256]byte
	x, y uint8
}

// NewRC4 sets up the state from key, as setupRC4state.
func NewRC4(key []byte) *RC4 {
	r := new(RC4)
	for i := range r.s {
		r.s[i] = byte(i)
	}
	if len(key) == 0 {
		key = []byte{0}
	}
	var j uint8
	for i := range r.s {
		t := r.s[i]
		j += key[i%len(key)] + t
		r.s[i] = r.s[j]
		r.s[j] = t
	}
	return r
}

// XOR encrypts or decrypts p in place.
func (r *RC4) XOR(p []byte) {
	x, y := r.x, r.y
	for i := range p {
		x++
		tx := r.s[x]
		y += tx
		ty := r.s[y]
		r.s[x], r.s[y] = ty, tx
		p[i] ^= r.s[tx+ty]
	}
	r.x, r.y = x, y
}

// Skip advances the key stream n bytes.
func (r *RC4) Skip(n int) {
	x, y := r.x, r.y
	for ; n > 0; n-- {
		x++
		tx := r.s[x]
		y += tx
		r.s[x], r.s[y] = r.s[y], tx
	}
	r.x, r.y = x, y
}

// Back backs the key stream up n bytes.
func (r *RC4) Back(n int) {
	x, y := r.x, r.y
	for ; n > 0; n-- {
		ty := r.s[x]
		tx := r.s[y]
		r.s[y], r.s[x] = ty, tx
		y -= tx
		x--
	}
	r.x, r.y = x, y
}
