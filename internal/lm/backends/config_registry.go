package backends

import (
	"fmt"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/go-viper/mapstructure/v2"
)

// DecodeLLMConfig decodes a labeled entry's raw body into the typed config for
// the named backend type. An unknown type is an error the caller degrades
// (fault tolerance). The label that keyed the entry is NOT consulted — only the
// explicit type drives which decoder runs.
func DecodeLLMConfig(backendType string, body map[string]interface{}) (agent.BackendConfig, error) {
	d, ok := lookup(backendType)
	if !ok {
		return nil, fmt.Errorf("unknown LLM backend type %q", backendType)
	}
	// One shared mapstructure pass fills the engine's own zero config (the
	// descriptor's NewConfig) from the raw YAML body; the "model" key and
	// the rest map straight onto the target's mapstructure tags. The engine
	// declares the TYPE, the registry owns the decode.
	cfg := d.NewConfig()
	if err := mapstructure.Decode(body, cfg); err != nil {
		// The mapstructure error names no backend, so a multi-backend
		// config load could not attribute a decode failure to its source
		// entry without this.
		return nil, fmt.Errorf("backend %q: %w", backendType, err)
	}
	return cfg, nil
}

// ConfiguredBackend instantiates the backend named by cfg's type and applies
// cfg to it. Returns nil when the type is unregistered.
func ConfiguredBackend(cfg agent.BackendConfig) agent.Backend {
	if cfg == nil {
		return nil
	}
	b := Get(cfg.BackendType())
	if b == nil {
		return nil
	}
	if c, ok := b.(Configurable); ok {
		c.Configure(cfg)
	}
	return b
}
