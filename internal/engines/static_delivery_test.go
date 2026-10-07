package engines

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/fsstatic"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/composite/compositetest"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// staticKinds is every static kind a package can carry.
var staticKinds = []present.Kind{present.Context, present.MCP, present.Settings, present.Hooks, present.Commands, present.Skills}

// everyKindPackage is one package carrying every static kind: a fragment, a
// command, a skill whose script is executable, an MCP server, a hook and a
// settings item.
func everyKindPackage(t *testing.T) composite.Package {
	t.Helper()
	pkg := compositetest.Fixture(t,
		compositetest.WithFragment("rules", "project rules"),
		compositetest.WithCommand("go", "go now"),
		compositetest.WithMCP("tasks", wire.MCPServer{Command: "tasks"}))
	pkg.Skills = append(pkg.Skills, composite.Item[composite.Skill]{Ref: "fixture#skill/greet", Value: composite.Skill{
		Name: "greet", Description: "greets",
		Files: []engine.SkillFile{
			{Path: "SKILL.md", Bytes: []byte("---\nname: greet\ndescription: greets\n---\nhello\n"), Mode: 0o644},
			{Path: "scripts/run.sh", Bytes: []byte("#!/bin/sh\necho hi\n"), Mode: 0o755},
		},
	}})
	pkg.Hooks.Unified.PreTool = []wire.Hook{{Type: "command", Command: "true"}}
	pkg.Statusline = true
	return pkg
}

// claimedFile is one file the record names for a writer, as it stood after
// the first delivery.
type claimedFile struct {
	bytes []byte
	mode  os.FileMode
	info  os.FileInfo
}

// TestStaticDelivery_UnchangedRedeliveryKeepsEveryDeclaredFile is the static
// writer's contract with every shipped engine, over the production record on
// a real filesystem: delivering the same package again changes nothing. Every
// file the first delivery left claimed is still claimed, holds the same bytes
// and mode, is the SAME file (not a replacement: on Windows a replace is a
// window in which a concurrent reader finds the file missing), and holds the
// value the record says it holds.
//
// What is checked is read from the record after the first delivery, never
// listed here, so an engine that gains a kind or a file is held to the same
// contract without this test changing. An approach that skips an identical
// write must still DECLARE the file, or the next delivery's release removes
// it: the defect this exists for.
func TestStaticDelivery_UnchangedRedeliveryKeepsEveryDeclaredFile(t *testing.T) {
	reg := Registry()
	var names []engine.Name
	for _, name := range reg.Names(nil) {
		e, _ := reg.Lookup(name)
		if len(e.Root().Surfaces()) > 0 {
			names = append(names, name)
		}
	}
	require.NotEmpty(t, names, "no shipped engine has a static surface, so this proves nothing")

	targets := map[string]func(t *testing.T, root engine.Base) (present.Paths, delivery.Target, delivery.Preference){
		"project": func(t *testing.T, root engine.Base) (present.Paths, delivery.Target, delivery.Preference) {
			project := t.TempDir()
			pref := lossTolerant(root)
			for kind, a := range root.Surfaces() {
				if a.Traits().Offers(present.RootProjectRoot) {
					pref.Root[kind] = present.RootProjectRoot
				}
			}
			return present.ProjectOnHost(project).Paths(), delivery.Target{Root: present.ProjectOnHost(project), Writer: delivery.ProjectWriter}, pref
		},
		"session": func(t *testing.T, root engine.Base) (present.Paths, delivery.Target, delivery.Preference) {
			cell := present.Paths{
				ProjectRoot: present.Root{Host: t.TempDir(), Engine: ""},
				SessionHome: present.Root{Host: t.TempDir(), Engine: ""},
			}
			cell.ProjectRoot.Engine, cell.SessionHome.Engine = cell.ProjectRoot.Host, cell.SessionHome.Host
			return cell, delivery.Target{Root: present.New(present.OnHost(cell)), Writer: delivery.SessionWriter("h")}, lossTolerant(root)
		},
	}
	for _, name := range names {
		for targetName, build := range targets {
			t.Run(string(name)+"/"+targetName, func(t *testing.T) {
				e, _ := reg.Lookup(name)
				root := e.Root()
				fs := afero.NewOsFs()
				rec, err := fsstatic.NewRecords(fs, filepath.Join(t.TempDir(), "records"))
				require.NoError(t, err)
				pkg := everyKindPackage(t)
				items := pkg.EngineItems(root.Name)
				exports, err := e.Exports(items)
				require.NoError(t, err)
				paths, target, pref := build(t, root)
				target.Ownership = rec
				plan, err := delivery.Route(items, root, pref, paths)
				require.NoError(t, err)
				require.NotEmpty(t, plan.Static, "the plan delivers nothing statically, so this proves nothing")
				static := fsstatic.New(safefs.NewMem(fs))
				deliver := func() delivery.Delivered {
					t.Helper()
					d, err := static.Deliver(context.Background(), delivery.Loadout{Plan: plan, Package: pkg, Exports: exports}, root, target)
					require.NoError(t, err)
					return d
				}

				first := deliver()
				claimed, err := rec.Targets(target.Writer)
				require.NoError(t, err)
				require.NotEmpty(t, claimed, "the first delivery claimed nothing")
				snapshot := map[string]claimedFile{}
				for _, path := range claimed {
					info, err := os.Stat(path)
					require.NoError(t, err, "claimed but not on disk: %s", path)
					b, err := os.ReadFile(path)
					require.NoError(t, err)
					snapshot[path] = claimedFile{bytes: b, mode: info.Mode(), info: info}
				}
				requirePresentedIsClaimed(t, first, exports, claimed)

				for run := 2; run <= 3; run++ {
					deliver()
					again, err := rec.Targets(target.Writer)
					require.NoError(t, err)
					require.Equal(t, claimed, again, "run %d: the claimed set changed", run)
					for _, path := range claimed {
						was := snapshot[path]
						info, err := os.Stat(path)
						require.NoError(t, err, "run %d: %s was removed", run, path)
						b, err := os.ReadFile(path)
						require.NoError(t, err)
						require.True(t, bytes.Equal(was.bytes, b), "run %d: %s changed bytes", run, path)
						require.Equal(t, was.mode, info.Mode(), "run %d: %s changed mode", run, path)
						require.True(t, os.SameFile(was.info, info), "run %d: an unchanged redelivery replaced %s", run, path)
						states, err := rec.Paths(fs, path)
						require.NoError(t, err)
						require.NotEmpty(t, states, "run %d: %s has no claimed place", run, path)
						for _, st := range states {
							require.True(t, st.Live, "run %d: %s does not hold the effective value at %q", run, path, st.Pointer)
						}
					}
				}
			})
		}
	}
}

// lossTolerant is a preference accepting the loss of every static kind root
// does not carry.
func lossTolerant(root engine.Base) delivery.Preference {
	pref := delivery.Preference{Root: map[present.Kind]present.RootKind{}, AcceptLoss: map[present.Kind]bool{}}
	surfaces := root.Surfaces()
	for _, kind := range staticKinds {
		if surfaces[kind] == nil {
			pref.AcceptLoss[kind] = true
		}
	}
	return pref
}

// requirePresentedIsClaimed holds every presentation's host path to the
// record: the file itself is claimed, or it is a directory holding a claimed
// file. A presentation naming something nobody claims is a delivery this
// test would otherwise not see. The one exception is a tree kind the engine
// exports nothing to (an engine may carry a surface and export no item to
// it): its directory is presented and rightly holds nothing.
func requirePresentedIsClaimed(t *testing.T, d delivery.Delivered, exports engine.Exports, claimed []string) {
	t.Helper()
	require.NotEmpty(t, d.Presented, "the delivery presented nothing")
	require.Len(t, d.Wrote, len(d.Presented), "one presentation per delivered kind")
	exportsNothing := map[present.Kind]bool{present.Commands: len(exports.Commands) == 0, present.Skills: len(exports.Skills) == 0}
	for i, p := range d.Presented {
		if p.HostPath == "" || exportsNothing[d.Wrote[i]] {
			continue
		}
		covered := false
		for _, c := range claimed {
			if c == p.HostPath || present.Under(c, p.HostPath) {
				covered = true
				break
			}
		}
		require.True(t, covered, "presented %s, but nothing there is claimed (claimed: %v)", p.HostPath, claimed)
	}
}
