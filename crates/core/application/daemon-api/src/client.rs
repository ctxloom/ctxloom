//! The client's version-skew rule (M3, owner 2026-11-12): a client whose
//! full versionator stamp is not the serving daemon's asks the daemon
//! (`GetDaemon`) whether it hosts live runs. None: it stops that daemon
//! (`StopDaemon`) and starts its own. Some: it is refused, a decline whose
//! fix is `ctxloom daemon stop --even-if-live`.

use std::fmt;

use angzarr_diagnostic::{code, Arg, Diagnostic};
use daemon_ports::{Called, DaemonApi};
use exit_code::{Exit, ExitClass};

/// What the client does with the daemon it found.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Reconciled {
    /// The same stamp: it uses the daemon.
    Same,
    /// Another stamp, hosting nothing: it was asked to stop, and the client
    /// starts its own.
    Stopped,
}

/// Why the client cannot use the daemon it found.
#[derive(Clone, Debug, PartialEq)]
pub enum SkewError {
    /// Another stamp, hosting live runs.
    Busy {
        own: String,
        daemon: String,
        harps: Vec<String>,
    },
    /// The daemon could not be asked.
    Call(Called),
}

/// Reconcile the client's stamp `own` with the serving daemon's, `daemon`,
/// through `api` (that daemon's).
pub async fn reconcile<A: DaemonApi>(
    api: &A,
    own: &str,
    daemon: &str,
) -> Result<Reconciled, SkewError> {
    if own == daemon {
        return Ok(Reconciled::Same);
    }
    let busy = |harps: Vec<String>| SkewError::Busy {
        own: own.to_string(),
        daemon: daemon.to_string(),
        harps,
    };
    let found = api.get_daemon().await.map_err(SkewError::Call)?;
    if !found.runs.is_empty() {
        return Err(busy(found.runs.into_iter().map(|run| run.harp).collect()));
    }
    let stop = daemon_proto::v1::StopDaemonRequest {
        name: found.name,
        ..daemon_proto::v1::StopDaemonRequest::default()
    };
    match api.stop_daemon(stop).await {
        Ok(_) => Ok(Reconciled::Stopped),
        // A run started between the two calls.
        Err(called) => match called.refusal::<daemon_proto::v1::DaemonHosting>() {
            Some(hosting) => Err(busy(hosting.harps)),
            None => Err(SkewError::Call(called)),
        },
    }
}

impl fmt::Display for SkewError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        angzarr_diagnostic::display(self, f)
    }
}

impl std::error::Error for SkewError {}

impl Diagnostic for SkewError {
    fn code(&self) -> &str {
        match self {
            SkewError::Busy { .. } => code!("version-skew-busy"),
            SkewError::Call(called) => called.code(),
        }
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        match self {
            SkewError::Busy { own, daemon, harps } => vec![
                ("own", own.as_str().into()),
                ("daemon", daemon.as_str().into()),
                ("harps", harps.join(", ").into()),
            ],
            SkewError::Call(called) => called.args(),
        }
    }
}

/// A busy daemon of another version is a decline (exit 20); a call that
/// failed is a failure.
impl ExitClass for SkewError {
    fn exit(&self) -> Exit {
        match self {
            SkewError::Busy { .. } => Exit::REFUSED,
            SkewError::Call(_) => Exit::FAILED,
        }
    }
}

/// One value of every diagnostic here.
#[cfg(any(test, feature = "test-support"))]
pub(crate) fn samples() -> Vec<Box<dyn Diagnostic>> {
    vec![Box::new(SkewError::Busy {
        own: "0.2.0+b".to_string(),
        daemon: "0.1.0+a".to_string(),
        harps: vec!["brisk-copper-moth".to_string()],
    })]
}

#[cfg(test)]
#[path = "client.test.rs"]
mod tests;
