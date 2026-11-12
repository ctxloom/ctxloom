//! `run --one-shot` wired (Plan step 1b-ii): the run use cases over the
//! default adapters, in this process. The project is told, its store opened
//! and the core served in process over it; the launcher runs the engine's
//! command on this host (`claude`, found on the caller's `PATH`), and the
//! session's directory is written under the project's `.ctxloom/sessions`.
//! Nothing here starts a second process of ctxloom: the runner is this one.

use std::collections::BTreeMap;
use std::path::Path;
use std::sync::Arc;
use std::time::Duration;

use crate::CoreStores;
use angzarr_diagnostic::Report;
use angzarr_embedded::angzarr_store_sqlite::SqliteStore;
use angzarr_embedded::Catalog;
use credentials::OsTokenSource;
use ctxloom_store_sqlite::Stores;
use embedded_edge::EmbeddedEdge;
use file_system::Fs;
use harp_names::HarpNames;
use hook_listener::HookListener;
use host_launcher::HostLauncher;
use library_application::{Flags, Library, OUTPUT_DIR_DEFAULT};
use ports::{Names, Project, ProjectIdentity};
use process_env::EnvSnapshot;
use project_registry::Environment;
use run_application::{
    Answer, Core, Done, Ended, OneShot, Parts, Place, RunError, RunService, Runs,
};
use run_launcher::{Launcher, Orders};
use run_ports::{BoxFuture, Edge, Engine, HookCertificate, HookServer, Limits, Serving};
use runs_application::{AppError as CoreError, Enqueuer};
use session_files::SessionDirs;

use crate::signals::{self, Heard};
use crate::{InProcessCore, Wiring};

/// How long one turn has before its command, and everything it started, is
/// killed.
pub(crate) const TURN_DEADLINE: Duration = Duration::from_secs(30 * 60);

/// The run use cases as this process wires them.
type TheRuns = Runs<InProcessCore<SqliteStore>, OsTokenSource, HostLauncher, SessionDirs>;

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

/// The project, already told: the identity the use case asks again.
struct Told(Project);

impl ProjectIdentity for Told {
    fn for_write(&mut self, _: Option<&str>) -> Result<Project, angzarr_diagnostic::Report> {
        Ok(self.0.clone())
    }

    fn for_read(&mut self, _: Option<&str>) -> Result<Option<Project>, angzarr_diagnostic::Report> {
        Ok(Some(self.0.clone()))
    }

    fn every_project(&mut self) -> Result<Vec<Project>, angzarr_diagnostic::Report> {
        Ok(vec![self.0.clone()])
    }

    fn rootless_directory(&mut self) -> Result<Option<String>, angzarr_diagnostic::Report> {
        Ok(None)
    }
}

/// The run use cases, built when the one-shot is asked: the library plans, and
/// the rest is made for the project the run is in.
pub(crate) struct OneShots<'a> {
    wiring: &'a Wiring,
    library: Option<Library>,
    /// The pull a run's start makes when `sync.on_run` asks (rulings 59, 60);
    /// none, nothing is pulled.
    puller: Option<std::rc::Rc<dyn setup_application::Puller>>,
}

impl<'a> OneShots<'a> {
    pub(crate) fn new(wiring: &'a Wiring, library: Library) -> OneShots<'a> {
        OneShots {
            wiring,
            library: Some(library),
            puller: None,
        }
    }

    /// The same, pulling the missing dependencies at a run's start through
    /// `puller` when `sync.on_run` asks.
    pub(crate) fn with_puller(
        mut self,
        puller: std::rc::Rc<dyn setup_application::Puller>,
    ) -> OneShots<'a> {
        self.puller = Some(puller);
        self
    }

    /// The use case over the project the process is in.
    async fn assemble(
        &self,
        library: Library,
        output_dir: &str,
        stop: host_launcher::Stop,
    ) -> Result<TheRuns, RunError> {
        let wiring = self.wiring;
        let env = wiring.env();
        let environment = Environment {
            home: Some(wiring.home().to_path_buf()),
            root: env.non_empty("CTXLOOM_ROOT").map(std::path::PathBuf::from),
            project_id: env
                .non_empty("CTXLOOM_PROJECT_ID")
                .and_then(|v| v.to_str().map(String::from)),
        };
        let project =
            tell_project(wiring.fs(), environment, HarpNames).map_err(RunError::Project)?;
        // Named at its resolution, before any session of it is minted (slice
        // 2 M1): a session's name, and so its stable id, holds its harp.
        let project_harp = wiring
            .named_project()
            .await
            .map_err(|e| RunError::Project(angzarr_diagnostic::Report::of(&e)))?
            .harp;
        let unmade = |cause: &dyn std::fmt::Display| {
            RunError::Session(CoreError::Failed {
                reason: cause.to_string(),
            })
        };
        let store = Stores::new(wiring.fs(), wiring.home())
            .open_for(&project.id)
            .await
            .map_err(|e| unmade(&e))?;
        let core = InProcessCore::start(store).await;
        let project_dir = wiring.project_dir();
        let orders = Arc::new(Orders::new());
        // The engines' callbacks run this binary, by its file name, and its
        // directory leads their PATH (owner ruling 163).
        let program = crate::program::running();
        let claude: Arc<dyn Engine> = Arc::new(
            engine_claude::Claude::calling(&program.name)
                .over_user_settings(claude_user_settings(wiring)),
        );
        let opencode: Arc<dyn Engine> = Arc::new(
            engine_opencode::Opencode::calling(&program.name)
                .seeding_model(opencode_recent_models(wiring))
                .over_user_config(opencode_user_config(wiring)),
        );
        // The run locks: this run's own, and those another process's runs
        // are judged by (rulings 86, 92, 93).
        let run_locks: Arc<dyn run_ports::RunLocks> =
            Arc::new(liveness::RunLockFiles::new(wiring.fs(), wiring.home()));
        let launcher = Launcher::new(
            core.edge(),
            HostLauncher::stopped_by(stop),
            SessionDirs::new(wiring.fs(), project_dir.clone()),
            vec![claude, opencode],
            Arc::clone(&orders),
            Limits::new(TURN_DEADLINE),
        )
        .with_locks(Arc::clone(&run_locks))
        // Each command measured against this platform's command line before
        // it is spawned (a terminal session's opening prompt is on it).
        .on_os(wiring.os_name());
        let hooks = Box::new(Hooks { edge: core.edge() });
        let enqueuer = Enqueuer::new(core.edge(), Arc::new(OsTokenSource));
        let output_root = output_root(&project_dir, output_dir);
        Ok(Runs::new(Parts {
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
            },
            env: run_env(env, wiring.home()),
            links: Box::new(SessionDirs::new(wiring.fs(), project_dir.clone())),
            os: wiring.os_name().to_string(),
            hooks,
            certs: Box::new(hook_tls::Minter::new(hook_tls::SystemClock)),
            locks: Box::new(liveness::LockFiles::new(wiring.fs(), project_dir.clone())),
            run_locks,
            git_user: git_user(wiring, &project_dir),
        }))
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

/// The run's hook listener over the core's edge: `Handle` on the run's loopback port
/// behind the hook gate ([`hook_listener`]), started before an interactive
/// engine launches and stopped after it exits.
struct Hooks<E> {
    edge: Arc<E>,
}

impl<E: Edge + 'static> HookServer for Hooks<E> {
    fn serve_tcp<'a>(
        &'a self,
        bind: std::net::IpAddr,
        identity: &'a HookCertificate,
    ) -> BoxFuture<'a, Result<Box<dyn Serving>, Report>> {
        Box::pin(async move {
            // Port 0: each run gets a port of the system's choosing.
            let listener = HookListener::serve_tcp(
                Arc::clone(&self.edge),
                std::net::SocketAddr::new(bind, 0),
                hook_listener::TlsIdentity::from_pem(
                    identity.public().to_pem(),
                    identity.key_pem(),
                ),
            )
            .await
            .map_err(|e| Report::of(&e))?;
            Ok(Box::new(Live(listener)) as Box<dyn Serving>)
        })
    }
}

/// A listener that is serving.
struct Live(HookListener);

impl Serving for Live {
    fn port(&self) -> u16 {
        self.0.local_addr().port()
    }

    fn stop(self: Box<Self>) -> BoxFuture<'static, ()> {
        // A stop that failed has nothing to tell a run that is over; the
        // listener is aborted as it drops either way.
        Box::pin(async move {
            let _ = self.0.stop().await;
        })
    }
}

/// The environment a run is given: the caller's, and ctxloom's home named by
/// [`run_ports::HOME_VARIABLE`], so an engine that is given it (the
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
        run_ports::HOME_VARIABLE.to_string(),
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

/// The runtime a run is driven on: multi-thread, so a task spawned on it (the
/// run's listener, which serves TCP) keeps making progress while
/// the use case's own future blocks the thread that drives it. The run waits
/// for its engine in a blocking call ([`run_ports::Running::finish`]); on a
/// current-thread runtime that wait would starve every other task. One worker
/// is enough for the listener beside the thread that drives the run.
fn run_runtime() -> std::io::Result<tokio::runtime::Runtime> {
    tokio::runtime::Builder::new_multi_thread()
        .worker_threads(1)
        .enable_all()
        .build()
}

impl OneShots<'_> {
    /// The run use cases over the project, on a runtime of their own, for
    /// `use_case` to drive, hearing the signals that ask ctxloom to stop for
    /// the whole run and stopping the engine on one of `stop_on`
    /// ([`crate::signals`]).
    fn with_runs<T>(
        &mut self,
        flags: &Flags,
        stop_on: &'static [os::Signal],
        use_case: impl AsyncFnOnce(&mut TheRuns) -> Done<T>,
    ) -> Done<T> {
        let refused = |reason: String| Done {
            result: Err(RunError::Session(CoreError::Failed { reason })),
            notices: Vec::new(),
        };
        let Some(library) = self.library.take() else {
            return refused("the run use cases are made for one run".to_string());
        };
        let settings = library.run_settings(flags);
        let output_dir = settings
            .as_ref()
            .map_or_else(|| OUTPUT_DIR_DEFAULT.to_string(), |s| s.output_dir.clone());
        // Before the run is planned, so the plan sees what was installed.
        let synced = match (&settings, &self.puller) {
            (Some(settings), Some(puller)) => sync_at_start(settings.on_run, || puller.pull()),
            _ => Vec::new(),
        };
        let runtime = match run_runtime() {
            Ok(runtime) => runtime,
            Err(e) => return refused(e.to_string()),
        };
        let (stop, heard) = (host_launcher::Stop::new(), Heard::default());
        let mut notices = synced;
        let mut done = runtime.block_on(async {
            // Made on the runtime, before anything is launched.
            let hearing = match self.wiring.fs().os().signals(&signals::HEARD) {
                Ok(signals) => Some(tokio::spawn(signals::forward(
                    signals,
                    stop_on,
                    stop.clone(),
                    heard.clone(),
                ))),
                Err(e) => {
                    notices.push(library_application::Notice::text(format!(
                        "cannot listen for signals ({e}): a signal ends ctxloom without stopping the engine"
                    )));
                    None
                }
            };
            let done = match self.assemble(library, &output_dir, stop).await {
                Ok(mut runs) => {
                    let mut done = use_case(&mut runs).await;
                    // The store closed, not dropped, before the process exits
                    // (rulings 131, 140): every part but the core let go with
                    // their edges (the listener was stopped by the use case,
                    // its connections ended and its service released), then
                    // the core consumed and the store taken by value. A close
                    // that fails fails the run.
                    if let Err(unclosed) = runs.into_core().close().await {
                        let reason = unclosed.to_string();
                        match &done.result {
                            Ok(_) => {
                                done.result =
                                    Err(RunError::Session(CoreError::Failed { reason }));
                            }
                            Err(_) => done.notices.push(library_application::Notice::text(reason)),
                        }
                    }
                    done
                }
                Err(error) => Done {
                    result: Err(error),
                    notices: Vec::new(),
                },
            };
            if let Some(hearing) = hearing {
                hearing.abort();
            }
            done
        });
        heard.overrule(&mut done.result);
        notices.append(&mut done.notices);
        done.notices = notices;
        done
    }
}

impl RunService for OneShots<'_> {
    fn one_shot(&mut self, flags: &Flags, request: &OneShot) -> Done<Answer> {
        self.with_runs(flags, &signals::STOP_ONE_SHOT, async |runs| {
            runs.one_shot(flags, request).await
        })
    }

    fn interactive(&mut self, flags: &Flags, request: &OneShot) -> Done<Ended> {
        self.with_runs(flags, &signals::STOP_SESSION, async |runs| {
            runs.interactive(flags, request).await
        })
    }
}

#[cfg(test)]
#[path = "oneshot.test.rs"]
mod tests;
