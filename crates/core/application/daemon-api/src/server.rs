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

// RED: the use cases are not built yet.
#![allow(unused_imports, dead_code, unused_variables)]

use std::sync::Arc;

use angzarr_embedded::v1::{event_page, Cover};
use angzarr_embedded::{Failure, Payload, Timestamp};
use daemon_proto::v1;
use run_ports::Edge;
use session_index::{title_of, SessionIndexes, SessionRecord, State, View};
use sessions::{
    keyed_book, project_id_root, ClaimProjectName, NameProject, ProjectEvent, ProjectName,
    RegisterProject, SessionName, SessionRejection, PROJECTS_DOMAIN, PROJECT_NAMES_DOMAIN,
};

use crate::refusal::{ApiError, ApiRefusal};

/// The daemon's singleton name (AIP-156).
const DAEMON: &str = "daemon";

/// A `ListSessions` parent naming every project.
const EVERY_PROJECT: &str = "projects/-";

/// What the daemon's loop knows of itself.
pub trait Hosting: Send + Sync {
    /// The runs it hosts now (live), each with its session's name.
    fn hosted(&self) -> Vec<v1::HostedRun>;
    /// How long it has had nothing to do.
    fn idle_seconds(&self) -> u64;
    /// Ask it to stop: drain, or end what it hosts first (`end_hosted`).
    fn stop(&self, end_hosted: bool);
}

/// A session's state now, from its liveness lock in its project.
pub trait SessionStates: Send + Sync {
    fn state(&self, project_dir: &str, harp: &str) -> State;
}

/// Fresh project harps to try, in order (minted outside the core).
pub trait Harps: Send + Sync {
    fn fresh(&self) -> Vec<String>;
}

/// What the daemon says of itself, fixed at its start.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct About {
    pub pid: u32,
    pub endpoint: String,
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
    pub hosting: Arc<dyn Hosting>,
    pub states: Arc<dyn SessionStates>,
    pub harps: Arc<dyn Harps>,
    pub about: About,
}

/// The use cases of the daemon's API.
pub struct Api<E> {
    parts: Parts<E>,
}

impl<E: Edge> Api<E> {
    pub fn new(parts: Parts<E>) -> Api<E> {
        Api { parts }
    }

    /// `GetDaemon`: the daemon, what it hosts and its idle time. Never its
    /// bearer.
    pub async fn get_daemon(&self) -> Result<v1::Daemon, ApiError> {
        let _ = DAEMON;
        Err(ApiError::Failed {
            detail: "not built".to_string(),
        })
    }

    /// `StopDaemon`: refused `DaemonHosting` while it hosts live runs,
    /// unless `even_if_live`, which ends them first (S12).
    pub async fn stop_daemon(
        &self,
        request: v1::StopDaemonRequest,
    ) -> Result<v1::Daemon, ApiError> {
        let _ = request;
        Err(ApiError::Failed {
            detail: "not built".to_string(),
        })
    }

    /// `ResolveProject`: register-or-recheck (owner, 2026-11-12).
    pub async fn resolve_project(
        &self,
        request: v1::ResolveProjectRequest,
    ) -> Result<v1::Project, ApiError> {
        let _ = request;
        Err(ApiError::Failed {
            detail: "not built".to_string(),
        })
    }

    /// `GetProject`: a project by its resource name.
    pub async fn get_project(
        &self,
        request: v1::GetProjectRequest,
    ) -> Result<v1::Project, ApiError> {
        let _ = request;
        Err(ApiError::Failed {
            detail: "not built".to_string(),
        })
    }

    /// `GetSession`: a session by its resource name.
    pub async fn get_session(
        &self,
        request: v1::GetSessionRequest,
    ) -> Result<v1::Session, ApiError> {
        let _ = request;
        Err(ApiError::Failed {
            detail: "not built".to_string(),
        })
    }

    /// `ListSessions`.
    pub async fn list_sessions(
        &self,
        request: v1::ListSessionsRequest,
    ) -> Result<v1::ListSessionsResponse, ApiError> {
        let _ = request;
        Err(ApiError::Failed {
            detail: "not built".to_string(),
        })
    }

    /// `SearchSessions`.
    pub async fn search_sessions(
        &self,
        request: v1::SearchSessionsRequest,
    ) -> Result<v1::ListSessionsResponse, ApiError> {
        let _ = request;
        Err(ApiError::Failed {
            detail: "not built".to_string(),
        })
    }
}

#[cfg(test)]
#[path = "server.test.rs"]
mod tests;
