//! The hook listener end to end: a real unix socket in a short temporary path,
//! the generated `Handle` client with the hook bearer in `authorization`, the
//! real kernel (`App` over an in-memory store) behind the real gate. Each test
//! forces the interleaving it concerns: a run ends between two calls, a stop
//! is awaited, a client call is made from a thread while the one that drives
//! the runtime is blocked; none waits for a time.

use std::future::Future;
use std::io;
use std::path::{Path, PathBuf};
use std::pin::Pin;
use std::sync::atomic::{AtomicU32, Ordering};
use std::sync::{Arc, Mutex};
use std::task::{Context, Poll};

use angzarr_embedded::testing::{MemoryStore, Samples};
use angzarr_embedded::v1::{
    command_page, CommandBook, CommandPage, Cover, DeliveryOutcome, Fact, FactOrigin, Uuid,
};
use angzarr_embedded::{Any, CommandBookExt, OnRetryable, Registry};
use angzarr_embedded_host::config::Expose;
use angzarr_embedded_host::stores::OneStore;
use angzarr_embedded_host::App;
use angzarr_proto_transport_distributed::v1::command_handler_coordinator_service_client::CommandHandlerCoordinatorServiceClient;
use angzarr_test_dir::{OnPanic, TestDir};
use embedded_edge::EmbeddedEdge;
use file_system::Fs;
use hyper_util::rt::TokioIo;
use os::Mode;
use run_ports::{Bearer, Edge as _, Token, TokenSource};
use runs_application::{EnqueueRequest, Enqueuer, Observation};
use tonic::codegen::http::Uri;
use tonic::codegen::Service;
use tonic::transport::{Channel, Endpoint};
use tonic::Code;

use crate::HookListener;

type TheEdge = EmbeddedEdge<OneStore<MemoryStore>>;
type Boxed<T> = Pin<Box<dyn Future<Output = T> + Send>>;

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

/// Connects to a unix socket, whatever URI it is given.
#[derive(Clone)]
struct Uds(PathBuf);

impl Service<Uri> for Uds {
    type Response = TokioIo<tokio::net::UnixStream>;
    type Error = io::Error;
    type Future = Boxed<io::Result<Self::Response>>;

    fn poll_ready(&mut self, _: &mut Context<'_>) -> Poll<io::Result<()>> {
        Poll::Ready(Ok(()))
    }

    fn call(&mut self, _: Uri) -> Self::Future {
        let path = self.0.clone();
        Box::pin(async move { Ok(TokioIo::new(tokio::net::UnixStream::connect(path).await?)) })
    }
}

async fn connect(path: &Path) -> Result<Channel, tonic::transport::Error> {
    Endpoint::from_static("http://uds.invalid")
        .connect_with_connector(Uds(path.to_path_buf()))
        .await
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
    /// The runtime directory the socket goes under; removed on drop.
    dir: TestDir,
    edge: Arc<TheEdge>,
    run: Uuid,
    session: Uuid,
    other_session: Uuid,
    other_run: Uuid,
    bearer: String,
    runner_bearer: String,
    endpoint: PathBuf,
}

impl World {
    async fn new() -> World {
        let dir = TestDir::under(Path::new("/tmp"), "hl", OnPanic::Remove);
        fs().set_permissions(dir.path(), Mode::PRIVATE_DIR).unwrap();
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
        let runner_bearer = Bearer::compose(&run_text(&mine.run), &mine.runner_token)
            .expose()
            .to_string();
        // The socket is where the run names it: under the runtime directory
        // the use case is given, at the short path run-ports computes.
        let endpoint = run_ports::endpoint_path(
            Some(dir.path().to_str().unwrap()),
            "/nonexistent",
            &mine.run.value,
        )
        .unwrap();
        World {
            dir,
            edge,
            run: mine.run,
            session: uuid(SESSION),
            other_session: uuid(OTHER_SESSION),
            other_run: other.run,
            bearer,
            runner_bearer,
            endpoint: PathBuf::from(endpoint),
        }
    }

    async fn serve(&self) -> HookListener {
        HookListener::serve(Arc::clone(&self.edge), &self.endpoint)
            .await
            .expect("the listener starts")
    }

    async fn client(&self) -> CommandHandlerCoordinatorServiceClient<Channel> {
        CommandHandlerCoordinatorServiceClient::new(connect(&self.endpoint).await.unwrap())
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

/// The filesystem the tests read modes through (the platform seam, not std's
/// unix extensions).
fn fs() -> file_system::Os {
    file_system::Os::new(os::host(), Path::new("/")).unwrap()
}

fn present(path: &Path) -> bool {
    fs().exists(path).unwrap()
}

fn mode(path: &Path) -> u32 {
    fs().metadata(path).unwrap().mode.unwrap().bits()
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
    let cases: [(&str, Option<String>); 7] = [
        ("no authorization", None),
        ("a wrong secret", Some(wrong)),
        ("an unknown run", Some(unknown_run)),
        ("not a bearer text", Some("Bearer garbage".to_string())),
        ("the secret alone", Some(format!("Bearer {secret}"))),
        (
            "the runner's bearer",
            Some(format!("Bearer {}", world.runner_bearer)),
        ),
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
            "a runner-lifecycle fact on the session's stream",
            fact(
                mine.clone(),
                &sessions::BindEngineSession {
                    native_id: "forged".to_string(),
                    transcript_path: "/t".to_string(),
                    engine_version: None,
                },
                "bind:forged",
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
        (
            "a command to mint a session",
            CommandBook::command_of(
                mine,
                &sessions::SessionCommand::from(sessions::MintSession {
                    harp: "forged-name-here".to_string(),
                    project_id: "p".to_string(),
                    project_dir: "/w".to_string(),
                    output_dir: "/o".to_string(),
                    engine: "claude-code".to_string(),
                    origin: sessions::Origin::Session,
                    started_at: None,
                    depth: 0,
                }),
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

/// The socket policy is the shared listener's, asserted where this listener's
/// users rely on it: 0600 in a 0700 directory, removed on stop.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn the_socket_is_private_and_is_removed_when_the_listener_stops() {
    let world = World::new().await;
    // The socket's own directory does not exist yet: the listener makes it.
    let directory = world.endpoint.parent().unwrap().to_path_buf();
    assert!(!present(&directory));
    let listener = world.serve().await;
    assert_eq!(listener.endpoint(), world.endpoint.to_str().unwrap());
    assert_eq!(mode(&world.endpoint), 0o600);
    assert_eq!(mode(&directory), 0o700);
    let mut client = world.client().await;
    client
        .handle(request(
            world.turn_ended("p1"),
            Some(&format!("Bearer {}", world.bearer)),
        ))
        .await
        .unwrap();
    listener.stop().await.unwrap();
    assert!(!present(&world.endpoint), "the socket is removed");
    assert!(connect(&world.endpoint).await.is_err(), "nobody listens");
    assert!(present(world.dir.path()));
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn dropping_the_listener_removes_the_socket_too() {
    let world = World::new().await;
    let listener = world.serve().await;
    assert!(present(&world.endpoint));
    drop(listener);
    assert!(!present(&world.endpoint));
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn a_socket_another_listener_holds_is_refused_and_the_first_keeps_serving() {
    let world = World::new().await;
    let first = world.serve().await;
    let error = HookListener::serve(Arc::clone(&world.edge), &world.endpoint)
        .await
        .err()
        .expect("refused");
    assert_eq!(
        angzarr_diagnostic::Diagnostic::code(&error),
        "hook-listener-bind"
    );
    let mut client = world.client().await;
    client
        .handle(request(
            world.turn_ended("p1"),
            Some(&format!("Bearer {}", world.bearer)),
        ))
        .await
        .expect("the first listener still serves");
    first.stop().await.unwrap();
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn a_directory_that_is_not_private_is_refused() {
    let world = World::new().await;
    let directory = world.endpoint.parent().unwrap();
    fs().create_dir_all(directory, Mode::DIR).unwrap();
    fs().set_permissions(directory, Mode::DIR).unwrap();
    let error = HookListener::serve(Arc::clone(&world.edge), &world.endpoint)
        .await
        .err()
        .expect("refused");
    let rendered = error.to_string();
    assert!(rendered.contains(directory.to_str().unwrap()), "{rendered}");
    assert!(!present(&world.endpoint));
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
        let endpoint = world.endpoint.clone();
        let book = world.turn_ended("p1");
        let bearer = format!("Bearer {}", world.bearer);
        let hook = std::thread::spawn(move || {
            let client_runtime = tokio::runtime::Builder::new_current_thread()
                .enable_all()
                .build()
                .unwrap();
            client_runtime.block_on(async {
                let mut client =
                    CommandHandlerCoordinatorServiceClient::new(connect(&endpoint).await.unwrap());
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

/// A client that connects and never completes its handshake holds a connection
/// open, which a graceful shutdown waits for. The connection is held by the
/// test, so the wait is forced, never hoped for: the stop must end past its
/// grace ([`crate::STOP_GRACE`], three seconds, really elapsed) with the typed timeout, the socket must be gone, and the
/// listener must be aborted (nobody listens).
#[tokio::test(flavor = "current_thread")]
async fn a_client_that_holds_a_connection_open_cannot_hold_the_stop() {
    let world = World::new().await;
    let listener = world.serve().await;
    let mut held = tokio::net::UnixStream::connect(&world.endpoint)
        .await
        .unwrap();
    tokio::io::AsyncWriteExt::write_all(&mut held, b"PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n")
        .await
        .unwrap();
    let error = listener
        .stop()
        .await
        .expect_err("the held connection outlasts the grace");
    assert_eq!(
        error,
        crate::ListenerError::StopTimedOut {
            endpoint: world.endpoint.display().to_string(),
            grace_ms: 3_000,
        }
    );
    assert!(!present(&world.endpoint), "the socket is removed");
    assert!(connect(&world.endpoint).await.is_err(), "nobody listens");
    drop(held);
}

/// A stop with nothing open ends within the grace with Ok, and leaves no
/// socket: the success the timeout path must differ from.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn a_stop_with_nothing_open_is_ok_within_the_grace() {
    let world = World::new().await;
    let listener = world.serve().await;
    listener
        .stop_within(std::time::Duration::from_secs(30))
        .await
        .expect("stops");
    assert!(!present(&world.endpoint));
}
