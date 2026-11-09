//! Why the run's hook listener could not be started or stopped.

use std::fmt;
use std::path::Path;

use angzarr_coordinator_grpc::ListenError;
use angzarr_diagnostic::{code, Arg, Diagnostic};

/// A listener that could not be bound, or did not stop cleanly.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum ListenerError {
    /// The socket could not be bound: its directory is not ours at mode 0700,
    /// another run serves it, the path is not a socket, or a system call
    /// failed. `cause` is the listener's own account.
    Bind {
        endpoint: String,
        cause: ListenError,
    },
    /// The address to bind is unspecified (0.0.0.0, ::): on every interface.
    /// A hook listener never binds one.
    Unspecified { addr: String },
    /// The open calls did not finish within the grace: the listener was
    /// aborted and its socket removed.
    StopTimedOut { endpoint: String, grace_ms: u64 },
    /// The listener did not stop cleanly.
    Stop {
        endpoint: String,
        cause: ListenError,
    },
}

impl ListenerError {
    pub(crate) fn bind(endpoint: &Path, cause: &ListenError) -> ListenerError {
        ListenerError::Bind {
            endpoint: endpoint.display().to_string(),
            cause: cause.clone(),
        }
    }

    pub(crate) fn timed_out(endpoint: &str, grace: std::time::Duration) -> ListenerError {
        ListenerError::StopTimedOut {
            endpoint: endpoint.to_string(),
            grace_ms: u64::try_from(grace.as_millis()).unwrap_or(u64::MAX),
        }
    }

    pub(crate) fn stop(endpoint: &str, cause: &ListenError) -> ListenerError {
        ListenerError::Stop {
            endpoint: endpoint.to_string(),
            cause: cause.clone(),
        }
    }
}

impl fmt::Display for ListenerError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        angzarr_diagnostic::display(self, f)
    }
}

impl Diagnostic for ListenerError {
    fn code(&self) -> &str {
        match self {
            ListenerError::Bind { .. } => code!("bind"),
            ListenerError::Stop { .. } => code!("stop"),
            ListenerError::Unspecified { .. } => code!("bind-unspecified"),
            ListenerError::StopTimedOut { .. } => code!("stop-timed-out"),
        }
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        match self {
            ListenerError::Unspecified { addr } => vec![("addr", addr.as_str().into())],
            ListenerError::StopTimedOut { endpoint, grace_ms } => vec![
                ("endpoint", endpoint.as_str().into()),
                ("grace_ms", (*grace_ms).into()),
            ],
            ListenerError::Bind { endpoint, cause } | ListenerError::Stop { endpoint, cause } => {
                vec![
                    ("endpoint", endpoint.as_str().into()),
                    ("cause", Arg::nested(cause)),
                ]
            }
        }
    }
}

impl std::error::Error for ListenerError {}

/// This crate's en-US messages, its name first.
pub const EN_US: (&str, &str) = (
    env!("CARGO_PKG_NAME"),
    include_str!("../locales/en-US/messages.ftl"),
);

/// The messages of this crate and of the listener it serves with, whose
/// refusal a bind failure carries as its cause.
pub const CATALOGS: [(&str, &str); 2] = [EN_US, angzarr_coordinator_grpc::EN_US];

/// One value of every error, for the catalog test.
#[cfg(any(test, feature = "test-support"))]
pub fn diagnostic_samples() -> Vec<Box<dyn Diagnostic>> {
    let cause = ListenError::SocketInUse {
        path: "/run/user/1000/ctxloom/0011223344556677.sock".to_string(),
    };
    vec![
        Box::new(ListenerError::bind(
            Path::new("/run/user/1000/ctxloom/0011223344556677.sock"),
            &cause,
        )),
        Box::new(ListenerError::Unspecified {
            addr: "0.0.0.0:0".to_string(),
        }),
        Box::new(ListenerError::timed_out(
            "/run/user/1000/ctxloom/0011223344556677.sock",
            std::time::Duration::from_secs(3),
        )),
        Box::new(ListenerError::stop(
            "/run/user/1000/ctxloom/0011223344556677.sock",
            &cause,
        )),
    ]
}

#[cfg(test)]
#[path = "error.test.rs"]
mod tests;
