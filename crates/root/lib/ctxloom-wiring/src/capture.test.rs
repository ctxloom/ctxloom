//! Hook capture's composition over the real kernel: the in-process sink's
//! mapping of the edge's answers, and a hook's recording to the session its
//! harp names.

use std::sync::Arc;
use std::time::Duration;

use angzarr_embedded::testing::{Fault, FaultyStore, MemoryStore};
use angzarr_embedded::v1::{Cover, EventPage, Uuid};
use angzarr_embedded::{Answered, CommandBookExt, Failure, Rejected, StoreError};
use embedded_edge::EmbeddedEdge;
use hook_cli::Recording;
use run_ports::{Arrival, Delivery, Edge, FactSink, Unserved};
use runs_application::observe;
use setup_application::HookAnswer;
use surface_ports::{HookEvent, ToolCall};

use super::*;
use crate::InProcessCore;

const SESSION: &str = "5f0c7a52-1111-5111-8111-000000000001";
const HARP: &str = "amber-swift-owl";

type Store = FaultyStore<MemoryStore>;
type TheEdge = EmbeddedEdge<crate::CoreStores<Store>>;

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
                ..ToolCall::default()
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
        RunIds::from_environment(Some(SESSION)),
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
    let recording = HookRecording::new(Unreachable, RunIds::from_environment(Some(SESSION)));
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
fn a_hook_whose_run_names_no_session_records_nothing_and_says_so() {
    let recording = HookRecording::new(Unreachable, RunIds::from_environment(None));
    let missed = recording.record("UserPromptSubmit", HARP, &answer(prompt("p1")));
    assert_eq!(missed.len(), 1);
    assert!(missed[0].why.contains(HARP), "{missed:?}");
}

#[test]
fn an_answer_without_an_event_or_an_observable_one_records_nothing() {
    let recording = HookRecording::new(Unreachable, RunIds::from_environment(Some(SESSION)));
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

const A_BEARER: &str = "0a1b2c3d-0000-4000-8000-000000000001.abababababababababababababababababababababababababababababababab"; // gitleaks:allow

fn a_book() -> angzarr_embedded::v1::CommandBook {
    angzarr_embedded::v1::CommandBook::default()
}

/// A certificate value in the form the run puts in the environment (a DER
/// SEQUENCE, base64); the tests that reach no handshake need no real one.
const A_CERT: &str = "MAMBAgM=";
const ENDPOINT: &str = "https://127.0.0.1:9";

#[test]
fn the_hook_environment_makes_the_listener_sink_only_when_it_is_whole() {
    let whole = HookSink::from_environment(Some(ENDPOINT), Some(A_BEARER), Some(A_CERT));
    assert!(matches!(whole, HookSink::Listener(_)));
    let unreachable = |endpoint, bearer, cert| {
        matches!(
            HookSink::from_environment(endpoint, bearer, cert),
            HookSink::Unreachable(_)
        )
    };
    assert!(unreachable(None, Some(A_BEARER), Some(A_CERT)));
    assert!(unreachable(Some(""), Some(A_BEARER), Some(A_CERT)));
    assert!(unreachable(Some(ENDPOINT), None, Some(A_CERT)));
    assert!(unreachable(Some(ENDPOINT), Some(""), Some(A_CERT)));
    assert!(unreachable(Some(ENDPOINT), Some("no-dot"), Some(A_CERT)));
    assert!(unreachable(
        Some(ENDPOINT),
        Some("run.nothex"),
        Some(A_CERT)
    ));
}

// No fallback: an endpoint and a bearer with no usable certificate are a typed
// refusal (said as not recorded) that sends nothing (not plaintext, not the system's roots).
#[test]
fn a_missing_or_garbled_certificate_is_refused_and_nothing_is_sent() {
    for (cert, why) in [
        (None, "not set"),
        (Some(""), "not set"),
        (Some("!!!!"), "not a certificate"),
        (Some("AAAA"), "not a certificate"),
    ] {
        let sink = HookSink::from_environment(Some(&closed_url()), Some(A_BEARER), cert);
        assert!(matches!(sink, HookSink::Refused(_)), "{cert:?}");
        match sink.deliver(a_book(), Duration::from_secs(5)) {
            Delivery::Unavailable { detail } => {
                assert!(detail.contains("CTXLOOM_TLS_CERT"), "{detail}");
                assert!(detail.contains(why), "{detail}");
                assert!(!detail.contains(A_BEARER), "{detail}");
            }
            other => panic!("{cert:?}: {other:?}"),
        }
    }
}

// No downgrade: an http endpoint is refused by the sink, whatever the rest.
#[test]
fn a_plaintext_endpoint_is_refused_not_dialed() {
    let sink = HookSink::from_environment(Some("http://127.0.0.1:9"), Some(A_BEARER), Some(A_CERT));
    match sink.deliver(a_book(), Duration::from_secs(5)) {
        Delivery::Unavailable { detail } => assert!(detail.contains("plaintext"), "{detail}"),
        other => panic!("{other:?}"),
    }
}

#[test]
fn an_unreachable_hook_sink_says_the_listener_is_not_running() {
    let sink = HookSink::from_environment(None, None, None);
    match sink.deliver(a_book(), Duration::from_secs(1)) {
        Delivery::Unavailable { detail } => assert!(detail.contains("not running"), "{detail}"),
        other => panic!("{other:?}"),
    }
}

#[test]
fn a_listener_hook_sink_at_a_dead_port_is_unavailable_and_keeps_the_bearer_out() {
    let sink = HookSink::from_environment(Some(&closed_url()), Some(A_BEARER), Some(A_CERT));
    match sink.deliver(a_book(), Duration::from_secs(5)) {
        Delivery::Unavailable { detail } => {
            assert!(detail.contains("cannot reach"), "{detail}");
            assert!(!detail.contains(A_BEARER), "{detail}");
        }
        other => panic!("{other:?}"),
    }
}

#[test]
fn a_hook_recording_over_a_dead_listener_says_unavailable_once_and_stops_trying() {
    let sink = HookSink::from_environment(Some(&closed_url()), Some(A_BEARER), Some(A_CERT));
    let recording = HookRecording::new(sink, RunIds::from_environment(Some(SESSION)));
    let missed = recording.record("PostToolBatch", HARP, &answer(batch()));
    assert_eq!(missed.len(), 2);
    assert!(missed[0].why.contains("cannot reach"), "{missed:?}");
    assert!(missed[1].why.contains("not tried"), "{missed:?}");
}

/// A loopback URL nobody listens on: a port that was bound, then released.
fn closed_url() -> String {
    let probe = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
    let port = probe.local_addr().unwrap().port();
    drop(probe);
    format!("https://127.0.0.1:{port}")
}

// ------------------------------------------------- RunIds (review finding 6)

// Ruling 87: a hook's session is the run's, as its environment names it
// (`CTXLOOM_SESSION`), never read from a file in the session's directory:
// the same id whatever harp the hook says (a rename does not lose it).
#[test]
fn a_hooks_session_is_the_one_its_environment_names_whatever_the_harp() {
    let ids = RunIds::from_environment(Some(SESSION));
    assert_eq!(ids.session_of(HARP), Some(session_id()));
    assert_eq!(ids.session_of("renamed-since"), Some(session_id()));
}

#[test]
fn an_environment_that_names_no_usable_session_is_none() {
    for held in [None, Some(""), Some("not-a-uuid")] {
        assert_eq!(
            RunIds::from_environment(held).session_of(HARP),
            None,
            "{held:?}"
        );
    }
}

// Ruling 87: with no session directory at all (no metadata.json is written),
// a hook still records on its run's session.
#[tokio::test]
async fn a_hook_records_on_its_runs_session_with_no_session_directory() {
    let (_, edge) = world().await;
    let recording = HookRecording::new(
        EdgeSink::new(Arc::clone(&edge)),
        RunIds::from_environment(Some(SESSION)),
    );
    let before = pages(&edge).await.len();
    assert!(recording
        .record("PostToolBatch", HARP, &answer(batch()))
        .is_empty());
    assert_eq!(pages(&edge).await.len(), before + 2);
}
