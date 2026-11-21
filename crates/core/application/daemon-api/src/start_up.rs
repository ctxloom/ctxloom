//! A daemon whose start-up catch-up (or its sweep) failed (owner ruling
//! OD-52): it STILL serves, and says so, loud. The failure is kept on the
//! daemon's own state (its loop's, [`crate::Hosting::start_up_failed`]),
//! answered in `GetDaemon`'s `Daemon.warnings` as this notice, and said by
//! every command that reaches that daemon: what it answers may be missing
//! events. A later catch-up that succeeds (the daemon tries again each
//! round) clears it.

use std::fmt;

use angzarr_diagnostic::{code, Arg, Diagnostic};
use daemon_proto::v1;

/// The daemon's start-up catch-up or sweep failed, `why` its own words.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct StartUpIncomplete {
    pub why: String,
}

impl StartUpIncomplete {
    /// The notice `Daemon.warnings` carries: its code, its args and its
    /// en-US text.
    pub fn notice(&self) -> v1::Notice {
        v1::Notice {
            code: self.code().to_string(),
            args: run_application::client::flat_args(self),
            text: self.to_string(),
        }
    }
}

impl fmt::Display for StartUpIncomplete {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        angzarr_diagnostic::display(self, f)
    }
}

impl std::error::Error for StartUpIncomplete {}

impl Diagnostic for StartUpIncomplete {
    fn code(&self) -> &str {
        code!("start-up-incomplete")
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        vec![("why", self.why.as_str().into())]
    }
}

#[cfg(any(test, feature = "test-support"))]
pub(crate) fn samples() -> Vec<Box<dyn Diagnostic>> {
    vec![Box::new(StartUpIncomplete {
        why: "the board's catch-up: the store is locked".to_string(),
    })]
}

#[cfg(test)]
#[path = "start_up.test.rs"]
mod tests;
