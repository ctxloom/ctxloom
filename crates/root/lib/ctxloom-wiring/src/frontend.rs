//! The interactive session as the user's daemon prepares it and its
//! frontend starts it (U5: T9, S3, S17, S20), wired.
//!
//! The DAEMON's half ([`HostedTerminals`]): each session on a thread of its
//! own, the run use case's `Runs::interactive` over the daemon's one store,
//! in the environment and working directory its client sent (S7), planned,
//! enqueued, named, minted and set up exactly as before, its runner the
//! daemon-application's `Frontended`: the one launch handed to the client
//! that asked, the frontend's start and exit read from the run's stream
//! ([`StoreReports`]), the session's lock the frontend's.
//!
//! The FRONTEND's half ([`client_terminal`], the command `ctxloom run`, and
//! init's interview): its project resolved and named by the daemon, the
//! daemon reached (started if none serves, S16; none, exit 32 before
//! anything is planned), `CreateRun{TERMINAL}` answered with the launch,
//! then `run_application::frontend` over this host: the run's own lock
//! taken BEFORE the daemon is asked (U5's start-report race: a daemon
//! started before the start report reads its owner alive), the session's
//! lock taken, the TUI started on this terminal with exactly the launch's
//! program, arguments, environment and directory (`run_terminal`), its
//! start and exit reported as the run's facts to whichever daemon serves
//! (found, or started), each ONE attempt behind what the session's hold
//! holds, and HELD there (as a hook's facts are) when no up-to-date daemon
//! takes it, for the next report or any command's flush to deliver in
//! order (slice 4, a daemon dying under a live TUI; [`DaemonReports`]).
//! While the TUI runs, the run's `WatchRun` is followed (reopened on the
//! next daemon when one dies): a `stop` asks the TUI to stop, then kills it
//! after the grace (S20), and once the TUI is over the run's last notices
//! and its end are read, for a while. SIGHUP and SIGTERM stop the TUI too
//! (ruling 114); a SIGINT is the terminal's, and the engine's.
//!
//! Nothing here opens the store in the command's process (T1).

use std::future::Future;
use std::path::{Path, PathBuf};
use std::pin::Pin;
use std::sync::{Arc, Mutex};
use std::task::Poll;
use std::time::{Duration, Instant};

use angzarr_embedded::angzarr_store_sqlite::SqliteStore;
use angzarr_embedded::v1::{event_page, CommandBook, Cover, Uuid};
use angzarr_embedded::Payload;
use daemon_application::{
    FrontendReports, Frontended, HostRequest, Seen, TerminalHost, TerminalTold, Waits,
};
use daemon_ports::{Called, DaemonApi, Daemons, RunEvents};
use daemon_proto::v1;
use host_launcher::HostLauncher;
use library_application::{Flags, Notice};
use run_application::frontend::{Frontend, Prepared, Report, Reported, Reports, Terminal};
use run_application::{client, Done, Ended, OneShot, RunError};
use run_ports::{BoxFuture, Edge, Launch, LaunchAt, LaunchFailure, Limits, Running};
use session_ports::{Held, LockError, SessionLock};

use crate::daemon::Reach;
use crate::oneshot::{hosted_setup, runs_over, Driven, Hosted};
use crate::signals::{self, Heard};
use crate::Wiring;

/// How long, once the TUI is over and its exit reported, the run's last
/// notices and its end are waited for.
const LAST_WORD: Duration = Duration::from_secs(10);

/// How long a broken watch waits before it is opened again on the daemon
/// that serves then.
const WATCH_AGAIN: Duration = Duration::from_millis(250);

/// How often a frontend looks for a stop `ctxloom shutdown` asked of it,
/// and for a notice it was told.
const STOP_LOOK: Duration = Duration::from_millis(200);

/// How often a frontend asks the daemon for the permission asks waiting
/// under its session. An ask waits minutes (`approval_timeout`, five by
/// default) for a human who is in another program, so a second's lateness
/// costs nothing, and the daemon answers from its in-memory index; no
/// stream of asks exists, and a look at every `STOP_LOOK` would be five
/// calls a second for the length of a session.
const ASK_LOOK: Duration = Duration::from_secs(1);

/// The stack of the thread a prepared session is driven on, as a main
/// thread's is.
const SESSION_STACK: usize = 16 * 1024 * 1024;

/// The runtime the frontend runs on: multi-thread, so the run's watch and
/// its reports make progress while the TUI is waited for on a blocking
/// thread ([`run_ports::Running::finish`] blocks).
pub(crate) fn run_runtime() -> std::io::Result<tokio::runtime::Runtime> {
    crate::daemon::runtime::build(1)
}

// ----------------------------------------------------------- the daemon's

/// What prepares the daemon's interactive sessions (U5): each on a thread of
/// its own, over the daemon's ONE store (its core shared), in the
/// environment and directory its client sent (S7).
pub(crate) struct HostedTerminals {
    pub(crate) core: crate::SharedCore<SqliteStore>,
    /// The daemon's runtime, which each session is driven on.
    pub(crate) runtime: tokio::runtime::Handle,
    pub(crate) home: PathBuf,
    pub(crate) temp: PathBuf,
}

impl TerminalHost for HostedTerminals {
    // Spawns the thread a prepared session runs on; the acceptance tests
    // drive it (run.feature's daemon Rule, j002700).
    #[cfg_attr(test, mutants::skip)]
    fn host(&self, request: HostRequest, told: TerminalTold) {
        let core = self.core.clone();
        let runtime = self.runtime.clone();
        let (home, temp) = (self.home.clone(), self.temp.clone());
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
                            told.clone(),
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
    told: TerminalTold,
) -> Done<Ended> {
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
        waits: Arc::new(Waited(Instant::now())),
        locks: Arc::new(liveness::RunLockFiles::new(Arc::clone(&fs), places.0)),
        project_dir: project_dir.display().to_string(),
        lock_path: Arc::new(move |harp: &str| locks.path(harp).display().to_string()),
        finder: Arc::new(HostLauncher::default().searching(Arc::clone(&fs))),
    };
    let driven = Driven {
        runner,
        locks: Box::new(Unheld),
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

// --------------------------------------------------------- the frontend's

/// `ctxloom run` (and init's interview) as the frontend of the session the
/// user's daemon prepares (U5, T9): see the module.
pub(crate) fn client_terminal(
    wiring: &Wiring,
    stamp: &str,
    flags: &Flags,
    request: &OneShot,
) -> Done<Ended> {
    let refused = |error: RunError| Done {
        result: Err(error),
        notices: Vec::new(),
    };
    if let Err(e) = wiring.require_project_root() {
        return refused(crate::oneshot::failed(cli_support::Failure::new(
            e,
            cli_support::Exit::REFUSED,
        )));
    }
    // Resolved before the run's runtime starts: the resolution blocks on a
    // runtime of its own.
    let parent = match crate::daemon::library_parent(wiring, stamp, crate::Minting::AtRoot) {
        Ok(parent) => parent,
        Err(failure) => return refused(crate::oneshot::failed(failure)),
    };
    let runtime = match run_runtime() {
        Ok(runtime) => runtime,
        Err(e) => {
            return refused(RunError::NotStarted {
                detail: e.to_string(),
            })
        }
    };
    let (stop, heard) = (host_launcher::Stop::new(), Heard::default());
    // Its frontend lock, held for its life (owner, 2026-11-15): how
    // `ctxloom shutdown` finds it, and asks it to stop.
    let frontends = liveness::FrontendLocks::new(wiring.fs(), wiring.home());
    let pid = std::process::id();
    let held = frontends.hold(pid);
    let mut done = runtime.block_on(async {
        let mut said = Vec::new();
        let stopped = std::sync::Arc::new(Mutex::new(None::<StopCause>));
        // The watch runs IN this future, on the command's main thread, never
        // spawned: it writes its notices to stderr, whose std lock that
        // thread holds for the command's whole run (the binary's `main`), so
        // from the runtime's one worker the write would wait for the
        // command's end, and the worker with it: the signals it hears (ruling
        // 114), the stop, the session itself (2026-11-15: a frontend told of
        // a drain hung for good, SIGTERM ignored).
        let watching = match &held {
            Ok(_) => Some(watch_for_stop(
                liveness::FrontendLocks::new(wiring.fs(), wiring.home()),
                pid,
                stop.clone(),
                std::sync::Arc::clone(&stopped),
            )),
            Err(e) => {
                said.push(Notice::text(format!(
                    "cannot hold this frontend's lock ({e:?}): `ctxloom shutdown` will not find it"
                )));
                None
            }
        };
        // Made on the runtime, before anything is launched.
        let hearing = match wiring.fs().os().signals(&signals::HEARD) {
            Ok(signals) => Some(tokio::spawn(signals::forward(
                signals,
                &signals::STOP_SESSION,
                stop.clone(),
                heard.clone(),
            ))),
            Err(e) => {
                said.push(Notice::text(format!(
                    "cannot listen for signals ({e}): a signal ends ctxloom without stopping the engine"
                )));
                None
            }
        };
        let asked = client::Asked {
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
        let mut done = beside(terminal(wiring, stamp, &asked, &stop), watching).await;
        if let Some(hearing) = hearing {
            hearing.abort();
        }
        let cause = *stopped.lock().unwrap_or_else(std::sync::PoisonError::into_inner);
        if let Some(cause) = cause {
            said.push(Notice::text(cause.said().to_string()));
        }
        said.append(&mut done.notices);
        done.notices = said;
        done
    });
    drop(held);
    heard.overrule(&mut done.result);
    done
}

/// `main`'s output, `aside` polled beside it until it ends or `main` does
/// (then dropped), both on the polling thread.
async fn beside<T>(
    main: impl std::future::Future<Output = T>,
    aside: Option<impl std::future::Future<Output = ()>>,
) -> T {
    let mut main = std::pin::pin!(main);
    let mut aside = std::pin::pin!(aside);
    std::future::poll_fn(|cx| {
        if let Some(running) = aside.as_mut().as_pin_mut() {
            if running.poll(cx).is_ready() {
                aside.set(None);
            }
        }
        main.as_mut().poll(cx)
    })
    .await
}

/// Why a frontend stopped its TUI by itself.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum StopCause {
    /// `ctxloom shutdown` asked it to (its stop file).
    Asked,
    /// The drain it was told of ended (its notice's `stop_at`).
    DrainEnded,
}

impl StopCause {
    /// What the session's end says of why (the `DrainEnded` sentence is a
    /// DRAFT, OD-63).
    fn said(self) -> &'static str {
        match self {
            StopCause::Asked => {
                "asked to stop by `ctxloom shutdown`: the session's engine was stopped"
            }
            StopCause::DrainEnded => {
                "the drain ctxloom was told of ended: the session's engine was stopped"
            }
        }
    }
}

/// What a frontend watching for a stop has been told (owner, 2026-11-15,
/// OD-63): the notice it showed last, whose `stop_at` stands even once its
/// file is gone.
#[derive(Default)]
struct Watch {
    told: Option<liveness::FrontendNotice>,
}

impl Watch {
    /// One look, at `now` (seconds since the epoch): whether a stop was
    /// asked, and the notice its file holds. What to show its human (a
    /// notice not shown yet), and why to stop, if it should.
    fn look(
        &mut self,
        stop_asked: bool,
        notice: Option<liveness::FrontendNotice>,
        now: u64,
    ) -> (Option<String>, Option<StopCause>) {
        let show = match notice {
            Some(notice) if self.told.as_ref() != Some(&notice) => {
                let text = notice.text.clone();
                self.told = Some(notice);
                Some(text)
            }
            _ => None,
        };
        let ended = self
            .told
            .as_ref()
            .and_then(|told| told.stop_at)
            .is_some_and(|at| at <= now);
        let cause = if stop_asked {
            Some(StopCause::Asked)
        } else if ended {
            Some(StopCause::DrainEnded)
        } else {
            None
        };
        (show, cause)
    }
}

/// `text` as a line of ctxloom's on this terminal's standard error (which
/// its human sees above the TUI). Written by the command's main thread
/// alone (see `client_terminal`).
fn show_on_stderr(text: &str) {
    use std::io::Write;
    let mut err = std::io::stderr();
    let _ = writeln!(err, "\r\nctxloom: {text}\r");
    let _ = err.flush();
}

/// The terminal's bell, rung on its standard error: a control character
/// that draws nothing, so it cannot land in the TUI's input box as a line
/// would. Written by the command's main thread alone.
fn ring_bell() {
    use std::io::Write;
    let mut err = std::io::stderr();
    let _ = err.write_all(b"\x07");
    let _ = err.flush();
}

/// Watch for what `ctxloom shutdown` (or a daemon's SIGTERM drain) tells
/// or asks of the frontend `pid`: each notice told is shown at once on its
/// terminal (its standard error, which its human sees above the TUI); once
/// a stop is asked, or the drain told of ends, `stop` is requested (the TUI
/// asked to stop, then killed after its grace, as a SIGTERM to the frontend
/// does, ruling 114) and `stopped` says why.
async fn watch_for_stop(
    frontends: liveness::FrontendLocks,
    pid: u32,
    stop: host_launcher::Stop,
    stopped: Arc<Mutex<Option<StopCause>>>,
) {
    let mut watch = Watch::default();
    loop {
        tokio::time::sleep(STOP_LOOK).await;
        let (show, cause) = watch.look(
            frontends.stop_asked(pid),
            frontends.notice(pid),
            crate::daemon::shutdown::epoch_seconds(),
        );
        if let Some(text) = show {
            show_on_stderr(&text);
        }
        if let Some(cause) = cause {
            *stopped
                .lock()
                .unwrap_or_else(std::sync::PoisonError::into_inner) = Some(cause);
            stop.request();
            return;
        }
    }
}

/// The session: created, then started and followed (see the module).
async fn terminal(
    wiring: &Wiring,
    stamp: &str,
    asked: &client::Asked,
    stop: &host_launcher::Stop,
) -> Done<Ended> {
    let refused = |error: RunError, notices: Vec<Notice>| Done {
        result: Err(error),
        notices,
    };
    // H3: reaching the daemon delivers the home's holds first, at the
    // session's start (`Reach`'s flush on the way in).
    let reach = Reach::new(wiring, stamp);
    let api = match reach.reach().await {
        Ok(api) => api,
        Err(unreached) => return refused(RunError::Unreached(unreached), Vec::new()),
    };
    // U5's start-report race (owner 2026-11-14): the run's own lock, taken
    // before the daemon is asked to plan and journaled as the run's launcher
    // lock, so a daemon started before the start report is recorded reads
    // the run's owner alive; let go once it is.
    let run_lock = match run_ports::RunLocks::hold_run(
        &liveness::RunLockFiles::new(wiring.fs(), wiring.home()),
        &asked.request_id,
    ) {
        Ok(lock) => lock,
        Err(error) => return refused(RunError::Lock(error), Vec::new()),
    };
    let mut create = client::create_request(asked);
    create.mode = v1::run::Mode::Terminal as i32;
    create.launcher_lock = run_lock.path().to_string();
    let run = match api.create_run(create).await {
        Ok(run) => run,
        Err(called) => return refused(RunError::Called(Box::new(called)), Vec::new()),
    };
    let mut notices: Vec<Notice> = run.notices.iter().map(client::noticed).collect();
    if let Some(end) = &run.end {
        let error = client::error_of(end).unwrap_or(RunError::NotStarted {
            detail: "the daemon ended the session before its launch".to_string(),
        });
        return refused(error, notices);
    }
    let Some(launch) = run.launch.as_ref().and_then(daemon_api::launch::from_wire) else {
        return refused(
            RunError::NotStarted {
                detail: "the daemon answered no launch".to_string(),
            },
            notices,
        );
    };
    #[cfg(feature = "test-support")]
    record_launch(wiring, &launch);
    #[cfg(feature = "test-support")]
    wait_at_the_gate(wiring);
    let prepared = Prepared {
        run: run.name.clone(),
        session: run.session.clone(),
        launch,
    };
    let reports = DaemonReports {
        env: Arc::clone(wiring.env()),
        fs: wiring.fs(),
        home: wiring.home().to_path_buf(),
        own: crate::daemon::own_stamp(wiring.env(), stamp).to_string(),
        session: prepared.session.clone(),
    };
    let tty = Tty(HostLauncher::stopped_by(stop.clone()));
    let mut locks = liveness::LockFiles::new(wiring.fs(), project_of(&prepared.launch, wiring));
    let over = Over::default();
    let followed = Mutex::new(Followed::default());
    let session = async {
        let mut frontend = Frontend {
            locks: &mut locks,
            terminal: &tty,
            reports: &reports,
            limits: Limits::new(crate::oneshot::TURN_DEADLINE),
            run_lock: Some(run_lock),
        };
        let done = frontend.run(&prepared).await;
        over.set();
        done
    };
    let watched = both(
        follow(&reach, &run.name, stop, &over, &followed),
        tell_of_asks(&reach, &run.session, &over, wiring),
    );
    let (mut done, _) = both(session, watched).await;
    // H3: once the exit is reported, what the session's hooks held is
    // delivered too; what it set aside is said as the command ends.
    let flushed = crate::daemon::flush::flush_holds(wiring, stamp);
    crate::daemon::flush::to_say(crate::daemon::flush::notices(&flushed));
    let followed = followed.into_inner().unwrap_or_else(|e| e.into_inner());
    notices.extend(followed.notices);
    notices.append(&mut done.notices);
    done.notices = notices;
    // The daemon's own word on the run, once the TUI is over: an error of
    // its (the run's end not recorded) is the command's.
    if let Some(error) = followed.end.as_ref().and_then(client::error_of) {
        if done.result.is_ok() {
            done.result = Err(error);
        }
    }
    done
}

/// The project whose session the launch's lock is of
/// ([`session_ports::lock_project`]), else this command's.
fn project_of(launch: &Launch, wiring: &Wiring) -> PathBuf {
    session_ports::lock_project(Path::new(&launch.session_lock))
        .map_or_else(|| wiring.project_dir(), Path::to_path_buf)
}

/// The TUI on this terminal: `run_terminal`, its wait on a blocking thread.
struct Tty(HostLauncher);

impl Terminal for Tty {
    fn start(&self, launch: &Launch, limits: &Limits) -> Result<Box<dyn Running>, LaunchFailure> {
        run_ports::RunnerLauncher::start(&self.0, launch, limits)
    }

    fn wait(
        &self,
        running: Box<dyn Running>,
    ) -> BoxFuture<'_, Result<run_ports::TurnOutput, LaunchFailure>> {
        Box::pin(async move {
            tokio::task::spawn_blocking(move || running.finish())
                .await
                .unwrap_or_else(|e| {
                    Err(LaunchFailure {
                        deterministic: false,
                        detail: e.to_string(),
                    })
                })
        })
    }
}

/// How long one report may take in all: finding (or starting) the daemon
/// and delivering, a hook's bound (S16).
const REPORT_BOUND: Duration = crate::daemon::HOOK_BOUND;

/// The frontend's reports (slice 4, a daemon dying under a live TUI): each
/// ONE attempt, as a hook delivers, to the daemon serving this home (found,
/// or started and waited for), behind what the session's hold holds (it is
/// delivered first, in order), and HELD in the session's hold when no
/// up-to-date daemon takes it (`runs_application::Holding::report`).
struct DaemonReports {
    env: Arc<process_env::EnvSnapshot>,
    fs: Arc<dyn file_system::Fs>,
    home: PathBuf,
    /// This binary's stamp (a test's under test-support).
    own: String,
    /// The session's resource name.
    session: String,
}

impl Reports for DaemonReports {
    fn deliver<'a>(&'a self, launch: &'a Launch, report: &'a Report) -> BoxFuture<'a, Reported> {
        let (env, fs, home) = (
            Arc::clone(&self.env),
            Arc::clone(&self.fs),
            self.home.clone(),
        );
        let (own, session) = (self.own.clone(), self.session.clone());
        let book = report_book(launch, report);
        let (root, incarnation) = (launch.at.session.clone(), launch.at.incarnation);
        Box::pin(async move {
            // A delivery blocks (its sink has a runtime of its own), and the
            // spawn wait sleeps: a blocking thread.
            tokio::task::spawn_blocking(move || {
                let held = fact_spool::Spool::at_home(fs, &home);
                ReportAt {
                    env: &env,
                    held: &held,
                    own: &own,
                    session: &session,
                    root: &root,
                }
                .deliver(book, incarnation)
            })
            .await
            .unwrap_or_else(|e| Reported::Lost {
                why: format!("the report's thread failed: {e}"),
            })
        })
    }
}

/// One report's attempt, over the session's hold.
struct ReportAt<'a, H: run_ports::HeldFacts> {
    env: &'a Arc<process_env::EnvSnapshot>,
    held: &'a H,
    own: &'a str,
    /// The session's resource name.
    session: &'a str,
    /// The session's stable id (text).
    root: &'a str,
}

impl<H: run_ports::HeldFacts> ReportAt<'_, H> {
    fn deliver(&self, book: CommandBook, incarnation: u32) -> Reported {
        let clock = crate::capture::Deadline(Instant::now() + REPORT_BOUND);
        let found = crate::daemon::hook_daemon(self.env, crate::capture::spawn_wait(&clock));
        let item = runs_application::RunReport {
            external_id: external_id_of(&book),
            book,
            incarnation,
        };
        let root = uuid::Uuid::parse_str(self.root).unwrap_or_default();
        let session = Uuid::from_bytes(*root.as_bytes());
        let holding = runs_application::Holding::new(self.held, &clock, self.own, self.session);
        let report = match &found {
            Some(found) => {
                let sink = crate::capture::daemon_sink(found.clone());
                holding.report(
                    &session,
                    std::slice::from_ref(&item),
                    runs_application::Serving::Daemon {
                        stamp: &found.stamp,
                        sink: &sink,
                    },
                )
            }
            None => holding.report::<hook_sink_grpc::GrpcSink>(
                &session,
                std::slice::from_ref(&item),
                runs_application::Serving::Unreached {
                    why: format!(
                        "no ctxloom daemon could be reached or started within {} s",
                        REPORT_BOUND.as_secs()
                    ),
                },
            ),
        };
        // What its flush of the session's hold set aside is said as the
        // command ends, as a command's flush says it (H3).
        let flushed = crate::daemon::flush::Flushed::Done(vec![runs_application::SessionFlush {
            root,
            session: Some(self.session.to_string()),
            flushed: report.flushed.clone(),
            left: runs_application::Left::Nothing,
        }]);
        crate::daemon::flush::to_say(crate::daemon::flush::notices(&flushed));
        reported_of(report.own.first())
    }
}

/// What became of the report, as the frontend is told it.
fn reported_of(kept: Option<&runs_application::Kept>) -> Reported {
    use runs_application::Outcome;
    let Some(kept) = kept else {
        return Reported::Lost {
            why: "it was not made".to_string(),
        };
    };
    match &kept.outcome {
        Outcome::Delivered(delivery) => Reported::Delivered(delivery.clone()),
        Outcome::Held(why) => Reported::Held { why: held_why(why) },
        Outcome::HeldBehind => Reported::Held {
            why: "behind what the session's hold already holds".to_string(),
        },
        Outcome::NotHeld(not) => Reported::Lost {
            why: not_held_why(not),
        },
    }
}

/// Why a report was held, as the frontend says it.
fn held_why(why: &runs_application::Why) -> String {
    use runs_application::Why;
    match why {
        Why::Older { daemon, own } => {
            format!("the daemon serving ({daemon}) is older than this ctxloom ({own})")
        }
        Why::Unreached { why } => why.clone(),
        Why::Unsettled(_) => "the daemon did not answer it".to_string(),
        Why::OutOfTime => "its time was spent".to_string(),
    }
}

/// Why a report was neither recorded nor held.
fn not_held_why(not: &runs_application::NotHeld) -> String {
    use runs_application::NotHeld;
    match not {
        NotHeld::Full(_) => "the session's hold is full".to_string(),
        NotHeld::Failed(error) => error.to_string(),
        NotHeld::Foreign => "it is not a fact of this session".to_string(),
    }
}

/// The external id a fact's book carries.
fn external_id_of(book: &CommandBook) -> String {
    match book.page.as_ref().and_then(|page| page.body.as_ref()) {
        Some(angzarr_embedded::v1::command_page::Body::Fact(fact)) => fact
            .origin
            .as_ref()
            .map(|origin| origin.external_id.clone())
            .unwrap_or_default(),
        _ => String::new(),
    }
}

/// Under `test-support`, the frontend waits, before it takes the session's
/// lock and starts the TUI, while `<ctxloom home>/.ctxloom/test-frontend-gate`
/// is there (at most a minute): a test kills the daemon between the plan
/// and the start report (U5's start-report race), forced, never timed.
/// Compiled out of the shipped binary.
#[cfg(feature = "test-support")]
fn wait_at_the_gate(wiring: &Wiring) {
    let gate = wiring.home().join(".ctxloom/test-frontend-gate");
    let until = Instant::now() + Duration::from_secs(60);
    while wiring.fs().exists(&gate).unwrap_or(false) && Instant::now() < until {
        std::thread::sleep(Duration::from_millis(50));
    }
}

/// The fact a report is, on the run's stream, keyed by its run as the
/// launcher keys its own (`started:<run>`, `exit:<run>`,
/// `launch-failed:<run>`): a report the daemon also records is one fact.
fn report_book(launch: &Launch, report: &Report) -> CommandBook {
    let id = |text: &str| {
        let id = uuid::Uuid::parse_str(text).unwrap_or_default();
        Uuid::from_bytes(*id.as_bytes())
    };
    let who = runs_application::Identity {
        run: id(&launch.at.run),
        session: id(&launch.at.session),
        incarnation: launch.at.incarnation,
        harp: launch.at.harp.clone(),
        depth: 0,
    };
    match report {
        Report::Started { pid } => {
            runs_application::RunnerFact::RunnerStarted(runs::RecordRunnerStarted {
                runtime: launch.engine.clone(),
                workspace: launch.cwd.clone().unwrap_or_default(),
                pid: *pid,
            })
            .book(&who)
        }
        Report::Exited { code, signal } => {
            runs_application::RunnerFact::RunExited(runs::RecordRunExited {
                code: code.unwrap_or(0),
                signal: signal.unwrap_or(0),
            })
            .book(&who)
        }
        Report::NotStarted {
            deterministic,
            detail,
        } => runs_application::launch_failed_book(&who.run, *deterministic, detail),
    }
}

/// Whether the TUI is over, once it is.
#[derive(Default)]
struct Over(std::sync::atomic::AtomicBool, tokio::sync::Notify);

impl Over {
    fn set(&self) {
        self.0.store(true, std::sync::atomic::Ordering::SeqCst);
        self.1.notify_waiters();
    }

    fn is_set(&self) -> bool {
        self.0.load(std::sync::atomic::Ordering::SeqCst)
    }

    /// Once the TUI is over.
    async fn ended(&self) {
        while !self.is_set() {
            let notified = self.1.notified();
            if self.is_set() {
                break;
            }
            notified.await;
        }
    }

    /// Once the TUI is over, and [`LAST_WORD`] after.
    async fn and_last_word(&self) {
        self.ended().await;
        tokio::time::sleep(LAST_WORD).await;
    }
}

/// What the run's watch said: its notices and its end.
#[derive(Default)]
struct Followed {
    notices: Vec<Notice>,
    end: Option<v1::RunEnd>,
}

/// Follow the run `name` for the TUI's life, and its last word after: a
/// `stop` asks the TUI to stop (`stop`); a broken watch (its daemon gone) is
/// opened again on the next daemon; a daemon that does not know the run (a
/// new one) has nothing more to say of it.
async fn follow(
    reach: &Reach<'_>,
    name: &str,
    stop: &host_launcher::Stop,
    over: &Over,
    followed: &Mutex<Followed>,
) {
    let mut until: Pin<Box<dyn Future<Output = ()> + Send + '_>> = Box::pin(over.and_last_word());
    loop {
        let opened = async {
            let api = reach.reach().await.ok()?;
            Some(
                api.watch_run(v1::WatchRunRequest {
                    name: name.to_string(),
                })
                .await,
            )
        };
        let events = match first(Box::pin(opened), &mut until).await {
            First::Right(()) => return,
            First::Left(Some(Ok(events))) => events,
            // Not this daemon's run (a new daemon), or not served: nothing
            // more will be said of it.
            First::Left(Some(Err(Called::Refused { .. } | Called::Unimplemented { .. }))) => return,
            First::Left(_) => {
                if over.is_set() {
                    return;
                }
                tokio::time::sleep(WATCH_AGAIN).await;
                continue;
            }
        };
        if heard_out(events, stop, &mut until, followed).await {
            return;
        }
        if over.is_set() {
            return;
        }
        tokio::time::sleep(WATCH_AGAIN).await;
    }
}

/// Read `events` until their end (true), the bound `until` (true), or a
/// broken stream (false).
async fn heard_out(
    mut events: Box<dyn RunEvents>,
    stop: &host_launcher::Stop,
    until: &mut Pin<Box<dyn Future<Output = ()> + Send + '_>>,
    followed: &Mutex<Followed>,
) -> bool {
    loop {
        let next = match first(events.next(), until).await {
            First::Right(()) => return true,
            First::Left(next) => next,
        };
        let Some(Ok(event)) = next else {
            return false;
        };
        let mut followed = followed.lock().unwrap_or_else(|e| e.into_inner());
        match event.event {
            Some(v1::run_event::Event::Stop(_)) => stop.request(),
            Some(v1::run_event::Event::Notice(notice)) => {
                followed.notices.push(client::noticed(&notice));
            }
            Some(v1::run_event::Event::End(end)) => {
                followed.end = Some(end);
                return true;
            }
            Some(v1::run_event::Event::Reply(_)) | None => {}
        }
    }
}

/// For the TUI's life, look every [`ASK_LOOK`] at the permission asks
/// waiting in `session`'s whole project (whatever their root: a child's,
/// a one-shot's or another session's), and tell the human of each new one, once: the bell
/// ([`frontend_asks`]): the bell, and no line, for the TUI owns the terminal
/// (the count is the statusline's). It runs in the command's main-thread
/// future beside the TUI, never spawned, as the stop watch does: it writes
/// to stderr, whose std lock that thread holds. It
/// asks the daemon serving and starts none (a daemon that is not there has
/// no asks to tell of), and says nothing of a look that failed: the next is
/// a second away.
async fn tell_of_asks(reach: &Reach<'_>, session: &str, over: &Over, wiring: &Wiring) {
    let Some(listing) = crate::frontend_asks::project_listing(session) else {
        return;
    };
    let mut ended: Pin<Box<dyn Future<Output = ()> + Send + '_>> = Box::pin(over.ended());
    let mut told = crate::frontend_asks::AsksTold::default();
    loop {
        if let First::Right(()) = first(Box::pin(tokio::time::sleep(ASK_LOOK)), &mut ended).await {
            return;
        }
        let look = async {
            let api = reach.serving().await.ok()??;
            api.list_approvals(listing.clone()).await.ok()
        };
        let listed = match first(Box::pin(look), &mut ended).await {
            First::Right(()) => return,
            First::Left(listed) => listed,
        };
        let Some(listed) = listed else { continue };
        tell_news(&mut told, &listed.approvals, wiring, ring_bell);
    }
}

/// One look's outcome: each ask in `waiting` not told of yet is rung (the
/// bell, once) and recorded as told, then the look itself is recorded.
fn tell_news(
    told: &mut crate::frontend_asks::AsksTold,
    waiting: &[v1::Approval],
    wiring: &Wiring,
    mut ring: impl FnMut(),
) {
    for ask in told.news(waiting) {
        ring();
        record_ask(wiring, &format!("told {ask}"));
    }
    record_ask(wiring, "look");
}

/// Under `test-support`, a line of the frontend's watch for asks appended to
/// `<ctxloom home>/.ctxloom/test-frontend-asks` (`look` after each look,
/// `told <ask>` after each ask rung), which the acceptance tests wait on.
/// Compiled out of the shipped binary.
#[cfg(feature = "test-support")]
fn record_ask(wiring: &Wiring, line: &str) {
    let path = wiring.home().join(".ctxloom/test-frontend-asks");
    let mut held = wiring.fs().read_to_string(&path).unwrap_or_default();
    held.push_str(line);
    held.push('\n');
    let _ = wiring.fs().write_atomic(
        &path,
        held.as_bytes(),
        file_system::Perms::Exact(os::Mode::from_bits(0o600)),
    );
}

#[cfg(not(feature = "test-support"))]
fn record_ask(_wiring: &Wiring, _line: &str) {}

/// Which of two futures was ready first.
enum First<A, B> {
    Left(A),
    Right(B),
}

/// Poll `left` and `right` until one is ready; `right` is left as it was,
/// to be polled again.
async fn first<A, B>(
    mut left: Pin<Box<dyn Future<Output = A> + Send + '_>>,
    right: &mut Pin<Box<dyn Future<Output = B> + Send + '_>>,
) -> First<A, B> {
    std::future::poll_fn(|cx| {
        if let Poll::Ready(a) = left.as_mut().poll(cx) {
            return Poll::Ready(First::Left(a));
        }
        if let Poll::Ready(b) = right.as_mut().poll(cx) {
            return Poll::Ready(First::Right(b));
        }
        Poll::Pending
    })
    .await
}

/// Both futures, polled together, to their ends.
async fn both<A, B>(a: impl Future<Output = A>, b: impl Future<Output = B>) -> (A, B) {
    let (mut a, mut b) = (std::pin::pin!(a), std::pin::pin!(b));
    let (mut left, mut right) = (None, None);
    std::future::poll_fn(|cx| {
        if left.is_none() {
            if let Poll::Ready(value) = a.as_mut().poll(cx) {
                left = Some(value);
            }
        }
        if right.is_none() {
            if let Poll::Ready(value) = b.as_mut().poll(cx) {
                right = Some(value);
            }
        }
        match (left.take(), right.take()) {
            (Some(l), Some(r)) => Poll::Ready((l, r)),
            (l, r) => {
                left = l;
                right = r;
                Poll::Pending
            }
        }
    })
    .await
}

/// Under `test-support`, a terminal stood in (`CTXLOOM_TEST_TERMINAL`),
/// the launch the frontend was handed, written to
/// `<ctxloom home>/.ctxloom/test-frontend-launch.json`: the acceptance tests
/// compare the TUI's program, arguments, environment and directory with it.
/// Compiled out of the shipped binary.
#[cfg(feature = "test-support")]
fn record_launch(wiring: &Wiring, launch: &Launch) {
    if wiring.env().non_empty("CTXLOOM_TEST_TERMINAL").is_none() {
        return;
    }
    let record = serde_json::json!({
        "program": launch.program,
        "args": launch.args,
        "env": launch.env,
        "cwd": launch.cwd,
        "session_home": launch.session_home,
        "session_lock": launch.session_lock,
    });
    let path = wiring.home().join(".ctxloom/test-frontend-launch.json");
    let _ = wiring.fs().write_atomic(
        &path,
        record.to_string().as_bytes(),
        file_system::Perms::Exact(os::Mode::from_bits(0o600)),
    );
}

#[cfg(test)]
#[path = "frontend.test.rs"]
mod tests;
