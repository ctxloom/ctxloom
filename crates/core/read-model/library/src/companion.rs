//! A companion's registration (OD-9, OD-10, owner 2026-11-13): a NAME, never a
//! path or a command, so it holds wherever the program is installed (a
//! container's paths are not the host's). The program a name stands for is
//! found where the agent runs, on that place's `PATH`, each time it is asked.

/// The companions ctxloom ships, each its own program (`ltk`, not
/// `ctxloom-companion-ltk`), in the order init offers them.
pub const SHIPPED: [&str; 3] = ["ltk", "taskloom", "reprise"];

/// The program a companion's name stands for: a shipped companion's own
/// name, any other name `<n>` the program `ctxloom-companion-<n>`.
pub fn program(name: &str) -> String {
    if SHIPPED.contains(&name) {
        name.to_string()
    } else {
        format!("ctxloom-companion-{name}")
    }
}

/// Whether a registered companion contributes (`companions.<n>.state`, a
/// word: rulings 59, 60).
#[derive(Clone, Copy, PartialEq, Eq, Debug, Default)]
pub enum State {
    /// It is asked for its loadout, and what it gives is used: the default.
    #[default]
    Enabled,
    /// It stays registered and is listed, and contributes nothing.
    Disabled,
}

impl State {
    /// The word the config holds.
    pub fn word(self) -> &'static str {
        match self {
            State::Enabled => "enabled",
            State::Disabled => "disabled",
        }
    }

    /// The state a config's word says: `disabled`, else enabled (the
    /// default; the schema refuses any other word).
    pub fn of(word: &str) -> State {
        if word == "disabled" {
            State::Disabled
        } else {
            State::Enabled
        }
    }
}

/// The words `companions.<n>.state` takes.
pub const STATE_WORDS: [&str; 2] = ["enabled", "disabled"];

#[cfg(test)]
#[path = "companion.test.rs"]
mod tests;
