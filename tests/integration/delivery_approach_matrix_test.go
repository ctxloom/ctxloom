//go:build integration

package integration

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/fsstatic"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/composite/compositetest"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/shared/collections"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// This file covers ctxloom's CONTEXT DELIVERY MATRIX in its two halves:
//
//   - the NAMES a binding may select: every (engine, kind, approach name) an
//     engine's agent.Declaration declares, held equal to a stated table so a
//     name cannot appear or vanish unreviewed;
//   - the DELIVERY: every (engine, kind, root) an engine's typed approach
//     offers, delivered by the ONE static writer (fsstatic) and asserted by
//     PAYLOAD — a sentinel in the file the approach promises, and nowhere
//     else — never by exit code or by a "delivered" report.
//
// Both are DERIVED from the registered engines (operations.EngineNames), so a
// new engine, name or root is picked up automatically and fails the
// exhaustiveness assertions until its expectation is stated here.
//
// Why payload and not exit code: this codebase's characteristic bug is exit 0
// + a success line + zero bytes.

// matrixKinds is every surface kind a binding can name.
var matrixKinds = []agent.SurfaceKind{
	agent.SurfaceContext,
	agent.SurfaceMCP,
	agent.SurfaceSettings,
	agent.SurfaceCommands,
	agent.SurfaceSkills,
}

// matrixBackends returns the registered engines that declare at least one
// selectable name — derived, never hard-coded.
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

// pairKey is a matrix coordinate: engine/kind/(approach name or root).
func pairKey(backend string, k agent.SurfaceKind, a string) string {
	return backend + "/" + k.String() + "/" + a
}

// ---------------------------------------------------------------------------
// THE NAMES
// ---------------------------------------------------------------------------

// declaredNames is every (engine, kind, name) a binding may select.
// TestDeliveryApproach_DeclaredNamesAreExhaustive holds it equal to the
// registered engines' declarations.
var declaredNames = []string{
	"claude-code/commands/file",
	"claude-code/context/system-prompt",
	"claude-code/context/file",
	"claude-code/mcp/mcp-config",
	"claude-code/mcp/file",
	"claude-code/settings/file",
	"claude-code/skills/file",
	// mock-launch delivers only context at materialize time; its other
	// surfaces arrive per session, so it names nothing else.
	"mock-launch/context/session-file",
	"mock-launch/context/file",
	"mock-lossy/commands/session-file",
	"mock-lossy/commands/file",
	"mock-lossy/context/session-file",
	"mock-lossy/context/file",
	"mock-lossy/mcp/session-file",
	"mock-lossy/mcp/file",
	"mock-lossy/settings/session-file",
	"mock-lossy/settings/file",
	"mock-lossy/skills/session-file",
	"mock-lossy/skills/file",
	"mock-noskills/commands/session-file",
	"mock-noskills/commands/file",
	"mock-noskills/context/session-file",
	"mock-noskills/context/file",
	"mock-noskills/mcp/session-file",
	"mock-noskills/mcp/file",
	"mock-noskills/settings/session-file",
	"mock-noskills/settings/file",
	"mock-noskills/skills/session-file",
	"mock-noskills/skills/file",
	"mock/commands/session-file",
	"mock/commands/file",
	"mock/context/session-file",
	"mock/context/file",
	"mock/mcp/session-file",
	"mock/mcp/file",
	"mock/settings/session-file",
	"mock/settings/file",
	"mock/skills/session-file",
	"mock/skills/file",
}

// TestDeliveryApproach_DeclaredNamesAreExhaustive holds the DERIVED name
// table equal to declaredNames: an engine that declares a new name for a
// kind — or drops one — fails here until the change is stated.
func TestDeliveryApproach_DeclaredNamesAreExhaustive(t *testing.T) {
	var derived []string
	for _, name := range matrixBackends(t) {
		decl := hostedDeclaration(name)
		for _, k := range matrixKinds {
			for _, a := range decl.Names(k) {
				derived = append(derived, pairKey(name, k, a))
			}
		}
	}
	sort.Strings(derived)
	want := append([]string(nil), declaredNames...)
	sort.Strings(want)
	assert.Equal(t, want, derived, "the declared (engine, surface, name) table and the stated one disagree")
}

// TestDeliveryApproach_DefaultIsDeclared: an engine's default for a kind is
// one of the names it declares for it — a NAMED default, not a position.
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

// hostedDeclaration is the named engine's name table off the engine value
// (agent.Hosted); empty for an engine that is not Hosted.
func hostedDeclaration(name string) agent.Declaration {
	h, ok := engines.Hosted(name)
	if !ok {
		return agent.Declaration{}
	}
	return h.Declaration()
}

// ---------------------------------------------------------------------------
// THE DELIVERY
// ---------------------------------------------------------------------------

// Sentinels: each names one package input, so an assertion can say WHICH
// input reached WHICH file rather than "the tree is non-empty".
const (
	slotContext = "CTXSENTINEL-context"
	slotMCPCmd  = "CTXSENTINEL-mcpcmd"
	slotDeny    = "CTXSENTINEL-deny"
	// slotHook rides session_start, the event mock-lossy declares it cannot
	// carry; slotHookPreTool rides pre_tool, which every engine carries. A
	// partially lossy engine must drop exactly the first and deliver the
	// second, and each half is asserted on its own sentinel.
	slotHook        = "CTXSENTINEL-hookcmd"
	slotHookPreTool = "CTXSENTINEL-pretoolcmd"
	slotCommand     = "CTXSENTINEL-command"
	// slotSkill is the skill's name, carried in its SKILL.md frontmatter.
	slotSkill = "ctxsentinelskill"
)

// matrixPackage carries every sentinel.
func matrixPackage(t *testing.T) composite.Package {
	t.Helper()
	pkg := compositetest.Fixture(t,
		compositetest.WithFragment("sentinel-frag", slotContext),
		compositetest.WithMCP("ctxsentinelmcp", wire.MCPServer{Command: slotMCPCmd}),
		compositetest.WithCommand("ctxsentinelcmd", slotCommand))
	// A vendor-valid skill: claude refuses a SKILL.md without a name and a
	// description in its frontmatter (skillconstraints.go).
	skillMD := []byte("---\nname: " + slotSkill + "\ndescription: the sentinel skill\n---\n\nBody\n")
	pkg.Skills = append(pkg.Skills, composite.Item[composite.Skill]{Ref: "fixture#skill/" + slotSkill, Value: composite.Skill{
		Name: slotSkill, Description: "the sentinel skill",
		Files: []engine.SkillFile{{Path: "SKILL.md", Bytes: skillMD, Size: int64(len(skillMD)), Mode: 0o644}},
	}})
	pkg.Hooks.Unified.SessionStart = []wire.Hook{{Command: slotHook, Type: "command"}}
	pkg.Hooks.Unified.PreTool = []wire.Hook{{Command: slotHookPreTool, Type: "command"}}
	pkg.DenyTools = []string{slotDeny}
	return pkg
}

// deliverySpec is the promised DESTINATION and PAYLOAD for one (engine, kind,
// root): stated once, so the assertion is against the promise rather than
// against whatever the code happens to do.
type deliverySpec struct {
	// wantFile is the relpath beneath the selected root the payload must land
	// in. It may be "<dir>/*" to match one file in that directory (a leaf
	// named from a hash of its bytes).
	wantFile string
	wantSlot string
	// noOp, when set, says WHY this cell legitimately writes nothing; the
	// matrix then asserts both roots stay empty. A noOp with a wantFile is a
	// test bug.
	noOp string
}

// mockFamily is the delivery rows of a mock-family double: every kind it
// carries lands in the same file beneath either root.
func mockFamily(name string, kinds ...present.Kind) map[string]deliverySpec {
	files := map[present.Kind]deliverySpec{
		present.Context:  {wantFile: "MOCK_CONTEXT.md", wantSlot: slotContext},
		present.MCP:      {wantFile: ".mock/mcp.json", wantSlot: slotMCPCmd},
		present.Settings: {wantFile: ".mock/settings.json", wantSlot: slotDeny},
		present.Hooks:    {wantFile: ".mock/hooks.json", wantSlot: slotHookPreTool},
		present.Commands: {wantFile: ".mock/commands/ctxsentinelcmd.md", wantSlot: slotCommand},
		present.Skills:   {wantFile: ".mock/skills/" + slotSkill + "/SKILL.md", wantSlot: slotSkill},
	}
	out := map[string]deliverySpec{}
	for _, k := range kinds {
		for _, r := range []present.RootKind{present.RootSessionHome, present.RootProjectRoot} {
			out[pairKey(name, k, r.String())] = files[k]
		}
	}
	return out
}

// allKinds is every kind a complete engine delivers.
var allKinds = []present.Kind{present.Context, present.MCP, present.Settings, present.Hooks, present.Commands, present.Skills}

// deliverySpecs is the promised destination for every (engine, kind, root)
// the registered engines' typed approaches offer.
var deliverySpecs = func() map[string]deliverySpec {
	out := map[string]deliverySpec{
		// The session home is claude's CLAUDE_CONFIG_DIR: the framed system
		// prompt (its leaf a hash of the framed bytes), the private .mcp.json
		// announced on --mcp-config, and the user-level settings, commands and
		// skills claude loads from its config dir.
		"claude-code/context/session-home":  {wantFile: "./*", wantSlot: slotContext},
		"claude-code/mcp/session-home":      {wantFile: ".mcp.json", wantSlot: slotMCPCmd},
		"claude-code/settings/session-home": {wantFile: "settings.json", wantSlot: slotDeny},
		"claude-code/hooks/session-home":    {wantFile: "settings.json", wantSlot: slotHookPreTool},
		"claude-code/commands/session-home": {wantFile: "commands/ctxsentinelcmd.md", wantSlot: slotCommand},
		"claude-code/skills/session-home":   {wantFile: "skills/" + slotSkill + "/SKILL.md", wantSlot: slotSkill},
		// The project root is the well-known project files.
		"claude-code/context/project-root":  {wantFile: "CLAUDE.md", wantSlot: slotContext},
		"claude-code/mcp/project-root":      {wantFile: ".mcp.json", wantSlot: slotMCPCmd},
		"claude-code/settings/project-root": {wantFile: ".claude/settings.json", wantSlot: slotDeny},
		"claude-code/hooks/project-root":    {wantFile: ".claude/settings.json", wantSlot: slotHookPreTool},
		"claude-code/commands/project-root": {wantFile: ".claude/commands/ctxsentinelcmd.md", wantSlot: slotCommand},
		"claude-code/skills/project-root":   {wantFile: ".claude/skills/" + slotSkill + "/SKILL.md", wantSlot: slotSkill},
	}
	// "No skills" is the absence of a skills EXPORT, not of the surface: the
	// double carries a skills approach and exports nothing to it, so a
	// package's skill reaches no file. That absence is the double's subject
	// (operations.TestMaterializeProfile_NoSkillsEngineDumpsAPremisedFragmentIntoContext).
	noSkills := "mock-noskills exports no skill, so its skills approach is handed none"
	for _, m := range []map[string]deliverySpec{
		mockFamily("mock", allKinds...),
		// Byte-for-byte mock's surfaces; it differs only in the hook events it
		// declares it cannot carry (TestDeliveryApproach_HookLossesAreHonoured).
		mockFamily("mock-lossy", allKinds...),
		// Delivers only context statically; the rest arrive per session.
		mockFamily("mock-launch", present.Context),
		mockFamily("mock-noskills", allKinds...),
	} {
		for k, v := range m {
			out[k] = v
		}
	}
	for _, r := range []present.RootKind{present.RootSessionHome, present.RootProjectRoot} {
		out[pairKey("mock-noskills", present.Skills, r.String())] = deliverySpec{noOp: noSkills}
	}
	return out
}()

// matrixCell is one delivery's two roots on the host.
type matrixCell struct {
	project, home string
	paths         present.Paths
}

func newMatrixCell(t *testing.T) matrixCell {
	t.Helper()
	project, home := t.TempDir(), t.TempDir()
	return matrixCell{project: project, home: home, paths: present.Paths{
		ProjectRoot: present.Root{Host: project, Engine: project},
		SessionHome: present.Root{Host: home, Engine: home},
	}}
}

// deliverOne routes the matrix package for eng with kind selected at root,
// and delivers ONLY that kind's items through the static writer into cell.
func deliverOne(t *testing.T, eng engine.Engine, kind present.Kind, root present.RootKind, cell present.Paths) error {
	t.Helper()
	def := eng.Root()
	pkg := matrixPackage(t)
	items := pkg.EngineItems(def.Name)
	exports, err := eng.Exports(items)
	require.NoError(t, err)
	// Select root for kind; a kind the engine does not carry is a declared
	// loss the binding accepts, as a launch's would.
	pref := delivery.Preference{Root: map[present.Kind]present.RootKind{kind: root}, AcceptLoss: map[present.Kind]bool{}}
	for _, k := range allKinds {
		if !def.Carries(k) {
			pref.AcceptLoss[k] = true
		}
	}
	plan, err := delivery.Route(items, def, pref, cell)
	if err != nil {
		return err
	}
	var only []delivery.StaticItem
	for _, it := range plan.Static {
		if it.Kind == kind {
			only = append(only, it)
		}
	}
	require.NotEmpty(t, only, "%s routes no %v item — the matrix package carries nothing for it", def.Name, kind)
	plan.Static = only
	rec, err := fsstatic.NewRecords(afero.NewOsFs(), filepath.Join(t.TempDir(), "records"))
	require.NoError(t, err)
	lo := delivery.Loadout{Plan: plan, Package: pkg, Exports: exports, MCP: sessions.Endpoint{URL: "http://127.0.0.1:1/mcp", Credential: "matrix-bearer"}}
	_, err = fsstatic.New(safefs.New()).Deliver(context.Background(), lo, def, delivery.Target{
		Root: present.New(present.OnHost(cell)), Ownership: rec, Writer: delivery.SessionWriter("matrix"),
	})
	return err
}

// TestDeliveryApproach_EveryOfferedRootDeliversItsPayload is the core matrix:
// for every registered engine, every kind it delivers, and every root that
// kind's approach offers, the static writer delivers the matrix package with
// that root selected, and the kind's SENTINEL lands in the promised file
// beneath that root — and the OTHER root stays empty.
func TestDeliveryApproach_EveryOfferedRootDeliversItsPayload(t *testing.T) {
	isolatedRecords(t)
	isolatedLocks(t)
	t.Setenv("HOME", t.TempDir())
	var offered []string
	for _, name := range operations.EngineNames(engines.Registry()) {
		eng, ok := engines.Registry().Lookup(engine.Name(name))
		require.True(t, ok)
		def := eng.Root()
		for _, kind := range def.Static() {
			for _, root := range def.Surfaces()[kind].Traits().Roots {
				key := pairKey(name, kind, root.String())
				offered = append(offered, key)
				t.Run(key, func(t *testing.T) {
					spec, ok := deliverySpecs[key]
					require.True(t, ok, "%s is offered but has no promised destination", key)
					cell := newMatrixCell(t)
					require.NoError(t, deliverOne(t, eng, kind, root, cell.paths), "%s: delivery failed", key)

					at, other := cell.project, cell.home
					if root == present.RootSessionHome {
						at, other = cell.home, cell.project
					}
					if spec.noOp != "" {
						require.Empty(t, spec.wantFile, "%s: a noOp row names no file", key)
						assert.Empty(t, matrixTree(t, at), "%s is a no-op (%s) but wrote files", key, spec.noOp)
						assert.Empty(t, matrixTree(t, other), "%s is a no-op (%s) but wrote files", key, spec.noOp)
						return
					}
					tree := matrixTree(t, at)
					require.NotEmpty(t, tree, "%s: delivery reported success and wrote ZERO files", key)
					assert.Empty(t, matrixTree(t, other), "%s delivers beneath one root but wrote into the other", key)
					assertSentinelAt(t, key, tree, spec.wantFile, spec.wantSlot)
				})
			}
		}
	}
	sort.Strings(offered)
	stated := collections.SortedKeys(deliverySpecs)
	assert.Equal(t, stated, offered, "the offered (engine, kind, root) matrix and the promised destinations disagree")
}

// TestDeliveryApproach_HookLossesAgreeWithTheExports: every engine that
// delivers hooks delivers the hook on an event it carries, and its exports
// map exactly the unified events it does NOT declare lost
// (Definition.HookLosses) — so the loss report (operations.CapabilityLoss,
// which reads HookLosses) and what the engine says it carries agree. A lossy
// engine that also mapped a lost event would make the loss report a lie; one
// that mapped nothing would pass an "the lost event is unmapped" check alone.
func TestDeliveryApproach_HookLossesAgreeWithTheExports(t *testing.T) {
	isolatedRecords(t)
	isolatedLocks(t)
	t.Setenv("HOME", t.TempDir())
	sawALoss := false
	for _, name := range operations.EngineNames(engines.Registry()) {
		eng, _ := engines.Registry().Lookup(engine.Name(name))
		def := eng.Root()
		if !def.Carries(present.Hooks) {
			continue
		}
		sawALoss = sawALoss || len(def.HookLosses) > 0
		t.Run(name, func(t *testing.T) {
			cell := newMatrixCell(t)
			require.NoError(t, deliverOne(t, eng, present.Hooks, present.RootProjectRoot, cell.paths))
			assert.NotEmpty(t, findSentinel(matrixTree(t, cell.project), slotHookPreTool), "%s carries pre_tool, so its hook must land", name)

			exports, err := eng.Exports(matrixPackage(t).EngineItems(def.Name))
			require.NoError(t, err)
			for event := range def.HookLosses {
				assert.NotContains(t, exports.HookEvent, event, "%s declares %s lost, yet exports a native event for it", name, event)
			}
			assert.Contains(t, exports.HookEvent, "pre_tool", "%s carries pre_tool, so it exports a native event for it", name)
		})
	}
	require.True(t, sawALoss, "no registered engine declares a hook loss — the lossy arm is untested")
}

// TestDeliveryApproach_ClaudeSystemPromptIsTheFramedHashedFile pins the two
// facts the matrix's glob cannot express for claude's context at the session
// home: the leaf is <hash><SCMFramedContextSuffix>, and the payload is the
// FRAMED envelope rather than the bare context.
func TestDeliveryApproach_ClaudeSystemPromptIsTheFramedHashedFile(t *testing.T) {
	isolatedRecords(t)
	isolatedLocks(t)
	t.Setenv("HOME", t.TempDir())
	eng, ok := engines.Registry().Lookup("claude-code")
	require.True(t, ok)
	cell := newMatrixCell(t)
	require.NoError(t, deliverOne(t, eng, present.Context, present.RootSessionHome, cell.paths))

	assert.Empty(t, matrixTree(t, cell.project), "the system prompt must not touch the project root")
	tree := matrixTree(t, cell.home)
	hits := findSentinel(tree, slotContext)
	require.Len(t, hits, 1, "the framed file carries the context (home tree: %v)", collections.SortedKeys(tree))
	assert.True(t, strings.HasSuffix(hits[0], agent.SCMFramedContextSuffix), "the framed file is named <hash>%s, got %s", agent.SCMFramedContextSuffix, hits[0])
	assert.Contains(t, tree[hits[0]], agent.FrameProjectContext(slotContext), "the file carries the FRAMED envelope, not the bare context")
}

// TestDeliveryApproach_SessionHomeRefusesAnUnrootedRun: with no session home
// advised there is nowhere private to write, and a delivery selecting it is
// REFUSED rather than falling back to the project's well-known file — the
// fallback is what once turned an isolated launch's system prompt into
// project memory.
func TestDeliveryApproach_SessionHomeRefusesAnUnrootedRun(t *testing.T) {
	isolatedRecords(t)
	isolatedLocks(t)
	t.Setenv("HOME", t.TempDir())
	eng, ok := engines.Registry().Lookup("claude-code")
	require.True(t, ok)
	project := t.TempDir()
	err := deliverOne(t, eng, present.Context, present.RootSessionHome, present.Paths{ProjectRoot: present.Root{Host: project, Engine: project}})
	require.Error(t, err, "an unrooted run must be refused, never served the project file")
	assert.Empty(t, matrixTree(t, project), "a refused delivery writes zero files")
}

// matrixTree snapshots every file under root as slash-relpath -> contents.
func matrixTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info == nil || info.IsDir() {
			return nil //nolint:nilerr // a missing root means "nothing delivered", which the caller asserts on
		}
		b, readErr := os.ReadFile(path)
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

// assertSentinelAt asserts sentinel is in want (a relpath, or "<dir>/*"
// matching exactly one file in that directory) and in no other file.
func assertSentinelAt(t *testing.T, key string, tree map[string]string, want, sentinel string) {
	t.Helper()
	hits := findSentinel(tree, sentinel)
	if strings.HasSuffix(want, "/*") {
		dir := strings.TrimSuffix(want, "/*")
		require.Len(t, hits, 1, "%s: %s must reach exactly one file (tree: %v)", key, sentinel, collections.SortedKeys(tree))
		assert.Equal(t, dir, filepath.ToSlash(filepath.Dir(hits[0])), "%s: %s landed outside %s/", key, sentinel, dir)
		return
	}
	content, ok := tree[want]
	require.True(t, ok, "%s: the approach promises %s but it was not written (tree: %v)", key, want, collections.SortedKeys(tree))
	assert.Contains(t, content, sentinel, "%s: %s exists but does not carry %s", key, want, sentinel)
	assert.Equal(t, []string{want}, hits, "%s: %s must reach exactly the promised destination and no other file", key, sentinel)
}
