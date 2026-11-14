package core

import (
	"context"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/styx"
)

// DevIndir is #*, which attaches another device by name: #*name!spec
// (devindir.c). The name space resolves it (Pgrp.indir); this entry only
// lists the device in #c/drivers.
type DevIndir struct{ DevBase }

func (DevIndir) DC() rune     { return '*' }
func (DevIndir) Name() string { return "indir" }
func (DevIndir) Attach(context.Context, string) (Walked, error) {
	return Walked{}, Error(abi.Ebadspec)
}
func (DevIndir) Walk(context.Context, *Chan, string) (Walked, error) {
	return Walked{}, Error(abi.Enonexist)
}
func (DevIndir) Stat(context.Context, *Chan) (styx.Dir, error) {
	return styx.Dir{}, Error(abi.Enonexist)
}
func (DevIndir) Open(context.Context, *Chan, int) (*Chan, error) { return nil, Error(abi.Eperm) }
func (DevIndir) Read(context.Context, *Chan, []byte, int64) (int, error) {
	return 0, Error(abi.Eperm)
}
func (DevIndir) Write(context.Context, *Chan, []byte, int64) (int, error) {
	return 0, Error(abi.Eperm)
}
func (DevIndir) DirRead(context.Context, *Chan) ([]styx.Dir, error) { return nil, nil }
