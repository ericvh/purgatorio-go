// Command getauthinfo gets a certificate from a signer in exchange for
// a valid password and saves it (Inferno getauthinfo(8)), as
// /usr/user/keyring/keyname or the file named. It prompts for the
// signer, the remote user name and the password; given "no" to save in
// file, it serves the key from memory through file2chan instead.
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/keyring"
	"github.com/ericvh/purgatorio-go/sdk/login"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const (
	rawon  = 0
	rawoff = 1
)

func usage() {
	os.Stderr.WriteString("usage: getauthinfo {net!hostname | default | /file}\n")
	sys.Exits("fail:usage")
}

func main() {
	if len(os.Args) < 2 || os.Args[1] == "" {
		usage()
	}
	keyname := os.Args[1]
	user := readuser()
	path := keyname
	if path[0] != '/' && !strings.HasPrefix(path, "./") {
		path = "/usr/" + user + "/keyring/" + keyname
	}
	signer := "$SIGNER"
	passwd := ""
	save := "yes"
	for {
		signer = promptstring("use signer", signer, rawoff)
		user = promptstring("remote user name", user, rawoff)
		passwd = promptstring("password", passwd, rawon)
		if logon(user, passwd, signer, path, save) {
			break
		}
	}
}

func logon(user, passwd, server, path, save string) bool {
	info, err := login.Login(user, passwd, "net!"+server+"!inflogin")
	if err != nil {
		fmt.Fprintf(os.Stderr, "getauthinfo: failed to authenticate: %s\n", err)
		return false
	}
	save = promptstring("save in file", save, rawoff)
	if save == "" || save[0] != 'y' {
		i := strings.LastIndexByte(path, '/')
		dir, file := path[:i+1], path[i+1:]
		if err := sys.Bind("#s", dir, sys.MBEFORE); err != nil {
			fmt.Fprintf(os.Stderr, "getauthinfo: can't bind file channel on %s: %s\n", dir, sys.Errstr())
			return false
		}
		fio, err := sys.File2chan(dir, file)
		if err != nil {
			fmt.Fprintf(os.Stderr, "getauthinfo: can't make file2chan %s: %s\n", path, sys.Errstr())
			return false
		}
		// Serve the key from a child prog so that this one can write
		// it and exit.
		serveinfo(fio, keyring.MarshalAuthinfo(info))
		return true
	}
	if err := keyring.WriteAuthinfo(path, info); err != nil {
		fmt.Fprintf(os.Stderr, "getauthinfo: can't write certificate to %s: %s\n", path, err)
		return false
	}
	return true
}

// serveinfo serves the key file from memory: reads give it, and writes
// may only add to it. This prog detaches, as a Limbo command whose
// spawned server thread outlives init.
func serveinfo(fio *sys.FileIO, data []byte) {
	sys.Detach()
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
			if r.Off != int64(len(data)) {
				r.Reply(0, "cannot be rewritten")
				continue
			}
			data = append(data, r.Data...)
			r.Reply(len(r.Data), "")
		}
	}
}

func readuser() string {
	b, err := os.ReadFile("/dev/user")
	if err != nil {
		return ""
	}
	return string(b)
}

func promptstring(prompt, def string, mode int) string {
	if mode == rawon || def == "" {
		fmt.Printf("%s: ", prompt)
	} else {
		fmt.Printf("%s [%s]: ", prompt, def)
	}
	resp, eof := readline(mode)
	if eof {
		sys.Exits("")
	}
	if resp == "" {
		resp = def
	}
	return resp
}

// readline reads a line from standard input, with echo off for rawon.
func readline(mode int) (string, bool) {
	var ctl *os.File
	if mode == rawon {
		var err error
		ctl, err = os.OpenFile("/dev/consctl", os.O_WRONLY, 0)
		if err != nil {
			os.Stderr.WriteString("unable to change console mode")
			return "", true
		}
		ctl.Write([]byte("rawon"))
		defer func() { ctl.Write([]byte("rawoff")); ctl.Close() }()
	}
	var line []byte
	b := make([]byte, 1)
	for len(line) < 128 {
		n, _ := os.Stdin.Read(b)
		if n <= 0 {
			return string(line), true
		}
		if b[0] == '\n' || b[0] == '\r' && mode == rawon {
			break
		}
		line = append(line, b[0])
	}
	if mode == rawon {
		os.Stdout.Write([]byte("\n"))
	}
	return string(line), false
}
