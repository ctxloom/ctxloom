package content

// EngineExport is one engine's export block for an item, as authored: an
// OPAQUE mapping this package carries and never reads inside. What a block
// means is the named engine's business — it decodes its own block against
// its ExportSchema — so nothing here validates which engine honours which
// key, and a NEW ENGINE needs no change to this package at all.
type EngineExport map[string]any

// EngineExports is per-engine export blocks keyed by engine name
// ("claude-code", …).
//
// Engine names are NOT validated against a closed list. An unknown engine's
// block is carried verbatim rather than dropped: a bundle authored against a
// newer ctxloom must not silently lose its configuration when read by an older
// one, and losing it silently is worse than carrying something unrecognised.
type EngineExports map[string]EngineExport

// For returns one engine's block and whether it was declared.
func (e EngineExports) For(engine string) (EngineExport, bool) {
	x, ok := e[engine]
	return x, ok
}
