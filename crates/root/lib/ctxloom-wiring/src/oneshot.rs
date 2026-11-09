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

use angzarr_diagnostic::Report;
use angzarr_embedded::angzarr_store_sqlite::SqliteStore;
use angzarr_embedded::Catalog;
use angzarr_embedded_host::stores::OneStore;
use credentials::OsTokenSource;
use ctxloom_store_sqlite::Stores;
use embedded_edge::EmbeddedEdge;
use file_system::Fs;
use harp_names::HarpNames;
use hook_listener::HookListener;
use host_launcher::HostLauncher;
use library_application::{Flags, Library};
use ports::{Names, Project, ProjectIdentity};
use process_env::EnvSnapshot;
use project_registry::Environment;
use run_application::{
    Answer, Core, Done, Ended, OneShot, Parts, Place, RunError, RunService, Runs,
};
use run_launcher::{Launcher, Orders, Recorder};
use run_ports::{BoxFuture, Edge, Engine, HookServer, Limits, Serving};
use runs_application::{AppError as CoreError, Enqueuer};
use session_files::SessionDirs;
use system::System;

use crate::{InProcessCore, Wiring};

/// How long one turn has before its command, and everything it started, is
/// killed.
pub(crate) const TURN_DEADLINE: Duration = Duration::from_secs(30 * 60);

impl Core for InProcessCore<SqliteStore> {
    type Edge = EmbeddedEdge<OneStore<SqliteStore>>;
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
}

impl<'a> OneShots<'a> {
    pub(crate) fn new(wiring: &'a Wiring, library: Library) -> OneShots<'a> {
        OneShots {
            wiring,
            library: Some(library),
        }
    }

    /// The use case over the project the process is in.
    async fn assemble(
        &self,
        library: Library,
    ) -> Result<
        Runs<InProcessCore<SqliteStore>, OsTokenSource, HostLauncher, SessionDirs, SessionDirs>,
        RunError,
    > {
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
        let unmade = |cause: &dyn std::fmt::Display| {
            RunError::Session(CoreError::Failed {
                reason: cause.to_string(),
            })
        };
        let store = Stores::new(wiring.fs(), wiring.home())
            .open(&project.id)
            .await
            .map_err(|e| unmade(&e))?;
        let core = InProcessCore::start(store).await;
        let project_dir = wiring.project_dir();
        let orders = Arc::new(Orders::new());
        let claude: Arc<dyn Engine> = Arc::new(engine_claude::Claude::new());
        let launcher = Launcher::new(
            core.edge(),
            HostLauncher::default(),
            SessionDirs::new(wiring.fs(), project_dir.clone()),
            vec![claude],
            Arc::clone(&orders),
            Limits::new(TURN_DEADLINE),
        );
        let hooks = Box::new(Hooks {
            edge: core.edge(),
            fs: wiring.fs(),
        });
        let recorder = Recorder::new(SessionDirs::new(wiring.fs(), project_dir.clone()));
        let enqueuer = Enqueuer::new(core.edge(), Arc::new(OsTokenSource));
        let output_root = wiring
            .home()
            .join("Documents/ctxloom")
            .join(project_dir.file_name().unwrap_or_default());
        Ok(Runs::new(Parts {
            core,
            planner: Box::new(library),
            identity: Box::new(Told(project)),
            names: Box::new(HarpNames),
            ids: Box::new(System),
            enqueuer,
            launcher,
            recorder,
            orders,
            place: Place {
                project_dir: path_text(&project_dir),
                output_root: path_text(&output_root),
                program_dir: std::env::current_exe()
                    .ok()
                    .and_then(|exe| exe.parent().map(path_text))
                    .unwrap_or_default(),
            },
            env: run_env(env, wiring.home()),
            links: Box::new(SessionDirs::new(wiring.fs(), project_dir.clone())),
            os: std::env::consts::OS.to_string(),
            hooks,
        }))
    }
}

/// The run's hook listener over the core's edge: `Handle` on the run's socket
/// behind the hook gate ([`hook_listener`]), started before an interactive
/// engine launches and stopped after it exits.
struct Hooks<E> {
    edge: Arc<E>,
    fs: Arc<dyn Fs>,
}

impl<E: Edge + 'static> HookServer for Hooks<E> {
    fn serve<'a>(&'a self, endpoint: &'a str) -> BoxFuture<'a, Result<Box<dyn Serving>, Report>> {
        Box::pin(async move {
            // Sockets a killed run left are cleared first; failing to clear
            // them is no reason not to serve.
            let _ = crate::stale_socket::clear_stale(&*self.fs, Path::new(endpoint)).await;
            let listener = HookListener::serve(Arc::clone(&self.edge), Path::new(endpoint))
                .await
                .map_err(|e| Report::of(&e))?;
            Ok(Box::new(Live(listener)) as Box<dyn Serving>)
        })
    }
}

/// A listener that is serving.
struct Live(HookListener);

impl Serving for Live {
    fn stop(self: Box<Self>) -> BoxFuture<'static, ()> {
        // A stop that failed has nothing to tell a run that is over; the
        // socket's guard removes the file as the listener drops either way.
        Box::pin(async move {
            let _ = self.0.stop().await;
        })
    }
}

/// The environment a run is given: the caller's, and ctxloom's home named by
/// [`run_ports::HOME_VARIABLE`], so an engine that is given it (the
/// interactive one) can pass it on to its hook processes, which run without
/// `HOME`.
fn run_env(env: &EnvSnapshot, home: &Path) -> BTreeMap<String, String> {
    let mut vars: BTreeMap<String, String> = env.vars_utf8().into_iter().collect();
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

fn path_text(path: &Path) -> String {
    path.display().to_string()
}

/// The runtime a run is driven on: multi-thread, so a task spawned on it (the
/// run's listener, which serves a unix socket) keeps making progress while
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
    /// `use_case` to drive.
    fn with_runs<T>(
        &mut self,
        use_case: impl AsyncFnOnce(
            &mut Runs<
                InProcessCore<SqliteStore>,
                OsTokenSource,
                HostLauncher,
                SessionDirs,
                SessionDirs,
            >,
        ) -> Done<T>,
    ) -> Done<T> {
        let refused = |reason: String| Done {
            result: Err(RunError::Session(CoreError::Failed { reason })),
            notices: Vec::new(),
        };
        let Some(library) = self.library.take() else {
            return refused("the run use cases are made for one run".to_string());
        };
        let runtime = match run_runtime() {
            Ok(runtime) => runtime,
            Err(e) => return refused(e.to_string()),
        };
        runtime.block_on(async {
            match self.assemble(library).await {
                Ok(mut runs) => use_case(&mut runs).await,
                Err(error) => Done {
                    result: Err(error),
                    notices: Vec::new(),
                },
            }
        })
    }
}

impl RunService for OneShots<'_> {
    fn one_shot(&mut self, flags: &Flags, request: &OneShot) -> Done<Answer> {
        self.with_runs(async |runs| runs.one_shot(flags, request).await)
    }

    fn interactive(&mut self, flags: &Flags, request: &OneShot) -> Done<Ended> {
        self.with_runs(async |runs| runs.interactive(flags, request).await)
    }
}

#[cfg(test)]
#[path = "oneshot.test.rs"]
mod tests;
