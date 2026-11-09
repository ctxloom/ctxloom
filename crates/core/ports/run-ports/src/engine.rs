//! The engine port: what an agent engine contributes to running a child.
//!
//! Pure. An engine says what to put in a session home ([`Engine::prepare`]),
//! the command of ONE turn ([`Engine::drive_turn`]) and what a finished
//! turn's output means ([`Engine::finish_turn`]); a driven adapter
//! (`engine-claude`, `engine-mock`) implements it, `host-launcher` runs the
//! command, and nothing here touches a file or a process.
//!
//! A turn is one command and a later turn a new one that resumes the engine's
//! own session (`TurnRequest::resume`); there is no engine process between
//! turns.
//! `drive_interactive` is the human's terminal run: its command is given her
//! terminal ([`crate::RunnerLauncher::launch_terminal`]).

use std::collections::BTreeMap;
use std::fmt;

use angzarr_diagnostic::{code, Arg, Diagnostic};

use crate::launcher::{TurnCommand, TurnOutput};

/// The variable that names ctxloom's home (the directory ctxloom keeps
/// `.ctxloom` under) in an interactive engine's environment. The engine's own
/// children, hook processes among them, run without `HOME`, so they find
/// ctxloom's home through this one explicit variable.
pub const HOME_VARIABLE: &str = "CTXLOOM_HOME";

/// A file to write under a session home.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct HomeFile {
    /// Slash-separated, relative to the home.
    pub rel: String,
    pub bytes: Vec<u8>,
}

/// What [`Engine::prepare`] is asked.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct PrepareRequest {
    /// The session home's absolute path.
    pub home: String,
    /// The assembled context the engine is handed.
    pub context: String,
    /// The names found in the user's own engine configuration directory: the
    /// only ones an engine may copy in are those on its allowlist.
    pub offered: Vec<String>,
    /// The home is the human's terminal session's, not a child's turn: the
    /// engine installs its hooks in it.
    pub interactive: bool,
}

/// What [`Engine::prepare`] answers: the session home's surfaces.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Prepared {
    /// Files to write under the home.
    pub files: Vec<HomeFile>,
    /// Names to copy from the user's engine configuration into the home: the
    /// allowlist met with what was offered.
    pub copy_in: Vec<String>,
    /// The context file's absolute path.
    pub context_path: String,
}

/// One turn to drive.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct TurnRequest {
    pub prompt: String,
    /// The session home's absolute path.
    pub home: String,
    /// The context file [`Engine::prepare`] named.
    pub context_path: String,
    /// The engine's own session id from an earlier turn: this turn resumes it.
    pub resume: Option<String>,
    /// The caller's environment: the engine passes on only what it allows.
    pub env: BTreeMap<String, String>,
    pub cwd: Option<String>,
}

/// Why a turn failed.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum FailureClass {
    RateLimited,
    CredentialRefused,
    EngineError,
    Interrupted,
}

impl FailureClass {
    /// The class's name, as the plan spells it.
    pub fn name(self) -> &'static str {
        match self {
            FailureClass::RateLimited => "rate_limited",
            FailureClass::CredentialRefused => "credential_refused",
            FailureClass::EngineError => "engine_error",
            FailureClass::Interrupted => "interrupted",
        }
    }
}

/// What a finished turn meant.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum TurnOutcome {
    Answered {
        answer: String,
        /// The engine's own session id, if its output named one.
        native_id: Option<String>,
    },
    Failed {
        class: FailureClass,
        detail: String,
        native_id: Option<String>,
    },
}

/// An agent engine's contribution to running a child.
pub trait Engine: Send + Sync {
    /// The engine's name.
    fn name(&self) -> &str;

    /// The session home's surfaces.
    fn prepare(&self, request: &PrepareRequest) -> Result<Prepared, EngineError>;

    /// The command of ONE turn.
    fn drive_turn(&self, request: &TurnRequest) -> Result<TurnCommand, EngineError>;

    /// What a turn's finished command meant.
    fn finish_turn(&self, output: &TurnOutput) -> TurnOutcome;

    /// The command of the human's terminal run.
    fn drive_interactive(&self, request: &TurnRequest) -> Result<TurnCommand, EngineError>;
}

/// The names on `allowed` that `offered` holds, sorted, once each. A name that is not exactly on the
/// allowlist is never copied in.
pub fn copy_in(allowed: &[&str], offered: &[String]) -> Vec<String> {
    let mut names: Vec<String> = offered
        .iter()
        .filter(|name| allowed.contains(&name.as_str()))
        .cloned()
        .collect();
    names.sort();
    names.dedup();
    names
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum EngineError {
    /// The engine has not built this operation.
    NotYet { engine: String, operation: String },
}

impl fmt::Display for EngineError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        angzarr_diagnostic::display(self, f)
    }
}

impl Diagnostic for EngineError {
    fn code(&self) -> &str {
        match self {
            EngineError::NotYet { .. } => code!("engine-not-yet"),
        }
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        match self {
            EngineError::NotYet { engine, operation } => vec![
                ("engine", engine.as_str().into()),
                ("operation", operation.as_str().into()),
            ],
        }
    }
}

#[cfg(test)]
#[path = "engine.test.rs"]
mod tests;
