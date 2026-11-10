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
//! The transcript is the PROJECTOR's to write (D27; [`crate::Recorder`], the
//! one writer), from the capture facts on the session's stream. A child's
//! turn's facts are its hooks' alone (owner rulings 26 and 51): the launcher
//! records none of its prompt or its words, so a turn whose hooks never reach
//! the listener has no conversation in its transcript, though its reply is
//! still the launch's answer. The terminal session's opening prompt is its
//! hooks' to record too, like every other of its prompts. The events carry the
//! transcript's path (`answer_ref`) and never its bytes.

use std::sync::Arc;

use angzarr_embedded::v1::Uuid;
use angzarr_embedded::{Catalog, CatchUp, Processed, ReadStore, Store};
use pending_launches::{catch_up, PendingLaunch, PendingModel};
use run_ports::{
    Edge, Engine, FailureClass, Limits, PrepareRequest, RunnerLauncher, SessionRecords,
    TurnCommand, TurnOutcome, TurnRequest,
};
use runs_application::{Facts, Identity, Observation, Principal, RunnerFact};

use crate::error::LaunchError;
use crate::orders::{Order, Orders};
use crate::transcript::TRANSCRIPT;

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
}

/// A run launched, and its turn's outcome.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Launched {
    pub run: Uuid,
    pub session: String,
    pub harp: String,
    pub engine: String,
    pub outcome: Outcome,
}

/// The launcher: the engines it can drive, the way a command is run, the
/// session directory it writes, and the pending launches it catches up on.
pub struct Launcher<E: Edge, L: RunnerLauncher, R: SessionRecords> {
    facts: Facts<E>,
    runner: L,
    records: R,
    engines: Vec<Arc<dyn Engine>>,
    orders: Arc<Orders>,
    limits: Limits,
    pending: CatchUp<PendingModel>,
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
            facts: Facts::new(edge),
            runner,
            records,
            engines,
            orders,
            limits,
            pending: catch_up(),
        }
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
        let owed = self.pending.projector().pending().launches();
        let mut launched = Vec::new();
        for launch in owed {
            if let Some(order) = self.orders.take(&launch.session, launch.incarnation) {
                launched.push(self.launch(launch, order).await?);
            }
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
            principal: Principal::Runner,
        };
        let done = |outcome| Launched {
            run: launch.run.clone(),
            session: launch.session.clone(),
            harp: order.harp.clone(),
            engine: order.engine.clone(),
            outcome,
        };
        let (engine, command) = match self.prepare(&order) {
            Ok(prepared) => prepared,
            Err(detail) => {
                self.not_started(&who, true, &detail).await?;
                return Ok(done(Outcome::NotStarted { detail }));
            }
        };
        self.record(&who, attached()).await?;
        if order.interactive {
            // The terminal session's opening prompt, like every prompt of it,
            // is its hooks' to record (the engine submits it and its prompt
            // hook fires): the launcher records the run's own facts only.
            return self.terminal(&who, &order, &command, done).await;
        }
        // A child's turn is heard through its hooks alone (owner rulings 26
        // and 51): its prompt and its words are theirs to record, and the
        // stream is read below for the reply and the turn's outcome only.
        let running = match self.runner.launch(&command, &self.limits) {
            Ok(running) => running,
            Err(failure) => return self.never_started(&who, failure, done).await,
        };
        self.record(&who, runner_started(&order, running.pid()))
            .await?;
        self.record(
            &who,
            RunnerFact::TurnStarted(runs::TurnStarted { turn: TURN }),
        )
        .await?;
        let output = match running.finish() {
            Ok(output) => output,
            Err(failure) => return self.never_started(&who, failure, done).await,
        };
        let outcome = engine.finish_turn(&output);
        let code = output.exit.code.unwrap_or(0);
        let outcome = match outcome {
            TurnOutcome::Answered { answer, native_id } => {
                if let Some(native) = &native_id {
                    self.record(&who, bound(native, self.records.dir(&order.harp)))
                        .await?;
                }
                self.record(
                    &who,
                    RunnerFact::TurnCompleted(runs::TurnCompleted {
                        turn: TURN,
                        native_key: native_id.clone().unwrap_or_default(),
                        answer_ref: TRANSCRIPT.to_string(),
                        exit_code: code,
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
                    &who,
                    RunnerFact::TurnFailed(runs::TurnFailed {
                        turn: TURN,
                        class: failure_class(class),
                        detail: detail.clone(),
                    }),
                )
                .await?;
                Outcome::Failed { class, detail }
            }
        };
        self.record(
            &who,
            RunnerFact::RunExited(runs::RunExited {
                code,
                signal: output.exit.signal.unwrap_or(0),
            }),
        )
        .await?;
        Ok(done(outcome))
    }

    /// The human's session: the command is given her terminal and runs until
    /// she ends it, taking no turns of the run's; its exit ends the run.
    async fn terminal(
        &self,
        who: &Identity,
        order: &Order,
        command: &TurnCommand,
        done: impl Fn(Outcome) -> Launched,
    ) -> Result<Launched, LaunchError> {
        let running = match self.runner.launch_terminal(command) {
            Ok(running) => running,
            Err(failure) => return self.never_started(who, failure, done).await,
        };
        self.record(who, runner_started(order, running.pid()))
            .await?;
        let output = match running.finish() {
            Ok(output) => output,
            Err(failure) => return self.never_started(who, failure, done).await,
        };
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

    /// The command did not run: record why, and end the launch.
    async fn never_started(
        &self,
        who: &Identity,
        failure: run_ports::LaunchFailure,
        done: impl Fn(Outcome) -> Launched,
    ) -> Result<Launched, LaunchError> {
        self.not_started(who, failure.deterministic, &failure.detail)
            .await?;
        Ok(done(Outcome::NotStarted {
            detail: failure.detail,
        }))
    }

    fn engine(&self, name: &str) -> Option<Arc<dyn Engine>> {
        self.engines.iter().find(|e| e.name() == name).cloned()
    }

    /// Everything a launch does before it records a sign of life: the engine,
    /// the session home, the user's line, the command. Its error is why
    /// nothing was started.
    fn prepare(&mut self, order: &Order) -> Result<(Arc<dyn Engine>, TurnCommand), String> {
        let engine = self
            .engine(&order.engine)
            .ok_or_else(|| format!("no engine named {}", order.engine))?;
        let home = format!("{}/home", self.records.dir(&order.harp));
        let prepared = engine
            .prepare(&PrepareRequest {
                home: home.clone(),
                context: order.context.clone(),
                // Nothing of the user's own engine configuration is offered.
                offered: Vec::new(),
                interactive: order.interactive,
            })
            .map_err(|e| e.to_string())?;
        for file in &prepared.files {
            self.records
                .write(&order.harp, &format!("home/{}", file.rel), &file.bytes)
                .map_err(|e| e.to_string())?;
        }
        let request = TurnRequest {
            prompt: order.prompt.clone(),
            home,
            context_path: prepared.context_path,
            resume: None,
            env: order.env.clone(),
            cwd: order.cwd.clone(),
            model: order.model.clone(),
            effort: order.effort.clone(),
            settings: order.settings.clone(),
        };
        let command = if order.interactive {
            engine.drive_interactive(&request)
        } else {
            engine.drive_turn(&request)
        }
        .map_err(|e| e.to_string())?;
        Ok((engine, command))
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
/// where, and the pid the spawn gave.
fn runner_started(order: &Order, pid: u32) -> RunnerFact {
    RunnerFact::RunnerStarted(runs::RunnerStarted {
        runtime: order.engine.clone(),
        workspace: order.cwd.clone().unwrap_or_default(),
        pid,
    })
}

fn attached() -> RunnerFact {
    RunnerFact::RunnerAttached(runs::RunnerAttached {
        runner_id: RUNNER.to_string(),
        protocol: PROTOCOL.to_string(),
    })
}

/// The bind of the engine's own session id, with the session directory as
/// the place its record is.
fn bound(native_id: &str, transcript_dir: String) -> RunnerFact {
    RunnerFact::EngineSessionBound(sessions::BindEngineSession {
        native_id: native_id.to_string(),
        transcript_path: format!("{transcript_dir}/{TRANSCRIPT}"),
        engine_version: None,
    })
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

#[cfg(test)]
#[path = "launcher.test.rs"]
mod tests;
