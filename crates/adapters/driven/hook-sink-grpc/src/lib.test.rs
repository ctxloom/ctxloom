//! The sink against a fake `CommandHandlerCoordinatorService` on a loopback
//! TCP port (port 0, the URL read back from the bound listener). Each test forces the outcome it
//! concerns (a server that never answers, one stopped by the test while the
//! call is in flight); none waits for an interleaving.

use std::sync::mpsc;
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use angzarr_embedded::v1::{CommandBook, DeliveryOutcome};
use angzarr_proto_transport_distributed::v1::command_handler_coordinator_service_server::{
    CommandHandlerCoordinatorService, CommandHandlerCoordinatorServiceServer,
};
use angzarr_proto_transport_distributed::v1::{
    CommandResult, CommandSyncResult, HandleSyncRequest,
};
use hook_tls::{Clock, Minter, SystemClock};
use run_ports::{Bearer, CertMint, CertNames, Delivery, FactSink, HookCertificate, Token};
use tonic::transport::{Identity, ServerTlsConfig};
use tonic::{Request, Response, Status};

use super::*;

const SECRET_RUN: &str = "0a1b2c3d-0000-4000-8000-000000000001";

fn bearer() -> Bearer {
    Bearer::compose(SECRET_RUN, &Token::from_bytes([0xab; 32]))
}

#[derive(Debug)]
struct At(i64);

impl Clock for At {
    fn unix_seconds(&self) -> i64 {
        self.0
    }
}

/// 2020-01-01T00:00:00Z: a certificate minted then is long expired.
const LONG_AGO: i64 = 1_577_836_800;

/// A certificate for `ips` and `dns`, minted at `clock`.
fn mint(clock: impl Clock + 'static, ips: &[&str], dns: &[&str]) -> HookCertificate {
    Minter::new(clock)
        .mint(&CertNames::new(
            ips.iter().map(|ip| ip.parse().unwrap()).collect(),
            dns.iter().map(|d| d.to_string()).collect(),
        ))
        .unwrap()
}

/// The run's certificate for the loopback, valid now: the server's and the
/// sink's, unless a test says otherwise.
fn cert() -> &'static HookCertificate {
    static CERT: std::sync::OnceLock<HookCertificate> = std::sync::OnceLock::new();
    CERT.get_or_init(|| mint(SystemClock, &["127.0.0.1", "::1"], &[]))
}

/// A certificate of a test's own, for a sink that dials a port nobody is meant
/// to listen on: a port released (or a server stopped) can be given at once to
/// another test's server in the same process (cargo test, the mutation run),
/// and every one of those presents the shared [`cert`], so a sink pinning
/// [`cert`] would be answered there. Pinning this one, nobody can answer.
fn own_cert() -> HookCertificate {
    mint(SystemClock, &["127.0.0.1", "::1"], &[])
}

/// A sink that pins `cert` and dials `url`.
fn sink_to(url: impl Into<String>, cert: &HookCertificate) -> GrpcSink {
    GrpcSink::new(url, bearer(), cert.public().clone())
}

/// A loopback URL nobody listens on: a port that was bound, then released.
fn closed_url() -> String {
    let probe = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
    let port = probe.local_addr().unwrap().port();
    drop(probe);
    format!("https://127.0.0.1:{port}")
}

#[derive(Clone)]
enum Answer {
    Result(DeliveryOutcome),
    Fail(Code, &'static str),
    /// Tells the test the call arrived, then never answers.
    Never,
}

struct Fake {
    answer: Answer,
    /// The `authorization` metadata of each call.
    seen: Arc<Mutex<Vec<Option<String>>>>,
    arrived: mpsc::Sender<()>,
}

#[tonic::async_trait]
impl CommandHandlerCoordinatorService for Fake {
    async fn handle(
        &self,
        request: Request<CommandBook>,
    ) -> Result<Response<CommandResult>, Status> {
        let auth = request
            .metadata()
            .get("authorization")
            .and_then(|v| v.to_str().ok())
            .map(str::to_string);
        self.seen.lock().unwrap().push(auth);
        let _ = self.arrived.send(());
        match &self.answer {
            Answer::Result(outcome) => Ok(Response::new(CommandResult {
                events: None,
                delivery: (*outcome).into(),
            })),
            Answer::Fail(code, message) => Err(Status::new(*code, *message)),
            Answer::Never => std::future::pending().await,
        }
    }

    async fn handle_async(&self, _: Request<CommandBook>) -> Result<Response<()>, Status> {
        Err(Status::unimplemented("handle_async"))
    }

    async fn handle_sync(
        &self,
        _: Request<HandleSyncRequest>,
    ) -> Result<Response<CommandSyncResult>, Status> {
        Err(Status::unimplemented("handle_sync"))
    }

    async fn deliver(
        &self,
        _: Request<angzarr_embedded::v1::ReactionDelivery>,
    ) -> Result<Response<CommandResult>, Status> {
        Err(Status::unimplemented("deliver"))
    }
}

/// How a fake is served.
enum Serving<'a> {
    /// TLS, with this identity.
    Tls(&'a HookCertificate),
    /// Plaintext: what the sink must never talk to.
    Plain,
}

/// A running fake; stopping it (or dropping it) drops its runtime, which
/// closes every connection at once, even one with a call in flight.
struct Running {
    url: String,
    stop: Option<mpsc::Sender<()>>,
    thread: Option<std::thread::JoinHandle<()>>,
    seen: Arc<Mutex<Vec<Option<String>>>>,
    arrived: mpsc::Receiver<()>,
}

impl Running {
    /// The sink of the run's certificate, to this fake.
    fn sink(&self) -> GrpcSink {
        sink_to(self.url.clone(), cert())
    }

    /// The URL with its host replaced.
    fn url_at(&self, host: &str) -> String {
        self.url.replace("127.0.0.1", host)
    }

    fn stop(&mut self) {
        drop(self.stop.take());
        if let Some(thread) = self.thread.take() {
            thread.join().expect("the fake server stops");
        }
    }
}

impl Drop for Running {
    fn drop(&mut self) {
        self.stop();
    }
}

fn serve(answer: Answer) -> Running {
    serve_as(answer, Serving::Tls(cert()))
}

fn serve_as(answer: Answer, serving: Serving<'_>) -> Running {
    serve_with(Some(answer), serving, None)
}

/// A fake serving the health check alone, answering `status` (TLS, the
/// run's certificate).
fn serve_health(status: i32) -> (Running, Arc<Mutex<Vec<Option<String>>>>) {
    let seen = Arc::new(Mutex::new(Vec::new()));
    let health = FakeHealth {
        status,
        seen: Arc::clone(&seen),
    };
    (serve_with(None, Serving::Tls(cert()), Some(health)), seen)
}

/// `grpc.health.v1.Health`'s Check, by hand (as the sink's client is): it
/// answers `status` and keeps each call's `authorization` metadata.
#[derive(Clone)]
struct FakeHealth {
    status: i32,
    seen: Arc<Mutex<Vec<Option<String>>>>,
}

impl tonic::server::NamedService for FakeHealth {
    const NAME: &'static str = "grpc.health.v1.Health";
}

/// The Check's handler.
struct Check(i32);

impl tonic::server::UnaryService<health::HealthCheckRequest> for Check {
    type Response = health::HealthCheckResponse;
    type Future = tonic::codegen::BoxFuture<Response<health::HealthCheckResponse>, Status>;

    fn call(&mut self, _: Request<health::HealthCheckRequest>) -> Self::Future {
        let status = self.0;
        Box::pin(async move { Ok(Response::new(health::HealthCheckResponse { status })) })
    }
}

impl tonic::codegen::Service<tonic::codegen::http::Request<tonic::body::Body>> for FakeHealth {
    type Response = tonic::codegen::http::Response<tonic::body::Body>;
    type Error = std::convert::Infallible;
    type Future = tonic::codegen::BoxFuture<Self::Response, Self::Error>;

    fn poll_ready(
        &mut self,
        _: &mut std::task::Context<'_>,
    ) -> std::task::Poll<Result<(), Self::Error>> {
        std::task::Poll::Ready(Ok(()))
    }

    fn call(&mut self, request: tonic::codegen::http::Request<tonic::body::Body>) -> Self::Future {
        let auth = request
            .headers()
            .get("authorization")
            .and_then(|v| v.to_str().ok())
            .map(str::to_string);
        self.seen.lock().unwrap().push(auth);
        let status = self.status;
        Box::pin(async move {
            let codec =
                health::Prost::<health::HealthCheckResponse, health::HealthCheckRequest>::default();
            Ok(tonic::server::Grpc::new(codec)
                .unary(Check(status), request)
                .await)
        })
    }
}

fn serve_with(answer: Option<Answer>, serving: Serving<'_>, health: Option<FakeHealth>) -> Running {
    let tls = match serving {
        Serving::Tls(cert) => Some(Identity::from_pem(cert.public().to_pem(), cert.key_pem())),
        Serving::Plain => None,
    };
    let scheme = if tls.is_some() { "https" } else { "http" };
    let seen = Arc::new(Mutex::new(Vec::new()));
    let (arrived_tx, arrived) = mpsc::channel();
    let (stop, stopped) = mpsc::channel::<()>();
    let (ready_tx, ready) = mpsc::channel();
    let (url_tx, url_rx) = mpsc::channel();
    let fake = answer.map(|answer| Fake {
        answer,
        seen: Arc::clone(&seen),
        arrived: arrived_tx,
    });
    let thread = std::thread::spawn(move || {
        let runtime = tokio::runtime::Builder::new_current_thread()
            .enable_all()
            .build()
            .unwrap();
        let listener = {
            let _guard = runtime.enter();
            let std_listener = std::net::TcpListener::bind("127.0.0.1:0").expect("binds a port");
            std_listener.set_nonblocking(true).unwrap();
            let port = std_listener.local_addr().unwrap().port();
            url_tx.send(format!("{scheme}://127.0.0.1:{port}")).unwrap();
            tokio::net::TcpListener::from_std(std_listener).unwrap()
        };
        ready_tx.send(()).unwrap();
        let incoming = tokio_stream::wrappers::TcpListenerStream::new(listener);
        runtime.block_on(async {
            let mut builder = tonic::transport::Server::builder();
            if let Some(identity) = tls {
                builder = builder
                    .tls_config(ServerTlsConfig::new().identity(identity))
                    .unwrap();
            }
            let server = builder
                .add_optional_service(fake.map(CommandHandlerCoordinatorServiceServer::new))
                .add_optional_service(health)
                .serve_with_incoming(incoming);
            let stopped = async move {
                let _ = tokio::task::spawn_blocking(move || stopped.recv()).await;
            };
            tokio::select! {
                _ = server => {}
                _ = stopped => {}
            }
        });
        runtime.shutdown_background();
    });
    ready.recv().expect("the fake server is listening");
    Running {
        url: url_rx.recv().unwrap(),
        stop: Some(stop),
        thread: Some(thread),
        seen,
        arrived,
    }
}

const BUDGET: Duration = Duration::from_secs(5);

/// The health check's SERVING and NOT_SERVING.
const SERVING: i32 = 1;
const NOT_SERVING: i32 = 2;

// T16, M11: liveness is the standard health check over the pinned
// certificate, and presents no bearer.
#[test]
fn a_listener_answering_serving_over_the_pinned_certificate_serves() {
    let (server, seen) = serve_health(SERVING);
    assert!(serving(&server.url, cert().public(), BUDGET));
    assert_eq!(*seen.lock().unwrap(), vec![None], "no bearer is presented");
}

#[test]
fn a_listener_answering_not_serving_does_not_serve() {
    let (server, _) = serve_health(NOT_SERVING);
    assert!(!serving(&server.url, cert().public(), BUDGET));
}

#[test]
fn a_listener_whose_certificate_is_not_the_pinned_one_does_not_serve_and_hears_nothing() {
    let (server, seen) = serve_health(SERVING);
    assert!(!serving(&server.url, own_cert().public(), BUDGET));
    assert!(seen.lock().unwrap().is_empty(), "no call reached it");
}

#[test]
fn a_port_nobody_listens_on_does_not_serve() {
    assert!(!serving(&closed_url(), own_cert().public(), BUDGET));
}

#[test]
fn a_listener_with_no_health_service_does_not_serve() {
    let server = serve(Answer::Result(DeliveryOutcome::Applied));
    assert!(!serving(&server.url, cert().public(), BUDGET));
}

#[test]
fn a_plaintext_endpoint_is_never_checked() {
    let (server, seen) = serve_health(SERVING);
    let plain = server.url.replace("https://", "http://");
    assert!(!serving(&plain, cert().public(), BUDGET));
    assert!(seen.lock().unwrap().is_empty());
}

#[test]
fn a_listener_that_never_answers_is_not_serving_once_the_deadline_passes() {
    // A TLS port that accepts and never completes a handshake.
    let silent = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
    let url = format!("https://127.0.0.1:{}", silent.local_addr().unwrap().port());
    let started = Instant::now();
    assert!(!serving(
        &url,
        own_cert().public(),
        Duration::from_millis(300)
    ));
    assert!(started.elapsed() < Duration::from_secs(3), "bounded");
}

#[test]
fn an_applied_fact_is_applied_and_carries_the_bearer() {
    let server = serve(Answer::Result(DeliveryOutcome::Applied));
    let sink = server.sink();
    let started = Instant::now();
    assert_eq!(
        sink.deliver(CommandBook::default(), BUDGET),
        Delivery::Applied
    );
    // Informational: the wall time of one delivery, connect included.
    eprintln!("delivery wall time: {:?}", started.elapsed());
    assert_eq!(
        *server.seen.lock().unwrap(),
        vec![Some(format!("Bearer {}", bearer().expose()))]
    );
}

#[test]
fn a_duplicate_is_an_answer_not_an_error() {
    let server = serve(Answer::Result(DeliveryOutcome::Duplicate));
    let sink = server.sink();
    assert_eq!(
        sink.deliver(CommandBook::default(), BUDGET),
        Delivery::Duplicate
    );
}

#[test]
fn a_result_with_no_outcome_is_refused() {
    let server = serve(Answer::Result(DeliveryOutcome::Unspecified));
    let sink = server.sink();
    assert!(matches!(
        sink.deliver(CommandBook::default(), BUDGET),
        Delivery::Refused { .. }
    ));
}

#[test]
fn an_unauthenticated_call_is_refused_and_says_why() {
    let server = serve(Answer::Fail(
        Code::Unauthenticated,
        "angzarr-coordinator-unauthenticated",
    ));
    let sink = server.sink();
    match sink.deliver(CommandBook::default(), BUDGET) {
        Delivery::Refused { detail } => {
            assert!(
                detail.contains("angzarr-coordinator-unauthenticated"),
                "{detail}"
            );
            assert!(detail.contains("Unauthenticated"), "{detail}");
        }
        other => panic!("{other:?}"),
    }
}

#[test]
fn each_status_maps_as_the_table_says() {
    let cases = [
        (Code::PermissionDenied, "refused"),
        (Code::NotFound, "refused"),
        (Code::InvalidArgument, "refused"),
        (Code::FailedPrecondition, "refused"),
        (Code::AlreadyExists, "refused"),
        (Code::Unimplemented, "refused"),
        (Code::Unavailable, "unavailable"),
        (Code::Aborted, "unavailable"),
        (Code::ResourceExhausted, "unavailable"),
        (Code::Unknown, "unavailable"),
        (Code::Internal, "unavailable"),
        (Code::DataLoss, "unavailable"),
        (Code::DeadlineExceeded, "timed out"),
    ];
    for (code, expected) in cases {
        let kind = match delivery_of_code(code, "m", "") {
            Delivery::Refused { .. } => "refused",
            Delivery::Unavailable { .. } => "unavailable",
            Delivery::TimedOut => "timed out",
            other => panic!("{code:?}: {other:?}"),
        };
        assert_eq!(kind, expected, "{code:?}");
    }
}

#[test]
fn a_dead_lettered_fact_is_parked_by_message_or_by_reason() {
    assert_eq!(
        delivery_of_code(Code::Internal, DEAD_LETTERED, ""),
        Delivery::Parked
    );
    assert_eq!(
        delivery_of_code(Code::Internal, "x", DEAD_LETTERED),
        Delivery::Parked
    );
    // Only INTERNAL parks.
    assert!(matches!(
        delivery_of_code(Code::NotFound, DEAD_LETTERED, DEAD_LETTERED),
        Delivery::Refused { .. }
    ));
}

#[test]
fn a_dead_lettered_fact_over_the_wire_is_parked() {
    let server = serve(Answer::Fail(Code::Internal, DEAD_LETTERED));
    let sink = server.sink();
    assert_eq!(
        sink.deliver(CommandBook::default(), BUDGET),
        Delivery::Parked
    );
}

#[test]
fn the_reason_of_an_error_info_detail_is_read() {
    let info = ErrorInfo {
        reason: DEAD_LETTERED.to_string(),
    };
    let rpc = RpcStatus {
        code: Code::Internal as i32,
        message: "other".to_string(),
        details: vec![prost_types::Any {
            type_url: "type.googleapis.com/google.rpc.ErrorInfo".to_string(),
            value: info.encode_to_vec(),
        }],
    };
    let status = Status::with_details(Code::Internal, "other", rpc.encode_to_vec().into());
    assert_eq!(reason_of(&status), DEAD_LETTERED);
    assert_eq!(reason_of(&Status::internal("plain")), "");
}

#[test]
fn a_server_deadline_status_is_timed_out() {
    let server = serve(Answer::Fail(Code::DeadlineExceeded, "slow"));
    let sink = server.sink();
    assert_eq!(
        sink.deliver(CommandBook::default(), BUDGET),
        Delivery::TimedOut
    );
}

#[test]
fn a_server_that_never_answers_times_out_within_the_budget() {
    let server = serve(Answer::Never);
    let sink = server.sink();
    let budget = Duration::from_millis(300);
    let started = Instant::now();
    assert_eq!(
        sink.deliver(CommandBook::default(), budget),
        Delivery::TimedOut
    );
    let elapsed = started.elapsed();
    // The call did arrive: the deadline, not a refusal, ended it.
    assert!(server.arrived.try_recv().is_ok());
    assert!(elapsed >= budget, "{elapsed:?}");
    assert!(elapsed < budget + Duration::from_secs(2), "{elapsed:?}");
}

#[test]
fn a_port_nobody_listens_on_is_unavailable_at_once() {
    let sink = sink_to(closed_url(), &own_cert());
    let started = Instant::now();
    match sink.deliver(CommandBook::default(), BUDGET) {
        Delivery::Unavailable { detail } => assert!(detail.contains("cannot reach"), "{detail}"),
        other => panic!("{other:?}"),
    }
    assert!(started.elapsed() < Duration::from_secs(2));
}

#[test]
fn a_stopped_server_is_unavailable() {
    let own = own_cert();
    let mut server = serve_as(Answer::Never, Serving::Tls(&own));
    let url = server.url.clone();
    server.stop();
    let sink = sink_to(url, &own);
    assert!(matches!(
        sink.deliver(CommandBook::default(), BUDGET),
        Delivery::Unavailable { .. }
    ));
}

#[test]
fn what_is_not_a_listener_url_is_unavailable_and_says_so() {
    for endpoint in ["/run/user/1000/ctxloom/a.sock", "", "http://h", "ftp://h:9"] {
        let sink = sink_to(endpoint, cert());
        match sink.deliver(CommandBook::default(), BUDGET) {
            Delivery::Unavailable { detail } => {
                assert!(detail.contains("endpoint"), "{endpoint}: {detail}")
            }
            other => panic!("{other:?}"),
        }
    }
}

// No downgrade: an http endpoint is never dialed and nothing is sent, even to
// a listener that would answer it. It is unavailable, not a refusal, so the
// hook says it did not record.
#[test]
fn a_plaintext_endpoint_is_never_dialed_and_nothing_is_sent() {
    let server = serve_as(Answer::Result(DeliveryOutcome::Applied), Serving::Plain);
    assert!(server.url.starts_with("http://"));
    match server.sink().deliver(CommandBook::default(), BUDGET) {
        Delivery::Unavailable { detail } => assert!(detail.contains("plaintext"), "{detail}"),
        other => panic!("{other:?}"),
    }
    assert!(server.seen.lock().unwrap().is_empty());
}

// The sink never falls back: an https endpoint whose listener speaks no TLS is
// unavailable, and the bearer is never sent to it.
#[test]
fn a_listener_that_speaks_no_tls_is_unavailable_and_sees_nothing() {
    let server = serve_as(Answer::Result(DeliveryOutcome::Applied), Serving::Plain);
    let url = server.url.replacen("http://", "https://", 1);
    assert!(matches!(
        sink_to(url, cert()).deliver(CommandBook::default(), BUDGET),
        Delivery::Unavailable { .. }
    ));
    assert!(server.seen.lock().unwrap().is_empty());
}

// The certificate is the sole root: a different one, for the same address, is
// refused and nothing is sent; the pinned one, on the same server, is applied.
#[test]
fn a_listener_with_another_certificate_is_refused_and_sees_nothing() {
    let served = mint(SystemClock, &["127.0.0.1"], &[]);
    let server = serve_as(
        Answer::Result(DeliveryOutcome::Applied),
        Serving::Tls(&served),
    );
    assert_eq!(
        sink_to(server.url.clone(), &served).deliver(CommandBook::default(), BUDGET),
        Delivery::Applied,
        "the control"
    );
    let seen = server.seen.lock().unwrap().len();
    let other = mint(SystemClock, &["127.0.0.1"], &[]);
    assert!(matches!(
        sink_to(server.url.clone(), &other).deliver(CommandBook::default(), BUDGET),
        Delivery::Unavailable { .. }
    ));
    assert_eq!(server.seen.lock().unwrap().len(), seen, "nothing was sent");
}

// An expired certificate is refused even when it is the pinned root.
#[test]
fn an_expired_certificate_is_refused_and_sees_nothing() {
    let expired = mint(At(LONG_AGO), &["127.0.0.1"], &[]);
    let server = serve_as(
        Answer::Result(DeliveryOutcome::Applied),
        Serving::Tls(&expired),
    );
    assert!(matches!(
        sink_to(server.url.clone(), &expired).deliver(CommandBook::default(), BUDGET),
        Delivery::Unavailable { .. }
    ));
    assert!(server.seen.lock().unwrap().is_empty());
    let fresh = serve(Answer::Result(DeliveryOutcome::Applied));
    assert_eq!(
        fresh.sink().deliver(CommandBook::default(), BUDGET),
        Delivery::Applied,
        "the control"
    );
}

// An IP literal in the URL is checked against the certificate's IP names: a
// certificate for another address does not serve this one.
#[test]
fn an_ip_the_certificate_does_not_name_is_refused() {
    let wrong = mint(SystemClock, &["127.0.0.2"], &[]);
    let server = serve_as(
        Answer::Result(DeliveryOutcome::Applied),
        Serving::Tls(&wrong),
    );
    assert!(matches!(
        sink_to(server.url.clone(), &wrong).deliver(CommandBook::default(), BUDGET),
        Delivery::Unavailable { .. }
    ));
    assert!(server.seen.lock().unwrap().is_empty());
}

// A host name in the URL (a runtime's host alias) is checked against the
// certificate's DNS names.
#[test]
fn a_dns_name_is_checked_against_the_certificate() {
    let named = mint(SystemClock, &["127.0.0.1"], &["localhost"]);
    let server = serve_as(
        Answer::Result(DeliveryOutcome::Applied),
        Serving::Tls(&named),
    );
    assert_eq!(
        sink_to(server.url_at("localhost"), &named).deliver(CommandBook::default(), BUDGET),
        Delivery::Applied
    );
    let unnamed = mint(SystemClock, &["127.0.0.1"], &["other.example"]);
    let server = serve_as(
        Answer::Result(DeliveryOutcome::Applied),
        Serving::Tls(&unnamed),
    );
    assert!(matches!(
        sink_to(server.url_at("localhost"), &unnamed).deliver(CommandBook::default(), BUDGET),
        Delivery::Unavailable { .. }
    ));
    assert!(server.seen.lock().unwrap().is_empty());
}

// The bearer is still required over TLS: the listener's refusal comes back as
// the same refusal, and the sink sent the bearer it holds.
#[test]
fn a_wrong_bearer_over_tls_is_the_listeners_refusal() {
    let server = serve(Answer::Fail(
        Code::Unauthenticated,
        "angzarr-coordinator-unauthenticated",
    ));
    assert!(matches!(
        server.sink().deliver(CommandBook::default(), BUDGET),
        Delivery::Refused { .. }
    ));
    assert_eq!(
        *server.seen.lock().unwrap(),
        vec![Some(format!("Bearer {}", bearer().expose()))]
    );
}

#[test]
fn a_server_that_closes_mid_call_is_unavailable_not_timed_out() {
    let mut server = serve(Answer::Never);
    let sink = server.sink();
    let budget = Duration::from_secs(20);
    let started = Instant::now();
    let delivered = std::thread::scope(|scope| {
        let call = scope.spawn(|| sink.deliver(CommandBook::default(), budget));
        // Forced: the call is in the handler before the server is closed.
        server
            .arrived
            .recv_timeout(Duration::from_secs(10))
            .expect("the call arrives");
        server.stop();
        call.join().unwrap()
    });
    assert!(
        matches!(delivered, Delivery::Unavailable { .. }),
        "{delivered:?}"
    );
    assert!(started.elapsed() < Duration::from_secs(10));
}

#[test]
fn the_bearer_never_appears_in_a_debug_or_a_detail() {
    let secret = bearer().expose().to_string();
    let sink = sink_to(closed_url(), &own_cert());
    assert!(!format!("{sink:?}").contains(&secret));
    // Down: a transport failure.
    let down = sink.deliver(CommandBook::default(), BUDGET);
    assert!(!format!("{down:?}").contains(&secret));
    // A server that echoes the bearer in its refusal: scrubbed.
    let echoed: &'static str = Box::leak(format!("bad bearer {secret}").into_boxed_str());
    let server = serve(Answer::Fail(Code::Unauthenticated, echoed));
    let sink = server.sink();
    let refused = sink.deliver(CommandBook::default(), BUDGET);
    let Delivery::Refused { detail } = &refused else {
        panic!("{refused:?}");
    };
    assert!(detail.contains("<redacted>"), "{detail}");
    assert!(!format!("{refused:?}").contains(&secret));
    // Scrubbing is by the whole text, and an empty one scrubs nothing.
    assert_eq!(sink.scrub("a b".to_string()), "a b");
}

#[test]
fn delivery_may_be_called_from_inside_a_runtime() {
    let server = serve(Answer::Result(DeliveryOutcome::Applied));
    let sink = server.sink();
    let runtime = tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()
        .unwrap();
    let delivery = runtime.block_on(async { sink.deliver(CommandBook::default(), BUDGET) });
    assert_eq!(delivery, Delivery::Applied);
}

#[test]
fn a_sink_debugs_as_its_endpoint_with_the_bearer_redacted() {
    let sink = sink_to("https://127.0.0.1:9", cert());
    let shown = format!("{sink:?}");
    assert_eq!(
        shown,
        format!(
            r#"GrpcSink {{ endpoint: "https://127.0.0.1:9", bearer: Bearer(redacted), cert: {:?} }}"#,
            cert().public()
        )
    );
    assert!(!shown.contains(bearer().expose()));
}

#[derive(Debug)]
struct Layer(&'static str, Option<Box<Layer>>);

impl std::fmt::Display for Layer {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(self.0)
    }
}

impl std::error::Error for Layer {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        self.1
            .as_deref()
            .map(|e| e as &(dyn std::error::Error + 'static))
    }
}

#[test]
fn an_error_chain_is_one_line_of_its_causes_outermost_first() {
    let error = Layer(
        "transport error",
        Some(Box::new(Layer(
            "connection failed",
            Some(Box::new(Layer("No such file", None))),
        ))),
    );
    assert_eq!(
        chain(&error),
        "transport error: connection failed: No such file"
    );
    assert_eq!(chain(&Layer("alone", None)), "alone");
}
