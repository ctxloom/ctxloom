package backends

import (
	"fmt"

	"github.com/ctxloom/shared/agent"
	"github.com/go-viper/mapstructure/v2"
)

// configDecoder turns an LLM entry's raw body into a backend's typed config.
type configDecoder func(body map[string]interface{}) (agent.BackendConfig, error)

// configRegistry holds the per-backend decoders keyed by type discriminator.
var configRegistry = make(map[string]configDecoder)

// RegisterConfig registers a backend's config decoder under its type
// discriminator. Backends call this from init alongside Register.
func RegisterConfig(backendType string, decode configDecoder) {
	configRegistry[backendType] = decode
}

// DecodeLLMConfig decodes a labeled entry's raw body into the typed config for
// the named backend type. An unknown type is an error the caller degrades
// (fault tolerance). The label that keyed the entry is NOT consulted — only the
// explicit type drives which decoder runs.
func DecodeLLMConfig(backendType string, body map[string]interface{}) (agent.BackendConfig, error) {
	decode, ok := configRegistry[backendType]
	if !ok {
		return nil, fmt.Errorf("unknown LLM backend type %q", backendType)
	}
	return decode(body)
}

// decodeBody is the shared mapstructure pass each backend's decoder uses to
// fill its struct from the raw YAML body. The "model" key (and any other
// fields) map straight onto the target's mapstructure tags.
func decodeBody(body map[string]interface{}, target agent.BackendConfig) (agent.BackendConfig, error) {
	if err := mapstructure.Decode(body, target); err != nil {
		return nil, err
	}
	return target, nil
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
