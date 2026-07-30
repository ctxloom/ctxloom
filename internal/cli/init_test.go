package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestGenerateConfig covers the ctxloom init config builder. The selected
// engine's backend type lands in the registry, and the output must be valid
// YAML ending in a newline.
func TestGenerateConfig(t *testing.T) {
	for _, engine := range []string{"claude-code", "antigravity", "codex"} {
		t.Run(engine, func(t *testing.T) {
			data, err := operations.BuildInitialConfig(engine, "", false)
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
	data, err := operations.BuildInitialConfig("claude-code", "", false)
	require.NoError(t, err)
	body := string(data)
	// The scaffold settings survive into the written config.
	assert.Contains(t, body, "use_distilled: true")
	assert.Contains(t, body, "auto_register_ctxloom: true")
	// The engine's role pair is wired into llm.defaults.
	assert.Contains(t, body, "primary: claude-code")
	assert.Contains(t, body, "fast: claude-fast")
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

// TestRunInit_ExistingDir_HonoursRemoteFlags (U036-F04) proves `ctxloom init
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
	t.Chdir(dir)

	var gotRepos []string
	var gotForge string
	origAdd := addPersonalRemotesFn
	addPersonalRemotesFn = func(cmd *cobra.Command, repos []string, forge string) {
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
// internal/operations/setup_prompt_test.go.)
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
	config.Invalidate()
	t.Cleanup(config.Invalidate)

	cfg, _ := GetConfig()

	var out strings.Builder
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	require.NoError(t, runSetupPromptCmd(cmd, nil))

	assert.Equal(t, discoverySessionPrompt(cfg)+"\n", out.String(),
		"`init prompt` and the discovery session must emit the same setup body")
}
