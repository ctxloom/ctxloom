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
use run_ports::{Edge, HookServer, LoginLinks, RunnerLauncher, SessionRecords};
use runs_application::{AppError as CoreError, EnqueueRequest, Enqueuer};
use sessions::{
    claim_root, ClaimName, MintSession, NameClaimCommand, Origin, SessionCommand, NAMES_DOMAIN,
    SESSION_DOMAIN,
};

use crate::credential::{refuse_child, share_login};
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

/// What a human's session came to: it was hers, and it is over.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Ended {
    pub harp: String,
    pub session: String,
    pub engine: String,
    /// The engine's exit code, none where a signal ended it.
    pub code: Option<i32>,
}

/// This session's launch among those a step made.
fn ours(launched: Vec<Launched>, session: &str) -> Option<Launched> {
    launched
        .into_iter()
        .find(|l: &Launched| l.session == session)
}

/// The error of a session whose launch a step did not make.
fn not_ended(session: &str) -> RunError {
    RunError::NotEnded {
        run: session.to_string(),
    }
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

    /// `run`: the human's session, in her terminal.
    fn interactive(&mut self, flags: &Flags, request: &OneShot) -> Done<Ended>;
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
    /// The absolute path of the running ctxloom binary, which the session's
    /// own `bin/ctxloom` link names; empty when it could not be found.
    pub program: String,
}

/// The run use cases over their ports.
pub struct Runs<
    K: Core,
    T: run_ports::TokenSource,
    L: RunnerLauncher,
    R: SessionRecords,
    W: SessionRecords + 'static,
> {
    pub(crate) core: K,
    pub(crate) planner: Box<dyn Planner>,
    pub(crate) identity: Box<dyn ProjectIdentity>,
    pub(crate) names: Box<dyn Names>,
    pub(crate) ids: Box<dyn CommandIds>,
    pub(crate) enqueuer: Enqueuer<K::Edge, T>,
    pub(crate) launcher: Launcher<K::Edge, L, R>,
    pub(crate) recorder: Recorder<W>,
    pub(crate) orders: Arc<Orders>,
    pub(crate) place: Place,
    pub(crate) env: BTreeMap<String, String>,
    pub(crate) links: Box<dyn LoginLinks>,
    pub(crate) os: String,
    pub(crate) hooks: Box<dyn HookServer>,
}

/// Everything [`Runs`] is made of.
pub struct Parts<
    K: Core,
    T: run_ports::TokenSource,
    L: RunnerLauncher,
    R: SessionRecords,
    W: SessionRecords + 'static,
> {
    pub core: K,
    pub planner: Box<dyn Planner>,
    pub identity: Box<dyn ProjectIdentity>,
    pub names: Box<dyn Names>,
    pub ids: Box<dyn CommandIds>,
    pub enqueuer: Enqueuer<K::Edge, T>,
    pub launcher: Launcher<K::Edge, L, R>,
    pub recorder: Recorder<W>,
    /// The orders the launcher takes: the same the launcher holds.
    pub orders: Arc<Orders>,
    pub place: Place,
    /// The caller's environment, of which an engine passes on what it allows.
    pub env: BTreeMap<String, String>,
    /// The session directory's one link: the login the primary shares.
    pub links: Box<dyn LoginLinks>,
    /// The operating system's name (`std::env::consts::OS`), which says
    /// whether a login can be shared by a link at all.
    pub os: String,
    /// Where an interactive run's hook processes are served (the run's
    /// socket); a one-shot's child is given none.
    pub hooks: Box<dyn HookServer>,
}

impl<K, T, L, R, W> Runs<K, T, L, R, W>
where
    K: Core,
    T: run_ports::TokenSource,
    L: RunnerLauncher,
    R: SessionRecords,
    W: SessionRecords + 'static,
{
    pub fn new(parts: Parts<K, T, L, R, W>) -> Runs<K, T, L, R, W> {
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
            links: parts.links,
            os: parts.os,
            hooks: parts.hooks,
        }
    }

    /// `run --one-shot`: one turn on the prompt, and its answer.
    pub async fn one_shot(&mut self, flags: &Flags, request: &OneShot) -> Done<Answer> {
        let planned = self.planner.plan(flags, &request.run);
        let mut notices = planned.notices;
        let result = match planned.result {
            Err(error) => Err(RunError::Plan(Box::new(error))),
            Ok(previewed) => self.run(previewed, request).await,
        };
        Done {
            result,
            notices: std::mem::take(&mut notices),
        }
    }

    /// `run`: the human's session. The engine's command is given her terminal
    /// and the run ends when she leaves it; a prompt is optional, and opens the
    /// conversation when given.
    pub async fn interactive(&mut self, flags: &Flags, request: &OneShot) -> Done<Ended> {
        let planned = self.planner.plan(flags, &request.run);
        let mut notices = planned.notices;
        let result = match planned.result {
            Err(error) => Err(RunError::Plan(Box::new(error))),
            Ok(previewed) => self.session(previewed, request, &mut notices).await,
        };
        Done {
            result,
            notices: std::mem::take(&mut notices),
        }
    }

    async fn session(
        &mut self,
        previewed: Previewed,
        request: &OneShot,
        notices: &mut Vec<Notice>,
    ) -> Result<Ended, RunError> {
        let (session, harp, engine, run) = self.start(previewed, request, true).await?;
        // The primary is handed her login before her engine starts.
        if let Some(credentials) = self.launcher.credentials(&engine) {
            notices.extend(share_login(
                &mut *self.links,
                &credentials,
                &self.env,
                &self.os,
                &harp,
            ));
        }
        // The run's hooks are served from before the engine starts until it
        // has exited, and the socket goes with the run either way: a launch
        // that failed stops the listener before its error is returned.
        let endpoint = self
            .endpoint(&run.value)
            .map_err(|e| RunError::Endpoint(Box::new(e)))?;
        let serving = self
            .hooks
            .serve(&endpoint)
            .await
            .map_err(RunError::Listener)?;
        // The session's directory names it before the engine runs: its hooks
        // (processes of their own) find the session by that file.
        if let Err(error) = self.catch_up().await {
            serving.stop().await;
            return Err(error);
        }
        let launched = self.launch().await;
        // A hook still in flight as the engine exits is delivered before the
        // listener has drained; the last catch-up (the transcript's writer)
        // follows it, so nothing delivered is left out of the transcript.
        serving.stop().await;
        let launched = launched?;
        self.catch_up().await?;
        let launched = ours(launched, &session).ok_or_else(|| not_ended(&session))?;
        self.ended(&run).await?;
        match launched.outcome {
            Turn::Ended { code } => Ok(Ended {
                harp,
                session,
                engine,
                code,
            }),
            Turn::NotStarted { detail } => Err(RunError::NotStarted { detail }),
            other => Err(RunError::Turn {
                class: run_ports::FailureClass::EngineError,
                detail: format!("{other:?}"),
            }),
        }
    }

    /// Plan checked, the session minted, the order placed and the run
    /// enqueued: the session, its name, the engine and the run.
    async fn start(
        &mut self,
        previewed: Previewed,
        request: &OneShot,
        interactive: bool,
    ) -> Result<(String, String, String, Uuid), RunError> {
        if !previewed.fatal.is_empty() {
            return Err(RunError::Plan(Box::new(PlanError::aborted(
                &previewed.fatal,
            ))));
        }
        let plan = previewed.plan;
        if !interactive && plan.prompt.trim().is_empty() {
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
        // A child agent is given an environment credential and nothing else.
        if !interactive {
            let credentials = self.launcher.credentials(&engine).unwrap_or_default();
            if let Some(refusal) = refuse_child(&engine, &credentials, &self.env) {
                return Err(RunError::Refused(refusal));
            }
        }
        // The interactive run's listener socket must fit before anything is
        // made, so a path no socket can take refuses the run with nothing to
        // undo. Every run's path is the same width, so any root tells.
        if interactive {
            self.endpoint(&[0; 16])
                .map_err(|e| RunError::Endpoint(Box::new(e)))?;
        }
        let project = self.identity.for_write(None).map_err(RunError::Project)?;
        let (session, harp) = self.mint(&project, &engine, interactive).await?;
        self.recorder.watch(&session);
        let mode = if interactive {
            runs::RunMode::Interactive
        } else {
            runs::RunMode::OneShot
        };
        let enqueued = self
            .enqueuer
            .enqueue(EnqueueRequest {
                session: session.clone(),
                incarnation: 1,
                mode,
            })
            .await
            .map_err(RunError::Session)?;
        // The order is placed once the run's tokens exist: the hook bearer
        // reaches the interactive engine's environment through it, and a
        // child's turn is given none of the listener's variables (the caller's
        // own, from a session it was started inside, are dropped).
        let mut env = self.env.clone();
        for name in run_ports::HOOK_VARIABLES {
            env.remove(name);
        }
        env.remove(run_ports::HARP_VARIABLE);
        if interactive {
            // A hook finds its session by the harp, and the engine's
            // environment is the only thing a hook process is given.
            env.insert(run_ports::HARP_VARIABLE.to_string(), harp.clone());
            let endpoint = self
                .endpoint(&enqueued.run.value)
                .map_err(|e| RunError::Endpoint(Box::new(e)))?;
            let run_text = uuid::Uuid::from_slice(&enqueued.run.value)
                .unwrap_or_default()
                .hyphenated()
                .to_string();
            env.extend(run_ports::hook_environment(
                &endpoint,
                &run_text,
                &enqueued.hook_token,
            ));
        }
        self.orders.place(
            &session,
            1,
            Order {
                harp: harp.clone(),
                engine: engine.clone(),
                context: plan.context,
                prompt: plan.prompt,
                env,
                cwd: Some(self.place.project_dir.clone()),
                interactive,
                model: plan.model,
            },
        );
        Ok((session, harp, engine, enqueued.run))
    }

    /// The socket path of the run rooted at `run`: under the caller's
    /// `XDG_RUNTIME_DIR`, else under ctxloom's home.
    fn endpoint(&self, run: &[u8]) -> Result<String, run_ports::EndpointError> {
        let var = |name: &str| self.env.get(name).map(String::as_str);
        let home = var(run_ports::HOME_VARIABLE)
            .or_else(|| var("HOME"))
            .unwrap_or_default();
        run_ports::endpoint_path(var("XDG_RUNTIME_DIR"), home, run)
    }

    /// The recorder caught up with everything the core holds so far.
    async fn catch_up(&mut self) -> Result<(), RunError> {
        self.recorder
            .step(self.core.store(), self.core.catalog())
            .await
            .map_err(RunError::Launch)
    }

    /// Launch what is owed and record it: this session's launch.
    async fn launched(&mut self, session: &str) -> Result<Launched, RunError> {
        let launched = self.launch().await?;
        self.catch_up().await?;
        ours(launched, session).ok_or_else(|| not_ended(session))
    }

    /// Launch what is owed, not yet recorded.
    async fn launch(&mut self) -> Result<Vec<Launched>, RunError> {
        self.launcher
            .step(self.core.store(), self.core.catalog())
            .await
            .map_err(RunError::Launch)
    }

    async fn run(&mut self, previewed: Previewed, request: &OneShot) -> Result<Answer, RunError> {
        let (session, harp, engine, run) = self.start(previewed, request, false).await?;
        let ours = self.launched(&session).await?;
        self.ended(&run).await?;
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
            Turn::Ended { .. } => Err(RunError::Turn {
                class: run_ports::FailureClass::EngineError,
                detail: "a turn ended as a session".to_string(),
            }),
        }
    }

    /// Claim a name and mint the session on it: the session's id text and its
    /// harp. A name already claimed is tried again with another.
    async fn mint(
        &mut self,
        project: &Project,
        engine: &str,
        interactive: bool,
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
                    self.mint_on(&edge, project, engine, &session, &harp, interactive)
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
        interactive: bool,
    ) -> Result<(), RunError> {
        let mint: SessionCommand = MintSession {
            harp: harp.to_string(),
            project_id: project.id.clone(),
            project_dir: self.place.project_dir.clone(),
            output_dir: format!("{}/{harp}", self.place.output_root),
            engine: engine.to_string(),
            origin: if interactive {
                Origin::Session
            } else {
                Origin::Oneshot
            },
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
