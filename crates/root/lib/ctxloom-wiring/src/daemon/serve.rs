//! `ctxloom daemon serve`: the user's daemon (D18), one per user, over the
//! ONE store that holds every project's streams (T14). Only the holder of
//! the election lock serves (S-e: the one policy runner).
//!
//! This is composition alone (T18): the launcher is `daemon-application`'s
//! [`Daemon`], given its ports here (the engine registry, the child's plan by
//! its project's dry run, the hand-off files, the session directories, the
//! pump that hears each child, the run locks and the tokens). What stays
//! here is the root's: the election, the runtime, the store and its core
//! (the read models registered in its kernel, S32), the listener and its
//! certificate, the discovery file, and the pace: the launcher's start
//! (the orphan sweep) first, then a round at the first commit through this
//! process's store handles (`CommitNotify`, the push) or after the poll
//! interval, whichever is first (another process's commit reaches no
//! in-process signal, so the poll stays, S-N4). The daemon exits when asked
//! (`daemon stop`), or once it has hosted nothing and had nothing to do for
//! its idle time.

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
use runs_application::OrchestratorGate;
use session_files::SessionDirs;

use super::files::DaemonFiles;
use crate::{InProcessCore, Projectors};

/// How long a daemon starting waits for the election.
const ELECTION_WAIT: Duration = Duration::from_secs(5);

/// Where the daemon is: the ctxloom home, the temporary root, the
/// operating system's name, the user's own home (`Wiring::user_home`) and
/// the binary's full version stamp (M3).
pub(crate) struct Place {
    pub(crate) stamp: String,
    pub(crate) home: PathBuf,
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

/// Run the daemon in this process until it exits: elected, served,
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
        temp,
        os,
        user_home,
    } = place;
    let home = home.as_path();
    let files = DaemonFiles::new(Arc::clone(&fs), home);
    let Some(_elected) = files.elect(ELECTION_WAIT) else {
        return Err("another daemon holds the election".to_string());
    };
    files.clear_stop();
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
        let listener = HookListener::serve_tcp_gated(
            Arc::clone(&edge),
            OrchestratorGate::new(Arc::clone(&edge)),
            SocketAddr::new(IpAddr::V4(Ipv4Addr::LOCALHOST), 0),
            TlsIdentity::from_pem(certificate.public().to_pem(), certificate.key_pem()),
        )
        .await
        .map_err(|e| code(&e))?;
        let endpoint = run_ports::endpoint_url("127.0.0.1", listener.local_addr().port())
            .map_err(|e| code(&e))?;
        let mut launcher = launcher(
            &fs,
            (home, &temp),
            &edge,
            &kept,
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
            Arc::clone(&say),
        );
        files
            .publish(&Discovery {
                stamp,
                pid: std::process::id(),
                endpoint: endpoint.clone(),
                cert: certificate.public().clone(),
                bearer,
            })
            .map_err(|e| e.to_string())?;
        said(&say, &format!("serving at {endpoint}"));
        // The launcher's first step (T18): the runs other processes, and a
        // killed daemon, left are swept before anything is hosted.
        if let Err(detail) = launcher.start_up(core.store(), core.catalog()).await {
            said(&say, &format!("the start's sweep failed: {detail}"));
        }
        let result = run(&mut launcher, &core, &files, pace, &say).await;
        files.withdraw();
        launcher.end_all();
        drop(launcher);
        drop(edge);
        let _ = listener.stop().await;
        said(&say, "exited");
        // Closed, not dropped (ruling 131).
        if let Err(unclosed) = core.close().await {
            said(&say, &format!("the store did not close: {unclosed}"));
        }
        result
    })
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

/// Tick until the daemon is asked to stop, its discovery file is gone, or
/// it has had nothing to do for its idle time.
async fn run<E: run_ports::Edge, T: run_ports::TokenSource>(
    launcher: &mut Daemon<E, T>,
    core: &InProcessCore<SqliteStore>,
    files: &DaemonFiles,
    pace: Pace,
    say: &Say,
) -> Result<(), String> {
    let mut quiet_since = Instant::now();
    loop {
        if files.discovery().is_none() {
            // Its home is gone (or its file removed): nobody can find it.
            said(say, "the discovery file is gone: exiting");
            return Ok(());
        }
        let draining = files.stop_requested();
        if draining && launcher.hosting() == 0 {
            files.clear_stop();
            return Ok(());
        }
        // Armed before the round reads (S-N2): a commit during it is not
        // missed.
        let commit = core.store().next_commit();
        let busy = launcher.tick(core.store(), core.catalog(), draining).await;
        if busy || launcher.hosting() > 0 {
            quiet_since = Instant::now();
        } else if quiet_since.elapsed() >= pace.idle {
            said(say, "idle: exiting");
            return Ok(());
        }
        // The push: a commit through this process's store handles (the
        // daemon's own writes, its listener's hooks) wakes the next round at
        // once; another process's (a CLI command's) is seen by the poll on
        // `change_token` (S-N4: the required fallback).
        let _ = tokio::time::timeout(pace.poll, commit).await;
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
