//! What opencode says of a plan (owner ruling 168): advisory notices, never
//! a refusal.

use std::fmt;

use angzarr_diagnostic::{code, Arg, Diagnostic};
use run_ports::{Advice, Planned};

/// A notice of opencode's on what the plan resolved for it.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum OpencodeNotice {
    /// The agent sets no model, so opencode chooses its own: its fallback
    /// can be a model without tool support ("no endpoints found that support
    /// tool use"), which ends the run's first turn.
    NoModel { agent: String },
}

impl fmt::Display for OpencodeNotice {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        angzarr_diagnostic::display(self, f)
    }
}

impl Diagnostic for OpencodeNotice {
    fn code(&self) -> &str {
        match self {
            OpencodeNotice::NoModel { .. } => code!("no-model"),
        }
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        match self {
            OpencodeNotice::NoModel { agent } => vec![("agent", agent.as_str().into())],
        }
    }
}

/// What opencode says of `planned`: an agent with no model is told opencode
/// chooses its own. A run with no agent has no `agents.<agent>` key to name,
/// and is told nothing.
pub(crate) fn advise(planned: &Planned) -> Vec<Advice> {
    if planned.agent.is_empty() || !planned.model.is_empty() {
        return Vec::new();
    }
    vec![Box::new(OpencodeNotice::NoModel {
        agent: planned.agent.clone(),
    })]
}

/// This crate's en-US messages, its name first.
pub const EN_US: (&str, &str) = (
    env!("CARGO_PKG_NAME"),
    include_str!("../locales/en-US/messages.ftl"),
);

/// One value of every notice, for the catalog test.
#[cfg(any(test, feature = "test-support"))]
pub fn diagnostic_samples() -> Vec<Box<dyn Diagnostic>> {
    vec![Box::new(OpencodeNotice::NoModel {
        agent: "dev".to_string(),
    })]
}

#[cfg(test)]
#[path = "advice.test.rs"]
mod tests;
