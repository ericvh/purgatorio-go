package host

import (
	"testing"
	"time"

	"github.com/ericvh/purgatorio-go/platform"
)

func TestSockLoop(t *testing.T) {
	n := Net{}
	ls, _ := n.Socket(platform.TCP)
	lo := [16]byte{10: 0xff, 11: 0xff, 12: 127, 15: 1}
	if err := ls.Bind(lo, 0, false); err != nil {
		t.Fatal(err)
	}
	if err := ls.Listen(); err != nil {
		t.Fatal(err)
	}
	_, port := ls.LocalAddr()
	done := make(chan error, 1)
	go func() {
		cs, _ := n.Socket(platform.TCP)
		done <- cs.Connect(nil, lo, port)
	}()
	as, _, _, err := ls.Accept(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer as.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("connect never returned")
	}
}

// TestInterfaces finds the host's loopback interface and its address.
func TestInterfaces(t *testing.T) {
	lo := [16]byte{10: 0xff, 11: 0xff, 12: 127, 15: 1}
	for _, f := range (Net{}).Interfaces() {
		for _, a := range f.Addrs {
			if a.IP == lo {
				if a.Mask[0] != 0xff || a.Mask[12] != 0xff || a.Remote[12] != 127 || a.Remote[15] != 0 {
					t.Errorf("%s: mask %v remote %v", f.Dev, a.Mask, a.Remote)
				}
				return
			}
		}
	}
	t.Error("no interface with 127.0.0.1")
}
