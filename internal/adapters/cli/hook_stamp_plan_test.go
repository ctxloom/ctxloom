package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
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
