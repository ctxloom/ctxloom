package acp

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// TestChat_ContainerRuntimeUnavailable_FailsLoud is ISO1's fail-loud proof
// for "container runtime unavailable" — the FIRST of the three non-negotiable
// invariants (see acp.go/container_transport.go doc). PATH is stripped
// (t.Setenv), so isolation.SelectRuntime finds neither docker nor podman on
// ANY host, deterministically, regardless of what is actually installed —
// unlike the docker-gated tests, this needs no container runtime to run and
// belongs in the normal `just test` suite. It asserts the actual, actionable
// error text reaches the caller (never a silent host fallback): Chat must
// return a non-nil error naming the fix, and honor the StructuredChat
// contract (out closed exactly once) exactly like claude/codex's existing
// adapter-missing error does (internal/claude/chat.go).
func TestChat_ContainerRuntimeUnavailable_FailsLoud(t *testing.T) {
	t.Setenv("PATH", "")

	b := NewACP()
	b.command = "some-agent-acp"
	b.BinaryPath = "some-agent-acp"
	b.agentEngine = "claude"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	in := make(chan agent.ChatMessage)
	close(in)
	out := make(chan agent.ChatEvent, 4)

	err := b.Chat(ctx, agent.ChatRequest{
		WorkDir: t.TempDir(),
		Runtime: agent.RuntimeContainer,
	}, in, out)

	require.Error(t, err, "no docker/podman on PATH must fail the session open, never silently fall back to the host")
	assert.Contains(t, err.Error(), "no container runtime is reachable", "the error names WHAT is missing")
	assert.Contains(t, err.Error(), "install/start docker or podman", "the error names the fix, matching the adapter-missing error's quality bar")
	assert.Contains(t, err.Error(), "claude", "the error names the agent so a multi-agent editor session can tell which one failed")

	// out must still be closed exactly once per the StructuredChat contract
	// (session.go's Chat defers close(out) unconditionally) — a caller
	// ranging over out must see it close, not hang.
	_, stillOpen := <-out
	assert.False(t, stillOpen, "out must be closed on a container-transport failure")
}

// TestIsolationBackendFor_MapsKnownAliases pins the ACPConfig.AgentEngine
// (kiro/claude/codex) -> isolation container-spec-key (claude-code/
// kiro/codex/opencode) translation: the two vocabularies were
// never unified (see the function's doc), so a drift here would silently
// route a claude agent onto the WRONG image/auth spec. "agy" is
// deliberately in the unmapped set below: antigravity's container spec
// was removed in 0.7.0, so it now falls through like any other unrecognized
// name.
func TestIsolationBackendFor_MapsKnownAliases(t *testing.T) {
	cases := map[string]string{
		"claude":      "claude-code",
		"Claude":      "claude-code", // case-insensitive
		"agy":         "agy",
		"kiro":        "kiro",
		"codex":       "codex",
		"opencode":    "opencode",
		"":            "",
		"unknown-cli": "unknown-cli",
	}
	for in, want := range cases {
		assert.Equal(t, want, isolationBackendFor(in), "agent_engine %q", in)
	}
}

// TestSpawnTransport_RoutesOnTheLaunchNotOnLeftoverBackendState pins that the
// host/container decision belongs to the launch being made, not to whatever a
// previous argv build left on the backend. Building a CONTAINER chat's argv must
// not turn a subsequent HOST launch into a container launch: routing on residue
// makes correctness depend on Go's argument-evaluation order at one call site in
// session.go, which nothing at this seam can enforce.
//
// PATH is stripped so no container runtime is reachable on any host: if the call
// wrongly routes to the container transport, it says so in the error.
func TestSpawnTransport_RoutesOnTheLaunchNotOnLeftoverBackendState(t *testing.T) {
	t.Setenv("PATH", "")

	b := NewACP()
	b.command = "some-agent-acp"
	b.BinaryPath = filepath.Join(t.TempDir(), "no-such-agent-acp")

	_ = b.chatArgv(agent.ChatRequest{Runtime: agent.RuntimeContainer})

	_, err := b.spawnTransport(context.Background(), transportRequest{workDir: t.TempDir()})
	require.Error(t, err, "the binary does not exist, so the host spawn must fail")
	assert.NotContains(t, err.Error(), "no container runtime is reachable",
		"a host launch must not be routed into the container transport")
}
