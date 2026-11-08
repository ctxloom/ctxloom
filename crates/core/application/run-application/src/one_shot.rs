//! The one-shot run: a prompt, one turn, the answer.
//!
//! In one process, in order: the library plans the run (the agent or profile,
//! the assembled context, the engine, the startup findings); the project is
//! told; a name is claimed and the session minted; the run is enqueued; the
//! launcher, caught up on the store, finds it owed a runner and drives the
//! engine for one turn; the recorder writes the session's directory from the
//! events; the run is checked to have ended, and the answer is returned. A
//! plan that is refused, or a session that cannot be made, has started no
//! engine.

use std::collections::BTreeMap;
use std::sync::Arc;

use angzarr_embedded::v1::{Cover, Uuid};
use angzarr_embedded::{Catalog, CommandBookExt, Processed, ReadStore, Store};
use library_application::{AppError as PlanError, Flags, Notice, Outcome, Previewed, RunRequest};
use ports::{CommandIds, Names, Project, ProjectIdentity};
use run_launcher::{Launched, Launcher, Order, Orders, Outcome as Turn, Recorder};
use run_ports::{Edge, RunnerLauncher, SessionRecords};
use runs_application::{AppError as CoreError, EnqueueRequest, Enqueuer};
use sessions::{
    claim_root, ClaimName, MintSession, NameClaimCommand, Origin, SessionCommand, NAMES_DOMAIN,
    SESSION_DOMAIN,
};

use crate::error::RunError;

/// How many names are tried for a session before a taken one is the error.
const NAME_TRIES: usize = 3;

/// The refusal a name already claimed is answered with.
const NAMES_TAKEN: &str = "sessions-names-taken";

/// What a one-shot is asked.
#[derive(Clone, Debug, Default)]
pub struct OneShot {
    /// What the library plans from: the agent or the hand-assembled context,
    /// the prompt, the saved command.
    pub run: RunRequest,
    /// `--engine`: the engine to drive instead of the plan's.
    pub engine: String,
}

/// What a one-shot answered.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Answer {
    pub harp: String,
    pub session: String,
    pub engine: String,
    pub answer: String,
}

/// What a command came to: its answer or its error, and what it noticed.
pub struct Done<T> {
    pub result: Result<T, RunError>,
    pub notices: Vec<Notice>,
}

/// What the library plans a run with: its answer to `run --dry-run`.
pub trait Planner {
    fn plan(&self, flags: &Flags, request: &RunRequest) -> Outcome<Previewed>;
}

impl Planner for library_application::Library {
    fn plan(&self, flags: &Flags, request: &RunRequest) -> Outcome<Previewed> {
        self.dry_run(flags, request)
    }
}

/// What a driving adapter calls: the run use cases behind one trait, blocking.
pub trait RunService {
    /// `run --one-shot`.
    fn one_shot(&mut self, flags: &Flags, request: &OneShot) -> Done<Answer>;
}

/// The core the run is served by: its edge, and the store and catalog the
/// launcher and the recorder catch up from.
pub trait Core {
    type Edge: Edge;
    type Store: ReadStore + Store + Processed;

    fn edge(&self) -> Arc<Self::Edge>;
    fn store(&self) -> &Self::Store;
    fn catalog(&self) -> &Catalog;
}

/// Where the run is: the project's directory (the engine's) and the root of
/// the session output folders.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Place {
    pub project_dir: String,
    pub output_root: String,
}

/// The run use cases over their ports.
pub struct Runs<
    K: Core,
    T: run_ports::TokenSource,
    L: RunnerLauncher,
    R: SessionRecords,
    C: ports::Clock,
    W: SessionRecords + 'static,
> {
    pub(crate) core: K,
    pub(crate) planner: Box<dyn Planner>,
    pub(crate) identity: Box<dyn ProjectIdentity>,
    pub(crate) names: Box<dyn Names>,
    pub(crate) ids: Box<dyn CommandIds>,
    pub(crate) enqueuer: Enqueuer<K::Edge, T>,
    pub(crate) launcher: Launcher<K::Edge, L, R, C>,
    pub(crate) recorder: Recorder<W>,
    pub(crate) orders: Arc<Orders>,
    pub(crate) place: Place,
    pub(crate) env: BTreeMap<String, String>,
}

/// Everything [`Runs`] is made of.
pub struct Parts<
    K: Core,
    T: run_ports::TokenSource,
    L: RunnerLauncher,
    R: SessionRecords,
    C: ports::Clock,
    W: SessionRecords + 'static,
> {
    pub core: K,
    pub planner: Box<dyn Planner>,
    pub identity: Box<dyn ProjectIdentity>,
    pub names: Box<dyn Names>,
    pub ids: Box<dyn CommandIds>,
    pub enqueuer: Enqueuer<K::Edge, T>,
    pub launcher: Launcher<K::Edge, L, R, C>,
    pub recorder: Recorder<W>,
    /// The orders the launcher takes: the same the launcher holds.
    pub orders: Arc<Orders>,
    pub place: Place,
    /// The caller's environment, of which an engine passes on what it allows.
    pub env: BTreeMap<String, String>,
}

impl<K, T, L, R, C, W> Runs<K, T, L, R, C, W>
where
    K: Core,
    T: run_ports::TokenSource,
    L: RunnerLauncher,
    R: SessionRecords,
    C: ports::Clock,
    W: SessionRecords + 'static,
{
    pub fn new(parts: Parts<K, T, L, R, C, W>) -> Runs<K, T, L, R, C, W> {
        Runs {
            core: parts.core,
            planner: parts.planner,
            identity: parts.identity,
            names: parts.names,
            ids: parts.ids,
            enqueuer: parts.enqueuer,
            launcher: parts.launcher,
            recorder: parts.recorder,
            orders: parts.orders,
            place: parts.place,
            env: parts.env,
        }
    }

    /// `run --one-shot`: one turn on the prompt, and its answer.
    pub async fn one_shot(&mut self, flags: &Flags, request: &OneShot) -> Done<Answer> {
        let planned = self.planner.plan(flags, &request.run);
        let mut notices = planned.notices;
        let result = match planned.result {
            Err(error) => Err(RunError::Plan(error)),
            Ok(previewed) => self.run(previewed, request).await,
        };
        Done {
            result,
            notices: std::mem::take(&mut notices),
        }
    }

    async fn run(&mut self, previewed: Previewed, request: &OneShot) -> Result<Answer, RunError> {
        if !previewed.fatal.is_empty() {
            return Err(RunError::Plan(PlanError::aborted(&previewed.fatal)));
        }
        let plan = previewed.plan;
        if plan.prompt.trim().is_empty() {
            return Err(RunError::NoPrompt);
        }
        let engine = if request.engine.is_empty() {
            plan.backend.clone()
        } else {
            request.engine.clone()
        };
        if !self.launcher.drives(&engine) {
            return Err(RunError::UnknownEngine {
                name: engine,
                known: self.launcher.engines().join(", "),
            });
        }
        let project = self.identity.for_write(None).map_err(RunError::Project)?;
        let (session, harp) = self.mint(&project, &engine).await?;
        self.orders.place(
            &session,
            1,
            Order {
                harp: harp.clone(),
                engine: engine.clone(),
                context: plan.context,
                prompt: plan.prompt,
                env: self.env.clone(),
                cwd: Some(self.place.project_dir.clone()),
            },
        );
        self.recorder.watch(&session);
        let enqueued = self
            .enqueuer
            .enqueue(EnqueueRequest {
                session: session.clone(),
                incarnation: 1,
                mode: runs::RunMode::OneShot,
            })
            .await
            .map_err(RunError::Session)?;
        let launched = self
            .launcher
            .step(self.core.store(), self.core.catalog())
            .await
            .map_err(RunError::Launch)?;
        self.recorder
            .step(self.core.store(), self.core.catalog())
            .await
            .map_err(RunError::Launch)?;
        let ours = launched
            .into_iter()
            .find(|l: &Launched| l.session == session)
            .ok_or_else(|| RunError::NotEnded {
                run: text(&enqueued.run),
            })?;
        self.ended(&enqueued.run).await?;
        match ours.outcome {
            Turn::Answered { answer, .. } if answer.trim().is_empty() => Err(RunError::EmptyAnswer),
            Turn::Answered { answer, .. } => Ok(Answer {
                harp,
                session,
                engine,
                answer,
            }),
            Turn::Failed { class, detail } => Err(RunError::Turn { class, detail }),
            Turn::NotStarted { detail } => Err(RunError::NotStarted { detail }),
        }
    }

    /// Claim a name and mint the session on it: the session's id text and its
    /// harp. A name already claimed is tried again with another.
    async fn mint(
        &mut self,
        project: &Project,
        engine: &str,
    ) -> Result<(String, String), RunError> {
        let edge = self.core.edge();
        let id = uuid::Uuid::from_slice(&self.ids.command_id().value).unwrap_or_default();
        let session = id.hyphenated().to_string();
        let mut taken = None;
        for _ in 0..NAME_TRIES {
            let harp = self.names.project_id();
            let claim = NameClaimCommand::from(ClaimName {
                project_id: project.id.clone(),
                harp: harp.clone(),
                session: session.clone(),
            });
            let cover = Cover {
                domain: NAMES_DOMAIN.to_string(),
                root: Some(claim_root(&project.id, &harp)),
                edition: None,
            };
            match edge
                .handle(angzarr_embedded::v1::CommandBook::command_of(cover, &claim))
                .await
            {
                Ok(_) => {
                    self.mint_on(&edge, project, engine, &session, &harp)
                        .await?;
                    return Ok((session, harp));
                }
                Err(failure) => match CoreError::from_failure(failure) {
                    CoreError::Refused { code, .. } if code == NAMES_TAKEN => {
                        taken = Some(CoreError::Refused {
                            code,
                            args: BTreeMap::new(),
                        });
                    }
                    other => return Err(RunError::Session(other)),
                },
            }
        }
        Err(RunError::Session(taken.unwrap_or(CoreError::Failed {
            reason: "no name was tried".to_string(),
        })))
    }

    async fn mint_on(
        &self,
        edge: &Arc<K::Edge>,
        project: &Project,
        engine: &str,
        session: &str,
        harp: &str,
    ) -> Result<(), RunError> {
        let mint: SessionCommand = MintSession {
            harp: harp.to_string(),
            project_id: project.id.clone(),
            project_dir: self.place.project_dir.clone(),
            output_dir: format!("{}/{harp}", self.place.output_root),
            engine: engine.to_string(),
            origin: Origin::Oneshot,
            started_at: None,
            depth: 0,
        }
        .into();
        let root = uuid::Uuid::parse_str(session).map_or_else(
            |_| Uuid::from_bytes([0; 16]),
            |id| Uuid::from_bytes(*id.as_bytes()),
        );
        let cover = Cover {
            domain: SESSION_DOMAIN.to_string(),
            root: Some(root),
            edition: None,
        };
        edge.handle(angzarr_embedded::v1::CommandBook::command_of(cover, &mint))
            .await
            .map(drop)
            .map_err(|failure| RunError::Session(CoreError::from_failure(failure)))
    }

    /// The run's stream holds its end.
    async fn ended(&self, run: &Uuid) -> Result<(), RunError> {
        let cover = Cover {
            domain: runs::RUN_DOMAIN.to_string(),
            root: Some(run.clone()),
            edition: None,
        };
        let pages = self.core.edge().events(&cover, 0).await.map_err(|e| {
            RunError::Session(CoreError::Failed {
                reason: e.to_string(),
            })
        })?;
        let ended = pages.iter().any(|page| match &page.body {
            Some(angzarr_embedded::v1::event_page::Body::Event(any)) => {
                any.type_url.ends_with(".RunEnded")
            }
            _ => false,
        });
        if ended {
            Ok(())
        } else {
            Err(RunError::NotEnded { run: text(run) })
        }
    }
}

fn text(uuid: &Uuid) -> String {
    uuid::Uuid::from_slice(&uuid.value)
        .map(|id| id.hyphenated().to_string())
        .unwrap_or_default()
}

#[cfg(test)]
#[path = "one_shot.test.rs"]
mod tests;
