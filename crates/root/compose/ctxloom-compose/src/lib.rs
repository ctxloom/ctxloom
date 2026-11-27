//! ctxloom's wiring: the core's ports over their default adapters, built
//! once for the binary and the tests.
//!
//! - [`session_name`]: a session's harp, `adjective-adjective-noun`, from
//!   taskloom's `harp-names` (its project-id shape) behind the `Names` port.
//! - [`run_id`]: a run's id, UUIDv5 of its session and incarnation (D5 Q4),
//!   so the same incarnation of the same session is the same run.
//! - [`Wiring`]: the process's home and filesystem, and the project stores
//!   opened over them.

pub mod capture;
pub mod checkout;
pub mod compose;
pub mod daemon;
pub mod error;
pub mod frontend;
pub mod frontend_asks;
pub mod hook_asks;
pub mod hook_library;
pub mod hook_messages;
pub mod library_build;
pub mod oneshot;
pub mod process_env;
pub mod program;
pub mod rebind;
pub mod root_flag;
pub mod setup;
pub mod signals;
#[cfg(feature = "test-support")]
pub mod store_verb;
pub mod sweep_config;
#[cfg(feature = "test-support")]
pub mod test_error;

use std::path::PathBuf;
use std::sync::Arc;

use ::process_env::EnvSnapshot;
use config_files::{find_app_dir, AppDir, Surroundings};
use file_system::{Fs, Os};
use harp_names::HarpNames;
use isolation_application::{IsolationError, Report, WorktreeUse, Worktrees};
use library_build::PathSearch;
use liveness::LockFiles;
use ports::{Names, ProjectIdentity};
use project_registry::{Environment, ProjectRegistry};
use worktree_git::GitWorkspaces;

use uuid::Uuid;

pub use capture::{
    daemon_sink, Deadline, EdgeSink, HoldingRecording, HookReach, HookRecording, HookSink, RunIds,
    SessionIds, Unreachable,
};
pub use checkout::{checkout_file, checkout_harp, checkout_tree, mint_checkout_harp};
pub use compose::engines;
#[cfg(any(test, feature = "test-support"))]
pub use error::diagnostic_samples;
pub use error::{LibError, EN_US};
pub use process_env::ProcessEnv;
#[cfg(feature = "test-support")]
pub use store_verb::{SeedArgs, StoreTest, StoreVerb};
pub use sweep_config::SweepSettings;
#[cfg(feature = "test-support")]
pub use test_error::TestError;

/// Where ctxloom keeps a project's id marker, under the work root.
///
/// Invariant: it lives under `.ctxloom`, so a ctxloom session writes nothing
/// outside `.ctxloom` and `~/.ctxloom` (leaving is deleting them). A project
/// that already has taskloom's root `.project-id` is read and adopted into
/// this marker; markers that disagree are refused (`marker-conflict`).
pub const PROJECT_MARKER: &str = ".ctxloom/project-id";

/// The project registry, its marker at [`PROJECT_MARKER`].
pub fn registry<N>(fs: Arc<dyn Fs>, environment: Environment, names: N) -> ProjectRegistry<N> {
    ProjectRegistry::new(fs, environment, names)
        .with_marker(PROJECT_MARKER)
        .expect("PROJECT_MARKER is a relative path of plain names")
}

/// The names a first init's project is minted with (step 10c U6): its id
/// the one the daemon named it by, every other name harp-names'.
struct Minted(String);

impl ports::Names for Minted {
    fn task_id(&mut self) -> String {
        HarpNames.task_id()
    }

    fn task_id_for(&mut self, request_id: &str, position: u64) -> String {
        HarpNames.task_id_for(request_id, position)
    }

    fn project_id(&mut self) -> String {
        self.0.clone()
    }
}

/// How many fresh harps a project's naming tries before it gives up (S54).
const PROJECT_HARP_TRIES: usize = 3;

/// Fresh harps a project may be named with, tried in order.
pub fn fresh_project_harps() -> Vec<String> {
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

/// What the `session` commands run in this process: the scratch worktrees
/// sessions left behind (S11). Everything else a session command does is the
/// daemon's, which this process reaches as a client.
pub struct Sessions {
    /// The scratch worktrees sessions left behind (S11).
    pub worktrees: Worktrees<GitWorkspaces, LockFiles>,
}

impl WorktreeUse for Sessions {
    fn worktrees(&mut self, harp: Option<&str>, apply: bool) -> Result<Report, IsolationError> {
        self.worktrees.worktrees(harp, apply)
    }
}

/// Every catalog the binary's diagnostics are rendered from, beside its
/// frontends' own: angzarr's (the kernel's reasons, the store's), the
/// platform's, taskloom's project registry's, and this program's adapters and
/// use cases'.
pub fn catalog_sources() -> Vec<presentation::Source> {
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
        daemon_api_contract::EN_US,
        daemon_api_client::EN_US,
        daemon_ports::EN_US,
        session_files::EN_US,
    ];
    sources.extend(sessions_application::CATALOGS);
    sources.extend(isolation_application::CATALOGS);
    sources.push(worktree_git::EN_US);
    sources.extend([library_tree::EN_US, library_api::EN_US]);
    sources.push(tasks_ports::EN_US);
    sources.extend([scheduler::EN_US, scheduler_loop::EN_US]);
    sources.extend([
        runs::EN_US,
        runs_application::EN_US,
        run_ports::EN_US,
        trust::EN_US,
        run_application::EN_US,
        run_launcher::EN_US,
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

/// Whether a command that names its project may mint the project's id
/// (owner ruling 2026-11-14): at its resolved root, or never (a dry run's:
/// "dry run should *not* mint"), so a dry run cannot reach the minting
/// branch.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Minting {
    AtRoot,
    Never,
}

impl Wiring {
    /// The wiring over the environment the process was launched in, read
    /// once by the composition root: its home (CTXLOOM_DAEMON_ROOT, the variable a
    /// run names ctxloom's home by for its engine, whose own HOME is the
    /// session home (owner ruling 112); failing that HOME, or USERPROFILE on
    /// Windows), and the real filesystem over its working directory (with
    /// none when it cannot be read: only what needs it fails).
    pub fn new(env: Arc<EnvSnapshot>) -> Result<Wiring, LibError> {
        let home = env
            .non_empty(run_ports::DAEMON_ROOT_VARIABLE)
            .or_else(|| env.home())
            .map(PathBuf::from)
            .ok_or_else(|| LibError::NoHome {
                variable: env.flavor().home_var().to_string(),
            })?;
        Ok(Wiring::at(env, home))
    }

    /// The wiring over `env` with ctxloom's home `home`, resolved by the
    /// caller (the user daemon's is the account's, never its HOME).
    pub fn at(env: Arc<EnvSnapshot>, home: PathBuf) -> Wiring {
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
    pub fn with_user_home(self, user_home: Option<PathBuf>) -> Wiring {
        Wiring { user_home, ..self }
    }

    pub fn fs(&self) -> Arc<dyn Fs> {
        Arc::clone(&self.fs)
    }

    pub fn home(&self) -> &std::path::Path {
        &self.home
    }

    /// The user's own home: the platform's home variable (`HOME`, and
    /// `USERPROFILE` on Windows, Go's `os.UserHomeDir`), as [`Wiring::new`]
    /// falls back to it; none when unset or empty; the user daemon's is the
    /// account's ([`Wiring::with_user_home`]). The one place her home is
    /// resolved: her login, her claude and opencode settings, and the home a
    /// `session_home: host` engine is given are found under it.
    pub fn user_home(&self) -> Option<PathBuf> {
        self.user_home.clone()
    }

    /// The operating system's name the run's platform rules are keyed by
    /// (`std::env::consts::OS`), as the environment's flavor says: `windows`
    /// for a Windows flavor (so a test can give one on any host), the host's
    /// own name otherwise.
    pub fn os_name(&self) -> &'static str {
        match self.env.flavor() {
            os::Flavor::Windows => run_ports::WINDOWS,
            os::Flavor::Unix => std::env::consts::OS,
        }
    }

    /// The environment the process was launched in.
    pub fn env(&self) -> &Arc<EnvSnapshot> {
        &self.env
    }

    /// Where the process is: the root variable, the working directory, the
    /// home and the temporary root.
    pub fn surroundings(&self) -> Surroundings {
        Surroundings {
            root_env: self.env.utf8("CTXLOOM_ROOT").map(String::from),
            cwd: self.env.cwd().unwrap_or_else(|_| PathBuf::from(".")),
            home: self.home.clone(),
            user_home: self.user_home.clone(),
            temp_root: self.env.temp_dir().to_path_buf(),
        }
    }

    /// The project directory: the one holding the app dir found (the home
    /// where there is no project). A session's directory is
    /// `<project>/.ctxloom/sessions/<harp>`.
    pub fn project_dir(&self) -> PathBuf {
        let app = self.locate().1;
        app.path
            .parent()
            .map_or_else(|| self.home.clone(), std::path::Path::to_path_buf)
    }

    /// The surroundings, and the app dir found in them.
    pub fn locate(&self) -> (Surroundings, AppDir) {
        let around = self.surroundings();
        let app = find_app_dir(self.fs.as_ref(), &around);
        (around, app)
    }

    /// The programs on the process's `PATH`, found through the filesystem.
    pub fn path_search(&self) -> PathSearch {
        PathSearch {
            fs: Arc::clone(&self.fs),
            dirs: self.env.search_path(),
            flavor: self.env.flavor(),
            pathext: self.env.utf8("PATHEXT").map(String::from),
        }
    }

    /// The `session` commands this process runs itself over the default
    /// adapters, for the project [`Wiring::environment`] resolves.
    pub fn sessions(&self) -> Sessions {
        let environment = self.environment();
        let registry = || registry(Arc::clone(&self.fs), environment.clone(), HarpNames);
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

    /// The ctxloom project this process is in (owner ruling 2026-11-13, one
    /// project resolver): the directory holding the nearest `.ctxloom` above
    /// the working directory, or the one `CTXLOOM_ROOT` names, as every
    /// library read finds it ([`Wiring::locate`]). None where there is none
    /// but ctxloom's own home's.
    pub fn ctxloom_project(&self) -> Option<PathBuf> {
        let (_, app) = self.locate();
        if !matches!(app.source, ctxloom_ports::Source::Project) {
            return None;
        }
        let at = app.path.parent()?.to_path_buf();
        (at != self.home).then_some(at)
    }

    /// Refused where there is no project root to work in (a run's need):
    /// no `CTXLOOM_ROOT` or `--root`, no `.ctxloom` of a project found by
    /// the walk, and no git root above the working directory. The user's
    /// home, and the daemon root, are never taken for one.
    pub fn require_project_root(&self) -> Result<(), LibError> {
        let cwd = self.env.cwd().unwrap_or_else(|_| PathBuf::from("."));
        let named = self.env.non_empty("CTXLOOM_ROOT").is_some();
        if named
            || self.ctxloom_project().is_some()
            || config_files::git_root_above(self.fs.as_ref(), &cwd)
        {
            return Ok(());
        }
        Err(LibError::ProjectRootNeeded {
            cwd: cwd.display().to_string(),
        })
    }

    /// The registry's environment, the ONE project resolution every command
    /// shares (owner ruling 2026-11-13): runs, sessions and library reads
    /// belong to the project `CTXLOOM_ROOT` names, else to the nearest
    /// `.ctxloom` up ([`Wiring::ctxloom_project`]); only where there is
    /// neither is the root the registry's own (the git top, else the working
    /// directory). The home, and the project id the process was given.
    pub fn environment(&self) -> Environment {
        Environment {
            home: Some(self.home.clone()),
            root: self
                .env
                .non_empty("CTXLOOM_ROOT")
                .map(PathBuf::from)
                .or_else(|| self.ctxloom_project()),
            project_id: self
                .env
                .non_empty("CTXLOOM_PROJECT_ID")
                .and_then(|v| v.to_str().map(String::from)),
        }
    }

    /// The project this process is in, its identity READ, never minted. None
    /// where no project is bound and none is there to name (no `.ctxloom` but
    /// the home's, no `CTXLOOM_ROOT`); `ctxloom-compose-project-unnamed` where
    /// a ctxloom project is there and nothing has named it yet, which the
    /// session clients read as a project where nothing is recorded and a dry
    /// run's names refuse (a dry run never mints, owner 2026-11-14). A
    /// command that must name its project mints instead
    /// ([`Wiring::naming_registration`]).
    pub fn project(&self) -> Result<Option<ports::Project>, LibError> {
        use ports::ProjectIdentity as _;
        let mut identity = registry(Arc::clone(&self.fs), self.environment(), HarpNames);
        match identity.for_read(None).map_err(LibError::Project)? {
            Some(project) => Ok(Some(project)),
            None => match self.ctxloom_project() {
                Some(root) => Err(LibError::ProjectUnnamed {
                    root: root.display().to_string(),
                }),
                None => Ok(None),
            },
        }
    }

    /// The project this process is in, its identity minted at its resolved
    /// root (the nearest `.ctxloom`'s, never a subfolder) when none is bound
    /// yet: a run's, a bare `init`'s, and a command's that must name its
    /// project (owner ruling 2026-11-14).
    /// The project the daemon named `project_id` for a first init (step 10c
    /// U6) bound at its root: taskloom's registry mints it as that id (its
    /// index and its marker), as a first naming would have minted one.
    ///
    /// A root the home's index binds to ANOTHER project (a repository deleted
    /// and made again at its place: its `.ctxloom` is gone, so nothing else
    /// names the old project) is re-bound to `project_id`: the old binding
    /// is taken out first (owner ruling 2026-11-19, `rebind`).
    pub fn bind_project(&self, project_id: &str) -> Result<ports::Project, LibError> {
        use ports::ProjectIdentity as _;
        if !project_id.is_empty() {
            let identity = registry(Arc::clone(&self.fs), self.environment(), HarpNames);
            let root = identity
                .identity_root()
                .map_err(|e| LibError::Project(coded_diagnostic::Report::of(&e)))?;
            rebind::unbind_stale(&*self.fs, &self.home, &root, project_id)
                .map_err(LibError::Project)?;
        }
        registry(
            Arc::clone(&self.fs),
            self.environment(),
            Minted(project_id.to_string()),
        )
        .for_write(None)
        .map_err(LibError::Project)
    }

    pub fn mint_project(&self) -> Result<ports::Project, LibError> {
        use ports::ProjectIdentity as _;
        registry(Arc::clone(&self.fs), self.environment(), HarpNames)
            .for_write(None)
            .map_err(LibError::Project)
    }

    /// What this process registers with the daemon (owner, 2026-11-12,
    /// project registration): its project's id (taskloom's identity, so a
    /// worktree is its primary checkout's), its root, the repository's
    /// common dir and root commit (worktree-git's, empty outside git), and
    /// its working directory. The project is [`Wiring::project`]'s: read,
    /// never minted, so `None` where none is bound and there is no ctxloom
    /// project, and `ctxloom-compose-project-unnamed` where a ctxloom project
    /// is there that nothing has named.
    pub fn registration(
        &self,
    ) -> Result<Option<daemon_proto::v1::ResolveProjectRequest>, LibError> {
        Ok(self.project()?.map(|project| self.registering(project)))
    }

    /// The registration of a command that must NAME its project (owner
    /// ruling 2026-11-14, since `.ctxloom/project-id` is gitignored and every
    /// fresh clone has none): [`Wiring::registration`]'s, but a ctxloom
    /// project nothing has named has its id minted at its resolved root
    /// first ([`Wiring::mint_project`]).
    pub fn naming_registration(
        &self,
    ) -> Result<Option<daemon_proto::v1::ResolveProjectRequest>, LibError> {
        match self.registration() {
            Err(LibError::ProjectUnnamed { .. }) => {
                Ok(Some(self.registering(self.mint_project()?)))
            }
            read => read,
        }
    }

    /// What a library or deps json answer registers, to name its resources
    /// (OD-40), only inside a ctxloom project: where there is none but the
    /// home's the parent is `users/me`, no daemon is asked and nothing is
    /// written (OQ-3). A subdirectory names the project its root names
    /// (OQ-2). `minting` says whether a project nothing has named gets its
    /// id (at its root, [`Wiring::naming_registration`]) or is refused
    /// (`Minting::Never`, a dry run's: owner, "dry run should *not* mint").
    pub fn library_registration(
        &self,
        minting: Minting,
    ) -> Result<Option<daemon_proto::v1::ResolveProjectRequest>, LibError> {
        if self.ctxloom_project().is_none() {
            return Ok(None);
        }
        match minting {
            Minting::AtRoot => self.naming_registration(),
            Minting::Never => self.registration(),
        }
    }

    /// The registration of `project`, found from this process.
    fn registering(&self, project: ports::Project) -> daemon_proto::v1::ResolveProjectRequest {
        let root = project
            .root
            .unwrap_or_else(|| self.project_dir().display().to_string());
        let vcs = oneshot::identity_git(self).vcs_identity(std::path::Path::new(&root));
        daemon_proto::v1::ResolveProjectRequest {
            name: String::new(),
            project_id: project.id,
            root,
            git_common_dir: vcs.git_common_dir,
            root_commit: vcs.root_commit,
            // Its client's, filled as every request's is.
            caller: None,
        }
    }
}

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;

#[cfg(any(test, feature = "test-support"))]
#[path = "fixtures.test.rs"]
pub mod fixtures;
