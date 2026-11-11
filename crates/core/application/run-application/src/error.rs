//! Why a run did not answer.

use std::fmt;

use angzarr_diagnostic::{code, Arg, Diagnostic, Report};
use exit_code::{Exit, ExitClass};
use library_application::AppError as PlanError;
use run_launcher::LaunchError;
use run_ports::{BindRefusal, FailureClass};
use runs::RunRejection;
use runs_application::AppError as CoreError;
use sessions::SessionRejection;

#[derive(Debug)]
pub enum RunError {
    /// The library would not plan the run: a conflict of flags, an unreadable
    /// configuration, a startup finding that is fatal. Its own error, code
    /// and exit.
    Plan(Box<PlanError>),
    /// The run's domain refused it before anything was made: its own typed
    /// refusal, with the code, args and message of the domain's rejection
    /// (a one-shot with no prompt, an engine nobody drives, a child that
    /// cannot authenticate, or several of them at once: ruling 73).
    Refused(RunRejection),
    /// The run's listener has no address to bind (the bind table refused).
    /// Refused before anything is made.
    Bind(Box<BindRefusal>),
    /// The project could not be told.
    Project(Report),
    /// The run's hook listener could not be started: no hook could reach the
    /// run, so the engine is not launched.
    Listener(Report),
    /// The run's own lock, or its session's, could not be taken (rulings 86,
    /// 92, defect 1): another process holds it, or the file cannot be made.
    Lock(Report),
    /// The kernel refused, or failed, to make the session or enqueue its run.
    Session(CoreError),
    /// The session asked for cannot be resumed (owner ruling 115): the
    /// session domain's own refusal, with its code, args and message.
    Resume(SessionRejection),
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
    /// A signal asked ctxloom to stop while the run went on (ruling 114): the
    /// engine was stopped, the run's end recorded, and the process exits with
    /// `status`, the one a shell gives a process `signal` ended.
    Stopped { signal: String, status: u8 },
}

impl ExitClass for RunError {
    /// How a command this ends exits: what `PlanError` says for its own,
    /// fatal findings (21) for a refusal with the plan's fatal findings among
    /// its failures (rulings 73a, 152: the most severe wins), refused (20) for
    /// a refusal before anything ran (an ask that cannot be run, a refusal of
    /// the kernel), the signal for a run a signal stopped (128 plus its
    /// number), failed (10) otherwise.
    fn exit(&self) -> Exit {
        match self {
            RunError::Plan(error) => error.exit(),
            RunError::Stopped { status, .. } => Exit::signal(signal_number(*status)),
            RunError::Refused(RunRejection::LaunchRefused { fatal_findings, .. })
                if *fatal_findings > 0 =>
            {
                Exit::FATAL_FINDINGS
            }
            RunError::Refused(_) | RunError::Bind(_) | RunError::Resume(_) => Exit::REFUSED,
            RunError::Session(CoreError::Refused { .. }) => Exit::REFUSED,
            _ => Exit::FAILED,
        }
    }
}

/// The number of the signal a shell's `status` (128 plus it) names, kept in
/// 1..=127.
fn signal_number(status: u8) -> u8 {
    status.saturating_sub(128).max(1)
}

impl RunError {
    /// The refusals an aggregate refusal holds, in check order, each said as
    /// the run's single refusal would be (rulings 73, 73b); none for any other
    /// error, a single refusal included (it is its own plain envelope).
    pub fn refusals(&self) -> Vec<Box<dyn Diagnostic>> {
        match self {
            RunError::Refused(RunRejection::LaunchRefused { refusals, .. }) => refusals
                .iter()
                .map(|refusal| Box::new(refusal.clone()) as Box<dyn Diagnostic>)
                .collect(),
            _ => Vec::new(),
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
            RunError::Refused(rejection) => rejection.code(),
            RunError::Resume(rejection) => rejection.code(),
            RunError::Bind(error) => error.code(),
            RunError::Project(_) => code!("project"),
            RunError::Listener(_) => code!("listener"),
            RunError::Lock(_) => code!("lock"),
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
            RunError::Stopped { .. } => code!("stopped"),
        }
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        match self {
            RunError::Plan(error) => error.args(),
            RunError::EmptyAnswer => Vec::new(),
            RunError::Refused(rejection) => rejection.args(),
            RunError::Resume(rejection) => rejection.args(),
            RunError::Bind(error) => error.args(),
            RunError::Project(report) => vec![("reason", report.code().into())],
            RunError::Listener(report) | RunError::Lock(report) => {
                vec![("reason", report.code().into())]
            }
            RunError::Session(error) => vec![("reason", why(error).into())],
            RunError::Launch(error) => vec![("reason", error.code().into())],
            RunError::NotStarted { detail } | RunError::Turn { detail, .. } => {
                vec![("detail", detail.as_str().into())]
            }
            RunError::NotEnded { run } => vec![("run", run.as_str().into())],
            RunError::Stopped { signal, .. } => vec![("signal", signal.as_str().into())],
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
        Box::new(RunError::Project(Report::new("ports-registry"))),
        Box::new(RunError::Listener(Report::new("hook-listener-bind"))),
        Box::new(RunError::Lock(Report::new("file-lock-timeout"))),
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
        Box::new(RunError::Stopped {
            signal: "SIGTERM".to_string(),
            status: 143,
        }),
    ]
}

#[cfg(test)]
#[path = "error.test.rs"]
mod tests;
