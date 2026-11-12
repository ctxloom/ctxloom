//! The hook listener end to end: a real loopback TCP port (port 0), the
//! generated `Handle` client with the hook bearer in `authorization`, the
//! real kernel (`App` over an in-memory store) behind the real gate. Each test
//! forces the interleaving it concerns: a run ends between two calls, a stop
//! is awaited, a client call is made from a thread while the one that drives
//! the runtime is blocked; none waits for a time.

use std::sync::atomic::{AtomicU32, Ordering};
use std::sync::{Arc, Mutex};

use angzarr_embedded::testing::{MemoryStore, Samples};
use angzarr_embedded::v1::{
    command_page, CommandBook, CommandPage, Cover, DeliveryOutcome, Fact, FactOrigin, Uuid,
};
use angzarr_embedded::{Any, CommandBookExt, OnRetryable, Registry};
use angzarr_embedded_host::config::Expose;
use angzarr_embedded_host::stores::OneStore;
use angzarr_embedded_host::App;
use angzarr_proto_transport_distributed::v1::command_handler_coordinator_service_client::CommandHandlerCoordinatorServiceClient;
use embedded_edge::EmbeddedEdge;
use run_ports::{Bearer, Edge as _, Token, TokenSource};
use runs_application::{EnqueueRequest, Enqueuer, Observation};
use tonic::transport::{Certificate, Channel, ClientTlsConfig, Endpoint};
use tonic::Code;

use std::net::{IpAddr, Ipv4Addr, SocketAddr};

use crate::{HookListener, ListenerError, TlsIdentity};
use hook_tls::{Clock, Minter, SystemClock};
use run_ports::{CertMint, CertNames, HookCertificate};

const LOOPBACK_ANY_PORT: SocketAddr = SocketAddr::new(IpAddr::V4(Ipv4Addr::LOCALHOST), 0);

type TheEdge = EmbeddedEdge<OneStore<MemoryStore>>;

const SESSION: &str = "5f0c7a52-1111-5111-8111-000000000001";
const OTHER_SESSION: &str = "5f0c7a52-2222-5222-8222-000000000002";

/// Tokens `[1; 32]`, `[2; 32]`, ...
#[derive(Default)]
struct Counting(AtomicU32);

impl TokenSource for Counting {
    fn mint(&self) -> Token {
        let n = self.0.fetch_add(1, Ordering::SeqCst) + 1;
        Token::from_bytes([n as u8; 32])
    }
}

fn uuid(text: &str) -> Uuid {
    Uuid::from_bytes(*uuid::Uuid::parse_str(text).unwrap().as_bytes())
}

fn cover(domain: &str, root: &Uuid) -> Cover {
    Cover {
        domain: domain.to_string(),
        root: Some(root.clone()),
        edition: None,
    }
}

/// A fact as a client sends one, built here and not by the code under test.
fn fact(
    cover: Cover,
    message: &impl angzarr_embedded::markup::DomainMessage,
    id: &str,
) -> CommandBook {
    CommandBook {
        cover: Some(cover),
        page: Some(CommandPage {
            body: Some(command_page::Body::Fact(Fact {
                payload: Some(Any::from_msg(&message.to_wire()).unwrap()),
                origin: Some(FactOrigin {
                    external_id: id.to_string(),
                    description: String::new(),
                }),
            })),
            ..CommandPage::default()
        }),
    }
}

/// A COMMAND body carrying `message`, of a type that is a fact's: what the
/// allowlist refuses by kind, not by type.
fn command_of_type(
    cover: Cover,
    message: &impl angzarr_embedded::markup::DomainMessage,
) -> CommandBook {
    CommandBook {
        cover: Some(cover),
        page: Some(CommandPage {
            body: Some(command_page::Body::Command(angzarr_embedded::v1::Command {
                payload: Some(Any::from_msg(&message.to_wire()).unwrap()),
                handling: None,
            })),
            ..CommandPage::default()
        }),
    }
}

/// The run's certificate for the loopback, minted once for the tests that
/// need any one: valid now.
fn certificate() -> &'static HookCertificate {
    static CERT: std::sync::OnceLock<HookCertificate> = std::sync::OnceLock::new();
    CERT.get_or_init(|| mint(&SystemClock, LOOPBACK_ANY_PORT.ip()))
}

fn mint(clock: &dyn Clock, ip: IpAddr) -> HookCertificate {
    struct Ref<'a>(&'a dyn Clock);
    impl Clock for Ref<'_> {
        fn unix_seconds(&self) -> i64 {
            self.0.unix_seconds()
        }
    }
    Minter::new(Ref(clock))
        .mint(&CertNames::new(vec![ip], Vec::new()))
        .unwrap()
}

struct At(i64);

impl Clock for At {
    fn unix_seconds(&self) -> i64 {
        self.0
    }
}

fn identity_of(cert: &HookCertificate) -> TlsIdentity {
    TlsIdentity::from_pem(cert.public().to_pem(), cert.key_pem())
}

fn identity() -> TlsIdentity {
    identity_of(certificate())
}

/// A TLS client config trusting only `cert`, naming the loopback.
fn tls_trusting(cert: &HookCertificate) -> ClientTlsConfig {
    ClientTlsConfig::new()
        .ca_certificate(Certificate::from_pem(cert.public().to_pem()))
        .domain_name("127.0.0.1")
}

async fn try_connect(addr: SocketAddr, cert: &HookCertificate) -> Result<Channel, String> {
    Endpoint::from_shared(format!("https://{addr}"))
        .unwrap()
        .tls_config(tls_trusting(cert))
        .unwrap()
        .connect()
        .await
        .map_err(|e| format!("{e:?}"))
}

async fn connect(addr: SocketAddr) -> Channel {
    try_connect(addr, certificate()).await.unwrap()
}

/// `book`, presenting `authorization` as given (none when `None`).
fn request(book: CommandBook, authorization: Option<&str>) -> tonic::Request<CommandBook> {
    let mut request = tonic::Request::new(book);
    if let Some(value) = authorization {
        request
            .metadata_mut()
            .insert("authorization", value.parse().unwrap());
    }
    request
}

/// What the world holds: the kernel, two sessions each with a live run, the
/// hook's bearer for the first, and the socket path the first's run names.
struct World {
    edge: Arc<TheEdge>,
    run: Uuid,
    session: Uuid,
    other_session: Uuid,
    other_run: Uuid,
    bearer: String,
    /// Where the listener last served.
    addr: Mutex<Option<SocketAddr>>,
}

impl World {
    async fn new() -> World {
        let mut registry = Registry::new();
        registry
            .aggregate::<sessions::SessionAggregate>(sessions::SessionAggregate::default)
            .unwrap();
        registry
            .aggregate::<runs::RunAggregate>(runs::RunAggregate::default)
            .unwrap();
        let exposed = std::collections::BTreeMap::from([
            (sessions::SESSION_DOMAIN.to_string(), Expose::Ip),
            (runs::RUN_DOMAIN.to_string(), Expose::Ip),
        ]);
        let retries = OnRetryable {
            max_retries: 0,
            max_race_runs: std::num::NonZeroU32::new(3).unwrap(),
            initial_backoff: std::time::Duration::ZERO,
            multiplier: 1.0,
            max_backoff: std::time::Duration::ZERO,
            jitter: 0.0,
        };
        let app = App::builder(OneStore::new(MemoryStore::default()), registry)
            .exposed(exposed)
            .on_retryable(retries)
            .start()
            .await;
        let samples = Mutex::new(Samples::starting_at(1_000));
        let edge = Arc::new(EmbeddedEdge::new(Arc::new(app), move || {
            samples.lock().unwrap().next_sample()
        }));
        let source = Arc::new(Counting::default());
        let mut parties = Vec::new();
        for (session, harp) in [
            (SESSION, "amber-swift-owl"),
            (OTHER_SESSION, "calm-otter-dawn"),
        ] {
            let mint = sessions::MintSession {
                harp: harp.to_string(),
                project_id: "calm-otter".to_string(),
                project_dir: "/w".to_string(),
                output_dir: "/o".to_string(),
                engine: "claude-code".to_string(),
                origin: sessions::Origin::Session,
                started_at: None,
                depth: 0,
                owner: None,
            };
            edge.handle(CommandBook::command_of(
                cover(sessions::SESSION_DOMAIN, &uuid(session)),
                &sessions::SessionCommand::from(mint),
            ))
            .await
            .unwrap();
            let enqueued = Enqueuer::new(Arc::clone(&edge), Arc::clone(&source))
                .enqueue(EnqueueRequest {
                    session: session.to_string(),
                    incarnation: 1,
                    mode: runs::RunMode::Interactive,
                    evidence: runs_application::Evidence::passing("claude-code"),
                    launcher_lock: String::new(),
                })
                .await
                .unwrap();
            parties.push(enqueued);
        }
        let other = parties.pop().unwrap();
        let mine = parties.pop().unwrap();
        let run_text = |run: &Uuid| {
            uuid::Uuid::from_slice(&run.value)
                .unwrap()
                .hyphenated()
                .to_string()
        };
        let bearer = Bearer::compose(&run_text(&mine.run), &mine.hook_token)
            .expose()
            .to_string();
        World {
            edge,
            run: mine.run,
            session: uuid(SESSION),
            other_session: uuid(OTHER_SESSION),
            other_run: other.run,
            bearer,
            addr: Mutex::new(None),
        }
    }

    async fn serve(&self) -> HookListener {
        let listener =
            HookListener::serve_tcp(Arc::clone(&self.edge), LOOPBACK_ANY_PORT, identity())
                .await
                .expect("the listener starts");
        *self.addr.lock().unwrap() = Some(listener.local_addr());
        listener
    }

    fn addr(&self) -> SocketAddr {
        self.addr.lock().unwrap().expect("served")
    }

    async fn client(&self) -> CommandHandlerCoordinatorServiceClient<Channel> {
        CommandHandlerCoordinatorServiceClient::new(connect(self.addr()).await)
    }

    fn turn_ended(&self, id: &str) -> CommandBook {
        Observation::TurnEnded(sessions::RecordTurnEnded {
            prompt_id: id.to_string(),
        })
        .book(&self.session)
    }

    /// How many pages each stream the tests watch holds.
    async fn pages(&self, domain: &str, root: &Uuid) -> usize {
        self.edge
            .events(&cover(domain, root), 0)
            .await
            .unwrap()
            .len()
    }

    async fn watched(&self) -> [usize; 4] {
        [
            self.pages(sessions::SESSION_DOMAIN, &self.session).await,
            self.pages(sessions::SESSION_DOMAIN, &self.other_session)
                .await,
            self.pages(runs::RUN_DOMAIN, &self.run).await,
            self.pages(runs::RUN_DOMAIN, &self.other_run).await,
        ]
    }
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn a_capture_fact_with_the_hook_bearer_is_applied_to_the_session_stream() {
    let world = World::new().await;
    let listener = world.serve().await;
    let before = world.watched().await;
    let mut client = world.client().await;
    let answered = client
        .handle(request(
            world.turn_ended("p1"),
            Some(&format!("Bearer {}", world.bearer)),
        ))
        .await
        .expect("applied")
        .into_inner();
    assert_eq!(answered.delivery(), DeliveryOutcome::Applied);
    // Read back through the kernel: the session stream holds the new page, and
    // it is the capture fact's event.
    let pages = world
        .edge
        .events(&cover(sessions::SESSION_DOMAIN, &world.session), 0)
        .await
        .unwrap();
    assert_eq!(pages.len(), before[0] + 1);
    let last = pages.last().unwrap();
    let url = match last.body.as_ref().unwrap() {
        angzarr_embedded::v1::event_page::Body::Event(any) => any.type_url.clone(),
        other => panic!("{other:?}"),
    };
    assert!(url.ends_with("TurnEnded"), "{url}");
    // The answer carries the events the fact caused.
    assert_eq!(answered.events.unwrap().pages.len(), 1);
    let after = world.watched().await;
    assert_eq!(after, [before[0] + 1, before[1], before[2], before[3]]);
    listener.stop().await.unwrap();
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn the_same_origin_again_is_a_duplicate_and_writes_no_page() {
    let world = World::new().await;
    let listener = world.serve().await;
    let mut client = world.client().await;
    let bearer = format!("Bearer {}", world.bearer);
    let first = client
        .handle(request(world.turn_ended("p1"), Some(&bearer)))
        .await
        .unwrap()
        .into_inner();
    assert_eq!(first.delivery(), DeliveryOutcome::Applied);
    let held = world.watched().await;
    let again = client
        .handle(request(world.turn_ended("p1"), Some(&bearer)))
        .await
        .unwrap()
        .into_inner();
    assert_eq!(again.delivery(), DeliveryOutcome::Duplicate);
    assert_eq!(world.watched().await, held, "no page was written");
    listener.stop().await.unwrap();
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn a_credential_that_does_not_identify_is_unauthenticated_and_writes_nothing() {
    let world = World::new().await;
    let listener = world.serve().await;
    let mut client = world.client().await;
    let held = world.watched().await;
    let secret = world.bearer.rsplit_once('.').unwrap().1.to_string();
    let run_text = world.bearer.rsplit_once('.').unwrap().0.to_string();
    let wrong = format!("Bearer {run_text}.{}", "ee".repeat(32));
    let unknown_run = format!("Bearer 00000000-0000-4000-8000-000000000000.{secret}");
    let cases: [(&str, Option<String>); 6] = [
        ("no authorization", None),
        ("a wrong secret", Some(wrong)),
        ("an unknown run", Some(unknown_run)),
        ("not a bearer text", Some("Bearer garbage".to_string())),
        ("the secret alone", Some(format!("Bearer {secret}"))),
        ("another scheme", Some("Basic dXNlcjpwYXNz".to_string())),
    ];
    for (what, authorization) in cases {
        let status = client
            .handle(request(world.turn_ended("p1"), authorization.as_deref()))
            .await
            .expect_err(what);
        assert_eq!(status.code(), Code::Unauthenticated, "{what}");
    }
    assert_eq!(world.watched().await, held, "nothing was written");
    listener.stop().await.unwrap();
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn a_run_that_ends_between_two_calls_is_unauthenticated_on_the_second() {
    let world = World::new().await;
    let listener = world.serve().await;
    let mut client = world.client().await;
    let bearer = format!("Bearer {}", world.bearer);
    client
        .handle(request(world.turn_ended("p1"), Some(&bearer)))
        .await
        .expect("applied while the run is live");
    // The run ends (its runner exited), through the kernel, as a launcher
    // would record it.
    let exited = fact(
        cover(runs::RUN_DOMAIN, &world.run),
        &runs::RunExited { code: 0, signal: 0 },
        "exit:fixture",
    );
    world.edge.handle(exited).await.unwrap();
    let held = world.watched().await;
    let status = client
        .handle(request(world.turn_ended("p2"), Some(&bearer)))
        .await
        .expect_err("revoked");
    assert_eq!(status.code(), Code::Unauthenticated);
    assert_eq!(world.watched().await, held);
    listener.stop().await.unwrap();
}

/// A refusal of the gate after identify is answered as the kernel answers a
/// domain that is not there, whatever the reason: a probe learns nothing of
/// which streams, types or domains exist.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn what_the_gate_forbids_is_answered_as_an_unknown_domain_and_writes_nothing() {
    let world = World::new().await;
    let listener = world.serve().await;
    let mut client = world.client().await;
    let bearer = format!("Bearer {}", world.bearer);
    let held = world.watched().await;
    let mine = cover(sessions::SESSION_DOMAIN, &world.session);
    let nowhere = Cover {
        domain: "nowhere".to_string(),
        root: Some(world.session.clone()),
        edition: None,
    };
    let unknown = client
        .handle(request(
            fact(
                nowhere,
                &sessions::RecordTurnEnded {
                    prompt_id: "p".to_string(),
                },
                "x",
            ),
            Some(&bearer),
        ))
        .await
        .expect_err("a domain that is not there");
    assert_ne!(unknown.code(), Code::Unauthenticated);
    let forbidden: Vec<(&str, CommandBook)> = vec![
        (
            "a fact for another session's stream",
            world_fact(&world.other_session, "p9"),
        ),
        (
            "a runner-lifecycle fact on the run's stream",
            fact(
                cover(runs::RUN_DOMAIN, &world.run),
                &runs::RunExited { code: 0, signal: 0 },
                "exit:forged",
            ),
        ),
        (
            "a command body of a capture type",
            command_of_type(
                mine.clone(),
                &sessions::RecordTurnEnded {
                    prompt_id: "p".to_string(),
                },
            ),
        ),
    ];
    for (what, book) in forbidden {
        let status = client
            .handle(request(book, Some(&bearer)))
            .await
            .expect_err(what);
        assert_eq!(status.code(), unknown.code(), "{what}");
        assert_eq!(status.message(), unknown.message(), "{what}");
    }
    assert_eq!(world.watched().await, held, "nothing was written");
    listener.stop().await.unwrap();
}

fn world_fact(session: &Uuid, id: &str) -> CommandBook {
    Observation::TurnEnded(sessions::RecordTurnEnded {
        prompt_id: id.to_string(),
    })
    .book(session)
}

/// The run waits for its engine in a blocking call on the thread that drives
/// it. Here that thread blocks until a hook's client, on a thread of its own,
/// has delivered a fact: the answer exists only if the listener's tasks run
/// on another worker meanwhile. The block is the forcing, so nothing waits for
/// a time; a listener that depended on the blocked thread never answers and
/// the test never ends (the join below is the hang it would be).
#[test]
fn the_listener_serves_while_the_thread_that_drives_the_run_is_blocked() {
    let runtime = tokio::runtime::Builder::new_multi_thread()
        .worker_threads(1)
        .enable_all()
        .build()
        .unwrap();
    let (delivery, pages) = runtime.block_on(async {
        let world = World::new().await;
        let listener = world.serve().await;
        let addr = world.addr();
        let book = world.turn_ended("p1");
        let bearer = format!("Bearer {}", world.bearer);
        let hook = std::thread::spawn(move || {
            let client_runtime = tokio::runtime::Builder::new_current_thread()
                .enable_all()
                .build()
                .unwrap();
            client_runtime.block_on(async {
                let mut client = CommandHandlerCoordinatorServiceClient::new(connect(addr).await);
                client
                    .handle(request(book, Some(&bearer)))
                    .await
                    .unwrap()
                    .into_inner()
                    .delivery()
            })
        });
        // The engine's wait: this thread does nothing else until the hook is done.
        let delivery = hook.join().unwrap();
        let pages = world.pages(sessions::SESSION_DOMAIN, &world.session).await;
        listener.stop().await.unwrap();
        (delivery, pages)
    });
    assert_eq!(delivery, DeliveryOutcome::Applied);
    assert!(pages >= 2, "the mint and the fact");
}

/// An edge whose commands are admitted and then never finish: a call held in
/// the handler, with the test told that it arrived.
struct Stalling {
    inner: Arc<TheEdge>,
    arrived: tokio::sync::mpsc::UnboundedSender<()>,
}

impl run_ports::Edge for Stalling {
    async fn serve(
        &self,
        _: CommandBook,
        _: run_ports::Arrival,
    ) -> Result<angzarr_embedded::Answered, run_ports::Unserved> {
        let _ = self.arrived.send(());
        std::future::pending().await
    }

    async fn handle(
        &self,
        _: CommandBook,
    ) -> Result<angzarr_embedded::Answered, angzarr_embedded::Failure<angzarr_embedded::Rejected>>
    {
        let _ = self.arrived.send(());
        std::future::pending().await
    }

    async fn events(
        &self,
        cover: &Cover,
        from: u64,
    ) -> Result<Vec<angzarr_embedded::v1::EventPage>, angzarr_embedded::StoreError> {
        self.inner.events(cover, from).await
    }
}

/// A call that is held in the handler holds a connection open, which a
/// graceful shutdown waits for. The call is held by the test (the edge never
/// answers) and the test is told when it is in the handler over the TLS
/// connection, so the wait is forced, never hoped for. The stop must end past
/// its grace ([`crate::STOP_GRACE`], three seconds, really elapsed), end the
/// held connection (its caller sees it fail) and return only once every copy
/// of the service, and so of the edge it holds, is released (rulings 131,
/// 140: a run closes its store on the stop's word). Then nobody listens,
/// judged by this listener's own certificate, minted for this test alone
/// ([`stopped`]).
#[tokio::test(flavor = "current_thread")]
async fn a_client_that_holds_a_connection_open_cannot_hold_the_stop() {
    let world = World::new().await;
    let own = mint(&SystemClock, LOOPBACK_ANY_PORT.ip());
    let (arrived, mut in_handler) = tokio::sync::mpsc::unbounded_channel();
    let edge = Arc::new(Stalling {
        inner: Arc::clone(&world.edge),
        arrived,
    });
    let listener = HookListener::serve_tcp(Arc::clone(&edge), LOOPBACK_ANY_PORT, identity_of(&own))
        .await
        .expect("the listener starts");
    let addr = listener.local_addr();
    *world.addr.lock().unwrap() = Some(addr);
    let book = world.turn_ended("held");
    let bearer = format!("Bearer {}", world.bearer);
    let client = try_connect(addr, &own).await.expect("connects");
    let held = tokio::spawn(async move {
        CommandHandlerCoordinatorServiceClient::new(client)
            .handle(request(book, Some(&bearer)))
            .await
    });
    in_handler.recv().await.expect("the call is in the handler");
    let started = std::time::Instant::now();
    listener
        .stop()
        .await
        .expect("the held call is cut, and the stop ends");
    assert!(
        started.elapsed() >= crate::STOP_GRACE,
        "the grace ran out first"
    );
    assert_eq!(
        Arc::strong_count(&edge),
        1,
        "once the stop returns, nothing the listener served holds the edge"
    );
    let cut = held.await.expect("the caller's task ends");
    assert!(cut.is_err(), "the held call was cut: {cut:?}");
    stopped(addr, &own, "nobody listens").await;
}

/// Nobody presenting `own` answers at `addr` once its listener is stopped.
/// Judged by the certificate, never by the port staying free: in one process
/// (cargo test, the mutation run) a freed port can be given at once to another
/// test's listener, which a plain connect would take for this one. So a test
/// that asserts this serves on a certificate of its own, never the shared
/// [`certificate`], which every other test's listener presents too.
async fn stopped(addr: SocketAddr, own: &HookCertificate, what: &str) {
    assert!(try_connect(addr, own).await.is_err(), "{what}");
}

/// A stop with nothing open ends within the grace with Ok: the success the
/// timeout path must differ from.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn a_stop_with_nothing_open_is_ok_within_the_grace() {
    let world = World::new().await;
    let listener = world.serve().await;
    listener
        .stop_within(std::time::Duration::from_secs(30))
        .await
        .expect("stops");
}

// ---- TCP (owner rulings 2026-11-09, items 31-33) ----

async fn tcp_client(addr: SocketAddr) -> CommandHandlerCoordinatorServiceClient<Channel> {
    CommandHandlerCoordinatorServiceClient::new(connect(addr).await)
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn a_capture_fact_over_tcp_is_applied_to_the_session_stream() {
    let world = World::new().await;
    let own = mint(&SystemClock, LOOPBACK_ANY_PORT.ip());
    let listener = HookListener::serve_tcp(
        Arc::clone(&world.edge),
        LOOPBACK_ANY_PORT,
        identity_of(&own),
    )
    .await
    .expect("serves");
    let addr = listener.local_addr();
    assert!(addr.ip().is_loopback());
    assert_ne!(addr.port(), 0, "port 0 is replaced by the port given");
    let before = world.watched().await;
    let answered = CommandHandlerCoordinatorServiceClient::new(
        try_connect(addr, &own).await.expect("connects"),
    )
    .handle(request(
        world.turn_ended("p1"),
        Some(&format!("Bearer {}", world.bearer)),
    ))
    .await
    .expect("applied")
    .into_inner();
    assert_eq!(answered.delivery(), DeliveryOutcome::Applied);
    let after = world.watched().await;
    assert_eq!(after, [before[0] + 1, before[1], before[2], before[3]]);
    listener.stop().await.unwrap();
    stopped(addr, &own, "nobody listens after the stop").await;
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn two_listeners_on_port_zero_get_two_ports() {
    let world = World::new().await;
    let a = HookListener::serve_tcp(Arc::clone(&world.edge), LOOPBACK_ANY_PORT, identity())
        .await
        .unwrap();
    let b = HookListener::serve_tcp(Arc::clone(&world.edge), LOOPBACK_ANY_PORT, identity())
        .await
        .unwrap();
    assert_ne!(a.local_addr().port(), b.local_addr().port());
    a.stop().await.unwrap();
    b.stop().await.unwrap();
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn an_unspecified_address_is_never_bound() {
    let world = World::new().await;
    for addr in ["0.0.0.0:0", "[::]:0"] {
        let error =
            HookListener::serve_tcp(Arc::clone(&world.edge), addr.parse().unwrap(), identity())
                .await
                .err()
                .expect("refused");
        assert_eq!(
            error,
            crate::ListenerError::Unspecified {
                addr: addr.to_string()
            }
        );
        assert_eq!(
            angzarr_diagnostic::Diagnostic::code(&error),
            "hook-listener-bind-unspecified"
        );
    }
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn the_declared_arrival_follows_the_bind() {
    use crate::Reach;
    assert_eq!(
        crate::reach_of("127.0.0.1".parse().unwrap()),
        Reach::Loopback
    );
    assert_eq!(crate::reach_of("::1".parse().unwrap()), Reach::Loopback);
    assert_eq!(crate::reach_of("172.18.0.1".parse().unwrap()), Reach::Ip);
    assert_eq!(crate::reach_of("192.168.1.9".parse().unwrap()), Reach::Ip);
    let world = World::new().await;
    let listener = HookListener::serve_tcp(Arc::clone(&world.edge), LOOPBACK_ANY_PORT, identity())
        .await
        .unwrap();
    assert_eq!(listener.reach(), Reach::Loopback);
    listener.stop().await.unwrap();
}

// Owner additions (3): a request over the limit is refused before it is
// decoded and writes nothing; the refusal says nothing of the token.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn a_request_over_the_size_limit_is_refused_and_writes_nothing() {
    let world = World::new().await;
    let listener = HookListener::serve_tcp(Arc::clone(&world.edge), LOOPBACK_ANY_PORT, identity())
        .await
        .unwrap();
    let held = world.watched().await;
    let huge = fact(
        cover(sessions::SESSION_DOMAIN, &world.session),
        &sessions::RecordAssistantText {
            prompt_id: "p".to_string(),
            turn_id: "t".to_string(),
            message_id: "m".to_string(),
            index: 0,
            is_final: false,
            text: "x".repeat(crate::MAX_REQUEST_BYTES + 1024),
        },
        "big",
    );
    let status = tcp_client(listener.local_addr())
        .await
        .handle(request(huge, Some(&format!("Bearer {}", world.bearer))))
        .await
        .expect_err("over the limit");
    assert_eq!(
        status.code(),
        Code::OutOfRange,
        "tonic answers an over-limit message as out of range"
    );
    assert_eq!(world.watched().await, held);
    listener.stop().await.unwrap();
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn an_authentication_failure_says_nothing_of_the_token_or_its_digest() {
    use sha2::{Digest, Sha256};
    let world = World::new().await;
    let listener = HookListener::serve_tcp(Arc::clone(&world.edge), LOOPBACK_ANY_PORT, identity())
        .await
        .unwrap();
    let secret = world.bearer.rsplit_once('.').unwrap().1.to_string();
    let digest: String = Sha256::digest(hex_bytes(&secret))
        .iter()
        .map(|b| format!("{b:02x}"))
        .collect();
    let run_text = world.bearer.rsplit_once('.').unwrap().0.to_string();
    let attempted = format!("{run_text}.{}", "ee".repeat(32));
    let status = tcp_client(listener.local_addr())
        .await
        .handle(request(
            world.turn_ended("p1"),
            Some(&format!("Bearer {attempted}")),
        ))
        .await
        .expect_err("unauthenticated");
    assert_eq!(status.code(), Code::Unauthenticated);
    let shown = format!("{status:?}");
    for hidden in [&secret, &digest, &"ee".repeat(32)] {
        assert!(!shown.contains(hidden.as_str()), "{shown}");
    }
    listener.stop().await.unwrap();
}

fn hex_bytes(text: &str) -> Vec<u8> {
    (0..text.len())
        .step_by(2)
        .map(|i| u8::from_str_radix(&text[i..i + 2], 16).unwrap())
        .collect()
}

/// A capture fact whose encoded request is exactly `size` bytes: the text is
/// padded, then the message id, until the encoded length lands on it.
fn fact_of_encoded_size(world: &World, size: usize) -> CommandBook {
    use prost::Message as _;
    let build = |text: usize, id: usize| {
        fact(
            cover(sessions::SESSION_DOMAIN, &world.session),
            &sessions::RecordAssistantText {
                prompt_id: "p".to_string(),
                turn_id: "t".to_string(),
                message_id: "m".repeat(id.max(1)),
                index: 0,
                is_final: false,
                text: "x".repeat(text),
            },
            "sized",
        )
    };
    for id in 1..64 {
        for text in (size - 4096..size).rev() {
            let book = build(text, id);
            if book.encoded_len() == size {
                return book;
            }
        }
    }
    panic!("no fact encodes to exactly {size} bytes");
}

// The limit is exactly 1 MiB: a request of that size is admitted, one byte
// more is refused undecoded.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn the_request_limit_is_exactly_one_mebibyte() {
    assert_eq!(crate::MAX_REQUEST_BYTES, 1_048_576);
    let world = World::new().await;
    let listener = HookListener::serve_tcp(Arc::clone(&world.edge), LOOPBACK_ANY_PORT, identity())
        .await
        .unwrap();
    let bearer = format!("Bearer {}", world.bearer);
    let mut client = tcp_client(listener.local_addr()).await;
    let at_limit = fact_of_encoded_size(&world, crate::MAX_REQUEST_BYTES);
    let answered = client
        .handle(request(at_limit, Some(&bearer)))
        .await
        .expect("a request of exactly the limit is admitted")
        .into_inner();
    assert_eq!(answered.delivery(), DeliveryOutcome::Applied);
    let over = fact_of_encoded_size(&world, crate::MAX_REQUEST_BYTES + 1);
    let status = client
        .handle(request(over, Some(&bearer)))
        .await
        .expect_err("one byte over is refused");
    assert_eq!(status.code(), Code::OutOfRange);
    listener.stop().await.unwrap();
}

/// No downgrade: a client that speaks plaintext gets no answer from the
/// listener, and the bearer it sent is never read as a credential. The TLS
/// control, on the same listener, is applied.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn a_plaintext_client_cannot_talk_to_the_listener() {
    let world = World::new().await;
    let listener = world.serve().await;
    let before = world.watched().await;
    let plain = Endpoint::from_shared(format!("http://{}", world.addr())).unwrap();
    let outcome = match plain.connect().await {
        Ok(channel) => CommandHandlerCoordinatorServiceClient::new(channel)
            .handle(request(
                world.turn_ended("plain"),
                Some(&format!("Bearer {}", world.bearer)),
            ))
            .await
            .map(|_| ())
            .map_err(|e| e.to_string()),
        Err(e) => Err(e.to_string()),
    };
    assert!(outcome.is_err(), "{outcome:?}");
    assert_eq!(world.watched().await, before, "nothing was written");
    let applied = world
        .client()
        .await
        .handle(request(
            world.turn_ended("tls"),
            Some(&format!("Bearer {}", world.bearer)),
        ))
        .await
        .expect("the TLS control is applied")
        .into_inner();
    assert_eq!(applied.delivery(), DeliveryOutcome::Applied);
    listener.stop().await.unwrap();
}

/// The server is authenticated by the pinned certificate alone: a client that
/// trusts another certificate (same address, same names) refuses it.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn a_client_that_pins_another_certificate_refuses_the_listener() {
    let world = World::new().await;
    let listener = world.serve().await;
    let other = mint(&SystemClock, LOOPBACK_ANY_PORT.ip());
    assert!(try_connect(world.addr(), certificate()).await.is_ok());
    assert!(try_connect(world.addr(), &other).await.is_err());
    listener.stop().await.unwrap();
}

/// An expired certificate is refused by a client that pins it; the unexpired
/// one connects.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn an_expired_certificate_is_refused() {
    let world = World::new().await;
    let expired = mint(&At(1_577_836_800), LOOPBACK_ANY_PORT.ip());
    let listener = HookListener::serve_tcp(
        Arc::clone(&world.edge),
        LOOPBACK_ANY_PORT,
        identity_of(&expired),
    )
    .await
    .unwrap();
    assert!(try_connect(listener.local_addr(), &expired).await.is_err());
    listener.stop().await.unwrap();
}

/// A identity the TLS layer cannot use is a typed bind error, and nothing
/// listens.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn an_unusable_identity_is_a_typed_bind_error() {
    let world = World::new().await;
    let error = HookListener::serve_tcp(
        Arc::clone(&world.edge),
        LOOPBACK_ANY_PORT,
        TlsIdentity::from_pem("not a certificate", "not a key"),
    )
    .await
    .err()
    .expect("refused");
    assert!(matches!(error, ListenerError::Bind { .. }), "{error:?}");
}

/// The private key is in no rendering of the identity.
#[test]
fn the_identity_does_not_render_the_key() {
    let shown = format!("{:?}", identity());
    assert!(!shown.contains("PRIVATE"), "{shown}");
    assert!(!shown.contains(certificate().key_pem().lines().nth(1).unwrap()));
}

// The separation model: the daemon's listener has one principal, the user.
// Her bearer is admitted for any session's facts and commands; a run's hook
// bearer, and a bearer it did not mint, are a bare UNAUTHENTICATED that
// says nothing of why (S11).
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn the_daemons_listener_admits_the_user_bearer_alone() {
    let world = World::new().await;
    let user = Token::from_bytes([77; 32]);
    let gate = runs_application::UserGate::new(&user);
    let listener =
        HookListener::serve_tcp_gated(Arc::clone(&world.edge), gate, LOOPBACK_ANY_PORT, identity())
            .await
            .expect("serves");
    let mut client = tcp_client(listener.local_addr()).await;
    let theirs = format!("Bearer {}", user.to_text());
    let mine = client
        .handle(request(world.turn_ended("p1"), Some(&theirs)))
        .await
        .expect("the user's fact is applied")
        .into_inner();
    assert_eq!(mine.delivery(), DeliveryOutcome::Applied);
    let elsewhere = client
        .handle(request(
            world_fact(&world.other_session, "p2"),
            Some(&theirs),
        ))
        .await
        .expect("any session's stream is the user's")
        .into_inner();
    assert_eq!(elsewhere.delivery(), DeliveryOutcome::Applied);
    let held = world.watched().await;
    let mut said = Vec::new();
    for authorization in [
        format!("Bearer {}", world.bearer),
        format!("Bearer {}", Token::from_bytes([78; 32]).to_text()),
        "Bearer garbage".to_string(),
    ] {
        let status = client
            .handle(request(world.turn_ended("p3"), Some(&authorization)))
            .await
            .expect_err("refused");
        assert_eq!(status.code(), Code::Unauthenticated);
        assert!(!status.message().contains(&user.to_text()));
        said.push(status.message().to_string());
    }
    assert!(said.windows(2).all(|w| w[0] == w[1]), "{said:?}");
    assert_eq!(world.watched().await, held, "nothing was written");
    listener.stop().await.unwrap();
}
