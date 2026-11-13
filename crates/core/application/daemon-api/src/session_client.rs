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
//! reached again (a new one, S8); the reads are idempotent.

use daemon_ports::{Called, DaemonApi, Daemons};
use daemon_proto::v1;

use crate::client::CallError;

/// Every project's sessions, as a `ListSessions` parent.
const EVERY_PROJECT: &str = "projects/-";

/// What a command asks of its daemon, besides the call: its project (none
/// outside one) and its environment.
pub struct Client<D> {
    daemons: D,
    project: Option<v1::ResolveProjectRequest>,
    cwd: String,
    env: v1::EnvVars,
}

/// The session reads a command makes, as the CLI calls them.
#[allow(async_fn_in_trait)]
pub trait SessionReads {
    /// `session list`: this project's sessions (`all`: every project's), in
    /// `state` (unspecified: any).
    async fn list_sessions(
        &self,
        all: bool,
        state: v1::SessionState,
    ) -> Result<v1::ListSessionsResponse, CallError>;

    /// `session search`: the sessions whose words match.
    async fn search_sessions(
        &self,
        words: Vec<String>,
        all: bool,
        state: v1::SessionState,
    ) -> Result<v1::ListSessionsResponse, CallError>;

    /// `session show`: this project's session `harp`.
    async fn get_session(&self, harp: &str) -> Result<v1::Session, CallError>;
}

/// What a removal the daemon was asked came to: done (or planned, without
/// `yes`), or refused with its plan, which a refused removal still prints
/// (owner, 2026-11-12: rows and refusals say what was not done).
#[derive(Clone, Debug, PartialEq)]
pub enum Removed {
    Done(v1::DeleteSessionResponse),
    Refused {
        plan: v1::DeleteSessionResponse,
        why: crate::RemovalRejection,
    },
}

/// The destroys a command asks of its daemon (`session remove`, `session
/// sweep`: the `session-destroy` build's verbs).
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
    /// it resolved of its working directory `cwd`; none outside a project),
    /// with the caller's environment `env` (S7).
    pub fn new(
        daemons: D,
        project: Option<v1::ResolveProjectRequest>,
        cwd: String,
        env: v1::EnvVars,
    ) -> Client<D> {
        Client {
            daemons,
            project,
            cwd,
            env,
        }
    }

    /// `daemon status`: the serving daemon, or none (S14: none is started).
    pub async fn status(&self) -> Result<Option<v1::Daemon>, CallError> {
        let Some(api) = self.daemons.serving().await.map_err(CallError::Unreached)? else {
            return Ok(None);
        };
        let request = v1::GetDaemonRequest {
            name: "daemon".to_string(),
            env: Some(self.env.clone()),
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
            env: Some(self.env.clone()),
            ..v1::StopDaemonRequest::default()
        };
        match api.stop_daemon(request).await {
            Err(Called::Unreachable { .. }) => Ok(None),
            answered => Ok(Some(answered?)),
        }
    }

    /// `call` on a reached daemon, once more on the daemon reached again if
    /// this one could not be reached (S8).
    async fn on_daemon<T>(
        &self,
        call: impl AsyncFn(&D::Api) -> Result<T, Called>,
    ) -> Result<T, CallError> {
        let api = self.daemons.reach().await.map_err(CallError::Unreached)?;
        match call(&api).await {
            Err(Called::Unreachable { .. }) => {
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
                cwd: self.cwd.clone(),
            }));
        };
        let request = v1::ResolveProjectRequest {
            env: Some(self.env.clone()),
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
    let refusal = crate::ApiRefusal::of(message);
    Called::Refused {
        code: refusal.code,
        refusal: refusal.refusal,
    }
}

impl<D: Daemons> SessionReads for Client<D> {
    async fn list_sessions(
        &self,
        all: bool,
        state: v1::SessionState,
    ) -> Result<v1::ListSessionsResponse, CallError> {
        self.on_daemon(async |api: &D::Api| {
            let request = v1::ListSessionsRequest {
                parent: self.parent(api, all).await?,
                state: state as i32,
                env: Some(self.env.clone()),
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
        state: v1::SessionState,
    ) -> Result<v1::ListSessionsResponse, CallError> {
        self.on_daemon(async |api: &D::Api| {
            let request = v1::SearchSessionsRequest {
                parent: self.parent(api, all).await?,
                words: words.clone(),
                state: state as i32,
                env: Some(self.env.clone()),
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
                env: Some(self.env.clone()),
            };
            api.get_session(request).await
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
        self.on_daemon(async |api: &D::Api| {
            let request = v1::DeleteSessionRequest {
                name: format!("{}/sessions/{harp}", self.project(api).await?),
                even_if_live,
                validate_only: !yes,
                env: Some(self.env.clone()),
                ..v1::DeleteSessionRequest::default()
            };
            match api.delete_session(request).await {
                Ok(done) => Ok(Removed::Done(done)),
                Err(called) => {
                    let refused = called.refusal::<v1::RemovalRefused>();
                    let why = refused.as_ref().and_then(|refused| {
                        crate::RemovalRejection::of(refused, harp).map(|why| (refused, why))
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

    async fn sweep_sessions(
        &self,
        all: bool,
        older_than: Option<String>,
        yes: bool,
    ) -> Result<v1::SweepSessionsResponse, CallError> {
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
                validate_only: !yes,
                env: Some(self.env.clone()),
                ..v1::SweepSessionsRequest::default()
            };
            api.sweep_sessions(request).await
        })
        .await
    }
}

#[cfg(test)]
#[path = "session_client.test.rs"]
mod tests;
