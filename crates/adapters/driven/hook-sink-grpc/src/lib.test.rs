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
use run_ports::{Bearer, Delivery, FactSink, Token};
use tonic::{Request, Response, Status};

use super::*;

const SECRET_RUN: &str = "0a1b2c3d-0000-4000-8000-000000000001";

fn bearer() -> Bearer {
    Bearer::compose(SECRET_RUN, &Token::from_bytes([0xab; 32]))
}

/// A loopback URL nobody listens on: a port that was bound, then released.
fn closed_url() -> String {
    let probe = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
    let port = probe.local_addr().unwrap().port();
    drop(probe);
    format!("http://127.0.0.1:{port}")
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
    let seen = Arc::new(Mutex::new(Vec::new()));
    let (arrived_tx, arrived) = mpsc::channel();
    let (stop, stopped) = mpsc::channel::<()>();
    let (ready_tx, ready) = mpsc::channel();
    let (url_tx, url_rx) = mpsc::channel();
    let fake = Fake {
        answer,
        seen: Arc::clone(&seen),
        arrived: arrived_tx,
    };
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
            url_tx.send(format!("http://127.0.0.1:{port}")).unwrap();
            tokio::net::TcpListener::from_std(std_listener).unwrap()
        };
        ready_tx.send(()).unwrap();
        let incoming = tokio_stream::wrappers::TcpListenerStream::new(listener);
        runtime.block_on(async {
            let server = tonic::transport::Server::builder()
                .add_service(CommandHandlerCoordinatorServiceServer::new(fake))
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

#[test]
fn an_applied_fact_is_applied_and_carries_the_bearer() {
    let server = serve(Answer::Result(DeliveryOutcome::Applied));
    let sink = GrpcSink::new(server.url.clone(), bearer());
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
    let sink = GrpcSink::new(server.url.clone(), bearer());
    assert_eq!(
        sink.deliver(CommandBook::default(), BUDGET),
        Delivery::Duplicate
    );
}

#[test]
fn a_result_with_no_outcome_is_refused() {
    let server = serve(Answer::Result(DeliveryOutcome::Unspecified));
    let sink = GrpcSink::new(server.url.clone(), bearer());
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
    let sink = GrpcSink::new(server.url.clone(), bearer());
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
    let sink = GrpcSink::new(server.url.clone(), bearer());
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
    let sink = GrpcSink::new(server.url.clone(), bearer());
    assert_eq!(
        sink.deliver(CommandBook::default(), BUDGET),
        Delivery::TimedOut
    );
}

#[test]
fn a_server_that_never_answers_times_out_within_the_budget() {
    let server = serve(Answer::Never);
    let sink = GrpcSink::new(server.url.clone(), bearer());
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
    let sink = GrpcSink::new(closed_url(), bearer());
    let started = Instant::now();
    match sink.deliver(CommandBook::default(), BUDGET) {
        Delivery::Unavailable { detail } => assert!(detail.contains("cannot reach"), "{detail}"),
        other => panic!("{other:?}"),
    }
    assert!(started.elapsed() < Duration::from_secs(2));
}

#[test]
fn a_stopped_server_is_unavailable() {
    let mut server = serve(Answer::Never);
    let url = server.url.clone();
    server.stop();
    let sink = GrpcSink::new(url, bearer());
    assert!(matches!(
        sink.deliver(CommandBook::default(), BUDGET),
        Delivery::Unavailable { .. }
    ));
}

#[test]
fn what_is_not_a_listener_url_is_unavailable_and_says_so() {
    for endpoint in ["/run/user/1000/ctxloom/a.sock", "", "http://h", "ftp://h:9"] {
        let sink = GrpcSink::new(endpoint, bearer());
        match sink.deliver(CommandBook::default(), BUDGET) {
            Delivery::Unavailable { detail } => {
                assert!(detail.contains("endpoint"), "{endpoint}: {detail}")
            }
            other => panic!("{other:?}"),
        }
    }
}

#[test]
fn an_https_endpoint_is_unavailable_until_the_sink_has_a_certificate() {
    let sink = GrpcSink::new("https://127.0.0.1:9", bearer());
    match sink.deliver(CommandBook::default(), BUDGET) {
        Delivery::Unavailable { detail } => assert!(detail.contains("https"), "{detail}"),
        other => panic!("{other:?}"),
    }
}

#[test]
fn a_server_that_closes_mid_call_is_unavailable_not_timed_out() {
    let mut server = serve(Answer::Never);
    let sink = GrpcSink::new(server.url.clone(), bearer());
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
    let sink = GrpcSink::new(closed_url(), bearer());
    assert!(!format!("{sink:?}").contains(&secret));
    // Down: a transport failure.
    let down = sink.deliver(CommandBook::default(), BUDGET);
    assert!(!format!("{down:?}").contains(&secret));
    // A server that echoes the bearer in its refusal: scrubbed.
    let echoed: &'static str = Box::leak(format!("bad bearer {secret}").into_boxed_str());
    let server = serve(Answer::Fail(Code::Unauthenticated, echoed));
    let sink = GrpcSink::new(server.url.clone(), bearer());
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
    let sink = GrpcSink::new(server.url.clone(), bearer());
    let runtime = tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()
        .unwrap();
    let delivery = runtime.block_on(async { sink.deliver(CommandBook::default(), BUDGET) });
    assert_eq!(delivery, Delivery::Applied);
}

#[test]
fn a_sink_debugs_as_its_endpoint_with_the_bearer_redacted() {
    let sink = GrpcSink::new("http://127.0.0.1:9", bearer());
    let shown = format!("{sink:?}");
    assert_eq!(
        shown,
        r#"GrpcSink { endpoint: "http://127.0.0.1:9", bearer: Bearer(redacted) }"#
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
