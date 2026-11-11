//! Why a command did not complete, and how it exits (rulings 149, 152, 153:
//! 10 failed, 20 refused, 21 fatal findings; cli-support's `Exit`).

use std::fmt;

use angzarr_diagnostic::{code, Arg, Diagnostic};
use cli_support::{Exit, ExitClass};

/// This crate's en-US messages (`locales/en-US/messages.ftl`, ids under the
/// crate's name).
pub const EN_US: (&str, &str) = (
    env!("CARGO_PKG_NAME"),
    include_str!("../locales/en-US/messages.ftl"),
);

#[derive(Debug)]
pub enum CliError {
    /// The command line could not be parsed: clap's account of why.
    Usage { detail: String },
    /// A verb that is none of the command's: what to try instead.
    UnknownVerb {
        verb: String,
        command: String,
        choices: String,
    },
    /// A `--format` that is neither text nor json.
    Format { value: String },
    /// The answer could not be written out.
    Output { detail: String },
    /// A command that failed in its own adapter or its use case: its
    /// diagnostic, and the exit code it asks for.
    Command {
        error: Box<dyn Diagnostic>,
        exit: Exit,
    },
    /// What an extension's command said: its own diagnostic and exit code.
    Extension(crate::extension::Failure),
}

impl ExitClass for CliError {
    /// How the command exits: a usage error is refused (20, never clap's 2),
    /// an answer that could not be written failed (10).
    fn exit(&self) -> Exit {
        match self {
            CliError::Usage { .. } | CliError::UnknownVerb { .. } | CliError::Format { .. } => {
                Exit::REFUSED
            }
            CliError::Output { .. } => Exit::FAILED,
            CliError::Command { exit, .. } => *exit,
            CliError::Extension(failure) => failure.exit,
        }
    }
}

impl fmt::Display for CliError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        angzarr_diagnostic::display(self, f)
    }
}

impl Diagnostic for CliError {
    fn code(&self) -> &str {
        match self {
            CliError::Usage { .. } => code!("usage"),
            CliError::UnknownVerb { .. } => code!("unknown-verb"),
            CliError::Format { .. } => code!("unknown-format"),
            CliError::Output { .. } => code!("output"),
            CliError::Command { error, .. } => error.code(),
            CliError::Extension(failure) => failure.cause.code(),
        }
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        match self {
            CliError::Usage { detail } | CliError::Output { detail } => {
                vec![("ext_err_txt", detail.as_str().into())]
            }
            CliError::UnknownVerb {
                verb,
                command,
                choices,
            } => vec![
                ("verb", verb.as_str().into()),
                ("command", command.as_str().into()),
                ("choices", choices.as_str().into()),
            ],
            CliError::Format { value } => vec![("value", value.as_str().into())],
            CliError::Command { error, .. } => error.args(),
            CliError::Extension(failure) => failure.cause.args(),
        }
    }
}

/// One value of every error here, for a catalog test.
#[cfg(any(test, feature = "test-support"))]
pub fn diagnostic_samples() -> Vec<Box<dyn Diagnostic>> {
    vec![
        Box::new(CliError::Usage {
            detail: "unexpected argument".to_string(),
        }),
        Box::new(CliError::UnknownVerb {
            verb: "frobnicate".to_string(),
            command: "ctxloom".to_string(),
            choices: "version".to_string(),
        }),
        Box::new(CliError::Format {
            value: "yaml".to_string(),
        }),
        Box::new(CliError::Output {
            detail: "broken pipe".to_string(),
        }),
    ]
}

#[cfg(test)]
#[path = "error.test.rs"]
mod tests;
