package operations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// materializeFixture builds an isolated project whose `reviewer` profile
// tag-selects a fragment carrying MARK, plus a fresh target dir. Reuses the
// regen helpers (same package): a real appDir the exposure loader reads from.
func materializeFixture(t *testing.T, mark string) (cfg *config.Config, target string) {
	t.Helper()
	testsupport.Isolate(t) // junk HOME + temp cwd: no host-config leak, no source-tree writes
	appDir, _ := regenTestApp(t)
	writeRegenBundle(t, appDir, "dev", `version: "1.0"
fragments:
  rules:
    tags: ["security"]
    content: "`+mark+`"
`)
	cfg = cfgWithDirProfiles(t, afero.NewOsFs(), appDir, map[string]config.Profile{
		"reviewer": {SelectTags: []string{"security"}},
	}, config.Fixture{})
	return cfg, t.TempDir()
}

// TestMaterializeProfile_WritesClaudeMd proves the core surface: the assembled
// profile context lands in <target>/CLAUDE.md, the backend defaults to
// claude-code, and settings/mcp are written.
func TestMaterializeProfile_WritesClaudeMd(t *testing.T) {
	cfg, target := materializeFixture(t, "MATERIALIZED-CONTENT")

	res, err := MaterializeProfile(context.Background(), cfg, MaterializeProfileRequest{
		Profiles: []string{"reviewer"},
		Target:   target,
	})
	require.NoError(t, err)
	assert.Equal(t, "claude-code", res.Backend, "backend defaults to claude-code")
	assert.Contains(t, res.Wrote, "context", "the assembled context surface is reported")

	data, err := os.ReadFile(filepath.Join(target, "CLAUDE.md"))
	require.NoError(t, err, "CLAUDE.md must be written to the target dir")
	assert.Contains(t, string(data), "MATERIALIZED-CONTENT",
		"the assembled fragment block is the CLAUDE.md payload")
}

// TestMaterializeProfile_KeepsHomeShadowedCommand is the end-to-end
// regression: `profile materialize --target` must produce a PORTABLE,
// self-contained tree, so a builtin command (e.g. "discover") that happens to
// be byte-identical to a file already sitting in the MATERIALIZING machine's
// own ~/.claude/commands must still land in --target. Pre-fix, claude's
// DeliverCommands unconditionally deduped against GlobalCommandsDir(), silently
// dropping it — exactly the observed cr-correctness bug (3 built-ins missing
// for claude-code only and present for every other engine, because this host
// happened to already have them installed under ~/.claude/commands).
func TestMaterializeProfile_KeepsHomeShadowedCommand(t *testing.T) {
	cfg, target := materializeFixture(t, "X")

	// Render the "discover" builtin command exactly as materialize itself will
	// (the same package projection profile_materialize.go drives),
	// and pre-seed a byte-identical copy into $HOME/.claude/commands — simulating
	// a materializing host that has already installed its own commands (e.g. via
	// `manage hooks install`), which the --target launch environment does NOT
	// share.
	pkg, err := AssemblePackage(context.Background(), cfg, PackageRequest{Profiles: []string{"reviewer"}})
	require.NoError(t, err)
	engineExports, err := ExportsFor(pkg, "claude-code")
	require.NoError(t, err)
	exports := CommandExportsOf(engineExports)
	var seeded bool
	for _, e := range exports {
		if e.Name != "discover" {
			continue
		}
		home := filepath.Join(os.Getenv("HOME"), ".claude", "commands")
		require.NoError(t, os.MkdirAll(home, 0o755))
		require.NoError(t, os.WriteFile(
			filepath.Join(home, "discover.md"), []byte(claude.TransformToClaudeCommand(e)), 0o644))
		seeded = true
		break
	}
	require.True(t, seeded, "precondition: the discover builtin command must be among the exports")

	res, err := MaterializeProfile(context.Background(), cfg, MaterializeProfileRequest{
		Profiles: []string{"reviewer"}, Target: target,
	})
	require.NoError(t, err)
	assert.Contains(t, res.Wrote, "commands")

	assert.FileExists(t, filepath.Join(target, ".claude", "commands", "discover.md"),
		"a command byte-identical to one in the materializing host's ~/.claude/commands must still land in the portable --target tree")
}

// TestMaterializeProfile_RefusesARetiredShortSpelling: `claude` is not an
// engine name and no alias maps it to one, so the request is refused rather
// than materialized under a backend the caller did not name.
func TestMaterializeProfile_RefusesARetiredShortSpelling(t *testing.T) {
	cfg, target := materializeFixture(t, "X")
	_, err := MaterializeProfile(context.Background(), cfg, MaterializeProfileRequest{
		Profiles: []string{"reviewer"}, Target: target, Backend: "claude",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown backend "claude"`)
}

// TestMaterializeProfile_OverwritesEachRun proves the export is the source of
// truth: a second run produces a byte-identical CLAUDE.md (deterministic, full
// overwrite — no append/duplication).
func TestMaterializeProfile_OverwritesEachRun(t *testing.T) {
	cfg, target := materializeFixture(t, "ONCE")

	req := MaterializeProfileRequest{Profiles: []string{"reviewer"}, Target: target}
	_, err := MaterializeProfile(context.Background(), cfg, req)
	require.NoError(t, err)
	first, err := os.ReadFile(filepath.Join(target, "CLAUDE.md"))
	require.NoError(t, err)

	_, err = MaterializeProfile(context.Background(), cfg, req)
	require.NoError(t, err)
	second, err := os.ReadFile(filepath.Join(target, "CLAUDE.md"))
	require.NoError(t, err)

	assert.Equal(t, string(first), string(second), "re-materialize is a clean overwrite")
}

// TestMaterializeProfile_ExportsCtxloomsOwnMCPServer proves a materialized
// export carries NO entry for ctxloom's own server: the companion loadout's
// entry is the session-endpoint declaration, rendered only inside a session
// (the runner binds the endpoint; delivery writes its URL and bearer into
// the session's registry). At rest there is nothing to render, and an entry
// written anyway would name a command that speaks no protocol.
func TestMaterializeProfile_ExportsCtxloomsOwnMCPServer(t *testing.T) {
	cfg, target := materializeFixture(t, "X")
	cfg = withCtxloomLoadout(t, cfg)

	_, err := MaterializeProfile(context.Background(), cfg, MaterializeProfileRequest{
		Profiles: []string{"reviewer"}, Target: target,
	})
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(target, ".mcp.json"))
	if os.IsNotExist(err) {
		return // no registry at all: nothing under ctxloom's name, in the strongest form
	}
	require.NoError(t, err)
	var doc struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	require.NoError(t, json.Unmarshal(data, &doc))
	_, ok := doc.MCPServers[agent.MCPServerName]
	assert.False(t, ok, "ctxloom's own MCP server is served by the session and never exported at rest; got %v", doc.MCPServers)
}

// materializeHookFixture is materializeFixture with the selected profile shipping
// a session_start hook — the "team ships a guardrail" shape a prior defect was
// filed about.
//
// "reviewer" becomes a DIRECTORY profile here rather than an inline one, because
// a directory profile is the only place a hook can be declared. It carries the
// same select_tags, so the assembled content is unchanged and only the hook is
// added.
func materializeHookFixture(t *testing.T) (cfg *config.Config, target string) {
	t.Helper()
	cfg, target = materializeFixture(t, "HOOKED-CONTENT")
	f := cfg.ToFixture()
	require.NotEmpty(t, f.AppPaths, "materializeFixture must supply an app dir to seed the profile into")
	profilesDir := paths.ProfilesPath(f.AppPaths[0])
	require.NoError(t, os.MkdirAll(profilesDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "reviewer.yaml"), []byte(
		"select_tags:\n  - security\nhooks:\n  unified:\n    session_start:\n      - type: command\n        command: echo team-guardrail\n",
	), 0o644))
	return gatedFixture(f), target
}

// TestMaterializeProfile_ReportsHooksAnEngineCannotCarry is the
// characterization: the lossy mock engine (config.BackendMockLossy) declares no
// hook mechanism, so a profile's session_start hook lands NOWHERE — and pre-fix the report said only "wrote
// context / settings / commands / skills", every line true and the loss absent
// from all of them. A reader could not tell "this engine has no hooks" from
// "this profile declared no hooks"; both were silence.
//
// The report must now carry the loss STRUCTURALLY, so `--format json` consumers
// see it too.
func TestMaterializeProfile_ReportsHooksAnEngineCannotCarry(t *testing.T) {
	cfg, target := materializeHookFixture(t)

	res, err := MaterializeProfile(context.Background(), cfg, MaterializeProfileRequest{
		Profiles: []string{"reviewer"}, Target: target, Backend: config.BackendMockLossy,
	})
	require.NoError(t, err, "the loss is REPORTED, not fatal: the rest of the tree is still worth having")
	require.Contains(t, res.Wrote, "context",
		"precondition: the engine's own surfaces ARE written — the hook is what does not ride them")

	require.Len(t, res.NotCarried, 1,
		"the engine's one structural loss (hooks) must appear in the report")
	assert.Equal(t, "hooks", res.NotCarried[0].Surface)
	assert.Contains(t, res.NotCarried[0].Detail, "session_start",
		"the report must name WHICH hooks were dropped, not just that some were")
	assert.NotEmpty(t, res.NotCarried[0].Reason,
		"a loss with no stated reason is indistinguishable from a bug")
}

// TestMaterializeProfile_ReportsNoLossForAnEngineThatCarriesHooks is the other
// half: the loss report must be silent when there IS no loss. claude-code writes
// the same hook into .claude/settings.json, so a "not carried" line there would
// be a false alarm — and a report that cries wolf gets ignored, taking the real
// losses with it.
func TestMaterializeProfile_ReportsNoLossForAnEngineThatCarriesHooks(t *testing.T) {
	cfg, target := materializeHookFixture(t)

	res, err := MaterializeProfile(context.Background(), cfg, MaterializeProfileRequest{
		Profiles: []string{"reviewer"}, Target: target, Backend: "claude-code",
	})
	require.NoError(t, err)
	assert.Empty(t, res.NotCarried, "claude-code carries hooks; nothing is lost")

	data, err := os.ReadFile(filepath.Join(target, ".claude", "settings.json"))
	require.NoError(t, err, "precondition: claude-code's settings surface must exist")
	assert.Contains(t, string(data), "team-guardrail",
		"precondition: the hook this test says is NOT lost must actually be delivered")
}

// TestMaterializeProfile_ReportsNoHookLossWhenNoHooksDeclared pins the third
// case: a hook-less engine still cannot carry hooks, but a profile that declares none has
// lost nothing. Reporting a capability gap nobody asked to use is noise, and the
// same rule the unified hook router already applies (RouteUnifiedHooks warns
// only when hooks of the unsupported kind were actually configured).
func TestMaterializeProfile_ReportsNoHookLossWhenNoHooksDeclared(t *testing.T) {
	cfg, target := materializeFixture(t, "NO-HOOKS")

	res, err := MaterializeProfile(context.Background(), cfg, MaterializeProfileRequest{
		Profiles: []string{"reviewer"}, Target: target, Backend: "mock",
	})
	require.NoError(t, err)
	assert.Empty(t, res.NotCarried,
		"a gap nobody asked to use costs nothing and must stay quiet")
}

// TestMaterializeProfile_WritesSkills is the PERSISTENT-path proof of the
// skill/command split's Part B3-seam: a directory profile's bundle-shipped
// Agent Skill package lands at <target>/.claude/skills/<name>/SKILL.md (+
// sibling files, exec bit preserved) — the same materialize call that writes
// CLAUDE.md/.mcp.json/settings/commands (TestMaterializeProfile_WritesClaudeMd)
// now also reports and writes the skills surface. This exercises
// backends.SkillExportsFor + backends.LoadSkillExports end to end through a
// REAL bundle-shipped skill (config.ResolveBundleSkills), unlike the claude
// package's unit tests, which drive Surfaces.Skills directly against a
// synthetic agent.SkillExport fixture — together they cover both the live
// (claude package) and persistent (here) delivery paths the plan calls for.
func TestMaterializeProfile_WritesSkills(t *testing.T) {
	testsupport.Isolate(t)
	appDir, _ := regenTestApp(t)
	profilesDir := filepath.Join(appDir, "profiles")
	require.NoError(t, os.MkdirAll(profilesDir, 0755))
	bundlesDir := authoredV1(appDir)
	skillDir := filepath.Join(bundlesDir, "skill-bundle", "skills", "humanize")
	require.NoError(t, os.MkdirAll(filepath.Join(skillDir, "scripts"), 0755))

	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "skilled.yaml"),
		[]byte("name: skilled\nbundles:\n  - skill-bundle\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "skill-bundle", "bundle.yaml"),
		[]byte("version: \"1.0\"\nskills:\n  humanize:\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: humanize\ndescription: Removes AI writing tells.\n---\n\nInstructions body.\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "scripts", "run.sh"),
		[]byte("#!/bin/sh\necho hi\n"), 0755))

	// A skills-only bundle assembles no context text on its own; ctxloom's
	// own loadout supplies the always-on guidance a real materialize carries.
	cfg := withCtxloomLoadout(t, gatedFixture(config.Fixture{AppPaths: []string{appDir}}))
	target := t.TempDir()

	res, err := MaterializeProfile(context.Background(), cfg, MaterializeProfileRequest{
		Profiles: []string{"skilled"}, Target: target,
	})
	require.NoError(t, err)
	assert.Contains(t, res.Wrote, "skills", "the skills surface is reported as written")

	data, err := os.ReadFile(filepath.Join(target, ".claude", "skills", "humanize", "SKILL.md"))
	require.NoError(t, err, "the bundle-shipped skill's SKILL.md must land in the portable --target tree")
	assert.Contains(t, string(data), "Instructions body.")

	info, err := os.Stat(filepath.Join(target, ".claude", "skills", "humanize", "scripts", "run.sh"))
	require.NoError(t, err, "the skill's scripts/run.sh must be materialized")
	assert.Equal(t, os.FileMode(0755), info.Mode().Perm(), "the exec bit survives the persistent materialize path")
}

// TestMaterializeProfile_WritesSkills_MockBackend is the HERMETIC-VEHICLE
// twin of the test above: the same real bundle-shipped skill, materialized
// with --backend mock, must land under mock's own skills directory with the
// modes its bundle manifest DECLARES.
//
// It exists because eight rows of J001400's delivery matrix assert
// skills/reviewer/SKILL.md at 0644 and skills/reviewer/scripts/run.sh at 0755
// at a path an agent can read, and `profile materialize --backend mock` is the
// vehicle those rows retarget onto. Without a mock
// skills surface there was no hermetic way to run them at all.
//
// The manifest here is AUTHORED (bundle.yaml's `files:`), not derived: 0755 on
// scripts/run.sh is a declaration inside the bundle's own signed metadata, and
// that declaration is what the delivered file's mode must equal. The tree on
// disk is written to agree with it because the LOADER refuses a package whose
// declaration and tree disagree (bundles.VerifyExtractedManifest) — a refusal,
// not a re-derivation.
func TestMaterializeProfile_WritesSkills_MockBackend(t *testing.T) {
	testsupport.Isolate(t)
	appDir, _ := regenTestApp(t)
	profilesDir := filepath.Join(appDir, "profiles")
	require.NoError(t, os.MkdirAll(profilesDir, 0755))
	bundlesDir := authoredV1(appDir)
	skillDir := filepath.Join(bundlesDir, "skill-bundle", "skills", "reviewer")
	require.NoError(t, os.MkdirAll(filepath.Join(skillDir, "scripts"), 0755))

	skillMD := []byte("---\nname: reviewer\ndescription: Reviews things.\n---\n\nREVIEWER-BODY-51ab\n")
	script := []byte("#!/bin/sh\necho REVIEWER-SCRIPT-51ab\n")

	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "skilled.yaml"),
		[]byte("name: skilled\nbundles:\n  - skill-bundle\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), skillMD, 0644))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "scripts", "run.sh"), script, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "skill-bundle", "bundle.yaml"),
		[]byte("version: \"1.0\"\nskills:\n  reviewer:\n    files:\n"+
			"      SKILL.md:\n        sha256: "+sha256Of(skillMD)+"\n        mode: \"0644\"\n"+
			"      scripts/run.sh:\n        sha256: "+sha256Of(script)+"\n        mode: \"0755\"\n"), 0644))

	// A skills-only bundle assembles no context text on its own; ctxloom's
	// own loadout supplies the always-on guidance a real materialize carries.
	cfg := withCtxloomLoadout(t, gatedFixture(config.Fixture{AppPaths: []string{appDir}}))
	target := t.TempDir()

	res, err := MaterializeProfile(context.Background(), cfg, MaterializeProfileRequest{
		Profiles: []string{"skilled"}, Target: target, Backend: "mock",
	})
	require.NoError(t, err)
	assert.Contains(t, res.Wrote, "skills", "the mock skills surface is reported as written")

	skillsRoot := filepath.Join(target, ".mock", "skills", "reviewer")

	data, err := os.ReadFile(filepath.Join(skillsRoot, "SKILL.md"))
	require.NoError(t, err, "mock must materialize the bundle-shipped SKILL.md, not merely report it")
	assert.Contains(t, string(data), "REVIEWER-BODY-51ab", "the delivered bytes must be the package's own")

	runSh := filepath.Join(skillsRoot, "scripts", "run.sh")
	got, err := os.ReadFile(runSh)
	require.NoError(t, err, "a sibling file under scripts/ must be materialized too")
	assert.Contains(t, string(got), "REVIEWER-SCRIPT-51ab")

	doc, err := os.Stat(filepath.Join(skillsRoot, "SKILL.md"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0644), doc.Mode().Perm(), "SKILL.md is declared 0644")

	info, err := os.Stat(runSh)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0755), info.Mode().Perm(),
		"scripts/run.sh is DECLARED 0755 in the bundle manifest; a delivered script without its exec bit cannot run")
}

// sha256Of renders a fixture file's content hash in the form a bundle.yaml
// skill manifest records it.
func sha256Of(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// TestMaterializeProfile_Validation covers the guard rails.
func TestMaterializeProfile_Validation(t *testing.T) {
	ctx := context.Background()
	cfg := gatedFixture(config.Fixture{AppPaths: []string{t.TempDir()}})

	_, err := MaterializeProfile(ctx, cfg, MaterializeProfileRequest{Profiles: []string{"p"}})
	assert.Error(t, err, "missing target is rejected")

	_, err = MaterializeProfile(ctx, cfg, MaterializeProfileRequest{Target: t.TempDir()})
	assert.Error(t, err, "missing profiles is rejected")

	_, err = MaterializeProfile(ctx, cfg, MaterializeProfileRequest{
		Profiles: []string{"p"}, Target: t.TempDir(), Backend: "bogus",
	})
	assert.Error(t, err, "unknown backend is rejected")

	_, err = MaterializeProfile(ctx, nil, MaterializeProfileRequest{
		Profiles: []string{"p"}, Target: t.TempDir(),
	})
	assert.Error(t, err, "nil config is rejected")
}

// TestResolveMaterializeTarget_AcceptsOnlyTheRegisteredName pins the resolver
// to the registry's exact vocabulary. An engine has one name: the registered
// spelling resolves and is what the result reports; the retired short
// spellings and case variants are unknown backends, refused rather than
// rounded to the engine. resolveMaterializeTarget once carried its own
// one-entry alias table (`backend == "claude"`) — the second copy that
// drifts — so the refusals here are what keep one from growing back.
func TestResolveMaterializeTarget_AcceptsOnlyTheRegisteredName(t *testing.T) {
	cfg := gatedFixture(config.Fixture{AppPaths: []string{t.TempDir()}})

	got, err := resolveMaterializeTarget(cfg, MaterializeProfileRequest{
		Target: t.TempDir(), Profiles: []string{"p"}, Backend: "claude-code",
	})
	require.NoError(t, err)
	assert.Equal(t, "claude-code", got, "the resolved backend is the registered name the result reports")

	for _, spelling := range []string{"claude", "claudecode", "CLAUDE", "Claude-Code"} {
		t.Run(spelling, func(t *testing.T) {
			_, err := resolveMaterializeTarget(cfg, MaterializeProfileRequest{
				Target: t.TempDir(), Profiles: []string{"p"}, Backend: spelling,
			})
			require.Error(t, err, "%q is not a registered backend name and must be refused", spelling)
			assert.Contains(t, err.Error(), "unknown backend")
		})
	}

	// The empty request still means the default, which must itself be a
	// registered name — an unregistered default would make every unqualified
	// materialize report a name no registry key matches.
	got, err = resolveMaterializeTarget(cfg, MaterializeProfileRequest{
		Target: t.TempDir(), Profiles: []string{"p"},
	})
	require.NoError(t, err)
	assert.Equal(t, DefaultMaterializeBackend, got, "an unspecified backend means the default")
	assert.True(t, EngineExists(DefaultMaterializeBackend), "the default backend constant must itself be a registered name")
}

// A premise-withheld fragment must be REPORTED, not silently dropped.
//
// The assertion is deliberately on the RESULT and not on the context file's
// contents. "The marker is absent from the context" is the vacuous form: it
// passes when the fragment was withheld, and it passes just as happily when
// the fragment never existed, when the bundle failed to load, or when the
// whole assembly produced nothing. That vacuity is exactly what let this defect
// live — 22 scenarios across four features failed on an absent marker with
// clean exits, and two agent runs were spent before anyone could see why.
//
// It also pins the SECOND fact, which is the one that makes the report honest
// rather than alarming: where the withheld fragment actually went. On a
// skills-capable engine it is re-delivered as a skill package, so a withhold is
// not a loss. Reporting the withhold without the destination would turn every
// correct materialization into a false alarm.
func TestMaterializeProfile_ReportsAFragmentWithheldByItsPremise(t *testing.T) {
	testsupport.Isolate(t)
	appDir, _ := regenTestApp(t)
	profilesDir := filepath.Join(appDir, "profiles")
	require.NoError(t, os.MkdirAll(profilesDir, 0755))
	bundlesDir := authoredV1(appDir)
	require.NoError(t, os.MkdirAll(filepath.Join(bundlesDir, "premise-bundle"), 0755))

	// THE KEY DIFFERS BY BUNDLE FORMAT, and getting it wrong makes this test
	// pass vacuously rather than fail: a flat v1 bundle.yaml carries the
	// condition as `premise:` (bundles.BundleFragment's yaml tag), while the v2
	// TREE format carries it as `description:` and maps it across in
	// tree_read's `Premise: v.Description`. This is an authored v1 bundle, so
	// `description:` here would be read as a description and withhold nothing.
	require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "premise-bundle", "bundle.yaml"),
		[]byte("version: \"1.0\"\nfragments:\n"+
			"  always-applies:\n    content: \"UNCONDITIONAL-MARKER\"\n"+
			"  only-sometimes:\n    premise: \"You are about to cut a release.\"\n    content: \"PREMISED-MARKER\"\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "premised.yaml"),
		[]byte("name: premised\nbundles:\n  - premise-bundle\n"), 0644))

	cfg := gatedFixture(config.Fixture{AppPaths: []string{appDir}})
	target := t.TempDir()

	res, err := MaterializeProfile(context.Background(), cfg, MaterializeProfileRequest{
		Profiles: []string{"premised"}, Target: target,
	})
	require.NoError(t, err)

	// THE ASSERTION THIS TEST EXISTS FOR: the withhold is named in the result.
	var found *PremiseWithhold
	for i := range res.WithheldByPremise {
		if strings.Contains(res.WithheldByPremise[i].Name, "only-sometimes") {
			found = &res.WithheldByPremise[i]
		}
	}
	require.NotNil(t, found,
		"the premise-withheld fragment is not named in the result; materialize would exit 0 reporting every surface written while that content reached the agent by no route it announced. got: %+v", res.WithheldByPremise)
	assert.Equal(t, "You are about to cut a release.", found.Premise,
		"the report must carry WHY it was withheld, not merely that it was — a name alone sends the reader back to the bundle to find out")
	assert.Equal(t, "skills", found.Delivered,
		"claude has a skills surface, so the withheld fragment was re-delivered as a skill package; omitting that turns a correct materialization into a false alarm")

	// The unpremised fragment is unaffected: absence of a premise asserts that
	// it always applies, so it must NOT appear in the withhold report.
	for _, w := range res.WithheldByPremise {
		assert.NotContains(t, w.Name, "always-applies",
			"a fragment with no premise is unconditional and must never be reported as withheld")
	}
}

// THE DUMP ARM, which had no subject in the registry until mock-noskills
// existed and was therefore correct by inspection and asserted by nothing.
//
// A materialized surface is ctxloom OUT OF THE LOOP: a premise-withheld
// fragment cannot be pulled later, so it is LOST rather than deferred. Where the
// engine has a skills surface it is handed over as a skill package; where it has
// none, the assembly must run STATIC and put the fragment in the context
// instead. This asserts the second case.
//
// The pairing with the skills-capable test above is the point. Asserting only
// the skills arm proves the half that already worked, and this project's rule is
// that the untested arm is the entire defect.
func TestMaterializeProfile_NoSkillsEngineDumpsAPremisedFragmentIntoContext(t *testing.T) {
	testsupport.Isolate(t)
	appDir, _ := regenTestApp(t)
	profilesDir := filepath.Join(appDir, "profiles")
	require.NoError(t, os.MkdirAll(profilesDir, 0755))
	bundlesDir := authoredV1(appDir)
	require.NoError(t, os.MkdirAll(filepath.Join(bundlesDir, "premise-bundle-2"), 0755))

	// `premise:` is the flat v1 key; the v2 tree format uses `description:`.
	require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "premise-bundle-2", "bundle.yaml"),
		[]byte("version: \"1.0\"\nfragments:\n"+
			"  only-sometimes:\n    premise: \"You are about to cut a release.\"\n    content: \"PREMISED-MARKER-DUMPED\"\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "premised2.yaml"),
		[]byte("name: premised2\nbundles:\n  - premise-bundle-2\n"), 0644))

	cfg := gatedFixture(config.Fixture{AppPaths: []string{appDir}})
	target := t.TempDir()

	res, err := MaterializeProfile(context.Background(), cfg, MaterializeProfileRequest{
		Profiles: []string{"premised2"}, Target: target, Backend: "mock-noskills",
	})
	require.NoError(t, err)

	body, err := os.ReadFile(filepath.Join(target, "MOCK_CONTEXT.md"))
	require.NoError(t, err, "the no-skills engine's context file must exist")
	assert.Contains(t, string(body), "PREMISED-MARKER-DUMPED",
		"an engine with NO skills surface must receive the premised fragment IN THE CONTEXT; withholding it here loses the content outright, because a materialized surface has no way to pull it later")

	assert.Empty(t, res.WithheldByPremise,
		"nothing was withheld — the assembly ran static — so the withhold report must be empty. Reporting a withhold here would be a false alarm about content that WAS delivered")
}
