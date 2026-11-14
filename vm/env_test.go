package vm

import (
	"context"
	"testing"

	"github.com/ericvh/purgatorio-go/core"
)

// A guest reads its environment in two calls; a change to the shared
// group between them must not make the second call write more than the
// first promised.
func TestEnvironSnapshot(t *testing.T) {
	k := &core.Kernel{Progs: core.NewProgs()}
	root := core.NewRoot("u")
	k.DevTab = core.NewDevTab(root)
	rc, err := k.DevTab.Attach(context.Background(), '/', "")
	if err != nil {
		t.Fatal(err)
	}
	p := core.NewProg(k, core.NewPgrp(k.DevTab, rc), "u")
	p.Egrp().Set("a", []byte("1"))
	in := &Instance{Prog: p}
	first := len(in.environ())
	p.Egrp().Set("b", []byte("2"))
	if n := len(in.environ()); n != first {
		t.Errorf("environ changed from %d to %d entries within one instance", first, n)
	}
}
