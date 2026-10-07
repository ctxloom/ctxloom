package agent

// The WELL-KNOWN approach names. The set of approach names is OPEN — an
// engine declares whichever it supports in its own Declaration, under
// whatever names it chooses — and a constant exists here only for the one
// name every engine declares: its native file, spelled the same everywhere so
// a binding's `surfaces:` value means one thing whichever engine it names. An
// approach only one engine has (claude's system prompt) is named by that
// engine, in its own package; naming it here would be the enum growing back.
const (
	// ApproachUnsafeFile writes the engine's native, well-known file the engine
	// reads directly (CLAUDE.md, .mcp.json, settings, command/skill dirs…).
	// "unsafe" names itself LOUDLY because choosing it IS the race
	// acknowledgment: a well-known write into a SHARED live cwd cannot be
	// locked against a concurrent session using those exact files. Into an
	// isolated (private) cell it is always safe — isolation IS the conversion.
	ApproachUnsafeFile = "unsafe-file"
)
