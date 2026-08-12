package mcp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agentcoord/mcpschema"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/memory"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// withDistillBudget is the one place the host side of the relay's budget
// contract is applied. DistillBudget's own doc states the invariant: it
// "bounds BOTH sides of the relay: how long the caller waits, and how long
// the host lets the work run — one number, so the two can't drift into a host
// that outlives its caller's patience by design."
//
// A caller that already carries a deadline keeps it: the relay grants the
// budget on the request, and re-bounding would extend a caller who asked for
// less.
func TestWithDistillBudget(t *testing.T) {
	t.Run("a deadline-less context gains the distill budget", func(t *testing.T) {
		ctx, cancel := withDistillBudget(context.Background())
		defer cancel()

		dl, ok := ctx.Deadline()
		require.True(t, ok, "an unbounded host context must not be handed to minutes-long LLM work")
		assert.InDelta(t, mcpschema.DistillBudget.Seconds(), time.Until(dl).Seconds(), 60,
			"the host's bound must be the relay's budget, not some other number")
	})

	t.Run("an already-bounded context is left alone", func(t *testing.T) {
		caller, callerCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer callerCancel()

		ctx, cancel := withDistillBudget(caller)
		defer cancel()

		dl, ok := ctx.Deadline()
		require.True(t, ok)
		assert.Less(t, time.Until(dl), time.Minute,
			"a caller that asked for less must not be extended to the full budget")
	})
}

// THE HOST MUST NOT RUN list_sessions{distill_missing:true} UNBOUNDED.
//
// On the host-relay path the handler runs on the coordinator's deadline-less
// base context: the caller's budget bounds only how long it WAITS, never the
// work. distillSession and previousSessionByHarp each re-bound that context to
// DistillBudget before spending LLM time; distillMissingForList did not, so a
// wedged LLM subprocess held the listing open forever — the exact drift
// DistillBudget's doc says the single number exists to prevent. list_sessions
// is in relayBudgets for precisely this reason.
func TestDistillMissingForList_BoundsTheWorkWhenTheHostContextIsUnbounded(t *testing.T) {
	testsupport.Isolate(t)
	mgr, err := sessions.Open("")
	require.NoError(t, err)

	proj := t.TempDir()
	// A harp with no essence on disk is exactly what distill_missing targets.
	e, err := mgr.AssignHarp(proj, "claude-code")
	require.NoError(t, err)

	var gotDeadline bool
	var budget time.Duration
	prev := compactEntryFn
	compactEntryFn = func(ctx context.Context, _ *sessions.Entry, _ *config.Config, _ string, _ io.Writer) (*memory.CompactionResult, error) {
		dl, ok := ctx.Deadline()
		gotDeadline = ok
		if ok {
			budget = time.Until(dl)
		}
		return nil, context.Canceled // the outcome is irrelevant; the deadline is the subject
	}
	defer func() { compactEntryFn = prev }()

	s := &ctxServer{cfg: config.NewFixture(config.Fixture{AppDir: filepath.Join(proj, ".ctxloom")})}
	entries := []sessions.Entry{{HarpName: e.HarpName, Backend: "claude-code"}}

	// Deadline-less, as the coordinator's base context is.
	s.distillMissingForList(context.Background(), entries)

	require.True(t, gotDeadline,
		"distill_missing spends LLM time; it must not inherit an unbounded host context")
	assert.InDelta(t, mcpschema.DistillBudget.Seconds(), budget.Seconds(), 60,
		"the bound must be the relay's DistillBudget")
}

// THE SECOND sessionEssenceInfo CALL IS THE POINT, NOT WASTE.
//
// With distill_missing=true, list_sessions probes each entry's essence twice:
// once in distillMissingForList to decide whether the entry needs compacting,
// and again when building the returned rows. Those two probes read DIFFERENT
// states — before and after the distillation — and the entry list is re-read
// between them for the same reason. Caching the first probe's answer and
// reusing it would report a session that was just distilled as Distilled:false
// and Title:"", i.e. list_sessions would deny having done the work it was
// asked to do.
func TestHandleListSessions_DistillMissingReportsThePostDistillState(t *testing.T) {
	testsupport.Isolate(t)
	mgr, err := sessions.Open("")
	require.NoError(t, err)

	proj := t.TempDir()
	e, err := mgr.AssignHarp(proj, "claude-code")
	require.NoError(t, err)

	// Stand in for a successful compaction: write the essence the real
	// compactor would have written, so the SECOND probe sees a distilled
	// session where the first saw none.
	prev := compactEntryFn
	compactEntryFn = func(_ context.Context, entry *sessions.Entry, _ *config.Config, _ string, _ io.Writer) (*memory.CompactionResult, error) {
		p, perr := paths.HarpEssencePath(entry.HarpName)
		require.NoError(t, perr)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte("# essence\n"), 0o644))
		require.NoError(t, mgr.SetSummary(entry.HarpName, "distilled just now", nil, 0))
		return &memory.CompactionResult{SessionID: entry.SessionID}, nil
	}
	defer func() { compactEntryFn = prev }()

	s := &ctxServer{cfg: config.NewFixture(config.Fixture{AppDir: filepath.Join(proj, ".ctxloom")})}
	_, out, err := s.handleListSessions(context.Background(), nil, listSessionsInput{AllProjects: true, DistillMissing: true})
	require.NoError(t, err)
	require.Len(t, out.Sessions, 1)
	require.Equal(t, e.HarpName, out.Sessions[0].Harp)

	assert.True(t, out.Sessions[0].Distilled,
		"a session distilled by this very call must be reported distilled; a cached pre-distill probe would say false")
	assert.Equal(t, "distilled just now", out.Sessions[0].Title,
		"the re-read exists so freshly-written summaries reach the returned rows")
}

// THE HOST-RELAY WARNINGS ARE REDIRECTABLE, NOT RAW STDERR.
//
// distillSession's doc says progress is deliberately NOT written to stderr:
// on the host-relay path it runs inside the session-owning process, whose
// stderr is the terminal the harness draws its TUI on. The per-entry failure
// warnings in this file satisfy that constraint by going through the clidiag
// SINK rather than os.Stderr — and under `ctxloom run` the session repoints
// that sink at its own diagnostics log for its whole lifetime
// (redirectDiagnosticsForTUI), so nothing lands on the TUI's terminal.
//
// This pins the property the constraint actually needs (refuted: the row
// read clidiag.Warn as a raw stderr write). Writing these warnings to
// os.Stderr directly — or to any writer clidiag does not own — would leave
// the buffer empty and fail here.
func TestDistillMissingForList_WarningsGoToTheRedirectableSinkNotStderr(t *testing.T) {
	testsupport.Isolate(t)
	mgr, err := sessions.Open("")
	require.NoError(t, err)

	proj := t.TempDir()
	e, err := mgr.AssignHarp(proj, "claude-code")
	require.NoError(t, err)

	prev := compactEntryFn
	compactEntryFn = func(context.Context, *sessions.Entry, *config.Config, string, io.Writer) (*memory.CompactionResult, error) {
		return nil, errors.New("legacy session needs a cwd-bound reader")
	}
	defer func() { compactEntryFn = prev }()

	var diagnostics bytes.Buffer
	restore := clidiag.SetSink(&diagnostics)
	defer restore()

	s := &ctxServer{cfg: config.NewFixture(config.Fixture{AppDir: filepath.Join(proj, ".ctxloom")})}
	s.distillMissingForList(context.Background(), []sessions.Entry{{HarpName: e.HarpName, Backend: "claude-code"}})

	assert.Contains(t, diagnostics.String(), e.HarpName,
		"a per-entry distill failure must be reported through the clidiag sink the session can redirect, never straight to the harness's terminal")
}
