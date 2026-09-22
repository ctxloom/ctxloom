package cli

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestGenerateConfig covers the ctxloom init config builder. The selected
// engine's backend type lands in the registry, and the output must be valid
// YAML ending in a newline.
func TestGenerateConfig(t *testing.T) {
	for _, engine := range []string{"claude-code", "mock"} {
		t.Run(engine, func(t *testing.T) {
			data, err := operations.BuildInitialConfig(engine, "")
			require.NoError(t, err)
			body := string(data)
			assert.Contains(t, body, "type: "+engine,
				"engine must appear as a registry entry type")
			assert.NotContains(t, body, "role:",
				"role is registry-only and stripped on write")
			assert.True(t, strings.HasSuffix(body, "\n"),
				"config must end with newline (POSIX-friendly + diff-friendly)")
		})
	}
}

func TestGenerateConfig_DefaultsBlock(t *testing.T) {
	data, err := operations.BuildInitialConfig("claude-code", "")
	require.NoError(t, err)
	body := string(data)
	// The scaffold settings survive into the written config.
	assert.Contains(t, body, "use_distilled: true")
	// The engine's role pair is wired into llm.defaults.
	assert.Contains(t, body, "primary: claude-code")
	assert.Contains(t, body, "fast: claude-fast")
}

// TestCtxloomDefaultTrusted pins init's trust claim about the seeded
// "ctxloom-default" remote to the ACTUAL trust root, rather than asserting it
// unconditionally regardless of local state.
//
// MUTATION TARGET: replacing ctxloomDefaultTrusted's body with `return true`
// turns the "locally distrusted" subtest red; replacing it with `return
// false` turns the "default trust root" subtest red.
func TestCtxloomDefaultTrusted(t *testing.T) {
	t.Run("nil config never claims trust", func(t *testing.T) {
		assert.False(t, ctxloomDefaultTrusted(nil))
	})

	t.Run("an unmodified trust root trusts ctxloom's embedded publishing key", func(t *testing.T) {
		dir := t.TempDir()
		cfg := config.NewFixture(config.Fixture{AppPaths: []string{dir}})
		cfg.SetFS(afero.NewMemMapFs())

		assert.True(t, ctxloomDefaultTrusted(cfg),
			"an unmodified trust root must still trust ctxloom's own embedded publishing key")
	})

	t.Run("a locally distrusted embedded principal is no longer trusted", func(t *testing.T) {
		embedded := config.EmbeddedSigners().Entries()
		require.NotEmpty(t, embedded, "the embedded trust root must ship at least one signer for this test to mean anything")
		principal := embedded[0].Principals[0]

		dir := t.TempDir()
		fs := afero.NewMemMapFs()
		require.NoError(t, afero.WriteFile(fs, paths.DistrustedSignersPath(dir), []byte(principal+"\n"), 0o644))

		cfg := config.NewFixture(config.Fixture{AppPaths: []string{dir}})
		cfg.SetFS(fs)

		assert.False(t, ctxloomDefaultTrusted(cfg),
			"a locally distrusted embedded principal must no longer be reported as trusted")
	})
}

// TestPromptDirtyTreeHandler_EachOptionAndDefault exercises the init
// interview's single dirty-tree question end to end at the reader level: a
// blank line (Enter) picks the recommended "commit" answer with ack true, and
// each numbered choice returns its handler with ack true ONLY for "commit" —
// proving the one answer really does decide both dirty_tree_handler AND
// dirty_tree_commit_ack together, never asking a second question for the ack.
func TestPromptDirtyTreeHandler_EachOptionAndDefault(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantHandler string
		wantAck     bool
	}{
		{"blank_enter_picks_recommended_commit", "\n", "commit", true},
		{"1_is_commit_with_ack", "1\n", "commit", true},
		{"2_is_copy_without_ack", "2\n", "copy", false},
		{"3_is_stale_without_ack", "3\n", "stale", false},
		{"4_is_fail_without_ack", "4\n", "fail", false},
		// An out-of-range/garbage entry re-prompts rather than accepting it;
		// the loop must recover on the next valid line.
		{"invalid_then_valid_retries", "0\nnotanumber\n2\n", "copy", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newInitPromptsFrom(strings.NewReader(tt.input))
			handler, ack, err := p.promptDirtyTreeHandler()
			require.NoError(t, err)
			assert.Equal(t, tt.wantHandler, handler)
			assert.Equal(t, tt.wantAck, ack)
		})
	}
}

// TestPersonalRemoteRequests covers the pure request builder behind
// `ctxloom init`'s personal-repo registration: the first repo is named
// "personal" and the rest "personal-N", and the --forge label (when set) binds
// every personal remote to that forge. A remote no longer carries trust on add
// (spec §11) — its content takes the review path until its publisher key is
// added — so there is no trust flag to assert.
func TestPersonalRemoteRequests(t *testing.T) {
	t.Run("names and empty forge", func(t *testing.T) {
		reqs := personalRemoteRequests([]string{"me/a", "me/b", "me/c"}, "")
		require.Len(t, reqs, 3)
		assert.Equal(t, "personal", reqs[0].Name)
		assert.Equal(t, "personal-2", reqs[1].Name)
		assert.Equal(t, "personal-3", reqs[2].Name)
		for _, r := range reqs {
			assert.Empty(t, r.Forge, "no --forge means resolution falls back to host-match")
		}
		assert.Equal(t, "me/a", reqs[0].URL)
	})

	t.Run("forge binds every personal remote", func(t *testing.T) {
		reqs := personalRemoteRequests([]string{"me/a", "me/b"}, "work-ghe")
		require.Len(t, reqs, 2)
		for _, r := range reqs {
			assert.Equal(t, "work-ghe", r.Forge, "--forge must bind each personal remote")
		}
	})

	t.Run("no repos yields no requests", func(t *testing.T) {
		assert.Empty(t, personalRemoteRequests(nil, "github"))
	})
}

// TestRunInit_ExistingDir_HonoursRemoteFlags proves `ctxloom init
// --remote <repo>` against a PRE-EXISTING .ctxloom is honoured, not silently
// dropped. Before the fix, addPersonalRemotes was reachable only from
// setupNewCtxloomDir's fresh-init branch — runInit's alreadyExists branch
// never called it at all, so a repeat `ctxloom init --remote` printed
// "ctxloom directory already exists", exited 0, and added zero remotes.
// addPersonalRemotesFn is stubbed (the package-var seam mirroring
// launchEngineWithPromptFn) so this pins the WIRING without exercising the
// real network-touching remote-add machinery.
func TestRunInit_ExistingDir_HonoursRemoteFlags(t *testing.T) {
	testsupport.Isolate(t)
	dir := t.TempDir()
	appDir := filepath.Join(dir, ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "config.yaml"), []byte("version: 5\n"), 0o644))
	chdir(t, dir)

	var gotRepos []string
	var gotForge string
	origAdd := addPersonalRemotesFn
	addPersonalRemotesFn = func(_ *cobra.Command, gotAppDir string, repos []string, forge string) {
		assert.Equal(t, appDir, gotAppDir, "remotes must be added to the .ctxloom this init targets")
		gotRepos = repos
		gotForge = forge
	}
	t.Cleanup(func() { addPersonalRemotesFn = origAdd })

	origRemotes, origForge := initRemotes, initForge
	origHome, origNonInteractive, origSkipLaunch := initHome, initNonInteractive, initSkipLaunch
	initRemotes = []string{"me/repo"}
	initForge = "work-ghe"
	initHome = false
	initNonInteractive = true
	initSkipLaunch = true
	t.Cleanup(func() {
		initRemotes, initForge = origRemotes, origForge
		initHome, initNonInteractive, initSkipLaunch = origHome, origNonInteractive, origSkipLaunch
	})

	err := runInit(&cobra.Command{}, nil)
	require.NoError(t, err)

	assert.Equal(t, []string{"me/repo"}, gotRepos, "--remote must be honoured even when .ctxloom already exists")
	assert.Equal(t, "work-ghe", gotForge, "--forge must be honoured too")
}

// TestDiscoverySessionPrompt_MergesDiscoveryAndAgentSetup pins the collapsed
// init interview: the ONE prompt the discovery session receives is ctxloom's
// built-in six-phase setup body (ctxloomInitPrompt, init-as-skill slice 3),
// where profile discovery (phase 4) precedes the agent-setup interview
// (phase 5) so profile selection and agent binding happen in a single
// continuous conversation. A nil config (load failure at launch) must still
// return the built-in text verbatim — fault tolerance, never a truncated
// prompt. (Bundle- or companion-shipped `agent-setup` commands AUGMENT it via
// operations.ResolveSetupPrompt, whose composition contract is covered in
// internal/adapters/operations/setup_prompt_test.go.)
func TestDiscoverySessionPrompt_MergesDiscoveryAndAgentSetup(t *testing.T) {
	got := discoverySessionPrompt(nil)

	di := strings.Index(got, "search_library")       // profiles-phase marker
	si := strings.Index(got, "SCAN → DISCUSS → SET") // agents-phase marker
	require.GreaterOrEqual(t, di, 0, "profiles phase missing from the setup body")
	require.GreaterOrEqual(t, si, 0, "agent-setup phase missing from the setup body")
	assert.Less(t, di, si, "profiles must precede agent setup — profiles are the setup's inputs")
	assert.Equal(t, got, ctxloomInitPrompt,
		"nil config returns the built-in six-phase body verbatim")
}

// TestSetupPromptDoors_EmitTheSameBody is the parity gate across the setup
// doors: what `ctxloom init prompt` writes to stdout must be byte-identical to
// what the init discovery session is handed. They are the "one body, N doors"
// invariant's two independently-coded halves — one guards a nil config, the
// other guards a config LOAD ERROR — and a body that diverges between them is
// a session set up against instructions no other door ever emits.
func TestSetupPromptDoors_EmitTheSameBody(t *testing.T) {
	testsupport.Isolate(t)
	resetApp()
	t.Cleanup(resetApp)

	cfg, _ := GetConfig()

	var out strings.Builder
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	require.NoError(t, runSetupPromptCmd(cmd, nil))

	assert.Equal(t, discoverySessionPrompt(cfg)+"\n", out.String(),
		"`init prompt` and the discovery session must emit the same setup body")
}

// TestReadCleanLine pins what init's prompt reader keeps and what it
// throws away. It exists to strip terminal noise — CSI escapes from focus
// events, cursor reports, stray control bytes — from a line the user typed, and
// the two things it takes are repo names and filesystem paths, both of which
// are legitimately non-ASCII. Dropping every byte above 0x7e mangled such an
// entry silently: `ctxloom init` would then register a remote or path the user
// never typed instead of rejecting it or echoing it back.
func TestReadCleanLine(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"plain_ascii", "me/ctxloom-profiles\n", "me/ctxloom-profiles"},
		{"trims_surrounding_space", "  me/repo  \n", "me/repo"},
		{"utf8_accents_survive", "me/ctxloom-prófiles\n", "me/ctxloom-prófiles"},
		{"non_latin_script_survives", "私のリポジトリ\n", "私のリポジトリ"},
		{"emoji_survives", "me/repo-🚀\n", "me/repo-🚀"},
		{"strips_focus_events", "\x1b[Ime/repo\x1b[O\n", "me/repo"},
		{"strips_cursor_report", "me\x1b[12;3R/repo\n", "me/repo"},
		{"strips_bare_control_bytes", "me\x07/repo\x00\n", "me/repo"},
		{"drops_invalid_utf8_bytes", "me/\xffrepo\n", "me/repo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newInitPromptsFrom(strings.NewReader(tc.in))
			got, err := p.readCleanLine()
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestPromptAllEngines_LeavesTheCallersSliceAlone pins that
// building the combined menu never writes through the caller's slice.
// `append(primary, secondary...)` reuses primary's backing array whenever it
// has spare capacity, so the combined list would overwrite whatever the caller
// keeps past len(primary). getAvailableEngines happens to hand back cap==len
// slices today, which is the only reason this never corrupted a live menu — an
// invariant nothing states and no caller can see. The engine list a user picks
// from must not depend on the allocation history of the slice it came from.
func TestPromptAllEngines_LeavesTheCallersSliceAlone(t *testing.T) {
	backing := []string{"claude-code", "SENTINEL-1", "SENTINEL-2"}
	primary := backing[:1] // len 1, cap 3 — spare capacity to be clobbered

	var got string
	var err error
	captureStdout(t, func() {
		p := newInitPromptsFrom(strings.NewReader("2\n"))
		got, err = p.promptAllEngines(primary, []string{"mock", "other-engine"})
	})

	require.NoError(t, err)
	assert.Equal(t, "mock", got, "option 2 of [claude-code mock other-engine]")
	assert.Equal(t, []string{"claude-code", "SENTINEL-1", "SENTINEL-2"}, backing,
		"the combined menu must not be built through the caller's backing array")
}

// writeEngineConfig scaffolds a real config.yaml naming engine as its primary
// backend, built by the same operations.BuildInitialConfig `ctxloom init` writes
// — so what the resolver reads back is the shape init actually produces, not a
// hand-rolled approximation of it.
func writeEngineConfig(t *testing.T, appDir, engine string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	body, err := operations.BuildInitialConfig(engine, "")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "config.yaml"), body, 0o644))
}

// TestEngineForExistingDir pins the engine precedence on the
// RE-INIT path. `ctxloom init --engine mock` in a project whose config names
// another engine used to launch the CONFIG's engine: the resolver consulted the
// stored value first and returned it whenever it was set, so the flag the user
// typed on this invocation was read and then discarded — a flag silently
// overridden by a stored default, with no message saying so. An explicit
// selection wins; the recorded engine is the fallback; neither present leaves
// the choice to pickDefaultEngine.
func TestEngineForExistingDir(t *testing.T) {
	testsupport.Isolate(t)
	dir := t.TempDir()
	appDir := filepath.Join(dir, ".ctxloom")
	writeEngineConfig(t, appDir, "claude-code")
	chdir(t, dir)
	resetApp()
	t.Cleanup(resetApp)

	require.Equal(t, "claude-code", engineForExistingDir("", appDir),
		"with no flag, the engine recorded in the existing config is used")
	assert.Equal(t, "mock", engineForExistingDir("mock", appDir),
		"an explicit --engine must win over the engine recorded in the config")
}

// TestInitPostScaffoldStepsUseTheDirTheyJustWrote pins that init's
// post-scaffold steps read the config for the .ctxloom THIS init targets, not
// whatever .ctxloom the ambient discovery walk finds from the cwd. The two are
// different directories whenever `ctxloom init --home` runs inside a project (or
// CTXLOOM_ROOT points elsewhere): init then wrote one config and applied
// remotes, dependencies and hooks against a DIFFERENT one — a whole init's
// worth of work landing in the wrong project, reported as success.
//
// Asserted at the site whose resolved config is observable without a
// network: the engine resolver's return value. cloneConfiguredRemotes,
// pullSeededDependencies and addPersonalRemotes read the same pinned
// composition.
func TestInitPostScaffoldStepsUseTheDirTheyJustWrote(t *testing.T) {
	testsupport.Isolate(t)

	// The ambient project the cwd discovers: names claude-code.
	project := t.TempDir()
	projectApp := filepath.Join(project, ".ctxloom")
	writeEngineConfig(t, projectApp, "claude-code")
	chdir(t, project)

	// The .ctxloom this init actually targets: names mock.
	target := t.TempDir()
	targetApp := filepath.Join(target, ".ctxloom")
	writeEngineConfig(t, targetApp, "mock")

	resetApp()
	t.Cleanup(resetApp)
	// init pins the process composition to its target before its first read,
	// exactly as runInit does, so discovery from cwd never adopts the ambient
	// project.
	cmd := &cobra.Command{}
	require.NoError(t, pinAppDir(cmd, targetApp))

	assert.Equal(t, "mock", engineForExistingDir("", targetApp),
		"the engine must come from the .ctxloom this init targets")
}

// TestDirtyTreeHandlerOptions_AreTheOperationsHandlers pins the
// menu to the handler set that reads its answer back. The interview writes
// dirty_tree_handler and internal/adapters/operations/delegate.go dispatches on it, so
// "which four values exist" and "which one needs the commit acknowledgement"
// are that package's rules; the menu had them as its own string literals, which
// is a connascence of meaning across a package boundary — nothing links the two
// lists, and a handler renamed or added there leaves this menu quietly writing
// a value the dispatcher treats as absent.
//
// The literal values stay pinned in TestPromptDirtyTreeHandler_EachOptionAndDefault
// (they are what lands in config.yaml); this test pins the correspondence.
func TestDirtyTreeHandlerOptions_AreTheOperationsHandlers(t *testing.T) {
	values := make([]launch.DirtyTreeHandler, 0, len(dirtyTreeHandlerOptions))
	for _, opt := range dirtyTreeHandlerOptions {
		values = append(values, opt.value)
	}
	assert.Equal(t, []launch.DirtyTreeHandler{
		launch.DirtyTreeHandlerCommit,
		launch.DirtyTreeHandlerCopy,
		launch.DirtyTreeHandlerStale,
		launch.DirtyTreeHandlerFail,
	}, values, "the menu must offer exactly the handlers operations dispatches on, in display order")

	// And the ack rule keys on the SAME commit handler, for every arm.
	for i, opt := range dirtyTreeHandlerOptions {
		p := newInitPromptsFrom(strings.NewReader(strconv.Itoa(i+1) + "\n"))
		var handler string
		var ack bool
		var err error
		captureStdout(t, func() { handler, ack, err = p.promptDirtyTreeHandler() })
		require.NoError(t, err)
		assert.Equal(t, string(opt.value), handler)
		assert.Equal(t, opt.value == launch.DirtyTreeHandlerCommit, ack,
			"only the commit handler mutates the user's repo, so only it carries the ack")
	}
}

// TestDiscoverySessionPrompt_CarriesCompanionSetupGuidance asserts the PROMPT
// BYTES the discovery session is launched with: a companion's typed
// `init.setup_guidance` is spliced into the one body the session receives,
// after the built-in six-phase text. It pins the deterministic-composition
// contract end to end — the guidance arrives because the loadout declared
// it, not because a model asked for it.
func TestDiscoverySessionPrompt_CarriesCompanionSetupGuidance(t *testing.T) {
	_, cfg := setupProject(t, "claude-code")
	cfg = withCompanionProbe(t, cfg, func(context.Context) (bundles.CompanionProbe, error) {
		return bundles.CompanionProbe{Loadouts: []bundles.CompanionLoadout{{
			Bin:      "ltk",
			Path:     "/opt/bin/ltk",
			Document: []byte("run:\n  version: 1.0.0\ninit:\n  setup_guidance: COMPANION-INIT-GUIDANCE-MARKER\n"),
		}}}, nil
	})

	got := discoverySessionPrompt(cfg)

	require.True(t, strings.HasPrefix(got, ctxloomInitPrompt),
		"the built-in six-phase body leads the composed prompt verbatim")
	assert.Contains(t, got, "COMPANION-INIT-GUIDANCE-MARKER",
		"the companion's setup_guidance must reach the launched prompt's bytes")
}
