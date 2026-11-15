// Command ramfile makes a file held in memory (Inferno ramfile(1)): it
// binds #s before the directory and serves the file through file2chan.
// A write at offset 0 truncates the file, since file2chan cannot tell an
// open that truncates.
package main

import (
	"os"

	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func main() {
	if len(os.Args) < 2 || len(os.Args) > 3 {
		os.Stderr.WriteString("usage: ramfile path [data]\n")
		return
	}
	dir, f := pathsplit(os.Args[1])
	if _, err := sys.Bind("#s", dir, sys.MBEFORE); err != nil {
		os.Stderr.WriteString("ramfile: " + sys.Errstr() + "\n")
		return
	}
	fio, err := sys.File2chan(dir, f)
	if err != nil {
		os.Stderr.WriteString("ramfile: file2chan failed: " + sys.Errstr() + "\n")
		return
	}
	var data []byte
	if len(os.Args) == 3 {
		data = []byte(os.Args[2])
	}
	sys.Detach()
	serve(fio, data)
}

func serve(fio *sys.FileIO, data []byte) {
	for {
		req, err := fio.Next()
		if err != nil {
			return
		}
		switch r := req.(type) {
		case *sys.ReadReq:
			if r.Closed() {
				continue
			}
			if r.Off > int64(len(data)) {
				r.Reply(nil, "")
				continue
			}
			end := min(r.Off+int64(r.Count), int64(len(data)))
			r.Reply(data[r.Off:end], "")
		case *sys.WriteReq:
			if r.Closed() {
				continue
			}
			if r.Off == 0 {
				data = nil
			}
			end := r.Off + int64(len(r.Data))
			if end > int64(len(data)) {
				data = append(data, make([]byte, end-int64(len(data)))...)
			}
			copy(data[r.Off:], r.Data)
			r.Reply(len(r.Data), "")
		}
	}
}

func pathsplit(p string) (string, string) {
	i := len(p) - 1
	for i >= 0 && p[i] == '/' {
		i--
	}
	if i < 0 {
		return p, ""
	}
	p = p[:i+1]
	for i = len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			break
		}
	}
	if i < 0 {
		return ".", p
	}
	return p[:i+1], p[i+1:]
}
