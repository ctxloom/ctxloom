package content

// EngineExport is one engine's export settings for an item — how a command
// surfaces as that engine's slash command, or whether a skill is enabled for it.
//
// It is ONE struct keyed by engine name rather than four near-identical structs
// (ClaudeCodeConfig, CodexConfig, KiroConfig, OpencodeConfig in internal/bundles).
// Those four differ only in which of these fields they use, and mirroring them
// here would mean four shapes to keep in step with a new engine's arrival. A
// map keyed by engine name also means a NEW ENGINE needs no change to this
// package at all — the same property the surface-type registry gives kinds.
//
// Fields absent for a given engine are simply unset; nothing here validates which
// engine honours which field, because that is the exporting backend's business and
// duplicating its rules here would be a second source of truth.
type EngineExport struct {
	// Enabled is nil-means-true (the opt-out model every engine config uses), so
	// it is a pointer: "unset" and "explicitly false" are different answers.
	Enabled *bool `yaml:"enabled,omitempty"`
	// Description is the engine-facing description (help text, command palette).
	Description string `yaml:"description,omitempty"`
	// ArgumentHint is the autocomplete hint (claude-code, codex).
	ArgumentHint string `yaml:"argument_hint,omitempty"`
	// AllowedTools restricts tools for the exported command (claude-code).
	AllowedTools []string `yaml:"allowed_tools,omitempty"`
	// Model overrides the model for the exported command (claude-code).
	Model string `yaml:"model,omitempty"`
}

// IsEnabled reports whether the export is on, honouring the nil-means-true
// opt-out model.
func (e EngineExport) IsEnabled() bool {
	return e.Enabled == nil || *e.Enabled
}

// EngineExports is per-engine export settings keyed by engine name
// ("claude-code", "codex", "kiro", "opencode", …).
//
// Engine names are NOT validated against a closed list. An unknown engine's
// settings are carried verbatim rather than dropped: a bundle authored against a
// newer ctxloom must not silently lose its configuration when read by an older
// one, and losing it silently is worse than carrying something unrecognised.
type EngineExports map[string]EngineExport

// For returns one engine's export settings and whether they were declared.
func (e EngineExports) For(engine string) (EngineExport, bool) {
	x, ok := e[engine]
	return x, ok
}

// IsEnabledFor reports whether an item is exported to an engine. An engine with
// no declared settings is enabled, matching the opt-out model.
func (e EngineExports) IsEnabledFor(engine string) bool {
	x, ok := e[engine]
	if !ok {
		return true
	}
	return x.IsEnabled()
}
