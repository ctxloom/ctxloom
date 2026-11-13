//! A command as a client of the user's daemon (slice 2 step 6): the
//! `daemon status|stop` and `session list|show|search` use cases over the
//! `daemon-ports` API, never a transport.
//!
//! Each call reaches a daemon first (the one serving, or one started and
//! waited for, S16; `daemon status|stop` never start one, S14), resolves its
//! project with it (`ResolveProject`, register-or-recheck, S6), and sends
//! the caller's whole environment with every request (S7). A call whose
//! daemon could not be reached mid-call is made ONCE more, on the daemon
//! reached again (a new one, S8); the reads are idempotent.

use daemon_ports::{Called, Daemons};
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
        let _ = (&self.daemons, &self.project, &self.cwd, &self.env);
        todo!("RED: slice 2 step 6")
    }

    /// `daemon stop`: ask the serving daemon to stop.
    pub async fn stop(&self, even_if_live: bool) -> Result<Option<v1::Daemon>, CallError> {
        let _ = even_if_live;
        todo!("RED: slice 2 step 6")
    }
}

impl<D: Daemons> SessionReads for Client<D> {
    async fn list_sessions(
        &self,
        all: bool,
        state: v1::SessionState,
    ) -> Result<v1::ListSessionsResponse, CallError> {
        let _ = (all, state, EVERY_PROJECT, Called::Unauthenticated);
        todo!("RED: slice 2 step 6")
    }

    async fn search_sessions(
        &self,
        words: Vec<String>,
        all: bool,
        state: v1::SessionState,
    ) -> Result<v1::ListSessionsResponse, CallError> {
        let _ = (words, all, state);
        todo!("RED: slice 2 step 6")
    }

    async fn get_session(&self, harp: &str) -> Result<v1::Session, CallError> {
        let _ = harp;
        todo!("RED: slice 2 step 6")
    }
}

#[cfg(test)]
#[path = "session_client.test.rs"]
mod tests;
