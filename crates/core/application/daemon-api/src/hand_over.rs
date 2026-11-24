//! The drain's warning (owner ruling OD-54): a daemon asked to drain while
//! it hosts children says AT ONCE what it will do with them, as this
//! notice in `StopDaemon`'s and `GetDaemon`'s `Daemon.warnings`, said by the
//! command that asked (and any that reaches it meanwhile): a child busy in
//! a turn is steered to end it, and handed over once the hand-over's period
//! has passed at the latest, its turn in flight cut; the next daemon
//! resumes each in its own conversation.

use std::fmt;

use coded_diagnostic::{code, Arg, Diagnostic};
use daemon_proto::v1;

/// The daemon of the stamp `daemon` hands `harps` over to the next daemon,
/// a busy one within `seconds`.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct HandingOver {
    pub daemon: String,
    pub harps: Vec<String>,
    pub seconds: u64,
}

impl HandingOver {
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

impl fmt::Display for HandingOver {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        coded_diagnostic::display(self, f)
    }
}

impl std::error::Error for HandingOver {}

impl Diagnostic for HandingOver {
    fn code(&self) -> &str {
        code!("handing-over")
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        vec![
            ("daemon", self.daemon.as_str().into()),
            ("harps", self.harps.join(", ").into()),
            ("seconds", self.seconds.to_string().into()),
        ]
    }
}

#[cfg(any(test, feature = "test-support"))]
pub(crate) fn samples() -> Vec<Box<dyn Diagnostic>> {
    vec![Box::new(HandingOver {
        daemon: "v0.1.0-a".to_string(),
        harps: vec!["brisk-copper-moth".to_string()],
        seconds: 300,
    })]
}

#[cfg(test)]
#[path = "hand_over.test.rs"]
mod tests;
