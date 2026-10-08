package opencode

import (
	"encoding/json"
	"fmt"
	"maps"
	"path"

	"github.com/go-viper/mapstructure/v2"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// Config is an opencode label's typed body (agent.BackendConfig): the fields
// the `type: opencode` branch of the config schema admits. The model is read
// untyped from the body (config.ResolveLLM), as for every engine; the
// context window is the neutral per-label `context_window` every branch
// carries, read here because opencode cannot discover it and never compacts
// without it.
type Config struct {
	ContextWindow int            `mapstructure:"context_window"`
	Provider      ProviderConfig `mapstructure:"provider"`
	// ModelOptions is passed verbatim as the model's options, which the
	// OpenAI-compatible SDK spreads into every request body (sampling,
	// chat_template_kwargs). Opaque by design: ctxloom models no thinking
	// knob.
	ModelOptions map[string]any `mapstructure:"model_options"`
	// Snapshot, TitleGeneration and Share are opencode's own per-label
	// switches (Q-F). Unset: snapshots follow the mode (on for the TUI,
	// whose /undo needs them; off for a structured run, which never undoes),
	// title generation off (an extra model request
	// at every new session), sharing disabled.
	Snapshot        *bool  `mapstructure:"snapshot"`
	TitleGeneration *bool  `mapstructure:"title_generation"`
	Share           string `mapstructure:"share"`
}

// BackendType identifies the backend this config drives.
func (Config) BackendType() string { return string(EngineName) }

// ProviderConfig is the OpenAI-compatible endpoint. No key lives here: the
// key is read from the environment (opencodeAuth) and reaches opencode only
// as an {env:...} reference.
type ProviderConfig struct {
	BaseURL string `mapstructure:"base_url"`
	// OutputLimit is the model's output token limit; opencode reserves it
	// out of the context window when deciding to compact.
	OutputLimit int `mapstructure:"output_limit"`
	// HeaderTimeoutMS bounds the wait for response headers; 0 disables it
	// (a single-slot server queues a request, and a queued turn must wait
	// rather than fail). Unset is opencode's own default.
	HeaderTimeoutMS *int `mapstructure:"header_timeout_ms"`
	// ChunkTimeoutMS bounds the silence between streamed chunks; unset is
	// opencode's own default.
	ChunkTimeoutMS *int `mapstructure:"chunk_timeout_ms"`
}

// decodeConfig decodes a label's body. A body that does not decode is a
// label the schema should have refused; it is an error naming the engine.
func decodeConfig(body map[string]any) (Config, error) {
	var c Config
	if err := mapstructure.Decode(body, &c); err != nil {
		return Config{}, fmt.Errorf("%s: label config: %w", EngineName, err)
	}
	return c, nil
}

// providerID is the provider every ctxloom label is configured as.
const providerID = "ctxloom"

// configContent is the one OPENCODE_CONFIG_CONTENT document a session runs
// with, composed from the label: the provider (endpoint, key by reference,
// timeouts, the model's window and output limit and options) as the only
// enabled provider, the model, the fixed policy (no autoupdate) with the
// label's snapshot/title/share switches, and — for an untrusted repository,
// whose project config opencode is told to ignore — the project's AGENTS.md
// re-added by its engine path. It carries no secret.
func configContent(s engine.Session, c Config, projectAgents bool) (string, error) {
	doc := map[string]any{
		"$schema":    configSchemaURL,
		"autoupdate": false,
		"share":      or(c.Share, "disabled"),
		"snapshot":   boolOr(c.Snapshot, s.Mode == engine.Interactive),
		"agent":      map[string]any{"title": map[string]any{"disable": !boolOr(c.TitleGeneration, false)}},
	}
	if p := provider(s.Label.Model, c); p != nil {
		doc["provider"] = map[string]any{providerID: p}
		doc["enabled_providers"] = []string{providerID}
	}
	if m := s.Label.Model; m != "" {
		doc["model"] = providerID + "/" + m
		doc["small_model"] = providerID + "/" + m
	}
	if projectAgents {
		doc["instructions"] = []string{path.Join(s.Roots.ProjectRoot.Engine, ContextFileName)}
	}
	b, err := json.Marshal(doc)
	return string(b), err
}

// provider is the ctxloom provider block, or nil for a label that names no
// endpoint (nothing to configure).
func provider(model string, c Config) map[string]any {
	if c.Provider.BaseURL == "" {
		return nil
	}
	opts := map[string]any{"baseURL": c.Provider.BaseURL, "apiKey": "{env:" + APIKeyEnv + "}"}
	if t := c.Provider.HeaderTimeoutMS; t != nil {
		opts["headerTimeout"] = timeout(*t)
	}
	if t := c.Provider.ChunkTimeoutMS; t != nil {
		opts["chunkTimeout"] = timeout(*t)
	}
	p := map[string]any{"npm": "@ai-sdk/openai-compatible", "name": providerID, "options": opts}
	if model != "" {
		m := map[string]any{"limit": map[string]any{"context": c.ContextWindow, "output": c.Provider.OutputLimit}}
		if len(c.ModelOptions) > 0 {
			m["options"] = maps.Clone(c.ModelOptions)
		}
		p["models"] = map[string]any{model: m}
	}
	return p
}

// timeout is a millisecond timeout as opencode spells it: 0 is false, no
// timeout at all.
func timeout(ms int) any {
	if ms == 0 {
		return false
	}
	return ms
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func boolOr(b *bool, def bool) bool {
	if b == nil {
		return def
	}
	return *b
}
