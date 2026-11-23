//! A child the launcher starts and hosts, over the real aggregates and fakes
//! of the engine, its control and its observations (T18): what it is
//! started with, what its observations record, how its turns are fed, and
//! how it ends.

use std::collections::{BTreeMap, BTreeSet, VecDeque};
use std::sync::{Arc, Mutex};

use angzarr_embedded::v1::{event_page, CommandBook, Cover};
use angzarr_embedded::{CommandBookExt, Payload};
use library_application::{AuthSource, DryRun, Previewed, Source, Sourced};
use run_launcher::fakes::{session_cover, TestEdge};
use run_ports::{
    ChildControl, ChildEngine, ChildError, ChildMessage, ChildObservation, ChildSpec, Edge, Ended,
    Engine, EngineError, Exit, HomeFile, LockProbe, PrepareRequest, Prepared, Queued, RunLocks,
    StartedChild, TurnCommand, TurnOutcome, TurnOutput, TurnRequest, Urgency as ChildUrgency,
};
use runs::{RunEvent, TurnUrgency};
use sessions::{SessionEvent, SessionRef, Urgency};

use crate::{ChildHomes, ChildPlans, Daemon, Ears, Folds, HandOffs, HostedEngine, Place, Ports};

const OWNER: &str = "5f0c7a52-1111-5111-8111-000000000001";
const CHILD: &str = "5f0c7a52-2222-5222-8222-000000000002";
/// The child's resource name: its project's harp (named in `hosting`) and
/// its own.
const CHILD_NAME: &str = "projects/calm-quiet-heron/sessions/kid";
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
    /// What the latest child started will say: each start has a queue of its
    /// own (a restart's second incarnation hears none of the first's).
    said: Mutex<Arc<Mutex<VecDeque<ChildObservation>>>>,
    started: Mutex<Vec<ChildSpec>>,
    /// The launch each start was handed (T10).
    launches: Mutex<Vec<run_ports::Launch>>,
}

impl Engine for Scripted {
    fn name(&self) -> &str {
        ENGINE
    }

    /// Its settings (the context), a merge into what its home holds of
    /// `state.json` (as claude's into `.claude.json`), and two seeds.
    fn prepare(&self, request: &PrepareRequest) -> Result<Prepared, EngineError> {
        let mut state = request
            .existing
            .get("state.json")
            .cloned()
            .unwrap_or_default();
        state.extend_from_slice(b"+merged");
        let seed = |rel: &str| HomeFile {
            executable: false,
            rel: rel.to_string(),
            bytes: b"seeded".to_vec(),
        };
        let mut files = vec![
            HomeFile {
                executable: false,
                rel: "settings.json".to_string(),
                bytes: request.context.as_bytes().to_vec(),
            },
            HomeFile {
                executable: false,
                rel: "state.json".to_string(),
                bytes: state,
            },
        ];
        if let Some(permissions) = &request.permissions {
            files.push(HomeFile {
                executable: false,
                rel: "permissions.json".to_string(),
                bytes: permissions.as_bytes().to_vec(),
            });
        }
        let denied = &request.surfaces.0.deny_tools;
        if !denied.is_empty() {
            files.push(HomeFile {
                executable: false,
                rel: "denied.txt".to_string(),
                bytes: denied.join(",").into_bytes(),
            });
        }
        Ok(Prepared {
            files,
            copy_in: Vec::new(),
            seeds: vec![seed("model.json"), seed("kept.json")],
            left_out: Vec::new(),
            context_path: format!("{}/CONTEXT.md", request.home),
            workspace_files: Vec::new(),
        })
    }

    fn merges(&self) -> &'static [&'static str] {
        &["state.json"]
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
    /// Its spec's environment as it is, under the configured program.
    fn command(&self, spec: &ChildSpec) -> Result<TurnCommand, ChildError> {
        let mut command = TurnCommand::new(ENGINE);
        command.env = spec.env.clone();
        command.cwd = spec.cwd.clone();
        Ok(command)
    }

    fn stdio(&self) -> run_ports::Stdio {
        run_ports::Stdio::StreamJson
    }

    fn start(
        &self,
        spec: &ChildSpec,
        launch: &run_ports::Launch,
    ) -> Result<StartedChild, ChildError> {
        self.started.lock().unwrap().push(spec.clone());
        self.launches.lock().unwrap().push(launch.clone());
        let queue = Arc::new(Mutex::new(VecDeque::new()));
        *self.said.lock().unwrap() = Arc::clone(&queue);
        Ok(StartedChild {
            control: Box::new(Control(Arc::clone(&self.asked))),
            observations: Box::new(Said(queue)),
        })
    }
}

/// A plan of the child on its own engine, asking no credential.
struct Plans;

impl ChildPlans for Plans {
    fn output_root(
        &self,
        project_dir: &str,
        _: &BTreeMap<String, String>,
    ) -> Result<String, String> {
        Ok(format!("{project_dir}/the-outputs"))
    }

    fn preview(
        &self,
        engine: &str,
        _agent: &str,
        _: &str,
        _: &BTreeMap<String, String>,
        _trusted: bool,
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
                surfaces: Default::default(),
                untrusted: Vec::new(),
            },
            fatal: Vec::new(),
        })
    }

    fn git_user(&self, _: &str, _: &BTreeMap<String, String>) -> Option<run_ports::GitUser> {
        Some(run_ports::GitUser {
            name: "Alice".to_string(),
            email: "alice@example.com".to_string(),
        })
    }
}

/// The child's hand-off (T6): read at each start, until it is deleted.
struct Handed(Mutex<Option<crate::HandOff>>);

impl Handed {
    /// A hand-off of the child's environment `env`, its launcher saying
    /// nothing of its user's settings.
    fn of(env: BTreeMap<String, String>) -> Handed {
        Handed::with(env, BTreeMap::new())
    }

    /// A hand-off of `env` whose launcher ran `program`.
    fn programmed(env: BTreeMap<String, String>, program: Option<crate::Program>) -> Handed {
        Handed(Mutex::new(Some(crate::HandOff {
            env,
            launcher: BTreeMap::new(),
            agent: String::new(),
            program,
        })))
    }

    fn with(env: BTreeMap<String, String>, launcher: BTreeMap<String, String>) -> Handed {
        Handed(Mutex::new(Some(crate::HandOff {
            env,
            launcher,
            agent: String::new(),
            program: None,
        })))
    }
}

impl HandOffs for Handed {
    fn read(&self, _: &str) -> Option<crate::HandOff> {
        self.0.lock().unwrap().clone()
    }

    fn delete(&self, _: &str) {
        self.0.lock().unwrap().take();
    }

    fn claim(&self, _: &str) {}

    fn list(&self) -> Vec<crate::Kept> {
        Vec::new()
    }
}

/// A file of a project's session directories: its project, harp and path.
type HomeKey = (String, String, String);

/// The session locks held, by `<project>/<harp>`, shared by every clone.
type SessionsHeld = Arc<Mutex<BTreeSet<String>>>;

/// Every project's session directories, in memory, shared by every clone,
/// and the session locks held there.
#[derive(Clone, Default)]
struct Homes(Arc<Mutex<BTreeMap<HomeKey, Vec<u8>>>>, SessionsHeld);

/// A session lock [`Homes`] holds: let go when dropped.
struct HeldSession(SessionsHeld, String);

impl Drop for HeldSession {
    fn drop(&mut self) {
        self.0.lock().unwrap().remove(&self.1);
    }
}

impl Homes {
    fn key(project_dir: &str, harp: &str, rel: &str) -> HomeKey {
        (project_dir.to_string(), harp.to_string(), rel.to_string())
    }

    fn put(&self, project_dir: &str, harp: &str, rel: &str, bytes: &[u8]) {
        self.0
            .lock()
            .unwrap()
            .insert(Homes::key(project_dir, harp, rel), bytes.to_vec());
    }

    fn text(&self, project_dir: &str, harp: &str, rel: &str) -> Option<String> {
        self.0
            .lock()
            .unwrap()
            .get(&Homes::key(project_dir, harp, rel))
            .map(|bytes| String::from_utf8_lossy(bytes).into_owned())
    }
}

/// One project's session directories of [`Homes`].
struct ProjectHomes(Homes, String);

impl run_ports::SessionRecords for ProjectHomes {
    fn dir(&self, harp: &str) -> String {
        format!("{}/.ctxloom/sessions/{harp}", self.1)
    }

    fn write(
        &mut self,
        harp: &str,
        rel: &str,
        bytes: &[u8],
    ) -> Result<(), run_ports::RecordsError> {
        self.0.put(&self.1, harp, rel, bytes);
        Ok(())
    }

    fn write_executable(
        &mut self,
        harp: &str,
        rel: &str,
        bytes: &[u8],
    ) -> Result<(), run_ports::RecordsError> {
        self.write(harp, rel, bytes)
    }

    fn append(
        &mut self,
        harp: &str,
        rel: &str,
        bytes: &[u8],
    ) -> Result<(), run_ports::RecordsError> {
        let mut all = self
            .0
            .text(&self.1, harp, rel)
            .unwrap_or_default()
            .into_bytes();
        all.extend_from_slice(bytes);
        self.0.put(&self.1, harp, rel, &all);
        Ok(())
    }

    fn read(&mut self, harp: &str, rel: &str) -> Result<Option<Vec<u8>>, run_ports::RecordsError> {
        Ok(self
            .0
             .0
            .lock()
            .unwrap()
            .get(&Homes::key(&self.1, harp, rel))
            .cloned())
    }

    fn exists(&mut self, harp: &str) -> bool {
        self.0
             .0
            .lock()
            .unwrap()
            .keys()
            .any(|(p, h, _)| *p == self.1 && h == harp)
    }
}

impl ChildHomes for Homes {
    fn records(&self, project_dir: &str) -> Box<dyn run_ports::SessionRecords> {
        Box::new(ProjectHomes(self.clone(), project_dir.to_string()))
    }

    fn hold_session(&self, project_dir: &str, harp: &str) -> Result<run_ports::HeldLock, String> {
        let key = format!("{project_dir}/{harp}");
        if !self.1.lock().unwrap().insert(key.clone()) {
            return Err(format!("{key} is held"));
        }
        Ok(run_ports::HeldLock::new(
            format!("{project_dir}/.ctxloom/sessions/{harp}/session.lock"),
            HeldSession(Arc::clone(&self.1), key),
        ))
    }
}

/// The locks as the files would say them once a session's lock file was
/// left free by an earlier run (a CLI `run --session` of the child, say):
/// every run lock held (the daemon holds its children's), and a session's
/// held only while [`Homes`] holds it.
struct LeftLocks(SessionsHeld);

impl RunLocks for LeftLocks {
    fn hold_run(&self, run: &str) -> Result<run_ports::HeldLock, run_ports::LockError> {
        Ok(run_ports::HeldLock::new(format!("/locks/{run}.lock"), ()))
    }

    fn probe(&self, _: &str) -> LockProbe {
        LockProbe::Held
    }

    fn session(&self, project_dir: &str, harp: &str) -> LockProbe {
        match self
            .0
            .lock()
            .unwrap()
            .contains(&format!("{project_dir}/{harp}"))
        {
            true => LockProbe::Held,
            false => LockProbe::Free,
        }
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

/// Every lock probes as told: unproven, unless a test says otherwise.
struct Locks(LockProbe);

impl RunLocks for Locks {
    fn hold_run(&self, run: &str) -> Result<run_ports::HeldLock, run_ports::LockError> {
        Ok(run_ports::HeldLock::new(format!("/locks/{run}.lock"), ()))
    }

    fn probe(&self, _: &str) -> LockProbe {
        self.0
    }

    fn session(&self, _: &str, _: &str) -> LockProbe {
        self.0
    }
}

struct World {
    edge: Arc<TestEdge>,
    engine: Arc<Scripted>,
    homes: Homes,
    daemon: Daemon<TestEdge>,
    /// The daemon's clock, which a test moves (OD-54).
    clock: Moving,
    /// What the daemon said in its log.
    said: Arc<Mutex<Vec<String>>>,
}

/// A clock a test moves by hand: never the time of day.
#[derive(Clone, Default)]
struct Moving(Arc<Mutex<std::time::Duration>>);

impl Moving {
    fn pass(&self, by: std::time::Duration) {
        *self.0.lock().unwrap() += by;
    }
}

impl crate::Monotonic for Moving {
    fn now(&self) -> std::time::Duration {
        *self.0.lock().unwrap()
    }
}

impl World {
    fn new(plans: Box<dyn ChildPlans>) -> World {
        World::probing(plans, LockProbe::Unproven)
    }

    /// A world whose every lock probes as `locks`.
    fn probing(plans: Box<dyn ChildPlans>, locks: LockProbe) -> World {
        World::built(
            plans,
            locks,
            Box::new(Handed::of(BTreeMap::from([
                ("PATH".to_string(), "/usr/bin".to_string()),
                ("SECRET".to_string(), "v".to_string()),
            ]))),
        )
    }

    /// A world whose children's hand-offs are `hand_offs`.
    fn built(plans: Box<dyn ChildPlans>, locks: LockProbe, hand_offs: Box<dyn HandOffs>) -> World {
        World::built_within(plans, locks, hand_offs, None)
    }

    /// [`World::built`], its hand-over's period `period` when one is given.
    fn built_within(
        plans: Box<dyn ChildPlans>,
        locks: LockProbe,
        hand_offs: Box<dyn HandOffs>,
        period: Option<std::time::Duration>,
    ) -> World {
        let edge = Arc::new(TestEdge::new());
        let engine = Arc::new(Scripted::default());
        let homes = Homes::default();
        let clock = Moving::default();
        let said = Arc::new(Mutex::new(Vec::new()));
        let log = Arc::clone(&said);
        let daemon = Daemon::new(Ports {
            edge: Arc::clone(&edge),
            engines: vec![HostedEngine {
                engine: engine.clone(),
                child: engine.clone(),
                transcripts: Arc::new(|_| Some("/t".to_string())),
                for_launch: None,
            }],
            plans,
            hand_offs,
            homes: Box::new(homes.clone()),
            ears: Box::new(Listening::default()),
            run_locks: Arc::new(Locks(locks)),
            place: Place {
                home: "/home/u".to_string(),
                program_dir: Some("/opt/ctx".to_string()),
                os: "linux".to_string(),
                user_home: None,
            },
            folds: Folds::default(),
            say: Box::new(move |line: &str| log.lock().unwrap().push(line.to_string())),
        })
        .clocked(Arc::new(clock.clone()));
        let daemon = match period {
            Some(period) => daemon.handing_over_within(period),
            None => daemon,
        };
        World {
            edge,
            engine,
            homes,
            daemon,
            clock,
            said,
        }
    }

    async fn tick(&mut self) -> bool {
        self.daemon
            .tick(&self.edge.store, self.edge.catalog(), false)
            .await
    }

    /// The project named `calm-quiet-heron`, as a registration names it.
    async fn name_project(&self) {
        let named = sessions::ProjectCommand::from(sessions::NameProject {
            project_id: "calm-otter".to_string(),
            harp: "calm-quiet-heron".to_string(),
        });
        let cover = Cover {
            domain: sessions::PROJECTS_DOMAIN.to_string(),
            root: Some(sessions::project_id_root("calm-otter")),
            edition: None,
        };
        self.edge
            .handle(CommandBook::command_of(cover, &named))
            .await
            .unwrap();
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
                workspace: None,
            },
        )
        .await;
    }

    /// A child of the owner, hosted.
    async fn hosting() -> World {
        let mut world = World::new(Box::new(Plans));
        world.name_project().await;
        world.mint(OWNER, "boss", None).await;
        world.mint(CHILD, "kid", Some(OWNER)).await;
        world.tick().await;
        assert_eq!(world.daemon.hosting(), 1, "hosted");
        world
    }

    fn say(&self, observation: ChildObservation) {
        self.engine
            .said
            .lock()
            .unwrap()
            .lock()
            .unwrap()
            .push_back(observation);
    }

    async fn session_events(&self, session: &str) -> Vec<SessionEvent> {
        events(&self.edge, &session_cover(session))
            .await
            .into_iter()
            .map(|any| SessionEvent::from_any(&any).unwrap())
            .collect()
    }

    async fn run_events(&self) -> Vec<RunEvent> {
        self.events_of_run(1).await
    }

    /// The events of the run of the child's `incarnation`.
    async fn events_of_run(&self, incarnation: u32) -> Vec<RunEvent> {
        let cover = Cover {
            domain: runs::RUN_DOMAIN.to_string(),
            root: Some(runs::run_root(CHILD, incarnation)),
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
        spec.env
            .get(run_ports::DAEMON_ROOT_VARIABLE)
            .map(String::as_str),
        Some("/home/u")
    );
    // s3-Q3: the one variable is the child's resource name; no harp
    // variable beside it.
    assert!(!spec.env.contains_key("CTXLOOM_SESSION_HARP"));
    assert_eq!(
        spec.env
            .get(run_ports::SESSION_VARIABLE)
            .map(String::as_str),
        Some(CHILD_NAME)
    );
    // Slice 4 U7: its hooks are told its run, and no listener (they find
    // the daemon by its discovery file, with the user bearer).
    assert!(spec.env.contains_key(run_ports::RUN_VARIABLE));
    for name in run_ports::LISTENER_VARIABLES {
        assert!(!spec.env.contains_key(name), "{name}");
    }
    assert_eq!(
        world
            .homes
            .text("/work", "kid", "home/settings.json")
            .as_deref(),
        Some("THE-CONTEXT")
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
        engines: vec![HostedEngine {
            engine: world.engine.clone(),
            child: world.engine.clone(),
            transcripts: Arc::new(|_| None),
            for_launch: None,
        }],
        plans: Box::new(Plans),
        hand_offs: Box::new(Handed::of(BTreeMap::from([(
            "PATH".to_string(),
            path.to_string(),
        )]))),
        homes: Box::new(Homes::default()),
        ears: Box::new(Listening::default()),
        run_locks: Arc::new(Locks(LockProbe::Unproven)),
        place: Place {
            home: "/home/u".to_string(),
            program_dir: Some("/opt/ctx".to_string()),
            os: os.to_string(),
            user_home: user_home.map(str::to_string),
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

/// A finder that finds every program at `/found/<name>`; it starts nothing.
struct Finds;

impl run_ports::RunnerLauncher for Finds {
    fn launch(
        &self,
        _: &TurnCommand,
        _: &run_ports::Limits,
    ) -> Result<Box<dyn run_ports::Running>, run_ports::LaunchFailure> {
        unreachable!("a child is started by its engine")
    }

    fn launch_terminal(
        &self,
        _: &TurnCommand,
    ) -> Result<Box<dyn run_ports::Running>, run_ports::LaunchFailure> {
        unreachable!("a child is started by its engine")
    }

    fn find_program(&self, command: &TurnCommand) -> Result<String, run_ports::ProgramNotFound> {
        Ok(format!("/found/{}", command.program))
    }
}

// T10: a hosted child is made by the one launch: its engine's command with
// the launcher's permits applied (never over the HOME and PATH the daemon
// sets), its program found, and it is started as made.
#[tokio::test]
async fn a_hosted_childs_launch_has_its_program_found_and_its_permits_applied() {
    let mut world = World::new(Box::new(Plans));
    world.daemon = Daemon::new(Ports {
        edge: Arc::clone(&world.edge),
        engines: vec![HostedEngine {
            engine: world.engine.clone(),
            child: world.engine.clone(),
            transcripts: Arc::new(|_| None),
            for_launch: None,
        }],
        plans: Box::new(Plans),
        hand_offs: Box::new(Handed::of(BTreeMap::from([
            ("PATH".to_string(), "/usr/bin".to_string()),
            ("CTXLOOM_PERMIT_ACME_TOKEN".to_string(), "acme".to_string()),
            ("CTXLOOM_PERMIT_PATH".to_string(), "/evil".to_string()),
        ]))),
        homes: Box::new(Homes::default()),
        ears: Box::new(Listening::default()),
        run_locks: Arc::new(Locks(LockProbe::Unproven)),
        place: Place {
            home: "/home/u".to_string(),
            program_dir: Some("/opt/ctx".to_string()),
            os: "linux".to_string(),
            user_home: None,
        },
        folds: Folds::default(),
        say: Box::new(|_| {}),
    })
    .finding(Arc::new(Finds));
    world.mint(OWNER, "boss", None).await;
    world.mint(CHILD, "kid", Some(OWNER)).await;
    world.tick().await;
    let launches = world.engine.launches.lock().unwrap().clone();
    let [launch] = launches.as_slice() else {
        panic!("one start: {launches:?}");
    };
    assert_eq!(launch.kind, run_ports::LaunchKind::Child);
    assert_eq!(launch.program, format!("/found/{ENGINE}"));
    assert_eq!(
        launch.env.get("ACME_TOKEN").map(String::as_str),
        Some("acme")
    );
    assert_eq!(
        launch.env.get("PATH").map(String::as_str),
        Some("/opt/ctx:/usr/bin")
    );
    assert!(launch.env.keys().all(|n| !n.starts_with("CTXLOOM_PERMIT_")));
    assert_eq!(launch.session_home, "/work/.ctxloom/sessions/kid/home");
    assert_eq!(launch.at.harp, "kid");
}

/// A plan of the child whose agent wrote a permissions document.
struct Permitted;

/// The document `Permitted`'s agent wrote, as it wrote it.
const DOCUMENT: &str = r#"{"defaultMode":"plan","allow":["Bash(ls)"]}"#;

impl ChildPlans for Permitted {
    fn output_root(
        &self,
        project_dir: &str,
        env: &BTreeMap<String, String>,
    ) -> Result<String, String> {
        Plans.output_root(project_dir, env)
    }

    fn preview(
        &self,
        engine: &str,
        _agent: &str,
        project_dir: &str,
        env: &BTreeMap<String, String>,
        trusted: bool,
    ) -> Result<Previewed, String> {
        let mut previewed = Plans.preview(engine, "", project_dir, env, trusted)?;
        previewed.plan.permissions = Some(DOCUMENT.to_string());
        Ok(previewed)
    }

    fn git_user(
        &self,
        project_dir: &str,
        env: &BTreeMap<String, String>,
    ) -> Option<run_ports::GitUser> {
        Plans.git_user(project_dir, env)
    }
}

// Rulings 133-137: a hosted child's session home is prepared with its
// agent's own permissions document for the engine, verbatim, as every
// launch's is.
#[tokio::test]
async fn a_hosted_childs_home_is_prepared_with_its_agents_permissions() {
    let mut world = World::new(Box::new(Permitted));
    world.mint(OWNER, "boss", None).await;
    world.mint(CHILD, "kid", Some(OWNER)).await;
    world.tick().await;
    assert_eq!(world.daemon.hosting(), 1, "hosted");
    assert_eq!(
        world
            .homes
            .text("/work", "kid", "home/permissions.json")
            .as_deref(),
        Some(DOCUMENT)
    );
    let plain = World::hosting().await;
    assert_eq!(
        plain.homes.text("/work", "kid", "home/permissions.json"),
        None,
        "none written: the engine's default"
    );
}

// Step 9 (owner ruling 2026-11-17): a hosted child's run journals what it
// was given, its servers by name (ctxloom's own) and its agent's document,
// in its RunEnqueued.
#[tokio::test]
async fn a_hosted_childs_run_journals_what_it_was_given() {
    let mut world = World::new(Box::new(Permitted));
    world.mint(OWNER, "boss", None).await;
    world.mint(CHILD, "kid", Some(OWNER)).await;
    world.tick().await;
    let grant = world
        .run_events()
        .await
        .into_iter()
        .find_map(|event| match event {
            RunEvent::RunEnqueued(enqueued) => Some(enqueued.grant),
            _ => None,
        })
        .expect("the child's run is enqueued");
    assert_eq!(
        grant,
        Some(runs::Grant {
            mcp_servers: vec!["ctxloom".to_string()],
            permissions: DOCUMENT.to_string(),
        })
    );
}

/// A plan of the child whose run agent denies a tool.
struct Surfaced;

impl ChildPlans for Surfaced {
    fn output_root(
        &self,
        project_dir: &str,
        env: &BTreeMap<String, String>,
    ) -> Result<String, String> {
        Plans.output_root(project_dir, env)
    }

    fn preview(
        &self,
        engine: &str,
        _agent: &str,
        project_dir: &str,
        env: &BTreeMap<String, String>,
        trusted: bool,
    ) -> Result<Previewed, String> {
        let mut previewed = Plans.preview(engine, "", project_dir, env, trusted)?;
        previewed.plan.surfaces.deny_tools = vec!["Bash".to_string()];
        Ok(previewed)
    }

    fn git_user(
        &self,
        project_dir: &str,
        env: &BTreeMap<String, String>,
    ) -> Option<run_ports::GitUser> {
        Plans.git_user(project_dir, env)
    }
}

// Merge item 3 (main into the daemon line, 2026-11-13; the owner's "one
// session-setup path"): a hosted child is set up by the one function every
// launch is, so its home is prepared with its run agent's surfaces, and its
// engine is told the live project as CTXLOOM_ROOT.
#[tokio::test]
async fn a_hosted_child_is_set_up_with_its_surfaces_and_told_the_project() {
    let mut world = World::new(Box::new(Surfaced));
    world.mint(OWNER, "boss", None).await;
    world.mint(CHILD, "kid", Some(OWNER)).await;
    world.tick().await;
    assert_eq!(world.daemon.hosting(), 1, "hosted");
    assert_eq!(
        world
            .homes
            .text("/work", "kid", "home/denied.txt")
            .as_deref(),
        Some("Bash")
    );
    let started = world.engine.started.lock().unwrap().clone();
    let [spec] = started.as_slice() else {
        panic!("one start: {started:?}");
    };
    assert_eq!(
        spec.env.get(run_ports::ROOT_VARIABLE).map(String::as_str),
        Some("/work")
    );
    assert_eq!(spec.cwd.as_deref(), Some("/work"), "no axis: the project");
}

// T21: each launch's engine is composed for it, from where ITS launcher
// finds its user's settings (S7), never once for the daemon: the child is
// prepared and started with the engine so composed.
#[tokio::test]
async fn each_launch_is_composed_from_its_launchers_places() {
    let mut world = World::new(Box::new(Plans));
    let composed = Arc::new(Scripted::default());
    let asked: Arc<Mutex<Vec<BTreeMap<String, String>>>> = Arc::default();
    let for_launch: crate::ForLaunch = {
        let (composed, asked) = (Arc::clone(&composed), Arc::clone(&asked));
        Arc::new(
            move |launcher: &BTreeMap<String, String>, _: Option<&crate::Program>| {
                asked.lock().unwrap().push(launcher.clone());
                let engine: Arc<dyn Engine> = composed.clone();
                let child: Arc<dyn ChildEngine> = composed.clone();
                (engine, child)
            },
        )
    };
    let launcher = BTreeMap::from([(
        "CLAUDE_CONFIG_DIR".to_string(),
        "/home/alice/.claude-work".to_string(),
    )]);
    world.daemon = Daemon::new(Ports {
        edge: Arc::clone(&world.edge),
        engines: vec![HostedEngine {
            engine: world.engine.clone(),
            child: world.engine.clone(),
            transcripts: Arc::new(|_| None),
            for_launch: Some(for_launch),
        }],
        plans: Box::new(Plans),
        hand_offs: Box::new(Handed::with(
            BTreeMap::from([("PATH".to_string(), "/usr/bin".to_string())]),
            launcher.clone(),
        )),
        homes: Box::new(world.homes.clone()),
        ears: Box::new(Listening::default()),
        run_locks: Arc::new(Locks(LockProbe::Unproven)),
        place: Place {
            home: "/home/u".to_string(),
            program_dir: None,
            os: "linux".to_string(),
            user_home: None,
        },
        folds: Folds::default(),
        say: Box::new(|_| {}),
    });
    world.mint(OWNER, "boss", None).await;
    world.mint(CHILD, "kid", Some(OWNER)).await;
    world.tick().await;
    assert_eq!(*asked.lock().unwrap(), [launcher]);
    assert_eq!(
        composed.started.lock().unwrap().len(),
        1,
        "started composed"
    );
    assert!(
        world.engine.started.lock().unwrap().is_empty(),
        "never the engine composed once"
    );
    let started = composed.started.lock().unwrap().clone();
    assert!(
        !started[0].env.contains_key("CLAUDE_CONFIG_DIR"),
        "where her settings are is never the child's"
    );
}

/// The programs a hosted child's launch was composed with, and the
/// environment it was started in, its hand-off's launcher having run
/// `program` and the daemon's own program being in `/daemon/bin`.
async fn launched_running(
    program: Option<crate::Program>,
) -> (Vec<Option<crate::Program>>, BTreeMap<String, String>) {
    let mut world = World::new(Box::new(Plans));
    let composed = Arc::new(Scripted::default());
    let asked: Arc<Mutex<Vec<Option<crate::Program>>>> = Arc::default();
    let for_launch: crate::ForLaunch = {
        let (composed, asked) = (Arc::clone(&composed), Arc::clone(&asked));
        Arc::new(
            move |_: &BTreeMap<String, String>, program: Option<&crate::Program>| {
                asked.lock().unwrap().push(program.cloned());
                let engine: Arc<dyn Engine> = composed.clone();
                let child: Arc<dyn ChildEngine> = composed.clone();
                (engine, child)
            },
        )
    };
    world.daemon = Daemon::new(Ports {
        edge: Arc::clone(&world.edge),
        engines: vec![HostedEngine {
            engine: world.engine.clone(),
            child: world.engine.clone(),
            transcripts: Arc::new(|_| None),
            for_launch: Some(for_launch),
        }],
        plans: Box::new(Plans),
        hand_offs: Box::new(Handed::programmed(
            BTreeMap::from([("PATH".to_string(), "/usr/bin".to_string())]),
            program,
        )),
        homes: Box::new(world.homes.clone()),
        ears: Box::new(Listening::default()),
        run_locks: Arc::new(Locks(LockProbe::Unproven)),
        place: Place {
            home: "/home/u".to_string(),
            program_dir: Some("/daemon/bin".to_string()),
            os: "linux".to_string(),
            user_home: None,
        },
        folds: Folds::default(),
        say: Box::new(|_| {}),
    });
    world.mint(OWNER, "boss", None).await;
    world.mint(CHILD, "kid", Some(OWNER)).await;
    world.tick().await;
    let started = composed.started.lock().unwrap().clone();
    assert_eq!(started.len(), 1, "started composed");
    let asked = asked.lock().unwrap().clone();
    (asked, started[0].env.clone())
}

// Owner, 2026-11-18: a hosted child runs its LAUNCHER's program, not the
// daemon's: its engine is composed calling it (its hooks, its statusline)
// and its engine's PATH leads with the launcher's directory.
#[tokio::test]
async fn a_hosted_child_runs_its_launchers_program() {
    let program = crate::Program {
        name: "ctxloom-dev".to_string(),
        dir: "/opt/client/bin".to_string(),
    };
    let (asked, env) = launched_running(Some(program.clone())).await;
    assert_eq!(asked, [Some(program)]);
    assert_eq!(env["PATH"], "/opt/client/bin:/usr/bin");
}

// A launcher that named no program leaves the daemon's own: its directory
// leads the child's PATH.
#[tokio::test]
async fn a_hosted_child_whose_launcher_named_no_program_runs_the_daemons() {
    let (asked, env) = launched_running(None).await;
    assert_eq!(asked, [None]);
    assert_eq!(env["PATH"], "/daemon/bin:/usr/bin");
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

// Owner 2026-11-15 (clears captured, typed): a child's clear is in its
// session's record, then the bind of the conversation after it; a clear
// that names no conversation records neither.
#[tokio::test]
async fn a_childs_clear_is_recorded_before_the_bind_of_its_new_conversation() {
    let mut world = World::hosting().await;
    world.say(ChildObservation::Cleared {
        native_id: Some("n2".to_string()),
    });
    world.say(ChildObservation::Cleared { native_id: None });
    world.tick().await;
    let said: Vec<String> = world
        .session_events(CHILD)
        .await
        .into_iter()
        .filter_map(|event| match event {
            SessionEvent::ConversationCleared(cleared) => Some(format!(
                "clear {} {}",
                cleared.native_session, cleared.trigger
            )),
            SessionEvent::EngineSessionBound(bound) => Some(format!("bind {}", bound.native_id)),
            _ => None,
        })
        .collect();
    assert_eq!(said, ["clear n2 clear", "bind n2"]);
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

// One session-setup path (owner, 2026-11-12): a hosted child's home is set
// up by the same code as every other session's: what its engine merges into
// is read back and merged, each seed is written once (one there is left as
// it is), and, in a session home, the user's git identity is its
// `.gitconfig`.
#[tokio::test]
async fn a_hosted_childs_home_is_set_up_as_every_sessions_is() {
    let mut world = World::new(Box::new(Plans));
    world.homes.put("/work", "kid", "home/state.json", b"old");
    world.homes.put("/work", "kid", "home/kept.json", b"hers");
    world.mint(OWNER, "boss", None).await;
    world.mint(CHILD, "kid", Some(OWNER)).await;
    world.tick().await;
    assert_eq!(world.daemon.hosting(), 1, "hosted");
    let text = |rel: &str| world.homes.text("/work", "kid", rel);
    assert_eq!(text("home/state.json").as_deref(), Some("old+merged"));
    assert_eq!(text("home/model.json").as_deref(), Some("seeded"));
    assert_eq!(
        text("home/kept.json").as_deref(),
        Some("hers"),
        "left as it is"
    );
    let git = text("home/.gitconfig").expect("the git identity is seeded");
    assert!(
        git.contains("Alice") && git.contains("alice@example.com"),
        "{git}"
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
        fn output_root(
            &self,
            project_dir: &str,
            env: &BTreeMap<String, String>,
        ) -> Result<String, String> {
            Plans.output_root(project_dir, env)
        }

        fn preview(
            &self,
            _: &str,
            _: &str,
            _: &str,
            _: &BTreeMap<String, String>,
            _: bool,
        ) -> Result<Previewed, String> {
            Err("library-application-no-agent".to_string())
        }

        fn git_user(&self, _: &str, _: &BTreeMap<String, String>) -> Option<run_ports::GitUser> {
            None
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

// M9: a child the daemon hosts is its own live run, whatever its locks
// probe: it is never swept as another process's whose owner is gone.
#[tokio::test]
async fn a_hosted_child_is_live_and_never_swept_as_another_processs() {
    let mut world = World::probing(Box::new(Plans), LockProbe::Free);
    world.mint(OWNER, "boss", None).await;
    world.mint(CHILD, "kid", Some(OWNER)).await;
    world.tick().await;
    assert_eq!(world.daemon.hosting(), 1, "hosted");
    let live = world
        .daemon
        .live_runs(&world.edge.store, world.edge.catalog())
        .await;
    assert_eq!(live, Ok(1));
    assert_eq!(world.daemon.hosting(), 1, "still hosted");
}

// ---- the hand-off for the child's life (slice 4 U3, T6)

/// Hand-off files, in memory: each read and delete recorded, `listed`
/// what a listing answers.
#[derive(Clone, Default)]
struct Files {
    kept: Arc<Mutex<BTreeMap<String, crate::HandOff>>>,
    reads: Arc<Mutex<Vec<String>>>,
    deleted: Arc<Mutex<Vec<String>>>,
    claimed: Arc<Mutex<Vec<String>>>,
    listed: Arc<Mutex<Vec<crate::Kept>>>,
}

impl HandOffs for Files {
    fn read(&self, session: &str) -> Option<crate::HandOff> {
        self.reads.lock().unwrap().push(session.to_string());
        self.kept.lock().unwrap().get(session).cloned()
    }

    fn delete(&self, session: &str) {
        self.deleted.lock().unwrap().push(session.to_string());
        self.kept.lock().unwrap().remove(session);
    }

    fn claim(&self, session: &str) {
        self.claimed.lock().unwrap().push(session.to_string());
    }

    fn list(&self) -> Vec<crate::Kept> {
        self.listed.lock().unwrap().clone()
    }
}

/// `Plans`, recording the agent each plan was asked for.
#[derive(Clone, Default)]
struct ForAgent(Arc<Mutex<Vec<String>>>);

impl ChildPlans for ForAgent {
    fn output_root(
        &self,
        project_dir: &str,
        env: &BTreeMap<String, String>,
    ) -> Result<String, String> {
        Plans.output_root(project_dir, env)
    }

    fn preview(
        &self,
        engine: &str,
        agent: &str,
        project_dir: &str,
        env: &BTreeMap<String, String>,
        trusted: bool,
    ) -> Result<Previewed, String> {
        self.0.lock().unwrap().push(agent.to_string());
        Plans.preview(engine, agent, project_dir, env, trusted)
    }

    fn git_user(
        &self,
        project_dir: &str,
        env: &BTreeMap<String, String>,
    ) -> Option<run_ports::GitUser> {
        Plans.git_user(project_dir, env)
    }
}

/// A world whose child `kid` has a hand-off in `files`, for the agent
/// `reviewer`, hosted.
async fn hosting_from(files: &Files, plans: &ForAgent) -> World {
    hosting_from_within(files, plans, None).await
}

/// [`hosting_from`], its hand-over's period `period` when one is given.
async fn hosting_from_within(
    files: &Files,
    plans: &ForAgent,
    period: Option<std::time::Duration>,
) -> World {
    files.kept.lock().unwrap().insert(
        CHILD.to_string(),
        crate::HandOff {
            env: BTreeMap::from([("SECRET".to_string(), "v".to_string())]),
            launcher: BTreeMap::new(),
            agent: "reviewer".to_string(),
            program: None,
        },
    );
    let mut world = World::built_within(
        Box::new(plans.clone()),
        LockProbe::Unproven,
        Box::new(files.clone()),
        period,
    );
    world.name_project().await;
    world.mint(OWNER, "boss", None).await;
    world.mint(CHILD, "kid", Some(OWNER)).await;
    world.tick().await;
    assert_eq!(world.daemon.hosting(), 1, "hosted");
    world
}

// T6: a child's start READS its hand-off and leaves it for its life (a
// start after a daemon restart reads it again); the child is planned for
// the agent the hand-off names.
#[tokio::test]
async fn a_childs_start_reads_its_hand_off_and_leaves_it() {
    let files = Files::default();
    let plans = ForAgent::default();
    let world = hosting_from(&files, &plans).await;
    assert_eq!(*files.reads.lock().unwrap(), [CHILD.to_string()]);
    assert!(files.deleted.lock().unwrap().is_empty(), "kept");
    assert!(files.kept.lock().unwrap().contains_key(CHILD));
    assert_eq!(*plans.0.lock().unwrap(), ["reviewer".to_string()]);
    let started = world.engine.started.lock().unwrap().clone();
    assert_eq!(
        started[0].env.get("SECRET").map(String::as_str),
        Some("v"),
        "started with what it was handed"
    );
}

// Step 9 (S19's `session stop`, OD-57): a hosted child whose run is asked to
// stop has its engine ended, once; its exit ends its incarnation `stopped`;
// the child stays open, its hand-off kept for its next run.
#[tokio::test]
async fn a_hosted_childs_run_asked_to_stop_is_ended_and_the_child_stays_open() {
    let files = Files::default();
    let plans = ForAgent::default();
    let mut world = hosting_from(&files, &plans).await;
    world
        .edge
        .handle(CommandBook::command_of(
            Cover {
                domain: runs::RUN_DOMAIN.to_string(),
                root: Some(runs::run_root(CHILD, 1)),
                edition: None,
            },
            &runs::RunCommand::from(runs::RequestStop {
                reason: "stop".to_string(),
            }),
        ))
        .await
        .unwrap();
    world.tick().await;
    world.tick().await;
    assert_eq!(world.engine.asked.lock().unwrap().ends, 1, "ended once");
    world.say(ChildObservation::Exited {
        exit: Exit {
            code: Some(0),
            signal: None,
        },
        detail: String::new(),
    });
    world.tick().await;
    let ended: Vec<(u32, String)> = world
        .session_events(CHILD)
        .await
        .into_iter()
        .filter_map(|event| match event {
            SessionEvent::IncarnationEnded(ended) => Some((ended.incarnation, ended.cause)),
            _ => None,
        })
        .collect();
    assert_eq!(ended, [(1, "stopped".to_string())]);
    assert!(
        !world
            .session_events(CHILD)
            .await
            .iter()
            .any(|event| matches!(event, SessionEvent::SessionClosed(_))),
        "the child stays open"
    );
    assert!(
        files.deleted.lock().unwrap().is_empty(),
        "its hand-off kept"
    );
}

// T6: a child closed for good has its hand-off deleted.
#[tokio::test]
async fn a_closed_childs_hand_off_is_deleted() {
    let files = Files::default();
    let plans = ForAgent::default();
    let mut world = hosting_from(&files, &plans).await;
    world
        .handle(
            CHILD,
            sessions::CloseSession {
                cause: "closed by the user".to_string(),
                requester: None,
            },
        )
        .await;
    for _ in 0..3 {
        world.tick().await;
    }
    assert!(
        files.deleted.lock().unwrap().contains(&CHILD.to_string()),
        "{:?}",
        files.deleted.lock().unwrap()
    );
    assert!(!files.kept.lock().unwrap().contains_key(CHILD));
}

/// A hand-off of `session` as a listing has it.
fn kept(session: &str, marked: bool, creator_holds: bool) -> crate::Kept {
    crate::Kept {
        session: session.to_string(),
        marked,
        creator_holds,
    }
}

/// A child nothing launched yet.
const UNLAUNCHED: &str = "5f0c7a52-1111-5111-8111-0000000000d1";

/// The sweep over `listed`, the child `kid` hosted: what it deleted and
/// what it claimed.
async fn swept(listed: Vec<crate::Kept>) -> (Vec<String>, Vec<String>) {
    let files = Files::default();
    let plans = ForAgent::default();
    let mut world = hosting_from(&files, &plans).await;
    *files.listed.lock().unwrap() = listed;
    world.tick().await;
    let deleted = files.deleted.lock().unwrap().clone();
    let claimed = files.claimed.lock().unwrap().clone();
    (deleted, claimed)
}

// T6 (owner's ruling on U3): a hand-off that is not marked in launch and
// that no session knows of is swept at once; an open child's is kept.
#[tokio::test]
async fn an_unmarked_hand_off_no_session_knows_is_swept_at_once() {
    let (deleted, _) = swept(vec![
        kept(UNLAUNCHED, false, false),
        kept(CHILD, false, false),
    ])
    .await;
    assert_eq!(deleted, [UNLAUNCHED.to_string()], "the open child's kept");
}

// The race of the sweep between `CreateHandOff` and `LaunchChild`, forced:
// marked, its creator holding the launch lock, nothing recorded yet. Kept.
#[tokio::test]
async fn a_hand_off_in_launch_whose_creator_holds_it_is_kept() {
    let (deleted, claimed) = swept(vec![kept(UNLAUNCHED, true, true)]).await;
    assert!(deleted.is_empty(), "{deleted:?}");
    assert!(claimed.is_empty(), "nothing launched it yet: {claimed:?}");
}

// The race of a creator crashing mid-launch, forced: marked, its launch
// lock free. Its creator is proven gone, so it is swept.
#[tokio::test]
async fn a_hand_off_whose_creator_crashed_mid_launch_is_swept() {
    let (deleted, _) = swept(vec![kept(UNLAUNCHED, true, false)]).await;
    assert_eq!(deleted, [UNLAUNCHED.to_string()]);
}

// `LaunchChild` claims it: a marked hand-off of a child its owner holds
// (launched, not minted yet) or of a session the board knows is claimed,
// never swept, whatever its creator's lock says.
#[tokio::test]
async fn a_marked_hand_off_a_launch_recorded_is_claimed() {
    let files = Files::default();
    let plans = ForAgent::default();
    let mut world = hosting_from(&files, &plans).await;
    world
        .handle(
            OWNER,
            sessions::LaunchChild {
                child_project_id: "calm-otter".to_string(),
                child_session: UNLAUNCHED.to_string(),
                child_root: "/work".to_string(),
                harp: "late-kid".to_string(),
                engine: ENGINE.to_string(),
                trust: sessions::TargetTrust {
                    same_project: true,
                    trusted: true,
                },
                delegation_limit: 4,
                requester: None,
                prompt: String::new(),
            },
        )
        .await;
    *files.listed.lock().unwrap() = vec![kept(UNLAUNCHED, true, false), kept(CHILD, true, false)];
    world.tick().await;
    let deleted = files.deleted.lock().unwrap().clone();
    assert!(deleted.is_empty(), "{deleted:?}");
    let claimed = files.claimed.lock().unwrap().clone();
    assert!(claimed.contains(&UNLAUNCHED.to_string()), "{claimed:?}");
    assert!(claimed.contains(&CHILD.to_string()), "{claimed:?}");
}

// A closed child's hand-off is swept whatever its mark.
#[tokio::test]
async fn a_closed_childs_hand_off_is_swept_whatever_its_mark() {
    let files = Files::default();
    let plans = ForAgent::default();
    let mut world = hosting_from(&files, &plans).await;
    let closed = "5f0c7a52-1111-5111-8111-0000000000c1";
    world.mint(closed, "gone", Some(OWNER)).await;
    world
        .handle(
            closed,
            sessions::CloseSession {
                cause: "done".to_string(),
                requester: None,
            },
        )
        .await;
    *files.listed.lock().unwrap() = vec![kept(closed, true, true)];
    world.tick().await;
    assert_eq!(*files.deleted.lock().unwrap(), [closed.to_string()]);
}

// ---------------------------------------------------------------- U6

/// The pending launches' runs, owed and started, as the store holds them.
async fn runs_now(
    edge: &Arc<TestEdge>,
) -> (
    Vec<pending_launches::PendingLaunch>,
    Vec<pending_launches::StartedRun>,
) {
    let mut pending = pending_launches::catch_up();
    pending.step(&edge.store, edge.catalog()).await.unwrap();
    let model = pending.projector().pending();
    (model.launches(), model.started())
}

// U6 (owner ruling on U4): a hosted child holds its session's lock for its
// life, so another launcher's sweep (a hosted one-shot's, in the daemon's
// own process) does not take it for lost, even where its session's lock
// file was left free by an earlier run. Forced: the sweep runs while the
// child is hosted, its lock file free but for the daemon's hold.
#[tokio::test]
async fn a_one_shots_sweep_while_a_child_is_hosted_leaves_the_child() {
    let world = World::hosting().await;
    assert!(
        world.homes.1.lock().unwrap().contains("/work/kid"),
        "the child's session lock is held while it is hosted"
    );
    let (owed, started) = runs_now(&world.edge).await;
    assert_eq!(started.len(), 1, "the child's run is started");
    let swept = run_launcher::Sweeper::new(
        Arc::clone(&world.edge),
        Arc::new(LeftLocks(Arc::clone(&world.homes.1))),
    )
    .sweep(&owed, &started)
    .await
    .unwrap();
    assert!(swept.is_empty(), "nothing swept: {swept:?}");
    assert!(
        !world
            .run_events()
            .await
            .iter()
            .any(|event| matches!(event, RunEvent::RunEnded(_))),
        "the hosted child's run is not ended"
    );
    // Once let go (here: the daemon is gone), the same sweep ends it.
    drop(world.daemon);
    let swept = run_launcher::Sweeper::new(
        Arc::clone(&world.edge),
        Arc::new(LeftLocks(Arc::clone(&world.homes.1))),
    )
    .sweep(&owed, &started)
    .await
    .unwrap();
    assert_eq!(swept.len(), 1, "a child no one holds is lost");
}

// U6: its session's lock is let go once it exits; one another holds keeps
// it from starting at all (nothing enqueued), tried again later.
#[tokio::test]
async fn a_childs_session_lock_is_let_go_at_its_exit_and_one_held_elsewhere_starts_nothing() {
    let mut world = World::hosting().await;
    world.say(ChildObservation::Exited {
        exit: Exit {
            code: Some(0),
            signal: None,
        },
        detail: String::new(),
    });
    world.tick().await;
    assert!(world.homes.1.lock().unwrap().is_empty(), "let go");

    let mut world = World::new(Box::new(Plans));
    world.name_project().await;
    world
        .homes
        .1
        .lock()
        .unwrap()
        .insert("/work/kid".to_string());
    world.mint(OWNER, "boss", None).await;
    world.mint(CHILD, "kid", Some(OWNER)).await;
    world.tick().await;
    assert_eq!(world.daemon.hosting(), 0, "not started");
    assert!(world.run_events().await.is_empty(), "nothing enqueued");
    world.homes.1.lock().unwrap().clear();
    world.tick().await;
    assert_eq!(world.daemon.hosting(), 1, "started once it could be had");
}

/// The checkout a `worktree` child's workspaces make.
const CHECKOUT: &str = "/home/u/.ctxloom/worktrees/calm-otter/kid/dev";

/// A project's workspaces: a checkout for each request, each removal
/// answered `removal`, and what each was asked.
struct Checkouts {
    asked: Mutex<Vec<String>>,
    removal: isolation_ports::Removal,
    /// The environment each workspace was asked in (none: the daemon's).
    envs: Mutex<Vec<Option<BTreeMap<String, String>>>>,
}

impl isolation_ports::Workspaces for Checkouts {
    fn prepare(
        &self,
        request: &isolation_ports::Request,
    ) -> Result<isolation_ports::Prepared, isolation_ports::Failure> {
        self.asked.lock().unwrap().push(format!(
            "prepare {} {} {}",
            request.project_dir, request.work_dir, request.agent
        ));
        Ok(isolation_ports::Prepared::Checkout(
            isolation_ports::Checkout {
                path: CHECKOUT.to_string(),
                branch: "ctxloom/kid/dev".to_string(),
                repo_dir: "/work".to_string(),
                reused: false,
            },
        ))
    }

    fn session_known(&self, _: &str) -> Result<bool, isolation_ports::Failure> {
        Ok(true)
    }

    fn scratch(
        &self,
        _: Option<&str>,
    ) -> Result<Vec<isolation_ports::Scratch>, isolation_ports::Failure> {
        Ok(Vec::new())
    }

    fn place_files(
        &self,
        _: &str,
        _: &[isolation_ports::PlacedFile],
    ) -> Result<(), isolation_ports::Failure> {
        Ok(())
    }

    fn remove_files(&self, checkout: &str, _: &[String]) {
        self.asked
            .lock()
            .unwrap()
            .push(format!("remove-files {checkout}"));
    }

    fn inspect(&self, path: &str) -> isolation_ports::Standing {
        isolation_ports::Standing::Unresolved(path.to_string())
    }

    fn remove(&self, repo_dir: &str, path: &str) -> isolation_ports::Removal {
        self.asked
            .lock()
            .unwrap()
            .push(format!("remove {repo_dir} {path}"));
        self.removal.clone()
    }
}

/// The workspaces of every project: the one [`Checkouts`].
struct AllCheckouts(Arc<Checkouts>);

impl crate::ChildWorkspaces for AllCheckouts {
    fn of(
        &self,
        _: &str,
        _: &str,
        env: Option<&BTreeMap<String, String>>,
    ) -> Arc<dyn isolation_ports::Workspaces + Send + Sync> {
        self.0.envs.lock().unwrap().push(env.cloned());
        Arc::clone(&self.0) as Arc<dyn isolation_ports::Workspaces + Send + Sync>
    }
}

/// A plan of the child that records whether its project was trusted.
struct TrustSeen(Arc<Mutex<Vec<bool>>>);

impl ChildPlans for TrustSeen {
    fn output_root(
        &self,
        project_dir: &str,
        env: &BTreeMap<String, String>,
    ) -> Result<String, String> {
        Plans.output_root(project_dir, env)
    }

    fn preview(
        &self,
        engine: &str,
        agent: &str,
        project_dir: &str,
        env: &BTreeMap<String, String>,
        trusted: bool,
    ) -> Result<Previewed, String> {
        self.0.lock().unwrap().push(trusted);
        Plans.preview(engine, agent, project_dir, env, trusted)
    }

    fn git_user(
        &self,
        project_dir: &str,
        env: &BTreeMap<String, String>,
    ) -> Option<run_ports::GitUser> {
        Plans.git_user(project_dir, env)
    }
}

// Step 8 U2 (ruling 142): a hosted child is planned with its project's
// trust as the daemon's index has it, so a trusted project's own settings
// apply to it and an untrusted one's are held back.
#[tokio::test]
async fn a_hosted_child_is_planned_with_its_projects_trust() {
    for grant in [false, true] {
        let seen = Arc::new(Mutex::new(Vec::new()));
        let mut world = World::new(Box::new(TrustSeen(Arc::clone(&seen))));
        world.name_project().await;
        if grant {
            let name = "projects/calm-quiet-heron/trust";
            let granted = trust::GrantTrust {
                name: name.to_string(),
                project_id: "calm-otter".to_string(),
                etag: "e".to_string(),
                current_etag: "e".to_string(),
                actor: trust::Actor {
                    user: "alice".to_string(),
                },
            };
            let cover = Cover {
                domain: trust::TRUST_DOMAIN.to_string(),
                root: Some(trust::trust_root(name)),
                edition: None,
            };
            world
                .edge
                .handle(sessions::keyed_book(cover, &granted, "grant"))
                .await
                .unwrap();
        }
        world.mint(OWNER, "boss", None).await;
        world.mint(CHILD, "kid", Some(OWNER)).await;
        world.tick().await;
        assert_eq!(world.daemon.hosting(), 1, "hosted");
        assert_eq!(*seen.lock().unwrap(), [grant]);
    }
}

/// A plan of the child whose agent `dev` works in a worktree.
struct WorktreePlans;

impl ChildPlans for WorktreePlans {
    fn output_root(
        &self,
        project_dir: &str,
        env: &BTreeMap<String, String>,
    ) -> Result<String, String> {
        Plans.output_root(project_dir, env)
    }

    fn preview(
        &self,
        engine: &str,
        agent: &str,
        project_dir: &str,
        env: &BTreeMap<String, String>,
        trusted: bool,
    ) -> Result<Previewed, String> {
        let mut previewed = Plans.preview(engine, agent, project_dir, env, trusted)?;
        previewed.plan.axes.0 = "worktree".to_string();
        previewed.plan.agent = "dev".to_string();
        Ok(previewed)
    }

    fn git_user(&self, _: &str, _: &BTreeMap<String, String>) -> Option<run_ports::GitUser> {
        None
    }
}

/// A world whose child's agent works in a worktree, each removal of its
/// checkout answered `removal`, the child launched by its owner (its axis
/// recorded at its mint) and hosted.
async fn in_a_worktree(removal: isolation_ports::Removal) -> (World, Arc<Checkouts>, Log) {
    let checkouts = Arc::new(Checkouts {
        asked: Mutex::default(),
        removal,
        envs: Mutex::default(),
    });
    let mut world = World::new(Box::new(WorktreePlans));
    let said: Log = Arc::default();
    let log = Arc::clone(&said);
    world.daemon = Daemon::new(Ports {
        edge: Arc::clone(&world.edge),
        engines: vec![HostedEngine {
            engine: world.engine.clone(),
            child: world.engine.clone(),
            transcripts: Arc::new(|_| None),
            for_launch: None,
        }],
        plans: Box::new(WorktreePlans),
        // Her launcher's environment holds her ssh agent.
        hand_offs: Box::new(Handed::of(BTreeMap::from([(
            "SSH_AUTH_SOCK".to_string(),
            "/run/her-agent".to_string(),
        )]))),
        homes: Box::new(world.homes.clone()),
        ears: Box::new(Listening::default()),
        run_locks: Arc::new(Locks(LockProbe::Unproven)),
        place: Place {
            home: "/home/u".to_string(),
            program_dir: None,
            os: "linux".to_string(),
            user_home: None,
        },
        folds: Folds::default(),
        say: Box::new(move |line: &str| log.lock().unwrap().push(line.to_string())),
    })
    .placing(Arc::new(AllCheckouts(Arc::clone(&checkouts))));
    world.name_project().await;
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
    assert_eq!(
        world.daemon.hosting(),
        1,
        "hosted: {:?}",
        said.lock().unwrap()
    );
    (world, checkouts, said)
}

/// What the daemon logged.
type Log = Arc<Mutex<Vec<String>>>;

// The defect of 2026-11-18: a hosted child's checkout is asked of its
// project's workspaces in the environment its LAUNCHER sent, so its git sees
// her `GIT_*` and `SSH_AUTH_SOCK`, never the daemon's own alone.
#[tokio::test]
async fn a_childs_checkout_is_asked_in_its_launchers_environment() {
    let (_world, checkouts, _) = in_a_worktree(isolation_ports::Removal::Removed).await;
    let envs = checkouts.envs.lock().unwrap().clone();
    assert!(!envs.is_empty(), "its workspaces were asked");
    assert!(
        envs.iter().all(|env| env
            .as_ref()
            .and_then(|env| env.get("SSH_AUTH_SOCK"))
            .is_some_and(|sock| sock == "/run/her-agent")),
        "{envs:?}"
    );
}

// s4-Q11: a hosted child follows its agent's `workspace`: its axis is
// recorded at its mint, it is placed in a checkout of its session's own
// (under ctxloom's home), started there, and its start records the
// checkout as where it works.
#[tokio::test]
async fn a_worktree_agents_child_is_minted_on_its_axis_and_works_in_its_checkout() {
    let (world, checkouts, _) = in_a_worktree(isolation_ports::Removal::Removed).await;
    let minted: Vec<Option<String>> = world
        .session_events(CHILD)
        .await
        .into_iter()
        .filter_map(|event| match event {
            SessionEvent::SessionMinted(minted) => Some(minted.workspace),
            _ => None,
        })
        .collect();
    assert_eq!(minted, [Some("worktree".to_string())]);
    assert_eq!(
        checkouts.asked.lock().unwrap().first().map(String::as_str),
        Some("prepare /work /home/u/.ctxloom/worktrees/calm-otter/kid dev")
    );
    let started = world.engine.started.lock().unwrap().clone();
    assert_eq!(started[0].cwd.as_deref(), Some(CHECKOUT));
    let workspaces: Vec<String> = world
        .run_events()
        .await
        .into_iter()
        .filter_map(|event| match event {
            RunEvent::RunnerStartedRecorded(started) => Some(started.workspace),
            _ => None,
        })
        .collect();
    assert_eq!(workspaces, [CHECKOUT]);
}

// s4-Q11: at its exit its checkout is let go WIP-safe through the port: the
// files the launch placed taken out, then the port's removal (never
// forced); a checkout holding work is left, and said with its branch.
#[tokio::test]
async fn a_childs_checkout_holding_work_is_kept_at_its_close_and_said() {
    let (mut world, checkouts, said) = in_a_worktree(isolation_ports::Removal::Left(
        "it has uncommitted changes; left in place to preserve WIP".to_string(),
    ))
    .await;
    world.say(ChildObservation::Exited {
        exit: Exit {
            code: Some(0),
            signal: None,
        },
        detail: String::new(),
    });
    world.tick().await;
    let asked = checkouts.asked.lock().unwrap().clone();
    assert_eq!(
        &asked[1..],
        [
            format!("remove-files {CHECKOUT}"),
            format!("remove /work {CHECKOUT}")
        ]
    );
    let said = said.lock().unwrap().join("\n");
    assert!(
        said.contains(&format!(
            "kid: its checkout {CHECKOUT} (branch ctxloom/kid/dev) left: it has uncommitted changes"
        )),
        "{said}"
    );
}

// OD-19: the start's sweep lets a lost run's checkout go WIP-safe, only one
// under ctxloom's own worktrees; the project itself is never removed.
#[tokio::test]
async fn the_starts_sweep_releases_a_lost_childs_checkout_and_never_the_project() {
    let (world, checkouts, said) = in_a_worktree(isolation_ports::Removal::Removed).await;
    let World { edge, homes, .. } = world;
    drop(homes);
    // The daemon is killed: a new one starts over the same store, its locks
    // saying the child's run is lost.
    let mut next = Daemon::new(Ports {
        edge: Arc::clone(&edge),
        engines: Vec::new(),
        plans: Box::new(WorktreePlans),
        hand_offs: Box::new(Handed::of(BTreeMap::new())),
        homes: Box::new(Homes::default()),
        ears: Box::new(Listening::default()),
        run_locks: Arc::new(Locks(LockProbe::Free)),
        place: Place {
            home: "/home/u".to_string(),
            program_dir: None,
            os: "linux".to_string(),
            user_home: None,
        },
        folds: Folds::default(),
        say: Box::new(|_| {}),
    })
    .placing(Arc::new(AllCheckouts(Arc::clone(&checkouts))));
    let swept = next.start_up(&edge.store, edge.catalog()).await.unwrap();
    assert_eq!(
        swept.iter().map(|s| s.released.clone()).collect::<Vec<_>>(),
        [run_launcher::Released::Removed(CHECKOUT.to_string())]
    );
    assert_eq!(
        checkouts.asked.lock().unwrap().last().map(String::as_str),
        Some(format!("remove /work {CHECKOUT}").as_str())
    );
    let _ = said;
}

/// A daemon started over `world`'s store, engine and session homes, after
/// the world's own was killed, its locks probing as `locks`.
fn restarted(world: &World, locks: LockProbe) -> Daemon<TestEdge> {
    Daemon::new(Ports {
        edge: Arc::clone(&world.edge),
        engines: vec![HostedEngine {
            engine: world.engine.clone(),
            child: world.engine.clone(),
            transcripts: Arc::new(|_| None),
            for_launch: None,
        }],
        plans: Box::new(Plans),
        hand_offs: Box::new(Handed::of(BTreeMap::new())),
        homes: Box::new(world.homes.clone()),
        ears: Box::new(Listening::default()),
        run_locks: Arc::new(Locks(locks)),
        place: Place {
            home: "/home/u".to_string(),
            program_dir: None,
            os: "linux".to_string(),
            user_home: None,
        },
        folds: Folds::default(),
        say: Box::new(|_| {}),
    })
}

/// A hosted child bound to its engine's session `n1`, then its daemon
/// killed (dropped: its locks let go).
async fn killed_with_a_bound_child() -> World {
    let mut world = World::hosting().await;
    world.say(ChildObservation::Started {
        native_id: "n1".to_string(),
    });
    world.tick().await;
    world.daemon = restarted(&world, LockProbe::Unproven);
    world
}

/// The incarnations `CHILD` was owed, with their reasons.
async fn owed(world: &World) -> Vec<(u32, String)> {
    world
        .session_events(CHILD)
        .await
        .into_iter()
        .filter_map(|event| match event {
            SessionEvent::IncarnationOwed(owed) => Some((owed.incarnation, owed.reason)),
            _ => None,
        })
        .collect()
}

// T19, S18: a killed daemon's child is swept (its run lost) by the next
// start, then re-hosted as its session's next incarnation, cause `rehost`,
// and started again resuming its own conversation (its bound native id).
#[tokio::test]
async fn a_killed_daemons_child_is_rehosted_resuming_its_own_conversation() {
    let mut world = killed_with_a_bound_child().await;
    world.daemon = restarted(&world, LockProbe::Free);
    let swept = world
        .daemon
        .start_up(&world.edge.store, world.edge.catalog())
        .await
        .unwrap();
    assert_eq!(swept.len(), 1, "the lost run is swept");
    for _ in 0..3 {
        world.tick().await;
    }
    assert_eq!(
        owed(&world).await,
        [(1, "first".to_string()), (2, "rehost".to_string())]
    );
    let started = world.engine.started.lock().unwrap().clone();
    assert_eq!(started.len(), 2, "started again");
    assert_eq!(started[0].resume, None, "its first start resumed nothing");
    assert_eq!(
        started[1].resume.as_deref(),
        Some("n1"),
        "its own conversation"
    );
    assert_eq!(world.daemon.hosting(), 1);
}

// S47: a child re-hosted 60 times in the hour before its loss is not
// re-hosted again: it stays ended, and its owner is told it ended.
#[tokio::test]
async fn a_child_past_its_rehost_budget_stays_ended_and_its_owner_is_told() {
    let mut world = killed_with_a_bound_child().await;
    let facts = runs_application::Facts::new(Arc::clone(&world.edge));
    for n in 1..=60 {
        facts.incarnation_ended(CHILD, n, "orphaned").await.unwrap();
        world
            .handle(
                CHILD,
                sessions::RehostSession {
                    recent_rehosts: 0,
                    cause: String::new(),
                },
            )
            .await;
    }
    facts
        .incarnation_ended(CHILD, 61, "orphaned")
        .await
        .unwrap();
    world.daemon = restarted(&world, LockProbe::Free);
    world
        .daemon
        .start_up(&world.edge.store, world.edge.catalog())
        .await
        .unwrap();
    for _ in 0..3 {
        world.tick().await;
    }
    let owed = owed(&world).await;
    assert_eq!(owed.last(), Some(&(61, "rehost".to_string())), "no 62nd");
    assert_eq!(
        world.engine.started.lock().unwrap().len(),
        1,
        "not started again"
    );
    let told: Vec<(String, String)> = world
        .session_events(OWNER)
        .await
        .into_iter()
        .filter_map(|event| match event {
            SessionEvent::MessageSent(sent) => Some((sent.message_id, sent.text)),
            _ => None,
        })
        .collect();
    let notice = told
        .iter()
        .find(|(id, _)| id == &format!("notice:{CHILD}:61:rehost-budget"))
        .unwrap_or_else(|| panic!("the owner was told: {told:?}"));
    assert!(notice.1.starts_with("child kid ended"), "{}", notice.1);
}

// A hosted child's turn is in flight from its queue until its end.
#[tokio::test]
async fn a_hosted_childs_turn_is_in_flight_until_it_ends() {
    let mut world = World::hosting().await;
    assert!(!world.daemon.in_flight(), "no turn yet");
    world
        .handle(
            CHILD,
            sessions::SendMessage {
                message_id: "m-1".to_string(),
                urgency: Urgency::Wake,
                text: "go".to_string(),
                requester: None,
                supported: vec![Urgency::Inform, Urgency::Wake, Urgency::Steer],
            },
        )
        .await;
    for _ in 0..3 {
        world.tick().await;
    }
    assert!(world.daemon.in_flight(), "its turn is in flight");
}

// ---------------------------------------------------------------- the drain (s4-Q4)

/// The incarnations `session` ended, with their causes.
async fn ended(world: &World, session: &str) -> Vec<(u32, String)> {
    world
        .session_events(session)
        .await
        .into_iter()
        .filter_map(|event| match event {
            SessionEvent::IncarnationEnded(ended) => Some((ended.incarnation, ended.cause)),
            _ => None,
        })
        .collect()
}

/// A hosted child bound to its engine's session `n1`, its hand-off in
/// `files`, with no turn in flight.
async fn idle_bound_child(files: &Files) -> World {
    let mut world = hosting_from(files, &ForAgent::default()).await;
    world.say(ChildObservation::Started {
        native_id: "n1".to_string(),
    });
    world.tick().await;
    assert!(!world.daemon.in_flight(), "idle");
    world
}

/// The child's exit, as its engine says it.
fn exited() -> ChildObservation {
    ChildObservation::Exited {
        exit: Exit {
            code: Some(0),
            signal: None,
        },
        detail: String::new(),
    }
}

/// A wake to the child.
fn wake(id: &str) -> sessions::SendMessage {
    sessions::SendMessage {
        message_id: id.to_string(),
        urgency: Urgency::Wake,
        text: format!("text of {id}"),
        requester: None,
        supported: vec![Urgency::Inform, Urgency::Wake, Urgency::Steer],
    }
}

// OD-54: a daemon handing over ends an idle child at once;
// at its exit its incarnation ends `drained` (not `runner-exit`), its
// hand-off is KEPT for the daemon that re-hosts it, and, hosting nothing
// more, it may exit.
#[tokio::test]
async fn a_draining_daemon_ends_an_idle_child_at_once_keeping_its_hand_off() {
    let files = Files::default();
    let mut world = idle_bound_child(&files).await;
    world.daemon.hand_over();
    world.tick().await;
    assert_eq!(world.engine.asked.lock().unwrap().ends, 1, "ended at once");
    world.tick().await;
    assert_eq!(world.engine.asked.lock().unwrap().ends, 1, "ended once");
    world.say(exited());
    world.tick().await;
    assert_eq!(ended(&world, CHILD).await, [(1, "drained".to_string())]);
    assert_eq!(world.daemon.hosting(), 0, "it hosts nothing more");
    assert!(
        files.deleted.lock().unwrap().is_empty(),
        "its hand-off kept"
    );
    assert!(files.kept.lock().unwrap().contains_key(CHILD));
}

/// The drain's guidance's id to the child's first run (OD-54).
fn guidance_id() -> String {
    format!("drain:{CHILD}:1")
}

/// A hosted child bound to its engine's session `n1` whose turn for `m-1`
/// is in flight.
async fn busy_child(world: &mut World) {
    world.say(ChildObservation::Started {
        native_id: "n1".to_string(),
    });
    world.tick().await;
    world.handle(CHILD, wake("m-1")).await;
    for _ in 0..3 {
        world.tick().await;
    }
    assert!(world.daemon.in_flight(), "its turn is in flight");
}

/// The turn that carried `ids` ended, answered.
fn answered(ids: &[&str]) -> ChildObservation {
    ChildObservation::TurnEnded {
        message_ids: ids.iter().map(|id| id.to_string()).collect(),
        outcome: TurnOutcome::Answered {
            answer: "done".to_string(),
            native_id: Some("n1".to_string()),
        },
    }
}

/// The messages sent to the child: their ids, urgencies and texts.
async fn messages_sent(world: &World) -> Vec<(String, Urgency, String)> {
    world
        .session_events(CHILD)
        .await
        .into_iter()
        .filter_map(|event| match event {
            SessionEvent::MessageSent(sent) => Some((sent.message_id, sent.urgency, sent.text)),
            _ => None,
        })
        .collect()
}

// OD-54 (1): a busy child is STEERED with the approved guidance, the
// session's own steer delivered as any is, interrupting its turn now; it is
// ended once the guidance's turn ends, and only then.
#[tokio::test]
async fn a_draining_daemon_steers_a_busy_child_and_ends_it_once_its_guidance_turn_ends() {
    let files = Files::default();
    let mut world = hosting_from(&files, &ForAgent::default()).await;
    busy_child(&mut world).await;
    world.daemon.hand_over();
    for _ in 0..3 {
        world.tick().await;
    }
    let guidance = (
        guidance_id(),
        Urgency::Steer,
        session_policies::DRAIN_GUIDANCE.to_string(),
    );
    assert!(
        messages_sent(&world).await.contains(&guidance),
        "the guidance is the child's own steer"
    );
    assert!(
        world
            .engine
            .asked
            .lock()
            .unwrap()
            .sent
            .contains(&(guidance_id(), ChildUrgency::Steer)),
        "handed to the child as a steer, interrupting its turn"
    );
    assert_eq!(world.engine.asked.lock().unwrap().ends, 0, "not cut");
    world.say(ChildObservation::TurnEnded {
        message_ids: vec!["m-1".to_string()],
        outcome: TurnOutcome::Failed {
            class: run_ports::FailureClass::Interrupted,
            detail: "interrupted".to_string(),
            native_id: Some("n1".to_string()),
        },
    });
    world.say(ChildObservation::TurnStarted {
        message_ids: vec![guidance_id()],
    });
    for _ in 0..2 {
        world.tick().await;
    }
    assert_eq!(
        world.engine.asked.lock().unwrap().ends,
        0,
        "its guidance's turn is in flight"
    );
    // A wake sent meanwhile is the next daemon's: never a turn here.
    world.handle(CHILD, wake("m-late")).await;
    world.say(answered(&[&guidance_id()]));
    for _ in 0..3 {
        world.tick().await;
    }
    assert_eq!(world.engine.asked.lock().unwrap().ends, 1, "at its end");
    let late = world.run_events().await.into_iter().any(|event| {
        matches!(event, RunEvent::TurnQueued(queued)
            if queued.message_ids.contains(&"m-late".to_string()))
    });
    assert!(!late, "the late wake stays pending");
    world.say(exited());
    world.tick().await;
    assert_eq!(ended(&world, CHILD).await, [(1, "drained".to_string())]);
}

// OD-54 (1): an idle child is ended at once, given no guidance.
#[tokio::test]
async fn a_draining_daemon_gives_an_idle_child_no_guidance() {
    let files = Files::default();
    let mut world = idle_bound_child(&files).await;
    world.daemon.hand_over();
    for _ in 0..2 {
        world.tick().await;
    }
    assert!(
        !messages_sent(&world)
            .await
            .iter()
            .any(|(id, ..)| id.starts_with("drain:")),
        "no guidance"
    );
    assert_eq!(world.engine.asked.lock().unwrap().ends, 1);
}

// OD-54: a busy child whose turn ends before its guidance is delivered is
// given the guidance first (its turn, as a wake to an idle child), and only
// then ended, so no guidance is left pending for the next daemon.
#[tokio::test]
async fn a_child_idle_before_its_guidance_was_delivered_is_given_it_before_it_is_ended() {
    let files = Files::default();
    let mut world = hosting_from(&files, &ForAgent::default()).await;
    busy_child(&mut world).await;
    world.daemon.hand_over();
    world.tick().await;
    world.say(answered(&["m-1"]));
    for _ in 0..3 {
        world.tick().await;
    }
    assert_eq!(world.engine.asked.lock().unwrap().ends, 0, "not yet");
    assert!(
        world
            .engine
            .asked
            .lock()
            .unwrap()
            .sent
            .iter()
            .any(|(id, _)| *id == guidance_id()),
        "the guidance given"
    );
    world.say(answered(&[&guidance_id()]));
    for _ in 0..2 {
        world.tick().await;
    }
    assert_eq!(world.engine.asked.lock().unwrap().ends, 1);
}

// OD-54 (2): the hand-over's period is five minutes, a named constant.
#[test]
fn the_hand_over_period_is_five_minutes() {
    assert_eq!(
        crate::HAND_OVER_PERIOD,
        std::time::Duration::from_secs(5 * 60)
    );
}

// OD-54 (2): a child still busy once the hand-over's period has passed on
// the daemon's clock is handed over automatically: ended once, its turn in
// flight cut, its incarnation ended `drained`, its hand-off kept; the next
// daemon re-hosts it on its own conversation.
#[tokio::test]
async fn a_child_still_busy_after_the_period_is_handed_over_its_turn_cut() {
    let files = Files::default();
    let mut world = hosting_from(&files, &ForAgent::default()).await;
    busy_child(&mut world).await;
    world.daemon.hand_over();
    for _ in 0..3 {
        world.tick().await;
    }
    world
        .clock
        .pass(crate::HAND_OVER_PERIOD - std::time::Duration::from_millis(1));
    world.tick().await;
    assert_eq!(world.engine.asked.lock().unwrap().ends, 0, "within it");
    world.clock.pass(std::time::Duration::from_millis(1));
    world.tick().await;
    assert_eq!(world.engine.asked.lock().unwrap().ends, 1, "at its end");
    world.tick().await;
    assert_eq!(world.engine.asked.lock().unwrap().ends, 1, "once");
    let log = world.said.lock().unwrap().join("\n");
    assert!(
        log.contains("kid: still busy after 300s: its turn in flight cut, handed over"),
        "{log}"
    );
    world.say(exited());
    world.tick().await;
    assert_eq!(ended(&world, CHILD).await, [(1, "drained".to_string())]);
    assert!(files.kept.lock().unwrap().contains_key(CHILD), "kept");
    world.daemon = restarted(&world, LockProbe::Free);
    world
        .daemon
        .start_up(&world.edge.store, world.edge.catalog())
        .await
        .unwrap();
    for _ in 0..3 {
        world.tick().await;
    }
    assert_eq!(
        owed(&world).await,
        [(1, "first".to_string()), (2, "drain".to_string())]
    );
    let started = world.engine.started.lock().unwrap().clone();
    assert_eq!(started[1].resume.as_deref(), Some("n1"));
}

// OD-54 (2): the period is the daemon's to be given (a test's is short),
// timed from the hand-over's start, not the daemon's.
#[tokio::test]
async fn the_hand_over_period_is_the_one_given_timed_from_the_hand_over() {
    let files = Files::default();
    let period = std::time::Duration::from_secs(2);
    let mut world = hosting_from_within(&files, &ForAgent::default(), Some(period)).await;
    busy_child(&mut world).await;
    world.clock.pass(std::time::Duration::from_secs(60));
    world.daemon.hand_over();
    world.tick().await;
    assert_eq!(world.engine.asked.lock().unwrap().ends, 0, "timed from now");
    world.clock.pass(period);
    world.tick().await;
    assert_eq!(world.engine.asked.lock().unwrap().ends, 1);
}

// Owner, 2026-11-15: a timeout changed while the drain runs (the root
// reads `daemon.drain_timeout` again at each round) is honoured by the
// drain in progress, still timed from the hand-over's start: shortened, a
// busy child is handed over at the new period's end; lengthened, it waits.
#[tokio::test]
async fn a_period_changed_during_the_drain_is_the_drains() {
    use std::time::Duration;
    let files = Files::default();
    let mut world = hosting_from(&files, &ForAgent::default()).await;
    busy_child(&mut world).await;
    world.daemon.hand_over();
    world.tick().await;
    world.clock.pass(Duration::from_secs(10));
    world.daemon.hand_over_within(Duration::from_secs(600));
    world.clock.pass(crate::HAND_OVER_PERIOD);
    world.tick().await;
    assert_eq!(
        world.engine.asked.lock().unwrap().ends,
        0,
        "lengthened: it waits"
    );
    assert_eq!(world.daemon.hand_over_period(), Duration::from_secs(600));
    world.daemon.hand_over_within(Duration::from_secs(30));
    world.tick().await;
    assert_eq!(
        world.engine.asked.lock().unwrap().ends,
        1,
        "shortened: past it"
    );
    let log = world.said.lock().unwrap().join("\n");
    assert!(
        log.contains("kid: still busy after 30s: its turn in flight cut, handed over"),
        "{log}"
    );
}

// OD-54 (2): the drain warns at once, in the daemon's log: each busy
// child steered, and when it is handed over at the latest.
#[tokio::test]
async fn a_draining_daemon_warns_at_once_of_each_busy_child() {
    let files = Files::default();
    let mut world = hosting_from(&files, &ForAgent::default()).await;
    busy_child(&mut world).await;
    world.daemon.hand_over();
    for _ in 0..3 {
        world.tick().await;
    }
    let warned = "kid: a turn in flight: steered to end it; handed over within 300s, its turn cut";
    let said = world.said.lock().unwrap().clone();
    assert_eq!(
        said.iter().filter(|line| line.as_str() == warned).count(),
        1,
        "warned once: {said:?}"
    );
}

// s4-Q4: a daemon handing over delivers nothing more: a wake sent to an
// idle child stays pending (the daemon that re-hosts it delivers it), and
// the child is ended, not given a turn.
#[tokio::test]
async fn a_draining_daemon_delivers_no_message() {
    let files = Files::default();
    let mut world = idle_bound_child(&files).await;
    world.daemon.hand_over();
    world.handle(CHILD, wake("m-late")).await;
    for _ in 0..3 {
        world.tick().await;
    }
    assert!(
        world.engine.asked.lock().unwrap().sent.is_empty(),
        "nothing fed"
    );
    let queued = world
        .run_events()
        .await
        .into_iter()
        .any(|event| matches!(event, RunEvent::TurnQueued(_)));
    assert!(!queued, "no turn queued");
    assert_eq!(world.engine.asked.lock().unwrap().ends, 1);
}

// s4-Q4, s4-Q5: the daemon after a drain re-hosts the child it handed
// over as its next incarnation, cause `drain`, resuming its own
// conversation.
#[tokio::test]
async fn the_next_daemon_rehosts_a_drained_child_resuming_its_conversation() {
    let files = Files::default();
    let mut world = idle_bound_child(&files).await;
    world.daemon.hand_over();
    world.tick().await;
    world.say(exited());
    world.tick().await;
    world.daemon = restarted(&world, LockProbe::Free);
    world
        .daemon
        .start_up(&world.edge.store, world.edge.catalog())
        .await
        .unwrap();
    for _ in 0..3 {
        world.tick().await;
    }
    assert_eq!(
        owed(&world).await,
        [(1, "first".to_string()), (2, "drain".to_string())]
    );
    let started = world.engine.started.lock().unwrap().clone();
    assert_eq!(started.len(), 2, "started again");
    assert_eq!(started[1].resume.as_deref(), Some("n1"));
    assert_eq!(world.daemon.hosting(), 1);
}

// `daemon stop --even-if-live` (the escalation) ends a child as before: its
// incarnation ends `runner-exit`, and no daemon re-hosts it.
#[tokio::test]
async fn a_child_ended_by_an_escalation_is_not_handed_over() {
    let files = Files::default();
    let mut world = idle_bound_child(&files).await;
    world.daemon.end_all();
    world.say(exited());
    world.tick().await;
    assert_eq!(ended(&world, CHILD).await, [(1, "runner-exit".to_string())]);
}

/// A one-shot host whose one-shot is placed and runs until stopped; how
/// many times its control was asked to stop.
struct Working(Arc<std::sync::atomic::AtomicUsize>);

struct Stopped(Arc<std::sync::atomic::AtomicUsize>);

impl crate::OneShotControl for Stopped {
    fn stop(&self) {
        self.0.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
    }
}

/// The hosted one-shot's run root, as text.
const ONE_SHOT_RUN: &str = "0a0a0a0a-0a0a-4a0a-8a0a-0a0a0a0a0a0a";

impl crate::OneShotHost for Working {
    fn host(&self, _: crate::HostRequest, told: crate::Told) -> Box<dyn crate::OneShotControl> {
        told.placed(&run_application::Placement {
            run: ONE_SHOT_RUN.to_string(),
            session: "0b0b0b0b-0b0b-4b0b-8b0b-0b0b0b0b0b0b".to_string(),
            name: "projects/calm-quiet-heron/sessions/quick-fox".to_string(),
            harp: "quick-fox".to_string(),
            engine: ENGINE.to_string(),
            ..Default::default()
        });
        Box::new(Stopped(Arc::clone(&self.0)))
    }
}

impl World {
    /// The world, its daemon hosting the one-shots of `one_shots`.
    fn hosting_one_shots(self, one_shots: Arc<crate::OneShots>) -> World {
        let World {
            edge,
            engine,
            homes,
            daemon,
            clock,
            said,
        } = self;
        World {
            daemon: daemon.hosting_one_shots(one_shots),
            edge,
            engine,
            homes,
            clock,
            said,
        }
    }
}

// U8c (owner, 2026-11-15): a one-shot a draining daemon hosts is left to
// run within the hand-over's period, and once the period has passed on
// the daemon's clock, still running, it is stopped as `daemon stop
// --even-if-live` stops one (SIGTERM, so its caller exits 143), once, and
// said in the log.
#[tokio::test]
async fn a_one_shot_still_running_after_the_period_is_stopped() {
    let period = std::time::Duration::from_secs(2);
    let world = World::built_within(
        Box::new(Plans),
        LockProbe::Unproven,
        Box::new(Handed::of(BTreeMap::new())),
        Some(period),
    );
    let stops = Arc::new(std::sync::atomic::AtomicUsize::new(0));
    let one_shots = Arc::new(crate::OneShots::new(Box::new(Working(Arc::clone(&stops)))));
    let mut world = world.hosting_one_shots(Arc::clone(&one_shots));
    let _ = one_shots.create(crate::HostRequest::default());
    assert_eq!(world.daemon.hosting(), 1, "the one-shot is hosted");
    world.daemon.hand_over();
    world.tick().await;
    world
        .clock
        .pass(period - std::time::Duration::from_millis(1));
    world.tick().await;
    let asked = || stops.load(std::sync::atomic::Ordering::SeqCst);
    assert_eq!(asked(), 0, "within the period");
    world.clock.pass(std::time::Duration::from_millis(1));
    assert!(world.tick().await, "busy: it stopped one");
    assert_eq!(asked(), 1, "at its end");
    world.tick().await;
    assert_eq!(asked(), 1, "once");
    let events: Vec<daemon_proto::v1::RunEvent> =
        one_shots.watch(ONE_SHOT_RUN).unwrap().take(1).collect();
    match &events[0].event {
        Some(daemon_proto::v1::run_event::Event::Stop(stop)) => assert_eq!(stop.reason, "SIGTERM"),
        other => panic!("a stop: {other:?}"),
    }
    let log = world.said.lock().unwrap().join("\n");
    assert!(
        log.contains("quick-fox: a one-shot still running after 2s: its engine stopped (SIGTERM)"),
        "{log}"
    );
}

/// Each output root's read: its project and the handed `PATH`.
type RootsRead = Arc<Mutex<Vec<(String, Option<String>)>>>;

/// A plan of the child whose projects' output roots are read: each read's
/// project and handed `PATH` recorded; `root` none, the read fails.
struct Outputs {
    asked: RootsRead,
    root: Option<&'static str>,
}

impl ChildPlans for Outputs {
    fn output_root(
        &self,
        project_dir: &str,
        env: &BTreeMap<String, String>,
    ) -> Result<String, String> {
        self.asked
            .lock()
            .unwrap()
            .push((project_dir.to_string(), env.get("PATH").cloned()));
        self.root
            .map(str::to_string)
            .ok_or_else(|| "library-application-config-unreadable".to_string())
    }

    fn preview(
        &self,
        engine: &str,
        agent: &str,
        project_dir: &str,
        env: &BTreeMap<String, String>,
        trusted: bool,
    ) -> Result<Previewed, String> {
        Plans.preview(engine, agent, project_dir, env, trusted)
    }

    fn git_user(&self, _: &str, _: &BTreeMap<String, String>) -> Option<run_ports::GitUser> {
        None
    }
}

/// A world whose owner launched its child into the project `far-heron` at
/// `/far` (trusted), its output roots read by `outputs`, ticked thrice.
async fn launched_across(outputs: Outputs) -> World {
    launched_into(outputs, "far-heron", "/far", false).await
}

/// A world whose owner launched its child into the project `project` at
/// `root` (its own when `same_project`), its output roots read by
/// `outputs`, ticked thrice.
async fn launched_into(outputs: Outputs, project: &str, root: &str, same_project: bool) -> World {
    let mut world = World::new(Box::new(outputs));
    world.name_project().await;
    world.mint(OWNER, "boss", None).await;
    world
        .handle(
            OWNER,
            sessions::LaunchChild {
                child_project_id: project.to_string(),
                child_session: CHILD.to_string(),
                child_root: root.to_string(),
                harp: "kid".to_string(),
                engine: ENGINE.to_string(),
                trust: sessions::TargetTrust {
                    same_project,
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
    world
}

// Step 8 U2b: a child launched into another project keeps its outputs in
// THAT project's output folder, as its config says, under its own harp:
// read in its project with the environment its launcher handed.
#[tokio::test]
async fn a_child_in_another_project_keeps_its_outputs_in_its_projects_folder() {
    let asked = Arc::new(Mutex::new(Vec::new()));
    let world = launched_across(Outputs {
        asked: Arc::clone(&asked),
        root: Some("/far/notes/"),
    })
    .await;
    let minted: Vec<(String, String)> = world
        .session_events(CHILD)
        .await
        .into_iter()
        .filter_map(|event| match event {
            SessionEvent::SessionMinted(minted) => Some((minted.project_id, minted.output_dir)),
            _ => None,
        })
        .collect();
    assert_eq!(
        minted,
        [("far-heron".to_string(), "/far/notes/kid".to_string())]
    );
    assert_eq!(
        *asked.lock().unwrap(),
        [("/far".to_string(), Some("/usr/bin".to_string()))]
    );
}

// Owner ruling 2026-11-16: a child of its owner's own project keeps its
// outputs in its OWN folder, `<output.dir>/<its harp>`, never its owner's
// (`/work/out`): one rule for every child, as for every session.
#[tokio::test]
async fn a_child_in_its_owners_project_keeps_its_outputs_in_its_own_folder() {
    let asked = Arc::new(Mutex::new(Vec::new()));
    let world = launched_into(
        Outputs {
            asked: Arc::clone(&asked),
            root: Some("/work/ctxloom"),
        },
        "calm-otter",
        "/work",
        true,
    )
    .await;
    let minted: Vec<String> = world
        .session_events(CHILD)
        .await
        .into_iter()
        .filter_map(|event| match event {
            SessionEvent::SessionMinted(minted) => Some(minted.output_dir),
            _ => None,
        })
        .collect();
    assert_eq!(minted, ["/work/ctxloom/kid".to_string()]);
    assert_eq!(asked.lock().unwrap().len(), 1, "read in its project once");
    assert_eq!(asked.lock().unwrap()[0].0, "/work");
}

// Step 8 U2b: a child whose project's output folder cannot be read is not
// minted: its owner is told the launch failed, and holds it no longer.
#[tokio::test]
async fn a_child_whose_projects_output_folder_cannot_be_read_is_not_launched() {
    let world = launched_across(Outputs {
        asked: Arc::default(),
        root: None,
    })
    .await;
    assert!(world.session_events(CHILD).await.is_empty(), "not minted");
    let failed = world
        .session_events(OWNER)
        .await
        .into_iter()
        .any(|event| matches!(event, SessionEvent::ChildLaunchFailed(_)));
    assert!(failed, "the owner was told");
    let said = world.said.lock().unwrap().join("\n");
    assert!(
        said.contains("kid: its output folder not read: library-application-config-unreadable"),
        "{said}"
    );
}

// Step 8 U4: a hosted child asks as the agent it was launched for (its
// hand-off's), on the engine it is hosted on.
#[tokio::test]
async fn a_hosted_child_asks_as_the_agent_its_hand_off_names() {
    let files = Files::default();
    let plans = ForAgent::default();
    let world = hosting_from(&files, &plans).await;
    let askers = world.daemon.askers();
    let [asker] = askers.as_slice() else {
        panic!("one asker: {askers:?}");
    };
    assert_eq!(asker.session, CHILD);
    assert_eq!(asker.engine, ENGINE);
    assert_eq!(asker.agent, "reviewer");
}

/// A plan of the child that settles the agent `dev`.
struct DevPlans;

impl ChildPlans for DevPlans {
    fn output_root(
        &self,
        project_dir: &str,
        env: &BTreeMap<String, String>,
    ) -> Result<String, String> {
        Plans.output_root(project_dir, env)
    }

    fn preview(
        &self,
        engine: &str,
        agent: &str,
        project_dir: &str,
        env: &BTreeMap<String, String>,
        trusted: bool,
    ) -> Result<Previewed, String> {
        let mut previewed = Plans.preview(engine, agent, project_dir, env, trusted)?;
        previewed.plan.agent = "dev".to_string();
        Ok(previewed)
    }

    fn git_user(&self, _: &str, _: &BTreeMap<String, String>) -> Option<run_ports::GitUser> {
        None
    }
}

// With no agent handed, a hosted child asks as the agent its plan settled.
#[tokio::test]
async fn a_hosted_child_with_no_handed_agent_asks_as_its_plans() {
    let mut world = World::new(Box::new(DevPlans));
    world.name_project().await;
    world.mint(OWNER, "boss", None).await;
    world.mint(CHILD, "kid", Some(OWNER)).await;
    world.tick().await;
    let askers = world.daemon.askers();
    assert_eq!(askers.len(), 1, "{askers:?}");
    assert_eq!(askers[0].agent, "dev");
}

// ------------------------------------------- a restart (step 10e Stage 2a)
//
// The session decided a restart is owed (`RestartOwed`, from an operator's
// `RequestRestart`); the daemon that hosts the engine waits for the Active to
// Idle edge, ends the engine, and the session's next incarnation (reason
// `restart`) is hosted again resuming the engine's own conversation.

const CHILD_TWO: &str = "5f0c7a52-3333-5333-8333-000000000003";

fn restart_request() -> sessions::RequestRestart {
    sessions::RequestRestart {
        reason: sessions::RestartReason::Operator,
        note: String::new(),
    }
}

/// The `RestartOwed` events `session` holds.
async fn restarts_owed(world: &World, session: &str) -> usize {
    world
        .session_events(session)
        .await
        .iter()
        .filter(|event| matches!(event, SessionEvent::RestartOwed(_)))
        .count()
}

/// Rounds until the daemon has nothing more to do about what it was told.
async fn settle(world: &mut World) {
    for _ in 0..4 {
        world.tick().await;
    }
}

/// The messages the child's control was handed, by id and urgency.
fn handed_to_child(world: &World) -> Vec<(String, ChildUrgency)> {
    world.engine.asked.lock().unwrap().sent.clone()
}

// Idle, a restart is carried out at once: the engine ended, its incarnation
// ended `restarted`, the next owed for `restart` and started resuming `n1`.
#[tokio::test]
async fn a_restart_asked_of_an_idle_child_is_carried_out_at_once() {
    let files = Files::default();
    let mut world = idle_bound_child(&files).await;
    world.handle(CHILD, restart_request()).await;
    world.tick().await;
    assert_eq!(world.engine.asked.lock().unwrap().ends, 1, "ended at once");
    world.tick().await;
    assert_eq!(world.engine.asked.lock().unwrap().ends, 1, "ended once");
    world.say(exited());
    settle(&mut world).await;
    assert_eq!(ended(&world, CHILD).await, [(1, "restarted".to_string())]);
    assert_eq!(
        owed(&world).await,
        [(1, "first".to_string()), (2, "restart".to_string())]
    );
    let started = world.engine.started.lock().unwrap().clone();
    assert_eq!(started.len(), 2, "started again");
    assert_eq!(started[1].resume.as_deref(), Some("n1"), "its conversation");
    assert_eq!(world.daemon.hosting(), 1);
    assert!(
        files.deleted.lock().unwrap().is_empty(),
        "its hand-off kept"
    );
}

// Active, it is PARKED: the turn in flight is never cut; the restart is
// carried out at the turn's end.
#[tokio::test]
async fn a_restart_asked_of_a_busy_child_waits_for_its_turn_to_end() {
    let files = Files::default();
    let mut world = hosting_from(&files, &ForAgent::default()).await;
    busy_child(&mut world).await;
    world.handle(CHILD, restart_request()).await;
    settle(&mut world).await;
    assert_eq!(
        world.engine.asked.lock().unwrap().ends,
        0,
        "parked, not cut"
    );
    assert_eq!(restarts_owed(&world, CHILD).await, 1);
    world.say(answered(&["m-1"]));
    settle(&mut world).await;
    assert_eq!(
        world.engine.asked.lock().unwrap().ends,
        1,
        "at the turn's end"
    );
    world.say(exited());
    settle(&mut world).await;
    assert_eq!(ended(&world, CHILD).await, [(1, "restarted".to_string())]);
    let started = world.engine.started.lock().unwrap().clone();
    assert_eq!(started.len(), 2);
    assert_eq!(started[1].resume.as_deref(), Some("n1"));
}

// Asked twice, or replayed after it was carried out, it restarts once.
#[tokio::test]
async fn a_request_asked_twice_or_replayed_restarts_once() {
    let files = Files::default();
    let mut world = idle_bound_child(&files).await;
    let key = format!("restart:{CHILD}:1:operator");
    let keyed = || sessions::keyed_book(session_cover(CHILD), &restart_request(), &key);
    world.edge.handle(keyed()).await.unwrap();
    world.handle(CHILD, restart_request()).await;
    world.tick().await;
    world.say(exited());
    settle(&mut world).await;
    // Replayed once the new incarnation runs.
    world.edge.handle(keyed()).await.unwrap();
    settle(&mut world).await;
    assert_eq!(restarts_owed(&world, CHILD).await, 1, "recorded once");
    assert_eq!(world.engine.asked.lock().unwrap().ends, 1, "ended once");
    assert_eq!(
        world.engine.started.lock().unwrap().len(),
        2,
        "restarted once"
    );
}

// The engine exiting with no turn end (a dropped hook, a crash) forces Idle:
// the restart owed proceeds.
#[tokio::test]
async fn an_engine_exit_without_a_turn_end_lets_a_parked_restart_proceed() {
    let files = Files::default();
    let mut world = hosting_from(&files, &ForAgent::default()).await;
    busy_child(&mut world).await;
    world.handle(CHILD, restart_request()).await;
    settle(&mut world).await;
    assert_eq!(world.engine.asked.lock().unwrap().ends, 0, "parked");
    world.say(exited());
    settle(&mut world).await;
    assert_eq!(ended(&world, CHILD).await, [(1, "runner-exit".to_string())]);
    assert_eq!(
        owed(&world).await,
        [(1, "first".to_string()), (2, "restart".to_string())]
    );
    let started = world.engine.started.lock().unwrap().clone();
    assert_eq!(started.len(), 2, "started again");
    assert_eq!(started[1].resume.as_deref(), Some("n1"));
}

// A parked agent does not block another's launch.
#[tokio::test]
async fn a_second_agents_launch_is_not_blocked_while_the_first_is_parked() {
    let files = Files::default();
    let mut world = hosting_from(&files, &ForAgent::default()).await;
    busy_child(&mut world).await;
    world.handle(CHILD, restart_request()).await;
    settle(&mut world).await;
    assert_eq!(world.daemon.hosting(), 1);
    world.mint(CHILD_TWO, "kid-two", Some(OWNER)).await;
    settle(&mut world).await;
    assert_eq!(world.daemon.hosting(), 2, "the second is hosted");
    assert_eq!(
        world.engine.asked.lock().unwrap().ends,
        0,
        "the first is parked still"
    );
}

// While parked: a steer still reaches the turn in flight; an inform waits
// and is delivered into the resumed session with the wake that starts its
// turn.
#[tokio::test]
async fn a_parked_child_takes_a_steer_and_holds_an_inform_for_the_resumed_session() {
    let files = Files::default();
    let mut world = hosting_from(&files, &ForAgent::default()).await;
    busy_child(&mut world).await;
    world.handle(CHILD, restart_request()).await;
    world
        .handle(
            CHILD,
            sessions::SendMessage {
                message_id: "i-1".to_string(),
                urgency: Urgency::Inform,
                text: "fyi".to_string(),
                requester: None,
                supported: vec![Urgency::Inform, Urgency::Wake, Urgency::Steer],
            },
        )
        .await;
    settle(&mut world).await;
    assert!(
        !handed_to_child(&world).iter().any(|(id, _)| id == "i-1"),
        "the inform waits"
    );
    world
        .handle(
            CHILD,
            sessions::SendMessage {
                message_id: "s-1".to_string(),
                urgency: Urgency::Steer,
                text: "stop".to_string(),
                requester: None,
                supported: vec![Urgency::Inform, Urgency::Wake, Urgency::Steer],
            },
        )
        .await;
    settle(&mut world).await;
    assert!(
        handed_to_child(&world).contains(&("s-1".to_string(), ChildUrgency::Steer)),
        "the steer reaches the turn in flight"
    );
    assert_eq!(world.engine.asked.lock().unwrap().ends, 0, "still parked");
    // The turns end; the restart is carried out; the inform is still owed.
    world.say(ChildObservation::TurnEnded {
        message_ids: vec!["m-1".to_string()],
        outcome: TurnOutcome::Failed {
            class: run_ports::FailureClass::Interrupted,
            detail: "interrupted".to_string(),
            native_id: Some("n1".to_string()),
        },
    });
    world.say(answered(&["s-1", "i-1"]));
    settle(&mut world).await;
    world.say(exited());
    settle(&mut world).await;
    assert_eq!(world.daemon.hosting(), 1, "resumed");
    assert!(world.daemon.in_flight() || owed(&world).await.len() == 2);
    world.handle(CHILD, wake("w-1")).await;
    settle(&mut world).await;
    let queued: Vec<Vec<String>> = world
        .events_of_run(2)
        .await
        .into_iter()
        .filter_map(|event| match event {
            RunEvent::TurnQueued(queued) => Some(queued.message_ids),
            _ => None,
        })
        .collect();
    assert_eq!(queued.len(), 1, "{queued:?}");
    assert!(queued[0].contains(&"w-1".to_string()));
}

// A daemon restarted mid-park (its folds start from 0, its hosting lost):
// the restart owed is in the stream and is honoured exactly once.
#[tokio::test]
async fn a_restart_owed_survives_the_daemon_and_is_honoured_once() {
    let mut world = killed_with_a_bound_child().await;
    world.handle(CHILD, restart_request()).await;
    world.daemon = restarted(&world, LockProbe::Free);
    world
        .daemon
        .start_up(&world.edge.store, world.edge.catalog())
        .await
        .unwrap();
    for _ in 0..4 {
        world.tick().await;
    }
    assert_eq!(
        owed(&world).await,
        [(1, "first".to_string()), (2, "restart".to_string())]
    );
    let started = world.engine.started.lock().unwrap().clone();
    assert_eq!(started.len(), 2, "started again, once");
    assert_eq!(started[1].resume.as_deref(), Some("n1"));
    // A fresh fold of the same stream asks for nothing more.
    world.daemon = restarted(&world, LockProbe::Unproven);
    for _ in 0..3 {
        world.tick().await;
    }
    assert_eq!(world.engine.started.lock().unwrap().len(), 2, "not again");
}

// The projector is the daemon's: it outlives the engine's exit, and a second
// restart of the new incarnation is a second restart.
#[tokio::test]
async fn a_restarted_child_can_be_restarted_again() {
    let files = Files::default();
    let mut world = idle_bound_child(&files).await;
    world.handle(CHILD, restart_request()).await;
    settle(&mut world).await;
    world.say(exited());
    settle(&mut world).await;
    assert_eq!(world.engine.started.lock().unwrap().len(), 2);
    world.say(ChildObservation::Started {
        native_id: "n1".to_string(),
    });
    world.tick().await;
    world.handle(CHILD, restart_request()).await;
    settle(&mut world).await;
    world.say(exited());
    settle(&mut world).await;
    assert_eq!(world.engine.started.lock().unwrap().len(), 3);
    assert_eq!(
        ended(&world, CHILD).await,
        [(1, "restarted".to_string()), (2, "restarted".to_string())]
    );
    assert_eq!(
        owed(&world).await,
        [
            (1, "first".to_string()),
            (2, "restart".to_string()),
            (3, "restart".to_string())
        ]
    );
}

// A daemon handing over restarts nothing: it ends the idle child to hand it
// over (`drained`), the restart still owed in its stream for the daemon that
// re-hosts it, which resumes it for the cause `restart`.
#[tokio::test]
async fn a_draining_daemon_hands_a_child_owed_a_restart_over_and_restarts_none() {
    let files = Files::default();
    let mut world = idle_bound_child(&files).await;
    world.handle(CHILD, restart_request()).await;
    world.daemon.hand_over();
    settle(&mut world).await;
    assert_eq!(
        world.engine.asked.lock().unwrap().ends,
        1,
        "ended once, to hand over"
    );
    world.say(exited());
    settle(&mut world).await;
    assert_eq!(ended(&world, CHILD).await, [(1, "drained".to_string())]);
    assert_eq!(
        world.engine.started.lock().unwrap().len(),
        1,
        "not started again"
    );
    world.daemon = restarted(&world, LockProbe::Free);
    world
        .daemon
        .start_up(&world.edge.store, world.edge.catalog())
        .await
        .unwrap();
    settle(&mut world).await;
    assert_eq!(
        owed(&world).await,
        [(1, "first".to_string()), (2, "restart".to_string())]
    );
}

const PRIMARY: &str = "5f0c7a52-4444-5444-8444-000000000004";

// Only an engine this daemon hosts is ended: a primary (its own terminal
// session, run by its `ctxloom run` process) owed a restart is not, and no
// child hosted here is ended in its stead.
#[tokio::test]
async fn a_restart_owed_to_an_engine_this_daemon_does_not_host_ends_no_other() {
    let files = Files::default();
    let mut world = idle_bound_child(&files).await;
    world.mint(PRIMARY, "prim", None).await;
    world
        .edge
        .handle(sessions::bind_book(
            session_cover(PRIMARY),
            &sessions::BindEngineSession {
                native_id: "p1".to_string(),
                transcript_path: String::new(),
                engine_version: None,
            },
        ))
        .await
        .unwrap();
    runs_application::Enqueuer::new(Arc::clone(&world.edge))
        .enqueue(runs_application::EnqueueRequest {
            session: PRIMARY.to_string(),
            incarnation: 1,
            mode: runs::RunMode::Interactive,
            evidence: runs_application::Evidence::passing(ENGINE),
            launcher_lock: "/h/.ctxloom/runs/p.lock".to_string(),
            grant: None,
        })
        .await
        .unwrap();
    runs_application::Facts::new(Arc::clone(&world.edge))
        .record(
            &runs_application::Identity {
                run: runs::run_root(PRIMARY, 1),
                session: angzarr_embedded::v1::Uuid::from_bytes(
                    *uuid::Uuid::parse_str(PRIMARY).unwrap().as_bytes(),
                ),
                incarnation: 1,
                harp: "prim".to_string(),
                depth: 0,
            },
            runs_application::RunnerFact::RunnerStarted(runs::RecordRunnerStarted {
                runtime: "host".to_string(),
                workspace: "/work".to_string(),
                pid: 7,
            }),
        )
        .await
        .unwrap();
    world.handle(PRIMARY, restart_request()).await;
    assert_eq!(
        restarts_owed(&world, PRIMARY).await,
        1,
        "the session owes it"
    );
    settle(&mut world).await;
    assert_eq!(
        world.engine.asked.lock().unwrap().ends,
        0,
        "nothing ended here"
    );
    assert_eq!(world.daemon.hosting(), 1);
}
