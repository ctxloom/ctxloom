//! The runner's launch: a [`CatchUp`] over the pending launches hands each run
//! still owed a runner to the engine for ONE turn, through the
//! [`RunnerLauncher`], and records what came of it as the run's facts.
//!
//! The order of a launch, and why:
//! 1. the session home is written and the turn's command is built; a launch
//!    that fails here has started nothing, and is a `LaunchFailed`;
//! 2. `RunnerAttached` is recorded, then the command is SPAWNED
//!    ([`RunnerLauncher::launch`]), and only then are `RunnerStarted` (with the
//!    pid the spawn gave) and `TurnStarted` recorded, while the command runs:
//!    the pending launches clear on `RunnerStarted` (not on a runner's first
//!    word, which a spawned and silent turn never says), so a catch-up that
//!    reads the streams while the command runs does not launch the run
//!    again. A command that cannot be spawned is a `LaunchFailed`, which ends
//!    the run, and never a `RunnerStarted`;
//! 3. the command runs to its end ([`Running::finish`]; a wait that breaks
//!    after the spawn is a `LaunchFailed` too); one that ran is read by the engine
//!    ([`Engine::finish_turn`]): the reply is the launch's answer, the
//!    engine's own session id is bound to the session, and the turn is
//!    `TurnCompleted` or `TurnFailed`;
//! 4. `RunExited` ends the run.
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
//! [`Orders`], is let go once `RunnerStarted` is recorded (or the launch
//! failed); from then on the session's lock is the run's evidence (ruling
//! 93). A step also sweeps the runs other processes left: one still owed a
//! runner whose own lock is free, and one started whose session's lock is
//! free, have lost their owner. The sweep probes the lock, THEN re-reads the
//! run's stream (an owner records its start or end before it lets its lock
//! go), and records the run's end, keyed by the run so a second sweeper's is
//! a DUPLICATE: `LaunchOrphaned` for one owed a runner (no launch failed,
//! ruling 100), `RunnerLost` for one started (runner loss, ruling 95); then
//! the end of the session's incarnation (`orphaned`).

use std::sync::Arc;

use angzarr_embedded::v1::Uuid;
use angzarr_embedded::{Catalog, CatchUp, Processed, ReadStore, Store};
use pending_launches::{catch_up, PendingLaunch, PendingModel, StartedRun};
use run_ports::{
    Edge, Engine, FailureClass, Limits, RunnerLauncher, SessionRecords, TurnCommand, TurnOutcome,
    TurnRequest,
};
use runs_application::{Facts, Identity, Observation, RunnerFact};

use crate::error::LaunchError;
use crate::orders::{Order, Orders};
use crate::workspace::{place_workspace, Axis, Placement};

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

/// Why a launch's preparation started nothing.
enum Unprepared {
    /// Its words: no engine, a home that could not be written, a command the
    /// engine would not build.
    Failed(String),
    /// The command is too long for the platform (`run_ports::check_command_line`).
    TooLong(run_ports::CommandTooLong),
    /// A batch file's argument std will not pass (`run_ports::check_batch_arguments`).
    BatchArguments(run_ports::BatchArguments),
    /// The program is on no directory of the command's PATH.
    ProgramNotFound(run_ports::ProgramNotFound),
}

impl From<String> for Unprepared {
    fn from(detail: String) -> Unprepared {
        Unprepared::Failed(detail)
    }
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
    orders: Arc<Orders>,
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
}

impl<E: Edge, L: RunnerLauncher, R: SessionRecords> Launcher<E, L, R> {
    pub fn new(
        edge: Arc<E>,
        runner: L,
        records: R,
        engines: Vec<Arc<dyn Engine>>,
        orders: Arc<Orders>,
        limits: Limits,
    ) -> Launcher<E, L, R> {
        Launcher {
            facts: Facts::new(Arc::clone(&edge)),
            edge,
            runner,
            records,
            engines,
            orders,
            limits,
            os: String::new(),
            pending: catch_up(),
            locks: None,
            workspaces: None,
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

    /// The launcher, probing `locks` for the owners of other processes' runs:
    /// a run still owed a runner whose own lock is free has lost its launcher
    /// (rulings 86, 92), and a started run whose session's lock is free has
    /// lost its owner (ruling 93). Whichever process catches up on it first
    /// records it ended: `LaunchOrphaned` or `RunnerLost` (rulings 95, 100).
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
        let (owed, started) = {
            let pending = self.pending.projector().pending();
            (pending.launches(), pending.started())
        };
        let mut launched = Vec::new();
        let mut others = Vec::new();
        for launch in owed {
            match self.orders.take(&launch.session, launch.incarnation) {
                Some(order) => launched.push(self.launch(launch, order).await?),
                None => others.push(launch),
            }
        }
        if let Some(locks) = &self.locks {
            Sweeper::new(Arc::clone(&self.edge), Arc::clone(locks))
                .sweep(&others, &started)
                .await?;
        }
        Ok(launched)
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
        let prepared = self.prepare(&order, &mut ignored, &mut left_out, &mut placed);
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
        let (engine, command) = match prepared {
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
                .terminal(&who, &order, cwd.as_deref(), &command, done)
                .await;
        }
        // A child's turn is heard through its hooks alone (owner rulings 26
        // and 51): its prompt and its words are theirs to record, and the
        // stream is read below for the reply and the turn's outcome only.
        let running = match self.runner.launch(&command, &self.limits) {
            Ok(running) => running,
            Err(failure) => return self.never_started(&who, failure, done).await,
        };
        self.record(&who, runner_started(&order, cwd.as_deref(), running.pid()))
            .await?;
        // Started: the run's own lock has done its work (ruling 92).
        self.orders.release(&launch.session, launch.incarnation);
        self.record(&who, turn_started()).await?;
        let output = match running.finish() {
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
                    RunnerFact::TurnCompleted(runs::TurnCompleted {
                        turn: TURN,
                        native_key: native_id.clone().unwrap_or_default(),
                        // No file holds the answer (ruling 87a).
                        answer_ref: String::new(),
                        exit_code: code,
                        message_ids: Vec::new(),
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
                    RunnerFact::TurnFailed(runs::TurnFailed {
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
            RunnerFact::RunExited(runs::RunExited {
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
        command: &TurnCommand,
        done: impl Fn(Outcome) -> Launched,
    ) -> Result<Launched, LaunchError> {
        let running = match self.runner.launch_terminal(command) {
            Ok(running) => running,
            Err(failure) => return self.never_started(who, failure, done).await,
        };
        self.record(who, runner_started(order, cwd, running.pid()))
            .await?;
        self.orders.release(&who_session(who), who.incarnation);
        let output = match running.finish() {
            Ok(output) => output,
            Err(failure) => return self.never_started(who, failure, done).await,
        };
        // The engine's own session its hooks named at the session's end is
        // bound, so `run --session` can resume it (owner ruling 115).
        if let Some(native) = self.native_session_ended(who).await? {
            self.record(who, bound(&native)).await?;
        }
        // An engine with no hook for its session's end (opencode, ruling 58):
        // its exit is the end, and the launcher says so.
        if self
            .engine(&order.engine)
            .is_some_and(|engine| engine.session_ends_with_exit())
        {
            self.observe(who, session_ended_at_exit()).await?;
        }
        self.record(
            who,
            RunnerFact::RunExited(runs::RunExited {
                code: output.exit.code.unwrap_or(0),
                signal: output.exit.signal.unwrap_or(0),
            }),
        )
        .await?;
        Ok(done(Outcome::Ended {
            code: output.exit.code,
        }))
    }

    /// The engine's own session the session's hooks last named at its end
    /// (`SessionEnded.native_session`), if any.
    async fn native_session_ended(&self, who: &Identity) -> Result<Option<String>, LaunchError> {
        let cover = angzarr_embedded::v1::Cover {
            domain: sessions::SESSION_DOMAIN.to_string(),
            root: Some(who.session.clone()),
            edition: None,
        };
        let pages = self
            .edge
            .events(&cover, 0)
            .await
            .map_err(|e| LaunchError::Fact {
                reason: e.to_string(),
            })?;
        let mut native = None;
        for page in &pages {
            if let Some(angzarr_embedded::v1::event_page::Body::Event(any)) = &page.body {
                if let Ok(sessions::SessionEvent::SessionEnded(ended)) =
                    <sessions::SessionEvent as angzarr_embedded::Payload>::from_any(any)
                {
                    if !ended.native_session.is_empty() {
                        native = Some(ended.native_session);
                    }
                }
            }
        }
        Ok(native)
    }

    /// The launch's preparation started nothing: record why (deterministic:
    /// the same order prepares the same way), end the launch, and say it.
    async fn unprepared(
        &self,
        who: &Identity,
        launch: &PendingLaunch,
        unprepared: Unprepared,
    ) -> Result<Outcome, LaunchError> {
        let (detail, outcome) = match unprepared {
            Unprepared::Failed(detail) => (detail.clone(), Outcome::NotStarted { detail }),
            Unprepared::TooLong(refusal) => (refusal.to_string(), Outcome::TooLong(refusal)),
            Unprepared::BatchArguments(refusal) => {
                (refusal.to_string(), Outcome::BatchArguments(refusal))
            }
            Unprepared::ProgramNotFound(missing) => {
                (missing.to_string(), Outcome::ProgramNotFound(missing))
            }
        };
        self.not_started(who, true, &detail).await?;
        self.orders.release(&launch.session, launch.incarnation);
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
        self.orders.release(&who_session(who), who.incarnation);
        Ok(done(Outcome::NotStarted {
            detail: failure.detail,
        }))
    }

    fn engine(&self, name: &str) -> Option<Arc<dyn Engine>> {
        self.engines.iter().find(|e| e.name() == name).cloned()
    }

    /// Everything a launch does before it records a sign of life, for every
    /// launch path alike (the one place a launch is set up): the engine, the
    /// workspace it works in (`placed`), the session home, the user's line,
    /// the command. Its error is why nothing was started.
    fn prepare(
        &mut self,
        order: &Order,
        ignored: &mut Vec<run_ports::Ignored>,
        left_out: &mut Vec<run_ports::LeftOut>,
        placed: &mut Option<Placement>,
    ) -> Result<(Arc<dyn Engine>, TurnCommand), Unprepared> {
        let engine = self
            .engine(&order.engine)
            .ok_or_else(|| format!("no engine named {}", order.engine))?;
        *placed = self.place(
            order,
            &engine.workspace_patterns(order.settings.session_home),
        )?;
        let cwd = placed
            .as_ref()
            .map(|p| p.cwd.clone())
            .or_else(|| order.cwd.clone());
        // The one session-setup path (owner, 2026-11-12): the daemon's
        // hosted children are set up by the same function.
        let set_up = crate::home::set_up_home(
            &*engine,
            &mut self.records,
            &crate::home::HomeSetup {
                harp: order.harp.clone(),
                context: order.context.clone(),
                interactive: order.interactive,
                cwd: cwd.clone(),
                permissions: order.permissions.clone(),
                session_home: order.settings.session_home,
                git_user: order.git_user.clone(),
                surfaces: order.surfaces.clone(),
            },
        )?;
        left_out.clone_from(&set_up.left_out);
        self.place_files(placed.as_mut(), &set_up.workspace_files)?;
        let home = set_up.home;
        // The live project, named to the engine whatever its workspace, so its
        // hook processes and the agent's own `ctxloom` calls resolve it from
        // a checkout (whose `.ctxloom` is the project's tracked config
        // alone); one the caller had is replaced, and no permit replaces it.
        let mut env = order.env.clone();
        let mut owned = order.owned.clone();
        env.remove(run_ports::ROOT_VARIABLE);
        if let Some(project) = &order.cwd {
            env.insert(run_ports::ROOT_VARIABLE.to_string(), project.clone());
            owned.push(run_ports::ROOT_VARIABLE.to_string());
        }
        let request = TurnRequest {
            prompt: order.prompt.clone(),
            home,
            context_path: set_up.context_path,
            resume: order.resume.clone(),
            env,
            user_home: order.user_home.clone(),
            cwd,
            model: order.model.clone(),
            effort: order.effort.clone(),
            settings: order.settings.clone(),
        };
        let mut command = if order.interactive {
            engine.drive_interactive(&request)
        } else {
            engine.drive_turn(&request)
        }
        .map_err(|e| e.to_string())?;
        // The caller's CTXLOOM_PERMIT_<NAME> reaches every engine's command as
        // <NAME>, over what the engine chose (owner ruling 78): the one place
        // every command, a child's turn and the terminal's, passes.
        // Never over a name ctxloom set for the engine (ruling 78b): the
        // adapter's computed names, derived from the command, and the run's.
        *ignored = run_ports::apply_permits(&order.env, &mut command.env, &owned);
        // The program is found on the command's own PATH, by the names the
        // host looks it up by (PATHEXT's on Windows, which std's spawn does
        // not try), and the file found is what is spawned: the one place a
        // child's turn and the terminal's are both found. Found nowhere, it
        // is not started, naming what was searched.
        command.program = self
            .runner
            .find_program(&command)
            .map_err(Unprepared::ProgramNotFound)?;
        // A batch file (an npm `claude.cmd`) is run through cmd.exe, which
        // std will not hand an argument of more than one line: refused in
        // words before the spawn.
        run_ports::check_batch_arguments(&self.os, &command).map_err(Unprepared::BatchArguments)?;
        // The command is complete: measured against the platform's command
        // line before it is spawned (the terminal's opening prompt is on it).
        run_ports::check_command_line(&self.os, &command).map_err(Unprepared::TooLong)?;
        Ok((engine, command))
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

    /// Write what the launch carries that its engine reads only from where
    /// it works into the checkout it was placed in (owner ruling 2026-11-12,
    /// Q3), and keep their paths for the release; in the live project none
    /// is written.
    fn place_files(
        &self,
        placed: Option<&mut Placement>,
        files: &[run_ports::HomeFile],
    ) -> Result<(), Unprepared> {
        let (Some(placed), Some(workspaces)) = (placed, &self.workspaces) else {
            return Ok(());
        };
        let Some(checkout) = &placed.checkout else {
            return Ok(());
        };
        if files.is_empty() {
            return Ok(());
        }
        let pairs: Vec<(String, Vec<u8>)> = files
            .iter()
            .map(|f| (f.rel.clone(), f.bytes.clone()))
            .collect();
        workspaces
            .place_files(&checkout.path, &pairs)
            .map_err(|e| Unprepared::Failed(e.to_string()))?;
        placed.files = files.iter().map(|f| f.rel.clone()).collect();
        Ok(())
    }

    /// Where the order's engine works (the workspace axis): none for an
    /// order with no project; the project where it asks for no worktree, or
    /// this launcher makes none (with a warning); else what
    /// [`place_workspace`] says. A worktree refused is why nothing started.
    fn place(&self, order: &Order, patterns: &[String]) -> Result<Option<Placement>, Unprepared> {
        let Some(project) = &order.cwd else {
            return Ok(None);
        };
        let asked = &order.workspace;
        let Some(workspaces) = &self.workspaces else {
            return Ok(Some(Placement {
                cwd: project.clone(),
                checkout: None,
                warning: (asked.axis == Axis::Worktree)
                    .then(|| crate::workspace::NO_WORKSPACES.to_string()),
                files: Vec::new(),
            }));
        };
        place_workspace(
            asked.axis,
            project,
            &asked.work_dir,
            &asked.agent,
            patterns,
            workspaces.as_ref(),
        )
        .map(Some)
        .map_err(|e| Unprepared::Failed(e.to_string()))
    }

    /// Record `observation` on the session's stream.
    async fn observe(&self, who: &Identity, observation: Observation) -> Result<(), LaunchError> {
        self.facts
            .observe(who, &observation)
            .await
            .map(drop)
            .map_err(|e| LaunchError::Fact {
                reason: e.to_string(),
            })
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
    RunnerFact::RunnerStarted(runs::RunnerStarted {
        runtime: order.engine.clone(),
        workspace: cwd.unwrap_or_default().to_string(),
        pid,
    })
}

/// The one-shot's turn started: its turn 1, queued by no message (D28 S-b
/// keys it by its number).
fn turn_started() -> RunnerFact {
    RunnerFact::TurnStarted(runs::TurnStarted {
        turn: TURN,
        message_ids: Vec::new(),
    })
}

fn attached() -> RunnerFact {
    RunnerFact::RunnerAttached(runs::RunnerAttached {
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

/// The end of a terminal session whose engine has no hook for it: at its
/// exit, of no native session the launcher knows.
fn session_ended_at_exit() -> Observation {
    Observation::SessionEnded(sessions::RecordSessionEnded {
        native_session: String::new(),
        reason: "exit".to_string(),
    })
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
}

/// A run a sweep ended, and how.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Swept {
    pub run: Uuid,
    pub session: String,
    pub incarnation: u32,
    /// It had been started (`RunnerLost`), not only owed a runner
    /// (`LaunchOrphaned`).
    pub started: bool,
}

impl<E: Edge> Sweeper<E> {
    pub fn new(edge: Arc<E>, locks: Arc<dyn run_ports::RunLocks>) -> Sweeper<E> {
        Sweeper {
            facts: Facts::new(Arc::clone(&edge)),
            edge,
            locks,
        }
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
            if self
                .orphaned(&launch.run, &launch.session, launch.incarnation, false)
                .await?
            {
                swept.push(Swept {
                    run: launch.run.clone(),
                    session: launch.session.clone(),
                    incarnation: launch.incarnation,
                    started: false,
                });
            }
        }
        for run in started.iter().filter(|run| self.owner_lost(run)) {
            if self
                .orphaned(&run.run, &run.session, run.incarnation, true)
                .await?
            {
                swept.push(Swept {
                    run: run.run.clone(),
                    session: run.session.clone(),
                    incarnation: run.incarnation,
                    started: true,
                });
            }
        }
        Ok(swept)
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

    /// End the run `run` of `session`'s `incarnation`, whose owner was found
    /// gone, unless its stream, read again after the probe, shows its owner
    /// got there first: its start (for a run owed a runner, unless `started`)
    /// or its end. Whether it was ended here.
    async fn orphaned(
        &self,
        run: &Uuid,
        session: &str,
        incarnation: u32,
        started: bool,
    ) -> Result<bool, LaunchError> {
        let cover = angzarr_embedded::v1::Cover {
            domain: runs::RUN_DOMAIN.to_string(),
            root: Some(run.clone()),
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
                    || (!started && any.type_url.ends_with(".RunnerStartedRecorded"))
            }
            _ => false,
        });
        if since {
            return Ok(false);
        }
        let failed = |e: runs_application::AppError| LaunchError::Fact {
            reason: e.to_string(),
        };
        let ended = match started {
            true => self.facts.runner_lost(run).await,
            false => self.facts.launch_orphaned(run).await,
        };
        ended.map_err(failed)?;
        self.facts
            .incarnation_ended(session, incarnation, ORPHANED)
            .await
            .map(|_| true)
            .map_err(failed)
    }
}

#[cfg(test)]
#[path = "launcher.test.rs"]
mod tests;
