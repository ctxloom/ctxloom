package operations

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/agents"
	"github.com/ctxloom/ctxloom/internal/adapters/companions"
	"github.com/ctxloom/ctxloom/internal/adapters/signing"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// ---- the PUSH path's builtin/companion injection ----
//
// There are two accumulation points for one context, and they are
// near-duplicates that drifted: AssembleContext -> ingestBuiltinFragments is
// the PULL path, and regenerateContext -> agent.WriteContextFile is the PUSH
// path (the SessionStart-injected context file). The premise filter was added
// to the pull path's builtin loop and NOT to the push path's, so a premised
// companion fragment was withheld from what `ctxloom run` assembles while the
// context file on disk carried the whole body anyway.
//
// That is the ONE coupling constraint between the two layers violated: pull
// and push compose freely except that a fragment must not be delivered TWICE.
// A premised companion was pushed at SessionStart AND offered again through
// the premise index.
//
// TestBuiltinFragmentsHonourTheirPremise (builtin_premise_test.go) already
// names the exact same companion ref and passes REGARDLESS of this bug,
// because it only ever reaches the pull path. Nothing here may lean on it.
// These tests assert on the BYTES WRITTEN TO THE CONTEXT FILE, which is the
// only artifact that can prove the push path withheld anything.

const (
	pushPremisedBody   = "PUSH-PREMISED-COMPANION-BODY"
	pushUnpremisedBody = "PUSH-UNPREMISED-COMPANION-BODY"
	pushPremise        = "You are about to create, read or close a task."
)

// pushPremiseCompanion stands a fake `taskloom` companion on PATH whose
// loadout ships two fragments: one carrying a premise and one carrying none.
// The unpremised one is the CONTROL — it proves the companion route reached
// the context file at all, so a withheld premised body cannot be confused
// with the whole companion loadout being gated off (which would make the
// withholding assertion vacuously true).
func pushPremiseCompanion(t *testing.T) {
	t.Helper()
	t.Cleanup(companions.AdmitEveryDiscoveredCompanionForTesting())

	envelope, err := signing.EncodeLoadoutEnvelope([]byte(
		"version: \"1.0.0\"\nfragments:\n"+
			"  taskloom:\n"+
			"    premise: \""+pushPremise+"\"\n"+
			"    content: |\n      "+pushPremisedBody+"\n"+
			"  taskloom-always:\n"+
			"    content: |\n      "+pushUnpremisedBody+"\n"), nil, "")
	require.NoError(t, err)

	t.Cleanup(companions.SetLookPathForTesting(func(bin string) (string, error) {
		if bin == "taskloom" {
			return "/fake/taskloom", nil
		}
		return "", exec.ErrNotFound
	}))
	t.Cleanup(companions.SetCompanionLoadoutOutputForTesting(func(path string) ([]byte, error) {
		if path == "/fake/taskloom" {
			return envelope, nil
		}
		return nil, exec.ErrNotFound
	}))
}

// pushPremiseConfig mirrors cfgWithDirProfiles but deliberately does NOT call
// DisableCompanionProbe: the companion loadout is the only production route to
// a premised builtin fragment (no embedded builtin bundle authors a premise),
// so a config with the probe disabled cannot exercise this defect at all.
func pushPremiseConfig(t *testing.T, appDir string, defs map[string]config.Profile) *config.Config {
	t.Helper()
	fs := afero.NewOsFs()
	seed := make(map[string]any, len(defs))
	for name, p := range defs {
		seed[name] = p
	}
	testsupport.WriteDirProfiles(t, fs, appDir, seed)

	cfg := gatedFixture(config.Fixture{
		AppPaths:     []string{appDir},
		DefaultAgent: "default",
		Agents:       map[string]agents.Agent{"default": {Profiles: []string{"default"}}},
	})
	cfg.SetFS(fs)
	return cfg
}

// regenerateForPushPremise runs the real push path and returns the bytes that
// landed in the context file — what a session actually loads at SessionStart.
func regenerateForPushPremise(t *testing.T) string {
	t.Helper()
	appDir, workDir := regenTestApp(t)
	writeRegenBundle(t, appDir, "dev", `version: "1.0"
fragments:
  loader-fragment:
    content: "LOADER-SELECTED-BODY"
`)
	cfg := pushPremiseConfig(t, appDir, map[string]config.Profile{
		"default": {Fragments: []config.FragmentRef{{Name: "dev#fragments/loader-fragment"}}},
	})

	hash, err := regenerateContext(published(t, cfg), workDir)
	require.NoError(t, err)
	require.NotEmpty(t, hash, "the push path must actually write a context file")

	written, err := agent.ReadContextFile(workDir, hash)
	require.NoError(t, err)
	return written
}

// TestRegenerateContext_WithholdsPremisedBuiltinFromTheContextFile is the test
// that was missing, and whose absence is why this shipped. It fails on the
// unfixed push path: the premised companion body is written into the
// SessionStart context file verbatim.
func TestRegenerateContext_WithholdsPremisedBuiltinFromTheContextFile(t *testing.T) {
	pushPremiseCompanion(t)
	written := regenerateForPushPremise(t)

	// CONTROLS FIRST. Each of these failing means the test stopped being able
	// to observe the defect, not that the defect is fixed.
	require.Contains(t, written, "LOADER-SELECTED-BODY",
		"control: the loader-resolved fragment must reach the context file, or regeneration wrote nothing meaningful")
	require.Contains(t, written, pushUnpremisedBody,
		"control: a companion fragment with NO premise must still be injected — without this the withholding assertion below is vacuous, since a fully gated-off companion loadout would also 'withhold' the premised body")

	// THE ASSERTION.
	assert.NotContains(t, written, pushPremisedBody,
		"a premised companion fragment must be WITHHELD from the pushed context file: the pull layer offers it through the premise index, and delivering it here too is the one thing the two layers must never do — deliver the same fragment twice")
}

// TestRegenerateContext_PremiselessBuiltinsStayUnconditional pins the other
// half of the rule, which is what keeps the fix additive: absence of a premise
// asserts the fragment always applies, so every existing builtin and companion
// fragment must be unaffected. A filter that withheld these would strip the
// SessionStart context of content no one made conditional.
func TestRegenerateContext_PremiselessBuiltinsStayUnconditional(t *testing.T) {
	pushPremiseCompanion(t)
	written := regenerateForPushPremise(t)

	assert.Contains(t, written, pushUnpremisedBody,
		"a companion fragment carrying no premise is unconditional and must still be pushed")
	assert.Equal(t, 1, strings.Count(written, pushUnpremisedBody),
		"and exactly once — withholding must not disturb the ingest identity rule")
}

// TestRegenerateContext_PushAndPullAgreeOnThePremisedFragment states the
// coupling constraint directly, across BOTH implementations. The two paths are
// near-duplicates; this is the invariant that was silently allowed to drift
// when only one of them was fixed. It compares the push path's file against
// the pull path's filter decision for the same fragment, so a future
// divergence in either one fails here even if neither path's own tests notice.
func TestRegenerateContext_PushAndPullAgreeOnThePremisedFragment(t *testing.T) {
	pushPremiseCompanion(t)
	written := regenerateForPushPremise(t)

	// The pull layer's decision for this exact fragment.
	pull := newPremiseFilter(nil)
	withheldByPull := pull.withhold("ctxloom+companion:taskloom#fragments/taskloom", pushPremise,
		func() string { return pushPremisedBody })
	require.True(t, withheldByPull, "precondition: the pull layer withholds a premised fragment")

	pushedIt := strings.Contains(written, pushPremisedBody)
	assert.False(t, pushedIt,
		"push and pull must agree: the pull layer withheld this fragment and offered it in the premise index, so the push path delivering it means the agent gets the same bytes twice")
}

// pushPremiseTestFilesExist guards the harness itself: regenTestApp writes to a
// real temp dir, and a silently-missing app dir would make every assertion
// above run against an empty assembly.
func TestRegenerateContext_PushPremiseHarnessWritesRealFiles(t *testing.T) {
	appDir, _ := regenTestApp(t)
	info, err := os.Stat(authoredV1(appDir))
	require.NoError(t, err)
	require.True(t, info.IsDir(), "the regen harness must create a real authored bundles dir")
}
