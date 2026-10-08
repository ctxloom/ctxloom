// Package atrest delivers a package into a project AT REST through the one
// static writer, under the engine's per-kind project writers: what the
// at-rest install and uninstall run (operations.Deliver and Release at the
// project root, every kind). It exists for the engine packages' tests, which operations
// imports and which therefore cannot import operations back.
package atrest

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/fsstatic"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// Project is one project root and the record store its deliveries claim in.
type Project struct {
	FS      afero.Fs
	root    safefs.Root
	Dir     string
	Kind    engine.Engine
	Records delivery.Ownership
}

// New is dir on root's filesystem for kind. Its record store is the one every Project for
// that dir on that fs shares — as the home-rooted store is shared in
// production, so a re-install sees what the last one claimed — kept beside
// dir (<dir>.records) so a test's temp root removes it, and outside dir so a
// walk of the project never finds it.
func New(t testing.TB, root safefs.Root, kind engine.Engine, dir string) *Project {
	t.Helper()
	rec, err := fsstatic.NewRecords(root.Fs, filepath.Clean(dir)+".records")
	require.NoError(t, err)
	return &Project{FS: root.Fs, root: root, Dir: dir, Kind: kind, Records: rec}
}

// target is the project root under the engine's project writer family,
// split per kind over every kind.
func (p *Project) target() delivery.Target {
	return delivery.TargetFor(present.ProjectOnHost(p.Dir), p.Records, delivery.ProjectWriterFor(p.Kind.Root().Name), delivery.AllKinds())
}

// Install delivers pkg into the project as the engine's project writers.
func (p *Project) Install(pkg composite.Package) error {
	root := p.Kind.Root()
	items := pkg.EngineItems(root.Name)
	exports, err := p.Kind.Exports(items)
	if err != nil {
		return err
	}
	plan, err := delivery.PlanFor(root, items, present.ProjectOnHost(p.Dir).Paths(), nil, delivery.AllKinds(), false)
	if err != nil {
		return err
	}
	lo := delivery.Loadout{Plan: plan, Package: pkg, Exports: exports}
	_, err = fsstatic.New(p.root).Deliver(context.Background(), lo, root, p.target())
	return err
}

// Uninstall delivers the empty plan: the record's account of what the
// project writers put there, and only that, is removed.
func (p *Project) Uninstall() error {
	_, err := fsstatic.New(p.root).Deliver(context.Background(), delivery.Loadout{}, p.Kind.Root(), p.target())
	return err
}

// Settings is the options a status read of this project takes: its fs, and
// the record's account of what the project writer installed.
func (p *Project) Settings() agent.SettingsOptions {
	return agent.SettingsOptions{FS: p.FS, ProjectClaims: delivery.ProjectClaims(p.FS, p.Records)}
}
