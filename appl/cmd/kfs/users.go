package main

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The user table, from /adm/users in the file system:
// uid:user:leader:members[,...]

const (
	none    = 0    // user ID for "none"
	noworld = 9999 // conventional id for "noworld" group
)

type user struct {
	uid    int
	name   string
	leader int
	mem    []int
}

var users []*user

var admusers = []struct {
	id           int
	name, leader string
}{
	{-1, "adm", "adm"},
	{none, "none", "adm"},
	{noworld, "noworld", ""},
	{10000, "sys", ""},
	{10001, "upas", "upas"},
	{10002, "bootes", "bootes"},
	{10006, "inferno", ""},
}

var writegroup int

func userinit() {
	if !cmdusers() && users == nil {
		cprint("initializing minimal user table")
		defaultusers()
	}
	writegroup = strtouid("write")
}

// cmdusers reads /adm/users through the console's own conversation.
func cmdusers() bool {
	buf, e := consread("adm", "users")
	if e != "" {
		return false
	}
	var lines []string
	for _, l := range strings.Split(string(buf), "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		cprint("empty /adm/users")
		return false
	}
	old := users
	users = nil
	// first pass: enter id:name
	for _, s := range lines {
		if s[0] == '#' {
			continue
		}
		uid, r := field(s, ':')
		name, _ := field(r, ':')
		n, err := strconv.Atoi(uid)
		if uid == "" || name == "" || err != nil || strconv.Itoa(n) != uid {
			cprint("invalid /adm/users line: " + s)
			users = old
			return false
		}
		adduser(n, name, "", nil)
	}
	// second pass: groups and leaders
	for _, s := range lines {
		if s[0] == '#' {
			continue
		}
		_, r := field(s, ':')
		name, r := field(r, ':')
		lead, mem := field(r, ':')
		mems := strings.FieldsFunc(mem, func(r rune) bool { return r == ',' || r == '\n' })
		if name == "" || lead == "" && len(mems) == 0 {
			continue
		}
		u := finduname(name)
		if u == nil {
			continue
		}
		if lead != "" {
			if lu := strtouid(lead); lu != none {
				u.leader = lu
			} else {
				u.leader = u.uid // mimic kfs not fs
			}
		}
		var mids []int
		for _, m := range mems {
			if lu := strtouid(m); lu != none {
				mids = append(mids, lu)
			}
		}
		u.mem = mids
	}
	return true
}

func field(s string, c byte) (string, string) {
	if i := strings.IndexByte(s, c); i >= 0 {
		return s[:i], s[i+1:]
	}
	return s, ""
}

func defaultusers() {
	for _, u := range admusers {
		adduser(u.id, u.name, u.leader, nil)
	}
}

func finduname(s string) *user {
	for _, u := range users {
		if u.name == s {
			return u
		}
	}
	return nil
}

func uidtostr(id int) string {
	if id == none {
		return "none"
	}
	for _, u := range users {
		if u.uid == id {
			return u.name
		}
	}
	return fmt.Sprintf("#%d", id)
}

func leadgroup(ui, gi int) bool {
	for _, u := range users {
		if u.uid == gi {
			if u.leader == ui {
				return true
			}
			if u.leader == 0 {
				return ingroup(ui, gi)
			}
			return false
		}
	}
	return false
}

func strtouid(s string) int {
	if s == "none" {
		return none
	}
	if u := finduname(s); u != nil {
		return u.uid
	}
	return 0
}

func ingroup(uid, gid int) bool {
	if uid == gid {
		return true
	}
	for _, u := range users {
		if u.uid == gid {
			for _, m := range u.mem {
				if m == uid {
					return true
				}
			}
			return false
		}
	}
	return false
}

func checkname9p2(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] <= 0o40 {
			return 0
		}
	}
	return utf8.RuneCountInString(s)
}

func baduname(s string) bool {
	n := checkname9p2(s)
	if n == 0 || n+1 > namelen || s == "." || s == ".." {
		fmt.Printf("kfs: illegal user name %q\n", s)
		return true
	}
	return false
}

func adduser(id int, name, leader string, mem []string) {
	if baduname(name) {
		return
	}
	for _, u := range users {
		if u.uid == id {
			fmt.Printf("kfs: duplicate user ID %d (name %q)\n", id, u.name)
			return
		} else if u.name == name {
			fmt.Printf("kfs: duplicate user name %q (id %d)\n", name, u.uid)
			return
		}
	}
	var lid int
	switch {
	case name == leader:
		lid = id
	case leader == "":
		lid = 0
	case !baduname(leader):
		lid = strtouid(leader)
	default:
		return
	}
	var memid []int
	for _, m := range mem {
		if baduname(m) {
			return
		}
		if x := strtouid(m); x != 0 {
			memid = append(memid, x)
		}
	}
	users = append(users, &user{uid: id, name: name, leader: lid, mem: memid})
}
