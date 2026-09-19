package claude

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
)

// minimalBackend returns a claude backend whose Setup has RESOLVED the minimal
// launch posture for model. This is how a headless run acquires that argv now:
// the form is declared host-side (LaunchFormMinimal), Setup resolves the
// engine's declared posture from it, and buildArgs emits what Setup resolved.
// There is no request flag left for a test — or for production — to set.
//
// A backend without this Setup is a backend with no minimal posture, which is
// the honest way to write "an ordinary run" in these tests.
func minimalBackend(t *testing.T, model string) *ClaudeCode {
	t.Helper()
	b := NewClaudeCode()
	require.NoError(t, b.Setup(context.Background(), &agent.SetupRequest{
		Form:  agent.LaunchFormMinimal,
		Model: model,
	}))
	return b
}

// argValue returns the value following flag in args, or "" if the flag is
// absent or has no following element.
func argValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// sessionEnv is the run env a Setup reads its roots from: the session harp,
// from which the shared-cell Scratch derives, and the relocated engine home
// on claude's declared home var — the PRIVATE root the system-prompt and
// default mcp approaches land under. A run whose env carries no home advises
// no private root, and those approaches refuse rather than fall back; a test
// that wants that refusal builds its env without the var.
func sessionEnv(harp, home string) map[string]string {
	return map[string]string{sessionHarpEnv: harp, ConfigDirEnv: home}
}
