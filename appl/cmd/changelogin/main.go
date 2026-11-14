// Command changelogin makes or changes a user's entry in keyfs (Inferno
// changelogin(8)): the secret, a SHA-1 hash of a password of at least
// eight characters, and the expiry date.
package main

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/daytime"
	"github.com/ericvh/purgatorio-go/sdk/keyring"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

const keydb = "/mnt/keys"

func main() {
	argv0 := os.Args[0]
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: %s userid\n", argv0)
		sys.Exits("fail:usage")
	}
	id := os.Args[1]
	dbdir, secret, expiry, err := getuser(id)
	if dbdir == "" {
		if err != "" {
			fmt.Fprintf(os.Stderr, "%s: can't get auth info for %s in %s: %s\n", argv0, id, keydb, err)
			sys.Exits("fail:no key")
		}
		fmt.Print("new account\n")
	}
	var word string
	var ok bool
	for {
		if secret != nil {
			fmt.Print("secret [default = don't change]: ")
		} else {
			fmt.Print("secret: ")
		}
		if word, ok = readline(true); !ok {
			sys.Exits("")
		}
		if word == "" && secret != nil || len(word) >= 8 {
			break
		}
		fmt.Print("!secret must be at least 8 characters\n")
	}
	var newsecret []byte
	if word != "" {
		word1 := word
		fmt.Print("confirm: ")
		if word, ok = readline(true); !ok || word != word1 {
			fmt.Print("Entries do not match. Authinfo record unchanged.\n")
			sys.Exits("fail:mismatch")
		}
		newsecret = make([]byte, keyring.SHA1dlen)
		keyring.SHA1([]byte(word), newsecret, nil)
	}

	// The expiry time: the last second of the date given.
	maxdate := "17012038" // the last date before an integer overflow
	now := daytime.Now()
	tm := daytime.Local(now)
	tm.Sec, tm.Min, tm.Hour = 59, 59, 23
	tm.Year++
	expsecs := expiry
	if dbdir == "" {
		expsecs = daytime.Tm2epoch(tm) // a year from today
	}
	for {
		def := "permanent"
		if expsecs != 0 {
			otm := daytime.Local(expsecs)
			def = fmt.Sprintf("%2.2d%2.2d%4.4d", otm.Mday, otm.Mon+1, otm.Year+1900)
		}
		fmt.Printf("expires [DDMMYYYY/permanent, return = %s]: ", def)
		if word, ok = readline(false); !ok {
			sys.Exits("")
		}
		if word == "" {
			word = def
		}
		if word == "permanent" {
			expsecs = 0
			break
		}
		if len(word) != 8 {
			fmt.Printf("!bad date format %s\n", word)
			continue
		}
		tm.Mday, _ = strconv.Atoi(word[0:2])
		if tm.Mday > 31 || tm.Mday < 1 {
			fmt.Printf("!bad day of month %d\n", tm.Mday)
			continue
		}
		mon, _ := strconv.Atoi(word[2:4])
		tm.Mon = mon - 1
		if tm.Mon > 11 || tm.Mday < 0 {
			fmt.Printf("!bad month %d\n", tm.Mon+1)
			continue
		}
		year, _ := strconv.Atoi(word[4:8])
		tm.Year = year - 1900
		if tm.Year < 70 {
			fmt.Printf("!bad year %d (year may be no earlier than 1970)\n", tm.Year+1900)
			continue
		}
		expsecs = daytime.Tm2epoch(tm)
		if expsecs > now {
			break
		}
		newexp := fmt.Sprintf("%2.2d%2.2d%4.4d", tm.Mday, tm.Mon+1, tm.Year+1900)
		t := daytime.Local(daytime.Now())
		today := fmt.Sprintf("%2.2d%2.2d%4.4d", t.Mday, t.Mon+1, t.Year+1900)
		fmt.Printf("!bad expiration date %s (must be between %s and %s)\n", newexp, today, maxdate)
		expsecs = now
	}

	if dbdir == "" {
		dbdir = keydb + "/" + id
		fd, err := sys.Create(dbdir, sys.OREAD, sys.DMDIR|0o700)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: can't create account %s: %s\n", argv0, id, sys.Errstr())
			sys.Exits("fail:create user")
		}
		os.NewFile(uintptr(fd), dbdir).Close()
	}
	if !bytes.Equal(newsecret, secret) && newsecret != nil {
		if !putsecret(dbdir, newsecret) {
			fmt.Fprintf(os.Stderr, "%s: can't update secret for %s: %s\n", argv0, id, sys.Errstr())
			sys.Exits("fail:update")
		}
	}
	if expsecs != expiry {
		if !putexpiry(dbdir, expsecs) {
			fmt.Fprintf(os.Stderr, "%s: can't update expiry time for %s: %s\n", argv0, id, sys.Errstr())
			sys.Exits("fail:update")
		}
	}
	fmt.Print("change written\n")
}

func getuser(id string) (string, []byte, int, string) {
	if _, err := sys.Stat(keydb); err != nil {
		return "", nil, 0, fmt.Sprintf("can't stat %s: %s", id, sys.Errstr())
	}
	dbdir := keydb + "/" + id
	if _, err := sys.Stat(dbdir); err != nil {
		return "", nil, 0, ""
	}
	secret, err := os.ReadFile(dbdir + "/secret")
	if err != nil {
		return "", nil, 0, fmt.Sprintf("can't open %s/secret: %s", id, sys.Errstr())
	}
	if len(secret) == 0 {
		secret = nil
	}
	b, err := os.ReadFile(dbdir + "/expire")
	if err != nil || len(b) == 0 {
		return "", nil, 0, fmt.Sprintf("can't open %s/expiry: %s", id, sys.Errstr())
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return dbdir, secret, n, ""
}

func putsecret(dir string, secret []byte) bool {
	fd, err := sys.Create(dir+"/secret", sys.OWRITE, 0o600)
	if err != nil {
		return false
	}
	f := os.NewFile(uintptr(fd), "secret")
	defer f.Close()
	_, err = f.Write(secret)
	return err == nil
}

func putexpiry(dir string, expiry int) bool {
	f, err := os.OpenFile(dir+"/expire", os.O_WRONLY, 0)
	if err != nil {
		return false
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%d", expiry)
	return err == nil
}

// readline reads a line from standard input, echo off when raw.
func readline(raw bool) (string, bool) {
	if raw {
		if ctl, err := os.OpenFile("/dev/consctl", os.O_WRONLY, 0); err == nil {
			ctl.Write([]byte("rawon"))
			defer func() { ctl.Write([]byte("rawoff")); ctl.Close() }()
		}
	}
	var line []byte
	b := make([]byte, 1)
	for {
		n, _ := os.Stdin.Read(b)
		if n <= 0 {
			if len(line) == 0 {
				return "", false
			}
			break
		}
		if b[0] == '\n' || b[0] == '\r' {
			break
		}
		line = append(line, b[0])
	}
	if raw {
		fmt.Print("\n")
	}
	return string(line), true
}
