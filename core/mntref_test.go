package core

import (
	"testing"

	"github.com/ericvh/purgatorio-go/abi"
)

// A mount keeps its conversation's Chan: the fd it was made from can be
// closed (the mounting prog may exit) and the mount still works.
func TestMountOutlivesFd(t *testing.T) {
	p := newTestProg(t)
	p.Kernel.Mnt = NewDevMnt()
	fds, err := p.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = p.Export(fds[0], "/a", false) }()
	if err := p.Mount(fds[1], -1, "/b", abi.MREPL, ""); err != nil {
		t.Fatalf("mount: %v", err)
	}
	if err := p.Close(fds[1]); err != nil {
		t.Fatal(err)
	}
	if got := names(t, p, "/b"); got != "x f" {
		t.Errorf("mounted dir after closing the fd: %q", got)
	}
}
