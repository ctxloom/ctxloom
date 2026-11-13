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
            rel: rel.to_string(),
            bytes: b"seeded".to_vec(),
        };
        let mut files = vec![
            HomeFile {
                rel: "settings.json".to_string(),
                bytes: request.context.as_bytes().to_vec(),
            },
            HomeFile {
                rel: "state.json".to_string(),
                bytes: state,
            },
        ];
        if let Some(permissions) = &request.permissions {
            files.push(HomeFile {
                rel: "permissions.json".to_string(),
                bytes: permissions.as_bytes().to_vec(),
            });
        }
        let denied = &request.surfaces.0.deny_tools;
        if !denied.is_empty() {
            files.push(HomeFile {
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
                surfaces: Default::default(),
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

/// The launcher's hand-off: one, taken once.
struct Handed(Mutex<Option<crate::HandOff>>);

impl Handed {
    /// A hand-off of the child's environment `env`, its launcher saying
    /// nothing of its user's settings.
    fn of(env: BTreeMap<String, String>) -> Handed {
        Handed::with(env, BTreeMap::new())
    }

    fn with(env: BTreeMap<String, String>, launcher: BTreeMap<String, String>) -> Handed {
        Handed(Mutex::new(Some(crate::HandOff { env, launcher })))
    }
}

impl HandOffs for Handed {
    fn take(&self, _: &str) -> Option<crate::HandOff> {
        self.0.lock().unwrap().take()
    }
}

/// A file of a project's session directories: its project, harp and path.
type HomeKey = (String, String, String);

/// Every project's session directories, in memory, shared by every clone.
#[derive(Clone, Default)]
struct Homes(Arc<Mutex<BTreeMap<HomeKey, Vec<u8>>>>);

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
        World::probing(plans, LockProbe::Unproven)
    }

    /// A world whose every lock probes as `locks`.
    fn probing(plans: Box<dyn ChildPlans>, locks: LockProbe) -> World {
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
                for_launch: None,
            }],
            plans,
            hand_offs: Box::new(Handed::of(BTreeMap::from([
                ("PATH".to_string(), "/usr/bin".to_string()),
                ("SECRET".to_string(), "v".to_string()),
            ]))),
            homes: Box::new(homes.clone()),
            ears: Box::new(Listening::default()),
            run_locks: Arc::new(Locks(locks)),
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
                workspace: None,
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
        tokens: Arc::new(Counting::default()),
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

/// A plan of the child whose agent wrote a permissions document.
struct Permitted;

/// The document `Permitted`'s agent wrote, as it wrote it.
const DOCUMENT: &str = r#"{"defaultMode":"plan","allow":["Bash(ls)"]}"#;

impl ChildPlans for Permitted {
    fn preview(
        &self,
        engine: &str,
        project_dir: &str,
        env: &BTreeMap<String, String>,
    ) -> Result<Previewed, String> {
        let mut previewed = Plans.preview(engine, project_dir, env)?;
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

/// A plan of the child whose run agent denies a tool.
struct Surfaced;

impl ChildPlans for Surfaced {
    fn preview(
        &self,
        engine: &str,
        project_dir: &str,
        env: &BTreeMap<String, String>,
    ) -> Result<Previewed, String> {
        let mut previewed = Plans.preview(engine, project_dir, env)?;
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
        Arc::new(move |launcher: &BTreeMap<String, String>| {
            asked.lock().unwrap().push(launcher.clone());
            let engine: Arc<dyn Engine> = composed.clone();
            let child: Arc<dyn ChildEngine> = composed.clone();
            (engine, child)
        })
    };
    let launcher = BTreeMap::from([(
        "CLAUDE_CONFIG_DIR".to_string(),
        "/home/alice/.claude-work".to_string(),
    )]);
    world.daemon = Daemon::new(Ports {
        edge: Arc::clone(&world.edge),
        tokens: Arc::new(Counting::default()),
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
            endpoint: "https://127.0.0.1:1".to_string(),
            cert: PublicCert::from_der(vec![0x30, 0x00]).unwrap(),
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
        fn preview(
            &self,
            _: &str,
            _: &str,
            _: &BTreeMap<String, String>,
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
