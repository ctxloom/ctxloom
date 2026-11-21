//! Why a command to a trust was refused.

use std::fmt;

use angzarr_diagnostic::{code, Arg, Diagnostic};

#[derive(Clone, PartialEq, Eq, Debug)]
pub enum TrustRejection {
    /// A grant whose preview showed settings that have changed since.
    PlanStale,
}

impl fmt::Display for TrustRejection {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        angzarr_diagnostic::display(self, f)
    }
}

impl Diagnostic for TrustRejection {
    fn code(&self) -> &str {
        match self {
            TrustRejection::PlanStale => code!("plan-stale"),
        }
    }

    // No rejection has an arg: an empty list is the only right answer.
    #[cfg_attr(test, mutants::skip)]
    fn args(&self) -> Vec<(&str, Arg)> {
        Vec::new()
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
    vec![Box::new(TrustRejection::PlanStale)]
}
