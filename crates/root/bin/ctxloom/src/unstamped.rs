//! The binary refuses to run without a whole version stamp (Go's rule): a
//! build that cannot name itself cannot be told apart from any other. The
//! build script refuses first; this is the second guard, for a binary its
//! stamp never reached.

use std::fmt;

use angzarr_diagnostic::{code, Arg, Diagnostic};

use crate::stamp;

/// A stamp not of the shape [`stamp::valid`] holds it to.
#[derive(Debug)]
pub(crate) struct Unstamped {
    stamp: String,
}

impl fmt::Display for Unstamped {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        angzarr_diagnostic::display(self, f)
    }
}

impl Diagnostic for Unstamped {
    fn code(&self) -> &str {
        code!("unstamped")
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        vec![("stamp", self.stamp.as_str().into())]
    }
}

impl std::error::Error for Unstamped {}

/// `version`, when it is a whole stamp.
pub(crate) fn checked(version: &str) -> Result<&str, Unstamped> {
    if stamp::valid(version) {
        Ok(version)
    } else {
        Err(Unstamped {
            stamp: version.to_string(),
        })
    }
}

/// One of each, for `ctxloom test diagnostics`.
#[cfg(feature = "test-support")]
pub(crate) fn diagnostic_samples() -> Vec<Box<dyn Diagnostic>> {
    vec![Box::new(Unstamped {
        stamp: String::new(),
    })]
}

#[cfg(test)]
#[path = "unstamped.test.rs"]
mod tests;
