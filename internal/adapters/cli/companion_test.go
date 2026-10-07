// Tests for companion.go: `companion add` registers a NAME in the home config
// after checking the binary answers, `companion remove` previews by default
// and unregisters with --yes, and `companion list` reports each registered
// name and whether it resolves.
package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// setFlagForTest sets a package-level flag variable for one test.
func setFlagForTest(t *testing.T, v *bool, val bool) {
	t.Helper()
	prev := *v
	*v = val
	t.Cleanup(func() { *v = prev })
}

// answeringCompanionOnPath makes PATH one directory holding a real
// ctxloom-companion-<name> that answers the loadout probe, and returns that
// directory.
func answeringCompanionOnPath(t *testing.T, name string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script companions")
	}
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ctxloom-companion-"+name),
		[]byte("#!/bin/sh\n[ \"$1\" = loadout ] && printf 'version: 1.0.0\\n' && exit 0\nexit 1\n"), 0o755)) //nolint:gosec // an executable fixture
	t.Setenv("PATH", dir)
	return dir
}

func homeConfigText(t *testing.T) string {
	t.Helper()
	home, err := paths.HomeConfigDir()
	require.NoError(t, err)
	b, err := os.ReadFile(paths.ConfigPath(home)) //nolint:gosec // the sandboxed home's config
	require.NoError(t, err)
	return string(b)
}

func runAdd(t *testing.T, name string) string {
	t.Helper()
	resetApp()
	t.Cleanup(resetApp)
	cmd, out := formatCmd("json")
	cmd.SetContext(context.Background())
	require.NoError(t, runCompanionAddCmd(cmd, []string{name}))
	return out.String()
}

// TestCompanionAdd_RecordsOnlyTheNameInTheHomeConfig: run from inside a
// project, the registration still lands in the HOME config (the one every
// project and container reads), and it is the name, never the path.
func TestCompanionAdd_RecordsOnlyTheNameInTheHomeConfig(t *testing.T) {
	root, _ := setupProject(t, "claude-code")
	testsupport.ChangeDir(t, root)
	dir := answeringCompanionOnPath(t, "acme")

	var res operations.CompanionAddResult
	require.NoError(t, json.Unmarshal([]byte(runAdd(t, "acme")), &res))
	assert.Equal(t, operations.CompanionAddResult{Name: "acme", Bin: "ctxloom-companion-acme", Path: filepath.Join(dir, "ctxloom-companion-acme"), Added: true}, res)

	written := homeConfigText(t)
	assert.Contains(t, written, "companions:\n    - acme\n")
	assert.NotContains(t, written, dir, "the path is reported, never recorded")
	project, err := os.ReadFile(filepath.Join(root, paths.AppDirName, "config.yaml")) //nolint:gosec // the test project's config
	require.NoError(t, err)
	assert.NotContains(t, string(project), "acme", "the project config is not where a registration goes")
}

func TestCompanionAdd_NotOnPathFailsAndRecordsNothing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	resetApp()
	t.Cleanup(resetApp)
	cmd, _ := textCmd()
	cmd.SetContext(context.Background())
	err := runCompanionAddCmd(cmd, []string{"acme"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ctxloom-companion-acme")
	home, herr := paths.HomeConfigDir()
	require.NoError(t, herr)
	assert.NoFileExists(t, paths.ConfigPath(home))
}

// TestCompanionRemove_PreviewsThenUnregisters mirrors `remote remove`: bare
// reports and writes nothing; --yes unregisters; list then shows nothing.
func TestCompanionRemove_PreviewsThenUnregisters(t *testing.T) {
	answeringCompanionOnPath(t, "acme")
	runAdd(t, "acme")

	resetApp()
	cmd, out := textCmd()
	require.NoError(t, runCompanionRemoveCmd(cmd, []string{"acme"}))
	assert.Contains(t, out.String(), "ctxloom companion remove acme --yes")
	assert.Contains(t, homeConfigText(t), "- acme", "a preview writes nothing")

	resetApp()
	setFlagForTest(t, &companionRemoveYes, true)
	cmd, out = textCmd()
	cmd.SetContext(context.Background())
	require.NoError(t, runCompanionRemoveCmd(cmd, []string{"acme"}))
	assert.Contains(t, out.String(), "Removed companion 'acme'")
	assert.NotContains(t, homeConfigText(t), "acme")

	resetApp()
	cmd, out = formatCmd("json")
	require.NoError(t, runCompanionListCmd(cmd, nil))
	assert.JSONEq(t, `[]`, out.String())
}

func TestCompanionRemove_UnknownNameIsTheSentinel(t *testing.T) {
	resetApp()
	t.Cleanup(resetApp)
	cmd, _ := textCmd()
	require.ErrorIs(t, runCompanionRemoveCmd(cmd, []string{"acme"}), operations.ErrCompanionNotRegistered)
}

// TestCompanionList_ReportsWhetherEachResolves: a registered name whose
// binary is gone is listed with the way out, not dropped.
func TestCompanionList_ReportsWhetherEachResolves(t *testing.T) {
	answeringCompanionOnPath(t, "acme")
	runAdd(t, "acme")

	resetApp()
	cmd, out := formatCmd("json")
	require.NoError(t, runCompanionListCmd(cmd, nil))
	var got []operations.CompanionListing
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	require.Len(t, got, 1)
	assert.True(t, got[0].Resolves)

	t.Setenv("PATH", t.TempDir())
	resetApp()
	cmd, out = textCmd()
	require.NoError(t, runCompanionListCmd(cmd, nil))
	assert.Contains(t, out.String(), "NOT ON PATH")
	assert.Contains(t, out.String(), "ctxloom companion remove acme --yes")
}
