//! What the daemon's destroy reaches (slice 2 step 6, owner 2026-11-12):
//! a session's project, by its directory (its session directories and
//! locks, `session-files` and `liveness`; its scratch worktrees,
//! `worktree-git` run in the caller's environment; its configuration, read
//! from its registered root as a command in it reads it), and the daemon's
//! ONE store, purged stream by stream (`sessions_application::purge`, head
//! guarded). No second connection to the store is opened.

use std::path::PathBuf;
use std::sync::Arc;

use angzarr_embedded::angzarr_store_sqlite::SqliteStore;
use angzarr_embedded::v1::Uuid;
use angzarr_embedded::Timestamp;
use daemon_api::Answer;
use file_system::Fs;
use isolation_application::WorktreeUse;
use reclaim_application::SweepConfig;
use session_ports::SessionFiles;

/// A project's places, over this process's filesystem, ctxloom's `home`
/// and the temporary root.
pub(crate) struct ProjectPlaces {
    pub(crate) fs: Arc<dyn Fs>,
    pub(crate) home: PathBuf,
    pub(crate) temp: PathBuf,
}

impl daemon_api::Places for ProjectPlaces {
    fn files(&self, project_dir: &str) -> Box<dyn SessionFiles + Send> {
        let _ = (&self.fs, &self.home, &self.temp, project_dir);
        unimplemented!("RED: a project's session directories")
    }

    fn worktrees(
        &self,
        project_dir: &str,
        env: &[(String, String)],
    ) -> Box<dyn WorktreeUse + Send> {
        let _ = (project_dir, env);
        unimplemented!("RED: a project's scratch worktrees")
    }

    fn settings(&self, root: &str, env: &[(String, String)]) -> Box<dyn SweepConfig + Send> {
        let _ = (root, env);
        unimplemented!("RED: a project's configuration from its registered root")
    }

    fn now(&self) -> Timestamp {
        unimplemented!("RED: the clock")
    }
}

/// The daemon's one store, as the destroy purges it.
pub(crate) struct StorePurges(pub(crate) Arc<SqliteStore>);

impl daemon_api::Purges for StorePurges {
    fn purge<'a>(&'a self, domain: &'a str, root: Uuid) -> Answer<'a, u64> {
        let _ = (&self.0, domain, root);
        unimplemented!("RED: the one store's purge")
    }
}

#[cfg(test)]
#[path = "places.test.rs"]
mod tests;
