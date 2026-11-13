//! The daemon's one launcher over the real aggregates (run-launcher's
//! `TestEdge`, an in-memory store) and fakes of its ports (T18).

use std::collections::BTreeMap;
use std::sync::{Arc, Mutex};

use angzarr_embedded::v1::{event_page, CommandBook, Cover};
use angzarr_embedded::{CommandBookExt, Payload};
use library_application::Previewed;
use run_launcher::fakes::{session_cover, uuid, TestEdge};
use run_ports::{
    ChildEngine, ChildError, ChildObservation, ChildSpec, Edge, Engine, EngineError, LockProbe,
    PrepareRequest, Prepared, PublicCert, RunLocks, StartedChild, Token, TokenSource, TurnCommand,
    TurnOutcome, TurnOutput, TurnRequest,
};
use runs::RunEvent;
use sessions::{SessionEvent, SessionRef};

use crate::{
    ChildHomes, ChildPlans, Daemon, Ears, Folds, HandOffs, HostedEngine, Place, Ports, Swept,
};

const OWNER: &str = "5f0c7a52-1111-5111-8111-000000000001";
const CHILD: &str = "5f0c7a52-2222-5222-8222-000000000002";
const RUN_LOCK: &str = "/home/u/.ctxloom/runs/child-1.lock";

/// An engine of `name` that is never driven here.
struct Named(&'static str);

impl Engine for Named {
    fn name(&self) -> &str {
        self.0
    }

    fn prepare(&self, _: &PrepareRequest) -> Result<Prepared, EngineError> {
        unreachable!("no child is started in these tests")
    }

    fn drive_turn(&self, _: &TurnRequest) -> Result<TurnCommand, EngineError> {
        unreachable!("a hosted child takes no turn command")
    }

    fn finish_turn(&self, _: &TurnOutput) -> TurnOutcome {
        unreachable!("a hosted child takes no turn command")
    }

    fn drive_interactive(&self, _: &TurnRequest) -> Result<TurnCommand, EngineError> {
        unreachable!("a hosted child is no terminal")
    }
}

impl ChildEngine for Named {
    fn start(&self, _: &ChildSpec) -> Result<StartedChild, ChildError> {
        unreachable!("no child is started in these tests")
    }
}

fn hosted(name: &'static str) -> HostedEngine {
    let named = Arc::new(Named(name));
    HostedEngine {
        engine: named.clone(),
        child: named,
        transcripts: Arc::new(|_| None),
    }
}

/// A plan that fails, recording which engine it was asked for.
#[derive(Clone, Default)]
struct Plans(Arc<Mutex<Vec<String>>>);

impl ChildPlans for Plans {
    fn preview(
        &self,
        engine: &str,
        _: &str,
        _: &BTreeMap<String, String>,
    ) -> Result<Previewed, String> {
        self.0.lock().unwrap().push(engine.to_string());
        Err("library-application-no-agent".to_string())
    }

    fn git_user(&self, _: &str, _: &BTreeMap<String, String>) -> Option<run_ports::GitUser> {
        None
    }
}

struct NoHandOffs;

impl HandOffs for NoHandOffs {
    fn take(&self, _: &str) -> Option<BTreeMap<String, String>> {
        None
    }
}

struct NoHomes;

impl ChildHomes for NoHomes {
    fn records(&self, _: &str) -> Box<dyn run_ports::SessionRecords> {
        Box::new(run_launcher::fakes::MemoryRecords::default())
    }
}

struct Deaf;

impl Ears for Deaf {
    fn listen(&self, _: String, _: Box<dyn Iterator<Item = ChildObservation> + Send>) {}

    fn heard(&self) -> Vec<(String, ChildObservation)> {
        Vec::new()
    }
}

/// The locks: `RUN_LOCK` probes as told, every other unproven.
struct Locks(LockProbe);

impl RunLocks for Locks {
    fn hold_run(&self, run: &str) -> Result<run_ports::HeldLock, run_ports::LockError> {
        Ok(run_ports::HeldLock::new(format!("/locks/{run}.lock"), ()))
    }

    fn probe(&self, path: &str) -> LockProbe {
        match path == RUN_LOCK {
            true => self.0,
            false => LockProbe::Unproven,
        }
    }

    fn session(&self, _: &str, _: &str) -> LockProbe {
        LockProbe::Unproven
    }
}

#[derive(Default)]
struct Counting(std::sync::atomic::AtomicU8);

impl TokenSource for Counting {
    fn mint(&self) -> Token {
        let n = self.0.fetch_add(1, std::sync::atomic::Ordering::SeqCst) + 1;
        Token::from_bytes([n; 32])
    }
}

fn daemon(
    edge: &Arc<TestEdge>,
    engines: Vec<HostedEngine>,
    plans: Plans,
    locks: LockProbe,
) -> Daemon<TestEdge, Counting> {
    daemon_saying(edge, engines, plans, locks, Box::new(|_| {}))
}

/// A daemon whose log is `say`.
fn daemon_saying(
    edge: &Arc<TestEdge>,
    engines: Vec<HostedEngine>,
    plans: Plans,
    locks: LockProbe,
    say: Box<dyn FnMut(&str) + Send>,
) -> Daemon<TestEdge, Counting> {
    Daemon::new(Ports {
        edge: Arc::clone(edge),
        tokens: Arc::new(Counting::default()),
        engines,
        plans: Box::new(plans),
        hand_offs: Box::new(NoHandOffs),
        homes: Box::new(NoHomes),
        ears: Box::new(Deaf),
        run_locks: Arc::new(Locks(locks)),
        place: Place {
            home: "/home/u".to_string(),
            program_dir: None,
            os: "linux".to_string(),
            user_home: None,
            endpoint: "https://127.0.0.1:1".to_string(),
            cert: PublicCert::from_der(vec![0x30, 0x00]).unwrap(),
        },
        folds: Folds::default(),
        say,
    })
}

/// Mint `session` in project calm-otter, owned by `owner` when given.
async fn mint(edge: &TestEdge, session: &str, harp: &str, engine: &str, owner: Option<&str>) {
    let book = CommandBook::command_of(
        session_cover(session),
        &sessions::SessionCommand::from(sessions::MintSession {
            harp: harp.to_string(),
            project_id: "calm-otter".to_string(),
            project_dir: "/work".to_string(),
            output_dir: "/work/out".to_string(),
            engine: engine.to_string(),
            origin: sessions::Origin::Agent,
            started_at: None,
            depth: u32::from(owner.is_some()),
            owner: owner.map(|owner| SessionRef {
                project_id: "calm-otter".to_string(),
                session: owner.to_string(),
            }),
        }),
    );
    edge.handle(book).await.unwrap();
}

async fn session_events(edge: &TestEdge, session: &str) -> Vec<SessionEvent> {
    events(edge, &session_cover(session))
        .await
        .into_iter()
        .map(|any| SessionEvent::from_any(&any).unwrap())
        .collect()
}

async fn events(edge: &TestEdge, cover: &Cover) -> Vec<angzarr_embedded::Any> {
    edge.events(cover, 0)
        .await
        .unwrap()
        .into_iter()
        .filter_map(|page| match page.body {
            Some(event_page::Body::Event(any)) => Some(any),
            _ => None,
        })
        .collect()
}

fn ended(events: &[SessionEvent]) -> Vec<(u32, String)> {
    events
        .iter()
        .filter_map(|event| match event {
            SessionEvent::IncarnationEnded(ended) => Some((ended.incarnation, ended.cause.clone())),
            _ => None,
        })
        .collect()
}

// T18: the daemon's FIRST step is the sweep. A child a killed daemon hosted
// (started, its run lock left free) is ended `RunnerLost` and its
// incarnation closed, before anything is hosted; a second start ends
// nothing again.
#[tokio::test]
async fn the_start_sweeps_a_killed_daemons_child_and_ends_it_once() {
    let edge = Arc::new(TestEdge::new());
    mint(&edge, CHILD, "kid", "claude-code", None).await;
    let enqueued =
        runs_application::Enqueuer::new(Arc::clone(&edge), Arc::new(Counting::default()))
            .enqueue(runs_application::EnqueueRequest {
                session: CHILD.to_string(),
                incarnation: 1,
                mode: runs::RunMode::Structured,
                evidence: runs_application::Evidence::passing("claude-code"),
                launcher_lock: RUN_LOCK.to_string(),
            })
            .await
            .unwrap();
    let who = runs_application::Identity {
        run: runs::run_root(CHILD, 1),
        session: uuid(CHILD),
        incarnation: 1,
        harp: "kid".to_string(),
        depth: 0,
    };
    let _ = enqueued;
    runs_application::Facts::new(Arc::clone(&edge))
        .record(
            &who,
            runs_application::RunnerFact::RunnerStarted(runs::RunnerStarted {
                runtime: "host".to_string(),
                workspace: "/work".to_string(),
                pid: 7,
            }),
        )
        .await
        .unwrap();
    let mut daemon = daemon(
        &edge,
        vec![hosted("claude-code")],
        Plans::default(),
        LockProbe::Free,
    );
    let swept = daemon.start_up(&edge.store, edge.catalog()).await.unwrap();
    assert_eq!(
        swept,
        [Swept {
            run: runs::run_root(CHILD, 1),
            session: CHILD.to_string(),
            incarnation: 1,
            started: true,
        }]
    );
    let run = Cover {
        domain: runs::RUN_DOMAIN.to_string(),
        root: Some(runs::run_root(CHILD, 1)),
        edition: None,
    };
    let causes: Vec<runs::EndCause> = events(&edge, &run)
        .await
        .into_iter()
        .filter_map(|any| match RunEvent::from_any(&any).unwrap() {
            RunEvent::RunEnded(ended) => Some(ended.cause),
            _ => None,
        })
        .collect();
    assert_eq!(causes, [runs::EndCause::RunnerLoss]);
    assert_eq!(
        ended(&session_events(&edge, CHILD).await),
        [(1, "orphaned".to_string())]
    );
    assert!(daemon
        .start_up(&edge.store, edge.catalog())
        .await
        .unwrap()
        .is_empty());
}

/// Enqueue `CHILD`'s first run (its lock `RUN_LOCK`), and start it when
/// `started`.
async fn a_run(edge: &Arc<TestEdge>, started: bool) {
    mint(edge, CHILD, "kid", "claude-code", None).await;
    runs_application::Enqueuer::new(Arc::clone(edge), Arc::new(Counting::default()))
        .enqueue(runs_application::EnqueueRequest {
            session: CHILD.to_string(),
            incarnation: 1,
            mode: runs::RunMode::Interactive,
            evidence: runs_application::Evidence::passing("claude-code"),
            launcher_lock: RUN_LOCK.to_string(),
        })
        .await
        .unwrap();
    if !started {
        return;
    }
    let who = runs_application::Identity {
        run: runs::run_root(CHILD, 1),
        session: uuid(CHILD),
        incarnation: 1,
        harp: "kid".to_string(),
        depth: 0,
    };
    runs_application::Facts::new(Arc::clone(edge))
        .record(
            &who,
            runs_application::RunnerFact::RunnerStarted(runs::RunnerStarted {
                runtime: "host".to_string(),
                workspace: "/work".to_string(),
                pid: 7,
            }),
        )
        .await
        .unwrap();
}

/// How `CHILD`'s first run ended, each time.
async fn run_ends(edge: &TestEdge) -> Vec<runs::EndCause> {
    let run = Cover {
        domain: runs::RUN_DOMAIN.to_string(),
        root: Some(runs::run_root(CHILD, 1)),
        edition: None,
    };
    events(edge, &run)
        .await
        .into_iter()
        .filter_map(|any| match RunEvent::from_any(&any).unwrap() {
            RunEvent::RunEnded(ended) => Some(ended.cause),
            _ => None,
        })
        .collect()
}

// M9: a run started and not ended whose owner lives (an interactive session
// the CLI runs, not a child the daemon hosts) keeps the daemon busy.
#[tokio::test]
async fn a_run_another_process_started_and_has_not_ended_is_live() {
    let edge = Arc::new(TestEdge::new());
    a_run(&edge, true).await;
    let mut daemon = daemon(&edge, Vec::new(), Plans::default(), LockProbe::Held);
    assert_eq!(daemon.live_runs(&edge.store, edge.catalog()).await, Ok(1));
    assert!(run_ends(&edge).await.is_empty(), "nothing is ended");
}

// M9: one whose owner is gone is ended as the start's sweep would, and
// keeps nobody busy.
#[tokio::test]
async fn a_run_whose_owner_is_gone_is_ended_and_is_not_live() {
    let edge = Arc::new(TestEdge::new());
    a_run(&edge, true).await;
    let mut daemon = daemon(&edge, Vec::new(), Plans::default(), LockProbe::Free);
    assert_eq!(daemon.live_runs(&edge.store, edge.catalog()).await, Ok(0));
    assert_eq!(run_ends(&edge).await, [runs::EndCause::RunnerLoss]);
    assert_eq!(daemon.live_runs(&edge.store, edge.catalog()).await, Ok(0));
}

// M9: a run still owed a runner has not started: it is not live yet.
#[tokio::test]
async fn a_run_not_started_is_not_live() {
    let edge = Arc::new(TestEdge::new());
    a_run(&edge, false).await;
    let mut daemon = daemon(&edge, Vec::new(), Plans::default(), LockProbe::Held);
    assert_eq!(daemon.live_runs(&edge.store, edge.catalog()).await, Ok(0));
}

// A host still holding a child's run lock is alive: the start sweeps
// nothing.
#[tokio::test]
async fn the_start_leaves_a_child_whose_host_holds_its_lock() {
    let edge = Arc::new(TestEdge::new());
    mint(&edge, CHILD, "kid", "claude-code", None).await;
    runs_application::Enqueuer::new(Arc::clone(&edge), Arc::new(Counting::default()))
        .enqueue(runs_application::EnqueueRequest {
            session: CHILD.to_string(),
            incarnation: 1,
            mode: runs::RunMode::Structured,
            evidence: runs_application::Evidence::passing("claude-code"),
            launcher_lock: RUN_LOCK.to_string(),
        })
        .await
        .unwrap();
    let mut daemon = daemon(&edge, Vec::new(), Plans::default(), LockProbe::Held);
    assert!(daemon
        .start_up(&edge.store, edge.catalog())
        .await
        .unwrap()
        .is_empty());
}

// T18: a child's engine is resolved by NAME through the registry the root
// composes, never one adapter named in code: an opencode child is planned
// on opencode, and, its plan failing, its incarnation ends `launch-failed`
// so nothing is left owed.
#[tokio::test]
async fn a_child_is_planned_on_its_own_engine_by_name() {
    let edge = Arc::new(TestEdge::new());
    mint(&edge, OWNER, "boss", "claude-code", None).await;
    mint(&edge, CHILD, "kid", "opencode", Some(OWNER)).await;
    let plans = Plans::default();
    let mut daemon = daemon(
        &edge,
        vec![hosted("claude-code"), hosted("opencode")],
        plans.clone(),
        LockProbe::Unproven,
    );
    assert_eq!(daemon.engines(), ["claude-code", "opencode"]);
    assert!(daemon.tick(&edge.store, edge.catalog(), false).await);
    assert_eq!(*plans.0.lock().unwrap(), ["opencode"]);
    assert_eq!(
        ended(&session_events(&edge, CHILD).await),
        [(1, "launch-failed".to_string())]
    );
    assert_eq!(daemon.hosting(), 0);
}

// A child on an engine the registry hosts none of is not planned: its
// incarnation ends `launch-failed`.
#[tokio::test]
async fn a_child_on_an_engine_with_no_child_engine_ends_its_incarnation() {
    let edge = Arc::new(TestEdge::new());
    mint(&edge, OWNER, "boss", "claude-code", None).await;
    mint(&edge, CHILD, "kid", "mock", Some(OWNER)).await;
    let plans = Plans::default();
    let mut daemon = daemon(
        &edge,
        vec![hosted("claude-code")],
        plans.clone(),
        LockProbe::Unproven,
    );
    daemon.tick(&edge.store, edge.catalog(), false).await;
    assert!(plans.0.lock().unwrap().is_empty(), "not planned");
    assert_eq!(
        ended(&session_events(&edge, CHILD).await),
        [(1, "launch-failed".to_string())]
    );
}

// While draining (a stop asked), no child is started: nothing is planned.
#[tokio::test]
async fn a_draining_daemon_starts_no_child() {
    let edge = Arc::new(TestEdge::new());
    mint(&edge, OWNER, "boss", "claude-code", None).await;
    mint(&edge, CHILD, "kid", "claude-code", Some(OWNER)).await;
    let plans = Plans::default();
    let mut daemon = daemon(
        &edge,
        vec![hosted("claude-code")],
        plans.clone(),
        LockProbe::Unproven,
    );
    daemon.tick(&edge.store, edge.catalog(), true).await;
    assert!(plans.0.lock().unwrap().is_empty());
    assert!(ended(&session_events(&edge, CHILD).await).is_empty());
}

/// Send `command` to `session`'s stream.
async fn command(edge: &TestEdge, session: &str, command: sessions::SessionCommand) {
    let book = CommandBook::command_of(session_cover(session), &command);
    edge.handle(book).await.unwrap();
}

/// A daemon over `OWNER`, which launched `CHILD` (an opencode child, whose
/// incarnation has ended) with a first task: the task is a WAKE, and a
/// child off claude takes an inform alone, so the session refuses it each
/// time it is sent. What the daemon logs.
async fn a_refused_first_task(
    edge: &Arc<TestEdge>,
) -> (Daemon<TestEdge, Counting>, Arc<Mutex<Vec<String>>>) {
    mint(edge, OWNER, "boss", "claude-code", None).await;
    mint(edge, CHILD, "kid", "opencode", Some(OWNER)).await;
    runs_application::Facts::new(Arc::clone(edge))
        .incarnation_ended(CHILD, 1, "runner-exit")
        .await
        .unwrap();
    command(
        edge,
        OWNER,
        sessions::SessionCommand::from(sessions::LaunchChild {
            child_project_id: "calm-otter".to_string(),
            child_session: CHILD.to_string(),
            child_root: "/work".to_string(),
            harp: "kid".to_string(),
            engine: "opencode".to_string(),
            trust: sessions::TargetTrust {
                same_project: true,
                trusted: false,
            },
            delegation_limit: 4,
            requester: None,
            prompt: "port the parser".to_string(),
        }),
    )
    .await;
    let said = Arc::new(Mutex::new(Vec::new()));
    let log = Arc::clone(&said);
    let daemon = daemon_saying(
        edge,
        Vec::new(),
        Plans::default(),
        LockProbe::Unproven,
        Box::new(move |line| log.lock().unwrap().push(line.to_string())),
    );
    (daemon, said)
}

/// How many times the child's first task was refused.
fn refusals(said: &Mutex<Vec<String>>) -> usize {
    let refused = format!("launch:{CHILD} refused");
    said.lock()
        .unwrap()
        .iter()
        .filter(|line| line.starts_with(&refused))
        .count()
}

// A step refused is not sent again while the store has not changed: a
// second round over the same store sends nothing.
#[tokio::test]
async fn a_refused_step_is_not_retried_while_the_store_is_unchanged() {
    let edge = Arc::new(TestEdge::new());
    let (mut daemon, said) = a_refused_first_task(&edge).await;
    assert!(daemon.tick(&edge.store, edge.catalog(), false).await);
    assert_eq!(refusals(&said), 1);
    assert!(!daemon.tick(&edge.store, edge.catalog(), false).await);
    assert_eq!(refusals(&said), 1, "not retried");
}

// A step refused is sent again once the store changed: a new event on a
// session the board folds (an inform to the child) clears the refusals.
#[tokio::test]
async fn a_refused_step_is_retried_once_the_store_changed() {
    let edge = Arc::new(TestEdge::new());
    let (mut daemon, said) = a_refused_first_task(&edge).await;
    daemon.tick(&edge.store, edge.catalog(), false).await;
    assert_eq!(refusals(&said), 1);
    command(
        &edge,
        CHILD,
        sessions::SessionCommand::from(sessions::SendMessage {
            message_id: "m1".to_string(),
            urgency: sessions::Urgency::Inform,
            text: "an inform".to_string(),
            requester: None,
            supported: vec![sessions::Urgency::Inform],
        }),
    )
    .await;
    assert!(daemon.tick(&edge.store, edge.catalog(), false).await);
    assert_eq!(refusals(&said), 2, "retried");
}
