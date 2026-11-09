//! Why a run did not answer.

use std::fmt;

use angzarr_diagnostic::{code, Arg, Diagnostic, Report};
use library_application::AppError as PlanError;
use run_launcher::LaunchError;
use run_ports::{BindRefusal, FailureClass};
use runs::RunRejection;
use runs_application::AppError as CoreError;

#[derive(Debug)]
pub enum RunError {
    /// The library would not plan the run: a conflict of flags, an unreadable
    /// configuration, a startup finding that is fatal. Its own error, code
    /// and exit.
    Plan(Box<PlanError>),
    /// A one-shot with no prompt from any door asks nothing.
    NoPrompt,
    /// No engine of that name can be driven.
    UnknownEngine { name: String, known: String },
    /// The run's domain refused it before anything was made: its own typed
    /// refusal, with the code, args and message of the domain's rejection.
    Refused(RunRejection),
    /// The run's listener has no address to bind (the bind table refused).
    /// Refused before anything is made.
    Bind(Box<BindRefusal>),
    /// The project could not be told.
    Project(Report),
    /// The run's hook listener could not be started: no hook could reach the
    /// run, so the engine is not launched.
    Listener(Report),
    /// The kernel refused, or failed, to make the session or enqueue its run.
    Session(CoreError),
    /// The launch itself failed: the store, the files, the facts.
    Launch(LaunchError),
    /// The engine's command could not be started.
    NotStarted { detail: String },
    /// The turn failed, in the class the engine named.
    Turn { class: FailureClass, detail: String },
    /// The turn answered with nothing.
    EmptyAnswer,
    /// The launch ended with no end to the run in the stream.
    NotEnded { run: String },
}

impl RunError {
    /// The process exit code: what `PlanError` says for its own, 2 for a
    /// refusal before anything ran (an ask that cannot be run, a refusal of
    /// the kernel), 1 otherwise.
    pub fn exit_code(&self) -> u8 {
        match self {
            RunError::Plan(error) => error.exit_code(),
            RunError::NoPrompt
            | RunError::UnknownEngine { .. }
            | RunError::Refused(_)
            | RunError::Bind(_) => 2,
            RunError::Session(CoreError::Refused { .. }) => 2,
            _ => 1,
        }
    }
}

impl fmt::Display for RunError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        angzarr_diagnostic::display(self, f)
    }
}

/// The text of a failure with a code and a reason: the code of a refusal, the
/// reason of a failure.
fn why(error: &CoreError) -> String {
    match error {
        CoreError::Refused { code, .. } => code.clone(),
        CoreError::Failed { reason } => reason.clone(),
        other => other.code().to_string(),
    }
}

impl Diagnostic for RunError {
    fn code(&self) -> &str {
        match self {
            RunError::Plan(error) => error.code(),
            RunError::NoPrompt => code!("no-prompt"),
            RunError::UnknownEngine { .. } => code!("unknown-engine"),
            RunError::Refused(rejection) => rejection.code(),
            RunError::Bind(error) => error.code(),
            RunError::Project(_) => code!("project"),
            RunError::Listener(_) => code!("listener"),
            RunError::Session(_) => code!("session"),
            RunError::Launch(_) => code!("launch"),
            RunError::NotStarted { .. } => code!("not-started"),
            RunError::Turn { class, .. } => match class {
                FailureClass::RateLimited => code!("rate-limited"),
                FailureClass::CredentialRefused => code!("credential-refused"),
                FailureClass::EngineError => code!("engine-failed"),
                FailureClass::Interrupted => code!("interrupted"),
            },
            RunError::EmptyAnswer => code!("empty-answer"),
            RunError::NotEnded { .. } => code!("not-ended"),
        }
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        match self {
            RunError::Plan(error) => error.args(),
            RunError::NoPrompt | RunError::EmptyAnswer => Vec::new(),
            RunError::UnknownEngine { name, known } => vec![
                ("name", name.as_str().into()),
                ("known", known.as_str().into()),
            ],
            RunError::Refused(rejection) => rejection.args(),
            RunError::Bind(error) => error.args(),
            RunError::Project(report) => vec![("reason", report.code().into())],
            RunError::Listener(report) => vec![("reason", report.code().into())],
            RunError::Session(error) => vec![("reason", why(error).into())],
            RunError::Launch(error) => vec![("reason", error.code().into())],
            RunError::NotStarted { detail } | RunError::Turn { detail, .. } => {
                vec![("detail", detail.as_str().into())]
            }
            RunError::NotEnded { run } => vec![("run", run.as_str().into())],
        }
    }
}

/// This crate's en-US messages, its name first.
pub const EN_US: (&str, &str) = (
    env!("CARGO_PKG_NAME"),
    include_str!("../locales/en-US/messages.ftl"),
);

/// One value of every error that is this crate's own, for the catalog test.
#[cfg(any(test, feature = "test-support"))]
pub fn diagnostic_samples() -> Vec<Box<dyn Diagnostic>> {
    let turn = |class| RunError::Turn {
        class,
        detail: "d".to_string(),
    };
    vec![
        Box::new(RunError::NoPrompt),
        Box::new(RunError::UnknownEngine {
            name: "bogus".to_string(),
            known: "claude-code".to_string(),
        }),
        Box::new(RunError::Project(Report::new("ports-registry"))),
        Box::new(RunError::Listener(Report::new("hook-listener-bind"))),
        Box::new(RunError::Session(CoreError::Failed {
            reason: "unavailable".to_string(),
        })),
        Box::new(RunError::Launch(LaunchError::BadSession {
            session: "x".to_string(),
        })),
        Box::new(RunError::NotStarted {
            detail: "claude: not found".to_string(),
        }),
        Box::new(turn(FailureClass::RateLimited)),
        Box::new(turn(FailureClass::CredentialRefused)),
        Box::new(turn(FailureClass::EngineError)),
        Box::new(turn(FailureClass::Interrupted)),
        Box::new(RunError::EmptyAnswer),
        Box::new(RunError::NotEnded {
            run: "r".to_string(),
        }),
    ]
}

#[cfg(test)]
#[path = "error.test.rs"]
mod tests;
