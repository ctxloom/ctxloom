package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
)

// =============================================================================
// Deterministic --session resume tests
// =============================================================================
// --session (full resume, resumeFullContext) is IoC-extracted behind an
// entriesFn seam, so it is testable without a live session index or backend
// transcript reader.

// TestResumeFullContext_FoldsRenderedTranscript covers the happy path: a
// resolvable harp's recorded entries render and join onto the existing
// assembled context via the SAME operations.RenderResumedTranscript/
// textblocks.Join primitives the ACP resume path uses.
func TestResumeFullContext_FoldsRenderedTranscript(t *testing.T) {
	entries := []agent.SessionEntry{
		{Type: agent.EntryTypeUser, Content: "what does this function do?"},
		{Type: agent.EntryTypeAssistant, Content: "it parses the config file."},
	}
	var requestedHarp string
	entriesFn := func(h string) ([]agent.SessionEntry, error) {
		requestedHarp = h
		return entries, nil
	}

	got := resumeFullContext("existing project context", "swift-amber-falcon", entriesFn)

	assert.Equal(t, "swift-amber-falcon", requestedHarp, "entriesFn is called with the resumed harp")
	assert.Contains(t, got, "existing project context", "the run's own assembled context is preserved")
	assert.Contains(t, got, "Resumed session swift-amber-falcon", "the rendered transcript's lead-block header is folded in")
	assert.Contains(t, got, "what does this function do?")
	assert.Contains(t, got, "it parses the config file.")
}

// TestResumeFullContext_UnresolvableHarpDegrades covers the fault-tolerant
// path (CLAUDE.md): an unknown/unbound --session harp must warn, not block
// the launch — the existing assembled context is returned unchanged.
func TestResumeFullContext_UnresolvableHarpDegrades(t *testing.T) {
	entriesFn := func(string) ([]agent.SessionEntry, error) {
		return nil, errors.New("unknown session")
	}

	got := resumeFullContext("existing project context", "no-such-harp", entriesFn)

	assert.Equal(t, "existing project context", got, "an unresolvable harp must not alter or block on the existing context")
}

// TestResumeFullContext_EmptyExistingContext covers a bare `ctxloom run
// --session <harp>` with no -p/-f/-t/--agent context of its own: the rendered
// transcript becomes the WHOLE assembled context, not an empty string glued
// onto nothing.
func TestResumeFullContext_EmptyExistingContext(t *testing.T) {
	entries := []agent.SessionEntry{
		{Type: agent.EntryTypeUser, Content: "hello"},
	}
	got := resumeFullContext("", "swift-amber-falcon", func(string) ([]agent.SessionEntry, error) { return entries, nil })
	assert.Contains(t, got, "hello")
	assert.Contains(t, got, "Resumed session swift-amber-falcon")
}

// =============================================================================
// Run Command Tests
// =============================================================================
// Full run-command behavior requires real plugin execution; that lives in
// the integration suite. The unit tests below cover the IoC-extracted
// resume-intent decision tree, which has the most branching logic in
// run.go and previously had zero coverage.

// TestRunCommand_Integration documents that run command requires full system
// integration including config loading and plugin execution.
func TestRunCommand_Integration(t *testing.T) {
	t.Skip("Run command requires full system setup - tested in integration tests")
}

// The resolveSelfExecutable decision tree (deleted-suffix stripping, PATH
// fallback) is owned and tested by internal/adapters/selfexec.

// TestEndOnSignal_TheNoticeLandsBeforeTheRunEnds: a shutdown signal ends the
// run, and says so through the diagnostics sink BEFORE the run's context ends —
// the unwind that context starts is what takes the session's diagnostics log
// away, so a notice written after it would land on a terminal that may already
// be gone (a hangup is exactly that case).
func TestEndOnSignal_TheNoticeLandsBeforeTheRunEnds(t *testing.T) {
	sigs := make(chan os.Signal, 1)
	ctx, stop := endOnSignal(context.Background(), sigs)
	defer stop()
	sink := &runStateAtWrite{ctx: ctx}
	restore := clidiag.SetSink(sink)

	sigs <- syscall.SIGHUP
	<-ctx.Done()
	restore()
	assert.Contains(t, sink.diag.String(), fmt.Sprintf(ShutdownSignalNotice, syscall.SIGHUP))
	assert.False(t, sink.endedFirst, "the run ended before its shutdown notice was written")
}

// runStateAtWrite is a diagnostics sink that records whether ctx had already
// ended when anything was written to it.
type runStateAtWrite struct {
	ctx        context.Context
	diag       bytes.Buffer
	endedFirst bool
}

func (w *runStateAtWrite) Write(p []byte) (int, error) {
	w.endedFirst = w.endedFirst || w.ctx.Err() != nil
	return w.diag.Write(p)
}

// TestEndOnSignal_StopEndsTheRunWithoutANotice: a run that ends on its own
// says nothing about a signal it never received.
func TestEndOnSignal_StopEndsTheRunWithoutANotice(t *testing.T) {
	var diag bytes.Buffer
	restore := clidiag.SetSink(&diag)
	ctx, stop := endOnSignal(context.Background(), make(chan os.Signal))
	stop()
	<-ctx.Done()
	restore()
	assert.Empty(t, diag.String())
}

// TestAwaitDrain_ASignalDuringTheDrainCutsItShort: the session-exit drain is
// bounded in minutes, so a shutdown signal sent while it waits must end the
// wait instead of being swallowed until the drain settles on its own.
func TestAwaitDrain_ASignalDuringTheDrainCutsItShort(t *testing.T) {
	never := make(chan struct{})
	interrupt := make(chan os.Signal, 1)
	interrupt <- syscall.SIGTERM
	assert.False(t, awaitDrain(never, interrupt), "a signal must end the wait on a drain that has not settled")

	settled := make(chan struct{})
	close(settled)
	assert.True(t, awaitDrain(settled, make(chan os.Signal)), "a settled drain reports settled")
}
