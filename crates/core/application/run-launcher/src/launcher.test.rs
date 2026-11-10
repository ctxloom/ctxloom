//! The launcher over the real run and session aggregates in an in-memory
//! store: what it records, in what order, and what it writes.

use std::sync::{Arc, Mutex};

use run_ports::{FailureClass, Launch, LaunchFailure, Limits, TurnOutcome};
use runs::{EndCause, RunEvent};
use sessions::SessionEvent;

use crate::fakes::{
    enqueued, names, order, orders_with, run_events, session_events, MemoryRecords, ScriptedEngine,
    ScriptedRunner, TestEdge, HARP, SESSION,
};
use crate::{Launched, Launcher, Orders, Outcome, Recorder};

fn limits() -> Limits {
    Limits::new(std::time::Duration::from_secs(5))
}

struct Rig {
    edge: Arc<TestEdge>,
    launcher: Launcher<TestEdge, ScriptedRunner, MemoryRecords>,
    runner: ScriptedRunner,
    records: MemoryRecords,
    /// The projector, the transcript's only writer.
    recorder: Recorder<MemoryRecords>,
}

fn rig(
    engine: impl run_ports::Engine + 'static,
    runner: ScriptedRunner,
    orders: Arc<Orders>,
) -> Rig {
    let edge = Arc::new(TestEdge::new());
    let records = MemoryRecords::default();
    let launcher = Launcher::new(
        Arc::clone(&edge),
        runner.clone(),
        records.clone(),
        vec![Arc::new(engine)],
        orders,
        limits(),
    );
    let recorder = Recorder::new(records.clone());
    recorder.watch(SESSION);
    Rig {
        edge,
        launcher,
        runner,
        records,
        recorder,
    }
}

impl Rig {
    async fn step(&mut self) -> Vec<Launched> {
        let launched = self
            .launcher
            .step(&self.edge.store, self.edge.catalog())
            .await
            .unwrap();
        self.recorder
            .step(&self.edge.store, self.edge.catalog())
            .await
            .unwrap();
        launched
    }
}

// The turn: the run is owed a runner, the launcher drives the engine's command
// and records the sign of life BEFORE it runs (so the same run is not launched
// twice) and the end after.
#[tokio::test]
async fn an_owed_run_is_launched_once_and_its_turn_recorded_in_order() {
    let mut rig = rig(
        ScriptedEngine::answering("THE-ANSWER", Some("native-7")),
        ScriptedRunner::exits(0),
        orders_with(order("fake")),
    );
    enqueued(&rig.edge, SESSION, HARP).await;
    let launched = rig.step().await;
    assert_eq!(launched.len(), 1);
    assert_eq!(
        launched[0].outcome,
        Outcome::Answered {
            answer: "THE-ANSWER".to_string(),
            native_id: Some("native-7".to_string())
        }
    );
    assert_eq!(
        (launched[0].harp.as_str(), launched[0].engine.as_str()),
        (HARP, "fake")
    );
    assert_eq!(
        names(&run_events(&rig.edge, SESSION).await),
        [
            "RunEnqueued",
            "RunnerAttached",
            "RunnerStarted",
            "TurnStarted",
            "TurnCompleted",
            "RunEnded"
        ]
    );
    // Not again: the run is past, and its order was taken.
    assert!(rig.step().await.is_empty());
    assert_eq!(rig.runner.seen.lock().unwrap().len(), 1);
}

/// What the store held while the command ran: the run's events, and how many
/// runs the pending launches still owed.
type Glimpse = (Vec<&'static str>, usize);
type Glimpses = Arc<Mutex<Vec<Glimpse>>>;

/// Drive a store-reading future to its end on the spot: the in-memory store
/// never waits on anything, so one poll finishes it.
fn now<T>(future: impl std::future::Future<Output = T>) -> T {
    let mut future = std::pin::pin!(future);
    let mut cx = std::task::Context::from_waker(std::task::Waker::noop());
    match future.as_mut().poll(&mut cx) {
        std::task::Poll::Ready(value) => value,
        std::task::Poll::Pending => panic!("the in-memory store waited"),
    }
}

// The double launch, forced and not waited for: the instant the command is
// spawned (here: while it runs, silent, with no word of its own), the store
// already holds the launcher's RunnerStarted, and a catch-up that reads the
// streams THEN does not owe the run a runner again.
#[tokio::test]
async fn a_spawned_silent_turn_is_already_started_when_a_catch_up_reads_it() {
    let mut rig = rig(
        ScriptedEngine::answering("A", None),
        ScriptedRunner::exits(0),
        orders_with(order("fake")),
    );
    let seen: Arc<Mutex<Vec<Glimpse>>> = Arc::default();
    let (edge, record) = (Arc::clone(&rig.edge), Arc::clone(&seen));
    *rig.runner.during.lock().unwrap() = Some(Arc::new(move || {
        let events = now(run_events(&edge, SESSION));
        let mut model = pending_launches::Pending::new();
        let book = now(crate::fakes::run_pages(&edge, SESSION));
        model
            .project(&angzarr_embedded::v1::EventBook {
                cover: Some(crate::fakes::run_cover(SESSION)),
                pages: book,
                ..Default::default()
            })
            .unwrap();
        record
            .lock()
            .unwrap()
            .push((names(&events), model.launches().len()));
    }));
    enqueued(&rig.edge, SESSION, HARP).await;
    rig.step().await;
    let seen = seen.lock().unwrap();
    assert_eq!(seen.len(), 1);
    let (events, owed) = &seen[0];
    assert!(events.contains(&"RunnerStarted"), "{events:?}");
    assert!(!events.contains(&"TurnCompleted"), "{events:?}");
    assert_eq!(*owed, 0, "the run is not owed a runner while it runs");
}

#[tokio::test]
async fn the_runner_start_says_the_engine_the_directory_and_the_pid_of_the_spawn() {
    let mut rig = rig(
        ScriptedEngine::answering("A", None),
        ScriptedRunner::exits(0),
        orders_with(order("fake")),
    );
    enqueued(&rig.edge, SESSION, HARP).await;
    rig.step().await;
    let started = run_events(&rig.edge, SESSION)
        .await
        .into_iter()
        .find_map(|e| match e {
            RunEvent::RunnerStartedRecorded(s) => Some(s),
            _ => None,
        })
        .unwrap();
    assert_eq!(
        (
            started.runtime.as_str(),
            started.workspace.as_str(),
            started.pid
        ),
        ("fake", "/work/proj", crate::fakes::SCRIPTED_PID)
    );
}

#[tokio::test]
async fn the_command_is_the_engines_with_the_orders_prompt_environment_and_directory() {
    let mut rig = rig(
        ScriptedEngine::answering("A", None),
        ScriptedRunner::exits(0),
        orders_with(order("fake")),
    );
    enqueued(&rig.edge, SESSION, HARP).await;
    rig.step().await;
    let seen = rig.runner.seen.lock().unwrap();
    let command = &seen[0];
    assert_eq!(
        command.args,
        [format!("/work/proj/.ctxloom/sessions/{HARP}/home/ctx.md")],
        "the context is in the session home"
    );
    assert_eq!(command.stdin, b"THE-PROMPT");
    assert_eq!(command.env.get("PATH").map(String::as_str), Some("/bin"));
    assert_eq!(command.cwd.as_deref(), Some("/work/proj"));
}

#[tokio::test]
async fn the_session_home_holds_what_the_engine_prepared() {
    let mut rig = rig(
        ScriptedEngine::answering("A", None),
        ScriptedRunner::exits(0),
        orders_with(order("fake")),
    );
    enqueued(&rig.edge, SESSION, HARP).await;
    rig.step().await;
    assert_eq!(
        rig.records.text(HARP, "home/ctx.md").as_deref(),
        Some("THE-CONTEXT")
    );
}

// Owner rulings 26 and 51: a child's hooks are the ONLY source of its prompt
// and its words. The launcher records neither (it would be a second copy, under
// other ids), so a turn whose hooks never fire has no conversation in its
// transcript (the accepted cost); the reply its stream gave is still the
// launch's answer, and the turn's completion still names the transcript.
#[tokio::test]
async fn a_childs_turn_records_neither_its_prompt_nor_its_answer_and_still_answers() {
    let mut rig = rig(
        ScriptedEngine::answering("THE-ANSWER", None),
        ScriptedRunner::exits(0),
        orders_with(order("fake")),
    );
    enqueued(&rig.edge, SESSION, HARP).await;
    let launched = rig.step().await;
    assert_eq!(
        launched[0].outcome,
        Outcome::Answered {
            answer: "THE-ANSWER".to_string(),
            native_id: None
        }
    );
    let observed: Vec<SessionEvent> = session_events(&rig.edge, SESSION)
        .await
        .into_iter()
        .filter(|e| {
            matches!(
                e,
                SessionEvent::PromptSubmitted(_) | SessionEvent::AssistantTextShown(_)
            )
        })
        .collect();
    assert!(observed.is_empty(), "{observed:?}");
    assert_eq!(
        rig.records.text(HARP, "transcripts/transcript.jsonl"),
        None,
        "no conversation the hooks did not report"
    );
    let completed = run_events(&rig.edge, SESSION)
        .await
        .into_iter()
        .find_map(|e| match e {
            RunEvent::TurnCompletedRecorded(c) => Some(c),
            _ => None,
        });
    let completed = completed.unwrap();
    // Ruling 87a: there is no transcript file, so the event names none.
    // PROPOSAL: the reference is empty (the words are the session's events).
    assert_eq!(completed.answer_ref, "");
    assert_eq!(completed.turn, 1);
}

#[tokio::test]
async fn the_engines_own_session_is_bound_to_the_session() {
    let mut rig = rig(
        ScriptedEngine::answering("A", Some("native-7")),
        ScriptedRunner::exits(0),
        orders_with(order("fake")),
    );
    enqueued(&rig.edge, SESSION, HARP).await;
    rig.step().await;
    let bound = session_events(&rig.edge, SESSION)
        .await
        .into_iter()
        .find_map(|e| match e {
            SessionEvent::EngineSessionBound(b) => Some(b),
            _ => None,
        });
    let bound = bound.expect("the session was bound");
    assert_eq!(bound.native_id, "native-7");
    // Ruling 87a: the bind names no ctxloom transcript file (none is written).
    assert!(
        !bound
            .transcript_path
            .contains("transcripts/transcript.jsonl"),
        "{}",
        bound.transcript_path
    );
    let completed = run_events(&rig.edge, SESSION)
        .await
        .into_iter()
        .find_map(|e| match e {
            RunEvent::TurnCompletedRecorded(c) => Some(c),
            _ => None,
        });
    assert_eq!(completed.unwrap().native_key, "native-7");
}

#[tokio::test]
async fn the_exit_code_is_the_commands_and_the_run_ends_by_it() {
    let mut rig = rig(
        ScriptedEngine::answering("A", None),
        ScriptedRunner::exits(0),
        orders_with(order("fake")),
    );
    enqueued(&rig.edge, SESSION, HARP).await;
    rig.step().await;
    let ended = run_events(&rig.edge, SESSION)
        .await
        .into_iter()
        .find_map(|e| match e {
            RunEvent::RunEnded(ended) => Some(ended),
            _ => None,
        });
    let ended = ended.unwrap();
    assert_eq!(ended.cause, EndCause::RunnerExit);
    assert_eq!(ended.exit_code, Some(0));
}

#[tokio::test]
async fn a_run_with_no_order_here_is_left_for_its_owner() {
    let mut rig = rig(
        ScriptedEngine::answering("A", None),
        ScriptedRunner::exits(0),
        Arc::new(Orders::new()),
    );
    enqueued(&rig.edge, SESSION, HARP).await;
    assert!(rig.step().await.is_empty());
    assert_eq!(
        names(&run_events(&rig.edge, SESSION).await),
        ["RunEnqueued"]
    );
    assert!(rig.runner.seen.lock().unwrap().is_empty());
    // The launcher wrote nothing of its own (no home, no transcript); what the
    // watching recorder wrote is the session's record.
    let written = rig.records.names(HARP);
    assert!(
        written
            .iter()
            .all(|rel| !rel.starts_with("home/") && !rel.starts_with("transcripts/")),
        "{written:?}"
    );
}

#[tokio::test]
async fn a_failed_turn_is_recorded_with_its_class_and_says_nothing_in_the_transcript() {
    let engine = ScriptedEngine {
        outcome: TurnOutcome::Failed {
            class: FailureClass::RateLimited,
            detail: "slow down".to_string(),
            native_id: None,
        },
        refuses_to_drive: false,
    };
    let mut rig = rig(engine, ScriptedRunner::exits(1), orders_with(order("fake")));
    enqueued(&rig.edge, SESSION, HARP).await;
    let launched = rig.step().await;
    assert_eq!(
        launched[0].outcome,
        Outcome::Failed {
            class: FailureClass::RateLimited,
            detail: "slow down".to_string()
        }
    );
    let events = run_events(&rig.edge, SESSION).await;
    assert_eq!(
        names(&events),
        [
            "RunEnqueued",
            "RunnerAttached",
            "RunnerStarted",
            "TurnStarted",
            "TurnFailed",
            "RunEnded"
        ]
    );
    let failed = events.iter().find_map(|e| match e {
        RunEvent::TurnFailedRecorded(f) => Some(f),
        _ => None,
    });
    let failed = failed.unwrap();
    assert_eq!(failed.class, runs::TurnFailureClass::RateLimited);
    assert_eq!(failed.detail, "slow down");
    // The prompt is the hooks' to report (ruling 26): the launcher wrote none.
    assert_eq!(rig.records.text(HARP, "transcripts/transcript.jsonl"), None);
}

#[tokio::test]
async fn each_failure_class_is_the_runs_own() {
    for (class, expected) in [
        (
            FailureClass::RateLimited,
            runs::TurnFailureClass::RateLimited,
        ),
        (
            FailureClass::CredentialRefused,
            runs::TurnFailureClass::CredentialRefused,
        ),
        (
            FailureClass::EngineError,
            runs::TurnFailureClass::EngineError,
        ),
        (
            FailureClass::Interrupted,
            runs::TurnFailureClass::Interrupted,
        ),
    ] {
        let engine = ScriptedEngine {
            outcome: TurnOutcome::Failed {
                class,
                detail: String::new(),
                native_id: None,
            },
            refuses_to_drive: false,
        };
        let mut rig = rig(engine, ScriptedRunner::exits(0), orders_with(order("fake")));
        enqueued(&rig.edge, SESSION, HARP).await;
        rig.step().await;
        let failed = run_events(&rig.edge, SESSION)
            .await
            .into_iter()
            .find_map(|e| match e {
                RunEvent::TurnFailedRecorded(f) => Some(f),
                _ => None,
            });
        assert_eq!(failed.unwrap().class, expected, "{class:?}");
    }
}

// A command that could not be started: the run ends with the cause the
// aggregate gives a failed launch, and the reason is its detail.
#[tokio::test]
async fn a_command_that_could_not_start_ends_the_run_as_a_failed_launch() {
    let runner = ScriptedRunner::answering(Launch::NotStarted(LaunchFailure {
        deterministic: true,
        detail: "claude: not found".to_string(),
    }));
    let mut rig = rig(
        ScriptedEngine::answering("A", None),
        runner,
        orders_with(order("fake")),
    );
    enqueued(&rig.edge, SESSION, HARP).await;
    let launched = rig.step().await;
    assert_eq!(
        launched[0].outcome,
        Outcome::NotStarted {
            detail: "claude: not found".to_string()
        }
    );
    let events = run_events(&rig.edge, SESSION).await;
    let ended = events.iter().find_map(|e| match e {
        RunEvent::RunEnded(ended) => Some(ended),
        _ => None,
    });
    let ended = ended.unwrap();
    assert_eq!(ended.cause, EndCause::LaunchFailed);
    assert_eq!(ended.detail, "claude: not found");
    // Nothing was spawned, so nothing says a runner started.
    assert_eq!(
        names(&events),
        ["RunEnqueued", "RunnerAttached", "RunEnded"]
    );
}

// The ordering the truthful RunnerStarted rests on, forced and not waited
// for: INSIDE the spawn (the pid not yet given to the launcher) the store
// holds no RunnerStarted, and the moment the command runs it holds one with
// the spawn's pid.
#[tokio::test]
async fn the_runner_start_is_recorded_after_the_spawn_and_not_before() {
    let mut rig = rig(
        ScriptedEngine::answering("A", None),
        ScriptedRunner::exits(0),
        orders_with(order("fake")),
    );
    let at_spawn: Glimpses = Arc::default();
    let running: Glimpses = Arc::default();
    for (window, seen) in [
        (&rig.runner.at_spawn, &at_spawn),
        (&rig.runner.during, &running),
    ] {
        let (edge, seen) = (Arc::clone(&rig.edge), Arc::clone(seen));
        *window.lock().unwrap() = Some(Arc::new(move || {
            seen.lock()
                .unwrap()
                .push((names(&now(run_events(&edge, SESSION))), 0));
        }));
    }
    enqueued(&rig.edge, SESSION, HARP).await;
    rig.step().await;
    let at_spawn = at_spawn.lock().unwrap();
    assert_eq!(at_spawn.len(), 1);
    assert!(
        !at_spawn[0].0.contains(&"RunnerStarted"),
        "{:?}",
        at_spawn[0]
    );
    let running = running.lock().unwrap();
    assert!(running[0].0.contains(&"RunnerStarted"), "{:?}", running[0]);
}

// A command that cannot be spawned leaves the interactive run the same way.
#[tokio::test]
async fn an_interactive_command_that_could_not_start_records_no_runner_start() {
    let mut interactive = order("fake");
    interactive.interactive = true;
    let mut rig = rig(
        ScriptedEngine::answering("", None),
        ScriptedRunner::answering(Launch::NotStarted(LaunchFailure {
            deterministic: true,
            detail: "no claude".to_string(),
        })),
        orders_with(interactive),
    );
    enqueued(&rig.edge, SESSION, HARP).await;
    rig.step().await;
    assert_eq!(
        names(&run_events(&rig.edge, SESSION).await),
        ["RunEnqueued", "RunnerAttached", "RunEnded"]
    );
}

// The interactive launch records the pid of its spawn too.
#[tokio::test]
async fn an_interactive_runner_start_carries_the_pid_of_the_spawn() {
    let mut interactive = order("fake");
    interactive.interactive = true;
    let mut runner = ScriptedRunner::exits(0);
    runner.pid = 777;
    let mut rig = rig(
        ScriptedEngine::answering("", None),
        runner,
        orders_with(interactive),
    );
    enqueued(&rig.edge, SESSION, HARP).await;
    rig.step().await;
    let pid = run_events(&rig.edge, SESSION)
        .await
        .into_iter()
        .find_map(|e| match e {
            RunEvent::RunnerStartedRecorded(s) => Some(s.pid),
            _ => None,
        });
    assert_eq!(pid, Some(777));
}

// Nothing was started when there is no engine of that name, or it will not
// drive, or the home cannot be written: no sign of life is recorded and the
// command is never run.
#[tokio::test]
async fn an_engine_that_is_not_there_starts_nothing() {
    let mut rig = rig(
        ScriptedEngine::answering("A", None),
        ScriptedRunner::exits(0),
        orders_with(order("bogus")),
    );
    enqueued(&rig.edge, SESSION, HARP).await;
    let launched = rig.step().await;
    assert!(
        matches!(&launched[0].outcome, Outcome::NotStarted { detail } if detail.contains("bogus"))
    );
    assert_eq!(
        names(&run_events(&rig.edge, SESSION).await),
        ["RunEnqueued", "RunEnded"]
    );
    assert!(rig.runner.seen.lock().unwrap().is_empty());
}

#[tokio::test]
async fn an_engine_that_will_not_drive_starts_nothing() {
    let engine = ScriptedEngine {
        refuses_to_drive: true,
        ..ScriptedEngine::answering("A", None)
    };
    let mut rig = rig(engine, ScriptedRunner::exits(0), orders_with(order("fake")));
    enqueued(&rig.edge, SESSION, HARP).await;
    let launched = rig.step().await;
    assert!(matches!(launched[0].outcome, Outcome::NotStarted { .. }));
    assert_eq!(
        names(&run_events(&rig.edge, SESSION).await),
        ["RunEnqueued", "RunEnded"]
    );
    assert!(rig.runner.seen.lock().unwrap().is_empty());
}

#[tokio::test]
async fn a_home_that_cannot_be_written_starts_nothing() {
    let mut rig = rig(
        ScriptedEngine::answering("A", None),
        ScriptedRunner::exits(0),
        orders_with(order("fake")),
    );
    *rig.records.refuses.lock().unwrap() = Some("home/".to_string());
    enqueued(&rig.edge, SESSION, HARP).await;
    let launched = rig.step().await;
    assert!(
        matches!(&launched[0].outcome, Outcome::NotStarted { detail } if detail.contains("permission denied")),
        "{launched:?}"
    );
    assert_eq!(
        names(&run_events(&rig.edge, SESSION).await),
        ["RunEnqueued", "RunEnded"]
    );
    assert!(rig.runner.seen.lock().unwrap().is_empty());
}

#[tokio::test]
async fn an_order_names_the_engine_the_launcher_reports() {
    let rig = rig(
        ScriptedEngine::answering("A", None),
        ScriptedRunner::exits(0),
        Arc::new(Orders::new()),
    );
    assert!(rig.launcher.drives("fake"));
    assert!(!rig.launcher.drives("claude-code"));
    assert_eq!(rig.launcher.engines(), ["fake"]);
}

// The human's session: the engine's terminal command is given her terminal; no
// turn is recorded, the run ends by the exit, and the home carries the hooks.
#[tokio::test]
async fn an_interactive_launch_gives_the_terminal_to_the_engines_command_and_ends_by_its_exit() {
    let mut interactive = order("fake");
    interactive.interactive = true;
    interactive.prompt = String::new();
    let mut rig = rig(
        ScriptedEngine::answering("never read", None),
        ScriptedRunner::exits(4),
        orders_with(interactive),
    );
    enqueued(&rig.edge, SESSION, HARP).await;
    let launched = rig.step().await;
    assert_eq!(launched[0].outcome, Outcome::Ended { code: Some(4) });
    assert!(
        rig.runner.seen.lock().unwrap().is_empty(),
        "no captured turn"
    );
    {
        let seen = rig.runner.seen_terminal.lock().unwrap();
        assert_eq!(seen[0].program, "fake-terminal");
        assert_eq!(seen[0].cwd.as_deref(), Some("/work/proj"));
    }
    assert_eq!(
        names(&run_events(&rig.edge, SESSION).await),
        ["RunEnqueued", "RunnerAttached", "RunnerStarted", "RunEnded"]
    );
    // She has said nothing yet: the transcript holds no line.
    assert_eq!(rig.records.text(HARP, "transcripts/transcript.jsonl"), None);
    assert!(rig.records.text(HARP, "home/ctx.md").is_some());
}

#[tokio::test]
async fn an_interactive_command_that_cannot_start_is_not_started() {
    let mut interactive = order("fake");
    interactive.interactive = true;
    let mut rig = rig(
        ScriptedEngine::answering("", None),
        ScriptedRunner::answering(Launch::NotStarted(LaunchFailure {
            deterministic: true,
            detail: "no claude".to_string(),
        })),
        orders_with(interactive),
    );
    enqueued(&rig.edge, SESSION, HARP).await;
    let launched = rig.step().await;
    assert_eq!(
        launched[0].outcome,
        Outcome::NotStarted {
            detail: "no claude".to_string()
        }
    );
}

#[test]
fn the_launcher_hands_on_the_credentials_of_the_engine_it_holds() {
    use crate::fakes::Credentialed;
    let asks = run_ports::Credentials {
        child_env: &["A_TOKEN"],
        primary_env: &["A_TOKEN", "B_TOKEN"],
        user_dir: ".fake",
        login_file: Some("login.json"),
        ..run_ports::Credentials::NONE
    };
    let launcher = Launcher::new(
        Arc::new(TestEdge::new()),
        ScriptedRunner::exits(0),
        MemoryRecords::default(),
        vec![Arc::new(Credentialed(
            ScriptedEngine::answering("a", None),
            asks,
        ))],
        Arc::new(Orders::new()),
        limits(),
    );
    assert_eq!(launcher.credentials("fake"), Some(asks));
    assert_eq!(launcher.credentials("other"), None);
}

// The terminal session's opening prompt is its hooks' to record, as every
// other prompt is (ruling 26's principle): the engine submits it and its
// UserPromptSubmit hook records it, so the launcher recording it too made it
// twice in the transcript. The launcher records no prompt and no words of the
// assistant's for the terminal session, only the run's own facts.
#[tokio::test]
async fn the_terminal_sessions_opening_prompt_is_its_hooks_to_record_not_the_launchers() {
    let mut interactive = order("fake");
    interactive.interactive = true;
    let mut rig = rig(
        ScriptedEngine::answering("never read", None),
        ScriptedRunner::exits(0),
        orders_with(interactive),
    );
    enqueued(&rig.edge, SESSION, HARP).await;
    rig.step().await;
    // the prompt reached the engine's command, and nothing recorded it
    assert!(
        rig.runner.seen_terminal.lock().unwrap()[0]
            .args
            .iter()
            .any(|a| a.contains("THE-PROMPT")),
        "the opening prompt reached the engine"
    );
    let events = session_events(&rig.edge, SESSION).await;
    assert!(
        !events.iter().any(|e| matches!(
            e,
            SessionEvent::PromptSubmitted(_) | SessionEvent::AssistantTextShown(_)
        )),
        "{events:?}"
    );
    // the run's own facts are still the launcher's
    let run = names(&run_events(&rig.edge, SESSION).await);
    assert!(run.contains(&"RunnerStarted"), "{run:?}");
    assert!(run.contains(&"RunEnded"), "{run:?}");
}

// A live probe (2026-11-09): the model the plan resolved never reached the
// engine. The order carries it, and the engine's command is given it, for a
// turn and for the terminal session alike; an order with none gives none.
#[tokio::test]
async fn the_orders_model_reaches_the_engines_turn_and_terminal_command() {
    for interactive in [false, true] {
        let mut asked = order("fake");
        asked.model = "haiku".to_string();
        asked.interactive = interactive;
        let mut rig = rig(
            ScriptedEngine::answering("A", None),
            ScriptedRunner::exits(0),
            orders_with(asked),
        );
        enqueued(&rig.edge, SESSION, HARP).await;
        rig.step().await;
        let seen = if interactive {
            rig.runner.seen_terminal.lock().unwrap()
        } else {
            rig.runner.seen.lock().unwrap()
        };
        assert!(
            seen[0].args.contains(&"--model=haiku".to_string()),
            "interactive={interactive}: {:?}",
            seen[0].args
        );
    }
}

#[tokio::test]
async fn an_order_with_no_model_or_effort_gives_the_engine_none() {
    for interactive in [false, true] {
        let mut asked = order("fake");
        asked.interactive = interactive;
        let mut rig = rig(
            ScriptedEngine::answering("A", None),
            ScriptedRunner::exits(0),
            orders_with(asked),
        );
        enqueued(&rig.edge, SESSION, HARP).await;
        rig.step().await;
        let seen = if interactive {
            rig.runner.seen_terminal.lock().unwrap()
        } else {
            rig.runner.seen.lock().unwrap()
        };
        assert!(
            !seen[0]
                .args
                .iter()
                .any(|a| a.contains("model") || a.contains("effort")),
            "interactive={interactive}: {:?}",
            seen[0].args
        );
    }
}

// The settings the plan resolved (`engines.<e>.command` and the rest) are in
// the order, and reach the engine's request for a turn and for the terminal.
#[tokio::test]
async fn the_orders_engine_settings_reach_the_engines_request() {
    for interactive in [false, true] {
        let mut asked = order("fake");
        asked.interactive = interactive;
        asked.settings = run_ports::EngineSettings {
            command: "/opt/bin/claude".to_string(),
            ..run_ports::EngineSettings::default()
        };
        let mut rig = rig(
            ScriptedEngine::answering("A", None),
            ScriptedRunner::exits(0),
            orders_with(asked),
        );
        enqueued(&rig.edge, SESSION, HARP).await;
        rig.step().await;
        let seen = if interactive {
            rig.runner.seen_terminal.lock().unwrap()
        } else {
            rig.runner.seen.lock().unwrap()
        };
        assert_eq!(
            seen[0].program, "/opt/bin/claude",
            "interactive={interactive}"
        );
    }
}

// Ruling 58: an engine with no hook for its session's end (opencode) has it
// recorded by the launcher at its terminal command's exit, once, before the
// run ends; an engine whose hooks say it (claude) does not, and neither does a
// command that never started, nor a child's turn.
#[tokio::test]
async fn an_engine_whose_session_ends_with_its_exit_has_the_end_recorded_at_the_exit() {
    use crate::fakes::EndsByExit;
    let launch = |interactive: bool, runner: ScriptedRunner| {
        let mut asked = order("fake");
        asked.interactive = interactive;
        asked.prompt = String::new();
        let edge = Arc::new(TestEdge::new());
        let records = MemoryRecords::default();
        let launcher = Launcher::new(
            Arc::clone(&edge),
            runner,
            records.clone(),
            vec![Arc::new(EndsByExit(ScriptedEngine::answering("ok", None)))
                as Arc<dyn run_ports::Engine>],
            orders_with(asked),
            limits(),
        );
        (edge, launcher, records)
    };
    let (edge, mut launcher, records) = launch(true, ScriptedRunner::exits(0));
    let mut recorder = Recorder::new(records.clone());
    recorder.watch(SESSION);
    enqueued(&edge, SESSION, HARP).await;
    launcher.step(&edge.store, edge.catalog()).await.unwrap();
    recorder.step(&edge.store, edge.catalog()).await.unwrap();
    let ended: Vec<_> = session_events(&edge, SESSION)
        .await
        .into_iter()
        .filter_map(|e| match e {
            SessionEvent::SessionEnded(ended) => Some(ended),
            _ => None,
        })
        .collect();
    assert_eq!(
        ended,
        [sessions::SessionEnded {
            native_session: String::new(),
            reason: "exit".to_string(),
        }]
    );
    let transcript = records
        .text(HARP, "transcripts/transcript.jsonl")
        .expect("the end is a line");
    assert_eq!(transcript.lines().count(), 1, "{transcript}");
    assert!(
        transcript.contains(r#""kind":"session_end""#),
        "{transcript}"
    );
    assert_eq!(
        names(&run_events(&edge, SESSION).await),
        ["RunEnqueued", "RunnerAttached", "RunnerStarted", "RunEnded"]
    );

    for (interactive, runner) in [
        (
            true,
            ScriptedRunner::answering(Launch::NotStarted(LaunchFailure {
                deterministic: true,
                detail: "no opencode".to_string(),
            })),
        ),
        (false, ScriptedRunner::exits(0)),
    ] {
        let (edge, mut launcher, _) = launch(interactive, runner);
        enqueued(&edge, SESSION, HARP).await;
        launcher.step(&edge.store, edge.catalog()).await.unwrap();
        assert!(
            !session_events(&edge, SESSION)
                .await
                .iter()
                .any(|e| matches!(e, SessionEvent::SessionEnded(_))),
            "interactive {interactive}: no end recorded"
        );
    }
}

// Ruling 78: the caller's CTXLOOM_PERMIT_<NAME> reaches the engine's command
// as <NAME>, a child's turn and the terminal alike, whatever the engine passed;
// the permit itself is not passed, and the value is withheld from a Debug.
#[tokio::test]
async fn a_permit_reaches_the_command_under_its_own_name() {
    for interactive in [false, true] {
        let mut placed = order("fake");
        placed.interactive = interactive;
        placed.env.insert(
            "CTXLOOM_PERMIT_SECRET_KEY".to_string(),
            "permitted-value".to_string(),
        );
        let mut rig = rig(
            ScriptedEngine::answering("A", None),
            ScriptedRunner::exits(0),
            orders_with(placed),
        );
        enqueued(&rig.edge, SESSION, HARP).await;
        rig.step().await;
        let seen = if interactive {
            rig.runner.seen_terminal.lock().unwrap().clone()
        } else {
            rig.runner.seen.lock().unwrap().clone()
        };
        let command = &seen[0];
        assert_eq!(
            command.env.get("SECRET_KEY").map(String::as_str),
            Some("permitted-value"),
            "{interactive}"
        );
        assert!(!command.env.contains_key("CTXLOOM_PERMIT_SECRET_KEY"));
        assert!(!format!("{command:?}").contains("permitted-value"));
    }
}

// Ruling 78b: a permit never replaces what the engine's adapter computed (here
// a config dir it relocates) nor a name the run set (the order's `owned`); the
// launch says each it ignored, and a name merely passed through stays
// permittable.
#[tokio::test]
async fn a_permit_never_replaces_what_the_adapter_or_the_run_set() {
    let mut placed = order("fake");
    placed.env.insert(
        "CTXLOOM_PERMIT_FAKE_CONFIG_DIR".to_string(),
        "/x".to_string(),
    );
    placed
        .env
        .insert("CTXLOOM_PERMIT_PATH".to_string(), "/evil".to_string());
    placed
        .env
        .insert("CTXLOOM_PERMIT_SECRET_KEY".to_string(), "v".to_string());
    placed.owned = vec!["PATH".to_string()];
    let mut rig = rig(
        crate::fakes::Relocating(ScriptedEngine::answering("A", None)),
        ScriptedRunner::exits(0),
        orders_with(placed),
    );
    enqueued(&rig.edge, SESSION, HARP).await;
    let launched = rig.step().await;
    let command = rig.runner.seen.lock().unwrap()[0].clone();
    assert_eq!(
        command.env.get("FAKE_CONFIG_DIR").map(String::as_str),
        Some("/session/config")
    );
    assert_eq!(command.env.get("PATH").map(String::as_str), Some("/bin"));
    assert_eq!(command.env.get("SECRET_KEY").map(String::as_str), Some("v"));
    assert_eq!(
        launched[0].ignored,
        vec![
            run_ports::Ignored::Owned {
                permit: "CTXLOOM_PERMIT_FAKE_CONFIG_DIR".to_string()
            },
            run_ports::Ignored::Owned {
                permit: "CTXLOOM_PERMIT_PATH".to_string()
            },
        ]
    );
}
