//go:build integration

package integration

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/claude"
	"github.com/ctxloom/ctxloom/internal/codex"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// This file covers ctxloom's declared CONTEXT DELIVERY MATRIX — every
// (backend, agent.SurfaceKind, agent.Approach) triple a backend's
// agent.ApproachTable advertises — by PAYLOAD, never by exit code or by a
// "delivered" report.
//
// The matrix is DERIVED from the registered backends
// (backends.List + agent.SurfaceSet.SupportedApproaches), so a sixth backend or
// a newly declared approach is picked up automatically and fails the
// exhaustiveness assertion in TestDeliveryApproach_DeclaredPairsAreExhaustive
// until it is given an expected destination here.
//
// Why payload and not exit code: this codebase's characteristic bug is exit 0 +
// a success line + zero bytes (see agent.ContextWriter's ErrNoContext arm and
// the DeliverUnder "no argv sink at rest" refusal, both of which exist because
// silent no-ops shipped). Every assertion below names a SENTINEL string, the
// FILE it must reach, and — for the hook approach — the emitted hook JSON.

// matrixKinds is every surface kind the agent.SurfaceSelection builder can ask a
// backend about (agent's surfaceOrder), in delivery order.
var matrixKinds = []agent.SurfaceKind{
	agent.SurfaceContext,
	agent.SurfaceMCP,
	agent.SurfaceSettings,
	agent.SurfaceCommands,
	agent.SurfaceSkills,
}

// matrixApproaches is every declared agent.Approach value. Used for the NEGATIVE
// direction: the cross product minus the declared pairs must be refused loudly.
var matrixApproaches = []agent.Approach{
	agent.ApproachUnsafeFile,
	agent.ApproachSystemPrompt,
	agent.ApproachHook,
}

// sentinel slots. Each names one SurfaceInputs field, so an assertion can say
// WHICH input reached WHICH file rather than "the tree is non-empty".
const (
	slotContext  = "CTXSENTINEL-context"
	slotFragment = "CTXSENTINEL-fragment"
	slotMCP      = "CTXSENTINEL-mcpserver"
	slotMCPCmd   = "CTXSENTINEL-mcpcmd"
	slotHook     = "CTXSENTINEL-hookcmd"
	slotCommand  = "CTXSENTINEL-command"
	slotSkill    = "CTXSENTINEL-skill"
)

// matrixSentinelInputs is a fully populated agent.SurfaceInputs in which every
// field carries its own distinctive sentinel, so a delivery that writes the
// WRONG input into the right file is caught as surely as one that writes
// nothing.
func matrixSentinelInputs() agent.SurfaceInputs {
	return agent.SurfaceInputs{
		Context:   slotContext,
		Fragments: []*agent.Fragment{{Name: "sentinel-frag", Content: slotFragment}},
		MCP: &wire.MCPConfig{Servers: map[string]wire.MCPServer{
			slotMCP: {Command: slotMCPCmd},
		}},
		Hooks: &wire.HooksConfig{Unified: wire.UnifiedHooks{
			SessionStart: []wire.Hook{{Command: slotHook, Type: "command"}},
		}},
		Commands: []agent.CommandExport{
			{Name: "ctxsentinelcmd", Description: "sentinel command", Content: slotCommand, Enabled: true},
		},
		Skills: []agent.SkillExport{
			{Name: "ctxsentinelskill", Description: "sentinel skill", Enabled: true,
				Files: []agent.PackageFile{{RelPath: "SKILL.md", Content: []byte(slotSkill)}}},
		},
	}
}

// matrixTree snapshots every file under root as slash-relpath -> contents.
func matrixTree(t *testing.T, fs afero.Fs, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := afero.Walk(fs, root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info == nil || info.IsDir() {
			return nil //nolint:nilerr // a missing root means "nothing delivered", which the caller asserts on
		}
		b, readErr := afero.ReadFile(fs, path)
		if readErr != nil {
			return readErr
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		out[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	require.NoError(t, err)
	return out
}

// findSentinel reports the paths under tree whose contents contain sentinel.
func findSentinel(tree map[string]string, sentinel string) []string {
	var hits []string
	for p, c := range tree {
		if strings.Contains(c, sentinel) {
			hits = append(hits, p)
		}
	}
	sort.Strings(hits)
	return hits
}

// matrixBackends returns the registered backends that declare at least one
// surface kind — derived, never hard-coded, so a newly registered backend joins
// the matrix on its own.
func matrixBackends(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, name := range backends.List() {
		set := backends.BuildSurfaces(name, matrixSentinelInputs(), afero.NewMemMapFs())
		for _, k := range matrixKinds {
			if len(set.SupportedApproaches(k)) > 0 {
				out = append(out, name)
				break
			}
		}
	}
	sort.Strings(out)
	require.NotEmpty(t, out, "no registered backend declares any surface — the matrix would be vacuous")
	return out
}

// pairKey is the matrix coordinate: backend/kind/approach.
func pairKey(backend string, k agent.SurfaceKind, a agent.Approach) string {
	return backend + "/" + k.String() + "/" + a.String()
}

// derivedPairs enumerates every (backend, kind, approach) triple the registered
// backends DECLARE — the test matrix and the oracle.
func derivedPairs(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, name := range matrixBackends(t) {
		set := backends.BuildSurfaces(name, matrixSentinelInputs(), afero.NewMemMapFs())
		for _, k := range matrixKinds {
			for _, a := range set.SupportedApproaches(k) {
				out = append(out, pairKey(name, k, a))
			}
		}
	}
	sort.Strings(out)
	return out
}

// deliverySpec is the expected DESTINATION and PAYLOAD for one declared pair:
// what the approach promises, stated once, so the assertion is against the
// declaration rather than against whatever the code happens to do.
type deliverySpec struct {
	// wantFile is the relpath under the delivery root the approach's payload
	// must land in. Empty means the pair delivers NO file at this seam, which is
	// only ever accepted when noOp explains why.
	wantFile string
	// wantSlot is the sentinel that must appear inside wantFile.
	wantSlot string
	// alsoFile / alsoSlot pin a SECOND route the same pair delivers (codex's
	// hook approach writes both the cache file the hook reads and the native
	// AGENTS.md). alsoFile may end in "/*" to match one file in that directory.
	alsoFile, alsoSlot string
	// noOp records WHY a pair legitimately writes nothing at this seam, and
	// names the test that covers the real delivery instead. A pair with an empty
	// wantFile and an empty noOp is a test bug, asserted below.
	noOp string
	// disagreement records a DECLARED-vs-ACTUAL mismatch: the declaration
	// promises one destination and the code delivers another. The case is
	// SKIPPED (never quietly reconciled) so the disagreement stays visible.
	disagreement string
	// elsewhere records that this pair's promised payload is REAL and covered
	// — just not by this test's generic SurfaceFor(kind, approach).Deliver(root)
	// mechanism. claude's (context, system-prompt) is the one case: SurfaceFor
	// resolves the SAME dual-capable object unsafe-file does (its well-known
	// Deliver always writes the native file — that is what SurfaceFor decides,
	// not what approach was named), so the out-of-cwd scratch this approach
	// actually promises is reachable only through SharedRealization
	// (DeliverIsolated), which this generic loop never calls. Named test covers
	// the real payload with the right mechanism; unlike disagreement, this is
	// not an open mismatch to reconcile.
	elsewhere string
}

// codexHomeRel is the delivery-root-relative path to codex's CODEX_HOME —
// codex is the ONE engine whose config home ctxloom relocates out of the
// project root (internal/codex.StateHome, the uniform engine-home policy), so
// its home-keyed surfaces do not sit where every other engine's do. Asked of
// codex's own resolver with an empty root, because a literal here would be this
// file's private opinion about a location codex's writers own.
var codexHomeRel = codex.ProjectHome("")

// matrixSpecs is the expected destination for every DECLARED pair.
// TestDeliveryApproach_DeclaredPairsAreExhaustive holds these keys equal to the
// derived matrix, so a new pair cannot be added to a backend's ApproachTable
// without landing here first.
var matrixSpecs = map[string]deliverySpec{
	// ---- claude-code -------------------------------------------------------
	"claude-code/context/unsafe-file": {wantFile: "CLAUDE.md", wantSlot: slotContext},
	"claude-code/context/system-prompt": {
		elsewhere: "TestDeliveryApproach_ClaudeSystemPromptScratchPlacement — SharedRealization " +
			"is now keyed on (kind, approach) (U100-F05), so this pair's out-of-cwd " +
			"<hash>.sysprompt.md scratch is real; it is reached through SharedRealization " +
			"(DeliverIsolated), never through the generic SurfaceFor+Deliver(root) this " +
			"loop uses for every other cell.",
	},
	"claude-code/context/hook": {
		noOp: "claude's hook arm resolves to a documented no-op (a static CLAUDE.md " +
			"alongside the hook would double the context); the payload rides the " +
			"settings surface's SessionStart hook + the context cache file. Covered " +
			"end to end by TestDeliveryApproach_HookPayloadReachesInjectedContext.",
	},
	"claude-code/mcp/unsafe-file":      {wantFile: ".mcp.json", wantSlot: slotMCPCmd},
	"claude-code/settings/unsafe-file": {wantFile: ".claude/settings.json", wantSlot: slotHook},
	"claude-code/commands/unsafe-file": {wantFile: ".claude/commands/ctxsentinelcmd.md", wantSlot: slotCommand},
	"claude-code/skills/unsafe-file":   {wantFile: ".claude/skills/ctxsentinelskill/SKILL.md", wantSlot: slotSkill},

	// ---- codex -------------------------------------------------------------
	// codex declares TWO context approaches, and they are not two spellings of
	// one thing. Hook is the DEFAULT and delivers BOTH routes — the raw cache
	// file the SessionStart hook reads (keyed on Fragments) and the native
	// AGENTS.md (keyed on Context) — because materialize and launch each get a
	// single SurfaceFor call and both need what the other needs.
	"codex/context/hook": {
		wantFile: "AGENTS.md", wantSlot: slotContext,
		alsoFile: ".ctxloom/cache/context/*", alsoSlot: slotFragment,
	},
	// unsafe-file is the native file ALONE: the artifact codex reads by itself,
	// which is what makes it the one that outlives ctxloom being uninstalled.
	// Deliberately NO alsoFile — a cache file riding along would make this a
	// second spelling of hook, and would hand someone asking for a portable
	// document a file that means nothing without ctxloom.
	"codex/context/unsafe-file": {wantFile: "AGENTS.md", wantSlot: slotContext},
	// The three CODEX_HOME-keyed surfaces land under the home ctxloom
	// relocates, not at the project root beside AGENTS.md — see codexHomeRel.
	"codex/settings/unsafe-file": {wantFile: codexHomeRel + "/config.toml", wantSlot: slotHook},
	"codex/commands/unsafe-file": {wantFile: codexHomeRel + "/prompts/ctxsentinelcmd.md", wantSlot: slotCommand},
	"codex/skills/unsafe-file":   {wantFile: codexHomeRel + "/skills/ctxsentinelskill/SKILL.md", wantSlot: slotSkill},

	// ---- kiro --------------------------------------------------------------
	"kiro/context/unsafe-file":  {wantFile: ".kiro/steering/ctxloom-context.md", wantSlot: slotContext},
	"kiro/mcp/unsafe-file":      {wantFile: ".kiro/settings/mcp.json", wantSlot: slotMCPCmd},
	"kiro/settings/unsafe-file": {wantFile: ".kiro/agents/ctxloom.json", wantSlot: slotHook},
	"kiro/commands/unsafe-file": {wantFile: ".kiro/skills/ctxsentinelcmd/SKILL.md", wantSlot: slotCommand},
	"kiro/skills/unsafe-file":   {wantFile: ".kiro/skills/ctxsentinelskill/SKILL.md", wantSlot: slotSkill},

	// ---- opencode ----------------------------------------------------------
	// opencode's settings surface is asserted on the MCP payload, NOT the hook:
	// opencode has no hooks mechanism at all and drops the hook silently. That
	// omission is pinned by TestDeliveryApproach_OpencodeDropsHooksWithoutSaying.
	"opencode/context/unsafe-file":  {wantFile: ".opencode/ctxloom-context.md", wantSlot: slotContext},
	"opencode/settings/unsafe-file": {wantFile: "opencode.json", wantSlot: slotMCPCmd},
	"opencode/commands/unsafe-file": {wantFile: ".opencode/command/ctxsentinelcmd.md", wantSlot: slotCommand},
	"opencode/skills/unsafe-file":   {wantFile: ".opencode/skill/ctxsentinelskill/SKILL.md", wantSlot: slotSkill},

	// ---- mock ----------------------------------------------------------
	// mock declares context and skills (mock_surfaces.go's mockApproaches):
	// a hermetic, managed-marker MOCK_CONTEXT.md at the target dir root — the
	// same DeliverManagedContext shape claude's CLAUDE.md and codex's
	// AGENTS.md use — and a hermetic .mock/skills/ package tree through the
	// same agent.WriteManagedSkillPackages writer every real engine's skills
	// surface goes through. Its hook loss is declared via noHooksReason
	// (registry.go) the same way opencode's is, and pinned by
	// TestDeliveryApproach_HookCarriageMatchesDeclaration.
	"mock/context/unsafe-file": {wantFile: "MOCK_CONTEXT.md", wantSlot: slotContext},
	"mock/skills/unsafe-file":  {wantFile: ".mock/skills/ctxsentinelskill/SKILL.md", wantSlot: slotSkill},
}

// TestDeliveryApproach_DeclaredPairsAreExhaustive holds the DERIVED matrix equal
// to the specs above. It is the guard that makes every other test in this file
// a matrix test rather than a sample: a backend that declares a new
// (kind, approach) pair — or drops one — fails here until its expected
// destination is stated.
func TestDeliveryApproach_DeclaredPairsAreExhaustive(t *testing.T) {
	derived := derivedPairs(t)

	spec := make([]string, 0, len(matrixSpecs))
	for k := range matrixSpecs {
		spec = append(spec, k)
	}
	sort.Strings(spec)

	assert.Equal(t, spec, derived,
		"the declared (backend, surface, approach) matrix and the expected-destination table disagree; "+
			"a pair present in one and not the other is either an untested delivery approach or a stale expectation")

	for key, s := range matrixSpecs {
		if s.wantFile == "" {
			assert.True(t, s.noOp != "" || s.disagreement != "" || s.elsewhere != "",
				"%s expects NO delivered file via this loop's mechanism but records no noOp, "+
					"disagreement, or elsewhere reason — an unexplained zero-byte expectation is "+
					"exactly the silent-no-op shape these tests exist to catch", key)
		}
	}
}

// TestDeliveryApproach_DefaultIsFirstDeclared pins the other half of the
// declaration: agent.ApproachTable.Default is documented as the FIRST declared
// approach, and WithEverything (the materialize/launch selection) picks it. A
// backend whose default is not its first entry would silently materialize a
// different surface than its table advertises.
func TestDeliveryApproach_DefaultIsFirstDeclared(t *testing.T) {
	for _, name := range matrixBackends(t) {
		set := backends.BuildSurfaces(name, matrixSentinelInputs(), afero.NewMemMapFs())
		for _, k := range matrixKinds {
			supported := set.SupportedApproaches(k)
			def, ok := set.DefaultApproach(k)
			if len(supported) == 0 {
				assert.False(t, ok, "%s/%s declares no approach, so it must report no default", name, k)
				continue
			}
			require.True(t, ok, "%s/%s declares approaches but reports no default", name, k)
			assert.Equal(t, supported[0], def, "%s/%s: default must be the first declared approach", name, k)
		}
	}
}

// TestDeliveryApproach_EveryDeclaredPairDeliversItsPayload is the core matrix
// test: for every declared (backend, kind, approach) it resolves the concrete
// agent.Delivery via SurfaceSet.SurfaceFor, delivers it into a fresh root, and
// asserts the pair's SENTINEL landed in the file that approach promises.
//
// It deliberately does NOT assert on a returned error alone: agent.Delivery
// returns a nil error for a delivery that wrote nothing (that is the shared
// "nothing to write" convention), so an error-only assertion cannot tell
// delivered from silently-skipped.
func TestDeliveryApproach_EveryDeclaredPairDeliversItsPayload(t *testing.T) {
	for _, name := range matrixBackends(t) {
		probe := backends.BuildSurfaces(name, matrixSentinelInputs(), afero.NewMemMapFs())
		for _, k := range matrixKinds {
			for _, a := range probe.SupportedApproaches(k) {
				key := pairKey(name, k, a)
				t.Run(key, func(t *testing.T) {
					spec, ok := matrixSpecs[key]
					require.True(t, ok, "%s is declared but has no expected destination", key)
					if spec.disagreement != "" {
						t.Skipf("DECLARATION vs BEHAVIOUR disagreement (not reconciled here, reported instead): %s", spec.disagreement)
					}
					if spec.elsewhere != "" {
						t.Skipf("payload covered by a dedicated harness, not this loop's generic mechanism: %s", spec.elsewhere)
					}

					fs := afero.NewMemMapFs()
					root := "/cell"
					require.NoError(t, fs.MkdirAll(root, 0o755))

					set := backends.BuildSurfaces(name, matrixSentinelInputs(), fs)
					d, err := set.SurfaceFor(k, a)
					require.NoError(t, err, "%s: declared but SurfaceFor refused it", key)

					if spec.noOp != "" {
						if d != nil {
							_, derr := d.Deliver(root)
							require.NoError(t, derr)
						}
						assert.Empty(t, matrixTree(t, fs, root),
							"%s is recorded as a no-op at this seam (%s) but WROTE files — "+
								"the recorded reason is stale", key, spec.noOp)
						return
					}

					require.NotNil(t, d, "%s: declared pair resolved to a nil Delivery", key)
					_, derr := d.Deliver(root)
					require.NoError(t, derr, "%s: delivery failed", key)

					tree := matrixTree(t, fs, root)
					require.NotEmpty(t, tree, "%s: delivery reported success and wrote ZERO files", key)

					assertSentinelAt(t, key, tree, spec.wantFile, spec.wantSlot)
					if spec.alsoFile != "" {
						assertSentinelAt(t, key, tree, spec.alsoFile, spec.alsoSlot)
					}
				})
			}
		}
	}
}

// assertSentinelAt asserts sentinel is present in want (a relpath, or a
// "<dir>/*" glob matching exactly one file in that directory).
func assertSentinelAt(t *testing.T, key string, tree map[string]string, want, sentinel string) {
	t.Helper()
	if strings.HasSuffix(want, "/*") {
		dir := strings.TrimSuffix(want, "/*")
		var matched []string
		for p, c := range tree {
			if filepath.ToSlash(filepath.Dir(p)) == dir && strings.Contains(c, sentinel) {
				matched = append(matched, p)
			}
		}
		assert.NotEmpty(t, matched,
			"%s: no file under %s/ carries %s (tree: %v)", key, dir, sentinel, sortedTreePaths(tree))
		return
	}
	content, ok := tree[want]
	require.True(t, ok, "%s: the approach promises %s but it was not written (tree: %v)",
		key, want, sortedTreePaths(tree))
	assert.Contains(t, content, sentinel,
		"%s: %s exists but does not carry %s — the file was created without the payload", key, want, sentinel)
	assert.Equal(t, []string{want}, findSentinel(tree, sentinel),
		"%s: %s must reach exactly the promised destination and no other file", key, sentinel)
}

func sortedTreePaths(tree map[string]string) []string {
	out := make([]string, 0, len(tree))
	for p := range tree {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// TestDeliveryApproach_UndeclaredPairsAreRefusedLoudly covers the NEGATIVE
// direction across the whole cross product: every (backend, kind, approach)
// triple a backend does NOT declare must be REFUSED with an error naming the
// backend, the surface, and the approach — never accepted and silently skipped.
//
// The one deliberate exception is a kind a backend FOLDS or omits entirely
// (codex's and opencode's MCP, which rides their config surface): SurfaceFor
// still refuses it loudly, but agent.SurfaceSelection.Build treats selecting it
// as a permitted no-op. That asymmetry is documented, so it is pinned here
// rather than left to chance.
func TestDeliveryApproach_UndeclaredPairsAreRefusedLoudly(t *testing.T) {
	for _, name := range matrixBackends(t) {
		set := backends.BuildSurfaces(name, matrixSentinelInputs(), afero.NewMemMapFs())
		for _, k := range matrixKinds {
			supported := set.SupportedApproaches(k)
			for _, a := range matrixApproaches {
				if containsApproachValue(supported, a) {
					continue
				}
				t.Run("refuse/"+pairKey(name, k, a), func(t *testing.T) {
					d, err := set.SurfaceFor(k, a)
					require.Error(t, err,
						"%s: undeclared pair resolved to a surface instead of being refused", pairKey(name, k, a))
					assert.Nil(t, d, "a refused pair must not also hand back a Delivery")
					msg := err.Error()
					assert.Contains(t, msg, refusalLabel(name), "the refusal must name the backend")
					assert.Contains(t, msg, k.String(), "the refusal must name the surface kind")
					if len(supported) > 0 {
						// A kind the backend HAS, at an approach it does not: the
						// refusal must name the rejected approach so the user can
						// tell "wrong approach" from "no such surface".
						assert.Contains(t, msg, a.String(), "the refusal must name the rejected approach")
					}
				})
			}
		}
	}
}

// refusalLabel maps a REGISTERED backend name to the label that backend's own
// SurfaceFor refusal spells.
//
// FINDING (naming divergence, not fixed here): claude registers as "claude-code"
// but its Surfaces.SurfaceFor passes the literal "claude" to
// agent.ApproachTable.SurfaceFor, so its refusal reads "claude: no mcp surface
// via hook" for a backend the user names `claude-code` on the CLI. Every other
// backend's refusal label equals its registered name. Pinned rather than
// papered over: if the label is unified later, this map collapses to identity
// and the test still passes.
func refusalLabel(registered string) string {
	if registered == "claude-code" {
		return "claude"
	}
	return registered
}

func containsApproachValue(list []agent.Approach, a agent.Approach) bool {
	for _, x := range list {
		if x == a {
			return true
		}
	}
	return false
}

// TestDeliveryApproach_UndeclaredApproachFailsTheBuilder pins the same refusal
// one level up, at the seam callers actually use: naming an approach a backend
// does not support must fail agent.SurfaceSelection.Build with the SUPPORTED SET
// in the message, not deliver a different approach's file.
func TestDeliveryApproach_UndeclaredApproachFailsTheBuilder(t *testing.T) {
	// kiro/opencode declare context as unsafe-file ONLY, so both the
	// hook and the system-prompt approaches must be refused by the builder.
	for _, name := range []string{"kiro", "opencode"} {
		for _, w := range []struct {
			label string
			write agent.ContextWrite
		}{
			{"hook", agent.ContextWriteHook},
			{"system-prompt", agent.ContextWriteSystemPrompt},
		} {
			t.Run(name+"/context/"+w.label, func(t *testing.T) {
				fs := afero.NewMemMapFs()
				set := backends.BuildSurfaces(name, matrixSentinelInputs(), fs)
				sel := agent.Select(set).
					WithContext(w.write).
					WithSettings(agent.SettingsWriteUnsafeFile)
				resolved, err := sel.Build()
				require.Error(t, err, "%s must refuse context via %s", name, w.label)
				assert.Nil(t, resolved)
				assert.Contains(t, err.Error(), "not supported")
				assert.Contains(t, err.Error(), "unsafe-file",
					"the refusal must name the SUPPORTED set so the caller can correct the selection")

				// The refusal must be total: nothing at all is delivered.
				_, kinds, errs := sel.DeliverUnder("/cell")
				assert.Empty(t, kinds, "a refused selection must deliver no surface")
				assert.NotEmpty(t, errs)
				assert.Empty(t, matrixTree(t, fs, "/cell"), "a refused selection must write zero files")
			})
		}
	}
}

// TestDeliveryApproach_SharedRealizationIsApproachKeyed_U100F05 is the
// RESOLUTION of what was
// TestDeliveryApproach_SystemPromptScratchIsKindKeyedNotApproachKeyed — the
// disagreement this matrix used to surface as an executable fact.
// SharedRealization is now keyed on the (kind, approach) PAIR, not the surface
// kind alone, so:
//
//   - selecting unsafe-file and delivering into a SHARED cwd now writes the
//     NATIVE CLAUDE.md the caller actually asked for — honored, with the
//     existing "unsafe: ... races concurrent agents" warning (the
//     honor-with-warning fork DECIDED for U100-F05) — never silently upgraded
//     to the scratch the caller did not name.
//   - selecting system-prompt and delivering at rest is still refused outright
//     (DeliverUnder has no argv sink; unaffected by this fix) — and the raw
//     Delivery SurfaceFor hands back for that pair still writes the native
//     file, which is a SEPARATE, structural fact about SurfaceFor (it resolves
//     the SAME dual-capable object every context approach shares) rather than
//     a SharedRealization defect. The scratch this pair actually promises is
//     covered by TestDeliveryApproach_ClaudeSystemPromptScratchPlacement,
//     which calls SharedRealization directly rather than the raw Delivery.
func TestDeliveryApproach_SharedRealizationIsApproachKeyed_U100F05(t *testing.T) {
	scratch := "/out-of-cwd/run-1"

	t.Run("unsafe-file into a shared cwd honors CLAUDE.md, not the sysprompt scratch", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		require.NoError(t, fs.MkdirAll(scratch, 0o755))
		surfaces := claude.NewSurfaces(matrixSentinelInputs(), scratchPlacement{dir: scratch}, fs)

		resolved, err := agent.Select(surfaces).WithContext(agent.ContextWriteUnsafeFile).Build()
		require.NoError(t, err)
		_, kinds, errs := resolved.DeliverShared("/live-cwd")
		require.Empty(t, errs)
		require.Equal(t, []agent.SurfaceKind{agent.SurfaceContext}, kinds)

		// The caller's explicit native-file request is honored: CLAUDE.md lands
		// in the shared cwd, carrying the sentinel...
		liveTree := matrixTree(t, fs, "/live-cwd")
		assert.Equal(t, []string{"CLAUDE.md"}, findSentinel(liveTree, slotContext),
			"unsafe-file must be HONORED, not silently converted to the scratch")

		// ...and the out-of-cwd scratch is never touched — no silent upgrade.
		assert.Empty(t, matrixTree(t, fs, scratch),
			"unsafe-file must not be silently upgraded to the system-prompt scratch")
		assert.Empty(t, surfaces.Context.Path(),
			"DeliverIsolated never ran, so Path() (the --append-system-prompt-file argument) stays empty")
	})

	t.Run("system-prompt at rest never produces a sysprompt file", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		require.NoError(t, fs.MkdirAll(scratch, 0o755))
		surfaces := claude.NewSurfaces(matrixSentinelInputs(), scratchPlacement{dir: scratch}, fs)

		// (a) Through the sanctioned at-rest terminal: an honest, loud refusal —
		// unaffected by this fix (DeliverUnder never consults SharedRealization).
		resolved, err := agent.Select(surfaces).WithContext(agent.ContextWriteSystemPrompt).Build()
		require.NoError(t, err, "the approach IS declared, so Build must accept it")
		_, kinds, errs := resolved.DeliverUnder("/cell")
		assert.Empty(t, kinds)
		require.Len(t, errs, 1)
		assert.Contains(t, errs[0].Error(), "no argv sink at rest",
			"at-rest system-prompt delivery must be refused, naming the missing argv sink")
		assert.Empty(t, matrixTree(t, fs, "/cell"), "a refused delivery must write zero files")

		// (b) Through the raw Delivery SurfaceFor hands back: it writes the
		// NATIVE file — a structural fact about SurfaceFor resolving the SAME
		// dual-capable object unsafe-file resolves to (SurfaceFor decides WHICH
		// object answers a pair, not what its well-known Deliver does). This is
		// not the U100-F05 defect: the real out-of-cwd destination this pair
		// promises is reached only through SharedRealization (DeliverIsolated),
		// which this raw call never invokes.
		d, err := surfaces.SurfaceFor(agent.SurfaceContext, agent.ApproachSystemPrompt)
		require.NoError(t, err)
		_, err = d.Deliver("/raw")
		require.NoError(t, err)
		rawTree := matrixTree(t, fs, "/raw")
		assert.Equal(t, []string{"CLAUDE.md"}, findSentinel(rawTree, slotContext))
		for p := range rawTree {
			assert.False(t, strings.HasSuffix(p, agent.SCMFramedContextSuffix),
				"no sysprompt file is produced by the raw Delivery — only SharedRealization writes it (%s)", p)
		}
	})
}

// TestDeliveryApproach_ClaudeSystemPromptScratchPlacement is the
// scratch-placement-aware variant matrixSpecs' "elsewhere" entry for
// claude-code/context/system-prompt promises (U100-F05 un-skip). Unlike every
// other matrix cell, this pair's payload is not reached through
// SurfaceFor(kind, approach).Deliver(root) — that resolves to the SAME
// dual-capable contextSurface every context approach shares, whose well-known
// Deliver always writes CLAUDE.md regardless of which approach was named (see
// TestDeliveryApproach_SharedRealizationIsApproachKeyed_U100F05's second
// half). The out-of-cwd scratch this approach promises is reached through
// SharedRealization(context, system-prompt) — the exact call
// agent.ResolvedSelection.deliverOneShared makes for a real shared-cwd
// launch — which the pair-keyed re-key (U100-F05) makes trustworthy to assert
// on here: it fires ONLY for this pair, never for unsafe-file.
func TestDeliveryApproach_ClaudeSystemPromptScratchPlacement(t *testing.T) {
	fs := afero.NewMemMapFs()
	root := "/cell"
	scratch := "/isolated"
	require.NoError(t, fs.MkdirAll(root, 0o755))
	require.NoError(t, fs.MkdirAll(scratch, 0o755))

	set := claude.NewSurfaces(matrixSentinelInputs(), scratchPlacement{dir: scratch}, fs)

	realize, ok := set.SharedRealization(agent.SurfaceContext, agent.ApproachSystemPrompt)
	require.True(t, ok, "claude-code/context/system-prompt must realize")
	require.NotNil(t, realize)

	handle, err := realize()
	require.NoError(t, err)
	require.NotNil(t, handle)

	// root — the shared cwd this cell's launch-flag scratch exists to spare —
	// stays completely empty.
	assert.Empty(t, matrixTree(t, fs, root), "system-prompt must not touch the shared cwd")

	scratchTree := matrixTree(t, fs, scratch)
	hits := findSentinel(scratchTree, slotContext)
	require.Len(t, hits, 1,
		"the system-prompt scratch file must carry the context sentinel (scratch tree: %v)",
		sortedTreePaths(scratchTree))
	assert.True(t, strings.HasSuffix(hits[0], agent.SCMFramedContextSuffix),
		"the framed file must be named <hash>%s, got %s", agent.SCMFramedContextSuffix, hits[0])
	assert.Contains(t, scratchTree[hits[0]], agent.FrameProjectContext(slotContext),
		"the scratch file must carry the FRAMED envelope, not the bare context")

	assert.Equal(t, filepath.Join(scratch, hits[0]), set.Context.Path(),
		"Context.Path() (the --append-system-prompt-file argument) must name the written file")
}

// scratchPlacement is the out-of-cwd agent.Placement claude's race-safe surfaces
// write into — the launch path's per-run scratch dir, modelled here so the
// system-prompt destination is a real directory instead of the placeholder
// backends.BuildSurfaces binds for the well-known path.
type scratchPlacement struct{ dir string }

func (p scratchPlacement) Dir() string { return p.dir }

// TestDeliveryApproach_OpencodeSaysWhatItCannotCarry covers the one
// structurally-unsupported surface in the matrix: opencode has no hooks
// mechanism at all, so a profile's session_start hook is dropped. The delivery
// report itself CANNOT show that — it lists what landed, and every one of its
// lines is true — so the loss is read from the declaration instead, alongside
// it (whiny-exclusive).
//
// This test used to assert the opposite: that the drop happened with nothing
// said about it, with an untag condition naming exactly this fix. The delivered
// tree half is unchanged (the hook still reaches no file, and everything else
// still arrives); what changed is that the omission is now REPORTABLE.
func TestDeliveryApproach_OpencodeSaysWhatItCannotCarry(t *testing.T) {
	fs := afero.NewMemMapFs()
	root := "/cell"
	require.NoError(t, fs.MkdirAll(root, 0o755))

	inputs := matrixSentinelInputs()
	set := backends.BuildSurfaces("opencode", inputs, fs)

	// Structural: opencode declares no approach for a hooks-bearing surface
	// beyond the folded settings file.
	require.Empty(t, set.SupportedApproaches(agent.SurfaceMCP),
		"opencode folds MCP into settings; if that changed, this test's premise moved")

	_, kinds, errs := agent.Select(set).WithEverything().DeliverUnder(root)
	require.Empty(t, errs, "a structural gap is not a write failure — nothing errored")

	var wrote []string
	for _, k := range kinds {
		wrote = append(wrote, k.String())
	}
	assert.Equal(t, []string{"context", "settings", "commands", "skills"}, wrote,
		"the delivery report is unchanged: it lists what landed, and the hook did not")

	// The loss the report structurally cannot hold, held beside it.
	losses := backends.UncarriedSurfaces("opencode", inputs)
	require.Len(t, losses, 1, "opencode's dropped hooks must be reportable")
	assert.Equal(t, "hooks", losses[0].Surface)
	assert.Equal(t, "1 session_start", losses[0].Detail,
		"the loss names WHICH hooks, by the config key the user wrote")
	assert.Equal(t, "opencode has no hook mechanism", losses[0].Reason)

	tree := matrixTree(t, fs, root)
	require.NotEmpty(t, tree)
	// Everything else DID arrive...
	assert.NotEmpty(t, findSentinel(tree, slotContext), "context must still be delivered")
	assert.NotEmpty(t, findSentinel(tree, slotMCPCmd), "MCP must still be delivered (folded into opencode.json)")
	assert.NotEmpty(t, findSentinel(tree, slotCommand), "commands must still be delivered")
	assert.NotEmpty(t, findSentinel(tree, slotSkill), "skills must still be delivered")
	// ...and the hook still did not, which is what the loss line is FOR.
	assert.Empty(t, findSentinel(tree, slotHook),
		"opencode has no hooks surface, so the session_start hook reaches no file")
}

// TestDeliveryApproach_HookCarriageMatchesDeclaration is the drift guard on the
// declaration the loss report reads: a backend's noHooksReason is a claim about
// what its settings writer DOES, and a claim nobody checks is how the silence
// came back. Derived over every registered backend, so a seventh one is held to
// it the day it registers.
//
// Both directions are asserted from the PAYLOAD (never from a "delivered"
// report): a backend that declares no hook mechanism must land the hook
// sentinel in no file, and a backend that declares nothing must land it in one.
func TestDeliveryApproach_HookCarriageMatchesDeclaration(t *testing.T) {
	for _, name := range matrixBackends(t) {
		t.Run(name, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			root := "/cell"
			require.NoError(t, fs.MkdirAll(root, 0o755))

			inputs := matrixSentinelInputs()
			set := backends.BuildSurfaces(name, inputs, fs)
			_, _, errs := agent.Select(set).WithEverything().DeliverUnder(root)
			require.Empty(t, errs)

			hookFiles := findSentinel(matrixTree(t, fs, root), slotHook)
			declaredLoss := backends.UncarriedSurfaces(name, inputs)

			if len(declaredLoss) > 0 {
				assert.Empty(t, hookFiles,
					"%s declares it cannot carry hooks (%v) but the hook sentinel reached %v — "+
						"the loss report would be telling users something false", name, declaredLoss, hookFiles)
				return
			}
			assert.NotEmpty(t, hookFiles,
				"%s declares no hook loss, so the configured session_start hook must reach a file; "+
					"it reached none, which is the silent drop whiny-exclusive was filed about", name)
		})
	}
}
