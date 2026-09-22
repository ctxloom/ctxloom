package operations

import (
	"fmt"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/go-viper/mapstructure/v2"
)

// DecodeBackendConfig decodes the labeled LLM entry into its backend's typed
// config via the backend registry. The label is looked up verbatim; the
// backend is chosen solely by the entry's type. Returns nil on a missing label
// or unknown/undecodable type (fault tolerant — caller degrades to defaults).
func DecodeBackendConfig(cfg *config.Config, label string) agent.BackendConfig {
	entry, ok := cfg.GetLLMEntry(label)
	if !ok {
		return nil
	}
	bc, err := DecodeEngineConfig(cfg.EffectiveType(entry), entry.Body)
	if err != nil {
		clidiag.Warn("ctxloom", "LLM config %q: %v", label, err)
		if cfg.EffectiveType(entry) == "gemini" || cfg.EffectiveType(entry) == "antigravity" {
			// Both "gemini" (the pre-v4 name) and its v4 successor
			// "antigravity" are removed backends with no supported
			// replacement — 0.7.0 dropped the Antigravity CLI (agy) engine
			// entirely, not just renamed it. It rides clidiag like the line
			// above it: that is the one channel that honours the process's
			// structured-diagnostics wire shape and the TUI's sink redirect,
			// both of which a bare write to os.Stderr corrupts.
			clidiag.Warn("ctxloom", "the %q backend is not supported in this release; point this entry's type at a currently-supported engine (%s)", cfg.EffectiveType(entry),
				strings.Join(EngineNamesWhere(func(d engine.Definition) bool { return d.Distribution != engine.DistributionTestOnly }), ", "))
		}
		return nil
	}
	return bc
}

// mockControlConfig is implemented by the mock doubles' BackendConfig types
// (mock.Config), the only label bodies that carry a
// map of variables for the launched process: the CTXLOOM_MOCK_* test-control
// knobs. No real engine's config carries one — an engine's credentials and
// environment are ambient, never ctxloom's (config.RetiredLLMEnvKey). It is a
// structural interface local to this package, not part of
// agent.BackendConfig, so MockControlFor reaches the map without a
// concrete-type switch: internal/adapters/operations (the ADR-0026 core) must not
// branch on a backend's identity — the dispatch shape agent.BackendConfig's
// own doc prescribes: "shared code carries the interface and never
// type-switches on the backend."
type mockControlConfig interface {
	MockControl() map[string]string
}

// MockControlFor returns the test-control map a labeled entry carries, for
// callers that pass env through the run request. Empty when the label is
// unset, carries none, or its decoded config type does not implement
// mockControlConfig — every real engine.
func MockControlFor(cfg *config.Config, label string) map[string]string {
	bc := DecodeBackendConfig(cfg, label)
	if mc, ok := bc.(mockControlConfig); ok {
		return mc.MockControl()
	}
	return nil
}

// DecodeEngineConfig decodes a labeled entry's raw body into the typed
// config of the named engine (agent.Hosted.NewConfig). An unknown type is an
// error the caller degrades (fault tolerance). The label that keyed the
// entry is NOT consulted — only the explicit type drives which decoder runs.
// One shared mapstructure pass fills the engine's own zero config from the
// raw YAML body; the engine declares the TYPE, this owns the decode.
func DecodeEngineConfig(engineType string, body map[string]interface{}) (agent.BackendConfig, error) {
	h, ok := engines.Hosted(engineType)
	if !ok {
		return nil, fmt.Errorf("unknown LLM backend type %q", engineType)
	}
	cfg := h.NewConfig()
	if err := mapstructure.Decode(body, cfg); err != nil {
		// The mapstructure error names no backend, so a multi-backend
		// config load could not attribute a decode failure to its source
		// entry without this.
		return nil, fmt.Errorf("backend %q: %w", engineType, err)
	}
	return cfg, nil
}
