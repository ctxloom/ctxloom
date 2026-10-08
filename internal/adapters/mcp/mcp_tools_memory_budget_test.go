package mcp

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/mcpschema"
	"github.com/ctxloom/ctxloom/internal/adapters/memory"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
)

// TestCompactEntryFn_IsBoundToTheRealCompactor pins the seam's DEFAULT
// binding. Every other test in this file substitutes compactEntryFn to observe
// how it is called, so all of them pass just as happily against a seam wired to
// a stub that compacts nothing — a no-op default is exactly the silent-no-op
// failure this project keeps hitting (success reported, zero bytes written),
// and it survived the whole package unnoticed until a mutation went looking.
// Identity, not behaviour, is the thing to pin: what the callers observe is
// covered elsewhere, what nothing covered is that production still reaches the
// real compactor.
func TestCompactEntryFn_IsBoundToTheRealCompactor(t *testing.T) {
	assert.Equal(t,
		reflect.ValueOf(operations.CompactEntry).Pointer(),
		reflect.ValueOf(compactEntryFn).Pointer(),
		"compactEntryFn must default to operations.CompactEntry — it is a test OBSERVATION seam, never a production substitution point")
}

// withCompactBudget is the one place the host side of the relay's budget
// contract is applied. CompactBudget's own doc states the invariant: it
// "bounds BOTH sides of the relay: how long the caller waits, and how long
// the host lets the work run — one number, so the two can't drift into a host
// that outlives its caller's patience by design."
//
// A caller that already carries a deadline keeps it: the relay grants the
// budget on the request, and re-bounding would extend a caller who asked for
// less.
func TestWithCompactBudget(t *testing.T) {
	t.Run("a deadline-less context gains the compact budget", func(t *testing.T) {
		ctx, cancel := withCompactBudget(context.Background())
		defer cancel()

		dl, ok := ctx.Deadline()
		require.True(t, ok, "an unbounded host context must not be handed to minutes-long LLM work")
		assert.InDelta(t, mcpschema.CompactBudget.Seconds(), time.Until(dl).Seconds(), 60,
			"the host's bound must be the relay's budget, not some other number")
	})

	t.Run("an already-bounded context is left alone", func(t *testing.T) {
		caller, callerCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer callerCancel()

		ctx, cancel := withCompactBudget(caller)
		defer cancel()

		dl, ok := ctx.Deadline()
		require.True(t, ok)
		assert.Less(t, time.Until(dl), time.Minute,
			"a caller that asked for less must not be extended to the full budget")
	})
}

// THE HOST MUST NOT RUN list_sessions{compact_missing:true} UNBOUNDED.
//
// On the host-relay path the handler runs on the coordinator's deadline-less
// base context: the caller's budget bounds only how long it WAITS, never the
// work. compactSession and previousSessionByHarp each re-bound that context to
// CompactBudget before spending LLM time; compactMissingForList did not, so a
// wedged LLM subprocess held the listing open forever — the exact drift
// CompactBudget's doc says the single number exists to prevent. list_sessions
// is in relayBudgets for precisely this reason.
func TestCompactMissingForList_BoundsTheWorkWhenTheHostContextIsUnbounded(t *testing.T) {
	testsupport.Isolate(t)
	mgr, err := sessions.Open(nil)
	require.NoError(t, err)

	proj := t.TempDir()
	// A harp with no essence on disk is exactly what compact_missing targets.
	e, err := mgr.AssignHarp(proj, "claude-code")
	require.NoError(t, err)
	_, err = mgr.RecordOutputDir(e.HarpName, t.TempDir())
	require.NoError(t, err)
	_, err = mgr.RecordOutputDir(e.HarpName, t.TempDir())
	require.NoError(t, err)

	var gotDeadline bool
	var budget time.Duration
	prev := compactEntryFn
	compactEntryFn = func(ctx context.Context, _ operations.LaunchFacts, _ *sessions.Entry, _ *config.Config, _ operations.CompactOptions) (*memory.CompactionResult, error) {
		dl, ok := ctx.Deadline()
		gotDeadline = ok
		if ok {
			budget = time.Until(dl)
		}
		return nil, context.Canceled // the outcome is irrelevant; the deadline is the subject
	}
	defer func() { compactEntryFn = prev }()

	s := &ctxServer{facts: testLaunchFacts(), cfg: config.NewFixture(config.Fixture{AppDir: filepath.Join(proj, ".ctxloom")})}
	entries := []sessions.Entry{{HarpName: e.HarpName, Backend: "claude-code"}}

	// Deadline-less, as the coordinator's base context is.
	s.compactMissingForList(context.Background(), entries)

	require.True(t, gotDeadline,
		"compact_missing spends LLM time; it must not inherit an unbounded host context")
	assert.InDelta(t, mcpschema.CompactBudget.Seconds(), budget.Seconds(), 60,
		"the bound must be the relay's CompactBudget")
}

// THE SECOND sessionEssenceInfo CALL IS THE POINT, NOT WASTE.
//
// With compact_missing=true, list_sessions probes each entry's essence twice:
// once in compactMissingForList to decide whether the entry needs compacting,
// and again when building the returned rows. Those two probes read DIFFERENT
// states — before and after the compaction — and the entry list is re-read
// between them for the same reason. Caching the first probe's answer and
// reusing it would report a session that was just compacted as Compacted:false
// and Title:"", i.e. list_sessions would deny having done the work it was
// asked to do.
func TestHandleListSessions_CompactMissingReportsThePostCompactState(t *testing.T) {
	testsupport.Isolate(t)
	mgr, err := sessions.Open(nil)
	require.NoError(t, err)

	proj := t.TempDir()
	e, err := mgr.AssignHarp(proj, "claude-code")
	require.NoError(t, err)
	_, err = mgr.RecordOutputDir(e.HarpName, t.TempDir())
	require.NoError(t, err)

	// Stand in for a successful compaction: write the essence the real
	// compactor would have written, so the SECOND probe sees a compacted
	// session where the first saw none.
	prev := compactEntryFn
	compactEntryFn = func(_ context.Context, _ operations.LaunchFacts, entry *sessions.Entry, _ *config.Config, _ operations.CompactOptions) (*memory.CompactionResult, error) {
		out, perr := sessions.OutputDir(entry.HarpName)
		require.NoError(t, perr)
		p := filepath.Join(out, paths.EssenceFileName)
		require.NoError(t, os.MkdirAll(out, 0o755))
		require.NoError(t, os.WriteFile(p, []byte("---\nsummary: compacted just now\n---\n# essence\n"), 0o644))
		return &memory.CompactionResult{SessionID: entry.SessionID}, nil
	}
	defer func() { compactEntryFn = prev }()

	s := &ctxServer{facts: testLaunchFacts(), cfg: config.NewFixture(config.Fixture{AppDir: filepath.Join(proj, ".ctxloom")})}
	_, out, err := s.handleListSessions(context.Background(), nil, listSessionsInput{AllProjects: true, CompactMissing: true})
	require.NoError(t, err)
	require.Len(t, out.Sessions, 1)
	require.Equal(t, e.HarpName, out.Sessions[0].Harp)

	assert.True(t, out.Sessions[0].Compacted,
		"a session compacted by this very call must be reported compacted; a cached pre-compaction probe would say false")
	assert.Equal(t, "compacted just now", out.Sessions[0].Title,
		"the re-read exists so freshly-written summaries reach the returned rows")
}

// THE HOST-RELAY WARNINGS ARE REDIRECTABLE, NOT RAW STDERR.
//
// compactSession's doc says progress is deliberately NOT written to stderr:
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
func TestCompactMissingForList_WarningsGoToTheRedirectableSinkNotStderr(t *testing.T) {
	testsupport.Isolate(t)
	mgr, err := sessions.Open(nil)
	require.NoError(t, err)

	proj := t.TempDir()
	e, err := mgr.AssignHarp(proj, "claude-code")
	require.NoError(t, err)
	_, err = mgr.RecordOutputDir(e.HarpName, t.TempDir())
	require.NoError(t, err)

	prev := compactEntryFn
	compactEntryFn = func(context.Context, operations.LaunchFacts, *sessions.Entry, *config.Config, operations.CompactOptions) (*memory.CompactionResult, error) {
		return nil, errors.New("legacy session needs a cwd-bound reader")
	}
	defer func() { compactEntryFn = prev }()

	var diagnostics bytes.Buffer
	restore := clidiag.SetSink(&diagnostics)
	defer restore()

	s := &ctxServer{facts: testLaunchFacts(), cfg: config.NewFixture(config.Fixture{AppDir: filepath.Join(proj, ".ctxloom")})}
	s.compactMissingForList(context.Background(), []sessions.Entry{{HarpName: e.HarpName, Backend: "claude-code"}})

	assert.Contains(t, diagnostics.String(), e.HarpName,
		"a per-entry compact failure must be reported through the clidiag sink the session can redirect, never straight to the harness's terminal")
}

// healFixture indexes a harp bound to the real claude vendor transcript, never
// converted: the engine's own store is the only record of the session.
func healFixture(t *testing.T) (harp, canonPath string) {
	t.Helper()
	testsupport.Isolate(t)
	mgr, err := sessions.Open(nil)
	require.NoError(t, err)
	e, err := mgr.AssignHarp(t.TempDir(), "claude-code")
	require.NoError(t, err)
	_, err = mgr.RecordOutputDir(e.HarpName, t.TempDir())
	require.NoError(t, err)
	require.NoError(t, mgr.BindSession(e.HarpName, "5b0e7a52-2f8d-4c1b-9e47-6a3d1c8f0b29", claudeVendorFixture()))
	// Adapter selection refuses an entry with no engine version, which would
	// convert nothing for a reason unrelated to the sweep.
	require.NoError(t, mgr.RecordEngineVersion(e.HarpName, "2.1.225"))
	canonPath, err = paths.HarpCanonicalTranscriptPath(e.HarpName)
	require.NoError(t, err)
	return e.HarpName, canonPath
}

// THE list_sessions SWEEP COMPACTS FROM A HEALED TRANSCRIPT.
//
// Every compaction path resolves its source through
// operations.ResolveAndHeal before compacting; a sweep that compacts the
// stored transcript as-is compacts whatever an earlier conversion left
// behind, or nothing at all for a session never converted. The assertion is
// on the canonical transcript present at the moment of compaction — the
// compactor is stubbed, so its output could not tell a healed source from a
// stale one.
func TestCompactMissingForList_HealsTheTranscriptBeforeCompacting(t *testing.T) {
	harp, canonPath := healFixture(t)

	var canonAtCompact []byte
	prev := compactEntryFn
	compactEntryFn = func(context.Context, operations.LaunchFacts, *sessions.Entry, *config.Config, operations.CompactOptions) (*memory.CompactionResult, error) {
		canonAtCompact, _ = os.ReadFile(canonPath)
		return &memory.CompactionResult{}, nil
	}
	defer func() { compactEntryFn = prev }()

	s := &ctxServer{facts: testLaunchFacts(), cfg: config.NewFixture(config.Fixture{AppDir: t.TempDir()})}
	s.compactMissingForList(context.Background(), []sessions.Entry{{HarpName: harp, Backend: "claude-code"}})

	assert.NotEmpty(t, canonAtCompact,
		"the sweep must refresh the engine's transcript into the canonical one before it compacts")
}

// THE HEAL IS STALE-GATED: an entry the sweep skips pays nothing for it.
func TestCompactMissingForList_SkippedEntryIsNotHealed(t *testing.T) {
	harp, canonPath := healFixture(t)
	out, err := sessions.OutputDir(harp)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(out, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(out, paths.EssenceFileName), []byte("---\nsummary: already compacted\n---\n# essence\n"), 0o644))

	prev := compactEntryFn
	compactEntryFn = func(context.Context, operations.LaunchFacts, *sessions.Entry, *config.Config, operations.CompactOptions) (*memory.CompactionResult, error) {
		t.Fatal("an entry with an essence and no known staleness is not compacted")
		return nil, nil
	}
	defer func() { compactEntryFn = prev }()

	s := &ctxServer{facts: testLaunchFacts(), cfg: config.NewFixture(config.Fixture{AppDir: t.TempDir()})}
	s.compactMissingForList(context.Background(), []sessions.Entry{{HarpName: harp, Backend: "claude-code"}})

	_, statErr := os.Stat(canonPath)
	assert.ErrorIs(t, statErr, os.ErrNotExist, "a sweep across an index must not pay the heal for rows it does not compact")
}
