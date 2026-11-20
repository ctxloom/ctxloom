//! Why the launch could not go on.

use std::fmt;

use angzarr_diagnostic::{code, Arg, Diagnostic};

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum LaunchError {
    /// The store could not be read for the runs owed a runner.
    CatchUp { ext_err_txt: String },
    /// A file of the session's directory could not be written.
    Record { ext_err_txt: String },
    /// A fact of the launch was not recorded.
    Fact { reason: String },
    /// A run names a session that is not an id.
    BadSession { session: String },
}

impl fmt::Display for LaunchError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        angzarr_diagnostic::display(self, f)
    }
}

impl Diagnostic for LaunchError {
    fn code(&self) -> &str {
        match self {
            LaunchError::CatchUp { .. } => code!("catch-up"),
            LaunchError::Record { .. } => code!("record"),
            LaunchError::Fact { .. } => code!("fact"),
            LaunchError::BadSession { .. } => code!("bad-session"),
        }
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        match self {
            LaunchError::CatchUp { ext_err_txt } | LaunchError::Record { ext_err_txt } => {
                vec![("ext_err_txt", ext_err_txt.as_str().into())]
            }
            LaunchError::Fact { reason } => vec![("reason", reason.as_str().into())],
            LaunchError::BadSession { session } => vec![("session", session.as_str().into())],
        }
    }
}

/// This crate's en-US messages, its name first.
pub const EN_US: (&str, &str) = (
    env!("CARGO_PKG_NAME"),
    include_str!("../locales/en-US/messages.ftl"),
);

/// One value of every error, for the catalog test.
#[cfg(any(test, feature = "test-support"))]
pub fn diagnostic_samples() -> Vec<Box<dyn Diagnostic>> {
    vec![
        Box::new(LaunchError::CatchUp {
            ext_err_txt: "locked".to_string(),
        }),
        Box::new(LaunchError::Record {
            ext_err_txt: "denied".to_string(),
        }),
        Box::new(LaunchError::Fact {
            reason: "refused".to_string(),
        }),
        Box::new(LaunchError::BadSession {
            session: "x".to_string(),
        }),
    ]
}

#[cfg(test)]
#[path = "error.test.rs"]
mod tests;
