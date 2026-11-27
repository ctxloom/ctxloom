//! What the daemon's destroy reaches (slice 2 step 6, owner 2026-11-12):
//! a session's project, by its directory (its session directories and
//! locks, `session-files` and `liveness`; its scratch worktrees,
//! `worktree-git` run in the caller's environment; its configuration, read
//! from its registered root as a command in it reads it), and the daemon's
//! ONE store, purged stream by stream (`sessions_application::purge`, head
//! guarded), and the atomic operations a removal's trash is made by
//! (content-fs's, O2). No second connection to the store is opened.

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
        Arc::new(super::caller_env(
            os::host().flavor(),
            &self.temp,
            (dir, None),
            pairs(env),
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
        let wiring = super::caller_wiring(&self.home, &self.temp, (root, Some(root)), pairs(env));
        let mut env: Vec<(String, String)> = env
            .iter()
            .filter(|(name, _)| name != "CTXLOOM_ROOT")
            .cloned()
            .collect();
        env.push(("CTXLOOM_ROOT".to_string(), root.to_string()));
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

    fn operations(&self) -> Arc<dyn ctxloom_ports::Operations> {
        // A removal's trash is its checkout's own (O2, QUESTIONS 8), made
        // by the same atomic operations as a library write's.
        Arc::new(content_fs::TreeOperations::new(Arc::clone(&self.fs)))
    }
}

/// A tree's regenerable cache (step 10b U9, `clean`): cache-files' over
/// this process's filesystem, its `.ctxloom` the tree's directory.
pub(crate) struct TreeCaches(pub(crate) Arc<dyn Fs>);

impl daemon_api::Caches for TreeCaches {
    fn cache(&self, tree: &str) -> Arc<dyn ctxloom_ports::ProjectCache> {
        Arc::new(cache_files::CacheFiles::new(
            Arc::clone(&self.0),
            std::path::Path::new(tree),
        ))
    }
}

/// `env` as name and value pairs.
fn pairs(env: &[(String, String)]) -> impl Iterator<Item = (&str, &str)> {
    env.iter()
        .map(|(name, value)| (name.as_str(), value.as_str()))
}

/// What a library read's trees are seen by (step 10a U2; the library is
/// git-free, owner 2026-11-18): the daemon's one scanner, over this
/// process's filesystem, and the home's library, `<home>/.ctxloom`, when
/// there is one.
pub(crate) struct CallerGits {
    pub(crate) fs: Arc<dyn Fs>,
    pub(crate) home: PathBuf,
    /// The one scanner every read sees a tree through (step 10c U13
    /// rework), its memo shared.
    pub(crate) scanner: Arc<TreeScanner>,
}

impl library_api::Observers for CallerGits {
    fn scanner(&self) -> Arc<dyn ctxloom_ports::LibraryScanner> {
        Arc::clone(&self.scanner) as Arc<dyn ctxloom_ports::LibraryScanner>
    }

    fn home_tree(&self) -> Option<String> {
        let app = self.home.join(".ctxloom");
        let is_dir = self
            .fs
            .metadata(&app)
            .is_ok_and(|meta| meta.kind == file_system::Kind::Dir);
        is_dir.then(|| app.display().to_string())
    }
}

/// Each checkout's harp, in its tree's `state/checkout`: minted at the
/// tree's first read (L2), read back after.
pub(crate) struct TreeHarps(pub(crate) Arc<dyn Fs>);

impl library_api::CheckoutHarps for TreeHarps {
    fn harp(&self, tree: &str) -> Result<String, ctxloom_ports::Failure> {
        ctxloom_wiring::checkout::mint_checkout_harp(
            self.0.as_ref(),
            std::path::Path::new(tree),
            crate::session_name,
        )
        .map_err(|e| Box::new(e) as ctxloom_ports::Failure)
    }

    fn fresh(&self) -> String {
        crate::session_name()
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
                    detail: coded_diagnostic::Diagnostic::code(&e).to_string(),
                })
        })
    }
}

#[cfg(test)]
#[path = "places.test.rs"]
mod tests;

/// The library a read is answered from (step 10a U2, E1): opened as a
/// process at the caller's directory, in the caller's environment with
/// ctxloom's `home` as the platform's home variable, would open it, the
/// project's installed remote bundles in its catalog.
pub(crate) struct ProjectLibraries {
    pub(crate) home: PathBuf,
    pub(crate) temp: PathBuf,
}

impl ProjectLibraries {
    /// A process at `dir` in the caller's environment `env`.
    fn at(&self, dir: &str, env: &[(String, String)]) -> Wiring {
        super::caller_wiring(&self.home, &self.temp, (dir, None), pairs(env))
    }
}

impl daemon_api::Libraries for ProjectLibraries {
    fn root(&self, dir: &str, env: &[(String, String)]) -> Option<String> {
        if dir.is_empty() {
            return None;
        }
        let (_, app) = self.at(dir, env).locate();
        if !matches!(app.source, ctxloom_ports::Source::Project) {
            return None;
        }
        app.path.parent().map(|root| root.display().to_string())
    }

    fn read(
        &self,
        dir: &str,
        env: &[(String, String)],
        read: &mut dyn FnMut(&library_application::Library),
    ) -> Result<(), ApiError> {
        let failed = |e: &dyn coded_diagnostic::Diagnostic| ApiError::Failed {
            detail: e.code().to_string(),
        };
        let at = if dir.is_empty() {
            self.home.display().to_string()
        } else {
            dir.to_string()
        };
        let wiring = self.at(&at, env);
        let library = ctxloom_wiring::library::build(&wiring).map_err(|e| failed(&e))?;
        read(&library);
        Ok(())
    }

    fn read_deps(
        &self,
        dir: &str,
        env: &[(String, String)],
        read: &mut dyn FnMut(&deps_application::Deps),
    ) -> Result<(), ApiError> {
        let at = if dir.is_empty() {
            self.home.display().to_string()
        } else {
            dir.to_string()
        };
        let wiring = self.at(&at, env);
        let deps = ctxloom_wiring::commands::build_deps(&wiring).map_err(|e| ApiError::Failed {
            detail: coded_diagnostic::Diagnostic::code(&e).to_string(),
        })?;
        read(&deps);
        Ok(())
    }
}

/// What trusting a project would allow (step 8 U1), read in the daemon as
/// a process at the project's REGISTERED root reads its configuration and
/// bundles: its own layer's project-only settings and its own `ctxloom`
/// bundle, with ctxloom's `home`.
pub(crate) struct ProjectTrusts {
    pub(crate) home: PathBuf,
    pub(crate) temp: PathBuf,
}

impl daemon_api::Trusts for ProjectTrusts {
    fn settings(&self, root: &str) -> Result<Vec<daemon_proto::v1::TrustSetting>, ApiError> {
        // Read as a process there would, with ctxloom's home and nothing
        // else of an environment.
        let wiring = super::caller_wiring(&self.home, &self.temp, (root, Some(root)), []);
        let failed = |e: &dyn coded_diagnostic::Diagnostic| ApiError::Failed {
            detail: e.code().to_string(),
        };
        let library = ctxloom_wiring::library::build(&wiring).map_err(|e| failed(&e))?;
        let held = library.trust_settings().map_err(|e| failed(&e))?;
        Ok(held
            .into_iter()
            .map(|held| daemon_proto::v1::TrustSetting {
                path: held.path,
                value: held.value,
            })
            .collect())
    }
}

#[cfg(test)]
#[path = "reconcile_bench.test.rs"]
mod reconcile_bench;

/// The library trees each live session draws on (step 10c U13 rework),
/// as the daemon's OwedContexts handler asks: its project's checkout's (the
/// project directory's, and each of its own scratch checkouts', each by the
/// harp its library was named with: one never read is no tree yet), and the
/// home's. Read, never minted.
pub(crate) struct LibraryTrees {
    pub(crate) fs: Arc<dyn Fs>,
    pub(crate) index: session_index::SessionIndexes,
    /// ctxloom's home, whose `worktrees/` holds the sessions' checkouts.
    pub(crate) home: PathBuf,
}

impl daemon_application::owed::SessionTrees for LibraryTrees {
    fn trees(&self, record: &session_index::SessionRecord) -> Vec<String> {
        let project = self
            .index
            .index()
            .project_harp(&record.project_id)
            .map(str::to_string);
        let mut trees = Vec::new();
        if let Some(project) = project {
            let checkouts =
                isolation_ports::session_worktrees(&self.home, &record.project_id, &record.harp);
            let mut dirs = vec![PathBuf::from(&record.project_dir)];
            if let Ok(entries) = self.fs.read_dir(&checkouts) {
                dirs.extend(entries.into_iter().map(|entry| checkouts.join(entry.name)));
            }
            for dir in dirs {
                let app = dir.join(".ctxloom");
                if let Ok(Some(checkout)) =
                    ctxloom_wiring::checkout::checkout_harp(self.fs.as_ref(), &app)
                {
                    let tree = format!("projects/{project}/checkouts/{checkout}/library");
                    if !trees.contains(&tree) {
                        trees.push(tree);
                    }
                }
            }
        }
        trees.push(library_tree::HOME_TREE.to_string());
        trees
    }
}

/// How recently modified a file may be and still have its hash kept: one
/// changed within a clock tick of its hashing may change again unseen by
/// its stat (git's racily-clean case), so it is hashed again until then.
const SETTLED: std::time::Duration = std::time::Duration::from_secs(2);

/// What the memo keeps of one file: its stat when hashed, when, and the
/// hash.
#[derive(Clone)]
struct Hashed {
    len: u64,
    modified: std::time::SystemTime,
    at: std::time::SystemTime,
    id: String,
}

impl Hashed {
    /// Whether this hash still stands for a file of length `len` modified
    /// at `modified`: its stat unchanged, and it had settled when hashed.
    fn reusable(&self, len: u64, modified: std::time::SystemTime) -> bool {
        self.len == len && self.modified == modified && self.modified + SETTLED <= self.at
    }
}

/// The daemon's scanner (step 10c U13 rework, owner 2026-11-18): a library
/// tree walked, each file of [`ctxloom_ports::LIBRARY_PATHS`] hashed
/// (`library_tree::content_id`), no git asked. Its memo, keyed by each
/// file's path and stat, reuses a hash whose file has not changed and had
/// settled when it was hashed ([`SETTLED`]); `forget` drops it.
pub(crate) struct TreeScanner {
    fs: Arc<dyn Fs>,
    memo: std::sync::Mutex<std::collections::BTreeMap<PathBuf, Hashed>>,
    now: Box<dyn Fn() -> std::time::SystemTime + Send + Sync>,
}

impl TreeScanner {
    pub(crate) fn new(fs: Arc<dyn Fs>) -> TreeScanner {
        TreeScanner::timed(fs, Box::new(std::time::SystemTime::now))
    }

    /// The scanner, its clock `now`.
    fn timed(
        fs: Arc<dyn Fs>,
        now: Box<dyn Fn() -> std::time::SystemTime + Send + Sync>,
    ) -> TreeScanner {
        TreeScanner {
            fs,
            memo: std::sync::Mutex::new(std::collections::BTreeMap::new()),
            now,
        }
    }

    /// The content id of the file `path` with metadata `meta`: the memo's
    /// when its stat is unchanged and it had settled, else read and hashed.
    fn id_of(&self, path: &std::path::Path, meta: &file_system::Meta) -> std::io::Result<String> {
        let mut memo = self
            .memo
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner);
        if let Some(kept) = memo
            .get(path)
            .filter(|kept| kept.reusable(meta.len, meta.modified))
        {
            return Ok(kept.id.clone());
        }
        let id = library_tree::content_id(&self.fs.read(path)?);
        memo.insert(
            path.to_path_buf(),
            Hashed {
                len: meta.len,
                modified: meta.modified,
                at: (self.now)(),
                id: id.clone(),
            },
        );
        Ok(id)
    }

    /// Whether a link is among the steps of `rel` below `root` (the last
    /// one included): the library is read through no link, as the write side
    /// refuses a path through one (a cloned repository's `x -> /` must not
    /// hash the disk, nor a loop fail every read). `root` itself may be one.
    fn through_link(&self, root: &std::path::Path, rel: &str) -> std::io::Result<bool> {
        let mut here = root.to_path_buf();
        for part in rel.split('/') {
            here.push(part);
            match self.fs.symlink_metadata(&here) {
                Ok(meta) if meta.kind == file_system::Kind::Symlink => return Ok(true),
                Ok(_) => {}
                Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(false),
                Err(e) => return Err(e),
            }
        }
        Ok(false)
    }

    /// Every file under `path` (relative `rel`), into `found`. A link is
    /// skipped, not followed, and not said: the scanner has no notice to
    /// give, and a refusal would let one hostile entry refuse every read.
    fn walk(
        &self,
        path: &std::path::Path,
        rel: &str,
        found: &mut std::collections::BTreeMap<String, String>,
    ) -> std::io::Result<()> {
        let meta = match self.fs.symlink_metadata(path) {
            Ok(meta) => meta,
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(()),
            Err(e) => return Err(e),
        };
        match meta.kind {
            file_system::Kind::Dir => {
                let mut entries = self.fs.read_dir(path)?;
                entries.sort_by(|a, b| a.name.cmp(&b.name));
                for entry in entries {
                    let name = entry.name.to_string_lossy().into_owned();
                    self.walk(&path.join(&name), &format!("{rel}/{name}"), found)?;
                }
                Ok(())
            }
            file_system::Kind::File => {
                found.insert(rel.to_string(), self.id_of(path, &meta)?);
                Ok(())
            }
            _ => Ok(()),
        }
    }
}

/// An I/O error of a scan, as a failure naming the tree.
fn scan_failed(dir: &str, error: &std::io::Error) -> ctxloom_ports::Failure {
    Box::new(crate::LibError::Scan {
        path: dir.to_string(),
        detail: error.to_string(),
    })
}

impl ctxloom_ports::LibraryScanner for TreeScanner {
    fn scan(
        &self,
        dir: &str,
    ) -> Result<std::collections::BTreeMap<String, String>, ctxloom_ports::Failure> {
        let root = std::path::Path::new(dir);
        let mut found = std::collections::BTreeMap::new();
        for path in ctxloom_ports::LIBRARY_PATHS {
            if self
                .through_link(root, path)
                .map_err(|e| scan_failed(dir, &e))?
            {
                continue;
            }
            self.walk(&root.join(path), path, &mut found)
                .map_err(|e| scan_failed(dir, &e))?;
        }
        Ok(found)
    }

    fn content_ids(
        &self,
        dir: &str,
        paths: &[&str],
    ) -> Result<std::collections::BTreeMap<String, String>, ctxloom_ports::Failure> {
        let root = std::path::Path::new(dir);
        let mut found = std::collections::BTreeMap::new();
        for path in paths {
            let at = root.join(path);
            if self
                .through_link(root, path)
                .map_err(|e| scan_failed(dir, &e))?
            {
                continue;
            }
            match self.fs.symlink_metadata(&at) {
                Ok(meta) if meta.kind == file_system::Kind::File => {
                    let id = self.id_of(&at, &meta).map_err(|e| scan_failed(dir, &e))?;
                    found.insert(path.to_string(), id);
                }
                Ok(_) => {}
                Err(e) if e.kind() == std::io::ErrorKind::NotFound => {}
                Err(e) => return Err(scan_failed(dir, &e)),
            }
        }
        Ok(found)
    }

    fn forget(&self) {
        self.memo
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner)
            .clear();
    }
}

#[cfg(test)]
#[path = "scanner.test.rs"]
mod scanner_tests;
