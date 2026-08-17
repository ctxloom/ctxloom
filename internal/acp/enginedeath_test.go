//go:build !windows

package acp

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/stderrtail"
)

// dyingAgentScript reproduces a real incident's shape: the ACP adapter
// dies in its MODULE LOADER, printing the one decisive diagnostic on STDERR
// and exiting before it ever answers `initialize`. The real message was
// `SyntaxError: Unexpected token 'with'` from a Node 18 that could not load
// claude-code-acp's entry module; it was only ever recoverable via `docker
// logs` against a still-live container, and the container is force-removed on
// teardown — so in production it was unrecoverable, and the operator saw a
// child that wrote one `user` record and then nothing, with no reason
// anywhere.
const dyingAgentScript = `#!/bin/sh
echo "SyntaxError: Unexpected token 'with'" >&2
exit 1
`

// newDyingACP builds an ACP driver pointed at a throwaway script standing in
// for a real engine adapter, so the whole spawn→initialize→death path runs
// hermetically (no engine binary, no container).
func newDyingACP(t *testing.T, script string) (*ACP, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "dying-agent.sh")
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))

	b := &ACP{shutdownGrace: 50 * time.Millisecond}
	b.BaseBackend = agent.NewBaseBackend("acp", "1.0.0")
	b.BinaryPath = "/bin/sh"
	b.command = "/bin/sh " + path
	return b, dir
}

// runDyingChat drives one Chat call to completion against the dying script,
// draining `out` so the driver is never blocked on an unread event.
func runDyingChat(t *testing.T, b *ACP, dir string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	in := make(chan agent.ChatMessage)
	out := make(chan agent.ChatEvent, 32)
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for range out { //nolint:revive // drain
		}
	}()
	err := b.Chat(ctx, agent.ChatRequest{WorkDir: dir}, in, out)
	<-drained
	return err
}

// TestChat_EngineDiesBeforeInitialize_ErrorCarriesStderrTail is the
// regression this whole change exists for: an engine that dies in its module
// loader must be able to SAY WHY. Before the fix the driver inherited the
// adapter's stderr (cmd.Stderr = os.Stderr) and returned a bare
// "acp: connection closed" — the decisive line went to the container's
// stdout and nowhere a caller could read it. Asserting merely "Chat
// returned an error" is the exact failure this test exists to prevent.
func TestChat_EngineDiesBeforeInitialize_ErrorCarriesStderrTail(t *testing.T) {
	b, dir := newDyingACP(t, dyingAgentScript)

	err := runDyingChat(t, b, dir)

	require.Error(t, err, "an engine that exits before answering initialize must fail the chat")
	assert.Contains(t, err.Error(), "SyntaxError: Unexpected token 'with'",
		"the engine's dying words are the ONLY root-cause evidence there is; a bare transport error is what made the real incident take 49 minutes")
}

// TestChat_EngineStderrTail_IsBounded pins the tail's shape: an engine that
// floods stderr must not push an unbounded blob into the error (and through
// it into the transcript and the parent's mailbox) — a bounded TAIL is the
// right shape, and the tail keeps the LAST bytes, which is where a dying
// process's cause lives.
func TestChat_EngineStderrTail_IsBounded(t *testing.T) {
	// 200k of noise, then the real cause on the last line.
	flooding := `#!/bin/sh
i=0
while [ $i -lt 2000 ]; do
  echo "noise-line-padding-padding-padding-padding-padding-padding-padding-padding-padding" >&2
  i=$((i+1))
done
echo "FATAL: the actual cause" >&2
exit 1
`
	b, dir := newDyingACP(t, flooding)

	err := runDyingChat(t, b, dir)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "FATAL: the actual cause",
		"the tail must keep the LAST stderr bytes — a dying process says why at the end")
	assert.Less(t, len(err.Error()), 2*stderrtail.DefaultBytes,
		"the tail must be BOUNDED: unbounded engine stderr forwarded into a transcript is its own problem")
}
