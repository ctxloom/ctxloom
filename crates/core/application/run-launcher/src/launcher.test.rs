//! The launcher over the real run and session aggregates in an in-memory
//! store: what it records, in what order, and what it writes.

use std::sync::{Arc, Mutex};

use run_ports::{FailureClass, Launch, LaunchFailure, Limits, TurnOutcome};
use runs::{EndCause, RunEvent};
use sessions::SessionEvent;

use crate::fakes::{
    enqueued, enqueued_by, names, order, orders_with, run_events, session_events, MemoryRecords,
    ScriptedEngine, ScriptedRunner, TestEdge, HARP, SESSION,
};
use crate::{Launched, Launcher, Orders, Outcome};

fn limits() -> Limits {
    Limits::new(std::time::Duration::from_secs(5))
}

struct Rig {
    edge: Arc<TestEdge>,
    launcher: Launcher<TestEdge, ScriptedRunner, MemoryRecords>,
    runner: ScriptedRunner,
    records: MemoryRecords,
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
    Rig {
        edge,
        launcher,
        runner,
        records,
    }
}

impl Rig {
    async fn step(&mut self) -> Vec<Launched> {
        let launched = self
            .launcher
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

// Owner ruling 112: the engine's HOME is its session home, so the home is
// seeded with the user's git identity (`.gitconfig`, `[user]` only), and one
// already there (a tool may have written it in an earlier run) is left as it
// is.
#[tokio::test]
async fn the_session_home_is_seeded_with_the_users_git_identity_once() {
    let user = run_ports::GitUser {
        name: "Alice Example".to_string(),
        email: "alice@example.com".to_string(),
    };
    let mut seeded = order("fake");
    seeded.git_user = Some(user.clone());
    let mut first = rig(
        ScriptedEngine::answering("A", None),
        ScriptedRunner::exits(0),
        orders_with(seeded.clone()),
    );
    enqueued(&first.edge, SESSION, HARP).await;
    first.step().await;
    assert_eq!(
        first.records.text(HARP, "home/.gitconfig"),
        run_ports::git_config(&user)
    );
    assert!(run_ports::git_config(&user).is_some());

    let mut kept = rig(
        ScriptedEngine::answering("A", None),
        ScriptedRunner::exits(0),
        orders_with(seeded),
    );
    use run_ports::SessionRecords;
    kept.records
        .write(HARP, "home/.gitconfig", b"[credential]\n\thelper = store\n")
        .unwrap();
    enqueued(&kept.edge, SESSION, HARP).await;
    kept.step().await;
    assert_eq!(
        kept.records.text(HARP, "home/.gitconfig").as_deref(),
        Some("[credential]\n\thelper = store\n")
    );
}

// Owner ruling 113: an engine that keeps its own state in a home file is
// handed what the home holds of it (a resumed session's home has it), and the
// directory it runs in, so it merges its keys in rather than replace the file.
#[tokio::test]
async fn an_engine_is_handed_the_home_files_it_merges_into() {
    use run_ports::SessionRecords;
    let mut rig = rig(
        crate::fakes::Merging(ScriptedEngine::answering("A", None)),
        ScriptedRunner::exits(0),
        orders_with(order("fake")),
    );
    rig.records.write(HARP, "home/state.json", b"KEPT").unwrap();
    enqueued(&rig.edge, SESSION, HARP).await;
    rig.step().await;
    assert_eq!(
        rig.records.text(HARP, "home/state.json").as_deref(),
        Some("KEPT+/work/proj")
    );
}

// Owner ruling 167: what an engine seeds its home with (opencode's: the
// user's `model.json`) is written once, as the `.gitconfig` is; one already
// there (the engine of an earlier run may have rewritten it) is left as it is.
#[tokio::test]
async fn an_engines_seed_is_written_once_and_one_there_is_left_as_it_is() {
    use run_ports::SessionRecords;
    let mut first = rig(
        crate::fakes::Seeding(ScriptedEngine::answering("A", None)),
        ScriptedRunner::exits(0),
        orders_with(order("fake")),
    );
    enqueued(&first.edge, SESSION, HARP).await;
    first.step().await;
    assert_eq!(
        first.records.text(HARP, "home/seed.json").as_deref(),
        std::str::from_utf8(crate::fakes::SEED).ok()
    );

    let mut kept = rig(
        crate::fakes::Seeding(ScriptedEngine::answering("A", None)),
        ScriptedRunner::exits(0),
        orders_with(order("fake")),
    );
    kept.records
        .write(HARP, "home/seed.json", b"PICKED-IN-THE-SESSION")
        .unwrap();
    enqueued(&kept.edge, SESSION, HARP).await;
    kept.step().await;
    assert_eq!(
        kept.records.text(HARP, "home/seed.json").as_deref(),
        Some("PICKED-IN-THE-SESSION")
    );
}

// Owner ruling 169: a user settings file the engine left out of the home is
// reported with the launch, by path, for the run to say.
#[tokio::test]
async fn what_the_engine_left_out_of_the_home_is_reported_with_the_launch() {
    let mut rig = rig(
        ScriptedEngine::answering("A", None).leaving_out(),
        ScriptedRunner::exits(0),
        orders_with(order("fake")),
    );
    enqueued(&rig.edge, SESSION, HARP).await;
    let launched = rig.step().await;
    assert_eq!(launched.len(), 1);
    assert_eq!(
        launched[0].left_out,
        [run_ports::LeftOut {
            engine: "fake".to_string(),
            path: crate::fakes::LEFT_OUT.to_string(),
        }]
    );
    let mut plain = rig_plain();
    enqueued(&plain.edge, SESSION, HARP).await;
    assert!(plain.step().await[0].left_out.is_empty());
}

fn rig_plain() -> Rig {
    rig(
        ScriptedEngine::answering("A", None),
        ScriptedRunner::exits(0),
        orders_with(order("fake")),
    )
}

// Under `session_home: host` the engine's HOME is the user's own, which
// already holds her git identity: the session home is not seeded with a
// `.gitconfig` nothing reads.
#[tokio::test]
async fn a_host_home_is_not_seeded_with_the_users_git_identity() {
    let mut host = order("fake");
    host.git_user = Some(run_ports::GitUser {
        name: "Alice Example".to_string(),
        email: "alice@example.com".to_string(),
    });
    host.settings.session_home = run_ports::SessionHome::Host;
    let mut rig = rig(
        ScriptedEngine::answering("A", None),
        ScriptedRunner::exits(0),
        orders_with(host),
    );
    enqueued(&rig.edge, SESSION, HARP).await;
    rig.step().await;
    assert_eq!(rig.records.text(HARP, "home/.gitconfig"), None);
}

#[tokio::test]
async fn no_known_git_identity_seeds_no_gitconfig() {
    let mut rig = rig(
        ScriptedEngine::answering("A", None),
        ScriptedRunner::exits(0),
        orders_with(order("fake")),
    );
    enqueued(&rig.edge, SESSION, HARP).await;
    rig.step().await;
    assert_eq!(rig.records.text(HARP, "home/.gitconfig"), None);
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
    // The launcher wrote nothing of its own (no home).
    assert!(rig.records.names(HARP).is_empty());
}

#[tokio::test]
async fn a_failed_turn_is_recorded_with_its_class_and_says_nothing_in_the_transcript() {
    let engine = ScriptedEngine {
        outcome: TurnOutcome::Failed {
            class: FailureClass::RateLimited,
            detail: "slow down".to_string(),
            native_id: None,
        },
        ..ScriptedEngine::answering("", None)
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
            ..ScriptedEngine::answering("", None)
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

// Owner ruling 2026-11-12: a terminal session whose opening prompt makes its
// command too long for the platform is refused before the spawn, typed: the
// run ends launch-failed and nothing is spawned. A prompt that fits is
// spawned; a launcher told no OS checks nothing.
#[tokio::test]
async fn a_terminal_command_too_long_for_the_platform_is_refused_before_the_spawn() {
    let launch = |os: &str, prompt_len: usize| {
        let mut interactive = order("fake");
        interactive.interactive = true;
        interactive.prompt = "a".repeat(prompt_len);
        let mut rig = rig(
            ScriptedEngine::answering("A", None),
            ScriptedRunner::exits(0),
            orders_with(interactive),
        );
        rig.launcher = rig.launcher.on_os(os);
        rig
    };
    let mut over = launch("linux", run_ports::MAX_ARG_STRLEN);
    enqueued(&over.edge, SESSION, HARP).await;
    let launched = over.step().await;
    assert!(
        matches!(&launched[0].outcome, Outcome::TooLong(refusal)
            if refusal.os == "linux" && refusal.limit == run_ports::MAX_ARG_STRLEN),
        "{launched:?}"
    );
    assert!(over.runner.seen_terminal.lock().unwrap().is_empty());
    assert_eq!(
        names(&run_events(&over.edge, SESSION).await),
        ["RunEnqueued", "RunEnded"]
    );

    let mut under = launch("linux", run_ports::MAX_ARG_STRLEN - 1);
    enqueued(&under.edge, SESSION, HARP).await;
    let launched = under.step().await;
    assert!(
        matches!(launched[0].outcome, Outcome::Ended { .. }),
        "{launched:?}"
    );
    assert_eq!(under.runner.seen_terminal.lock().unwrap().len(), 1);

    let mut windows = launch("windows", run_ports::WINDOWS_COMMAND_LINE);
    enqueued(&windows.edge, SESSION, HARP).await;
    let launched = windows.step().await;
    assert!(
        matches!(&launched[0].outcome, Outcome::TooLong(refusal) if refusal.os == "windows"),
        "{launched:?}"
    );

    let mut untold = launch("", run_ports::MAX_ARG_STRLEN);
    enqueued(&untold.edge, SESSION, HARP).await;
    assert!(matches!(
        untold.step().await[0].outcome,
        Outcome::Ended { .. }
    ));
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

// Owner rulings 135, 137: the order's permissions document reaches the
// engine's home as the engine is handed it, byte for byte, for a turn and for
// the terminal session alike; none in the order, none for the engine. No
// flag carries it (ruling 137 retired `--permission-mode`).
#[tokio::test]
async fn the_orders_permissions_reach_the_engine_s_home_verbatim() {
    for interactive in [false, true] {
        let mut asked = order("fake");
        asked.permissions = Some(r#"{"z":"deny","*":"ask","defaultMode":"plan"}"#.to_string());
        asked.interactive = interactive;
        let mut rig = rig(
            ScriptedEngine::answering("A", None),
            ScriptedRunner::exits(0),
            orders_with(asked),
        );
        enqueued(&rig.edge, SESSION, HARP).await;
        rig.step().await;
        assert_eq!(
            rig.records.text(HARP, "home/permissions.json").as_deref(),
            Some(r#"{"z":"deny","*":"ask","defaultMode":"plan"}"#),
            "interactive={interactive}"
        );
    }
    let mut rig = rig(
        ScriptedEngine::answering("A", None),
        ScriptedRunner::exits(0),
        orders_with(order("fake")),
    );
    enqueued(&rig.edge, SESSION, HARP).await;
    rig.step().await;
    assert_eq!(rig.records.text(HARP, "home/permissions.json"), None);
}

// Owner T21: what the engine writes in the home (its permissions among it) is
// written again at EVERY launch, a fresh one, a turn and a resume alike, from
// the order (the user's config), over whatever the session's agent left there:
// an agent's edit of its own permissions never survives into a launch.
#[tokio::test]
async fn an_agents_edit_of_its_home_permissions_is_overwritten_at_every_launch() {
    use run_ports::SessionRecords;
    const DOCUMENT: &str = r#"{"defaultMode":"acceptEdits"}"#;
    for (interactive, resume) in [
        (false, None),
        (false, Some("native-7")),
        (true, Some("native-7")),
    ] {
        let mut asked = order("fake");
        asked.permissions = Some(DOCUMENT.to_string());
        asked.interactive = interactive;
        asked.resume = resume.map(String::from);
        let mut rig = rig(
            ScriptedEngine::answering("A", None),
            ScriptedRunner::exits(0),
            orders_with(asked),
        );
        rig.records
            .write(
                HARP,
                "home/permissions.json",
                br#"{"defaultMode":"bypassPermissions"}"#,
            )
            .unwrap();
        enqueued(&rig.edge, SESSION, HARP).await;
        rig.step().await;
        assert_eq!(
            rig.records.text(HARP, "home/permissions.json").as_deref(),
            Some(DOCUMENT),
            "interactive={interactive} resume={resume:?}"
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
                .any(|a| a.contains("model") || a.contains("effort") || a.contains("permission")),
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
    enqueued(&edge, SESSION, HARP).await;
    launcher.step(&edge.store, edge.catalog()).await.unwrap();
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
    assert!(records.text(HARP, "transcripts/transcript.jsonl").is_none());
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

// Owner ruling 115: a terminal session is resumed by its engine's own
// session, which its hooks name at its end (claude's SessionEnd): once the
// terminal exits, the launcher binds the last one named, as a child's turn
// binds the one its stream names. None named, nothing is bound.
#[tokio::test]
async fn a_terminal_sessions_native_session_its_hooks_named_is_bound_at_its_exit() {
    let bound = |events: Vec<SessionEvent>| -> Vec<String> {
        events
            .into_iter()
            .filter_map(|e| match e {
                SessionEvent::EngineSessionBound(b) => Some(b.native_id),
                _ => None,
            })
            .collect()
    };
    let mut asked = order("fake");
    asked.interactive = true;
    let mut named = rig(
        ScriptedEngine::answering("ok", None),
        ScriptedRunner::exits(0),
        orders_with(asked.clone()),
    );
    enqueued(&named.edge, SESSION, HARP).await;
    crate::fakes::hook_session_ended(&named.edge, SESSION, "native-term-1").await;
    named.step().await;
    assert_eq!(
        bound(session_events(&named.edge, SESSION).await),
        ["native-term-1"]
    );

    let mut none = rig(
        ScriptedEngine::answering("ok", None),
        ScriptedRunner::exits(0),
        orders_with(asked),
    );
    enqueued(&none.edge, SESSION, HARP).await;
    none.step().await;
    assert!(bound(session_events(&none.edge, SESSION).await).is_empty());
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

// ----------------------------------- orphaned runs (rulings 86, 92, 93)

/// The run lock of the run that `SESSION` enqueued, whose launcher died.
const RUN_LOCK: &str = "/home/u/.ctxloom/runs/run-1.lock";

/// The run and session locks, as a test says them: the run lock's probe is
/// `run`, the session's (`/work/proj`, `HARP`) is `session`, every other
/// unproven; `before` runs once, the first time a lock is probed (another
/// process, forced into the gap between the probe and the record).
struct Locks {
    run: run_ports::LockProbe,
    session: run_ports::LockProbe,
    before: Mutex<Option<Box<dyn FnOnce() + Send>>>,
    asked: Mutex<usize>,
}

impl Locks {
    fn saying(run: run_ports::LockProbe, session: run_ports::LockProbe) -> Arc<Locks> {
        Arc::new(Locks {
            run,
            session,
            before: Mutex::new(None),
            asked: Mutex::new(0),
        })
    }

    fn asked(&self) -> usize {
        *self.asked.lock().unwrap()
    }

    fn ask(&self) {
        *self.asked.lock().unwrap() += 1;
        let before = self.before.lock().unwrap().take();
        if let Some(before) = before {
            before();
        }
    }
}

impl run_ports::RunLocks for Locks {
    fn hold_run(&self, _: &str) -> Result<run_ports::HeldLock, run_ports::LockError> {
        unreachable!("the launcher takes no run's lock")
    }

    fn probe(&self, path: &str) -> run_ports::LockProbe {
        self.ask();
        if path == RUN_LOCK {
            self.run
        } else {
            run_ports::LockProbe::Unproven
        }
    }

    fn session(&self, project_dir: &str, harp: &str) -> run_ports::LockProbe {
        self.ask();
        if (project_dir, harp) == ("/work/proj", HARP) {
            self.session
        } else {
            run_ports::LockProbe::Unproven
        }
    }
}

use run_ports::LockProbe::{Free, Held, Unproven};

/// Another process's launcher over the same store: it holds no order.
fn another_process(
    edge: &Arc<TestEdge>,
    locks: Arc<Locks>,
) -> Launcher<TestEdge, ScriptedRunner, MemoryRecords> {
    Launcher::new(
        Arc::clone(edge),
        ScriptedRunner::exits(0),
        MemoryRecords::default(),
        vec![Arc::new(ScriptedEngine::answering("A", None))],
        Arc::new(Orders::new()),
        limits(),
    )
    .with_locks(locks)
}

/// The run's ends, as its stream holds them: (cause, detail).
async fn ends(edge: &TestEdge) -> Vec<(EndCause, String)> {
    run_events(edge, SESSION)
        .await
        .into_iter()
        .filter_map(|event| match event {
            RunEvent::RunEnded(ended) => Some((ended.cause, ended.detail)),
            _ => None,
        })
        .collect()
}

/// The end of a run owed a runner whose launcher died (ruling 100): orphaned,
/// never a launch failure.
fn orphaned() -> (EndCause, String) {
    (EndCause::LaunchOrphaned, String::new())
}

/// The end of a started run whose owner is gone (ruling 95): runner loss.
fn lost() -> (EndCause, String) {
    (EndCause::RunnerLoss, String::new())
}

/// The incarnations the session's stream records as ended.
async fn closed(edge: &TestEdge) -> Vec<(u32, String)> {
    session_events(edge, SESSION)
        .await
        .into_iter()
        .filter_map(|event| match event {
            SessionEvent::IncarnationEnded(ended) => Some((ended.incarnation, ended.cause)),
            _ => None,
        })
        .collect()
}

/// Record `fact` on the run of `SESSION`, as its runner would.
async fn as_the_runner(edge: &Arc<TestEdge>, fact: runs_application::RunnerFact) {
    let session = uuid::Uuid::parse_str(SESSION).unwrap();
    let who = runs_application::Identity {
        run: runs::run_root(SESSION, 1),
        session: angzarr_embedded::v1::Uuid::from_bytes(*session.as_bytes()),
        incarnation: 1,
        harp: HARP.to_string(),
        depth: 0,
    };
    runs_application::Facts::new(Arc::clone(edge))
        .record(&who, fact)
        .await
        .unwrap();
}

/// The run of `SESSION`, enqueued by a launcher holding `RUN_LOCK`, and its
/// runner started.
async fn started_run(edge: &Arc<TestEdge>) {
    enqueued_by(edge, SESSION, HARP, RUN_LOCK).await;
    as_the_runner(
        edge,
        runs_application::RunnerFact::RunnerStarted(runs::RunnerStarted {
            runtime: "fake".to_string(),
            workspace: "/work/proj".to_string(),
            pid: 7,
        }),
    )
    .await;
}

// Rulings 86, 92, 100: a run owed a runner whose own lock is free has lost
// its launcher, and nobody else holds its order: the next process to catch up
// records `LaunchOrphaned` (no launch failed), which ends it and closes its
// session's incarnation (ruling 85). Nothing is launched for it.
#[tokio::test]
async fn a_run_whose_launchers_lock_is_free_is_recorded_as_an_orphan() {
    let edge = Arc::new(TestEdge::new());
    enqueued_by(&edge, SESSION, HARP, RUN_LOCK).await;
    let mut later = another_process(&edge, Locks::saying(Free, Unproven));
    let launched = later.step(&edge.store, edge.catalog()).await.unwrap();
    assert!(launched.is_empty(), "an orphan is not launched");
    // Not `LaunchFailed`: no orphan claims a launch failure.
    assert_eq!(ends(&edge).await, [orphaned()]);
    assert_eq!(closed(&edge).await, [(1, "orphaned".to_string())]);
}

// Ruling 92: a lock still held is its launcher's, and one that proves nothing
// is never read as free: the run is left as it is.
#[tokio::test]
async fn a_run_whose_launchers_lock_is_held_or_unproven_is_left() {
    for probe in [Held, Unproven] {
        let edge = Arc::new(TestEdge::new());
        enqueued_by(&edge, SESSION, HARP, RUN_LOCK).await;
        let locks = Locks::saying(probe, Unproven);
        let mut later = another_process(&edge, Arc::clone(&locks));
        later.step(&edge.store, edge.catalog()).await.unwrap();
        assert!(ends(&edge).await.is_empty(), "{probe:?}");
        assert!(locks.asked() > 0, "{probe:?}: it was asked");
    }
}

// Ruling 92: a run enqueued before its lock was journaled names none, and is
// never judged an orphan.
#[tokio::test]
async fn a_run_that_names_no_lock_is_never_an_orphan() {
    let edge = Arc::new(TestEdge::new());
    enqueued(&edge, SESSION, HARP).await;
    let mut later = another_process(&edge, Locks::saying(Free, Free));
    later.step(&edge.store, edge.catalog()).await.unwrap();
    assert!(ends(&edge).await.is_empty());
}

// Ruling 92, the race forced: two processes find the same orphan, the second
// catching up in the gap between the first one's probe and its record. The
// orphan is recorded ONCE (the fact is keyed by the run, so the second is a
// DUPLICATE), and neither process fails for it.
#[tokio::test]
async fn two_processes_that_find_the_same_orphan_record_it_once() {
    let edge = Arc::new(TestEdge::new());
    enqueued_by(&edge, SESSION, HARP, RUN_LOCK).await;
    let second = Arc::new(Mutex::new(another_process(
        &edge,
        Locks::saying(Free, Unproven),
    )));
    let first_locks = Locks::saying(Free, Unproven);
    let (racer, racing_edge) = (Arc::clone(&second), Arc::clone(&edge));
    *first_locks.before.lock().unwrap() = Some(Box::new(move || {
        let mut second = racer.lock().unwrap();
        now(second.step(&racing_edge.store, racing_edge.catalog())).unwrap();
    }));
    let mut first = another_process(&edge, first_locks);
    first.step(&edge.store, edge.catalog()).await.unwrap();
    assert_eq!(ends(&edge).await, [orphaned()], "ended once");
    assert_eq!(closed(&edge).await.len(), 1, "closed once");
}

// Ruling 92: a run this process holds the order for is its own, launched as
// ever, never judged by its lock.
#[tokio::test]
async fn a_run_whose_order_this_process_holds_is_launched_not_orphaned() {
    let edge = Arc::new(TestEdge::new());
    enqueued_by(&edge, SESSION, HARP, RUN_LOCK).await;
    let runner = ScriptedRunner::exits(0);
    let mut owner = Launcher::new(
        Arc::clone(&edge),
        runner.clone(),
        MemoryRecords::default(),
        vec![Arc::new(ScriptedEngine::answering("A", None))],
        orders_with(order("fake")),
        limits(),
    )
    .with_locks(Locks::saying(Free, Free));
    let launched = owner.step(&edge.store, edge.catalog()).await.unwrap();
    assert_eq!(launched.len(), 1);
    assert_eq!(runner.seen.lock().unwrap().len(), 1);
    assert!(!ends(&edge).await.contains(&orphaned()));
}

// Rulings 93, 95: a STARTED run whose session's lock is free has lost its
// owner (the run holds its session's lock from its mint to its end): it is
// ended by `RunnerLost`, for runner loss, and its incarnation closed.
#[tokio::test]
async fn a_started_run_whose_sessions_lock_is_free_is_ended_for_runner_loss() {
    let edge = Arc::new(TestEdge::new());
    started_run(&edge).await;
    let mut later = another_process(&edge, Locks::saying(Unproven, Free));
    later.step(&edge.store, edge.catalog()).await.unwrap();
    assert_eq!(ends(&edge).await, [lost()]);
    assert_eq!(closed(&edge).await, [(1, "orphaned".to_string())]);
}

// Ruling 95, the race forced: two processes find the same started orphan,
// the second catching up in the gap between the first one's probe and its
// record. `RunnerLost` is keyed by the run, so the second is a DUPLICATE: the
// run ends once, its incarnation is closed once, and neither fails.
#[tokio::test]
async fn two_processes_that_find_the_same_started_orphan_end_it_once() {
    let edge = Arc::new(TestEdge::new());
    started_run(&edge).await;
    let second = Arc::new(Mutex::new(another_process(
        &edge,
        Locks::saying(Unproven, Free),
    )));
    let first_locks = Locks::saying(Unproven, Free);
    let (racer, racing_edge) = (Arc::clone(&second), Arc::clone(&edge));
    *first_locks.before.lock().unwrap() = Some(Box::new(move || {
        let mut second = racer.lock().unwrap();
        now(second.step(&racing_edge.store, racing_edge.catalog())).unwrap();
    }));
    let mut first = another_process(&edge, first_locks);
    first.step(&edge.store, edge.catalog()).await.unwrap();
    assert_eq!(ends(&edge).await, [lost()], "ended once");
    assert_eq!(closed(&edge).await.len(), 1, "closed once");
}

// Ruling 93: a session lock that is held (its owner runs) or proves nothing
// leaves the started run as it is.
#[tokio::test]
async fn a_started_run_whose_sessions_lock_is_held_or_unproven_is_left() {
    for probe in [Held, Unproven] {
        let edge = Arc::new(TestEdge::new());
        started_run(&edge).await;
        let locks = Locks::saying(Unproven, probe);
        let mut later = another_process(&edge, Arc::clone(&locks));
        later.step(&edge.store, edge.catalog()).await.unwrap();
        assert!(ends(&edge).await.is_empty(), "{probe:?}");
        assert!(locks.asked() > 0, "{probe:?}: it was asked");
    }
}

// Ruling 93, the race with a normal end forced: the sweeper caught up while
// the run was started; in the gap before it records, the owner ends the run
// as usual (its exit, its incarnation closed) and lets its session lock go,
// so the sweeper's probe finds it free. The run ends ONCE, by its exit; the
// incarnation is closed once, by its owner.
#[tokio::test]
async fn a_sweep_racing_a_normal_end_ends_the_run_once_by_its_exit() {
    let edge = Arc::new(TestEdge::new());
    started_run(&edge).await;
    let locks = Locks::saying(Unproven, Free);
    let owner_edge = Arc::clone(&edge);
    *locks.before.lock().unwrap() = Some(Box::new(move || {
        now(as_the_runner(
            &owner_edge,
            runs_application::RunnerFact::RunExited(runs::RunExited { code: 0, signal: 0 }),
        ));
        now(
            runs_application::Facts::new(Arc::clone(&owner_edge)).incarnation_ended(
                SESSION,
                1,
                "runner-exit",
            ),
        )
        .unwrap();
    }));
    let mut sweeper = another_process(&edge, locks);
    sweeper.step(&edge.store, edge.catalog()).await.unwrap();
    let ended = ends(&edge).await;
    assert_eq!(ended.len(), 1, "{ended:?}");
    assert_eq!(ended[0].0, EndCause::RunnerExit);
    assert_eq!(closed(&edge).await, [(1, "runner-exit".to_string())]);
}

// ------------------------------------- the run lock's release (ruling 92)

/// A run lock held in the orders for `SESSION`'s first run: the flag says
/// when it is let go.
fn lock_held_in(orders: &Orders) -> Arc<std::sync::atomic::AtomicBool> {
    struct Guard(Arc<std::sync::atomic::AtomicBool>);
    impl Drop for Guard {
        fn drop(&mut self) {
            self.0.store(true, std::sync::atomic::Ordering::SeqCst);
        }
    }
    let gone = Arc::new(std::sync::atomic::AtomicBool::new(false));
    orders.hold(
        SESSION,
        1,
        run_ports::HeldLock::new(RUN_LOCK.to_string(), Guard(Arc::clone(&gone))),
    );
    gone
}

// Ruling 92: the run's own lock is held until its runner is started, then let
// go while the command still runs (the session's lock covers it from then
// on): a turn's and the terminal's alike.
#[tokio::test]
async fn the_run_lock_is_let_go_once_the_runner_is_started() {
    for interactive in [false, true] {
        let mut asked = order("fake");
        asked.interactive = interactive;
        let orders = orders_with(asked);
        let gone = lock_held_in(&orders);
        let runner = ScriptedRunner::exits(0);
        let seen: Arc<Mutex<Vec<(&str, bool)>>> = Arc::default();
        for (window, when) in [(&runner.at_spawn, "spawn"), (&runner.during, "running")] {
            let (seen, gone) = (Arc::clone(&seen), Arc::clone(&gone));
            *window.lock().unwrap() = Some(Arc::new(move || {
                let let_go = gone.load(std::sync::atomic::Ordering::SeqCst);
                seen.lock().unwrap().push((when, let_go));
            }));
        }
        let edge = Arc::new(TestEdge::new());
        let mut launcher = Launcher::new(
            Arc::clone(&edge),
            runner,
            MemoryRecords::default(),
            vec![Arc::new(ScriptedEngine::answering("A", None))],
            orders,
            limits(),
        );
        enqueued_by(&edge, SESSION, HARP, RUN_LOCK).await;
        launcher.step(&edge.store, edge.catalog()).await.unwrap();
        let seen = seen.lock().unwrap().clone();
        assert!(
            seen.contains(&("spawn", false)) && seen.contains(&("running", true)),
            "interactive={interactive}: held at the spawn, let go while it runs: {seen:?}"
        );
    }
}

// Ruling 92: a launch that never started lets its run lock go too.
#[tokio::test]
async fn a_launch_that_never_started_lets_its_run_lock_go() {
    for interactive in [false, true] {
        let mut asked = order("fake");
        asked.interactive = interactive;
        let orders = orders_with(asked);
        let gone = lock_held_in(&orders);
        let edge = Arc::new(TestEdge::new());
        let mut launcher = Launcher::new(
            Arc::clone(&edge),
            ScriptedRunner::answering(Launch::NotStarted(LaunchFailure {
                deterministic: true,
                detail: "no program".to_string(),
            })),
            MemoryRecords::default(),
            vec![Arc::new(ScriptedEngine::answering("A", None))],
            orders,
            limits(),
        );
        enqueued_by(&edge, SESSION, HARP, RUN_LOCK).await;
        launcher.step(&edge.store, edge.catalog()).await.unwrap();
        assert!(
            gone.load(std::sync::atomic::Ordering::SeqCst),
            "interactive={interactive}"
        );
    }
}

// Rulings 92, 93, the race the re-read is for, forced: a sweeper caught up
// while the run was owed a runner; in the gap, its owner records the runner's
// start and lets the run's lock go, so the probe finds it free. The re-read
// sees the start: the sweeper records nothing, and the run, started, is not
// ended.
#[tokio::test]
async fn a_sweep_that_finds_the_runner_started_since_records_nothing() {
    let edge = Arc::new(TestEdge::new());
    enqueued_by(&edge, SESSION, HARP, RUN_LOCK).await;
    let locks = Locks::saying(Free, Unproven);
    let owner_edge = Arc::clone(&edge);
    *locks.before.lock().unwrap() = Some(Box::new(move || {
        now(as_the_runner(
            &owner_edge,
            runs_application::RunnerFact::RunnerStarted(runs::RunnerStarted {
                runtime: "fake".to_string(),
                workspace: "/work/proj".to_string(),
                pid: 7,
            }),
        ));
    }));
    let mut sweeper = another_process(&edge, locks);
    sweeper.step(&edge.store, edge.catalog()).await.unwrap();
    assert!(ends(&edge).await.is_empty(), "the started run is not ended");
    assert!(closed(&edge).await.is_empty());
}

// T3, T18: a STARTED run whose own lock is free has lost the host that held
// it for the run's life (a killed daemon's child: no session lock, its run
// lock left free): it is ended by `RunnerLost`, and its incarnation closed.
#[tokio::test]
async fn a_started_run_whose_own_lock_is_free_is_ended_for_runner_loss() {
    let edge = Arc::new(TestEdge::new());
    started_run(&edge).await;
    let mut later = another_process(&edge, Locks::saying(Free, Unproven));
    later.step(&edge.store, edge.catalog()).await.unwrap();
    assert_eq!(ends(&edge).await, [lost()]);
    assert_eq!(closed(&edge).await, [(1, "orphaned".to_string())]);
}

// T18: the sweep is the daemon's first step too, on its own: it answers the
// runs it ended, and a second sweep of the same runs ends nothing again.
#[tokio::test]
async fn the_sweep_answers_the_runs_it_ended_and_a_second_ends_none() {
    let edge = Arc::new(TestEdge::new());
    started_run(&edge).await;
    let mut pending = pending_launches::catch_up();
    pending.step(&edge.store, edge.catalog()).await.unwrap();
    let (owed, started) = {
        let model = pending.projector().pending();
        (model.launches(), model.started())
    };
    let sweeper = crate::Sweeper::new(Arc::clone(&edge), Locks::saying(Free, Unproven));
    let swept = sweeper.sweep(&owed, &started).await.unwrap();
    assert_eq!(
        swept,
        [crate::Swept {
            run: runs::run_root(SESSION, 1),
            session: SESSION.to_string(),
            incarnation: 1,
            started: true,
        }]
    );
    assert!(sweeper.sweep(&owed, &started).await.unwrap().is_empty());
    assert_eq!(ends(&edge).await, [lost()], "ended once");
}

/// An engine that tells its command the user's home it was asked with (as
/// `FAKE_USER_HOME`), and is otherwise `0`.
struct TellsUserHome(ScriptedEngine);

impl run_ports::Engine for TellsUserHome {
    fn name(&self) -> &str {
        self.0.name()
    }
    fn prepare(
        &self,
        request: &run_ports::PrepareRequest,
    ) -> Result<run_ports::Prepared, run_ports::EngineError> {
        self.0.prepare(request)
    }
    fn drive_turn(
        &self,
        request: &run_ports::TurnRequest,
    ) -> Result<run_ports::TurnCommand, run_ports::EngineError> {
        self.0.drive_turn(request).map(|mut command| {
            if let Some(home) = &request.user_home {
                command
                    .env
                    .insert("FAKE_USER_HOME".to_string(), home.clone());
            }
            command
        })
    }
    fn finish_turn(&self, output: &run_ports::TurnOutput) -> TurnOutcome {
        self.0.finish_turn(output)
    }
    fn drive_interactive(
        &self,
        request: &run_ports::TurnRequest,
    ) -> Result<run_ports::TurnCommand, run_ports::EngineError> {
        self.0.drive_interactive(request)
    }
}

// The user's home the composition root resolved (USERPROFILE on Windows)
// reaches the engine from the order as it was placed: a `session_home: host`
// engine is given it, never the environment's HOME.
#[tokio::test]
async fn the_users_home_reaches_the_engine_as_the_order_placed_it() {
    let mut placed = order("fake");
    placed.user_home = Some(r"C:\Users\her".to_string());
    let mut rig = rig(
        TellsUserHome(ScriptedEngine::answering("A", None)),
        ScriptedRunner::exits(0),
        orders_with(placed),
    );
    enqueued(&rig.edge, SESSION, HARP).await;
    rig.step().await;
    let command = rig.runner.seen.lock().unwrap()[0].clone();
    assert_eq!(
        command.env.get("FAKE_USER_HOME").map(String::as_str),
        Some(r"C:\Users\her")
    );
}

// ------------------------------------------------------------ the workspace

/// A rig whose launcher places worktrees through `workspaces`, for one order.
fn rig_placing(order: crate::Order, workspaces: Arc<crate::fakes::ScriptedWorkspaces>) -> Rig {
    let mut rig = rig(
        ScriptedEngine::answering("A", None),
        ScriptedRunner::exits(0),
        orders_with(order),
    );
    rig.launcher = rig.launcher.with_workspaces(workspaces);
    rig
}

/// The `RunnerStarted` the launch recorded: where it said the runner works.
async fn started_in(edge: &Arc<TestEdge>) -> String {
    run_events(edge, SESSION)
        .await
        .into_iter()
        .find_map(|e| match e {
            RunEvent::RunnerStartedRecorded(s) => Some(s.workspace),
            _ => None,
        })
        .expect("a runner started")
}

// j002200 "The same run lands in the workspace its axis dictates": the
// engine is placed in the checkout, as every launch's setup is, in the
// launcher's prepare: its home is prepared for it, its command runs in it,
// and its start says so.
#[tokio::test]
async fn a_worktree_run_is_prepared_driven_and_recorded_in_its_checkout() {
    use crate::fakes::{worktree_checkout, worktree_order, ScriptedWorkspaces, WORK_DIR};
    let workspaces = Arc::new(ScriptedWorkspaces::new(worktree_checkout));
    let mut rig = rig_placing(worktree_order("fake", "reviewer"), Arc::clone(&workspaces));
    enqueued(&rig.edge, SESSION, HARP).await;
    let launched = rig.step().await;
    let path = format!("{WORK_DIR}/reviewer");
    assert_eq!(
        rig.runner.seen.lock().unwrap()[0].cwd.as_deref(),
        Some(path.as_str())
    );
    assert_eq!(started_in(&rig.edge).await, path);
    assert_eq!(launched[0].checkout.as_ref().unwrap().path, path);
    assert_eq!(launched[0].workspace_warning, None);
    let asked = workspaces.asked.lock().unwrap();
    assert_eq!(asked.len(), 1);
    assert_eq!(asked[0].project_dir, "/work/proj");
    assert_eq!(asked[0].work_dir, WORK_DIR);
    assert_eq!(asked[0].agent, "reviewer");
}

// "Asking for a worktree outside git runs in the live project directory
// instead of refusing", and says why.
#[tokio::test]
async fn a_worktree_that_cannot_be_had_runs_in_the_project_with_a_warning() {
    use crate::fakes::{worktree_order, worktree_unavailable, ScriptedWorkspaces};
    let workspaces = Arc::new(ScriptedWorkspaces::new(worktree_unavailable));
    let mut rig = rig_placing(worktree_order("fake", "reviewer"), workspaces);
    enqueued(&rig.edge, SESSION, HARP).await;
    let launched = rig.step().await;
    assert_eq!(
        rig.runner.seen.lock().unwrap()[0].cwd.as_deref(),
        Some("/work/proj")
    );
    assert_eq!(started_in(&rig.edge).await, "/work/proj");
    assert_eq!(launched[0].checkout, None);
    assert!(launched[0]
        .workspace_warning
        .as_deref()
        .is_some_and(|w| w.contains("not a git repository")));
}

// A worktree the port refuses (an occupant it will not adopt, a git that
// hung) starts nothing: the launch fails, deterministically.
#[tokio::test]
async fn a_refused_worktree_ends_the_run_as_a_failed_launch() {
    use crate::fakes::{worktree_order, worktree_refused, ScriptedWorkspaces};
    let workspaces = Arc::new(ScriptedWorkspaces::new(worktree_refused));
    let mut rig = rig_placing(worktree_order("fake", "reviewer"), workspaces);
    enqueued(&rig.edge, SESSION, HARP).await;
    let launched = rig.step().await;
    assert!(
        matches!(&launched[0].outcome, Outcome::NotStarted { detail } if detail.contains("run-ports-records-bad-path")),
        "{:?}",
        launched[0].outcome
    );
    assert!(
        rig.runner.seen.lock().unwrap().is_empty(),
        "nothing spawned"
    );
    let events = run_events(&rig.edge, SESSION).await;
    assert!(events.iter().any(|e| matches!(e,
        RunEvent::RunEnded(ended) if ended.cause == EndCause::LaunchFailed)));
}

// A launcher with no workspaces port places a worktree run in the project,
// and says so.
#[tokio::test]
async fn a_worktree_run_with_no_workspaces_port_runs_in_the_project_with_a_warning() {
    let mut rig = rig(
        ScriptedEngine::answering("A", None),
        ScriptedRunner::exits(0),
        orders_with(crate::fakes::worktree_order("fake", "reviewer")),
    );
    enqueued(&rig.edge, SESSION, HARP).await;
    let launched = rig.step().await;
    assert_eq!(
        rig.runner.seen.lock().unwrap()[0].cwd.as_deref(),
        Some("/work/proj")
    );
    assert!(launched[0].workspace_warning.is_some());
}

// The live project asks the port nothing.
#[tokio::test]
async fn a_run_in_the_project_asks_no_workspace() {
    use crate::fakes::{worktree_checkout, ScriptedWorkspaces};
    let workspaces = Arc::new(ScriptedWorkspaces::new(worktree_checkout));
    let mut rig = rig_placing(order("fake"), Arc::clone(&workspaces));
    enqueued(&rig.edge, SESSION, HARP).await;
    let launched = rig.step().await;
    assert!(workspaces.asked.lock().unwrap().is_empty());
    assert_eq!(launched[0].checkout, None);
    assert_eq!(started_in(&rig.edge).await, "/work/proj");
}

// Owner ruling 2026-11-12 (Q2): a checkout is let go once its engine is over:
// removed when clean, through the workspaces port, which removes it
// WIP-safely and says what it left.
#[tokio::test]
async fn a_checkout_is_released_through_the_workspaces_port() {
    use crate::fakes::{worktree_checkout, worktree_order, ScriptedWorkspaces};
    let workspaces = Arc::new(ScriptedWorkspaces::new(worktree_checkout).leaving("it has work"));
    let mut rig = rig_placing(worktree_order("fake", "reviewer"), Arc::clone(&workspaces));
    enqueued(&rig.edge, SESSION, HARP).await;
    let launched = rig.step().await;
    let checkout = launched[0].checkout.clone().unwrap();
    assert!(
        workspaces.removed.lock().unwrap().is_empty(),
        "not at launch"
    );
    assert_eq!(
        rig.launcher.release(&checkout, &[]),
        isolation_ports::Removal::Left("it has work".to_string())
    );
    assert_eq!(
        *workspaces.removed.lock().unwrap(),
        [(checkout.repo_dir.clone(), checkout.path.clone())]
    );
}

// A launcher that makes no worktrees removes none.
#[tokio::test]
async fn a_launcher_with_no_workspaces_leaves_a_checkout() {
    let rig = rig(
        ScriptedEngine::answering("A", None),
        ScriptedRunner::exits(0),
        orders_with(order("fake")),
    );
    let checkout = isolation_ports::Checkout {
        path: "/h/w/reviewer".to_string(),
        branch: "ctxloom/h/reviewer".to_string(),
        repo_dir: "/work/proj".to_string(),
        reused: false,
    };
    assert!(matches!(
        rig.launcher.release(&checkout, &[]),
        isolation_ports::Removal::Left(_)
    ));
}

// A placed engine works outside the project: the launch names the live
// project to it, and so to its hook processes and the agent's own `ctxloom`
// calls, as CTXLOOM_ROOT, over any the caller had, and no permit replaces it.
#[tokio::test]
async fn the_engine_is_told_the_live_project_whatever_its_workspace() {
    use crate::fakes::{worktree_checkout, worktree_order, ScriptedWorkspaces};
    let mut order = worktree_order("fake", "reviewer");
    order.env.insert(
        run_ports::ROOT_VARIABLE.to_string(),
        "/elsewhere".to_string(),
    );
    order.env.insert(
        "CTXLOOM_PERMIT_CTXLOOM_ROOT".to_string(),
        "/permitted".to_string(),
    );
    let workspaces = Arc::new(ScriptedWorkspaces::new(worktree_checkout));
    let mut rig = rig_placing(order, workspaces);
    enqueued(&rig.edge, SESSION, HARP).await;
    rig.step().await;
    let seen = rig.runner.seen.lock().unwrap();
    assert_eq!(
        seen[0]
            .env
            .get(run_ports::ROOT_VARIABLE)
            .map(String::as_str),
        Some("/work/proj")
    );
    assert_ne!(seen[0].cwd.as_deref(), Some("/work/proj"), "placed");
}

// An order with no project names none.
#[tokio::test]
async fn an_order_with_no_project_names_no_root() {
    let mut order = order("fake");
    order.cwd = None;
    let mut rig = rig(
        ScriptedEngine::answering("A", None),
        ScriptedRunner::exits(0),
        orders_with(order),
    );
    enqueued(&rig.edge, SESSION, HARP).await;
    rig.step().await;
    assert!(!rig.runner.seen.lock().unwrap()[0]
        .env
        .contains_key(run_ports::ROOT_VARIABLE));
}

// The trust entry (plan §6): an engine that keys its state by the directory
// it runs in (claude's `.claude.json`, `projects.<cwd>.hasTrustDialogAccepted`,
// which `seeded_state` writes from `PrepareRequest.cwd`) is prepared with the
// checkout's path as the workspaces port gave it, which `GitWorkspaces`
// resolves (macOS's `/private/var`), so the checkout is the directory
// trusted, not the project.
#[tokio::test]
async fn the_engines_state_is_keyed_by_the_checkout_it_is_placed_in() {
    use crate::fakes::{worktree_checkout, worktree_order, Merging, ScriptedWorkspaces, WORK_DIR};
    let mut rig = rig(
        Merging(ScriptedEngine::answering("A", None)),
        ScriptedRunner::exits(0),
        orders_with(worktree_order("fake", "reviewer")),
    );
    rig.launcher = rig
        .launcher
        .with_workspaces(Arc::new(ScriptedWorkspaces::new(worktree_checkout)));
    enqueued(&rig.edge, SESSION, HARP).await;
    rig.step().await;
    assert_eq!(
        rig.records.text(HARP, "home/state.json"),
        Some(format!("+{WORK_DIR}/reviewer"))
    );
}

// Owner ruling 2026-11-12: what the run's selection carries rides its launch,
// handed to the engine's prepare whatever its workspace.
#[tokio::test]
async fn the_engine_is_prepared_with_the_launchs_surfaces() {
    let engine = ScriptedEngine::answering("A", None);
    let prepared = Arc::clone(&engine.prepared);
    let mut order = order("fake");
    order.surfaces = run_ports::LaunchSurfaces(surface_ports::Inputs {
        deny_tools: vec!["Bash".to_string()],
        statusline: true,
        ..Default::default()
    });
    let mut rig = rig(engine, ScriptedRunner::exits(0), orders_with(order.clone()));
    enqueued(&rig.edge, SESSION, HARP).await;
    rig.step().await;
    let prepared = prepared.lock().unwrap();
    assert_eq!(prepared.len(), 1);
    assert_eq!(prepared[0].surfaces, order.surfaces);
}

// Owner ruling 2026-11-12 (Q3): what a launch carries that its engine reads
// only from where it works is written into the session's checkout, hidden
// from git there, and taken out at its release, before the checkout goes.
#[tokio::test]
async fn files_the_engine_reads_where_it_works_go_into_its_checkout_and_out_at_release() {
    use crate::fakes::{
        worktree_checkout, worktree_order, Placing, ScriptedWorkspaces, PLACED, WORK_DIR,
    };
    let workspaces = Arc::new(ScriptedWorkspaces::new(worktree_checkout));
    let mut rig = rig(
        Placing(ScriptedEngine::answering("A", None)),
        ScriptedRunner::exits(0),
        orders_with(worktree_order("fake", "reviewer")),
    );
    rig.launcher = rig.launcher.with_workspaces(Arc::clone(&workspaces) as _);
    enqueued(&rig.edge, SESSION, HARP).await;
    let launched = rig.step().await;
    let path = format!("{WORK_DIR}/reviewer");
    assert_eq!(
        workspaces.asked.lock().unwrap()[0].artifact_patterns,
        [".placed/"]
    );
    assert_eq!(
        *workspaces.files.lock().unwrap(),
        [format!("{path}/{PLACED}")]
    );
    assert_eq!(launched[0].checkout_files, [PLACED]);
    let checkout = launched[0].checkout.clone().unwrap();
    rig.launcher.release(&checkout, &launched[0].checkout_files);
    assert_eq!(
        *workspaces.files.lock().unwrap(),
        [
            format!("{path}/{PLACED}"),
            format!("-{path}/{PLACED}"),
            format!("remove {path}")
        ]
    );
}

// In the live project nothing of it is written: the project is not the
// launch's to write.
#[tokio::test]
async fn files_the_engine_reads_where_it_works_never_go_into_the_live_project() {
    use crate::fakes::{worktree_order, worktree_unavailable, Placing, ScriptedWorkspaces};
    let workspaces = Arc::new(ScriptedWorkspaces::new(worktree_unavailable));
    let mut rig = rig(
        Placing(ScriptedEngine::answering("A", None)),
        ScriptedRunner::exits(0),
        orders_with(worktree_order("fake", "reviewer")),
    );
    rig.launcher = rig.launcher.with_workspaces(Arc::clone(&workspaces) as _);
    enqueued(&rig.edge, SESSION, HARP).await;
    let launched = rig.step().await;
    assert!(workspaces.files.lock().unwrap().is_empty());
    assert!(launched[0].checkout_files.is_empty());
}

// Owner, 2026-11-12 (Windows): std's spawn searches PATH for `<name>.exe`
// alone, so the launch finds the program itself, on the command's own PATH,
// and spawns the file found: a child's turn and the terminal's alike.
#[tokio::test]
async fn the_program_found_is_the_file_spawned_for_a_turn_and_for_the_terminal() {
    for interactive in [false, true] {
        let mut ordered = order("fake");
        ordered.interactive = interactive;
        let mut rig = rig(
            ScriptedEngine::answering("A", None),
            ScriptedRunner::exits(0).finding(Ok(r"C:\npm\claude.cmd".to_string())),
            orders_with(ordered),
        );
        enqueued(&rig.edge, SESSION, HARP).await;
        rig.step().await;
        let seen = if interactive {
            rig.runner.seen_terminal.lock().unwrap().clone()
        } else {
            rig.runner.seen.lock().unwrap().clone()
        };
        assert_eq!(seen.len(), 1, "interactive {interactive}");
        assert_eq!(seen[0].program, r"C:\npm\claude.cmd");
    }
}

// A program found on no directory of the PATH is not started, in the words
// that say what was looked for and where; nothing is spawned.
#[tokio::test]
async fn a_program_found_nowhere_is_not_started_naming_what_was_searched() {
    let missing = run_ports::ProgramNotFound {
        program: "claude".to_string(),
        names: vec!["claude.exe".to_string(), "claude.cmd".to_string()],
        dirs: vec![r"C:\bin".to_string()],
    };
    let mut rig = rig(
        ScriptedEngine::answering("A", None),
        ScriptedRunner::exits(0).finding(Err(missing)),
        orders_with(order("fake")),
    );
    enqueued(&rig.edge, SESSION, HARP).await;
    let launched = rig.step().await;
    assert!(
        matches!(&launched[0].outcome, Outcome::ProgramNotFound(missing)
            if missing.names == ["claude.exe", "claude.cmd"] && missing.dirs == [r"C:\bin"]),
        "{launched:?}"
    );
    assert_eq!(
        names(&run_events(&rig.edge, SESSION).await),
        ["RunEnqueued", "RunEnded"]
    );
    assert!(rig.runner.seen.lock().unwrap().is_empty());
}

// std refuses a batch file an argument of more than one line ("batch file
// arguments are invalid"): on Windows the terminal's multi-line opening
// prompt to a `claude.cmd` is refused, typed, before the spawn. One line, an
// executable, or another OS is spawned.
#[tokio::test]
async fn a_batch_files_multi_line_prompt_is_refused_before_the_spawn_on_windows() {
    let launch = |os: &str, prompt: &str, found: &str| {
        let mut interactive = order("fake");
        interactive.interactive = true;
        interactive.prompt = prompt.to_string();
        let mut rig = rig(
            ScriptedEngine::answering("A", None),
            ScriptedRunner::exits(0).finding(Ok(found.to_string())),
            orders_with(interactive),
        );
        rig.launcher = rig.launcher.on_os(os);
        rig
    };
    let mut refused = launch("windows", "one\ntwo", r"C:\npm\claude.cmd");
    enqueued(&refused.edge, SESSION, HARP).await;
    let launched = refused.step().await;
    assert!(
        matches!(&launched[0].outcome, Outcome::BatchArguments(refusal)
            if refusal.program == r"C:\npm\claude.cmd"),
        "{launched:?}"
    );
    assert!(refused.runner.seen_terminal.lock().unwrap().is_empty());
    assert_eq!(
        names(&run_events(&refused.edge, SESSION).await),
        ["RunEnqueued", "RunEnded"]
    );

    for (os, prompt, found) in [
        ("windows", "one line", r"C:\npm\claude.cmd"),
        ("windows", "one\ntwo", r"C:\bin\claude.exe"),
        ("linux", "one\ntwo", "/bin/claude.cmd"),
    ] {
        let mut spawned = launch(os, prompt, found);
        enqueued(&spawned.edge, SESSION, HARP).await;
        let launched = spawned.step().await;
        assert!(
            matches!(launched[0].outcome, Outcome::Ended { .. }),
            "{os} {found}: {launched:?}"
        );
    }
}

// Ruling 168's seam at the launch: the launcher asks the engine of the name,
// and an engine it cannot drive says nothing.
#[test]
fn the_launcher_asks_the_named_engine_for_its_advice() {
    let engine = ScriptedEngine {
        advises: true,
        ..ScriptedEngine::answering("A", None)
    };
    let advised = Arc::clone(&engine.advised);
    let rig = rig(engine, ScriptedRunner::exits(0), orders_with(order("fake")));
    let planned = run_ports::Planned {
        os: "windows".to_string(),
        interactive: true,
        ..run_ports::Planned::default()
    };
    assert_eq!(rig.launcher.advise("fake", &planned).len(), 1);
    assert_eq!(*advised.lock().unwrap(), vec![planned.clone()]);
    assert!(rig.launcher.advise("other", &planned).is_empty());

    let quiet = rig_quiet();
    assert!(quiet.launcher.advise("fake", &planned).is_empty());
}

fn rig_quiet() -> Rig {
    rig(
        ScriptedEngine::answering("A", None),
        ScriptedRunner::exits(0),
        orders_with(order("fake")),
    )
}
