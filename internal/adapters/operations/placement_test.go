package operations

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/composite/compositetest"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/delivery/deliverytest"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// The placement core's at-rest semantics (materialize tests 7-13, 55, and
// the session-endpoint constraint), driven through Deliver and Release at a
// Placement: the operation the CLI and the install both call.

const placeDir = "/project"

func lookupEngine(t *testing.T, name string) engine.Engine {
	t.Helper()
	kind, ok := engines.Registry().Lookup(engine.Name(name))
	require.True(t, ok)
	return kind
}

// everyKindPackage holds an item of every kind; body is the context text.
func everyKindPackage(t *testing.T, body string) composite.Package {
	t.Helper()
	pkg := compositetest.Fixture(t, compositetest.WithFragment("hello", body), compositetest.WithCommand("go", "go now"), compositetest.WithSkill("greet"), compositetest.WithMCP("tasks", wire.MCPServer{Command: "tasks"}))
	pkg.Hooks.Unified.PreShell = []wire.Hook{{Command: "ltk", Args: []string{"evaluate"}}}
	pkg.DenyTools = []string{"Task"}
	pkg.Statusline = true
	// A skill every engine accepts: claude refuses one with no description.
	skill := "---\nname: greet\ndescription: say hello\n---\n# greet\n"
	pkg.Skills[0].Value.Description = "say hello"
	pkg.Skills[0].Value.Files = []engine.SkillFile{{Path: "SKILL.md", Bytes: []byte(skill), Size: int64(len(skill))}}
	return pkg
}

func deliverAt(t *testing.T, fs afero.Fs, kind engine.Engine, pkg composite.Package, kinds ...present.Kind) {
	t.Helper()
	_, _, err := Deliver(context.Background(), safefs.NewMem(fs), kind, pkg, delivery.Loadout{}, atRestPlacement(placeDir, kind.Root().Name, kinds))
	require.NoError(t, err)
}

func releaseAt(t *testing.T, fs afero.Fs, kind engine.Engine, kinds ...present.Kind) {
	t.Helper()
	require.NoError(t, Release(context.Background(), safefs.NewMem(fs), kind, atRestPlacement(placeDir, kind.Root().Name, kinds)))
}

func placedString(t *testing.T, fs afero.Fs, rel string) string {
	t.Helper()
	b, err := afero.ReadFile(fs, filepath.Join(placeDir, filepath.FromSlash(rel)))
	require.NoError(t, err)
	return string(b)
}

func jsonDoc(t *testing.T, fs afero.Fs, rel string) map[string]any {
	t.Helper()
	var doc map[string]any
	require.NoError(t, json.Unmarshal([]byte(placedString(t, fs, rel)), &doc))
	return doc
}

var claudeSettings = filepath.ToSlash(filepath.Join(".claude", claude.SettingsFileName))

// TestDeliver_ASubsetRedeliveryLeavesTheOtherKindsStanding (test 7, the key
// one): a full delivery, then a context-only one with changed content —
// .mcp.json, settings.json's entries, the commands and the skills are all
// still claimed and present, and the context section appears once.
func TestDeliver_ASubsetRedeliveryLeavesTheOtherKindsStanding(t *testing.T) {
	fs := afero.NewMemMapFs()
	kind := lookupEngine(t, "claude-code")
	deliverAt(t, fs, kind, everyKindPackage(t, "FIRST-BODY"), delivery.AllKinds()...)
	before := deliverytest.RelativeFiles(fs, placeDir)
	require.Contains(t, before, claude.MCPFileName)
	require.Contains(t, before, claudeSettings)

	deliverAt(t, fs, kind, everyKindPackage(t, "SECOND-BODY"), present.Context)
	require.Equal(t, before, deliverytest.RelativeFiles(fs, placeDir), "the context-only run took no other kind's file")
	require.Contains(t, jsonDoc(t, fs, claude.MCPFileName)["mcpServers"], "tasks")
	settings := jsonDoc(t, fs, claudeSettings)
	require.Contains(t, settings, "hooks")
	require.Contains(t, settings, "permissions")
	ctx := placedString(t, fs, claude.ContextFileName)
	require.Contains(t, ctx, "SECOND-BODY")
	require.NotContains(t, ctx, "FIRST-BODY")
}

// TestDeliver_HooksAndSettingsShareAFileWithoutStrippingEachOther (test 8):
// a hooks-only redelivery keeps the settings kind's claims (permissions,
// statusline) in the same settings.json, and a settings-only one keeps the
// hooks.
func TestDeliver_HooksAndSettingsShareAFileWithoutStrippingEachOther(t *testing.T) {
	fs := afero.NewMemMapFs()
	kind := lookupEngine(t, "claude-code")
	pkg := everyKindPackage(t, "body")
	deliverAt(t, fs, kind, pkg, delivery.AllKinds()...)
	full := jsonDoc(t, fs, claudeSettings)

	deliverAt(t, fs, kind, pkg, present.Hooks)
	require.Equal(t, full, jsonDoc(t, fs, claudeSettings), "hooks alone kept the settings kind's entries")
	deliverAt(t, fs, kind, pkg, present.Settings)
	require.Equal(t, full, jsonDoc(t, fs, claudeSettings), "settings alone kept the hooks")
}

// TestDeliver_ASelectedEmptyKindIsReleasedAnUnselectedOneUntouched (test 9).
func TestDeliver_ASelectedEmptyKindIsReleasedAnUnselectedOneUntouched(t *testing.T) {
	fs := afero.NewMemMapFs()
	kind := lookupEngine(t, "mock")
	deliverAt(t, fs, kind, everyKindPackage(t, "body"), delivery.AllKinds()...)
	noExtras := compositetest.Fixture(t, compositetest.WithFragment("hello", "body"))
	deliverAt(t, fs, kind, noExtras, present.Context, present.Skills)
	files := deliverytest.RelativeFiles(fs, placeDir)
	require.NotContains(t, files, ".mock/skills/greet/SKILL.md", "skills was selected and the profile has none: released")
	require.Contains(t, files, ".mock/commands/go.md", "commands was not selected: untouched")
}

// TestRelease_PerKindAndWhole (test 10): releasing skills removes only the
// skills; a full release restores a hand-written context file byte for
// byte.
func TestRelease_PerKindAndWhole(t *testing.T) {
	fs := afero.NewMemMapFs()
	kind := lookupEngine(t, "claude-code")
	mine := "# My notes\n\nhand-written, keep me\n"
	testsupport.WriteFileString(t, fs, filepath.Join(placeDir, claude.ContextFileName), mine, 0o644)
	deliverAt(t, fs, kind, everyKindPackage(t, "OURS"), delivery.AllKinds()...)
	require.Contains(t, placedString(t, fs, claude.ContextFileName), "OURS")

	before := deliverytest.RelativeFiles(fs, placeDir)
	releaseAt(t, fs, kind, present.Skills)
	after := deliverytest.RelativeFiles(fs, placeDir)
	require.NotEqual(t, before, after)
	for _, f := range after {
		require.Contains(t, before, f)
	}
	for _, f := range before {
		if !slices.Contains(after, f) {
			require.Contains(t, f, claude.SkillsDirName, "only skills left: %s", f)
		}
	}

	releaseAt(t, fs, kind, delivery.AllKinds()...)
	require.Equal(t, mine, placedString(t, fs, claude.ContextFileName), "the user's file is restored byte for byte")
	require.Equal(t, []string{claude.ContextFileName}, deliverytest.RelativeFiles(fs, placeDir))
}

// TestDeliver_TwoEnginesCoexistInOneDirectory (tests 12 and 13): each
// engine delivers under its own writers, so the second never releases the
// first's files, and releasing one leaves the other's.
func TestDeliver_TwoEnginesCoexistInOneDirectory(t *testing.T) {
	fs := afero.NewMemMapFs()
	c, m := lookupEngine(t, "claude-code"), lookupEngine(t, "mock")
	pkg := everyKindPackage(t, "body")
	deliverAt(t, fs, c, pkg, delivery.AllKinds()...)
	claudeFiles := deliverytest.RelativeFiles(fs, placeDir)
	deliverAt(t, fs, m, pkg, delivery.AllKinds()...)
	both := deliverytest.RelativeFiles(fs, placeDir)
	for _, f := range claudeFiles {
		require.Contains(t, both, f, "the second engine kept the first's %s", f)
	}
	require.Contains(t, both, mock.ContextFileName)

	releaseAt(t, fs, m, delivery.AllKinds()...)
	require.Equal(t, claudeFiles, deliverytest.RelativeFiles(fs, placeDir), "releasing the mock left claude's files")
}

// TestDeliver_AtRestWritesNoSessionEndpoint (owner constraint 2026-10-07):
// ctxloom's MCP endpoint is session-scoped, so an at-rest delivery writes
// the profiles' servers and never ctxloom's endpoint entry — and with no
// declared server, no MCP file at all.
func TestDeliver_AtRestWritesNoSessionEndpoint(t *testing.T) {
	fs := afero.NewMemMapFs()
	kind := lookupEngine(t, "claude-code")
	pkg := everyKindPackage(t, "body")
	pkg.MCP[wire.LayerServerName] = wire.MCPServer{ServedBy: wire.ServedBySessionEndpoint}
	deliverAt(t, fs, kind, pkg, delivery.AllKinds()...)
	servers, _ := jsonDoc(t, fs, claude.MCPFileName)["mcpServers"].(map[string]any)
	require.Contains(t, servers, "tasks")
	require.NotContains(t, servers, wire.LayerServerName, "no ctxloom endpoint entry at rest")
	require.NotContains(t, placedString(t, fs, claude.MCPFileName), "Bearer")

	fs = afero.NewMemMapFs()
	only := compositetest.Fixture(t, compositetest.WithFragment("hello", "body"), compositetest.WithMCP(wire.LayerServerName, wire.MCPServer{ServedBy: wire.ServedBySessionEndpoint}))
	deliverAt(t, fs, kind, only, delivery.AllKinds()...)
	require.NotContains(t, deliverytest.RelativeFiles(fs, placeDir), claude.MCPFileName, "the endpoint alone asks for no MCP file at rest")
}

// TestDeliver_ASessionShapedPlacementUsesTheSessionHomeBranch (test 55):
// the core is not hard-wired to the project root — at a session-shaped
// placement (a cell with a session home, the session writer, nil kinds,
// zero extra) every engine delivers through its approaches' session-home
// branch and writes nothing into the project.
func TestDeliver_ASessionShapedPlacementUsesTheSessionHomeBranch(t *testing.T) {
	for _, name := range []string{"claude-code", "mock"} {
		t.Run(name, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			kind := lookupEngine(t, name)
			const home = "/session/home"
			cell := present.New(present.OnHost(present.Paths{SessionHome: present.Root{Host: home, Engine: home}, ProjectRoot: present.Root{Host: placeDir, Engine: placeDir}}))
			p := Placement{Start: cell, Family: delivery.SessionWriter("brisk-otter")}
			d, plan, err := Deliver(context.Background(), safefs.NewMem(fs), kind, everyKindPackage(t, "body"), delivery.Loadout{}, p)
			require.NoError(t, err)
			require.NotEmpty(t, plan.Static)
			for _, it := range plan.Static {
				require.Equal(t, present.RootSessionHome, it.Root, "%v", it.Kind)
			}
			require.NotEmpty(t, d.Wrote)
			require.Empty(t, deliverytest.RelativeFiles(fs, placeDir), "nothing in the project")
			require.NotEmpty(t, deliverytest.RelativeFiles(fs, home))
		})
	}
}

// TestDeliver_TheWritersSkipsAreReported (owner ruling 2026-10-07): a
// command or skill the engine's writers skip is reported on the loadout's
// sink, never discarded — here a skill claude's constraints refuse (no
// description) and a command whose name escapes the commands directory.
func TestDeliver_TheWritersSkipsAreReported(t *testing.T) {
	fs := afero.NewMemMapFs()
	kind := lookupEngine(t, "claude-code")
	pkg := compositetest.Fixture(t, compositetest.WithFragment("hello", "body"), compositetest.WithSkill("nodesc"), compositetest.WithCommand("../escape", "go"))
	var skipped report.Findings
	_, _, err := Deliver(context.Background(), safefs.NewMem(fs), kind, pkg, delivery.Loadout{Report: &skipped}, atRestPlacement(placeDir, kind.Root().Name, delivery.AllKinds()))
	require.NoError(t, err)
	var texts []string
	for _, f := range skipped {
		texts = append(texts, f.Text)
	}
	joined := strings.Join(texts, "\n")
	require.Contains(t, joined, `"nodesc"`, "the refused skill is reported")
	require.Contains(t, joined, "escape", "the skipped command is reported")
}
