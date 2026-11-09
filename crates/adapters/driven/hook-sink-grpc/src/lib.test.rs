//! The sink against a fake `CommandHandlerCoordinatorService` on a unix
//! socket in a short temporary directory. Each test forces the outcome it
//! concerns (a server that never answers, one stopped by the test while the
//! call is in flight); none waits for an interleaving.

use std::path::{Path, PathBuf};
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
use file_system::{Fs, Rooted};
use run_ports::{Bearer, Delivery, FactSink, Token};
use tonic::{Request, Response, Status};

use super::*;

const SECRET_RUN: &str = "0a1b2c3d-0000-4000-8000-000000000001";

fn bearer() -> Bearer {
    Bearer::compose(SECRET_RUN, &Token::from_bytes([0xab; 32]))
}

/// A temporary directory of the file-system seam; its socket path must fit
/// `sun_path` (107 bytes).
struct Dir(Rooted);

impl Dir {
    fn new() -> Dir {
        let dir = Rooted::temp().expect("a temporary directory");
        assert!(dir.root().join("s").as_os_str().len() <= 107);
        Dir(dir)
    }

    fn socket(&self) -> PathBuf {
        self.0.root().join("s")
    }

    fn socket_exists(&self) -> bool {
        let caller = Path::new("/s");
        self.0.exists(caller).unwrap()
    }
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

fn serve(socket: &Path, answer: Answer) -> Running {
    let seen = Arc::new(Mutex::new(Vec::new()));
    let (arrived_tx, arrived) = mpsc::channel();
    let (stop, stopped) = mpsc::channel::<()>();
    let (ready_tx, ready) = mpsc::channel();
    let fake = Fake {
        answer,
        seen: Arc::clone(&seen),
        arrived: arrived_tx,
    };
    let socket = socket.to_path_buf();
    let thread = std::thread::spawn(move || {
        let runtime = tokio::runtime::Builder::new_current_thread()
            .enable_all()
            .build()
            .unwrap();
        let listener = {
            let _guard = runtime.enter();
            tokio::net::UnixListener::bind(&socket).expect("binds the socket")
        };
        ready_tx.send(()).unwrap();
        let incoming = tokio_stream::wrappers::UnixListenerStream::new(listener);
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
        stop: Some(stop),
        thread: Some(thread),
        seen,
        arrived,
    }
}

const BUDGET: Duration = Duration::from_secs(5);

#[test]
fn an_applied_fact_is_applied_and_carries_the_bearer() {
    let dir = Dir::new();
    let server = serve(&dir.socket(), Answer::Result(DeliveryOutcome::Applied));
    let sink = GrpcSink::new(dir.socket(), bearer());
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
    let dir = Dir::new();
    let _server = serve(&dir.socket(), Answer::Result(DeliveryOutcome::Duplicate));
    let sink = GrpcSink::new(dir.socket(), bearer());
    assert_eq!(
        sink.deliver(CommandBook::default(), BUDGET),
        Delivery::Duplicate
    );
}

#[test]
fn a_result_with_no_outcome_is_refused() {
    let dir = Dir::new();
    let _server = serve(&dir.socket(), Answer::Result(DeliveryOutcome::Unspecified));
    let sink = GrpcSink::new(dir.socket(), bearer());
    assert!(matches!(
        sink.deliver(CommandBook::default(), BUDGET),
        Delivery::Refused { .. }
    ));
}

#[test]
fn an_unauthenticated_call_is_refused_and_says_why() {
    let dir = Dir::new();
    let _server = serve(
        &dir.socket(),
        Answer::Fail(Code::Unauthenticated, "angzarr-coordinator-unauthenticated"),
    );
    let sink = GrpcSink::new(dir.socket(), bearer());
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
    let dir = Dir::new();
    let _server = serve(&dir.socket(), Answer::Fail(Code::Internal, DEAD_LETTERED));
    let sink = GrpcSink::new(dir.socket(), bearer());
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
    let dir = Dir::new();
    let _server = serve(&dir.socket(), Answer::Fail(Code::DeadlineExceeded, "slow"));
    let sink = GrpcSink::new(dir.socket(), bearer());
    assert_eq!(
        sink.deliver(CommandBook::default(), BUDGET),
        Delivery::TimedOut
    );
}

#[test]
fn a_server_that_never_answers_times_out_within_the_budget() {
    let dir = Dir::new();
    let server = serve(&dir.socket(), Answer::Never);
    let sink = GrpcSink::new(dir.socket(), bearer());
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
fn a_missing_socket_is_unavailable_at_once() {
    let dir = Dir::new();
    let sink = GrpcSink::new(dir.socket(), bearer());
    let started = Instant::now();
    match sink.deliver(CommandBook::default(), BUDGET) {
        Delivery::Unavailable { detail } => assert!(detail.contains("cannot reach"), "{detail}"),
        other => panic!("{other:?}"),
    }
    assert!(started.elapsed() < Duration::from_secs(2));
}

#[test]
fn a_socket_nobody_listens_on_is_unavailable() {
    let dir = Dir::new();
    // Served, then stopped: the socket file stays, the connection is refused.
    serve(&dir.socket(), Answer::Never).stop();
    assert!(dir.socket_exists());
    let sink = GrpcSink::new(dir.socket(), bearer());
    assert!(matches!(
        sink.deliver(CommandBook::default(), BUDGET),
        Delivery::Unavailable { .. }
    ));
}

#[test]
fn a_server_that_closes_mid_call_is_unavailable_not_timed_out() {
    let dir = Dir::new();
    let mut server = serve(&dir.socket(), Answer::Never);
    let sink = GrpcSink::new(dir.socket(), bearer());
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
    let sink = GrpcSink::new("/nonexistent/s", bearer());
    assert!(!format!("{sink:?}").contains(&secret));
    // Down: a transport failure.
    let down = sink.deliver(CommandBook::default(), BUDGET);
    assert!(!format!("{down:?}").contains(&secret));
    // A server that echoes the bearer in its refusal: scrubbed.
    let dir = Dir::new();
    let echoed: &'static str = Box::leak(format!("bad bearer {secret}").into_boxed_str());
    let _server = serve(&dir.socket(), Answer::Fail(Code::Unauthenticated, echoed));
    let sink = GrpcSink::new(dir.socket(), bearer());
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
    let dir = Dir::new();
    let _server = serve(&dir.socket(), Answer::Result(DeliveryOutcome::Applied));
    let sink = GrpcSink::new(dir.socket(), bearer());
    let runtime = tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()
        .unwrap();
    let delivery = runtime.block_on(async { sink.deliver(CommandBook::default(), BUDGET) });
    assert_eq!(delivery, Delivery::Applied);
}
