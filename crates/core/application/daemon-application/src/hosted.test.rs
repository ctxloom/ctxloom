//! A child the launcher starts and hosts, over the real aggregates and fakes
//! of the engine, its control and its observations (T18): what it is
//! started with, what its observations record, how its turns are fed, and
//! how it ends.

use std::collections::{BTreeMap, VecDeque};
use std::sync::{Arc, Mutex};

use angzarr_embedded::v1::{event_page, CommandBook, Cover};
use angzarr_embedded::{CommandBookExt, Payload};
use library_application::{AuthSource, DryRun, Previewed, Source, Sourced};
use run_launcher::fakes::{session_cover, TestEdge};
use run_ports::{
    ChildControl, ChildEngine, ChildError, ChildMessage, ChildObservation, ChildSpec, Edge, Ended,
    Engine, EngineError, Exit, HomeFile, LockProbe, PrepareRequest, Prepared, PublicCert, Queued,
    RunLocks, StartedChild, Token, TokenSource, TurnCommand, TurnOutcome, TurnOutput, TurnRequest,
    Urgency as ChildUrgency,
};
use runs::{RunEvent, TurnUrgency};
use sessions::{SessionEvent, SessionRef, Urgency};

use crate::{ChildHomes, ChildPlans, Daemon, Ears, Folds, HandOffs, HostedEngine, Place, Ports};

const OWNER: &str = "5f0c7a52-1111-5111-8111-000000000001";
const CHILD: &str = "5f0c7a52-2222-5222-8222-000000000002";
const ENGINE: &str = "scripted";

/// What the child's control was asked.
#[derive(Default)]
struct Asked {
    sent: Vec<(String, ChildUrgency)>,
    clears: usize,
    ends: usize,
}

struct Control(Arc<Mutex<Asked>>);

impl ChildControl for Control {
    fn pid(&self) -> u32 {
        4242
    }

    fn send(&self, message: ChildMessage, urgency: ChildUrgency) -> Result<Queued, ChildError> {
        self.0.lock().unwrap().sent.push((message.id, urgency));
        Ok(Queued::Written)
    }

    fn interrupt(&self) -> Result<bool, ChildError> {
        Ok(false)
    }

    fn clear(&self) -> Result<Queued, ChildError> {
        self.0.lock().unwrap().clears += 1;
        Ok(Queued::Written)
    }

    fn end(&self, _: std::time::Duration) -> Result<Ended, ChildError> {
        self.0.lock().unwrap().ends += 1;
        Ok(Ended {
            exit: Exit {
                code: Some(0),
                signal: None,
            },
            undelivered: Vec::new(),
        })
    }
}

/// The child's observations, as a test pushes them: the iterator ends where
/// the queue is empty, and is read again later.
struct Said(Arc<Mutex<VecDeque<ChildObservation>>>);

impl Iterator for Said {
    type Item = ChildObservation;

    fn next(&mut self) -> Option<ChildObservation> {
        self.0.lock().unwrap().pop_front()
    }
}

/// An engine that prepares one file and starts the scripted child.
#[derive(Default)]
struct Scripted {
    asked: Arc<Mutex<Asked>>,
    said: Arc<Mutex<VecDeque<ChildObservation>>>,
    started: Mutex<Vec<ChildSpec>>,
}

impl Engine for Scripted {
    fn name(&self) -> &str {
        ENGINE
    }

    fn prepare(&self, request: &PrepareRequest) -> Result<Prepared, EngineError> {
        Ok(Prepared {
            files: vec![HomeFile {
                rel: "settings.json".to_string(),
                bytes: request.context.as_bytes().to_vec(),
            }],
            copy_in: Vec::new(),
            seeds: Vec::new(),
            left_out: Vec::new(),
            context_path: format!("{}/CONTEXT.md", request.home),
        })
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

impl ChildEngine for Scripted {
    fn start(&self, spec: &ChildSpec) -> Result<StartedChild, ChildError> {
        self.started.lock().unwrap().push(spec.clone());
        Ok(StartedChild {
            control: Box::new(Control(Arc::clone(&self.asked))),
            observations: Box::new(Said(Arc::clone(&self.said))),
        })
    }
}

/// A plan of the child on its own engine, asking no credential.
struct Plans;

impl ChildPlans for Plans {
    fn preview(
        &self,
        engine: &str,
        _: &str,
        _: &BTreeMap<String, String>,
    ) -> Result<Previewed, String> {
        Ok(Previewed {
            plan: DryRun {
                agent: String::new(),
                workspace: String::new(),
                runtime: String::new(),
                engine: engine.to_string(),
                profiles: Vec::new(),
                fragments: Vec::new(),
                context: "THE-CONTEXT".to_string(),
                tokens: 0,
                prompt: String::new(),
                findings: Vec::new(),
                axes: (String::new(), "host".to_string()),
                model: "opus".to_string(),
                effort: String::new(),
                permissions: None,
                drive: library_application::Drive {
                    auth: Sourced {
                        value: AuthSource::None,
                        source: Source::Default,
                    },
                    ..library_application::Drive::default()
                },
                settings: None,
                settings_agent: String::new(),
            },
            fatal: Vec::new(),
        })
    }
}

/// The launcher's hand-off: one environment, taken once.
struct HandOff(Mutex<Option<BTreeMap<String, String>>>);

impl HandOffs for HandOff {
    fn take(&self, _: &str) -> Option<BTreeMap<String, String>> {
        self.0.lock().unwrap().take()
    }
}

#[derive(Clone, Default)]
struct Homes(Arc<Mutex<Vec<(String, String, String)>>>);

impl ChildHomes for Homes {
    fn home(&self, project_dir: &str, harp: &str) -> String {
        format!("{project_dir}/.ctxloom/sessions/{harp}/home")
    }

    fn write(&self, project_dir: &str, harp: &str, rel: &str, _: &[u8]) -> Result<(), String> {
        self.0
            .lock()
            .unwrap()
            .push((project_dir.to_string(), harp.to_string(), rel.to_string()));
        Ok(())
    }
}

/// A child's observations, as the launcher hands them to its ears.
type Observations = Box<dyn Iterator<Item = ChildObservation> + Send>;

/// Ears that read each child's observations when asked.
#[derive(Default)]
struct Listening(Mutex<Vec<(String, Observations)>>);

impl Ears for Listening {
    fn listen(&self, run: String, observations: Box<dyn Iterator<Item = ChildObservation> + Send>) {
        self.0.lock().unwrap().push((run, observations));
    }

    fn heard(&self) -> Vec<(String, ChildObservation)> {
        let mut heard = Vec::new();
        for (run, observations) in self.0.lock().unwrap().iter_mut() {
            heard.extend(observations.map(|o| (run.clone(), o)));
        }
        heard
    }
}

struct Locks;

impl RunLocks for Locks {
    fn hold_run(&self, run: &str) -> Result<run_ports::HeldLock, run_ports::LockError> {
        Ok(run_ports::HeldLock::new(format!("/locks/{run}.lock"), ()))
    }

    fn probe(&self, _: &str) -> LockProbe {
        LockProbe::Unproven
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

struct World {
    edge: Arc<TestEdge>,
    engine: Arc<Scripted>,
    homes: Homes,
    daemon: Daemon<TestEdge, Counting>,
}

impl World {
    fn new(plans: Box<dyn ChildPlans>) -> World {
        let edge = Arc::new(TestEdge::new());
        let engine = Arc::new(Scripted::default());
        let homes = Homes::default();
        let daemon = Daemon::new(Ports {
            edge: Arc::clone(&edge),
            tokens: Arc::new(Counting::default()),
            engines: vec![HostedEngine {
                engine: engine.clone(),
                child: engine.clone(),
                transcripts: Arc::new(|_| Some("/t".to_string())),
            }],
            plans,
            hand_offs: Box::new(HandOff(Mutex::new(Some(BTreeMap::from([
                ("PATH".to_string(), "/usr/bin".to_string()),
                ("SECRET".to_string(), "v".to_string()),
            ]))))),
            homes: Box::new(homes.clone()),
            ears: Box::new(Listening::default()),
            run_locks: Arc::new(Locks),
            place: Place {
                home: "/home/u".to_string(),
                program_dir: Some("/opt/ctx".to_string()),
                os: "linux".to_string(),
                user_home: None,
                endpoint: "https://127.0.0.1:1".to_string(),
                cert: PublicCert::from_der(vec![0x30, 0x00]).unwrap(),
            },
            folds: Folds::default(),
            say: Box::new(|_| {}),
        });
        World {
            edge,
            engine,
            homes,
            daemon,
        }
    }

    async fn tick(&mut self) -> bool {
        self.daemon
            .tick(&self.edge.store, self.edge.catalog(), false)
            .await
    }

    async fn handle(&self, session: &str, command: impl Into<sessions::SessionCommand>) {
        let book = CommandBook::command_of(session_cover(session), &command.into());
        self.edge.handle(book).await.unwrap();
    }

    async fn mint(&self, session: &str, harp: &str, owner: Option<&str>) {
        self.handle(
            session,
            sessions::MintSession {
                harp: harp.to_string(),
                project_id: "calm-otter".to_string(),
                project_dir: "/work".to_string(),
                output_dir: "/work/out".to_string(),
                engine: ENGINE.to_string(),
                origin: sessions::Origin::Agent,
                started_at: None,
                depth: u32::from(owner.is_some()),
                owner: owner.map(here),
            },
        )
        .await;
    }

    /// A child of the owner, hosted.
    async fn hosting() -> World {
        let mut world = World::new(Box::new(Plans));
        world.mint(OWNER, "boss", None).await;
        world.mint(CHILD, "kid", Some(OWNER)).await;
        world.tick().await;
        assert_eq!(world.daemon.hosting(), 1, "hosted");
        world
    }

    fn say(&self, observation: ChildObservation) {
        self.engine.said.lock().unwrap().push_back(observation);
    }

    async fn session_events(&self, session: &str) -> Vec<SessionEvent> {
        events(&self.edge, &session_cover(session))
            .await
            .into_iter()
            .map(|any| SessionEvent::from_any(&any).unwrap())
            .collect()
    }

    async fn run_events(&self) -> Vec<RunEvent> {
        let cover = Cover {
            domain: runs::RUN_DOMAIN.to_string(),
            root: Some(runs::run_root(CHILD, 1)),
            edition: None,
        };
        events(&self.edge, &cover)
            .await
            .into_iter()
            .map(|any| RunEvent::from_any(&any).unwrap())
            .collect()
    }
}

fn here(session: &str) -> SessionRef {
    SessionRef {
        project_id: "calm-otter".to_string(),
        session: session.to_string(),
    }
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

// T18: a child is started on its engine, its session home prepared in its
// project, with its launcher's environment, ctxloom's home, `PATH` led by the
// program's directory, and the run's own hook variables; its start is
// recorded on its run with the pid the spawn gave.
#[tokio::test]
async fn a_child_is_started_with_its_launchers_environment_and_the_runs_hooks() {
    let world = World::hosting().await;
    let started = world.engine.started.lock().unwrap().clone();
    let [spec] = started.as_slice() else {
        panic!("one start: {started:?}");
    };
    assert_eq!(spec.home, "/work/.ctxloom/sessions/kid/home");
    assert_eq!(spec.model, "opus");
    assert_eq!(spec.env.get("SECRET").map(String::as_str), Some("v"));
    assert_eq!(
        spec.env.get("PATH").map(String::as_str),
        Some("/opt/ctx:/usr/bin")
    );
    assert_eq!(
        spec.env.get(run_ports::HOME_VARIABLE).map(String::as_str),
        Some("/home/u")
    );
    assert_eq!(
        spec.env.get(run_ports::HARP_VARIABLE).map(String::as_str),
        Some("kid")
    );
    assert_eq!(
        spec.env
            .get(run_ports::SESSION_VARIABLE)
            .map(String::as_str),
        Some(CHILD)
    );
    assert!(spec.env.contains_key(run_ports::BEARER_VARIABLE));
    assert_eq!(
        *world.homes.0.lock().unwrap(),
        [(
            "/work".to_string(),
            "kid".to_string(),
            "home/settings.json".to_string()
        )]
    );
    let started: Vec<u32> = world
        .run_events()
        .await
        .into_iter()
        .filter_map(|event| match event {
            RunEvent::RunnerStartedRecorded(started) => Some(started.pid),
            _ => None,
        })
        .collect();
    assert_eq!(started, [4242], "recorded on the child's own run");
}

/// The spec a child is started with, by a daemon on `os` with the user's
/// home `user_home`, whose launcher sent `path` as its `PATH`.
async fn started_on(os: &str, path: &str, user_home: Option<&str>) -> run_ports::ChildSpec {
    let mut world = World::new(Box::new(Plans));
    world.daemon = Daemon::new(Ports {
        edge: Arc::clone(&world.edge),
        tokens: Arc::new(Counting::default()),
        engines: vec![HostedEngine {
            engine: world.engine.clone(),
            child: world.engine.clone(),
            transcripts: Arc::new(|_| None),
        }],
        plans: Box::new(Plans),
        hand_offs: Box::new(HandOff(Mutex::new(Some(BTreeMap::from([(
            "PATH".to_string(),
            path.to_string(),
        )]))))),
        homes: Box::new(Homes::default()),
        ears: Box::new(Listening::default()),
        run_locks: Arc::new(Locks),
        place: Place {
            home: "/home/u".to_string(),
            program_dir: Some("/opt/ctx".to_string()),
            os: os.to_string(),
            user_home: user_home.map(str::to_string),
            endpoint: "https://127.0.0.1:1".to_string(),
            cert: PublicCert::from_der(vec![0x30, 0x00]).unwrap(),
        },
        folds: Folds::default(),
        say: Box::new(|_| {}),
    });
    world.mint(OWNER, "boss", None).await;
    world.mint(CHILD, "kid", Some(OWNER)).await;
    world.tick().await;
    let started = world.engine.started.lock().unwrap().clone();
    started[0].clone()
}

// A launcher that sent no `PATH`: the program's directory is the whole of it.
#[tokio::test]
async fn a_child_given_no_path_is_given_the_programs_directory_alone() {
    let spec = started_on("linux", "", None).await;
    assert_eq!(spec.env.get("PATH").map(String::as_str), Some("/opt/ctx"));
    assert_eq!(spec.user_home, None);
}

// On Windows the program's directory leads the `PATH` joined as Windows
// joins it, and the user's home the root resolved (`USERPROFILE` there) is
// the child's, as a value, never its environment's `HOME`.
#[tokio::test]
async fn a_childs_path_is_joined_as_its_platform_joins_it_and_it_is_told_her_home() {
    let spec = started_on("windows", r"C:\Windows", Some(r"C:\Users\her")).await;
    assert_eq!(
        spec.env.get("PATH").map(String::as_str),
        Some(r"/opt/ctx;C:\Windows")
    );
    assert_eq!(spec.user_home.as_deref(), Some(r"C:\Users\her"));
}

// What the child says is recorded as its run's facts: its engine's session
// bound (its transcript where the engine keeps them), and its exit, which
// ends its run and its incarnation and lets it go. A round that heard
// something did something; one that heard nothing and had nothing to do
// did not.
#[tokio::test]
async fn what_a_child_says_is_recorded_and_its_exit_ends_it() {
    let mut world = World::hosting().await;
    assert!(!world.tick().await, "nothing heard, nothing to do");
    world.say(ChildObservation::Started {
        native_id: "n1".to_string(),
    });
    assert!(world.tick().await, "heard");
    let bound: Vec<(String, String)> = world
        .session_events(CHILD)
        .await
        .into_iter()
        .filter_map(|event| match event {
            SessionEvent::EngineSessionBound(bound) => {
                Some((bound.native_id, bound.transcript_path))
            }
            _ => None,
        })
        .collect();
    assert_eq!(bound, [("n1".to_string(), "/t/n1.jsonl".to_string())]);
    world.say(ChildObservation::Exited {
        exit: Exit {
            code: None,
            signal: Some(9),
        },
        detail: String::new(),
    });
    world.tick().await;
    let exited: Vec<(runs::EndCause, Option<i32>)> = world
        .run_events()
        .await
        .into_iter()
        .filter_map(|event| match event {
            RunEvent::RunEnded(ended) => Some((ended.cause, ended.exit_code)),
            _ => None,
        })
        .collect();
    // Killed by a signal: no code of its own, recorded as -1.
    assert_eq!(exited, [(runs::EndCause::RunnerExit, Some(-1))]);
    let ended: Vec<(u32, String)> = world
        .session_events(CHILD)
        .await
        .into_iter()
        .filter_map(|event| match event {
            SessionEvent::IncarnationEnded(ended) => Some((ended.incarnation, ended.cause)),
            _ => None,
        })
        .collect();
    assert_eq!(ended, [(1, "runner-exit".to_string())]);
    assert_eq!(world.daemon.hosting(), 0, "let go");
}

// A turn queued on the hosted run is fed to its child once, the informs it
// carries riding before the message that starts it.
#[tokio::test]
async fn a_queued_turn_is_fed_informs_first() {
    let mut world = World::hosting().await;
    let send = |id: &str, urgency| sessions::SendMessage {
        message_id: id.to_string(),
        urgency,
        text: format!("text of {id}"),
        requester: None,
        supported: vec![Urgency::Inform, Urgency::Wake, Urgency::Steer],
    };
    world.handle(CHILD, send("m-wake", Urgency::Wake)).await;
    world.handle(CHILD, send("m-inform", Urgency::Inform)).await;
    for _ in 0..3 {
        world.tick().await;
    }
    assert_eq!(
        world.engine.asked.lock().unwrap().sent,
        [
            ("m-inform".to_string(), ChildUrgency::Inform),
            ("m-wake".to_string(), ChildUrgency::Wake),
        ]
    );
}

// The daemon exiting ends every child it hosts.
#[tokio::test]
async fn ending_all_ends_each_hosted_child() {
    let mut world = World::hosting().await;
    world.daemon.end_all();
    assert_eq!(world.engine.asked.lock().unwrap().ends, 1);
}

// K2's compensation: a launched child that cannot be planned, on its first
// incarnation, is a launch its owner is told failed.
#[tokio::test]
async fn an_owner_is_told_its_childs_launch_failed() {
    struct Failing;
    impl ChildPlans for Failing {
        fn preview(
            &self,
            _: &str,
            _: &str,
            _: &BTreeMap<String, String>,
        ) -> Result<Previewed, String> {
            Err("library-application-no-agent".to_string())
        }
    }
    let mut world = World::new(Box::new(Failing));
    world.mint(OWNER, "boss", None).await;
    world
        .handle(
            OWNER,
            sessions::LaunchChild {
                child_project_id: "calm-otter".to_string(),
                child_session: CHILD.to_string(),
                child_root: "/work".to_string(),
                harp: "kid".to_string(),
                engine: ENGINE.to_string(),
                trust: sessions::TargetTrust {
                    same_project: true,
                    trusted: true,
                },
                delegation_limit: 1,
                requester: None,
                prompt: String::new(),
            },
        )
        .await;
    for _ in 0..3 {
        world.tick().await;
    }
    let failed = world
        .session_events(OWNER)
        .await
        .into_iter()
        .any(|event| matches!(event, SessionEvent::ChildLaunchFailed(_)));
    assert!(failed, "{:?}", world.session_events(OWNER).await);
}

// A message's urgency as the child takes it within its turn.
#[test]
fn a_messages_urgency_is_the_childs_within_its_turn() {
    use crate::daemon::child_urgency;
    assert_eq!(
        child_urgency(Urgency::Inform, TurnUrgency::Steer),
        ChildUrgency::Inform
    );
    assert_eq!(
        child_urgency(Urgency::Wake, TurnUrgency::Steer),
        ChildUrgency::Steer
    );
    assert_eq!(
        child_urgency(Urgency::Steer, TurnUrgency::Wake),
        ChildUrgency::Steer
    );
    assert_eq!(
        child_urgency(Urgency::Wake, TurnUrgency::Wake),
        ChildUrgency::Wake
    );
}

// GetDaemon (slice 2 step 5): the runs it hosts, each by its session and
// its run.
#[tokio::test]
async fn the_hosted_runs_are_named_by_session_and_run() {
    let world = World::hosting().await;
    assert_eq!(
        world.daemon.hosted_runs(),
        [(CHILD.to_string(), daemon_board::run_text(CHILD, 1))]
    );
}
