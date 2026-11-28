//! Why the launch could not go on.

use std::fmt;

use coded_diagnostic::{code, Arg, Diagnostic};

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
    /// A run's enqueue records no launch spec: it was enqueued before the
    /// record held one, and its launch is not made from nothing.
    NoSpec { run: String },
    /// A run's record holds what cannot be read back (a sealed value that
    /// does not open, a hooks document that is no document).
    Unreadable { run: String, reason: String },
}

impl fmt::Display for LaunchError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        coded_diagnostic::display(self, f)
    }
}

impl Diagnostic for LaunchError {
    fn code(&self) -> &str {
        match self {
            LaunchError::CatchUp { .. } => code!("catch-up"),
            LaunchError::Record { .. } => code!("record"),
            LaunchError::Fact { .. } => code!("fact"),
            LaunchError::BadSession { .. } => code!("bad-session"),
            LaunchError::NoSpec { .. } => code!("no-spec"),
            LaunchError::Unreadable { .. } => code!("unreadable"),
        }
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        match self {
            LaunchError::CatchUp { ext_err_txt } | LaunchError::Record { ext_err_txt } => {
                vec![("ext_err_txt", ext_err_txt.as_str().into())]
            }
            LaunchError::Fact { reason } => vec![("reason", reason.as_str().into())],
            LaunchError::BadSession { session } => vec![("session", session.as_str().into())],
            LaunchError::NoSpec { run } => vec![("run", run.as_str().into())],
            LaunchError::Unreadable { run, reason } => vec![
                ("run", run.as_str().into()),
                ("reason", reason.as_str().into()),
            ],
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
        Box::new(LaunchError::NoSpec {
            run: "r".to_string(),
        }),
        Box::new(LaunchError::Unreadable {
            run: "r".to_string(),
            reason: "does not open".to_string(),
        }),
        Box::new(crate::ContextRedelivered {
            changed: vec!["demo".to_string()],
        }),
        Box::new(crate::ContextChangedOnly {
            changed: vec!["demo".to_string()],
        }),
        Box::new(crate::ContextTooLong {
            changed: vec!["demo".to_string()],
        }),
    ]
}

#[cfg(test)]
#[path = "error.test.rs"]
mod tests;
