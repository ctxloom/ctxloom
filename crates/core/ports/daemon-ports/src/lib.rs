//! The daemon's API as its clients reach it (owner M7, slice 2 step 5): the
//! `Ctxloom` service's methods (`proto/ctxloom/v1/daemon.proto`) as a
//! port of the application layer, so a command calls its application,
//! which calls this, and never a transport. `daemon-client` implements it
//! over tonic (the pinned certificate and the user bearer the discovery
//! file names).
//!
//! A method takes and answers the service's own messages (generated, pure
//! data). What a call can come to besides its answer is [`Called`]: refused
//! with a typed refusal (S11: the refusal's status, the message itself from
//! `google.rpc.Status.details`), the credential not taken, the method not
//! served (a plain build's `DeleteSession`, M5; another version's daemon),
//! the daemon not reached, or another failure.
//!
//! A domain WRITE is not a method of the service (T16, S1 amended): it is
//! the domain's command, sent through angzarr's command coordinator beside
//! it, `HandleSync` ([`DaemonApi::handle_sync`], slice 3 step 4), under the
//! command id the client chose, so the one retry on a new daemon is a
//! DUPLICATE.

use std::fmt;
use std::future::Future;

use angzarr_diagnostic::{code, Arg, Diagnostic};
pub use angzarr_embedded::v1::{CommandBook, Projection};
use daemon_proto::v1;

pub use prost_types::Any;

/// The daemon's `Ctxloom` service, as a client calls it. Each method is
/// one RPC; the ones not served yet are not here.
pub trait DaemonApi: Send + Sync {
    /// `GetDaemon`: the daemon (a singleton), what it hosts and its idle time.
    fn get_daemon(
        &self,
        request: v1::GetDaemonRequest,
    ) -> impl Future<Output = Result<v1::Daemon, Called>> + Send;

    /// `StopDaemon`: refused `DaemonHosting` while it hosts live runs,
    /// unless `even_if_live`.
    fn stop_daemon(
        &self,
        request: v1::StopDaemonRequest,
    ) -> impl Future<Output = Result<v1::Daemon, Called>> + Send;

    /// `ResolveProject`: register-or-recheck the client's project.
    fn resolve_project(
        &self,
        request: v1::ResolveProjectRequest,
    ) -> impl Future<Output = Result<v1::Project, Called>> + Send;

    /// `GetProject`: a project by its resource name.
    fn get_project(
        &self,
        request: v1::GetProjectRequest,
    ) -> impl Future<Output = Result<v1::Project, Called>> + Send;

    /// `GetSession`: a session by its resource name.
    fn get_session(
        &self,
        request: v1::GetSessionRequest,
    ) -> impl Future<Output = Result<v1::Session, Called>> + Send;

    /// `ListSessions`: a project's sessions (`projects/-`: every project's).
    fn list_sessions(
        &self,
        request: v1::ListSessionsRequest,
    ) -> impl Future<Output = Result<v1::ListSessionsResponse, Called>> + Send;

    /// `SearchSessions`: the sessions whose words match.
    fn search_sessions(
        &self,
        request: v1::SearchSessionsRequest,
    ) -> impl Future<Output = Result<v1::ListSessionsResponse, Called>> + Send;

    /// `GetTranscript`: a session's transcript, its D27 lines as delivered
    /// so far (slice 3 step 5). The default is a daemon that does not
    /// serve it.
    fn get_transcript(
        &self,
        request: v1::GetTranscriptRequest,
    ) -> impl Future<Output = Result<v1::Transcript, Called>> + Send {
        let _ = request;
        async { Err(unserved("GetTranscript")) }
    }

    /// `AssembleContext`: a project's context, assembled in the daemon from
    /// its registered root (slice 3 step 6). The default is a daemon that
    /// does not serve it.
    fn assemble_context(
        &self,
        request: v1::AssembleContextRequest,
    ) -> impl Future<Output = Result<v1::AssembledContext, Called>> + Send {
        let _ = request;
        async { Err(unserved("AssembleContext")) }
    }

    /// The coordinator's `HandleSync` (T16): `book`, a domain command under
    /// the id its page names, applied (or found applied: a DUPLICATE), and
    /// answered once each of `projectors` has projected its events, with
    /// what each projected. The default is a daemon that serves no writes.
    fn handle_sync(
        &self,
        book: CommandBook,
        projectors: Vec<String>,
    ) -> impl Future<Output = Result<Synced, Called>> + Send {
        let _ = (book, projectors);
        async {
            Err(Called::Unimplemented {
                method: HANDLE_SYNC.to_string(),
            })
        }
    }

    /// `DeleteSession`: a session removed whole or not at all (refused
    /// `RemovalRefused`, its plan inside); a `session-destroy` build's daemon
    /// serves it (M5). The default is a daemon that does not.
    fn delete_session(
        &self,
        request: v1::DeleteSessionRequest,
    ) -> impl Future<Output = Result<v1::DeleteSessionResponse, Called>> + Send {
        let _ = request;
        async { Err(unserved("DeleteSession")) }
    }

    /// `SweepSessions`: every session by the sweep's table; as
    /// `DeleteSession`, a `session-destroy` build's.
    fn sweep_sessions(
        &self,
        request: v1::SweepSessionsRequest,
    ) -> impl Future<Output = Result<v1::SweepSessionsResponse, Called>> + Send {
        let _ = request;
        async { Err(unserved("SweepSessions")) }
    }
}

/// The coordinator's `HandleSync`, as a status names it.
pub const HANDLE_SYNC: &str =
    "/io.angzarr.transport.distributed.v1.CommandHandlerCoordinatorService/HandleSync";

/// What a `HandleSync` came to: the command applied now, or found applied
/// before (a DUPLICATE), and each projector's projection, in the order
/// they were named.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Synced {
    /// Whether this delivery applied it (false: a DUPLICATE of one that did).
    pub applied: bool,
    pub projections: Vec<Projection>,
}

impl Synced {
    /// The projection of `projector`, as `T`, if it answered one.
    pub fn projection<T: prost::Name + Default>(&self, projector: &str) -> Option<T> {
        self.projections
            .iter()
            .find(|projection| projection.projector == projector)
            .and_then(|projection| projection.projection.as_ref())
            .and_then(|any| any.to_msg().ok())
    }
}

/// A method the daemon does not serve, as a call is answered.
fn unserved(method: &str) -> Called {
    Called::Unimplemented {
        method: format!("/ctxloom.v1.Ctxloom/{method}"),
    }
}

/// The user's daemon as a client reaches it (S14, S16): the one serving,
/// found, or one started for the call and waited for. The root implements it
/// over the discovery file, the health check and the spawn.
pub trait Daemons: Send + Sync {
    /// A daemon's API, as this client calls it.
    type Api: DaemonApi;

    /// The daemon serving now, if one does. It never starts one (S14:
    /// `daemon status|stop` are answered locally when none serves).
    fn serving(&self) -> impl Future<Output = Result<Option<Self::Api>, Unreached>> + Send;

    /// A daemon to call: the one serving, else one started and waited for,
    /// within S16's bound.
    fn reach(&self) -> impl Future<Output = Result<Self::Api, Unreached>> + Send;
}

/// Why no daemon could be called.
#[derive(Clone)]
pub enum Unreached {
    /// None serves, and none could be started: where its log is.
    NotStarted { log: String },
    /// One serves that this client may not use, said in its own crate's
    /// words (another ctxloom home's daemon; a busy daemon of another
    /// version, M3): a decline.
    Declined(std::sync::Arc<dyn Diagnostic + Send + Sync>),
}

impl fmt::Debug for Unreached {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Unreached::NotStarted { log } => {
                f.debug_struct("NotStarted").field("log", log).finish()
            }
            Unreached::Declined(cause) => f.debug_tuple("Declined").field(&cause.code()).finish(),
        }
    }
}

/// The same outcome: the same code and args.
impl PartialEq for Unreached {
    fn eq(&self, other: &Unreached) -> bool {
        let said = |u: &Unreached| -> (String, String) {
            let args = u
                .args()
                .iter()
                .map(|(name, arg)| format!("{name}={arg:?}"))
                .collect::<Vec<_>>()
                .join(",");
            (u.code().to_string(), args)
        };
        said(self) == said(other)
    }
}

impl fmt::Display for Unreached {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        angzarr_diagnostic::display(self, f)
    }
}

impl std::error::Error for Unreached {}

impl Diagnostic for Unreached {
    fn code(&self) -> &str {
        match self {
            Unreached::NotStarted { .. } => code!("not-started"),
            Unreached::Declined(cause) => cause.code(),
        }
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        match self {
            Unreached::NotStarted { log } => vec![("log", log.as_str().into())],
            Unreached::Declined(cause) => cause.args(),
        }
    }
}

/// What a call came to, when it was not answered.
#[derive(Clone, Debug, PartialEq)]
pub enum Called {
    /// Refused (S11): the gRPC code its refusal's `(refusal_status)`
    /// declares, and the typed refusal message the daemon packed.
    Refused { code: i32, refusal: Any },
    /// The credential was not taken (a bare UNAUTHENTICATED: no reason).
    Unauthenticated,
    /// The daemon does not serve the method (`/<service>/<method>`).
    Unimplemented { method: String },
    /// No daemon answered at the endpoint.
    Unreachable { detail: String },
    /// Any other status: its gRPC code and the text it carried.
    Failed { code: i32, detail: String },
}

impl Called {
    /// The typed refusal, as `T`, if the call was refused with one.
    pub fn refusal<T: prost::Name + Default>(&self) -> Option<T> {
        match self {
            Called::Refused { refusal, .. } => refusal.to_msg().ok(),
            _ => None,
        }
    }
}

impl fmt::Display for Called {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        angzarr_diagnostic::display(self, f)
    }
}

impl std::error::Error for Called {}

impl Diagnostic for Called {
    fn code(&self) -> &str {
        match self {
            Called::Refused { .. } => code!("refused"),
            Called::Unauthenticated => code!("unauthenticated"),
            Called::Unimplemented { .. } => code!("unimplemented"),
            Called::Unreachable { .. } => code!("unreachable"),
            Called::Failed { .. } => code!("failed"),
        }
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        match self {
            Called::Refused { refusal, .. } => vec![("code", refusal.type_url.as_str().into())],
            Called::Unauthenticated => Vec::new(),
            Called::Unimplemented { method } => vec![("method", method.as_str().into())],
            Called::Unreachable { detail } => vec![("ext_err_txt", detail.as_str().into())],
            Called::Failed { code, detail } => vec![
                ("code", i64::from(*code).into()),
                ("ext_err_txt", detail.as_str().into()),
            ],
        }
    }
}

/// This crate's en-US messages, its name first.
pub const EN_US: (&str, &str) = (
    env!("CARGO_PKG_NAME"),
    include_str!("../locales/en-US/messages.ftl"),
);

/// One value of every variant, for the catalog test.
#[cfg(any(test, feature = "test-support"))]
pub fn diagnostic_samples() -> Vec<Box<dyn Diagnostic>> {
    vec![
        Box::new(Called::Refused {
            code: 9,
            refusal: Any::default(),
        }),
        Box::new(Called::Unauthenticated),
        Box::new(Called::Unimplemented {
            method: "/ctxloom.v1.Ctxloom/DeleteSession".to_string(),
        }),
        Box::new(Called::Unreachable {
            detail: "connection refused".to_string(),
        }),
        Box::new(Called::Failed {
            code: 13,
            detail: "boom".to_string(),
        }),
        Box::new(Unreached::NotStarted {
            log: "~/.ctxloom/daemon/daemon.log".to_string(),
        }),
    ]
}

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
