//! Hook capture's composition over the real kernel: the in-process sink's
//! mapping of the edge's answers, and a hook's recording to the session its
//! harp names.

use std::collections::BTreeMap;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use angzarr_embedded::testing::{Fault, FaultyStore, MemoryStore};
use angzarr_embedded::v1::{Cover, EventPage, Uuid};
use angzarr_embedded::{Answered, CommandBookExt, Failure, Rejected, StoreError};
use embedded_edge::EmbeddedEdge;
use hook_cli::Recording;
use run_ports::{Arrival, Delivery, Edge, FactSink, RecordsError, SessionRecords, Unserved};
use runs_application::observe;
use setup_application::HookAnswer;
use surface_ports::{HookEvent, ToolCall};

use super::*;
use crate::InProcessCore;

const SESSION: &str = "5f0c7a52-1111-5111-8111-000000000001";
const HARP: &str = "amber-swift-owl";

type Store = FaultyStore<MemoryStore>;
type TheEdge = EmbeddedEdge<angzarr_embedded_host::stores::OneStore<Store>>;

fn session_id() -> Uuid {
    Uuid::from_bytes(*uuid::Uuid::parse_str(SESSION).unwrap().as_bytes())
}

fn session_cover() -> Cover {
    Cover {
        domain: sessions::SESSION_DOMAIN.to_string(),
        root: Some(session_id()),
        edition: None,
    }
}

/// A kernel with the session minted.
async fn world() -> (Store, Arc<TheEdge>) {
    let store = Store::default();
    let core = InProcessCore::start(store.clone()).await;
    let edge = core.edge();
    let mint = sessions::MintSession {
        harp: HARP.to_string(),
        project_id: "calm-otter".to_string(),
        project_dir: "/w".to_string(),
        output_dir: "/o".to_string(),
        engine: "claude-code".to_string(),
        origin: sessions::Origin::Session,
        started_at: None,
        depth: 0,
    };
    edge.handle(angzarr_embedded::v1::CommandBook::command_of(
        session_cover(),
        &sessions::SessionCommand::from(mint),
    ))
    .await
    .unwrap();
    (store, edge)
}

fn prompt(id: &str) -> HookEvent {
    HookEvent {
        event: "turn_start".to_string(),
        prompt_id: id.to_string(),
        prompt: "echo hi".to_string(),
        ..HookEvent::default()
    }
}

fn book_of(event: &HookEvent) -> angzarr_embedded::v1::CommandBook {
    observe(event)[0].book(&session_id())
}

async fn pages(edge: &TheEdge) -> Vec<EventPage> {
    edge.events(&session_cover(), 0).await.unwrap()
}

// ------------------------------------------------------------------ EdgeSink

#[tokio::test]
async fn the_in_process_sink_applies_a_fact_and_answers_its_redelivery_duplicate() {
    let (_, edge) = world().await;
    let sink = EdgeSink::new(Arc::clone(&edge));
    let before = pages(&edge).await.len();
    let book = book_of(&prompt("p1"));
    // Delivered from inside a runtime, as the launcher would.
    assert_eq!(
        sink.deliver(book.clone(), Duration::from_secs(5)),
        Delivery::Applied
    );
    assert_eq!(pages(&edge).await.len(), before + 1);
    assert_eq!(
        sink.deliver(book, Duration::from_secs(5)),
        Delivery::Duplicate
    );
    assert_eq!(pages(&edge).await.len(), before + 1);
}

#[tokio::test]
async fn a_domain_the_edge_does_not_serve_is_refused_not_unavailable() {
    let (_, edge) = world().await;
    let sink = EdgeSink::new(edge);
    let mut book = book_of(&prompt("p1"));
    book.cover.as_mut().unwrap().domain = "nosuch".to_string();
    assert!(matches!(
        sink.deliver(book, Duration::from_secs(5)),
        Delivery::Refused { .. }
    ));
}

#[tokio::test]
async fn a_fact_that_loses_every_race_is_parked() {
    let (store, edge) = world().await;
    store.fail(std::iter::repeat_n(
        Fault::Conflict,
        crate::core_link::RACE_RUNS.get() as usize,
    ));
    let sink = EdgeSink::new(edge);
    assert_eq!(
        sink.deliver(book_of(&prompt("p1")), Duration::from_secs(5)),
        Delivery::Parked
    );
}

/// An edge that never answers.
struct Silent;

impl Edge for Silent {
    async fn serve(
        &self,
        _: angzarr_embedded::v1::CommandBook,
        _: Arrival,
    ) -> Result<Answered, Unserved> {
        std::future::pending().await
    }

    async fn handle(
        &self,
        _: angzarr_embedded::v1::CommandBook,
    ) -> Result<Answered, Failure<Rejected>> {
        std::future::pending().await
    }

    async fn events(&self, _: &Cover, _: u64) -> Result<Vec<EventPage>, StoreError> {
        Ok(Vec::new())
    }
}

// The race this concerns: nobody answers. The sink gives up at its deadline
// and does not wait for the answer.
#[test]
fn an_edge_that_never_answers_is_timed_out_at_the_deadline() {
    let sink = EdgeSink::new(Arc::new(Silent));
    let started = std::time::Instant::now();
    assert_eq!(
        sink.deliver(book_of(&prompt("p1")), Duration::from_millis(100)),
        Delivery::TimedOut
    );
    assert!(started.elapsed() < Duration::from_secs(5));
}

#[test]
fn the_sink_of_a_hook_process_without_a_listener_is_unavailable() {
    assert!(matches!(
        Unreachable.deliver(book_of(&prompt("p1")), Duration::from_secs(1)),
        Delivery::Unavailable { detail } if detail.contains("listener")
    ));
}

// ------------------------------------------------------------------ DirIds

/// The files of a session directory, by harp and relative path.
type Files = BTreeMap<(String, String), Vec<u8>>;

#[derive(Clone, Default)]
struct Dir(Arc<Mutex<Files>>);

impl SessionRecords for Dir {
    fn dir(&self, harp: &str) -> String {
        format!("/w/.ctxloom/sessions/{harp}")
    }

    fn write(&mut self, harp: &str, rel: &str, bytes: &[u8]) -> Result<(), RecordsError> {
        self.0
            .lock()
            .unwrap()
            .insert((harp.to_string(), rel.to_string()), bytes.to_vec());
        Ok(())
    }

    fn append(&mut self, harp: &str, rel: &str, bytes: &[u8]) -> Result<(), RecordsError> {
        self.0
            .lock()
            .unwrap()
            .entry((harp.to_string(), rel.to_string()))
            .or_default()
            .extend_from_slice(bytes);
        Ok(())
    }

    fn read(&mut self, harp: &str, rel: &str) -> Result<Option<Vec<u8>>, RecordsError> {
        Ok(self
            .0
            .lock()
            .unwrap()
            .get(&(harp.to_string(), rel.to_string()))
            .cloned())
    }
}

fn dir_with_metadata() -> Dir {
    let mut dir = Dir::default();
    dir.write(
        HARP,
        run_launcher::METADATA,
        format!("{{\n  \"v\": 1,\n  \"id\": \"{SESSION}\",\n  \"harp\": \"{HARP}\"\n}}\n")
            .as_bytes(),
    )
    .unwrap();
    dir
}

#[test]
fn a_harp_is_the_session_its_directory_names() {
    let ids = DirIds::new(Some(dir_with_metadata()));
    assert_eq!(ids.session_of(HARP), Some(session_id()));
    assert_eq!(ids.session_of("no-such-harp"), None);
}

#[test]
fn a_directory_that_names_no_usable_id_is_no_session() {
    for held in [
        "",
        "not json",
        r#"{"id": "not-a-uuid"}"#,
        r#"{"harp": "x"}"#,
    ] {
        let mut dir = Dir::default();
        dir.write(HARP, run_launcher::METADATA, held.as_bytes())
            .unwrap();
        assert_eq!(DirIds::new(Some(dir)).session_of(HARP), None, "{held:?}");
    }
    assert_eq!(DirIds::<Dir>::new(None).session_of(HARP), None);
}

// ------------------------------------------------------------- HookRecording

fn answer(event: HookEvent) -> HookAnswer {
    HookAnswer {
        event: Some(event),
        ..HookAnswer::default()
    }
}

fn batch() -> HookEvent {
    HookEvent {
        event: "post_tool_batch".to_string(),
        prompt_id: "p1".to_string(),
        tool_calls: ["a", "b"]
            .iter()
            .map(|id| ToolCall {
                tool: "Bash".to_string(),
                tool_use_id: (*id).to_string(),
                input: "{}".to_string(),
                response: "hi".to_string(),
            })
            .collect(),
        ..HookEvent::default()
    }
}

#[tokio::test]
async fn a_hook_records_its_observations_on_the_session_its_harp_names() {
    let (_, edge) = world().await;
    let recording = HookRecording::new(
        EdgeSink::new(Arc::clone(&edge)),
        DirIds::new(Some(dir_with_metadata())),
    );
    let before = pages(&edge).await.len();
    assert!(recording
        .record("PostToolBatch", HARP, &answer(batch()))
        .is_empty());
    assert_eq!(pages(&edge).await.len(), before + 2);
    // The engine fires it again: recorded once.
    assert!(recording
        .record("PostToolBatch", HARP, &answer(batch()))
        .is_empty());
    assert_eq!(pages(&edge).await.len(), before + 2);
}

#[test]
fn a_hook_with_no_listener_names_each_observation_it_could_not_record() {
    let recording = HookRecording::new(Unreachable, DirIds::new(Some(dir_with_metadata())));
    let missed = recording.record("PostToolBatch", HARP, &answer(batch()));
    let what: Vec<&str> = missed.iter().map(|m| m.what.as_str()).collect();
    assert_eq!(
        what,
        [
            format!("hook:tool:{SESSION}:a"),
            format!("hook:tool:{SESSION}:b")
        ]
    );
    assert!(
        missed[0].why.contains("listener is not running"),
        "{missed:?}"
    );
    assert!(missed[1].why.contains("not tried"), "{missed:?}");
}

#[test]
fn a_harp_with_no_session_directory_records_nothing_and_says_so() {
    let recording = HookRecording::new(Unreachable, DirIds::<Dir>::new(None));
    let missed = recording.record("UserPromptSubmit", HARP, &answer(prompt("p1")));
    assert_eq!(missed.len(), 1);
    assert!(missed[0].why.contains(HARP), "{missed:?}");
}

#[test]
fn an_answer_without_an_event_or_an_observable_one_records_nothing() {
    let recording = HookRecording::new(Unreachable, DirIds::new(Some(dir_with_metadata())));
    assert!(recording
        .record("Stop", HARP, &HookAnswer::default())
        .is_empty());
    let silent = HookEvent {
        event: "pre_tool".to_string(),
        ..HookEvent::default()
    };
    assert!(recording
        .record("PreToolUse", HARP, &answer(silent))
        .is_empty());
}
