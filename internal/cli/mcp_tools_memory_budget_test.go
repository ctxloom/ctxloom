package cli

import (
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agentcoord/mcpschema"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/memory"
	"github.com/ctxloom/ctxloom/internal/sessions"
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
