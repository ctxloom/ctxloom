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

use crate::InProcessCore;
use ctxloom_wiring::capture::*;
use run_ports::HeldFacts;

/// The session's resource name, as `CTXLOOM_SESSION` carries it (s3-Q3).
const SESSION: &str = "projects/amber-quiet-heron/sessions/amber-swift-owl";
const HARP: &str = "amber-swift-owl";

type Store = FaultyStore<MemoryStore>;
type TheEdge = EmbeddedEdge<crate::CoreStores<Store>>;

fn session_id() -> Uuid {
    Uuid::from_bytes(
        *sessions::SessionName::parse(SESSION)
            .unwrap()
            .id()
            .as_bytes(),
    )
}

/// The session's stable id, as text.
fn session_uid() -> String {
    sessions::SessionName::parse(SESSION).unwrap().uid()
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
        owner: None,
        workspace: None,
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

// s4-Q14, H3 follow-up: the in-process sink answers a capture fact
// addressed to a session the store has no record of as the daemon's
// listener does: no such session, never applied.
#[tokio::test]
async fn a_fact_to_a_session_with_no_record_is_no_such_session() {
    let (_, edge) = world().await;
    let sink = EdgeSink::new(edge);
    let nobody = Uuid::from_bytes(*uuid::Uuid::from_u128(7).as_bytes());
    let book = observe(&prompt("p1"))[0].book(&nobody);
    assert_eq!(
        sink.deliver(book, Duration::from_secs(5)),
        Delivery::NoSuchSession
    );
    assert_eq!(
        sink.deliver(book_of(&prompt("p1")), Duration::from_secs(5)),
        Delivery::Applied,
        "a live session's as ever"
    );
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

/// The not-recorded report an [`Unrecorded`] is (a test's expectation).
fn dropped(unrecorded: &hook_cli::Unrecorded) -> &hook_cli::NotRecorded {
    match unrecorded {
        hook_cli::Unrecorded::Dropped(dropped) => dropped,
        other => panic!("not dropped: {other:?}"),
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
    let what: Vec<&str> = missed.iter().map(|m| dropped(m).what.as_str()).collect();
    assert_eq!(
        what,
        [
            format!("hook:tool:{}:a", session_uid()),
            format!("hook:tool:{}:b", session_uid())
        ]
    );
    assert!(
        dropped(&missed[0]).why.contains("listener is not running"),
        "{missed:?}"
    );
    assert!(dropped(&missed[1]).why.contains("not tried"), "{missed:?}");
}

#[test]
fn a_hook_whose_run_names_no_session_records_nothing_and_says_so() {
    let recording = HookRecording::new(Unreachable, RunIds::from_environment(None));
    let missed = recording.record("UserPromptSubmit", HARP, &answer(prompt("p1")));
    assert_eq!(missed.len(), 1);
    assert!(dropped(&missed[0]).why.contains(HARP), "{missed:?}");
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
    assert!(
        dropped(&missed[0]).why.contains("cannot reach"),
        "{missed:?}"
    );
    assert!(dropped(&missed[1]).why.contains("not tried"), "{missed:?}");
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

// Slice 3 step 1 (s3-Q3, ruled): `CTXLOOM_SESSION` carries the session's
// resource name, and a hook derives the stable id from it, the UUIDv5 the
// session's stream is rooted on: no second variable, no lookup.
#[test]
fn a_hooks_session_is_the_uuidv5_of_the_resource_name_its_environment_names() {
    let name = "projects/amber-quiet-heron/sessions/amber-swift-owl";
    let id = sessions::SessionName::parse(name).unwrap().id();
    assert_eq!(
        RunIds::from_environment(Some(name)).session_of(HARP),
        Some(Uuid::from_bytes(*id.as_bytes()))
    );
}

#[test]
fn an_environment_that_names_no_usable_session_is_none() {
    for held in [
        None,
        Some(""),
        Some("not-a-name"),
        Some("projects/x/sessions/"),
    ] {
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

/// The daemon's listener over `edge`: the user's gate for `bearer`, on a
/// loopback port, with a fresh certificate; and the discovery it would
/// write.
async fn a_daemon(
    edge: &Arc<TheEdge>,
    bearer: &run_ports::Token,
) -> (hook_listener::HookListener, run_ports::Discovery) {
    use run_ports::CertMint;
    let names = run_ports::CertNames::new(
        vec![std::net::IpAddr::V4(std::net::Ipv4Addr::LOCALHOST)],
        Vec::new(),
    );
    let certificate = hook_tls::Minter::lasting(hook_tls::SystemClock, 3_600)
        .mint(&names)
        .unwrap();
    let listener = hook_listener::HookListener::serve_tcp_gated(
        Arc::clone(edge),
        runs_application::UserGate::new(bearer),
        "127.0.0.1:0".parse().unwrap(),
        hook_listener::TlsIdentity::from_pem(certificate.public().to_pem(), certificate.key_pem()),
    )
    .await
    .unwrap();
    let discovery = run_ports::Discovery {
        stamp: "v0.1.0-abc1234".to_string(),
        pid: 1,
        endpoint: format!("https://127.0.0.1:{}", listener.local_addr().port()),
        cert: certificate.public().clone(),
        bearer: bearer.clone(),
        home: "/home/u".to_string(),
    };
    (listener, discovery)
}

// The separation model: at the daemon a hook presents the USER bearer its
// discovery file names (the run's own bearer is not asked for), and a
// discovery whose bearer the daemon did not mint delivers nothing there.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn a_hook_presents_the_user_bearer_its_discovery_names_to_the_daemon() {
    let (_store, edge) = world().await;
    let minted = run_ports::Token::from_bytes([5; 32]);
    let (listener, discovery) = a_daemon(&edge, &minted).await;
    let before = pages(&edge).await.len();
    let stale = run_ports::Discovery {
        bearer: run_ports::Token::from_bytes([6; 32]),
        ..discovery.clone()
    };
    let deliver = |discovery: run_ports::Discovery, id: &'static str| {
        tokio::task::spawn_blocking(move || {
            daemon_sink(discovery).deliver(book_of(&prompt(id)), Duration::from_secs(10))
        })
    };
    assert_eq!(deliver(discovery, "p1").await.unwrap(), Delivery::Applied);
    assert_eq!(pages(&edge).await.len(), before + 1);
    assert!(matches!(
        deliver(stale, "p2").await.unwrap(),
        Delivery::Refused { .. }
    ));
    assert_eq!(pages(&edge).await.len(), before + 1, "nothing more");
    listener.stop().await.unwrap();
}

// ------------------------------------------- HoldingRecording (s4-Q14, H2)

/// The hook's own stamp, and an older and a newer daemon's.
const OWN: &str = "v0.2.0-aaaaaaa";
const OLDER: &str = "v0.1.0-bbbbbbb";

/// The daemon a test's hook reaches: none, or one of `stamp` over the
/// in-process edge; the waits it was asked for, and what it runs inside its
/// first delivery (another hook of the session, forcing their race).
struct Reaching {
    daemon: Option<(String, Arc<TheEdge>)>,
    waits: std::sync::Mutex<Vec<Duration>>,
    during: std::sync::Mutex<Option<Box<dyn FnOnce() + Send>>>,
}

impl Reaching {
    fn none() -> Reaching {
        Reaching {
            daemon: None,
            waits: std::sync::Mutex::new(Vec::new()),
            during: std::sync::Mutex::new(None),
        }
    }

    fn daemon(stamp: &str, edge: &Arc<TheEdge>) -> Reaching {
        Reaching {
            daemon: Some((stamp.to_string(), Arc::clone(edge))),
            ..Reaching::none()
        }
    }
}

/// The in-process sink, running `during` inside its first delivery.
struct Racing {
    sink: EdgeSink<TheEdge>,
    during: std::sync::Mutex<Option<Box<dyn FnOnce() + Send>>>,
}

impl FactSink for Racing {
    fn deliver(&self, book: angzarr_embedded::v1::CommandBook, within: Duration) -> Delivery {
        let during = self.during.lock().unwrap().take();
        if let Some(during) = during {
            during();
        }
        self.sink.deliver(book, within)
    }
}

impl HookReach for Reaching {
    type Sink = Racing;

    fn reach(&self, wait: Duration) -> Option<(String, Racing)> {
        self.waits.lock().unwrap().push(wait);
        let (stamp, edge) = self.daemon.as_ref()?;
        Some((
            stamp.clone(),
            Racing {
                sink: EdgeSink::new(Arc::clone(edge)),
                during: std::sync::Mutex::new(self.during.lock().unwrap().take()),
            },
        ))
    }
}

/// A home's holds on a temporary file system.
fn spool() -> fact_spool::Spool {
    let fs = Arc::new(file_system::Rooted::temp().unwrap());
    let home = std::path::PathBuf::from(os::host().flavor().from_top("/home/a"));
    fact_spool::Spool::at_home(fs, &home)
}

fn holding(
    reach: Reaching,
    held: &fact_spool::Spool,
) -> HoldingRecording<Reaching, fact_spool::Spool, RunIds> {
    HoldingRecording::new(
        reach,
        held.clone(),
        RunIds::from_environment(Some(SESSION)),
        OWN,
        SESSION,
        std::time::Instant::now(),
    )
}

fn root() -> uuid::Uuid {
    uuid::Uuid::from_slice(&session_id().value).unwrap()
}

/// What the session's hold holds, by external id, in order.
fn held_ids(held: &fact_spool::Spool) -> Vec<String> {
    let batch = held.take(&root()).unwrap().expect("no other taker");
    batch
        .entries()
        .iter()
        .map(|entry| match entry {
            run_ports::HeldEntry::Fact(fact) => fact.external_id.clone(),
            run_ports::HeldEntry::Gap(gap) => format!("gap:{}", gap.external_id),
        })
        .collect()
}

fn external_ids(event: &HookEvent) -> Vec<String> {
    observe(event)
        .iter()
        .map(|o| o.external_id(&session_id()))
        .collect()
}

/// The external ids of the facts the session's stream recorded, in order.
async fn recorded(edge: &TheEdge) -> Vec<String> {
    pages(edge)
        .await
        .iter()
        .filter_map(|page| match page.meta.as_ref()?.causation.as_ref()? {
            angzarr_embedded::v1::event_meta::Causation::Fact(fact) => {
                Some(fact.origin.as_ref()?.external_id.clone())
            }
            _ => None,
        })
        .collect()
}

// Owner 2026-11-15 (H-Q9 folded in): a SessionStart's bind of the engine's
// conversation reaches the daemon as every capture fact does, and the
// session is bound to it; none is written in the hook's own process.
#[tokio::test]
async fn a_session_starts_bind_is_delivered_to_the_daemon_and_binds_the_session() {
    let (_, edge) = world().await;
    let start = HookEvent {
        event: "session_start".to_string(),
        native_session: "native-9".to_string(),
        transcript: "/t/native-9.jsonl".to_string(),
        source: "startup".to_string(),
        ..HookEvent::default()
    };
    let said = holding(Reaching::daemon(OWN, &edge), &spool()).record(
        "SessionStart",
        HARP,
        &answer(start),
    );
    assert!(said.is_empty(), "{said:?}");
    assert_eq!(
        recorded(&edge).await,
        [format!("bind:{}:native-9", session_uid())]
    );
}

// A hook whose daemon is older than it (H-Q1) holds its facts, delivers
// nothing, and says each held, naming both versions.
#[tokio::test]
async fn a_hook_newer_than_its_daemon_holds_its_facts_and_says_so() {
    let (_, edge) = world().await;
    let held = spool();
    let before = pages(&edge).await.len();
    let said = holding(Reaching::daemon(OLDER, &edge), &held).record(
        "PostToolBatch",
        HARP,
        &answer(batch()),
    );
    assert_eq!(pages(&edge).await.len(), before, "nothing delivered");
    assert_eq!(held_ids(&held), external_ids(&batch()));
    assert_eq!(said.len(), 2);
    for unrecorded in &said {
        let hook_cli::Unrecorded::Held { harp, why, .. } = unrecorded else {
            panic!("held: {unrecorded:?}")
        };
        assert_eq!(harp, HARP);
        assert!(why.contains(OLDER) && why.contains(OWN), "{why}");
    }
}

// A hook that reaches no daemon (none serves, none could be started) holds
// (H-Q2), and says why, within the bound it was given.
#[test]
fn a_hook_that_reaches_no_daemon_holds_and_says_why() {
    let held = spool();
    let recording = holding(Reaching::none(), &held);
    let said = recording.record("UserPromptSubmit", HARP, &answer(prompt("p1")));
    assert_eq!(held_ids(&held), external_ids(&prompt("p1")));
    let [hook_cli::Unrecorded::Held { why, .. }] = said.as_slice() else {
        panic!("one held: {said:?}")
    };
    assert!(
        why.contains("no ctxloom daemon could be reached or started within 10s"),
        "{why}"
    );
    let waits = recording.reach.waits.lock().unwrap().clone();
    assert_eq!(waits.len(), 1);
    assert!(
        waits[0] <= ctxloom_wiring::daemon::SPAWN_WAIT,
        "a spawn waits at most a client's wait: {waits:?}"
    );
}

// The spawn wait is what the hook's bound leaves a delivery's margin of:
// S16's ten seconds, or the engine's own budget where it is shorter
// (MessageDisplay's five); a bound no longer than the margin leaves no wait.
#[test]
fn a_hooks_spawn_wait_is_what_its_bound_leaves() {
    assert_eq!(
        hook_bound("UserPromptSubmit"),
        ctxloom_wiring::daemon::HOOK_BOUND
    );
    assert_eq!(hook_bound("MessageDisplay"), Duration::from_secs(5));
    let start = std::time::Instant::now();
    let wait = spawn_wait(&Deadline(start + hook_bound("UserPromptSubmit")));
    assert!(
        wait <= ctxloom_wiring::daemon::SPAWN_WAIT && wait > Duration::from_secs(7),
        "{wait:?}"
    );
    assert_eq!(
        spawn_wait(&Deadline(start + ctxloom_wiring::daemon::DELIVERY_MARGIN)),
        Duration::ZERO
    );
}

// H-Q5 revised: an up-to-date hook delivers its session's hold first, in
// held order, then its own fact, and the hold is gone.
#[tokio::test]
async fn an_up_to_date_hook_delivers_what_its_session_holds_then_its_own() {
    let (_, edge) = world().await;
    let held = spool();
    holding(Reaching::daemon(OLDER, &edge), &held).record(
        "UserPromptSubmit",
        HARP,
        &answer(prompt("p1")),
    );
    let before = recorded(&edge).await;
    let said = holding(Reaching::daemon(OWN, &edge), &held).record(
        "PostToolBatch",
        HARP,
        &answer(batch()),
    );
    assert!(said.is_empty(), "{said:?}");
    let now = recorded(&edge).await;
    assert_eq!(
        now[before.len()..].to_vec(),
        [external_ids(&prompt("p1")), external_ids(&batch())].concat()
    );
    assert!(held.is_empty(&root()).unwrap(), "nothing is held");
}

// Two hooks of one session at once, over the real hold and its flock: the
// second fires INSIDE the first one's delivery of the held fact. It cannot
// take the hold (the first holds its flush lock), so it holds behind; the
// first takes it again and delivers it before its own. Each fact once, in
// held order.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn two_hooks_of_one_session_at_once_record_each_fact_once_in_held_order() {
    let (_, edge) = world().await;
    let held = spool();
    let (first_edge, first_held) = (Arc::clone(&edge), held.clone());
    tokio::task::spawn_blocking(move || {
        holding(Reaching::daemon(OLDER, &first_edge), &first_held).record(
            "UserPromptSubmit",
            HARP,
            &answer(prompt("p1")),
        )
    })
    .await
    .unwrap();
    let before = recorded(&edge).await;
    let (second_said, told) = std::sync::mpsc::channel();
    let reach = Reaching::daemon(OWN, &edge);
    {
        let (edge, held) = (Arc::clone(&edge), held.clone());
        *reach.during.lock().unwrap() = Some(Box::new(move || {
            let said = holding(Reaching::daemon(OWN, &edge), &held).record(
                "UserPromptSubmit",
                HARP,
                &answer(prompt("p2")),
            );
            second_said.send(said).unwrap();
        }));
    }
    let first_held = held.clone();
    let first = tokio::task::spawn_blocking(move || {
        holding(reach, &first_held).record("PostToolBatch", HARP, &answer(batch()))
    })
    .await
    .unwrap();
    let second = told.recv().unwrap();
    assert!(
        matches!(second.as_slice(), [hook_cli::Unrecorded::HeldBehind { .. }]),
        "{second:?}"
    );
    assert!(first.is_empty(), "{first:?}");
    let now = recorded(&edge).await;
    assert_eq!(
        now[before.len()..].to_vec(),
        [
            external_ids(&prompt("p1")),
            external_ids(&prompt("p2")),
            external_ids(&batch())
        ]
        .concat()
    );
    assert!(held.is_empty(&root()).unwrap());
}

// ----------------------------- the fold is fresh (owner, H3's condition)

type SqliteEdge =
    EmbeddedEdge<crate::CoreStores<angzarr_embedded::angzarr_store_sqlite::SqliteStore>>;

/// The daemon's listener over `edge` (any core), as [`a_daemon`] serves one.
async fn listening<E: Edge + 'static>(
    edge: &Arc<E>,
    bearer: &run_ports::Token,
) -> (hook_listener::HookListener, run_ports::Discovery) {
    use run_ports::CertMint;
    let names = run_ports::CertNames::new(
        vec![std::net::IpAddr::V4(std::net::Ipv4Addr::LOCALHOST)],
        Vec::new(),
    );
    let certificate = hook_tls::Minter::lasting(hook_tls::SystemClock, 3_600)
        .mint(&names)
        .unwrap();
    let listener = hook_listener::HookListener::serve_tcp_gated(
        Arc::clone(edge),
        runs_application::UserGate::new(bearer),
        "127.0.0.1:0".parse().unwrap(),
        hook_listener::TlsIdentity::from_pem(certificate.public().to_pem(), certificate.key_pem()),
    )
    .await
    .unwrap();
    let discovery = run_ports::Discovery {
        stamp: "v0.1.0-abc1234".to_string(),
        pid: 1,
        endpoint: format!("https://127.0.0.1:{}", listener.local_addr().port()),
        cert: certificate.public().clone(),
        bearer: bearer.clone(),
        home: "/home/u".to_string(),
    };
    (listener, discovery)
}

/// The mint of the session `SESSION` names, as a command book.
fn mint_book() -> angzarr_embedded::v1::CommandBook {
    let mint = sessions::MintSession {
        harp: HARP.to_string(),
        project_id: "calm-otter".to_string(),
        project_dir: "/w".to_string(),
        output_dir: "/o".to_string(),
        engine: "claude-code".to_string(),
        origin: sessions::Origin::Session,
        started_at: None,
        depth: 0,
        owner: None,
        workspace: None,
    };
    angzarr_embedded::v1::CommandBook::command_of(
        session_cover(),
        &sessions::SessionCommand::from(mint),
    )
}

// The owner's condition on "no such session": a fact is decided on its
// aggregate's stream folded FRESH from the store, so a session another
// process minted a moment ago is seen. Forced over the real one-file store
// (sqlite) with two handles, the daemon's and another process's: the
// daemon's listener first answers a capture fact for the session "no such
// session" (it has loaded that empty stream once), then the other process
// mints the session, and the very next delivery through the listener is
// applied, never "no such session" from a cached fold.
#[allow(
    clippy::disallowed_methods,
    reason = "a test kernel or store of its own"
)]
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn a_session_another_process_minted_a_moment_ago_is_seen_by_the_listener() {
    let fs = Arc::new(file_system::Rooted::temp().unwrap());
    let open = || async {
        ctxloom_store_sqlite::Stores::new(fs.clone(), "/home/u")
            .open()
            .await
            .unwrap()
    };
    let daemon = InProcessCore::start(open().await).await;
    let other = InProcessCore::start(open().await).await;
    let edge: Arc<SqliteEdge> = daemon.edge();
    let bearer = run_ports::Token::from_bytes([5; 32]);
    let (listener, discovery) = listening(&edge, &bearer).await;
    let deliver = |id: &'static str| {
        let discovery = discovery.clone();
        tokio::task::spawn_blocking(move || {
            daemon_sink(discovery).deliver(book_of(&prompt(id)), Duration::from_secs(10))
        })
    };
    assert_eq!(deliver("p0").await.unwrap(), Delivery::NoSuchSession);
    other.edge().handle(mint_book()).await.unwrap();
    assert_eq!(
        deliver("p1").await.unwrap(),
        Delivery::Applied,
        "the mint the other handle committed is in the fold"
    );
    let recorded = edge.events(&session_cover(), 0).await.unwrap();
    assert!(
        recorded.iter().any(|page| matches!(
            page.meta.as_ref().and_then(|m| m.causation.as_ref()),
            Some(angzarr_embedded::v1::event_meta::Causation::Fact(fact))
                if fact.origin.as_ref().is_some_and(|o| o.external_id.ends_with(":p1"))
        )),
        "the prompt is recorded on the daemon's store"
    );
    listener.stop().await.unwrap();
}
