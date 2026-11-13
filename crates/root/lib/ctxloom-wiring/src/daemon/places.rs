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
        project_id: &str,
        env: &[(String, String)],
    ) -> Box<dyn WorktreeUse + Send> {
        let dir = PathBuf::from(project_dir);
        Box::new(Worktrees::new(
            GitWorkspaces::new(
                Arc::clone(&self.fs),
                dir.clone(),
                // Its checkouts are under the account's ctxloom home, by its
                // project's id (owner ruling 2026-11-12, Q1); none, and the
                // destroy would find none and never reclaim them.
                Some(isolation_ports::worktrees_root(&self.home, project_id)),
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

/// A project's context, assembled in the daemon (slice 3 step 6, the
/// smallest piece of T13): the library composed as a process at the
/// project's REGISTERED root would compose it, in the caller's environment
/// (S7) with ctxloom's `home`; a fragment named that the project lacks is
/// said as a notice, never a refusal.
pub(crate) struct ProjectAssemblies {
    pub(crate) home: PathBuf,
    pub(crate) temp: PathBuf,
}

impl daemon_api::Assemblies for ProjectAssemblies {
    fn assemble(
        &self,
        root: &str,
        request: &daemon_proto::v1::AssembleContextRequest,
    ) -> Result<daemon_proto::v1::AssembledContext, ApiError> {
        let mut vars: Vec<(OsString, OsString)> = request
            .env
            .iter()
            .flat_map(|env| env.vars.iter())
            .filter(|var| !matches!(var.name.as_str(), "CTXLOOM_ROOT" | "HOME"))
            .map(|var| (var.name.clone().into(), var.value.clone().into()))
            .collect();
        vars.push(("CTXLOOM_ROOT".into(), root.into()));
        vars.push(("HOME".into(), self.home.as_os_str().to_os_string()));
        let snapshot = EnvSnapshot::from_parts(
            os::host().flavor(),
            Ok(PathBuf::from(root)),
            self.temp.clone(),
            vars,
        );
        let failed = |e: &dyn angzarr_diagnostic::Diagnostic| ApiError::Failed {
            detail: e.code().to_string(),
        };
        let wiring = Wiring::new(Arc::new(snapshot)).map_err(|e| failed(&e))?;
        let library = crate::library::build(&wiring, None).map_err(|e| failed(&e))?;
        let globals = request.globals.clone().unwrap_or_default();
        let flags = library_application::Flags {
            degraded: globals.degraded,
            config_set: globals.config_set,
            no_companions: globals.no_companions,
        };
        let asked = library_application::AssembleRequest {
            profiles: request.profiles.clone(),
            fragments: request.fragments.clone(),
            tags: request.tags.clone(),
        };
        let assembled = library
            .assemble(&flags, &asked)
            .result
            .map_err(|e| failed(&e))?;
        Ok(assembled_context(assembled))
    }
}

/// An assembly as the API's message: its context, the fragments it used,
/// and a notice for each fragment or tag named that nothing holds.
fn assembled_context(
    assembled: library_application::Assembled,
) -> daemon_proto::v1::AssembledContext {
    use daemon_proto::v1;
    let missing = |code: &str, name: &str, what: String| v1::Notice {
        code: code.to_string(),
        args: vec![v1::NeedArg {
            name: "name".to_string(),
            value: name.to_string(),
        }],
        text: what,
    };
    let mut notices: Vec<v1::Notice> = assembled
        .missing_fragments
        .iter()
        .map(|name| {
            missing(
                "daemon-api-assembly-missing-fragment",
                name,
                format!("no fragment is named {name}"),
            )
        })
        .collect();
    notices.extend(assembled.missing_tags.iter().map(|name| {
        missing(
            "daemon-api-assembly-missing-tag",
            name,
            format!("no fragment is tagged {name}"),
        )
    }));
    v1::AssembledContext {
        tokens: 0,
        fragments: assembled
            .fragments_loaded
            .iter()
            .map(|fragment| v1::AssembledFragment {
                fragment: fragment.clone(),
                form: String::new(),
            })
            .collect(),
        context: assembled.context,
        notices,
    }
}
