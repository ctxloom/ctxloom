//! `ctxloom daemon serve`: the user's daemon (D18), one per user, over the
//! ONE store that holds every project's streams (T14). Only the holder of
//! the election lock serves (S-e: the one policy runner): it serves the
//! listener, runs the plan and hosts the children.
//!
//! Each tick: the board and the pending messages catch up on the store's
//! `change_token` (`CatchUp::step_if_changed`), a plan is made from them for
//! each project on the board, and its steps are taken. The next tick comes at the first
//! commit through this process's store handles (`CommitNotify`, the push) or
//! after the poll interval, whichever is first: another process's commit
//! (a CLI command's) reaches no in-process signal, so the poll stays (S-N4). A child's
//! observations come back on a channel and are recorded as the run's facts.
//! The daemon exits when asked (`daemon stop`), or once it has hosted nothing
//! and had nothing to do for its idle time.

use std::collections::{BTreeMap, BTreeSet};
use std::net::{IpAddr, Ipv4Addr, Ipv6Addr, SocketAddr};
use std::path::{Path, PathBuf};
use std::sync::mpsc::{channel, Receiver, Sender};
use std::sync::Arc;
use std::time::{Duration, Instant};

use angzarr_embedded::angzarr_ports::{CommitNotify, CommitWait};
use angzarr_embedded::angzarr_store_sqlite::SqliteStore;
use angzarr_embedded::v1::Uuid;
use angzarr_embedded::{CatchUp, Failure};
use credentials::OsTokenSource;
use ctxloom_store_sqlite::Stores;
use daemon_board::Boards;
use embedded_edge::EmbeddedEdge;
use file_system::Fs;
use hook_listener::{HookListener, TlsIdentity};
use pending_messages::PendingMessages;
use run_ports::{
    CertMint, CertNames, ChildControl, ChildEngine, ChildMessage, ChildObservation, ChildSpec,
    Discovery, Edge, Engine, FailureClass, HeldLock, PrepareRequest, RunLocks, SessionRecords,
    TurnOutcome,
};
use runs::{RunMode, TurnUrgency};
use runs_application::{
    EnqueueError, EnqueueRequest, Enqueuer, Facts, Identity, OrchestratorGate, Principal,
    RunnerFact,
};
use session_files::SessionDirs;
use session_policies::{plan, Fed, RunAt, Scene, Sent, Step};
use sessions::Urgency;
use std::future::Future as _;

use super::files::DaemonFiles;
use crate::InProcessCore;

type ProjectEdge = EmbeddedEdge<crate::CoreStores<SqliteStore>>;

/// How long an ended child has to exit before its tree is stopped.
const END_GRACE: Duration = Duration::from_secs(5);

/// How long a daemon starting waits for the election.
const ELECTION_WAIT: Duration = Duration::from_secs(5);

/// Where the daemon is: the ctxloom home and the temporary root.
pub(crate) struct Place {
    pub(crate) home: PathBuf,
    pub(crate) temp: PathBuf,
}

/// How the daemon paces itself.
#[derive(Clone, Copy, Debug)]
pub(crate) struct Pace {
    /// Exit after this long with nothing hosted and nothing to do.
    pub(crate) idle: Duration,
    /// How often it looks at the stores.
    pub(crate) poll: Duration,
}

/// A child the daemon hosts: its run, the runner's identity, its control,
/// the turn each message it was handed is in, and its run's lock (held for
/// the run's life, rulings 86, 92: the sweep sees the run alive).
struct Hosted {
    project_id: String,
    at: RunAt,
    who: Identity,
    control: Box<dyn ChildControl>,
    turns: BTreeMap<String, u64>,
    /// Where its engine writes its transcripts (`transcripts_of`): a bind
    /// names the transcript of its native id, as the engine's hook would, so a
    /// clear's rotation displaces the binding (D-N51).
    transcripts: Option<String>,
    _lock: HeldLock,
}

/// What a child said, and of which run.
type Heard = (String, ChildObservation);

/// The daemon.
pub(crate) struct Daemon {
    fs: Arc<dyn Fs>,
    home: PathBuf,
    temp: PathBuf,
    /// Each hosted child's environment, by its session's stable id: its
    /// launcher's, taken from the hand-off at its first start (S6, D-N45),
    /// held for its life (a relaunch reuses it), dropped at its end. Never
    /// the daemon's own; never logged.
    envs: BTreeMap<String, BTreeMap<String, String>>,
    files: DaemonFiles,
    /// The one store's core: every project's streams (T14).
    core: InProcessCore<SqliteStore>,
    board: CatchUp<Boards>,
    pending: CatchUp<PendingMessages>,
    hosted: BTreeMap<String, Hosted>,
    done: BTreeSet<String>,
    /// Steps refused this time round: tried again once a store changes.
    refused: BTreeSet<String>,
    /// The steps whose failure was said already (said once, not each tick).
    complained: BTreeSet<String>,
    heard: Receiver<Heard>,
    tell: Sender<Heard>,
    endpoint: String,
    cert: run_ports::PublicCert,
    run_locks: liveness::RunLockFiles,
    say: Box<dyn FnMut(&str)>,
}

/// Run the daemon in this process until it exits: elected, served,
/// advertised, then ticking. `say` is its log.
pub(crate) fn serve(
    fs: Arc<dyn Fs>,
    place: Place,
    pace: Pace,
    say: Box<dyn FnMut(&str)>,
) -> Result<(), String> {
    let home = place.home.as_path();
    let files = DaemonFiles::new(Arc::clone(&fs), home);
    let Some(_elected) = files.elect(ELECTION_WAIT) else {
        return Err("another daemon holds the election".to_string());
    };
    files.clear_stop();
    let runtime = tokio::runtime::Builder::new_multi_thread()
        .worker_threads(2)
        .enable_all()
        .build()
        .map_err(|e| e.to_string())?;
    runtime.block_on(async move {
        let store = Stores::new(Arc::clone(&fs), home.to_path_buf())
            .open()
            .await
            .map_err(|e| angzarr_diagnostic::Diagnostic::code(&e).to_string())?;
        let core = InProcessCore::start(store).await;
        let edge = core.edge();
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
        .map_err(|e| angzarr_diagnostic::Diagnostic::code(&e).to_string())?;
        let listener = HookListener::serve_tcp_gated(
            Arc::clone(&edge),
            OrchestratorGate::new(Arc::clone(&edge)),
            SocketAddr::new(IpAddr::V4(Ipv4Addr::LOCALHOST), 0),
            TlsIdentity::from_pem(certificate.public().to_pem(), certificate.key_pem()),
        )
        .await
        .map_err(|e| angzarr_diagnostic::Diagnostic::code(&e).to_string())?;
        let endpoint = run_ports::endpoint_url("127.0.0.1", listener.local_addr().port())
            .map_err(|e| angzarr_diagnostic::Diagnostic::code(&e).to_string())?;
        let (tell, heard) = channel();
        let mut daemon = Daemon {
            run_locks: liveness::RunLockFiles::new(Arc::clone(&fs), home.to_path_buf()),
            fs,
            home: home.to_path_buf(),
            temp: place.temp.clone(),
            envs: BTreeMap::new(),
            files,
            core,
            board: daemon_board::catch_up(),
            pending: pending_messages::catch_up(),
            hosted: BTreeMap::new(),
            done: BTreeSet::new(),
            refused: BTreeSet::new(),
            complained: BTreeSet::new(),
            heard,
            tell,
            endpoint: endpoint.clone(),
            cert: certificate.public().clone(),
            say,
        };
        daemon
            .files
            .publish(&Discovery {
                version: env!("CARGO_PKG_VERSION").to_string(),
                pid: std::process::id(),
                endpoint,
                cert: certificate.public().clone(),
            })
            .map_err(|e| e.to_string())?;
        (daemon.say)(&format!("serving at {}", daemon.endpoint));
        let result = daemon.run(pace).await;
        daemon.files.withdraw();
        daemon.end_all();
        drop(edge);
        let _ = listener.stop().await;
        (daemon.say)("exited");
        let Daemon { core, mut say, .. } = daemon;
        // Closed, not dropped (ruling 131).
        if let Err(unclosed) = core.close().await {
            say(&format!("the store did not close: {unclosed}"));
        }
        result
    })
}

/// A message's urgency as a child takes it, within a turn of `turn`.
fn child_urgency(message: Urgency, turn: TurnUrgency) -> run_ports::Urgency {
    match (message, turn) {
        (Urgency::Inform, _) => run_ports::Urgency::Inform,
        (_, TurnUrgency::Steer) | (Urgency::Steer, _) => run_ports::Urgency::Steer,
        _ => run_ports::Urgency::Wake,
    }
}

/// A run root's text as its stream's root.
fn root_of(text: &str) -> Uuid {
    let id = uuid::Uuid::parse_str(text).unwrap_or_default();
    Uuid::from_bytes(*id.as_bytes())
}

impl Daemon {
    async fn run(&mut self, pace: Pace) -> Result<(), String> {
        let mut quiet_since = Instant::now();
        loop {
            if self.files.discovery().is_none() {
                // Its home is gone (or its file removed): nobody can find it.
                (self.say)("the discovery file is gone: exiting");
                return Ok(());
            }
            let draining = self.files.stop_requested();
            if draining && self.hosted.is_empty() {
                self.files.clear_stop();
                return Ok(());
            }
            // Armed before the tick reads (S-N2): a commit during it is not
            // missed.
            let commits: Vec<CommitWait> = vec![self.core.store().next_commit()];
            let busy = self.tick(draining).await;
            if busy || !self.hosted.is_empty() {
                quiet_since = Instant::now();
            } else if quiet_since.elapsed() >= pace.idle {
                (self.say)("idle: exiting");
                return Ok(());
            }
            // The push: a commit through this process's store handles
            // (the daemon's own writes, its listener's hooks) wakes the next
            // tick at once; another process's (a CLI command's) is seen by
            // the poll on `change_token` (S-N4: the required fallback).
            let _ = tokio::time::timeout(pace.poll, any_commit(commits)).await;
        }
    }

    /// One round: hear the children, catch up, plan, act. Whether it did
    /// anything.
    async fn tick(&mut self, draining: bool) -> bool {
        let mut busy = self.hear().await;
        let mut changed = false;
        {
            let store = self.core.store();
            let catalog = self.core.catalog();
            let board = self.board.step_if_changed(store, catalog).await;
            let pending = self.pending.step_if_changed(store, catalog).await;
            changed |= matches!(board, Ok(Some(ref caught)) if !caught.handed().is_empty());
            changed |= matches!(pending, Ok(Some(ref caught)) if !caught.handed().is_empty());
        }
        if changed {
            self.refused.clear();
        }
        let mut steps = Vec::new();
        let hosted: BTreeSet<String> = self.hosted.keys().cloned().collect();
        let skip: BTreeSet<String> = self.done.union(&self.refused).cloned().collect();
        {
            let board = self.board.projector().board();
            let pending = self.pending.projector().pending();
            let projects: BTreeSet<String> = board
                .sessions()
                .map(|(_, session)| session.project_id.clone())
                .collect();
            for project in projects {
                steps.extend(
                    plan(&Scene {
                        project_id: &project,
                        board: &board,
                        pending: &pending,
                        hosted: &hosted,
                        done: &skip,
                    })
                    .into_iter()
                    .map(|step| (project.clone(), step)),
                );
            }
        }
        for (project_id, step) in steps {
            if draining && matches!(step, Step::Start { .. }) {
                continue;
            }
            let key = step.key().to_string();
            match self.take(&project_id, step).await {
                Taken::Done => {
                    busy = true;
                    self.done.insert(key);
                }
                Taken::Refused => {
                    busy = true;
                    self.refused.insert(key);
                }
                Taken::Again => {}
            }
        }
        busy
    }

    /// The edge of the one store, which holds `project_id`'s streams (T14).
    fn edge(&self, project_id: &str) -> Option<Arc<ProjectEdge>> {
        let _ = project_id;
        Some(self.core.edge())
    }

    /// Send `sent` to its store.
    async fn send(&mut self, sent: &Sent) -> Taken {
        let Some(edge) = self.edge(&sent.project_id) else {
            return Taken::Again;
        };
        match edge.handle(sent.book.clone()).await {
            Ok(_) => Taken::Done,
            Err(Failure::Rejected(rejected)) => {
                (self.say)(&format!("{} refused: {}", sent.key, rejected.report()));
                Taken::Refused
            }
            Err(other) => {
                if self.complained.insert(sent.key.clone()) {
                    (self.say)(&format!("{} failed: {other:?}", sent.key));
                }
                Taken::Again
            }
        }
    }

    async fn take(&mut self, project_id: &str, step: Step) -> Taken {
        match step {
            Step::Send(sent) => self.send(&sent).await,
            Step::Launch { mint, failed, .. } => {
                let claim = Sent {
                    key: mint.claim.key.clone(),
                    project_id: mint.claim.project_id.clone(),
                    book: mint.claim.book(),
                };
                let made = Sent {
                    key: mint.mint.key.clone(),
                    project_id: mint.mint.project_id.clone(),
                    book: mint.mint.book(),
                };
                let mut outcome = self.send(&claim).await;
                if matches!(outcome, Taken::Done) {
                    outcome = self.send(&made).await;
                }
                match outcome {
                    Taken::Refused => self.send(&failed).await,
                    other => other,
                }
            }
            Step::Start {
                at,
                harp,
                engine,
                project_dir,
                ..
            } => {
                self.start(project_id, at, &harp, &engine, &project_dir)
                    .await
            }
            Step::Feed {
                at,
                turn,
                urgency,
                messages,
                ..
            } => self.feed(&at, turn, urgency, &messages),
            Step::Clear { at, .. } => match self.hosted.get(&at.run()) {
                Some(hosted) => match hosted.control.clear() {
                    Ok(_) => Taken::Done,
                    Err(_) => Taken::Again,
                },
                None => Taken::Again,
            },
            Step::End { at, .. } => {
                self.envs.remove(&at.session);
                if let Some(hosted) = self.hosted.get(&at.run()) {
                    let _ = hosted.control.end(END_GRACE);
                }
                Taken::Done
            }
        }
    }

    /// Hand a turn to its hosted child: each message with its urgency, the
    /// informs riding on the message that starts the turn.
    fn feed(&mut self, at: &RunAt, turn: u64, urgency: TurnUrgency, messages: &[Fed]) -> Taken {
        let Some(hosted) = self.hosted.get_mut(&at.run()) else {
            return Taken::Again;
        };
        let mut ordered: Vec<&Fed> = messages
            .iter()
            .filter(|m| m.urgency == Urgency::Inform)
            .collect();
        ordered.extend(messages.iter().filter(|m| m.urgency != Urgency::Inform));
        for message in ordered {
            hosted.turns.insert(message.message_id.clone(), turn);
            let sent = hosted.control.send(
                ChildMessage::new(message.message_id.clone(), message.text.clone()),
                child_urgency(message.urgency, urgency),
            );
            if sent.is_err() {
                return Taken::Again;
            }
        }
        Taken::Done
    }

    /// Host a run for the child `at` names: plan it by its project (D18),
    /// enqueue it judged on its launcher's environment (its three tokens
    /// minted; rulings 53, 73e), prepare its session home, start its engine
    /// and record the runner started. A child that cannot be planned or is
    /// refused ends its incarnation and is released by its owner (D-N47); one
    /// that cannot start ends its run and its incarnation, so nothing is left
    /// owed.
    async fn start(
        &mut self,
        project_id: &str,
        at: RunAt,
        harp: &str,
        engine: &str,
        project_dir: &str,
    ) -> Taken {
        let Some(edge) = self.edge(project_id) else {
            return Taken::Again;
        };
        let facts = Facts::new(Arc::clone(&edge));
        let run = at.run();
        if engine != session_policies::CHILD_ENGINE {
            (self.say)(&format!("{harp}: no child engine for {engine}"));
            let _ = facts
                .incarnation_ended(&at.session, at.incarnation, "launch-failed")
                .await;
            return Taken::Done;
        }
        let env = self.child_env(&at.session);
        let child = match self.plan_child(engine, project_dir, &env) {
            Ok(child) => child,
            Err(code) => {
                (self.say)(&format!("{harp}: not planned: {code}"));
                self.launch_refused(project_id, &at, &code).await;
                return Taken::Done;
            }
        };
        let lock = match self.run_locks.hold_run(&run) {
            Ok(lock) => lock,
            Err(report) => {
                (self.say)(&format!("{harp}: the run's lock: {report:?}"));
                return Taken::Again;
            }
        };
        let enqueued = Enqueuer::new(Arc::clone(&edge), Arc::new(OsTokenSource))
            .enqueue(EnqueueRequest {
                session: at.session.clone(),
                incarnation: at.incarnation,
                mode: RunMode::Structured,
                evidence: child.evidence.clone(),
                launcher_lock: lock.path().to_string(),
            })
            .await;
        let enqueued = match enqueued {
            Ok(enqueued) => enqueued,
            // Judged as any child is (rulings 53, 73e): refused for good.
            Err(EnqueueError::Refused(rejection)) => {
                let code = angzarr_diagnostic::Diagnostic::code(&rejection).to_string();
                (self.say)(&format!("{harp}: refused: {code}"));
                drop(lock);
                self.launch_refused(project_id, &at, &code).await;
                return Taken::Done;
            }
            Err(EnqueueError::Failed(error)) => {
                (self.say)(&format!("{harp}: not enqueued: {error:?}"));
                return Taken::Again;
            }
        };
        let who = Identity {
            run: root_of(&run),
            session: root_of(&at.session),
            incarnation: at.incarnation,
            harp: harp.to_string(),
            depth: 1,
            principal: Principal::Runner,
        };
        let started = self.spawn_child(&at, harp, project_dir, &child, &env, &enqueued.hook_token);
        let (child, transcripts) = match started {
            Ok(started) => started,
            Err(detail) => {
                (self.say)(&format!("{harp}: not started: {detail}"));
                let _ = facts.launch_failed(&who.run, false, &detail).await;
                let _ = facts
                    .incarnation_ended(&at.session, at.incarnation, "launch-failed")
                    .await;
                return Taken::Done;
            }
        };
        let pid = child.control.pid();
        let _ = facts
            .record(
                &who,
                RunnerFact::RunnerStarted(runs::RunnerStarted {
                    runtime: "host".to_string(),
                    workspace: project_dir.to_string(),
                    pid,
                }),
            )
            .await;
        (self.say)(&format!("{harp}: started, pid {pid}"));
        self.host(child, transcripts, project_id, at, who, lock);
        Taken::Done
    }

    /// Hold the started `child`: its observations passed to the daemon's
    /// channel from a thread of their own, its control and its run's lock
    /// kept for its life.
    fn host(
        &mut self,
        child: run_ports::StartedChild,
        transcripts: Option<String>,
        project_id: &str,
        at: RunAt,
        who: Identity,
        lock: HeldLock,
    ) {
        let run = at.run();
        let tell = self.tell.clone();
        let heard_of = run.clone();
        let observations = child.observations;
        std::thread::spawn(move || {
            for observation in observations {
                if tell.send((heard_of.clone(), observation)).is_err() {
                    return;
                }
            }
        });
        self.hosted.insert(
            run,
            Hosted {
                project_id: project_id.to_string(),
                at,
                who,
                control: child.control,
                turns: BTreeMap::new(),
                transcripts,
                _lock: lock,
            },
        );
    }

    /// The hosted child of `project_dir` on `engine`, planned by its
    /// project's dry run and judged on `env` (D-N46, D-N47); the plan's error
    /// code where it cannot be planned.
    fn plan_child(
        &self,
        engine: &str,
        project_dir: &str,
        env: &BTreeMap<String, String>,
    ) -> Result<run_application::HostedChild, String> {
        let previewed = super::child_plan(&self.home, &self.temp, project_dir, env)?;
        Ok(run_application::hosted_child(
            &previewed,
            engine,
            &[session_policies::CHILD_ENGINE.to_string()],
            engine_claude::Claude::new().credentials(),
            env,
        ))
    }

    /// The environment the child `session` is started with: the one held,
    /// else its launcher's hand-off (taken, D-N45), else none at all (a child
    /// no launcher handed anything is judged with no credential).
    fn child_env(&mut self, session: &str) -> BTreeMap<String, String> {
        if let Some(env) = self.envs.get(session) {
            return env.clone();
        }
        let env = self.files.take_hand_off(session).unwrap_or_default();
        self.envs.insert(session.to_string(), env.clone());
        env
    }

    /// A child that could not be started (`code`, a refusal's or a plan's):
    /// its incarnation ended, and, for its launch (its first incarnation),
    /// its owner told the launch failed (K2's compensation), so nothing is
    /// left owed and the owner holds it no longer.
    async fn launch_refused(&mut self, project_id: &str, at: &RunAt, code: &str) {
        let Some(edge) = self.edge(project_id) else {
            return;
        };
        let facts = Facts::new(edge);
        let _ = facts
            .incarnation_ended(&at.session, at.incarnation, "launch-failed")
            .await;
        if at.incarnation != 1 {
            return;
        }
        let owner = self
            .board
            .projector()
            .board()
            .session(&at.session)
            .and_then(|s| s.owner.clone());
        let child = sessions::SessionRef {
            project_id: project_id.to_string(),
            session: at.session.clone(),
        };
        if let Some(failed) = owner
            .as_ref()
            .and_then(|owner| session_policies::launch_failed(owner, &child, code))
        {
            let _ = self.send(&Sent::from(failed)).await;
        }
    }

    /// The child's session home prepared with its project's context and its
    /// engine started with its launcher's environment, told the daemon's
    /// listener for its hooks.
    fn spawn_child(
        &self,
        at: &RunAt,
        harp: &str,
        project_dir: &str,
        child: &run_application::HostedChild,
        sent: &BTreeMap<String, String>,
        hook_token: &run_ports::Token,
    ) -> Result<(run_ports::StartedChild, Option<String>), String> {
        let claude = engine_claude::Claude::new();
        let mut dirs = SessionDirs::new(Arc::clone(&self.fs), PathBuf::from(project_dir));
        let home = format!("{}/home", SessionRecords::dir(&dirs, harp));
        let prepared = claude
            .prepare(&PrepareRequest {
                home: home.clone(),
                context: child.context.clone(),
                offered: Vec::new(),
                interactive: false,
                cwd: Some(project_dir.to_string()),
                // Nothing read back to merge into yet (ruling 113's merge
                // reaches the launcher's homes; a hosted child's is a
                // follow-up of the merge of main).
                existing: BTreeMap::new(),
                // A hosted child's permissions passthrough (ruling 137) is
                // a follow-up of the merge of main.
                permissions: None,
                session_home: child.settings.session_home,
            })
            .map_err(|e| e.to_string())?;
        for file in &prepared.files {
            dirs.write(harp, &format!("home/{}", file.rel), &file.bytes)
                .map_err(|e| e.to_string())?;
        }
        let mut env = sent.clone();
        env.insert(
            run_ports::HOME_VARIABLE.to_string(),
            self.home.to_string_lossy().into_owned(),
        );
        if let Some(dir) = std::env::current_exe()
            .ok()
            .and_then(|exe| exe.parent().map(Path::to_path_buf))
        {
            let path = match env.get("PATH").filter(|path| !path.is_empty()) {
                Some(path) => format!("{}:{path}", dir.display()),
                None => dir.display().to_string(),
            };
            env.insert("PATH".to_string(), path);
        }
        env.extend(run_ports::hook_environment(
            &self.endpoint,
            &at.run(),
            &at.session,
            hook_token,
            &self.cert,
        ));
        env.insert(run_ports::HARP_VARIABLE.to_string(), harp.to_string());
        let spec = ChildSpec {
            home,
            context_path: prepared.context_path,
            env,
            cwd: Some(project_dir.to_string()),
            model: child.model.clone(),
            effort: child.effort.clone(),
            settings: child.settings.clone(),
            resume: None,
            session_id: None,
            // claude asks no permission over its stream: unused here.
            permissions: run_ports::ChildPermissions::default(),
        };
        let started = claude.start(&spec).map_err(|e| e.to_string())?;
        Ok((started, engine_claude::transcripts_of(&spec)))
    }

    /// Record what the children said; whether any said anything.
    async fn hear(&mut self) -> bool {
        let mut heard = Vec::new();
        while let Ok(one) = self.heard.try_recv() {
            heard.push(one);
        }
        let any = !heard.is_empty();
        for (run, observation) in heard {
            self.record(&run, observation).await;
        }
        any
    }

    async fn record(&mut self, run: &str, observation: ChildObservation) {
        let Some(hosted) = self.hosted.get(run) else {
            return;
        };
        let Some(edge) = self.edge(&hosted.project_id) else {
            return;
        };
        let facts = Facts::new(edge);
        let who = hosted.who.clone();
        let turn_of = |ids: &[String]| ids.iter().find_map(|id| hosted.turns.get(id).copied());
        let fact = match observation {
            ChildObservation::Started { native_id }
            | ChildObservation::Cleared {
                native_id: Some(native_id),
            } => Some(RunnerFact::EngineSessionBound(
                sessions::BindEngineSession {
                    transcript_path: hosted
                        .transcripts
                        .as_ref()
                        .map(|dir| format!("{dir}/{native_id}.jsonl"))
                        .unwrap_or_default(),
                    native_id,
                    engine_version: None,
                },
            )),
            ChildObservation::TurnStarted { message_ids } => turn_of(&message_ids)
                .map(|turn| RunnerFact::TurnStarted(runs::TurnStarted { turn, message_ids })),
            ChildObservation::TurnEnded {
                message_ids,
                outcome,
            } => turn_of(&message_ids).map(|turn| match outcome {
                TurnOutcome::Answered { native_id, .. } => {
                    RunnerFact::TurnCompleted(runs::TurnCompleted {
                        turn,
                        native_key: native_id.unwrap_or_default(),
                        answer_ref: String::new(),
                        exit_code: 0,
                        message_ids,
                    })
                }
                TurnOutcome::Failed { class, detail, .. } => {
                    RunnerFact::TurnFailed(runs::TurnFailed {
                        turn,
                        class: failure_class(class),
                        detail,
                        message_ids,
                    })
                }
            }),
            ChildObservation::Exited { exit, .. } => {
                let at = hosted.at.clone();
                let _ = facts
                    .record(
                        &who,
                        RunnerFact::RunExited(runs::RunExited {
                            code: exit.code.unwrap_or(-1),
                            signal: exit.signal.unwrap_or(0),
                        }),
                    )
                    .await;
                let _ = facts
                    .incarnation_ended(&at.session, at.incarnation, "runner-exit")
                    .await;
                (self.say)(&format!("{}: exited {exit:?}", who.harp));
                self.hosted.remove(run);
                return;
            }
            ChildObservation::InterruptAnswered { .. } | ChildObservation::Cleared { .. } => None,
        };
        if let Some(fact) = fact {
            if let Err(error) = facts.record(&who, fact).await {
                (self.say)(&format!("{}: not recorded: {error:?}", who.harp));
            }
        }
    }

    /// End every child still hosted (the daemon exits).
    fn end_all(&mut self) {
        for hosted in self.hosted.values() {
            let _ = hosted.control.end(END_GRACE);
        }
    }
}

/// The first of `commits` to resolve; never, for none.
async fn any_commit(mut commits: Vec<CommitWait>) {
    std::future::poll_fn(|cx| {
        let landed = commits
            .iter_mut()
            .any(|wait| std::pin::Pin::new(wait).poll(cx).is_ready());
        match landed {
            true => std::task::Poll::Ready(()),
            false => std::task::Poll::Pending,
        }
    })
    .await;
}

/// What a step came to.
enum Taken {
    /// It took effect: not again in this process.
    Done,
    /// It was refused: again once a store changed.
    Refused,
    /// It could not be taken now: again next round.
    Again,
}

fn failure_class(class: FailureClass) -> runs::TurnFailureClass {
    match class {
        FailureClass::RateLimited => runs::TurnFailureClass::RateLimited,
        FailureClass::CredentialRefused => runs::TurnFailureClass::CredentialRefused,
        FailureClass::EngineError => runs::TurnFailureClass::EngineError,
        FailureClass::Interrupted => runs::TurnFailureClass::Interrupted,
    }
}
