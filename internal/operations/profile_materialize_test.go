package operations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/claude"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
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
// for claude-code only, present for antigravity/kiro/codex, because this host
// happened to already have them installed under ~/.claude/commands).
func TestMaterializeProfile_KeepsHomeShadowedCommand(t *testing.T) {
	cfg, target := materializeFixture(t, "X")

	// Render the "discover" builtin command exactly as materialize itself will (same
	// LoadCommandExports/CommandExportsFor pipeline profile_materialize.go drives),
	// and pre-seed a byte-identical copy into $HOME/.claude/commands — simulating
	// a materializing host that has already installed its own commands (e.g. via
	// `manage hooks install`), which the --target launch environment does NOT
	// share.
	exports := backends.CommandExportsFor("claude-code", backends.LoadCommandExports(cfg, []string{"reviewer"}))
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

// TestMaterializeProfile_BackendAlias proves `claude` maps to claude-code.
func TestMaterializeProfile_BackendAlias(t *testing.T) {
	cfg, target := materializeFixture(t, "X")
	res, err := MaterializeProfile(context.Background(), cfg, MaterializeProfileRequest{
		Profiles: []string{"reviewer"}, Target: target, Backend: "claude",
	})
	require.NoError(t, err)
	assert.Equal(t, "claude-code", res.Backend)
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

// TestMaterializeProfile_ExportsCtxloomsOwnMCPServer proves the builtin
// ctxloom bundle reaches a materialized export: the exported .mcp.json carries
// ctxloom's own server, resolved to an absolute ctxloom path rather than the
// bare name the bundle declares. No profile names the bundle — builtins are
// injected unconditionally — so this is also the proof that the injection route
// carries MCP.
func TestMaterializeProfile_ExportsCtxloomsOwnMCPServer(t *testing.T) {
	cfg, target := materializeFixture(t, "X")

	res, err := MaterializeProfile(context.Background(), cfg, MaterializeProfileRequest{
		Profiles: []string{"reviewer"}, Target: target,
	})
	require.NoError(t, err)
	assert.Contains(t, res.Wrote, "mcp")

	data, err := os.ReadFile(filepath.Join(target, ".mcp.json"))
	require.NoError(t, err, "the backend MCP config must be written")
	var doc struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	require.NoError(t, json.Unmarshal(data, &doc))
	entry, ok := doc.MCPServers[agent.MCPServerName]
	require.True(t, ok, "ctxloom's own MCP server must be exported; got %v", doc.MCPServers)
	assert.Equal(t, agent.CtxloomMCPArgs, entry.Args, "the exported entry must invoke `mcp serve`")
	assert.Equal(t, agent.CtxloomCommand(), entry.Command,
		"the exported command must be the binary that materialized the surface, not the bare name the bundle declares")
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
	return config.NewFixture(f), target
}

// TestMaterializeProfile_ReportsHooksAnEngineCannotCarry is the
// characterization: opencode has no hook mechanism at all, so a profile's
// session_start hook lands NOWHERE — and pre-fix the report said only "wrote
// context / settings / commands / skills", every line true and the loss absent
// from all of them. A reader could not tell "this engine has no hooks" from
// "this profile declared no hooks"; both were silence.
//
// The report must now carry the loss STRUCTURALLY, so `--format json` consumers
// see it too.
func TestMaterializeProfile_ReportsHooksAnEngineCannotCarry(t *testing.T) {
	cfg, target := materializeHookFixture(t)

	res, err := MaterializeProfile(context.Background(), cfg, MaterializeProfileRequest{
		Profiles: []string{"reviewer"}, Target: target, Backend: "opencode",
	})
	require.NoError(t, err, "the loss is REPORTED, not fatal: the rest of the tree is still worth having")
	require.Contains(t, res.Wrote, "settings",
		"precondition: opencode's folded settings surface IS written — the hook is what does not ride it")

	require.Len(t, res.NotCarried, 1,
		"opencode's one structural loss (hooks) must appear in the report")
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
// opencode case with it.
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
// case: opencode still cannot carry hooks, but a profile that declares none has
// lost nothing. Reporting a capability gap nobody asked to use is noise, and the
// same rule the unified hook router already applies (RouteUnifiedHooks warns
// only when hooks of the unsupported kind were actually configured).
func TestMaterializeProfile_ReportsNoHookLossWhenNoHooksDeclared(t *testing.T) {
	cfg, target := materializeFixture(t, "NO-HOOKS")

	res, err := MaterializeProfile(context.Background(), cfg, MaterializeProfileRequest{
		Profiles: []string{"reviewer"}, Target: target, Backend: "opencode",
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
	bundlesDir := paths.LocalBundlesPath(appDir)
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

	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
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
	bundlesDir := paths.LocalBundlesPath(appDir)
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

	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
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
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{t.TempDir()}})

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

// TestMaterializeProfile_SurfaceOverrideChangesWhereContextLands is the claim
// the whole --surface flag rests on: an override has to REACH delivery.
//
// Written after a surviving mutation. Deleting the loop that copies
// req.Surfaces onto the selection — so every override was parsed, validated and
// then silently dropped — passed this package's entire suite. The flag would
// have shipped accepting a value, reporting success, and changing nothing:
// exactly the accepted-and-ignored shape j000500_editor.feature exists to catch on a
// different command.
//
// claude-code is the engine that can show it, because its context surface is
// the only one offering more than one approach. Default is unsafe-file, so the
// assembled context lands in CLAUDE.md; asked for the hook instead, it must not.
func TestMaterializeProfile_SurfaceOverrideChangesWhereContextLands(t *testing.T) {
	cfg, target := materializeFixture(t, "OVERRIDE-ROUTED-CONTENT")

	// Baseline: the default approach writes the context as a file.
	_, err := MaterializeProfile(context.Background(), cfg, MaterializeProfileRequest{
		Profiles: []string{"reviewer"},
		Target:   target,
	})
	require.NoError(t, err)
	def, err := os.ReadFile(filepath.Join(target, "CLAUDE.md"))
	require.NoError(t, err, "the default context approach writes CLAUDE.md; without that this test proves nothing")
	require.Contains(t, string(def), "OVERRIDE-ROUTED-CONTENT")

	// Same profile, same engine, context routed through the hook instead.
	overridden := t.TempDir()
	_, err = MaterializeProfile(context.Background(), cfg, MaterializeProfileRequest{
		Profiles: []string{"reviewer"},
		Target:   overridden,
		Surfaces: map[agent.SurfaceKind]agent.Approach{
			agent.SurfaceContext: agent.ApproachHook,
		},
	})
	require.NoError(t, err)

	body, rerr := os.ReadFile(filepath.Join(overridden, "CLAUDE.md"))
	if rerr == nil {
		assert.NotContains(t, string(body), "OVERRIDE-ROUTED-CONTENT",
			"context was routed to the hook, so the native context file must not carry the assembled payload")
	}
}
