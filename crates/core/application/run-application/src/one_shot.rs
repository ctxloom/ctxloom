//! The one-shot run: a prompt, one turn, the answer.
//!
//! In one process, in order: the library plans the run (the agent or profile,
//! the assembled context, the engine, the startup findings); the run's own
//! lock is taken (rulings 86, 92) and the run enqueued, its pre-launch checks
//! judged by the run itself; the project is told, a name claimed (never one
//! whose directory exists, defect 3) and the session minted; the session's
//! lock is taken for the run's life (defect 1); the launcher, caught up on
//! the store, finds it owed a runner, drives the engine for one turn (its
//! hooks deliver to the user's daemon, to the session the engine is told by
//! name, `CTXLOOM_SESSION`: no run serves a listener of its own, slice 4 U7)
//! and lets the run's lock go once the runner is started; the session is
//! told its incarnation ended (ruling 85); the run
//! is checked to have ended, and the answer is returned. Nothing is written
//! into the session's directory but its home (ruling 87a: the events are the
//! record). A plan that is refused, or a session that cannot be made, has
//! started no engine.

use std::collections::BTreeMap;
use std::sync::Arc;

use coded_diagnostic::Diagnostic;

use angzarr_embedded::v1::{Cover, Uuid};
use angzarr_embedded::{Catalog, CommandBookExt, Failure, Payload, Processed, ReadStore, Store};
use library_application::{AppError as PlanError, Flags, Notice, Outcome, Previewed, RunRequest};
use ports::{Names, Project, ProjectIdentity};
use run_launcher::{Launched, Launcher, Order, Orders, Outcome as Turn};
use run_ports::{Edge, LoginLinks, RunnerLauncher, SessionRecords};
use runs_application::{
    AppError as CoreError, EnqueueError, EnqueueRequest, Enqueued, Enqueuer, Evidence,
};
use sessions::{
    ClaimName, MintSession, NameClaimCommand, NameClaimEvent, Origin, ReleaseName, ResumeSession,
    SessionCommand, SessionEvent, SessionName, SessionRejection, SessionState, NAMES_DOMAIN,
    SESSION_DOMAIN,
};

use crate::credential::{credential_evidence, holds, share_login};
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

impl Answer {
    /// The reply as a primary may hold it where no human reads it: one
    /// escaped `<message>` element (owner ruling 2026-11-20), the same
    /// containment a hook-delivered message has. The reply is trimmed.
    pub fn contained(&self) -> String {
        setup_application::contained_reply(&self.session, &self.harp, self.answer.trim_end())
    }
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
/// rulings 78, 78b), the user's own settings files the session home was not
/// started from, by path (owner ruling 169), and a worktree asked for that
/// could not be had.
fn said_permits(launched: &Launched, notices: &mut Vec<Notice>) {
    notices.extend(
        launched
            .ignored
            .iter()
            .map(crate::credential::ignored_permit),
    );
    notices.extend(
        launched
            .left_out
            .iter()
            .map(|left_out| Notice::UserSettingsLeftOut {
                engine: left_out.engine.clone(),
                path: left_out.path.clone(),
            }),
    );
    // A worktree asked for and not had: the engine worked in the project.
    notices.extend(
        launched
            .workspace_warning
            .iter()
            .map(|reason| Notice::WorkspaceUnavailable {
                reason: reason.clone(),
            }),
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
    /// The harp the project was named with at its resolution (slice 2 M1),
    /// before any session of it is minted: a session's name, and so its
    /// stable id, is this and its own harp.
    pub project_harp: String,
    pub output_root: String,
    /// The directory of the running ctxloom binary, which leads an
    /// engine's `PATH` (the terminal's and a child's) so a hook's bare
    /// program (the binary's file name, owner ruling 163, which the
    /// composition root tells the engines) is it; empty when it could not be
    /// found.
    pub program_dir: String,
    /// The user's own home, as the composition root resolved it once (the
    /// platform's home variable: `HOME`, `USERPROFILE` on Windows); none when
    /// unset. Her login is found under it, and `session_home: host` gives it
    /// to the engine.
    pub user_home: Option<String>,
    /// The project's checkouts directory under ctxloom's home
    /// (`isolation_ports::worktrees_root`); each session's checkouts are
    /// `<worktrees>/<harp>`. Empty: none, and a worktree asked for is
    /// unavailable.
    pub worktrees: String,
}

/// The run use cases over their ports.
pub struct Runs<K: Core, L: RunnerLauncher, R: SessionRecords> {
    pub(crate) core: K,
    pub(crate) planner: Box<dyn Planner>,
    pub(crate) identity: Box<dyn ProjectIdentity>,
    pub(crate) names: Box<dyn Names>,
    pub(crate) enqueuer: Enqueuer<K::Edge>,
    pub(crate) launcher: Launcher<K::Edge, L, R>,
    pub(crate) dirs: Box<dyn SessionRecords>,
    pub(crate) orders: Arc<Orders>,
    pub(crate) place: Place,
    pub(crate) env: BTreeMap<String, String>,
    pub(crate) links: Box<dyn LoginLinks>,
    pub(crate) os: String,
    pub(crate) locks: Box<dyn session_ports::SessionLock>,
    pub(crate) run_locks: Arc<dyn run_ports::RunLocks>,
    pub(crate) git_user: Option<run_ports::GitUser>,
    /// Told once the run is placed (U4: the daemon that hosts a one-shot
    /// answers its client then); none, nobody is told.
    pub(crate) told: Option<Telling>,
    /// The run's own lock, held by its FRONTEND at this path since before
    /// it asked (the start-report race, U5): journaled as the run's
    /// launcher lock instead of one of the use cases' own, while it reads
    /// held; none, they take their own.
    pub(crate) frontend_lock: Option<String>,
}

/// What is told where each run was placed ([`Runs::telling`]).
pub type Telling = Box<dyn FnMut(&Placement) + Send>;

/// Where a run was placed, once its run is enqueued, its session made and
/// its order placed, before its engine is launched: what the daemon that
/// hosts it tells its client (U4).
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Placement {
    /// The run's root, as text.
    pub run: String,
    /// The session's stable id.
    pub session: String,
    /// The session's resource name (`projects/{project}/sessions/{harp}`).
    pub name: String,
    pub harp: String,
    pub engine: String,
    /// The agent the run was planned for (the plan's, else the default
    /// agent it took): what an ask of its engine names (step 8, D7).
    pub agent: String,
    /// Its engine's `approval_timeout`, as its plan read it: how long an
    /// ask of its engine waits (step 8, D7).
    pub approval_timeout: std::time::Duration,
    /// The human's terminal session: its asks are hers to answer at her
    /// terminal (S28), never routed.
    pub interactive: bool,
}

/// The agent `plan` is for: the one it was asked for, else the default
/// agent it took; none, empty.
fn planned_agent(plan: &library_application::DryRun) -> String {
    [&plan.agent, &plan.settings_agent]
        .into_iter()
        .find(|a| !a.is_empty())
        .cloned()
        .unwrap_or_default()
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
    /// The workspace axis it was minted on (none recorded: `none`).
    workspace: String,
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
pub(crate) fn settings_of(
    plan: &library_application::DryRun,
    engine: &str,
) -> run_ports::EngineSettings {
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
        // Read with the plan, once per launch and re-host (T21, OD-41).
        approval_timeout: drive.approval_timeout,
    }
}

/// Everything [`Runs`] is made of.
pub struct Parts<K: Core, L: RunnerLauncher, R: SessionRecords> {
    pub core: K,
    pub planner: Box<dyn Planner>,
    pub identity: Box<dyn ProjectIdentity>,
    pub names: Box<dyn Names>,
    pub enqueuer: Enqueuer<K::Edge>,
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

impl<K, L, R> Runs<K, L, R>
where
    K: Core,
    L: RunnerLauncher,
    R: SessionRecords,
{
    pub fn new(parts: Parts<K, L, R>) -> Runs<K, L, R> {
        Runs {
            core: parts.core,
            planner: parts.planner,
            identity: parts.identity,
            names: parts.names,
            enqueuer: parts.enqueuer,
            launcher: parts.launcher,
            dirs: parts.dirs,
            orders: parts.orders,
            place: parts.place,
            env: parts.env,
            links: parts.links,
            os: parts.os,
            locks: parts.locks,
            run_locks: parts.run_locks,
            git_user: parts.git_user,
            told: None,
            frontend_lock: None,
        }
    }

    /// Journal `lock` (the path of the run lock its
    /// FRONTEND took before it asked, U5's start-report race) as the run's
    /// launcher lock, instead of taking one of their own, when it reads
    /// held; one that does not (none, or an older frontend's) is passed
    /// over and their own is taken, as before.
    pub fn held_by_frontend(&mut self, lock: Option<String>) {
        self.frontend_lock = lock.filter(|path| !path.is_empty());
    }

    /// The use cases, telling `told` where each run was placed, once it is
    /// (U4: a hosted one-shot's daemon answers its client then).
    #[must_use]
    pub fn telling(mut self, told: Telling) -> Runs<K, L, R> {
        self.told = Some(told);
        self
    }

    /// The core, every other part let go: the enqueuer's and the launcher's
    /// edges dropped with them, so what reaches the core's
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
    /// one (owner ruling 115): planned on the session's own engine and
    /// workspace unless `--engine` or `--workspace` names one (the session's
    /// aggregate refuses another).
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
        // A resume works where its session worked (owner ruling
        // 2026-11-12); another `--workspace` is the session's to refuse.
        if request.run.workspace.is_empty() {
            request.run.workspace = resume.workspace.clone();
        }
        Ok((request, Some(resume)))
    }

    /// The session named `harp`, if it can be resumed as far as a read says:
    /// a name no session holds is `SessionUnknown`, a claimed name whose
    /// stream holds nothing (`session remove` destroyed it, its claim kept,
    /// ruling 94c) `SessionIsDestroyed`, and a session whose lock another
    /// process holds `SessionRunning`. Runs other processes left are swept
    /// first (the launcher's step: an owner found dead ends its incarnation,
    /// ruling 93), so a crashed session's open incarnation does not keep it.
    async fn resumable(&mut self, harp: &str) -> Result<Resume, RunError> {
        let refuse = RunError::Resume;
        self.launch().await?;
        self.identity.for_write(None).map_err(RunError::Project)?;
        let edge = self.core.edge();
        let failed = |e: &dyn std::fmt::Display| {
            RunError::Session(CoreError::Failed {
                reason: e.to_string(),
            })
        };
        let mut holder = None;
        for any in event_bodies(
            &edge
                .events(&self.claim_cover(harp).map_err(no_name)?, 0)
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
            workspace: state.workspace.unwrap_or_else(|| "none".to_string()),
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
        // A purge's tombstone closes the stream: it folds from after it.
        for page in run_ports::since_closed(&pages) {
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

    /// Owe `resume`'s session its next incarnation on `engine` in
    /// `workspace` (the aggregate's `ResumeSession`, which refuses a session
    /// running, one with no native session, another engine or another
    /// workspace): the incarnation owed.
    async fn owe(&self, resume: &Resume, engine: &str, workspace: &str) -> Result<u32, RunError> {
        let command: SessionCommand = ResumeSession {
            harp: resume.harp.clone(),
            engine: engine.to_string(),
            workspace: workspace.to_string(),
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
            engine_named: true,
            ..request.run.clone()
        };
        self.planner.plan(flags, &run)
    }

    /// Everything a run does before its engine is launched (owner rulings
    /// 73f, 73g, in this order): the run's lock taken and the run enqueued,
    /// its pre-launch checks judged by the run itself; the project told, a
    /// name claimed, the session minted and its lock taken; the order placed
    /// and the primary handed her login. A refused run has made nothing (its
    /// run lock let go); a failure after the enqueue ends the run it enqueued
    /// and its session's incarnation.
    async fn ready(
        &mut self,
        previewed: Previewed,
        request: &OneShot,
        resume: Option<Resume>,
        interactive: bool,
        notices: &mut Vec<Notice>,
    ) -> Result<Started, RunError> {
        let engine = chosen_engine(&previewed.plan, request);
        let settings = settings_of(&previewed.plan, &engine);
        let axis = run_launcher::Axis::of(&previewed.plan.axes.0);
        let (session, incarnation, chosen) =
            self.placing(resume.as_ref(), &engine, axis.word()).await?;
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
        let made = match (&resume, chosen) {
            (Some(resume), _) => Ok(resume.harp.clone()),
            (None, Some(chosen)) => {
                self.make(&session, chosen, &engine, axis, interactive)
                    .await
            }
            (None, None) => Err(RunError::Session(CoreError::Failed {
                reason: "no name was chosen".to_string(),
            })),
        };
        let harp = match made {
            Ok(harp) => harp,
            Err(error) => {
                return Err(self
                    .abandon(&enqueued.run, &session, incarnation, false, error)
                    .await)
            }
        };
        // What its plan draws on, recorded on its session (step 10c U13).
        self.record_loadout(&session, incarnation, &previewed.plan)
            .await;
        let lock = match self.locks.hold(&harp) {
            Ok(lock) => lock,
            Err(lock) => {
                let error = RunError::Lock(lock);
                return Err(self
                    .abandon(&enqueued.run, &session, incarnation, true, error)
                    .await);
            }
        };
        let agent = planned_agent(&previewed.plan);
        let started = self.start(
            previewed,
            request,
            interactive,
            Placed {
                session,
                harp,
                incarnation,
                resume: resume.map(|resume| resume.native_id),
            },
            &enqueued,
            lock,
        );
        let placement = Placement {
            run: text(&started.run),
            session: started.session.clone(),
            name: self
                .name_of(&started.harp)
                .map(|name| name.to_string())
                .unwrap_or_default(),
            harp: started.harp.clone(),
            engine: started.engine.clone(),
            agent,
            approval_timeout: settings.approval_timeout,
            interactive,
        };
        if let Some(told) = self.told.as_mut() {
            told(&placement);
        }
        // The primary is handed her login before her engine starts.
        if interactive {
            notices.extend(self.sign_in(&settings, &started));
        }
        Ok(started)
    }

    /// What the primary's terminal session is told of how it is signed in,
    /// having been handed her login where it is shared. The engine is asked
    /// first (owner ruling 168's seam): where it advises (claude on Windows
    /// with no token, which is token-only there, owner ruling 2026-11-12),
    /// its notice is what she is told, and no login is shared.
    fn sign_in(&mut self, settings: &run_ports::EngineSettings, started: &Started) -> Vec<Notice> {
        let Some(credentials) = self.launcher.credentials(&started.engine) else {
            return Vec::new();
        };
        let policy = settings.credentials(credentials);
        let advice = self.launcher.advise(
            &started.engine,
            &run_ports::Planned {
                os: self.os.clone(),
                interactive: true,
                credential_set: holds(&self.env, &policy.primary_env),
                ..run_ports::Planned::default()
            },
        );
        if !advice.is_empty() {
            return advice.into_iter().map(Notice::Engine).collect();
        }
        share_login(
            &mut *self.links,
            &policy,
            &self.env,
            self.place.user_home.as_deref(),
            &self.os,
            &started.harp,
        )
        .into_iter()
        .collect()
    }

    /// Launch what is owed, let go of the engine's checkout, then tell the
    /// session its incarnation ended
    /// (ruling 85). This run's launch, its
    /// run's end checked. The session's lock is let go by the caller, after.
    async fn drive(
        &mut self,
        started: &Started,
        notices: &mut Vec<Notice>,
    ) -> Result<Launched, RunError> {
        let launched = self.launch().await?;
        let ours = ours(launched, &started.session).ok_or_else(|| not_ended(&started.session))?;
        // The engine is over: its checkout is let
        // go while the session's lock is still held (owner ruling
        // 2026-11-12, Q2), removed when clean, else left and said.
        if let Some(checkout) = &ours.checkout {
            if let isolation_ports::Removal::Left(reason) =
                self.launcher.release(checkout, &ours.checkout_files)
            {
                notices.push(Notice::CheckoutLeft {
                    path: checkout.path.clone(),
                    branch: checkout.branch.clone(),
                    reason,
                });
            }
        }
        let cause = match ours.outcome {
            Turn::NotStarted { .. }
            | Turn::TooLong(_)
            | Turn::BatchArguments(_)
            | Turn::ProgramNotFound(_) => LAUNCH_FAILED,
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
        let started = self
            .ready(previewed, request, resume, true, notices)
            .await?;
        let launched = self.drive(&started, notices).await?;
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
            Turn::TooLong(refusal) => Err(RunError::TooLong(Box::new(refusal))),
            Turn::BatchArguments(refusal) => Err(RunError::BatchArguments(Box::new(refusal))),
            Turn::ProgramNotFound(missing) => Err(RunError::ProgramNotFound(Box::new(missing))),
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
            // The library says a one-shot's findings beside the refusal.
            fatal: Vec::new(),
            untrusted_settings: previewed.plan.untrusted.clone(),
        };
        // The run's own lock, taken before its enqueue is visible (ruling 92):
        // no other process ever sees it owed a runner with its lock free.
        // A frontend's, held since before it asked (U5): journaled, and
        // let go by the frontend once its start report is recorded.
        let frontend = self
            .frontend_lock
            .take()
            .filter(|path| self.run_locks.probe(path) == run_ports::LockProbe::Held);
        let (launcher_lock, own) = match frontend {
            Some(path) => (path, None),
            None => {
                let run = text(&runs::run_root(session, incarnation));
                let lock = self.run_locks.hold_run(&run).map_err(RunError::Lock)?;
                (lock.path().to_string(), Some(lock))
            }
        };
        let request = EnqueueRequest {
            session: session.to_string(),
            incarnation,
            mode,
            evidence,
            launcher_lock,
            // What a one-shot is given, as a hosted child's launch journals
            // it (owner ruling 2026-11-20); the human's terminal session is
            // her own, and records none.
            grant: (!interactive).then(|| {
                crate::hosted::grant_of(
                    &previewed.plan.surfaces,
                    previewed.plan.permissions.as_deref(),
                )
            }),
        };
        let enqueued = self.enqueuer.enqueue(request).await;
        if let (true, Some(lock)) = (enqueued.is_ok(), own) {
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
        chosen: Chosen,
        engine: &str,
        axis: run_launcher::Axis,
        interactive: bool,
    ) -> Result<String, RunError> {
        let project = self.identity.for_write(None).map_err(RunError::Project)?;
        self.mint(&project, session, chosen, engine, axis, interactive)
            .await
    }

    /// Where the run is placed before it is enqueued: its session's id, its
    /// incarnation, and a new session's harp. A resume owes its session the
    /// next incarnation first (owner ruling 115), so a refused resume has
    /// burned no run stream; a new session's stable id is the UUIDv5 of its
    /// name (slice 2 B3), so its harp is chosen now and claimed after the
    /// enqueue, with its first incarnation.
    async fn placing(
        &mut self,
        resume: Option<&Resume>,
        engine: &str,
        workspace: &str,
    ) -> Result<(String, u32, Option<Chosen>), RunError> {
        if let Some(resume) = resume {
            return Ok((
                resume.session.clone(),
                self.owe(resume, engine, workspace).await?,
                None,
            ));
        }
        let chosen = self.choose().await?;
        let session = self.name_of(&chosen.harp).map_err(no_name)?.uid();
        Ok((session, 1, Some(chosen)))
    }

    /// The claim stream of the name `harp`: its name's root (slice 2 B3), the
    /// session's own.
    fn claim_cover(&self, harp: &str) -> Result<Cover, SessionRejection> {
        Ok(Cover {
            domain: NAMES_DOMAIN.to_string(),
            root: Some(self.name_of(harp)?.root()),
            edition: None,
        })
    }

    /// The resource name of the session `harp` of this run's project (slice
    /// 2 B3): its project's harp, named at the project's resolution, and its
    /// own.
    fn name_of(&self, harp: &str) -> Result<SessionName, SessionRejection> {
        SessionName::new(&self.place.project_harp, harp)
    }

    /// Whether `harp`'s claim stream holds no claim now.
    async fn unclaimed(&self, harp: &str) -> Result<bool, RunError> {
        let pages = self
            .core
            .edge()
            .events(&self.claim_cover(harp).map_err(no_name)?, 0)
            .await
            .map_err(|e| {
                RunError::Session(CoreError::Failed {
                    reason: e.to_string(),
                })
            })?;
        let mut held = false;
        for any in event_bodies(&pages) {
            match NameClaimEvent::from_any(any) {
                Ok(NameClaimEvent::Claimed(_)) => held = true,
                Ok(NameClaimEvent::Released(_)) => held = false,
                Err(_) => {}
            }
        }
        Ok(!held)
    }

    /// The harp a new session is minted on, chosen before its run is
    /// enqueued (its stable id is its name's): the first fresh harp whose
    /// directory is not there (defect 3) and whose name nobody holds; else
    /// the last tried, marked taken, so the run it is enqueued for ends
    /// refused `NamesTaken` and claims nothing.
    async fn choose(&mut self) -> Result<Chosen, RunError> {
        let mut last = None;
        for _ in 0..NAME_TRIES {
            let harp = self.names.project_id();
            if !self.dirs.exists(&harp) && self.unclaimed(&harp).await? {
                return Ok(Chosen { harp, free: true });
            }
            last = Some(harp);
        }
        Ok(Chosen {
            harp: last.unwrap_or_default(),
            free: false,
        })
    }

    /// End the enqueued `run` of `session` that `error` stopped before its
    /// engine was launched (owner ruling 73g): a `RecordLaunchFailed`, so no run is
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

    /// The order placed for the run enqueued and its session minted.
    #[allow(clippy::too_many_arguments)]
    fn start(
        &mut self,
        previewed: Previewed,
        request: &OneShot,
        interactive: bool,
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
        let workspace = self.workspace(&plan, &harp);
        // The engine's environment names its run and session (the terminal
        // session's and a one-shot child's alike, ruling 51), and the
        // caller's own hook variables, from a session it was started inside,
        // are replaced by this run's, never passed on: no endpoint, bearer
        // or certificate (slice 4 U7), so its hooks deliver to the daemon.
        let mut env = self.env.clone();
        // What the run itself sets for the engine, which no permit replaces
        // (ruling 78b): HOME, which the engine's adapter makes the session
        // home (ruling 112; ctxloom's own is CTXLOOM_DAEMON_ROOT, which its hook
        // processes' wiring reads first), and the PATH below; the CTXLOOM_*
        // names are the run's whatever this says.
        let mut owned = vec!["HOME".to_string()];
        // The running binary's directory leads the engine's PATH, so a hook's
        // bare program (this binary's file name, ruling 163) is this binary
        // and not another one the user has; the inherited PATH follows it,
        // joined as the platform joins it (`;` on Windows). The caller's
        // names are keyed as the platform compares them (`PATH` whatever its
        // spelling on Windows), by the composition root.
        if !self.place.program_dir.is_empty() {
            let path = run_ports::search_path_led_by(
                &self.os,
                &self.place.program_dir,
                env.get("PATH").map(String::as_str),
            );
            env.insert("PATH".to_string(), path);
            owned.push("PATH".to_string());
        }
        let run_text = text(&enqueued.run);
        // The engine's environment is the only thing a hook process is
        // given: the session by its resource name (s3-Q3), whose harp a
        // statusline shows and whose UUIDv5 a hook addresses its facts to.
        let name = self
            .name_of(&harp)
            .map(|name| name.to_string())
            .unwrap_or_default();
        run_ports::name_the_run(&mut env, &run_text, &name);
        self.orders.place(
            &session,
            incarnation,
            Order {
                harp: harp.clone(),
                engine: engine.clone(),
                context: plan.context,
                prompt: plan.prompt,
                env,
                user_home: self.place.user_home.clone(),
                cwd: Some(self.place.project_dir.clone()),
                workspace,
                interactive,
                model: plan.model,
                effort: plan.effort,
                permissions: plan.permissions,
                settings,
                owned,
                git_user: self.git_user.clone(),
                resume,
                surfaces: run_ports::LaunchSurfaces(plan.surfaces),
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

    /// What the run asks of the workspace axis: the axis the plan settled
    /// on (`axes.0`: the flag, else the config's default), the session's
    /// checkouts directory under the project's, and the agent the checkout
    /// is for (the plan's, else the default agent it took). The launcher
    /// places the engine.
    fn workspace(&self, plan: &library_application::DryRun, harp: &str) -> run_launcher::Workspace {
        let agent = planned_agent(plan);
        run_launcher::Workspace {
            axis: run_launcher::Axis::of(&plan.axes.0),
            work_dir: if self.place.worktrees.is_empty() {
                String::new()
            } else {
                format!("{}/{harp}", self.place.worktrees)
            },
            agent,
        }
    }

    /// Launch what is owed, not yet recorded.
    async fn launch(&mut self) -> Result<Vec<Launched>, RunError> {
        self.launcher
            .step(self.core.store(), self.core.catalog())
            .await
            .map_err(RunError::Launch)
    }

    /// The one-shot: one turn, and the reply the child's stream gave.
    async fn run(
        &mut self,
        previewed: Previewed,
        request: &OneShot,
        resume: Option<Resume>,
        notices: &mut Vec<Notice>,
    ) -> Result<Answer, RunError> {
        let started = self
            .ready(previewed, request, resume, false, notices)
            .await?;
        let ours = self.drive(&started, notices).await?;
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
            Turn::TooLong(refusal) => Err(RunError::TooLong(Box::new(refusal))),
            Turn::BatchArguments(refusal) => Err(RunError::BatchArguments(Box::new(refusal))),
            Turn::ProgramNotFound(missing) => Err(RunError::ProgramNotFound(Box::new(missing))),
            Turn::Ended { .. } => Err(RunError::Turn {
                class: run_ports::FailureClass::EngineError,
                detail: "a turn ended as a session".to_string(),
            }),
        }
    }

    /// Claim the chosen name and mint the session on it: its harp. A name
    /// taken (by its directory, or a claim another run made since it was
    /// chosen: the claim refused `NamesTaken`, read by the refusal's type) is
    /// the run's refusal. A mint refused after its name was claimed gives
    /// the name back (review finding 7), best effort: the mint's refusal is
    /// the error.
    async fn mint(
        &mut self,
        project: &Project,
        session: &str,
        chosen: Chosen,
        engine: &str,
        axis: run_launcher::Axis,
        interactive: bool,
    ) -> Result<String, RunError> {
        let edge = self.core.edge();
        let Chosen { harp, free } = chosen;
        if !free {
            return Err(RunError::Session(names_taken(&harp)));
        }
        let claim = NameClaimCommand::from(ClaimName {
            project_id: project.id.clone(),
            harp: harp.clone(),
            session: session.to_string(),
        });
        let cover = self.claim_cover(&harp).map_err(no_name)?;
        match edge
            .handle(angzarr_embedded::v1::CommandBook::command_of(
                cover.clone(),
                &claim,
            ))
            .await
        {
            Ok(_) => {
                let minted = self
                    .mint_on(project, engine, session, &harp, axis, interactive)
                    .await;
                if minted.is_err() {
                    let release = NameClaimCommand::from(ReleaseName {
                        session: session.to_string(),
                    });
                    let _ = edge
                        .handle(angzarr_embedded::v1::CommandBook::command_of(
                            cover, &release,
                        ))
                        .await;
                }
                minted.map(|()| harp)
            }
            Err(Failure::Rejected(rejected)) if refused_taken(&rejected) => {
                Err(RunError::Session(names_taken(&harp)))
            }
            Err(failure) => Err(RunError::Session(CoreError::from_failure(failure))),
        }
    }

    async fn mint_on(
        &self,
        project: &Project,
        engine: &str,
        session: &str,
        harp: &str,
        axis: run_launcher::Axis,
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
            owner: None,
            // The axis is a session trait: a resume works in the same place.
            workspace: Some(axis.word().to_string()),
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
        self.core
            .edge()
            .handle(angzarr_embedded::v1::CommandBook::command_of(cover, &mint))
            .await
            .map(drop)
            .map_err(|failure| RunError::Session(CoreError::from_failure(failure)))
    }

    /// The loadout of `plan` recorded as the incarnation `incarnation`'s of
    /// the session `session` (step 10c U13; keyed by both, so it is
    /// recorded once). Best effort: one not recorded leaves the run as it
    /// is, and a change is told the session at no hook.
    async fn record_loadout(
        &self,
        session: &str,
        incarnation: u32,
        plan: &library_application::DryRun,
    ) {
        let Ok(id) = uuid::Uuid::parse_str(session) else {
            return;
        };
        let cover = Cover {
            domain: SESSION_DOMAIN.to_string(),
            root: Some(Uuid::from_bytes(*id.as_bytes())),
            edition: None,
        };
        let agent = planned_agent(plan);
        let fact = sessions::RecordLoadout {
            loadout: crate::loadout_of_plan(&agent, &plan.profiles, &plan.fragments),
            incarnation,
            covered: Vec::new(),
        };
        let key = sessions::loadout_key(session, incarnation);
        let _ = self
            .core
            .edge()
            .handle(sessions::fact_book(cover, &fact, &key))
            .await;
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

/// A name that is no session's (its project's harp, or its own, no harp),
/// as the run's error.
fn no_name(rejection: SessionRejection) -> RunError {
    RunError::Session(CoreError::Refused {
        code: rejection.code().to_string(),
        args: BTreeMap::new(),
    })
}

/// A new session's harp, chosen before its run is enqueued: `free` when
/// no directory or claim held it then.
struct Chosen {
    harp: String,
    free: bool,
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
