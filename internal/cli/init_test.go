package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/operations"
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
