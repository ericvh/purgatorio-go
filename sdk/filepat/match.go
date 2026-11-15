package filepat

// Match reports whether name matches the pattern pat: * ? [class] and
// \ to escape.
func Match(pat, name string) bool {
	return matchr([]rune(pat), []rune(name))
}

func matchr(pat, name []rune) bool {
	n, p := 0, 0
	for p < len(pat) {
		r := pat[p]
		p++
		switch r {
		case '*':
			pat = pat[p:]
			if len(pat) == 0 {
				return true
			}
			for ; n <= len(name); n++ {
				if matchr(pat, name[n:]) {
					return true
				}
			}
			return false
		case '[':
			if n == len(name) {
				return false
			}
			s := name[n]
			n++
			matched, invert, first := false, false, true
			var esc bool
			closed := false
			for p < len(pat) {
				var c rune
				p, c, esc = patchar(pat, p)
				if first && !esc && c == '^' {
					invert = true
					first = false
					continue
				}
				first = false
				if !esc && c == ']' {
					closed = true
					break
				}
				var lo, hi rune
				p, lo, hi = patrange(pat, p-1)
				if lo <= s && s <= hi {
					matched = true
				}
			}
			if !closed || invert == matched {
				return false
			}
		case '?':
			if n == len(name) {
				return false
			}
			n++
		case '\\':
			if n == len(name) || p == len(pat) {
				return false
			}
			c := pat[p]
			p++
			if c != name[n] {
				return false
			}
			n++
		default:
			if n == len(name) || r != name[n] {
				return false
			}
			n++
		}
	}
	return n == len(name)
}

// patrange returns a character or a range a-z starting at p.
func patrange(pat []rune, p int) (int, rune, rune) {
	q, lo, _ := patchar(pat, p)
	q1, hi, esc := patchar(pat, q)
	if !esc && hi == '-' {
		q1, hi, _ = patchar(pat, q1)
		return q1, lo, hi
	}
	return q, lo, lo
}

// patchar returns the possibly escaped character at p.
func patchar(pat []rune, p int) (int, rune, bool) {
	if p == len(pat) {
		return p, 0, true
	}
	r := pat[p]
	p++
	if p == len(pat) || r != '\\' {
		return p, r, false
	}
	return p + 1, pat[p], true
}
