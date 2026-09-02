// Untagged: the STRUCTURAL half of P1's adjudication, hermetic, no engine and
// no paid turn.
//
// WHY THIS FILE EXISTS. P1's first live pass produced two claims about delivery
// MECHANISMS, and both were inferred from what a model said rather than from
// what ctxloom wrote. That is exactly backwards for a probe whose whole subject
// is which mechanism carried the bytes: a model's answer is downstream of every
// channel at once, so it can never attribute one. S4's hook-firing probe found
// the same class of error from the other side (a cell answered correctly while
// its hook had provably never run — the engine had searched the workspace for
// the phrase), which is what forced this re-adjudication.
//
// The corrective is not a better live assertion. It is to pin the mechanisms
// where they are DECLARED — in each backend's SurfaceFor — by building the real
// surface set over an in-memory filesystem and looking at the bytes that land.
// A test here fails the day a backend changes what an approach delivers, which
// is the day a P1 cell would otherwise start quietly measuring something else.
//
// The finding below was established by reading production and is now held by
// it:
//
//   - claude's ApproachHook context delivery is a documented NO-OP. Pinning it
//     writes no context at all, which is why that cell reds — the route is not
//     broken, it is empty by declaration.
package acceptance

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// channelProbeHarp is a stand-in nonce for the structural tests. It never
// reaches an engine, so it does not go through the ledger — minting here would
// consume a cell key for a test that has no cell.
const channelProbeHarp = "probe-structural-harp"

// deliverContextUnder builds engine's REAL surface set over an in-memory
// filesystem, selects the context surface at approach, delivers it into a
// directory, and returns every file that landed with its content.
//
// The whole point is that nothing is simulated: this is backends.BuildSurfaces
// and the backend's own SurfaceFor, the same two calls the launch path makes.
func deliverContextUnder(t *testing.T, engine string, approach agent.Approach) map[string]string {
	t.Helper()
	fs := afero.NewMemMapFs()
	dir := "/work"
	require.NoError(t, fs.MkdirAll(dir, 0o755))

	set := backends.BuildSurfaces(engine, agent.SurfaceInputs{
		Context:   "The nonce for this session is " + channelProbeHarp,
		Fragments: []*agent.Fragment{{Name: "nonce", Content: "The nonce for this session is " + channelProbeHarp}},
	}, fs)
	require.NotNil(t, set, "%s must build a surface set, or this test compares against nothing", engine)

	delivery, err := set.SurfaceFor(agent.SurfaceContext, approach)
	require.NoError(t, err, "%s must resolve its context surface at %s — the P1 cell that pins it depends on this call succeeding", engine, approach)
	require.NotNil(t, delivery)

	_, err = delivery.Deliver(dir)
	require.NoError(t, err)

	out := map[string]string{}
	require.NoError(t, afero.Walk(fs, dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return err
		}
		b, rerr := afero.ReadFile(fs, path)
		if rerr != nil {
			return rerr
		}
		rel, _ := filepath.Rel(dir, path)
		out[rel] = string(b)
		return nil
	}))
	return out
}

// TestClaudeHookApproach_DeliversNothing pins the mechanism behind P1's one red.
//
// claude's SurfaceFor returns noopContextDelivery for (context, Hook) — a
// documented no-op, on the reasoning that claude's apply path carries context
// through the settings-borne inject hook plus a regenerated cache file, so the
// context surface has nothing extra to write. On a LAUNCH that reasoning does
// not hold: the context surface is the thing that would have written the cache
// file, and at this approach it writes nothing.
//
// So the red cell is not a broken route. It is a user-selectable approach that
// delivers zero bytes and reports success — this project's characteristic bug,
// sitting in the delivery layer, reachable from a config key.
func TestClaudeHookApproach_DeliversNothing(t *testing.T) {
	files := deliverContextUnder(t, "claude-code", agent.ApproachHook)
	require.Empty(t, files,
		"claude's context surface at ApproachHook is documented as a no-op. If it has started writing something, P1's red cell must be re-measured rather than assumed: got %v", keysOf(files))
}

// TestSharedCwdDelivery_OnlyClaudeSystemPromptStaysOutOfTheWorkspace is the
// structural basis for the side-channel judgement recorded against every P1
// cell.
//
// A workspace=none cell is a SHARED cell, and a shared delivery does not use the
// well-known write: deliverSet asks the backend for a SharedRealization first —
// the out-of-cwd conversion — and only falls back to the loud native write when
// there is none. So "does this cell's DELIVERY put nonce bytes where a
// workspace search can reach them" is answered here, per (engine, approach),
// and nowhere else.
//
// The answer is lopsided, and the asymmetry is exactly which of claude's cells
// can be argued side-channel-controlled:
//
//	claude  context/system-prompt -> HAS a realization (out-of-cwd scratch)
//	claude  context/unsafe-file   -> none: the caller asked for CLAUDE.md
//	claude  context/hook          -> none: it is a no-op anyway
//
// An engine with no realization writes its context INTO the working directory
// by construction. For a tool-using engine that is a channel, whatever the
// approach nominally is.
func TestSharedCwdDelivery_OnlyClaudeSystemPromptStaysOutOfTheWorkspace(t *testing.T) {
	fs := afero.NewMemMapFs()
	build := func(engine string) agent.SurfaceSet {
		return backends.BuildSurfaces(engine, agent.SurfaceInputs{Context: channelProbeHarp}, fs)
	}

	_, ok := build("claude-code").SharedRealization(agent.SurfaceContext, agent.ApproachSystemPrompt)
	require.True(t, ok,
		"claude's system-prompt approach must keep its out-of-cwd realization: it is the ONE context delivery in the ladder that puts no nonce bytes in the workspace, and P1's side-channel argument for that cell rests entirely on it")

	_, ok = build("claude-code").SharedRealization(agent.SurfaceContext, agent.ApproachUnsafeFile)
	require.False(t, ok,
		"unsafe-file is the caller's explicit request for the native in-workspace write; a realization here would silently convert it and make the two claude cells measure the same thing")
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
