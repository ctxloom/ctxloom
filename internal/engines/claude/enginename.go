package claude

// EngineName is the registered name of this engine — the registry key, the
// name its backend reports, and the spelling every binary (ctxloom, ltk,
// taskloom) resolves. It is the ONLY spelling: there is no alias table, so a
// user who types anything else is refused as an unknown engine. Declared
// HERE, in the engine's own package, and read by its descriptor and by the
// per-binary registries; nothing else may carry a copy.
const EngineName = "claude-code"
