//! `ctxloom daemon serve`: the user's daemon (D18), one per user, over the
//! ONE store that holds every project's streams (T14). Only the holder of
//! the single-instance lock serves (S-e: the one policy runner).
//!
//! This is composition alone (T18): the launcher is `daemon-application`'s
//! [`Daemon`], given its ports here (the engine registry, the child's plan by
//! its project's dry run, the hand-off files, the session directories, the
//! pump that hears each child, the run locks and the tokens). What stays
//! here is the root's: the single-instance lock, the runtime, the store and its core
//! (the read models registered in its kernel, S32), the listener and its
//! certificate, the discovery file, and the pace: the launcher's start
//! (the orphan sweep) first, then a round at the first commit through this
//! process's store handles (`CommitNotify`, the push) or after the poll
//! interval, whichever is first (another process's commit reaches no
//! in-process signal, so the poll stays, S-N4). The daemon exits when asked
//! (`daemon stop`), when another daemon's discovery replaced its own (it
//! lost the single-instance lock), or once it has hosted nothing, had nothing to do and
//! known no run started and not ended (M9: any process's) for its idle
//! time.

use std::collections::BTreeMap;
use std::net::{IpAddr, Ipv4Addr, Ipv6Addr, SocketAddr};
use std::path::{Path, PathBuf};
use std::sync::mpsc::{channel, Receiver, Sender};
use std::sync::{Arc, Mutex, PoisonError};
use std::time::{Duration, Instant};

use angzarr_embedded::angzarr_ports::CommitNotify;
use angzarr_embedded::angzarr_store_sqlite::SqliteStore;
use credentials::OsTokenSource;
use ctxloom_store_sqlite::Stores;
use daemon_application::{
    ChildHomes, ChildPlans, Daemon, Ears, Folds, HandOffs, OneShots, Place as Told, Ports,
};
use file_system::Fs;
use hook_listener::{HookListener, TlsIdentity};
use run_ports::{CertMint, CertNames, ChildObservation, Discovery, SessionRecords, TokenSource};
use runs_application::UserGate;
use session_files::SessionDirs;

use super::files::{DaemonFiles, Instance, Unheld};
use crate::{InProcessCore, Pace, Projectors};

/// How long a daemon starting waits for the single-instance lock.
const INSTANCE_WAIT: Duration = Duration::from_secs(5);

/// How long a daemon exiting waits for each one-shot it stopped to end:
/// the stop's grace (five seconds), and its end's recording.
const ONE_SHOT_END_WAIT: Duration = Duration::from_secs(10);

/// Where the daemon is: the ctxloom home, the temporary root, the
/// operating system's name, the user's own home (`Wiring::user_home`) and
/// the binary's full version stamp (M3).
pub(crate) struct Place {
    pub(crate) stamp: String,
    pub(crate) home: PathBuf,
    /// The single-instance lock's path (`instance_lock_path`).
    pub(crate) instance_lock: PathBuf,
    pub(crate) temp: PathBuf,
    pub(crate) os: String,
    pub(crate) user_home: Option<PathBuf>,
    /// The engines it hosts children on, composed as every launch's are
    /// (`commands::hosted_engines`).
    pub(crate) engines: Vec<daemon_application::HostedEngine>,
    /// The environment its children's checkouts' git runs in (s4-Q11).
    pub(crate) env: Arc<process_env::EnvSnapshot>,
}

/// The daemon's log, shared by the loop and the launcher.
type Say = Arc<Mutex<Box<dyn FnMut(&str) + Send>>>;

fn said(say: &Say, line: &str) {
    (say.lock().unwrap_or_else(PoisonError::into_inner))(line);
}

/// Why the daemon did not serve to its end.
#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) enum Unserved {
    /// Another daemon held the single-instance lock for the whole of
    /// [`INSTANCE_WAIT`]: that one serves (or is starting, or stopping).
    Held { lock: String },
    /// It could not start, or its run failed: why.
    Failed(String),
}

impl From<String> for Unserved {
    fn from(why: String) -> Unserved {
        Unserved::Failed(why)
    }
}

/// The single-instance lock, waited for within [`INSTANCE_WAIT`]; held by
/// another, the daemon declines ([`Unserved::Held`]).
fn instance(files: &DaemonFiles) -> Result<Instance, Unserved> {
    files
        .take_instance(INSTANCE_WAIT)
        .map_err(|unheld| match unheld {
            Unheld::Held { lock } => Unserved::Held { lock },
            Unheld::Failed(why) => {
                Unserved::Failed(format!("the single-instance lock cannot be taken: {why}"))
            }
        })
}

#[expect(clippy::disallowed_methods, reason = "the daemon is the one writer")]
/// Run the daemon in this process until it exits: the instance held, served,
/// advertised, swept, then ticking. `say` is its log.
pub(crate) fn serve(
    fs: Arc<dyn Fs>,
    place: Place,
    pace: Pace,
    say: Box<dyn FnMut(&str) + Send>,
) -> Result<(), Unserved> {
    let Place {
        stamp,
        home,
        instance_lock,
        temp,
        os,
        user_home,
        engines,
        env,
    } = place;
    let home = home.as_path();
    let files = DaemonFiles::with_instance(Arc::clone(&fs), home, instance_lock);
    let held = instance(&files)?;
    let say: Say = Arc::new(Mutex::new(say));
    let runtime = super::runtime::build(super::runtime::workers()).map_err(|e| e.to_string())?;
    let served = runtime.block_on(async move {
        let code = |e: &dyn coded_diagnostic::Diagnostic| e.code().to_string();
        let store = Stores::new(Arc::clone(&fs), home.to_path_buf())
            .open()
            .await
            .map_err(|e| code(&e))?;
        // The read models are registered in the kernel (S32), so a SIMPLE
        // request waits for them; the launcher catches the same folds up
        // for what other processes commit.
        let mut kept = Projectors::default();
        let core = InProcessCore::start_with(store, |store| {
            kept = Projectors::filled(store);
            kept.clone()
        })
        .await;
        // Under test-support, the kill switch at a step (U6), on every book
        // the core applies: the launcher's, its one-shots', its clients'.
        #[cfg(feature = "test-support")]
        dies_at_steps(&core.edge(), &files, &say);
        // What the loop shares with the API's use cases (slice 2 step 5).
        let one_shots = one_shots_over(&core, home, &temp);
        let shared = shared_over(&kept, &one_shots, (&fs, home), pace);
        let terminals = terminals_over(&core, home, &temp);
        // Listening for SIGTERM BEFORE it advertises: a signal sent as the
        // advertisement is seen must not meet the default disposition.
        let terminated = hear_terminate((&fs, home), &shared, &say);
        let started = start(
            &fs,
            (home, &temp),
            &core,
            &kept,
            (os, user_home, stamp, pace, engines),
            (&files, &shared, &one_shots, &terminals),
            (&say, &env),
        )
        .await;
        let (listener, mut launcher, own) = match started {
            Ok(started) => started,
            Err(detail) => {
                // A start that fails before it serves closes its store as
                // every exit does (ruling 131): what holds it is let go first.
                cut(terminated).await;
                drop((shared, kept, one_shots, terminals));
                said(&say, &format!("the start failed: {detail}"));
                match core.close().await {
                    Ok(()) => said(&say, "the store is closed"),
                    Err(unclosed) => said(&say, &format!("the store did not close: {unclosed}")),
                }
                return Err(detail);
            }
        };
        let endpoint = own.endpoint.clone();
        said(&say, &format!("serving at {endpoint}"));
        // Under test-support, held while `test-hold-at` names `serving`.
        #[cfg(feature = "test-support")]
        files.hold_at("serving");
        let scheduler = scheduler_over(&core, &kept, (pace, &files), &say);
        // A SIGTERM is a drain (owner, 2026-11-15), a second one the plain
        // stop (`--even-if-live`); heard for the daemon's life.
        let frontends = liveness::FrontendLocks::new(Arc::clone(&fs), home);
        let lifeline = Lifeline::of(&fs, &env);
        let result = run(
            &mut launcher,
            &core,
            (&files, &own, &held, &frontends),
            (pace, &shared, &terminals, &lifeline),
            &say,
        )
        .await;
        lifelong_ended(terminated, scheduler, &say).await;
        files.withdraw_own(&own);
        // Every holder of the store let go before the close (ruling 131):
        // the launcher (its edge and its folds), the edge, the listener's
        // connection and stream tasks (its stop returns once every copy of
        // its service is released), and the read models kept here, each of
        // which holds the store through its gap fill (`StoreFill`) for as
        // long as any copy of it lives; the kernel's copies go with the
        // core.
        launcher.end_all();
        // The one-shots it hosts were stopped with its children: each is
        // waited for, so its run's end is recorded and its copy of the core
        // let go before the store closes.
        if !one_shots.wait_ended(ONE_SHOT_END_WAIT) {
            said(
                &say,
                "a one-shot did not end in time: its run is left to the next sweep",
            );
        }
        drop(launcher);
        let _ = listener.stop().await;
        drop(shared);
        drop(kept);
        drop(one_shots);
        // A session a frontend still runs keeps its thread (and its copy of
        // the core) until its frontend reports: the store's close then says
        // so, and the frontend reports to the next daemon (S17).
        drop(terminals);
        said(&say, "exited");
        // Closed, not dropped (ruling 131).
        if let Err(unclosed) = core.close().await {
            said(&say, &format!("the store did not close: {unclosed}"));
        }
        result
    });
    // Its runtime let go within a bound, never waited on for good: a live
    // frontend's session keeps a blocking task (its `WatchRun`, waiting on
    // the session's thread, which waits on the frontend) that a plain drop
    // waits out for the frontend's life, and every SIGTERM meanwhile is
    // taken by the runtime's handler and heard by nothing (2026-11-15: a
    // test's daemon outlived its home 43 minutes; ruling 114). What is left
    // ends with the process.
    runtime.shutdown_timeout(RUNTIME_END);
    served.map_err(Unserved::Failed)
}

/// How long the daemon's exit waits for its runtime's blocking tasks.
const RUNTIME_END: Duration = Duration::from_secs(5);

/// How long the daemon's exit waits for its scheduler's tick to finish.
const SCHEDULER_END: Duration = Duration::from_secs(5);

/// The scheduler (step 8 U5a), for the daemon's life, over `core` and the
/// due model `kept` registered in its kernel, on the system clock, looking
/// at the store at the daemon's poll; its lines in the daemon's log. Its
/// alarms fire while the daemon drains too.
fn scheduler_over(
    core: &InProcessCore<SqliteStore>,
    kept: &Projectors,
    (pace, files): (Pace, &DaemonFiles),
    say: &Say,
) -> crate::scheduling::Scheduler {
    let say = Arc::clone(say);
    crate::scheduling::start(
        core.share(),
        kept.due.clone().unwrap_or_default(),
        scheduler_clock(files),
        pace.poll,
        Arc::new(move |line: &str| said(&say, line)),
    )
}

/// The scheduler's clock: the system's; under test-support, held while
/// the daemon's `test-hold-alarms` is there ([`DaemonFiles::alarms_held`]).
#[cfg(feature = "test-support")]
fn scheduler_clock(files: &DaemonFiles) -> Arc<dyn scheduler_ports::Clock> {
    Arc::new(HeldAlarms {
        files: files.clone(),
    })
}

/// The shipped scheduler's clock is the system's.
#[cfg(not(feature = "test-support"))]
fn scheduler_clock(_: &DaemonFiles) -> Arc<dyn scheduler_ports::Clock> {
    Arc::new(crate::SystemClock)
}

/// The system's clock, standing at the instant a hold of the alarms names
/// while it lasts (test-support).
#[cfg(feature = "test-support")]
struct HeldAlarms {
    files: DaemonFiles,
}

#[cfg(feature = "test-support")]
impl scheduler_ports::Clock for HeldAlarms {
    fn now(&self) -> angzarr_embedded::Timestamp {
        match self.files.alarms_held_at() {
            Some(seconds) => angzarr_embedded::Timestamp { seconds, nanos: 0 },
            None => scheduler_ports::Clock::now(&crate::SystemClock),
        }
    }
}

/// The SIGTERM listener cut and JOINED: an aborted task is dropped later by
/// its worker, and what it holds (the read models, so the store) must be let
/// go before the store closes.
async fn cut(terminated: Option<tokio::task::JoinHandle<()>>) {
    if let Some(terminated) = terminated {
        terminated.abort();
        let _ = terminated.await;
    }
}

/// What runs for the daemon's life, ended once its loop returns and before
/// anything else is let go: the SIGTERM listener, then the scheduler, whose
/// copy of the core goes before the store closes (ruling 131). A scheduler
/// that does not end in time is cut, and said.
async fn lifelong_ended(
    terminated: Option<tokio::task::JoinHandle<()>>,
    scheduler: crate::scheduling::Scheduler,
    say: &Say,
) {
    cut(terminated).await;
    if !scheduler.stop(SCHEDULER_END).await {
        said(say, "the scheduler did not stop in time: it was cut");
    }
}

/// What the loop shares with its API, its drain's period read from
/// `pace`'s default and `home`'s config.
fn shared_over(
    kept: &Projectors,
    one_shots: &Arc<OneShots>,
    (fs, home): (&Arc<dyn Fs>, &Path),
    pace: Pace,
) -> Arc<Shared> {
    Arc::new(
        Shared::new(
            kept.index.clone().unwrap_or_default(),
            Arc::clone(one_shots),
            Period {
                default: pace.hand_over,
                configured: configured_drain_timeout(fs, home),
            },
        )
        .watching(DaemonFiles::new(Arc::clone(fs), home)),
    )
}

/// The daemon's monotonic clock (OD-54): the time since its process made it.
struct ProcessClock(Instant);

impl daemon_application::Monotonic for ProcessClock {
    fn now(&self) -> Duration {
        self.0.elapsed()
    }
}

/// The home config's `daemon.drain_timeout` (owner, 2026-11-15), read
/// afresh each time it is asked ([`super::drain::configured_drain_timeout`]).
fn configured_drain_timeout(fs: &Arc<dyn Fs>, home: &Path) -> Reader {
    let (fs, home) = (Arc::clone(fs), home.to_path_buf());
    Box::new(move || super::drain::configured_drain_timeout(&fs, &home))
}

/// Whether the daemon is to ignore a stop and a drain: only under
/// test-support, while its switch is there.
#[cfg(feature = "test-support")]
fn deaf_to_stop(files: &DaemonFiles) -> bool {
    files.deaf_to_stop()
}

#[cfg(not(feature = "test-support"))]
fn deaf_to_stop(_: &DaemonFiles) -> bool {
    false
}

/// Under test-support, the lifeline to the test that started the daemon
/// (`CTXLOOM_TEST_LIFELINE`, `super::LIFELINE_VARIABLE`): a lock file the
/// test process holds for its life. Once the daemon can take that lock
/// (the kernel lets it go however the test ended, SIGKILL included, and a
/// pid reused names no lock) its test is gone. A plain build has none.
struct Lifeline {
    fs: Arc<dyn Fs>,
    path: Option<PathBuf>,
}

impl Lifeline {
    #[cfg(feature = "test-support")]
    fn of(fs: &Arc<dyn Fs>, env: &process_env::EnvSnapshot) -> Lifeline {
        Lifeline {
            fs: Arc::clone(fs),
            path: env.non_empty(super::LIFELINE_VARIABLE).map(PathBuf::from),
        }
    }

    #[cfg(not(feature = "test-support"))]
    fn of(fs: &Arc<dyn Fs>, _: &process_env::EnvSnapshot) -> Lifeline {
        Lifeline {
            fs: Arc::clone(fs),
            path: None,
        }
    }

    /// Whether the test that started it is gone: its lock taken at once
    /// (and let go at once). Held is alive; a lock that cannot be tried is
    /// too.
    fn cut(&self) -> bool {
        self.path
            .as_deref()
            .is_some_and(|path| file_lock::lock_within(&*self.fs, path, Duration::ZERO).is_ok())
    }
}

/// How often a SIGTERM's drain looks again at its timeout, to tell the live
/// frontends a changed one.
const RETELL_LOOK: Duration = Duration::from_secs(1);

/// Hear SIGTERM for the daemon's life (owner, 2026-11-15): the first asks
/// it to drain within its configured timeout, as
/// `daemon stop --drain` does; a second is the plain stop, ending what it
/// hosts (`--even-if-live`, ruling 114's stop of each engine). None heard
/// when the platform cannot listen (said in the log).
///
/// The drain also TELLS each live interactive frontend of `home` at once
/// (owner, 2026-11-15, OD-63) when it will be stopped: at the drain's
/// timeout from its start, by itself (its notice's `stop_at`), as nothing
/// else asks it then. The draining daemon stays while a live frontend
/// remains ([`stays_for`]), so the frontend's last reports reach it. A
/// timeout changed while the drain runs is told again while the daemon
/// lives.
fn hear_terminate(
    (fs, home): (&Arc<dyn Fs>, &Path),
    shared: &Arc<Shared>,
    say: &Say,
) -> Option<tokio::task::JoinHandle<()>> {
    let mut signals = match fs.os().signals(&[os::Signal::Terminate]) {
        Ok(signals) => signals,
        Err(e) => {
            said(
                say,
                &format!("cannot listen for SIGTERM ({e}): a SIGTERM ends the daemon"),
            );
            return None;
        }
    };
    let (shared, say) = (Arc::clone(shared), Arc::clone(say));
    let frontends = liveness::FrontendLocks::new(Arc::clone(fs), home);
    Some(tokio::spawn(async move {
        let _ = signals.recv().await;
        said(&say, "SIGTERM: asked to drain");
        daemon_api::Hosting::drain(shared.as_ref(), None);
        shared.wake.notify_one();
        let started = super::drain::epoch_seconds();
        let mut told = None;
        loop {
            let period = shared.drain_period();
            if told != Some(period) {
                told = Some(period);
                tell_frontends(&frontends, started + period.as_secs(), &say);
            }
            // `recv` is a poll of the signal source: cancelled by the
            // timeout, it loses no signal.
            if tokio::time::timeout(RETELL_LOOK, signals.recv())
                .await
                .is_ok()
            {
                break;
            }
        }
        said(&say, "SIGTERM again: asked to stop even if live");
        daemon_api::Hosting::stop(shared.as_ref(), true);
        shared.wake.notify_one();
    }))
}

/// Tell each live frontend that it will be stopped at `stop_at` (seconds
/// since the epoch), by itself; each said in the log.
fn tell_frontends(frontends: &liveness::FrontendLocks, stop_at: u64, say: &Say) {
    let left =
        std::time::Duration::from_secs(stop_at.saturating_sub(super::drain::epoch_seconds()));
    for pid in frontends.live() {
        let notice = liveness::FrontendNotice {
            text: super::drain::notice_text(left),
            stop_at: Some(stop_at),
        };
        match frontends.tell(pid, &notice) {
            Ok(()) => said(
                say,
                &format!("told the frontend pid {pid}: {}", notice.text),
            ),
            Err(e) => said(say, &format!("cannot tell the frontend pid {pid}: {e}")),
        }
    }
}

/// What the daemon says of itself, fixed at its start (`started_at` its
/// seconds since the epoch).
fn about(stamp: &str, started_at: i64, pace: Pace) -> daemon_api::About {
    daemon_api::About {
        pid: std::process::id(),
        version: stamp.to_string(),
        start_time: angzarr_embedded::Timestamp {
            seconds: started_at,
            nanos: 0,
        },
        idle_limit_seconds: pace.idle.as_secs(),
    }
}

/// The launcher, its drain's period `pace.hand_over` timed by this process's
/// clock (OD-54).
fn timed<E: run_ports::Edge>(launcher: Daemon<E>, pace: Pace) -> Daemon<E> {
    launcher
        .clocked(Arc::new(ProcessClock(Instant::now())))
        .handing_over_within(pace.hand_over)
}

/// The clock a kept trash slot's age is told by (G12): now; under
/// test-support, moved on by the span the daemon's own environment names in
/// `CTXLOOM_TEST_CLOCK_OFFSET` (the harness stops the daemon and starts the
/// next with the offset). A value that is no span refuses the start.
fn trash_clock(env: &process_env::EnvSnapshot) -> Result<library_api::Clock, String> {
    let offset = super::clock_offset(env)
        .map_err(|cause| format!("CTXLOOM_TEST_CLOCK_OFFSET: {cause}"))?
        .as_secs()
        .try_into()
        .unwrap_or(i64::MAX);
    Ok(Arc::new(move || seconds_now().saturating_add(offset)))
}

fn seconds_now() -> i64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map_or(0, |d| i64::try_from(d.as_secs()).unwrap_or(i64::MAX))
}

/// The one-shots the daemon hosts (U4), each over `core` shared, on the
/// daemon's runtime (the one this is called on): the API creates and
/// watches them, the launcher stops them.
fn one_shots_over(
    core: &InProcessCore<SqliteStore>,
    home: &Path,
    temp: &Path,
) -> Arc<daemon_application::OneShots> {
    Arc::new(daemon_application::OneShots::new(Box::new(
        crate::hosted_oneshots::HostedOneShots {
            core: core.share(),
            runtime: tokio::runtime::Handle::current(),
            home: home.to_path_buf(),
            temp: temp.to_path_buf(),
        },
    )))
}

/// What a daemon's start shares with its loop and its API: its files, the
/// round's state, the one-shots it hosts and the sessions it prepares.
type Hosts<'a> = (
    &'a DaemonFiles,
    &'a Arc<Shared>,
    &'a Arc<OneShots>,
    &'a Arc<daemon_application::Terminals>,
);

/// The interactive sessions the daemon prepares (U5), each over `core`
/// shared, on the daemon's runtime: the API creates and watches them.
fn terminals_over(
    core: &InProcessCore<SqliteStore>,
    home: &Path,
    temp: &Path,
) -> Arc<daemon_application::Terminals> {
    Arc::new(daemon_application::Terminals::new(Box::new(
        crate::hosted_terminals::HostedTerminals {
            core: core.share(),
            runtime: tokio::runtime::Handle::current(),
            home: home.to_path_buf(),
            temp: temp.to_path_buf(),
        },
    )))
}

/// The daemon started (its bearer and certificate minted, its listener
/// serving the coordinator and the `Ctxloom` service, the launcher
/// composed, the discovery file published): the listener, the launcher and
/// its own advertisement. On a failure what it started is let go (the
/// listener stopped) before it answers, so its caller can close the store.
async fn start(
    fs: &Arc<dyn Fs>,
    (home, temp): (&Path, &Path),
    core: &InProcessCore<SqliteStore>,
    kept: &Projectors,
    (os, user_home, stamp, pace, engines): (
        String,
        Option<PathBuf>,
        String,
        Pace,
        Vec<daemon_application::HostedEngine>,
    ),
    (files, shared, one_shots, terminals): Hosts<'_>,
    (say, env): (&Say, &Arc<process_env::EnvSnapshot>),
) -> Result<(HookListener, Daemon<CoreEdge>, Discovery), String> {
    let code = |e: &dyn coded_diagnostic::Diagnostic| e.code().to_string();
    let edge = core.edge();
    // The user bearer (T12): minted at each start, advertised in the
    // discovery file alone.
    let bearer = OsTokenSource.mint();
    let certificate = daemon_certificate()?;
    let started_at = seconds_now();
    // The sessions their ctxloom MCP server watches (the terminal wake):
    // the API's `WatchSession` watches, the loop publishes each round.
    let watches = daemon_application::SessionWatches::new(daemon_application::KEEPALIVE);
    // Each hosted launch's client program, from its hand-off to its close.
    let handed = super::files::HandedPrograms::default();
    let api = daemon_api::Api::new(daemon_api::Parts {
        edge: Arc::clone(&edge),
        index: kept.index.clone().unwrap_or_default(),
        pending: kept.pending.clone().unwrap_or_default(),
        board: kept.board.clone().unwrap_or_default(),
        hosting: Arc::clone(shared) as Arc<dyn daemon_api::Hosting>,
        states: Arc::new(Locks(Arc::clone(fs))),
        harps: Arc::new(FreshHarps),
        about: about(&stamp, started_at, pace),
    })
    // The one-shots it hosts (U4).
    .hosting_one_shots(Arc::clone(one_shots))
    // A live session's re-assembled context is handed through its
    // engine (step 10c U13).
    .delivering(Arc::new(EngineDeliveries(
        engines
            .iter()
            .map(|hosted| Arc::clone(&hosted.engine))
            .collect(),
    )))
    .watching(Arc::clone(&watches));
    #[cfg(feature = "purge")]
    let api = api
        .destroying(
            Arc::new(super::places::ProjectPlaces {
                fs: Arc::clone(fs),
                home: home.to_path_buf(),
                temp: temp.to_path_buf(),
            }),
            Arc::new(super::places::StorePurges(core.store_handle())),
        )
        .on_os(&os);
    let calls = served(
        api,
        (fs, files, trash_clock(env)?),
        (home, temp, &stamp),
        (&engines, terminals, kept),
        &handed,
        &edge,
    );
    let listener = HookListener::serve_tcp_beside(
        coordinated(&edge, files),
        UserGate::new(&bearer),
        SocketAddr::new(IpAddr::V4(Ipv4Addr::LOCALHOST), 0),
        TlsIdentity::from_pem(certificate.public().to_pem(), certificate.key_pem()),
        Some(api_served(
            daemon_api_grpc::service(calls, UserGate::new(&bearer)),
            say,
        )),
    )
    .await
    .map_err(|e| code(&e))?;
    let endpoint = match run_ports::endpoint_url("127.0.0.1", listener.local_addr().port()) {
        Ok(endpoint) => endpoint,
        Err(e) => {
            let _ = listener.stop().await;
            return Err(code(&e));
        }
    };
    shared.serving_at(&endpoint);
    let launcher = launcher(
        fs,
        (home, temp),
        &edge,
        kept,
        (engines, user_home.clone(), env),
        (told(home, os, user_home), &handed),
        Arc::clone(say),
    )
    .hosting_one_shots(Arc::clone(one_shots))
    .watching(watches);
    let launcher = timed(launcher, pace)
        // OD-59 (2), the ONE named exception to "the daemon never reads a
        // hold": its start delivers a judged run's session's hold first.
        .flushing_at_start(Arc::new(super::flush::StartFlush {
            fs: Arc::clone(fs),
            home: home.to_path_buf(),
            edge: Arc::clone(&edge),
            stamp: stamp.clone(),
        }));
    let own = Discovery {
        stamp,
        pid: std::process::id(),
        endpoint,
        cert: certificate.public().clone(),
        bearer,
        home: home.display().to_string(),
    };
    match swept_and_advertised(launcher, core, (files, shared.as_ref()), &own, say).await {
        Ok(launcher) => Ok((listener, launcher, own)),
        Err(detail) => {
            let _ = listener.stop().await;
            Err(detail)
        }
    }
}

/// The daemon's engines, as a live session's re-assembled context is
/// handed through them (step 10c U13), by name.
struct EngineDeliveries(Vec<Arc<dyn run_ports::Engine>>);

impl daemon_api::Deliveries for EngineDeliveries {
    fn engine(&self, name: &str) -> Option<Arc<dyn run_ports::Engine>> {
        self.0.iter().find(|engine| engine.name() == name).cloned()
    }
}

/// The launcher's first step (T18), before the daemon is advertised: the
/// runs other processes, and a killed daemon, left are swept (their
/// checkouts let go, OD-19), so the first command that finds it finds the
/// record whole; then `own` is published. A start-up that failed (its
/// catch-up or its sweep) still publishes it (owner ruling OD-52), loud:
/// the log says so and `shared` keeps why, which `GetDaemon` answers as a
/// warning and every command that reaches it says. On a failure to
/// publish, the launcher is let go and why is answered.
async fn swept_and_advertised(
    mut launcher: Daemon<CoreEdge>,
    core: &InProcessCore<SqliteStore>,
    (files, shared): (&DaemonFiles, &Shared),
    own: &Discovery,
    say: &Say,
) -> Result<Daemon<CoreEdge>, String> {
    if let Err(detail) = started_up(&mut launcher, core, files).await {
        said(say, &incomplete_line(&detail));
        shared.start_up_failed_with(Some(detail));
    }
    if let Err(e) = files.publish(own) {
        drop(launcher);
        return Err(format!("the discovery file cannot be written: {e}"));
    }
    Ok(launcher)
}

/// What the log says of a start-up that failed (OD-52), `detail` its why.
fn incomplete_line(detail: &str) -> String {
    format!(
        "WARNING: the start-up catch-up failed: {detail}; serving anyway, and \
         what it answers may be missing events until a later catch-up \
         succeeds (tried again each round)"
    )
}

/// While the start-up has failed (OD-52), it is tried again (its catch-up
/// and its sweep, as at the start): once it succeeds the warning is
/// cleared and the log says so; failing again, its why is the latest.
async fn started_up_again<E: run_ports::Edge>(
    launcher: &mut Daemon<E>,
    core: &InProcessCore<SqliteStore>,
    (files, shared): (&DaemonFiles, &Shared),
    say: &Say,
) {
    if daemon_api::Hosting::start_up_failed(shared).is_none() {
        return;
    }
    match started_up(launcher, core, files).await {
        Ok(_) => {
            shared.start_up_failed_with(None);
            said(
                say,
                "the start-up catch-up, tried again, succeeded: its warning is cleared",
            );
        }
        Err(detail) => shared.start_up_failed_with(Some(detail)),
    }
}

/// The launcher's start-up (T18): its read models caught up with the
/// store's log, then its sweep.
#[cfg(not(feature = "test-support"))]
async fn started_up<E: run_ports::Edge>(
    launcher: &mut Daemon<E>,
    core: &InProcessCore<SqliteStore>,
    _: &DaemonFiles,
) -> Result<Vec<daemon_application::Swept>, String> {
    launcher.start_up(core.store(), core.catalog()).await
}

/// Under test-support, failed while the switch
/// ([`DaemonFiles::start_up_fails`]) is there, its why the switch's line
/// (OD-52).
#[cfg(feature = "test-support")]
async fn started_up<E: run_ports::Edge>(
    launcher: &mut Daemon<E>,
    core: &InProcessCore<SqliteStore>,
    files: &DaemonFiles,
) -> Result<Vec<daemon_application::Swept>, String> {
    match files.start_up_fails() {
        Some(why) => Err(why),
        None => launcher.start_up(core.store(), core.catalog()).await,
    }
}

/// Where the daemon's API reads a trust (step 8 U1): a project's previewed
/// at its registered root over `home` and `temp`, and the trust index `kept`
/// registered in the kernel.
fn trusting(kept: &Projectors, (home, temp): (&Path, &Path)) -> daemon_api::Trusting {
    daemon_api::Trusting {
        trusts: Arc::new(super::places::ProjectTrusts {
            home: home.to_path_buf(),
            temp: temp.to_path_buf(),
        }),
        index: kept.trusts.clone().unwrap_or_default(),
    }
}

/// The API the daemon serves: `api`, a project's context assembled from its
/// registered root (slice 3 step 6), and a child's hand-off planned, judged
/// and kept here (slice 4 U3), over `home`, `temp` and the `engines` its
/// children are hosted on, and the interactive sessions `terminals`
/// prepares (U5), and the trusts it reads through `kept`'s trust index
/// (step 8 U1).
fn served(
    api: daemon_api::Api<CoreEdge>,
    (fs, files, clock): (&Arc<dyn Fs>, &DaemonFiles, library_api::Clock),
    (home, temp, stamp): (&Path, &Path, &str),
    (engines, terminals, kept): (
        &[daemon_application::HostedEngine],
        &Arc<daemon_application::Terminals>,
        &Projectors,
    ),
    handed: &super::files::HandedPrograms,
    edge: &Arc<CoreEdge>,
) -> Arc<dyn daemon_api::Calls> {
    // Shared, so what follows from a run it hosts (a distill's write, step
    // 10b) reaches it once the call that started the run has answered.
    daemon_api::Api::shared(
        // The sessions it prepares (U5); under test-support, a library
        // write's kill points (step 10b U3).
        api.preparing_terminals(Arc::clone(terminals))
            .dying_at_writes(write_kills(files))
            // The library's reads (step 10a U2): each at the caller's
            // directory, in its environment.
            .reading(Arc::new(super::places::ProjectLibraries {
                home: home.to_path_buf(),
                temp: temp.to_path_buf(),
            }))
            // Go's sessions imported by commands sent through the core's own
            // edge (ONE WRITER).
            .importing(Arc::new(super::imports::Importing {
                fs: Arc::clone(fs),
                home: home.to_path_buf(),
                edge: Arc::clone(edge),
            }))
            // A project with a store of its own from before the one store is
            // refused at its resolution (T17).
            .refusing_legacy_stores(Arc::new(crate::LegacyStores {
                stores: Stores::new(Arc::clone(fs), home.to_path_buf()),
            }))
            .handing_off(crate::hand_offs::api_hand_offs(
                fs,
                (home, temp),
                engines,
                handed,
            ))
            // Other tools' files, planned where the caller stands and
            // written as one sequence (step 10c U10).
            .writing_foreign(Arc::new(super::foreign::ForeignWriters {
                home: home.to_path_buf(),
                temp: temp.to_path_buf(),
            }))
            // The setup reads, each answered where the caller stands (step
            // 10c U5, U6).
            .reading_setup(Arc::new(super::setup_reads::SetupReaders {
                home: home.to_path_buf(),
                temp: temp.to_path_buf(),
                stamp: stamp.to_string(),
            }))
            // The remotes', the lock's and the cache's writes, planned
            // where the caller stands, git in its environment (step 10c U4).
            .writing_deps(Arc::new(super::deps::DepsWriters {
                home: home.to_path_buf(),
                temp: temp.to_path_buf(),
            }))
            // The trusts it reads (step 8 U1).
            .trusting(trusting(kept, (home, temp)))
            // The asks it records and reads (step 8 U4).
            .approving(daemon_api::Approving {
                index: kept.approvals.clone().unwrap_or_default(),
                names: Arc::new(FreshAskNames),
            })
            // The log `top` follows, one timeline a session.
            .timelining(kept.timelines.clone().unwrap_or_default())
            // Each library read observes its trees first (step 10a U2),
            // reconciled against the checkouts its kernel projects.
            .observing(daemon_api::Observing {
                checkouts: kept.checkouts.clone().unwrap_or_default(),
                harps: Arc::new(super::places::TreeHarps(Arc::clone(fs))),
                observers: Arc::new(super::places::CallerGits {
                    fs: Arc::clone(fs),
                    home: home.to_path_buf(),
                    scanner: Arc::new(super::places::TreeScanner::new(Arc::clone(fs))),
                }),
                // A write's change is one atomic operation (step 10b U8).
                operations: Arc::new(content_fs::TreeOperations::new(Arc::clone(fs))),
                // A kept trash slot's age is told by it (G12).
                clock,
                // `clean` measures a tree's cache (step 10b U9).
                caches: Arc::new(super::places::TreeCaches(Arc::clone(fs))),
            }),
    )
}

/// The `Ctxloom` service as the listener serves it.
#[cfg(not(feature = "test-support"))]
fn api_served<S>(service: S, _: &Say) -> S {
    service
}

/// Under test-support, the service telling the daemon's log each call
/// made of it (`served <path>`): a test counts the calls a command made
/// (OD-55: none asks `GetDaemon` for the daemon's warnings).
#[cfg(feature = "test-support")]
fn api_served<S>(service: S, say: &Say) -> daemon_api_grpc::Told<S> {
    let say = Arc::clone(say);
    daemon_api_grpc::telling(
        service,
        Arc::new(move |path: &str| said(&say, &format!("served {path}"))),
    )
}

/// The edge of the daemon's core, which the launcher writes through.
type CoreEdge = embedded_edge::EmbeddedEdge<crate::CoreStores<SqliteStore>>;

/// The edge the coordinator serves the clients' writes through: the core's.
#[cfg(not(feature = "test-support"))]
fn coordinated(edge: &Arc<CoreEdge>, _: &DaemonFiles) -> Arc<CoreEdge> {
    Arc::clone(edge)
}

/// Under test-support, the core's behind the kill switch
/// ([`DaemonFiles::take_die_after_apply`]).
#[cfg(feature = "test-support")]
fn coordinated(edge: &Arc<CoreEdge>, files: &DaemonFiles) -> Arc<Dying<CoreEdge>> {
    Arc::new(Dying {
        inner: Arc::clone(edge),
        files: files.clone(),
    })
}

/// A test-support edge (slice 3 step 4): once a client's `HandleSync` is
/// applied, a daemon that finds the switch file takes it and is killed
/// outright before it answers, as a crash between the apply and the answer
/// would leave it; the daemon started after finds none. Forced, never
/// waited for (the race rule).
#[cfg(feature = "test-support")]
pub(crate) struct Dying<E> {
    inner: Arc<E>,
    files: DaemonFiles,
}

#[cfg(feature = "test-support")]
impl<E: run_ports::Edge> run_ports::Edge for Dying<E> {
    async fn serve(
        &self,
        book: angzarr_embedded::v1::CommandBook,
        arrival: run_ports::Arrival,
    ) -> Result<angzarr_embedded::Answered, run_ports::Unserved> {
        self.inner.serve(book, arrival).await
    }

    async fn handle(
        &self,
        book: angzarr_embedded::v1::CommandBook,
    ) -> Result<angzarr_embedded::Answered, angzarr_embedded::Failure<angzarr_embedded::Rejected>>
    {
        self.inner.handle(book).await
    }

    async fn serve_sync(
        &self,
        book: angzarr_embedded::v1::CommandBook,
        projectors: Vec<String>,
        arrival: run_ports::Arrival,
    ) -> Result<run_ports::SyncAnswered, run_ports::Unserved> {
        let answered = self.inner.serve_sync(book, projectors, arrival).await;
        if answered.is_ok() && self.files.take_die_after_apply() {
            // Killed outright: no answer, no drain, no discovery removed.
            std::process::abort();
        }
        answered
    }

    async fn events(
        &self,
        cover: &angzarr_embedded::v1::Cover,
        from: u64,
    ) -> Result<Vec<angzarr_embedded::v1::EventPage>, angzarr_embedded::StoreError> {
        self.inner.events(cover, from).await
    }
}

/// Under test-support (U6): the core's `edge` tells each book it applied to
/// the kill switch, which kills the daemon outright once a book of the step
/// it names is applied, before it is answered: `enqueued` (a run's
/// `EnqueueRun`), `minted` (a `MintSession`), `started` (a
/// `RecordRunnerStarted`), `turn` (a `RecordTurnStarted`), `hook` (a hook's capture
/// fact). Forced, never waited for (the race rule).
#[cfg(feature = "test-support")]
fn dies_at_steps(edge: &Arc<CoreEdge>, files: &DaemonFiles, say: &Say) {
    let files = files.clone();
    // Each command the edge takes, applied or refused, is said as handled.
    let say = Arc::clone(say);
    edge.hearing(Box::new(move |book| {
        if let Some(command) = command_of(book) {
            said(&say, &format!("handled {command}"));
        }
    }));
    edge.telling(Box::new(move |book, _| {
        if let Some(step) = step_of(book) {
            if files.take_die_at(step) {
                std::process::abort();
            }
        }
    }));
}

/// A library write's kill points (step 10b U3, U8; `test-die-at`'s
/// `intended`, `staged`, `trashed`, `renamed`, `done`): under test-support
/// the daemon that finds the switch naming the point it reached takes it
/// and is killed outright; one held there (`test-hold-at`) waits first.
/// Forced, never waited for (the race rule).
#[cfg(feature = "test-support")]
fn write_kills(files: &DaemonFiles) -> daemon_api::library_writes::DiesAt {
    let files = files.clone();
    Arc::new(move |point: &str| {
        files.hold_at(point);
        // A write recorded done and not yet answered is also where the
        // client's write is "applied, before it is answered" (slice 3's
        // `test-die-after-apply`, the retry's switch).
        let applied = point == "done" && files.take_die_after_apply();
        if applied || files.take_die_at(point) {
            std::process::abort();
        }
    })
}

/// The shipped daemon is never killed by a switch.
#[cfg(not(feature = "test-support"))]
fn write_kills(_: &DaemonFiles) -> daemon_api::library_writes::DiesAt {
    Arc::new(|_: &str| {})
}

/// The name of the command a book carries (a fact is not one), as its
/// message is named: what the log says the daemon `handled` (ONE WRITER:
/// every write to the store is a command, and a test reads which).
#[cfg(feature = "test-support")]
fn command_of(book: &angzarr_embedded::v1::CommandBook) -> Option<String> {
    use angzarr_embedded::v1::command_page::Body;
    match book.page.as_ref()?.body.as_ref()? {
        Body::Command(command) => command
            .payload
            .as_ref()?
            .type_url
            .rsplit('.')
            .next()
            .map(str::to_string),
        Body::Fact(_) => None,
    }
}

/// The kill switch's step a book applied is, if any.
#[cfg(feature = "test-support")]
fn step_of(book: &angzarr_embedded::v1::CommandBook) -> Option<&'static str> {
    use angzarr_embedded::v1::command_page::Body;
    let payload = match book.page.as_ref()?.body.as_ref()? {
        Body::Command(command) => command.payload.as_ref()?,
        Body::Fact(fact) => fact.payload.as_ref()?,
    };
    let name = payload.type_url.rsplit('.').next()?;
    Some(match name {
        "EnqueueRun" => "enqueued",
        "MintSession" => "minted",
        "RecordRunnerStarted" => "started",
        "RecordTurnStarted" => "turn",
        "RecordPromptSubmitted"
        | "RecordAssistantText"
        | "RecordToolCall"
        | "RecordTurnEnded"
        | "RecordCompaction"
        | "RecordConversationCleared" => "hook",
        _ => return None,
    })
}

/// Where the daemon is, as a child it hosts is told: ctxloom's `home`, the
/// directory of its own program (a launcher's, where it names one, leads
/// instead), the `os` and the user's own home.
fn told(home: &Path, os: String, user_home: Option<PathBuf>) -> Told {
    Told {
        home: home.to_string_lossy().into_owned(),
        program_dir: std::env::current_exe()
            .ok()
            .and_then(|exe| exe.parent().map(|dir| dir.display().to_string())),
        os,
        user_home: user_home.map(|home| home.to_string_lossy().into_owned()),
    }
}

/// The launcher (T18), composed: its ports over this process's home, the
/// core's `edge` (`home` and `temp` the process's), the folds `kept`
/// registered in the kernel, the daemon's
/// listener as `told`, and the log.
fn launcher(
    fs: &Arc<dyn Fs>,
    (home, temp): (&Path, &Path),
    edge: &Arc<CoreEdge>,
    kept: &Projectors,
    (engines, user_home, env): (
        Vec<daemon_application::HostedEngine>,
        Option<PathBuf>,
        &Arc<process_env::EnvSnapshot>,
    ),
    (told, handed): (Told, &super::files::HandedPrograms),
    say: Say,
) -> Daemon<CoreEdge> {
    Daemon::new(Ports {
        edge: Arc::clone(edge),
        engines,
        plans: Box::new(Plans {
            home: home.to_path_buf(),
            temp: temp.to_path_buf(),
            user_home,
        }),
        hand_offs: Box::new(HandOffFiles(
            DaemonFiles::new(Arc::clone(fs), home),
            handed.clone(),
        )),
        homes: Box::new(Homes(Arc::clone(fs))),
        ears: Box::new(Pumps::new()),
        run_locks: Arc::new(liveness::RunLockFiles::new(
            Arc::clone(fs),
            home.to_path_buf(),
        )),
        place: told,
        folds: Folds {
            board: kept.board.clone().unwrap_or_default(),
            pending: kept.pending.clone().unwrap_or_default(),
            index: kept.index.clone().unwrap_or_default(),
            trusts: kept.trusts.clone().unwrap_or_default(),
            approvals: kept.approvals.clone().unwrap_or_default(),
            timelines: kept.timelines.clone().unwrap_or_default(),
            checkouts: kept.checkouts.clone().unwrap_or_default(),
        },
        say: Box::new(move |line: &str| said(&say, line)),
    })
    // What each live session is owed, recorded as each round catches up
    // (step 10c U13 rework).
    .owing(Arc::new(super::places::LibraryTrees {
        fs: Arc::clone(fs),
        home: home.to_path_buf(),
    }))
    // A child's program found on its own PATH, as every launch's (T10).
    .finding(Arc::new(
        host_launcher::HostLauncher::default().searching(Arc::clone(fs)),
    ))
    // A child follows its agent's `workspace` (s4-Q11): its checkout under
    // ctxloom's home, let go WIP-safe at its exit and, lost, at the next
    // start's sweep (OD-19).
    .placing(Arc::new(Workspaces {
        fs: Arc::clone(fs),
        home: home.to_path_buf(),
        env: Arc::clone(env),
    }))
}

/// How long past its drain's time a draining daemon that hosts nothing
/// still stays for the live frontends of its home ([`stays_for`]): a
/// frontend asked to stop then (by `ctxloom shutdown`, or by itself at its
/// notice's `stop_at`) stops its TUI within its grace and reports its end
/// (`shutdown`'s `FRONTENDS_WAIT`, 20 s), with a margin.
const FRONTENDS_GRACE: Duration = Duration::from_secs(30);

/// Whether a draining daemon that hosts nothing still stays (owner ruling,
/// 2026-11-16: PREVENT the early drain exit): while a frontend of its home
/// is live (`live`, by its lock: a frontend gone without reporting reads
/// free, so it holds nothing) and its drain's time and [`FRONTENDS_GRACE`]
/// after it have not passed (`within`), so a frontend's last reports reach
/// a daemon. A plain stop (`plain`) never stays.
fn stays_for(live: &[u32], within: bool, plain: bool) -> bool {
    !plain && within && !live.is_empty()
}

/// Tick until the daemon is asked to stop, its single-instance lock file
/// is no longer the one it holds (`held`: removed or replaced, so it
/// excludes nobody), its home's ctxloom directory is gone, another daemon's discovery replaced its own (`own`, in
/// `files`) or was removed, or it has had nothing to do for its idle time
/// ([`why_it_exits`]). It never advertises itself again (owner, 2026-11-17).
async fn run<E: run_ports::Edge>(
    launcher: &mut Daemon<E>,
    core: &InProcessCore<SqliteStore>,
    (files, own, held, frontends): (
        &DaemonFiles,
        &Discovery,
        &Instance,
        &liveness::FrontendLocks,
    ),
    (pace, shared, terminals, lifeline): (
        Pace,
        &Arc<Shared>,
        &daemon_application::Terminals,
        &Lifeline,
    ),
    say: &Say,
) -> Result<(), String> {
    let mut quiet_since = Instant::now();
    // Asked to stop through its API (`StopDaemon`): drain, its hosted
    // engines ended first when asked `even_if_live` (S12), or handed over
    // when asked to drain (s4-Q4).
    let mut stopping = false;
    let mut handing_over = false;
    let mut staying = Staying::for_the(frontends);
    loop {
        shared.tell(launcher.hosted_runs(), launcher.askers(), quiet_since);
        if let Some(why) = why_it_exits(files, own, held) {
            said(say, why);
            return Ok(());
        }
        // Under test-support, a daemon told to be deaf takes a stop and a
        // drain and ignores them (`ctxloom shutdown`'s what-will-not-stop).
        let deaf = deaf_to_stop(files);
        if let Some(end_hosted) = shared.take_stop().filter(|_| !deaf) {
            if end_hosted {
                said(say, "asked to stop even if live: ending what it hosts");
                launcher.end_all();
            }
            stopping = true;
            staying.plain = true;
        }
        // Under test-support, outliving the test that started it is the
        // stop even if live: what it hosts ended, then it exits.
        if !stopping && lifeline.cut() {
            said(say, "its test is gone: ending what it hosts");
            launcher.end_all();
            stopping = true;
            staying.plain = true;
        }
        // Asked to drain (`daemon stop --drain`, `ctxloom shutdown`, a
        // SIGTERM; OD-54): it hands what it hosts over to the next daemon,
        // and exits once it hosts nothing.
        if !handing_over && !deaf && daemon_api::Hosting::draining(shared.as_ref()) {
            said(say, "asked to drain: handing what it hosts over");
            launcher.hand_over();
            handing_over = true;
            stopping = true;
            staying.began = Some(Instant::now());
        }
        // The drain's timeout, read again each round (owner, 2026-11-15): a
        // `daemon.drain_timeout` changed while it drains is the drain's.
        if handing_over {
            let period = shared.drain_period();
            if period != launcher.hand_over_period() {
                said(
                    say,
                    &format!(
                        "the drain waits {} from its start",
                        duration_text::format_exact(period)
                    ),
                );
                launcher.hand_over_within(period);
            }
        }
        let draining = stopping;
        // A drain's daemon stays for the live frontends of its home (owner
        // ruling, 2026-11-16), so their last reports reach it.
        if draining
            && launcher.hosting() == 0
            && !staying.stays(handing_over, shared.drain_period(), say)
        {
            return Ok(());
        }
        // Armed before the round reads (S-N2): a commit during it is not
        // missed.
        // Under test-support, killed outright while a hosted child's turn is
        // in flight, when the switch says so (U6).
        #[cfg(feature = "test-support")]
        if launcher.in_flight() && files.take_die_at("turn") {
            std::process::abort();
        }
        let commit = core.store().next_commit();
        let round = shared.round_starts();
        // A start-up that failed is tried again first (OD-52).
        started_up_again(launcher, core, (files, shared.as_ref()), say).await;
        let busy = launcher.tick(core.store(), core.catalog(), draining).await;
        shared.round_done(round);
        // A session it prepares (asked for, or its frontend's TUI running)
        // keeps it: its frontend is a client it serves (U5).
        if busy || launcher.hosting() > 0 || terminals.hosting() > 0 {
            quiet_since = Instant::now();
        } else if quiet_since.elapsed() >= pace.idle {
            // M9: a run any process started and has not ended (an
            // interactive session the CLI runs) keeps it, once the runs
            // whose owner is gone are ended; asked only at the idle limit,
            // which is when it matters.
            match launcher.live_runs(core.store(), core.catalog()).await {
                Ok(0) => {
                    said(say, "idle: exiting");
                    return Ok(());
                }
                Ok(_) => quiet_since = Instant::now(),
                Err(detail) => {
                    said(say, &format!("the live runs cannot be read: {detail}"));
                    quiet_since = Instant::now();
                }
            }
        }
        // The push: a commit through this process's store handles (the
        // daemon's own writes, its listener's hooks) wakes the next round at
        // once; another process's (a CLI command's) is seen by the poll on
        // `change_token` (S-N4: the required fallback).
        // A read of the API asking for a fresh round wakes it too.
        let _ = tokio::time::timeout(pace.poll, either(commit, shared.wake.notified())).await;
    }
}

/// What a draining daemon that hosts nothing stays for: the live frontends
/// of its home ([`stays_for`]).
struct Staying<'a> {
    frontends: &'a liveness::FrontendLocks,
    /// When its drain began, on this process's clock.
    began: Option<Instant>,
    /// Asked the plain stop (never a drain's): it stays for no frontend.
    plain: bool,
    /// The live frontends it last said it stays for.
    said_for: Vec<u32>,
}

impl<'a> Staying<'a> {
    fn for_the(frontends: &'a liveness::FrontendLocks) -> Staying<'a> {
        Staying {
            frontends,
            began: None,
            plain: false,
            said_for: Vec::new(),
        }
    }

    /// Whether the daemon, hosting nothing and `handing_over` (a drain) or
    /// stopping, stays this round, its drain's time `period`; said in the
    /// log as the frontends it stays for change, and when it leaves any.
    fn stays(&mut self, handing_over: bool, period: Duration, say: &Say) -> bool {
        let live = match handing_over && !self.plain {
            true => self.frontends.live(),
            false => Vec::new(),
        };
        let within = self
            .began
            .is_some_and(|began| began.elapsed() < period + FRONTENDS_GRACE);
        if !stays_for(&live, within, self.plain) {
            if !live.is_empty() && !self.plain {
                said(
                    say,
                    &format!(
                        "after the drain's {} and {} more, the frontends {} still run: exiting",
                        duration_text::format_exact(period),
                        duration_text::format_exact(FRONTENDS_GRACE),
                        pids_said(&live)
                    ),
                );
            }
            return false;
        }
        if live != self.said_for {
            said(
                say,
                &format!(
                    "draining: hosting nothing, it stays for the live frontends {} \
                     (their last reports are this daemon's)",
                    pids_said(&live)
                ),
            );
            self.said_for = live;
        }
        true
    }
}

/// Process ids as the log says them: `pid 1, pid 2`.
fn pids_said(pids: &[u32]) -> String {
    pids.iter()
        .map(|pid| format!("pid {pid}"))
        .collect::<Vec<_>>()
        .join(", ")
}

/// Whichever of `a` and `b` is ready first.
async fn either(a: impl std::future::Future, b: impl std::future::Future) {
    let mut a = std::pin::pin!(a);
    let mut b = std::pin::pin!(b);
    std::future::poll_fn(|cx| {
        match a.as_mut().poll(cx).is_ready() || b.as_mut().poll(cx).is_ready() {
            true => std::task::Poll::Ready(()),
            false => std::task::Poll::Pending,
        }
    })
    .await;
}

/// How long a read of the API waits for the loop's next round (its catch-up)
/// before it answers from the folds as they are.
const FRESH_WAIT: Duration = Duration::from_secs(2);

/// What the loop shares with its API's use cases (slice 2 step 5): where
/// it serves, the children it hosts and since when it has been quiet, as of
/// its last round, and whether it was asked to stop. Its one-shots are read
/// as they are NOW (askew-glitter): one ends on its own thread, its client
/// told its reply in the same change, so a copy of the last round would
/// still count it for a stop asked at once.
struct Shared {
    index: session_index::SessionIndexes,
    one_shots: Arc<daemon_application::OneShots>,
    state: Mutex<Round>,
    /// The rounds the loop has started.
    started: Mutex<u64>,
    /// The last round the loop finished.
    done: tokio::sync::watch::Sender<u64>,
    /// Wakes the loop for a round a read asked for.
    wake: tokio::sync::Notify,
    /// Why its start-up catch-up (or its sweep) failed, while no later one
    /// has succeeded (OD-52).
    start_up: Mutex<Option<String>>,
    /// Where a drain's period comes from.
    period: Period,
    /// The daemon's home's ctxloom directory, once it is gone (OD-60, owner
    /// 2026-11-17), looked at each time it is asked.
    home_gone: HomeGone,
}

/// Where [`Shared`] looks for its home: none while it is there.
type HomeGone = Box<dyn Fn() -> Option<String> + Send + Sync>;

#[derive(Default)]
struct Round {
    endpoint: String,
    /// Each hosted child's session id and run root.
    children: Vec<(String, String)>,
    /// What each hosted child's ask is recorded with (step 8 U4).
    askers: Vec<daemon_application::Asker>,
    quiet_since: Option<Instant>,
    /// Asked to stop; `true` to end what it hosts first.
    stop: Option<bool>,
    /// Asked to drain (OD-54).
    drain: bool,
    /// The timeout the drain was asked with (`ctxloom shutdown <duration>`),
    /// none for the configured one.
    drain_within: Option<Duration>,
}

/// Reads the home config's drain timeout afresh.
type Reader = Box<dyn Fn() -> Option<Duration> + Send + Sync>;

/// Where a drain's period comes from, the first that says: the drain's own
/// request, the home config's `daemon.drain_timeout` (read each time), the
/// daemon's default.
struct Period {
    default: Duration,
    configured: Reader,
}

impl Shared {
    fn new(
        index: session_index::SessionIndexes,
        one_shots: Arc<daemon_application::OneShots>,
        period: Period,
    ) -> Shared {
        Shared {
            index,
            one_shots,
            period,
            state: Mutex::new(Round::default()),
            started: Mutex::new(0),
            done: tokio::sync::watch::Sender::new(0),
            wake: tokio::sync::Notify::new(),
            start_up: Mutex::new(None),
            home_gone: Box::new(|| None),
        }
    }

    /// The same, looking at `files`' home for whether it is gone.
    fn watching(self, files: DaemonFiles) -> Shared {
        Shared {
            home_gone: Box::new(move || files.gone_home()),
            ..self
        }
    }

    /// A round starts: its number.
    fn round_starts(&self) -> u64 {
        let mut started = self.started.lock().unwrap_or_else(PoisonError::into_inner);
        *started += 1;
        *started
    }

    /// The round `round` is done: a read waiting for it answers.
    fn round_done(&self, round: u64) {
        self.done.send_replace(round);
    }

    fn round(&self) -> std::sync::MutexGuard<'_, Round> {
        self.state.lock().unwrap_or_else(PoisonError::into_inner)
    }

    fn serving_at(&self, endpoint: &str) {
        self.round().endpoint = endpoint.to_string();
    }

    /// What the round found it hosts: its children, each its session id
    /// and run root.
    fn tell(
        &self,
        children: Vec<(String, String)>,
        askers: Vec<daemon_application::Asker>,
        quiet_since: Instant,
    ) {
        let mut round = self.round();
        round.children = children;
        round.askers = askers;
        round.quiet_since = Some(quiet_since);
    }

    /// Why the start-up failed (OD-52), or none once one succeeded.
    fn start_up_failed_with(&self, why: Option<String>) {
        *self.start_up.lock().unwrap_or_else(PoisonError::into_inner) = why;
    }

    fn take_stop(&self) -> Option<bool> {
        self.round().stop.take()
    }

    /// How long the drain waits for what it hosts: as it was asked, else
    /// as the home config says now, else the default.
    fn drain_period(&self) -> Duration {
        let asked = self.round().drain_within;
        asked
            .or_else(|| (self.period.configured)())
            .unwrap_or(self.period.default)
    }
}

impl daemon_api::Hosting for Shared {
    fn endpoint(&self) -> String {
        self.round().endpoint.clone()
    }

    fn hosted(&self) -> Vec<daemon_proto::v1::HostedRun> {
        use daemon_proto::v1::hosted_run::Kind;
        let children = self.round().children.clone();
        let one_shots = self
            .one_shots
            .live()
            .into_iter()
            .map(|placed| (placed.session, placed.run, Kind::OneShot));
        let index = self.index.index();
        children
            .into_iter()
            .map(|(session, run)| (session, run, Kind::Child))
            .chain(one_shots)
            .map(|(session, run, kind)| {
                let record = index.find_by_id(&session).unwrap_or_default();
                let project = index.project_harp(&record.project_id).unwrap_or_default();
                daemon_proto::v1::HostedRun {
                    session: index.name_of(&record),
                    harp: record.harp,
                    run: format!("projects/{project}/runs/{run}"),
                    kind: kind as i32,
                }
            })
            .collect()
    }

    /// A hosted child's, else a live one-shot's (step 8, D7: one-shots ask
    /// too).
    fn asker(&self, session: &str) -> Option<daemon_application::Asker> {
        let child = self
            .round()
            .askers
            .iter()
            .find(|asker| asker.session == session)
            .cloned();
        child.or_else(|| self.one_shots.asker(session))
    }

    fn idle_seconds(&self) -> u64 {
        self.round()
            .quiet_since
            .map_or(0, |since| since.elapsed().as_secs())
    }

    fn stop(&self, end_hosted: bool) {
        let mut round = self.round();
        round.stop = Some(end_hosted || round.stop.unwrap_or(false));
    }

    fn drain(&self, within: Option<Duration>) {
        let mut round = self.round();
        if !round.drain {
            round.drain = true;
            round.drain_within = within;
        }
    }

    fn hand_over_seconds(&self) -> u64 {
        self.drain_period().as_secs()
    }

    fn draining(&self) -> bool {
        self.round().drain
    }

    fn home_gone(&self) -> Option<String> {
        (self.home_gone)()
    }

    /// Told by a test build alone (the harness's "the daemon's next round
    /// runs", step 10c U13): a plain daemon's status is as it was.
    #[cfg(feature = "test-support")]
    fn rounds(&self) -> u32 {
        u32::try_from(*self.done.borrow()).unwrap_or(u32::MAX)
    }

    /// The next round to START after this call (so its catch-up reads what
    /// was committed before it), waited for within [`FRESH_WAIT`].
    fn fresh(&self) -> std::pin::Pin<Box<dyn std::future::Future<Output = ()> + Send + '_>> {
        Box::pin(async move {
            let mut done = self.done.subscribe();
            let need = *self.started.lock().unwrap_or_else(PoisonError::into_inner) + 1;
            self.wake.notify_one();
            let _ = tokio::time::timeout(FRESH_WAIT, done.wait_for(|round| *round >= need)).await;
        })
    }

    fn start_up_failed(&self) -> Option<String> {
        self.start_up
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
            .clone()
    }
}

/// The daemon's listener certificate, minted at each start for the
/// loopback addresses its clients dial, lasting the daemon's lifetime cap.
fn daemon_certificate() -> Result<run_ports::HookCertificate, String> {
    let names = CertNames::new(
        vec![
            IpAddr::V4(Ipv4Addr::LOCALHOST),
            IpAddr::V6(Ipv6Addr::LOCALHOST),
        ],
        Vec::new(),
    );
    hook_tls::Minter::lasting(
        hook_tls::SystemClock,
        run_ports::DAEMON_CERT_LIFETIME_SECONDS,
    )
    .mint(&names)
    .map_err(|e| coded_diagnostic::Diagnostic::code(&e).to_string())
}

/// A session's state, from its liveness lock in its project.
struct Locks(Arc<dyn Fs>);

impl daemon_api::SessionStates for Locks {
    fn lock(&self, project_dir: &str, harp: &str) -> String {
        liveness::LockFiles::new(Arc::clone(&self.0), project_dir)
            .path(harp)
            .display()
            .to_string()
    }

    fn state(&self, project_dir: &str, harp: &str) -> session_index::State {
        use session_ports::{Liveness, LockState};
        let mut locks = liveness::LockFiles::new(Arc::clone(&self.0), project_dir);
        match locks.lock_state(harp) {
            LockState::Held => session_index::State::Live,
            LockState::Free => session_index::State::Ended,
            LockState::Unproven => session_index::State::Unknown,
        }
    }
}

/// Fresh project harps, minted as a CLI command's are.
struct FreshHarps;

impl daemon_api::Harps for FreshHarps {
    fn fresh(&self) -> Vec<String> {
        crate::fresh_project_harps()
    }
}

/// How many ask names one ask tries before it fails.
const ASK_NAME_TRIES: usize = 8;

/// Fresh ask names (step 8 U4, Q7): harp-names' two-word mints, as a
/// task's id (`tidy-wren`); the daemon prefixes `ask-`.
struct FreshAskNames;

impl daemon_api::AskNames for FreshAskNames {
    fn fresh(&self) -> Vec<String> {
        use ports::Mint as _;
        (0..ASK_NAME_TRIES)
            .map(|_| harp_names::HarpNames.mint())
            .collect()
    }
}

/// Why the daemon exits at this tick, by what its files say: its
/// single-instance lock file is no longer the one it holds (`held`), its
/// home's ctxloom directory is gone (OD-60), or its discovery file is not
/// its own advertisement (`own`); none while all are as it left them.
fn why_it_exits(files: &DaemonFiles, own: &Discovery, held: &Instance) -> Option<&'static str> {
    if !files.still_holds(held) {
        return Some("the single-instance lock file was replaced or removed: exiting");
    }
    // Its home deleted (OD-60): the lock may be elsewhere (the account's,
    // while `CTXLOOM_DAEMON_ROOT` names another home), so the home says it itself.
    if files.home_gone() {
        return Some("the daemon's home was removed: exiting");
    }
    match advertised(own, files.discovery().as_ref()) {
        Advertised::Ours => None,
        // Removed (owner, 2026-11-17): never advertised again, since a
        // daemon makes nothing in its home once started, and a home being
        // deleted is emptied depth-first while it is still there. The next
        // client starts a fresh daemon.
        Advertised::Gone => Some("the discovery file was removed: exiting"),
        Advertised::Replaced => Some("another daemon's discovery replaced this one's: exiting"),
    }
}

/// What a serving daemon's discovery file says of it, at a tick.
#[derive(Debug, PartialEq, Eq)]
enum Advertised {
    /// Its own advertisement: nothing to do.
    Ours,
    /// None (removed, or its home's directory removed): it exits, and makes
    /// nothing in its home again (owner, 2026-11-17).
    Gone,
    /// Another daemon's: it lost the single-instance lock (its lock file was replaced),
    /// so it exits.
    Replaced,
}

/// What `found` (the file now) is to the daemon that advertised `own`.
fn advertised(own: &Discovery, found: Option<&Discovery>) -> Advertised {
    match found {
        None => Advertised::Gone,
        Some(found) if found == own => Advertised::Ours,
        Some(_) => Advertised::Replaced,
    }
}

/// A child's plan: its project's dry run, as a ctxloom process in that
/// project would make it (`child_plan`).
struct Plans {
    home: PathBuf,
    temp: PathBuf,
    /// The user's own home (the account's): her git configuration's.
    user_home: Option<PathBuf>,
}

impl ChildPlans for Plans {
    fn preview(
        &self,
        engine: &str,
        agent: &str,
        project_dir: &str,
        env: &BTreeMap<String, String>,
        trusted: bool,
    ) -> Result<library_application::Previewed, String> {
        super::child_plan(
            &self.home,
            &self.temp,
            engine,
            agent,
            project_dir,
            env,
            trusted,
        )
    }

    fn git_user(
        &self,
        project_dir: &str,
        env: &BTreeMap<String, String>,
    ) -> Option<run_ports::GitUser> {
        super::child_git_user(&self.temp, self.user_home.as_deref(), project_dir, env)
    }

    fn output_root(
        &self,
        project_dir: &str,
        env: &BTreeMap<String, String>,
    ) -> Result<String, String> {
        super::child_output_root(&self.home, &self.temp, project_dir, env)
    }
}

/// The hand-off files under the daemon's directory (T6: read at each of
/// the child's starts, deleted at its close, swept).
/// Each read with the launching client's program, as the API kept it.
struct HandOffFiles(DaemonFiles, super::files::HandedPrograms);

impl HandOffs for HandOffFiles {
    fn read(&self, session: &str) -> Option<daemon_application::HandOff> {
        self.0
            .read_hand_off(session)
            .map(|handed| daemon_application::HandOff {
                program: self.1.of(session),
                ..handed
            })
    }

    fn delete(&self, session: &str) {
        self.1.forget(session);
        self.0.delete_hand_off(session);
    }

    fn claim(&self, session: &str) {
        self.0.claim_hand_off(session);
    }

    fn list(&self) -> Vec<daemon_application::Kept> {
        self.0.hand_offs()
    }
}

/// A child's session directory in its project: `session-files`'.
struct Homes(Arc<dyn Fs>);

impl ChildHomes for Homes {
    fn records(&self, project_dir: &str) -> Box<dyn SessionRecords> {
        Box::new(SessionDirs::new(
            Arc::clone(&self.0),
            PathBuf::from(project_dir),
        ))
    }

    /// Its session's lock, `liveness`' (the platform's file lock), the
    /// sessions directory made first.
    fn hold_session(&self, project_dir: &str, harp: &str) -> Result<run_ports::HeldLock, String> {
        use session_ports::SessionLock;
        let mut locks = liveness::LockFiles::new(Arc::clone(&self.0), project_dir);
        let path = locks.path(harp);
        if let Some(dir) = path.parent() {
            self.0
                .create_dir_all(dir, os::Mode::from_bits(0o755))
                .map_err(|e| e.to_string())?;
        }
        let held = locks.hold(harp).map_err(|e| e.report().to_string())?;
        Ok(run_ports::HeldLock::new(path.display().to_string(), held))
    }
}

/// A child's project's workspaces: worktree-git's, its checkouts under
/// ctxloom's home (`isolation_ports::worktrees_root`).
struct Workspaces {
    fs: Arc<dyn Fs>,
    home: PathBuf,
    env: Arc<process_env::EnvSnapshot>,
}

impl daemon_application::ChildWorkspaces for Workspaces {
    fn of(
        &self,
        project_id: &str,
        project_dir: &str,
        env: Option<&BTreeMap<String, String>>,
    ) -> Arc<dyn isolation_ports::Workspaces + Send + Sync> {
        // Its git in the launcher's environment (her `GIT_*`, her ssh
        // agent), as her git identity is asked; a sweep's in the daemon's.
        let env = match env {
            Some(env) => Arc::new(process_env::EnvSnapshot::from_parts(
                self.env.flavor(),
                Ok(PathBuf::from(project_dir)),
                self.env.temp_dir().to_path_buf(),
                env.iter()
                    .map(|(name, value)| (name.into(), value.into()))
                    .collect::<Vec<(std::ffi::OsString, std::ffi::OsString)>>(),
            )),
            None => Arc::clone(&self.env),
        };
        Arc::new(worktree_git::GitWorkspaces::new(
            Arc::clone(&self.fs),
            PathBuf::from(project_dir),
            Some(isolation_ports::worktrees_root(&self.home, project_id)),
            env,
        ))
    }
}

/// Each child's observations pumped from a thread of its own onto one
/// channel the launcher drains.
struct Pumps {
    tell: Mutex<Sender<(String, ChildObservation)>>,
    heard: Mutex<Receiver<(String, ChildObservation)>>,
}

impl Pumps {
    fn new() -> Pumps {
        let (tell, heard) = channel();
        Pumps {
            tell: Mutex::new(tell),
            heard: Mutex::new(heard),
        }
    }
}

impl Ears for Pumps {
    fn listen(&self, run: String, observations: Box<dyn Iterator<Item = ChildObservation> + Send>) {
        let tell = self
            .tell
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
            .clone();
        std::thread::spawn(move || {
            for observation in observations {
                if tell.send((run.clone(), observation)).is_err() {
                    return;
                }
            }
        });
    }

    fn heard(&self) -> Vec<(String, ChildObservation)> {
        let heard = self.heard.lock().unwrap_or_else(PoisonError::into_inner);
        std::iter::from_fn(|| heard.try_recv().ok()).collect()
    }
}

#[cfg(test)]
#[path = "serve.test.rs"]
mod tests;
