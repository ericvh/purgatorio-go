package tk

import (
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/draw"
)

// tkimage is an image made with "image create" (image(9)).
type tkimage struct {
	name string
	img  *draw.Image
}

func (i *tkimage) size() draw.Point {
	if i == nil || i.img == nil {
		return draw.Point{}
	}
	return i.img.R.Size()
}

// cmdImage is the image command: create, delete, names, width,
// height and type.
func cmdImage(t *Toplevel, args []string) (string, error) {
	if len(args) == 0 {
		return "", tkerr(errBadcm)
	}
	switch args[0] {
	case "create":
		if len(args) < 2 {
			return "", tkerr(errBadvl)
		}
		// Inferno knows only bitmap images; photo is taken as the
		// same, for scripts written for other Tks.
		if args[1] != "bitmap" && args[1] != "photo" {
			return "", tkerr(errBadvl)
		}
		return t.imageCreate(args[2:])
	case "names":
		var n []string
		for k := range t.images {
			n = append(n, k)
		}
		sort.Strings(n)
		return strings.Join(n, " "), nil
	}
	if len(args) < 2 {
		return "", tkerr(errBadvl)
	}
	im := t.images[args[1]]
	if im == nil {
		return "", tkerr(errBadvl)
	}
	switch args[0] {
	case "height":
		return itoa(im.size().Y), nil
	case "width":
		return itoa(im.size().X), nil
	case "type":
		return "bitmap", nil
	case "delete":
		for _, n := range args[1:] {
			im := t.images[n]
			if im == nil {
				return "", tkerr(errBadvl)
			}
			delete(t.images, n)
		}
		t.dirty = true
		return "", nil
	}
	return "", tkerr(errBadcm)
}

// imageCreate makes a bitmap image from -file and -maskfile.
func (t *Toplevel) imageCreate(args []string) (string, error) {
	name := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		name, args = args[0], args[1:]
	}
	if len(args)%2 != 0 {
		return "", tkerr(errOparg, args[len(args)-1])
	}
	var fg, mask *draw.Image
	for i := 0; i < len(args); i += 2 {
		switch args[i] {
		case "-file", "-maskfile":
			im, err := t.bitmap(args[i+1])
			if err != nil {
				return "", err
			}
			if args[i] == "-file" {
				fg = im
			} else {
				mask = im
			}
		default:
			return "", tkerr(errBadop, args[i])
		}
	}
	img := fg
	switch {
	case fg == nil && mask != nil:
		// The mask alone: an alpha image drawn in the foreground
		// colour.
		r := draw.Rect(0, 0, mask.R.Dx(), mask.R.Dy())
		im, err := t.Display.AllocImage(r, draw.ParseChan("a8k8"), false, draw.Transparent)
		if err != nil {
			return "", tkerr(errNomem)
		}
		im.Draw(r, t.Display.Black, mask, mask.R.Min)
		img = im
	case fg != nil && mask != nil:
		r := draw.Rect(0, 0, fg.R.Dx(), fg.R.Dy())
		ch := draw.RGBA32
		if hasGrey(fg.Chan) {
			ch = draw.ParseChan("a8k8")
		}
		im, err := t.Display.AllocImage(r, ch, false, draw.Transparent)
		if err != nil {
			return "", tkerr(errNomem)
		}
		im.GenDraw(r, fg, fg.R.Min, mask, mask.R.Min)
		img = im
	}
	if name == "" {
		name = "image" + strconv.Itoa(t.imgseq)
		t.imgseq++
	}
	if t.images == nil {
		t.images = map[string]*tkimage{}
	}
	t.images[name] = &tkimage{name: name, img: img}
	t.dirty = true
	return name, nil
}

// bitmap loads an image file named as types(9) says of a bitmap:
// @path, <fd, or a name in /icons/tk. Empty or "-" is no bitmap.
func (t *Toplevel) bitmap(s string) (*draw.Image, error) {
	if s == "" || s == "-" {
		return nil, nil
	}
	var b []byte
	var err error
	switch s[0] {
	case '@':
		b, err = os.ReadFile(s[1:])
	case '<':
		fd, cerr := strconv.Atoi(s[1:])
		if cerr != nil {
			return nil, tkerr(errBadvl)
		}
		f := os.NewFile(uintptr(fd), "bitmap")
		if f == nil {
			return nil, tkerr(errBadbm)
		}
		b, err = readAll(f)
	default:
		b, err = os.ReadFile("/icons/tk/" + s)
	}
	if err != nil {
		return nil, tkerr(errBadbm)
	}
	im, _, err := t.Display.ReadImage(b, false)
	if err != nil {
		return nil, tkerr(errBadbm)
	}
	return im, nil
}

// readAll reads f to its end without closing it.
func readAll(f *os.File) ([]byte, error) {
	var b []byte
	buf := make([]byte, 8192)
	for {
		n, err := f.Read(buf)
		b = append(b, buf[:n]...)
		if err != nil || n == 0 {
			if len(b) > 0 {
				return b, nil
			}
			return nil, err
		}
	}
}

// drawImage draws im at p: a grey image is a mask for colour fg.
func (w *Widget) drawImage(dst, im *draw.Image, p draw.Point, fg uint32) {
	r := draw.Rectangle{Min: p, Max: p.Add(im.R.Size())}
	if hasGrey(im.Chan) {
		dst.Draw(r, w.col(fg), im, im.R.Min)
		return
	}
	dst.Draw(r, im, nil, im.R.Min)
}

// hasGrey reports whether channel descriptor c has a grey channel.
func hasGrey(c draw.Chan) bool {
	for ; c != 0; c >>= 8 {
		if int(c>>4&15) == draw.CGrey {
			return true
		}
	}
	return false
}
