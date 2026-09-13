//go:build integration

package integration

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/claude"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/agent/present"
	"github.com/ctxloom/ctxloom/internal/shared/collections"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// This file covers ctxloom's declared CONTEXT DELIVERY MATRIX — every
// (backend, agent.SurfaceKind, agent.Approach) triple a backend's
// agent.Declaration declares — by PAYLOAD, never by exit code or by a
// "delivered" report.
//
// The matrix is DERIVED from the registered backends
// (backends.List + agent.Declaration.Names), so a sixth backend or
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

// matrixApproaches is every approach name ANY registered engine declares —
// derived, so an engine's new name joins the cross product on its own. Used
// for the NEGATIVE direction: the cross product minus the declared pairs must
// be refused loudly.
func matrixApproaches() []string { return backends.KnownApproachNames() }

// sentinel slots. Each names one SurfaceInputs field, so an assertion can say
// WHICH input reached WHICH file rather than "the tree is non-empty".
const (
	slotContext  = "CTXSENTINEL-context"
	slotFragment = "CTXSENTINEL-fragment"
	slotMCP      = "CTXSENTINEL-mcpserver"
	slotMCPCmd   = "CTXSENTINEL-mcpcmd"
	slotHook     = "CTXSENTINEL-hookcmd"
	// slotHookPreTool is a SECOND hook sentinel, on a kind no backend declares
	// unsupported. Without it a partially-lossy backend was untestable: the
	// inputs carried only session_start, which mock-lossy declares it cannot
	// carry, so "strips the declared loss" and "carries nothing at all" produced
	// the identical empty result and no assertion could tell them apart.
	slotHookPreTool = "CTXSENTINEL-pretoolcmd"
	slotCommand     = "CTXSENTINEL-command"
	slotSkill       = "CTXSENTINEL-skill"
)

// matrixSentinelInputs is a fully populated agent.SurfaceInputs in which every
// field carries its own distinctive sentinel, so a delivery that writes the
// WRONG input into the right file is caught as surely as one that writes
// nothing.
func matrixSentinelInputs() agent.SurfaceInputs {
	return agent.SurfaceInputs{
		Context:   slotContext,
		Fragments: []*agent.Fragment{{Name: "sentinel-frag", Content: slotFragment}},
		BundleMCP: map[string]wire.MCPServer{
			slotMCP: {Command: slotMCPCmd},
		},
		// TWO hook kinds, on purpose. session_start is the one mock-lossy
		// declares unsupported; pre_tool is one every backend carries. A
		// partially-lossy engine must therefore drop exactly one and deliver the
		// other, and each half is asserted on its OWN sentinel.
		Hooks: &wire.HooksConfig{Unified: wire.UnifiedHooks{
			SessionStart: []wire.Hook{{Command: slotHook, Type: "command"}},
			PreTool:      []wire.Hook{{Command: slotHookPreTool, Type: "command"}},
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
		decl := backends.Declared(name)
		for _, k := range matrixKinds {
			if len(decl.Names(k)) > 0 {
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
func pairKey(backend string, k agent.SurfaceKind, a string) string {
	return backend + "/" + k.String() + "/" + a
}

// derivedPairs enumerates every (backend, kind, approach) triple the registered
// backends DECLARE — the test matrix and the oracle.
func derivedPairs(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, name := range matrixBackends(t) {
		decl := backends.Declared(name)
		for _, k := range matrixKinds {
			for _, a := range decl.Names(k) {
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
	// underEngineHome roots wantFile beneath the ENGINE HOME rather than the
	// project root: the approach writes the engine's private home and refuses
	// a Start that advises none (agent.EngineHomeRooted). The loop advises
	// both roots for such a pair and asserts the project root stays EMPTY —
	// a private-home delivery that also touched the project tree would be
	// the shared-cwd exposure the approach exists to avoid.
	underEngineHome bool
	// alsoFile / alsoSlot pin a SECOND route the same pair delivers (a
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
	// — just not by this test's generic Construct(kind, approach).Deliver(present.ProjectOnHost(root))
	// mechanism. claude's (context, system-prompt) is the one case: the declaration
	// resolves the SAME dual-capable object unsafe-file does (its well-known
	// Deliver always writes the native file — that is what the approach value decides,
	// not what approach was named), so the out-of-cwd scratch this approach
	// actually promises is reachable only through its OutOfCwd form
	// (DeliverIsolated), which this generic loop never calls. Named test covers
	// the real payload with the right mechanism; unlike disagreement, this is
	// not an open mismatch to reconcile.
	elsewhere string
}

// matrixSpecs is the expected destination for every DECLARED pair.
// TestDeliveryApproach_DeclaredPairsAreExhaustive holds these keys equal to the
// derived matrix, so a new pair cannot be added to a backend's Declaration
// without landing here first.
var matrixSpecs = map[string]deliverySpec{
	// ---- claude-code -------------------------------------------------------
	"claude-code/context/unsafe-file": {wantFile: "CLAUDE.md", wantSlot: slotContext},
	"claude-code/context/system-prompt": {
		elsewhere: "TestDeliveryApproach_ClaudeSystemPromptScratchPlacement — the out-of-cwd form " +
			"is now keyed on (kind, approach) (U100-F05), so this pair's out-of-cwd " +
			"<hash>.sysprompt.md scratch is real; it is reached through SharedRealization " +
			"(DeliverIsolated), never through the generic Construct+Deliver(root) this " +
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
	"claude-code/settings/hew-record":  {wantFile: "settings.json", wantSlot: slotHook, underEngineHome: true},
	"claude-code/commands/unsafe-file": {wantFile: ".claude/commands/ctxsentinelcmd.md", wantSlot: slotCommand},
	"claude-code/skills/unsafe-file":   {wantFile: ".claude/skills/ctxsentinelskill/SKILL.md", wantSlot: slotSkill},

	// ---- mock ----------------------------------------------------------
	// mock is a COMPLETE engine minus a model: it declares all five surfaces
	// (mock_surfaces.go's mockPresentations). Context is a managed-marker
	// MOCK_CONTEXT.md at the target root — the same DeliverManagedContext shape
	// claude's CLAUDE.md uses — and the rest live under its own .mock/ config
	// dir, the shape every real engine has rather than a top-level scatter.
	// Completeness is the point: mock exists to prove the surface seam is
	// POLYMORPHIC, and a partial double makes its gaps load-bearing somewhere
	// nothing states them.
	"mock/context/unsafe-file":  {wantFile: "MOCK_CONTEXT.md", wantSlot: slotContext},
	"mock/skills/unsafe-file":   {wantFile: ".mock/skills/ctxsentinelskill/SKILL.md", wantSlot: slotSkill},
	"mock/mcp/unsafe-file":      {wantFile: ".mock/mcp.json", wantSlot: slotMCPCmd},
	"mock/settings/unsafe-file": {wantFile: ".mock/settings.json", wantSlot: slotHook},
	"mock/commands/unsafe-file": {wantFile: ".mock/commands/ctxsentinelcmd.md", wantSlot: slotCommand},

	// ---- mock-lossy ----------------------------------------------------
	// Byte-for-byte mock's surfaces: it shares NewMockSurfaces and differs ONLY
	// in the hook KINDS its descriptor declares unsupported. Its rows are
	// therefore identical, and that identity is the evidence — a lossy double
	// whose deliveries diverged from the complete one would be testing two
	// things at once, and its loss reporting could no longer be attributed to
	// the declaration rather than to a different surface set.
	"mock-lossy/context/unsafe-file": {wantFile: "MOCK_CONTEXT.md", wantSlot: slotContext},
	"mock-lossy/skills/unsafe-file":  {wantFile: ".mock/skills/ctxsentinelskill/SKILL.md", wantSlot: slotSkill},
	"mock-lossy/mcp/unsafe-file":     {wantFile: ".mock/mcp.json", wantSlot: slotMCPCmd},
	// The ONE row where mock-lossy diverges from mock, and the divergence is
	// the entire reason this double exists. The sentinel inputs configure a
	// single session_start hook — precisely the kind mock-lossy declares it has
	// no native event for — so its settings surface strips it and reports
	// delivering nothing. The file is still created (an empty unified block),
	// which is why this is a noOp rather than a missing file.
	//
	// If a payload ever lands here again, the declaration and the delivery have
	// come apart, and it is the LOSS REPORT that becomes false: it would tell a
	// user their guardrail did not land while the hook sits in the file.
	// TestDeliveryApproach_HookCarriageMatchesDeclaration is the other side of
	// this same coin.
	// The pair carries the pre_tool sentinel and NOT the session_start one, and
	// that asymmetry is the assertion: mock-lossy delivers what it can while
	// stripping exactly what it declared it cannot. Pinning slotHookPreTool here
	// and slotHook's ABSENCE in
	// TestDeliveryApproach_HookCarriageMatchesDeclaration is what separates
	// "honoured the declaration" from "carried nothing at all".
	"mock-lossy/settings/unsafe-file": {wantFile: ".mock/settings.json", wantSlot: slotHookPreTool},
	"mock-lossy/commands/unsafe-file": {wantFile: ".mock/commands/ctxsentinelcmd.md", wantSlot: slotCommand},

	// ---- mock-launch ---------------------------------------------------
	// ONE row, and the absence of the other four is the assertion. This double
	// delivers only context at materialize time; its settings, MCP, commands
	// and skills arrive per session at launch, so they are declared through
	// launchOnlySettingsReason and reported by backends.LaunchOnlySurfaces
	// rather than written anywhere a static caller could find them. If rows for
	// them ever appear here, the double has stopped being launch-delivered.
	"mock-launch/context/unsafe-file": {wantFile: "MOCK_CONTEXT.md", wantSlot: slotContext},

	// ---- mock-noskills -------------------------------------------------
	// Byte-for-byte mock's surfaces, because it shares NewMockSurfaces and
	// differs ONLY in declaring no skillExports mapper on its descriptor.
	//
	// THE SKILLS ROW IS IDENTICAL TO MOCK'S, and that is not an oversight — it
	// is where this double's semantics become precise.
	//
	// "No skills" here means NO EXPORT MAPPER on the descriptor, not a missing
	// surface. The skills SURFACE exists (every mock-family double gets the
	// complete set), and this test builds SurfaceInputs directly, so the surface
	// is handed a sentinel skill and correctly writes it. The mapper only
	// governs the MATERIALIZE path, where bundle-loaded skills are turned into
	// exports — which is exactly what backends.SupportsSkills reads and what
	// callers branch on.
	//
	// So the arm this double exists for is not visible at THIS seam at all.
	// It is pinned by backends.TestSupportsSkills_HasATrueArmAndAFalseArm and by
	// operations.TestMaterializeProfile_NoSkillsEngineDumpsAPremisedFragmentIntoContext.
	// A reader who expects a missing surface here will look for one and not find
	// it.
	"mock-noskills/context/unsafe-file":  {wantFile: "MOCK_CONTEXT.md", wantSlot: slotContext},
	"mock-noskills/mcp/unsafe-file":      {wantFile: ".mock/mcp.json", wantSlot: slotMCPCmd},
	"mock-noskills/settings/unsafe-file": {wantFile: ".mock/settings.json", wantSlot: slotHook},
	"mock-noskills/commands/unsafe-file": {wantFile: ".mock/commands/ctxsentinelcmd.md", wantSlot: slotCommand},
	"mock-noskills/skills/unsafe-file":   {wantFile: ".mock/skills/ctxsentinelskill/SKILL.md", wantSlot: slotSkill},
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

// TestDeliveryApproach_DefaultIsDeclared pins the other half of the
// declaration: an engine's default for a kind is one of the names it declares
// for it — a NAMED default, not a position — and WithEverything (the
// materialize/launch selection) picks it. A backend whose default named
// something it cannot construct would silently materialize nothing.
func TestDeliveryApproach_DefaultIsDeclared(t *testing.T) {
	for _, name := range matrixBackends(t) {
		decl := backends.Declared(name)
		for _, k := range matrixKinds {
			supported := decl.Names(k)
			def, ok := decl.Default(k)
			if len(supported) == 0 {
				assert.False(t, ok, "%s/%s declares no approach, so it must report no default", name, k)
				continue
			}
			require.True(t, ok, "%s/%s declares approaches but reports no default", name, k)
			assert.Contains(t, supported, def, "%s/%s: default must be one of the declared approaches", name, k)
		}
	}
}

// TestDeliveryApproach_EveryDeclaredPairDeliversItsPayload is the core matrix
// test: for every declared (backend, kind, approach) it resolves the concrete
// agent.Approach via the Declaration, delivers it into a fresh root, and
// asserts the pair's SENTINEL landed in the file that approach promises.
//
// It deliberately does NOT assert on a returned error alone: agent.Delivery
// returns a nil error for a delivery that wrote nothing (that is the shared
// "nothing to write" convention), so an error-only assertion cannot tell
// delivered from silently-skipped.
func TestDeliveryApproach_EveryDeclaredPairDeliversItsPayload(t *testing.T) {
	isolatedRecords(t)
	for _, name := range matrixBackends(t) {
		decl := backends.Declared(name)
		for _, k := range matrixKinds {
			for _, a := range decl.Names(k) {
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

					d, ok := decl.Construct(k, a, matrixSentinelInputs(), fs)
					require.True(t, ok, "%s: declared but Construct refused it", key)

					if spec.noOp != "" {
						if d != nil {
							_, derr := d.Deliver(present.ProjectOnHost(root))
							require.NoError(t, derr)
						}
						assert.Empty(t, matrixTree(t, fs, root),
							"%s is recorded as a no-op at this seam (%s) but WROTE files — "+
								"the recorded reason is stale", key, spec.noOp)
						return
					}

					require.NotNil(t, d, "%s: declared pair resolved to a nil Delivery", key)
					start, deliveryRoot := present.ProjectOnHost(root), root
					if spec.underEngineHome {
						const home = "/engine-home"
						start = present.New(present.OnHost(present.Paths{
							ProjectRoot: present.Root{Host: root},
							EngineHome:  present.Root{Host: home},
						}))
						deliveryRoot = home
					}
					_, derr := d.Deliver(start)
					require.NoError(t, derr, "%s: delivery failed", key)

					tree := matrixTree(t, fs, deliveryRoot)
					require.NotEmpty(t, tree, "%s: delivery reported success and wrote ZERO files", key)
					if spec.underEngineHome {
						assert.Empty(t, matrixTree(t, fs, root),
							"%s promises a private-home delivery but wrote into the PROJECT root", key)
					}

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
			"%s: no file under %s/ carries %s (tree: %v)", key, dir, sentinel, collections.SortedKeys(tree))
		return
	}
	content, ok := tree[want]
	require.True(t, ok, "%s: the approach promises %s but it was not written (tree: %v)",
		key, want, collections.SortedKeys(tree))
	assert.Contains(t, content, sentinel,
		"%s: %s exists but does not carry %s — the file was created without the payload", key, want, sentinel)
	assert.Equal(t, []string{want}, findSentinel(tree, sentinel),
		"%s: %s must reach exactly the promised destination and no other file", key, sentinel)
}

// TestDeliveryApproach_UndeclaredPairsAreRefusedLoudly covers the NEGATIVE
// direction across the whole cross product: every (backend, kind, approach)
// triple a backend does NOT declare must be REFUSED with an error naming the
// backend, the surface, and the approach — never accepted and silently skipped.
//
// The one deliberate exception is a kind a backend FOLDS or omits entirely
// (a backend whose MCP rides its config surface): SurfaceFor
// still refuses it loudly, but agent.SurfaceSelection.Build treats selecting it
// as a permitted no-op. That asymmetry is documented, so it is pinned here
// rather than left to chance.
func TestDeliveryApproach_UndeclaredPairsAreRefusedLoudly(t *testing.T) {
	for _, name := range matrixBackends(t) {
		decl := backends.Declared(name)
		for _, k := range matrixKinds {
			supported := decl.Names(k)
			if len(supported) == 0 {
				// A kind the engine does not declare is a FOLD: selecting it is
				// a permitted no-op, never a refusal (see cells.go's Build).
				continue
			}
			for _, a := range matrixApproaches() {
				if slices.Contains(supported, a) {
					continue
				}
				t.Run("refuse/"+pairKey(name, k, a), func(t *testing.T) {
					d, ok := decl.Construct(k, a, matrixSentinelInputs(), afero.NewMemMapFs())
					assert.False(t, ok, "%s: undeclared pair constructed a surface instead of being refused", pairKey(name, k, a))
					assert.Nil(t, d, "a refused pair must not also hand back an Approach")

					_, err := agent.Select(decl).With(k, a).Build(matrixSentinelInputs(), afero.NewMemMapFs())
					require.Error(t, err, "%s: undeclared pair must be refused by Build", pairKey(name, k, a))
					msg := err.Error()
					assert.Contains(t, msg, refusalLabel(name), "the refusal must name the backend")
					assert.Contains(t, msg, k.String(), "the refusal must name the surface kind")
					// A kind the backend HAS, at an approach it does not: the
					// refusal must name the rejected approach so the user can
					// tell "wrong approach" from "no such surface".
					assert.Contains(t, msg, a, "the refusal must name the rejected approach")
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

// TestDeliveryApproach_UndeclaredApproachFailsTheBuilder pins the same refusal
// one level up, at the seam callers actually use: naming an approach a backend
// does not support must fail agent.SurfaceSelection.Build with the SUPPORTED SET
// in the message, not deliver a different approach's file.
func TestDeliveryApproach_UndeclaredApproachFailsTheBuilder(t *testing.T) {
	// mock declares context as unsafe-file ONLY, so both the
	// hook and the system-prompt approaches must be refused by the builder.
	for _, name := range []string{"mock"} {
		for _, w := range []struct {
			label string
			write string
		}{
			{"hook", agent.ApproachHook},
			{"system-prompt", claude.ApproachSystemPrompt},
		} {
			t.Run(name+"/context/"+w.label, func(t *testing.T) {
				fs := afero.NewMemMapFs()
				sel := agent.Select(backends.Declared(name)).
					With(agent.SurfaceContext, w.write).
					With(agent.SurfaceSettings, agent.ApproachUnsafeFile)
				resolved, err := sel.Build(matrixSentinelInputs(), fs)
				require.Error(t, err, "%s must refuse context via %s", name, w.label)
				assert.Nil(t, resolved)
				assert.Contains(t, err.Error(), "not supported")
				assert.Contains(t, err.Error(), "unsafe-file",
					"the refusal must name the SUPPORTED set so the caller can correct the selection")

				// The refusal must be total: nothing at all is delivered.
				_, kinds, errs := sel.DeliverUnder(matrixSentinelInputs(), fs, present.ProjectOnHost("/cell"))
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
//     honor-with-warning fork was the one chosen) — never silently upgraded
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
		resolved, err := agent.Select(claude.Surfaces).With(agent.SurfaceContext, agent.ApproachUnsafeFile).Build(matrixSentinelInputs(), fs)
		require.NoError(t, err)
		_, kinds, errs := resolved.DeliverShared(runRoots("/live-cwd", scratch))
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
		for _, ra := range resolved.Approaches() {
			_, converts := ra.Approach.(agent.OutOfCwd)
			assert.False(t, converts, "the native-file context approach has no out-of-cwd form, so nothing could have run one")
		}
	})

	t.Run("system-prompt at rest never produces a sysprompt file", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		require.NoError(t, fs.MkdirAll(scratch, 0o755))
		// (a) Through the sanctioned at-rest terminal: an honest, loud refusal —
		// the approach is LaunchOnly (DeliverUnder never runs an out-of-cwd form).
		resolved, err := agent.Select(claude.Surfaces).With(agent.SurfaceContext, claude.ApproachSystemPrompt).Build(matrixSentinelInputs(), fs)
		require.NoError(t, err, "the approach IS declared, so Build must accept it")
		_, kinds, errs := resolved.DeliverUnder(present.ProjectOnHost("/cell"))
		assert.Empty(t, kinds)
		require.Len(t, errs, 1)
		assert.Contains(t, errs[0].Error(), "no argv sink at rest",
			"at-rest system-prompt delivery must be refused, naming the missing argv sink")
		assert.Empty(t, matrixTree(t, fs, "/cell"), "a refused delivery must write zero files")

		// (b) Through the approach's raw well-known Deliver: it writes the
		// NATIVE file — the system-prompt approach's Deliver IS the CLAUDE.md
		// write (the form an isolated cell runs for it, preserved as it was).
		// The real out-of-cwd destination this pair promises is reached only
		// through its OutOfCwd form (DeliverIsolated), which this raw call
		// never invokes.
		d, ok := claude.Surfaces.Construct(agent.SurfaceContext, claude.ApproachSystemPrompt, matrixSentinelInputs(), fs)
		require.True(t, ok)
		_, err = d.Deliver(present.ProjectOnHost("/raw"))
		require.NoError(t, err)
		rawTree := matrixTree(t, fs, "/raw")
		assert.Equal(t, []string{"CLAUDE.md"}, findSentinel(rawTree, slotContext))
		for p := range rawTree {
			assert.False(t, strings.HasSuffix(p, agent.SCMFramedContextSuffix),
				"no sysprompt file is produced by the raw Delivery — only the out-of-cwd form writes it (%s)", p)
		}
	})
}

// TestDeliveryApproach_ClaudeSystemPromptScratchPlacement is the
// scratch-placement-aware variant matrixSpecs' "elsewhere" entry for
// claude-code/context/system-prompt promises, now un-skipped. Unlike every
// other matrix cell, this pair's payload is not reached through
// Construct(kind, approach).Deliver(present.ProjectOnHost(root)) — that resolves to the SAME
// dual-capable contextSurface every context approach shares, whose well-known
// Deliver always writes CLAUDE.md regardless of which approach was named (see
// TestDeliveryApproach_SharedRealizationIsApproachKeyed_U100F05's second
// half). The out-of-cwd scratch this approach promises is reached through
// SharedRealization(context, system-prompt) — the exact call
// agent.ResolvedSelection.deliverOneShared makes for a real shared-cwd
// launch — which the pair-keyed re-key makes trustworthy to assert
// on here: it fires ONLY for this pair, never for unsafe-file.
func TestDeliveryApproach_ClaudeSystemPromptScratchPlacement(t *testing.T) {
	fs := afero.NewMemMapFs()
	root := "/cell"
	scratch := "/isolated"
	require.NoError(t, fs.MkdirAll(root, 0o755))
	require.NoError(t, fs.MkdirAll(scratch, 0o755))

	a, ok := claude.Surfaces.Construct(agent.SurfaceContext, claude.ApproachSystemPrompt, matrixSentinelInputs(), fs)
	require.True(t, ok)
	outOfCwd, ok := a.(agent.OutOfCwd)
	require.True(t, ok, "claude-code/context/system-prompt must have an out-of-cwd form")

	handle, err := outOfCwd.DeliverIsolated(runRoots(root, scratch))
	require.NoError(t, err)
	require.NotNil(t, handle)

	// root — the shared cwd this cell's launch-flag scratch exists to spare —
	// stays completely empty.
	assert.Empty(t, matrixTree(t, fs, root), "system-prompt must not touch the shared cwd")

	scratchTree := matrixTree(t, fs, scratch)
	hits := findSentinel(scratchTree, slotContext)
	require.Len(t, hits, 1,
		"the system-prompt scratch file must carry the context sentinel (scratch tree: %v)",
		collections.SortedKeys(scratchTree))
	assert.True(t, strings.HasSuffix(hits[0], agent.SCMFramedContextSuffix),
		"the framed file must be named <hash>%s, got %s", agent.SCMFramedContextSuffix, hits[0])
	assert.Contains(t, scratchTree[hits[0]], agent.FrameProjectContext(slotContext),
		"the scratch file must carry the FRAMED envelope, not the bare context")

	assert.Equal(t, filepath.Join(scratch, hits[0]), a.(interface{ Path() string }).Path(),
		"Path() (the --append-system-prompt-file argument) must name the written file")
}

// runRoots advises a run rooted at project with its out-of-cwd scratch at
// scratch, on the host — the launch path's per-run roots, modelled here so the
// system-prompt destination is a real directory rather than an unresolved
// root.
func runRoots(project, scratch string) present.Start {
	return present.New(present.OnHost(present.Paths{
		ProjectRoot: present.Root{Host: project},
		Scratch:     present.Root{Host: scratch},
	}))
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
//
// TWO DECLARATIONS COUNT AS ONE HERE, deliberately. noHooksReason says the
// ENGINE has no hook mechanism at all; launchOnlySettingsReason says a HARPLESS
// caller — which is exactly what this loop is, BuildSurfaces with no session —
// has nowhere to write one. The claim under test is "if anything was declared,
// nothing landed", and both declarations make that claim about this seam.
// Reading only the first would fail a backend for honouring the second.
func TestDeliveryApproach_HookCarriageMatchesDeclaration(t *testing.T) {
	isolatedRecords(t)
	for _, name := range matrixBackends(t) {
		t.Run(name, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			root := "/cell"
			require.NoError(t, fs.MkdirAll(root, 0o755))

			inputs := matrixSentinelInputs()
			_, _, errs := agent.Select(backends.Declared(name)).WithEverything().DeliverUnder(inputs, fs, present.ProjectOnHost(root))
			require.Empty(t, errs)

			hookFiles := findSentinel(matrixTree(t, fs, root), slotHook)
			declaredLoss := append(backends.UncarriedSurfaces(name, inputs),
				backends.LaunchOnlySurfaces(name, inputs)...)

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
