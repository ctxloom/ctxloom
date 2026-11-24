//! `run` wired (Plan step 1b-ii): the run use cases over the default
//! adapters. A ONE-SHOT is the user's daemon's (U4, T7): the command is its
//! client ([`client_one_shot`]), and the daemon hosts it
//! ([`HostedOneShots`]: `Runs::one_shot` over the daemon's one store, in
//! the wiring of its client's environment and directory, [`hosted_wiring`]).
//! The interactive session is the daemon's too (U5): the daemon prepares it
//! and this command, its frontend, starts it (`frontend`). No run serves a
//! listener of its own (slice 4 U7, owner ruling s4-Q3): its engine's hooks
//! find the daemon by its discovery file and present the user bearer.

use std::collections::BTreeMap;
use std::path::Path;
use std::sync::Arc;
use std::time::Duration;

use crate::CoreStores;
use angzarr_embedded::angzarr_store_sqlite::SqliteStore;
use angzarr_embedded::Catalog;
use coded_diagnostic::Report;
use daemon_ports::Daemons;
use embedded_edge::EmbeddedEdge;
use file_system::Fs;
use harp_names::HarpNames;
use host_launcher::HostLauncher;
use library_application::{Flags, Library, OUTPUT_DIR_DEFAULT};
use ports::{Names, Project, ProjectIdentity};
use process_env::EnvSnapshot;
use project_registry::Environment;
use run_application::{
    Answer, Core, Done, Ended, OneShot, Parts, Place, RunError, RunService, Runs,
};
use run_launcher::{Launcher, Orders};
use run_ports::{Engine, Limits};
use runs_application::{AppError as CoreError, Enqueuer};
use session_files::SessionDirs;

use crate::signals;
use crate::{InProcessCore, Wiring};

/// How long one turn has before its command, and everything it started, is
/// killed.
pub(crate) const TURN_DEADLINE: Duration = Duration::from_secs(30 * 60);

impl Core for InProcessCore<SqliteStore> {
    type Edge = EmbeddedEdge<CoreStores<SqliteStore>>;
    type Store = SqliteStore;

    fn edge(&self) -> Arc<Self::Edge> {
        InProcessCore::edge(self)
    }

    fn store(&self) -> &SqliteStore {
        InProcessCore::store(self)
    }

    fn catalog(&self) -> &Catalog {
        InProcessCore::catalog(self)
    }
}

impl Core for crate::SharedCore<SqliteStore> {
    type Edge = EmbeddedEdge<CoreStores<SqliteStore>>;
    type Store = SqliteStore;

    fn edge(&self) -> Arc<Self::Edge> {
        crate::SharedCore::edge(self)
    }

    fn store(&self) -> &SqliteStore {
        crate::SharedCore::store(self)
    }

    fn catalog(&self) -> &Catalog {
        crate::SharedCore::catalog(self)
    }
}

/// The project, already told: the identity the use case asks again.
struct Told(Project);

impl ProjectIdentity for Told {
    fn for_write(&mut self, _: Option<&str>) -> Result<Project, coded_diagnostic::Report> {
        Ok(self.0.clone())
    }

    fn for_read(&mut self, _: Option<&str>) -> Result<Option<Project>, coded_diagnostic::Report> {
        Ok(Some(self.0.clone()))
    }

    fn every_project(&mut self) -> Result<Vec<Project>, coded_diagnostic::Report> {
        Ok(vec![self.0.clone()])
    }

    fn rootless_directory(&mut self) -> Result<Option<String>, coded_diagnostic::Report> {
        Ok(None)
    }
}

/// The runs as this command asks them of the user's daemon: a one-shot as
/// its client (U4), a session as its frontend (U5). The daemon plans each
/// (its dependencies pulled when `sync.on_run` asks) and records it.
pub(crate) struct OneShots<'a> {
    wiring: &'a Wiring,
    /// This binary's stamp, which the daemon that hosts a one-shot must
    /// share (M3).
    stamp: String,
}

impl<'a> OneShots<'a> {
    pub(crate) fn new(wiring: &'a Wiring) -> OneShots<'a> {
        OneShots {
            wiring,
            stamp: String::new(),
        }
    }

    /// The same, a one-shot hosted by a daemon of the stamp `stamp` (this
    /// binary's, M3).
    pub(crate) fn stamped(mut self, stamp: &str) -> OneShots<'a> {
        self.stamp = stamp.to_string();
        self
    }
}

/// A run's session that could not be made, for `cause`.
fn unmade(cause: &dyn std::fmt::Display) -> RunError {
    RunError::Session(CoreError::Failed {
        reason: cause.to_string(),
    })
}

/// How a run's engine is started and its session's lock held: by this
/// host (a hosted one-shot's, the daemon's own), or by the frontend that
/// asked (a terminal session's, U5: its runner hands the launch over, and
/// the frontend takes the lock).
pub(crate) struct Driven<L> {
    pub(crate) runner: L,
    pub(crate) locks: Box<dyn session_ports::SessionLock>,
}

impl Driven<HostLauncher> {
    /// The engine started on this host, stopped by `stop`, each program
    /// found on its own PATH by the host's rules (PATHEXT on Windows); the
    /// session's lock taken here.
    pub(crate) fn here(wiring: &Wiring, stop: host_launcher::Stop) -> Driven<HostLauncher> {
        Driven {
            runner: HostLauncher::stopped_by(stop).searching(wiring.fs()),
            locks: Box::new(liveness::LockFiles::new(wiring.fs(), wiring.project_dir())),
        }
    }
}

/// The run use cases over `core`, for the run `wiring` is in: the project
/// `project` (named `project_harp`), its engine started and its session's
/// lock held as `driven` says, its callbacks running `program` (the
/// client's, owner 2026-11-18) and its engine's `PATH` led by its directory.
pub(crate) fn runs_over<K: Core, L: run_ports::RunnerLauncher>(
    wiring: &Wiring,
    library: Library,
    output_dir: &str,
    driven: Driven<L>,
    core: K,
    (project, project_harp): (Project, String),
    program: &crate::program::Program,
) -> Runs<K, L, SessionDirs>
where
    K::Edge: 'static,
{
    let Driven { runner, locks } = driven;
    let project_dir = wiring.project_dir();
    let orders = Arc::new(Orders::new());
    // The engines' callbacks run the client's binary, by its file name, and
    // its directory leads their PATH (owner rulings 163, 2026-11-18).
    let LaunchEngines { claude, opencode } = launch_engines(wiring, &program.name);
    // The run locks: this run's own, and those another process's runs
    // are judged by (rulings 86, 92, 93).
    let run_locks: Arc<dyn run_ports::RunLocks> =
        Arc::new(liveness::RunLockFiles::new(wiring.fs(), wiring.home()));
    // The project's checkouts, under ctxloom's home (owner ruling
    // 2026-11-12, Q1), which the launcher makes a worktree run's in.
    let worktrees = isolation_ports::worktrees_root(wiring.home(), &project.id);
    let workspaces = Arc::new(worktree_git::GitWorkspaces::new(
        wiring.fs(),
        project_dir.clone(),
        Some(worktrees.clone()),
        Arc::clone(wiring.env()),
    ));
    let launcher = Launcher::new(
        core.edge(),
        runner,
        SessionDirs::new(wiring.fs(), project_dir.clone()),
        vec![claude as Arc<dyn Engine>, opencode as Arc<dyn Engine>],
        Arc::clone(&orders),
        Limits::new(TURN_DEADLINE),
    );
    let launcher = equipped(launcher, (&run_locks, workspaces, wiring.os_name()));
    let enqueuer = Enqueuer::new(core.edge());
    let output_root = output_root(&project_dir, output_dir);
    Runs::new(Parts {
        core,
        planner: Box::new(library),
        identity: Box::new(Told(project)),
        names: Box::new(HarpNames),
        enqueuer,
        launcher,
        dirs: Box::new(SessionDirs::new(wiring.fs(), project_dir.clone())),
        orders,
        place: Place {
            project_dir: path_text(&project_dir),
            project_harp,
            output_root: path_text(&output_root),
            program_dir: program.dir.clone(),
            user_home: wiring.user_home().map(|home| path_text(&home)),
            worktrees: path_text(&worktrees),
        },
        env: run_env(wiring.env(), wiring.home()),
        links: Box::new(SessionDirs::new(wiring.fs(), project_dir.clone())),
        os: wiring.os_name().to_string(),
        locks,
        run_locks,
        git_user: git_user(wiring, &project_dir),
    })
}

/// The launcher as every run has it: the run locks other processes' runs are
/// judged by, the checkouts' port, the turn's wait PARKED on a blocking
/// thread, never an async worker (owner, 2026-11-19), and each command
/// measured against this platform's command line before it is spawned (a
/// terminal session's opening prompt is on it).
pub(crate) fn equipped<E, L, R>(
    launcher: Launcher<E, L, R>,
    (run_locks, workspaces, os): (
        &Arc<dyn run_ports::RunLocks>,
        Arc<dyn isolation_ports::Workspaces + Send + Sync>,
        &str,
    ),
) -> Launcher<E, L, R>
where
    E: run_ports::Edge,
    L: run_ports::RunnerLauncher,
    R: run_ports::SessionRecords,
{
    launcher
        .with_locks(Arc::clone(run_locks))
        .with_workspaces(workspaces)
        .with_parks(Arc::new(crate::daemon::runtime::BlockingParks))
        .on_os(os)
}

/// The engines a launch is driven on, as the composition root builds them
/// for EVERY launch (a CLI run's and a daemon's hosted children alike, the
/// one session-setup path): their callbacks run `program` (owner ruling
/// 163), claude's settings start from hers (ruling 169), opencode seeds her
/// recent models (ruling 167) and starts from her config (169).
pub(crate) struct LaunchEngines {
    pub(crate) claude: Arc<engine_claude::Claude>,
    pub(crate) opencode: Arc<engine_opencode::Opencode>,
}

/// [`LaunchEngines`] over `wiring`'s user, calling `program`.
pub(crate) fn launch_engines(wiring: &Wiring, program: &str) -> LaunchEngines {
    LaunchEngines {
        claude: Arc::new(
            engine_claude::Claude::calling(program)
                .over_user_settings(claude_user_settings(wiring)),
        ),
        opencode: Arc::new(
            engine_opencode::Opencode::calling(program)
                .seeding_model(opencode_recent_models(wiring))
                .over_user_config(opencode_user_config(wiring)),
        ),
    }
}

/// How long the git identity is asked for: a git that hangs delays the run's
/// start by no more than this.
const GIT_USER_DEADLINE: std::time::Duration = std::time::Duration::from_secs(5);

/// The git that is asked the user's identity: in the caller's own
/// environment (her `HOME`, never ctxloom's home), under
/// [`GIT_USER_DEADLINE`].
pub(crate) fn identity_git(wiring: &Wiring) -> worktree_git::Git {
    worktree_git::Git::new(
        wiring.fs(),
        "git",
        GIT_USER_DEADLINE,
        Arc::clone(wiring.env()),
    )
}

/// The user's git identity in the project (owner ruling 112), asked of git in
/// the caller's own environment; none when git knows neither part.
pub(crate) fn git_user(wiring: &Wiring, project_dir: &Path) -> Option<run_ports::GitUser> {
    let user = identity_git(wiring).user(project_dir);
    (!user.name.is_empty() || !user.email.is_empty()).then_some(run_ports::GitUser {
        name: user.name,
        email: user.email,
    })
}

/// The user's own claude `settings.json` (owner ruling 169), which a session
/// home's settings start from: in claude's config dir as claude finds it,
/// `$CLAUDE_CONFIG_DIR` when set, else `~/.claude` (her home as
/// [`Wiring::user_home`] resolves it, `USERPROFILE` on Windows). No home, or
/// no file, is none; a file there that does not read is told unread.
pub(crate) fn claude_user_settings(wiring: &Wiring) -> Option<run_ports::UserSettings> {
    let dir = match wiring.env().non_empty("CLAUDE_CONFIG_DIR") {
        Some(dir) => std::path::PathBuf::from(dir),
        None => wiring.user_home()?.join(".claude"),
    };
    user_settings(wiring, &dir.join("settings.json"))
}

/// The user's own global opencode config (owner ruling 169, by analogy with
/// claude's): where opencode reads it ([`engine_opencode::user_config_file`]),
/// in the caller's environment, under her home as [`Wiring::user_home`]
/// resolves it. No home, or no file, is none.
pub(crate) fn opencode_user_config(wiring: &Wiring) -> Option<run_ports::UserSettings> {
    let config = wiring
        .env()
        .non_empty("XDG_CONFIG_HOME")
        .and_then(|v| v.to_str());
    let home = wiring.user_home();
    let home = home.as_deref().and_then(Path::to_str);
    let file = engine_opencode::user_config_file(home, config, wiring.os_name())?;
    user_settings(wiring, Path::new(&file))
}

/// The file at `path` as an engine is told it: none when nothing is there,
/// its bytes when it reads, and unread otherwise.
fn user_settings(wiring: &Wiring, path: &Path) -> Option<run_ports::UserSettings> {
    let bytes = match wiring.fs().read(path) {
        Ok(bytes) => Some(bytes),
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return None,
        Err(_) => None,
    };
    Some(run_ports::UserSettings {
        path: path_text(path),
        bytes,
    })
}

/// The user's own opencode `model.json`, as read (owner ruling 167): where
/// opencode keeps it ([`engine_opencode::user_model_file`]), in the caller's
/// environment, under her home as [`Wiring::user_home`] resolves it
/// (`USERPROFILE` on Windows). No home, no file, or one that does not read is
/// none, said nothing of: opencode runs without it, as on a machine where it
/// never ran.
pub(crate) fn opencode_recent_models(wiring: &Wiring) -> Option<Vec<u8>> {
    let env = wiring.env();
    let state = env.non_empty("XDG_STATE_HOME").and_then(|v| v.to_str());
    let home = wiring.user_home();
    let home = home.as_deref().and_then(Path::to_str);
    let file = engine_opencode::user_model_file(home, state, wiring.os_name())?;
    wiring.fs().read(Path::new(&file)).ok()
}

/// The environment a run is given: the caller's, and ctxloom's home named by
/// [`run_ports::DAEMON_ROOT_VARIABLE`], so an engine that is given it (the
/// interactive one) can pass it on to its hook processes, which run without
/// `HOME`. Each name is keyed as the platform compares names
/// (`os::Flavor::env_key`: as it is on unix, uppercase on Windows, where
/// `Path` and `PATH` are one variable), so the run's `PATH`, the engines'
/// allowlists, the names ctxloom owns and the permits, all written in
/// uppercase, find the caller's variable whatever its spelling. The spelling
/// is not kept: on Windows it means nothing to the engine, whose environment
/// is looked up as case-insensitively as it was set.
fn run_env(env: &EnvSnapshot, home: &Path) -> BTreeMap<String, String> {
    let flavor = env.flavor();
    let mut vars: BTreeMap<String, String> = env
        .vars_utf8()
        .into_iter()
        .map(|(name, value)| (flavor.env_key(&name), value))
        .collect();
    vars.insert(
        run_ports::DAEMON_ROOT_VARIABLE.to_string(),
        home.to_string_lossy().into_owned(),
    );
    vars
}

/// The project the process is in: the one the registry already holds, else
/// a new one named by `names` and marked in the project's `.ctxloom`
/// ([`crate::PROJECT_MARKER`]), so a run writes nothing outside it.
fn tell_project<N: Names>(
    fs: Arc<dyn Fs>,
    environment: Environment,
    names: N,
) -> Result<Project, Report> {
    crate::registry(fs, environment, names).for_write(None)
}

/// The root of the session output folders (owner rulings 89, 106):
/// `output.dir` as `written`, against the project root unless absolute; each
/// session's folder is `<root>/<harp>`. A trailing slash says nothing more.
pub(crate) fn output_root(project_dir: &Path, written: &str) -> std::path::PathBuf {
    project_dir.join(
        Path::new(written)
            .components()
            .collect::<std::path::PathBuf>(),
    )
}

/// What a run does with the dependencies at its start (`sync.on_run`, owner
/// rulings 59, 60): with `missing` it `pull`s what the profiles reference and
/// is not installed; with `none` nothing. A pull that fails (offline, a
/// remote unreachable) is said and the run goes on with what is installed:
/// the plan judges what is still missing. What it installed is said; nothing
/// installed, nothing said.
pub(crate) fn sync_at_start(
    on_run: library_application::OnRun,
    pull: impl FnOnce() -> Result<u64, String>,
) -> Vec<library_application::Notice> {
    use library_application::{Notice, OnRun};
    if on_run == OnRun::None {
        return Vec::new();
    }
    match pull() {
        Ok(0) => Vec::new(),
        Ok(installed) => vec![Notice::text(format!(
            "installed {installed} missing dependencies at the start of the run (sync.on_run: missing)"
        ))],
        Err(why) => vec![Notice::Finding {
            text: format!(
                "could not install the missing dependencies at the start of the run: {why}; the run goes on with what is installed"
            ),
            remedy: "run `ctxloom deps pull` once the remotes are reachable, or set `sync.on_run: none` to skip this".to_string(),
        }],
    }
}

fn path_text(path: &Path) -> String {
    path.display().to_string()
}

impl RunService for OneShots<'_> {
    /// `run --one-shot`: hosted by the user's daemon (U4, T7), this
    /// command its client; nothing of the run is in this process.
    fn one_shot(&mut self, flags: &Flags, request: &OneShot) -> Done<Answer> {
        client_one_shot(self.wiring, &self.stamp, flags, request)
    }

    /// `run`: the session prepared by the user's daemon (U5, T9), this
    /// command its frontend: the TUI started on this terminal exactly as the
    /// daemon's launch says, its start and exit reported. Nothing of the
    /// run is recorded in this process.
    fn interactive(&mut self, flags: &Flags, request: &OneShot) -> Done<Ended> {
        crate::frontend::client_terminal(self.wiring, &self.stamp, flags, request)
    }
}

/// `run --one-shot` as the daemon's client (U4, T7): its project resolved
/// and named by the daemon (its id minted at its root where nothing has),
/// the daemon reached (started if none serves, S16), the run created and
/// followed (`run_application::client`), and each of SIGHUP, SIGINT and
/// SIGTERM sent to the daemon as the run's stop (ruling 114). Nothing here
/// opens the store or starts an engine (T1).
pub(crate) fn client_one_shot(
    wiring: &Wiring,
    stamp: &str,
    flags: &Flags,
    request: &OneShot,
) -> Done<Answer> {
    let refused = |error: RunError| Done {
        result: Err(error),
        notices: Vec::new(),
    };
    if let Err(e) = wiring.require_project_root() {
        return refused(failed(cli_support::Failure::new(
            e,
            cli_support::Exit::REFUSED,
        )));
    }
    // Resolved before the run's runtime starts: the resolution blocks on a
    // runtime of its own.
    let parent = match crate::daemon::library_parent(wiring, stamp, crate::Minting::AtRoot) {
        Ok(parent) => parent,
        Err(failure) => return refused(failed(failure)),
    };
    let runtime = match tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()
    {
        Ok(runtime) => runtime,
        Err(e) => return refused(unmade(&e)),
    };
    runtime.block_on(async {
        let api = match crate::daemon::Reach::new(wiring, stamp).reach().await {
            Ok(api) => api,
            Err(unreached) => return refused(RunError::Unreached(unreached)),
        };
        let asked = run_application::client::Asked {
            parent,
            cwd: wiring
                .env()
                .cwd()
                .map(|cwd| cwd.display().to_string())
                .unwrap_or_default(),
            env: wiring.env().vars_utf8().into_iter().collect(),
            flags: flags.clone(),
            request: request.clone(),
            request_id: crate::daemon::request_id(),
            client: crate::daemon::calling_client(
                wiring.env(),
                daemon_proto::v1::client::Kind::Cli,
                stamp,
            ),
        };
        let (heard, hearing, unheard) = heard_stops(wiring);
        let mut done = run_application::client::one_shot(&api, &asked, heard).await;
        if let Some(hearing) = hearing {
            hearing.abort();
        }
        done.notices.splice(0..0, unheard);
        done
    })
}

/// The run `run` a call was answered with (a distill's model, step 10b:
/// the daemon owns the model call as a run), followed to its end through
/// the daemon of the stamp `stamp`, as a one-shot is: a signal heard asks
/// the daemon to stop it.
pub(crate) fn client_follow(
    wiring: &Wiring,
    stamp: &str,
    run: &str,
    stops: Option<&crate::signals::CommandStops>,
) -> Done<Answer> {
    let refused = |error: RunError| Done {
        result: Err(error),
        notices: Vec::new(),
    };
    let runtime = match tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()
    {
        Ok(runtime) => runtime,
        Err(e) => return refused(unmade(&e)),
    };
    runtime.block_on(async {
        let api = match crate::daemon::Reach::new(wiring, stamp).reach().await {
            Ok(api) => api,
            Err(unreached) => return refused(RunError::Unreached(unreached)),
        };
        let asked = daemon_proto::v1::Run {
            name: run.to_string(),
            ..daemon_proto::v1::Run::default()
        };
        // The command's own stops when it hears them already (ruling 114: a
        // stop heard before the follow began asks the daemon to stop the
        // run at once); else heard from here on.
        let (heard, hearing, unheard, signal) = match stops {
            Some(stops) => {
                let signal = stops.signal().map(|signal| signal.name().to_string());
                let heard: run_application::client::Heard<'static> = if signal.is_some() {
                    Box::pin(std::future::pending())
                } else {
                    stops.heard()
                };
                let unheard = stops.unheard().map(unheard_notice).into_iter().collect();
                (heard, None, unheard, signal)
            }
            None => {
                let (heard, hearing, unheard) = heard_stops(wiring);
                (heard, hearing, unheard, None)
            }
        };
        let mut done = run_application::client::followed(&api, &asked, heard, signal).await;
        if let Some(hearing) = hearing {
            hearing.abort();
        }
        done.notices.splice(0..0, unheard);
        done
    })
}

/// A command's failure, as the run's error: its diagnostic, exit and
/// refusals, said as they were.
pub(crate) fn failed(failure: cli_support::Failure) -> RunError {
    RunError::Hosted(Box::new(run_application::HostedEnd {
        report: Report::of(&*failure.cause),
        exit: failure.exit,
        refusals: failure
            .refusals
            .iter()
            .map(|refusal| Report::of(&**refusal))
            .collect(),
    }))
}

/// What is said when no signal can be heard: `why`.
fn unheard_notice(why: &str) -> library_application::Notice {
    library_application::Notice::text(format!(
        "cannot listen for signals ({why}): a signal ends ctxloom without asking the daemon to stop the engine"
    ))
}

/// The first of the signals that stop a one-shot (all three), once heard,
/// by its name; the task that hears them for the command's whole run (so
/// none ends it by its default, and a second changes nothing); and the
/// notice that none can be heard, when none can.
fn heard_stops(
    wiring: &Wiring,
) -> (
    run_application::client::Heard<'static>,
    Option<tokio::task::JoinHandle<()>>,
    Vec<library_application::Notice>,
) {
    let mut signals = match wiring.fs().os().signals(&signals::HEARD) {
        Ok(signals) => signals,
        Err(e) => {
            return (
                Box::pin(std::future::pending()),
                None,
                vec![unheard_notice(&e.to_string())],
            )
        }
    };
    let (tell, heard) = tokio::sync::oneshot::channel::<&'static str>();
    let hearing = tokio::spawn(async move {
        let mut tell = Some(tell);
        loop {
            let signal = signals.recv().await;
            if signals::STOP_ONE_SHOT.contains(&signal) {
                if let Some(tell) = tell.take() {
                    let _ = tell.send(signal.name());
                }
            }
        }
    });
    let heard = Box::pin(async move {
        match heard.await {
            Ok(name) => name.to_string(),
            Err(_) => std::future::pending().await,
        }
    });
    (heard, Some(hearing), Vec::new())
}

/// What hosts the daemon's one-shots (U4, T7; owner s4-Q8): each on a
/// thread of its own, the run use case's one-shot over the daemon's ONE
/// store (its core shared), in the environment and working directory its
/// client sent (S7), so it is planned, its dependencies pulled and its
/// engine set up and driven exactly as a process there would. Its engine
/// runs under the daemon; its session's lock and its run's are the
/// daemon's.
pub(crate) struct HostedOneShots {
    pub(crate) core: crate::SharedCore<SqliteStore>,
    /// The daemon's runtime, which each one-shot is driven on.
    pub(crate) runtime: tokio::runtime::Handle,
    /// ctxloom's home, the daemon's (a client's is the same, M11).
    pub(crate) home: std::path::PathBuf,
    pub(crate) temp: std::path::PathBuf,
}

/// A hosted one-shot's engine, stopped through its launcher's [`Stop`]
/// (its tree asked to stop, killed after the grace).
///
/// [`Stop`]: host_launcher::Stop
struct Stopping(host_launcher::Stop);

impl daemon_application::OneShotControl for Stopping {
    fn stop(&self) {
        self.0.request();
    }
}

impl daemon_application::OneShotHost for HostedOneShots {
    // Spawns the thread a hosted one-shot runs on; the acceptance tests
    // drive it (run.feature's daemon Rule, j002700).
    #[cfg_attr(test, mutants::skip)]
    fn host(
        &self,
        request: daemon_application::HostRequest,
        told: daemon_application::Told,
    ) -> Box<dyn daemon_application::OneShotControl> {
        let stop = host_launcher::Stop::new();
        let control = Box::new(Stopping(stop.clone()));
        let core = self.core.clone();
        let runtime = self.runtime.clone();
        let (home, temp) = (self.home.clone(), self.temp.clone());
        let spawned = std::thread::Builder::new()
            .name("ctxloom-one-shot".to_string())
            .spawn({
                let told = told.clone();
                move || {
                    let hosted = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
                        runtime.block_on(host_one_shot(
                            core,
                            (&home, &temp),
                            request,
                            stop,
                            told.clone(),
                        ))
                    }));
                    let panicked = || Done {
                        result: Err(RunError::NotStarted {
                            detail: "the daemon's one-shot panicked".to_string(),
                        }),
                        notices: Vec::new(),
                    };
                    let done = hosted.unwrap_or_else(|_| panicked());
                    // Its end awaited on the daemon's runtime: what follows
                    // from it (a distill's write, step 10b) is made there. A
                    // reaction that panics is taken already, so the second
                    // end is said as it is, and the run never stays open.
                    let ended = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
                        runtime.block_on(told.done(done))
                    }));
                    if ended.is_err() {
                        runtime.block_on(told.done(panicked()));
                    }
                }
            });
        if let Err(e) = spawned {
            // On the caller's runtime, where nothing may block on it: a run
            // that never started is answered at once, its reaction too
            // (nothing follows from a failure but its end).
            polled_once(told.done(Done {
                result: Err(RunError::NotStarted {
                    detail: format!("no thread for the one-shot: {e}"),
                }),
                notices: Vec::new(),
            }));
        }
        control
    }
}

/// Poll `future` once: what is answered at once is done; what would wait
/// is dropped (the core's end of a run that never started waits on
/// nothing).
pub(crate) fn polled_once(future: impl std::future::Future<Output = ()>) {
    let mut future = std::pin::pin!(future);
    let mut cx = std::task::Context::from_waker(std::task::Waker::noop());
    let _ = future.as_mut().poll(&mut cx);
}

/// The wiring a hosted one-shot is planned and driven in: its client's
/// environment and working directory (S7), with ctxloom's `home`.
pub(crate) fn hosted_wiring(
    home: &Path,
    temp: &Path,
    request: &daemon_application::HostRequest,
) -> Wiring {
    crate::daemon::caller_wiring(
        home,
        temp,
        (&request.cwd, None),
        request
            .env
            .iter()
            .map(|(name, value)| (name.as_str(), value.as_str())),
    )
}

/// What a run the daemon hosts is planned and driven in (a hosted
/// one-shot's and a prepared session's alike): its client's wiring (S7), the
/// library over it, where its outputs go, what the start's pull said, and
/// its project, told and named.
pub(crate) struct Hosted {
    pub(crate) wiring: Wiring,
    pub(crate) library: Library,
    pub(crate) output_dir: String,
    /// What the dependencies' pull at the run's start said (`sync.on_run`).
    pub(crate) synced: Vec<library_application::Notice>,
    pub(crate) project: (Project, String),
    /// The program its callbacks run: its client's, else the daemon's own.
    pub(crate) program: crate::program::Program,
}

/// The set-up of a run the daemon hosts for `request`: the dependencies
/// pulled first when `sync.on_run` asks. What failed, and what was said
/// before it did, otherwise.
pub(crate) async fn hosted_setup(
    (home, temp): (&Path, &Path),
    request: &daemon_application::HostRequest,
) -> Result<Hosted, (RunError, Vec<library_application::Notice>)> {
    let wiring = hosted_wiring(home, temp, request);
    let deps = match crate::commands::build_deps(&wiring) {
        Ok(deps) => std::rc::Rc::new(deps),
        Err(e) => return Err((RunError::Project(Report::of(&e)), Vec::new())),
    };
    let remotes: std::rc::Rc<dyn deps_ports::RemoteLibrary> = deps.clone();
    let library = match crate::library::build_over(&wiring, remotes) {
        Ok(library) => library,
        Err(e) => return Err((RunError::Project(Report::of(&e)), Vec::new())),
    };
    let settings = library.run_settings(&request.flags);
    let output_dir = settings
        .as_ref()
        .map_or_else(|| OUTPUT_DIR_DEFAULT.to_string(), |s| s.output_dir.clone());
    // Before the run is planned, so the plan sees what was installed.
    let synced = match &settings {
        Some(settings) => {
            let puller = crate::commands::DepsPuller(deps);
            sync_at_start(settings.on_run, || setup_application::Puller::pull(&puller))
        }
        None => Vec::new(),
    };
    let project = match tell_project(wiring.fs(), wiring.environment(), HarpNames) {
        Ok(project) => project,
        Err(e) => return Err((RunError::Project(e), synced)),
    };
    // Named by the daemon at its client's resolution; a client outside a
    // project's (its parent `users/me`) is named here.
    let project_harp = match &request.project {
        Some(named) => named.harp.clone(),
        None => match wiring.named_project().await {
            Ok(named) => named.harp,
            Err(e) => return Err((RunError::Project(Report::of(&e)), synced)),
        },
    };
    Ok(Hosted {
        wiring,
        library,
        output_dir,
        synced,
        project: (project, project_harp),
        program: crate::program::of_client(request.program.as_ref()),
    })
}

/// One hosted one-shot, start to end (`Runs::one_shot` over the daemon's
/// core, in its client's wiring): the dependencies pulled first when
/// `sync.on_run` asks, its placement told as soon as it is placed.
async fn host_one_shot(
    core: crate::SharedCore<SqliteStore>,
    places: (&Path, &Path),
    request: daemon_application::HostRequest,
    stop: host_launcher::Stop,
    told: daemon_application::Told,
) -> Done<Answer> {
    let hosted = match hosted_setup(places, &request).await {
        Ok(hosted) => hosted,
        Err((error, notices)) => {
            return Done {
                result: Err(error),
                notices,
            }
        }
    };
    let Hosted {
        wiring,
        library,
        output_dir,
        synced,
        project,
        program,
    } = hosted;
    let mut runs = runs_over(
        &wiring,
        library,
        &output_dir,
        Driven::here(&wiring, stop),
        core,
        project,
        &program,
    )
    .telling(Box::new(move |placed| told.placed(placed)));
    let mut done = runs.one_shot(&request.flags, &request.request).await;
    // Every part of the run let go (its edges) before it is told over: the daemon closes its store once its one-shots have ended.
    drop(runs);
    let mut notices = synced;
    notices.append(&mut done.notices);
    done.notices = notices;
    done
}

#[cfg(test)]
#[path = "oneshot.test.rs"]
mod tests;
