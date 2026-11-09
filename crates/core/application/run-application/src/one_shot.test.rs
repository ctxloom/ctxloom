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
    Arrival, Credentials, Edge, FailureClass, Limits, LinkState, LoginLinks, RecordsError, Token,
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
            llm: "fake".to_string(),
            backend: self.backend.clone(),
            profiles: Vec::new(),
            fragments: Vec::new(),
            context: "THE-CONTEXT".to_string(),
            tokens: 0,
            prompt: self.prompt.clone(),
            findings: Vec::new(),
            axes: (String::new(), String::new()),
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

struct Rig {
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
        self.login && path == "/home/u/.claude/.credentials.json"
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
}

impl Script {
    fn answering() -> Script {
        Script {
            prompt: "THE-PROMPT",
            backend: "fake",
            engine: ScriptedEngine::answering("THE-ANSWER", Some("native-7")),
            runner: ScriptedRunner::exits(0),
            fatal: Vec::new(),
            plan_error: None,
            project_error: None,
            names: vec!["amber-swift-owl", "calm-quiet-fox", "wild-brave-elk"],
            credentials: Credentials::default(),
            env: vec![("PATH", "/bin"), ("HOME", "/home/u")],
            login: true,
            os: "linux",
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
    let runs = Runs::new(Parts {
        core: TestCore(Arc::clone(&seen)),
        planner: Box::new(ScriptedPlan {
            prompt: script.prompt.to_string(),
            backend: script.backend.to_string(),
            fatal: script.fatal,
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
        },
        env: script
            .env
            .iter()
            .map(|(k, v)| (k.to_string(), v.to_string()))
            .collect(),
        links: Box::new(links.clone()),
        os: script.os.to_string(),
    });
    Rig {
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
        "transcripts/transcript.jsonl",
        "home/ctx.md",
    ] {
        assert!(
            names.iter().any(|n| n == expected),
            "{expected} in {names:?}"
        );
    }
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
        ("XDG_RUNTIME_DIR", "/run/user/1000"),
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
    let short: String = run.value[..8].iter().map(|b| format!("{b:02x}")).collect();
    assert_eq!(
        env["CTXLOOM_ENDPOINT"],
        format!("/run/user/1000/ctxloom/{short}.sock")
    );
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

// NEW-DESIGN: with no runtime directory the endpoint is under ctxloom's home.
#[tokio::test]
async fn without_a_runtime_directory_the_endpoint_is_under_ctxloom_home() {
    let mut script = Script::answering();
    script.env = vec![("PATH", "/bin"), ("CTXLOOM_HOME", "/home/u")];
    let mut rig = rig(script);
    rig.runs
        .interactive(&Flags::default(), &ask())
        .await
        .result
        .unwrap();
    let env = rig.runner.seen_terminal.lock().unwrap()[0].env.clone();
    assert!(
        env["CTXLOOM_ENDPOINT"].starts_with("/home/u/.ctxloom/run/"),
        "{env:?}"
    );
}

// NEW-DESIGN: a CHILD's turn never gets them, even when the caller's own
// environment (a run started from inside an interactive session) holds some.
#[tokio::test]
async fn a_childs_turn_never_gets_the_listener_variables() {
    let mut script = Script::answering();
    script.env = vec![
        ("PATH", "/bin"),
        ("CTXLOOM_ENDPOINT", "/inherited.sock"),
        ("CTXLOOM_BEARER", "inherited.secret"),
        ("CTXLOOM_RUN", "inherited"),
    ];
    let mut rig = rig(script);
    rig.runs
        .one_shot(&Flags::default(), &ask())
        .await
        .result
        .unwrap();
    let env = rig.runner.seen.lock().unwrap()[0].env.clone();
    for name in run_ports::HOOK_VARIABLES {
        assert!(!env.contains_key(name), "{name} in {env:?}");
    }
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

// NEW-DESIGN (sun_path is about 108 bytes): a runtime directory so long that the
// socket path cannot fit is a typed, refusal-class error before anything is
// made: no name claimed, no run enqueued.
#[tokio::test]
async fn an_endpoint_too_long_for_a_socket_refuses_the_run_before_anything_is_made() {
    let long: &'static str = Box::leak(format!("/{}", "d".repeat(120)).into_boxed_str());
    let mut script = Script::answering();
    script.env = vec![("CTXLOOM_HOME", "/home/u"), ("XDG_RUNTIME_DIR", long)];
    let mut rig = rig(script);
    let error = rig
        .runs
        .interactive(&Flags::default(), &ask())
        .await
        .result
        .unwrap_err();
    assert_eq!(error.code(), "run-ports-endpoint-too-long");
    assert_eq!(error.exit_code(), 2);
    assert!(matches!(error, RunError::Endpoint(_)));
    assert_eq!(rig.runs.core.0.claims.load(Ordering::SeqCst), 0);
    assert!(rig.runner.seen_terminal.lock().unwrap().is_empty());
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
            }
        ),
    ] {
        assert!(!shown.contains(&secret), "{shown}");
    }
}
