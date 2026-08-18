package operations

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// This file rounds out ISO4's coverage per the release-backlog verification
// pass: direct, payload-asserting unit tests for the three helper functions
// buildSessionInitSummary leans on (namesOrCount, listSessionSkillNames,
// MCPServerNames) — none of which had a dedicated test before, only
// indirect exercise through buildSessionInitSummary/OpenEngineSession — plus
// a real end-to-end proof that the fragments/commands lines in the summary
// are the WIRING's own resolved values (OpenEngineSession's fragmentsLoaded/
// commandNames locals), not just buildSessionInitSummary's formatting.

// --- namesOrCount: the cap boundary, directly ------------------------------

// TestNamesOrCount_BoundaryAtCap pins the off-by-one this function's whole
// purpose depends on: nameListCap (8) items must still print every name, and
// nameListCap+1 must collapse to a count + the CLI pointer — tested directly
// against the function, not filtered through buildSessionInitSummary's other
// formatting.
func TestNamesOrCount_BoundaryAtCap(t *testing.T) {
	names := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = "item" + string(rune('a'+i))
		}
		return out
	}

	cases := []struct {
		name string
		n    int
		want string
	}{
		{"empty set is a distinct 'none', not '0 ()'", 0, "none"},
		{"one below the cap prints in full", nameListCap - 1, "7 (itema, itemb, itemc, itemd, iteme, itemf, itemg)"},
		{"exactly at the cap still prints in full", nameListCap, "8 (itema, itemb, itemc, itemd, iteme, itemf, itemg, itemh)"},
		{"one past the cap collapses to a count", nameListCap + 1, "9 (see `ctxloom widget list`)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := namesOrCount(listed(names(tc.n)), "ctxloom widget list")
			assert.Equal(t, tc.want, got)
		})
	}
}

// --- MCPServerNames: direct payload proofs -------------------------------

// TestMCPServerNames_EmptyIsNil proves the zero-servers case degrades to
// nil (which namesOrCount then renders as "none"), not an empty-but-non-nil
// slice — matching listSessionSkillNames'/buildSessionCommands' identical
// nil-on-empty convention.
func TestMCPServerNames_EmptyIsNil(t *testing.T) {
	got := MCPServerNames(nil)
	assert.Nil(t, got)
	got = MCPServerNames([]agent.ChatMCPServer{})
	assert.Nil(t, got)
}

// TestMCPServerNames_SortsRegardlessOfInputOrder proves the function
// actually SORTS (not just projects .Name) — the init summary's mcp line
// must be deterministic across sessions regardless of the order servers were
// composed in (client-supplied first, then ctxloom's managed injection).
func TestMCPServerNames_SortsRegardlessOfInputOrder(t *testing.T) {
	got := MCPServerNames([]agent.ChatMCPServer{
		{Name: "zeta-server"},
		{Name: "alpha-server"},
		{Name: "mid-server"},
	})
	assert.Equal(t, []string{"alpha-server", "mid-server", "zeta-server"}, got)
}

// --- listSessionSkillNames: direct payload proofs over a REAL bundle -------

// writeMinimalSkillBundle authors a directory-form bundle at
// <appDir>/content/bundles/<bundleName>/bundle.yaml with one skill entry
// (SKILL.md only — scripts/assets aren't needed to prove name listing),
// mirroring internal/bundles/loader_skills_test.go's writeSkillBundle
// (unexported in a different package, so reproduced minimally here).
func writeMinimalSkillBundle(t *testing.T, fs afero.Fs, appDir, bundleName, skillName string) {
	t.Helper()
	bundlesDir := paths.LocalBundlesPath(appDir)
	bundleDir := bundlesDir + "/" + bundleName
	bundleYAML := "version: \"1.0\"\nskills:\n  " + skillName + ": {}\n"
	require.NoError(t, afero.WriteFile(fs, bundleDir+"/bundle.yaml", []byte(bundleYAML), 0644))
	skillDir := bundleDir + "/skills/" + skillName
	skillMD := "---\nname: " + skillName + "\ndescription: A test skill.\n---\n\n# " + skillName + "\n\nBody.\n"
	require.NoError(t, afero.WriteFile(fs, skillDir+"/SKILL.md", []byte(skillMD), 0644))
}

// TestListSessionSkillNames_RealCatalog proves the function's actual value
// (projecting ListSkills' []SkillEntry down to []string of real names) over
// a genuine bundle-authored skill — not a mock, not "non-empty".
func TestListSessionSkillNames_RealCatalog(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/proj/.ctxloom"
	require.NoError(t, fs.MkdirAll(appDir, 0o755))
	writeMinimalSkillBundle(t, fs, appDir, "skillbundle", "humanize")

	cfg, err := config.Load(config.WithFS(fs), config.WithAppDir(appDir))
	require.NoError(t, err)

	got := listSessionSkillNames(context.Background(), cfg)
	require.NoError(t, got.err)
	require.Len(t, got.names, 1)
	assert.Equal(t, "skillbundle/humanize", got.names[0], "the REAL bundle/skill-qualified name, not a placeholder")
}

// TestListSessionSkillNames_EmptyCatalogYieldsEmpty proves a project with no
// installed skills degrades to an empty name list (namesOrCount then renders
// "none"), the same fault-tolerant empty-set convention as
// MCPServerNames's zero-servers case.
//
// The function's OTHER branch — ListSkills returning a non-nil error — is
// NOT exercised here: ListSkills' only error source is loader.ListAllSkills,
// whose only error source is Loader.List() (internal/bundles/loader.go),
// which returns a nil error unconditionally on every path (a corrupt bundle
// file is warned-and-skipped, never surfaced as a returned error). That
// makes listSessionSkillNames' `if err != nil` branch unreachable through
// this composition today — dead code under the current Loader contract,
// not a gap a real input can drive; a test forcing it would have to inject a
// fake that violates List()'s actual contract, asserting nothing true.
func TestListSessionSkillNames_EmptyCatalogYieldsEmpty(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/proj/.ctxloom"
	require.NoError(t, fs.MkdirAll(appDir, 0o755))

	cfg, err := config.Load(config.WithFS(fs), config.WithAppDir(appDir))
	require.NoError(t, err)

	got := listSessionSkillNames(context.Background(), cfg)
	assert.NoError(t, got.err, "an empty catalog is a SUCCESSFUL listing, not a failed one")
	assert.Empty(t, got.names, "an empty catalog projects to an empty (nil-or-zero-length) name list")
}

// --- buildSessionInitSummary: all four collapsible fields, own CLI pointers

// TestBuildSessionInitSummary_AllFieldsCollapseWithOwnCLIPointer proves EVERY
// namesOrCount call site in buildSessionInitSummary collapses past the cap
// with its OWN correct CLI pointer (fragments/commands/skills/mcp each name
// a DIFFERENT command) — TestBuildSessionInitSummary_LongListsCollapseToCount
// only proved this for fragments; a copy-paste error swapping one field's
// listCmd for another's would not be caught by that test alone.
func TestBuildSessionInitSummary_AllFieldsCollapseWithOwnCLIPointer(t *testing.T) {
	mk := func(prefix string, n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = prefix + string(rune('a'+i))
		}
		return out
	}
	got := buildSessionInitSummary(sessionInitSummaryInputs{
		cfg:             &config.Config{},
		backendName:     "claude-sonnet",
		requestedAgent:  "coder",
		currentAgent:    "coder",
		label:           "claude-sonnet",
		fragmentsLoaded: listed(mk("frag", nameListCap+1)),
		commandNames:    listed(mk("cmd", nameListCap+2)),
		skillNames:      listed(mk("skill", nameListCap+3)),
		mcpServerNames:  listed(mk("mcp", nameListCap+4)),
		workDir:         "/home/user/project",
	})
	assert.Contains(t, got, "fragments : 9 (see `ctxloom run --dry-run`) loaded into the lead context")
	assert.Contains(t, got, "commands  : 10 (see `ctxloom command list`) available")
	assert.Contains(t, got, "skills    : 11 (see `ctxloom skill list`) installed")
	assert.Contains(t, got, "mcp       : 12 (see `ctxloom mcp list`) configured (connection status is not observable at connect)")
	assert.NotContains(t, got, "fraga")
	assert.NotContains(t, got, "cmda")
	assert.NotContains(t, got, "skilla")
	assert.NotContains(t, got, "mcpa")
}

// --- end-to-end: fragments/commands are the WIRING's own resolved values --

// TestOpenEngineSession_FragmentsAndCommandsWireThroughToSummary proves the
// sessionInitSummaryInputs ASSEMBLY inside OpenEngineSession (not just
// buildSessionInitSummary's pure formatting, already proven by the iso3
// table above) carries the REAL resolved fragment/command names for an
// agent bound to a profile that actually composes bundle content — an
// end-to-end wiring proof, over a real bundle + profile + agent binding, of
// exactly the two locals (fragmentsLoaded, commandNames) engine_session.go's
// doc comments name as flowing from ResolvedAgent.Fragments and
// buildSessionCommands' listing into the summary.
func TestOpenEngineSession_FragmentsAndCommandsWireThroughToSummary(t *testing.T) {
	resetStrictness(t)
	t.Setenv("HOME", t.TempDir())
	repo := initTestRepo(t)
	appDir := filepath.Join(repo, ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))

	bundlesDir := filepath.Join(appDir, "content", "bundles")
	require.NoError(t, os.MkdirAll(bundlesDir, 0o755))
	bundleYAML := "version: \"1.0\"\n" +
		"fragments:\n" +
		"  onboarding:\n" +
		"    content: |\n" +
		"      Onboarding fragment content.\n" +
		"commands:\n" +
		"  greet:\n" +
		"    content: |\n" +
		"      Say hello.\n"
	require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "local.yaml"), []byte(bundleYAML), 0o644))

	profilesDir := filepath.Join(appDir, "profiles")
	require.NoError(t, os.MkdirAll(profilesDir, 0o755))
	profileYAML := "name: reviewer-profile\ndescription: test\nbundles:\n  - local#fragments/onboarding\n"
	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "reviewer-profile.yaml"), []byte(profileYAML), 0o644))

	body := "version: 5\n" +
		"config:\n  use_distilled: false\n" +
		"agents:\n  reviewer:\n    llm: mock\n    profiles: [reviewer-profile]\n"
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "config.yaml"), []byte(body), 0o644))

	client := &fakeACPEngineClient{}
	stubACPEngineClient(t, client)

	chat, err := OpenEngineSession(context.Background(), OpenRequest{Cwd: repo}, fakeEngineSessionCoord{},
		"", "reviewer", "", "")
	require.NoError(t, err)
	require.NotNil(t, chat)

	// Two fragments load for real: the profile's own bundle fragment (named
	// by its canonical trust-gate ref, ctxloom:local@bundles/<bundle>#... —
	// see internal/bundles/loader.go's doc on that format) PLUS the
	// unconditionally-injected builtin isolation fragment
	// (appendBuiltinFragments, context.go) — both real, resolved facts, not
	// placeholders.
	assert.Contains(t, chat.InitSummary,
		"fragments : 2 (ctxloom:local@bundles/local#fragments/onboarding, ctxloom+builtin:isolation#fragments/isolation-axes) loaded into the lead context",
		"the REAL resolved fragment names from the composed profile, not a placeholder")
	assert.Contains(t, chat.InitSummary, "commands  : 1 (greet) available",
		"the REAL command ctxloom assembled from the same bundle")

	<-chat.Events
	chat.Close()
}
