package cli

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/errs"
)

// Reconcile is the one place in ctxloom where a READ decides a DELETION, so
// every test here is about the same question: what is allowed to count as
// "upstream no longer has this".
//
// THE FAILURE BEING DESIGNED OUT. An unreachable host, a revoked token and a
// paginated API that answers with an empty page all produce the identical local
// observation as a genuine deletion — nothing came back. A private repository
// reached with an expired credential does not even answer 401; forges answer
// 404, because telling you a repo exists is itself a disclosure. So "the
// content was not found" is, on its own, indistinguishable from "you have lost
// your right to look", and a reconcile that treats it as authority converts one
// expired token into an emptied installation, reported as a successful sync.
//
// The answer is that absence is authority only from a remote this run PROVED it
// could read. The reach probe is that proof, it runs per REPOSITORY, and a
// repository that fails it has none of its dependencies touched no matter what
// the per-item probe would have said. That ordering is the whole guard, and it
// is what these tests hold.

const (
	aliceDemo    = "https://github.com/alice/ctxloom@bundles/demo"
	aliceGuide   = "https://github.com/alice/ctxloom@bundles/guide"
	corpSecurity = "https://github.com/corp/ctxloom@bundles/security"
)

// probes builds a reconcile's two seams from lookup tables. A URL absent from
// unreachable is reachable; a ref absent from missing is served.
func probes(unreachable map[string]error, missing map[string]error) (reachProbe, contentProbe) {
	reach := func(_ context.Context, repoURL string) error { return unreachable[repoURL] }
	content := func(_ context.Context, ref string) error { return missing[ref] }
	return reach, content
}

func gone(t *testing.T, plan reconcilePlan) []string {
	t.Helper()
	return plan.Gone
}

// A reachable remote that genuinely no longer serves a bundle IS authority.
// Without this the guard would be trivially satisfiable by never removing
// anything, which is not synchronization.
func TestReconcile_AReachableRemoteThatDroppedABundleIsAuthority(t *testing.T) {
	reach, content := probes(nil, map[string]error{
		aliceDemo: fmt.Errorf("file not found: %w", errs.ErrRemoteContentNotFound),
	})

	plan := planReconcile(context.Background(), []string{aliceDemo, aliceGuide}, reach, content)

	assert.Equal(t, []string{aliceDemo}, gone(t, plan), "a reachable remote's absence is a deletion")
	assert.Empty(t, plan.Unreachable)
}

// THE LOAD-BEARING CASE. The repository cannot be read at all, and the item
// probe would report every single dependency missing — which is exactly what a
// revoked credential looks like. Nothing may be removed.
func TestReconcile_AnUnreachableRemoteIsNeverAuthority(t *testing.T) {
	reach, content := probes(
		map[string]error{"https://github.com/alice/ctxloom": errors.New("authentication failed")},
		map[string]error{
			aliceDemo:  fmt.Errorf("file not found: %w", errs.ErrRemoteContentNotFound),
			aliceGuide: fmt.Errorf("file not found: %w", errs.ErrRemoteContentNotFound),
		})

	plan := planReconcile(context.Background(), []string{aliceDemo, aliceGuide}, reach, content)

	assert.Empty(t, gone(t, plan),
		"a remote that could not be read removes NOTHING, whatever the per-item probe says")
	require.Len(t, plan.Unreachable, 1)
	assert.ElementsMatch(t, []string{aliceDemo, aliceGuide}, plan.Unreachable[0].Refs,
		"every dependency of the unreachable remote is reported as unchecked")
	assert.Contains(t, plan.Unreachable[0].Reason, "authentication failed",
		"the report carries why the remote could not be read; 'could not check' with no cause is unactionable")
}

// A reachability failure is scoped to its own repository. One dead remote must
// not freeze reconciliation of every other one, or a single stale entry in
// remotes.yaml would disable the mechanism entirely.
func TestReconcile_OneDeadRemoteDoesNotBlockTheOthers(t *testing.T) {
	reach, content := probes(
		map[string]error{"https://github.com/alice/ctxloom": errors.New("no route to host")},
		map[string]error{
			aliceDemo:    fmt.Errorf("file not found: %w", errs.ErrRemoteContentNotFound),
			corpSecurity: fmt.Errorf("file not found: %w", errs.ErrRemoteContentNotFound),
		})

	plan := planReconcile(context.Background(), []string{aliceDemo, corpSecurity}, reach, content)

	assert.Equal(t, []string{corpSecurity}, gone(t, plan))
	require.Len(t, plan.Unreachable, 1)
	assert.Equal(t, []string{aliceDemo}, plan.Unreachable[0].Refs)
}

// Reachable, but the item probe failed for a reason that is not "not found" —
// a transport hiccup, a rate limit, a malformed response. Not found is a fact
// about the repository; anything else is a fact about the attempt.
func TestReconcile_AnItemProbeThatMerelyFailedIsNotADeletion(t *testing.T) {
	reach, content := probes(nil, map[string]error{
		aliceDemo: errors.New("unexpected EOF"),
	})

	plan := planReconcile(context.Background(), []string{aliceDemo}, reach, content)

	assert.Empty(t, gone(t, plan), "only a not-found from a reachable remote is a deletion")
	require.Len(t, plan.Unreachable, 1)
	assert.Equal(t, []string{aliceDemo}, plan.Unreachable[0].Refs)
}

// The reach probe runs ONCE per repository, not once per dependency. A project
// with thirty bundles from one remote must not pay thirty round-trips, and —
// more importantly — must not be able to observe the remote as reachable for
// some of its bundles and unreachable for others within a single reconcile.
func TestReconcile_ReachIsProvedOncePerRepository(t *testing.T) {
	reached := map[string]int{}
	reach := func(_ context.Context, repoURL string) error { reached[repoURL]++; return nil }
	content := func(context.Context, string) error { return nil }

	planReconcile(context.Background(), []string{aliceDemo, aliceGuide, corpSecurity}, reach, content)

	assert.Equal(t, 1, reached["https://github.com/alice/ctxloom"])
	assert.Equal(t, 1, reached["https://github.com/corp/ctxloom"])
}

// A repository that failed the reach probe is never asked about its items. The
// per-item answers are known to be untrustworthy at that point, and asking
// anyway spends round-trips to collect evidence that must be discarded.
func TestReconcile_AnUnreachableRepositoryIsNeverProbedForItems(t *testing.T) {
	reach := func(context.Context, string) error { return errors.New("connection refused") }
	asked := 0
	content := func(context.Context, string) error { asked++; return nil }

	planReconcile(context.Background(), []string{aliceDemo, aliceGuide}, reach, content)

	assert.Zero(t, asked, "no item is probed against a repository that could not be read")
}

// A reference that cannot be parsed carries no repository to prove reachable,
// so there is nothing it could be authority about. It is reported, never
// removed: an unparseable entry is a lockfile the user needs to look at, not
// content to delete on their behalf.
func TestReconcile_AnUnparseableReferenceIsReportedNotRemoved(t *testing.T) {
	reach, content := probes(nil, nil)

	plan := planReconcile(context.Background(), []string{"::::not-a-reference"}, reach, content)

	assert.Empty(t, gone(t, plan))
	require.Len(t, plan.Unreachable, 1)
	assert.Equal(t, []string{"::::not-a-reference"}, plan.Unreachable[0].Refs)
}

// An empty closure is an empty plan, and specifically NOT a plan that removes
// everything — the degenerate shape of the bug this whole file guards.
func TestReconcile_AnEmptyClosureRemovesNothing(t *testing.T) {
	reach, content := probes(nil, nil)

	plan := planReconcile(context.Background(), nil, reach, content)

	assert.Empty(t, plan.Gone)
	assert.Empty(t, plan.Unreachable)
}

// renderReconcile must NAME what it removed. A pull that silently prunes is
// indistinguishable from a pull that found nothing to do, and the user only
// learns which by missing the content later.
func TestRenderReconcile_NamesEveryRemoval(t *testing.T) {
	var b testWriter

	renderReconcile(&b, reconcilePlan{Gone: []string{aliceDemo, corpSecurity}})

	assert.Contains(t, b.String(), "no longer published")
	assert.Contains(t, b.String(), aliceDemo, "a removal nobody can name is a removal nobody can undo")
	assert.Contains(t, b.String(), corpSecurity)
}

// The unchecked half has to be as loud as the removed half. "I could not reach
// this remote" is the sentence that stops a user concluding their installation
// was verified against upstream when it was not.
func TestRenderReconcile_SaysWhatItCouldNotCheck(t *testing.T) {
	var b testWriter

	renderReconcile(&b, reconcilePlan{Unreachable: []uncheckedRemote{{
		URL:    "https://github.com/alice/ctxloom",
		Refs:   []string{aliceDemo},
		Reason: "authentication failed",
	}}})
	out := b.String()

	assert.Contains(t, out, "could not be reached")
	assert.Contains(t, out, "https://github.com/alice/ctxloom")
	assert.Contains(t, out, "authentication failed")
	assert.NotContains(t, out, "no longer published",
		"nothing was found gone, so nothing may be described as gone")
}

// Silence when there is nothing to say: a pull against a healthy closure must
// not print a reconciliation section at all.
func TestRenderReconcile_AnEmptyPlanIsSilent(t *testing.T) {
	var b testWriter

	renderReconcile(&b, reconcilePlan{})

	assert.Empty(t, b.String())
}

// testWriter is a minimal io.Writer with a String(), so these tests do not
// depend on any fixture.
type testWriter struct{ b []byte }

func (w *testWriter) Write(p []byte) (int, error) { w.b = append(w.b, p...); return len(p), nil }
func (w *testWriter) String() string              { return string(w.b) }
