//! The one-shot run: a prompt, one turn, the answer.
//!
//! In one process, in order: the library plans the run (the agent or profile,
//! the assembled context, the engine, the startup findings); the project is
//! told; the run's hook listener is served (a one-shot is its own owner: its
//! child's hooks call back to it); a name is claimed and the session minted;
//! the run is enqueued; the session's directory is written so a hook can find
//! it; the launcher, caught up on the store, finds it owed a runner and drives
//! the engine for one turn; the listener is stopped; the recorder writes the
//! session's directory from the events (the transcript from the hooks'
//! facts); the run is checked to have ended, and the answer is returned. A
//! plan that is refused, or a session that cannot be made, has started no
//! engine.

use std::collections::BTreeMap;
use std::sync::Arc;

use angzarr_embedded::v1::{Cover, Uuid};
use angzarr_embedded::{Catalog, CommandBookExt, Processed, ReadStore, Store};
use library_application::{AppError as PlanError, Flags, Notice, Outcome, Previewed, RunRequest};
use ports::{CommandIds, Names, Project, ProjectIdentity};
use run_launcher::{Launched, Launcher, Order, Orders, Outcome as Turn, Recorder};
use run_ports::{CertMint, Edge, HookServer, LoginLinks, RunnerLauncher, SessionRecords};
use runs_application::{AppError as CoreError, EnqueueRequest, Enqueuer};
use sessions::{
    claim_root, ClaimName, MintSession, NameClaimCommand, Origin, SessionCommand, NAMES_DOMAIN,
    SESSION_DOMAIN,
};

use crate::credential::{refuse_child, refuse_file_only, share_login};
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
    /// The directory of the running ctxloom binary, which leads an
    /// engine's `PATH` (the terminal's and a child's) so a hook's bare
    /// `ctxloom` is it; empty
    /// when it could not be found.
    pub program_dir: String,
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
    pub(crate) certs: Box<dyn CertMint>,
}

/// What an engine is told of the run's listener: its URL and the public part
/// of its certificate.
struct Told {
    endpoint: String,
    cert: run_ports::PublicCert,
}

/// The run's listener, serving, and what the engine is told of it.
struct Listening {
    serving: Box<dyn run_ports::Serving>,
    told: Told,
}

/// A run made and enqueued, its order placed: the session, its name, the
/// engine and the run.
struct Started {
    session: String,
    harp: String,
    engine: String,
    run: Uuid,
}

/// The engine a run is on: the one asked for, else the plan's.
fn chosen_engine(plan: &library_application::DryRun, request: &OneShot) -> String {
    if request.engine.is_empty() {
        plan.engine.clone()
    } else {
        request.engine.clone()
    }
}

/// How the plan says `engine` is run, in the launch ports' terms. The plan is
/// asked for the engine the caller forces (`run --engine`, ruling 57), so it
/// resolved that engine's settings; a plan for another engine (a caller that
/// did not ask it) runs on the forced engine's built-ins.
fn settings_of(plan: &library_application::DryRun, engine: &str) -> run_ports::EngineSettings {
    if plan.engine != engine {
        return run_ports::EngineSettings::default();
    }
    let drive = &plan.drive;
    run_ports::EngineSettings {
        command: drive.command.clone(),
        session_home: if drive.session_home == "host" {
            run_ports::SessionHome::Host
        } else {
            run_ports::SessionHome::Session
        },
        auth: match (&drive.auth.source, &drive.auth.value) {
            // Nobody said: the engine's own source, as the adapter holds it.
            (library_application::Source::Default, _) => run_ports::AuthSetting::Builtin,
            (_, library_application::AuthSource::None) => run_ports::AuthSetting::None,
            (_, library_application::AuthSource::Login) => run_ports::AuthSetting::Login,
            (_, library_application::AuthSource::Token) => run_ports::AuthSetting::Builtin,
            (_, library_application::AuthSource::Credential { env, file }) => {
                run_ports::AuthSetting::Source {
                    env: env.clone(),
                    file: file.clone(),
                }
            }
        },
        env: drive.env.clone(),
    }
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
    /// Where a run's hook processes are served (the run's loopback port), the
    /// terminal session's and a one-shot child's alike.
    pub hooks: Box<dyn HookServer>,
    /// What mints a run's listener certificate.
    pub certs: Box<dyn CertMint>,
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
            certs: parts.certs,
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

    /// The run's hook listener: bound before the run is minted (the port it
    /// was given is part of what the engine is told), on loopback (the host
    /// row of the bind table), with the run's freshly minted certificate. A
    /// one-shot is its own owner (ruling 29), so it serves one as the
    /// terminal session does. Its caller stops it if the run does not start.
    async fn serve(&self) -> Result<Listening, RunError> {
        let plan = run_ports::bind_for(run_ports::Runtime::Host, &run_ports::PlatformFacts::NONE)
            .and_then(|plan| plan.check().map(|()| plan))
            .map_err(|e| RunError::Bind(Box::new(e)))?;
        // The run's certificate is minted for every address the child may
        // dial, before anything is served or made; its private key goes to the
        // listener alone, and the child is told the public part.
        let identity = self
            .certs
            .mint(&run_ports::CertNames::for_plan(&plan))
            .map_err(RunError::Listener)?;
        let serving = self
            .hooks
            .serve_tcp(plan.bind_addr, &identity)
            .await
            .map_err(RunError::Listener)?;
        match run_ports::endpoint_url(&plan.told_host, serving.port()) {
            Ok(endpoint) => Ok(Listening {
                serving,
                told: Told {
                    endpoint,
                    cert: identity.public().clone(),
                },
            }),
            Err(error) => {
                serving.stop().await;
                Err(RunError::Listener(angzarr_diagnostic::Report::of(&error)))
            }
        }
    }

    /// Everything a run does before its engine is launched, with its listener
    /// serving: the plan checked, the session minted, the run enqueued and its
    /// order placed, the primary handed her login, and the session's directory
    /// naming it (its hooks, processes of their own, find the session by that
    /// file). The listener is stopped if any of it fails.
    async fn ready(
        &mut self,
        previewed: Previewed,
        request: &OneShot,
        interactive: bool,
        notices: &mut Vec<Notice>,
    ) -> Result<(Box<dyn run_ports::Serving>, Started), RunError> {
        let settings = settings_of(&previewed.plan, &chosen_engine(&previewed.plan, request));
        // A child that cannot authenticate is refused before anything of its
        // run exists: no listener, no certificate, no session (ruling 53).
        if !interactive {
            let engine = chosen_engine(&previewed.plan, request);
            if let Some(refusal) = self.refuse_unauthenticated(&engine, &settings) {
                return Err(RunError::Refused(refusal));
            }
        }
        let Listening { serving, told } = self.serve().await?;
        let started = match self.start(previewed, request, interactive, &told).await {
            Ok(started) => started,
            Err(error) => {
                serving.stop().await;
                return Err(error);
            }
        };
        // The primary is handed her login before her engine starts.
        if interactive {
            if let Some(credentials) = self.launcher.credentials(&started.engine) {
                notices.extend(share_login(
                    &mut *self.links,
                    &settings.credentials(credentials),
                    &self.env,
                    &self.os,
                    &started.harp,
                ));
            }
        }
        if let Err(error) = self.catch_up().await {
            serving.stop().await;
            return Err(error);
        }
        Ok((serving, started))
    }

    /// Launch what is owed, stop the listener, then record: a hook still in
    /// flight as the engine exits is delivered before the listener has
    /// drained, and the last catch-up (the transcript's writer) follows it, so
    /// nothing delivered is left out of the transcript. This run's launch, its
    /// run's end checked.
    async fn drive(
        &mut self,
        serving: Box<dyn run_ports::Serving>,
        started: &Started,
    ) -> Result<Launched, RunError> {
        let launched = self.launch().await;
        serving.stop().await;
        let launched = launched?;
        self.catch_up().await?;
        let ours = ours(launched, &started.session).ok_or_else(|| not_ended(&started.session))?;
        self.ended(&started.run).await?;
        Ok(ours)
    }

    async fn session(
        &mut self,
        previewed: Previewed,
        request: &OneShot,
        notices: &mut Vec<Notice>,
    ) -> Result<Ended, RunError> {
        let (serving, started) = self.ready(previewed, request, true, notices).await?;
        let launched = self.drive(serving, &started).await?;
        let Started {
            session,
            harp,
            engine,
            ..
        } = started;
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
    /// enqueued.
    async fn start(
        &mut self,
        previewed: Previewed,
        request: &OneShot,
        interactive: bool,
        told: &Told,
    ) -> Result<Started, RunError> {
        if !previewed.fatal.is_empty() {
            return Err(RunError::Plan(Box::new(PlanError::aborted(
                &previewed.fatal,
            ))));
        }
        let plan = previewed.plan;
        if !interactive && plan.prompt.trim().is_empty() {
            return Err(RunError::NoPrompt);
        }
        let engine = chosen_engine(&plan, request);
        let settings = settings_of(&plan, &engine);
        if !self.launcher.drives(&engine) {
            return Err(RunError::UnknownEngine {
                name: engine,
                known: self.launcher.engines().join(", "),
            });
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
        // reaches the engine's environment through it (the terminal session's
        // and a one-shot child's alike, ruling 51), and the caller's own
        // listener variables, from a session it was started inside, are
        // replaced by this run's, never passed on.
        let mut env = self.env.clone();
        for name in run_ports::HOOK_VARIABLES {
            env.remove(name);
        }
        // A hook finds its session by the harp, and the engine's environment
        // is the only thing a hook process is given.
        env.insert(run_ports::HARP_VARIABLE.to_string(), harp.clone());
        // The running binary's directory leads the engine's PATH, so a hook's
        // bare `ctxloom` is this binary and not another one the user has (the
        // Go one); the inherited PATH follows it.
        if !self.place.program_dir.is_empty() {
            let path = match env.get("PATH").filter(|path| !path.is_empty()) {
                Some(inherited) => format!("{}:{inherited}", self.place.program_dir),
                None => self.place.program_dir.clone(),
            };
            env.insert("PATH".to_string(), path);
        }
        let run_text = text(&enqueued.run);
        env.extend(run_ports::hook_environment(
            &told.endpoint,
            &run_text,
            &enqueued.hook_token,
            &told.cert,
        ));
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
                effort: plan.effort,
                settings,
            },
        );
        Ok(Started {
            session,
            harp,
            engine,
            run: enqueued.run,
        })
    }

    /// A child agent is given an environment credential and nothing else:
    /// one whose engine's source is a file alone, or with none of its
    /// variables set, is refused.
    fn refuse_unauthenticated(
        &self,
        engine: &str,
        settings: &run_ports::EngineSettings,
    ) -> Option<runs::RunRejection> {
        let base = self.launcher.credentials(engine).unwrap_or_default();
        refuse_file_only(engine, &settings.auth, &base)
            .or_else(|| refuse_child(engine, &settings.credentials(base), &self.env))
    }

    /// The recorder caught up with everything the core holds so far.
    async fn catch_up(&mut self) -> Result<(), RunError> {
        self.recorder
            .step(self.core.store(), self.core.catalog())
            .await
            .map_err(RunError::Launch)
    }

    /// Launch what is owed, not yet recorded.
    async fn launch(&mut self) -> Result<Vec<Launched>, RunError> {
        self.launcher
            .step(self.core.store(), self.core.catalog())
            .await
            .map_err(RunError::Launch)
    }

    /// The one-shot: its listener served for its child's hooks (a one-shot is
    /// its own owner, ruling 29), one turn, and the reply the child's stream
    /// gave.
    async fn run(&mut self, previewed: Previewed, request: &OneShot) -> Result<Answer, RunError> {
        let mut unused = Vec::new();
        let (serving, started) = self.ready(previewed, request, false, &mut unused).await?;
        let ours = self.drive(serving, &started).await?;
        let Started {
            session,
            harp,
            engine,
            ..
        } = started;
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
