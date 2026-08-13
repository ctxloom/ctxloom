//go:build docker_integration

// ISO1's docker-gated end-to-end proof: a REAL ACP conversation (initialize
// -> session/new -> session/prompt -> completion), all real production
// protocol code (this driver + jsonrpc + the SAME acpagent.Serve `ctxloom
// acp` itself calls), running INSIDE a container via containerTransport
// instead of on the host. Build-tagged so `just test` never compiles it (the
// gate stays green without docker); run with:
//
//	GOWORK=off just test-pkg ./internal/acp/... -tags docker_integration -run ContainerTransport
//
// The "engine" is cmd/acpl1harness — test infrastructure that speaks the
// REAL ACP agent protocol (acpagent.Serve) over real stdio with a
// deterministic, credential-free scripted engine (see its doc) — chosen so
// this proof needs no API key, no network, and no multi-hundred-MB engine
// image: only the container TRANSPORT is under test here, not any specific
// engine's own behavior (that's claude/chat.go's job, unchanged by ISO1).
//
// It still exercises the production container gate for real: PrepareWorkspace
// probes the real docker daemon, resolves real claude auth off THIS host's
// ambient credentials/env (the "default" container spec — see
// isolationBackendFor's doc; the spec's ENGINE identity is
// irrelevant here, only its auth resolver runs), and RunAttached spawns a
// REAL `docker run` with the identical-path project mount. Skips outright
// when docker is unavailable OR no claude auth is resolvable (ANTHROPIC_API_
// KEY/ANTHROPIC_AUTH_TOKEN or ~/.claude credentials) — the SAME ambient
// requirement internal/lm/isolation's own committed docker-gated container
// test already carries (TestContainerPolicy_TransportEndToEnd), not a new
// one this test introduces.
package acp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport/dockergate"
)

const acpl1HarnessImage = "ctxloom-acp-iso1-itest:latest"

// hasResolvableClaudeAuth mirrors resolveClaudeContainerAuth's own trigger
// check (auth.go) without importing package isolation's unexported pieces:
// same env vars, same credentials-file fallback path. If NEITHER is present
// PrepareWorkspace will fail this test's gate for a reason that has nothing
// to do with ISO1's container transport, so this test skips instead of
// reporting a false red.
func hasResolvableClaudeAuth() bool {
	if os.Getenv("ANTHROPIC_API_KEY") != "" || os.Getenv("ANTHROPIC_AUTH_TOKEN") != "" {
		return true
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(home, ".claude", ".credentials.json"))
	return err == nil
}

// TestACPContainerTransport_RealTurn drives one full ACP conversation through
// containerTransport against a REAL container, asserting genuine payload —
// never just "no error": the exact echoed assistant text and the exact
// completion stop reason the harness engine's default (non-sentinel) case
// deterministically produces (cmd/acpl1harness/engine.go's runTurn).
func TestACPContainerTransport_RealTurn(t *testing.T) {
	dockergate.RequireRuntime(t, (isolation.Docker{}).Available(), "the ACP container transport integration test")
	if !hasResolvableClaudeAuth() {
		dockergate.SkipCapability(t, "no resolvable claude auth (ANTHROPIC_API_KEY/ANTHROPIC_AUTH_TOKEN or ~/.claude credentials): PrepareWorkspace's auth gate needs SOME resolvable auth regardless of the test engine (see isolationBackendFor's doc)")
	}
	buildHarnessImage(t)

	workDir := t.TempDir()

	b := NewACP()
	b.command = "/acpl1harness"
	b.BinaryPath = "/acpl1harness"
	b.containerImage = acpl1HarnessImage

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	in := make(chan agent.ChatMessage, 1)
	out := make(chan agent.ChatEvent, 32)

	const prompt = "ISO1 container transport proof"
	in <- agent.ChatMessage{Text: prompt}
	close(in)

	errCh := make(chan error, 1)
	go func() {
		errCh <- b.Chat(ctx, agent.ChatRequest{
			WorkDir:     workDir,
			Runtime:     agent.RuntimeContainer,
			Permissions: agent.PermissionBypass,
		}, in, out)
	}()

	var (
		gotSession  bool
		gotEcho     bool
		gotComplete bool
	)
	for ev := range out {
		switch {
		case ev.Session != nil:
			gotSession = true
		case ev.Entry != nil:
			assert.Equal(t, "echo: "+prompt, ev.Entry.Content,
				"the harness engine's default case must echo the prompt VERBATIM — proves the turn actually ran inside the container, not a stub success")
			gotEcho = true
		case ev.Complete != nil:
			assert.Equal(t, "end_turn", ev.Complete.StopReason,
				"the harness engine's default case completes with end_turn")
			gotComplete = true
		}
	}

	require.NoError(t, <-errCh, "Chat must complete cleanly over the container transport")
	assert.True(t, gotSession, "must have received the session-start event")
	assert.True(t, gotEcho, "must have received the echoed assistant entry")
	assert.True(t, gotComplete, "must have received the turn-completion event")
}

// buildHarnessImage builds cmd/acpl1harness statically and layers it into a
// minimal alpine image. Rebuilt each run so the test exercises the current
// tree's harness, matching the sibling isolation package's own
// buildIntegrationImage pattern (container_integration_test.go).
func buildHarnessImage(t *testing.T) {
	t.Helper()
	dir := t.TempDir()

	bin := filepath.Join(dir, "acpl1harness")
	build := exec.Command("go", "build", "-o", bin, "github.com/ctxloom/ctxloom/cmd/acpl1harness")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH, "GOWORK=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build static acpl1harness: %v\n%s", err, out)
	}

	dockerfile := "FROM alpine:latest\nCOPY acpl1harness /acpl1harness\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0o644))

	img := exec.Command("docker", "build", "-t", acpl1HarnessImage, dir)
	if out, err := img.CombinedOutput(); err != nil {
		t.Fatalf("docker build harness image: %v\n%s", err, out)
	}
}
