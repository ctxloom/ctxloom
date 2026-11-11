//! The one-shot run: a prompt, one turn, the answer.
//!
//! In one process, in order: the library plans the run (the agent or profile,
//! the assembled context, the engine, the startup findings); the run's own
//! lock is taken (rulings 86, 92) and the run enqueued, its pre-launch checks
//! judged by the run itself; the project is told, a name claimed (never one
//! whose directory exists, defect 3) and the session minted; the session's
//! lock is taken for the run's life (defect 1); the run's hook listener is
//! served (a one-shot is its own owner: its child's hooks call back to it, to
//! the session the engine is told by id, `CTXLOOM_SESSION`); the launcher,
//! caught up on the store, finds it owed a runner, drives the engine for one
//! turn and lets the run's lock go once the runner is started; the listener
//! is stopped; the session is told its incarnation ended (ruling 85); the run
//! is checked to have ended, and the answer is returned. Nothing is written
//! into the session's directory but its home (ruling 87a: the events are the
//! record). A plan that is refused, or a session that cannot be made, has
//! started no engine.

use std::collections::BTreeMap;
use std::sync::Arc;

use angzarr_diagnostic::Diagnostic;

use angzarr_embedded::v1::{Cover, Uuid};
use angzarr_embedded::{Catalog, CommandBookExt, Failure, Payload, Processed, ReadStore, Store};
use library_application::{AppError as PlanError, Flags, Notice, Outcome, Previewed, RunRequest};
use ports::{CommandIds, Names, Project, ProjectIdentity};
use run_launcher::{Launched, Launcher, Order, Orders, Outcome as Turn};
use run_ports::{CertMint, Edge, HookServer, LoginLinks, RunnerLauncher, SessionRecords};
use runs_application::{
    AppError as CoreError, EnqueueError, EnqueueRequest, Enqueued, Enqueuer, Evidence,
};
use sessions::{
    claim_root, ClaimName, MintSession, NameClaimCommand, NameClaimEvent, Origin, ReleaseName,
    ResumeSession, SessionCommand, SessionEvent, SessionRejection, SessionState, NAMES_DOMAIN,
    SESSION_DOMAIN,
};

use crate::credential::{credential_evidence, share_login};
use crate::error::RunError;

/// How many names are tried for a session before a taken one is the error.
const NAME_TRIES: usize = 3;

/// Why an incarnation ended (ruling 85, Go's causes): its engine ran and
/// exited, or its launch failed before that.
const RUNNER_EXIT: &str = "runner-exit";
const LAUNCH_FAILED: &str = "launch-failed";

/// What a one-shot is asked.
#[derive(Clone, Debug, Default)]
pub struct OneShot {
    /// What the library plans from: the agent or the hand-assembled context,
    /// the prompt, the saved command.
    pub run: RunRequest,
    /// `--engine`: the engine to drive instead of the plan's.
    pub engine: String,
    /// `--session <harp>`: the session to resume (owner ruling 115); empty
    /// is a new session.
    pub session: String,
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

/// The permits the launcher did not follow, said by the permit's name (owner
/// rulings 78, 78b).
fn said_permits(launched: &Launched, notices: &mut Vec<Notice>) {
    notices.extend(
        launched
            .ignored
            .iter()
            .map(crate::credential::ignored_permit),
    );
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
pub struct Runs<K: Core, T: run_ports::TokenSource, L: RunnerLauncher, R: SessionRecords> {
    pub(crate) core: K,
    pub(crate) planner: Box<dyn Planner>,
    pub(crate) identity: Box<dyn ProjectIdentity>,
    pub(crate) names: Box<dyn Names>,
    pub(crate) ids: Box<dyn CommandIds>,
    pub(crate) enqueuer: Enqueuer<K::Edge, T>,
    pub(crate) launcher: Launcher<K::Edge, L, R>,
    pub(crate) dirs: Box<dyn SessionRecords>,
    pub(crate) orders: Arc<Orders>,
    pub(crate) place: Place,
    pub(crate) env: BTreeMap<String, String>,
    pub(crate) links: Box<dyn LoginLinks>,
    pub(crate) os: String,
    pub(crate) hooks: Box<dyn HookServer>,
    pub(crate) certs: Box<dyn CertMint>,
    pub(crate) locks: Box<dyn session_ports::SessionLock>,
    pub(crate) run_locks: Arc<dyn run_ports::RunLocks>,
    pub(crate) git_user: Option<run_ports::GitUser>,
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
/// engine and the run, and the session's lock, held for the run's life.
struct Started {
    session: String,
    harp: String,
    engine: String,
    run: Uuid,
    /// The session's incarnation the run is: 1 for a new session, the next
    /// for a resume (owner ruling 115).
    incarnation: u32,
    lock: session_ports::Held,
}

/// A session a run resumes (owner ruling 115): its id and name, its engine,
/// and the engine's own session the run hands it.
#[derive(Clone, Debug)]
struct Resume {
    session: String,
    harp: String,
    engine: String,
    native_id: String,
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
pub struct Parts<K: Core, T: run_ports::TokenSource, L: RunnerLauncher, R: SessionRecords> {
    pub core: K,
    pub planner: Box<dyn Planner>,
    pub identity: Box<dyn ProjectIdentity>,
    pub names: Box<dyn Names>,
    pub ids: Box<dyn CommandIds>,
    pub enqueuer: Enqueuer<K::Edge, T>,
    pub launcher: Launcher<K::Edge, L, R>,
    /// The session directories, asked whether a harp's exists (defect 3).
    pub dirs: Box<dyn SessionRecords>,
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
    /// The session's liveness lock, which the run holds for its life
    /// (defect 1), so the session reads live to every other process.
    pub locks: Box<dyn session_ports::SessionLock>,
    /// The run's own lock, which this process holds from before the enqueue
    /// is visible until the runner is started (rulings 86, 92).
    pub run_locks: Arc<dyn run_ports::RunLocks>,
    /// The user's git identity in the project, which a session home is
    /// seeded with (owner ruling 112); none known, none is seeded.
    pub git_user: Option<run_ports::GitUser>,
}

impl<K, T, L, R> Runs<K, T, L, R>
where
    K: Core,
    T: run_ports::TokenSource,
    L: RunnerLauncher,
    R: SessionRecords,
{
    pub fn new(parts: Parts<K, T, L, R>) -> Runs<K, T, L, R> {
        Runs {
            core: parts.core,
            planner: parts.planner,
            identity: parts.identity,
            names: parts.names,
            ids: parts.ids,
            enqueuer: parts.enqueuer,
            launcher: parts.launcher,
            dirs: parts.dirs,
            orders: parts.orders,
            place: parts.place,
            env: parts.env,
            links: parts.links,
            os: parts.os,
            hooks: parts.hooks,
            certs: parts.certs,
            locks: parts.locks,
            run_locks: parts.run_locks,
            git_user: parts.git_user,
        }
    }

    /// The core, every other part let go: the enqueuer's, the launcher's and
    /// the listener's edges dropped with them, so what reaches the core's
    /// store afterwards is the core alone, and what its runtime still runs
    /// (ruling 131: the composition root closes the store once both are
    /// gone).
    pub fn into_core(self) -> K {
        self.core
    }

    /// `run --one-shot`: one turn on the prompt, and its answer.
    pub async fn one_shot(&mut self, flags: &Flags, request: &OneShot) -> Done<Answer> {
        let (request, resume) = match self.resolve(request).await {
            Ok(resolved) => resolved,
            Err(error) => return refused(error),
        };
        let request = &request;
        let planned = self.plan(flags, request);
        let mut notices = planned.notices;
        let result = match planned.result {
            Err(error) => Err(RunError::Plan(Box::new(error))),
            Ok(previewed) => self.run(previewed, request, resume, &mut notices).await,
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
        let (request, resume) = match self.resolve(request).await {
            Ok(resolved) => resolved,
            Err(error) => return refused(error),
        };
        let request = &request;
        let planned = self.plan(flags, request);
        let mut notices = planned.notices;
        let result = match planned.result {
            Err(error) => Err(RunError::Plan(Box::new(error))),
            Ok(previewed) => self.session(previewed, request, resume, &mut notices).await,
        };
        Done {
            result,
            notices: std::mem::take(&mut notices),
        }
    }

    /// The request as it is planned, and the session it resumes, if it names
    /// one (owner ruling 115): planned on the session's own engine unless
    /// `--engine` names one (the session's aggregate refuses another).
    async fn resolve(&mut self, request: &OneShot) -> Result<(OneShot, Option<Resume>), RunError> {
        if request.session.is_empty() {
            return Ok((request.clone(), None));
        }
        let resume = self.resumable(&request.session).await?;
        let mut request = request.clone();
        if request.engine.is_empty() {
            request.engine = resume.engine.clone();
            request.run.engine = resume.engine.clone();
        }
        Ok((request, Some(resume)))
    }

    /// The session named `harp`, if it can be resumed as far as a read says:
    /// a name no session holds is `SessionUnknown`, a claimed name whose
    /// stream holds nothing (`session remove` destroyed it, its claim kept,
    /// ruling 94c) `SessionDestroyed`, and a session whose lock another
    /// process holds `SessionRunning`. Runs other processes left are swept
    /// first (the launcher's step: an owner found dead ends its incarnation,
    /// ruling 93), so a crashed session's open incarnation does not keep it.
    async fn resumable(&mut self, harp: &str) -> Result<Resume, RunError> {
        let refuse = RunError::Resume;
        self.launch().await?;
        let project = self.identity.for_write(None).map_err(RunError::Project)?;
        let edge = self.core.edge();
        let failed = |e: &dyn std::fmt::Display| {
            RunError::Session(CoreError::Failed {
                reason: e.to_string(),
            })
        };
        let mut holder = None;
        for any in event_bodies(
            &edge
                .events(&claim_cover(&project, harp), 0)
                .await
                .map_err(|e| failed(&e))?,
        ) {
            match NameClaimEvent::from_any(any).map_err(|e| failed(&e))? {
                NameClaimEvent::Claimed(claimed) => holder = Some(claimed.session),
                NameClaimEvent::Released(_) => holder = None,
            }
        }
        let Some(session) = holder else {
            return Err(refuse(SessionRejection::SessionUnknown {
                harp: harp.to_string(),
            }));
        };
        let state = self.session_state(&session).await?;
        if state.harp.is_empty() {
            return Err(refuse(SessionRejection::SessionDestroyed {
                harp: harp.to_string(),
            }));
        }
        if self.run_locks.session(&self.place.project_dir, harp) == run_ports::LockProbe::Held {
            return Err(refuse(SessionRejection::SessionRunning {
                harp: harp.to_string(),
            }));
        }
        Ok(Resume {
            session,
            harp: state.harp,
            engine: state.engine,
            native_id: state.native_id,
        })
    }

    /// The session `session` as its stream folds now.
    async fn session_state(&self, session: &str) -> Result<SessionState, RunError> {
        let failed = |e: &dyn std::fmt::Display| {
            RunError::Session(CoreError::Failed {
                reason: e.to_string(),
            })
        };
        let pages = self
            .core
            .edge()
            .events(&session_cover(session), 0)
            .await
            .map_err(|e| failed(&e))?;
        let mut state = SessionState::default();
        for page in &pages {
            if let Some(angzarr_embedded::v1::event_page::Body::Event(any)) = &page.body {
                let event = SessionEvent::from_any(any).map_err(|e| failed(&e))?;
                let at = page
                    .meta
                    .as_ref()
                    .and_then(|meta| meta.recorded_at)
                    .unwrap_or_default();
                sessions::apply(&mut state, &event, at);
            }
        }
        Ok(state)
    }

    /// Owe `resume`'s session its next incarnation on `engine` (the
    /// aggregate's `ResumeSession`, which refuses a session running, one with
    /// no native session, or another engine): the incarnation owed.
    async fn owe(&self, resume: &Resume, engine: &str) -> Result<u32, RunError> {
        let command: SessionCommand = ResumeSession {
            harp: resume.harp.clone(),
            engine: engine.to_string(),
        }
        .into();
        self.core
            .edge()
            .handle(angzarr_embedded::v1::CommandBook::command_of(
                session_cover(&resume.session),
                &command,
            ))
            .await
            .map_err(resume_refused)?;
        Ok(self.session_state(&resume.session).await?.incarnation)
    }

    /// The plan of the run asked for. An engine this build does not drive is
    /// planned anyway (ruling 73d), on the agent's own engine, so the plan's
    /// findings and the prompt are still judged beside it: the run's enqueue
    /// refuses the engine itself.
    fn plan(&self, flags: &Flags, request: &OneShot) -> Outcome<Previewed> {
        if request.engine.is_empty() || self.launcher.drives(&request.engine) {
            return self.planner.plan(flags, &request.run);
        }
        let run = RunRequest {
            engine: String::new(),
            ..request.run.clone()
        };
        self.planner.plan(flags, &run)
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

    /// Everything a run does before its engine is launched (owner rulings
    /// 73f, 73g, in this order): the run's lock taken and the run enqueued,
    /// its pre-launch checks judged by the run itself; the project told, a
    /// name claimed, the session minted and its lock taken; the certificate
    /// minted and the listener served; the order placed and the primary
    /// handed her login. A refused run has made nothing (its run lock let
    /// go); a failure after the enqueue ends the run it enqueued and its
    /// session's incarnation, and the listener is stopped if it was served.
    async fn ready(
        &mut self,
        previewed: Previewed,
        request: &OneShot,
        resume: Option<Resume>,
        interactive: bool,
        notices: &mut Vec<Notice>,
    ) -> Result<(Box<dyn run_ports::Serving>, Started), RunError> {
        let engine = chosen_engine(&previewed.plan, request);
        let settings = settings_of(&previewed.plan, &engine);
        // A resume owes its session the next incarnation before its run is
        // enqueued (owner ruling 115), so a refused resume has burned no run
        // stream; a new session is minted after (its first incarnation).
        let (session, incarnation) = match &resume {
            Some(resume) => (resume.session.clone(), self.owe(resume, &engine).await?),
            None => {
                let id = uuid::Uuid::from_slice(&self.ids.command_id().value).unwrap_or_default();
                (id.hyphenated().to_string(), 1)
            }
        };
        let enqueued = match self
            .enqueue(
                &previewed,
                &session,
                incarnation,
                &engine,
                &settings,
                interactive,
            )
            .await
        {
            Ok(enqueued) => enqueued,
            Err(error) => {
                if matches!(error, RunError::Refused(_)) {
                    // The plan's fatal findings, beside a refusal of other
                    // checks, are said as warnings (the preview leaves them
                    // to the refusal that lists them, which this one does
                    // not).
                    notices.extend(previewed.fatal.iter().map(|f| Notice::Finding {
                        text: f.text.clone(),
                        remedy: f.remedy.clone(),
                    }));
                }
                if resume.is_some() {
                    self.incarnation_ended(&session, incarnation, LAUNCH_FAILED)
                        .await;
                }
                return Err(error);
            }
        };
        let made = match &resume {
            Some(resume) => Ok(resume.harp.clone()),
            None => self.make(&session, &engine, interactive).await,
        };
        let harp = match made {
            Ok(harp) => harp,
            Err(error) => {
                return Err(self
                    .abandon(&enqueued.run, &session, incarnation, false, error)
                    .await)
            }
        };
        let lock = match self.locks.hold(&harp) {
            Ok(lock) => lock,
            Err(report) => {
                let error = RunError::Lock(report);
                return Err(self
                    .abandon(&enqueued.run, &session, incarnation, true, error)
                    .await);
            }
        };
        let Listening { serving, told } = match self.serve().await {
            Ok(listening) => listening,
            Err(error) => {
                return Err(self
                    .abandon(&enqueued.run, &session, incarnation, true, error)
                    .await)
            }
        };
        let started = self.start(
            previewed,
            request,
            interactive,
            &told,
            Placed {
                session,
                harp,
                incarnation,
                resume: resume.map(|resume| resume.native_id),
            },
            &enqueued,
            lock,
        );
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
        Ok((serving, started))
    }

    /// Launch what is owed, stop the listener (a hook still in flight as
    /// the engine exits is delivered before it has drained), then tell the
    /// session its incarnation ended (ruling 85). This run's launch, its
    /// run's end checked. The session's lock is let go by the caller, after.
    async fn drive(
        &mut self,
        serving: Box<dyn run_ports::Serving>,
        started: &Started,
    ) -> Result<Launched, RunError> {
        let launched = self.launch().await;
        serving.stop().await;
        let launched = launched?;
        let ours = ours(launched, &started.session).ok_or_else(|| not_ended(&started.session))?;
        let cause = match ours.outcome {
            Turn::NotStarted { .. } => LAUNCH_FAILED,
            _ => RUNNER_EXIT,
        };
        self.incarnation_ended(&started.session, started.incarnation, cause)
            .await;
        self.ended(&started.run).await?;
        Ok(ours)
    }

    /// Tell `session` its incarnation ended, for `cause`; a telling that
    /// fails leaves it to a sweep (ruling 93), and is not the run's failure.
    async fn incarnation_ended(&self, session: &str, incarnation: u32, cause: &str) {
        let facts = runs_application::Facts::new(self.core.edge());
        let _ = facts.incarnation_ended(session, incarnation, cause).await;
    }

    async fn session(
        &mut self,
        previewed: Previewed,
        request: &OneShot,
        resume: Option<Resume>,
        notices: &mut Vec<Notice>,
    ) -> Result<Ended, RunError> {
        let (serving, started) = self
            .ready(previewed, request, resume, true, notices)
            .await?;
        let launched = self.drive(serving, &started).await?;
        said_permits(&launched, notices);
        let Started {
            session,
            harp,
            engine,
            lock,
            ..
        } = started;
        drop(lock);
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

    /// The run enqueued, its pre-launch checks judged by the run (rulings
    /// 73e, 73f) on what the ports say: the plan's prompt and fatal findings,
    /// the engines the launcher drives, and a child's credential by the names
    /// of its variables. A refusal the plan's fatal findings alone make is the
    /// plan's own error, as it always was.
    async fn enqueue(
        &mut self,
        previewed: &Previewed,
        session: &str,
        incarnation: u32,
        engine: &str,
        settings: &run_ports::EngineSettings,
        interactive: bool,
    ) -> Result<Enqueued, RunError> {
        let mode = if interactive {
            runs::RunMode::Interactive
        } else {
            runs::RunMode::OneShot
        };
        let evidence = Evidence {
            prompt_given: !previewed.plan.prompt.trim().is_empty(),
            engine: engine.to_string(),
            drivable_engines: self.launcher.engines(),
            credential: self.credential(engine, settings, interactive),
            fatal_findings: u32::try_from(previewed.fatal.len()).unwrap_or(u32::MAX),
            posture: posture_of(&previewed.plan),
        };
        // The run's own lock, taken before its enqueue is visible (ruling 92):
        // no other process ever sees it owed a runner with its lock free.
        let run = text(&runs::run_root(session, incarnation));
        let lock = self.run_locks.hold_run(&run).map_err(RunError::Lock)?;
        let request = EnqueueRequest {
            session: session.to_string(),
            incarnation,
            mode,
            evidence,
            launcher_lock: lock.path().to_string(),
        };
        let enqueued = self.enqueuer.enqueue(request).await;
        if enqueued.is_ok() {
            // Held until the launcher has started the runner.
            self.orders.hold(session, incarnation, lock);
        }
        enqueued.map_err(|error| match error {
            EnqueueError::Refused(runs::RunRejection::LaunchRefused { refusals, .. })
                if refusals.is_empty() =>
            {
                RunError::Plan(Box::new(PlanError::aborted(&previewed.fatal)))
            }
            EnqueueError::Refused(rejection) => RunError::Refused(rejection),
            EnqueueError::Failed(error) => RunError::Session(error),
        })
    }

    /// What a child's run authenticates with (ruling 53): none for the
    /// primary, and none for an engine nobody drives (its policy is its
    /// launcher's, ruling 73d).
    fn credential(
        &self,
        engine: &str,
        settings: &run_ports::EngineSettings,
        interactive: bool,
    ) -> Option<runs_application::CredentialEvidence> {
        if interactive {
            return None;
        }
        let base = self.launcher.credentials(engine)?;
        let policy = settings.credentials(base);
        Some(credential_evidence(
            engine,
            &settings.auth,
            &base,
            &policy,
            &self.env,
        ))
    }

    /// The project told, a name claimed and the session minted on it, for
    /// the run already enqueued. The session's harp.
    async fn make(
        &mut self,
        session: &str,
        engine: &str,
        interactive: bool,
    ) -> Result<String, RunError> {
        let project = self.identity.for_write(None).map_err(RunError::Project)?;
        self.mint(&project, session, engine, interactive).await
    }

    /// End the enqueued `run` of `session` that `error` stopped before its
    /// engine was launched (owner ruling 73g): a `LaunchFailed`, so no run is
    /// left owed a runner, its run lock let go, and, for a session already
    /// `minted`, its incarnation ended (ruling 85). The error is the run's,
    /// whatever recording its end met.
    async fn abandon(
        &mut self,
        run: &Uuid,
        session: &str,
        incarnation: u32,
        minted: bool,
        error: RunError,
    ) -> RunError {
        let facts = runs_application::Facts::new(self.core.edge());
        let _ = facts.launch_failed(run, false, error.code()).await;
        self.orders.release(session, incarnation);
        // A resume's incarnation was owed before its run was enqueued: its
        // callers pass `minted` for it.
        if minted {
            self.incarnation_ended(session, incarnation, LAUNCH_FAILED)
                .await;
        }
        error
    }

    /// The order placed for the run enqueued and its session minted, with
    /// what the engine is told of the listener.
    #[allow(clippy::too_many_arguments)]
    fn start(
        &mut self,
        previewed: Previewed,
        request: &OneShot,
        interactive: bool,
        told: &Told,
        placed: Placed,
        enqueued: &Enqueued,
        lock: session_ports::Held,
    ) -> Started {
        let Placed {
            session,
            harp,
            incarnation,
            resume,
        } = placed;
        let plan = previewed.plan;
        let engine = chosen_engine(&plan, request);
        let settings = settings_of(&plan, &engine);
        // The order is placed once the run's tokens exist: the hook bearer
        // reaches the engine's environment through it (the terminal session's
        // and a one-shot child's alike, ruling 51), and the caller's own
        // listener variables, from a session it was started inside, are
        // replaced by this run's, never passed on.
        let mut env = self.env.clone();
        // What the run itself sets for the engine, which no permit replaces
        // (ruling 78b): HOME, which the engine's adapter makes the session
        // home (ruling 112; ctxloom's own is CTXLOOM_HOME, which its hook
        // processes' wiring reads first), and the PATH below; the CTXLOOM_*
        // names are the run's whatever this says.
        let mut owned = vec!["HOME".to_string()];
        for name in run_ports::HOOK_VARIABLES {
            env.remove(name);
        }
        // The engine's environment is the only thing a hook process is
        // given: the session's harp (for its statusline) and, below, the
        // session's id it addresses its facts to.
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
            owned.push("PATH".to_string());
        }
        let run_text = text(&enqueued.run);
        env.extend(run_ports::hook_environment(
            &told.endpoint,
            &run_text,
            &session,
            &enqueued.hook_token,
            &told.cert,
        ));
        self.orders.place(
            &session,
            incarnation,
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
                permission_mode: plan.permission_mode,
                settings,
                owned,
                git_user: self.git_user.clone(),
                resume,
            },
        );
        Started {
            session,
            harp,
            engine,
            run: enqueued.run.clone(),
            incarnation,
            lock,
        }
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
    async fn run(
        &mut self,
        previewed: Previewed,
        request: &OneShot,
        resume: Option<Resume>,
        notices: &mut Vec<Notice>,
    ) -> Result<Answer, RunError> {
        let (serving, started) = self
            .ready(previewed, request, resume, false, notices)
            .await?;
        let ours = self.drive(serving, &started).await?;
        said_permits(&ours, notices);
        let Started {
            session,
            harp,
            engine,
            lock,
            ..
        } = started;
        drop(lock);
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
    /// harp. A name already claimed (the claim refused `NamesTaken`, read by
    /// the refusal's type) is tried again with another, and so is one whose
    /// directory exists (defect 3: however the store came to have no claim on
    /// it), without claiming it. A mint refused after its name was claimed
    /// gives the name back (review finding 7), best effort: the mint's
    /// refusal is the error.
    async fn mint(
        &mut self,
        project: &Project,
        session: &str,
        engine: &str,
        interactive: bool,
    ) -> Result<String, RunError> {
        let edge = self.core.edge();
        let session = session.to_string();
        let mut taken = None;
        for _ in 0..NAME_TRIES {
            let harp = self.names.project_id();
            if self.dirs.exists(&harp) {
                taken = Some(names_taken(&harp));
                continue;
            }
            let claim = NameClaimCommand::from(ClaimName {
                project_id: project.id.clone(),
                harp: harp.clone(),
                session: session.clone(),
            });
            match edge
                .handle(angzarr_embedded::v1::CommandBook::command_of(
                    claim_cover(project, &harp),
                    &claim,
                ))
                .await
            {
                Ok(_) => {
                    let minted = self
                        .mint_on(&edge, project, engine, &session, &harp, interactive)
                        .await;
                    if minted.is_err() {
                        let release = NameClaimCommand::from(ReleaseName {
                            session: session.clone(),
                        });
                        let _ = edge
                            .handle(angzarr_embedded::v1::CommandBook::command_of(
                                claim_cover(project, &harp),
                                &release,
                            ))
                            .await;
                    }
                    return minted.map(|()| harp);
                }
                Err(Failure::Rejected(rejected)) if refused_taken(&rejected) => {
                    taken = Some(names_taken(&harp));
                }
                Err(failure) => return Err(RunError::Session(CoreError::from_failure(failure))),
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

/// Where a run's order is placed: its session, name and incarnation, and
/// the engine's own session it resumes (none: a new one).
struct Placed {
    session: String,
    harp: String,
    incarnation: u32,
    resume: Option<String>,
}

/// A command refused before it ran: its error, nothing noticed.
fn refused<T>(error: RunError) -> Done<T> {
    Done {
        result: Err(error),
        notices: Vec::new(),
    }
}

/// A resume's command refused or failed: the session domain's refusal as
/// itself (its type read back as the rejection), anything else the kernel's.
fn resume_refused(failure: Failure<angzarr_embedded::Rejected>) -> RunError {
    if let Failure::Rejected(rejected) = &failure {
        let rejection = rejected
            .rejection::<angzarr_embedded::markup::TypedRefusal>()
            .and_then(|typed| SessionRejection::try_from(typed).ok());
        if let Some(rejection) = rejection {
            return RunError::Resume(rejection);
        }
    }
    RunError::Session(CoreError::from_failure(failure))
}

/// The event bodies of a stream's pages, oldest first.
fn event_bodies(pages: &[angzarr_embedded::v1::EventPage]) -> Vec<&angzarr_embedded::Any> {
    pages
        .iter()
        .filter_map(|page| match &page.body {
            Some(angzarr_embedded::v1::event_page::Body::Event(any)) => Some(any),
            _ => None,
        })
        .collect()
}

/// The stream of the session `session` (its id's text).
fn session_cover(session: &str) -> Cover {
    let root = uuid::Uuid::parse_str(session).map_or_else(
        |_| Uuid::from_bytes([0; 16]),
        |id| Uuid::from_bytes(*id.as_bytes()),
    );
    Cover {
        domain: SESSION_DOMAIN.to_string(),
        root: Some(root),
        edition: None,
    }
}

/// The claim stream of `harp` in `project`.
fn claim_cover(project: &Project, harp: &str) -> Cover {
    Cover {
        domain: NAMES_DOMAIN.to_string(),
        root: Some(claim_root(&project.id, harp)),
        edition: None,
    }
}

/// Whether a claim was refused because the name is taken: the refusal's
/// TYPE, read back as the domain's rejection, never its code's text.
fn refused_taken(rejected: &angzarr_embedded::Rejected) -> bool {
    rejected
        .rejection::<angzarr_embedded::markup::TypedRefusal>()
        .and_then(|typed| SessionRejection::try_from(typed).ok())
        .is_some_and(|rejection| matches!(rejection, SessionRejection::NamesTaken { .. }))
}

/// The refusal of a run whose names were all taken, the last being `harp`.
fn names_taken(harp: &str) -> CoreError {
    let rejection = SessionRejection::NamesTaken {
        harp: harp.to_string(),
    };
    CoreError::Refused {
        code: rejection.code().to_string(),
        args: BTreeMap::new(),
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

/// The posture a run claims, as its plan says, and what the runtime it is
/// launched on enforces (owner rulings 59, 109), for the run to judge.
fn posture_of(plan: &library_application::DryRun) -> runs_application::PostureEvidence {
    let runtime = plan.axes.1.clone();
    let enforced = run_ports::enforced(&runtime);
    runs_application::PostureEvidence {
        runtime,
        network: plan.posture.network.clone(),
        sandbox: plan.posture.sandbox.clone(),
        enforced_network: enforced.network,
        enforced_sandbox: enforced.sandbox,
    }
}
