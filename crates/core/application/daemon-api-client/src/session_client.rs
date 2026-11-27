//! A command as a client of the user's daemon (slice 2 step 6): the
//! `daemon status|stop`, `session list|show|search` and (owner,
//! 2026-11-12) `session remove|sweep` use cases over the `daemon-ports`
//! API, never a transport.
//!
//! Each call reaches a daemon first (the one serving, or one started and
//! waited for, S16; `daemon status|stop` never start one, S14), resolves its
//! project with it (`ResolveProject`, register-or-recheck, S6), and sends
//! the caller's whole environment with every request (S7). A call whose
//! daemon could not be reached mid-call is made ONCE more, on the daemon
//! reached again (a new one, S8); the reads are idempotent, and a write
//! (`session remove|sweep`) carries one request id, the same on its retry,
//! so the daemon answers one already applied with its first result.

use daemon_ports::{Called, DaemonApi, Daemons};
use daemon_proto::v1;
use daemon_proto::{Carries as _, CarriesMut as _};

use crate::client::CallError;

/// Every project's sessions, as a `ListSessions` parent.
const EVERY_PROJECT: &str = "projects/-";

/// The home layer's root, outside a project (OQ-3).
const HOME_PARENT: &str = "users/me";

/// What a command asks of its daemon, besides the call: its project (none
/// outside one) and its environment.
pub struct Client<D> {
    daemons: D,
    project: Option<v1::ResolveProjectRequest>,
    /// What every request made on the user's behalf carries of this client
    /// (owner, 2026-11-18): its working directory, its whole environment
    /// (S7) and which client it is; a command's own flags and presentable
    /// are the request's.
    caller: v1::Caller,
    /// Mints a write's request id (S8), once per call, retry included.
    ids: RequestIds,
    /// Its directory's project has no identity yet (a session read names no
    /// project, so it mints none: owner ruling 2026-11-14), so nothing is
    /// recorded in it.
    unnamed: bool,
    /// Whether a call whose daemon could not be reached is made once more
    /// on a new one (S8); only test-support turns it off.
    retries: bool,
    /// What runs before that one retry (test-support's
    /// `CTXLOOM_TEST_BEFORE_RETRY`, step 10c U13's harness); none, nothing.
    before_retry: Option<BeforeRetry>,
}

/// What a client runs before its one retry.
pub type BeforeRetry = Box<dyn Fn() + Send + Sync>;

/// What mints a write's request id (S8): unique per call.
pub type RequestIds = Box<dyn Fn() -> String + Send + Sync>;

/// The session reads a command makes, as the CLI calls them.
#[allow(async_fn_in_trait)]
pub trait SessionReads {
    /// `session list`: this project's sessions (`all`: every project's), in
    /// `state` (unspecified: any).
    async fn list_sessions(
        &self,
        all: bool,
        state: v1::session::State,
    ) -> Result<v1::ListSessionsResponse, CallError>;

    /// `session transcript list`: this project's sessions (`all`: every
    /// project's), each with whether its events hold a transcript and its size.
    async fn list_transcripts(&self, all: bool) -> Result<v1::ListSessionsResponse, CallError>;

    /// `session search`: the sessions whose words match.
    async fn search_sessions(
        &self,
        words: Vec<String>,
        all: bool,
        state: v1::session::State,
    ) -> Result<v1::ListSessionsResponse, CallError>;

    /// `session show`: this project's session `harp`.
    async fn get_session(&self, harp: &str) -> Result<v1::Session, CallError>;

    /// `session transcript show`: this project's session `harp`'s
    /// transcript, its D27 lines as delivered so far (owner, H3 follow-up:
    /// a session read reaches the daemon, so its flush runs first).
    async fn get_transcript(&self, harp: &str) -> Result<v1::Transcript, CallError>;
}

/// What a removal the daemon was asked came to: done (or planned, without
/// `yes`), or refused with its plan, which a refused removal still prints
/// (owner, 2026-11-12: rows and refusals say what was not done).
#[derive(Clone, Debug, PartialEq)]
pub enum Removed {
    Done(v1::DeleteSessionResponse),
    Refused {
        plan: v1::DeleteSessionResponse,
        why: daemon_api_contract::RemovalRejection,
    },
}

/// The destroys a command asks of its daemon (`session remove`, `session
/// sweep`: the `purge` build's verbs).
#[allow(async_fn_in_trait)]
pub trait SessionRemovals {
    /// `session remove <harp>`: this project's session removed whole or not
    /// at all; without `yes` its plan.
    async fn delete_session(
        &self,
        harp: &str,
        yes: bool,
        even_if_live: bool,
    ) -> Result<Removed, CallError>;

    /// `session remove <harp>` at a terminal (step 10c U11): the apply of
    /// the preview tagged `etag`, with the person's `answers` to its needs.
    async fn apply_removal(
        &self,
        harp: &str,
        even_if_live: bool,
        etag: &str,
        answers: Vec<v1::Answer>,
    ) -> Result<Removed, CallError>;

    /// `session sweep`: this project's sessions (`all`: every project's)
    /// by the sweep's table, purged before `older_than` (none: the
    /// configured age); without `yes` the report.
    async fn sweep_sessions(
        &self,
        all: bool,
        older_than: Option<String>,
        yes: bool,
    ) -> Result<v1::SweepSessionsResponse, CallError>;
}

impl<D: Daemons> Client<D> {
    /// A client reaching its daemon through `daemons`, in `project` (what
    /// it resolved of its working directory; none outside a project), as
    /// `caller` (its directory, its whole environment, which client it is).
    pub fn new(
        daemons: D,
        project: Option<v1::ResolveProjectRequest>,
        caller: v1::Caller,
    ) -> Client<D> {
        Client {
            daemons,
            project,
            caller,
            ids: Box::new(String::new),
            unnamed: false,
            retries: true,
            before_retry: None,
        }
    }

    /// `own`, a request's own caller (a command's flags and presentable),
    /// with what this client says that it does not: its directory, its
    /// environment and which client it is.
    fn filled(&self, own: Option<&v1::Caller>) -> v1::Caller {
        let mut filled = own.cloned().unwrap_or_default();
        filled.fill_from(&self.caller);
        filled
    }

    /// This client, its directory's project one with no identity yet (a
    /// session read names no project, so it mints none: owner ruling
    /// 2026-11-14): nothing can be
    /// recorded in it, so its own listing and search are empty, asked of no
    /// daemon; what addresses one of its sessions is refused as outside a
    /// project, and every project's listing is every project's.
    pub fn unnamed(self) -> Client<D> {
        Client {
            unnamed: true,
            ..self
        }
    }

    /// Whether a read of this client's own project answers nothing,
    /// unasked: its project has no identity, so nothing is recorded in it.
    fn nothing_recorded(&self, all: bool) -> bool {
        self.unnamed && !all && self.project.is_none()
    }

    /// This client, a call whose daemon could not be reached answered as
    /// it is, never made on a new one: test-support's
    /// `CTXLOOM_TEST_NO_RETRY`, which leaves what a killed daemon left.
    #[must_use]
    pub fn without_retry(self) -> Client<D> {
        Client {
            retries: false,
            ..self
        }
    }

    /// This client, `then` run once before a call whose daemon could not
    /// be reached is made again (test-support's
    /// `CTXLOOM_TEST_BEFORE_RETRY`: a person's edit between the loss and
    /// the retry, forced rather than waited for).
    #[must_use]
    pub fn before_retry(self, then: BeforeRetry) -> Client<D> {
        Client {
            before_retry: Some(then),
            ..self
        }
    }

    /// This client, each write's request id minted by `ids` (none, by
    /// default).
    pub fn with_request_ids(self, ids: RequestIds) -> Client<D> {
        Client { ids, ..self }
    }

    /// `RedeliverContext` (step 10c U13): what the live session `harp` of
    /// this client's project, at its engine's hook `carried_by`, is to
    /// carry: a re-assembled context, or none.
    pub async fn redeliver_context(
        &self,
        harp: &str,
        carried_by: &str,
        budget: usize,
    ) -> Result<v1::RedeliveredContext, CallError> {
        self.on_daemon(async |api: &D::Api| {
            let request = v1::RedeliverContextRequest {
                name: format!("{}/sessions/{harp}", self.project(api).await?),
                carried_by: carried_by.to_string(),
                // What the hook's other parts left of its one budget
                // (`setup_application::remaining`).
                budget: u32::try_from(budget).unwrap_or(u32::MAX),
                caller: Some(self.caller.clone()),
            };
            api.redeliver_context(request).await
        })
        .await
    }

    /// `daemon status`: the serving daemon, or none (S14: none is started).
    pub async fn status(&self) -> Result<Option<v1::Daemon>, CallError> {
        let Some(api) = self.daemons.serving().await.map_err(CallError::Unreached)? else {
            return Ok(None);
        };
        let request = v1::GetDaemonRequest {
            name: "daemon".to_string(),
        };
        // One that stopped answering since it was found serves no more.
        match api.get_daemon(request).await {
            Err(Called::Unreachable { .. }) => Ok(None),
            answered => Ok(Some(answered?)),
        }
    }

    /// `daemon stop`: ask the serving daemon to stop (S12: refused while it
    /// hosts live runs, unless `even_if_live`); none serving, nothing to do.
    pub async fn stop(&self, even_if_live: bool) -> Result<Option<v1::Daemon>, CallError> {
        let Some(api) = self.daemons.serving().await.map_err(CallError::Unreached)? else {
            return Ok(None);
        };
        let request = v1::StopDaemonRequest {
            name: "daemon".to_string(),
            even_if_live,
            ..v1::StopDaemonRequest::default()
        };
        match api.stop_daemon(request).await {
            Err(Called::Unreachable { .. }) => Ok(None),
            answered => Ok(Some(answered?)),
        }
    }

    /// `daemon stop --drain` and `ctxloom shutdown` (OD-54): ask the serving
    /// daemon to hand what it hosts over to the next daemon and exit,
    /// waiting `within` for it (none: its configured `daemon.drain_timeout`);
    /// none serving, nothing. Its answer carries the drain's warning
    /// (`Daemon.warnings`).
    pub async fn drain(
        &self,
        within: Option<std::time::Duration>,
    ) -> Result<Option<v1::Daemon>, CallError> {
        let Some(api) = self.daemons.serving().await.map_err(CallError::Unreached)? else {
            return Ok(None);
        };
        let request = v1::StopDaemonRequest {
            name: "daemon".to_string(),
            drain: true,
            drain_timeout_seconds: within.map(|within| within.as_secs()),
            ..v1::StopDaemonRequest::default()
        };
        match api.stop_daemon(request).await {
            Err(Called::Unreachable { .. }) => Ok(None),
            answered => Ok(Some(answered?)),
        }
    }

    /// Where a library verb's resource names are rooted (OD-40; OQ-2, OQ-3):
    /// this client's project as the daemon's `ResolveProject` names it
    /// (`projects/{harp}`, registered or rechecked, so the daemon is reached,
    /// started if none serves), or `users/me` outside a project, where no
    /// daemon is asked.
    pub async fn library_parent(&self) -> Result<String, CallError> {
        if self.project.is_none() {
            return Ok(HOME_PARENT.to_string());
        }
        self.on_daemon(async |api: &D::Api| self.project(api).await)
            .await
    }

    /// A dry run's parent (owner ruling 2026-11-14: every dry run calls the
    /// daemon, spawning it if needed, and never mints): this client's
    /// project as the daemon names it, `users/me` outside a project, or
    /// none in a project nothing has named ([`Client::unnamed`]). Where
    /// there is nothing to resolve, the daemon is reached all the same and
    /// asked `GetDaemon`, so the call proves it answers; one that cannot be
    /// had fails it.
    pub async fn named_parent(&self) -> Result<Option<String>, CallError> {
        if self.project.is_some() {
            return self
                .on_daemon(async |api: &D::Api| self.project(api).await)
                .await
                .map(Some);
        }
        let request = v1::GetDaemonRequest {
            name: "daemon".to_string(),
        };
        self.on_daemon(async |api: &D::Api| api.get_daemon(request.clone()).await)
            .await?;
        Ok((!self.unnamed).then(|| HOME_PARENT.to_string()))
    }

    /// `call` on a reached daemon, once more on the daemon reached again if
    /// this one could not be reached (S8).
    async fn on_daemon<T>(
        &self,
        call: impl AsyncFn(&D::Api) -> Result<T, Called>,
    ) -> Result<T, CallError> {
        let api = self.daemons.reach().await.map_err(CallError::Unreached)?;
        match call(&api).await {
            Err(Called::Unreachable { .. }) if self.retries => {
                if let Some(then) = &self.before_retry {
                    then();
                }
                let again = self.daemons.reach().await.map_err(CallError::Unreached)?;
                Ok(call(&again).await?)
            }
            answered => Ok(answered?),
        }
    }

    /// This client's project, resolved with `api` (registered or rechecked):
    /// its resource name. Outside a project it is refused.
    async fn project(&self, api: &D::Api) -> Result<String, Called> {
        let Some(request) = &self.project else {
            return Err(refused(&v1::ProjectUnknown {
                cwd: self.caller.cwd.clone(),
            }));
        };
        let request = v1::ResolveProjectRequest {
            caller: Some(self.filled(request.caller())),
            ..request.clone()
        };
        Ok(api.resolve_project(request).await?.name)
    }

    /// The parent a listing reads under: every project's (this one, if any,
    /// resolved first all the same), or this one's.
    async fn parent(&self, api: &D::Api, all: bool) -> Result<String, Called> {
        match (all, self.project.is_some()) {
            (true, false) => Ok(EVERY_PROJECT.to_string()),
            (true, true) => {
                self.project(api).await?;
                Ok(EVERY_PROJECT.to_string())
            }
            (false, _) => self.project(api).await,
        }
    }
}

/// The daemon's own refusal `message`, as a call answered with it.
fn refused<T: daemon_proto::Refusal>(message: &T) -> Called {
    let refusal = daemon_api_contract::ApiRefusal::of(message);
    Called::Refused {
        code: refusal.code,
        refusal: refusal.refusal,
    }
}

impl<D: Daemons> SessionReads for Client<D> {
    async fn list_transcripts(&self, all: bool) -> Result<v1::ListSessionsResponse, CallError> {
        if self.nothing_recorded(all) {
            return Ok(v1::ListSessionsResponse::default());
        }
        self.on_daemon(async |api: &D::Api| {
            let request = v1::ListSessionsRequest {
                parent: self.parent(api, all).await?,
                transcripts: true,
                ..v1::ListSessionsRequest::default()
            };
            api.list_sessions(request).await
        })
        .await
    }

    async fn list_sessions(
        &self,
        all: bool,
        state: v1::session::State,
    ) -> Result<v1::ListSessionsResponse, CallError> {
        if self.nothing_recorded(all) {
            return Ok(v1::ListSessionsResponse::default());
        }
        self.on_daemon(async |api: &D::Api| {
            let request = v1::ListSessionsRequest {
                parent: self.parent(api, all).await?,
                state: state as i32,
                ..v1::ListSessionsRequest::default()
            };
            api.list_sessions(request).await
        })
        .await
    }

    async fn search_sessions(
        &self,
        words: Vec<String>,
        all: bool,
        state: v1::session::State,
    ) -> Result<v1::ListSessionsResponse, CallError> {
        if self.nothing_recorded(all) {
            return Ok(v1::ListSessionsResponse::default());
        }
        self.on_daemon(async |api: &D::Api| {
            let request = v1::SearchSessionsRequest {
                parent: self.parent(api, all).await?,
                words: words.clone(),
                state: state as i32,
                ..v1::SearchSessionsRequest::default()
            };
            api.search_sessions(request).await
        })
        .await
    }

    async fn get_session(&self, harp: &str) -> Result<v1::Session, CallError> {
        self.on_daemon(async |api: &D::Api| {
            let request = v1::GetSessionRequest {
                name: format!("{}/sessions/{harp}", self.project(api).await?),
            };
            api.get_session(request).await
        })
        .await
    }

    async fn get_transcript(&self, harp: &str) -> Result<v1::Transcript, CallError> {
        self.on_daemon(async |api: &D::Api| {
            let request = v1::GetTranscriptRequest {
                name: format!("{}/sessions/{harp}/transcript", self.project(api).await?),
            };
            api.get_transcript(request).await
        })
        .await
    }
}

impl<D: Daemons> Client<D> {
    /// `asked` of the session `harp` (its name, request id and environment
    /// this client's), the plan or the removal, refused with its plan.
    async fn removal(
        &self,
        harp: &str,
        asked: v1::DeleteSessionRequest,
    ) -> Result<Removed, CallError> {
        // One id for the call, its retry on a new daemon included (S8).
        let request_id = (self.ids)();
        self.on_daemon(async |api: &D::Api| {
            let request = v1::DeleteSessionRequest {
                name: format!("{}/sessions/{harp}", self.project(api).await?),
                request_id: request_id.clone(),
                caller: Some(self.caller.clone()),
                ..asked.clone()
            };
            match api.delete_session(request).await {
                Ok(done) => Ok(Removed::Done(done)),
                Err(called) => {
                    let refused = called.refusal::<v1::RemovalRefusal>();
                    let why = refused.as_ref().and_then(|refused| {
                        daemon_api_contract::RemovalRejection::of(refused, harp)
                            .map(|why| (refused, why))
                    });
                    match why {
                        Some((refused, why)) => Ok(Removed::Refused {
                            plan: refused.plan.clone().unwrap_or_default(),
                            why,
                        }),
                        None => Err(called),
                    }
                }
            }
        })
        .await
    }
}

impl<D: Daemons> SessionRemovals for Client<D> {
    async fn delete_session(
        &self,
        harp: &str,
        yes: bool,
        even_if_live: bool,
    ) -> Result<Removed, CallError> {
        let asked = v1::DeleteSessionRequest {
            even_if_live,
            validate_only: !yes,
            ..v1::DeleteSessionRequest::default()
        };
        self.removal(harp, asked).await
    }

    async fn apply_removal(
        &self,
        harp: &str,
        even_if_live: bool,
        etag: &str,
        answers: Vec<v1::Answer>,
    ) -> Result<Removed, CallError> {
        let asked = v1::DeleteSessionRequest {
            even_if_live,
            etag: etag.to_string(),
            answers,
            ..v1::DeleteSessionRequest::default()
        };
        self.removal(harp, asked).await
    }

    async fn sweep_sessions(
        &self,
        all: bool,
        older_than: Option<String>,
        yes: bool,
    ) -> Result<v1::SweepSessionsResponse, CallError> {
        let request_id = (self.ids)();
        self.on_daemon(async |api: &D::Api| {
            let parent = self.parent(api, all).await?;
            let project = match self.project.is_some() {
                true => self.project(api).await?,
                false => String::new(),
            };
            let request = v1::SweepSessionsRequest {
                parent,
                older_than: older_than.clone().unwrap_or_default(),
                project,
                request_id: request_id.clone(),
                validate_only: !yes,
                caller: Some(self.caller.clone()),
                ..v1::SweepSessionsRequest::default()
            };
            api.sweep_sessions(request).await
        })
        .await
    }
}

/// The library reads a command (or a hook, O3) asks of its daemon (step
/// 10a U2, D12: one method per row of `daemon_proto::library_reads!`),
/// each under this client's project (`users/me` outside one), at its
/// working directory, with its environment (E1); each answered with the
/// notices the read said. A read's `request` says the rest (its `globals`
/// among it); its target (a Get's `name`, a List's `parent`) is the
/// parent followed by `under` (`library_api::names`' targets: empty for
/// the parent itself, else `/bundles/...`).
#[allow(async_fn_in_trait)]
pub trait LibraryReads {
    daemon_proto::library_reads!(library_read_declarations);

    /// `context assemble`'s `AssembleContext`, named this client's parent:
    /// the context, what a premise held back as data, and the read's
    /// notices among its own.
    async fn assemble_context(
        &self,
        request: v1::AssembleContextRequest,
    ) -> Result<v1::AssembledContext, CallError>;

    /// `run --dry-run`'s `CreateRun` with `validate_only` (step 10a U2),
    /// under this client's parent (`users/me` in a project nothing has
    /// named, which the daemon then leaves unnamed): the plan, its notices
    /// in the run's own.
    async fn preview_run(&self, request: v1::CreateRunRequest) -> Result<v1::Run, CallError>;
}

/// [`LibraryReads`]' methods, from `daemon_proto::library_reads!`.
macro_rules! library_read_declarations {
    ($($method:ident ($rpc:ident) $request:ident . $target:ident => $response:ident),* $(,)?) => {
        $(
            #[doc = concat!("`", stringify!($rpc), "`, under this client's parent.")]
            async fn $method(
                &self,
                under: &str,
                request: v1::$request,
            ) -> Result<crate::Read<v1::$response>, CallError>;
        )*
    };
}
use library_read_declarations;

impl<D: Daemons> Client<D> {
    /// The parent a library read is under: this client's project, as the
    /// daemon names it, else the home's.
    async fn library_parent_on(&self, api: &D::Api) -> Result<String, Called> {
        match self.project.is_some() {
            true => self.project(api).await,
            false => Ok(HOME_PARENT.to_string()),
        }
    }
}

impl<D: Daemons> LibraryReads for Client<D> {
    daemon_proto::library_reads!(library_read_requests);

    async fn assemble_context(
        &self,
        request: v1::AssembleContextRequest,
    ) -> Result<v1::AssembledContext, CallError> {
        self.on_daemon(async |api: &D::Api| {
            let request = v1::AssembleContextRequest {
                name: self.library_parent_on(api).await?,
                caller: Some(self.filled(request.caller())),
                ..request.clone()
            };
            api.assemble_context(request).await
        })
        .await
    }

    async fn preview_run(&self, request: v1::CreateRunRequest) -> Result<v1::Run, CallError> {
        self.on_daemon(async |api: &D::Api| {
            let request = v1::CreateRunRequest {
                parent: self.library_parent_on(api).await?,
                caller: Some(self.filled(request.caller())),
                validate_only: true,
                ..request.clone()
            };
            api.create_run(request).await
        })
        .await
    }
}

/// [`Client`]'s [`LibraryReads`], from `daemon_proto::library_reads!`:
/// each request's target, directory and environment this client's (asked
/// again on a retry, the daemon reached again).
macro_rules! library_read_requests {
    ($($method:ident ($rpc:ident) $request:ident . $target:ident => $response:ident),* $(,)?) => {
        $(
            async fn $method(
                &self,
                under: &str,
                request: v1::$request,
            ) -> Result<crate::Read<v1::$response>, CallError> {
                self.on_daemon(async |api: &D::Api| {
                    let parent = self.library_parent_on(api).await?;
                    let mut request = request.clone();
                    request.$target = format!("{parent}{under}");
                    request.fill_caller_from(&self.caller);
                    api.$method(request).await
                })
                .await
            }
        )*
    };
}
use library_read_requests;

/// The library writes a command asks of its daemon (step 10b U3, D12: one
/// method per row of `daemon_proto::library_writes!`), each under this
/// client's project (`users/me` outside one), with its environment and
/// working directory (E1, as the reads carry them), and
/// under ONE request id minted for the call (a request that names none),
/// so its retry on a new daemon is the same write (S8, AIP-155).
#[allow(async_fn_in_trait)]
pub trait LibraryWrites {
    daemon_proto::library_writes!(library_write_declarations);
}

/// [`LibraryWrites`]' methods, from `daemon_proto::library_writes!`.
macro_rules! library_write_declarations {
    ($($method:ident ($rpc:ident) $request:ident => $response:ident),* $(,)?) => {
        $(
            #[doc = concat!("`", stringify!($rpc), "`, under this client's parent.")]
            async fn $method(
                &self,
                under: &str,
                request: v1::$request,
            ) -> Result<v1::$response, CallError>;
        )*
    };
}
use library_write_declarations;

impl<D: Daemons> LibraryWrites for Client<D> {
    daemon_proto::library_writes!(library_write_requests);
}

/// [`Client`]'s [`LibraryWrites`], from `daemon_proto::library_writes!`.
macro_rules! library_write_requests {
    ($($method:ident ($rpc:ident) $request:ident => $response:ident),* $(,)?) => {
        $(
            async fn $method(
                &self,
                under: &str,
                request: v1::$request,
            ) -> Result<v1::$response, CallError> {
                use daemon_api_contract::LibraryWriteRequest as _;
                let mut request = request;
                if request.request_id().is_empty() {
                    request.set_request_id((self.ids)());
                }
                request.fill_caller(&self.caller);
                self.on_daemon(async |api: &D::Api| {
                    let parent = self.library_parent_on(api).await?;
                    let mut request = request.clone();
                    request.set_target(format!("{parent}{under}"));
                    api.$method(request).await
                })
                .await
            }
        )*
    };
}
use library_write_requests;

#[cfg(test)]
#[path = "session_client.test.rs"]
mod tests;
