//! The runner's launch: a [`CatchUp`] over the pending launches hands each run
//! still owed a runner to the engine for ONE turn, through the
//! [`RunnerLauncher`], and records what came of it as the run's facts.
//!
//! The order of a launch, and why:
//! 1. the session home and the user's line of the transcript are written, and
//!    the turn's command is built; a launch that fails here has started
//!    nothing, and is a `LaunchFailed`;
//! 2. `RunnerAttached` then `TurnStarted` are recorded BEFORE the command
//!    runs: the pending launches clear on the first sign of life (the runs
//!    protos have no `RunnerStarted` yet, an open decision), so a second
//!    catch-up in the same process does not launch the run again;
//! 3. the command runs to its end; a command that could not be started is a
//!    `LaunchFailed`; one that ran is read by the engine
//!    ([`Engine::finish_turn`]): the assistant's line is written to the
//!    transcript, the engine's own session id is bound to the session, and the
//!    turn is `TurnCompleted` or `TurnFailed`;
//! 4. `RunExited` ends the run.
//!
//! The transcript is the runner's to write (D27): the events carry its path
//! (`answer_ref`) and never its bytes.

use std::sync::Arc;

use angzarr_embedded::v1::Uuid;
use angzarr_embedded::{Catalog, CatchUp, Processed, ReadStore, Store};
use pending_launches::{catch_up, PendingLaunch, PendingModel};
use ports::Clock;
use run_ports::{
    Edge, Engine, FailureClass, Launch, Limits, PrepareRequest, RunnerLauncher, SessionRecords,
    TurnCommand, TurnOutcome, TurnRequest,
};
use runs_application::{Facts, Identity, Principal, RunnerFact};

use crate::error::LaunchError;
use crate::orders::{Order, Orders};
use crate::transcript::{entry_line, next_seq, Speaker, TRANSCRIPT};

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
pub struct Launcher<E: Edge, L: RunnerLauncher, R: SessionRecords, C: Clock> {
    facts: Facts<E>,
    runner: L,
    records: R,
    clock: C,
    engines: Vec<Arc<dyn Engine>>,
    orders: Arc<Orders>,
    limits: Limits,
    pending: CatchUp<PendingModel>,
}

impl<E: Edge, L: RunnerLauncher, R: SessionRecords, C: Clock> Launcher<E, L, R, C> {
    pub fn new(
        edge: Arc<E>,
        runner: L,
        records: R,
        clock: C,
        engines: Vec<Arc<dyn Engine>>,
        orders: Arc<Orders>,
        limits: Limits,
    ) -> Launcher<E, L, R, C> {
        Launcher {
            facts: Facts::new(edge),
            runner,
            records,
            clock,
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
        self.record(
            &who,
            RunnerFact::TurnStarted(runs::TurnStarted { turn: TURN }),
        )
        .await?;
        let output = match self.runner.launch(&command, &self.limits) {
            Launch::NotStarted(failure) => {
                self.not_started(&who, failure.deterministic, &failure.detail)
                    .await?;
                return Ok(done(Outcome::NotStarted {
                    detail: failure.detail,
                }));
            }
            Launch::Over(output) => output,
        };
        let outcome = engine.finish_turn(&output);
        let code = output.exit.code.unwrap_or(0);
        let outcome = match outcome {
            TurnOutcome::Answered { answer, native_id } => {
                self.speak(&order, Speaker::Assistant, &answer)?;
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
            })
            .map_err(|e| e.to_string())?;
        for file in &prepared.files {
            self.records
                .write(&order.harp, &format!("home/{}", file.rel), &file.bytes)
                .map_err(|e| e.to_string())?;
        }
        self.speak(order, Speaker::User, &order.prompt)
            .map_err(|e| e.to_string())?;
        let command = engine
            .drive_turn(&TurnRequest {
                prompt: order.prompt.clone(),
                home,
                context_path: prepared.context_path,
                resume: None,
                env: order.env.clone(),
                cwd: order.cwd.clone(),
            })
            .map_err(|e| e.to_string())?;
        Ok((engine, command))
    }

    /// Add `content` to the session's transcript.
    fn speak(&mut self, order: &Order, speaker: Speaker, content: &str) -> Result<(), LaunchError> {
        let record = |e: run_ports::RecordsError| LaunchError::Record {
            ext_err_txt: e.to_string(),
        };
        let seq = self
            .records
            .read(&order.harp, TRANSCRIPT)
            .map_err(record)?
            .map_or(0, |held| next_seq(&held));
        let line = entry_line(
            &order.harp,
            &order.engine,
            seq,
            self.clock.now().seconds,
            speaker,
            content,
        );
        self.records
            .append(&order.harp, TRANSCRIPT, line.as_bytes())
            .map_err(record)
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
