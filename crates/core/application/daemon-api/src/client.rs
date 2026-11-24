//! The client's version-skew rule (M3, owner 2026-11-12): a client whose
//! full versionator stamp is not the serving daemon's asks the daemon
//! (`GetDaemon`) whether it hosts live runs. None: it stops that daemon
//! (`StopDaemon`) and starts its own. Some: it is refused
//! (`daemon-api-version-skew`, naming both versions and what it hosts), a
//! decline whose fix is `ctxloom daemon stop`, whatever the two versions
//! (owner, OD-61/OD-62: one ctxloom version runs at a time; nothing hands a
//! daemon's work to another version's).

use std::fmt;

use coded_diagnostic::{code, Arg, Diagnostic};
use daemon_ports::{Called, DaemonApi};
use exit_code::{Exit, ExitClass};
/// The one semver "newer" (run-ports'), which the hook's hold judges by
/// too (s4-Q14, H-Q1): re-exported where the skew rule always named it.
pub use run_ports::newer;

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
    /// The daemon could not be asked.
    Call(Called),
    /// Another stamp, that the client may not stop: hosting live runs
    /// (`harps`), or, for a caller that never stops a daemon (OD-3, a
    /// session's MCP server), not older than it. Its call is refused,
    /// naming both stamps.
    Mismatch {
        own: String,
        daemon: String,
        harps: Vec<String>,
    },
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
    let busy = |harps: Vec<String>| SkewError::Mismatch {
        own: own.to_string(),
        daemon: daemon.to_string(),
        harps,
    };
    let request = daemon_proto::v1::GetDaemonRequest {
        name: "daemon".to_string(),
    };
    let found = api.get_daemon(request).await.map_err(SkewError::Call)?;
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

/// OD-3 (owner, 2026-11-13, 2026-11-14: "don't stop the daemon, stop new
/// invocations against the old daemon with an appropriate error message.
/// old daemon should drain and shut down, to be started by the new
/// binary"; OD-61/OD-62, 2026-11-15: no daemon is drained for its
/// version), as a session's call (its MCP server) meets the serving daemon.
/// It never ends what a daemon hosts. Of the same stamp, it is used. Of an
/// OLDER version ([`newer`]) that hosts nothing, it is asked to stop (never
/// `even_if_live`), so the caller starts its own and its call goes there.
/// One that hosts runs (or refuses the stop for them), or one not older (an
/// older or equal version: two binaries never stop each other's daemon in
/// turn), refuses the call ([`SkewError::Mismatch`]).
pub async fn reconcile_session<A: DaemonApi>(
    api: &A,
    own: &str,
    daemon: &str,
) -> Result<Reconciled, SkewError> {
    if own == daemon {
        return Ok(Reconciled::Same);
    }
    if !newer(own, daemon) {
        return Err(SkewError::Mismatch {
            own: own.to_string(),
            daemon: daemon.to_string(),
            harps: Vec::new(),
        });
    }
    reconcile(api, own, daemon).await
}

impl fmt::Display for SkewError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        coded_diagnostic::display(self, f)
    }
}

impl std::error::Error for SkewError {}

impl Diagnostic for SkewError {
    fn code(&self) -> &str {
        match self {
            SkewError::Mismatch { .. } => code!("version-skew"),
            SkewError::Call(called) => called.code(),
        }
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        match self {
            SkewError::Mismatch { own, daemon, harps } => vec![
                ("own", own.as_str().into()),
                ("daemon", daemon.as_str().into()),
                (
                    "harps",
                    match harps.is_empty() {
                        true => "nothing".into(),
                        false => harps.join(", ").into(),
                    },
                ),
            ],
            SkewError::Call(called) => called.args(),
        }
    }
}

/// A daemon of another version is a decline (exit 48); a call that failed
/// is a failure.
impl ExitClass for SkewError {
    fn exit(&self) -> Exit {
        match self {
            SkewError::Mismatch { .. } => Exit::REFUSED,
            SkewError::Call(_) => Exit::FAILED,
        }
    }
}

/// Why a client's call came to nothing: refused (its typed refusal, in
/// its own crate's words, a decline), or the call failed.
#[derive(Clone, Debug, PartialEq)]
pub enum CallError {
    Refused(crate::refusal::Rejection),
    Call(Called),
    /// No daemon could be called: none started, or the one serving declined.
    Unreached(daemon_ports::Unreached),
    /// A launch's lock could not be made or opened (T6).
    LaunchLock {
        detail: String,
    },
}

impl CallError {
    /// What an aggregate launch refusal holds, each said as it would be alone,
    /// in check order and then the plan's fatal findings (owner ruling
    /// 2026-11-21): for the command line to list beside the summary. None for
    /// any other error.
    pub fn refusals(&self) -> Vec<Box<dyn Diagnostic>> {
        match self {
            CallError::Refused(crate::refusal::Rejection::Runs(
                aggregate @ runs::RunRejection::LaunchRefused { .. },
            )) => aggregate
                .refusals()
                .into_iter()
                .map(|refusal| Box::new(refusal) as Box<dyn Diagnostic>)
                .chain(
                    aggregate
                        .findings()
                        .into_iter()
                        .map(|finding| Box::new(finding) as Box<dyn Diagnostic>),
                )
                .collect(),
            _ => Vec::new(),
        }
    }
}

impl From<Called> for CallError {
    fn from(called: Called) -> CallError {
        let rejection = match &called {
            Called::Refused { refusal, .. } => crate::rejection_of(refusal),
            _ => None,
        };
        match rejection {
            Some(rejection) => CallError::Refused(rejection),
            None => CallError::Call(called),
        }
    }
}

impl fmt::Display for CallError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        coded_diagnostic::display(self, f)
    }
}

impl std::error::Error for CallError {}

impl Diagnostic for CallError {
    fn code(&self) -> &str {
        match self {
            CallError::Refused(crate::refusal::Rejection::Api(api)) => api.code(),
            CallError::Refused(crate::refusal::Rejection::Sessions(ours)) => ours.code(),
            CallError::Refused(crate::refusal::Rejection::Removal(removal)) => removal.code(),
            CallError::Refused(crate::refusal::Rejection::Runs(ran)) => ran.code(),
            CallError::Refused(crate::refusal::Rejection::Trust(trust)) => trust.code(),
            CallError::Refused(crate::refusal::Rejection::Approvals(asked)) => asked.code(),
            CallError::Refused(crate::refusal::Rejection::Library(library)) => library.code(),
            CallError::Refused(
                crate::refusal::Rejection::LibraryRead(read)
                | crate::refusal::Rejection::FatalFindings(read),
            ) => read.code(),
            CallError::Call(called) => called.code(),
            CallError::Unreached(unreached) => unreached.code(),
            CallError::LaunchLock { .. } => code!("launch-lock"),
        }
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        match self {
            CallError::Refused(crate::refusal::Rejection::Api(api)) => api.args(),
            CallError::Refused(crate::refusal::Rejection::Sessions(ours)) => ours.args(),
            CallError::Refused(crate::refusal::Rejection::Removal(removal)) => removal.args(),
            CallError::Refused(crate::refusal::Rejection::Runs(ran)) => ran.args(),
            CallError::Refused(crate::refusal::Rejection::Trust(trust)) => trust.args(),
            CallError::Refused(crate::refusal::Rejection::Approvals(asked)) => asked.args(),
            CallError::Refused(crate::refusal::Rejection::Library(library)) => library.args(),
            CallError::Refused(
                crate::refusal::Rejection::LibraryRead(read)
                | crate::refusal::Rejection::FatalFindings(read),
            ) => read.args(),
            CallError::Call(called) => called.args(),
            CallError::Unreached(unreached) => unreached.args(),
            CallError::LaunchLock { detail } => vec![("ext_err_txt", detail.as_str().into())],
        }
    }
}

/// A refusal is a decline (48), as is a daemon this client may not use; a
/// call that failed, or a daemon that could not be started, a failure (32).
impl ExitClass for CallError {
    fn exit(&self) -> Exit {
        match self {
            // Refused over the plan's fatal findings (step 10c U10): 21.
            CallError::Refused(crate::refusal::Rejection::FatalFindings(_)) => Exit::FATAL_FINDINGS,
            CallError::Refused(_) | CallError::Unreached(daemon_ports::Unreached::Declined(_)) => {
                Exit::REFUSED
            }
            CallError::Call(_)
            | CallError::LaunchLock { .. }
            | CallError::Unreached(
                daemon_ports::Unreached::NotStarted { .. } | daemon_ports::Unreached::Exited { .. },
            ) => Exit::FAILED,
        }
    }
}

/// `daemon stop`: ask the daemon to stop (S12), refused while it hosts live
/// runs unless `even_if_live`.
pub async fn stop<A: DaemonApi>(
    api: &A,
    even_if_live: bool,
) -> Result<daemon_proto::v1::Daemon, CallError> {
    let request = daemon_proto::v1::StopDaemonRequest {
        name: "daemon".to_string(),
        even_if_live,
        ..daemon_proto::v1::StopDaemonRequest::default()
    };
    Ok(api.stop_daemon(request).await?)
}

/// Register-or-recheck the client's project (owner, 2026-11-12): what the
/// daemon names it, or why it refused.
pub async fn resolve<A: DaemonApi>(
    api: &A,
    request: daemon_proto::v1::ResolveProjectRequest,
) -> Result<daemon_proto::v1::Project, CallError> {
    Ok(api.resolve_project(request).await?)
}

/// One value of every diagnostic here.
#[cfg(any(test, feature = "test-support"))]
pub(crate) fn samples() -> Vec<Box<dyn Diagnostic>> {
    vec![
        Box::new(SkewError::Mismatch {
            own: "0.2.0+b".to_string(),
            daemon: "0.1.0+a".to_string(),
            harps: vec!["brisk-copper-moth".to_string()],
        }),
        Box::new(CallError::LaunchLock {
            detail: "permission denied".to_string(),
        }),
    ]
}

#[cfg(test)]
#[path = "client.test.rs"]
mod tests;
