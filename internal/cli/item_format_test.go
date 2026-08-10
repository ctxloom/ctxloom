package cli

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// itemFormatCmd is a bare command with the persistent --format flag registered
// and set to format, so emit() takes the structured branch. It mirrors
// llmDefaultTestCmd's shape for the item flow.
func itemFormatCmd(t *testing.T, format string) (*cobra.Command, func() []byte) {
	t.Helper()
	cmd, buf := testCmd()
	cmd.Flags().String("format", format, "")
	require.NoError(t, cmd.Flags().Set("format", format))
	return cmd, func() []byte { return buf.Bytes() }
}

// itemFormatProject builds a project the item commands can mutate, chdir'd so
// GetConfig() resolves it.
func itemFormatProject(t *testing.T) *config.Config {
	t.Helper()
	root := t.TempDir()
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{filepath.Join(root, ".ctxloom")}})
	t.Chdir(root)
	return cfg
}

// TestItemCommands_HonourFormatJSON pins the item half of this invariant:
// `fragment create|remove|distill` (and their `command` twins) printed human
// text with bare fmt.Printf straight to the process's stdout and exited 0
// whatever --format asked for: the flag was accepted and discarded, so a
// frontend that asked for JSON got prose it could not parse and no error saying
// so. The assertion is on the PAYLOAD — valid JSON carrying the operation's own
// result fields.
func TestItemCommands_HonourFormatJSON(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind ItemType
	}{
		{"fragment", ItemTypeFragment},
		{"command", ItemTypeCommand},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := itemFormatProject(t)
			_, err := operations.CreateBundle(context.Background(), cfg, operations.CreateBundleRequest{Name: "demo"})
			require.NoError(t, err)
			ref := "demo#" + itemRefPrefix(tc.kind) + "x"

			cmd, out := itemFormatCmd(t, string(clifmt.FormatJSON))
			require.NoError(t, createItem(cmd, "demo", "x", tc.kind))
			created := decodeItemJSON(t, out())
			assert.Equal(t, "created", created["status"])
			assert.Equal(t, "x", created["name"])

			cmd, out = itemFormatCmd(t, string(clifmt.FormatJSON))
			require.NoError(t, distillItem(cmd, ref, tc.kind, false))
			distilled := decodeItemJSON(t, out())
			assert.Equal(t, "skipped", distilled["status"], "no LLM plugin is configured here")
			assert.NotEmpty(t, distilled["reason"], "the skip reason must reach a machine reader")

			cmd, out = itemFormatCmd(t, string(clifmt.FormatJSON))
			require.NoError(t, removeItem(cmd, ref, tc.kind, false))
			previewed := decodeItemJSON(t, out())
			assert.Equal(t, "preview", previewed["status"])
			assert.Equal(t, false, previewed["applied"])

			cmd, out = itemFormatCmd(t, string(clifmt.FormatJSON))
			require.NoError(t, removeItem(cmd, ref, tc.kind, true))
			removed := decodeItemJSON(t, out())
			assert.Equal(t, "deleted", removed["status"])
			assert.Equal(t, "x", removed["name"])
		})
	}
}

// TestRemoveItem_BareReportsAndDestroysNothing pins the report side of the
// safety posture: a bare `fragment|command remove` (yes == false) must name
// the target, say plainly that nothing was removed, name the --yes command
// to apply it, and — the assertion that actually catches a broken guard —
// leave the item exactly as it was. A "preview" that quietly deletes anyway
// passes any test that only checks exit code or output text; this one
// re-reads the item afterward.
func TestRemoveItem_BareReportsAndDestroysNothing(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind ItemType
	}{
		{"fragment", ItemTypeFragment},
		{"command", ItemTypeCommand},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := itemFormatProject(t)
			_, err := operations.CreateBundle(context.Background(), cfg, operations.CreateBundleRequest{Name: "demo"})
			require.NoError(t, err)
			createCmd, _ := testCmd()
			require.NoError(t, createItem(createCmd, "demo", "x", tc.kind))
			ref := "demo#" + itemRefPrefix(tc.kind) + "x"

			cmd, buf := testCmd()
			require.NoError(t, removeItem(cmd, ref, tc.kind, false))
			out := buf.String()
			assert.Contains(t, out, "Nothing was removed")
			assert.Contains(t, out, "--yes")

			_, err = operations.GetItemContent(context.Background(), cfg, operations.GetItemRequest{
				Bundle: "demo", Kind: tc.kind, Name: "x",
			})
			assert.NoError(t, err, "the bare (no --yes) path must leave the item in place")
		})
	}
}

// TestRemoveItem_YesDestroys pins the apply side: --yes (yes == true) must
// actually remove the item. Paired with the bare-path test above so a
// regression in either direction — bare destroys, or --yes no-ops — is
// caught by an assertion on the item's continued existence, not just on exit
// code or a status string a broken implementation could still print.
func TestRemoveItem_YesDestroys(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind ItemType
	}{
		{"fragment", ItemTypeFragment},
		{"command", ItemTypeCommand},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := itemFormatProject(t)
			_, err := operations.CreateBundle(context.Background(), cfg, operations.CreateBundleRequest{Name: "demo"})
			require.NoError(t, err)
			createCmd, _ := testCmd()
			require.NoError(t, createItem(createCmd, "demo", "x", tc.kind))
			ref := "demo#" + itemRefPrefix(tc.kind) + "x"

			cmd, _ := testCmd()
			require.NoError(t, removeItem(cmd, ref, tc.kind, true))

			_, err = operations.GetItemContent(context.Background(), cfg, operations.GetItemRequest{
				Bundle: "demo", Kind: tc.kind, Name: "x",
			})
			assert.ErrorIs(t, err, operations.ErrItemNotFound, "--yes must actually remove the item")
		})
	}
}

// TestItemCommands_TextGoesToTheCommandsWriter pins the other half of the same
// defect: the human lines went to the process's real os.Stdout, so a caller that
// redirected the command's output (a test, the VSCode companion capturing a
// subprocess) saw nothing while the command reported success — the bug
// printItemInfos was already fixed for.
func TestItemCommands_TextGoesToTheCommandsWriter(t *testing.T) {
	cfg := itemFormatProject(t)
	_, err := operations.CreateBundle(context.Background(), cfg, operations.CreateBundleRequest{Name: "demo"})
	require.NoError(t, err)

	cmd, buf := testCmd()
	require.NoError(t, createItem(cmd, "demo", "x", ItemTypeFragment))
	assert.Contains(t, buf.String(), `Created fragment "x" in bundle "demo"`)
	assert.Contains(t, buf.String(), "Edit with: ctxloom fragment edit demo#fragments/x")

	cmd, buf = testCmd()
	require.NoError(t, removeItem(cmd, "demo#fragments/x", ItemTypeFragment, true))
	assert.Contains(t, buf.String(), `Removed fragment "x" from bundle "demo"`)
}

func decodeItemJSON(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	require.Truef(t, json.Valid(raw), "--format json must emit JSON, got:\n%s", raw)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(raw, &payload))
	return payload
}
