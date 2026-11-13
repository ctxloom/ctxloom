//! The launcher itself: the sweep at its start, then one round at a time.

use std::collections::{BTreeMap, BTreeSet};
use std::sync::Arc;

use angzarr_embedded::v1::Uuid;
use angzarr_embedded::{Catalog, CatchUp, Failure, Processed, ReadStore, Store, Watch};
use daemon_board::Boards;
use pending_launches::{PendingLaunch, PendingModel, StartedRun};
use pending_messages::PendingMessages;
use run_ports::{
    ChildControl, ChildMessage, ChildObservation, ChildSpec, Edge, FailureClass, HeldLock,
    PublicCert, RunLocks, TokenSource, TurnOutcome,
};
use runs::{RunMode, TurnUrgency};
use runs_application::{EnqueueError, EnqueueRequest, Enqueuer, Facts, Identity, RunnerFact};
use session_index::SessionIndexes;
use session_policies::{plan, Fed, RunAt, Scene, Sent, Step};
use sessions::Urgency;

use crate::ports::{ChildHomes, ChildPlans, Ears, HandOffs, HostedEngine};
use run_launcher::{Sweeper, Swept};

/// How long an ended child has to exit before its tree is stopped.
const END_GRACE: std::time::Duration = std::time::Duration::from_secs(5);

/// Where the daemon is, as a child it hosts is told: ctxloom's home, the
/// directory of the running program (its engine's `PATH` leads with it, so
/// a hook's bare `ctxloom` resolves), the operating system's name (how that
/// `PATH` is joined), the user's own home (what a `session_home: host`
/// engine is given), and its listener.
#[derive(Clone, Debug)]
pub struct Place {
    pub home: String,
    pub program_dir: Option<String>,
    pub os: String,
    pub user_home: Option<String>,
    pub endpoint: String,
    pub cert: PublicCert,
}

/// The read models the daemon plans from, each the same fold its kernel
/// runs inline (S32): handed in by the root, which registered them.
#[derive(Clone, Default)]
pub struct Folds {
    pub board: Boards,
    pub pending: PendingMessages,
    pub index: SessionIndexes,
}

/// A child the daemon hosts: its run, the runner's identity, its control,
/// the turn each message it was handed is in, and its run's lock (held for
/// the run's life, rulings 86, 92: the sweep sees the run alive).
struct Hosted {
    at: RunAt,
    who: Identity,
    control: Box<dyn ChildControl>,
    turns: BTreeMap<String, u64>,
    /// Where its engine writes its transcripts: a bind names the transcript
    /// of its native id, as the engine's hook would (D-N51).
    transcripts: Option<String>,
    _lock: HeldLock,
}

/// What a step came to.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Taken {
    /// It took effect: not again in this process.
    Done,
    /// It was refused: again once the store changed.
    Refused,
    /// It could not be taken now: again next round.
    Again,
}

/// The daemon's launcher over the one store's edge `E`, minting run tokens
/// from `T`.
pub struct Daemon<E: Edge, T: TokenSource> {
    edge: Arc<E>,
    tokens: Arc<T>,
    engines: Vec<HostedEngine>,
    plans: Box<dyn ChildPlans>,
    hand_offs: Box<dyn HandOffs>,
    homes: Box<dyn ChildHomes>,
    ears: Box<dyn Ears>,
    run_locks: Arc<dyn RunLocks>,
    place: Place,
    say: Box<dyn FnMut(&str) + Send>,
    board: CatchUp<Boards>,
    pending: CatchUp<PendingMessages>,
    index: CatchUp<SessionIndexes>,
    launches: CatchUp<PendingModel>,
    /// Each hosted child's environment, by its session's stable id: its
    /// launcher's, taken from the hand-off at its first start (S6, D-N45),
    /// held for its life, dropped at its end. Never the daemon's own.
    envs: BTreeMap<String, BTreeMap<String, String>>,
    hosted: BTreeMap<String, Hosted>,
    done: BTreeSet<String>,
    /// Steps refused this time round: tried again once the store changes.
    refused: BTreeSet<String>,
    /// The steps whose failure was said already (once, not each round).
    complained: BTreeSet<String>,
}

/// The ports a [`Daemon`] is built over.
pub struct Ports<E: Edge, T: TokenSource> {
    pub edge: Arc<E>,
    pub tokens: Arc<T>,
    pub engines: Vec<HostedEngine>,
    pub plans: Box<dyn ChildPlans>,
    pub hand_offs: Box<dyn HandOffs>,
    pub homes: Box<dyn ChildHomes>,
    pub ears: Box<dyn Ears>,
    pub run_locks: Arc<dyn RunLocks>,
    pub place: Place,
    pub folds: Folds,
    /// The daemon's log.
    pub say: Box<dyn FnMut(&str) + Send>,
}

/// A message's urgency as a child takes it, within a turn of `turn`.
pub(crate) fn child_urgency(message: Urgency, turn: TurnUrgency) -> run_ports::Urgency {
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

/// Whether a catch-up's step handed anything.
fn handed(step: &Result<Option<angzarr_embedded::Caught>, angzarr_embedded::KernelError>) -> bool {
    matches!(step, Ok(Some(caught)) if !caught.handed().is_empty())
}

/// Whether either catch-up was handed anything: the store changed.
// Equivalent under `&&`: the board and the pending messages fold the same
// sources (sessions, runs), so either is handed a book exactly when the
// other is.
#[cfg_attr(test, mutants::skip)]
fn any_handed(board: bool, pending: bool) -> bool {
    board || pending
}

fn failure_class(class: FailureClass) -> runs::TurnFailureClass {
    match class {
        FailureClass::RateLimited => runs::TurnFailureClass::RateLimited,
        FailureClass::CredentialRefused => runs::TurnFailureClass::CredentialRefused,
        FailureClass::EngineError => runs::TurnFailureClass::EngineError,
        FailureClass::Interrupted => runs::TurnFailureClass::Interrupted,
    }
}

impl<E: Edge, T: TokenSource> Daemon<E, T> {
    pub fn new(ports: Ports<E, T>) -> Daemon<E, T> {
        let Ports {
            edge,
            tokens,
            engines,
            plans,
            hand_offs,
            homes,
            ears,
            run_locks,
            place,
            folds,
            say,
        } = ports;
        Daemon {
            edge,
            tokens,
            engines,
            plans,
            hand_offs,
            homes,
            ears,
            run_locks,
            place,
            say,
            board: daemon_board::caught_up(folds.board),
            pending: pending_messages::caught_up(folds.pending),
            index: session_index::SessionIndex::caught_up(folds.index),
            launches: pending_launches::catch_up(),
            envs: BTreeMap::new(),
            hosted: BTreeMap::new(),
            done: BTreeSet::new(),
            refused: BTreeSet::new(),
            complained: BTreeSet::new(),
        }
    }

    /// How many children it hosts now.
    pub fn hosting(&self) -> usize {
        self.hosted.len()
    }

    /// The runs it hosts, each as its session's stable id and its run's
    /// root (text), in run order: what `GetDaemon` names.
    pub fn hosted_runs(&self) -> Vec<(String, String)> {
        self.hosted
            .iter()
            .map(|(run, hosted)| (hosted.at.session.clone(), run.clone()))
            .collect()
    }

    /// How many runs are started and not ended (M9): every run the store
    /// holds whose runner started, the CLI's interactive sessions and
    /// one-shots as well as the children it hosts, once those whose owner is
    /// gone are ended (the sweep of its start, of every run it does not host
    /// itself). What is left keeps the daemon from idling.
    pub async fn live_runs<S: ReadStore + Store + Processed>(
        &mut self,
        store: &S,
        catalog: &Catalog,
    ) -> Result<usize, String> {
        let started = self.caught_up_launches(store, catalog).await?.1;
        let theirs: Vec<StartedRun> = started
            .iter()
            .filter(|run| !self.hosted.values().any(|hosted| hosted.who.run == run.run))
            .cloned()
            .collect();
        let swept = self.swept(&[], &theirs).await?;
        Ok(started.len().saturating_sub(swept.len()))
    }

    /// The engines it can host a child on, by name.
    pub fn engines(&self) -> Vec<String> {
        self.engines.iter().map(|e| e.name().to_string()).collect()
    }

    fn engine(&self, name: &str) -> Option<HostedEngine> {
        self.engines.iter().find(|e| e.name() == name).cloned()
    }

    /// The daemon's FIRST step (T18): every run the store holds caught up,
    /// and the ones whose owner is gone ended (the sweep every launcher
    /// runs: `LaunchOrphaned` for one owed a runner, `RunnerLost` for one
    /// started, its incarnation ended `orphaned`), before anything is
    /// hosted. What it ended.
    pub async fn start_up<S: ReadStore + Store + Processed>(
        &mut self,
        store: &S,
        catalog: &Catalog,
    ) -> Result<Vec<Swept>, String> {
        self.sweep(store, catalog).await
    }

    async fn sweep<S: ReadStore + Store + Processed>(
        &mut self,
        store: &S,
        catalog: &Catalog,
    ) -> Result<Vec<Swept>, String> {
        let (owed, started) = self.caught_up_launches(store, catalog).await?;
        self.swept(&owed, &started).await
    }

    /// The runs owed a runner, and the runs started and not ended, as the
    /// store holds them now.
    async fn caught_up_launches<S: ReadStore + Store + Processed>(
        &mut self,
        store: &S,
        catalog: &Catalog,
    ) -> Result<(Vec<PendingLaunch>, Vec<StartedRun>), String> {
        let caught = self
            .launches
            .step(store, catalog)
            .await
            .map_err(|e| e.to_string())?;
        if let Some((_, failure)) = caught.failures().first() {
            return Err(failure.to_string());
        }
        let pending = self.launches.projector().pending();
        Ok((pending.launches(), pending.started()))
    }

    /// Of `owed` and `started`, the runs whose owner is gone, ended and said.
    async fn swept(
        &mut self,
        owed: &[PendingLaunch],
        started: &[StartedRun],
    ) -> Result<Vec<Swept>, String> {
        let swept = Sweeper::new(Arc::clone(&self.edge), Arc::clone(&self.run_locks))
            .sweep(owed, started)
            .await
            .map_err(|e| angzarr_diagnostic::Diagnostic::code(&e).to_string())?;
        for run in &swept {
            (self.say)(&format!(
                "{}: its owner is gone; ended {}",
                run.session,
                match run.started {
                    true => "runner-lost",
                    false => "launch-orphaned",
                }
            ));
        }
        Ok(swept)
    }

    /// One round: hear the children, catch up, plan, act. Whether it did
    /// anything. While `draining`, no child is started.
    pub async fn tick<S: ReadStore + Store + Processed + Watch>(
        &mut self,
        store: &S,
        catalog: &Catalog,
        draining: bool,
    ) -> bool {
        let mut busy = self.hear().await;
        let board = self.board.step_if_changed(store, catalog).await;
        let pending = self.pending.step_if_changed(store, catalog).await;
        let _ = self.index.step_if_changed(store, catalog).await;
        if any_handed(handed(&board), handed(&pending)) {
            self.refused.clear();
        }
        for (project_id, step) in self.plan() {
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

    /// The steps owed now, for each project on the board (one store holds
    /// every project's, T14; a plan is one project's).
    fn plan(&self) -> Vec<(String, Step)> {
        let hosted: BTreeSet<String> = self.hosted.keys().cloned().collect();
        let skip: BTreeSet<String> = self.done.union(&self.refused).cloned().collect();
        let board = self.board.projector().board();
        let pending = self.pending.projector().pending();
        let projects: BTreeSet<String> = board
            .sessions()
            .map(|(_, session)| session.project_id.clone())
            .collect();
        let mut steps = Vec::new();
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
        steps
    }

    /// Send `sent` to the store.
    async fn send(&mut self, sent: &Sent) -> Taken {
        match self.edge.handle(sent.book.clone()).await {
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
    /// and record the runner started. A child on an engine the daemon hosts
    /// none of, or that cannot be planned or is refused, ends its
    /// incarnation and is released by its owner (D-N47); one that cannot
    /// start ends its run and its incarnation, so nothing is left owed.
    async fn start(
        &mut self,
        project_id: &str,
        at: RunAt,
        harp: &str,
        engine: &str,
        project_dir: &str,
    ) -> Taken {
        let facts = Facts::new(Arc::clone(&self.edge));
        let run = at.run();
        let Some(hosted_engine) = self.engine(engine) else {
            (self.say)(&format!("{harp}: no child engine for {engine}"));
            let _ = facts
                .incarnation_ended(&at.session, at.incarnation, "launch-failed")
                .await;
            return Taken::Done;
        };
        let env = self.child_env(&at.session);
        let child = match self.plan_child(&hosted_engine, project_dir, &env) {
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
        let enqueued = Enqueuer::new(Arc::clone(&self.edge), Arc::clone(&self.tokens))
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
        };
        let started = self.spawn_child(
            &hosted_engine,
            &at,
            harp,
            project_dir,
            &child,
            &env,
            &enqueued.hook_token,
        );
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
        let recorded = facts
            .record(
                &who,
                RunnerFact::RunnerStarted(runs::RunnerStarted {
                    runtime: "host".to_string(),
                    workspace: project_dir.to_string(),
                    pid,
                }),
            )
            .await;
        if let Err(error) = recorded {
            // Hosted all the same: its run lock says it is alive, and the
            // start is recorded again by no one; the run reads unstarted
            // until it ends (its exit is recorded as any is).
            (self.say)(&format!("{harp}: its start not recorded: {error:?}"));
        }
        (self.say)(&format!("{harp}: started, pid {pid}"));
        self.host(child, transcripts, at, who, lock);
        Taken::Done
    }

    /// Hold the started `child`: its observations heard through the ears,
    /// its control and its run's lock kept for its life.
    fn host(
        &mut self,
        child: run_ports::StartedChild,
        transcripts: Option<String>,
        at: RunAt,
        who: Identity,
        lock: HeldLock,
    ) {
        let run = at.run();
        self.ears.listen(run.clone(), child.observations);
        self.hosted.insert(
            run,
            Hosted {
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
    /// project's dry run and judged on `env` (D-N46, D-N47); the plan's
    /// error code where it cannot be planned.
    fn plan_child(
        &self,
        engine: &HostedEngine,
        project_dir: &str,
        env: &BTreeMap<String, String>,
    ) -> Result<run_application::HostedChild, String> {
        let previewed = self.plans.preview(engine.name(), project_dir, env)?;
        Ok(run_application::hosted_child(
            &previewed,
            engine.name(),
            &self.engines(),
            engine.engine.credentials(),
            env,
        ))
    }

    /// The environment the child `session` is started with: the one held,
    /// else its launcher's hand-off (taken, D-N45), else none at all (a
    /// child no launcher handed anything is judged with no credential).
    fn child_env(&mut self, session: &str) -> BTreeMap<String, String> {
        if let Some(env) = self.envs.get(session) {
            return env.clone();
        }
        let env = self.hand_offs.take(session).unwrap_or_default();
        self.envs.insert(session.to_string(), env.clone());
        env
    }

    /// A child that could not be started (`code`, a refusal's or a plan's):
    /// its incarnation ended, and, for its launch (its first incarnation),
    /// its owner told the launch failed (K2's compensation), so nothing is
    /// left owed and the owner holds it no longer.
    async fn launch_refused(&mut self, project_id: &str, at: &RunAt, code: &str) {
        let facts = Facts::new(Arc::clone(&self.edge));
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
    #[allow(clippy::too_many_arguments)]
    fn spawn_child(
        &self,
        engine: &HostedEngine,
        at: &RunAt,
        harp: &str,
        project_dir: &str,
        child: &run_application::HostedChild,
        sent: &BTreeMap<String, String>,
        hook_token: &run_ports::Token,
    ) -> Result<(run_ports::StartedChild, Option<String>), String> {
        // The one session-setup path (owner, 2026-11-12): the launcher's,
        // over the child's project's session directories, so a hosted
        // child's home gets what every session's does (the engine's merge,
        // its seeds, her git identity, and what the engine was composed
        // with: her own settings, the program its callbacks run).
        let mut records = self.homes.records(project_dir);
        let set_up = run_launcher::set_up_home(
            &*engine.engine,
            &mut *records,
            &run_launcher::HomeSetup {
                harp: harp.to_string(),
                context: child.context.clone(),
                interactive: false,
                cwd: Some(project_dir.to_string()),
                // A hosted child's permissions passthrough (ruling 137) is a
                // follow-up.
                permissions: None,
                session_home: child.settings.session_home,
                git_user: self.plans.git_user(project_dir, sent),
            },
        )?;
        let home = set_up.home;
        let spec = ChildSpec {
            home,
            context_path: set_up.context_path,
            env: self.child_environment(at, harp, sent, hook_token),
            user_home: self.place.user_home.clone(),
            cwd: Some(project_dir.to_string()),
            model: child.model.clone(),
            effort: child.effort.clone(),
            settings: child.settings.clone(),
            resume: None,
            session_id: None,
            // claude asks no permission over its stream: unused here.
            permissions: run_ports::ChildPermissions::default(),
        };
        let started = engine.child.start(&spec).map_err(|e| e.to_string())?;
        Ok((started, (engine.transcripts)(&spec)))
    }

    /// The environment a child's engine is started with: its launcher's,
    /// ctxloom's home, `PATH` led by the program's directory, and the run's
    /// hook variables (always the run's own).
    fn child_environment(
        &self,
        at: &RunAt,
        harp: &str,
        sent: &BTreeMap<String, String>,
        hook_token: &run_ports::Token,
    ) -> BTreeMap<String, String> {
        let mut env = sent.clone();
        env.insert(
            run_ports::HOME_VARIABLE.to_string(),
            self.place.home.clone(),
        );
        if let Some(dir) = &self.place.program_dir {
            let path = run_ports::search_path_led_by(
                &self.place.os,
                dir,
                env.get("PATH").map(String::as_str),
            );
            env.insert("PATH".to_string(), path);
        }
        env.extend(run_ports::hook_environment(
            &self.place.endpoint,
            &at.run(),
            &at.session,
            hook_token,
            &self.place.cert,
        ));
        env.insert(run_ports::HARP_VARIABLE.to_string(), harp.to_string());
        env
    }

    /// Record what the children said; whether any said anything.
    async fn hear(&mut self) -> bool {
        let heard = self.ears.heard();
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
        let facts = Facts::new(Arc::clone(&self.edge));
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

    /// End every child still hosted (the daemon exits), each given its
    /// grace.
    pub fn end_all(&mut self) {
        for hosted in self.hosted.values() {
            let _ = hosted.control.end(END_GRACE);
        }
    }
}
