//! The recorder: the session's directory, written from the events.

use std::sync::Arc;

use run_ports::Limits;

use crate::fakes::{
    enqueued, order, orders_with, MemoryRecords, ScriptedEngine, ScriptedRunner, TestEdge, HARP,
    SESSION,
};
use crate::{
    LaunchError, Launcher, Recorder, METADATA, RUNS_RECORD, SESSION_RECORD, TRANSCRIPT,
    TRANSCRIPT_POSITION,
};

/// A one-shot that ran to its end, in a store, and the directory it left in
/// `records` once `recorder` has stepped.
async fn ran(recorder: &mut Recorder<MemoryRecords>) -> Arc<TestEdge> {
    ran_with(recorder, ScriptedEngine::answering("A", Some("native-7"))).await
}

/// The same, the engine as scripted.
async fn ran_with(recorder: &mut Recorder<MemoryRecords>, engine: ScriptedEngine) -> Arc<TestEdge> {
    let edge = Arc::new(TestEdge::new());
    enqueued(&edge, SESSION, HARP).await;
    let mut launcher = Launcher::new(
        Arc::clone(&edge),
        ScriptedRunner::exits(0),
        MemoryRecords::default(),
        vec![Arc::new(engine)],
        orders_with(order("fake")),
        Limits::new(std::time::Duration::from_secs(5)),
    );
    launcher.step(&edge.store, edge.catalog()).await.unwrap();
    heard(&edge).await;
    recorder.step(&edge.store, edge.catalog()).await.unwrap();
    edge
}

/// What the child's hooks report of its turn (the launcher records none of
/// it, owner ruling 26): its prompt, and its answer as one final delta.
async fn heard(edge: &Arc<TestEdge>) {
    use runs_application::{Facts, Observation};
    let facts = Facts::new(Arc::clone(edge));
    for observation in [
        Observation::PromptSubmitted(sessions::RecordPromptSubmitted {
            prompt_id: "p1".to_string(),
            prompt: "THE-PROMPT".to_string(),
        }),
        Observation::AssistantText(sessions::RecordAssistantText {
            prompt_id: "p1".to_string(),
            turn_id: "t1".to_string(),
            message_id: "m1".to_string(),
            index: 0,
            is_final: true,
            text: "A".to_string(),
        }),
    ] {
        facts.observe(&who(), &observation).await.unwrap();
    }
}

/// The run's identity, as the facts of this session's turn carry it.
fn who() -> runs_application::Identity {
    runs_application::Identity {
        run: runs::run_root(SESSION, 1),
        session: crate::fakes::uuid(SESSION),
        incarnation: 1,
        harp: HARP.to_string(),
        depth: 0,
        principal: runs_application::Principal::Runner,
    }
}

fn lines(records: &MemoryRecords, rel: &str) -> Vec<serde_json::Value> {
    records
        .text(HARP, rel)
        .unwrap_or_default()
        .lines()
        .map(|l| serde_json::from_str(l).unwrap())
        .collect()
}

#[tokio::test]
async fn a_minted_session_is_its_metadata() {
    let records = MemoryRecords::default();
    let mut recorder = Recorder::new(records.clone());
    recorder.watch(SESSION);
    ran(&mut recorder).await;
    let metadata: serde_json::Value =
        serde_json::from_str(&records.text(HARP, METADATA).unwrap()).unwrap();
    assert_eq!(
        metadata,
        serde_json::json!({
            "v": 1,
            "id": SESSION,
            "harp": HARP,
            "project_id": "calm-otter",
            "project_dir": "/work/proj",
            "output_dir": "/out/amber-swift-owl",
            "engine": "fake",
            "origin": "Oneshot",
        })
    );
}

#[tokio::test]
async fn every_event_of_the_session_and_its_run_is_a_line_of_its_record() {
    let records = MemoryRecords::default();
    let mut recorder = Recorder::new(records.clone());
    recorder.watch(SESSION);
    ran(&mut recorder).await;
    let session: Vec<String> = lines(&records, SESSION_RECORD)
        .iter()
        .map(|l| l["event"].as_str().unwrap().to_string())
        .collect();
    assert_eq!(
        session,
        [
            "SessionMinted",
            "IncarnationOwed",
            "EngineSessionBound",
            "PromptSubmitted",
            "AssistantTextShown"
        ]
    );
    let runs = lines(&records, RUNS_RECORD);
    let names: Vec<&str> = runs.iter().map(|l| l["event"].as_str().unwrap()).collect();
    assert_eq!(
        names,
        [
            "RunEnqueued",
            "RunnerAttachedRecorded",
            "RunnerStartedRecorded",
            "TurnStartedRecorded",
            "TurnCompletedRecorded",
            "RunEnded"
        ]
    );
    assert_eq!(
        (runs[2]["runtime"].as_str(), runs[2]["pid"].as_u64()),
        (Some("fake"), Some(u64::from(crate::fakes::SCRIPTED_PID)))
    );
    assert_eq!(runs[4]["answer_ref"], "transcripts/transcript.jsonl");
    assert_eq!(runs[4]["native_key"], "native-7");
    assert_eq!(runs[5]["cause"], "RunnerExit");
    assert!(runs.iter().all(|l| l["run"].as_str().is_some()));
}

#[tokio::test]
async fn a_credentials_digest_is_in_no_record() {
    let records = MemoryRecords::default();
    let mut recorder = Recorder::new(records.clone());
    recorder.watch(SESSION);
    ran(&mut recorder).await;
    let all: String = records
        .names(HARP)
        .iter()
        .filter_map(|rel| records.text(HARP, rel))
        .collect();
    assert!(!all.contains("credential"), "{all}");
}

#[tokio::test]
async fn a_session_that_is_not_watched_is_not_written() {
    let records = MemoryRecords::default();
    let mut recorder = Recorder::new(records.clone());
    ran(&mut recorder).await;
    assert!(records.names(HARP).is_empty(), "{:?}", records.names(HARP));
}

// At least once: a recorder that starts again from the streams' start (another
// process) hands every book again, and the directory holds each event once.
#[tokio::test]
async fn a_recorder_started_again_writes_each_event_once() {
    let records = MemoryRecords::default();
    let mut first = Recorder::new(records.clone());
    first.watch(SESSION);
    let edge = ran(&mut first).await;
    let (session, runs) = (
        lines(&records, SESSION_RECORD).len(),
        lines(&records, RUNS_RECORD).len(),
    );
    let mut second = Recorder::new(records.clone());
    second.watch(SESSION);
    second.step(&edge.store, edge.catalog()).await.unwrap();
    first.step(&edge.store, edge.catalog()).await.unwrap();
    assert_eq!(lines(&records, SESSION_RECORD).len(), session);
    assert_eq!(lines(&records, RUNS_RECORD).len(), runs);
    assert_eq!(
        lines(&records, TRANSCRIPT).len(),
        2,
        "the transcript holds each line once"
    );
}

#[tokio::test]
async fn a_record_that_cannot_be_written_is_the_error_and_is_tried_again() {
    let records = MemoryRecords::default();
    *records.refuses.lock().unwrap() = Some("records/".to_string());
    let mut recorder = Recorder::new(records.clone());
    recorder.watch(SESSION);
    let edge = Arc::new(TestEdge::new());
    enqueued(&edge, SESSION, HARP).await;
    let error = recorder
        .step(&edge.store, edge.catalog())
        .await
        .unwrap_err();
    assert!(
        matches!(&error, LaunchError::Record { ext_err_txt } if ext_err_txt.contains("permission denied")),
        "{error:?}"
    );
    // The system relents: the same books are handed again, and written.
    *records.refuses.lock().unwrap() = None;
    recorder.step(&edge.store, edge.catalog()).await.unwrap();
    assert_eq!(lines(&records, SESSION_RECORD).len(), 2);
    assert_eq!(lines(&records, RUNS_RECORD).len(), 1);
}

// The projector is named for what it writes.
#[test]
fn the_projector_is_named_session_records() {
    use angzarr_embedded::ProjectorHandler;
    let recorder = Recorder::new(MemoryRecords::default());
    assert_eq!(recorder.catch.projector().name(), "session-records");
}

// Each event's line carries the fields that name what happened, beside its
// name: the session's, and the run's.
#[tokio::test]
async fn an_event_line_carries_what_the_event_says() {
    let records = MemoryRecords::default();
    let mut recorder = Recorder::new(records.clone());
    recorder.watch(SESSION);
    ran(&mut recorder).await;
    let session = lines(&records, SESSION_RECORD);
    assert_eq!(session[0]["harp"], HARP);
    assert_eq!(session[1]["incarnation"], 1);
    assert_eq!(session[2]["native_id"], "native-7");
    assert!(
        session[2].get("transcript_path").is_some(),
        "{}",
        session[2]
    );
    let runs = lines(&records, RUNS_RECORD);
    assert_eq!(runs[0]["session"], SESSION);
    assert_eq!(runs[0]["incarnation"], 1);
    assert_eq!(runs[0]["mode"], "OneShot");
    assert!(runs[1]["runner_id"].is_string() && runs[1]["protocol"].is_string());
    assert_eq!(runs[3]["turn"], 1);
}

#[tokio::test]
async fn a_failed_turns_line_carries_its_class_and_detail() {
    let records = MemoryRecords::default();
    let mut recorder = Recorder::new(records.clone());
    recorder.watch(SESSION);
    let engine = ScriptedEngine {
        outcome: run_ports::TurnOutcome::Failed {
            class: run_ports::FailureClass::RateLimited,
            detail: "slow down".to_string(),
            native_id: None,
        },
        refuses_to_drive: false,
    };
    ran_with(&mut recorder, engine).await;
    let runs = lines(&records, RUNS_RECORD);
    let failed = runs
        .iter()
        .find(|l| l["event"] == "TurnFailedRecorded")
        .expect("a failed turn is a line");
    assert_eq!(failed["turn"], 1);
    assert_eq!(failed["detail"], "slow down");
    assert!(
        failed["class"].as_str().unwrap().contains("RateLimited"),
        "{failed}"
    );
}

// The one writer: the transcript is the projection of the session's capture
// events, in the stream's order, with `seq` counting lines.
#[tokio::test]
async fn the_transcript_is_written_from_the_capture_events_by_the_recorder_alone() {
    let records = MemoryRecords::default();
    let mut recorder = Recorder::new(records.clone());
    recorder.watch(SESSION);
    let edge = ran(&mut recorder).await;
    observe_more(&edge).await;
    recorder.step(&edge.store, edge.catalog()).await.unwrap();
    let transcript = lines(&records, TRANSCRIPT);
    let kinds: Vec<&str> = transcript
        .iter()
        .map(|l| l["kind"].as_str().unwrap())
        .collect();
    assert_eq!(
        kinds,
        [
            "entry",
            "assistant_delta",
            "assistant_delta",
            "tool",
            "turn_end"
        ]
    );
    let seqs: Vec<u64> = transcript
        .iter()
        .map(|l| l["seq"].as_u64().unwrap())
        .collect();
    assert_eq!(seqs, [0, 1, 2, 3, 4]);
    assert_eq!(transcript[2]["delta"]["text"], "more");
    assert!(transcript
        .iter()
        .all(|l| l["engine"] == "fake" && l["session_id"] == SESSION));
}

/// What the engine's hooks would add to the session after the run.
async fn observe_more(edge: &Arc<TestEdge>) {
    use runs_application::{Facts, Observation};
    let who = who();
    let observations = [
        Observation::AssistantText(sessions::RecordAssistantText {
            prompt_id: "p2".to_string(),
            turn_id: "t2".to_string(),
            message_id: "m2".to_string(),
            index: 0,
            is_final: true,
            text: "more".to_string(),
        }),
        Observation::ToolCall(sessions::RecordToolCall {
            prompt_id: "p2".to_string(),
            tool_use_id: "toolu_1".to_string(),
            tool: "Bash".to_string(),
            input: "{}".to_string(),
            response_bytes: 2,
            response_sha256: "00".repeat(32),
        }),
        Observation::TurnEnded(sessions::RecordTurnEnded {
            prompt_id: "p2".to_string(),
        }),
    ];
    let facts = Facts::new(Arc::clone(edge));
    for observation in &observations {
        facts.observe(&who, observation).await.unwrap();
    }
}

// Position: the file the projector owns says how far the stream is written, so
// a transcript with its position is not written again by a projector that
// starts again with nothing in memory (a new process), and a stream that grew
// adds only what is new.
#[tokio::test]
async fn a_projector_started_again_continues_the_transcript_from_its_position() {
    let records = MemoryRecords::default();
    let mut first = Recorder::new(records.clone());
    first.watch(SESSION);
    let edge = ran(&mut first).await;
    let position: serde_json::Value =
        serde_json::from_str(&records.text(HARP, TRANSCRIPT_POSITION).unwrap()).unwrap();
    assert!(position["page"].as_u64().unwrap() >= 3, "{position}");
    observe_more(&edge).await;
    let mut second = Recorder::new(records.clone());
    second.watch(SESSION);
    second.step(&edge.store, edge.catalog()).await.unwrap();
    let seqs: Vec<u64> = lines(&records, TRANSCRIPT)
        .iter()
        .map(|l| l["seq"].as_u64().unwrap())
        .collect();
    assert_eq!(seqs, [0, 1, 2, 3, 4], "each line once, in order, no gap");
}

// A transcript that cannot be written is the error, and the books are handed
// again: the lines then come once, never doubled, never lost.
#[tokio::test]
async fn a_transcript_that_cannot_be_written_is_tried_again_and_written_once() {
    let records = MemoryRecords::default();
    let mut recorder = Recorder::new(records.clone());
    recorder.watch(SESSION);
    let edge = Arc::new(TestEdge::new());
    enqueued(&edge, SESSION, HARP).await;
    let mut launcher = Launcher::new(
        Arc::clone(&edge),
        ScriptedRunner::exits(0),
        MemoryRecords::default(),
        vec![Arc::new(ScriptedEngine::answering("A", None))],
        orders_with(order("fake")),
        Limits::new(std::time::Duration::from_secs(5)),
    );
    launcher.step(&edge.store, edge.catalog()).await.unwrap();
    heard(&edge).await;
    *records.refuses.lock().unwrap() = Some("transcripts/".to_string());
    recorder
        .step(&edge.store, edge.catalog())
        .await
        .unwrap_err();
    assert!(records.text(HARP, TRANSCRIPT).is_none());
    *records.refuses.lock().unwrap() = None;
    recorder.step(&edge.store, edge.catalog()).await.unwrap();
    let seqs: Vec<u64> = lines(&records, TRANSCRIPT)
        .iter()
        .map(|l| l["seq"].as_u64().unwrap())
        .collect();
    assert_eq!(seqs, [0, 1]);
}

// A transcript line's instant is the instant its page was recorded at,
// exactly: each line's `ts` is its own page's, and the pages differ.
#[tokio::test]
async fn a_transcript_lines_instant_is_its_pages_recorded_instant() {
    use angzarr_embedded::v1::event_page::Body;
    use angzarr_embedded::Payload;
    use run_ports::Edge;
    let records = MemoryRecords::default();
    let mut recorder = Recorder::new(records.clone());
    recorder.watch(SESSION);
    let edge = ran(&mut recorder).await;
    let pages = edge
        .events(&crate::fakes::session_cover(SESSION), 0)
        .await
        .unwrap();
    let instants: Vec<String> = pages
        .iter()
        .filter_map(|page| {
            let Some(Body::Event(any)) = &page.body else {
                return None;
            };
            matches!(
                sessions::SessionEvent::from_any(any).unwrap(),
                sessions::SessionEvent::PromptSubmitted(_)
                    | sessions::SessionEvent::AssistantTextShown(_)
            )
            .then(|| {
                let at = page.meta.as_ref().unwrap().recorded_at.as_ref().unwrap();
                crate::rfc3339(at.seconds)
            })
        })
        .collect();
    let written: Vec<String> = lines(&records, TRANSCRIPT)
        .iter()
        .map(|l| l["ts"].as_str().unwrap().to_string())
        .collect();
    assert_eq!(instants.len(), 2);
    assert_ne!(
        instants[0], instants[1],
        "the pages were recorded at two instants"
    );
    assert_eq!(written, instants);
}
