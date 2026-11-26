//! What hosts the daemon's one-shots (U4, T7; owner s4-Q8): each on a thread
//! of its own, the run use case's one-shot over the daemon's ONE store (its
//! core shared), in the environment and working directory its client sent,
//! so it is planned, its dependencies pulled and its engine set up and driven
//! exactly as a process there would. The command is its client
//! (`ctxloom_wiring::oneshot::client_one_shot`).

use std::path::Path;
use std::sync::Arc;

use angzarr_embedded::angzarr_store_sqlite::SqliteStore;
use angzarr_embedded::Catalog;
use coded_diagnostic::Report;
use ctxloom_wiring::oneshot::{runs_over, sync_at_start, tell_project, Driven};
use embedded_edge::EmbeddedEdge;
use harp_names::HarpNames;
use library_application::{Library, OUTPUT_DIR_DEFAULT};
use ports::Project;
use run_application::{Answer, Core, Done, RunError};

use crate::{CoreStores, InProcessCore, Wiring};

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
    crate::caller_wiring(
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
    pub(crate) program: ctxloom_wiring::program::Program,
}

/// The set-up of a run the daemon hosts for `request`: the dependencies
/// pulled first when `sync.on_run` asks. What failed, and what was said
/// before it did, otherwise.
pub(crate) async fn hosted_setup<E: run_ports::Edge>(
    (home, temp): (&Path, &Path),
    request: &daemon_application::HostRequest,
    edge: &Arc<E>,
) -> Result<Hosted, (RunError, Vec<library_application::Notice>)> {
    let wiring = hosted_wiring(home, temp, request);
    let deps = match ctxloom_wiring::commands::build_deps(&wiring) {
        Ok(deps) => std::rc::Rc::new(deps),
        Err(e) => return Err((RunError::Project(Report::of(&e)), Vec::new())),
    };
    let remotes: std::rc::Rc<dyn deps_ports::RemoteLibrary> = deps.clone();
    let library = match ctxloom_wiring::library::build_over(&wiring, remotes) {
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
            let puller = ctxloom_wiring::commands::DepsPuller(deps);
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
        // Through the daemon's own edge (ONE WRITER), never a second
        // connection to the store.
        None => match sessions_application::name_through(
            &mut crate::EdgeDispatch {
                edge: Arc::clone(edge),
            },
            &project,
            &crate::fresh_project_harps(),
        )
        .await
        {
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
        program: ctxloom_wiring::program::of_client(request.program.as_ref()),
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
    let hosted = match hosted_setup(places, &request, &core.edge()).await {
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
#[path = "hosted_oneshots.test.rs"]
mod tests;
