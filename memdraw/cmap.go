package memdraw

// Cmap is a color map: the RGB of each of 256 indices, and the nearest
// index of each RGB quantized to 4 bits per channel.
type Cmap struct {
	Cmap2rgb [3 * 256]byte
	Rgb2cmap [16 * 16 * 16]byte
}

// DefCmap is Inferno's default color map, rgbv: a 4x4x4 RGB cube with
// four levels of brightness in each cell.
var DefCmap = mkcmap()

// CmapRGB returns the 24-bit RGB of index c of the rgbv map.
func CmapRGB(c int) uint32 {
	r := c >> 6
	v := (c >> 4) & 3
	j := (c - v + r) & 15
	g := j >> 2
	b := j & 3
	den := max(r, g, b)
	if den == 0 {
		v *= 17
		return uint32(v<<16 | v<<8 | v)
	}
	num := 17 * (4*den + v)
	return uint32((r*num/den)<<16 | (g*num/den)<<8 | (b * num / den))
}

// RGBCmap returns the rgbv index nearest the color (cr, cg, cb).
func RGBCmap(cr, cg, cb int) int {
	best, bestsq := 0, int(^uint32(0)>>1)
	for i := 0; i < 256; i++ {
		rgb := CmapRGB(i)
		r, g, b := int(rgb>>16&0xFF), int(rgb>>8&0xFF), int(rgb&0xFF)
		sq := (r-cr)*(r-cr) + (g-cg)*(g-cg) + (b-cb)*(b-cb)
		if sq < bestsq {
			bestsq = sq
			best = i
		}
	}
	return best
}

func mkcmap() *Cmap {
	m := new(Cmap)
	for i := 0; i < 256; i++ {
		v := CmapRGB(i)
		m.Cmap2rgb[3*i] = byte(v >> 16)
		m.Cmap2rgb[3*i+1] = byte(v >> 8)
		m.Cmap2rgb[3*i+2] = byte(v)
	}
	// The table is indexed by the top 4 bits of each channel; each cell
	// holds the entry nearest the 4-bit value replicated to 8 bits.
	for r := 0; r < 16; r++ {
		for g := 0; g < 16; g++ {
			for b := 0; b < 16; b++ {
				m.Rgb2cmap[r*256+g*16+b] = byte(RGBCmap(r*17, g*17, b*17))
			}
		}
	}
	return m
}
