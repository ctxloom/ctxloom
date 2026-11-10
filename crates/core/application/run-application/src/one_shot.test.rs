//! The one-shot over the real aggregates in an in-memory store, with the
//! plan, the project, the names and the engine scripted.

use std::collections::VecDeque;
use std::sync::atomic::{AtomicBool, AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};

use angzarr_diagnostic::{Diagnostic, Report};
use angzarr_embedded::testing::MemoryStore;
use angzarr_embedded::v1::{event_page, CommandBook, Cover, EventPage, Uuid};
use angzarr_embedded::{Answered, Catalog, CommandBookExt, Failure, Rejected, StoreError};
use library_application::{
    AppError as PlanError, DryRun, FindingRow, Flags, Notice, Outcome, Previewed, RunRequest,
};
use ports::{CommandIds, Names, Project, ProjectIdentity};
use run_launcher::fakes::{
    names as event_names, run_events, session_events, Credentialed, MemoryRecords, ScriptedEngine,
    ScriptedRunner, TestEdge,
};
use run_launcher::{Launcher, Orders, Recorder};
use run_ports::{
    Arrival, BoxFuture, CertMint, CertNames, Credentials, Edge, FailureClass, HookCertificate,
    HookServer, Limits, LinkState, LoginLinks, PublicCert, RecordsError, Serving, Token,
    TokenSource, TurnOutcome, Unserved,
};
use runs_application::{AppError as CoreError, Enqueuer};

use crate::{Core, OneShot, Parts, Place, Planner, RunError, Runs};

/// The edge the use cases see, over the test edge, with two ways to be
/// unfaithful that a test turns on: a run stream shown without its end, and a
/// claim of a name answered as the release of a name nobody holds (a refusal
/// that is not the name being taken).
#[derive(Default)]
struct Seen {
    edge: Arc<TestEdge>,
    hide_end: AtomicBool,
    claims_refused: AtomicBool,
    /// The claims of a name that were made.
    claims: AtomicUsize,
}

impl Edge for Seen {
    async fn serve(&self, book: CommandBook, arrival: Arrival) -> Result<Answered, Unserved> {
        self.edge.serve(book, arrival).await
    }

    async fn handle(&self, book: CommandBook) -> Result<Answered, Failure<Rejected>> {
        let names = book
            .cover
            .as_ref()
            .is_some_and(|c| c.domain == sessions::NAMES_DOMAIN);
        if names {
            self.claims.fetch_add(1, Ordering::SeqCst);
        }
        if names && self.claims_refused.load(Ordering::SeqCst) {
            let release = sessions::NameClaimCommand::from(sessions::ReleaseName {
                session: "none".to_string(),
            });
            return self
                .edge
                .handle(CommandBook::command_of(
                    book.cover.unwrap_or_default(),
                    &release,
                ))
                .await;
        }
        self.edge.handle(book).await
    }

    async fn events(&self, cover: &Cover, from: u64) -> Result<Vec<EventPage>, StoreError> {
        let mut pages = self.edge.events(cover, from).await?;
        if self.hide_end.load(Ordering::SeqCst) {
            pages.retain(|page| {
                !matches!(&page.body, Some(event_page::Body::Event(any))
                    if any.type_url.ends_with(".RunEnded"))
            });
        }
        Ok(pages)
    }
}

struct TestCore(Arc<Seen>);

impl Core for TestCore {
    type Edge = Seen;
    type Store = MemoryStore;

    fn edge(&self) -> Arc<Seen> {
        Arc::clone(&self.0)
    }
    fn store(&self) -> &MemoryStore {
        &self.0.edge.store
    }
    fn catalog(&self) -> &Catalog {
        self.0.edge.catalog()
    }
}

/// What the library would plan, scripted.
struct ScriptedPlan {
    prompt: String,
    backend: String,
    fatal: Vec<FindingRow>,
    model: String,
    effort: String,
    drive: library_application::Drive,
    error: Option<fn() -> PlanError>,
    asked: Arc<Mutex<Vec<RunRequest>>>,
}

impl Planner for ScriptedPlan {
    fn plan(&self, _: &Flags, request: &RunRequest) -> Outcome<Previewed> {
        self.asked.lock().unwrap().push(request.clone());
        let notices = vec![Notice::text(
            "a finding the plan proceeded past".to_string(),
        )];
        if let Some(error) = self.error {
            return Outcome {
                result: Err(error()),
                notices,
            };
        }
        let plan = DryRun {
            agent: String::new(),
            workspace: String::new(),
            runtime: String::new(),
            engine: self.backend.clone(),
            profiles: Vec::new(),
            fragments: Vec::new(),
            context: "THE-CONTEXT".to_string(),
            tokens: 0,
            prompt: self.prompt.clone(),
            findings: Vec::new(),
            axes: (String::new(), String::new()),
            model: self.model.clone(),
            effort: self.effort.clone(),
            drive: self.drive.clone(),
            settings: None,
        };
        Outcome {
            result: Ok(Previewed {
                plan,
                fatal: self.fatal.clone(),
            }),
            notices,
        }
    }
}

struct FixedProject(Option<Report>);

impl ProjectIdentity for FixedProject {
    fn for_write(&mut self, _: Option<&str>) -> Result<Project, Report> {
        match self.0.take() {
            Some(report) => Err(report),
            None => Ok(Project {
                id: "calm-otter".to_string(),
                root: Some("/work/proj".to_string()),
                store: "/h/.ctxloom/projects/calm-otter/ctxloom.db".into(),
            }),
        }
    }
    fn for_read(&mut self, _: Option<&str>) -> Result<Option<Project>, Report> {
        Ok(None)
    }
    fn every_project(&mut self) -> Result<Vec<Project>, Report> {
        Ok(Vec::new())
    }
    fn rootless_directory(&mut self) -> Result<Option<String>, Report> {
        Ok(None)
    }
}

struct ScriptedNames(VecDeque<String>);

impl Names for ScriptedNames {
    fn task_id(&mut self) -> String {
        unreachable!("a run names a project-shaped session")
    }
    fn task_id_for(&mut self, _: &str, _: u64) -> String {
        unreachable!("a run names a project-shaped session")
    }
    fn project_id(&mut self) -> String {
        self.0.pop_front().expect("a name was scripted")
    }
}

struct FixedIds;

impl CommandIds for FixedIds {
    fn command_id(&mut self) -> Uuid {
        Uuid::from_bytes(*uuid::Uuid::now_v7().as_bytes())
    }
}

#[derive(Default)]
struct CountingSource(Mutex<u8>);

impl TokenSource for CountingSource {
    fn mint(&self) -> Token {
        let mut n = self.0.lock().unwrap();
        *n += 1;
        Token::from_bytes([*n; 32])
    }
}

/// What the hook server saw, in order, each with how many launches (a turn's
/// or the terminal's) the runner had seen by then (the engine's starts), so
/// the order is read off the run and never waited for.
#[derive(Clone, Default)]
struct Hooks {
    log: Arc<Mutex<Vec<(String, usize)>>>,
    /// The runner whose launches are counted.
    runner: Option<ScriptedRunner>,
    /// A reason to refuse to serve, if any.
    refuse: Option<&'static str>,
    /// The session directories: at the stop, whether the run's end had
    /// already been projected into them.
    records: Option<MemoryRecords>,
    end_projected_at_stop: Arc<Mutex<Option<bool>>>,
    /// The private key of the identity each serve was given.
    identities: Arc<Mutex<Vec<String>>>,
}

/// The key the fake mint gives: a secret that must reach the listener and
/// nothing else.
const MINTED_KEY: &str = "MINTED-KEY-PEM-SECRET";

/// The DER the fake mint gives.
fn minted_public() -> PublicCert {
    PublicCert::from_der(vec![0x30, 0x04, 1, 2, 3, 4]).unwrap()
}

/// What mints the run's certificate: it records the names asked for.
#[derive(Clone, Default)]
struct Certs {
    asked: Arc<Mutex<Vec<CertNames>>>,
    refuse: Option<&'static str>,
}

impl CertMint for Certs {
    fn mint(&self, names: &CertNames) -> Result<HookCertificate, Report> {
        self.asked.lock().unwrap().push(names.clone());
        if let Some(reason) = self.refuse {
            return Err(Report::new(reason));
        }
        Ok(HookCertificate::new(
            minted_public(),
            MINTED_KEY.to_string(),
        ))
    }
}

impl Hooks {
    fn note(&self, what: String) {
        let launched = self.runner.as_ref().map_or(0, |r| {
            r.seen.lock().unwrap().len() + r.seen_terminal.lock().unwrap().len()
        });
        self.log.lock().unwrap().push((what, launched));
    }
}

struct Listening(Hooks);

/// The port the fake listener reports.
const FAKE_PORT: u16 = 40123;

impl Serving for Listening {
    fn port(&self) -> u16 {
        FAKE_PORT
    }

    fn stop(self: Box<Self>) -> BoxFuture<'static, ()> {
        Box::pin(async move {
            if let Some(records) = &self.0.records {
                let projected = records
                    .names("amber-swift-owl")
                    .iter()
                    .any(|rel| rel == "records/runs.jsonl")
                    && records
                        .text("amber-swift-owl", "records/runs.jsonl")
                        .is_some_and(|runs| runs.contains("RunEnded"));
                *self.0.end_projected_at_stop.lock().unwrap() = Some(projected);
            }
            self.0.note("stop".to_string())
        })
    }
}

impl HookServer for Hooks {
    fn serve_tcp<'a>(
        &'a self,
        bind: std::net::IpAddr,
        identity: &'a HookCertificate,
    ) -> BoxFuture<'a, Result<Box<dyn Serving>, Report>> {
        Box::pin(async move {
            if let Some(reason) = self.refuse {
                return Err(Report::new(reason));
            }
            self.identities
                .lock()
                .unwrap()
                .push(identity.key_pem().to_string());
            self.note(format!("serve_tcp {bind}"));
            Ok(Box::new(Listening(self.clone())) as Box<dyn Serving>)
        })
    }
}

struct Rig {
    hooks: Hooks,
    certs: Certs,
    runs: Runs<TestCore, CountingSource, ScriptedRunner, MemoryRecords, MemoryRecords>,
    edge: Arc<TestEdge>,
    runner: ScriptedRunner,
    records: MemoryRecords,
    asked: Arc<Mutex<Vec<RunRequest>>>,
    links: Links,
}

/// The session directory's link, in memory, shared by every clone.
#[derive(Clone, Default)]
struct Links {
    login: bool,
    held: Arc<Mutex<Option<LinkState>>>,
    made: Arc<Mutex<Vec<(String, String, String)>>>,
}

impl LoginLinks for Links {
    fn present(&self, path: &str) -> bool {
        // the file exists wherever the configured source (or the default) puts it
        self.login
            && (path == "/home/u/.claude/.credentials.json" || path == "/home/u/.acme/creds.json")
    }
    fn state(&self, _: &str, _: &str) -> LinkState {
        self.held
            .lock()
            .unwrap()
            .clone()
            .unwrap_or(LinkState::Absent)
    }
    fn link(&mut self, harp: &str, rel: &str, target: &str) -> Result<(), RecordsError> {
        self.made
            .lock()
            .unwrap()
            .push((harp.to_string(), rel.to_string(), target.to_string()));
        Ok(())
    }
}

struct Script {
    prompt: &'static str,
    backend: &'static str,
    engine: ScriptedEngine,
    runner: ScriptedRunner,
    fatal: Vec<FindingRow>,
    /// The model and effort the plan resolved.
    model: &'static str,
    effort: &'static str,
    /// How the plan says the engine is run.
    drive: library_application::Drive,
    plan_error: Option<fn() -> PlanError>,
    project_error: Option<Report>,
    names: Vec<&'static str>,
    /// What the engine asks for as a credential; none by default.
    credentials: Credentials,
    /// The caller's environment.
    env: Vec<(&'static str, &'static str)>,
    /// Whether the user's login file is there.
    login: bool,
    os: &'static str,
    /// The directory of the running binary.
    program_dir: &'static str,
    /// A reason the hook server refuses to serve, if it does.
    hooks_refuse: Option<&'static str>,
    certs_refuse: Option<&'static str>,
}

impl Script {
    fn answering() -> Script {
        Script {
            prompt: "THE-PROMPT",
            backend: "fake",
            engine: ScriptedEngine::answering("THE-ANSWER", Some("native-7")),
            runner: ScriptedRunner::exits(0),
            fatal: Vec::new(),
            model: "",
            effort: "",
            drive: Default::default(),
            plan_error: None,
            project_error: None,
            names: vec!["amber-swift-owl", "calm-quiet-fox", "wild-brave-elk"],
            credentials: Credentials::default(),
            env: vec![("PATH", "/bin"), ("HOME", "/home/u")],
            login: true,
            os: "linux",
            program_dir: "/opt/ctxloom/bin",
            hooks_refuse: None,
            certs_refuse: None,
        }
    }
}

fn rig(script: Script) -> Rig {
    let edge = Arc::new(TestEdge::new());
    let records = MemoryRecords::default();
    let orders = Arc::new(Orders::new());
    let asked = Arc::new(Mutex::new(Vec::new()));
    let seen = Arc::new(Seen {
        edge: Arc::clone(&edge),
        ..Seen::default()
    });
    let links = Links {
        login: script.login,
        ..Links::default()
    };
    let launcher = Launcher::new(
        Arc::clone(&seen),
        script.runner.clone(),
        records.clone(),
        vec![Arc::new(Credentialed(script.engine, script.credentials))],
        Arc::clone(&orders),
        Limits::new(std::time::Duration::from_secs(5)),
    );
    let hooks = Hooks {
        runner: Some(script.runner.clone()),
        refuse: script.hooks_refuse,
        records: Some(records.clone()),
        ..Hooks::default()
    };
    let certs = Certs {
        refuse: script.certs_refuse,
        ..Certs::default()
    };
    let runs = Runs::new(Parts {
        core: TestCore(Arc::clone(&seen)),
        planner: Box::new(ScriptedPlan {
            prompt: script.prompt.to_string(),
            backend: script.backend.to_string(),
            fatal: script.fatal,
            model: script.model.to_string(),
            effort: script.effort.to_string(),
            drive: script.drive.clone(),
            error: script.plan_error,
            asked: Arc::clone(&asked),
        }),
        identity: Box::new(FixedProject(script.project_error)),
        names: Box::new(ScriptedNames(
            script.names.iter().map(|n| n.to_string()).collect(),
        )),
        ids: Box::new(FixedIds),
        enqueuer: Enqueuer::new(Arc::clone(&seen), Arc::new(CountingSource::default())),
        launcher,
        recorder: Recorder::new(records.clone()),
        orders,
        place: Place {
            project_dir: "/work/proj".to_string(),
            output_root: "/home/u/Documents/ctxloom/proj".to_string(),
            program_dir: script.program_dir.to_string(),
        },
        env: script
            .env
            .iter()
            .map(|(k, v)| (k.to_string(), v.to_string()))
            .collect(),
        links: Box::new(links.clone()),
        os: script.os.to_string(),
        hooks: Box::new(hooks.clone()),
        certs: Box::new(certs.clone()),
    });
    Rig {
        hooks,
        certs,
        runs,
        edge,
        runner: script.runner,
        records,
        asked,
        links,
    }
}

fn ask() -> OneShot {
    OneShot::default()
}

#[tokio::test]
async fn a_one_shot_answers_with_the_engines_reply_on_a_session_of_its_own() {
    let mut rig = rig(Script::answering());
    let done = rig.runs.one_shot(&Flags::default(), &ask()).await;
    let answer = done.result.unwrap();
    assert_eq!(answer.answer, "THE-ANSWER");
    assert_eq!(answer.harp, "amber-swift-owl");
    assert_eq!(answer.engine, "fake");
    // The session was minted on the name, once, and its run ran and ended.
    let events = session_events(&rig.edge, &answer.session).await;
    assert!(events
        .iter()
        .any(|e| matches!(e, sessions::SessionEvent::SessionMinted(m)
        if m.harp == "amber-swift-owl" && m.origin == sessions::Origin::Oneshot
            && m.project_id == "calm-otter" && m.project_dir == "/work/proj"
            && m.output_dir == "/home/u/Documents/ctxloom/proj/amber-swift-owl")));
    assert_eq!(
        event_names(&run_events(&rig.edge, &answer.session).await),
        [
            "RunEnqueued",
            "RunnerAttached",
            "RunnerStarted",
            "TurnStarted",
            "TurnCompleted",
            "RunEnded"
        ]
    );
}

#[tokio::test]
async fn the_engine_is_handed_the_plans_context_and_prompt_and_the_callers_environment() {
    let mut rig = rig(Script::answering());
    rig.runs
        .one_shot(&Flags::default(), &ask())
        .await
        .result
        .unwrap();
    let seen = rig.runner.seen.lock().unwrap();
    assert_eq!(seen.len(), 1);
    assert_eq!(seen[0].stdin, b"THE-PROMPT");
    assert_eq!(seen[0].cwd.as_deref(), Some("/work/proj"));
    assert_eq!(seen[0].env.get("HOME").map(String::as_str), Some("/home/u"));
    assert_eq!(
        rig.records
            .text("amber-swift-owl", "home/ctx.md")
            .as_deref(),
        Some("THE-CONTEXT")
    );
}

#[tokio::test]
async fn the_session_directory_is_written_from_the_events() {
    let mut rig = rig(Script::answering());
    rig.runs
        .one_shot(&Flags::default(), &ask())
        .await
        .result
        .unwrap();
    let names = rig.records.names("amber-swift-owl");
    for expected in [
        "metadata.json",
        "records/runs.jsonl",
        "records/session.jsonl",
        "home/ctx.md",
    ] {
        assert!(
            names.iter().any(|n| n == expected),
            "{expected} in {names:?}"
        );
    }
    // The conversation is the child's hooks' to report (ruling 26), and this
    // scripted engine has none: no transcript was written for it.
    assert!(
        !names.iter().any(|n| n == "transcripts/transcript.jsonl"),
        "{names:?}"
    );
}

#[tokio::test]
async fn the_plan_is_asked_with_the_request_and_its_notices_come_back() {
    let mut rig = rig(Script::answering());
    let request = OneShot {
        run: RunRequest {
            profile: "dev".to_string(),
            prompt: "p".to_string(),
            ..RunRequest::default()
        },
        engine: String::new(),
    };
    let done = rig.runs.one_shot(&Flags::default(), &request).await;
    assert_eq!(rig.asked.lock().unwrap()[0].profile, "dev");
    assert_eq!(done.notices.len(), 1);
}

#[tokio::test]
async fn a_plan_that_is_refused_starts_nothing_and_keeps_its_error() {
    let mut rig = rig(Script {
        plan_error: Some(|| PlanError::AgentExclusive {
            other: "profile".to_string(),
        }),
        ..Script::answering()
    });
    let done = rig.runs.one_shot(&Flags::default(), &ask()).await;
    let error = done.result.unwrap_err();
    assert!(matches!(
        &error,
        RunError::Plan(plan) if matches!(**plan, PlanError::AgentExclusive { .. })
    ));
    assert_eq!(error.exit_code(), 2);
    assert_eq!(done.notices.len(), 1, "what the plan noticed is still said");
    assert!(rig.runner.seen.lock().unwrap().is_empty());
}

#[tokio::test]
async fn a_fatal_startup_finding_refuses_the_run_before_any_session() {
    let mut rig = rig(Script {
        fatal: vec![FindingRow {
            kind: "profile".to_string(),
            text: "no such bundle".to_string(),
            remedy: String::new(),
            fatal: true,
        }],
        ..Script::answering()
    });
    let error = rig
        .runs
        .one_shot(&Flags::default(), &ask())
        .await
        .result
        .unwrap_err();
    assert_eq!(error.exit_code(), 3);
    assert!(matches!(
        &error,
        RunError::Plan(plan) if matches!(**plan, PlanError::StartupAborted { .. })
    ));
    assert!(rig.runner.seen.lock().unwrap().is_empty());
}

// A one-shot gets exactly one turn: an empty prompt asks nothing.
#[tokio::test]
async fn a_one_shot_with_no_prompt_refuses_before_a_session_exists() {
    for prompt in ["", "  \n"] {
        let mut rig = rig(Script {
            prompt,
            ..Script::answering()
        });
        let error = rig
            .runs
            .one_shot(&Flags::default(), &ask())
            .await
            .result
            .unwrap_err();
        assert!(matches!(error, RunError::NoPrompt), "{prompt:?}");
        assert!(rig.records.names("amber-swift-owl").is_empty());
        assert!(rig.runner.seen.lock().unwrap().is_empty());
    }
}

#[tokio::test]
async fn an_engine_that_cannot_be_driven_is_refused_naming_it_and_the_ones_that_can() {
    let mut rig = rig(Script {
        backend: "mock",
        ..Script::answering()
    });
    let error = rig
        .runs
        .one_shot(&Flags::default(), &ask())
        .await
        .result
        .unwrap_err();
    match &error {
        RunError::UnknownEngine { name, known } => {
            assert_eq!((name.as_str(), known.as_str()), ("mock", "fake"));
        }
        other => panic!("{other:?}"),
    }
    assert_eq!(error.exit_code(), 2);
    assert!(rig.runner.seen.lock().unwrap().is_empty());
}

#[tokio::test]
async fn the_engine_flag_overrides_the_plans() {
    let mut rig = rig(Script {
        backend: "mock",
        ..Script::answering()
    });
    let request = OneShot {
        engine: "fake".to_string(),
        ..ask()
    };
    let answer = rig
        .runs
        .one_shot(&Flags::default(), &request)
        .await
        .result
        .unwrap();
    assert_eq!(answer.engine, "fake");
    let planned = rig.asked.lock().unwrap().len();
    let bogus = OneShot {
        engine: "bogus-engine".to_string(),
        ..ask()
    };
    let error = rig
        .runs
        .one_shot(&Flags::default(), &bogus)
        .await
        .result
        .unwrap_err();
    assert!(matches!(&error, RunError::UnknownEngine { name, .. } if name == "bogus-engine"));
    assert_eq!(
        rig.asked.lock().unwrap().len(),
        planned,
        "an engine nobody drives is refused before the plan is asked"
    );
}

#[tokio::test]
async fn a_project_that_cannot_be_told_starts_nothing() {
    let mut rig = rig(Script {
        project_error: Some(Report::new("ports-registry-locked")),
        ..Script::answering()
    });
    let error = rig
        .runs
        .one_shot(&Flags::default(), &ask())
        .await
        .result
        .unwrap_err();
    assert!(
        matches!(&error, RunError::Project(report) if report.code() == "ports-registry-locked")
    );
    assert!(rig.runner.seen.lock().unwrap().is_empty());
}

// The mint is the first thing that can fail, and the name is claimed first: a
// name another session holds is not taken, and another is tried.
#[tokio::test]
async fn a_name_already_claimed_is_passed_over_for_the_next() {
    let mut rig = rig(Script::answering());
    // Another session already holds the first name.
    claim(
        &rig.edge,
        "amber-swift-owl",
        "5f0c7a52-2222-5222-8222-000000000002",
    )
    .await;
    let answer = rig
        .runs
        .one_shot(&Flags::default(), &ask())
        .await
        .result
        .unwrap();
    assert_eq!(answer.harp, "calm-quiet-fox");
}

#[tokio::test]
async fn names_that_are_all_taken_refuse_the_run_with_the_kernels_refusal() {
    let mut rig = rig(Script {
        names: vec!["a-a-a", "b-b-b", "c-c-c"],
        ..Script::answering()
    });
    for (n, harp) in ["a-a-a", "b-b-b", "c-c-c"].iter().enumerate() {
        claim(
            &rig.edge,
            harp,
            &format!("5f0c7a52-3333-5333-8333-00000000000{n}"),
        )
        .await;
    }
    let error = rig
        .runs
        .one_shot(&Flags::default(), &ask())
        .await
        .result
        .unwrap_err();
    assert!(
        matches!(&error, RunError::Session(CoreError::Refused { code, .. }) if code == "sessions-names-taken"),
        "{error:?}"
    );
    assert_eq!(error.exit_code(), 2);
    assert!(rig.runner.seen.lock().unwrap().is_empty());
}

#[tokio::test]
async fn a_rate_limited_turn_is_that_error_and_the_run_still_ended() {
    let engine = ScriptedEngine {
        outcome: TurnOutcome::Failed {
            class: FailureClass::RateLimited,
            detail: "slow down".to_string(),
            native_id: None,
        },
        refuses_to_drive: false,
    };
    let mut rig = rig(Script {
        engine,
        ..Script::answering()
    });
    let error = rig
        .runs
        .one_shot(&Flags::default(), &ask())
        .await
        .result
        .unwrap_err();
    assert!(
        matches!(&error, RunError::Turn { class: FailureClass::RateLimited, detail } if detail == "slow down")
    );
    assert_eq!(error.exit_code(), 1);
}

#[tokio::test]
async fn a_credential_the_engine_refused_is_that_error() {
    let engine = ScriptedEngine {
        outcome: TurnOutcome::Failed {
            class: FailureClass::CredentialRefused,
            detail: "401".to_string(),
            native_id: None,
        },
        refuses_to_drive: false,
    };
    let mut rig = rig(Script {
        engine,
        ..Script::answering()
    });
    let error = rig
        .runs
        .one_shot(&Flags::default(), &ask())
        .await
        .result
        .unwrap_err();
    assert!(matches!(
        &error,
        RunError::Turn {
            class: FailureClass::CredentialRefused,
            ..
        }
    ));
}

#[tokio::test]
async fn a_command_that_never_started_is_that_error() {
    let runner =
        ScriptedRunner::answering(run_ports::Launch::NotStarted(run_ports::LaunchFailure {
            deterministic: true,
            detail: "claude: not found".to_string(),
        }));
    let mut rig = rig(Script {
        runner,
        ..Script::answering()
    });
    let error = rig
        .runs
        .one_shot(&Flags::default(), &ask())
        .await
        .result
        .unwrap_err();
    assert!(matches!(&error, RunError::NotStarted { detail } if detail == "claude: not found"));
}

#[tokio::test]
async fn an_answer_of_nothing_is_an_error_not_an_empty_success() {
    let mut rig = rig(Script {
        engine: ScriptedEngine::answering("  ", None),
        ..Script::answering()
    });
    let error = rig
        .runs
        .one_shot(&Flags::default(), &ask())
        .await
        .result
        .unwrap_err();
    assert!(matches!(error, RunError::EmptyAnswer));
}

/// Claim `harp` for `session` in the names domain, as another run has.
async fn claim(edge: &TestEdge, harp: &str, session: &str) {
    use angzarr_embedded::CommandBookExt;
    use run_ports::Edge;
    let claim = sessions::NameClaimCommand::from(sessions::ClaimName {
        project_id: "calm-otter".to_string(),
        harp: harp.to_string(),
        session: session.to_string(),
    });
    let cover = angzarr_embedded::v1::Cover {
        domain: sessions::NAMES_DOMAIN.to_string(),
        root: Some(sessions::claim_root("calm-otter", harp)),
        edition: None,
    };
    edge.handle(angzarr_embedded::v1::CommandBook::command_of(cover, &claim))
        .await
        .unwrap();
}

// The human's session: the same plan and the same launch, her terminal given to
// the engine's command, no prompt needed, ended by her leaving it.
#[tokio::test]
async fn an_interactive_run_gives_her_terminal_to_the_engine_and_ends_by_its_exit() {
    let mut script = Script::answering();
    script.prompt = "";
    script.runner = ScriptedRunner::exits(0);
    let mut rig = rig(script);
    let done = rig.runs.interactive(&Flags::default(), &ask()).await;
    let ended = done.result.unwrap();
    assert_eq!(
        (ended.harp.as_str(), ended.engine.as_str()),
        ("amber-swift-owl", "fake")
    );
    assert_eq!(ended.code, Some(0));
    assert!(rig.runner.seen.lock().unwrap().is_empty());
    {
        let seen = rig.runner.seen_terminal.lock().unwrap();
        assert_eq!(seen[0].cwd.as_deref(), Some("/work/proj"));
    }
    let events = session_events(&rig.edge, &ended.session).await;
    assert!(events
        .iter()
        .any(|e| matches!(e, sessions::SessionEvent::SessionMinted(m)
        if m.origin == sessions::Origin::Session)));
    assert_eq!(
        event_names(&run_events(&rig.edge, &ended.session).await),
        ["RunEnqueued", "RunnerAttached", "RunnerStarted", "RunEnded"]
    );
}

#[tokio::test]
async fn an_interactive_run_that_cannot_start_its_engine_says_so() {
    let mut script = Script::answering();
    script.runner =
        ScriptedRunner::answering(run_ports::Launch::NotStarted(run_ports::LaunchFailure {
            deterministic: true,
            detail: "no claude".to_string(),
        }));
    let mut rig = rig(script);
    let done = rig.runs.interactive(&Flags::default(), &ask()).await;
    assert!(matches!(done.result, Err(RunError::NotStarted { .. })));
}

// A refusal of the claim that is not the name being taken is the run's error
// at once: another name would be refused the same way.
#[tokio::test]
async fn a_claim_refused_for_another_reason_is_not_tried_again_with_another_name() {
    let mut rig = rig(Script::answering());
    rig.runs.core.0.claims_refused.store(true, Ordering::SeqCst);
    let error = rig
        .runs
        .one_shot(&Flags::default(), &ask())
        .await
        .result
        .unwrap_err();
    assert!(
        matches!(&error, RunError::Session(CoreError::Refused { code, .. }) if code == "sessions-no-claim"),
        "{error:?}"
    );
    assert_eq!(rig.runs.core.0.claims.load(Ordering::SeqCst), 1);
    assert!(rig.runner.seen.lock().unwrap().is_empty());
}

// A launch that left no end to the run in its stream is an error naming the
// run, even though the engine answered.
#[tokio::test]
async fn a_run_whose_stream_holds_no_end_is_an_error_naming_it() {
    let mut rig = rig(Script::answering());
    rig.runs.core.0.hide_end.store(true, Ordering::SeqCst);
    let error = rig
        .runs
        .one_shot(&Flags::default(), &ask())
        .await
        .result
        .unwrap_err();
    let RunError::NotEnded { run } = &error else {
        panic!("{error:?}")
    };
    assert!(uuid::Uuid::parse_str(run).is_ok(), "{run:?} names a run");
    let mut rig = rig_again();
    rig.runs.core.0.hide_end.store(true, Ordering::SeqCst);
    let error = rig
        .runs
        .interactive(&Flags::default(), &ask())
        .await
        .result
        .unwrap_err();
    assert!(matches!(error, RunError::NotEnded { .. }), "{error:?}");
}

fn rig_again() -> Rig {
    let mut script = Script::answering();
    script.prompt = "";
    rig(script)
}

const NEEDS_A_TOKEN: Credentials = Credentials {
    child_env: &["CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"],
    primary_env: &[
        "CLAUDE_CODE_OAUTH_TOKEN",
        "ANTHROPIC_API_KEY",
        "ANTHROPIC_AUTH_TOKEN",
    ],
    user_dir: ".claude",
    login_file: Some(".credentials.json"),
};

fn needing_a_token() -> Script {
    Script {
        credentials: NEEDS_A_TOKEN,
        ..Script::answering()
    }
}

// A child agent is given an environment credential and nothing else: without
// one it is refused, before a name is claimed, a session made or an engine
// started.
#[tokio::test]
async fn a_one_shot_with_no_env_credential_is_refused_before_anything_is_made() {
    let mut rig = rig(needing_a_token());
    let error = rig
        .runs
        .one_shot(&Flags::default(), &ask())
        .await
        .result
        .unwrap_err();
    assert!(
        matches!(&error, RunError::Refused(runs::RunRejection::NoCredential { engine, .. }) if engine == "fake"),
        "{error:?}"
    );
    assert_eq!(error.code(), "runs-no-credential");
    assert_eq!(error.exit_code(), 2);
    assert_eq!(rig.runs.core.0.claims.load(Ordering::SeqCst), 0);
    assert!(rig.runner.seen.lock().unwrap().is_empty());
}

#[tokio::test]
async fn a_one_shot_with_an_env_credential_runs() {
    let mut script = needing_a_token();
    script.env = vec![("HOME", "/home/u"), ("ANTHROPIC_API_KEY", "k")]; // gitleaks:allow
    let mut rig = rig(script);
    let answer = rig
        .runs
        .one_shot(&Flags::default(), &ask())
        .await
        .result
        .unwrap();
    assert_eq!(answer.answer, "THE-ANSWER");
    // A child is never handed the login, whatever the host holds.
    assert!(rig.links.made.lock().unwrap().is_empty());
}

/// An engine block whose credential source is a file alone: what the primary
/// may share, and no child can use (ruling 53).
fn file_only() -> Script {
    let mut script = needing_a_token();
    script.drive.auth = library_application::Sourced {
        value: library_application::AuthSource::Credential {
            env: Vec::new(),
            file: Some("~/.claude/.credentials.json".to_string()),
        },
        source: library_application::Source::Engine,
    };
    script
}

// Ruling 53: a child of an engine whose source is a file alone is refused with
// the typed refusal naming the engine and the variables to export, even with
// one of them exported (the source names none, so the engine would not be
// given it): no listener served, no certificate minted, no name claimed, no
// engine spawned.
#[tokio::test]
async fn a_one_shot_on_a_file_only_source_is_refused_before_its_listener_or_session() {
    let mut script = file_only();
    script.env.push(("CLAUDE_CODE_OAUTH_TOKEN", "t")); // gitleaks:allow
    let mut rig = rig(script);
    let error = rig
        .runs
        .one_shot(&Flags::default(), &ask())
        .await
        .result
        .unwrap_err();
    assert!(
        matches!(&error, RunError::Refused(runs::RunRejection::FileOnlyCredential { engine, variables })
            if engine == "fake" && variables == "CLAUDE_CODE_OAUTH_TOKEN, ANTHROPIC_API_KEY"),
        "{error:?}"
    );
    assert_eq!(error.code(), "runs-file-only-credential");
    assert_eq!(error.exit_code(), 2);
    assert!(
        rig.hooks.log.lock().unwrap().is_empty(),
        "no listener served"
    );
    assert!(
        rig.certs.asked.lock().unwrap().is_empty(),
        "no certificate minted"
    );
    assert_eq!(rig.runs.core.0.claims.load(Ordering::SeqCst), 0);
    assert!(rig.runner.seen.lock().unwrap().is_empty());
}

// The primary is unchanged: her session may share the file.
#[tokio::test]
async fn the_primary_on_a_file_only_source_runs() {
    let mut script = file_only();
    script.prompt = "";
    let mut rig = rig(script);
    let done = rig.runs.interactive(&Flags::default(), &ask()).await;
    assert!(done.result.is_ok(), "{:?}", done.result.err());
}

// A child with no env credential at all is refused before its listener is
// served, as a file-only one is.
#[tokio::test]
async fn a_one_shot_with_no_env_credential_serves_no_listener() {
    let mut rig = rig(needing_a_token());
    let done = rig.runs.one_shot(&Flags::default(), &ask()).await;
    assert!(done.result.is_err());
    assert!(
        rig.hooks.log.lock().unwrap().is_empty(),
        "no listener served"
    );
    assert!(
        rig.certs.asked.lock().unwrap().is_empty(),
        "no certificate minted"
    );
}

#[tokio::test]
async fn the_primary_with_no_env_credential_is_linked_to_her_login_and_runs() {
    let mut script = needing_a_token();
    script.prompt = "";
    let mut rig = rig(script);
    let done = rig.runs.interactive(&Flags::default(), &ask()).await;
    assert!(done.result.is_ok());
    assert_eq!(
        *rig.links.made.lock().unwrap(),
        vec![(
            "amber-swift-owl".to_string(),
            "home/.credentials.json".to_string(),
            "/home/u/.claude/.credentials.json".to_string()
        )]
    );
}

#[tokio::test]
async fn the_primary_with_a_token_exported_is_not_linked() {
    let mut script = needing_a_token();
    script.env = vec![("HOME", "/home/u"), ("CLAUDE_CODE_OAUTH_TOKEN", "t")]; // gitleaks:allow
    let mut rig = rig(script);
    let done = rig.runs.interactive(&Flags::default(), &ask()).await;
    assert!(done.result.is_ok());
    assert!(rig.links.made.lock().unwrap().is_empty());
}

#[tokio::test]
async fn the_primary_with_no_login_to_link_runs_and_the_notice_says_what_to_do() {
    let mut script = needing_a_token();
    script.login = false;
    let mut rig = rig(script);
    let done = rig.runs.interactive(&Flags::default(), &ask()).await;
    assert!(done.result.is_ok());
    assert!(rig.links.made.lock().unwrap().is_empty());
    assert!(done.notices.iter().any(|n| matches!(n,
        Notice::Finding { text, remedy }
        if text.contains("no login at ~/.claude/.credentials.json") && remedy.contains("claude setup-token"))));
}

#[tokio::test]
async fn the_primary_on_macos_is_not_linked_and_is_told_to_set_a_token() {
    let mut script = needing_a_token();
    script.os = "macos";
    let mut rig = rig(script);
    let done = rig.runs.interactive(&Flags::default(), &ask()).await;
    assert!(done.result.is_ok());
    assert!(rig.links.made.lock().unwrap().is_empty());
    assert!(done.notices.iter().any(|n| matches!(n,
        Notice::Finding { remedy, .. } if remedy.contains("CLAUDE_CODE_OAUTH_TOKEN"))));
}

#[tokio::test]
async fn an_engine_that_asks_for_nothing_shares_nothing_and_notices_nothing_of_it() {
    let mut script = Script::answering();
    script.prompt = "";
    let mut rig = rig(script);
    let done = rig.runs.interactive(&Flags::default(), &ask()).await;
    assert!(done.result.is_ok());
    assert!(rig.links.made.lock().unwrap().is_empty());
    assert_eq!(done.notices.len(), 1, "only the plan's own");
}

// NEW-DESIGN (owner ruling 2026-11-09, item 16; ruling 9): the interactive
// engine's environment holds the endpoint, the run and the hook bearer: the
// bearer's token is the one whose digest the run journaled as the HOOK's, and
// no other principal's.
#[tokio::test]
async fn an_interactive_run_is_handed_the_listener_endpoint_and_the_hook_bearer() {
    let mut script = Script::answering();
    script.env = vec![
        ("PATH", "/bin"),
        ("HOME", "/home/u"),
        ("CTXLOOM_HOME", "/home/u"),
    ];
    let mut rig = rig(script);
    let ended = rig
        .runs
        .interactive(&Flags::default(), &ask())
        .await
        .result
        .unwrap();
    let env = rig.runner.seen_terminal.lock().unwrap()[0].env.clone();
    let run = runs::run_root(&ended.session, 1);
    let run_text = uuid::Uuid::from_slice(&run.value)
        .unwrap()
        .hyphenated()
        .to_string();
    assert_eq!(env["CTXLOOM_RUN"], run_text);
    // The hook finds its session by the harp: the engine's environment names it.
    assert_eq!(env["CTXLOOM_SESSION_HARP"], ended.harp);
    assert_eq!(env["CTXLOOM_ENDPOINT"], "https://127.0.0.1:40123");
    let (bearer_run, token) = run_ports::Bearer::parse(&env["CTXLOOM_BEARER"]).unwrap();
    assert_eq!(bearer_run, run_text);
    let events = run_events(&rig.edge, &ended.session).await;
    let runs::RunEvent::RunEnqueued(enqueued) = &events[0] else {
        panic!("first event is the enqueue: {events:?}");
    };
    assert_eq!(enqueued.hook_credential_hash, token.hash().as_bytes());
    assert_ne!(enqueued.runner_credential_hash, token.hash().as_bytes());
    assert_ne!(enqueued.model_credential_hash, token.hash().as_bytes());
}

// Ruling 51 (with 27 and 29): a one-shot is its own owner, so its CHILD's turn
// is handed the run's own listener variables too: the endpoint of the port it
// serves, its run, the hook bearer (whose digest the run journaled as the
// HOOK's), its certificate and its harp. What the caller's own environment held
// (a run started from inside another session) is replaced, never passed on.
#[tokio::test]
async fn a_childs_turn_is_handed_its_own_listener_endpoint_and_hook_bearer() {
    let mut script = Script::answering();
    script.env = vec![
        ("PATH", "/bin"),
        ("CTXLOOM_ENDPOINT", "https://10.0.0.9:1"),
        ("CTXLOOM_BEARER", "inherited.secret"),
        ("CTXLOOM_RUN", "inherited"),
        ("CTXLOOM_TLS_CERT", "inherited-cert"),
        ("CTXLOOM_SESSION_HARP", "inherited-harp"),
    ];
    let mut rig = rig(script);
    let answer = rig
        .runs
        .one_shot(&Flags::default(), &ask())
        .await
        .result
        .unwrap();
    let env = rig.runner.seen.lock().unwrap()[0].env.clone();
    let run = runs::run_root(&answer.session, 1);
    let run_text = uuid::Uuid::from_slice(&run.value)
        .unwrap()
        .hyphenated()
        .to_string();
    assert_eq!(env["CTXLOOM_RUN"], run_text);
    assert_eq!(env["CTXLOOM_SESSION_HARP"], answer.harp);
    assert_eq!(env["CTXLOOM_ENDPOINT"], "https://127.0.0.1:40123");
    assert_eq!(env["CTXLOOM_TLS_CERT"], minted_public().to_env_value());
    let (bearer_run, token) = run_ports::Bearer::parse(&env["CTXLOOM_BEARER"]).unwrap();
    assert_eq!(bearer_run, run_text);
    let events = run_events(&rig.edge, &answer.session).await;
    let runs::RunEvent::RunEnqueued(enqueued) = &events[0] else {
        panic!("first event is the enqueue: {events:?}");
    };
    assert_eq!(enqueued.hook_credential_hash, token.hash().as_bytes());
    assert_ne!(enqueued.runner_credential_hash, token.hash().as_bytes());
    assert_ne!(enqueued.model_credential_hash, token.hash().as_bytes());
}

// NEW-DESIGN: a primary run launched from inside another interactive session
// gets its OWN bearer, never the inherited one.
#[tokio::test]
async fn an_inherited_bearer_is_replaced_by_the_runs_own() {
    let mut script = Script::answering();
    script.env = vec![
        ("CTXLOOM_HOME", "/home/u"),
        ("CTXLOOM_BEARER", "inherited.secret"),
    ];
    let mut rig = rig(script);
    rig.runs
        .interactive(&Flags::default(), &ask())
        .await
        .result
        .unwrap();
    let env = rig.runner.seen_terminal.lock().unwrap()[0].env.clone();
    assert_ne!(env["CTXLOOM_BEARER"], "inherited.secret");
}

// NEW-DESIGN (no secret in a log line or an error): the bearer is in no Debug of
// what carries it from the launching code to the engine, nor in the error of a
// launch that failed to start.
#[tokio::test]
async fn the_bearer_is_in_no_debug_or_error_rendering() {
    let mut script = Script::answering();
    script.env = vec![("CTXLOOM_HOME", "/home/u")];
    script.runner =
        ScriptedRunner::answering(run_ports::Launch::NotStarted(run_ports::LaunchFailure {
            deterministic: true,
            detail: "no claude".to_string(),
        }));
    let mut rig = rig(script);
    let error = rig
        .runs
        .interactive(&Flags::default(), &ask())
        .await
        .result
        .unwrap_err();
    let command = rig.runner.seen_terminal.lock().unwrap()[0].clone();
    let bearer = command.env["CTXLOOM_BEARER"].clone();
    let secret = bearer.rsplit_once('.').unwrap().1.to_string();
    for shown in [
        format!("{error} {error:?}"),
        format!("{command:?}"),
        format!(
            "{:?}",
            run_launcher::Order {
                harp: "h".into(),
                engine: "e".into(),
                context: String::new(),
                prompt: String::new(),
                env: command.env.clone(),
                cwd: None,
                interactive: true,
                model: String::new(),
                effort: String::new(),
                settings: Default::default(),
            }
        ),
    ] {
        assert!(!shown.contains(&secret), "{shown}");
    }
}

// A launch that fails stops the listener too: no socket outlives the run.
#[tokio::test]
async fn a_launch_that_fails_still_stops_the_listener() {
    let mut script = Script::answering();
    script.runner =
        ScriptedRunner::answering(run_ports::Launch::NotStarted(run_ports::LaunchFailure {
            deterministic: true,
            detail: "no claude".to_string(),
        }));
    let mut rig = rig(script);
    let done = rig.runs.interactive(&Flags::default(), &ask()).await;
    assert!(matches!(done.result, Err(RunError::NotStarted { .. })));
    let log = rig.hooks.log.lock().unwrap().clone();
    let what: Vec<&str> = log.iter().map(|(what, _)| what.as_str()).collect();
    assert_eq!(what.len(), 2, "{what:?}");
    assert!(what[0].starts_with("serve_tcp "), "{what:?}");
    assert_eq!(what[1], "stop");
}

// A listener that cannot be started means no hook could reach the run: the
// engine is not launched, and the error says why.
#[tokio::test]
async fn a_listener_that_cannot_start_stops_the_run_before_its_engine() {
    let mut script = Script::answering();
    script.hooks_refuse = Some("hook-listener-bind");
    let mut rig = rig(script);
    let done = rig.runs.interactive(&Flags::default(), &ask()).await;
    let error = done.result.unwrap_err();
    assert!(
        matches!(&error, RunError::Listener(report) if report.code() == "hook-listener-bind"),
        "{error:?}"
    );
    assert_eq!(error.exit_code(), 1);
    assert!(rig.runner.seen_terminal.lock().unwrap().is_empty());
}

// Ruling 51: a one-shot serves the same listener as the terminal session, on
// loopback, before its child is launched, and stops it after the child exits.
#[tokio::test]
async fn a_one_shot_serves_its_listener_on_loopback_before_the_child_and_stops_it_after() {
    let mut rig = rig(Script::answering());
    rig.runs
        .one_shot(&Flags::default(), &ask())
        .await
        .result
        .unwrap();
    assert_eq!(
        *rig.hooks.log.lock().unwrap(),
        [
            ("serve_tcp 127.0.0.1".to_string(), 0),
            ("stop".to_string(), 1)
        ],
        "served on loopback before the child starts, stopped after it exits"
    );
}

// The child's hooks are delivered while the listener serves, so a one-shot's
// last catch-up (the transcript's writer) follows the listener's stop, as the
// terminal session's does.
#[tokio::test]
async fn a_one_shots_listener_is_stopped_before_the_run_is_last_recorded() {
    let mut rig = rig(Script::answering());
    rig.runs
        .one_shot(&Flags::default(), &ask())
        .await
        .result
        .unwrap();
    assert_eq!(
        *rig.hooks.end_projected_at_stop.lock().unwrap(),
        Some(false),
        "the run's end was recorded before the listener stopped"
    );
    assert!(rig
        .records
        .text("amber-swift-owl", "records/runs.jsonl")
        .unwrap()
        .contains("RunEnded"));
}

// No listener, no child: a hook could not be heard, so nothing is made and no
// engine started, and the error says why.
#[tokio::test]
async fn a_one_shot_whose_listener_cannot_start_launches_no_child() {
    let mut rig = rig(Script {
        hooks_refuse: Some("hook-listener-bind"),
        ..Script::answering()
    });
    let error = rig
        .runs
        .one_shot(&Flags::default(), &ask())
        .await
        .result
        .unwrap_err();
    assert_eq!(error.code(), "run-application-listener");
    assert_eq!(rig.runs.core.0.claims.load(Ordering::SeqCst), 0);
    assert!(rig.runner.seen.lock().unwrap().is_empty());
}

// A child that cannot be spawned stops the listener: no port outlives the run.
// A one-shot the plan's checks refuse (no prompt) serves none at all.
#[tokio::test]
async fn a_one_shot_that_does_not_run_still_stops_its_listener() {
    let mut never = Script::answering();
    never.runner =
        ScriptedRunner::answering(run_ports::Launch::NotStarted(run_ports::LaunchFailure {
            deterministic: true,
            detail: "no claude".to_string(),
        }));
    let mut silent = Script::answering();
    silent.prompt = "";
    for (script, served) in [
        (never, &["serve_tcp 127.0.0.1", "stop"][..]),
        (silent, &[][..]),
    ] {
        let mut rig = rig(script);
        let done = rig.runs.one_shot(&Flags::default(), &ask()).await;
        assert!(done.result.is_err());
        let log = rig.hooks.log.lock().unwrap().clone();
        let what: Vec<&str> = log.iter().map(|(what, _)| what.as_str()).collect();
        assert_eq!(what, served);
    }
}

// A child's hook finds its session by the directory's `metadata.json` too, so
// the directory names the session before the child is launched; and nothing of
// the conversation is written while it runs (the transcript is the projector's,
// at the run's end).
#[tokio::test]
async fn a_one_shots_session_directory_names_its_session_while_the_child_runs() {
    let rig = rig(Script::answering());
    let during = Arc::new(Mutex::new(None::<(Option<String>, Option<String>)>));
    let window = {
        let (records, during) = (rig.records.clone(), Arc::clone(&during));
        Arc::new(move || {
            *during.lock().unwrap() = Some((
                records.text("amber-swift-owl", "metadata.json"),
                records.text("amber-swift-owl", "transcripts/transcript.jsonl"),
            ));
        })
    };
    *rig.runner.during.lock().unwrap() = Some(window);
    let mut rig = rig;
    let answer = rig
        .runs
        .one_shot(&Flags::default(), &ask())
        .await
        .result
        .unwrap();
    let (metadata, transcript) = during.lock().unwrap().clone().expect("the child ran");
    let metadata = metadata.expect("metadata.json was written before the child ran");
    assert!(metadata.contains(&answer.session), "{metadata}");
    assert_eq!(transcript, None, "no conversation is written while it runs");
}

// A hook is a process of its own that finds its session by reading the
// session directory's `metadata.json`, so the directory must name the session
// before the engine is launched, not only after it ends (found by the capture
// end-to-end scenario: the hooks of the running engine saw no directory).
#[tokio::test]
async fn an_interactive_runs_session_directory_names_its_session_while_the_engine_runs() {
    let script = Script::answering();
    let rig = rig(script);
    let during = Arc::new(Mutex::new(None::<(Option<String>, Option<String>)>));
    let window = {
        let (records, during) = (rig.records.clone(), Arc::clone(&during));
        Arc::new(move || {
            *during.lock().unwrap() = Some((
                records.text("amber-swift-owl", "metadata.json"),
                records.text("amber-swift-owl", "transcripts/transcript.jsonl"),
            ));
        })
    };
    *rig.runner.during.lock().unwrap() = Some(window);
    let mut rig = rig;
    let ended = rig
        .runs
        .interactive(&Flags::default(), &ask())
        .await
        .result
        .unwrap();
    let (metadata, _) = during.lock().unwrap().clone().expect("the engine ran");
    let metadata = metadata.expect("metadata.json was written before the engine ran");
    assert!(metadata.contains(&ended.session), "{metadata}");
}

// A hook still in flight as the engine exits is delivered while the listener
// still serves, so the run's last catch-up (the transcript's writer) must come
// AFTER the listener has stopped and drained, or a fact delivered between the
// two is in the stream but never in the transcript. Forced here by reading,
// at the stop, whether the run's end was projected yet.
#[tokio::test]
async fn the_listener_is_stopped_before_the_run_is_last_recorded() {
    let mut rig = rig(Script::answering());
    rig.runs
        .interactive(&Flags::default(), &ask())
        .await
        .result
        .unwrap();
    assert_eq!(
        *rig.hooks.end_projected_at_stop.lock().unwrap(),
        Some(false),
        "the run's end was recorded before the listener stopped"
    );
    assert!(rig
        .records
        .text("amber-swift-owl", "records/runs.jsonl")
        .unwrap()
        .contains("RunEnded"));
}

fn rig_again_with(script: Script) -> Rig {
    rig(script)
}

// Live probe (2026-11-09), owner ruling: a different `ctxloom` (the Go binary)
// can come first on PATH, so the hook's bare `ctxloom` ran the wrong program.
// The directory of the RUNNING binary leads the terminal session's engine
// PATH, the inherited one after it; a child's turn is given its PATH as it was.
async fn interactive_path(script: Script) -> String {
    let mut rig = rig_again_with(script);
    rig.runs
        .interactive(&Flags::default(), &ask())
        .await
        .result
        .unwrap();
    let env = rig.runner.seen_terminal.lock().unwrap()[0].env.clone();
    env["PATH"].clone()
}

#[tokio::test]
async fn the_running_binarys_directory_leads_the_interactive_path_and_the_inherited_path_follows() {
    assert_eq!(
        interactive_path(Script::answering()).await,
        "/opt/ctxloom/bin:/bin"
    );
}

#[tokio::test]
async fn with_no_inherited_path_the_interactive_path_is_just_the_directory() {
    let mut bare = Script::answering();
    bare.env = vec![("HOME", "/home/u")];
    assert_eq!(interactive_path(bare).await, "/opt/ctxloom/bin");
}

#[tokio::test]
async fn a_binary_whose_directory_is_unknown_leaves_the_path_as_it_was() {
    let mut unknown = Script::answering();
    unknown.program_dir = "";
    assert_eq!(interactive_path(unknown).await, "/bin");
}

// Ruling 51: a child's hooks are the bare `ctxloom` too, so the running
// binary's directory leads a child's PATH as it leads the terminal's.
#[tokio::test]
async fn the_running_binarys_directory_leads_a_childs_path() {
    let mut rig = rig_again_with(Script::answering());
    rig.runs
        .one_shot(&Flags::default(), &ask())
        .await
        .result
        .unwrap();
    assert_eq!(
        rig.runner.seen.lock().unwrap()[0].env["PATH"],
        "/opt/ctxloom/bin:/bin"
    );
}

// Owner rulings 2026-11-09, items 31-33: the interactive run's hook listener
// binds loopback on a port of the system's choosing, serves before the engine
// starts, and the engine is told the URL of that port.
#[tokio::test]
async fn an_interactive_runs_engine_is_told_the_loopback_url_of_the_port_served() {
    let mut script = Script::answering();
    script.env = vec![("PATH", "/bin"), ("CTXLOOM_HOME", "/home/u")];
    let mut rig = rig(script);
    rig.runs
        .interactive(&Flags::default(), &ask())
        .await
        .result
        .unwrap();
    let env = rig.runner.seen_terminal.lock().unwrap()[0].env.clone();
    assert_eq!(env["CTXLOOM_ENDPOINT"], "https://127.0.0.1:40123");
    let log = rig.hooks.log.lock().unwrap().clone();
    assert_eq!(
        log,
        [
            ("serve_tcp 127.0.0.1".to_string(), 0),
            ("stop".to_string(), 1)
        ],
        "served on loopback before the engine starts, stopped after it exits"
    );
}

// A run refused by the plan's checks (an engine nobody drives) serves no
// listener at all; one refused after its listener was bound (the project
// cannot be told) stops it: no port outlives a run that never started.
#[tokio::test]
async fn a_run_refused_after_the_listener_was_bound_stops_it() {
    let mut rig = rig(Script::answering());
    let bogus = OneShot {
        engine: "bogus-engine".to_string(),
        ..ask()
    };
    let error = rig
        .runs
        .interactive(&Flags::default(), &bogus)
        .await
        .result
        .unwrap_err();
    assert!(matches!(error, RunError::UnknownEngine { .. }), "{error:?}");
    assert!(
        rig.hooks.log.lock().unwrap().is_empty(),
        "no listener served"
    );
    let mut rig = rig_with_project_error();
    let error = rig
        .runs
        .interactive(&Flags::default(), &ask())
        .await
        .result
        .unwrap_err();
    assert!(matches!(error, RunError::Project(_)), "{error:?}");
    let log = rig.hooks.log.lock().unwrap().clone();
    let what: Vec<&str> = log.iter().map(|(what, _)| what.as_str()).collect();
    assert_eq!(what, ["serve_tcp 127.0.0.1", "stop"]);
}

fn rig_with_project_error() -> Rig {
    rig(Script {
        project_error: Some(Report::new("ports-project-unwritable")),
        ..Script::answering()
    })
}

// A listener that cannot be bound is the run's refusal, with nothing made.
#[tokio::test]
async fn a_listener_that_cannot_bind_refuses_the_run_before_anything_is_made() {
    let mut rig = rig(Script {
        hooks_refuse: Some("hook-listener-bind"),
        ..Script::answering()
    });
    let error = rig
        .runs
        .interactive(&Flags::default(), &ask())
        .await
        .result
        .unwrap_err();
    assert_eq!(error.code(), "run-application-listener");
    assert_eq!(rig.runs.core.0.claims.load(Ordering::SeqCst), 0);
    assert!(rig.runner.seen_terminal.lock().unwrap().is_empty());
}

// A live probe (2026-11-09): a model never reached claude. The agent's model
// and effort the plan resolved are the order's, for the turn and for the
// terminal session.
#[tokio::test]
async fn the_plans_model_and_effort_are_handed_to_the_engine_for_a_turn_and_for_the_terminal() {
    let mut script = Script::answering();
    script.model = "haiku";
    script.effort = "low";
    let mut rig = rig(script);
    rig.runs
        .one_shot(&Flags::default(), &ask())
        .await
        .result
        .unwrap();
    let args = rig.runner.seen.lock().unwrap()[0].args.clone();
    assert!(args.contains(&"--model=haiku".to_string()), "{args:?}");
    assert!(args.contains(&"--effort=low".to_string()), "{args:?}");

    let mut script = Script::answering();
    script.model = "haiku";
    script.effort = "low";
    let mut rig = rig_again_with(script);
    rig.runs
        .interactive(&Flags::default(), &ask())
        .await
        .result
        .unwrap();
    let args = rig.runner.seen_terminal.lock().unwrap()[0].args.clone();
    assert!(args.contains(&"--model=haiku".to_string()), "{args:?}");
    assert!(args.contains(&"--effort=low".to_string()), "{args:?}");
}

// Rulings 35-37: the run's certificate is minted for every address the child
// may dial, BEFORE the listener is served with it (the identity the listener
// was given is the mint's), and the child is told the PUBLIC part alone.
#[tokio::test]
async fn the_certificate_is_minted_for_the_binds_names_and_served_and_told() {
    let mut script = Script::answering();
    script.env = vec![("PATH", "/bin"), ("CTXLOOM_HOME", "/home/u")];
    let mut rig = rig(script);
    rig.runs
        .interactive(&Flags::default(), &ask())
        .await
        .result
        .unwrap();
    let asked = rig.certs.asked.lock().unwrap().clone();
    assert_eq!(asked.len(), 1);
    assert_eq!(
        asked[0].ips(),
        vec![
            std::net::IpAddr::V4(std::net::Ipv4Addr::LOCALHOST),
            std::net::IpAddr::V6(std::net::Ipv6Addr::LOCALHOST)
        ]
    );
    assert!(asked[0].dns().is_empty());
    // The listener was served with the mint's identity.
    assert_eq!(*rig.hooks.identities.lock().unwrap(), [MINTED_KEY]);
    // The child was told the certificate, and no variable holds the key.
    let env = rig.runner.seen_terminal.lock().unwrap()[0].env.clone();
    assert_eq!(env["CTXLOOM_TLS_CERT"], minted_public().to_env_value());
    for (name, value) in &env {
        assert!(!value.contains(MINTED_KEY), "{name}");
    }
}

// Ruling 51: a one-shot's listener has its own certificate, minted for the
// bind's names before it is served, served with it, and told (the public part
// alone) to the child.
#[tokio::test]
async fn a_one_shot_mints_its_certificate_serves_with_it_and_tells_the_child() {
    let mut rig = rig(Script::answering());
    rig.runs
        .one_shot(&Flags::default(), &ask())
        .await
        .result
        .unwrap();
    assert_eq!(rig.certs.asked.lock().unwrap().len(), 1);
    assert_eq!(*rig.hooks.identities.lock().unwrap(), [MINTED_KEY]);
    let env = rig.runner.seen.lock().unwrap()[0].env.clone();
    assert_eq!(env["CTXLOOM_TLS_CERT"], minted_public().to_env_value());
    for (name, value) in &env {
        assert!(!value.contains(MINTED_KEY), "{name}");
    }
}

// A certificate that cannot be minted is the run's refusal, with nothing
// served, nothing made and no engine started.
#[tokio::test]
async fn a_certificate_that_cannot_be_minted_refuses_the_run_before_anything_is_made() {
    let mut rig = rig(Script {
        certs_refuse: Some("hook-tls-generate"),
        ..Script::answering()
    });
    let error = rig
        .runs
        .interactive(&Flags::default(), &ask())
        .await
        .result
        .unwrap_err();
    assert_eq!(error.code(), "run-application-listener");
    assert!(rig.hooks.log.lock().unwrap().is_empty(), "nothing served");
    assert_eq!(rig.runs.core.0.claims.load(Ordering::SeqCst), 0);
    assert!(rig.runner.seen_terminal.lock().unwrap().is_empty());
}

// ---- engines.<e>.* reaches the launch (owner rulings 44, 46, 48) ----

use library_application::{AuthSource, Drive, Source, Sourced};

fn driven(script: Script, drive: Drive) -> Script {
    Script { drive, ..script }
}

fn configured(auth: AuthSource) -> Sourced<AuthSource> {
    Sourced {
        value: auth,
        source: Source::Engine,
    }
}

fn names(of: &[&str]) -> Vec<String> {
    of.iter().map(ToString::to_string).collect()
}

// The program the plan resolved is the program the runner is given.
#[tokio::test]
async fn the_configured_command_is_the_program_run_for_a_turn_and_the_terminal() {
    for interactive in [false, true] {
        let mut script = driven(
            Script::answering(),
            Drive {
                command: "/opt/bin/claude".to_string(),
                ..Drive::default()
            },
        );
        script.prompt = if interactive { "" } else { "THE-PROMPT" };
        let mut rig = rig(script);
        if interactive {
            let _ = rig.runs.interactive(&Flags::default(), &ask()).await;
            let seen = rig.runner.seen_terminal.lock().unwrap();
            assert_eq!(seen[0].program, "/opt/bin/claude");
        } else {
            let _ = rig.runs.one_shot(&Flags::default(), &ask()).await;
            let seen = rig.runner.seen.lock().unwrap();
            assert_eq!(seen[0].program, "/opt/bin/claude");
        }
    }
}

// A configured env source replaces the engine's built-in variables: a child
// is refused without ONE OF ITS names, whatever else is exported.
#[tokio::test]
async fn a_child_is_refused_without_a_variable_of_the_configured_source() {
    let drive = Drive {
        auth: configured(AuthSource::Credential {
            env: names(&["ACME_TOKEN"]),
            file: None,
        }),
        ..Drive::default()
    };
    let mut script = driven(needing_a_token(), drive.clone());
    script.env = vec![("HOME", "/home/u"), ("ANTHROPIC_API_KEY", "k")]; // gitleaks:allow
    let mut rig_one = rig(script);
    let error = rig_one
        .runs
        .one_shot(&Flags::default(), &ask())
        .await
        .result
        .unwrap_err();
    assert!(
        matches!(&error, RunError::Refused(runs::RunRejection::NoCredential { variables, .. }) if variables == "ACME_TOKEN"),
        "{error:?}"
    );
    let mut script = driven(needing_a_token(), drive);
    script.env = vec![("HOME", "/home/u"), ("ACME_TOKEN", "t")]; // gitleaks:allow
    let mut rig_two = rig(script);
    let answer = rig_two
        .runs
        .one_shot(&Flags::default(), &ask())
        .await
        .result;
    assert!(answer.is_ok(), "{answer:?}");
}

// `auth: none` and `auth: login` ask a child for no variable.
#[tokio::test]
async fn a_source_of_none_or_login_refuses_no_child() {
    for auth in [AuthSource::None, AuthSource::Login] {
        let drive = Drive {
            auth: configured(auth),
            ..Drive::default()
        };
        let mut rig = rig(driven(needing_a_token(), drive));
        let answer = rig.runs.one_shot(&Flags::default(), &ask()).await.result;
        assert!(answer.is_ok(), "{answer:?}");
    }
}

// The primary shares the file the source names, and only when it names one.
#[tokio::test]
async fn the_primary_is_linked_to_the_file_the_source_names() {
    let drive = Drive {
        auth: configured(AuthSource::Credential {
            env: names(&["ACME_TOKEN"]),
            file: Some("~/.acme/creds.json".to_string()),
        }),
        ..Drive::default()
    };
    let mut script = driven(needing_a_token(), drive);
    script.prompt = "";
    let mut rig = rig(script);
    let done = rig.runs.interactive(&Flags::default(), &ask()).await;
    assert!(done.result.is_ok());
    assert_eq!(
        *rig.links.made.lock().unwrap(),
        vec![(
            "amber-swift-owl".to_string(),
            "home/.credentials.json".to_string(),
            "/home/u/.acme/creds.json".to_string()
        )]
    );
}

#[tokio::test]
async fn a_source_with_no_file_links_no_login_for_the_primary() {
    let drive = Drive {
        auth: configured(AuthSource::Credential {
            env: names(&["ACME_TOKEN"]),
            file: None,
        }),
        ..Drive::default()
    };
    let mut script = driven(needing_a_token(), drive);
    script.prompt = "";
    let mut rig = rig(script);
    let done = rig.runs.interactive(&Flags::default(), &ask()).await;
    assert!(done.result.is_ok());
    assert!(rig.links.made.lock().unwrap().is_empty());
}

// The host's own home has its own login: nothing is linked into it.
#[tokio::test]
async fn a_host_session_home_is_linked_no_login() {
    let drive = Drive {
        session_home: "host".to_string(),
        ..Drive::default()
    };
    let mut script = driven(needing_a_token(), drive);
    script.prompt = "";
    let mut rig = rig(script);
    let done = rig.runs.interactive(&Flags::default(), &ask()).await;
    assert!(done.result.is_ok());
    assert!(rig.links.made.lock().unwrap().is_empty());
}

// The settings of one engine are not another's: a run forced onto a different
// engine than the plan resolved is run on that engine's built-ins.
#[tokio::test]
async fn the_settings_of_the_planned_engine_are_not_applied_to_another() {
    let drive = Drive {
        command: "/opt/bin/other".to_string(),
        ..Drive::default()
    };
    let mut script = driven(Script::answering(), drive);
    script.backend = "planned-engine";
    let mut rig = rig(script);
    let mut asked = ask();
    asked.engine = "fake".to_string();
    let answer = rig.runs.one_shot(&Flags::default(), &asked).await.result;
    assert!(answer.is_ok(), "{answer:?}");
    assert_eq!(rig.runner.seen.lock().unwrap()[0].program, "fake");
}
