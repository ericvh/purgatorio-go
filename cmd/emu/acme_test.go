package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestAcme drives acme through /mnt/acme: a window made through new,
// its body, tag, addr, data, xdata and ctl files, the index, and the
// events that file writes, typing and button 2 make, with an event
// written back for acme to carry out.
func TestAcme(t *testing.T) {
	if err := os.MkdirAll(filepath.Join(root, "tmp"), 0o777); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(filepath.Join(root, "tmp", "acmetest.txt")) })
	out, st := emu(t, "", "sh", "-c",
		"acme & apid=$apid; /bin/test/acme.wasm; echo killgrp > /prog/$apid/ctl")
	want := "mnt/acme cons consctl index label new\n" +
		"new window 1: tag 18 body 0 isdir 0 dirty 0\n" +
		"body \"hello world\\nsecond line\\n\"\n" +
		"tag \"/tmp/acmetest.txt Del Snarf Undo Put | Look \"\n" +
		"bad ctl: ill-formed control message\n" +
		"addr \"6 11\"\n" +
		"line 2 \"second line\\n\"\n" +
		"addr 9: address out of range\n" +
		"body \"hello there\\nsecond line\\n\"\n" +
		"dirty 1\n" +
		"put wrote \"hello there\\nsecond line\\n\"; dirty 0\n" +
		"index 1 /tmp/acmetest.txt\n" +
		"event EI24 29 0 5 more\n" +
		"event KI29 30 0 1 Z\n" +
		"event \"Mx24 24 3 0 \"\n" +
		"event \"Mx22 27 0 5 Snarf\"\n" +
		"bad event: bad event syntax\n" +
		"+Errors \"from acme\\n\"\n" +
		"del: file dirty\n" +
		"after delete, window 1 listed 0 times\n" +
		"stat 1: file does not exist\n"
	if out != want || st != "" {
		t.Errorf("got %q status %q, want %q", out, st, want)
	}
}
