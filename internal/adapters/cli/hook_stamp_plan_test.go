package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
)

// TestStampPlanHook_WarnsOnUnparsablePayload pins the hook's
// warn-and-continue contract on an UNPARSABLE payload: a payload this hook
// cannot decode at all is a contract break with the firing engine, so it must
// be diagnosable on stderr rather than
// vanishing — the same rule the sibling stdin-read branch already follows. The
// hook still returns nil: a stamping hiccup never fails the host's tool call.
func TestStampPlanHook_WarnsOnUnparsablePayload(t *testing.T) {
	t.Setenv("CTXLOOM_SESSION_HARP", "wave-u036-f10")

	var sink bytes.Buffer
	restore := clidiag.SetSink(&sink)
	t.Cleanup(restore)

	cmd := firedBy(t, &cobra.Command{}, claude.EngineName)
	cmd.SetIn(strings.NewReader("not json"))
	require.NoError(t, stampPlanCmd.RunE(cmd, nil),
		"a malformed payload must never fail the host agent's tool call")

	assert.Contains(t, sink.String(), "stamp-plan",
		"the unparsable payload must be reported on the diagnostic channel")
}

// TestStampPlanHook_SilentOnNonEditPayload is TestStampPlanHook_WarnsOnUnparsablePayload's
// counterpart: a well-formed payload that simply names no file (a non-edit
// tool call) is an ordinary, expected event and must stay silent, so the warn
// above cannot degrade into per-tool-call noise.
func TestStampPlanHook_SilentOnNonEditPayload(t *testing.T) {
	t.Setenv("CTXLOOM_SESSION_HARP", "wave-u036-f10")

	var sink bytes.Buffer
	restore := clidiag.SetSink(&sink)
	t.Cleanup(restore)

	cmd := firedBy(t, &cobra.Command{}, claude.EngineName)
	cmd.SetIn(strings.NewReader(`{"tool_input":{}}`))
	require.NoError(t, stampPlanCmd.RunE(cmd, nil))

	assert.Empty(t, sink.String(), "a payload with no file_path is not an error")
}

// stampPlanPayload is a post_file_edit payload naming path.
func stampPlanPayload(t *testing.T, path string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"tool_input": map[string]string{"file_path": path}})
	require.NoError(t, err)
	return string(b)
}

// The hook stamps the session's harp into an edited plan file, and leaves an
// edited file that is no plan file byte-for-byte alone.
func TestStampPlanHook_StampsOnlyAPlanFile(t *testing.T) {
	const harp = "wave-u036-f10"
	t.Setenv("CTXLOOM_SESSION_HARP", harp)
	dir := t.TempDir()
	const body = "# notes\n"
	plan := filepath.Join(dir, "plan.md")
	other := filepath.Join(dir, "notes.md")
	for _, p := range []string{plan, other} {
		require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	}

	for _, p := range []string{plan, other} {
		cmd := firedBy(t, &cobra.Command{}, claude.EngineName)
		cmd.SetIn(strings.NewReader(stampPlanPayload(t, p)))
		require.NoError(t, stampPlanCmd.RunE(cmd, nil))
	}

	got, err := os.ReadFile(plan)
	require.NoError(t, err)
	assert.Contains(t, string(got), harp, "a plan file is stamped with the session's harp")
	got, err = os.ReadFile(other)
	require.NoError(t, err)
	assert.Equal(t, body, string(got), "a file that is no plan file is never touched")
}
