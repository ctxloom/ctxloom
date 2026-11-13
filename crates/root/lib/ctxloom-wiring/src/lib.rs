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
mod daemon;
mod error;
mod interview;
mod isolation;
mod library;
mod mcp;
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
use config_files::{find_app_dir, AppDir, Surroundings};
use ctxloom_store_sqlite::{OpenError, SqliteStores, StoreOpenError, Stores};
use file_system::{Fs, Os};
use harp_names::HarpNames;
use isolation_application::{IsolationError, Report, WorktreeUse, Worktrees};
use liveness::LockFiles;
use ports::{Names, ProjectIdentity};
use project_registry::{Environment, ProjectRegistry};
use session_files::GoDirs;
use sessions_application::{
    AppError, BindRequest, Bound, ImportReport, ImportRequest, ImportService, Importer,
    KeepRequest, Kept, ListQuery, SearchQuery, Service, SessionService, ShowQuery, TranscriptQuery,
    TranscriptRow, View,
};
use system::System;

use crate::library::PathSearch;
use uuid::Uuid;
use worktree_git::GitWorkspaces;

pub use capture::{EdgeSink, HookRecording, HookSink, RunIds, SessionIds, Unreachable};
pub use commands::{engines, Commands, Hooks};
pub use core_link::{CoreStores, InProcessCore, Projectors, Unclosed};
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

/// How many fresh harps a project's naming tries before it gives up (S54).
const PROJECT_HARP_TRIES: usize = 3;

/// Fresh harps a project may be named with, tried in order.
pub(crate) fn fresh_project_harps() -> Vec<String> {
    (0..PROJECT_HARP_TRIES).map(|_| session_name()).collect()
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
    Service<ProjectRegistry<HarpNames>, SqliteStores, LockFiles, System, System>;

/// The import over the same adapters, and Go's session directories.
pub type ImportUseCase = Importer<ProjectRegistry<HarpNames>, SqliteStores, GoDirs, System, System>;

/// Everything the `session` commands and the session hooks call: the sessions
/// use cases and the import, one value to the command line.
pub struct Sessions {
    pub use_cases: SessionUseCases,
    pub importer: ImportUseCase,
    /// The scratch worktrees sessions left behind (S11).
    pub worktrees: Worktrees<GitWorkspaces, LockFiles>,
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
    /// The import, a project it writes to named with fresh harps when it
    /// has none (slice 2 M1).
    async fn import(&mut self, request: ImportRequest) -> Result<ImportReport, AppError> {
        let request = match request.project_harps.is_empty() {
            true => ImportRequest {
                project_harps: fresh_project_harps(),
            },
            false => request,
        };
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
        daemon_api::EN_US,
        daemon_ports::EN_US,
        ctxloom_store_sqlite::OPENER_EN_US,
        session_files::EN_US,
        mcp_server::EN_US,
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
    sources.push(engine_claude::EN_US);
    sources.push(engine_opencode::EN_US);
    sources
}

/// The process's home and filesystem, and the environment it was read from.
pub struct Wiring {
    fs: Arc<dyn Fs>,
    home: PathBuf,
    env: Arc<EnvSnapshot>,
    /// The user's own home ([`Wiring::user_home`]).
    user_home: Option<PathBuf>,
}

impl Wiring {
    /// The wiring over the environment the process was launched in, read
    /// once by the composition root: its home (CTXLOOM_HOME, the variable a
    /// run names ctxloom's home by for its engine, whose own HOME is the
    /// session home (owner ruling 112); failing that HOME, or USERPROFILE on
    /// Windows), and the real filesystem over its working directory (with
    /// none when it cannot be read: only what needs it fails).
    pub fn new(env: Arc<EnvSnapshot>) -> Result<Wiring, LibError> {
        let home = env
            .non_empty(run_ports::HOME_VARIABLE)
            .or_else(|| env.home())
            .map(PathBuf::from)
            .ok_or_else(|| LibError::NoHome {
                variable: env.flavor().home_var().to_string(),
            })?;
        Ok(Wiring::at(env, home))
    }

    /// The wiring over `env` with ctxloom's home `home`, resolved by the
    /// caller (the user daemon's is the account's, never its HOME).
    pub(crate) fn at(env: Arc<EnvSnapshot>, home: PathBuf) -> Wiring {
        let host = os::host();
        let fs = match env.cwd().and_then(|cwd| Os::new(host.clone(), &cwd)) {
            Ok(fs) => fs,
            Err(why) => Os::without_cwd(host, why),
        };
        Wiring {
            fs: Arc::new(fs),
            home,
            user_home: env.home().map(PathBuf::from),
            env,
        }
    }

    /// The wiring, the user's own home `user_home` in place of the one its
    /// environment names (the user daemon's is the account's: it has no
    /// HOME of its own, M4).
    pub(crate) fn with_user_home(self, user_home: Option<PathBuf>) -> Wiring {
        Wiring { user_home, ..self }
    }

    pub(crate) fn fs(&self) -> Arc<dyn Fs> {
        Arc::clone(&self.fs)
    }

    pub(crate) fn home(&self) -> &std::path::Path {
        &self.home
    }

    /// The user's own home: the platform's home variable (`HOME`, and
    /// `USERPROFILE` on Windows, Go's `os.UserHomeDir`), as [`Wiring::new`]
    /// falls back to it; none when unset or empty; the user daemon's is the
    /// account's ([`Wiring::with_user_home`]). The one place her home is
    /// resolved: her login, her claude and opencode settings, and the home a
    /// `session_home: host` engine is given are found under it.
    pub(crate) fn user_home(&self) -> Option<PathBuf> {
        self.user_home.clone()
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
        }
    }

    /// The registry's environment: the home, and the root and project id the
    /// process was given.
    fn environment(&self) -> Environment {
        Environment {
            home: Some(self.home.clone()),
            root: self.env.non_empty("CTXLOOM_ROOT").map(PathBuf::from),
            project_id: self
                .env
                .non_empty("CTXLOOM_PROJECT_ID")
                .and_then(|v| v.to_str().map(String::from)),
        }
    }

    /// Refused where the project this process is in still has a store of its
    /// own from before the one store (T17, S48: per command; nothing is
    /// imported). Nothing is refused outside a project, nor where the project
    /// cannot be told: the command says what is wrong then.
    pub fn refuse_per_project_store(&self) -> Result<(), StoreOpenError> {
        use ports::ProjectIdentity as _;
        let mut identity = registry(Arc::clone(&self.fs), self.environment(), HarpNames);
        let Ok(Some(project)) = identity.for_read(None) else {
            return Ok(());
        };
        Stores::new(Arc::clone(&self.fs), self.home.clone()).refuse_per_project(&project.id)
    }

    /// What this process registers with the daemon (owner, 2026-11-12,
    /// project registration): its project's id (taskloom's identity, so a
    /// worktree is its primary checkout's), its root, the repository's
    /// common dir and root commit (worktree-git's, empty outside git), and
    /// its working directory. `None` outside any project.
    pub(crate) fn registration(&self) -> Option<daemon_proto::v1::ResolveProjectRequest> {
        use ports::ProjectIdentity as _;
        let mut identity = registry(Arc::clone(&self.fs), self.environment(), HarpNames);
        let project = identity.for_write(None).ok()?;
        let root = project
            .root
            .unwrap_or_else(|| self.project_dir().display().to_string());
        let vcs = oneshot::identity_git(self).vcs_identity(std::path::Path::new(&root));
        Some(daemon_proto::v1::ResolveProjectRequest {
            name: String::new(),
            cwd: self
                .env
                .cwd()
                .map(|cwd| cwd.display().to_string())
                .unwrap_or_default(),
            project_id: project.id,
            root,
            git_common_dir: vcs.git_common_dir,
            root_commit: vcs.root_commit,
            env: None,
        })
    }

    /// Name the project this process is in (S54), once, in the one store:
    /// its harp, minted from fresh harps, or the one it has. Every command
    /// that mints a session names its project first, at its resolution
    /// (slice 2 M1: a run, a child's launch, an import, the test seed); the
    /// daemon's `ResolveProject` is a later step's.
    pub fn name_project(&self) -> Result<sessions_application::ProjectHarp, LibError> {
        let runtime = tokio::runtime::Builder::new_current_thread()
            .enable_all()
            .build()
            .map_err(|e: io::Error| LibError::NoRuntime {
                detail: e.to_string(),
            })?;
        runtime.block_on(self.named_project())
    }

    /// [`Wiring::name_project`], on the caller's runtime.
    pub async fn named_project(&self) -> Result<sessions_application::ProjectHarp, LibError> {
        let mut sessions = self.sessions();
        let named = sessions
            .use_cases
            .name_project(sessions_application::NameProjectRequest {
                project: None,
                harps: fresh_project_harps(),
            })
            .await
            .map_err(|e| LibError::Project(angzarr_diagnostic::Report::of(&e)));
        // Closed, not dropped (ruling 131).
        let closed = sessions.close().await;
        let named = named?;
        closed.map_err(|e| LibError::Project(angzarr_diagnostic::Report::of(&e)))?;
        Ok(named)
    }

    /// The one store opened (made, if need be) under the home for `project`
    /// (T14), the path of its database; refused where the project still has
    /// a store of its own (T17).
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
                let store = stores.open_for(project).await?;
                store
                    .close()
                    .await
                    .map_err(|e| StoreOpenError::Open(OpenError::Store(e)))
            })
            .map_err(LibError::Store)?;
        Ok(stores.path())
    }
}

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;

#[cfg(test)]
#[path = "fixtures.test.rs"]
mod fixtures;
