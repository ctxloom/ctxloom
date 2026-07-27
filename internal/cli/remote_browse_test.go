package cli

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// U039-F03: a failed browse (operations.BrowseRemote returning an error, not
// a genuinely empty remote) reported "No bundles found in <remote>" and
// exited 0 — the canonical exit-0-empty-payload defect, and the message
// asserted a FALSE fact (the remote may be full of bundles; the browse just
// never reached it). An empty remote name is a real, deterministic way to
// force operations.BrowseRemote to fail ("remote is required") without
// mocking network/registry state.
func TestRunRemoteBrowse_FailedBrowseIsAnErrorNotEmptyClaim(t *testing.T) {
	testsupport.Isolate(t)
	cmd := &cobra.Command{}

	err := runRemoteBrowse(cmd, []string{""})
	assert.Error(t, err, "a browse failure must surface as an error, not a silent 'no bundles found' success")
}
