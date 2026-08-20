package backends

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// A builtin slash command that failed to load used to be dropped with
// `continue` and zero diagnostics. Because the command writers reconcile by
// removing every ctxloom-managed file then re-adding the assembled set, a
// silently dropped builtin simply vanishes from the next materialize with no
// signal at all. It must now warn.
func TestBuiltinCommands_LoadFailureIsWarned(t *testing.T) {
	orig := getBuiltinCommandFn
	getBuiltinCommandFn = func(name string) ([]byte, error) {
		return nil, fmt.Errorf("embedded read exploded")
	}
	defer func() { getBuiltinCommandFn = orig }()

	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	prompts := builtinCommands()
	assert.Empty(t, prompts, "every builtin failed to load, so nothing is returned")
	assert.Contains(t, buf.String(), "unavailable", "a failed builtin command load must be warned, not silently dropped")
}

// TestBuiltinCommands_RealResourcesLoad is a smoke test that the real
// embedded resources still load through the seam unchanged (guards against
// the seam accidentally becoming the only path exercised by tests).
func TestBuiltinCommands_RealResourcesLoad(t *testing.T) {
	prompts := builtinCommands()
	require.NotEmpty(t, prompts, "the real embedded builtin commands must still load")
}

// forceExport must force-enable a curated command for EVERY engine with a
// per-prompt opt-out flag, not just claude/antigravity/codex. Before the
// fix, a bundle that set `kiro: {enabled: false}` (or opencode) on a prompt
// a profile explicitly curates via `commands:` still exported nothing for
// that engine — contradicting forceExport's own doc ("the per-prompt
// opt-out flag is overridden"). forceExportSkill (skillfiles.go) already
// does this correctly for all five engines; forceExport is its command-side
// twin and must match.
func TestForceExport_EnablesEveryEngine(t *testing.T) {
	off := false
	c := &bundles.LoadedContent{Name: "x", Content: "body"}
	c.LLM.Kiro.Enabled = &off
	c.LLM.Opencode.Enabled = &off

	forceExport(c)

	assert.True(t, c.LLM.Kiro.IsEnabled(), "forceExport must override kiro's opt-out, like every other engine")
	assert.True(t, c.LLM.Opencode.IsEnabled(), "forceExport must override opencode's opt-out, like every other engine")
}

// LoadCommandExports(nil, ...) must not panic. Before the fix, it
// nil-checked cfg twice (once to skip the trust-gate wiring, once
// redundantly before resolveProfilePromptRefs, which itself already returns
// nil for a nil cfg) but then dereferenced cfg unguarded at the final
// cfg.ResolveBundleCommands(...) call — contrast LoadSkillExports, its
// sibling, which returns nil cleanly for a nil cfg.
func TestLoadCommandExports_NilConfigDoesNotPanic(t *testing.T) {
	require.NotPanics(t, func() {
		LoadCommandExports(nil, nil)
	})
}

// An explicitly-selected (non-default) profile that fails to resolve must
// not be warned as a "default profile" — mirrors the managed.go regression
// tests for the same wording bug.
func TestResolveProfilePromptRefs_ExplicitProfileWarningOmitsDefault(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{})

	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	resolveProfilePromptRefs(cfg, []string{"explicitly-selected-and-missing"})

	assert.NotContains(t, buf.String(), "default profile",
		"an explicitly-selected profile must not be misreported as a default: got %q", buf.String())
}

// resolveProfilePromptRefs must diagnose a BROKEN inline profile (circular
// parent inheritance) instead of silently retrying it as a directory
// profile, whose own unrelated not-found error then masks the real cause.
// Mirrors the managed.go regression tests for the same defect.
func TestResolveProfilePromptRefs_CircularProfileIsWarnedNotMasked(t *testing.T) {
	// A profile that parents itself. The claim is unchanged by the inline arm's
	// retirement: the REAL cause (inheritance) must reach the warning, rather
	// than being swallowed and reported as some unrelated not-found.
	cfg := dirProfileCfg(t, []string{"loopy"}, map[string]string{
		"loopy": "parents:\n  - loopy\n",
	})

	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	resolveProfilePromptRefs(cfg, nil)

	assert.Contains(t, buf.String(), "inheritance",
		"the real cause (inheritance) must reach the warning, not the directory loader's unrelated not-found error: got %q", buf.String())
}
