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

pub use angzarr_embedded::v1::{CommandBook, Projection};
use coded_diagnostic::{code, Arg, Diagnostic};
use daemon_proto::v1;

pub use prost_types::Any;

/// When a wait ends, by the wall clock: an ask's recorded expiry and a
/// margin (step 8 U5). An instant, not a span, so a call retried on a new
/// daemon ends when the first would have, never a whole span later.
pub type Until = daemon_proto::google::protobuf::Timestamp;

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

    /// `GetTrust` (step 8 U1): a project's trust, what trusting it would
    /// allow and the etag of that. The default is a daemon that does not
    /// serve it.
    fn get_trust(
        &self,
        request: v1::GetTrustRequest,
    ) -> impl Future<Output = Result<v1::Trust, Called>> + Send {
        let _ = request;
        async { Err(unserved("GetTrust")) }
    }

    /// `ListTrusts` (step 8 U1): the projects trusted now. The default is a
    /// daemon that does not serve it.
    fn list_trusts(
        &self,
        request: v1::ListTrustsRequest,
    ) -> impl Future<Output = Result<v1::ListTrustsResponse, Called>> + Send {
        let _ = request;
        async { Err(unserved("ListTrusts")) }
    }

    /// `CreateApproval` (step 8 U4, D10): a session's permission ask
    /// recorded by the daemon, which fills what only it knows (the run, the
    /// engine, the root, the timeout, the name and the options); a retry of
    /// the same request is the same approval. The default is a daemon that
    /// does not serve it.
    fn create_approval(
        &self,
        request: v1::CreateApprovalRequest,
    ) -> impl Future<Output = Result<v1::Approval, Called>> + Send {
        let _ = request;
        async { Err(unserved("CreateApproval")) }
    }

    /// `GetApproval` (step 8 U4): an ask, by its name. The default is a
    /// daemon that does not serve it.
    fn get_approval(
        &self,
        request: v1::GetApprovalRequest,
    ) -> impl Future<Output = Result<v1::Approval, Called>> + Send {
        let _ = request;
        async { Err(unserved("GetApproval")) }
    }

    /// `ListApprovals` (step 8 U4): a project's asks. The default is a
    /// daemon that does not serve it.
    fn list_approvals(
        &self,
        request: v1::ListApprovalsRequest,
    ) -> impl Future<Output = Result<v1::ListApprovalsResponse, Called>> + Send {
        let _ = request;
        async { Err(unserved("ListApprovals")) }
    }

    /// `WaitApproval` (step 8 U4, S27): an ask, answered once it is settled,
    /// waited for until `until` (its own deadline: the ask's recorded
    /// expiry and a margin, never a read's; U5: the expiry is the daemon's
    /// alarm, so the deadline is only a backstop). The default is a daemon
    /// that does not serve it.
    fn wait_approval(
        &self,
        request: v1::WaitApprovalRequest,
        until: Until,
    ) -> impl Future<Output = Result<v1::Approval, Called>> + Send {
        let _ = (request, until);
        async { Err(unserved("WaitApproval")) }
    }

    /// `CreateRun` (U4, T7): a ONE_SHOT run hosted by the daemon, answered
    /// once it is placed (its run and session made, its engine about to
    /// start) or ended before that (its `end`). The default is a daemon
    /// that does not serve it.
    fn create_run(
        &self,
        request: v1::CreateRunRequest,
    ) -> impl Future<Output = Result<v1::Run, Called>> + Send {
        let _ = request;
        async { Err(unserved("CreateRun")) }
    }

    /// `GetRun`: a run the daemon hosts, by its resource name. The default
    /// is a daemon that does not serve it.
    fn get_run(
        &self,
        request: v1::GetRunRequest,
    ) -> impl Future<Output = Result<v1::Run, Called>> + Send {
        let _ = request;
        async { Err(unserved("GetRun")) }
    }

    /// `WatchRun`: a hosted run's notices, its reply, a stop and its end,
    /// in order, as they come, until its end; unbounded in time (a turn may
    /// take as long as its deadline). The default is a daemon that does not
    /// serve it.
    fn watch_run(
        &self,
        request: v1::WatchRunRequest,
    ) -> impl Future<Output = Result<Box<dyn RunEvents>, Called>> + Send {
        let _ = request;
        async { Err(unserved("WatchRun")) }
    }

    /// `RedeliverContext` (step 10c U13): what a live session's hook is to
    /// carry to its engine, a re-assembled context or none. The default is
    /// a daemon that does not serve it.
    fn redeliver_context(
        &self,
        request: v1::RedeliverContextRequest,
    ) -> impl Future<Output = Result<v1::RedeliveredContext, Called>> + Send {
        let _ = request;
        async { Err(unserved("RedeliverContext")) }
    }

    /// `DeliverHookContext` (10c, the lean hook RPC): all a live session's
    /// hook carries, its messages then what it is owed, in one call. The
    /// default is a daemon that does not serve it.
    fn deliver_hook_context(
        &self,
        request: v1::DeliverHookContextRequest,
    ) -> impl Future<Output = Result<v1::HookContext, Called>> + Send {
        let _ = request;
        async { Err(unserved("DeliverHookContext")) }
    }

    /// `AcknowledgeHookDelivery` (message delivery, 2026-11-19): the hook
    /// printed what `DeliverHookContext` offered; record it delivered. The
    /// default is a daemon that does not serve it.
    fn acknowledge_hook_delivery(
        &self,
        request: v1::AcknowledgeHookDeliveryRequest,
    ) -> impl Future<Output = Result<v1::HookDeliveryAcknowledged, Called>> + Send {
        let _ = request;
        async { Err(unserved("AcknowledgeHookDelivery")) }
    }

    /// `WatchLibrary` (step 10c G7): each recorded change of the named
    /// library trees, as a thin notice, from the call on; unbounded in time.
    /// The default is a daemon that does not serve it.
    fn watch_library(
        &self,
        request: v1::WatchLibraryRequest,
    ) -> impl Future<Output = Result<Box<dyn LibraryChanges>, Called>> + Send {
        let _ = request;
        async { Err(unserved("WatchLibrary")) }
    }

    /// `WatchSession` (the terminal wake): the session's signals, the one
    /// it opened with, then each change and a keepalive, until the caller
    /// drops it or the daemon goes; the session is watched while it is
    /// open. The default is a daemon that does not serve it.
    fn watch_session(
        &self,
        request: v1::WatchSessionRequest,
    ) -> impl Future<Output = Result<Box<dyn SessionSignals>, Called>> + Send {
        let _ = request;
        async { Err(unserved("WatchSession")) }
    }

    /// `WatchTop` (`ctxloom top`, DRAFT): the watch's snapshot, then each
    /// change as the daemon pushes it, until the caller drops it, the watch
    /// ends or the daemon goes. The default is a daemon that does not serve it.
    fn watch_top(
        &self,
        request: v1::WatchTopRequest,
    ) -> impl Future<Output = Result<Box<dyn TopEvents>, Called>> + Send {
        let _ = request;
        async { Err(unserved("WatchTop")) }
    }

    /// The coordinator's `Handle` (T16, API Q11): `book`, a domain command
    /// under the id its page names, applied (or found applied: a
    /// DUPLICATE); whether this delivery applied it. Nothing waits for a
    /// projection (a one-shot's `RequestStop`). The default is a daemon that
    /// serves no writes.
    fn handle(&self, book: CommandBook) -> impl Future<Output = Result<bool, Called>> + Send {
        let _ = book;
        async {
            Err(Called::Unimplemented {
                method: HANDLE.to_string(),
            })
        }
    }

    /// `CreateHandOff`: a child's launch environment handed to the daemon
    /// before its `LaunchChild` (T6, S10), its owner checked and its
    /// credential judged first, so a refused one writes nothing (slice 4).
    /// The default is a daemon that does not serve it.
    fn create_hand_off(
        &self,
        request: v1::CreateHandOffRequest,
    ) -> impl Future<Output = Result<v1::HandOff, Called>> + Send {
        let _ = request;
        async { Err(unserved("CreateHandOff")) }
    }

    /// `DeleteHandOff`: a hand-off withdrawn, its `LaunchChild` refused.
    /// The default is a daemon that does not serve it.
    fn delete_hand_off(
        &self,
        request: v1::DeleteHandOffRequest,
    ) -> impl Future<Output = Result<v1::HandOff, Called>> + Send {
        let _ = request;
        async { Err(unserved("DeleteHandOff")) }
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
    /// `RemovalRefusal`, its plan inside); a `session-destroy` build's daemon
    /// serves it (M5). The default is a daemon that does not.
    fn delete_session(
        &self,
        request: v1::DeleteSessionRequest,
    ) -> impl Future<Output = Result<v1::DeleteSessionResponse, Called>> + Send {
        let _ = request;
        async { Err(unserved("DeleteSession")) }
    }

    /// `ImportSessions`: Go's sessions of this account, imported by the
    /// daemon, which sends the commands.
    fn import_sessions(
        &self,
        request: v1::ImportSessionsRequest,
    ) -> impl Future<Output = Result<v1::ImportSessionsResponse, Called>> + Send {
        let _ = request;
        async { Err(unserved("ImportSessions")) }
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

    // The library's reads (step 10a U2, D12): one method each, from the one
    // table; the default is a daemon that does not serve it.
    daemon_proto::library_reads!(library_read_methods);

    // The library's writes (step 10b U3, D12), from their one table.
    daemon_proto::library_writes!(library_write_methods);
}

/// [`DaemonApi`]'s library writes, from `daemon_proto::library_writes!`.
macro_rules! library_write_methods {
    ($($method:ident ($rpc:ident) $request:ident => $response:ident),* $(,)?) => {
        $(
            #[doc = concat!("`", stringify!($rpc), "` (step 10b U3): a write the daemon intends, makes and records.")]
            fn $method(
                &self,
                request: v1::$request,
            ) -> impl Future<Output = Result<v1::$response, Called>> + Send {
                let _ = request;
                async { Err(unserved(stringify!($rpc))) }
            }
        )*
    };
}
use library_write_methods;

/// A library read's answer (step 10a U2): its message, and the notices the
/// read said beside it (`daemon_proto::NOTICE_METADATA`), in order.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Read<T> {
    pub answer: T,
    pub notices: Vec<v1::Notice>,
}

/// [`DaemonApi`]'s library reads, from `daemon_proto::library_reads!`.
macro_rules! library_read_methods {
    ($($method:ident ($rpc:ident) $request:ident . $target:ident => $response:ident),* $(,)?) => {
        $(
            #[doc = concat!("`", stringify!($rpc), "` (step 10a U2), observed first in the daemon.")]
            fn $method(
                &self,
                request: v1::$request,
            ) -> impl Future<Output = Result<Read<v1::$response>, Called>> + Send {
                let _ = request;
                async { Err(unserved(stringify!($rpc))) }
            }
        )*
    };
}
use library_read_methods;

/// A hosted run's events as `WatchRun` streams them (U4): each in order,
/// none once the stream has ended (its `end` was the last), or what broke
/// it (the daemon gone: unreachable).
pub trait RunEvents: Send {
    fn next(&mut self) -> NextEvent<'_>;
}

/// A hosted run's next event, as [`RunEvents::next`] answers it.
pub type NextEvent<'a> =
    std::pin::Pin<Box<dyn Future<Output = Option<Result<v1::RunEvent, Called>>> + Send + 'a>>;

/// A library's recorded changes, as `WatchLibrary` streams them (step 10c
/// G7): each in order, waited for, until the stream ends (`None`) or breaks.
pub trait LibraryChanges: Send {
    fn next(&mut self) -> NextChange<'_>;
}

/// A library's next change, as [`LibraryChanges::next`] answers it.
pub type NextChange<'a> =
    std::pin::Pin<Box<dyn Future<Output = Option<Result<v1::LibraryChange, Called>>> + Send + 'a>>;

/// A watched session's signals as `WatchSession` streams them (the
/// terminal wake): each as it comes, none once the stream ended, or what
/// broke it (the daemon gone).
pub trait SessionSignals: Send {
    fn next(&mut self) -> NextSignal<'_>;
}

/// A watched session's next signal, as [`SessionSignals::next`] answers it.
pub type NextSignal<'a> =
    std::pin::Pin<Box<dyn Future<Output = Option<Result<v1::SessionSignal, Called>>> + Send + 'a>>;

/// A `ctxloom top` watch's events as `WatchTop` streams them: each as it
/// comes, none once the watch ended, or what broke it (the daemon gone).
pub trait TopEvents: Send {
    fn next(&mut self) -> NextTopEvent<'_>;
}

/// A watch's next event, as [`TopEvents::next`] answers it.
pub type NextTopEvent<'a> =
    std::pin::Pin<Box<dyn Future<Output = Option<Result<v1::TopEvent, Called>>> + Send + 'a>>;

/// The coordinator's `Handle`, as a status names it.
pub const HANDLE: &str =
    "/io.angzarr.transport.distributed.v1.CommandHandlerCoordinatorService/Handle";

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
    /// None serves, and the one started for the call exited first: how it
    /// exited (`status`, as the platform says it) and the end of its log
    /// (`tail`), where its stderr went.
    Exited {
        log: String,
        status: String,
        tail: String,
    },
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
            Unreached::Exited { log, status, .. } => f
                .debug_struct("Exited")
                .field("log", log)
                .field("status", status)
                .finish_non_exhaustive(),
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
        coded_diagnostic::display(self, f)
    }
}

impl std::error::Error for Unreached {}

impl Diagnostic for Unreached {
    fn code(&self) -> &str {
        match self {
            Unreached::NotStarted { .. } => code!("not-started"),
            Unreached::Exited { .. } => code!("exited"),
            Unreached::Declined(cause) => cause.code(),
        }
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        match self {
            Unreached::NotStarted { log } => vec![("log", log.as_str().into())],
            Unreached::Exited { log, status, tail } => vec![
                ("log", log.as_str().into()),
                ("status", status.as_str().into()),
                ("tail", tail.as_str().into()),
            ],
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
    /// A failure the daemon said in a diagnostic of its own (step 10a U2:
    /// `library-application-bundle-unreadable`, ...): its code and its args, as text,
    /// said in its own catalog's words.
    Diagnosed {
        code: String,
        args: Vec<(String, String)>,
    },
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
        coded_diagnostic::display(self, f)
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
            Called::Diagnosed { code, .. } => code,
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
            Called::Diagnosed { args, .. } => args
                .iter()
                .map(|(name, value)| (name.as_str(), value.as_str().into()))
                .collect(),
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
        Box::new(Unreached::Exited {
            log: "~/.ctxloom/daemon/daemon.log".to_string(),
            status: "exit status: 101".to_string(),
            tail: "thread 'main' panicked".to_string(),
        }),
    ]
}

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
