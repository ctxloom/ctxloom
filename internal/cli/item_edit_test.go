package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// itemEditSpyDistiller is a minimal operations.Distiller used only to SEED a
// fragment/command that already has a distilled form + content_hash (as if
// `... distill` had run once) — the precondition every --no-distill edit test
// starts from.
type itemEditSpyDistiller struct {
	value string
	model string
}

func (d *itemEditSpyDistiller) Distill(_ context.Context, _ operations.DistillRequest) (operations.DistillResult, error) {
	return operations.DistillResult{Distilled: d.value, ModelID: d.model}, nil
}

// setFakeEditor points $EDITOR at a tiny script that overwrites whatever file
// path it's given with newContent, so editItem's editInEditor round-trip is
// deterministic without spawning a real interactive editor. $VISUAL is
// cleared so it can't take precedence over $EDITOR (config.EditorFromEnv
// checks VISUAL first).
func setFakeEditor(t *testing.T, newContent string) {
	t.Helper()
	t.Setenv("VISUAL", "")
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-editor.sh")
	require.NoError(t, os.WriteFile(script,
		[]byte("#!/bin/sh\nprintf '%s' \"$CTXLOOM_TEST_EDIT_CONTENT\" > \"$1\"\n"), 0o755))
	t.Setenv("EDITOR", script)
	t.Setenv("CTXLOOM_TEST_EDIT_CONTENT", newContent)
}

// seedDistilledItem creates bundle "demo" with one item (fragment or command)
// that already carries a distilled form + content_hash, as GetItemContent/
// SetItemContent would see it after a prior successful `... distill`.
func seedDistilledItem(t *testing.T, cfg *config.Config, kind operations.ItemKind, name, content string) {
	t.Helper()
	_, err := operations.CreateBundle(context.Background(), cfg, operations.CreateBundleRequest{Name: "demo"})
	require.NoError(t, err)
	_, err = operations.AddItem(context.Background(), cfg, operations.AddItemRequest{
		Bundle: "demo", Kind: kind, Name: name, Content: content,
		Distiller: &itemEditSpyDistiller{value: "DISTILLED:" + content, model: "mock-model"},
	})
	require.NoError(t, err)
}

// setupEditProject wires a fresh project dir (fixture cfg + chdir, mirroring
// TestShowItem_NonInteractiveStdoutUnchanged) so editItem's own GetConfig()
// call resolves the same bundle the fixture cfg seeded.
//
// testsupport.ProjectDir, NOT a bare t.TempDir + t.Chdir: a fixture cfg
// governs only the config value this test holds, never the package-level
// GetConfig() that editItem itself calls. That one resolves the real app dir,
// and t.Chdir alone leaves HOME pointing at the developer's home — so
// findAppDir's ~/.ctxloom fallback landed editItem's writes in the user's real
// home. ProjectDir isolates the environment as well as the working directory.
func setupEditProject(t *testing.T) *config.Config {
	t.Helper()
	root := testsupport.ProjectDir(t)
	appDir := filepath.Join(root, ".ctxloom")
	return config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
}

// TestEditItem_NoDistillClearsDistilledAndWarns is the RED-then-GREEN case for
// the --no-distill flag: content is updated, the stale distilled form is
// cleared (never left describing the OLD content — the correctness hazard
// the flag must not introduce), and a warning names the exact fix command.
func TestEditItem_NoDistillClearsDistilledAndWarns(t *testing.T) {
	for _, tc := range []struct {
		name     string
		kind     operations.ItemKind
		itemType ItemType
	}{
		{"fragment", operations.ItemKindFragment, ItemTypeFragment},
		{"command", operations.ItemKindCommand, ItemTypeCommand},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := setupEditProject(t)
			seedDistilledItem(t, cfg, tc.kind, "x", "v1")
			setFakeEditor(t, "v2")

			ref := "demo#" + itemRefPrefix(tc.itemType) + "x"
			// The renderer writes to cmd.OutOrStdout(), not the process's real
			// stdout, so this buffer IS the delivered payload.
			cmd, buf := testCmd()
			require.NoError(t, editItem(cmd, ref, tc.itemType, true))
			out := buf.String()

			wantWarn := "warning: distilled form not refreshed (--no-distill); run `ctxloom " +
				string(tc.itemType) + " distill " + ref + "`"
			assert.Contains(t, out, wantWarn, "warning must name the exact fix command")
			assert.NotContains(t, out, "(re-distilled)", "must not claim a redistill happened")

			got, err := operations.GetItemContent(context.Background(), cfg, operations.GetItemRequest{
				Bundle: "demo", Kind: tc.kind, Name: "x",
			})
			require.NoError(t, err)
			assert.Equal(t, "v2", got.Content, "raw content must be updated")
			assert.Empty(t, got.Distilled,
				"distilled form must be CLEARED, never left describing the OLD content (the correctness hazard)")
		})
	}
}

// TestEditItem_NoDistillRedundantOnAlreadyNoDistillItem confirms the warning
// is suppressed when the item was already marked no_distill: the flag had no
// effect (the item was never going to auto-distill), so warning would be
// noise pointing at a distill command that itself just reports "no_distill".
func TestEditItem_NoDistillRedundantOnAlreadyNoDistillItem(t *testing.T) {
	cfg := setupEditProject(t)
	_, err := operations.CreateBundle(context.Background(), cfg, operations.CreateBundleRequest{
		Name: "demo",
		Fragments: map[string]operations.BundleFragmentInput{
			"x": {Content: "v1", NoDistill: true},
		},
	})
	require.NoError(t, err)
	setFakeEditor(t, "v2")

	cmd, buf := testCmd()
	require.NoError(t, editItem(cmd, "demo#fragments/x", ItemTypeFragment, true))
	out := buf.String()

	assert.NotContains(t, out, "warning: distilled form not refreshed",
		"an already no_distill item's --no-distill edit changes nothing about distillation; no warning needed")
}

// TestEditNoDistillWarning pins the exact decision table for the warning text,
// independent of the editor round-trip or the real (LLM-backed) distiller —
// distillerForEdit/editNoDistillWarning are the two pure seams editItem's
// control flow delegates to, and this is where "no flag -> no warning" is
// covered without paying for a real distill attempt (see
// TestEditItem_NoDistillClearsDistilledAndWarns for the end-to-end case).
func TestEditNoDistillWarning(t *testing.T) {
	const ref = "demo#fragments/x"
	cases := []struct {
		name                string
		noDistillFlag       bool
		wasAlreadyNoDistill bool
		want                string
	}{
		{"flag unset: no warning regardless of persistent state", false, false, ""},
		{"flag unset, item already no_distill: still no warning", false, true, ""},
		{"flag set, item already no_distill: no warning (flag was redundant)", true, true, ""},
		{
			"flag set, item was distillable: warns with the exact fix command",
			true, false,
			"warning: distilled form not refreshed (--no-distill); run `ctxloom fragment distill demo#fragments/x` before relying on distilled-mode output\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := editNoDistillWarning(ItemTypeFragment, ref, tc.noDistillFlag, tc.wasAlreadyNoDistill)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestDistillerForEdit pins that --no-distill produces a nil Distiller
// (SetItemContent's documented "skip distillation entirely" signal) without
// ever touching newLLMDistiller's real backend-resolution/plugin-launch path.
func TestDistillerForEdit(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{})
	d, err := distillerForEdit(cfg, true)
	require.NoError(t, err, "--no-distill resolves no prompt, so it can never be refused over one")
	assert.Nil(t, d, "--no-distill must yield a nil Distiller")
}
