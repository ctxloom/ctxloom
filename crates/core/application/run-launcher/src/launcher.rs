//! The runner's launch: a [`CatchUp`] over the pending launches hands each run
//! still owed a runner to the engine for ONE turn, through the
//! [`RunnerLauncher`], and records what came of it as the run's facts.
//!
//! The order of a launch, and why:
//! 1. the launch is made ([`crate::launch_of`], T10: the session home
//!    written, the engine's command built, the permits applied, the program
//!    found, the command checked); a launch that fails here has started
//!    nothing, and is a `RecordLaunchFailed`;
//! 2. `RecordRunnerAttached` is recorded, then the launch is SPAWNED
//!    ([`RunnerLauncher::start`]), and only then are `RecordRunnerStarted` (with the
//!    pid the spawn gave) and `RecordTurnStarted` recorded, while the command runs:
//!    the pending launches clear on `RecordRunnerStarted` (not on a runner's first
//!    word, which a spawned and silent turn never says), so a catch-up that
//!    reads the streams while the command runs does not launch the run
//!    again. A command that cannot be spawned is a `RecordLaunchFailed`, which ends
//!    the run, and never a `RecordRunnerStarted`;
//! 3. the command runs to its end ([`Running::finish`]; a wait that breaks
//!    after the spawn is a `RecordLaunchFailed` too); one that ran is read by the engine
//!    ([`Engine::finish_turn`]): the reply is the launch's answer, the
//!    engine's own session id is bound to the session, and the turn is
//!    `RecordTurnCompleted` or `RecordTurnFailed`;
//! 4. `RecordRunExited` ends the run.
//!
//! The transcript is read from the capture facts on the session's stream (no
//! file, owner ruling 87a). A child's turn's facts are its hooks' alone (owner
//! rulings 26 and 51): the launcher records none of its prompt or its words,
//! so a turn whose hooks never reach the listener has no conversation in its
//! transcript, though its reply is still the launch's answer. The terminal
//! session's opening prompt is its hooks' to record too, like every other of
//! its prompts. The events carry no file reference (`answer_ref` is empty).
//!
//! The run's own lock (rulings 86, 92), which its enqueuer holds through the
//! run lock table, is let go once `RecordRunnerStarted` is recorded (or the launch
//! failed); from then on the session's lock is the run's evidence (ruling
//! 93). A step also sweeps the runs other processes left: one still owed a
//! runner whose own lock is free, and one started whose session's lock is
//! free, have lost their owner. The sweep probes the lock, THEN re-reads the
//! run's stream (an owner records its start or end before it lets its lock
//! go), and records the run's end, keyed by the run so a second sweeper's is
//! a DUPLICATE: `RecordLaunchOrphaned` for one owed a runner (no launch failed,
//! ruling 100), `RecordRunnerLost` for one started (runner loss, ruling 95); then
//! the end of the session's incarnation (`orphaned`).

use std::sync::Arc;

use angzarr_embedded::v1::Uuid;
use angzarr_embedded::{Catalog, CatchUp, Processed, ReadStore, Store};
use pending_launches::{catch_up, PendingLaunch, PendingModel, StartedRun};
use run_ports::{
    Edge, Engine, FailureClass, Launch, LaunchAt, LaunchKind, Limits, RunnerLauncher,
    SessionRecords, TurnOutcome,
};
use runs_application::{Facts, Identity, RunnerFact};

use crate::error::LaunchError;
use crate::launch::{LaunchEngines, LaunchRequest, NotLaunched, Unprepared};
use crate::order::{Order, RunLockTable};
use crate::record::{order_of, RecordError};
use crate::workspace::Placement;

/// The cause the session's incarnation ends with when its run's owner was
/// lost.
const ORPHANED: &str = "orphaned";

/// The turn a one-shot takes.
const TURN: u64 = 1;

/// The name a runner attaches with.
const RUNNER: &str = "host-launcher";

/// What the runner speaks: one command per turn.
const PROTOCOL: &str = "turn-as-command";

/// What came of one launch.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Outcome {
    Answered {
        answer: String,
        /// The engine's own session id, bound to the session.
        native_id: Option<String>,
    },
    Failed {
        class: FailureClass,
        detail: String,
    },
    /// The human's terminal session is over; it took no turns.
    Ended {
        code: Option<i32>,
    },
    /// Nothing was started: no engine, no home, no program.
    NotStarted {
        detail: String,
    },
    /// Nothing was started: the engine's command is too long for the
    /// platform to start (its terminal session's opening prompt), refused
    /// before the spawn.
    TooLong(run_ports::CommandTooLong),
    /// Nothing was started: the engine's program is a batch file (a
    /// `claude.cmd` shim on Windows) and an argument (the terminal session's
    /// opening prompt) has more than one line, which std will not hand
    /// cmd.exe. Refused before the spawn.
    BatchArguments(run_ports::BatchArguments),
    /// Nothing was started: the engine's program is on no directory of its
    /// PATH, by any name the host looks it up by; what was searched is said.
    ProgramNotFound(run_ports::ProgramNotFound),
}

/// A run launched, and its turn's outcome.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Launched {
    pub run: Uuid,
    pub session: String,
    pub harp: String,
    pub engine: String,
    pub outcome: Outcome,
    /// The caller's permits the launch did not follow (owner rulings 78,
    /// 78b), by the permit's name: the run says them.
    pub ignored: Vec<run_ports::Ignored>,
    /// The user's own engine settings the session home was not started from
    /// because they are not a JSON object (owner ruling 169), by path: the
    /// run says them.
    pub left_out: Vec<run_ports::LeftOut>,
    /// The checkout the engine worked in (the workspace axis); none in the
    /// live project. Its run releases it once the engine is over.
    pub checkout: Option<isolation_ports::Checkout>,
    /// What the launch placed in the checkout (relative to it): taken out
    /// at its release, before the checkout is removed.
    pub checkout_files: crate::workspace::CheckoutFiles,
    /// Why a worktree asked for could not be had: the engine worked in the
    /// project, and the run says so.
    pub workspace_warning: Option<String>,
}

/// The launcher: the engines it can drive, the way a command is run, the
/// session directory it writes, and the pending launches it catches up on.
pub struct Launcher<E: Edge, L: RunnerLauncher, R: SessionRecords> {
    edge: Arc<E>,
    facts: Facts<E>,
    runner: L,
    records: R,
    engines: Vec<Arc<dyn Engine>>,
    claims: Arc<RunLockTable>,
    sealing: Arc<dyn run_ports::Sealing>,
    limits: Limits,
    /// The OS name the commands are measured for (`std::env::consts::OS`,
    /// as the composition root takes it); none, nothing is measured.
    os: String,
    pending: CatchUp<PendingModel>,
    /// The locks other processes' runs are judged by; none: no sweep.
    locks: Option<Arc<dyn run_ports::RunLocks>>,
    /// Where a worktree is made; none: a run asking for one works in the
    /// project, and says so.
    workspaces: Option<Arc<dyn isolation_ports::Workspaces + Send + Sync>>,
    /// Where a turn's end is waited for (owner, 2026-11-19: on a thread
    /// parked for it, never an async worker); none: on the calling thread,
    /// which only a test without a runtime of workers should do.
    parks: Option<Arc<dyn run_ports::Parks>>,
}

impl<E: Edge, L: RunnerLauncher, R: SessionRecords> Launcher<E, L, R> {
    pub fn new(
        edge: Arc<E>,
        runner: L,
        records: R,
        engines: Vec<Arc<dyn Engine>>,
        claims: Arc<RunLockTable>,
        sealing: Arc<dyn run_ports::Sealing>,
        limits: Limits,
    ) -> Launcher<E, L, R> {
        Launcher {
            facts: Facts::new(Arc::clone(&edge)),
            edge,
            runner,
            records,
            engines,
            claims,
            sealing,
            limits,
            os: String::new(),
            pending: catch_up(),
            locks: None,
            workspaces: None,
            parks: None,
        }
    }

    /// The launcher, placing a run that asks for a worktree in a checkout
    /// `workspaces` makes (`crate::place_workspace`).
    pub fn with_workspaces(
        mut self,
        workspaces: Arc<dyn isolation_ports::Workspaces + Send + Sync>,
    ) -> Self {
        self.workspaces = Some(workspaces);
        self
    }

    /// The launcher, waiting for each command's end where `parks` says
    /// ([`run_ports::Parks`]): the composition root's, a blocking thread.
    pub fn with_parks(mut self, parks: Arc<dyn run_ports::Parks>) -> Self {
        self.parks = Some(parks);
        self
    }

    /// Whether a turn's end is waited for on a parked thread
    /// ([`Self::with_parks`] was said).
    pub fn parks_turns(&self) -> bool {
        self.parks.is_some()
    }

    /// The command's end, waited for where [`Self::with_parks`] said.
    async fn park(
        &self,
        running: Box<dyn run_ports::Running>,
    ) -> Result<run_ports::TurnOutput, run_ports::LaunchFailure> {
        match &self.parks {
            Some(parks) => parks.park(running).await,
            None => running.finish(),
        }
    }

    /// The launcher, probing `locks` for the owners of other processes' runs:
    /// a run still owed a runner whose own lock is free has lost its launcher
    /// (rulings 86, 92), and a started run whose session's lock is free has
    /// lost its owner (ruling 93). Whichever process catches up on it first
    /// records it ended: `RecordLaunchOrphaned` or `RecordRunnerLost` (rulings 95, 100).
    pub fn with_locks(mut self, locks: Arc<dyn run_ports::RunLocks>) -> Self {
        self.locks = Some(locks);
        self
    }

    /// The launcher, measuring each command against the command line of
    /// `os` before it is spawned ([`run_ports::check_command_line`]).
    pub fn on_os(mut self, os: &str) -> Self {
        self.os = os.to_string();
        self
    }

    /// Whether an engine of this name can be driven.
    pub fn drives(&self, engine: &str) -> bool {
        self.engines.iter().any(|e| e.name() == engine)
    }

    /// How the engine of this name is handed a credential; none for an
    /// engine that cannot be driven.
    pub fn credentials(&self, engine: &str) -> Option<run_ports::Credentials> {
        self.engine(engine).map(|e| e.credentials())
    }

    /// What the engine of this name says of `planned` (owner ruling 168):
    /// its notices, never a refusal; an engine that cannot be driven says
    /// nothing.
    pub fn advise(&self, engine: &str, planned: &run_ports::Planned) -> Vec<run_ports::Advice> {
        self.engine(engine)
            .map(|e| e.advise(planned))
            .unwrap_or_default()
    }

    /// The names of the engines that can be driven.
    pub fn engines(&self) -> Vec<String> {
        self.engines.iter().map(|e| e.name().to_string()).collect()
    }

    /// One step: catch up on the store, and launch every run owed a runner
    /// whose order this process holds. A run owed one with no order here is
    /// another process's, and is left.
    pub async fn step<S: ReadStore + Store + Processed>(
        &mut self,
        store: &S,
        catalog: &Catalog,
    ) -> Result<Vec<Launched>, LaunchError> {
        let caught = self
            .pending
            .step(store, catalog)
            .await
            .map_err(|e| LaunchError::CatchUp {
                ext_err_txt: e.to_string(),
            })?;
        if let Some((_, failure)) = caught.failures().first() {
            return Err(LaunchError::CatchUp {
                ext_err_txt: failure.to_string(),
            });
        }
        let (owed, launchable, started) = {
            let pending = self.pending.projector().pending();
            (pending.launches(), pending.launchable(), pending.started())
        };
        let mut launched = Vec::new();
        let mut others = Vec::new();
        for launch in owed {
            if !self.claims.holds(&launch.session, launch.incarnation) {
                others.push(launch);
                continue;
            }
            // Ours, and launched once the record says it may be: its mint
            // and its loadout are in the session's stream.
            let ready = launchable
                .iter()
                .any(|l| l.run == launch.run && l.incarnation == launch.incarnation);
            if ready {
                let order = match order_of(&launch, self.sealing.as_ref()) {
                    Ok(order) => order,
                    Err(error) => {
                        self.unrecorded(&launch, &error).await?;
                        continue;
                    }
                };
                launched.push(self.launch(launch, order).await?);
            }
        }
        if let Some(locks) = &self.locks {
            Sweeper::new(Arc::clone(&self.edge), Arc::clone(locks))
                .sweep(&others, &started)
                .await?;
        }
        Ok(launched)
    }

    /// A run whose record cannot be read back is ended launch-failed, naming
    /// why, never launched from nothing.
    async fn unrecorded(
        &self,
        launch: &PendingLaunch,
        error: &RecordError,
    ) -> Result<(), LaunchError> {
        let run = uuid::Uuid::from_slice(&launch.run.value)
            .map(|id| id.hyphenated().to_string())
            .unwrap_or_default();
        let reason = match error {
            RecordError::NoSpec => {
                return self
                    .end_unrecorded(launch, LaunchError::NoSpec { run })
                    .await
            }
            RecordError::Sealing(e) => e.to_string(),
            RecordError::Hooks(why) => why.clone(),
        };
        self.end_unrecorded(launch, LaunchError::Unreadable { run, reason })
            .await
    }

    async fn end_unrecorded(
        &self,
        launch: &PendingLaunch,
        error: LaunchError,
    ) -> Result<(), LaunchError> {
        self.facts
            .launch_failed(&launch.run, true, &error.to_string())
            .await
            .map(drop)
            .map_err(|e| LaunchError::Fact {
                reason: e.to_string(),
            })?;
        self.claims.release(&launch.session, launch.incarnation);
        Ok(())
    }

    async fn launch(
        &mut self,
        launch: PendingLaunch,
        order: Order,
    ) -> Result<Launched, LaunchError> {
        let session_root =
            uuid::Uuid::parse_str(&launch.session).map_err(|_| LaunchError::BadSession {
                session: launch.session.clone(),
            })?;
        let who = Identity {
            run: launch.run.clone(),
            session: Uuid::from_bytes(*session_root.as_bytes()),
            incarnation: launch.incarnation,
            harp: order.harp.clone(),
            depth: 0,
        };
        let mut ignored = Vec::new();
        let mut left_out = Vec::new();
        let mut placed = None;
        let at = LaunchAt {
            project_id: String::new(),
            session: launch.session.clone(),
            harp: order.harp.clone(),
            run: uuid::Uuid::from_slice(&launch.run.value)
                .map(|id| id.hyphenated().to_string())
                .unwrap_or_default(),
            incarnation: launch.incarnation,
        };
        let prepared = self.prepare(at, &order, &mut ignored, &mut left_out, &mut placed);
        let checkout = placed.as_ref().and_then(|p: &Placement| p.checkout.clone());
        let workspace_warning = placed.as_ref().and_then(|p| p.warning.clone());
        let checkout_files = placed.as_ref().map(|p| p.files.clone()).unwrap_or_default();
        let cwd = placed.map(|p| p.cwd).or_else(|| order.cwd.clone());
        let done = |outcome| Launched {
            run: launch.run.clone(),
            session: launch.session.clone(),
            harp: order.harp.clone(),
            engine: order.engine.clone(),
            outcome,
            ignored: ignored.clone(),
            left_out: left_out.clone(),
            checkout: checkout.clone(),
            checkout_files: checkout_files.clone(),
            workspace_warning: workspace_warning.clone(),
        };
        let (engine, made) = match prepared {
            Ok(prepared) => prepared,
            Err(unprepared) => {
                let outcome = self.unprepared(&who, &launch, unprepared).await?;
                return Ok(done(outcome));
            }
        };
        self.record(&who, attached()).await?;
        if order.interactive {
            // The terminal session's opening prompt, like every prompt of it,
            // is its hooks' to record (the engine submits it and its prompt
            // hook fires): the launcher records the run's own facts only.
            return self
                .terminal(&who, &order, cwd.as_deref(), &made, done)
                .await;
        }
        // A child's turn is heard through its hooks alone (owner rulings 26
        // and 51): its prompt and its words are theirs to record, and the
        // stream is read below for the reply and the turn's outcome only.
        let running = match self.runner.start(&made, &self.limits) {
            Ok(running) => running,
            Err(failure) => return self.never_started(&who, failure, done).await,
        };
        self.record(&who, runner_started(&order, cwd.as_deref(), running.pid()))
            .await?;
        // Started: the run's own lock has done its work (ruling 92).
        self.claims.release(&launch.session, launch.incarnation);
        self.record(&who, turn_started()).await?;
        let output = match self.park(running).await {
            Ok(output) => output,
            Err(failure) => return self.never_started(&who, failure, done).await,
        };
        let outcome = self.turn_ended(&who, engine.as_ref(), &output).await?;
        Ok(done(outcome))
    }

    /// The turn's command is over: what the engine reads of it recorded (its
    /// own session bound, the turn completed or failed), then the run's
    /// exit.
    async fn turn_ended(
        &self,
        who: &Identity,
        engine: &dyn Engine,
        output: &run_ports::TurnOutput,
    ) -> Result<Outcome, LaunchError> {
        let outcome = engine.finish_turn(output);
        let code = output.exit.code.unwrap_or(0);
        let outcome = match outcome {
            TurnOutcome::Answered { answer, native_id } => {
                if let Some(native) = &native_id {
                    self.record(who, bound(native)).await?;
                }
                self.record(
                    who,
                    RunnerFact::TurnCompleted(runs::RecordTurnCompleted {
                        turn: TURN,
                        native_key: native_id.clone().unwrap_or_default(),
                        // No file holds the answer (ruling 87a).
                        answer_ref: String::new(),
                        exit_code: code,
                        message_ids: Vec::new(),
                        answer: answer.clone(),
                    }),
                )
                .await?;
                Outcome::Answered { answer, native_id }
            }
            TurnOutcome::Failed {
                class,
                detail,
                native_id: _,
            } => {
                self.record(
                    who,
                    RunnerFact::TurnFailed(runs::RecordTurnFailed {
                        turn: TURN,
                        class: failure_class(class),
                        detail: detail.clone(),
                        message_ids: Vec::new(),
                    }),
                )
                .await?;
                Outcome::Failed { class, detail }
            }
        };
        self.record(
            who,
            RunnerFact::RunExited(runs::RecordRunExited {
                code,
                signal: output.exit.signal.unwrap_or(0),
            }),
        )
        .await?;
        Ok(outcome)
    }

    /// The human's session: the command is given her terminal and runs until
    /// she ends it, taking no turns of the run's; its exit ends the run.
    async fn terminal(
        &self,
        who: &Identity,
        order: &Order,
        cwd: Option<&str>,
        made: &Launch,
        done: impl Fn(Outcome) -> Launched,
    ) -> Result<Launched, LaunchError> {
        let running = match self.runner.start(made, &self.limits) {
            Ok(running) => running,
            Err(failure) => return self.never_started(who, failure, done).await,
        };
        self.record(who, runner_started(order, cwd, running.pid()))
            .await?;
        self.claims.release(&who_session(who), who.incarnation);
        let output = match self.park(running).await {
            Ok(output) => output,
            Err(failure) => return self.never_started(who, failure, done).await,
        };
        // Nothing is bound or observed at the exit (owner 2026-11-15): the
        // engine's own conversation is the one its SessionStart bound,
        // delivered like any capture fact, and the session's end is its
        // run's end.
        self.record(
            who,
            RunnerFact::RunExited(runs::RecordRunExited {
                code: output.exit.code.unwrap_or(0),
                signal: output.exit.signal.unwrap_or(0),
            }),
        )
        .await?;
        Ok(done(Outcome::Ended {
            code: output.exit.code,
        }))
    }

    /// The launch's preparation started nothing: record why (deterministic:
    /// the same order prepares the same way), end the launch, and say it.
    async fn unprepared(
        &self,
        who: &Identity,
        launch: &PendingLaunch,
        unprepared: NotLaunched,
    ) -> Result<Outcome, LaunchError> {
        let (detail, outcome) = match unprepared {
            NotLaunched::Failed(detail) => (detail.clone(), Outcome::NotStarted { detail }),
            NotLaunched::TooLong(refusal) => (refusal.to_string(), Outcome::TooLong(refusal)),
            NotLaunched::BatchArguments(refusal) => {
                (refusal.to_string(), Outcome::BatchArguments(refusal))
            }
            NotLaunched::ProgramNotFound(missing) => {
                (missing.to_string(), Outcome::ProgramNotFound(missing))
            }
        };
        self.not_started(who, true, &detail).await?;
        self.claims.release(&launch.session, launch.incarnation);
        Ok(outcome)
    }

    /// The command did not run: record why, and end the launch.
    async fn never_started(
        &self,
        who: &Identity,
        failure: run_ports::LaunchFailure,
        done: impl Fn(Outcome) -> Launched,
    ) -> Result<Launched, LaunchError> {
        self.not_started(who, failure.deterministic, &failure.detail)
            .await?;
        self.claims.release(&who_session(who), who.incarnation);
        Ok(done(Outcome::NotStarted {
            detail: failure.detail,
        }))
    }

    fn engine(&self, name: &str) -> Option<Arc<dyn Engine>> {
        self.engines.iter().find(|e| e.name() == name).cloned()
    }

    /// Everything a launch does before it records a sign of life: the
    /// engine, then the one launch ([`crate::launch_of`], the one place a
    /// launch is made, every launch path alike). What its steps said is kept
    /// (`ignored`, `left_out`, the workspace it `placed` the engine in),
    /// whether it was made or not; its error is why nothing was started.
    fn prepare(
        &mut self,
        at: LaunchAt,
        order: &Order,
        ignored: &mut Vec<run_ports::Ignored>,
        left_out: &mut Vec<run_ports::LeftOut>,
        placed: &mut Option<Placement>,
    ) -> Result<(Arc<dyn Engine>, Launch), NotLaunched> {
        let engine = self
            .engine(&order.engine)
            .ok_or_else(|| NotLaunched::Failed(format!("no engine named {}", order.engine)))?;
        let request = LaunchRequest {
            at,
            kind: match order.interactive {
                true => LaunchKind::Terminal,
                false => LaunchKind::OneShot,
            },
            setup: crate::home::LaunchSetup {
                home: crate::home::HomeSetup {
                    harp: order.harp.clone(),
                    context: order.context.clone(),
                    interactive: order.interactive,
                    cwd: order.cwd.clone(),
                    permissions: order.permissions.clone(),
                    session_home: order.settings.session_home,
                    git_user: order.git_user.clone(),
                    surfaces: order.surfaces.clone(),
                },
                workspace: order.workspace.clone(),
                env: order.env.clone(),
                owned: order.owned.clone(),
            },
            prompt: order.prompt.clone(),
            resume: order.resume.clone(),
            session_id: None,
            user_home: order.user_home.clone(),
            model: order.model.clone(),
            effort: order.effort.clone(),
            settings: order.settings.clone(),
            child_permissions: run_ports::ChildPermissions::default(),
            // The run's session lock is its caller's (run-application holds
            // it); the launcher takes none.
            session_lock: String::new(),
        };
        let runner = &self.runner;
        let made = crate::launch_of(
            LaunchEngines {
                engine: &*engine,
                child: None,
            },
            &|command| runner.find_program(command),
            &mut self.records,
            self.workspaces
                .as_deref()
                .map(|w| w as &dyn isolation_ports::Workspaces),
            &request,
            &self.os,
        );
        match made {
            Ok(made) => {
                ignored.clone_from(&made.launch.ignored);
                left_out.clone_from(&made.launch.left_out);
                *placed = made.placed;
                Ok((engine, made.launch))
            }
            Err(unprepared) => {
                let Unprepared {
                    why,
                    placed: was,
                    left_out: said,
                    ignored: not_followed,
                } = *unprepared;
                *ignored = not_followed;
                *left_out = said;
                *placed = was;
                Err(why)
            }
        }
    }

    /// Let go of the checkout a launch placed its engine in, once the engine
    /// is over (owner ruling 2026-11-12, Q2): a clean one is removed (its
    /// branch with it when merged), one holding work, or that will not go,
    /// is left, and why; never forced. A launcher that makes no worktrees
    /// leaves it.
    pub fn release(
        &self,
        checkout: &isolation_ports::Checkout,
        files: &[String],
    ) -> isolation_ports::Removal {
        match &self.workspaces {
            Some(workspaces) => {
                workspaces.remove_files(&checkout.path, files);
                workspaces.remove(&checkout.repo_dir, &checkout.path)
            }
            None => isolation_ports::Removal::Left(crate::workspace::NO_WORKSPACES.to_string()),
        }
    }

    async fn record(&self, who: &Identity, fact: RunnerFact) -> Result<(), LaunchError> {
        self.facts
            .record(who, fact)
            .await
            .map(drop)
            .map_err(|e| LaunchError::Fact {
                reason: e.to_string(),
            })
    }

    async fn not_started(
        &self,
        who: &Identity,
        deterministic: bool,
        detail: &str,
    ) -> Result<(), LaunchError> {
        self.facts
            .launch_failed(&who.run, deterministic, detail)
            .await
            .map(drop)
            .map_err(|e| LaunchError::Fact {
                reason: e.to_string(),
            })
    }
}

/// The launcher's word that the runner is spawned: the engine it runs under,
/// where (`cwd`, its placed workspace), and the pid the spawn gave.
fn runner_started(order: &Order, cwd: Option<&str>, pid: u32) -> RunnerFact {
    RunnerFact::RunnerStarted(runs::RecordRunnerStarted {
        runtime: order.engine.clone(),
        workspace: cwd.unwrap_or_default().to_string(),
        pid,
    })
}

/// The one-shot's turn started: its turn 1, queued by no message (D28 S-b
/// keys it by its number).
fn turn_started() -> RunnerFact {
    RunnerFact::TurnStarted(runs::RecordTurnStarted {
        turn: TURN,
        message_ids: Vec::new(),
    })
}

fn attached() -> RunnerFact {
    RunnerFact::RunnerAttached(runs::RecordRunnerAttached {
        runner_id: RUNNER.to_string(),
        protocol: PROTOCOL.to_string(),
    })
}

/// The bind of the engine's own session id. No ctxloom file is named: the
/// session's record is its events (ruling 87a).
fn bound(native_id: &str) -> RunnerFact {
    RunnerFact::EngineSessionBound(sessions::BindEngineSession {
        native_id: native_id.to_string(),
        transcript_path: String::new(),
        engine_version: None,
    })
}

/// The session `who` acts for, as its id's text.
fn who_session(who: &Identity) -> String {
    uuid::Uuid::from_slice(&who.session.value)
        .map(|id| id.hyphenated().to_string())
        .unwrap_or_default()
}

fn failure_class(class: FailureClass) -> runs::TurnFailureClass {
    match class {
        FailureClass::RateLimited => runs::TurnFailureClass::RateLimited,
        FailureClass::CredentialRefused => runs::TurnFailureClass::CredentialRefused,
        FailureClass::EngineError => runs::TurnFailureClass::EngineError,
        FailureClass::Interrupted => runs::TurnFailureClass::Interrupted,
    }
}

/// The sweep of the runs other processes left (rulings 86, 92, 93, 95, 100):
/// what every launcher's step runs over the runs it holds no order for, and
/// the daemon's FIRST step at its start (T18), before it hosts anything. A
/// run still owed a runner whose own lock is free has lost its launcher; a
/// started run whose session's lock is free, or whose own lock (held for its
/// life by a host such as the daemon) is free, has lost its owner. Each is
/// ended, keyed by the run, so a second sweeper's end is a DUPLICATE.
pub struct Sweeper<E: Edge> {
    edge: Arc<E>,
    facts: Facts<E>,
    locks: Arc<dyn run_ports::RunLocks>,
    releases: Option<Arc<dyn Releases>>,
}

/// A run a sweep ended, and how.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Swept {
    pub run: Uuid,
    pub session: String,
    pub incarnation: u32,
    /// It had been started (`RecordRunnerLost`), not only owed a runner
    /// (`RecordLaunchOrphaned`).
    pub started: bool,
    /// The project it ran for (its session's), empty until its mint is seen.
    pub project_dir: String,
    /// Where its runner worked, as its `RecordRunnerStarted` recorded it (its
    /// checkout, or the project); empty when it never started.
    pub workspace: String,
    /// What became of what it held, once it was ended.
    pub released: Released,
}

/// What a sweep let go of for a lost run (OD-19): its checkout, released
/// WIP-safe (a clean one removed, one holding work left, never forced).
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub enum Released {
    /// It held nothing to let go (it worked in the project, or never
    /// started), or nothing was asked to release it.
    #[default]
    Nothing,
    /// Its checkout at the path is removed.
    Removed(String),
    /// Its checkout at `path` is left, and why.
    Left { path: String, why: String },
}

/// What a sweep lets go of for each run it ended (OD-19): the lost run's
/// checkout now, and in M10b its container (`<runtime> stop`/`rm`, hook 3).
pub trait Releases: Send + Sync {
    /// Let go of what the lost run `swept` held.
    fn release(&self, swept: &Swept) -> Released;
}

impl<E: Edge> Sweeper<E> {
    pub fn new(edge: Arc<E>, locks: Arc<dyn run_ports::RunLocks>) -> Sweeper<E> {
        Sweeper {
            facts: Facts::new(Arc::clone(&edge)),
            edge,
            locks,
            releases: None,
        }
    }

    /// The sweeper, letting go through `releases` of what each run it ends
    /// held (OD-19: the daemon's start).
    pub fn releasing(mut self, releases: Arc<dyn Releases>) -> Sweeper<E> {
        self.releases = Some(releases);
        self
    }

    /// Sweep `owed` (runs owed a runner that this process does not launch)
    /// and `started` (runs started and not ended): the ones whose owner is
    /// gone are ended, and answered.
    pub async fn sweep(
        &self,
        owed: &[PendingLaunch],
        started: &[StartedRun],
    ) -> Result<Vec<Swept>, LaunchError> {
        let mut swept = Vec::new();
        for launch in owed.iter().filter(|launch| self.launcher_lost(launch)) {
            let lost = Lost {
                run: &launch.run,
                session: &launch.session,
                incarnation: launch.incarnation,
                harp: "",
                started: false,
            };
            if let Some(workspace) = self.orphaned(&lost).await? {
                swept.push(self.released(Swept {
                    run: launch.run.clone(),
                    session: launch.session.clone(),
                    incarnation: launch.incarnation,
                    started: false,
                    project_dir: String::new(),
                    workspace,
                    released: Released::Nothing,
                }));
            }
        }
        for run in started.iter().filter(|run| self.owner_lost(run)) {
            let lost = Lost {
                run: &run.run,
                session: &run.session,
                incarnation: run.incarnation,
                harp: &run.harp,
                started: true,
            };
            if let Some(workspace) = self.orphaned(&lost).await? {
                swept.push(self.released(Swept {
                    run: run.run.clone(),
                    session: run.session.clone(),
                    incarnation: run.incarnation,
                    started: true,
                    project_dir: run.project_dir.clone(),
                    workspace,
                    released: Released::Nothing,
                }));
            }
        }
        Ok(swept)
    }

    /// `swept`, with what its release came to.
    fn released(&self, mut swept: Swept) -> Swept {
        if let Some(releases) = &self.releases {
            swept.released = releases.release(&swept);
        }
        swept
    }

    /// Whether the launcher of `launch` (another process's: this one holds no
    /// order for it) has died: its run lock, journaled, is free.
    fn launcher_lost(&self, launch: &PendingLaunch) -> bool {
        !launch.launcher_lock.is_empty()
            && self.locks.probe(&launch.launcher_lock) == run_ports::LockProbe::Free
    }

    /// Whether the owner of the started `run` has died: its session's lock is
    /// free (ruling 93), or its own lock is (a host that held it for the
    /// run's life is gone; a primary lets its own go, file and all, once its
    /// runner started, which proves nothing).
    fn owner_lost(&self, run: &StartedRun) -> bool {
        let session = !run.harp.is_empty()
            && self.locks.session(&run.project_dir, &run.harp) == run_ports::LockProbe::Free;
        let host = !run.launcher_lock.is_empty()
            && self.locks.probe(&run.launcher_lock) == run_ports::LockProbe::Free;
        session || host
    }

    /// End the run `lost` names, whose owner was found gone, unless its
    /// stream, read again after the probe, shows its owner got there first:
    /// its start (for a run owed a runner, unless `started`) or its end. A
    /// started run's turns still open (the one in flight, and any queued
    /// behind it) are ended failed first, `runner-lost` (S18): the run's end
    /// would leave them open for good, and its owner is told of each. Where
    /// its runner worked (its `RecordRunnerStarted`'s workspace) when it was ended
    /// here; none when it was not.
    async fn orphaned(&self, lost: &Lost<'_>) -> Result<Option<String>, LaunchError> {
        let cover = angzarr_embedded::v1::Cover {
            domain: runs::RUN_DOMAIN.to_string(),
            root: Some(lost.run.clone()),
            edition: None,
        };
        let pages = self
            .edge
            .events(&cover, 0)
            .await
            .map_err(|e| LaunchError::Fact {
                reason: e.to_string(),
            })?;
        let since = pages.iter().any(|page| match &page.body {
            Some(angzarr_embedded::v1::event_page::Body::Event(any)) => {
                any.type_url.ends_with(".RunEnded")
                    || (!lost.started && any.type_url.ends_with(".RunnerStartedRecorded"))
            }
            _ => false,
        });
        if since {
            return Ok(None);
        }
        let state = folded(&pages);
        let failed = |e: runs_application::AppError| LaunchError::Fact {
            reason: e.to_string(),
        };
        if lost.started {
            self.turns_lost(lost, &state).await.map_err(failed)?;
        }
        let ended = match lost.started {
            true => self.facts.runner_lost(lost.run).await,
            false => self.facts.launch_orphaned(lost.run).await,
        };
        ended.map_err(failed)?;
        self.facts
            .incarnation_ended(lost.session, lost.incarnation, ORPHANED)
            .await
            .map_err(failed)?;
        Ok(Some(
            state
                .runner_started
                .map(|started| started.workspace)
                .unwrap_or_default(),
        ))
    }

    /// Each turn `state` holds open, ended failed for runner loss, keyed as
    /// its runner would have failed it (`turn:<run>:<turn>:failed`).
    async fn turns_lost(
        &self,
        lost: &Lost<'_>,
        state: &runs::RunState,
    ) -> Result<(), runs_application::AppError> {
        let session = uuid::Uuid::parse_str(lost.session).unwrap_or_default();
        let who = Identity {
            run: lost.run.clone(),
            session: Uuid::from_bytes(*session.as_bytes()),
            incarnation: lost.incarnation,
            harp: lost.harp.to_string(),
            depth: 0,
        };
        for open in &state.turns {
            self.facts
                .record(
                    &who,
                    RunnerFact::TurnFailed(runs::RecordTurnFailed {
                        turn: open.turn,
                        class: runs::TurnFailureClass::RunnerLost,
                        detail: LOST_TURN.to_string(),
                        message_ids: open.message_ids.clone(),
                    }),
                )
                .await?;
        }
        Ok(())
    }
}

/// What a turn its run's host lost is failed with.
const LOST_TURN: &str = "its host was lost while the turn was in flight";

/// A run a sweep found its owner gone of.
struct Lost<'a> {
    run: &'a Uuid,
    session: &'a str,
    incarnation: u32,
    harp: &'a str,
    started: bool,
}

/// The run's state, folded from its `pages` as its aggregate folds them.
fn folded(pages: &[angzarr_embedded::v1::EventPage]) -> runs::RunState {
    use angzarr_embedded::Payload;
    let mut state = runs::RunState::default();
    for page in pages {
        if let Some(angzarr_embedded::v1::event_page::Body::Event(any)) = &page.body {
            if let Ok(event) = runs::RunEvent::from_any(any) {
                runs::apply(&mut state, &event, page.sequence);
            }
        }
    }
    state
}

#[cfg(test)]
#[path = "launcher.test.rs"]
mod tests;
