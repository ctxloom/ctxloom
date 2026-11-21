//! The faults of the command line itself.

use std::fmt;

use angzarr_diagnostic::{code, Arg, Diagnostic};

#[derive(Clone, PartialEq, Eq, Debug)]
pub enum CliFault {
    /// The answer could not be written out.
    Output { ext_err_txt: String },
    /// A noun the program gave this crate that it does not own.
    NoSuchCommand { noun: String },
    /// A notice, not a failure: no agent token is exported, so delegated
    /// agents cannot authenticate (init guides her to mint one).
    TokenNotExported,
}

impl fmt::Display for CliFault {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        angzarr_diagnostic::display(self, f)
    }
}

impl Diagnostic for CliFault {
    fn code(&self) -> &str {
        match self {
            CliFault::Output { .. } => code!("output"),
            CliFault::NoSuchCommand { .. } => code!("no-such-command"),
            CliFault::TokenNotExported => code!("token-not-exported"),
        }
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        match self {
            CliFault::Output { ext_err_txt } => vec![("ext_err_txt", ext_err_txt.as_str().into())],
            CliFault::NoSuchCommand { noun } => vec![("noun", noun.as_str().into())],
            CliFault::TokenNotExported => vec![
                (
                    "variables",
                    "CLAUDE_CODE_OAUTH_TOKEN or ANTHROPIC_API_KEY".into(),
                ),
                ("export", "CLAUDE_CODE_OAUTH_TOKEN".into()),
                ("command", "claude setup-token".into()),
            ],
        }
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
    vec![
        Box::new(CliFault::Output {
            ext_err_txt: "broken pipe".to_string(),
        }),
        Box::new(CliFault::NoSuchCommand {
            noun: "doctor".to_string(),
        }),
        Box::new(CliFault::TokenNotExported),
    ]
}

#[cfg(test)]
#[path = "fault.test.rs"]
mod tests;
