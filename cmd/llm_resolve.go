package cmd

import (
	"fmt"
	"os"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
)

// decodeBackendConfig decodes the labeled LLM entry into its backend's typed
// config via the backend registry. The label is looked up verbatim; the
// backend is chosen solely by the entry's type. Returns nil on a missing label
// or unknown/undecodable type (fault tolerant — caller degrades to defaults).
func decodeBackendConfig(cfg *config.Config, label string) backends.BackendConfig {
	entry, ok := cfg.LM.Configs[label]
	if !ok {
		return nil
	}
	backendType := entry.Type
	if backendType == "" {
		backendType = config.DefaultLLM
	}
	bc, err := backends.DecodeLLMConfig(backendType, entry.Body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ctxloom: warning: LLM config %q: %v\n", label, err)
		return nil
	}
	return bc
}

// decodeBackendConfigForType returns the decoded config of the first labeled
// entry whose type matches backendType. Used where only a backend type is
// known (the self-invoked serve transport); ties resolve to map order.
func decodeBackendConfigForType(cfg *config.Config, backendType string) backends.BackendConfig {
	for label, entry := range cfg.LM.Configs {
		t := entry.Type
		if t == "" {
			t = config.DefaultLLM
		}
		if t == backendType {
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
	case *backends.ClaudeConfig:
		return c.Env
	case *backends.GeminiConfig:
		return c.Env
	case *backends.CodexConfig:
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
	case *backends.ClaudeConfig:
		return c.BinaryPath, c.Args
	case *backends.GeminiConfig:
		return c.BinaryPath, c.Args
	case *backends.CodexConfig:
		return c.BinaryPath, c.Args
	}
	return "", nil
}
