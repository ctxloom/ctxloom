//! Why a `session` command did not complete at the command line, before or
//! after the application ran.

use std::fmt;

use angzarr_diagnostic::{code, Arg, Diagnostic};
use cli_support::{Exit, ExitClass};

#[derive(Clone, PartialEq, Eq, Debug)]
pub enum CliFault {
    /// A `--state` that is not a state (Go: `checkSessionState`).
    BadState { value: String, states: String },
    /// The runtime commands block on could not start.
    Runtime { ext_err_txt: String },
    /// The answer could not be written.
    Output { ext_err_txt: String },
    /// A sweep that acted had actions that failed; its report says which.
    SweepFailed { count: u64 },
    /// `top --sort` named a column it has none of (DRAFT).
    TopSortColumn { value: String, columns: String },
    /// `top --tree` with no session to root it (DRAFT).
    TopTreeWithoutSession,
    /// `top --all` with a session to watch, which names one project's (DRAFT).
    TopAllWithSession,
    /// `top --format text` off a terminal: a screen needs one (DRAFT).
    TopNeedsTerminal,
    /// `top` at a terminal: the full-screen UI is not built yet (DRAFT).
    TopScreenNotBuilt,
    /// `top` was asked to watch a session the project does not have (DRAFT).
    TopSessionUnknown { name: String },
}

impl ExitClass for CliFault {
    /// How the command exits: refused (20), a request invalid on its own
    /// content, or failed (10).
    fn exit(&self) -> Exit {
        match self {
            CliFault::BadState { .. }
            | CliFault::TopSortColumn { .. }
            | CliFault::TopTreeWithoutSession
            | CliFault::TopAllWithSession
            | CliFault::TopNeedsTerminal
            | CliFault::TopSessionUnknown { .. } => Exit::REFUSED,
            CliFault::Runtime { .. }
            | CliFault::Output { .. }
            | CliFault::SweepFailed { .. }
            | CliFault::TopScreenNotBuilt => Exit::FAILED,
        }
    }
}

impl fmt::Display for CliFault {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        angzarr_diagnostic::display(self, f)
    }
}

impl Diagnostic for CliFault {
    fn code(&self) -> &str {
        match self {
            CliFault::BadState { .. } => code!("bad-state"),
            CliFault::Runtime { .. } => code!("runtime"),
            CliFault::Output { .. } => code!("output"),
            CliFault::SweepFailed { .. } => code!("sweep-failed"),
            CliFault::TopSortColumn { .. } => code!("top-unknown-sort-column"),
            CliFault::TopTreeWithoutSession => code!("top-tree-without-session"),
            CliFault::TopAllWithSession => code!("top-all-with-session"),
            CliFault::TopNeedsTerminal => code!("top-needs-terminal"),
            CliFault::TopScreenNotBuilt => code!("top-screen-not-built"),
            CliFault::TopSessionUnknown { .. } => code!("top-session-unknown"),
        }
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        match self {
            CliFault::BadState { value, states } => vec![
                ("value", value.as_str().into()),
                ("states", states.as_str().into()),
            ],
            CliFault::Runtime { ext_err_txt } | CliFault::Output { ext_err_txt } => {
                vec![("ext_err_txt", ext_err_txt.as_str().into())]
            }
            CliFault::SweepFailed { count } => vec![("count", (*count).into())],
            CliFault::TopSortColumn { value, columns } => vec![
                ("value", value.as_str().into()),
                ("columns", columns.as_str().into()),
            ],
            CliFault::TopSessionUnknown { name } => vec![("name", name.as_str().into())],
            CliFault::TopTreeWithoutSession
            | CliFault::TopAllWithSession
            | CliFault::TopNeedsTerminal
            | CliFault::TopScreenNotBuilt => Vec::new(),
        }
    }
}

/// A close of a session that was closed already (slice 4 U3): said beside
/// the session's json, as its text says it on the answer's line.
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct AlreadyClosed {
    pub harp: String,
}

impl fmt::Display for AlreadyClosed {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        angzarr_diagnostic::display(self, f)
    }
}

impl Diagnostic for AlreadyClosed {
    fn code(&self) -> &str {
        code!("already-closed")
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        vec![("harp", self.harp.as_str().into())]
    }
}

/// This crate's en-US messages, its name first.
pub const EN_US: (&str, &str) = (
    env!("CARGO_PKG_NAME"),
    include_str!("../locales/en-US/messages.ftl"),
);

/// One value of every fault, for the catalog test.
#[cfg(any(test, feature = "test-support"))]
pub fn diagnostic_samples() -> Vec<Box<dyn Diagnostic>> {
    let faults: Vec<Box<dyn Diagnostic>> = vec![
        Box::new(CliFault::BadState {
            value: "running".to_string(),
            states: "live, ended, unknown".to_string(),
        }),
        Box::new(CliFault::Runtime {
            ext_err_txt: "no timer".to_string(),
        }),
        Box::new(CliFault::Output {
            ext_err_txt: "broken pipe".to_string(),
        }),
        Box::new(CliFault::SweepFailed { count: 2 }),
        Box::new(CliFault::TopSortColumn {
            value: "mood".to_string(),
            columns: "name, state, agent, profile, engine, owner, started".to_string(),
        }),
        Box::new(CliFault::TopTreeWithoutSession),
        Box::new(CliFault::TopAllWithSession),
        Box::new(CliFault::TopNeedsTerminal),
        Box::new(CliFault::TopScreenNotBuilt),
        Box::new(CliFault::TopSessionUnknown {
            name: "projects/p/sessions/amber-swift-owl".to_string(),
        }),
        Box::new(AlreadyClosed {
            harp: "brisk-copper-moth".to_string(),
        }),
    ];
    faults.into_iter().chain(crate::text::samples()).collect()
}

#[cfg(test)]
#[path = "error.test.rs"]
mod tests;
