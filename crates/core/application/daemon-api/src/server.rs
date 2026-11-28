//! The use cases the daemon's `Ctxloom` service serves (slice 2 step 5).
//! Each answers the service's own message, read from the warm session
//! index the daemon's kernel keeps (S32): nothing replays the store per
//! call. `ResolveProject` writes through the core's edge (its `projects`
//! commands, keyed, so a retry is a DUPLICATE) and reads the project's own
//! stream.
//!
//! What the daemon's loop alone knows (what it hosts, how long it has been
//! idle, how to stop it) is the [`Hosting`] port; a session's state is its
//! lock's ([`SessionStates`]); a project's harp candidates are minted
//! outside the core ([`Harps`]).

use std::future::Future;
use std::pin::Pin;
use std::sync::Arc;

use angzarr_embedded::v1::{event_page, Cover};
use angzarr_embedded::{Failure, Payload, Timestamp};
use daemon_proto::v1;
use daemon_proto::Carries as _;
use pending_messages::PendingMessages;
use run_ports::Edge;
use session_index::{title_of, Captured, SessionIndex, SessionIndexes, SessionRecord, State, View};
use sessions::{
    keyed_book, project_id_root, ClaimProjectName, NameProject, ProjectEvent, ProjectName,
    RegisterProject, SessionName, SessionRejection, PROJECTS_DOMAIN, PROJECT_NAMES_DOMAIN,
};

use daemon_api_contract::{ApiError, ApiRefusal};

/// The daemon's singleton name (AIP-156).
const DAEMON: &str = "daemon";

/// A session's transcript, as its resource name ends.
const TRANSCRIPT: &str = "/transcript";

/// A `ListSessions` parent naming every project.
pub(crate) const EVERY_PROJECT: &str = "projects/-";

/// What the daemon's loop knows of itself.
pub trait Hosting: Send + Sync {
    /// Where it serves (`https://127.0.0.1:<port>`), once its port is bound.
    fn endpoint(&self) -> String;
    /// The runs it hosts now (live), each with its session's name.
    fn hosted(&self) -> Vec<v1::HostedRun>;
    /// How long it has had nothing to do.
    fn idle_seconds(&self) -> u64;
    /// Ask it to stop: drain, or end what it hosts first (`end_hosted`).
    fn stop(&self, end_hosted: bool);
    /// Ask it to hand what it hosts over to the next daemon and exit
    /// (OD-54), waiting `within` for what it hosts (none: its configured
    /// `daemon.drain_timeout`).
    fn drain(&self, within: Option<std::time::Duration>);
    /// Whether it was asked to drain.
    fn draining(&self) -> bool;
    /// How long the drain in progress waits for a busy child, in seconds
    /// (the period the loop last read; OD-54, owner 2026-11-15).
    fn hand_over_seconds(&self) -> u64;
    /// Wait for its next round, which catches its read models up with what
    /// other processes committed, so a read answers what was committed
    /// before it was asked (bounded: a round that does not come is waited
    /// for no longer than the loop's own pace allows).
    fn fresh(&self) -> Pin<Box<dyn Future<Output = ()> + Send + '_>>;
    /// Why its start-up catch-up (or its sweep) failed, while no later
    /// catch-up has succeeded (OD-52): it serves anyway, and says so.
    fn start_up_failed(&self) -> Option<String>;
    /// The daemon's home's ctxloom directory, once it is gone (OD-60; owner
    /// ruling 2026-11-17: the daemon never makes a file in its home again
    /// once it has started); none while it is there, or cannot be told.
    fn home_gone(&self) -> Option<String>;
    /// The rounds its loop has finished (step 10c U13, G5(b)); none told,
    /// none.
    // Equivalent: its one mutant (`1`) is the count of a loop that tells
    // none, which no daemon is.
    #[cfg_attr(test, mutants::skip)]
    fn rounds(&self) -> u32 {
        0
    }
    /// What the session `session` (its stable id), which it hosts, asks
    /// with (step 8 U4): its run, engine, agent and `approval_timeout`, as
    /// of the last round; none for a session it does not host.
    // Equivalent: its one mutant (`None`) is this default body.
    #[cfg_attr(test, mutants::skip)]
    fn asker(&self, session: &str) -> Option<daemon_application::Asker> {
        let _ = session;
        None
    }
}

/// What a library read observes first (step 10a U2): the trees as last
/// observed (the kernel's `library-checkout`), each checkout's harp, and
/// git as the read's caller runs it.
#[derive(Clone)]
pub struct Observing {
    pub checkouts: library_checkout::LibraryCheckouts,
    pub harps: Arc<dyn library_api::CheckoutHarps>,
    pub observers: Arc<dyn library_api::Observers>,
    /// The atomic operations a write makes its change through (step 10b
    /// U8), whose staging of a tree is swept at its first observation.
    pub operations: Arc<dyn ctxloom_ports::Operations>,
    /// The daemon's clock, by which a kept trash slot's age is told and
    /// purged at its tree's observation (G12).
    pub clock: library_api::Clock,
    /// A tree's regenerable cache, which `clean` measures (step 10b U9).
    pub caches: Arc<dyn crate::Caches>,
}

/// A session's state now, from its liveness lock in its project.
pub trait SessionStates: Send + Sync {
    fn state(&self, project_dir: &str, harp: &str) -> State;

    /// Where the session's lock is, as its adapter forms it
    /// (`session_ports::lock_path`): what a refusal names.
    fn lock(&self, project_dir: &str, harp: &str) -> String;
}

/// Fresh project harps to try, in order (minted outside the core).
pub trait Harps: Send + Sync {
    fn fresh(&self) -> Vec<String>;
}

/// What the daemon says of itself, fixed at its start.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct About {
    pub pid: u32,
    /// The binary's full versionator stamp (M3).
    pub version: String,
    pub start_time: Timestamp,
    pub idle_limit_seconds: u64,
}

/// The API's ports, composed by the root.
pub struct Parts<E> {
    pub edge: Arc<E>,
    /// The session index the daemon's kernel keeps warm.
    pub index: SessionIndexes,
    /// The pending messages the daemon's kernel keeps warm (`show`'s, D30).
    pub pending: PendingMessages,
    /// The board the daemon's kernel keeps warm (its policies plan from
    /// it), told of a destroy as the index is.
    pub board: daemon_board::Boards,
    pub hosting: Arc<dyn Hosting>,
    pub states: Arc<dyn SessionStates>,
    pub harps: Arc<dyn Harps>,
    pub about: About,
}

/// An answer of a use case, boxed: what the API's edge awaits.
pub type Answer<'a, T> = Pin<Box<dyn Future<Output = Result<T, ApiError>> + Send + 'a>>;

/// The use cases as the API's edge calls them, whatever the core's edge
/// is: one method per RPC served.
pub trait Calls: Send + Sync {
    fn get_daemon(&self) -> Answer<'_, v1::Daemon>;
    fn stop_daemon(&self, request: v1::StopDaemonRequest) -> Answer<'_, v1::Daemon>;
    fn resolve_project(&self, request: v1::ResolveProjectRequest) -> Answer<'_, v1::Project>;
    fn get_project(&self, request: v1::GetProjectRequest) -> Answer<'_, v1::Project>;
    fn get_session(&self, request: v1::GetSessionRequest) -> Answer<'_, v1::Session>;
    fn list_sessions(
        &self,
        request: v1::ListSessionsRequest,
    ) -> Answer<'_, v1::ListSessionsResponse>;
    fn search_sessions(
        &self,
        request: v1::SearchSessionsRequest,
    ) -> Answer<'_, v1::ListSessionsResponse>;
    /// `DeleteSession`: an edge serves it only in a `purge`
    /// build (M5).
    #[cfg(feature = "purge")]
    fn delete_session(
        &self,
        request: v1::DeleteSessionRequest,
    ) -> Answer<'_, v1::DeleteSessionResponse>;
    /// `SweepSessions`: as `DeleteSession`, a `purge` build's.
    #[cfg(feature = "purge")]
    fn sweep_sessions(
        &self,
        request: v1::SweepSessionsRequest,
    ) -> Answer<'_, v1::SweepSessionsResponse>;
    /// `ImportSessions` (ONE WRITER): an edge that serves none fails it.
    fn import_sessions(
        &self,
        request: v1::ImportSessionsRequest,
    ) -> Answer<'_, v1::ImportSessionsResponse> {
        let _ = request;
        Box::pin(async { Err(not_served("ImportSessions")) })
    }
    /// `GetTranscript` (slice 3 step 5); an edge that serves none fails it.
    fn get_transcript(&self, request: v1::GetTranscriptRequest) -> Answer<'_, v1::Transcript> {
        let _ = request;
        Box::pin(async { Err(not_served("GetTranscript")) })
    }
    /// `CreateHandOff` (slice 4 U3); as `GetTranscript`.
    fn create_hand_off(&self, request: v1::CreateHandOffRequest) -> Answer<'_, v1::HandOff> {
        let _ = request;
        Box::pin(async { Err(not_served("CreateHandOff")) })
    }
    /// `DeleteHandOff` (slice 4 U3); as `GetTranscript`.
    fn delete_hand_off(&self, request: v1::DeleteHandOffRequest) -> Answer<'_, v1::HandOff> {
        let _ = request;
        Box::pin(async { Err(not_served("DeleteHandOff")) })
    }
    /// `WatchLibrary` (step 10c G7): the named trees' changes from now on,
    /// drained by the poll each time `wake` is called (the catch-up's
    /// signal, step 10c U13 rework); as `GetTranscript`.
    fn watch_library(
        &self,
        request: v1::WatchLibraryRequest,
        wake: crate::reactions::Wake,
    ) -> Answer<'_, crate::reactions::LibraryPoll> {
        let _ = (request, wake);
        Box::pin(async { Err(not_served("WatchLibrary")) })
    }
    /// `RedeliverContext` (step 10c U13); as `GetTranscript`.
    fn redeliver_context(
        &self,
        request: v1::RedeliverContextRequest,
    ) -> Answer<'_, v1::RedeliveredContext> {
        let _ = request;
        Box::pin(async { Err(not_served("RedeliverContext")) })
    }
    /// `DeliverHookContext` (10c, the lean hook RPC); as `GetTranscript`.
    fn deliver_hook_context(
        &self,
        request: v1::DeliverHookContextRequest,
    ) -> Answer<'_, v1::HookContext> {
        let _ = request;
        Box::pin(async { Err(not_served("DeliverHookContext")) })
    }
    /// `AcknowledgeHookDelivery` (message delivery, 2026-11-19); as
    /// `GetTranscript`.
    fn acknowledge_hook_delivery(
        &self,
        request: v1::AcknowledgeHookDeliveryRequest,
    ) -> Answer<'_, v1::HookDeliveryAcknowledged> {
        let _ = request;
        Box::pin(async { Err(not_served("AcknowledgeHookDelivery")) })
    }
    /// `AssembleContext` (slice 3 step 6); as `GetTranscript`.
    fn assemble_context(
        &self,
        request: v1::AssembleContextRequest,
    ) -> Answer<'_, v1::AssembledContext> {
        let _ = request;
        Box::pin(async { Err(not_served("AssembleContext")) })
    }
    /// `GetTrust` (step 8 U1); as `GetTranscript`.
    fn get_trust(&self, request: v1::GetTrustRequest) -> Answer<'_, v1::Trust> {
        let _ = request;
        Box::pin(async { Err(not_served("GetTrust")) })
    }
    /// `ListTrusts` (step 8 U1); as `GetTranscript`.
    fn list_trusts(&self, request: v1::ListTrustsRequest) -> Answer<'_, v1::ListTrustsResponse> {
        let _ = request;
        Box::pin(async { Err(not_served("ListTrusts")) })
    }
    /// `CreateApproval` (step 8 U4); as `GetTranscript`.
    fn create_approval(&self, request: v1::CreateApprovalRequest) -> Answer<'_, v1::Approval> {
        let _ = request;
        Box::pin(async { Err(not_served("CreateApproval")) })
    }
    /// `GetApproval` (step 8 U4); as `GetTranscript`.
    fn get_approval(&self, request: v1::GetApprovalRequest) -> Answer<'_, v1::Approval> {
        let _ = request;
        Box::pin(async { Err(not_served("GetApproval")) })
    }
    /// `ListApprovals` (step 8 U4); as `GetTranscript`.
    fn list_approvals(
        &self,
        request: v1::ListApprovalsRequest,
    ) -> Answer<'_, v1::ListApprovalsResponse> {
        let _ = request;
        Box::pin(async { Err(not_served("ListApprovals")) })
    }
    /// `WaitApproval` (step 8 U4); as `GetTranscript`.
    fn wait_approval(&self, request: v1::WaitApprovalRequest) -> Answer<'_, v1::Approval> {
        let _ = request;
        Box::pin(async { Err(not_served("WaitApproval")) })
    }
    /// `CreateRun` (U4); as `GetTranscript`.
    fn create_run(&self, request: v1::CreateRunRequest) -> Answer<'_, v1::Run> {
        let _ = request;
        Box::pin(async { Err(not_served("CreateRun")) })
    }
    /// `GetRun` (U4); as `GetTranscript`.
    fn get_run(&self, request: v1::GetRunRequest) -> Answer<'_, v1::Run> {
        let _ = request;
        Box::pin(async { Err(not_served("GetRun")) })
    }
    /// `WatchRun` (U4): the run's events, a stream the edge pumps from a
    /// blocking thread; as `GetTranscript`.
    fn watch_run(&self, request: v1::WatchRunRequest) -> Answer<'_, crate::RunStream> {
        let _ = request;
        Box::pin(async { Err(not_served("WatchRun")) })
    }
    /// `WatchSession` (the terminal wake): the session's signals, a stream
    /// the edge pumps from a blocking thread.
    fn watch_session(&self, request: v1::WatchSessionRequest) -> Answer<'_, crate::SessionStream> {
        let _ = request;
        Box::pin(async { Err(not_served("WatchSession")) })
    }
    /// `WatchTop` (`ctxloom top`, DRAFT); as `GetTranscript`.
    fn watch_top(&self, request: v1::WatchTopRequest) -> Answer<'_, crate::TopStream> {
        let _ = request;
        Box::pin(async { Err(not_served("WatchTop")) })
    }
    /// The daemon's standing warnings (OD-52's start-up, OD-54's
    /// hand-over), as they stand now: what its edge attaches to every
    /// answer it gives (owner ruling OD-55), so no command asks for them.
    /// An edge with none, none.
    fn warnings(&self) -> Vec<v1::Notice> {
        Vec::new()
    }

    /// Whether a request whose caller is `caller` is served (owner,
    /// 2026-11-18): the edge asks it of every request that carries a
    /// `Caller`, before its use case. The daemon admits only a client of its
    /// own version ([`crate::admit`]); an edge with no version of its own
    /// admits every one.
    fn admit(&self, caller: Option<&v1::Caller>) -> Result<(), ApiError> {
        let _ = caller;
        Ok(())
    }

    // The library's reads (step 10a U2, D12), from the one table; each
    // answers its message and the notices the read said. An edge that
    // serves one fails it.
    daemon_proto::library_reads!(library_read_calls);

    // The library's writes (step 10b U3, D12), from their one table.
    daemon_proto::library_writes!(library_write_calls);
}

/// [`Calls`]'s library writes, from `daemon_proto::library_writes!`.
macro_rules! library_write_calls {
    ($($method:ident ($rpc:ident) $request:ident => $response:ident),* $(,)?) => {
        $(
            #[doc = concat!("`", stringify!($rpc), "` (step 10b U3).")]
            fn $method(&self, request: v1::$request) -> Answer<'_, v1::$response> {
                let _ = request;
                Box::pin(async { Err(not_served(stringify!($rpc))) })
            }
        )*
    };
}
use library_write_calls;

/// [`Calls`]'s library reads, from `daemon_proto::library_reads!`.
macro_rules! library_read_calls {
    ($($method:ident ($rpc:ident) $request:ident . $target:ident => $response:ident),* $(,)?) => {
        $(
            #[doc = concat!("`", stringify!($rpc), "` (step 10a U2); as `GetTranscript`.")]
            fn $method(
                &self,
                request: v1::$request,
            ) -> Answer<'_, daemon_ports::Read<v1::$response>> {
                let _ = request;
                Box::pin(async { Err(not_served(stringify!($rpc))) })
            }
        )*
    };
}
use library_read_calls;

/// A deps write answered as another verb: a bug of the wiring.
pub(crate) fn misanswered(
    verb: &str,
    other: &(crate::deps_writes::DepsAnswer, String),
) -> ApiError {
    ApiError::Failed {
        detail: format!("{verb} answered {:?}", other.0),
    }
}

/// A call this edge does not serve, failed.
fn not_served(method: &str) -> ApiError {
    ApiError::Failed {
        detail: format!("{method} is not served here"),
    }
}

/// Every call but `GetDaemon`, `StopDaemon`, `GetConfig`, the project's names
/// (`ResolveProject`, `GetProject`) and the calls on work a client already
/// started (`WatchRun`, `DeleteHandOff`) is taken only while the daemon is
/// open ([`Api::open`]): a draining one refuses it. The names are served
/// while it drains (owner, 2026-11-15): a command that only names its
/// project (a config change, which must land while a drain runs) starts no
/// work. Nor does a watch of a run it already hosts (its client opens it
/// only once `CreateRun` answered, so a drain asked in between would
/// otherwise refuse the one-shot the period then stops), or the withdrawal
/// of a hand-off whose launch the drain refused (OD-62: NEW invocations
/// are refused). Every approvals call is served while it drains too (D6):
/// an ask comes only from a session it already hosts, and its answer is
/// that session's held work.
impl<E: Edge + 'static> Calls for Api<E> {
    fn warnings(&self) -> Vec<v1::Notice> {
        Api::warnings(self)
    }

    fn admit(&self, caller: Option<&v1::Caller>) -> Result<(), ApiError> {
        crate::admit(&self.parts.about.version, caller)
    }

    fn get_daemon(&self) -> Answer<'_, v1::Daemon> {
        Box::pin(Api::get_daemon(self))
    }

    fn stop_daemon(&self, request: v1::StopDaemonRequest) -> Answer<'_, v1::Daemon> {
        Box::pin(Api::stop_daemon(self, request))
    }

    fn resolve_project(&self, request: v1::ResolveProjectRequest) -> Answer<'_, v1::Project> {
        Box::pin(Api::resolve_project(self, request))
    }

    fn get_project(&self, request: v1::GetProjectRequest) -> Answer<'_, v1::Project> {
        Box::pin(Api::get_project(self, request))
    }

    fn get_session(&self, request: v1::GetSessionRequest) -> Answer<'_, v1::Session> {
        Box::pin(async move {
            self.open()?;
            Api::get_session(self, request).await
        })
    }

    fn watch_session(&self, request: v1::WatchSessionRequest) -> Answer<'_, crate::SessionStream> {
        Box::pin(async move {
            self.open()?;
            Api::watch_session(self, request).await
        })
    }

    fn watch_top(&self, request: v1::WatchTopRequest) -> Answer<'_, crate::TopStream> {
        Box::pin(async move {
            self.open()?;
            Api::watch_top(self, request).await
        })
    }

    fn list_sessions(
        &self,
        request: v1::ListSessionsRequest,
    ) -> Answer<'_, v1::ListSessionsResponse> {
        Box::pin(async move {
            self.open()?;
            Api::list_sessions(self, request).await
        })
    }

    fn search_sessions(
        &self,
        request: v1::SearchSessionsRequest,
    ) -> Answer<'_, v1::ListSessionsResponse> {
        Box::pin(async move {
            self.open()?;
            Api::search_sessions(self, request).await
        })
    }

    #[cfg(feature = "purge")]
    fn delete_session(
        &self,
        request: v1::DeleteSessionRequest,
    ) -> Answer<'_, v1::DeleteSessionResponse> {
        Box::pin(async move {
            self.open()?;
            Api::delete_session(self, request).await
        })
    }

    fn import_sessions(
        &self,
        request: v1::ImportSessionsRequest,
    ) -> Answer<'_, v1::ImportSessionsResponse> {
        Box::pin(async move {
            self.open()?;
            Api::import_sessions(self, request).await
        })
    }

    #[cfg(feature = "purge")]
    fn sweep_sessions(
        &self,
        request: v1::SweepSessionsRequest,
    ) -> Answer<'_, v1::SweepSessionsResponse> {
        Box::pin(async move {
            self.open()?;
            Api::sweep_sessions(self, request).await
        })
    }

    fn get_transcript(&self, request: v1::GetTranscriptRequest) -> Answer<'_, v1::Transcript> {
        Box::pin(async move {
            self.open()?;
            Api::get_transcript(self, request).await
        })
    }

    fn assemble_context(
        &self,
        request: v1::AssembleContextRequest,
    ) -> Answer<'_, v1::AssembledContext> {
        Box::pin(async move {
            self.open()?;
            Api::assemble_context(self, request).await
        })
    }

    fn redeliver_context(
        &self,
        request: v1::RedeliverContextRequest,
    ) -> Answer<'_, v1::RedeliveredContext> {
        Box::pin(async move { Api::redeliver_context(self, request).await })
    }

    fn deliver_hook_context(
        &self,
        request: v1::DeliverHookContextRequest,
    ) -> Answer<'_, v1::HookContext> {
        Box::pin(async move { Api::deliver_hook_context(self, request).await })
    }

    fn acknowledge_hook_delivery(
        &self,
        request: v1::AcknowledgeHookDeliveryRequest,
    ) -> Answer<'_, v1::HookDeliveryAcknowledged> {
        Box::pin(async move { Api::acknowledge_hook_delivery(self, request).await })
    }

    fn watch_library(
        &self,
        request: v1::WatchLibraryRequest,
        wake: crate::reactions::Wake,
    ) -> Answer<'_, crate::reactions::LibraryPoll> {
        Box::pin(async move {
            self.open()?;
            Api::watch_library(self, &request, wake)
        })
    }

    fn create_run(&self, request: v1::CreateRunRequest) -> Answer<'_, v1::Run> {
        Box::pin(async move {
            self.creating()?;
            Api::create_run(self, request).await
        })
    }

    fn get_trust(&self, request: v1::GetTrustRequest) -> Answer<'_, v1::Trust> {
        Box::pin(async move {
            self.open()?;
            Api::get_trust(self, request).await
        })
    }

    fn list_trusts(&self, request: v1::ListTrustsRequest) -> Answer<'_, v1::ListTrustsResponse> {
        Box::pin(async move {
            self.open()?;
            Api::list_trusts(self, request).await
        })
    }

    fn get_run(&self, request: v1::GetRunRequest) -> Answer<'_, v1::Run> {
        Box::pin(async move {
            self.open()?;
            Api::get_run(self, request).await
        })
    }

    fn watch_run(&self, request: v1::WatchRunRequest) -> Answer<'_, crate::RunStream> {
        Box::pin(Api::watch_run(self, request))
    }

    fn create_hand_off(&self, request: v1::CreateHandOffRequest) -> Answer<'_, v1::HandOff> {
        Box::pin(async move {
            self.creating()?;
            Api::create_hand_off(self, request).await
        })
    }

    fn delete_hand_off(&self, request: v1::DeleteHandOffRequest) -> Answer<'_, v1::HandOff> {
        Box::pin(Api::delete_hand_off(self, request))
    }

    fn create_approval(&self, request: v1::CreateApprovalRequest) -> Answer<'_, v1::Approval> {
        Box::pin(Api::create_approval(self, request))
    }

    fn get_approval(&self, request: v1::GetApprovalRequest) -> Answer<'_, v1::Approval> {
        Box::pin(Api::get_approval(self, request))
    }

    fn list_approvals(
        &self,
        request: v1::ListApprovalsRequest,
    ) -> Answer<'_, v1::ListApprovalsResponse> {
        Box::pin(Api::list_approvals(self, request))
    }

    fn wait_approval(&self, request: v1::WaitApprovalRequest) -> Answer<'_, v1::Approval> {
        Box::pin(Api::wait_approval(self, request))
    }

    // The library's reads served (step 10a U2, D12), from library-api's
    // table: each observed first, then answered by its use case.
    library_api::served!(served_library_reads);
    // The deps reads served (step 10a U2), as the library's.
    library_api::deps_served!(served_deps_reads);
    // The library's writes served (step 10b U3, D12), from library-api's
    // table: each observed, planned, intended, made and recorded.
    library_api::served_writes!(served_library_writes);

    /// `Clean` (step 10b U9): the cache's present trees into the trash.
    fn clean(&self, request: v1::CleanRequest) -> Answer<'_, v1::CleanResponse> {
        Box::pin(async move { self.serve_clean(request).await })
    }

    /// `Materialize` (step 10c U10): other tools' files, one sequence.
    fn materialize(&self, request: v1::MaterializeRequest) -> Answer<'_, v1::MaterializeResponse> {
        Box::pin(async move {
            let ask = crate::foreign::ForeignAsk::Materialize(&request);
            match self
                .foreign_write(&request, "materialize", ask, request.dry_run)
                .await?
            {
                crate::foreign::ForeignAnswer::Materialized(answer) => Ok(answer),
                other => Err(ApiError::Failed {
                    detail: format!("materialize answered {other:?}"),
                }),
            }
        })
    }

    /// `UninstallManage` (step 10c U10): every engine's claims given back.
    fn uninstall_manage(
        &self,
        request: v1::UninstallManageRequest,
    ) -> Answer<'_, v1::UninstallManageResponse> {
        Box::pin(async move {
            let ask = crate::foreign::ForeignAsk::Uninstall;
            match self
                .foreign_write(&request, "manage uninstall", ask, false)
                .await?
            {
                crate::foreign::ForeignAnswer::Uninstalled(answer) => Ok(answer),
                other => Err(ApiError::Failed {
                    detail: format!("manage uninstall answered {other:?}"),
                }),
            }
        })
    }

    /// `RunDoctor` (step 10c U10 serves its `--fix`; U5 the checks).
    fn run_doctor(&self, request: v1::RunDoctorRequest) -> Answer<'_, v1::RunDoctorResponse> {
        Box::pin(async move {
            if !request.fix {
                let ask = crate::setup_reads::SetupRead::Doctor { deps: request.deps };
                return match self.setup_read(&request, ask).await? {
                    crate::setup_reads::SetupAnswer::Doctor(answer) => Ok(answer),
                    other => Err(ApiError::Failed {
                        detail: format!("doctor answered {other:?}"),
                    }),
                };
            }
            let ask = crate::foreign::ForeignAsk::DoctorFix;
            match self
                .foreign_write(&request, "doctor --fix", ask, false)
                .await?
            {
                crate::foreign::ForeignAnswer::Fixed(answer) => Ok(answer),
                other => Err(ApiError::Failed {
                    detail: format!("doctor --fix answered {other:?}"),
                }),
            }
        })
    }

    /// `GetAuthStatus` (step 10c U5): read where the caller stands.
    fn get_auth_status(
        &self,
        request: v1::GetAuthStatusRequest,
    ) -> Answer<'_, v1::GetAuthStatusResponse> {
        Box::pin(async move {
            let ask = crate::setup_reads::SetupRead::AuthStatus;
            match self.setup_read(&request, ask).await? {
                crate::setup_reads::SetupAnswer::AuthStatus(answer) => Ok(answer),
                other => Err(ApiError::Failed {
                    detail: format!("auth status answered {other:?}"),
                }),
            }
        })
    }

    /// `CheckManage` (step 10c U5): read where the caller stands.
    fn check_manage(&self, request: v1::CheckManageRequest) -> Answer<'_, v1::CheckManageResponse> {
        Box::pin(async move {
            let ask = crate::setup_reads::SetupRead::CheckManage;
            match self.setup_read(&request, ask).await? {
                crate::setup_reads::SetupAnswer::CheckManage(answer) => Ok(answer),
                other => Err(ApiError::Failed {
                    detail: format!("manage check answered {other:?}"),
                }),
            }
        })
    }

    /// `GetInitPrompt` (step 10c U6): read where the caller stands.
    fn get_init_prompt(
        &self,
        request: v1::GetInitPromptRequest,
    ) -> Answer<'_, v1::GetInitPromptResponse> {
        Box::pin(async move {
            let ask = crate::setup_reads::SetupRead::InitPrompt;
            match self.setup_read(&request, ask).await? {
                crate::setup_reads::SetupAnswer::InitPrompt(answer) => Ok(answer),
                other => Err(ApiError::Failed {
                    detail: format!("init prompt answered {other:?}"),
                }),
            }
        })
    }

    /// `InstallGitignore` (step 10c U5): `.ctxloom/.gitignore`, one library
    /// write of the caller's project.
    fn install_gitignore(
        &self,
        request: v1::InstallGitignoreRequest,
    ) -> Answer<'_, v1::InstallGitignoreResponse> {
        Box::pin(async move {
            let wrote = self
                .library_write(&request, "manage gitignore install", |library, _, _| {
                    library.gitignore_install()
                })
                .await?;
            Ok(crate::setup_reads::gitignore_installed(&wrote))
        })
    }

    /// `InstallManage` (step 10c U6): `manage install`, init's creation.
    fn install_manage(
        &self,
        request: v1::InstallManageRequest,
    ) -> Answer<'_, v1::InstallManageResponse> {
        Box::pin(Api::install_manage(self, request))
    }

    /// `InitProject` (step 10c U6): a project's `.ctxloom` created whole.
    fn init_project(&self, request: v1::InitProjectRequest) -> Answer<'_, v1::InitProjectResponse> {
        Box::pin(Api::init_project(self, request))
    }

    /// `CreateRemote` (step 10c U4): `remotes.yaml`, one library write.
    fn create_remote(
        &self,
        request: v1::CreateRemoteRequest,
    ) -> Answer<'_, v1::CreateRemoteResponse> {
        Box::pin(async move { self.create_remote_write(&request).await })
    }

    /// `UpdateRemote` (step 10c U4): the fields its mask names (all it
    /// carries, with none).
    fn update_remote(
        &self,
        request: v1::UpdateRemoteRequest,
    ) -> Answer<'_, v1::UpdateRemoteResponse> {
        Box::pin(async move {
            let remote = request.remote.clone().unwrap_or_default();
            // A field the mask names is written as given, empty too (a
            // forge cleared); with no mask, each field the remote carries.
            let named = |field: &str, value: &str| match request.update_mask.as_ref() {
                Some(mask) => mask
                    .paths
                    .iter()
                    .any(|path| path == field)
                    .then(|| value.to_string()),
                None => (!value.is_empty()).then(|| value.to_string()),
            };
            let current = remote
                .name
                .rsplit_once("/remotes/")
                .map_or(remote.name.as_str(), |(_, id)| id)
                .to_string();
            let renamed = named("name", &request.new_remote_id);
            let ask = crate::deps_writes::DepsAsk::EditRemote {
                remote: current,
                name: renamed,
                url: named("url", &remote.url),
                forge: named("forge", &remote.forge),
            };
            match self.deps_write(&request, "remote edit", &ask).await? {
                (crate::deps_writes::DepsAnswer::Edited(answer), etag) => {
                    Ok(v1::UpdateRemoteResponse { etag, ..answer })
                }
                other => Err(misanswered("remote edit", &other)),
            }
        })
    }

    /// `DeleteRemote` (step 10c U4): previewed without `--yes`.
    fn delete_remote(
        &self,
        request: v1::DeleteRemoteRequest,
    ) -> Answer<'_, v1::DeleteRemoteResponse> {
        Box::pin(async move {
            let name = request
                .name
                .rsplit_once("/remotes/")
                .map_or(request.name.as_str(), |(_, id)| id)
                .to_string();
            let ask = crate::deps_writes::DepsAsk::RemoveRemote {
                name,
                apply: !request.validate_only,
            };
            match self.deps_write(&request, "remote remove", &ask).await? {
                (crate::deps_writes::DepsAnswer::Removed(answer), etag) => {
                    Ok(v1::DeleteRemoteResponse { etag, ..answer })
                }
                other => Err(misanswered("remote remove", &other)),
            }
        })
    }

    /// `SetDefaultRemote` (step 10c U4): none named, the default cleared.
    fn set_default_remote(
        &self,
        request: v1::SetDefaultRemoteRequest,
    ) -> Answer<'_, v1::SetDefaultRemoteResponse> {
        Box::pin(async move {
            let name = request
                .remote
                .rsplit_once("/remotes/")
                .map_or(request.remote.as_str(), |(_, id)| id)
                .to_string();
            let ask = crate::deps_writes::DepsAsk::DefaultRemote {
                name: (!name.is_empty()).then_some(name),
            };
            match self.deps_write(&request, "remote default", &ask).await? {
                (crate::deps_writes::DepsAnswer::Defaulted(answer), etag) => {
                    Ok(v1::SetDefaultRemoteResponse { etag, ..answer })
                }
                other => Err(misanswered("remote default", &other)),
            }
        })
    }

    /// `PullDependencies` (step 10c U4): one sequence of whole trees, then
    /// the lock; its refresh previewed without `--yes` (`validate_only`).
    fn pull_dependencies(
        &self,
        request: v1::PullDependenciesRequest,
    ) -> Answer<'_, v1::PullDependenciesResponse> {
        Box::pin(async move {
            let ask = crate::deps_writes::DepsAsk::Pull {
                force: request.force,
                lock: request.lock.unwrap_or(true),
                yes: !request.validate_only,
            };
            match self.deps_write(&request, "deps pull", &ask).await? {
                (crate::deps_writes::DepsAnswer::Pulled(answer), _) => Ok(answer),
                other => Err(misanswered("deps pull", &other)),
            }
        })
    }

    /// `UpgradeDependencies` (step 10c U4): as a pull; a preview without
    /// `--yes`.
    fn upgrade_dependencies(
        &self,
        request: v1::UpgradeDependenciesRequest,
    ) -> Answer<'_, v1::UpgradeDependenciesResponse> {
        Box::pin(async move {
            let ask = crate::deps_writes::DepsAsk::Upgrade {
                reference: (!request.reference.is_empty()).then(|| request.reference.clone()),
                apply: !request.validate_only,
            };
            match self.deps_write(&request, "deps upgrade", &ask).await? {
                (crate::deps_writes::DepsAnswer::Upgraded(answer), _) => Ok(answer),
                other => Err(misanswered("deps upgrade", &other)),
            }
        })
    }

    /// `DistillFragment` (step 10b, owner 2026-11-18): the model asked as a
    /// run the daemon hosts, its distilled form written once it ends.
    fn distill_fragment(
        &self,
        request: v1::DistillFragmentRequest,
    ) -> Answer<'_, v1::DistillFragmentResponse> {
        Box::pin(async move { self.distill_fragment(request).await })
    }

    /// `DistillCommand`, as `DistillFragment`.
    fn distill_command(
        &self,
        request: v1::DistillCommandRequest,
    ) -> Answer<'_, v1::DistillCommandResponse> {
        Box::pin(async move { self.distill_command(request).await })
    }
}

/// [`Api`]'s served library writes, from `library_api::served_writes!`:
/// each the daemon's `library_write` of its use case, answered by its
/// response.
macro_rules! served_library_writes {
    ($($method:ident ($rpc:ident) $request:ident => $response:ident, $verb:literal;)*) => {
        $(
            fn $method(&self, request: v1::$request) -> Answer<'_, v1::$response> {
                Box::pin(async move {
                    let wrote = self
                        .library_write(&request, $verb, |library, flags, apply| {
                            library_api::served_writes::$method::plan(library, flags, &request, apply)
                        })
                        .await?;
                    Ok(library_api::served_writes::$method::respond(&request, wrote))
                })
            }
        )*
    };
}
use served_library_writes;

/// [`Api`]'s served deps reads, from `library_api::deps_served!`: each
/// taken only while the daemon is open, observed first, then answered by
/// `library_api::deps::$method`.
macro_rules! served_deps_reads {
    ($($method:ident ($rpc:ident) $request:ident . $target:ident => $response:ident),* $(,)?) => {
        $(
            fn $method(
                &self,
                request: v1::$request,
            ) -> Answer<'_, daemon_ports::Read<v1::$response>> {
                Box::pin(async move {
                    self.open()?;
                    self.deps_read(&request, |deps, parent| {
                        library_api::deps::$method(deps, parent, &request)
                    })
                    .await
                })
            }
        )*
    };
}
use served_deps_reads;

/// [`Api`]'s served library reads, from `library_api::served!`: each taken
/// only while the daemon is open (`GetConfig` while it drains too),
/// observed first, then answered by
/// `library_api::reads::$method`.
macro_rules! served_library_reads {
    ($($method:ident ($rpc:ident) $request:ident . $target:ident => $response:ident),* $(,)?) => {
        $(
            fn $method(
                &self,
                request: v1::$request,
            ) -> Answer<'_, daemon_ports::Read<v1::$response>> {
                Box::pin(async move {
                    // `GetConfig` is served while the daemon drains (step
                    // 10a U2): a drain's own timeout is changed and read
                    // back while it runs (shutdown.feature), and the read
                    // starts no work.
                    if stringify!($rpc) != "GetConfig" {
                        self.open()?;
                    }
                    self.library_read(&request, |library, flags, parent| {
                        library_api::reads::$method(library, flags, parent, &request)
                    })
                    .await
                })
            }
        )*
    };
}
use served_library_reads;

/// The use cases of the daemon's API.
pub struct Api<E> {
    pub(crate) parts: Parts<E>,
    /// The projects' places a destroy reaches (`DeleteSession`,
    /// `SweepSessions`) and the daemon's one store, as it purges it: set by
    /// [`Api::destroying`], which the daemon calls in a `purge` build alone.
    #[cfg(feature = "purge")]
    pub(crate) destroyers: Option<(Arc<dyn crate::Places>, Arc<dyn crate::Purges>)>,
    /// The one-shots the daemon hosts (U4); none, `CreateRun` fails.
    pub(crate) one_shots: Option<Arc<daemon_application::OneShots>>,
    /// Where a child's hand-off is planned and kept; none, `CreateHandOff`
    /// and `DeleteHandOff` fail.
    pub(crate) hand_offs: Option<Arc<dyn crate::HandOffs>>,
    /// What imports Go's sessions (ONE WRITER); none, `ImportSessions` fails.
    pub(crate) imports: Option<Arc<dyn crate::Imports>>,
    /// What refuses a project with a store of its own from before the one
    /// store; none, no project is refused.
    pub(crate) legacy_stores: Option<Arc<dyn crate::LegacyStores>>,
    /// The interactive sessions the daemon prepares (U5); none, a TERMINAL
    /// `CreateRun` fails.
    pub(crate) terminals: Option<Arc<daemon_application::Terminals>>,
    /// Where a trust is read (step 8 U1); none, `GetTrust` and `ListTrusts`
    /// fail.
    pub(crate) trusting: Option<crate::Trusting>,
    /// Where an ask is recorded and read (step 8 U4); none, the approvals
    /// calls fail.
    pub(crate) approving: Option<crate::Approving>,
    /// Each session's timeline (`ctxloom top`'s log); none, a watch carries
    /// no log.
    pub(crate) timelines: Option<session_timeline::SessionTimelines>,
    /// Where a library read is answered from (step 10a U2); none, every
    /// library read fails.
    pub(crate) libraries: Option<Arc<dyn crate::Libraries>>,
    /// What a library read observes first (step 10a U2), and git as its
    /// caller runs it; none, a read observes nothing.
    pub(crate) observing: Option<(
        library_api::Observations<E>,
        Arc<dyn library_api::Observers>,
    )>,
    /// The test-support kill points of a library write (step 10b U3);
    /// none, a write is never killed.
    pub(crate) write_dies_at: Option<crate::library_writes::DiesAt>,
    /// A tree's regenerable cache (step 10b U9); none, `Clean` fails.
    pub(crate) caches: Option<Arc<dyn crate::Caches>>,
    /// This API, once it is shared ([`Api::shared`]): what a reaction to a
    /// run it hosts (a distill's write, step 10b) makes its write through,
    /// after the call that started the run has answered. Unset, a distill
    /// that asks a model fails.
    pub(crate) me: std::sync::OnceLock<std::sync::Weak<Api<E>>>,
    /// The platform's name (`std::env::consts::OS`'s), which a removal's
    /// evidence is judged by (D18); empty is a unix.
    #[cfg(feature = "purge")]
    pub(crate) os: String,
    /// The engines a live session's re-assembled context is handed through
    /// (step 10c U13); none, `RedeliverContext` answers nothing.
    pub(crate) deliveries: Option<Arc<dyn crate::reactions::Deliveries>>,
    /// The setup use cases that write other tools' files (step 10c U10);
    /// none, `Materialize`, `UninstallManage` and `RunDoctor` fail.
    pub(crate) foreign: Option<Arc<dyn crate::foreign::ForeignPlans>>,
    /// The deps use cases that write the remotes, the lock and the cache
    /// (step 10c U4); none, the remotes' writes, `PullDependencies` and
    /// `UpgradeDependencies` fail.
    pub(crate) deps: Option<Arc<dyn crate::deps_writes::DepsPlans>>,
    /// The setup use cases that read (step 10c U5); none, `RunDoctor`
    /// without `--fix`, `GetAuthStatus`, `CheckManage` and `GetInitPrompt`
    /// fail.
    pub(crate) setup_reads: Option<Arc<dyn crate::setup_reads::SetupReads>>,
    /// The sessions watched for the terminal wake, shared with the daemon's
    /// loop; none, `WatchSession` fails and no session is wakeable.
    pub(crate) watches: Option<Arc<daemon_application::SessionWatches>>,
}

impl<E: Edge> Api<E> {
    pub fn new(parts: Parts<E>) -> Api<E> {
        Api {
            parts,
            #[cfg(feature = "purge")]
            destroyers: None,
            one_shots: None,
            hand_offs: None,
            imports: None,
            legacy_stores: None,
            terminals: None,
            trusting: None,
            approving: None,
            timelines: None,
            libraries: None,
            observing: None,
            write_dies_at: None,
            caches: None,
            watches: None,
            me: std::sync::OnceLock::new(),
            #[cfg(feature = "purge")]
            os: String::new(),
            deliveries: None,
            foreign: None,
            setup_reads: None,
            deps: None,
        }
    }

    /// The API, the remotes' and the dependencies' writes planned by
    /// `plans` (step 10c U4).
    #[must_use]
    pub fn writing_deps(self, plans: Arc<dyn crate::deps_writes::DepsPlans>) -> Api<E> {
        Api {
            deps: Some(plans),
            ..self
        }
    }

    /// The API answering the setup reads through `reads` (step 10c U5).
    #[must_use]
    pub fn reading_setup(self, reads: Arc<dyn crate::setup_reads::SetupReads>) -> Api<E> {
        Api {
            setup_reads: Some(reads),
            ..self
        }
    }

    /// The API, other tools' files planned by `plans` (step 10c U10).
    #[must_use]
    pub fn writing_foreign(self, plans: Arc<dyn crate::foreign::ForeignPlans>) -> Api<E> {
        Api {
            foreign: Some(plans),
            ..self
        }
    }

    /// The API destroying through `places` (each session's project) and
    /// `purges` (the one store): what `DeleteSession` and `SweepSessions`
    /// run over.
    #[cfg(feature = "purge")]
    #[must_use]
    pub fn destroying(
        self,
        places: Arc<dyn crate::Places>,
        purges: Arc<dyn crate::Purges>,
    ) -> Api<E> {
        Api {
            destroyers: Some((places, purges)),
            ..self
        }
    }

    /// The API on the platform `os` (`std::env::consts::OS`'s name; the
    /// wiring's `os_name`): on `windows` a held session lock blocks its
    /// removal (D18).
    #[cfg(feature = "purge")]
    #[must_use]
    pub fn on_os(self, os: &str) -> Api<E> {
        Api {
            os: os.to_string(),
            ..self
        }
    }

    /// The API, a live session's re-assembled context handed through
    /// `deliveries`' engines (step 10c U13).
    #[must_use]
    pub fn delivering(self, deliveries: Arc<dyn crate::reactions::Deliveries>) -> Api<E> {
        Api {
            deliveries: Some(deliveries),
            ..self
        }
    }

    /// The API, each library write telling `dies_at` the points it reaches
    /// (`intended`, `staged`, `trashed`, `renamed`, `done`; test-support's
    /// kill switch).
    #[must_use]
    pub fn dying_at_writes(self, dies_at: crate::library_writes::DiesAt) -> Api<E> {
        Api {
            write_dies_at: Some(dies_at),
            ..self
        }
    }

    /// The API, each library read observing its trees first through
    /// `observing` (step 10a U2), recorded through its own edge.
    #[must_use]
    pub fn observing(self, observing: Observing) -> Api<E> {
        let observations = library_api::Observations::new(
            Arc::clone(&self.parts.edge),
            observing.checkouts,
            observing.harps,
        )
        .sweeping(observing.operations)
        .purging(observing.clock);
        Api {
            observing: Some((observations, observing.observers)),
            caches: Some(observing.caches),
            ..self
        }
    }

    /// A library read's observations (step 10a U2): the project's checkout
    /// at `root` (none: a read outside a project), then the home's
    /// library, as the notices they say, in any directory (the library is
    /// git-free, owner 2026-11-18); a scan or a record failing fails it.
    pub(crate) async fn observed(
        &self,
        project: Option<(ProjectName, &str)>,
    ) -> Result<Vec<v1::Notice>, ApiError> {
        let Some((observations, observers)) = &self.observing else {
            return Ok(Vec::new());
        };
        let said = observations
            .observe_read(observers.as_ref(), project)
            .await
            .map_err(|library_api::ReadError::Failed(failure)| {
                ApiError::diagnosed(failure.as_ref())
            })?;
        Ok(said
            .iter()
            .map(|notice| v1::Notice {
                code: coded_diagnostic::Diagnostic::code(notice).to_string(),
                args: run_application::client::flat_args(notice),
                text: notice.to_string(),
            })
            .collect())
    }

    /// The API, its watches' logs read from `timelines`.
    #[must_use]
    pub fn timelining(self, timelines: session_timeline::SessionTimelines) -> Api<E> {
        Api {
            timelines: Some(timelines),
            ..self
        }
    }

    /// The API, its asks recorded and read through `approving` (step 8 U4).
    #[must_use]
    pub fn approving(self, approving: crate::Approving) -> Api<E> {
        Api {
            approving: Some(approving),
            ..self
        }
    }

    /// The API, its trusts read through `trusting` (step 8 U1).
    #[must_use]
    pub fn trusting(self, trusting: crate::Trusting) -> Api<E> {
        Api {
            trusting: Some(trusting),
            ..self
        }
    }

    /// The API, its interactive sessions prepared by `terminals` (U5).
    #[must_use]
    pub fn preparing_terminals(self, terminals: Arc<daemon_application::Terminals>) -> Api<E> {
        Api {
            terminals: Some(terminals),
            ..self
        }
    }

    /// The API, its one-shots hosted by `one_shots` (U4), the same the
    /// daemon's launcher stops.
    #[must_use]
    /// This API, shared: as the edge serves it, and as what follows from
    /// a run it hosts (a distill's write) reaches it once its call is over.
    pub fn shared(self) -> Arc<Api<E>> {
        let api = Arc::new(self);
        let _ = api.me.set(Arc::downgrade(&api));
        api
    }

    pub fn hosting_one_shots(self, one_shots: Arc<daemon_application::OneShots>) -> Api<E> {
        Api {
            one_shots: Some(one_shots),
            ..self
        }
    }

    /// The API, its children's hand-offs planned and kept by `hand_offs`.
    #[must_use]
    pub fn handing_off(self, hand_offs: Arc<dyn crate::HandOffs>) -> Api<E> {
        Api {
            hand_offs: Some(hand_offs),
            ..self
        }
    }

    /// The API, its library reads answered through `libraries` (step 10a
    /// U2).
    #[must_use]
    pub fn reading(self, libraries: Arc<dyn crate::Libraries>) -> Api<E> {
        Api {
            libraries: Some(libraries),
            ..self
        }
    }

    /// `GetTranscript` (slice 3 step 5): a session's D27 lines, from the
    /// warm index, as its hooks have delivered them so far; one with none
    /// is refused `TranscriptEmpty`.
    pub async fn get_transcript(
        &self,
        request: v1::GetTranscriptRequest,
    ) -> Result<v1::Transcript, ApiError> {
        let session = request
            .name
            .strip_suffix(TRANSCRIPT)
            .unwrap_or(request.name.as_str());
        let name =
            SessionName::parse(session).map_err(|rejection| invalid(&request.name, &rejection))?;
        self.parts.hosting.fresh().await;
        let index = self.parts.index.index();
        let record = index.find_by_id(&name.uid()).ok_or_else(|| {
            ApiError::from(ApiRefusal::of(&v1::SessionNotFound {
                name: session.to_string(),
            }))
        })?;
        let text = index.transcript(&record.id).ok_or_else(|| {
            ApiError::from(ApiRefusal::of(&v1::TranscriptEmpty {
                name: session.to_string(),
            }))
        })?;
        Ok(v1::Transcript {
            name: format!("{name}{TRANSCRIPT}"),
            entries: text,
        })
    }

    /// `AssembleContext` (slice 3 step 6): the context of the project
    /// `request.name` names, assembled at its REGISTERED root in the
    /// caller's environment.
    pub async fn assemble_context(
        &self,
        request: v1::AssembleContextRequest,
    ) -> Result<v1::AssembledContext, ApiError> {
        // A refresh (step 10c U13 rework): what was seen of the trees is
        // forgotten first, so the read's observation hashes every file.
        if request.refresh {
            if let Some((observations, observers)) = self.observing.as_ref() {
                observations.forget(observers.as_ref());
            }
        }
        // A library read (step 10a U2, E1): at the caller's directory, in
        // its environment, observed first; what it said rides in the
        // message (the MCP tool's answer is the message alone).
        let read = self
            .library_read(&request, |library, flags, parent| {
                library_api::reads::assemble_context(library, flags, parent, &request)
            })
            .await?;
        let mut assembled = read.answer;
        assembled.notices.extend(read.notices);
        Ok(assembled)
    }

    /// The root project `name` is registered at and its id, from the warm
    /// index once it is fresh; a project nobody registered is refused
    /// `ProjectNotFound`.
    pub(crate) async fn registered_project(
        &self,
        name: &ProjectName,
    ) -> Result<(String, String), ApiError> {
        self.parts.hosting.fresh().await;
        let row = self.parts.index.index().project_named_by(name.harp());
        let row = row.filter(|row| !row.root.is_empty());
        row.map(|row| (row.root, row.project_id)).ok_or_else(|| {
            ApiError::from(ApiRefusal::of(&v1::ProjectNotFound {
                name: name.to_string(),
            }))
        })
    }

    /// `GetDaemon`: the daemon, what it hosts and its idle time. Never its
    /// bearer.
    pub async fn get_daemon(&self) -> Result<v1::Daemon, ApiError> {
        Ok(self.daemon())
    }

    fn daemon(&self) -> v1::Daemon {
        let about = &self.parts.about;
        v1::Daemon {
            name: DAEMON.to_string(),
            pid: about.pid,
            endpoint: self.parts.hosting.endpoint(),
            start_time: Some(about.start_time.into()),
            version: about.version.clone(),
            runs: self.parts.hosting.hosted(),
            clients: 0,
            idle_seconds: self.parts.hosting.idle_seconds(),
            idle_limit_seconds: about.idle_limit_seconds,
            warnings: self.warnings(),
            draining: self.parts.hosting.draining(),
            rounds: self.parts.hosting.rounds(),
            health: v1::daemon::Health::Unspecified as i32,
        }
    }

    /// The daemon's standing warnings, as they stand now: its start-up
    /// incomplete (OD-52), then its hand-over (OD-54). `GetDaemon` answers
    /// them as `Daemon.warnings`, and the edge attaches them to every
    /// answer (OD-55, [`Calls::warnings`]).
    pub fn warnings(&self) -> Vec<v1::Notice> {
        self.parts
            .hosting
            .start_up_failed()
            .map(|why| crate::StartUpIncomplete { why }.notice())
            .into_iter()
            .chain(self.handing_over())
            .collect()
    }

    /// The drain's warning (OD-54), while it drains and hosts anything:
    /// what it hands over, and within how long a busy child.
    fn handing_over(&self) -> Option<v1::Notice> {
        if !self.parts.hosting.draining() {
            return None;
        }
        let harps: Vec<String> = self
            .parts
            .hosting
            .hosted()
            .into_iter()
            .map(|run| run.harp)
            .collect();
        if harps.is_empty() {
            return None;
        }
        Some(
            crate::HandingOver {
                daemon: self.parts.about.version.clone(),
                harps,
                seconds: self.parts.hosting.hand_over_seconds(),
            }
            .notice(),
        )
    }

    /// Whether the daemon takes a new call: not once it was asked to drain
    /// (OD-54, or a SIGTERM), when every call but `GetDaemon`, `StopDaemon`,
    /// the project's names and the calls on work already started
    /// (`WatchRun`, `DeleteHandOff`) is refused
    /// `DaemonDraining`, naming its stamp and what it still hosts.
    pub(crate) fn open(&self) -> Result<(), ApiError> {
        if !self.parts.hosting.draining() {
            return Ok(());
        }
        Err(ApiRefusal::of(&v1::DaemonDraining {
            version: self.parts.about.version.clone(),
            harps: self
                .parts
                .hosting
                .hosted()
                .into_iter()
                .map(|run| run.harp)
                .collect(),
        })
        .into())
    }

    /// Whether the daemon takes a call that would create files under its
    /// home (`CreateRun`, `CreateHandOff`: hand-offs and their marks, run
    /// locks, a child's or one-shot's session directory and checkout): as
    /// [`open`](Self::open), and not once its home is gone, when it is
    /// refused `DaemonHomeGone` before anything is planned or written (owner,
    /// 2026-11-17); the loop exits on its next round on the same look. A
    /// launch the loop makes itself (a `LaunchChild`'s) is looked at by the
    /// loop's own round first (`serve::why_it_exits`). The accepted
    /// residual: a call already past this look while the home is removed
    /// can still make a file there.
    pub(crate) fn creating(&self) -> Result<(), ApiError> {
        self.open()?;
        match self.parts.hosting.home_gone() {
            None => Ok(()),
            Some(home) => Err(ApiRefusal::of(&v1::DaemonHomeGone { home }).into()),
        }
    }

    /// `StopDaemon`: refused `DaemonHosting` while it hosts live runs,
    /// unless `even_if_live`, which ends them first (S12), or `drain`
    /// (OD-54), which hands them over to the next daemon within the asked
    /// `drain_timeout_seconds` (else the configured one), never refused for
    /// them. Answers the daemon as it was
    /// asked to stop.
    pub async fn stop_daemon(
        &self,
        request: v1::StopDaemonRequest,
    ) -> Result<v1::Daemon, ApiError> {
        if request.drain && !request.even_if_live {
            let within = request
                .drain_timeout_seconds
                .map(std::time::Duration::from_secs);
            self.parts.hosting.drain(within);
            return Ok(self.daemon());
        }
        let daemon = self.daemon();
        if !daemon.runs.is_empty() && !request.even_if_live {
            return Err(ApiRefusal::of(&v1::DaemonHosting {
                runs: daemon.runs.len() as u64,
                harps: daemon.runs.iter().map(|run| run.harp.clone()).collect(),
            })
            .into());
        }
        self.parts.hosting.stop(request.even_if_live);
        Ok(daemon)
    }

    /// `ResolveProject`: register-or-recheck (owner, 2026-11-12). The
    /// project the client resolved is named on first sight (the first free
    /// harp offered, S54), then registered with its root and VCS identity,
    /// or rechecked against the identity registered; the project it is.
    pub async fn resolve_project(
        &self,
        request: v1::ResolveProjectRequest,
    ) -> Result<v1::Project, ApiError> {
        if request.project_id.is_empty() {
            return Err(ApiRefusal::of(&v1::ProjectUnknown {
                cwd: request.caller_cwd().to_string(),
            })
            .into());
        }
        let expected = match request.name.is_empty() {
            true => String::new(),
            false => ProjectName::parse(&request.name)
                .map_err(|rejection| invalid(&request.name, &rejection))?
                .harp()
                .to_string(),
        };
        let id = request.project_id.as_str();
        if let Some(path) = self
            .legacy_stores
            .as_ref()
            .and_then(|legacy| legacy.held(id))
        {
            return Err(ApiRefusal::of(&v1::PerProjectStore { path }).into());
        }
        let before = self.project(id).await?;
        if before.harp.is_empty() {
            self.name(id).await?;
        }
        let register = RegisterProject {
            project_id: id.to_string(),
            root: request.root,
            git_common_dir: request.git_common_dir,
            root_commit: request.root_commit,
            expected_harp: expected,
        };
        // Keyed by all it says and the identity it is judged against: the
        // same recheck against the same registration is a DUPLICATE, and
        // decides as it did (a harp never changes, and an identity changes
        // only once, when an empty one is adopted, after which the key is
        // another and the recheck strict).
        let key = format!(
            "register:{id}:{}:{}:{}:{}:{}:{}",
            !before.root.is_empty(),
            before.root_commit,
            register.root,
            register.git_common_dir,
            register.root_commit,
            register.expected_harp
        );
        self.send(keyed_book(project_cover(id), &register, &key))
            .await?;
        let state = self.project(id).await?;
        Ok(v1::Project {
            name: format!("projects/{}", state.harp),
            root: state.root,
            uid: id.to_string(),
            git_common_dir: state.git_common_dir,
            root_commit: state.root_commit,
        })
    }

    /// Name the project `id` with the first harp offered that no other
    /// project holds (its claim first, then the name).
    async fn name(&self, id: &str) -> Result<(), ApiError> {
        for harp in self.parts.harps.fresh() {
            let Ok(name) = ProjectName::new(&harp) else {
                continue;
            };
            let claim = ClaimProjectName {
                harp: harp.clone(),
                project_id: id.to_string(),
            };
            let cover = Cover {
                domain: PROJECT_NAMES_DOMAIN.to_string(),
                root: Some(name.root()),
                edition: None,
            };
            let claimed = keyed_book(cover, &claim, &format!("project-claim:{id}:{harp}"));
            match self.send(claimed).await {
                Ok(()) => {}
                Err(ApiError::Refused(_)) => continue,
                Err(failed) => return Err(failed),
            }
            let named = NameProject {
                project_id: id.to_string(),
                harp: harp.clone(),
            };
            let key = format!("project-name:{id}:{harp}");
            return self.send(keyed_book(project_cover(id), &named, &key)).await;
        }
        Err(ApiError::Failed {
            detail: format!("no harp offered was free for the project {id}"),
        })
    }

    /// Send `book` through the core: a refusal is the domain's, as it
    /// refused.
    async fn send(&self, book: angzarr_embedded::v1::CommandBook) -> Result<(), ApiError> {
        match self.parts.edge.handle(book).await {
            Ok(_) => Ok(()),
            Err(Failure::Rejected(rejected)) => Err(ApiRefusal::rejected(&rejected).into()),
            Err(other) => Err(ApiError::failed(other)),
        }
    }

    /// The project `id` as its own stream says it now.
    pub(crate) async fn project(&self, id: &str) -> Result<sessions::ProjectState, ApiError> {
        let pages = self
            .parts
            .edge
            .events(&project_cover(id), 0)
            .await
            .map_err(ApiError::failed)?;
        let mut state = sessions::ProjectState::default();
        for page in pages {
            let Some(event_page::Body::Event(event)) = page.body else {
                continue;
            };
            sessions::fold_project(
                &mut state,
                ProjectEvent::from_any(&event).map_err(ApiError::failed)?,
            );
        }
        Ok(state)
    }

    /// `GetProject`: a project by its resource name, from the warm index.
    pub async fn get_project(
        &self,
        request: v1::GetProjectRequest,
    ) -> Result<v1::Project, ApiError> {
        let name = ProjectName::parse(&request.name)
            .map_err(|rejection| invalid(&request.name, &rejection))?;
        // Read fresh, as every read is: a daemon just started answers what
        // was registered before it (a session's MCP call retried on it).
        self.parts.hosting.fresh().await;
        let row = self.parts.index.index().project_named_by(name.harp());
        let row = row.ok_or_else(|| {
            ApiError::from(ApiRefusal::of(&v1::ProjectNotFound {
                name: request.name.clone(),
            }))
        })?;
        Ok(v1::Project {
            name: name.to_string(),
            root: row.root,
            uid: row.project_id,
            git_common_dir: row.git_common_dir,
            root_commit: row.root_commit,
        })
    }

    /// `GetSession`: a session by its resource name, from the warm index,
    /// its state its lock's.
    pub async fn get_session(
        &self,
        request: v1::GetSessionRequest,
    ) -> Result<v1::Session, ApiError> {
        let name = SessionName::parse(&request.name)
            .map_err(|rejection| invalid(&request.name, &rejection))?;
        self.parts.hosting.fresh().await;
        let index = self.parts.index.index();
        let record = index.find_by_id(&name.uid()).ok_or_else(|| {
            ApiError::from(ApiRefusal::of(&v1::SessionNotFound {
                name: request.name.clone(),
            }))
        })?;
        let mut session = self.resource(&index, &record).0;
        session.pending = self
            .parts
            .pending
            .pending()
            .pending(&record.id)
            .into_iter()
            .map(|message| v1::PendingMessage {
                message_id: message.message_id,
                urgency: sessions::urgency_word(message.urgency).to_string(),
                text: message.text,
                sender: message
                    .sender
                    .map(|sender| index.name_of_ref(&sender))
                    .unwrap_or_default(),
            })
            .collect();
        // What its latest run was given and how it failed (step 9).
        let board = self.parts.board.board();
        let latest = (1..=record.incarnation)
            .rev()
            .find_map(|incarnation| board.run(&record.id, incarnation));
        if let Some(run) = latest {
            session.grant = crate::shown::grant_of(run);
            session.last_failure =
                crate::shown::failure_of(&record.harp, record.owner.is_some(), run);
        }
        // The terminal wake: whether its MCP server watches it, and its wake.
        session.wakeable = self.wakeable(&record.id);
        session.wake = crate::watch::wake_of(board.session(&record.id)).to_string();
        Ok(session)
    }

    /// The session `record` as its resource, with its state, and as the
    /// view a search matches.
    fn resource(&self, index: &SessionIndex, record: &SessionRecord) -> (v1::Session, View) {
        let (session, state) = state_resource(self.parts.states.as_ref(), index, record);
        let transcript = index.transcript(&record.id);
        let title = match record.title.is_empty() {
            false => Some(record.title.clone()),
            true => transcript.as_deref().and_then(title_of),
        };
        let view = View {
            record: record.clone(),
            state,
            title,
            captured: transcript.map(|entries| Captured {
                bytes: session_index::printed_bytes(&entries),
            }),
            pending: Vec::new(),
        };
        (session, view)
    }

    /// Every session under `parent` (`projects/-`: every project's), as
    /// resources and views, most recently started first.
    pub(crate) async fn under(&self, parent: &str) -> Result<Vec<(v1::Session, View)>, ApiError> {
        let project = match parent == EVERY_PROJECT {
            true => None,
            false => Some(
                ProjectName::parse(parent)
                    .map_err(|rejection| invalid(parent, &rejection))?
                    .harp()
                    .to_string(),
            ),
        };
        self.parts.hosting.fresh().await;
        let index = self.parts.index.index();
        Ok(index
            .records()
            .into_iter()
            .filter(|record| match &project {
                None => true,
                Some(harp) => index.project_harp(&record.project_id) == Some(harp.as_str()),
            })
            .map(|record| self.resource(&index, &record))
            .collect())
    }

    /// `ListSessions`: a project's sessions (or every project's), in a
    /// state (unspecified: any), only those with a transcript if asked.
    pub async fn list_sessions(
        &self,
        request: v1::ListSessionsRequest,
    ) -> Result<v1::ListSessionsResponse, ApiError> {
        let sessions = self
            .under(&request.parent)
            .await?
            .into_iter()
            .filter(|(session, _)| in_state(session, request.state))
            .filter(|(_, view)| !request.has_transcript || view.captured.is_some())
            .map(|(mut session, view)| {
                if request.transcripts {
                    session.transcript_captured = view.captured.is_some();
                    session.transcript_bytes = view.captured.as_ref().map_or(0, |c| c.bytes);
                }
                session
            })
            .collect();
        Ok(v1::ListSessionsResponse {
            sessions,
            next_page_token: String::new(),
        })
    }

    /// `SearchSessions`: the sessions under `parent` whose harp or title
    /// holds every word, case-insensitively (the listing's search).
    pub async fn search_sessions(
        &self,
        request: v1::SearchSessionsRequest,
    ) -> Result<v1::ListSessionsResponse, ApiError> {
        let sessions = self
            .under(&request.parent)
            .await?
            .into_iter()
            .filter(|(session, _)| in_state(session, request.state))
            .filter(|(_, view)| view.matches(&request.words))
            .map(|(session, _)| session)
            .collect();
        Ok(v1::ListSessionsResponse {
            sessions,
            next_page_token: String::new(),
        })
    }
}

/// The session `record` as its resource with its state, which is its lock's
/// (the index knows none); a live session's end is blank: a resumed one keeps
/// its last run's stamp, which is no end of the run in progress.
pub(crate) fn state_resource(
    states: &dyn SessionStates,
    index: &SessionIndex,
    record: &SessionRecord,
) -> (v1::Session, State) {
    let state = states.state(&record.project_dir, &record.harp);
    let mut session = index.resource(record);
    session.state = state_of(state) as i32;
    if state == State::Live {
        session.end_time = None;
    }
    (session, state)
}

/// A project's own stream, rooted on its id (S54).
fn project_cover(project_id: &str) -> Cover {
    Cover {
        domain: PROJECTS_DOMAIN.to_string(),
        root: Some(project_id_root(project_id)),
        edition: None,
    }
}

/// `name`, refused as no resource name: the reason the grammar gave.
pub(crate) fn invalid(name: &str, rejection: &SessionRejection) -> ApiError {
    let reason = match rejection {
        SessionRejection::NameMalformed { reason, .. } => reason.clone(),
        _ => "shape".to_string(),
    };
    ApiRefusal::of(&v1::NameInvalid {
        name: name.to_string(),
        reason,
    })
    .into()
}

/// A lock's state, as the API says it.
fn state_of(state: State) -> v1::session::State {
    match state {
        State::Live => v1::session::State::Live,
        State::Ended => v1::session::State::Ended,
        State::Unknown => v1::session::State::Unknown,
    }
}

/// Whether `session` is in `state` (unspecified: any).
fn in_state(session: &v1::Session, state: i32) -> bool {
    state == v1::session::State::Unspecified as i32 || session.state == state
}

#[cfg(test)]
#[path = "server.test.rs"]
pub(crate) mod tests;
