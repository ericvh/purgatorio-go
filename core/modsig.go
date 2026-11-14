package core

import (
	"context"
	"strings"

	"github.com/ericvh/purgatorio-go/abi"
	"github.com/ericvh/purgatorio-go/pgauth"
	"github.com/ericvh/purgatorio-go/pgauth/modsig"
)

// The per-prog module policy (docs/signing.md): none, warn or require,
// with classical (Ed25519) signatures counting only when it says so. It
// is set with "modules <level> [classical]" on /prog/<pid>/ctl, only
// tightens, is inherited at spawn, and reads its trust lists in a copy of
// the prog's namespace pinned when it first leaves none.

// ModLevel is a module policy's level.
type ModLevel int

const (
	ModNone ModLevel = iota
	ModWarn
	ModRequire
)

func (l ModLevel) String() string { return [...]string{"none", "warn", "require"}[l] }

// ModPolicy is a prog's module policy. It is never changed in place.
type ModPolicy struct {
	Level     ModLevel
	Classical bool  // Ed25519 signatures count, besides hybrid ones
	Pin       *Pgrp // the namespace the lists are read in
}

// strictness orders policies: a higher level is stricter, and within a
// level taking hybrid signatures only is stricter than taking both.
func (m *ModPolicy) strictness() int {
	if m == nil || m.Level == ModNone {
		return 0
	}
	s := 2 * int(m.Level)
	if !m.Classical {
		s++
	}
	return s
}

func (m *ModPolicy) String() string {
	if m == nil {
		return "none"
	}
	s := m.Level.String()
	if m.Classical && m.Level != ModNone {
		s += " classical"
	}
	return s
}

// ModRecord is a module a prog loaded, for /prog/<pid>/module.
type ModRecord struct {
	Digest  [32]byte
	Signers []pgauth.PublicKey // of the trusted entries; none when not checked or under warn
	Path    string
}

// ModulePolicy is the prog's policy (nil is none).
func (p *Prog) ModulePolicy() *ModPolicy {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.modpol
}

// SetModulePolicy tightens the prog's policy. Lowering it is
// permission denied; writing the current one does nothing. The first
// policy past none pins a copy of the prog's namespace.
func (p *Prog) SetModulePolicy(level ModLevel, classical bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := &ModPolicy{Level: level, Classical: classical}
	cur := p.modpol
	switch {
	case n.strictness() < cur.strictness():
		return Error(abi.Eperm)
	case n.strictness() == cur.strictness():
		return nil
	}
	if cur != nil && cur.Pin != nil {
		n.Pin = cur.Pin
	} else {
		n.Pin = p.pgrp.Fork()
	}
	p.modpol = n
	return nil
}

// Modules is what the prog has loaded, its command first.
func (p *Prog) Modules() []ModRecord {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]ModRecord(nil), p.modules...)
}

// AddModule records a module the prog loaded; reset starts the list
// again (a new command, from spawn or exec).
func (p *Prog) AddModule(r ModRecord, reset bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if reset {
		p.modules = nil
	}
	p.modules = append(p.modules, r)
}

// ModuleTrust reads the trust lists for modules or images in pgrp:
// anchors (/lib/keys/modules, or /lib/keys/aot for the AOT engine) and
// /lib/keys/revoked, which refuses everything when it is there but
// damaged.
func ModuleTrust(ctx context.Context, pgrp *Pgrp, anchors string, now uint64, classical bool) *modsig.Trust {
	t := &modsig.Trust{Anchors: nsList(ctx, pgrp, anchors), Now: now, Classical: classical}
	t.Revoked, t.Damaged = revocations(ctx, pgrp)
	return t
}

// CheckModule checks code, loaded by path, against the prog's policy:
// it computes the digest and, past none, reads the lists in the pinned
// namespace. Under warn a module that fails loads after a warning on the
// prog's fd 2; under require it does not load ("<path>: <error>"). It
// returns the record of the load.
func (p *Prog) CheckModule(path string, code []byte) (ModRecord, error) {
	rec := ModRecord{Path: path}
	info, err := modsig.Inspect(code)
	if err != nil {
		return rec, Error(path + ": " + abi.Ebadexec)
	}
	rec.Digest = info.Digest
	pol := p.ModulePolicy()
	if pol == nil || pol.Level == ModNone {
		return rec, nil
	}
	t := ModuleTrust(p.Ctx, pol.Pin, "/lib/keys/modules", nowSecs(p.Kernel), pol.Classical)
	signers, err := modsig.Check(info, t)
	if err == nil {
		rec.Signers = signers
		return rec, nil
	}
	if pol.Level == ModRequire {
		return rec, Error(path + ": " + err.Error())
	}
	_, _ = p.Write(2, []byte(path+": warning: "+err.Error()+"\n"))
	return rec, nil
}

// moduleCtl carries out "modules <level> [classical]".
func moduleCtl(p *Prog, f []string) error {
	if len(f) < 2 || len(f) > 3 {
		return Error(abi.Ebadctl)
	}
	var l ModLevel
	switch f[1] {
	case "none":
		l = ModNone
	case "warn":
		l = ModWarn
	case "require":
		l = ModRequire
	default:
		return Error(abi.Ebadctl)
	}
	classical := false
	if len(f) == 3 {
		if f[2] != "classical" {
			return Error(abi.Ebadctl)
		}
		classical = true
	}
	return p.SetModulePolicy(l, classical)
}

// moduleText is /prog/<pid>/module.
func moduleText(p *Prog) string {
	var b strings.Builder
	b.WriteString("policy " + p.ModulePolicy().String() + "\n")
	for _, m := range p.Modules() {
		signers := "-"
		if len(m.Signers) > 0 {
			var ids []string
			for _, k := range m.Signers {
				ids = append(ids, k.ID().String())
			}
			signers = strings.Join(ids, ",")
		}
		b.WriteString("module " + pgauth.Hex(m.Digest[:]) + " " + signers + " " + quotePath(m.Path) + "\n")
	}
	return b.String()
}

func quotePath(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
