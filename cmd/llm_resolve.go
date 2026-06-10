package cmd

import (
	"fmt"
	"maps"
	"os"
	"slices"

	"github.com/ctxloom/antigravity"
	"github.com/ctxloom/claude"
	"github.com/ctxloom/codex"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/shared/agent"
)

// decodeBackendConfig decodes the labeled LLM entry into its backend's typed
// config via the backend registry. The label is looked up verbatim; the
// backend is chosen solely by the entry's type. Returns nil on a missing label
// or unknown/undecodable type (fault tolerant — caller degrades to defaults).
func decodeBackendConfig(cfg *config.Config, label string) agent.BackendConfig {
	entry, ok := cfg.LM.Configs[label]
	if !ok {
		return nil
	}
	bc, err := backends.DecodeLLMConfig(entry.EffectiveType(), entry.Body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ctxloom: warning: LLM config %q: %v\n", label, err)
		return nil
	}
	return bc
}

// decodeBackendConfigForType returns the decoded config of a labeled entry
// whose type matches backendType. Used where only a backend type is known
// (the self-invoked serve transport without --label). Selection is
// deterministic: the primary role's label wins when its type matches, then
// the lexicographically first matching label — never Go map order, which
// with two labels of the same type would configure a random entry per
// process.
func decodeBackendConfigForType(cfg *config.Config, backendType string) agent.BackendConfig {
	matchesType := func(label string) bool {
		entry, ok := cfg.LM.Configs[label]
		return ok && entry.EffectiveType() == backendType
	}
	if primary := cfg.PrimaryLabel(); matchesType(primary) {
		return decodeBackendConfig(cfg, primary)
	}
	for _, label := range slices.Sorted(maps.Keys(cfg.LM.Configs)) {
		if matchesType(label) {
			return decodeBackendConfig(cfg, label)
		}
	}
	return nil
}

// llmEnvFor returns the env map a labeled entry carries, for callers that pass
// env through the run request. Empty when the label is unset or carries none.
func llmEnvFor(cfg *config.Config, label string) map[string]string {
	bc := decodeBackendConfig(cfg, label)
	switch c := bc.(type) {
	case *claude.ClaudeConfig:
		return c.Env
	case *antigravity.AntigravityConfig:
		return c.Env
	case *codex.CodexConfig:
		return c.Env
	case *backends.MockConfig:
		return c.Env
	}
	return nil
}

// llmBinaryArgsFor returns the binary path and args a labeled entry carries,
// for the external-runner launch path. Empty when unset.
func llmBinaryArgsFor(cfg *config.Config, label string) (binary string, args []string) {
	bc := decodeBackendConfig(cfg, label)
	switch c := bc.(type) {
	case *claude.ClaudeConfig:
		return c.BinaryPath, c.Args
	case *antigravity.AntigravityConfig:
		return c.BinaryPath, c.Args
	case *codex.CodexConfig:
		return c.BinaryPath, c.Args
	}
	return "", nil
}
