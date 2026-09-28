package agent

// The WELL-KNOWN approach names. The set of approaches is OPEN — an engine
// declares whichever it supports in its own Declaration, under whatever names
// it chooses — and these constants exist only because SHARED code has to refer
// to these two by name: the at-rest callers (materialize, apply, remove,
// currency) ask every engine for its native file, and apply and the launch
// fallback ask for hook-carried context. An approach only one engine has
// (claude's system prompt) is named by that engine, in its own package; naming
// it here would be the enum growing back.
//
// Preference between approaches is expressed by the CALLER and by the cell,
// never by a list order: `profile materialize` names the native file because
// its output must outlive ctxloom; a launch takes the default.
const (
	// ApproachUnsafeFile writes the engine's native, well-known file the engine
	// reads directly (CLAUDE.md, .mcp.json, settings, command/skill dirs…).
	// "unsafe" names itself LOUDLY because choosing it IS the race
	// acknowledgment: a well-known write into a SHARED live cwd cannot be
	// locked against a concurrent session using those exact files. Into an
	// isolated (private) cell it is always safe — isolation IS the conversion.
	ApproachUnsafeFile = "unsafe-file"
	// ApproachHook delivers context via a SessionStart inject-context hook
	// reading a content-addressed cache file. It RIDES the settings surface
	// (the hook is written there), which is what the shared HookCarriedContext
	// implementation declares.
	ApproachHook = "hook"
)
