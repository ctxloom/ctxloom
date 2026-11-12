//! The doubles and the world the launcher's and the recorder's tests share (and
//! the use cases above them): an
//! edge over the kernel and an in-memory store (so a fact is applied by the
//! real run and session aggregates), a scripted way to run a command, a
//! scripted engine, and a session directory held in memory.

use std::collections::BTreeMap;
use std::sync::{Arc, Mutex};

use angzarr_embedded::testing::{MemoryStore, Samples};
use angzarr_embedded::v1::{
    command_page, Command, CommandBook, CommandPage, Cover, EventBook, EventPage, Uuid,
};
use angzarr_embedded::{
    Answered, Failure, Handled, Kernel, OnRetryable, Payload, Registry, Rejected, Sampled,
    StoreError, StreamRef,
};
use run_ports::{
    check_rel, Edge, Engine, EngineError, Exit, HomeFile, Launch, LaunchFailure, Limits,
    PrepareRequest, Prepared, RecordsError, RunnerLauncher, Running, SessionRecords, TurnCommand,
    TurnOutcome, TurnOutput, TurnRequest, Unserved,
};
use runs::{RunEvent, RunMode};
use sessions::{Origin, SessionEvent};

use crate::{Order, Orders};

pub const SESSION: &str = "5f0c7a52-1111-5111-8111-000000000001";
pub const HARP: &str = "amber-swift-owl";

/// An edge over the kernel and an in-memory store: the real aggregates.
pub struct TestEdge {
    registry: Registry,
    pub store: MemoryStore,
    samples: Mutex<Samples>,
}

impl Default for TestEdge {
    fn default() -> TestEdge {
        TestEdge::new()
    }
}

impl TestEdge {
    pub fn new() -> TestEdge {
        let mut registry = Registry::new();
        registry
            .aggregate::<sessions::SessionAggregate>(sessions::SessionAggregate::default)
            .unwrap();
        registry
            .aggregate::<sessions::NameClaimAggregate>(sessions::NameClaimAggregate::default)
            .unwrap();
        registry
            .aggregate::<runs::RunAggregate>(runs::RunAggregate::default)
            .unwrap();
        TestEdge {
            registry,
            store: MemoryStore::default(),
            samples: Mutex::new(Samples::starting_at(1_000)),
        }
    }

    // A test double shipped for tests only; its tests are the callers', not its own.
    #[cfg_attr(test, mutants::skip)]
    pub fn catalog(&self) -> &angzarr_embedded::Catalog {
        self.registry.catalog()
    }
}

impl Edge for TestEdge {
    async fn serve(&self, book: CommandBook, _: run_ports::Arrival) -> Result<Answered, Unserved> {
        self.handle(book).await.map_err(Unserved::Failed)
    }

    // A test double shipped for tests only; its tests are the callers', not its own.
    #[cfg_attr(test, mutants::skip)]
    async fn handle(&self, book: CommandBook) -> Result<Answered, Failure<Rejected>> {
        let sampled: Sampled = self.samples.lock().unwrap().next_sample();
        let kernel = Kernel::new(&self.registry).on_retryable(OnRetryable {
            max_retries: 0,
            ..OnRetryable::default()
        });
        let cover = book.cover.clone();
        let handled: Handled = kernel.handle_any(&self.store, book, &sampled).await?;
        let events = handled
            .books()
            .into_iter()
            .find(|book| book.cover == cover)
            .unwrap_or(EventBook {
                cover,
                ..EventBook::default()
            });
        Ok(Answered {
            events: Some(events),
            delivery: handled.delivery(),
        })
    }

    async fn events(&self, cover: &Cover, from: u64) -> Result<Vec<EventPage>, StoreError> {
        let stream = StreamRef::try_from(cover)?;
        let slice =
            angzarr_embedded::angzarr_ports::EventStore::read(&self.store, stream, from, None)
                .await?;
        Ok(slice.pages)
    }
}

pub fn uuid(text: &str) -> Uuid {
    Uuid::from_bytes(*uuid::Uuid::parse_str(text).unwrap().as_bytes())
}

pub fn session_cover(session: &str) -> Cover {
    Cover {
        domain: sessions::SESSION_DOMAIN.to_string(),
        root: Some(uuid(session)),
        edition: None,
    }
}

pub fn run_cover(session: &str) -> Cover {
    Cover {
        domain: runs::RUN_DOMAIN.to_string(),
        root: Some(runs::run_root(session, 1)),
        edition: None,
    }
}

fn command_book(cover: Cover, payload: angzarr_embedded::Any) -> CommandBook {
    CommandBook {
        cover: Some(cover),
        page: Some(CommandPage {
            body: Some(command_page::Body::Command(Command {
                payload: Some(payload),
                handling: None,
            })),
            ..CommandPage::default()
        }),
    }
}

/// The session's end as its engine's hook reports it (claude's SessionEnd),
/// naming the engine's own session `native`: a fact on the session stream.
// A test double shipped for tests only; its tests are the callers', not its own.
#[cfg_attr(test, mutants::skip)]
pub async fn hook_session_ended(edge: &TestEdge, session: &str, native: &str) {
    use angzarr_embedded::markup::DomainMessage;
    let fact = sessions::RecordSessionEnded {
        native_session: native.to_string(),
        reason: "prompt_input_exit".to_string(),
    };
    let book = CommandBook {
        cover: Some(session_cover(session)),
        page: Some(CommandPage {
            body: Some(command_page::Body::Fact(angzarr_embedded::v1::Fact {
                payload: Some(angzarr_embedded::Any::from_msg(&fact.to_wire()).unwrap()),
                origin: Some(angzarr_embedded::v1::FactOrigin {
                    external_id: format!("hook:end:{session}:{native}"),
                    description: String::new(),
                }),
            })),
            ..CommandPage::default()
        }),
    };
    edge.handle(book).await.unwrap();
}

/// Mint `session` as `harp` and enqueue its first run, as a one-shot is.
pub async fn enqueued(edge: &TestEdge, session: &str, harp: &str) {
    enqueued_by(edge, session, harp, "").await;
}

/// [`enqueued`], its launcher holding the run lock at `launcher_lock` (rulings
/// 86, 92; empty: none).
pub async fn enqueued_by(edge: &TestEdge, session: &str, harp: &str, launcher_lock: &str) {
    let mint: sessions::SessionCommand = sessions::MintSession {
        harp: harp.to_string(),
        project_id: "calm-otter".to_string(),
        project_dir: "/work/proj".to_string(),
        output_dir: format!("/out/{harp}"),
        engine: "fake".to_string(),
        origin: Origin::Oneshot,
        started_at: None,
        depth: 0,
        owner: None,
    }
    .into();
    edge.handle(command_book(session_cover(session), mint.to_any()))
        .await
        .unwrap();
    let enqueue: runs::RunCommand = runs::EnqueueRun {
        session: session.to_string(),
        incarnation: 1,
        mode: RunMode::OneShot,
        hook_credential_hash: vec![3; 32],
        prompt_given: true,
        engine: "claude-code".to_string(),
        drivable_engines: vec!["claude-code".to_string()],
        credential: runs::CredentialEvidence {
            file_only: false,
            builtin_variables: Vec::new(),
            accepted_variables: Vec::new(),
            accepted_set: false,
        },
        fatal_findings: 0,
        launcher_lock: launcher_lock.to_string(),
    }
    .into();
    edge.handle(command_book(run_cover(session), enqueue.to_any()))
        .await
        .unwrap();
}

/// The run stream's events, decoded.
pub async fn run_events(edge: &TestEdge, session: &str) -> Vec<RunEvent> {
    events_of(edge.events(&run_cover(session), 0).await.unwrap())
        .iter()
        .map(|any| RunEvent::from_any(any).unwrap())
        .collect()
}

/// The run stream's pages, as a store holds them.
// A test double shipped for tests only; its tests are the callers', not its own.
#[cfg_attr(test, mutants::skip)]
pub async fn run_pages(edge: &TestEdge, session: &str) -> Vec<EventPage> {
    edge.events(&run_cover(session), 0).await.unwrap()
}

/// The session stream's events, decoded.
pub async fn session_events(edge: &TestEdge, session: &str) -> Vec<SessionEvent> {
    events_of(edge.events(&session_cover(session), 0).await.unwrap())
        .iter()
        .map(|any| SessionEvent::from_any(any).unwrap())
        .collect()
}

fn events_of(pages: Vec<EventPage>) -> Vec<angzarr_embedded::Any> {
    pages
        .into_iter()
        .filter_map(|page| match page.body {
            Some(angzarr_embedded::v1::event_page::Body::Event(any)) => Some(any),
            _ => None,
        })
        .collect()
}

/// The names of the run's events, in order.
pub fn names(events: &[RunEvent]) -> Vec<&'static str> {
    events
        .iter()
        .map(|event| match event {
            RunEvent::RunEnqueued(_) => "RunEnqueued",
            RunEvent::RunnerAttachedRecorded(_) => "RunnerAttached",
            RunEvent::RunnerStartedRecorded(_) => "RunnerStarted",
            RunEvent::TurnStartedRecorded(_) => "TurnStarted",
            RunEvent::TurnCompletedRecorded(_) => "TurnCompleted",
            RunEvent::TurnFailedRecorded(_) => "TurnFailed",
            RunEvent::RunEnded(_) => "RunEnded",
            _ => "other",
        })
        .collect()
}

/// What a spawned command does while it runs.
pub type Window = Arc<dyn Fn() + Send + Sync>;

/// A command run that answers as scripted, and remembers what it was given.
#[derive(Clone)]
pub struct ScriptedRunner {
    pub answer: Launch,
    pub seen: Arc<Mutex<Vec<TurnCommand>>>,
    /// The commands given a terminal.
    pub seen_terminal: Arc<Mutex<Vec<TurnCommand>>>,
    /// What the command's process does while it runs, silent: a test's
    /// window onto the store at the moment of a spawn.
    pub during: Arc<Mutex<Option<Window>>>,
    /// A window onto the store INSIDE the spawn, before the pid is known to
    /// the caller.
    pub at_spawn: Arc<Mutex<Option<Window>>>,
    /// The pid of the spawned command.
    pub pid: u32,
}

/// The pid a scripted command has.
pub const SCRIPTED_PID: u32 = 4242;

/// A scripted command, spawned and running.
struct ScriptedRunning {
    pid: u32,
    answer: Launch,
    during: Option<Window>,
}

impl Running for ScriptedRunning {
    fn pid(&self) -> u32 {
        self.pid
    }

    fn finish(self: Box<Self>) -> Result<TurnOutput, LaunchFailure> {
        if let Some(during) = &self.during {
            during();
        }
        match self.answer {
            Launch::Over(output) => Ok(output),
            Launch::NotStarted(failure) => Err(failure),
        }
    }
}

impl ScriptedRunner {
    pub fn exits(code: i32) -> ScriptedRunner {
        ScriptedRunner::answering(Launch::Over(TurnOutput {
            exit: Exit {
                code: Some(code),
                signal: None,
            },
            stdout: Vec::new(),
            stderr: Vec::new(),
            timed_out: false,
            truncated: false,
            drain_incomplete: false,
        }))
    }

    pub fn answering(answer: Launch) -> ScriptedRunner {
        ScriptedRunner {
            answer,
            seen: Arc::default(),
            seen_terminal: Arc::default(),
            during: Arc::default(),
            at_spawn: Arc::default(),
            pid: SCRIPTED_PID,
        }
    }

    /// The spawn: a scripted refusal is a command that never started.
    fn spawn(&self) -> Result<Box<dyn Running>, LaunchFailure> {
        let at_spawn = self.at_spawn.lock().unwrap().clone();
        if let Some(at_spawn) = at_spawn {
            at_spawn();
        }
        if let Launch::NotStarted(failure) = &self.answer {
            return Err(failure.clone());
        }
        Ok(Box::new(ScriptedRunning {
            pid: self.pid,
            answer: self.answer.clone(),
            during: self.during.lock().unwrap().clone(),
        }))
    }
}

impl RunnerLauncher for ScriptedRunner {
    fn launch(&self, command: &TurnCommand, _: &Limits) -> Result<Box<dyn Running>, LaunchFailure> {
        self.seen.lock().unwrap().push(command.clone());
        self.spawn()
    }

    fn launch_terminal(&self, command: &TurnCommand) -> Result<Box<dyn Running>, LaunchFailure> {
        self.seen_terminal.lock().unwrap().push(command.clone());
        self.spawn()
    }
}

/// An engine that puts the context in its home, drives a command that names
/// it, and reads any output as it was scripted to.
#[derive(Clone)]
pub struct ScriptedEngine {
    pub outcome: TurnOutcome,
    pub refuses_to_drive: bool,
}

impl ScriptedEngine {
    pub fn answering(answer: &str, native_id: Option<&str>) -> ScriptedEngine {
        ScriptedEngine {
            outcome: TurnOutcome::Answered {
                answer: answer.to_string(),
                native_id: native_id.map(String::from),
            },
            refuses_to_drive: false,
        }
    }
}

impl Engine for ScriptedEngine {
    fn name(&self) -> &str {
        "fake"
    }

    fn prepare(&self, request: &PrepareRequest) -> Result<Prepared, EngineError> {
        let mut files = vec![HomeFile {
            rel: "ctx.md".to_string(),
            bytes: request.context.clone().into_bytes(),
        }];
        // The permissions document, where this engine reads it: as given.
        if let Some(document) = &request.permissions {
            files.push(HomeFile {
                rel: "permissions.json".to_string(),
                bytes: document.clone().into_bytes(),
            });
        }
        Ok(Prepared {
            files,
            copy_in: Vec::new(),
            seeds: Vec::new(),
            context_path: format!("{}/ctx.md", request.home),
        })
    }

    fn drive_turn(&self, request: &TurnRequest) -> Result<TurnCommand, EngineError> {
        if self.refuses_to_drive {
            return Err(EngineError::NotYet {
                engine: "fake".to_string(),
                operation: "drive_turn".to_string(),
            });
        }
        let mut command = TurnCommand::new(if request.settings.command.is_empty() {
            "fake"
        } else {
            request.settings.command.as_str()
        });
        command.args = vec![request.context_path.clone()];
        if let Some(native) = &request.resume {
            command.args.push(format!("--resume={native}"));
        }
        if !request.model.is_empty() {
            command.args.push(format!("--model={}", request.model));
        }
        if !request.effort.is_empty() {
            command.args.push(format!("--effort={}", request.effort));
        }
        command.env = request.env.clone();
        command.stdin = request.prompt.clone().into_bytes();
        command.cwd = request.cwd.clone();
        Ok(command)
    }

    fn finish_turn(&self, _: &TurnOutput) -> TurnOutcome {
        self.outcome.clone()
    }

    fn drive_interactive(&self, request: &TurnRequest) -> Result<TurnCommand, EngineError> {
        let mut command = TurnCommand::new(if request.settings.command.is_empty() {
            "fake-terminal"
        } else {
            request.settings.command.as_str()
        });
        command.args = vec![request.context_path.clone(), request.prompt.clone()];
        if let Some(native) = &request.resume {
            command.args.push(format!("--resume={native}"));
        }
        if !request.model.is_empty() {
            command.args.push(format!("--model={}", request.model));
        }
        if !request.effort.is_empty() {
            command.args.push(format!("--effort={}", request.effort));
        }
        command.env = request.env.clone();
        command.cwd = request.cwd.clone();
        Ok(command)
    }
}

/// An engine that relocates a config dir of its own (`FAKE_CONFIG_DIR`, a
/// name it computes), and is otherwise `0`.
pub struct Relocating(pub ScriptedEngine);

impl Relocating {
    fn relocated(command: Result<TurnCommand, EngineError>) -> Result<TurnCommand, EngineError> {
        command.map(|mut command| {
            command
                .env
                .insert("FAKE_CONFIG_DIR".to_string(), "/session/config".to_string());
            command
        })
    }
}

// A test double shipped for tests only; its tests are the callers' own.
#[cfg_attr(test, mutants::skip)]
impl Engine for Relocating {
    fn name(&self) -> &str {
        self.0.name()
    }
    fn prepare(&self, request: &PrepareRequest) -> Result<Prepared, EngineError> {
        self.0.prepare(request)
    }
    fn drive_turn(&self, request: &TurnRequest) -> Result<TurnCommand, EngineError> {
        Relocating::relocated(self.0.drive_turn(request))
    }
    fn finish_turn(&self, output: &TurnOutput) -> TurnOutcome {
        self.0.finish_turn(output)
    }
    fn drive_interactive(&self, request: &TurnRequest) -> Result<TurnCommand, EngineError> {
        Relocating::relocated(self.0.drive_interactive(request))
    }
}

/// An engine that asks for credentials as `1` says, and is otherwise `0`.
pub struct Credentialed(pub ScriptedEngine, pub run_ports::Credentials);

// A test double shipped for tests only; its tests are the callers' own.
#[cfg_attr(test, mutants::skip)]
impl Engine for Credentialed {
    fn name(&self) -> &str {
        self.0.name()
    }
    fn prepare(&self, request: &PrepareRequest) -> Result<Prepared, EngineError> {
        self.0.prepare(request)
    }
    fn drive_turn(&self, request: &TurnRequest) -> Result<TurnCommand, EngineError> {
        self.0.drive_turn(request)
    }
    fn finish_turn(&self, output: &TurnOutput) -> TurnOutcome {
        self.0.finish_turn(output)
    }
    fn credentials(&self) -> run_ports::Credentials {
        self.1
    }
    fn drive_interactive(&self, request: &TurnRequest) -> Result<TurnCommand, EngineError> {
        self.0.drive_interactive(request)
    }
}

/// An engine whose terminal session ends with its exit (as opencode's does),
/// and is otherwise `0`.
pub struct EndsByExit(pub ScriptedEngine);

// A test double shipped for tests only; its tests are the callers' own.
#[cfg_attr(test, mutants::skip)]
impl Engine for EndsByExit {
    fn name(&self) -> &str {
        self.0.name()
    }
    fn prepare(&self, request: &PrepareRequest) -> Result<Prepared, EngineError> {
        self.0.prepare(request)
    }
    fn drive_turn(&self, request: &TurnRequest) -> Result<TurnCommand, EngineError> {
        self.0.drive_turn(request)
    }
    fn finish_turn(&self, output: &TurnOutput) -> TurnOutcome {
        self.0.finish_turn(output)
    }
    fn drive_interactive(&self, request: &TurnRequest) -> Result<TurnCommand, EngineError> {
        self.0.drive_interactive(request)
    }
    fn session_ends_with_exit(&self) -> bool {
        true
    }
}

/// An engine that merges into its home's `state.json` (as claude does into
/// `.claude.json`): it writes what it was handed of it, then `+` and the
/// directory it runs in, and is otherwise `0`.
pub struct Merging(pub ScriptedEngine);

// A test double shipped for tests only; its tests are the callers' own.
#[cfg_attr(test, mutants::skip)]
impl Engine for Merging {
    fn name(&self) -> &str {
        self.0.name()
    }
    fn prepare(&self, request: &PrepareRequest) -> Result<Prepared, EngineError> {
        let mut prepared = self.0.prepare(request)?;
        let mut bytes = request
            .existing
            .get("state.json")
            .cloned()
            .unwrap_or_default();
        bytes.extend_from_slice(format!("+{}", request.cwd.clone().unwrap_or_default()).as_bytes());
        prepared.files.push(HomeFile {
            rel: "state.json".to_string(),
            bytes,
        });
        Ok(prepared)
    }
    fn drive_turn(&self, request: &TurnRequest) -> Result<TurnCommand, EngineError> {
        self.0.drive_turn(request)
    }
    fn finish_turn(&self, output: &TurnOutput) -> TurnOutcome {
        self.0.finish_turn(output)
    }
    fn drive_interactive(&self, request: &TurnRequest) -> Result<TurnCommand, EngineError> {
        self.0.drive_interactive(request)
    }
    fn merges(&self) -> &'static [&'static str] {
        &["state.json"]
    }
}

/// An engine that seeds its home with `seed.json` (`SEED`), once.
#[derive(Clone)]
pub struct Seeding(pub ScriptedEngine);

/// What [`Seeding`] seeds its home with.
pub const SEED: &[u8] = b"{\"recent\":[]}";

// A test double shipped for tests only; its tests are the callers' own.
#[cfg_attr(test, mutants::skip)]
impl Engine for Seeding {
    fn name(&self) -> &str {
        self.0.name()
    }
    fn prepare(&self, request: &PrepareRequest) -> Result<Prepared, EngineError> {
        let mut prepared = self.0.prepare(request)?;
        prepared.seeds.push(HomeFile {
            rel: "seed.json".to_string(),
            bytes: SEED.to_vec(),
        });
        Ok(prepared)
    }
    fn drive_turn(&self, request: &TurnRequest) -> Result<TurnCommand, EngineError> {
        self.0.drive_turn(request)
    }
    fn finish_turn(&self, output: &TurnOutput) -> TurnOutcome {
        self.0.finish_turn(output)
    }
    fn drive_interactive(&self, request: &TurnRequest) -> Result<TurnCommand, EngineError> {
        self.0.drive_interactive(request)
    }
}

/// A file of a session directory, by its harp and relative path.
type Files = BTreeMap<(String, String), Vec<u8>>;

/// A session directory held in memory, shared by every clone.
#[derive(Clone, Default)]
pub struct MemoryRecords {
    files: Arc<Mutex<Files>>,
    /// A path prefix whose writes the system refuses.
    pub refuses: Arc<Mutex<Option<String>>>,
}

impl MemoryRecords {
    pub fn text(&self, harp: &str, rel: &str) -> Option<String> {
        self.files
            .lock()
            .unwrap()
            .get(&(harp.to_string(), rel.to_string()))
            .map(|bytes| String::from_utf8(bytes.clone()).unwrap())
    }

    // A test double shipped for tests only; its tests are the callers', not its own.
    #[cfg_attr(test, mutants::skip)]
    pub fn names(&self, harp: &str) -> Vec<String> {
        self.files
            .lock()
            .unwrap()
            .keys()
            .filter(|(h, _)| h == harp)
            .map(|(_, rel)| rel.clone())
            .collect()
    }

    // A test double shipped for tests only; its tests are the callers', not its own.
    #[cfg_attr(test, mutants::skip)]
    fn denied(&self, harp: &str, rel: &str) -> Result<(), RecordsError> {
        match self.refuses.lock().unwrap().as_deref() {
            Some(prefix) if rel.starts_with(prefix) => Err(RecordsError::Io {
                harp: harp.to_string(),
                rel: rel.to_string(),
                ext_err_txt: "permission denied".to_string(),
            }),
            _ => Ok(()),
        }
    }
}

impl SessionRecords for MemoryRecords {
    fn dir(&self, harp: &str) -> String {
        format!("/work/proj/.ctxloom/sessions/{harp}")
    }

    // A test double shipped for tests only; its tests are the callers'
    // (run-application's mint tests), not its own.
    #[cfg_attr(test, mutants::skip)]
    fn exists(&mut self, harp: &str) -> bool {
        !self.names(harp).is_empty()
    }

    fn write(&mut self, harp: &str, rel: &str, bytes: &[u8]) -> Result<(), RecordsError> {
        check_rel(rel)?;
        self.denied(harp, rel)?;
        self.files
            .lock()
            .unwrap()
            .insert((harp.to_string(), rel.to_string()), bytes.to_vec());
        Ok(())
    }

    fn append(&mut self, harp: &str, rel: &str, bytes: &[u8]) -> Result<(), RecordsError> {
        check_rel(rel)?;
        self.denied(harp, rel)?;
        self.files
            .lock()
            .unwrap()
            .entry((harp.to_string(), rel.to_string()))
            .or_default()
            .extend_from_slice(bytes);
        Ok(())
    }

    fn read(&mut self, harp: &str, rel: &str) -> Result<Option<Vec<u8>>, RecordsError> {
        check_rel(rel)?;
        Ok(self
            .files
            .lock()
            .unwrap()
            .get(&(harp.to_string(), rel.to_string()))
            .cloned())
    }
}

/// The order a one-shot places.
pub fn order(engine: &str) -> Order {
    Order {
        harp: HARP.to_string(),
        engine: engine.to_string(),
        context: "THE-CONTEXT".to_string(),
        prompt: "THE-PROMPT".to_string(),
        env: BTreeMap::from([("PATH".to_string(), "/bin".to_string())]),
        user_home: None,
        cwd: Some("/work/proj".to_string()),
        interactive: false,
        model: String::new(),
        effort: String::new(),
        permissions: None,
        settings: Default::default(),
        owned: Vec::new(),
        git_user: None,
        resume: None,
    }
}

pub fn orders_with(order: Order) -> Arc<Orders> {
    let orders = Arc::new(Orders::new());
    orders.place(SESSION, 1, order);
    orders
}
