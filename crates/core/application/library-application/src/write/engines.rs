//! What a write needs to know of an engine (Go's engine registry, the part a
//! binding and a label are checked against). An engine's permissions are its
//! own document, never checked by ctxloom (owner rulings 133-139), so there is
//! no mode list here. Which
//! surfaces and roots an engine offers is each engine's own answer and arrives
//! with the engines (S9); until then a surface preference is checked for its
//! shape and, for claude-code, against its approaches.

use crate::agents::ENGINES;

/// The roots a surface can be placed in.
pub(crate) const ROOTS: [&str; 3] = ["session-home", "project-root", "work-dir"];

/// The kinds of surface a binding has a preference for.
pub(crate) const SURFACE_KINDS: [&str; 6] =
    ["context", "mcp", "settings", "hooks", "commands", "skills"];

/// Whether `name` is an engine this build knows.
pub(crate) fn known(name: &str) -> bool {
    ENGINES.contains(&name)
}

/// The approach `engine` offers for the surface `kind`: none known for an
/// engine whose declaration this build does not carry yet.
pub(crate) fn approaches(engine: &str, kind: &str) -> Option<&'static [&'static str]> {
    if engine != "claude-code" {
        return None;
    }
    match kind {
        "context" => Some(&["system-prompt"]),
        "mcp" => Some(&["mcp-config"]),
        "settings" => Some(&["settings"]),
        "hooks" => Some(&["settings-hooks"]),
        "commands" => Some(&["commands-dir"]),
        "skills" => Some(&["skills-dir"]),
        _ => None,
    }
}

#[cfg(test)]
#[path = "engines.test.rs"]
mod tests;
