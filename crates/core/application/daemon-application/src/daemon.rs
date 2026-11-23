//! The launcher itself: the sweep at its start, then one round at a time.

use std::collections::{BTreeMap, BTreeSet};
use std::sync::Arc;

use angzarr_embedded::v1::{Cover, Uuid};
use angzarr_embedded::{Catalog, CatchUp, Failure, Processed, ReadStore, Store, Watch};
use daemon_board::{run_text, Boards};
use pending_launches::{PendingLaunch, PendingModel, StartedRun};
use pending_messages::PendingMessages;
use run_ports::{
    ChildControl, ChildMessage, ChildObservation, Edge, FailureClass, HeldLock, RunLocks,
    RunnerLauncher, TurnOutcome,
};
use runs::{RunMode, TurnUrgency};
use runs_application::{EnqueueError, EnqueueRequest, Enqueuer, Facts, Identity, RunnerFact};
use session_index::SessionIndexes;
use session_policies::{plan, Fed, RunAt, Scene, Sent, Step};
use sessions::Urgency;

use crate::one_shot::{OneShots, STOP_SIGNAL};
use crate::ports::{
    ChildHomes, ChildPlans, ChildWorkspaces, Ears, HandOff, HandOffs, HostedEngine, Monotonic,
};
use run_launcher::{Released, Releases, Sweeper, Swept};

/// How long an ended child has to exit before its tree is stopped.
const END_GRACE: std::time::Duration = std::time::Duration::from_secs(5);

/// Why a hosted child's incarnation ends when its run was asked to stop
/// (S19's `session stop`): told to its owner as the run's end.
const STOPPED: &str = "stopped";

/// How long a draining daemon waits for a busy child, steered to end its
/// turn, before it hands the child over anyway, its turn in flight cut
/// (owner ruling OD-54: five minutes).
pub const HAND_OVER_PERIOD: std::time::Duration = std::time::Duration::from_secs(300);

/// Where the daemon is, as a child it hosts is told: ctxloom's home, the
/// directory of the running program (its engine's `PATH` leads with it, so
/// a hook's bare `ctxloom` resolves), the operating system's name (how that
/// `PATH` is joined) and the user's own home (what a `session_home: host`
/// engine is given). A child is told no listener: its hooks find the daemon
/// by its discovery file and present the user bearer (slice 4 U7).
#[derive(Clone, Debug)]
pub struct Place {
    pub home: String,
    pub program_dir: Option<String>,
    pub os: String,
    pub user_home: Option<String>,
}

/// The read models the daemon plans from, each the same fold its kernel
/// runs inline (S32): handed in by the root, which registered them.
#[derive(Clone, Default)]
pub struct Folds {
    pub board: Boards,
    pub pending: PendingMessages,
    pub index: SessionIndexes,
    /// The trusts given (step 8 U1): what `GetTrust` and `ListTrusts`
    /// read.
    pub trusts: trust_index::TrustIndexes,
    /// The permission asks (step 8 U4): what the approvals calls read.
    pub approvals: pending_approvals::PendingApprovals,
    /// Each session's timeline: what `ctxloom top`'s log reads.
    pub timelines: session_timeline::SessionTimelines,
    /// The library trees as last observed (step 10a U0): what a read
    /// reconciles against and answers each resource's etag from (U2).
    pub checkouts: library_checkout::LibraryCheckouts,
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
    /// Its session's lock, held for its life (U6): its session reads live,
    /// and no sweep takes it for lost while it is hosted.
    _session_lock: Option<HeldLock>,
    /// The checkout it works in, let go WIP-safe once it exits (s4-Q11).
    checkout: Option<Placed>,
    /// What an ask of its engine is recorded with (step 8 U4).
    asker: Asker,
}

/// What the daemon knows of a session it hosts that its engine's ask is
/// recorded with (step 8 U4, D10): its run, its engine, the agent it was
/// planned for and its engine's `approval_timeout`, read with its plan at
/// its launch (T21).
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Asker {
    /// The session's stable id.
    pub session: String,
    /// Its run's root, as text.
    pub run: String,
    pub engine: String,
    pub agent: String,
    pub approval_timeout: std::time::Duration,
}

impl Asker {
    /// The asker of the child at `at`, hosted on `engine`, its hand-off
    /// `handed` and its plan `planned`: its agent the one it was launched
    /// for, else the one its plan settled.
    fn of(at: &RunAt, engine: &str, handed: &HandOff, planned: &Planned) -> Asker {
        Asker {
            session: at.session.clone(),
            run: at.run(),
            engine: engine.to_string(),
            agent: [&handed.agent, &planned.agent]
                .into_iter()
                .find(|agent| !agent.is_empty())
                .cloned()
                .unwrap_or_default(),
            approval_timeout: planned.settings.approval_timeout,
        }
    }
}

/// A hosted child's checkout: where the launch placed it, what it wrote
/// there, and the workspaces that let it go.
struct Placed {
    checkout: isolation_ports::Checkout,
    files: Vec<String>,
    workspaces: Arc<dyn isolation_ports::Workspaces + Send + Sync>,
}

impl Placed {
    /// Let the checkout go WIP-safe (owner ruling 2026-11-12, Q2): the
    /// files the launch placed taken out, then a clean one removed (its
    /// branch with it when merged), one holding work left, never forced.
    fn release(&self) -> isolation_ports::Removal {
        self.workspaces
            .remove_files(&self.checkout.path, &self.files);
        self.workspaces
            .remove(&self.checkout.repo_dir, &self.checkout.path)
    }
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

/// The child a start hosts: its name, its engine, its project and the
/// engine's own session it resumes (none: a new conversation).
pub(crate) struct Child<'a> {
    harp: &'a str,
    engine: &'a str,
    project_dir: &'a str,
    resume: Option<String>,
}

/// What a child's spawn is told beside its plan.
struct Spawn<'a> {
    at: &'a RunAt,
    harp: &'a str,
    name: &'a str,
    project_dir: &'a str,
    resume: Option<String>,
    workspace: run_launcher::Workspace,
    workspaces: Option<&'a dyn isolation_ports::Workspaces>,
}

/// A child's plan: what it is hosted with, the workspace axis its agent
/// works on, and the agent it was planned for (its checkout's name).
struct Planned {
    hosted: run_application::HostedChild,
    axis: String,
    agent: String,
    /// What its plan draws on (step 10c U13): recorded on its session once
    /// its run is enqueued.
    loadout: sessions::SessionLoadout,
}

impl std::ops::Deref for Planned {
    type Target = run_application::HostedChild;

    fn deref(&self) -> &run_application::HostedChild {
        &self.hosted
    }
}

/// A child enqueued, to start: its engine, its plan, its hand-off and the
/// locks it holds.
struct Ready {
    engine: HostedEngine,
    child: Planned,
    handed: HandOff,
    run_lock: HeldLock,
    session_lock: HeldLock,
}

/// What a hosted child holds for its life.
struct Locks {
    run: HeldLock,
    session: Option<HeldLock>,
    checkout: Option<Placed>,
}

/// The release of a lost run's checkout (OD-19): only one under ctxloom's
/// own `worktrees` (a scratch checkout of a session's), never the project
/// or anything else; WIP-safe through the project's workspaces.
struct LostCheckouts {
    workspaces: Arc<dyn ChildWorkspaces>,
    root: std::path::PathBuf,
}

impl Releases for LostCheckouts {
    fn release(&self, swept: &Swept) -> Released {
        let path = std::path::Path::new(&swept.workspace);
        let ours = path.starts_with(&self.root) && path != self.root;
        if swept.workspace.is_empty() || swept.project_dir.is_empty() || !ours {
            return Released::Nothing;
        }
        // The project id is the checkouts directory's first segment under
        // the root: `<root>/<project-id>/<harp>/<agent>`.
        let project_id = path
            .strip_prefix(&self.root)
            .ok()
            .and_then(|rel| rel.components().next())
            .map(|first| first.as_os_str().to_string_lossy().into_owned())
            .unwrap_or_default();
        let workspaces = self.workspaces.of(&project_id, &swept.project_dir, None);
        match workspaces.remove(&swept.project_dir, &swept.workspace) {
            isolation_ports::Removal::Removed => Released::Removed(swept.workspace.clone()),
            isolation_ports::Removal::Left(why) => Released::Left {
                path: swept.workspace.clone(),
                why,
            },
        }
    }
}

/// The daemon's launcher over the one store's edge `E`.
pub struct Daemon<E: Edge> {
    pub(crate) edge: Arc<E>,
    engines: Vec<HostedEngine>,
    plans: Box<dyn ChildPlans>,
    hand_offs: Box<dyn HandOffs>,
    homes: Box<dyn ChildHomes>,
    ears: Box<dyn Ears>,
    pub(crate) run_locks: Arc<dyn RunLocks>,
    place: Place,
    pub(crate) say: Box<dyn FnMut(&str) + Send>,
    pub(crate) board: CatchUp<Boards>,
    pending: CatchUp<PendingMessages>,
    index: CatchUp<SessionIndexes>,
    trusts: CatchUp<trust_index::TrustIndexes>,
    approvals: CatchUp<pending_approvals::PendingApprovals>,
    timelines: CatchUp<session_timeline::SessionTimelines>,
    checkouts: CatchUp<library_checkout::LibraryCheckouts>,
    /// What each live session is owed (step 10c U13 rework), its
    /// positions in the store; none until [`Daemon::owing`].
    owed: Option<CatchUp<crate::owed::OwedContexts<E>>>,
    launches: CatchUp<PendingModel>,
    /// Each hosted child's environment, by its session's stable id: its
    /// launcher's, taken from the hand-off at its first start (S6, D-N45),
    /// held for its life, dropped at its end. Never the daemon's own.
    envs: BTreeMap<String, HandOff>,
    hosted: BTreeMap<String, Hosted>,
    done: BTreeSet<String>,
    /// Steps refused this time round: tried again once the store changes.
    refused: BTreeSet<String>,
    /// The steps whose failure was said already (once, not each round).
    pub(crate) complained: BTreeSet<String>,
    /// Where a child's program is found (`RunnerLauncher::find_program`:
    /// PATHEXT's on Windows); none, it is started as named.
    finder: Option<Arc<dyn RunnerLauncher>>,
    /// The one-shots it hosts (U4), shared with its API; none, it hosts
    /// none.
    one_shots: Option<Arc<OneShots>>,
    /// The sessions whose ctxloom MCP server watches them (the terminal
    /// wake), shared with its API's `WatchSession`: their signals are
    /// published each round, and a primary among them takes a wake; none,
    /// no session is watched.
    watches: Option<Arc<crate::SessionWatches>>,
    /// Where a child works (s4-Q11); none, every child in its project.
    workspaces: Option<Arc<dyn ChildWorkspaces>>,
    /// Whether it hands what it hosts over to the next daemon (OD-3
    /// extended, s4-Q4): asked to drain ([`Daemon::hand_over`]).
    handing_over: bool,
    /// The runs of the children it ended to hand them over: each one's
    /// incarnation ends `drained` at its exit.
    drained: BTreeSet<String>,
    /// The runs whose stop was asked (S19's `session stop`) and whose
    /// engine this daemon ended for it: their exit ends them `stopped`.
    stopping: BTreeSet<String>,
    /// The runs whose engine this daemon ended to restart it (step 10e
    /// Stage 2a): their exit ends them `restarted`. In memory only: a daemon
    /// that dies between the end and the exit finds the restart still owed
    /// in the session's stream, which is what it acts on.
    restarting: BTreeSet<String>,
    /// Its clock (OD-54), which times a hand-over's period; none, a busy
    /// child is waited for until its turn ends.
    clock: Option<Arc<dyn Monotonic>>,
    /// How long a hand-over waits for a busy child ([`HAND_OVER_PERIOD`]).
    hand_over_period: std::time::Duration,
    /// When its hand-over began, on its clock.
    hand_over_began: Option<std::time::Duration>,
    /// Whether the busy children were given the drain's guidance (once, at
    /// the hand-over's first round).
    guided_once: bool,
    /// The guidance given each busy child, by its run: its message's id.
    guided: BTreeMap<String, String>,
    /// The runs it adopted at its start (a daemon dying under a live TUI,
    /// owner 2026-11-14): another daemon's, their owner alive; their end
    /// path is this daemon's ([`crate::adopt`]).
    pub(crate) adopted: BTreeMap<String, crate::adopt::Adopted>,
    /// What delivers a judged run's session's hold at the start, before
    /// the sweep judges it (OD-59 (2), the named exception,
    /// [`crate::StartHolds`]); none, nothing is delivered.
    start_holds: Option<Arc<dyn crate::StartHolds>>,
}

/// The ports a [`Daemon`] is built over.
pub struct Ports<E: Edge> {
    pub edge: Arc<E>,
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

/// What a start-up flush did not deliver, as the log says it.
fn not_whole(flushed: &crate::HoldFlushed) -> String {
    let mut parts = Vec::new();
    if flushed.set_aside > 0 {
        parts.push(format!("{} set aside", flushed.set_aside));
    }
    if let Some(why) = &flushed.left {
        parts.push(format!("left held: {why}"));
    }
    parts.join("; ")
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

impl<E: Edge> Daemon<E> {
    pub fn new(ports: Ports<E>) -> Daemon<E> {
        let Ports {
            edge,
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
            trusts: trust_index::caught_up(folds.trusts),
            approvals: pending_approvals::caught_up(folds.approvals),
            timelines: session_timeline::caught_up(folds.timelines),
            checkouts: library_checkout::caught_up(folds.checkouts),
            owed: None,
            launches: pending_launches::catch_up(),
            envs: BTreeMap::new(),
            hosted: BTreeMap::new(),
            done: BTreeSet::new(),
            refused: BTreeSet::new(),
            complained: BTreeSet::new(),
            finder: None,
            one_shots: None,
            workspaces: None,
            handing_over: false,
            drained: BTreeSet::new(),
            stopping: BTreeSet::new(),
            restarting: BTreeSet::new(),
            clock: None,
            hand_over_period: HAND_OVER_PERIOD,
            hand_over_began: None,
            guided_once: false,
            guided: BTreeMap::new(),
            adopted: BTreeMap::new(),
            start_holds: None,
            watches: None,
        }
    }

    /// The daemon, publishing each watched session's signal to `watches`
    /// each round (the terminal wake), and planning a wake for a primary
    /// among them.
    pub fn watching(mut self, watches: Arc<crate::SessionWatches>) -> Daemon<E> {
        self.watches = Some(watches);
        self
    }

    /// Each watched session's signal, as the board and the pending
    /// messages stand now.
    fn publish_signals(&self) {
        let Some(watches) = self.watches.as_ref() else {
            return;
        };
        let board = self.board.projector().board();
        let pending = self.pending.projector().pending();
        for session in watches.watched() {
            let signal = crate::signal_of(&pending.pending(&session), board.session(&session));
            watches.publish(&session, signal);
        }
    }

    /// The sessions watched now (stable ids).
    pub(crate) fn wakeable(&self) -> BTreeSet<String> {
        self.watches
            .as_ref()
            .map(|watches| watches.watched())
            .unwrap_or_default()
    }

    /// The daemon, its start-up sweep delivering, through `holds`, the hold
    /// of each session whose run it judges, before it judges it (OD-59 (2):
    /// the ONE named exception to "the daemon never reads a hold").
    pub fn flushing_at_start(mut self, holds: Arc<dyn crate::StartHolds>) -> Daemon<E> {
        self.start_holds = Some(holds);
        self
    }

    /// The daemon, recording what each live session is owed as each round
    /// catches up (step 10c U13 rework), each session's trees told by
    /// `trees`.
    pub fn owing(mut self, trees: Arc<dyn crate::owed::SessionTrees>) -> Daemon<E> {
        let owed = crate::owed::OwedContexts::new(
            Arc::clone(&self.edge),
            self.index.projector().clone(),
            self.checkouts.projector().clone(),
            trees,
        );
        self.owed = Some(crate::owed::caught_up(owed));
        self
    }

    /// The daemon, timing a hand-over's period by `clock` (OD-54).
    pub fn clocked(mut self, clock: Arc<dyn Monotonic>) -> Daemon<E> {
        self.clock = Some(clock);
        self
    }

    /// The daemon, waiting `period` for a busy child before it hands it over
    /// anyway (default [`HAND_OVER_PERIOD`]; a test's is short).
    pub fn handing_over_within(mut self, period: std::time::Duration) -> Daemon<E> {
        self.hand_over_period = period;
        self
    }

    /// Wait `period` from the hand-over's start, from now on: the drain in
    /// progress honours a timeout changed while it runs (owner, 2026-11-15:
    /// the root reads `daemon.drain_timeout` again at each round).
    pub fn hand_over_within(&mut self, period: std::time::Duration) {
        self.hand_over_period = period;
    }

    /// How long a hand-over waits for a busy child (the period it was last
    /// told).
    pub fn hand_over_period(&self) -> std::time::Duration {
        self.hand_over_period
    }

    /// The daemon, placing each child where its agent's `workspace` says
    /// (s4-Q11) through `workspaces`, and letting a lost child's checkout
    /// go at its start's sweep (OD-19).
    pub fn placing(mut self, workspaces: Arc<dyn ChildWorkspaces>) -> Daemon<E> {
        self.workspaces = Some(workspaces);
        self
    }

    /// The daemon, hosting the one-shots of `one_shots` (U4): counted as
    /// what it hosts, stopped when their run's stream asks
    /// ([`Daemon::tick`]'s stop step), and ended with its children.
    pub fn hosting_one_shots(mut self, one_shots: Arc<OneShots>) -> Daemon<E> {
        self.one_shots = Some(one_shots);
        self
    }

    /// The daemon, finding each child's program through `finder` (the one
    /// launch's step 4, T10) before it is started.
    pub fn finding(mut self, finder: Arc<dyn RunnerLauncher>) -> Daemon<E> {
        self.finder = Some(finder);
        self
    }

    /// Hand what it hosts over to the next daemon (OD-54; `ctxloom shutdown`, a SIGTERM):
    /// from its next round on it starts no child and delivers no message
    /// but the drain's guidance, and each child it hosts with no turn in
    /// flight is ended, its incarnation ended `drained` and its hand-off
    /// kept, so the next daemon re-hosts it (cause `drain`) resuming its
    /// conversation. A busy child is steered with the guidance (OD-54) and
    /// ended at its turn's end, or, still busy once the hand-over's period
    /// has passed on its clock, ended anyway, its turn cut. Its one-shots
    /// finish their turn, or, still running once the period has passed,
    /// are stopped as [`Daemon::end_all`] stops one (U8c).
    pub fn hand_over(&mut self) {
        self.handing_over = true;
        self.hand_over_began = self.clock.as_ref().map(|clock| clock.now());
    }

    /// How many runs it hosts now: its children and its one-shots.
    pub fn hosting(&self) -> usize {
        self.hosted.len() + self.one_shots.as_ref().map_or(0, |o| o.hosting())
    }

    /// Whether a child it hosts has a turn in flight (its run holds a turn
    /// queued and not ended), as its board says.
    pub fn in_flight(&self) -> bool {
        let board = self.board.projector().board();
        self.hosted.values().any(|hosted| {
            board
                .run(&hosted.at.session, hosted.at.incarnation)
                .is_some_and(|run| !run.state.turns.is_empty())
        })
    }

    /// The one-shots it hosts, each as its session's stable id and its
    /// run's root (text), in run order.
    pub fn hosted_one_shots(&self) -> Vec<(String, String)> {
        self.one_shots
            .as_ref()
            .map(|one_shots| {
                one_shots
                    .live()
                    .into_iter()
                    .map(|placed| (placed.session, placed.run))
                    .collect()
            })
            .unwrap_or_default()
    }

    /// What each child it hosts asks with (step 8 U4), in run order.
    pub fn askers(&self) -> Vec<Asker> {
        self.hosted
            .values()
            .map(|hosted| hosted.asker.clone())
            .collect()
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
        // Its adopted runs are their end path's ([`crate::adopt`]), never
        // this sweep's.
        let adopted = self.adopted_runs();
        let theirs: Vec<StartedRun> = started
            .iter()
            .filter(|run| !self.hosted.values().any(|hosted| hosted.who.run == run.run))
            .filter(|run| !adopted.values().any(|a| a.run == run.run))
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

    /// The daemon's FIRST step (T18), before the root publishes it: every
    /// read model it plans and answers from caught up with the store's log
    /// (the board, the pending messages, the session index, the trust index), then every run
    /// the store holds caught up,
    /// and the ones whose owner is gone ended (the sweep every launcher
    /// runs: `RecordLaunchOrphaned` for one owed a runner, `RecordRunnerLost` for one
    /// started, its incarnation ended `orphaned`), before anything is
    /// hosted; every run the sweep leaves (its owner alive: a live TUI's
    /// frontend) is ADOPTED, its end path this daemon's (owner
    /// 2026-11-14, [`crate::adopt`]). Before the sweep judges a run, its
    /// session's hold is delivered (OD-59 (2), the ONE named exception to
    /// "the daemon never reads a hold", [`crate::StartHolds`]), so a TUI
    /// exit report held while no daemon served ends its run as it says.
    /// What it ended.
    pub async fn start_up<S: ReadStore + Store + Processed>(
        &mut self,
        store: &S,
        catalog: &Catalog,
    ) -> Result<Vec<Swept>, String> {
        // A fresh daemon serves nothing before its read models hold the
        // store's log (owner, s4-Q14 H3's condition): the board, the
        // pending messages and the session index the API reads are caught
        // up here, before the root publishes it; the first round's step
        // then finds them current.
        self.board
            .step(store, catalog)
            .await
            .map_err(|e| format!("the board's catch-up: {e:?}"))?;
        self.pending
            .step(store, catalog)
            .await
            .map_err(|e| format!("the pending messages' catch-up: {e:?}"))?;
        self.index
            .step(store, catalog)
            .await
            .map_err(|e| format!("the session index's catch-up: {e:?}"))?;
        self.trusts
            .step(store, catalog)
            .await
            .map_err(|e| format!("the trust index's catch-up: {e:?}"))?;
        self.approvals
            .step(store, catalog)
            .await
            .map_err(|e| format!("the pending approvals' catch-up: {e:?}"))?;
        self.timelines
            .step(store, catalog)
            .await
            .map_err(|e| format!("the session timelines' catch-up: {e:?}"))?;
        self.checkouts
            .step(store, catalog)
            .await
            .map_err(|e| format!("the library checkouts' catch-up: {e:?}"))?;
        self.sweep(store, catalog).await
    }

    async fn sweep<S: ReadStore + Store + Processed>(
        &mut self,
        store: &S,
        catalog: &Catalog,
    ) -> Result<Vec<Swept>, String> {
        let (owed, started) = self.caught_up_launches(store, catalog).await?;
        let holds = self.flush_the_judged_holds(&owed, &started).await;
        let swept = self.swept(&owed, &started).await?;
        for run in &swept {
            if let Some(flushed) = holds.get(&run.session).filter(|f| !f.whole()) {
                (self.say)(&format!(
                    "{}: ended {} without what its hold held: {}",
                    run.session,
                    match run.started {
                        true => "runner-lost",
                        false => "launch-orphaned",
                    },
                    not_whole(flushed)
                ));
            }
        }
        // What the sweep left is another process's, its owner alive: at the
        // start nothing is this daemon's own.
        let left = |run: &Uuid| !swept.iter().any(|s| &s.run == run);
        let owed: Vec<PendingLaunch> = owed.into_iter().filter(|l| left(&l.run)).collect();
        let started: Vec<StartedRun> = started.into_iter().filter(|r| left(&r.run)).collect();
        self.adopt(&owed, &started);
        Ok(swept)
    }

    /// THE ONE NAMED EXCEPTION to "the daemon never reads a hold" (OD-59
    /// (2), owner 2026-11-16, [`crate::StartHolds`]): before the start's
    /// sweep judges the runs `owed` and `started`, the hold of each one's
    /// session is delivered, as an up-to-date client's flush delivers it,
    /// so a TUI exit report held while no daemon served ends its run as it
    /// says (the sweep's re-read then finds the run ended and leaves it to
    /// the adoption's end path). Each session once; what each flush did, by
    /// session. Only the start's sweep calls it.
    async fn flush_the_judged_holds(
        &mut self,
        owed: &[PendingLaunch],
        started: &[StartedRun],
    ) -> BTreeMap<String, crate::HoldFlushed> {
        let mut flushed = BTreeMap::new();
        let Some(holds) = self.start_holds.clone() else {
            return flushed;
        };
        let sessions = owed
            .iter()
            .map(|launch| &launch.session)
            .chain(started.iter().map(|run| &run.session));
        for session in sessions {
            if flushed.contains_key(session) {
                continue;
            }
            let done = holds.flush(session).await;
            if done.delivered > 0 || !done.whole() {
                (self.say)(&format!(
                    "{session}: its hold delivered at the start, before its run is judged: {} \
                     delivered{}",
                    done.delivered,
                    match done.whole() {
                        true => String::new(),
                        false => format!("; {}", not_whole(&done)),
                    }
                ));
            }
            flushed.insert(session.clone(), done);
        }
        flushed
    }

    /// What lets a lost run's checkout go (OD-19): the project's workspaces,
    /// for a run that worked in a checkout of ctxloom's own (under its
    /// home's `worktrees`), never the project itself; none, nothing.
    pub(crate) fn releases(&self) -> Option<Arc<dyn Releases>> {
        let workspaces = Arc::clone(self.workspaces.as_ref()?);
        Some(Arc::new(LostCheckouts {
            workspaces,
            root: std::path::Path::new(&self.place.home)
                .join(".ctxloom")
                .join("worktrees"),
        }))
    }

    /// The runs owed a runner, and the runs started and not ended, as the
    /// store holds them now.
    pub(crate) async fn caught_up_launches<S: ReadStore + Store + Processed>(
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
    pub(crate) async fn swept(
        &mut self,
        owed: &[PendingLaunch],
        started: &[StartedRun],
    ) -> Result<Vec<Swept>, String> {
        let mut sweeper = Sweeper::new(Arc::clone(&self.edge), Arc::clone(&self.run_locks));
        if let Some(releases) = self.releases() {
            sweeper = sweeper.releasing(releases);
        }
        let swept = sweeper
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
            match &run.released {
                Released::Nothing => {}
                Released::Removed(path) => {
                    (self.say)(&format!("{}: its checkout {path} removed", run.session));
                }
                Released::Left { path, why } => {
                    (self.say)(&format!("{}: its checkout {path} left: {why}", run.session))
                }
            }
        }
        Ok(swept)
    }

    /// The hand-over's step (s4-Q4): each child it hosts that is bound to
    /// its engine's session (so the next daemon can resume it), has no
    /// turn in flight, as the board caught up says, and no guidance of its
    /// own still pending (OD-54), is ended, once; its exit ends its
    /// incarnation `drained`. Whether it ended any.
    fn end_the_idle(&mut self) -> bool {
        let board = self.board.projector().board();
        let idle: Vec<String> = self
            .hosted
            .iter()
            .filter(|(run, _)| !self.drained.contains(*run))
            .filter(|(run, _)| !self.guidance_pending(run))
            .filter(|(_, hosted)| {
                let at = &hosted.at;
                let bound = board
                    .session(&at.session)
                    .is_some_and(|session| !session.native_id.is_empty());
                let quiet = board
                    .run(&at.session, at.incarnation)
                    .is_none_or(|run| run.state.turns.is_empty());
                bound && quiet
            })
            .map(|(run, _)| run.clone())
            .collect();
        drop(board);
        for run in &idle {
            self.drained.insert(run.clone());
            if let Some(hosted) = self.hosted.get(run) {
                (self.say)(&format!(
                    "{}: no turn in flight: ended, to be handed over",
                    hosted.who.harp
                ));
                let _ = hosted.control.end(END_GRACE);
            }
        }
        !idle.is_empty()
    }

    /// Whether the guidance given the child of `run` is still pending (not
    /// yet a turn of its run).
    fn guidance_pending(&self, run: &str) -> bool {
        let (Some(id), Some(hosted)) = (self.guided.get(run), self.hosted.get(run)) else {
            return false;
        };
        self.pending
            .projector()
            .pending()
            .pending(&hosted.at.session)
            .iter()
            .any(|message| &message.message_id == id)
    }

    /// Whether `step` (a delivery) is to a run whose drain's guidance is
    /// still pending: the one delivery a draining daemon makes.
    fn guides(&self, step: &Step) -> bool {
        let Step::Send(sent) = step else {
            return false;
        };
        self.guided.keys().any(|run| {
            self.hosted.get(run).is_some_and(|hosted| {
                let cover = session_policies::run_cover(&hosted.at.session, hosted.at.incarnation);
                sent.book.cover.as_ref() == Some(&cover)
            }) && self.guidance_pending(run)
        })
    }

    /// The drain's guidance (OD-54), once, at the hand-over's first round:
    /// each hosted child with a turn in flight is sent the approved
    /// guidance as its session's own steer (the one delivery path), and the
    /// log says so at once. Whether it sent any.
    async fn guide_the_busy(&mut self) -> bool {
        if self.guided_once {
            return false;
        }
        self.guided_once = true;
        let board = self.board.projector().board();
        let mut guidance = Vec::new();
        for (run, hosted) in &self.hosted {
            let at = &hosted.at;
            let Some(session) = board.session(&at.session) else {
                continue;
            };
            let busy = board
                .run(&at.session, at.incarnation)
                .is_some_and(|run| !run.state.turns.is_empty());
            let child = sessions::SessionRef {
                project_id: session.project_id.clone(),
                session: at.session.clone(),
            };
            // A child it hosts takes every urgency over its engine's
            // stream (D15), whatever engine it is on.
            let supported = [Urgency::Inform, Urgency::Wake, Urgency::Steer];
            if let Some(keyed) = busy
                .then(|| session_policies::drain_guidance(&child, at.incarnation, &supported))
                .flatten()
            {
                guidance.push((run.clone(), hosted.who.harp.clone(), keyed));
            }
        }
        drop(board);
        let within = match self.hand_over_began {
            Some(_) => format!(
                "handed over within {}s, its turn cut",
                self.hand_over_period.as_secs()
            ),
            None => "handed over at its turn's end".to_string(),
        };
        let any = !guidance.is_empty();
        for (run, harp, keyed) in guidance {
            (self.say)(&format!(
                "{harp}: a turn in flight: steered to end it; {within}"
            ));
            let id = keyed.command.message_id.clone();
            if matches!(self.send(&Sent::from(keyed)).await, Taken::Done) {
                self.guided.insert(run, id);
            }
        }
        any
    }

    /// The hand-over's period (OD-54): once it has passed on the daemon's
    /// clock, each child still hosted and not yet ended is ended anyway, its
    /// turn in flight cut, its incarnation to end `drained`; and each
    /// one-shot still running is stopped (U8c, owner 2026-11-15), as the
    /// daemon's end stops one ([`STOP_SIGNAL`]). Whether it ended or
    /// stopped any.
    fn hand_over_the_overdue(&mut self) -> bool {
        let (Some(clock), Some(began)) = (&self.clock, self.hand_over_began) else {
            return false;
        };
        if clock.now().saturating_sub(began) < self.hand_over_period {
            return false;
        }
        let overdue: Vec<String> = self
            .hosted
            .keys()
            .filter(|run| !self.drained.contains(*run))
            .cloned()
            .collect();
        for run in &overdue {
            self.drained.insert(run.clone());
            if let Some(hosted) = self.hosted.get(run) {
                (self.say)(&format!(
                    "{}: still busy after {}s: its turn in flight cut, handed over",
                    hosted.who.harp,
                    self.hand_over_period.as_secs()
                ));
                let _ = hosted.control.end(END_GRACE);
            }
        }
        let stopped = self.stop_the_overdue_one_shots();
        !overdue.is_empty() || stopped
    }

    /// The restarts owed whose agent is Idle (step 10e Stage 2a): the
    /// session decided each is owed (`RestartOwed`); the hosting state says
    /// its turn has ended, or its engine has nothing in flight. Its engine is
    /// ended, once; its exit ends its incarnation `restarted`, and the plan
    /// re-hosts it for the cause `restart`, resuming its conversation. An
    /// agent parked behind its turn is passed over, and no other is held up.
    /// Whether it ended any (`caught_up`: the board was caught up this round).
    fn restart_the_due(&mut self, caught_up: bool) -> bool {
        // Only on a board that caught up, and never while handing over: a
        // draining daemon hands the child over, the restart still owed.
        if !caught_up || self.handing_over {
            return false;
        }
        let due = self.board.projector().board().restarts_due();
        let mut ended = false;
        for restart in due {
            let run = run_text(&restart.session, restart.incarnation);
            let Some(hosted) = self.hosted.get(&run) else {
                continue;
            };
            if !self.restarting.insert(run) {
                continue;
            }
            (self.say)(&format!(
                "{}: a restart owed ({:?}): ended, to be resumed",
                hosted.who.harp, restart.reason
            ));
            let _ = hosted.control.end(END_GRACE);
            ended = true;
        }
        ended
    }

    /// Each one-shot still running once the hand-over's period has passed
    /// stopped, once, and said. Whether it stopped any.
    fn stop_the_overdue_one_shots(&mut self) -> bool {
        let Some(one_shots) = self.one_shots.clone() else {
            return false;
        };
        let mut stopped = false;
        for placed in one_shots.live() {
            if one_shots.stop(&placed.run, STOP_SIGNAL) {
                (self.say)(&format!(
                    "{}: a one-shot still running after {}s: its engine stopped ({STOP_SIGNAL})",
                    placed.harp,
                    self.hand_over_period.as_secs()
                ));
                stopped = true;
            }
        }
        stopped
    }

    /// Catch up the read models a round's plan does not read: the index,
    /// the trusts, the asks, the timelines and the checkouts.
    async fn step_the_other_models<S: ReadStore + Store + Processed + Watch>(
        &mut self,
        store: &S,
        catalog: &Catalog,
    ) {
        let _ = self.index.step_if_changed(store, catalog).await;
        let _ = self.trusts.step_if_changed(store, catalog).await;
        let _ = self.approvals.step_if_changed(store, catalog).await;
        let _ = self.timelines.step_if_changed(store, catalog).await;
        let _ = self.checkouts.step_if_changed(store, catalog).await;
    }

    /// One round: hear the children, catch up, plan, act. Whether it did
    /// anything. While `draining` (or handing over), no child is started
    /// and no message delivered.
    pub async fn tick<S: ReadStore + Store + Processed + Watch>(
        &mut self,
        store: &S,
        catalog: &Catalog,
        draining: bool,
    ) -> bool {
        let draining = draining || self.handing_over;
        let mut busy = self.hear().await;
        busy |= self.stop_one_shots().await;
        busy |= self.end_the_adopted(store, catalog).await;
        let board = self.board.step_if_changed(store, catalog).await;
        let pending = self.pending.step_if_changed(store, catalog).await;
        self.step_the_other_models(store, catalog).await;
        // Once the index and the checkouts are caught up: what each live
        // session is owed, recorded on it, and the index caught up with it.
        if let Some(owed) = self.owed.as_mut() {
            if let Ok(Some(caught)) = owed.step_if_changed(store, catalog).await {
                for (cover, failure) in caught.failures() {
                    if self.complained.insert(format!("owed:{}", cover.domain)) {
                        (self.say)(&format!("an owed context not recorded: {failure:?}"));
                    }
                }
            }
            let _ = self.index.step_if_changed(store, catalog).await;
        }
        if any_handed(handed(&board), handed(&pending)) {
            self.refused.clear();
        }
        // What would wake each watched session now (the terminal wake).
        self.publish_signals();
        // Once the board has caught up, so a child just launched is known.
        if board.is_ok() {
            self.sweep_hand_offs();
        }
        // The Active to Idle edge of an agent owed a restart (step 10e Stage
        // 2a): its engine is ended, and its next incarnation resumed by the
        // plan's re-host once it has exited.
        busy |= self.restart_the_due(board.is_ok());
        if self.handing_over {
            busy |= self.guide_the_busy().await;
            busy |= self.end_the_idle();
            busy |= self.hand_over_the_overdue();
        }
        for (project_id, step) in self.plan() {
            // Draining, it starts no child and gives none a turn but the
            // drain's guidance: what is pending is the next daemon's to
            // deliver.
            let held = step.delivers() && !self.guides(&step);
            if draining && (matches!(step, Step::Start { .. }) || held) {
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
        let wakeable = self.wakeable();
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
                    wakeable: &wakeable,
                })
                .into_iter()
                .map(|step| (project.clone(), step)),
            );
        }
        steps
    }

    /// Send `sent` to the store.
    pub(crate) async fn send(&mut self, sent: &Sent) -> Taken {
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
            Step::Launch {
                mut mint, failed, ..
            } => {
                // The axis it works on, recorded at its mint (s4-Q11): its
                // agent's `workspace`, as its plan settles it.
                if mint.mint.command.workspace.is_none() {
                    let session = mint.claim.command.session.clone();
                    mint.mint.command.workspace = self.axis_of(&session, &mint.mint.command);
                }
                // Its output folder, which its plan leaves to its project:
                // that project's `output.dir`, as its config says, and its
                // OWN harp, whether its owner's project or another (step 8
                // U2b; owner ruling 2026-11-16: one rule for every
                // session). One that cannot be read is a launch that cannot
                // be made.
                {
                    let session = mint.claim.command.session.clone();
                    match self.output_of(&session, &mint.mint.command) {
                        Ok(dir) => mint.mint.command.output_dir = dir,
                        Err(code) => {
                            (self.say)(&format!(
                                "{}: its output folder not read: {code}",
                                mint.mint.command.harp
                            ));
                            return self.send(&failed).await;
                        }
                    }
                }
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
                resume,
                ..
            } => {
                let child = Child {
                    harp: &harp,
                    engine: &engine,
                    project_dir: &project_dir,
                    resume,
                };
                self.start(project_id, at, &child).await
            }
            Step::Rehost {
                at,
                harp,
                owner,
                cause,
                ..
            } => self.rehost(project_id, &at, &harp, &owner, &cause).await,
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
            // Its run asked to stop (S19): its engine ended, the child left
            // open, its hand-off kept for its next run.
            Step::Stop { at, .. } => match self.hosted.get(&at.run()) {
                Some(hosted) => {
                    self.stopping.insert(at.run());
                    let _ = hosted.control.end(END_GRACE);
                    Taken::Done
                }
                None => Taken::Again,
            },
            Step::End { at, .. } => {
                // The child is closed for good (T6): what it was launched
                // with is let go, at rest too.
                self.envs.remove(&at.session);
                self.hand_offs.delete(&at.session);
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
    /// enqueue it judged on its launcher's environment (rulings 53, 73e), prepare its session home, start its engine
    /// and record the runner started. A child on an engine the daemon hosts
    /// none of, or that cannot be planned or is refused, ends its
    /// incarnation and is released by its owner (D-N47); one that cannot
    /// start ends its run and its incarnation, so nothing is left owed.
    async fn start(&mut self, project_id: &str, at: RunAt, start: &Child<'_>) -> Taken {
        let (harp, engine, project_dir) = (start.harp, start.engine, start.project_dir);
        let facts = Facts::new(Arc::clone(&self.edge));
        let run = at.run();
        let Some(hosted_engine) = self.engine(engine) else {
            (self.say)(&format!("{harp}: no child engine for {engine}"));
            let _ = facts
                .incarnation_ended(&at.session, at.incarnation, "launch-failed")
                .await;
            return Taken::Done;
        };
        let handed = self.child_env(&at.session);
        let env = handed.env.clone();
        // The child's project's trust, as the daemon's index has it now
        // (step 8 U2): its own settings apply only once the user trusts it.
        let trusted = self.trusts.projector().index().trusts(project_id);
        // Where its project's trees are, read BEFORE it is planned (step 10c
        // U13 rework): a change recorded past them is owed to it.
        let covered = self.positions_of(project_id);
        let child = match self.plan_child(&hosted_engine, &handed.agent, project_dir, &env, trusted)
        {
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
        // Its session's lock, held for its life (U6): one another holds is
        // a session running elsewhere, tried again later.
        let session_lock = match self.homes.hold_session(project_dir, harp) {
            Ok(held) => held,
            Err(why) => {
                if self.complained.insert(format!("session-lock:{run}")) {
                    (self.say)(&format!("{harp}: its session's lock: {why}"));
                }
                return Taken::Again;
            }
        };
        let enqueued = Enqueuer::new(Arc::clone(&self.edge))
            .enqueue(EnqueueRequest {
                session: at.session.clone(),
                incarnation: at.incarnation,
                mode: RunMode::Structured,
                evidence: child.evidence.clone(),
                launcher_lock: lock.path().to_string(),
                // What it is given, journaled (step 9).
                grant: Some(child.grant()),
            })
            .await;
        match enqueued {
            Ok(_) => self.record_loadout(&at, &child.loadout, covered).await,
            // Judged as any child is (rulings 53, 73e): refused for good.
            Err(EnqueueError::Refused(rejection)) => {
                let code = angzarr_diagnostic::Diagnostic::code(&rejection).to_string();
                (self.say)(&format!("{harp}: refused: {code}"));
                drop(session_lock);
                drop(lock);
                self.launch_refused(project_id, &at, &code).await;
                return Taken::Done;
            }
            Err(EnqueueError::Failed(error)) => {
                (self.say)(&format!("{harp}: not enqueued: {error:?}"));
                return Taken::Again;
            }
        };
        let ready = Ready {
            engine: hosted_engine,
            child,
            handed,
            run_lock: lock,
            session_lock,
        };
        self.started(project_id, at, start, ready).await
    }

    /// The child enqueued (`ready`), started and hosted: its engine started
    /// where it works, its start recorded, held for its life. One that
    /// cannot start ends its run and its incarnation.
    async fn started(
        &mut self,
        project_id: &str,
        at: RunAt,
        start: &Child<'_>,
        ready: Ready,
    ) -> Taken {
        let (harp, project_dir) = (start.harp, start.project_dir);
        let facts = Facts::new(Arc::clone(&self.edge));
        let run = at.run();
        let Ready {
            engine: hosted_engine,
            child: planned,
            handed,
            run_lock: lock,
            session_lock,
        } = ready;
        let who = Identity {
            run: root_of(&run),
            session: root_of(&at.session),
            incarnation: at.incarnation,
            harp: harp.to_string(),
            depth: 1,
        };
        let name = self.name_of(project_id, harp);
        let workspaces = self
            .workspaces
            .as_ref()
            .map(|of| of.of(project_id, project_dir, Some(&handed.env)));
        let workspace = self.workspace_of(project_id, &at, harp, &planned, &handed);
        let started = self.spawn_child(
            &hosted_engine,
            Spawn {
                at: &at,
                harp,
                name: &name,
                project_dir,
                resume: start.resume.clone(),
                workspace,
                workspaces: workspaces
                    .as_deref()
                    .map(|w| w as &dyn isolation_ports::Workspaces),
            },
            &planned,
            &handed,
        );
        let (child, transcripts, placed) = match started {
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
        // Where it works: its checkout, else the project.
        let cwd = placed
            .as_ref()
            .map_or_else(|| project_dir.to_string(), |p| p.cwd.clone());
        if let Some(warning) = placed.as_ref().and_then(|p| p.warning.as_ref()) {
            (self.say)(&format!("{harp}: works in the project: {warning}"));
        }
        let checkout = placed.and_then(|placed| {
            Some(Placed {
                checkout: placed.checkout?,
                files: placed.files,
                workspaces: Arc::clone(workspaces.as_ref()?),
            })
        });
        let recorded = facts
            .record(
                &who,
                RunnerFact::RunnerStarted(runs::RecordRunnerStarted {
                    runtime: "host".to_string(),
                    workspace: cwd,
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
        let asker = Asker::of(&at, hosted_engine.name(), &handed, &planned);
        self.host(
            child,
            transcripts,
            at,
            who,
            Locks {
                run: lock,
                session: Some(session_lock),
                checkout,
            },
            asker,
        );
        Taken::Done
    }

    /// The workspace axis the child minted by `mint` works on, as its plan
    /// settles it (its agent's `workspace`), on its engine and in its
    /// launcher's environment (its hand-off); none where it cannot be
    /// planned (its start says why).
    fn axis_of(&mut self, session: &str, mint: &sessions::MintSession) -> Option<String> {
        let handed = self.hand_offs.read(session)?;
        let trusted = self.trusts.projector().index().trusts(&mint.project_id);
        let previewed = self
            .plans
            .preview(
                &mint.engine,
                &handed.agent,
                &mint.project_dir,
                &handed.env,
                trusted,
            )
            .ok()?;
        Some(
            run_launcher::Axis::of(&previewed.plan.axes.0)
                .word()
                .to_string(),
        )
    }

    /// The output folder of the child `session` that `mint` makes: its
    /// project's root of session outputs, read in that project with the
    /// environment its launcher handed (none handed, an empty one), then
    /// its harp.
    fn output_of(&self, session: &str, mint: &sessions::MintSession) -> Result<String, String> {
        let env = self
            .hand_offs
            .read(session)
            .map(|handed| handed.env)
            .unwrap_or_default();
        let root = self.plans.output_root(&mint.project_dir, &env)?;
        Ok(format!("{}/{}", root.trim_end_matches('/'), mint.harp))
    }

    /// What the child asks of the workspace axis: the axis its session was
    /// minted on (its own, so a re-host re-enters its checkout), else its
    /// plan's; its checkouts directory under ctxloom's home; its agent.
    fn workspace_of(
        &self,
        project_id: &str,
        at: &RunAt,
        harp: &str,
        child: &Planned,
        handed: &HandOff,
    ) -> run_launcher::Workspace {
        let board = self.board.projector().board();
        let recorded = board
            .session(&at.session)
            .and_then(|session| session.workspace.clone());
        let axis = recorded.unwrap_or_else(|| child.axis.clone());
        let agent = [&handed.agent, &child.agent]
            .into_iter()
            .find(|agent| !agent.is_empty())
            .cloned()
            .unwrap_or_default();
        run_launcher::Workspace {
            axis: run_launcher::Axis::of(&axis),
            work_dir: match project_id.is_empty() {
                true => String::new(),
                false => isolation_ports::session_worktrees(
                    std::path::Path::new(&self.place.home),
                    project_id,
                    harp,
                )
                .display()
                .to_string(),
            },
            agent,
        }
    }

    /// Hold the started `child`: its observations heard through the ears,
    /// its control and its run's lock kept for its life.
    fn host(
        &mut self,
        child: run_ports::StartedChild,
        transcripts: Option<String>,
        at: RunAt,
        who: Identity,
        locks: Locks,
        asker: Asker,
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
                _lock: locks.run,
                _session_lock: locks.session,
                checkout: locks.checkout,
                asker,
            },
        );
    }

    /// The loadout of the incarnation `at` planned on, recorded on its
    /// session (step 10c U13; keyed by the session and the incarnation, so
    /// a retried start records it once). One not recorded is said: the
    /// child runs, and a change is told it at no hook.
    async fn record_loadout(
        &mut self,
        at: &RunAt,
        loadout: &sessions::SessionLoadout,
        covered: Vec<sessions::TreePosition>,
    ) {
        let Ok(root) = uuid::Uuid::parse_str(&at.session) else {
            return;
        };
        let cover = Cover {
            domain: sessions::SESSION_DOMAIN.to_string(),
            root: Some(Uuid::from_bytes(*root.as_bytes())),
            edition: None,
        };
        let fact = sessions::RecordLoadout {
            loadout: loadout.clone(),
            incarnation: at.incarnation,
            covered,
        };
        let key = sessions::loadout_key(&at.session, at.incarnation);
        if let Err(failure) = self
            .edge
            .handle(sessions::fact_book(cover, &fact, &key))
            .await
        {
            (self.say)(&format!("its loadout was not recorded: {failure:?}"));
        }
    }

    /// Where each library tree of the project `project_id` (each checkout's)
    /// and the home's are, as the checkouts projection holds them now:
    /// every tree's latest sequence that changed a path (step 10c U13
    /// rework), sorted by tree.
    fn positions_of(&self, project_id: &str) -> Vec<sessions::TreePosition> {
        let prefix = self
            .index
            .projector()
            .index()
            .project_harp(project_id)
            .map(|harp| format!("projects/{harp}/checkouts/"));
        crate::owed::positions(&self.checkouts.projector().checkouts(), |tree| {
            tree == library_tree::HOME_TREE
                || prefix
                    .as_ref()
                    .is_some_and(|prefix| tree.starts_with(prefix))
        })
    }

    /// The hosted child of `project_dir` on `engine`, planned by its
    /// project's dry run and judged on `env` (D-N46, D-N47); the plan's
    /// error code where it cannot be planned.
    fn plan_child(
        &self,
        engine: &HostedEngine,
        agent: &str,
        project_dir: &str,
        env: &BTreeMap<String, String>,
        trusted: bool,
    ) -> Result<Planned, String> {
        let previewed = self
            .plans
            .preview(engine.name(), agent, project_dir, env, trusted)?;
        let plan = &previewed.plan;
        let agent = [&plan.agent, &plan.settings_agent]
            .into_iter()
            .find(|agent| !agent.is_empty())
            .cloned()
            .unwrap_or_default();
        let loadout = crate::reactions::loadout_of_plan(&agent, &plan.profiles, &plan.fragments);
        Ok(Planned {
            hosted: run_application::hosted_child(
                &previewed,
                engine.name(),
                &self.engines(),
                engine.engine.credentials(),
                env,
            ),
            axis: plan.axes.0.clone(),
            agent,
            loadout,
        })
    }

    /// Let go of what the exited child `gone` held: its checkout, WIP-safe
    /// (s4-Q11), said; then its locks, as it is dropped.
    fn let_go(&mut self, harp: &str, gone: Hosted) {
        if let Some(placed) = &gone.checkout {
            match placed.release() {
                isolation_ports::Removal::Removed => (self.say)(&format!(
                    "{harp}: its checkout {} removed",
                    placed.checkout.path
                )),
                isolation_ports::Removal::Left(why) => (self.say)(&format!(
                    "{harp}: its checkout {} (branch {}) left: {why}",
                    placed.checkout.path, placed.checkout.branch
                )),
            }
        }
        drop(gone);
    }

    /// The environment the child `session` is started with: its hand-off,
    /// READ at each start (T6: it lives as long as the child is open, so a
    /// start after a daemon restart reads it too), else the one held, else
    /// none at all (a child no launcher handed anything is judged with no
    /// credential).
    fn child_env(&mut self, session: &str) -> HandOff {
        let handed = match self.hand_offs.read(session) {
            Some(handed) => handed,
            None => self.envs.get(session).cloned().unwrap_or_default(),
        };
        self.envs.insert(session.to_string(), handed.clone());
        handed
    }

    /// Every hand-off no open session holds, deleted (T6, the owner's ruling
    /// on U3), the board caught up: its child's session closed; or, a
    /// session the board knows nothing of (neither a session nor an owner's
    /// child), unless it is marked in launch and its launch's creator still
    /// holds the launch lock (between its `CreateHandOff` and its
    /// `LaunchChild`). A marked one a `LaunchChild` has recorded (its owner
    /// holds the child) is claimed: its mark cleared.
    fn sweep_hand_offs(&mut self) {
        let board = self.board.projector().board();
        let launched = |id: &str| {
            board.sessions().any(|(_, owner)| {
                owner
                    .children
                    .iter()
                    .any(|child| child.session.as_ref().is_some_and(|c| c.session == id))
            })
        };
        for kept in self.hand_offs.list() {
            let known = board.session(&kept.session);
            let gone = match known {
                Some(session) => session.closed,
                None if launched(&kept.session) => false,
                None => !(kept.marked && kept.creator_holds),
            };
            let hosted = self.hosted.values().any(|h| h.at.session == kept.session);
            if gone && !hosted {
                self.hand_offs.delete(&kept.session);
                self.envs.remove(&kept.session);
                (self.say)(&format!("{}: its hand-off swept", kept.session));
            } else if !gone && kept.marked && (known.is_some() || launched(&kept.session)) {
                self.hand_offs.claim(&kept.session);
            }
        }
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
    /// engine started with its launcher's environment, its hooks told its
    /// run and session (they find the daemon themselves): made by the one
    /// launch (T10,
    /// `run_launcher::launch_of`), its program found and its permits
    /// applied there, then started exactly as made.
    fn spawn_child(
        &self,
        engine: &HostedEngine,
        spawn: Spawn<'_>,
        child: &Planned,
        handed: &HandOff,
    ) -> Result<
        (
            run_ports::StartedChild,
            Option<String>,
            Option<run_launcher::Placement>,
        ),
        String,
    > {
        let Spawn {
            at,
            harp,
            name,
            project_dir,
            ..
        } = spawn;
        let sent = &handed.env;
        // Composed for this launch (T21): her settings read now, where her
        // launcher finds them.
        let (prepares, starts) = engine.launched(&handed.launcher, handed.program.as_ref());
        // The one session-setup path (owner, 2026-11-12): the launcher's,
        // over the child's project's session directories, so a hosted
        // child's home gets what every session's does (the engine's merge,
        // its seeds, her git identity, and what the engine was composed
        // with: her own settings, the program its callbacks run).
        let mut records = self.homes.records(project_dir);
        // The one launch (T10): set up, the child's command, the permits,
        // its program found, the batch and length checks, as every launch's.
        let request = run_launcher::LaunchRequest {
            at: run_ports::LaunchAt {
                // A hosted launch never leaves the daemon: no project id.
                project_id: String::new(),
                session: at.session.clone(),
                harp: harp.to_string(),
                run: at.run(),
                incarnation: at.incarnation,
            },
            kind: run_ports::LaunchKind::Child,
            setup: run_launcher::LaunchSetup {
                home: run_launcher::HomeSetup {
                    harp: harp.to_string(),
                    context: child.context.clone(),
                    interactive: false,
                    cwd: Some(project_dir.to_string()),
                    // The agent's own document for the engine, verbatim
                    // (rulings 133-137), as every launch's.
                    permissions: child.permissions.clone(),
                    session_home: child.settings.session_home,
                    git_user: self.plans.git_user(project_dir, sent),
                    // What the run agent's selection carries, as every
                    // launch's plan does.
                    surfaces: child.surfaces.clone(),
                },
                // Its agent's `workspace` (s4-Q11): the project, or a
                // checkout of its session's own.
                workspace: spawn.workspace.clone(),
                env: self.child_environment(at, name, sent, handed.program.as_ref()),
                // What the daemon sets for the child, which no permit
                // replaces (ruling 78b): its HOME (the session home) and the
                // PATH led by the program's directory.
                owned: vec!["HOME".to_string(), "PATH".to_string()],
            },
            prompt: String::new(),
            // Its own conversation, from its second incarnation on (T19).
            resume: spawn.resume.clone(),
            session_id: None,
            user_home: self.place.user_home.clone(),
            model: child.model.clone(),
            effort: child.effort.clone(),
            settings: child.settings.clone(),
            // claude asks no permission over its stream: unused here.
            child_permissions: run_ports::ChildPermissions::default(),
            session_lock: String::new(),
        };
        let as_named = |command: &run_ports::TurnCommand| Ok(command.program.clone());
        let finder = self.finder.as_deref();
        let find = |command: &run_ports::TurnCommand| match finder {
            Some(finder) => finder.find_program(command),
            None => as_named(command),
        };
        let made = run_launcher::launch_of(
            run_launcher::LaunchEngines {
                engine: &*prepares,
                child: Some(&*starts),
            },
            &find,
            &mut *records,
            spawn.workspaces,
            &request,
            &self.place.os,
        )
        .map_err(|unprepared| unprepared.why.to_string())?;
        let spec = made
            .child
            .ok_or_else(|| format!("{harp}: no child was planned"))?;
        let started = starts
            .start(&spec, &made.launch)
            .map_err(|e| e.to_string())?;
        Ok((started, (engine.transcripts)(&spec), made.placed))
    }

    /// The environment a child's engine is started with: its launcher's,
    /// ctxloom's home, `PATH` led by the program's directory (its launcher's
    /// `program`, else the daemon's own), and its run and session (always the
    /// run's own; no endpoint, bearer or certificate: slice 4 U7).
    fn child_environment(
        &self,
        at: &RunAt,
        name: &str,
        sent: &BTreeMap<String, String>,
        program: Option<&crate::Program>,
    ) -> BTreeMap<String, String> {
        let mut env = sent.clone();
        env.insert(
            run_ports::HOME_VARIABLE.to_string(),
            self.place.home.clone(),
        );
        let dir = program
            .map(|program| &program.dir)
            .filter(|dir| !dir.is_empty())
            .or(self.place.program_dir.as_ref());
        if let Some(dir) = dir {
            let path = run_ports::search_path_led_by(
                &self.place.os,
                dir,
                env.get("PATH").map(String::as_str),
            );
            env.insert("PATH".to_string(), path);
        }
        run_ports::name_the_run(&mut env, &at.run(), name);
        env
    }

    /// The resource name of the session `harp` of the project `project_id`
    /// (s3-Q3: what its engine is told in `CTXLOOM_SESSION`), as the index
    /// names it; empty while the project has no harp.
    fn name_of(&self, project_id: &str, harp: &str) -> String {
        let indexes = self.index.projector();
        let index = indexes.index();
        index
            .project_harp(project_id)
            .and_then(|project| sessions::SessionName::new(project, harp).ok())
            .map(|name| name.to_string())
            .unwrap_or_default()
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
        // A clear (owner 2026-11-15: clears captured, typed) is in the
        // session's record before the bind of the conversation after it,
        // keyed by that conversation as the hook keys it, so the two
        // reports of one clear are one fact.
        if let ChildObservation::Cleared {
            native_id: Some(native_id),
        } = &observation
        {
            let cleared =
                runs_application::Observation::Cleared(sessions::RecordConversationCleared {
                    native_session: native_id.clone(),
                    trigger: "clear".to_string(),
                });
            if let Err(error) = facts.observe(&who, &cleared).await {
                (self.say)(&format!("{}: not recorded: {error:?}", who.harp));
            }
        }
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
                .map(|turn| RunnerFact::TurnStarted(runs::RecordTurnStarted { turn, message_ids })),
            ChildObservation::TurnEnded {
                message_ids,
                outcome,
            } => turn_of(&message_ids).map(|turn| match outcome {
                TurnOutcome::Answered { native_id, .. } => {
                    RunnerFact::TurnCompleted(runs::RecordTurnCompleted {
                        turn,
                        native_key: native_id.unwrap_or_default(),
                        answer_ref: String::new(),
                        exit_code: 0,
                        message_ids,
                    })
                }
                TurnOutcome::Failed { class, detail, .. } => {
                    RunnerFact::TurnFailed(runs::RecordTurnFailed {
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
                        RunnerFact::RunExited(runs::RecordRunExited {
                            code: exit.code.unwrap_or(-1),
                            signal: exit.signal.unwrap_or(0),
                        }),
                    )
                    .await;
                // Ended to be handed over (s4-Q4): the next daemon re-hosts it.
                let cause = self.end_cause(run);
                let _ = facts
                    .incarnation_ended(&at.session, at.incarnation, cause)
                    .await;
                (self.say)(&format!("{}: exited {exit:?}", who.harp));
                if let Some(gone) = self.hosted.remove(run) {
                    self.let_go(&who.harp, gone);
                }
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

    /// Why the incarnation of the run `run`, whose engine exited, ends: the
    /// reason this daemon ended its engine (handed over, stopped, restarted),
    /// else its own exit. Each reason is let go of.
    fn end_cause(&mut self, run: &str) -> &'static str {
        let stopped = self.stopping.remove(run);
        let restarted = self.restarting.remove(run);
        match (self.drained.remove(run), stopped, restarted) {
            (true, _, _) => session_policies::ENDED_DRAINED,
            (false, true, _) => STOPPED,
            (false, false, true) => session_policies::ENDED_RESTARTED,
            (false, false, false) => "runner-exit",
        }
    }

    /// End every child still hosted (the daemon exits), each given its
    /// grace, and stop every one-shot it hosts.
    pub fn end_all(&mut self) {
        for hosted in self.hosted.values() {
            let _ = hosted.control.end(END_GRACE);
        }
        if let Some(one_shots) = &self.one_shots {
            one_shots.stop_all();
        }
    }

    /// The stop step (U4, T7): each hosted one-shot whose run's stream holds
    /// a `StopRequested` (its client's `RequestStop`, on a signal) has its
    /// engine stopped, for the reason recorded. Whether it stopped any.
    async fn stop_one_shots(&mut self) -> bool {
        let Some(one_shots) = self.one_shots.clone() else {
            return false;
        };
        let mut stopped = false;
        for run in one_shots.unstopped() {
            let Some(reason) = self.stop_asked(&run).await else {
                continue;
            };
            if one_shots.stop(&run, &reason) {
                (self.say)(&format!(
                    "{run}: asked to stop ({reason}): stopping its engine"
                ));
                stopped = true;
            }
        }
        stopped
    }

    /// Why `run`'s stream asks it to stop, if it does.
    async fn stop_asked(&self, run: &str) -> Option<String> {
        use angzarr_embedded::Payload;
        let cover = angzarr_embedded::v1::Cover {
            domain: runs::RUN_DOMAIN.to_string(),
            root: Some(root_of(run)),
            edition: None,
        };
        let pages = self.edge.events(&cover, 0).await.ok()?;
        pages.iter().find_map(|page| match &page.body {
            Some(angzarr_embedded::v1::event_page::Body::Event(any)) => {
                match runs::RunEvent::from_any(any) {
                    Ok(runs::RunEvent::StopRequested(stop)) => Some(stop.reason),
                    _ => None,
                }
            }
            _ => None,
        })
    }
}
