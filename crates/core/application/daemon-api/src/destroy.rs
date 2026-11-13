//! The daemon's destroy (slice 2 step 6, owner 2026-11-12): `DeleteSession`
//! (`session remove`) and `SweepSessions` (`session sweep`), run INSIDE the
//! daemon, the one owner of the store (T1, ruling 131), so no client opens
//! the store to destroy a session.
//!
//! A removal is reclaim-application's [`reclaim_application::remove`], the
//! one safe order both verbs share: the session's scratch worktrees
//! classified first, the whole removal refused if one is not safe, else the
//! worktrees removed, then the session destroyed. What it takes is
//! [`Removing`], over the session index the daemon keeps warm, the daemon's
//! one store ([`Purges`]) and the session's own project ([`Places`]: its
//! files and lock, its worktrees, its configuration read from its
//! REGISTERED root). A refused removal answers [`v1::RemovalRefused`],
//! which carries its plan, so the client says what was not done.

use angzarr_embedded::v1::Uuid;
use angzarr_embedded::Timestamp;
use daemon_proto::v1;
use isolation_application::WorktreeUse;
use reclaim_application::SweepConfig;
use run_ports::Edge;
use session_ports::SessionFiles;

use crate::refusal::ApiError;
use crate::server::{Answer, Api};

/// A project's places a removal reaches, by its directory (the root's):
/// its sessions' files and locks, its scratch worktrees, its configuration.
pub trait Places: Send + Sync {
    /// The session directories of the project at `project_dir`.
    fn files(&self, project_dir: &str) -> Box<dyn SessionFiles + Send>;
    /// The scratch worktrees of the project at `project_dir`, git run in the
    /// caller's environment `env` (S7).
    fn worktrees(&self, project_dir: &str, env: &[(String, String)])
        -> Box<dyn WorktreeUse + Send>;
    /// The configuration of the project registered at `root`, its
    /// overrides the caller's environment `env`.
    fn settings(&self, root: &str, env: &[(String, String)]) -> Box<dyn SweepConfig + Send>;
    /// Now: the retention's and the purge age's instant.
    fn now(&self) -> Timestamp;
}

/// The daemon's one store, as a destroy purges a stream of it whole.
pub trait Purges: Send + Sync {
    /// Purge the stream `domain`/`root` at the head just read (retried on a
    /// conflict); how many events it removed.
    fn purge<'a>(&'a self, domain: &'a str, root: Uuid) -> Answer<'a, u64>;
}

impl<E: Edge> Api<E> {
    /// `DeleteSession` (`session remove`, a `session-destroy` build's, M5):
    /// the session by its resource name, removed whole or not at all
    /// ([`reclaim_application::remove`]); `validate_only` is the plan.
    /// Refused, it answers [`v1::RemovalRefused`] carrying the plan.
    pub async fn delete_session(
        &self,
        request: v1::DeleteSessionRequest,
    ) -> Result<v1::DeleteSessionResponse, ApiError> {
        let _ = request;
        unimplemented!("RED: DeleteSession")
    }

    /// `SweepSessions` (`session sweep`, a `session-destroy` build's, M5).
    pub async fn sweep_sessions(
        &self,
        request: v1::SweepSessionsRequest,
    ) -> Result<v1::SweepSessionsResponse, ApiError> {
        let _ = request;
        unimplemented!("RED: SweepSessions")
    }
}

#[cfg(test)]
#[path = "destroy.test.rs"]
pub(crate) mod tests;
