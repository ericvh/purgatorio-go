package main

import (
	"fmt"
	"strings"

	"github.com/ericvh/purgatorio-go/styx"
)

// The text of Styx messages as Inferno's Tmsg.text and Rmsg.text give
// it (styx(2)).

var msgnames = map[uint8]string{
	styx.Tversion: "Version", styx.Rversion: "Version",
	styx.Tauth: "Auth", styx.Rauth: "Auth",
	styx.Tattach: "Attach", styx.Rattach: "Attach",
	styx.Rerror: "Error",
	styx.Tflush: "Flush", styx.Rflush: "Flush",
	styx.Twalk: "Walk", styx.Rwalk: "Walk",
	styx.Topen: "Open", styx.Ropen: "Open",
	styx.Tcreate: "Create", styx.Rcreate: "Create",
	styx.Tread: "Read", styx.Rread: "Read",
	styx.Twrite: "Write", styx.Rwrite: "Write",
	styx.Tclunk: "Clunk", styx.Rclunk: "Clunk",
	styx.Tremove: "Remove", styx.Rremove: "Remove",
	styx.Tstat: "Stat", styx.Rstat: "Stat",
	styx.Twstat: "Wstat", styx.Rwstat: "Wstat",
}

func qid2text(q styx.Qid) string {
	return fmt.Sprintf("Qid(16r%x,%d,16r%.2x)", q.Path, int32(q.Vers), q.Type)
}

func dir2text(b []byte) string {
	d, err := styx.UnmarshalDir(b)
	if err != nil {
		return "nil"
	}
	return fmt.Sprintf("Dir(\"%s\",\"%s\",\"%s\",%s,8r%o,%d,%d,%d,16r%x,%d)",
		d.Name, d.UID, d.GID, qid2text(d.Qid), d.Mode, int32(d.Atime), int32(d.Mtime), int64(d.Length), d.Type, int32(d.Dev))
}

func tmsgtext(m *styx.Fcall) string {
	s := fmt.Sprintf("Tmsg.%s(%d", msgnames[m.Type], m.Tag)
	switch m.Type {
	case styx.Tversion:
		return s + fmt.Sprintf(",%d,\"%s\")", int32(m.Msize), m.Version)
	case styx.Tauth:
		return s + fmt.Sprintf(",%d,\"%s\",\"%s\")", m.Afid, m.Uname, m.Aname)
	case styx.Tflush:
		return s + fmt.Sprintf(",%d)", m.Oldtag)
	case styx.Tattach:
		return s + fmt.Sprintf(",%d,%d,\"%s\",\"%s\")", m.Fid, m.Afid, m.Uname, m.Aname)
	case styx.Twalk:
		s += fmt.Sprintf(",%d,%d", m.Fid, m.Newfid)
		if len(m.Wname) == 0 {
			return s + ",nil)"
		}
		q := make([]string, len(m.Wname))
		for i, n := range m.Wname {
			q[i] = "\"" + n + "\""
		}
		return s + ",array[] of {" + strings.Join(q, ",") + "})"
	case styx.Topen:
		return s + fmt.Sprintf(",%d,%d)", m.Fid, m.Mode)
	case styx.Tcreate:
		return s + fmt.Sprintf(",%d,\"%s\",8r%o,%d)", m.Fid, m.Name, m.Perm, m.Mode)
	case styx.Tread:
		return s + fmt.Sprintf(",%d,%d,%d)", m.Fid, int64(m.Offset), m.Count)
	case styx.Twrite:
		return s + fmt.Sprintf(",%d,%d,array[%d] of byte)", m.Fid, int64(m.Offset), len(m.Data))
	case styx.Tclunk, styx.Tremove, styx.Tstat:
		return s + fmt.Sprintf(",%d)", m.Fid)
	case styx.Twstat:
		return s + fmt.Sprintf(",%d,%s)", m.Fid, dir2text(m.Stat))
	}
	return s + ",ILLEGAL)"
}

func rmsgtext(m *styx.Fcall) string {
	s := fmt.Sprintf("Rmsg.%s(%d", msgnames[m.Type], m.Tag)
	switch m.Type {
	case styx.Rversion:
		return s + fmt.Sprintf(",%d,\"%s\")", int32(m.Msize), m.Version)
	case styx.Rauth:
		return s + "," + qid2text(m.Qid) + ")"
	case styx.Rerror:
		return s + ",\"" + m.Ename + "\")"
	case styx.Rflush, styx.Rclunk, styx.Rremove, styx.Rwstat:
		return s + ")"
	case styx.Rattach:
		return s + "," + qid2text(m.Qid) + ")"
	case styx.Rwalk:
		q := make([]string, len(m.Wqid))
		for i, x := range m.Wqid {
			q[i] = qid2text(x)
		}
		return s + ",array[] of {" + strings.Join(q, ",") + "})"
	case styx.Rcreate, styx.Ropen:
		return s + fmt.Sprintf(",%s,%d)", qid2text(m.Qid), int32(m.Iounit))
	case styx.Rread:
		return s + fmt.Sprintf(",array[%d] of byte)", len(m.Data))
	case styx.Rwrite:
		return s + fmt.Sprintf(",%d)", int32(m.Count))
	case styx.Rstat:
		return s + "," + dir2text(m.Stat) + ")"
	}
	return s + "ERROR)"
}

func msgtext(m *styx.Fcall) string {
	if m.Type%2 == 0 {
		return tmsgtext(m)
	}
	return rmsgtext(m)
}
