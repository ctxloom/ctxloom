//! What claude says of a plan (owner ruling 168): advisory notices, never a
//! refusal.
//!
//! On Windows claude is signed in by a token alone (owner ruling 82, and the
//! owner's ruling of 2026-11-12): `CLAUDE_CODE_OAUTH_TOKEN` (from `claude
//! setup-token`) or an API key from the environment. No credentials file is
//! linked or copied into a session home there, so a terminal session started
//! with none of the variables set is told so, at its launch, with why and
//! what to do.

use std::fmt;

use angzarr_diagnostic::{code, Arg, Diagnostic};
use run_ports::{Advice, Planned};

/// A notice of claude's on what the plan resolved for it.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum ClaudeNotice {
    /// The human's terminal session on Windows has no credential variable
    /// set, and is handed no login file there: a workaround for claude's own
    /// credential handling, where sessions refreshing one OAuth login can
    /// sign each other out.
    WindowsTokenOnly,
}

impl fmt::Display for ClaudeNotice {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        angzarr_diagnostic::display(self, f)
    }
}

impl Diagnostic for ClaudeNotice {
    fn code(&self) -> &str {
        match self {
            ClaudeNotice::WindowsTokenOnly => code!("windows-token-only"),
        }
    }

    // The notice has no args: `vec![]` is the same value, an equivalent
    // mutant.
    #[cfg_attr(test, mutants::skip)]
    fn args(&self) -> Vec<(&str, Arg)> {
        match self {
            ClaudeNotice::WindowsTokenOnly => Vec::new(),
        }
    }
}

/// What claude says of `planned`: the terminal session on Windows with no
/// credential variable set is told it is handed no login. Anything else (a
/// child, whose missing token its run's enqueue refuses; another OS; a
/// token set; the plan, which knows no OS) is told nothing.
pub(crate) fn advise(planned: &Planned) -> Vec<Advice> {
    if planned.os == run_ports::WINDOWS && planned.interactive && !planned.credential_set {
        return vec![Box::new(ClaudeNotice::WindowsTokenOnly)];
    }
    Vec::new()
}

/// This crate's en-US messages, its name first.
pub const EN_US: (&str, &str) = (
    env!("CARGO_PKG_NAME"),
    include_str!("../locales/en-US/messages.ftl"),
);

/// One value of every notice, for the catalog test.
#[cfg(any(test, feature = "test-support"))]
pub fn diagnostic_samples() -> Vec<Box<dyn Diagnostic>> {
    vec![Box::new(ClaudeNotice::WindowsTokenOnly)]
}

#[cfg(test)]
#[path = "advice.test.rs"]
mod tests;
