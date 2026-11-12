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
    ChildHomes, ChildPlans, Daemon, Ears, Folds, HandOffs, Place as Told, Ports,
};
use file_system::Fs;
use hook_listener::{HookListener, TlsIdentity};
use run_ports::{CertMint, CertNames, ChildObservation, Discovery, SessionRecords, TokenSource};
use runs_application::UserGate;
use session_files::SessionDirs;

use super::files::{DaemonFiles, Instance};
use crate::{InProcessCore, Projectors};

/// How long a daemon starting waits for the single-instance lock.
const INSTANCE_WAIT: Duration = Duration::from_secs(5);

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
}

/// How the daemon paces itself.
#[derive(Clone, Copy, Debug)]
pub(crate) struct Pace {
    /// Exit after this long with nothing hosted and nothing to do.
    pub(crate) idle: Duration,
    /// How often it looks at the store.
    pub(crate) poll: Duration,
}

/// The daemon's log, shared by the loop and the launcher.
type Say = Arc<Mutex<Box<dyn FnMut(&str) + Send>>>;

fn said(say: &Say, line: &str) {
    (say.lock().unwrap_or_else(PoisonError::into_inner))(line);
}

/// Run the daemon in this process until it exits: the instance held, served,
/// advertised, swept, then ticking. `say` is its log.
pub(crate) fn serve(
    fs: Arc<dyn Fs>,
    place: Place,
    pace: Pace,
    say: Box<dyn FnMut(&str) + Send>,
) -> Result<(), String> {
    let Place {
        stamp,
        home,
        instance_lock,
        temp,
        os,
        user_home,
    } = place;
    let home = home.as_path();
    let files = DaemonFiles::with_instance(Arc::clone(&fs), home, instance_lock);
    let Some(held) = files.hold_instance(INSTANCE_WAIT) else {
        return Err("another daemon holds the single-instance lock".to_string());
    };
    let say: Say = Arc::new(Mutex::new(say));
    let runtime = tokio::runtime::Builder::new_multi_thread()
        .worker_threads(2)
        .enable_all()
        .build()
        .map_err(|e| e.to_string())?;
    runtime.block_on(async move {
        let code = |e: &dyn angzarr_diagnostic::Diagnostic| e.code().to_string();
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
        // What the loop shares with the API's use cases (slice 2 step 5).
        let shared = Arc::new(Shared::new(kept.index.clone().unwrap_or_default()));
        let started = start(
            &fs,
            (home, &temp),
            &core,
            &kept,
            (os, user_home, stamp, pace),
            (&files, &shared),
            &say,
        )
        .await;
        let (listener, mut launcher, own) = match started {
            Ok(started) => started,
            Err(detail) => {
                // A start that fails before it serves closes its store as
                // every exit does (ruling 131): what holds it is let go first.
                drop(shared);
                drop(kept);
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
        // The launcher's first step (T18): the runs other processes, and a
        // killed daemon, left are swept before anything is hosted.
        if let Err(detail) = launcher.start_up(core.store(), core.catalog()).await {
            said(&say, &format!("the start's sweep failed: {detail}"));
        }
        let result = run(
            &mut launcher,
            &core,
            (&files, &own, &held),
            (pace, &shared),
            &say,
        )
        .await;
        files.withdraw_own(&own);
        // Every holder of the store let go before the close (ruling 131):
        // the launcher (its edge and its folds), the edge, the listener's
        // connection and stream tasks (its stop returns once every copy of
        // its service is released), and the read models kept here, each of
        // which holds the store through its gap fill (`StoreFill`) for as
        // long as any copy of it lives; the kernel's copies go with the
        // core.
        launcher.end_all();
        drop(launcher);
        let _ = listener.stop().await;
        drop(shared);
        drop(kept);
        said(&say, "exited");
        // Closed, not dropped (ruling 131).
        if let Err(unclosed) = core.close().await {
            said(&say, &format!("the store did not close: {unclosed}"));
        }
        result
    })
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
    (os, user_home, stamp, pace): (String, Option<PathBuf>, String, Pace),
    (files, shared): (&DaemonFiles, &Arc<Shared>),
    say: &Say,
) -> Result<(HookListener, Daemon<CoreEdge, OsTokenSource>, Discovery), String> {
    let code = |e: &dyn angzarr_diagnostic::Diagnostic| e.code().to_string();
    let edge = core.edge();
    // The user bearer (T12): minted at each start, advertised in the
    // discovery file alone.
    let bearer = OsTokenSource.mint();
    let names = CertNames::new(
        vec![
            IpAddr::V4(Ipv4Addr::LOCALHOST),
            IpAddr::V6(Ipv6Addr::LOCALHOST),
        ],
        Vec::new(),
    );
    let certificate = hook_tls::Minter::lasting(
        hook_tls::SystemClock,
        run_ports::DAEMON_CERT_LIFETIME_SECONDS,
    )
    .mint(&names)
    .map_err(|e| code(&e))?;
    let started_at = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map_or(0, |d| i64::try_from(d.as_secs()).unwrap_or(i64::MAX));
    let calls: Arc<dyn daemon_api::Calls> = Arc::new(daemon_api::Api::new(daemon_api::Parts {
        edge: Arc::clone(&edge),
        index: kept.index.clone().unwrap_or_default(),
        hosting: Arc::clone(shared) as Arc<dyn daemon_api::Hosting>,
        states: Arc::new(Locks(Arc::clone(fs))),
        harps: Arc::new(FreshHarps),
        about: daemon_api::About {
            pid: std::process::id(),
            version: stamp.clone(),
            start_time: angzarr_embedded::Timestamp {
                seconds: started_at,
                nanos: 0,
            },
            idle_limit_seconds: pace.idle.as_secs(),
        },
    }));
    let listener = HookListener::serve_tcp_beside(
        Arc::clone(&edge),
        UserGate::new(&bearer),
        SocketAddr::new(IpAddr::V4(Ipv4Addr::LOCALHOST), 0),
        TlsIdentity::from_pem(certificate.public().to_pem(), certificate.key_pem()),
        Some(daemon_api_grpc::service(calls, UserGate::new(&bearer))),
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
        Told {
            home: home.to_string_lossy().into_owned(),
            program_dir: std::env::current_exe()
                .ok()
                .and_then(|exe| exe.parent().map(|dir| dir.display().to_string())),
            os,
            user_home: user_home.map(|home| home.to_string_lossy().into_owned()),
            endpoint: endpoint.clone(),
            cert: certificate.public().clone(),
        },
        Arc::clone(say),
    );
    let own = Discovery {
        stamp,
        pid: std::process::id(),
        endpoint,
        cert: certificate.public().clone(),
        bearer,
        home: home.display().to_string(),
    };
    if let Err(e) = files.publish(&own) {
        drop(launcher);
        let _ = listener.stop().await;
        return Err(format!("the discovery file cannot be written: {e}"));
    }
    Ok((listener, launcher, own))
}

/// The edge of the daemon's core, which the launcher writes through.
type CoreEdge = embedded_edge::EmbeddedEdge<crate::CoreStores<SqliteStore>>;

/// The launcher (T18), composed: its ports over this process's home, the
/// core's `edge` (`home` and `temp` the process's), the folds `kept`
/// registered in the kernel, the daemon's
/// listener as `told`, and the log.
fn launcher(
    fs: &Arc<dyn Fs>,
    (home, temp): (&Path, &Path),
    edge: &Arc<CoreEdge>,
    kept: &Projectors,
    told: Told,
    say: Say,
) -> Daemon<CoreEdge, OsTokenSource> {
    Daemon::new(Ports {
        edge: Arc::clone(edge),
        tokens: Arc::new(OsTokenSource),
        engines: crate::commands::hosted_engines(),
        plans: Box::new(Plans {
            home: home.to_path_buf(),
            temp: temp.to_path_buf(),
        }),
        hand_offs: Box::new(HandOffFiles(DaemonFiles::new(Arc::clone(fs), home))),
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
        },
        say: Box::new(move |line: &str| said(&say, line)),
    })
}

/// Tick until the daemon is asked to stop, its single-instance lock file
/// is no longer the one it holds (`held`: removed or replaced, so it
/// excludes nobody), another daemon's discovery replaced its own (`own`, in
/// `files`), or it has had nothing to do for its idle time. A discovery file that is gone is published again: the daemon
/// still holds the single-instance lock, and a client finds it by the file (M11).
async fn run<E: run_ports::Edge, T: run_ports::TokenSource>(
    launcher: &mut Daemon<E, T>,
    core: &InProcessCore<SqliteStore>,
    (files, own, held): (&DaemonFiles, &Discovery, &Instance),
    (pace, shared): (Pace, &Arc<Shared>),
    say: &Say,
) -> Result<(), String> {
    let mut quiet_since = Instant::now();
    // Asked to stop through its API (`StopDaemon`): drain, its hosted
    // engines ended first when asked `even_if_live` (S12).
    let mut stopping = false;
    loop {
        shared.tell(launcher.hosted_runs(), quiet_since);
        if !files.still_holds(held) {
            said(
                say,
                "the single-instance lock file was replaced or removed: exiting",
            );
            return Ok(());
        }
        match advertised(own, files.discovery().as_ref()) {
            Advertised::Ours => {}
            Advertised::Gone => match files.publish(own) {
                Ok(()) => said(say, "the discovery file was gone: advertised again"),
                Err(e) => said(
                    say,
                    &format!("the discovery file was gone, and cannot be written: {e}"),
                ),
            },
            Advertised::Replaced => {
                said(
                    say,
                    "another daemon's discovery replaced this one's: exiting",
                );
                return Ok(());
            }
        }
        if let Some(end_hosted) = shared.take_stop() {
            if end_hosted {
                said(say, "asked to stop even if live: ending what it hosts");
                launcher.end_all();
            }
            stopping = true;
        }
        let draining = stopping;
        if draining && launcher.hosting() == 0 {
            return Ok(());
        }
        // Armed before the round reads (S-N2): a commit during it is not
        // missed.
        let commit = core.store().next_commit();
        let busy = launcher.tick(core.store(), core.catalog(), draining).await;
        if busy || launcher.hosting() > 0 {
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
        let _ = tokio::time::timeout(pace.poll, commit).await;
    }
}

/// What the loop shares with its API's use cases (slice 2 step 5): where
/// it serves, what it hosts and since when it has been quiet, as of its
/// last round, and whether it was asked to stop.
struct Shared {
    index: session_index::SessionIndexes,
    state: Mutex<Round>,
}

#[derive(Default)]
struct Round {
    endpoint: String,
    /// Each hosted run's session id and run root.
    hosted: Vec<(String, String)>,
    quiet_since: Option<Instant>,
    /// Asked to stop; `true` to end what it hosts first.
    stop: Option<bool>,
}

impl Shared {
    fn new(index: session_index::SessionIndexes) -> Shared {
        Shared {
            index,
            state: Mutex::new(Round::default()),
        }
    }

    fn round(&self) -> std::sync::MutexGuard<'_, Round> {
        self.state.lock().unwrap_or_else(PoisonError::into_inner)
    }

    fn serving_at(&self, endpoint: &str) {
        self.round().endpoint = endpoint.to_string();
    }

    fn tell(&self, hosted: Vec<(String, String)>, quiet_since: Instant) {
        let mut round = self.round();
        round.hosted = hosted;
        round.quiet_since = Some(quiet_since);
    }

    fn take_stop(&self) -> Option<bool> {
        self.round().stop.take()
    }
}

impl daemon_api::Hosting for Shared {
    fn endpoint(&self) -> String {
        self.round().endpoint.clone()
    }

    fn hosted(&self) -> Vec<daemon_proto::v1::HostedRun> {
        let hosted = self.round().hosted.clone();
        let index = self.index.index();
        hosted
            .into_iter()
            .map(|(session, run)| {
                let record = index.find_by_id(&session).unwrap_or_default();
                let project = index.project_harp(&record.project_id).unwrap_or_default();
                daemon_proto::v1::HostedRun {
                    session: index.name_of(&record),
                    harp: record.harp,
                    run: format!("projects/{project}/runs/{run}"),
                    kind: daemon_proto::v1::RunKind::Child as i32,
                }
            })
            .collect()
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
}

/// A session's state, from its liveness lock in its project.
struct Locks(Arc<dyn Fs>);

impl daemon_api::SessionStates for Locks {
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

/// What a serving daemon's discovery file says of it, at a tick.
#[derive(Debug, PartialEq, Eq)]
enum Advertised {
    /// Its own advertisement: nothing to do.
    Ours,
    /// None (removed, or its home's directory removed): it advertises
    /// itself again, since it still holds the single-instance lock.
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
}

impl ChildPlans for Plans {
    fn preview(
        &self,
        engine: &str,
        project_dir: &str,
        env: &BTreeMap<String, String>,
    ) -> Result<library_application::Previewed, String> {
        super::child_plan(&self.home, &self.temp, engine, project_dir, env)
    }
}

/// The hand-off files under the daemon's directory (taken at the child's
/// first start, D-N45).
struct HandOffFiles(DaemonFiles);

impl HandOffs for HandOffFiles {
    fn take(&self, session: &str) -> Option<BTreeMap<String, String>> {
        self.0.take_hand_off(session)
    }
}

/// A child's session directory in its project: `session-files`'.
struct Homes(Arc<dyn Fs>);

impl ChildHomes for Homes {
    fn home(&self, project_dir: &str, harp: &str) -> String {
        let dirs = SessionDirs::new(Arc::clone(&self.0), PathBuf::from(project_dir));
        format!("{}/home", SessionRecords::dir(&dirs, harp))
    }

    fn write(&self, project_dir: &str, harp: &str, rel: &str, bytes: &[u8]) -> Result<(), String> {
        let mut dirs = SessionDirs::new(Arc::clone(&self.0), Path::new(project_dir).to_path_buf());
        dirs.write(harp, rel, bytes).map_err(|e| e.to_string())
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
