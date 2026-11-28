//! What prepares the daemon's interactive sessions (U5): each on a thread of
//! its own, over the daemon's ONE store (its core shared), in the environment
//! and directory its client sent (S7). Its frontend is the `ctxloom run`
//! command (`ctxloom_compose::frontend`), which starts the prepared TUI.

use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::time::{Duration, Instant};

use angzarr_embedded::angzarr_store_sqlite::SqliteStore;
use angzarr_embedded::v1::{event_page, Cover, Uuid};
use angzarr_embedded::Payload;
use ctxloom_compose::frontend::SESSION_STACK;
use ctxloom_compose::oneshot::{runs_over, Driven};
use daemon_application::{
    FrontendReports, Frontended, HostRequest, PreparedLaunches, Seen, TerminalHost, TerminalTold,
    Waits,
};
use daemon_proto::v1;
use host_launcher::HostLauncher;
use prost::Message as _;
use run_application::{client, Done, Ended, RunError};
use run_ports::{Edge, Launch, LaunchAt, LaunchFailure};
use session_ports::{Held, LockError, SessionLock};

use crate::hosted_oneshots::{hosted_setup, Hosted};

/// What prepares the daemon's interactive sessions (U5): each on a thread of
/// its own, over the daemon's ONE store (its core shared), in the
/// environment and directory its client sent (S7).
pub(crate) struct HostedTerminals {
    pub(crate) core: crate::SharedCore<SqliteStore>,
    /// The daemon's runtime, which each session is driven on.
    pub(crate) runtime: tokio::runtime::Handle,
    pub(crate) home: PathBuf,
    pub(crate) temp: PathBuf,
    /// Seals the secrets of each run's record; the daemon's own key.
    pub(crate) sealing: Arc<dyn run_ports::Sealing>,
}

impl HostedTerminals {
    fn launches(&self) -> StoreLaunches<impl Edge + 'static> {
        StoreLaunches {
            edge: self.core.edge(),
            runtime: self.runtime.clone(),
            sealing: Arc::clone(&self.sealing),
        }
    }
}

impl TerminalHost for HostedTerminals {
    fn launch_of(&self, run: &str) -> Option<Launch> {
        self.launches().read(run)
    }

    // Spawns the thread a prepared session runs on; the acceptance tests
    // drive it (run.feature's daemon Rule, j002700).
    #[cfg_attr(test, mutants::skip)]
    fn host(&self, request: HostRequest, told: TerminalTold) {
        let core = self.core.clone();
        let runtime = self.runtime.clone();
        let (home, temp) = (self.home.clone(), self.temp.clone());
        let sealing = Arc::clone(&self.sealing);
        let spawned = std::thread::Builder::new()
            .name("ctxloom-terminal".to_string())
            // A session's whole run is driven on it (the plan, the set-up,
            // the launcher): more than a thread's default stack holds.
            .stack_size(SESSION_STACK)
            .spawn({
                let told = told.clone();
                move || {
                    let hosted = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
                        runtime.block_on(Box::pin(host_terminal(
                            core,
                            (&home, &temp),
                            request,
                            (told.clone(), sealing),
                        )))
                    }));
                    let done = hosted.unwrap_or_else(|_| Done {
                        result: Err(RunError::NotStarted {
                            detail: "the daemon's session panicked".to_string(),
                        }),
                        notices: Vec::new(),
                    });
                    told.done(events_of(&done));
                }
            });
        if let Err(e) = spawned {
            told.done(events_of(&Done {
                result: Err(RunError::NotStarted {
                    detail: format!("no thread for the session: {e}"),
                }),
                notices: Vec::new(),
            }));
        }
    }
}

/// What a prepared session's end says on its watch: what it noticed, then
/// its end (exit 0 and no code for a session that ended; its error's
/// otherwise).
pub(crate) fn events_of(done: &Done<Ended>) -> Vec<v1::RunEvent> {
    let event = |event| v1::RunEvent { event: Some(event) };
    let mut events: Vec<v1::RunEvent> = done
        .notices
        .iter()
        .map(|notice| event(v1::run_event::Event::Notice(client::notice_of(notice))))
        .collect();
    let end = match &done.result {
        Ok(_) => client::answered(),
        Err(error) => client::end_of(error),
    };
    events.push(event(v1::run_event::Event::End(end)));
    events
}

/// A session's lock the daemon does not take: its frontend does.
struct Unheld;

impl SessionLock for Unheld {
    fn hold(&mut self, _: &str) -> Result<Held, LockError> {
        Ok(Held::new(()))
    }
}

/// One prepared session, start to end (`Runs::interactive` over the
/// daemon's core, in its client's wiring), its launch handed to its
/// frontend by the runner.
async fn host_terminal(
    core: crate::SharedCore<SqliteStore>,
    places: (&Path, &Path),
    request: HostRequest,
    (told, sealing): (TerminalTold, Arc<dyn run_ports::Sealing>),
) -> Done<Ended> {
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
    let fs = wiring.fs();
    let project_dir = wiring.project_dir();
    let locks = liveness::LockFiles::new(Arc::clone(&fs), project_dir.clone());
    let runner = Frontended {
        told,
        project_harp: project.1.clone(),
        reports: Arc::new(StoreReports {
            edge: core.edge(),
            runtime: tokio::runtime::Handle::current(),
        }),
        launches: Arc::new(StoreLaunches {
            edge: core.edge(),
            runtime: tokio::runtime::Handle::current(),
            sealing: Arc::clone(&sealing),
        }),
        waits: Arc::new(Waited(Instant::now())),
        locks: Arc::new(liveness::RunLockFiles::new(Arc::clone(&fs), places.0)),
        project_dir: project_dir.display().to_string(),
        lock_path: Arc::new(move |harp: &str| locks.path(harp).display().to_string()),
        finder: Arc::new(HostLauncher::default().searching(Arc::clone(&fs))),
    };
    let driven = Driven {
        runner,
        locks: Box::new(Unheld),
        sealing,
    };
    let mut runs = runs_over(
        &wiring,
        library,
        &output_dir,
        driven,
        core,
        project,
        &program,
    );
    // U5's start-report race: the run's own lock is its frontend's.
    runs.held_by_frontend(request.launcher_lock.clone());
    let mut done = runs.interactive(&request.flags, &request.request).await;
    // Every part of the run let go before it is told over: the daemon
    // closes its store once nothing holds it.
    drop(runs);
    let mut notices = synced;
    notices.append(&mut done.notices);
    done.notices = notices;
    done
}

/// The daemon's clock, for a session waiting on its frontend.
struct Waited(Instant);

impl Waits for Waited {
    fn elapsed(&self) -> Duration {
        self.0.elapsed()
    }

    // Sleeps the session's own thread between two reads of its run.
    #[cfg_attr(test, mutants::skip)]
    fn wait(&self, pause: Duration) {
        std::thread::sleep(pause);
    }
}

/// The frontend's reports as the daemon's store holds them: read from the
/// run's stream and the session's, on the daemon's runtime, the session's
/// thread waiting for the answer.
/// The launch of a prepared session, on its run's stream: its environment's
/// values sealed to the project's key, the rest the encoding of the wire's
/// `Launch`.
pub(crate) struct StoreLaunches<E> {
    pub(crate) edge: Arc<E>,
    pub(crate) runtime: tokio::runtime::Handle,
    pub(crate) sealing: Arc<dyn run_ports::Sealing>,
}

/// What a value of the launch's environment is sealed for.
fn env_field(name: &str) -> String {
    format!("launch-env:{name}")
}

fn unrecorded(detail: impl ToString) -> LaunchFailure {
    LaunchFailure {
        deterministic: false,
        detail: detail.to_string(),
    }
}

impl<E: Edge + 'static> PreparedLaunches for StoreLaunches<E> {
    fn record(&self, launch: &Launch) -> Result<(), LaunchFailure> {
        let project = launch.at.project_id.as_str();
        let mut wire = daemon_api_contract::launch::to_wire(launch);
        let env = std::mem::take(&mut wire.env)
            .into_iter()
            .map(|var| {
                let sealed =
                    self.sealing
                        .seal(project, &env_field(&var.name), var.value.as_bytes())?;
                Ok(runs::EnvEntry {
                    name: var.name,
                    value: runs::SealedValue { sealed },
                })
            })
            .collect::<Result<Vec<_>, run_ports::SealingError>>()
            .map_err(unrecorded)?;
        let fact = runs::RecordLaunchPrepared {
            launch: runs::PreparedLaunch {
                launch: wire.encode_to_vec(),
                env,
            },
        };
        let book = sessions::fact_book(
            cover(runs::RUN_DOMAIN, &launch.at.run),
            &fact,
            &format!("prepared:{}", launch.at.run),
        );
        let edge = Arc::clone(&self.edge);
        let (tell, heard) = std::sync::mpsc::channel();
        self.runtime.spawn(async move {
            let _ = tell.send(edge.handle(book).await.map(|_| ()).map_err(unrecorded));
        });
        heard
            .recv()
            .map_err(|_| unrecorded("the daemon's runtime ended"))?
    }
}

impl<E: Edge + 'static> StoreLaunches<E> {
    /// The launch `run`'s stream holds, its environment opened.
    pub(crate) fn read(&self, run: &str) -> Option<Launch> {
        let edge = Arc::clone(&self.edge);
        let run_cover = cover(runs::RUN_DOMAIN, run);
        let (tell, heard) = std::sync::mpsc::channel();
        self.runtime.spawn(async move {
            let _ = tell.send(edge.events(&run_cover, 0).await.unwrap_or_default());
        });
        let pages = heard.recv().ok()?;
        let prepared = bodies(&pages).find_map(|any| match runs::RunEvent::from_any(any) {
            Ok(runs::RunEvent::LaunchPrepared(prepared)) => Some(prepared.launch),
            _ => None,
        })?;
        let mut wire = daemon_proto::v1::Launch::decode(prepared.launch.as_slice()).ok()?;
        let project = wire.at.as_ref()?.project_id.clone();
        for entry in prepared.env {
            let plain = self
                .sealing
                .open(&project, &env_field(&entry.name), &entry.value.sealed)
                .ok()?;
            wire.env.push(daemon_proto::v1::EnvVar {
                name: entry.name,
                value: String::from_utf8_lossy(&plain).into_owned(),
            });
        }
        daemon_api_contract::launch::from_wire(&wire)
    }
}

struct StoreReports<E> {
    edge: Arc<E>,
    runtime: tokio::runtime::Handle,
}

/// The stream of `domain` whose root is `id` (UUID text).
fn cover(domain: &str, id: &str) -> Cover {
    let root = uuid::Uuid::parse_str(id).unwrap_or_default();
    Cover {
        domain: domain.to_string(),
        root: Some(Uuid::from_bytes(*root.as_bytes())),
        edition: None,
    }
}

impl<E: Edge + 'static> FrontendReports for StoreReports<E> {
    fn seen(&self, at: &LaunchAt) -> Seen {
        let edge = Arc::clone(&self.edge);
        let (run, session) = (
            cover(runs::RUN_DOMAIN, &at.run),
            cover(sessions::SESSION_DOMAIN, &at.session),
        );
        let (tell, heard) = std::sync::mpsc::channel();
        self.runtime.spawn(async move {
            let run = edge.events(&run, 0).await.unwrap_or_default();
            let session = edge.events(&session, 0).await.unwrap_or_default();
            let _ = tell.send((run, session));
        });
        let Ok((run, session)) = heard.recv() else {
            return Seen::default();
        };
        seen_in(&run, &session)
    }
}

/// What the frontend's reports came to, as the run's and the session's
/// pages hold them: the start's pid, the run's end (its exit, or, before
/// any start, why it never started), and whether the session was closed.
fn seen_in(
    run: &[angzarr_embedded::v1::EventPage],
    session: &[angzarr_embedded::v1::EventPage],
) -> Seen {
    let mut seen = Seen::default();
    for any in bodies(run) {
        match runs::RunEvent::from_any(any) {
            Ok(runs::RunEvent::RunnerStartedRecorded(started)) => seen.started = Some(started.pid),
            Ok(runs::RunEvent::RunEnded(ended)) => match seen.started {
                Some(_) => {
                    seen.exited = Some(run_ports::Exit {
                        code: ended.exit_code,
                        signal: None,
                    })
                }
                None => {
                    seen.failed = Some(LaunchFailure {
                        deterministic: true,
                        detail: ended.detail,
                    })
                }
            },
            _ => {}
        }
    }
    seen.closed = bodies(session).any(|any| any.type_url.ends_with(".SessionClosed"));
    seen
}

fn bodies(
    pages: &[angzarr_embedded::v1::EventPage],
) -> impl Iterator<Item = &angzarr_embedded::Any> {
    pages.iter().filter_map(|page| match &page.body {
        Some(event_page::Body::Event(any)) => Some(any),
        _ => None,
    })
}

#[cfg(test)]
#[path = "hosted_terminals.test.rs"]
mod tests;
