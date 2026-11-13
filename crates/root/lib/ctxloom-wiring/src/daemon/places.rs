//! What the daemon's destroy reaches (slice 2 step 6, owner 2026-11-12):
//! a session's project, by its directory (its session directories and
//! locks, `session-files` and `liveness`; its scratch worktrees,
//! `worktree-git` run in the caller's environment; its configuration, read
//! from its registered root as a command in it reads it), and the daemon's
//! ONE store, purged stream by stream (`sessions_application::purge`, head
//! guarded). No second connection to the store is opened.

use std::ffi::OsString;
use std::path::PathBuf;
use std::sync::Arc;

use angzarr_embedded::angzarr_store_sqlite::SqliteStore;
use angzarr_embedded::v1::Uuid;
use angzarr_embedded::{StreamRef, Timestamp};
use config_files::ConfigFilesFs;
use daemon_api::{Answer, ApiError};
use file_system::Fs;
use isolation_application::{WorktreeUse, Worktrees};
use liveness::LockFiles;
use process_env::EnvSnapshot;
use reclaim_application::SweepConfig;
use session_files::SessionDirs;
use session_ports::SessionFiles;
use worktree_git::GitWorkspaces;

use crate::{SweepSettings, Wiring};

/// A project's places, over this process's filesystem, ctxloom's `home`
/// and the temporary root.
pub(crate) struct ProjectPlaces {
    pub(crate) fs: Arc<dyn Fs>,
    pub(crate) home: PathBuf,
    pub(crate) temp: PathBuf,
}

impl ProjectPlaces {
    /// The caller's environment `env` (S7), as a process at `dir` sees it.
    fn snapshot(&self, dir: &str, env: &[(String, String)]) -> Arc<EnvSnapshot> {
        let vars: Vec<(OsString, OsString)> = env
            .iter()
            .map(|(name, value)| (name.into(), value.into()))
            .collect();
        Arc::new(EnvSnapshot::from_parts(
            os::host().flavor(),
            Ok(PathBuf::from(dir)),
            self.temp.clone(),
            vars,
        ))
    }
}

impl daemon_api::Places for ProjectPlaces {
    fn files(&self, project_dir: &str) -> Box<dyn SessionFiles + Send> {
        Box::new(SessionDirs::new(
            Arc::clone(&self.fs),
            PathBuf::from(project_dir),
        ))
    }

    fn worktrees(
        &self,
        project_dir: &str,
        _project_id: &str,
        env: &[(String, String)],
    ) -> Box<dyn WorktreeUse + Send> {
        let dir = PathBuf::from(project_dir);
        Box::new(Worktrees::new(
            GitWorkspaces::new(
                Arc::clone(&self.fs),
                dir.clone(),
                None,
                self.snapshot(project_dir, env),
            ),
            LockFiles::new(Arc::clone(&self.fs), dir),
        ))
    }

    fn settings(&self, root: &str, env: &[(String, String)]) -> Box<dyn SweepConfig + Send> {
        // The project's layered configuration as a command run at its
        // registered root finds it, under this daemon's home.
        let mut env: Vec<(String, String)> = env
            .iter()
            .filter(|(name, _)| name != "CTXLOOM_ROOT")
            .cloned()
            .collect();
        env.push(("CTXLOOM_ROOT".to_string(), root.to_string()));
        let wiring = Wiring::at(self.snapshot(root, &env), self.home.clone());
        Box::new(SweepSettings::new(
            Arc::new(ConfigFilesFs::new(
                Arc::clone(&self.fs),
                wiring.locate().1,
                &self.home,
            )),
            env,
        ))
    }

    fn now(&self) -> Timestamp {
        let now = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap_or_default();
        Timestamp {
            seconds: i64::try_from(now.as_secs()).unwrap_or(i64::MAX),
            nanos: i32::try_from(now.subsec_nanos()).unwrap_or(0),
        }
    }
}

/// The daemon's one store, as the destroy purges it.
pub(crate) struct StorePurges(pub(crate) Arc<SqliteStore>);

impl daemon_api::Purges for StorePurges {
    fn purge<'a>(&'a self, domain: &'a str, root: Uuid) -> Answer<'a, u64> {
        Box::pin(async move {
            let root = uuid::Uuid::from_slice(&root.value).map_err(|e| ApiError::Failed {
                detail: e.to_string(),
            })?;
            sessions_application::purge(self.0.as_ref(), StreamRef::main(domain, root))
                .await
                .map_err(|e| ApiError::Failed {
                    detail: angzarr_diagnostic::Diagnostic::code(&e).to_string(),
                })
        })
    }
}

#[cfg(test)]
#[path = "places.test.rs"]
mod tests;
