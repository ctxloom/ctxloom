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

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/shared/collections"
)

// This file covers ctxloom's declared CONTEXT DELIVERY MATRIX — every
// (backend, agent.SurfaceKind, agent.Approach) triple a backend's
// agent.Declaration declares — by PAYLOAD, never by exit code or by a
// "delivered" report.
//
// The matrix is DERIVED from the registered backends
// (operations.EngineNames + agent.Declaration.Names), so a sixth backend or
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
func matrixApproaches() []string { return operations.KnownApproachNames(engines.Registry()) }

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
	for _, name := range operations.EngineNames(engines.Registry()) {
		decl := hostedDeclaration(name)
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
		decl := hostedDeclaration(name)
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
	// a Start that advises none (agent.SessionHomeRooted). The loop advises
	// both roots for such a pair and asserts the project root stays EMPTY —
	// a private-home delivery that also touched the project tree would be
	// the shared-cwd exposure the approach exists to avoid.
	underEngineHome bool
	// underScratch roots wantFile beneath the run's SCRATCH — the session's
	// own directory — rather than the project root: the approach is an
	// engine's session form, the default a binding that selects no root
	// gets, and it refuses a Start that advises no Scratch rather than
	// writing a bare relative path. The loop advises both roots for such a
	// pair and asserts the project root stays EMPTY.
	underScratch bool
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
}

// matrixSpecs is the expected destination for every DECLARED pair.
// TestDeliveryApproach_DeclaredPairsAreExhaustive holds these keys equal to the
// derived matrix, so a new pair cannot be added to a backend's Declaration
// without landing here first.
var matrixSpecs = map[string]deliverySpec{
	// ---- claude-code -------------------------------------------------------
	"claude-code/context/unsafe-file": {wantFile: "CLAUDE.md", wantSlot: slotContext},
	// ONE form, on every cell: Deliver writes the framed <hash>.sysprompt.md
	// beneath the private root, and an unrooted run REFUSES rather than
	// writing CLAUDE.md instead. The leaf is a sha256 prefix over the framed
	// bytes, so it is matched by glob rather than named;
	// TestDeliveryApproach_ClaudeSystemPromptScratchPlacement pins the
	// framing and the leaf shape this glob cannot express.
	"claude-code/context/system-prompt": {wantFile: "./*", wantSlot: slotContext, underEngineHome: true},
	"claude-code/context/hook": {
		noOp: "claude's hook arm resolves to a documented no-op (a static CLAUDE.md " +
			"alongside the hook would double the context); the payload rides the " +
			"settings surface's SessionStart hook + the context cache file. Covered " +
			"end to end by TestDeliveryApproach_HookPayloadReachesInjectedContext.",
	},
	"claude-code/mcp/unsafe-file": {wantFile: ".mcp.json", wantSlot: slotMCPCmd},
	// The DEFAULT MCP approach, and it is private: the merged .mcp.json lands
	// beneath the run's private root for --mcp-config, never the user's project
	// file. An unresolved private root refuses (ErrUnrootedSessionHome) rather
	// than falling back to the project file — the fallback IS the defect.
	"claude-code/mcp/mcp-config":       {wantFile: ".mcp.json", wantSlot: slotMCPCmd, underEngineHome: true},
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
	// The session form of each surface (mock.MockSessionFile, the
	// DEFAULT): the same well-known file beneath the run's Scratch, so a
	// binding that selects no root leaves the project tree alone.
	"mock/context/session-file":  {wantFile: "MOCK_CONTEXT.md", wantSlot: slotContext, underScratch: true},
	"mock/skills/session-file":   {wantFile: ".mock/skills/ctxsentinelskill/SKILL.md", wantSlot: slotSkill, underScratch: true},
	"mock/mcp/session-file":      {wantFile: ".mock/mcp.json", wantSlot: slotMCPCmd, underScratch: true},
	"mock/settings/session-file": {wantFile: ".mock/settings.json", wantSlot: slotHook, underScratch: true},
	"mock/commands/session-file": {wantFile: ".mock/commands/ctxsentinelcmd.md", wantSlot: slotCommand, underScratch: true},

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
	"mock-lossy/settings/unsafe-file":  {wantFile: ".mock/settings.json", wantSlot: slotHookPreTool},
	"mock-lossy/commands/unsafe-file":  {wantFile: ".mock/commands/ctxsentinelcmd.md", wantSlot: slotCommand},
	"mock-lossy/context/session-file":  {wantFile: "MOCK_CONTEXT.md", wantSlot: slotContext, underScratch: true},
	"mock-lossy/skills/session-file":   {wantFile: ".mock/skills/ctxsentinelskill/SKILL.md", wantSlot: slotSkill, underScratch: true},
	"mock-lossy/mcp/session-file":      {wantFile: ".mock/mcp.json", wantSlot: slotMCPCmd, underScratch: true},
	"mock-lossy/settings/session-file": {wantFile: ".mock/settings.json", wantSlot: slotHookPreTool, underScratch: true},
	"mock-lossy/commands/session-file": {wantFile: ".mock/commands/ctxsentinelcmd.md", wantSlot: slotCommand, underScratch: true},

	// ---- mock-launch ---------------------------------------------------
	// ONE row, and the absence of the other four is the assertion. This double
	// delivers only context at materialize time; its settings, MCP, commands
	// and skills arrive per session at launch, so they are declared through
	// launchOnlySettingsReason and reported by backends.LaunchOnlySurfaces
	// rather than written anywhere a static caller could find them. If rows for
	// them ever appear here, the double has stopped being launch-delivered.
	"mock-launch/context/unsafe-file":  {wantFile: "MOCK_CONTEXT.md", wantSlot: slotContext},
	"mock-launch/context/session-file": {wantFile: "MOCK_CONTEXT.md", wantSlot: slotContext, underScratch: true},

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
	"mock-noskills/context/unsafe-file":   {wantFile: "MOCK_CONTEXT.md", wantSlot: slotContext},
	"mock-noskills/mcp/unsafe-file":       {wantFile: ".mock/mcp.json", wantSlot: slotMCPCmd},
	"mock-noskills/settings/unsafe-file":  {wantFile: ".mock/settings.json", wantSlot: slotHook},
	"mock-noskills/commands/unsafe-file":  {wantFile: ".mock/commands/ctxsentinelcmd.md", wantSlot: slotCommand},
	"mock-noskills/skills/unsafe-file":    {wantFile: ".mock/skills/ctxsentinelskill/SKILL.md", wantSlot: slotSkill},
	"mock-noskills/context/session-file":  {wantFile: "MOCK_CONTEXT.md", wantSlot: slotContext, underScratch: true},
	"mock-noskills/mcp/session-file":      {wantFile: ".mock/mcp.json", wantSlot: slotMCPCmd, underScratch: true},
	"mock-noskills/settings/session-file": {wantFile: ".mock/settings.json", wantSlot: slotHook, underScratch: true},
	"mock-noskills/commands/session-file": {wantFile: ".mock/commands/ctxsentinelcmd.md", wantSlot: slotCommand, underScratch: true},
	"mock-noskills/skills/session-file":   {wantFile: ".mock/skills/ctxsentinelskill/SKILL.md", wantSlot: slotSkill, underScratch: true},
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
			assert.True(t, s.noOp != "" || s.disagreement != "",
				"%s expects NO delivered file via this loop's mechanism but records no noOp, "+
					"or a disagreement reason — an unexplained zero-byte expectation is "+
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
		decl := hostedDeclaration(name)
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
		decl := hostedDeclaration(name)
		for _, k := range matrixKinds {
			for _, a := range decl.Names(k) {
				key := pairKey(name, k, a)
				t.Run(key, func(t *testing.T) {
					spec, ok := matrixSpecs[key]
					require.True(t, ok, "%s is declared but has no expected destination", key)
					if spec.disagreement != "" {
						t.Skipf("DECLARATION vs BEHAVIOUR disagreement (not reconciled here, reported instead): %s", spec.disagreement)
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
							SessionHome: present.Root{Host: home},
						}))
						deliveryRoot = home
					}
					if spec.underScratch {
						const scratch = "/session-scratch"
						_, unrooted := d.Deliver(present.ProjectOnHost(root))
						require.ErrorIs(t, unrooted, agent.ErrUnrootedDelivery, "%s: a session form must refuse a Start that advises no session home", key)
						start = present.New(present.OnHost(present.Paths{
							ProjectRoot: present.Root{Host: root},
							SessionHome: present.Root{Host: scratch},
						}))
						deliveryRoot = scratch
					}
					_, derr := d.Deliver(start)
					require.NoError(t, derr, "%s: delivery failed", key)

					tree := matrixTree(t, fs, deliveryRoot)
					require.NotEmpty(t, tree, "%s: delivery reported success and wrote ZERO files", key)
					if spec.underEngineHome || spec.underScratch {
						assert.Empty(t, matrixTree(t, fs, root),
							"%s promises a delivery outside the project but wrote into the PROJECT root", key)
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
		decl := hostedDeclaration(name)
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
				})
			}
		}
	}
}

// TestDeliveryApproach_ClaudeSystemPromptScratchPlacement pins the two facts
// the matrix loop's glob cannot express for claude-code/context/system-prompt:
// that the framed <hash>.sysprompt.md leaf is named from a hash over the FRAMED
// bytes, and that the payload carries the framed envelope rather than the bare
// context.
//
// The approach has exactly ONE form now. It previously had two, named backwards
// from the cell that ran them — the plain Deliver was the CLAUDE.md write, so an
// ISOLATED launch that selected system-prompt was silently handed project memory
// instead. This test therefore asserts the ordinary Deliver, not a separate
// out-of-cwd form: there is no longer one to call, and its absence is the fix.
func TestDeliveryApproach_ClaudeSystemPromptScratchPlacement(t *testing.T) {
	fs := afero.NewMemMapFs()
	root := "/cell"
	private := "/engine-home"
	require.NoError(t, fs.MkdirAll(root, 0o755))
	require.NoError(t, fs.MkdirAll(private, 0o755))

	a, ok := claudeDeclaration(t).Construct(agent.SurfaceContext, claude.ApproachSystemPrompt, matrixSentinelInputs(), fs)
	require.True(t, ok)

	handle, err := a.Deliver(present.New(present.OnHost(present.Paths{
		ProjectRoot: present.Root{Host: root},
		SessionHome: present.Root{Host: private},
	})))
	require.NoError(t, err)
	require.NotNil(t, handle)

	// The project tree — the shared cwd this approach's private file exists to
	// spare — stays completely empty.
	assert.Empty(t, matrixTree(t, fs, root), "system-prompt must not touch the project root")

	privateTree := matrixTree(t, fs, private)
	hits := findSentinel(privateTree, slotContext)
	require.Len(t, hits, 1,
		"the system-prompt file must carry the context sentinel (private tree: %v)",
		collections.SortedKeys(privateTree))
	assert.True(t, strings.HasSuffix(hits[0], agent.SCMFramedContextSuffix),
		"the framed file must be named <hash>%s, got %s", agent.SCMFramedContextSuffix, hits[0])
	assert.Contains(t, privateTree[hits[0]], agent.FrameProjectContext(slotContext),
		"the file must carry the FRAMED envelope, not the bare context")
}

// TestDeliveryApproach_SystemPromptRefusesAnUnrootedRun is the other half of the
// one-form fix: with no engine home advised there is nowhere private to write,
// and the approach REFUSES instead of falling back to the well-known CLAUDE.md.
// The fallback is what silently converted an isolated launch's system prompt
// into project memory, so its absence is asserted, not just described.
func TestDeliveryApproach_SystemPromptRefusesAnUnrootedRun(t *testing.T) {
	fs := afero.NewMemMapFs()
	root := "/cell"
	require.NoError(t, fs.MkdirAll(root, 0o755))

	a, ok := claudeDeclaration(t).Construct(agent.SurfaceContext, claude.ApproachSystemPrompt, matrixSentinelInputs(), fs)
	require.True(t, ok)

	_, err := a.Deliver(present.ProjectOnHost(root))
	require.Error(t, err, "an unrooted run must be refused, never served the project file")
	assert.ErrorIs(t, err, agent.ErrUnrootedSessionHome)
	assert.Empty(t, matrixTree(t, fs, root), "a refused delivery must write zero files")
}

// claudeDeclaration is claude's named-form table off the engine value
// (agent.Hosted), the seam the matrix constructs its forms from.
func claudeDeclaration(t *testing.T) agent.Declaration {
	t.Helper()
	e, err := claude.Build()
	require.NoError(t, err)
	return e.(agent.Hosted).Declaration()
}

// hostedDeclaration is the named engine's named-form table off the engine
// value (agent.Hosted); empty for an engine that is not Hosted.
func hostedDeclaration(name string) agent.Declaration {
	h, ok := engines.Hosted(name)
	if !ok {
		return agent.Declaration{}
	}
	return h.Declaration()
}
