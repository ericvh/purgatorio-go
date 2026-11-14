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
