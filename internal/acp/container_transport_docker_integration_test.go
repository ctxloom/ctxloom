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
// probes the real docker daemon, resolves the run's container auth through the
// production table (isolationBackendFor -> engineContainerSpecFor), and
// RunAttached spawns a REAL `docker run` with the identical-path project
// mount. The agent engine is set to "mock" precisely because container auth is
// keyed on the ENGINE: the harness is credential-free, so it maps to the one
// mapping that authenticates against no vendor, and this test needs NO host
// credential of any kind. It used to leave the engine unset, which reached the
// table's default arm and borrowed whatever claude auth the developer's host
// happened to have — which is how it came to need an ambient-credentials skip,
// and then to fail outright once that default started failing closed. Skips
// only when docker is unavailable.
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

// TestACPContainerTransport_RealTurn drives one full ACP conversation through
// containerTransport against a REAL container, asserting genuine payload —
// never just "no error": the exact echoed assistant text and the exact
// completion stop reason the harness engine's default (non-sentinel) case
// deterministically produces (cmd/acpl1harness/engine.go's runTurn).
func TestACPContainerTransport_RealTurn(t *testing.T) {
	dockergate.RequireRuntime(t, (isolation.Docker{}).Available(), "the ACP container transport integration test")
	buildHarnessImage(t)

	workDir := t.TempDir()

	b := NewACP()
	b.command = "/acpl1harness"
	b.BinaryPath = "/acpl1harness"
	b.containerImage = acpl1HarnessImage
	// Container auth keys on the ENGINE. The harness authenticates against no
	// vendor, so it declares the engine whose auth mapping says exactly that
	// ("mock"); the extra `--agent-engine mock` this puts on the harness argv
	// is ignored by cmd/acpl1harness, which parses no flags at all.
	b.agentEngine = "mock"

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
