// Command xd dumps files in octal, decimal, hex or characters
// (Inferno xd(1)).
package main

import (
	"bufio"
	"io"
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

var (
	wbytes   = []int{1, 2, 4, 8}
	fmtbases = []int{8, 10, 16}
	// Field widths by width index and base index.
	fwidths = []int{3, 3, 2, 6, 5, 4, 11, 10, 8, 22, 20, 16}
)

type format struct{ nbytes, base, width int }

const cfmt = 256 // the -c base

var (
	bytepos  [16]int
	formats  []format
	addrbase = 16
	repeats  bool
	swab     bool
	flush    bool
	output   = bufio.NewWriter(os.Stdout)
	pad      = strings.Repeat(" ", 32)
)

func usage() {
	os.Stderr.WriteString("usage: xd [-u] [-r] [-s] [-a{odx}] [-c|{b1w2l4v8}{odx}] ... file ...\n")
	sys.Exits("fail:usage")
}

func main() {
	args := os.Args[1:]
	for ; len(args) > 0; args = args[1:] {
		a := args[0]
		if a == "" {
			continue
		}
		if a[0] != '-' {
			break
		}
		switch len(a) {
		case 2:
			switch a[1] {
			case 'c':
				addformat(0, cfmt)
			case 'r':
				repeats = true
			case 's':
				swab = true
			case 'u':
				flush = true
			default:
				usage()
			}
		case 3:
			baseix := strings.IndexByte("odx", a[2])
			if baseix < 0 {
				usage()
			}
			var n int
			switch a[1] {
			case 'a':
				addrbase = fmtbases[baseix]
				continue
			case 'b', '1':
				n = 0
			case 'w', '2':
				n = 1
			case 'l', '4':
				n = 2
			case 'v', '8':
				n = 3
			default:
				usage()
			}
			addformat(n, baseix)
		default:
			usage()
		}
	}
	if len(formats) == 0 {
		addformat(2, 2) // 4x
	}
	switch len(args) {
	case 0:
		dump("", false)
	case 1:
		dump(args[0], false)
	default:
		for _, f := range args {
			dump(f, true)
		}
	}
}

func addformat(widix, baseix int) {
	nbytes := wbytes[widix]
	if len(formats) >= 10 {
		os.Stderr.WriteString("xd: too many formats\n")
		sys.Exits("fail:error")
	}
	var fw int
	if baseix == cfmt {
		fw = 2
		formats = append(formats, format{nbytes, cfmt, fw})
	} else {
		fw = fwidths[baseix+widix*len(fmtbases)]
		formats = append(formats, format{nbytes, fmtbases[baseix], fw})
	}
	// Align the columns of all formats.
	bpos := 0
	for ix := 0; ix < 16; ix += nbytes {
		if bytepos[ix] >= bpos {
			bpos = bytepos[ix]
		} else {
			d := bpos - bytepos[ix]
			for dix := ix; dix < 16; dix++ {
				bytepos[dix] += d
			}
		}
		bpos += fw + 1
	}
}

func dump(path string, title bool) {
	input := io.Reader(os.Stdin)
	if path != "" {
		fd, err := sys.Open(path, sys.OREAD)
		if err != nil {
			output.Flush()
			os.Stderr.WriteString("xd: cannot open " + path + ": " + sys.Errstr() + "\n")
			sys.Exits("fail:cannot open")
		}
		f := os.NewFile(uintptr(fd), path)
		defer f.Close()
		input = f
	}
	input = bufio.NewReader(input)
	if title {
		output.WriteString(path + "\n")
	}
	addr := uint64(0)
	star := false
	var obuf []byte
	for {
		buf := make([]byte, 16)
		n, _ := io.ReadFull(input, buf)
		if swab {
			doswab(buf)
		}
		if n == 16 && repeats {
			if obuf != nil && string(obuf) == string(buf) {
				addr += 16
				if !star {
					star = true
					output.WriteString("*\n")
				}
				continue
			}
			obuf = buf
			star = false
		}
		for i, f := range formats {
			padc := byte(' ')
			if i == 0 {
				padc = '0'
			}
			output.WriteString(num2str(addr, 7, addrbase, padc))
			output.WriteByte(' ')
			pdata(f, n, buf)
			output.WriteByte('\n')
			if flush {
				output.Flush()
			}
		}
		addr += uint64(n)
		if n < 16 {
			output.WriteString(num2str(addr, 7, addrbase, '0'))
			output.WriteByte('\n')
			if flush {
				output.Flush()
			}
			break
		}
	}
	output.Flush()
}

const hexchars = "0123456789abcdef"

// num2str formats v in base, right-aligned in minw characters of padc.
func num2str(v uint64, minw, base int, padc byte) string {
	var s []byte
	for {
		s = append(s, hexchars[v%uint64(base)])
		v /= uint64(base)
		if v == 0 {
			break
		}
	}
	t := append([]byte(nil), s...)
	if len(s) < minw {
		t = []byte(strings.Repeat(string(padc), minw))
	}
	for i := len(s) - 1; i >= 0; i-- {
		t[len(t)-1-i] = s[i]
	}
	return string(t)
}

func pdata(f format, dlen int, data []byte) {
	nout := 0
	for i := 0; i < dlen; i += f.nbytes {
		if i != 0 {
			padlen := bytepos[i] - nout
			output.WriteString(pad[:padlen])
			nout += padlen
		}
		var text string
		if f.base == cfmt {
			switch ch := data[i]; ch {
			case '\t':
				text = "\\t"
			case '\r':
				text = "\\r"
			case '\n':
				text = "\\n"
			case '\b':
				text = "\\b"
			default:
				if ch >= 0x7f || ch < ' ' {
					text = string(hexchars[ch>>4]) + string(hexchars[ch&15])
				} else {
					text = string(rune(ch))
				}
			}
		} else {
			v := uint64(data[i])
			for ix := 1; ix < f.nbytes; ix++ {
				v = v<<8 | uint64(data[i+ix])
			}
			text = num2str(v, f.width, f.base, '0')
		}
		output.WriteString(text)
		nout += len(text)
	}
}

func doswab(b []byte) {
	for ix := 0; ix < 16; ix += 4 {
		b[ix], b[ix+3] = b[ix+3], b[ix]
		b[ix+1], b[ix+2] = b[ix+2], b[ix+1]
	}
}
