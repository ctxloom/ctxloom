package fsstatic_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/fsstatic"
	"github.com/ctxloom/ctxloom/internal/core/composite/compositetest"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/delivery/deliverytest"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
)

// TestDeliver_OverTheProductionRecord_MaterializeThenUninstallLeavesTheProjectClean
// pairs the writer with confpatch.Records on a real filesystem: a materialize
// into a project root delivers the mock's files and one record per file;
// the empty plan removes exactly them, record included, and leaves the
// user's own file untouched.
func TestDeliver_OverTheProductionRecord_MaterializeThenUninstallLeavesTheProjectClean(t *testing.T) {
	fs := afero.NewOsFs()
	project := t.TempDir()
	theirs := filepath.Join(project, "README.md")
	require.NoError(t, os.WriteFile(theirs, []byte("theirs"), 0o644))
	rec, err := fsstatic.NewRecords(fs, filepath.Join(t.TempDir(), "records"))
	require.NoError(t, err)

	eng := mock.New()
	pkg := compositetest.Fixture(t, compositetest.WithFragment("hello", "hello"), compositetest.WithCommand("go", "go now"), compositetest.WithSkill("greet"), compositetest.WithMCP("tasks", wire.MCPServer{Command: "tasks"}))
	pkg.Hooks.Unified.PreTool = []wire.Hook{{Type: "command", Command: "true"}}
	pkg.Statusline = true
	items := pkg.EngineItems(eng.Root().Name)
	exports, err := eng.Exports(items)
	require.NoError(t, err)
	roots := present.Paths{ProjectRoot: present.Root{Host: project, Engine: project}}
	pref := delivery.Preference{Root: map[present.Kind]present.RootKind{}}
	for _, k := range eng.Root().Static() {
		pref.Root[k] = present.RootProjectRoot
	}
	plan, err := delivery.Route(items, eng.Root(), pref, roots)
	require.NoError(t, err)
	require.Len(t, plan.Static, 6, "every kind is routed to the project root")

	target := delivery.Target{Root: present.ProjectOnHost(project), Ownership: rec, Writer: delivery.ProjectWriter}
	static := fsstatic.New(fs)
	d, err := static.Deliver(context.Background(), delivery.Loadout{Plan: plan, Package: pkg, Exports: exports}, eng.Root().Surfaces(), target)
	require.NoError(t, err)
	require.Len(t, d.Wrote, 6)
	delivered := deliverytest.RelativeFiles(fs, project)
	require.Contains(t, delivered, mock.ContextFileName)
	require.Contains(t, delivered, ".mock/commands/go.md")
	require.Contains(t, delivered, ".mock/skills/greet/SKILL.md")
	owned, err := rec.Targets(delivery.ProjectWriter)
	require.NoError(t, err)
	require.Len(t, owned, len(delivered)-1, "one record per delivered file; the user's README has none")

	// A second delivery is idempotent: the same files, the same records.
	_, err = static.Deliver(context.Background(), delivery.Loadout{Plan: plan, Package: pkg, Exports: exports}, eng.Root().Surfaces(), target)
	require.NoError(t, err)
	require.Equal(t, delivered, deliverytest.RelativeFiles(fs, project))

	// Uninstall: the empty plan over the record.
	_, err = static.Deliver(context.Background(), delivery.Loadout{Package: pkg}, eng.Root().Surfaces(), target)
	require.NoError(t, err)
	require.Equal(t, []string{"README.md"}, deliverytest.RelativeFiles(fs, project), "only the user's file remains")
	owned, err = rec.Targets(delivery.ProjectWriter)
	require.NoError(t, err)
	require.Empty(t, owned)

	// Then a RUN: a session's delivery into a cell over the same project
	// (its session home beside it, no root selected) lands under the session
	// home and leaves the project as the uninstall left it.
	home := t.TempDir()
	cell := present.Paths{ProjectRoot: present.Root{Host: project, Engine: project}, Scratch: present.Root{Host: home, Engine: home}}
	sessionPlan, err := delivery.Route(items, eng.Root(), delivery.Preference{}, cell)
	require.NoError(t, err)
	session := delivery.Target{Root: present.New(present.OnHost(cell)), Ownership: rec, Writer: delivery.SessionWriter("h")}
	_, err = static.Deliver(context.Background(), delivery.Loadout{Plan: sessionPlan, Package: pkg, Exports: exports}, eng.Root().Surfaces(), session)
	require.NoError(t, err)
	require.Equal(t, []string{"README.md"}, deliverytest.RelativeFiles(fs, project), "the run delivered into its session, not the project")
	require.NotEmpty(t, deliverytest.RelativeFiles(fs, home))
}

// TestDeliver_KeepsTheModeAnApproachWrote: the record writes the bytes; the
// mode is the approach's — a 0600 file lands 0600 under the root, a 0755
// one 0755, through the overlay and the record alike.
func TestDeliver_KeepsTheModeAnApproachWrote(t *testing.T) {
	fs := afero.NewOsFs()
	project := t.TempDir()
	rec, err := fsstatic.NewRecords(fs, filepath.Join(t.TempDir(), "records"))
	require.NoError(t, err)
	eng := mock.New()
	pkg := compositetest.Fixture(t, compositetest.WithFragment("hello", "hello"), compositetest.WithSkill("greet"))
	pkg.Skills[0].Value.Files = append(pkg.Skills[0].Value.Files, engine.SkillFile{Path: "scripts/run.sh", Bytes: []byte("#!/bin/sh\n"), Size: 10, Mode: 0o755})
	items := pkg.EngineItems(eng.Root().Name)
	exports, err := eng.Exports(items)
	require.NoError(t, err)
	pref := delivery.Preference{Root: map[present.Kind]present.RootKind{present.Context: present.RootProjectRoot, present.Skills: present.RootProjectRoot}}
	plan, err := delivery.Route(items, eng.Root(), pref, present.Paths{ProjectRoot: present.Root{Host: project, Engine: project}})
	require.NoError(t, err)
	_, err = fsstatic.New(fs).Deliver(context.Background(), delivery.Loadout{Plan: plan, Package: pkg, Exports: exports}, eng.Root().Surfaces(), delivery.Target{Root: present.ProjectOnHost(project), Ownership: rec, Writer: delivery.ProjectWriter})
	require.NoError(t, err)
	for rel, want := range map[string]os.FileMode{mock.ContextFileName: 0o600, ".mock/skills/greet/SKILL.md": 0o644, ".mock/skills/greet/scripts/run.sh": 0o755} {
		info, err := os.Stat(filepath.Join(project, rel))
		require.NoError(t, err, rel)
		require.Equal(t, want, info.Mode().Perm(), rel)
	}
}
