package main

// The file tools and the resources. They run in a file worker (mcpd
// --file), on names in the session's namespace.

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/ericvh/purgatorio-go/sdk/mcp"
	"github.com/ericvh/purgatorio-go/sdk/sys"
)

func perr(path string, err error) error { return fail(path, err) }

// entry is the members list_dir and stat share.
func entry(d *sys.Dir) mcp.Obj {
	typ := "file"
	if d.Mode&sys.DMDIR != 0 {
		typ = "dir"
	}
	return mcp.O(
		"name", d.Name,
		"type", typ,
		"size", d.Length,
		"mode", fmt.Sprintf("%o", d.Mode&0o777),
		"append_only", d.Mode&sys.DMAPPEND != 0,
		"mtime", d.Mtime)
}

// statTool is stat: the directory entry and the cleaned name.
func statTool(path string) (mcp.Value, error) {
	d, err := sys.Stat(path)
	if err != nil {
		return nil, perr(path, err)
	}
	full := path
	if fd, err := sys.Open(path, sys.OREAD); err == nil {
		if p, err := sys.Fd2path(fd); err == nil {
			full = p
		}
		closefd(fd)
	}
	m := entry(d)
	m = append(m, mcp.O("path", full, "atime", d.Atime, "uid", d.UID, "gid", d.GID, "muid", d.MUID)...)
	return m, nil
}

func readDir(path string) ([]sys.Dir, error) {
	d, err := sys.Stat(path)
	if err != nil {
		return nil, perr(path, err)
	}
	if d.Mode&sys.DMDIR == 0 {
		return nil, sys.Error(path + ": not a directory")
	}
	fd, err := sys.Open(path, sys.OREAD)
	if err != nil {
		return nil, perr(path, err)
	}
	defer closefd(fd)
	var all []sys.Dir
	for {
		ds, err := sys.Dirread(fd)
		if err != nil {
			return nil, perr(path, err)
		}
		if len(ds) == 0 {
			return all, nil
		}
		all = append(all, ds...)
	}
}

func listDir(path string) (mcp.Value, error) {
	all, err := readDir(path)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	truncated := len(all) > maxEntries
	if truncated {
		all = all[:maxEntries]
	}
	entries := make([]mcp.Value, len(all))
	for i := range all {
		entries[i] = entry(&all[i])
	}
	return mcp.O("entries", entries, "truncated", truncated), nil
}

func removeTool(path string, recursive bool, removed *uint64) error {
	if recursive {
		if d, err := sys.Stat(path); err == nil && d.Mode&sys.DMDIR != 0 {
			ds, err := readDir(path)
			if err != nil {
				return err
			}
			for _, d := range ds {
				if err := removeTool(path+"/"+d.Name, true, removed); err != nil {
					return err
				}
			}
		}
	}
	if err := sys.Remove(path); err != nil {
		return perr(path, err)
	}
	*removed++
	return nil
}

func readFileTool(path string, args mcp.Value) (mcp.Value, error) {
	offset, _ := mcp.Int(args, "offset")
	offset = max(offset, 0)
	length, ok := mcp.Int(args, "length")
	if !ok {
		length = defaultRead
	}
	length = max(length, 0)
	enc, _ := mcp.Str(args, "encoding")
	force := enc == "base64"
	fd, err := sys.Open(path, sys.OREAD)
	if err != nil {
		return nil, perr(path, err)
	}
	defer closefd(fd)
	d, err := sys.Fstat(fd)
	if err != nil {
		return nil, perr(path, err)
	}
	if d.Mode&sys.DMDIR != 0 {
		return nil, sys.Error(path + ": file is a directory")
	}
	size := d.Length
	if offset > 0 {
		if _, err := sys.Seek(fd, offset, 0); err != nil {
			return nil, perr(path, err)
		}
	}
	buf := make([]byte, length)
	n := 0
	eof := false
	for n < int(length) {
		k, err := rd(fd, buf[n:])
		if err != nil {
			return nil, perr(path, err)
		}
		if k == 0 {
			eof = true
			break
		}
		n += k
	}
	buf = buf[:n]
	if size > 0 && uint64(offset)+uint64(n) >= size {
		eof = true
	}
	// A read cut in the middle of a character gives the whole
	// characters; the next read starts at the cut one.
	if !force {
		if v, incomplete := mcp.ValidUTF8Prefix(buf); incomplete && v > 0 {
			buf = buf[:v]
			eof = false
		}
	}
	content, encoding := string(buf), "utf8"
	if force || !utf8.Valid(buf) {
		content, encoding = mcp.Base64(buf), "base64"
	}
	return mcp.O(
		"content", content,
		"encoding", encoding,
		"offset", offset,
		"bytes", len(buf),
		"size", size,
		"eof", eof), nil
}

func writeFileTool(path string, args mcp.Value) (mcp.Value, error) {
	content, _ := mcp.Str(args, "content")
	data := []byte(content)
	if enc, _ := mcp.Str(args, "encoding"); enc == "base64" {
		var err error
		if data, err = mcp.Unbase64(content); err != nil {
			return nil, sys.Error("arguments.content: " + err.Error())
		}
	}
	mode, ok := mcp.Str(args, "mode")
	if !ok {
		mode = "truncate"
	}
	_, serr := sys.Stat(path)
	exists := serr == nil
	var fd int
	var err error
	switch {
	case mode == "create" && exists:
		return nil, sys.Error(path + ": file already exists")
	case mode == "append" && exists:
		// Inferno has no append open mode; the offset is set by hand.
		if fd, err = sys.Open(path, sys.OWRITE); err != nil {
			return nil, perr(path, err)
		}
		if _, err = sys.Seek(fd, 0, 2); err != nil {
			closefd(fd)
			return nil, perr(path, err)
		}
	default:
		if fd, err = sys.Create(path, sys.OWRITE, 0o666); err != nil {
			return nil, perr(path, err)
		}
	}
	defer closefd(fd)
	if err := writeAll(fd, data); err != nil {
		return nil, perr(path, err)
	}
	var size uint64
	if d, err := sys.Fstat(fd); err == nil {
		size = d.Length
	}
	return mcp.O("bytes", len(data), "size", size), nil
}

// listResources is resources/list: the files under home, the session's
// own, by name. Other files are reached through the template.
func listResources(home string) (mcp.Value, error) {
	var out []mcp.Obj
	dirs := []string{home}
	for len(dirs) > 0 && len(out) < maxResources {
		dir := dirs[len(dirs)-1]
		dirs = dirs[:len(dirs)-1]
		entries, err := readDir(dir)
		if err != nil {
			continue
		}
		sort.SliceStable(entries, func(i, j int) bool { return entries[i].Name > entries[j].Name })
		for _, d := range entries {
			path := dir + "/" + d.Name
			if d.Mode&sys.DMDIR != 0 {
				dirs = append(dirs, path)
				continue
			}
			if len(out) == maxResources {
				break
			}
			out = append(out, mcp.O("uri", uriOf(path), "name", path, "size", d.Length))
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, _ := mcp.Get(out[i], "uri")
		b, _ := mcp.Get(out[j], "uri")
		return mcp.Encode(a) < mcp.Encode(b)
	})
	res := make([]mcp.Value, len(out))
	for i, o := range out {
		res[i] = o
	}
	return mcp.O("resources", res), nil
}

// readResource is resources/read: a whole file as text or base64, or a
// directory as its names. A file longer than maxResource is refused.
func readResource(path string) (mcp.Value, error) {
	d, err := sys.Stat(path)
	if err != nil {
		return nil, perr(path, err)
	}
	if d.Mode&sys.DMDIR != 0 {
		ds, err := readDir(path)
		if err != nil {
			return nil, err
		}
		names := make([]string, len(ds))
		for i, e := range ds {
			slash := ""
			if e.Mode&sys.DMDIR != 0 {
				slash = "/"
			}
			names[i] = e.Name + slash + "\n"
		}
		sort.Strings(names)
		return mcp.O("mimeType", "inode/directory", "text", strings.Join(names, "")), nil
	}
	fd, err := sys.Open(path, sys.OREAD)
	if err != nil {
		return nil, perr(path, err)
	}
	defer closefd(fd)
	// Device files report length 0, so the length read is what counts.
	var data []byte
	buf := make([]byte, 8192)
	for len(data) <= maxResource {
		n, err := rd(fd, buf)
		if err != nil {
			return nil, perr(path, err)
		}
		if n == 0 {
			break
		}
		data = append(data, buf[:n]...)
	}
	if len(data) > maxResource {
		return nil, sys.Error(fmt.Sprintf("%s: longer than %d bytes; read it in pieces with read_file", path, maxResource))
	}
	if utf8.Valid(data) {
		return mcp.O("mimeType", "text/plain", "text", string(data)), nil
	}
	return mcp.O("mimeType", "application/octet-stream", "blob", mcp.Base64(data)), nil
}
