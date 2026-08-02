package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/operations"
)

// TestCompletionCmd_UnsupportedShellFailsInsteadOfWritingNothing pins that
// the RunE switch had no default, so any unmatched shell fell through
// to `return nil` — exit 0, zero bytes, no message, and a user sourcing an
// empty completion file. cobra's ValidArgs+OnlyValidArgs makes that unreachable
// TODAY, which is the whole hazard: adding a shell to ValidArgs without adding
// a generator arm would ship the silent no-op with no compiler complaint.
func TestCompletionCmd_UnsupportedShellFailsInsteadOfWritingNothing(t *testing.T) {
	err := completionCmd.RunE(completionCmd, []string{"tcsh"})

	require.Error(t, err, "an unmatched shell must not exit 0 having written nothing")
	assert.Contains(t, err.Error(), "tcsh", "the error must name what was asked for")
	for _, shell := range completionCmd.ValidArgs {
		assert.Contains(t, err.Error(), shell, "and enumerate what IS supported")
	}
}

// TestCompletionCmd_ValidArgsMatchTheGeneratorArms is the other half of the
// same invariant, from the opposite direction: every advertised shell must have
// a generator, so the guard above can never fire for a value cobra accepts.
func TestCompletionCmd_ValidArgsMatchTheGeneratorArms(t *testing.T) {
	assert.Equal(t, []string{"bash", "zsh", "fish", "powershell"}, completionCmd.ValidArgs,
		"a new entry here needs a new arm in the RunE switch")
}

// TestCompleteLLMNames_BackendFallbackIsAdmissible refutes the claim that
// completeLLMNames' fallback to backends.List() is an inconsistency with
// completeFragmentNames/ProfileNames/TagNames/PromptNames (all of which return
// nil when the config does not load), which would have it return nil too.
//
// The divergence is justified, and removing it would delete a working
// completion. The four siblings complete names that exist ONLY inside a config
// (fragments, profiles, tags, commands): with no config there is genuinely
// nothing to offer. The --llm/--engine value space is wider — operations.
// ResolveBackend accepts a bare REGISTERED BACKEND NAME as well as a config
// label (oneshot.go: `if _, configured := cfg.GetLLMEntry(label); !configured &&
// backends.Exists(label) { return label, "" }`) — and those names are compiled
// in, so they remain valid, offerable values even when nothing loaded.
//
// This test pins that reason rather than the fallback line, so the fallback
// stays defensible for as long as it stays true.
func TestCompleteLLMNames_BackendFallbackIsAdmissible(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{})
	names := backends.List()
	require.NotEmpty(t, names)

	for _, name := range names {
		backend, _ := operations.ResolveBackend(cfg, name)
		assert.Equal(t, name, backend,
			"a bare backend name is a valid --llm/--engine value, so completing it is not a defect")
	}
}

// TestCompleteLLMNames_PrefersConfigLabels pins the primary path the fallback
// is a fallback FOR: with a config loaded, the labels under llm.configs are
// what gets offered — not backend names.
func TestCompleteLLMNames_PrefersConfigLabels(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{
		LM: config.LMConfig{Configs: map[string]config.LLMConfig{
			"big":  {Type: "antigravity"},
			"fast": {Type: "claude-code"},
		}},
	})

	assert.Equal(t, []string{"big", "fast"}, cfg.GetLLMLabels(),
		"completeLLMNames offers these; the backend list is only the no-config fallback")
	assert.Equal(t, []string{"fast"}, filterPrefix(cfg.GetLLMLabels(), "f"))
}
