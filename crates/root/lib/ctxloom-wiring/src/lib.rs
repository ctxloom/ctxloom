//! ctxloom's wiring: the core's ports over their default adapters, built
//! once for the binary and the tests.
//!
//! - [`session_name`]: a session's harp, `adjective-adjective-noun`, from
//!   taskloom's `harp-names` (its project-id shape) behind the `Names` port.
//! - [`run_id`]: a run's id, UUIDv5 of its session and incarnation (D5 Q4),
//!   so the same incarnation of the same session is the same run.
//! - [`Wiring`]: the process's home and filesystem, and the project stores
//!   opened over them.

mod capture;
mod commands;
mod core_link;
mod error;
mod interview;
mod isolation;
mod library;
mod oneshot;
mod process_env;
mod program;
#[cfg(feature = "test-support")]
mod seed;
mod sessions;
mod setup;
mod signals;
mod sweep_config;
#[cfg(feature = "test-support")]
mod test_commands;
#[cfg(feature = "test-support")]
mod test_error;

use std::io;
use std::path::PathBuf;
use std::sync::Arc;

use ::process_env::EnvSnapshot;
use angzarr_embedded::StoreError;
use config_files::{find_app_dir, AppDir, ConfigFilesFs, Surroundings};
use ctxloom_store_sqlite::{OpenError, SqliteStores, StoreOpenError, Stores};
use file_system::{Fs, Os};
use harp_names::HarpNames;
use isolation_application::{IsolationError, Report, WorktreeUse, Worktrees};
use liveness::LockFiles;
use ports::{Names, ProjectIdentity};
use project_registry::{Environment, ProjectRegistry};
use reclaim_application::SweepConfig;
use session_files::{GoDirs, SessionDirs};
use sessions_application::{
    AppError, BindRequest, Bound, ImportReport, ImportRequest, ImportService, Importer,
    KeepRequest, Kept, ListQuery, Removal, RemoveRequest, SearchQuery, Service, SessionService,
    ShowQuery, TranscriptQuery, TranscriptRow, View,
};
use system::System;

use crate::library::PathSearch;
use uuid::Uuid;
use worktree_git::GitWorkspaces;

pub use capture::{EdgeSink, HookRecording, HookSink, RunIds, SessionIds, Unreachable};
pub use commands::{engines, Commands, Hooks};
pub use core_link::{CoreStores, InProcessCore, Unclosed};
#[cfg(any(test, feature = "test-support"))]
pub use error::diagnostic_samples;
pub use error::{LibError, EN_US};
pub use library::LibraryCommands;
pub use process_env::ProcessEnv;
#[cfg(feature = "test-support")]
pub use seed::Seed;
pub use sweep_config::SweepSettings;
#[cfg(feature = "test-support")]
pub use test_commands::Samples;
#[cfg(feature = "test-support")]
pub use test_error::TestError;

/// Where ctxloom keeps a project's id marker, under the work root.
///
/// Invariant: it lives under `.ctxloom`, so a ctxloom session writes nothing
/// outside `.ctxloom` and `~/.ctxloom` (leaving is deleting them). A project
/// that already has taskloom's root `.project-id` is read and adopted into
/// this marker; markers that disagree are refused (`marker-conflict`).
pub(crate) const PROJECT_MARKER: &str = ".ctxloom/project-id";

/// The project registry, its marker at [`PROJECT_MARKER`].
pub(crate) fn registry<N>(
    fs: Arc<dyn Fs>,
    environment: Environment,
    names: N,
) -> ProjectRegistry<N> {
    ProjectRegistry::new(fs, environment, names)
        .with_marker(PROJECT_MARKER)
        .expect("PROJECT_MARKER is a relative path of plain names")
}

/// A newly minted session's harp: two adjectives and a noun.
pub fn session_name() -> String {
    HarpNames.project_id()
}

/// The run id of `incarnation` of `session`: UUIDv5, in the session's
/// namespace, of the incarnation's decimal digits.
pub fn run_id(session: &str, incarnation: u64) -> Result<Uuid, LibError> {
    let namespace = Uuid::parse_str(session).map_err(|e| LibError::BadSession {
        session: session.to_string(),
        detail: e.to_string(),
    })?;
    Ok(Uuid::new_v5(&namespace, incarnation.to_string().as_bytes()))
}

/// The sessions use cases over their default adapters: the project registry
/// (taskloom's, so a project is the same one in both programs), the project's
/// SQLite store, the session directories, the lock files, and the system's
/// clock, ids and timer.
pub type SessionUseCases =
    Service<ProjectRegistry<HarpNames>, SqliteStores, SessionDirs, LockFiles, System, System>;

/// The import over the same adapters, and Go's session directories.
pub type ImportUseCase = Importer<ProjectRegistry<HarpNames>, SqliteStores, GoDirs, System, System>;

/// Everything the `session` commands and the session hooks call: the sessions
/// use cases and the import, one value to the command line.
pub struct Sessions {
    pub use_cases: SessionUseCases,
    pub importer: ImportUseCase,
    /// The scratch worktrees sessions left behind (S11).
    pub worktrees: Worktrees<GitWorkspaces, LockFiles>,
    /// `session_purge_age`, for a sweep given no `--purge-older-than`.
    pub settings: SweepSettings,
}

impl Sessions {
    /// Close the stores the use cases hold, each waited for (ruling 131), so
    /// a command exits with its store's log checkpointed into the file:
    /// dropped, a store is closed on the driver's thread whenever that runs,
    /// which a process that exits first never lets it do. Both are closed
    /// whatever the first says; the first failure is answered.
    pub async fn close(&mut self) -> Result<(), StoreError> {
        let used = self.use_cases.stores.close().await;
        let imported = self.importer.stores.close().await;
        used.and(imported)
    }
}

impl SweepConfig for Sessions {
    fn purge_age(&mut self) -> Result<Option<String>, ctxloom_ports::Failure> {
        self.settings.purge_age()
    }
}

impl WorktreeUse for Sessions {
    fn worktrees(&mut self, harp: Option<&str>, apply: bool) -> Result<Report, IsolationError> {
        self.worktrees.worktrees(harp, apply)
    }
}

impl SessionService for Sessions {
    async fn list(&mut self, query: ListQuery) -> Result<Vec<View>, AppError> {
        self.use_cases.list(query).await
    }

    async fn search(&mut self, query: SearchQuery) -> Result<Vec<View>, AppError> {
        self.use_cases.search(query).await
    }

    async fn show(&mut self, query: ShowQuery) -> Result<View, AppError> {
        self.use_cases.show(query).await
    }

    async fn keep(&mut self, request: KeepRequest) -> Result<Kept, AppError> {
        self.use_cases.keep(request).await
    }

    async fn remove(&mut self, request: RemoveRequest) -> Result<Removal, AppError> {
        self.use_cases.remove(request).await
    }

    async fn set_title(
        &mut self,
        request: sessions_application::TitleRequest,
    ) -> Result<sessions_application::Titled, AppError> {
        self.use_cases.set_title(request).await
    }

    async fn transcripts(
        &mut self,
        query: TranscriptQuery,
    ) -> Result<Vec<TranscriptRow>, AppError> {
        self.use_cases.transcripts(query).await
    }

    async fn transcript(&mut self, harp: &str) -> Result<String, AppError> {
        self.use_cases.transcript(harp).await
    }

    async fn bind(&mut self, request: BindRequest) -> Result<Bound, AppError> {
        self.use_cases.bind(request).await
    }
}

impl ImportService for Sessions {
    async fn import(&mut self, request: ImportRequest) -> Result<ImportReport, AppError> {
        self.importer.import(request).await
    }
}

/// Every catalog the binary's diagnostics are rendered from, beside its
/// frontends' own: angzarr's (the kernel's reasons, the store's), the
/// platform's, taskloom's project registry's, and this program's adapters and
/// use cases'.
pub fn catalog_sources() -> Vec<angzarr_presentation::Source> {
    let mut sources = vec![
        EN_US,
        angzarr_embedded::angzarr_proto::REASONS_EN_US,
        angzarr_embedded::EN_US,
        angzarr_embedded::angzarr_ports::EN_US,
        angzarr_embedded::angzarr_store_sqlite::EN_US,
        file_lock::EN_US,
        file_system::EN_US,
        project_registry::EN_US,
        project_dir::EN_US,
        duration_text::EN_US,
        ctxloom_store_sqlite::EN_US,
        session_files::EN_US,
    ];
    sources.extend(sessions_application::CATALOGS);
    sources.extend(isolation_application::CATALOGS);
    sources.extend([worktree_git::EN_US, container_cli::EN_US]);
    sources.push(tasks_ports::EN_US);
    sources.extend([scheduler::EN_US, scheduler_loop::EN_US]);
    sources.extend([
        runs::EN_US,
        runs_application::EN_US,
        run_ports::EN_US,
        trees::EN_US,
        run_application::EN_US,
        run_launcher::EN_US,
        run_cli::EN_US,
    ]);
    sources.extend(hook_listener::CATALOGS);
    sources.push(hook_tls::EN_US);
    sources.push(engine_opencode::EN_US);
    sources
}

/// The process's home and filesystem, and the environment it was read from.
pub struct Wiring {
    fs: Arc<dyn Fs>,
    home: PathBuf,
    env: Arc<EnvSnapshot>,
}

impl Wiring {
    /// The wiring over the environment the process was launched in, read
    /// once by the composition root: its home (CTXLOOM_HOME, the variable a
    /// run names ctxloom's home by for its engine, whose own HOME is the
    /// session home (owner ruling 112); failing that HOME, or USERPROFILE on
    /// Windows), and the real filesystem over its working directory (with
    /// none when it cannot be read: only what needs it fails).
    pub fn new(env: Arc<EnvSnapshot>) -> Result<Wiring, LibError> {
        let host = os::host();
        let home = env
            .non_empty(run_ports::HOME_VARIABLE)
            .or_else(|| env.home())
            .map(PathBuf::from)
            .ok_or_else(|| LibError::NoHome {
                variable: env.flavor().home_var().to_string(),
            })?;
        let fs = match env.cwd().and_then(|cwd| Os::new(host.clone(), &cwd)) {
            Ok(fs) => fs,
            Err(why) => Os::without_cwd(host, why),
        };
        Ok(Wiring {
            fs: Arc::new(fs),
            home,
            env,
        })
    }

    pub(crate) fn fs(&self) -> Arc<dyn Fs> {
        Arc::clone(&self.fs)
    }

    pub(crate) fn home(&self) -> &std::path::Path {
        &self.home
    }

    /// The user's own home: the platform's home variable (`HOME`, and
    /// `USERPROFILE` on Windows, Go's `os.UserHomeDir`), as [`Wiring::new`]
    /// falls back to it; none when unset or empty. The one place her home is
    /// resolved: her login, her claude and opencode settings, and the home a
    /// `session_home: host` engine is given are found under it.
    pub(crate) fn user_home(&self) -> Option<PathBuf> {
        self.env.home().map(PathBuf::from)
    }

    /// The operating system's name the run's platform rules are keyed by
    /// (`std::env::consts::OS`), as the environment's flavor says: `windows`
    /// for a Windows flavor (so a test can give one on any host), the host's
    /// own name otherwise.
    pub(crate) fn os_name(&self) -> &'static str {
        match self.env.flavor() {
            os::Flavor::Windows => run_ports::WINDOWS,
            os::Flavor::Unix => std::env::consts::OS,
        }
    }

    /// The environment the process was launched in.
    pub(crate) fn env(&self) -> &Arc<EnvSnapshot> {
        &self.env
    }

    /// Where the process is: the root variable, the working directory, the
    /// home and the temporary root.
    pub(crate) fn surroundings(&self) -> Surroundings {
        Surroundings {
            root_env: self.env.utf8("CTXLOOM_ROOT").map(String::from),
            cwd: self.env.cwd().unwrap_or_else(|_| PathBuf::from(".")),
            home: self.home.clone(),
            temp_root: self.env.temp_dir().to_path_buf(),
        }
    }

    /// The project directory: the one holding the app dir found (the home
    /// where there is no project). A session's directory is
    /// `<project>/.ctxloom/sessions/<harp>`.
    pub(crate) fn project_dir(&self) -> PathBuf {
        let app = self.locate().1;
        app.path
            .parent()
            .map_or_else(|| self.home.clone(), std::path::Path::to_path_buf)
    }

    /// The surroundings, and the app dir found in them.
    pub(crate) fn locate(&self) -> (Surroundings, AppDir) {
        let around = self.surroundings();
        let app = find_app_dir(self.fs.as_ref(), &around);
        (around, app)
    }

    /// The programs on the process's `PATH`, found through the filesystem.
    pub(crate) fn path_search(&self) -> PathSearch {
        PathSearch {
            fs: Arc::clone(&self.fs),
            dirs: self.env.search_path(),
            flavor: self.env.flavor(),
            pathext: self.env.utf8("PATHEXT").map(String::from),
        }
    }

    /// The sessions use cases over the default adapters, in this process's
    /// home and working directory, reading CTXLOOM_ROOT and CTXLOOM_PROJECT_ID
    /// as the project registry does.
    pub fn sessions(&self) -> Sessions {
        let environment = Environment {
            home: Some(self.home.clone()),
            root: self.env.non_empty("CTXLOOM_ROOT").map(PathBuf::from),
            project_id: self
                .env
                .non_empty("CTXLOOM_PROJECT_ID")
                .and_then(|v| v.to_str().map(String::from)),
        };
        let registry = || registry(Arc::clone(&self.fs), environment.clone(), HarpNames);
        let stores = || SqliteStores::new(Arc::clone(&self.fs), self.home.clone());
        let project = self.project_dir();
        // The project's checkouts are under ctxloom's home, by its id (owner
        // ruling 2026-11-12, Q1); a directory that is no project, or whose id
        // cannot be read, has none.
        let worktrees = registry()
            .for_read(None)
            .ok()
            .flatten()
            .map(|known| isolation_ports::worktrees_root(&self.home, &known.id));
        Sessions {
            use_cases: Service::new(
                registry(),
                stores(),
                SessionDirs::new(Arc::clone(&self.fs), project.clone()),
                LockFiles::new(Arc::clone(&self.fs), project.clone()),
                System,
                System,
            ),
            importer: Importer::new(
                registry(),
                stores(),
                GoDirs::new(Arc::clone(&self.fs), self.home.clone()),
                System,
                System,
            ),
            worktrees: Worktrees::new(
                GitWorkspaces::new(
                    Arc::clone(&self.fs),
                    project.clone(),
                    worktrees,
                    Arc::clone(&self.env),
                ),
                LockFiles::new(Arc::clone(&self.fs), project.clone()),
            ),
            settings: SweepSettings::new(
                Arc::new(ConfigFilesFs::new(
                    Arc::clone(&self.fs),
                    self.locate().1,
                    &self.home,
                )),
                // Overrides are text: a variable that is not UTF-8 is none.
                self.env.vars_utf8().into_iter().collect(),
            ),
        }
    }

    /// `project`'s store opened (made, if need be) under the home; the path
    /// of its database.
    pub fn open_store(&self, project: &str) -> Result<PathBuf, LibError> {
        let stores = Stores::new(Arc::clone(&self.fs), self.home.clone());
        let runtime = tokio::runtime::Builder::new_current_thread()
            .enable_all()
            .build()
            .map_err(|e: io::Error| LibError::NoRuntime {
                detail: e.to_string(),
            })?;
        // Closed, not dropped (ruling 131): the file holds the open's
        // migrations, with no log beside it, when this returns.
        runtime
            .block_on(async {
                let store = stores.open(project).await?;
                store
                    .close()
                    .await
                    .map_err(|e| StoreOpenError::Open(OpenError::Store(e)))
            })
            .map_err(LibError::Store)?;
        stores.path(project).map_err(LibError::Store)
    }
}

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;

#[cfg(test)]
#[path = "fixtures.test.rs"]
mod fixtures;
