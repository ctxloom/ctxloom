// Command acpl1harness is TEST INFRASTRUCTURE for the ACP H1 conformance
// harness's L1 loopback suite (internal/acpagent/loopback_test.go). It is
// NEVER built or shipped by any release path (no justfile/goreleaser target
// references this package) — it exists purely so the L1 suite can spawn a
// REAL, SEPARATE OS PROCESS speaking ACP over REAL stdio, joining the two
// halves that server_test.go and fakeagent_test.go otherwise only ever
// exercise in-process.
//
// It calls the EXACT SAME production entrypoint `ctxloom acp` calls —
// acpagent.Serve — so every byte of protocol-serving code under test
// (internal/acpagent/server.go, wire.go, mapping.go) is 100% real,
// unmodified production code running in a genuine child process. The ONLY
// thing swapped out is the ENGINE behind it (see engine.go): a small
// deterministic state machine keyed on the prompt text, instead of a real
// LLM backend.
//
// Why not literally spawn `ctxloom acp`, unmodified, against a real engine
// config: no registered backend is both StructuredChat-capable (required for
// ACP mode) AND hermetic/credential-free. The "mock" backend
// (internal/lm/backends/mock.go) only implements the oneshot Execute path,
// not StructuredChat, so `ctxloom acp` would fail every session/new against
// it. The real engines (claude/kiro/codex) need live credentials and
// network. The generic "acp" backend would work but requires standing up a
// THIRD real subprocess hop (ctxloom acp -> self-exec `ctxloom llm serve
// acp` gRPC plugin -> a hand-written fake-agent binary) plus a full ctxloom
// project/config/session-accounting environment — none of which this
// foundational slice needs, and all of which would couple L1's stability to
// unrelated subsystems (config loading, executable trust gates, session
// harp accounting). acp_cmd.go / openACPEngineChat is also explicitly out of
// scope for this slice (ISO0 owns it), so adding a test-mode opener hook
// there was not an option anyway. This binary is the minimal, honest
// alternative: it adds ZERO new surface to the shipped ctxloom binary (this
// package is never referenced by cmd/ctxloom or any build script) while
// still exercising the real acpagent.Serve across a genuine process
// boundary. See the H1 harness report for the full rationale.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/ctxloom/ctxloom/internal/acpagent"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := acpagent.Serve(ctx, os.Stdin, os.Stdout, openHarnessEngine); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "acpl1harness:", err)
		os.Exit(1)
	}
}
