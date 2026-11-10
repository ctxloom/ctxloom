//! Why a command to a run was refused.

use std::fmt;

use angzarr_diagnostic::{code, Arg, Diagnostic};

#[derive(Clone, PartialEq, Eq, Debug)]
pub enum RunRejection {
    /// An enqueue on a stream that already holds a run.
    AlreadyEnqueued,
    /// A command to a stream holding no run.
    NoRun,
    /// A turn queued while one is in flight.
    TurnInFlight { turn: u64 },
    /// A command to a run that has ended (the end fence).
    Ended,
    /// A credential hash that is not a SHA-256 digest (32 bytes).
    CredentialHashInvalid { length: u64 },
    /// A stream whose root is not the run's derived root.
    NotItsStream,
    /// The runner's and the model's credential hashes are the same, so a
    /// token would not name one principal (fail closed).
    CredentialsNotDistinct,
    /// A child agent's run asked for with none of the environment
    /// credentials its engine authenticates with.
    NoCredential { engine: String, variables: String },
    /// A child agent's run whose engine's credential source names a file and
    /// no variable (owner ruling 53): only the human's primary session may
    /// use a file. `variables` are the ones the engine's built-in source
    /// accepts, to name in the source and export.
    FileOnlyCredential { engine: String, variables: String },
    /// A one-shot with no prompt from any door: it would ask nothing (ruling
    /// 73c).
    NoPrompt,
    /// A run on an engine this build cannot drive (ruling 73c): `known` are
    /// the ones it drives, comma separated.
    UnknownEngine { name: String, known: String },
    /// A launch refused for more than one reason, or for one beside the plan's
    /// fatal findings (rulings 73, 73b, 73c): each refusal, in check order;
    /// `plan_aborted` when the plan had fatal findings too (the library says
    /// them). Its `count` is how many checks failed, the findings one of them.
    LaunchRefused {
        refusals: Vec<RunRejection>,
        plan_aborted: bool,
    },
}

impl RunRejection {
    /// The refusals this one stands for, in check order: an aggregate's
    /// own, else itself.
    pub fn refusals(&self) -> Vec<RunRejection> {
        match self {
            RunRejection::LaunchRefused { refusals, .. } => refusals.clone(),
            other => vec![other.clone()],
        }
    }
}

impl fmt::Display for RunRejection {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        angzarr_diagnostic::display(self, f)
    }
}

impl Diagnostic for RunRejection {
    fn code(&self) -> &str {
        match self {
            RunRejection::AlreadyEnqueued => code!("already-enqueued"),
            RunRejection::NoRun => code!("no-run"),
            RunRejection::TurnInFlight { .. } => code!("turn-in-flight"),
            RunRejection::Ended => code!("ended"),
            RunRejection::CredentialHashInvalid { .. } => code!("credential-hash-invalid"),
            RunRejection::NotItsStream => code!("not-its-stream"),
            RunRejection::CredentialsNotDistinct => code!("credentials-not-distinct"),
            RunRejection::NoCredential { .. } => code!("no-credential"),
            RunRejection::FileOnlyCredential { .. } => code!("file-only-credential"),
            RunRejection::NoPrompt => code!("no-prompt"),
            RunRejection::UnknownEngine { .. } => code!("unknown-engine"),
            RunRejection::LaunchRefused { .. } => code!("launch-refused"),
        }
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        match self {
            RunRejection::TurnInFlight { turn } => vec![("turn", (*turn).into())],
            RunRejection::CredentialHashInvalid { length } => vec![("length", (*length).into())],
            RunRejection::NoCredential { engine, variables }
            | RunRejection::FileOnlyCredential { engine, variables } => vec![
                ("engine", engine.as_str().into()),
                ("variables", variables.as_str().into()),
            ],
            RunRejection::UnknownEngine { name, known } => vec![
                ("name", name.as_str().into()),
                ("known", known.as_str().into()),
            ],
            RunRejection::LaunchRefused {
                refusals,
                plan_aborted,
            } => vec![(
                "count",
                (refusals.len() as u64 + u64::from(*plan_aborted)).into(),
            )],
            RunRejection::AlreadyEnqueued
            | RunRejection::NoRun
            | RunRejection::Ended
            | RunRejection::NotItsStream
            | RunRejection::CredentialsNotDistinct
            | RunRejection::NoPrompt => Vec::new(),
        }
    }
}

/// This crate's en-US messages, its name first.
pub const EN_US: (&str, &str) = (
    env!("CARGO_PKG_NAME"),
    include_str!("../locales/en-US/messages.ftl"),
);

/// One value of every rejection, for the catalog test.
#[cfg(any(test, feature = "test-support"))]
pub fn diagnostic_samples() -> Vec<Box<dyn Diagnostic>> {
    vec![
        Box::new(RunRejection::AlreadyEnqueued),
        Box::new(RunRejection::NoRun),
        Box::new(RunRejection::TurnInFlight { turn: 1 }),
        Box::new(RunRejection::Ended),
        Box::new(RunRejection::CredentialHashInvalid { length: 7 }),
        Box::new(RunRejection::NotItsStream),
        Box::new(RunRejection::CredentialsNotDistinct),
        Box::new(RunRejection::NoCredential {
            engine: "claude-code".to_string(),
            variables: "CLAUDE_CODE_OAUTH_TOKEN, ANTHROPIC_API_KEY".to_string(),
        }),
        Box::new(RunRejection::FileOnlyCredential {
            engine: "claude-code".to_string(),
            variables: "CLAUDE_CODE_OAUTH_TOKEN, ANTHROPIC_API_KEY".to_string(),
        }),
        Box::new(RunRejection::NoPrompt),
        Box::new(RunRejection::UnknownEngine {
            name: "bogus".to_string(),
            known: "claude-code, opencode".to_string(),
        }),
        Box::new(RunRejection::LaunchRefused {
            refusals: vec![RunRejection::NoPrompt],
            plan_aborted: true,
        }),
    ]
}
