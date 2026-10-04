package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	taskops "github.com/ctxloom/ctxloom/internal/shared/tasks/operations"
)

// mockProjectYAML is a project whose fast role is the mock engine, so an
// internal one-shot resolves without any real vendor CLI.
const mockProjectYAML = "schema_version: 6\nllm:\n  configs:\n    fast: { type: mock }\n  defaults:\n    fast: fast\n"

// blockCoordinatorRoots makes every coordinator root of the project
// impossible to create: a regular file stands where the project's directory
// of roots belongs. The project key is resolved by the same call the
// coordinator host makes, so the block lands where it looks.
func blockCoordinatorRoots(t *testing.T, projectDir string) {
	t.Helper()
	key, _, err := taskops.ResolveProjectIdentity(projectDir)
	require.NoError(t, err, "fixture precondition: the project resolves a stable identity")
	dir, err := coord.RootStateDir(key, projectDir, "any-root")
	require.NoError(t, err)
	project := filepath.Dir(dir)
	require.NoError(t, os.MkdirAll(filepath.Dir(project), 0o700))
	require.NoError(t, os.WriteFile(project, nil, 0o600))
}

// distillBlockedProject is a project whose fast role resolves (so a real
// distiller is built) but whose distill one-shot cannot run: no coordinator
// can stand up to host it. That is the reported production shape of a failed
// distillation — content saved raw — and the command must not call it
// success.
func distillBlockedProject(t *testing.T) string {
	t.Helper()
	isolatedHome(t)
	root := agentProject(t, mockProjectYAML)
	blockCoordinatorRoots(t, root)
	t.Cleanup(closeInternalCoordinator)
	return root
}

// seedDemoItem creates bundle "demo" holding item x through the operations
// core, then republishes the generation so the command under test sees it.
func seedDemoItem(t *testing.T, kind operations.ItemKind, content string, d operations.Distiller) {
	t.Helper()
	cfg, err := GetConfig()
	require.NoError(t, err)
	_, err = operations.CreateBundle(context.Background(), cfg, operations.CreateBundleRequest{Name: "demo"})
	require.NoError(t, err)
	cfg = reloaded(t)
	_, err = operations.AddItem(context.Background(), cfg, operations.AddItemRequest{
		Bundle: "demo", Kind: kind, Name: "x", Content: content, Distiller: d,
	})
	require.NoError(t, err)
	reloaded(t)
}

func bufferedCmd() (*cobra.Command, *bytes.Buffer, *bytes.Buffer) {
	var out, errBuf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	return cmd, &out, &errBuf
}

// TestBundleDistill_FailedItemIsNotSuccess: an item whose distillation failed
// used to be tallied as "skipped" and the command exited 0.
func TestBundleDistill_FailedItemIsNotSuccess(t *testing.T) {
	root := distillBlockedProject(t)
	target := filepath.Dir(bundletree.WriteOS(t, root, "target", "name: target\ndescription: a bundle\nfragments:\n  f:\n    content: some prose worth compressing, at length, repeatedly.\n"))

	cmd, out, _ := bufferedCmd()
	err := runBundleDistill(cmd, []string{target})

	require.ErrorIs(t, err, errDistillFailed, "a failed distillation must fail the command (stdout: %s)", out.String())
	assert.Contains(t, err.Error(), "1 item", "the failure names how many items were left undistilled")
}

// TestItemDistill_FailedIsNotSuccess: `fragment distill` on a failed
// distillation used to print nothing and exit 0.
func TestItemDistill_FailedIsNotSuccess(t *testing.T) {
	distillBlockedProject(t)
	seedDemoItem(t, operations.ItemKindFragment, "some prose worth compressing, at length, repeatedly.", nil)

	cmd, out, _ := bufferedCmd()
	err := distillItem(cmd, "demo#fragments/x", ItemTypeFragment, false)

	require.ErrorIs(t, err, errDistillFailed, "a failed distillation must fail the command (stdout: %s)", out.String())
	assert.NotContains(t, out.String(), "Distilled x", "nothing may be reported distilled")
}

// TestItemEdit_FailedRedistillIsNotSuccess: an edit whose re-distillation
// failed printed "Updated ..." and exited 0 — the new content saved raw, the
// old distilled form cleared, and nothing said.
func TestItemEdit_FailedRedistillIsNotSuccess(t *testing.T) {
	distillBlockedProject(t)
	seedDemoItem(t, operations.ItemKindFragment, "v1", &itemEditSpyDistiller{value: "DISTILLED:v1", model: "mock-model"})
	setFakeEditor(t, "v2 is new prose worth compressing, at length, repeatedly.")

	cmd, out, _ := bufferedCmd()
	err := editItem(cmd, "demo#fragments/x", ItemTypeFragment, false)

	require.ErrorIs(t, err, errDistillFailed, "a failed re-distillation must fail the command (stdout: %s)", out.String())
	assert.NotContains(t, out.String(), "(re-distilled)")

	cfg, gerr := GetConfig()
	require.NoError(t, gerr)
	got, gerr := operations.GetItemContent(context.Background(), cfg, operations.GetItemRequest{Bundle: "demo", Kind: operations.ItemKindFragment, Name: "x"})
	require.NoError(t, gerr)
	assert.Equal(t, "v2 is new prose worth compressing, at length, repeatedly.", got.Content, "the edit itself is kept: only the distillation failed")
}
