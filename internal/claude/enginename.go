package claude

// EngineName is the registered name of this engine — the registry key, the
// name its backend reports, and the spelling every other binary (ltk,
// taskloom) resolves to. EngineAliases are the alternate spellings that
// resolve to it. Both are declared HERE, in the engine's own package, and
// read by its descriptor and by the per-binary registries; nothing else may
// carry a copy.
const EngineName = "claude-code"

// EngineAliases returns a fresh copy each call so no caller can rewrite the
// declaration through a read.
func EngineAliases() []string { return []string{"claude", "claudecode"} }
