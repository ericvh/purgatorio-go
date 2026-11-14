// Command plumb sends a message to the plumber (Inferno plumb(1)). The
// arguments, joined by spaces, are the data, or with -i the standard
// input is; the options set the message's other fields.
package main

import (
	"os"
	"syscall"

	"github.com/ericvh/purgatorio-go/sdk/arg"
	"github.com/ericvh/purgatorio-go/sdk/plumbmsg"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func main() {
	if err := plumbmsg.Init(true, "", 0); err != nil {
		fail("can't connect to plumb: " + sys.Errstr())
	}
	m := &plumbmsg.Msg{Src: "plumb", Dir: workdir(), Kind: "text"}
	var attrs []*plumbmsg.Attr
	input := false
	a := arg.New(os.Args)
	a.SetUsage("plumb [-s src] [-d dest] [-w wdir] [-t type] [-a name val] -i | ... data ...")
	for o := a.Opt(); o != 0; o = a.Opt() {
		switch o {
		case 's':
			m.Src = a.EArg()
		case 'd':
			m.Dst = a.EArg()
		case 'w', 'D':
			m.Dir = a.EArg()
		case 'i':
			input = true
		case 't', 'k':
			m.Kind = a.Arg()
		case 'a':
			name := a.EArg()
			val := a.EArg()
			attrs = append(attrs, &plumbmsg.Attr{Name: name, Val: val})
		default:
			a.Usage()
		}
	}
	args := a.Argv()
	if input == (len(args) != 0) {
		a.Usage()
	}
	if input {
		m.Data = gather(0)
		if _, ok := plumbmsg.Lookup(attrs, "action"); !ok {
			attrs = append(attrs, &plumbmsg.Attr{Name: "action", Val: "showdata"})
		}
		m.Attr = plumbmsg.Attrs2string(attrs)
		if err := m.Send(); err != nil {
			fail("can't send message: " + err.Error())
		}
		return
	}
	var data []byte
	for i, s := range args {
		if i > 0 {
			data = append(data, ' ')
		}
		data = append(data, s...)
	}
	m.Data = data
	m.Attr = plumbmsg.Attrs2string(attrs)
	if err := m.Send(); err != nil {
		fail("can't plumb message: " + err.Error())
	}
}

// workdir is the current directory, as workdir(2) gives it.
func workdir() string {
	fd, err := sys.Open(".", sys.OREAD)
	if err != nil {
		return ""
	}
	defer sys.Close(fd)
	d, _ := sys.Fd2path(fd)
	return d
}

func gather(fd int) []byte {
	var data []byte
	buf := make([]byte, 8192)
	for {
		n, err := syscall.Read(fd, buf)
		if n > 0 {
			data = append(data, buf[:n]...)
		}
		if err != nil {
			fail("error reading input: " + sys.Errstr())
		}
		if n <= 0 {
			return data
		}
	}
}

func fail(s string) {
	os.Stderr.WriteString("plumb: " + s + "\n")
	sys.Exits("fail:error")
}
