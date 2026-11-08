//! The one-shot over the real aggregates in an in-memory store, with the
//! plan, the project, the names and the engine scripted.

use std::collections::{BTreeMap, VecDeque};
use std::sync::{Arc, Mutex};

use angzarr_diagnostic::{Diagnostic, Report};
use angzarr_embedded::testing::MemoryStore;
use angzarr_embedded::v1::Uuid;
use angzarr_embedded::Catalog;
use library_application::{
    AppError as PlanError, DryRun, FindingRow, Flags, Notice, Outcome, Previewed, RunRequest,
};
use ports::{CommandIds, Names, Project, ProjectIdentity};
use run_launcher::fakes::{
    names as event_names, run_events, session_events, FixedClock, MemoryRecords, ScriptedEngine,
    ScriptedRunner, TestEdge,
};
use run_launcher::{Launcher, Orders, Recorder};
use run_ports::{FailureClass, Limits, Token, TokenSource, TurnOutcome};
use runs_application::{AppError as CoreError, Enqueuer};

use crate::{Core, OneShot, Parts, Place, Planner, RunError, Runs};

struct TestCore(Arc<TestEdge>);

impl Core for TestCore {
    type Edge = TestEdge;
    type Store = MemoryStore;

    fn edge(&self) -> Arc<TestEdge> {
        Arc::clone(&self.0)
    }
    fn store(&self) -> &MemoryStore {
        &self.0.store
    }
    fn catalog(&self) -> &Catalog {
        self.0.catalog()
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
    runs: Runs<TestCore, CountingSource, ScriptedRunner, MemoryRecords, FixedClock, MemoryRecords>,
    edge: Arc<TestEdge>,
    runner: ScriptedRunner,
    records: MemoryRecords,
    asked: Arc<Mutex<Vec<RunRequest>>>,
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
        }
    }
}

fn rig(script: Script) -> Rig {
    let edge = Arc::new(TestEdge::new());
    let records = MemoryRecords::default();
    let orders = Arc::new(Orders::new());
    let asked = Arc::new(Mutex::new(Vec::new()));
    let launcher = Launcher::new(
        Arc::clone(&edge),
        script.runner.clone(),
        records.clone(),
        FixedClock(1_700_000_000),
        vec![Arc::new(script.engine)],
        Arc::clone(&orders),
        Limits::new(std::time::Duration::from_secs(5)),
    );
    let runs = Runs::new(Parts {
        core: TestCore(Arc::clone(&edge)),
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
        enqueuer: Enqueuer::new(Arc::clone(&edge), Arc::new(CountingSource::default())),
        launcher,
        recorder: Recorder::new(records.clone()),
        orders,
        place: Place {
            project_dir: "/work/proj".to_string(),
            output_root: "/home/u/Documents/ctxloom/proj".to_string(),
        },
        env: BTreeMap::from([
            ("PATH".to_string(), "/bin".to_string()),
            ("HOME".to_string(), "/home/u".to_string()),
        ]),
    });
    Rig {
        runs,
        edge,
        runner: script.runner,
        records,
        asked,
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
        RunError::Plan(PlanError::AgentExclusive { .. })
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
        error,
        RunError::Plan(PlanError::StartupAborted { .. })
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
