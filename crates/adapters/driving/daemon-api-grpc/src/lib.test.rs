//! The service on a loopback port, over use cases a test scripts: the
//! user bearer admits, anything else is a bare UNAUTHENTICATED, a refusal
//! travels in the status's details, an RPC not served is UNIMPLEMENTED,
//! and a use case runs off the async workers (M10).

use std::sync::{mpsc, Arc, Mutex};
use std::time::Duration;

use daemon_api::{Answer, ApiError, ApiRefusal, Calls};
use daemon_proto::service::ctxloom_client::CtxloomClient;
use daemon_proto::v1;
use prost::Message;
use runs_application::UserGate;
use tonic::transport::{Channel, Server};
use tonic::{Code, Request};

const BEARER: &str = "11111111111111111111111111111111111111111111111111111111111111aa"; // gitleaks:allow

/// Use cases a test scripts: `get_daemon` waits for `release` (if set)
/// before it answers; `stop_daemon` refuses `DaemonHosting`.
#[derive(Default)]
struct Scripted {
    release: Mutex<Option<mpsc::Receiver<()>>>,
}

impl Calls for Scripted {
    fn get_daemon(&self) -> Answer<'_, v1::Daemon> {
        let waiting = self.release.lock().unwrap().take();
        Box::pin(async move {
            if let Some(release) = waiting {
                // Blocks this thread: a worker's, were it run on one.
                release
                    .recv_timeout(Duration::from_secs(10))
                    .expect("released");
            }
            Ok(v1::Daemon {
                pid: 7,
                ..v1::Daemon::default()
            })
        })
    }

    fn stop_daemon(&self, _: v1::StopDaemonRequest) -> Answer<'_, v1::Daemon> {
        Box::pin(async {
            Err(ApiRefusal::of(&v1::DaemonHosting {
                runs: 1,
                harps: vec!["brisk-copper-moth".to_string()],
            })
            .into())
        })
    }

    fn resolve_project(&self, _: v1::ResolveProjectRequest) -> Answer<'_, v1::Project> {
        Box::pin(async {
            Err(ApiError::Failed {
                detail: "the store".to_string(),
            })
        })
    }

    fn get_project(&self, request: v1::GetProjectRequest) -> Answer<'_, v1::Project> {
        Box::pin(async move {
            Ok(v1::Project {
                name: request.name,
                ..v1::Project::default()
            })
        })
    }

    fn get_session(&self, request: v1::GetSessionRequest) -> Answer<'_, v1::Session> {
        Box::pin(async move {
            Ok(v1::Session {
                name: request.name,
                ..v1::Session::default()
            })
        })
    }

    fn list_sessions(
        &self,
        request: v1::ListSessionsRequest,
    ) -> Answer<'_, v1::ListSessionsResponse> {
        Box::pin(async move {
            Ok(v1::ListSessionsResponse {
                sessions: vec![v1::Session {
                    name: format!("{}/sessions/listed", request.parent),
                    ..v1::Session::default()
                }],
                ..v1::ListSessionsResponse::default()
            })
        })
    }

    fn search_sessions(
        &self,
        request: v1::SearchSessionsRequest,
    ) -> Answer<'_, v1::ListSessionsResponse> {
        Box::pin(async move {
            Ok(v1::ListSessionsResponse {
                sessions: vec![v1::Session {
                    name: format!("{}/sessions/{}", request.parent, request.words.join("-")),
                    ..v1::Session::default()
                }],
                ..v1::ListSessionsResponse::default()
            })
        })
    }

    fn delete_session(
        &self,
        request: v1::DeleteSessionRequest,
    ) -> Answer<'_, v1::DeleteSessionResponse> {
        Box::pin(async move {
            match request.validate_only {
                true => Ok(v1::DeleteSessionResponse {
                    name: request.name,
                    ..v1::DeleteSessionResponse::default()
                }),
                false => Err(daemon_api::ApiRefusal::of(&v1::RemovalRefused {
                    name: request.name,
                    reason: v1::RemovalRefusal::Kept as i32,
                    plan: Some(v1::DeleteSessionResponse {
                        authored: vec!["notes.md".to_string()],
                        ..v1::DeleteSessionResponse::default()
                    }),
                    ..v1::RemovalRefused::default()
                })
                .into()),
            }
        })
    }

    fn sweep_sessions(&self, _: v1::SweepSessionsRequest) -> Answer<'_, v1::SweepSessionsResponse> {
        Box::pin(async {
            Ok(v1::SweepSessionsResponse {
                purge_cutoff: "2026-08-14".to_string(),
                ..v1::SweepSessionsResponse::default()
            })
        })
    }
}

/// Serve `calls` on a loopback port of a runtime with ONE worker; a
/// channel to it.
async fn serve(calls: Arc<Scripted>) -> Channel {
    let token = run_token();
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    let service = crate::service(calls, UserGate::new(&token));
    tokio::spawn(
        Server::builder()
            .add_service(service)
            .serve_with_incoming(tokio_stream::wrappers::TcpListenerStream::new(listener)),
    );
    Channel::from_shared(format!("http://{addr}"))
        .unwrap()
        .connect()
        .await
        .unwrap()
}

fn run_token() -> run_ports::Token {
    run_ports::Token::from_text(BEARER).unwrap()
}

fn with_bearer<T>(message: T, bearer: &str) -> Request<T> {
    let mut request = Request::new(message);
    request
        .metadata_mut()
        .insert("authorization", format!("Bearer {bearer}").parse().unwrap());
    request
}

// The user bearer admits; the call is answered.
#[tokio::test(flavor = "multi_thread", worker_threads = 1)]
async fn the_user_bearer_is_answered() {
    let mut client = CtxloomClient::new(serve(Arc::default()).await);
    let name = "projects/calm-otter/sessions/amber-swift-owl".to_string();
    let session = client
        .get_session(with_bearer(
            v1::GetSessionRequest {
                name: name.clone(),
                ..v1::GetSessionRequest::default()
            },
            BEARER,
        ))
        .await
        .unwrap()
        .into_inner();
    assert_eq!(session.name, name);
}

// No bearer, another bearer, another scheme: a bare UNAUTHENTICATED, the
// same for each, naming no reason of its own.
#[tokio::test(flavor = "multi_thread", worker_threads = 1)]
async fn anything_but_the_user_bearer_is_a_bare_unauthenticated() {
    let mut client = CtxloomClient::new(serve(Arc::default()).await);
    let other = "22222222222222222222222222222222222222222222222222222222222222bb"; // gitleaks:allow
    let mut answers = Vec::new();
    for request in [
        Request::new(v1::GetDaemonRequest::default()),
        with_bearer(v1::GetDaemonRequest::default(), other),
    ] {
        let status = client.get_daemon(request).await.unwrap_err();
        assert_eq!(status.code(), Code::Unauthenticated);
        answers.push(status.message().to_string());
    }
    assert_eq!(answers[0], answers[1], "nothing says why");
}

// S11: a refusal is answered under its declared status, the typed refusal
// in google.rpc.Status.details.
#[tokio::test(flavor = "multi_thread", worker_threads = 1)]
async fn a_refusal_carries_its_typed_message_in_the_details() {
    let mut client = CtxloomClient::new(serve(Arc::default()).await);
    let status = client
        .stop_daemon(with_bearer(v1::StopDaemonRequest::default(), BEARER))
        .await
        .unwrap_err();
    assert_eq!(status.code(), Code::FailedPrecondition);
    let details = super::RpcStatus::decode(status.details()).unwrap();
    assert_eq!(details.code, Code::FailedPrecondition as i32);
    let hosting = v1::DaemonHosting::decode(details.details[0].value.as_slice()).unwrap();
    assert_eq!(hosting.harps, ["brisk-copper-moth"]);
    assert!(details.details[0]
        .type_url
        .ends_with("ctxloom.v1.DaemonHosting"));
}

// A failure is INTERNAL.
#[tokio::test(flavor = "multi_thread", worker_threads = 1)]
async fn a_failure_is_internal() {
    let mut client = CtxloomClient::new(serve(Arc::default()).await);
    let status = client
        .resolve_project(with_bearer(v1::ResolveProjectRequest::default(), BEARER))
        .await
        .unwrap_err();
    assert_eq!(status.code(), Code::Internal);
}

// Every RPC not served is UNIMPLEMENTED, to the user bearer alone (a plain
// build's DeleteSession and SweepSessions too, M5: plain-destroy-check.sh
// probes that build's daemon).
#[tokio::test(flavor = "multi_thread", worker_threads = 1)]
async fn an_rpc_not_served_is_unimplemented_to_the_user_bearer() {
    let mut client = CtxloomClient::new(serve(Arc::default()).await);
    let status = client
        .get_transcript(with_bearer(v1::GetTranscriptRequest::default(), BEARER))
        .await
        .unwrap_err();
    assert_eq!(status.code(), Code::Unimplemented);
}

// Gate first, everything (owner, after step 5): a call without the user
// bearer is the same bare UNAUTHENTICATED whether its RPC is served or not,
// so a caller that is not the user learns nothing, not even which RPCs the
// API serves.
#[tokio::test(flavor = "multi_thread", worker_threads = 1)]
async fn an_rpc_not_served_is_a_bare_unauthenticated_without_the_user_bearer() {
    let mut client = CtxloomClient::new(serve(Arc::default()).await);
    let other = "22222222222222222222222222222222222222222222222222222222222222bb"; // gitleaks:allow
    let served = client
        .get_daemon(Request::new(v1::GetDaemonRequest::default()))
        .await
        .unwrap_err();
    assert_eq!(served.code(), Code::Unauthenticated);
    let unserved = [
        client
            .delete_session(Request::new(v1::DeleteSessionRequest::default()))
            .await
            .unwrap_err(),
        client
            .sweep_sessions(with_bearer(v1::SweepSessionsRequest::default(), other))
            .await
            .unwrap_err(),
        client
            .get_transcript(Request::new(v1::GetTranscriptRequest::default()))
            .await
            .unwrap_err(),
    ];
    for status in unserved {
        assert_eq!(status.code(), Code::Unauthenticated);
        assert_eq!(status.message(), served.message(), "nothing says which");
    }
}

// M10: a use case that blocks its thread holds a blocking-pool thread, not
// the runtime's one worker: another call is answered meanwhile.
#[tokio::test(flavor = "multi_thread", worker_threads = 1)]
async fn a_blocked_use_case_holds_no_async_worker() {
    let (release, released) = mpsc::channel();
    let calls = Arc::new(Scripted {
        release: Mutex::new(Some(released)),
    });
    let channel = serve(calls).await;
    let mut blocked = CtxloomClient::new(channel.clone());
    let waiting = tokio::spawn(async move {
        blocked
            .get_daemon(with_bearer(v1::GetDaemonRequest::default(), BEARER))
            .await
    });
    tokio::time::sleep(Duration::from_millis(200)).await;
    let mut other = CtxloomClient::new(channel);
    let answered = tokio::time::timeout(
        Duration::from_secs(5),
        other.get_session(with_bearer(v1::GetSessionRequest::default(), BEARER)),
    )
    .await
    .expect("answered while the other use case blocks");
    assert!(answered.is_ok());
    release.send(()).unwrap();
    assert_eq!(waiting.await.unwrap().unwrap().into_inner().pid, 7);
}

// M5: a `session-destroy` build serves DeleteSession and SweepSessions; a
// refused removal is its typed refusal, its plan inside, in the details.
#[tokio::test(flavor = "multi_thread", worker_threads = 1)]
async fn a_destroy_build_serves_delete_and_sweep() {
    let mut client = CtxloomClient::new(serve(Arc::default()).await);
    let plan = client
        .delete_session(with_bearer(
            v1::DeleteSessionRequest {
                name: "projects/p/sessions/s".to_string(),
                validate_only: true,
                ..v1::DeleteSessionRequest::default()
            },
            BEARER,
        ))
        .await
        .unwrap()
        .into_inner();
    assert_eq!(plan.name, "projects/p/sessions/s");
    let status = client
        .delete_session(with_bearer(v1::DeleteSessionRequest::default(), BEARER))
        .await
        .unwrap_err();
    assert_eq!(status.code(), Code::FailedPrecondition);
    let details = super::RpcStatus::decode(status.details()).unwrap();
    let refused: v1::RemovalRefused = details.details[0].to_msg().unwrap();
    assert_eq!(refused.plan.unwrap().authored, ["notes.md"]);
    let swept = client
        .sweep_sessions(with_bearer(v1::SweepSessionsRequest::default(), BEARER))
        .await
        .unwrap()
        .into_inner();
    assert_eq!(swept.purge_cutoff, "2026-08-14");
}

// The reads are answered with what their use cases answered.
#[tokio::test(flavor = "multi_thread", worker_threads = 1)]
async fn the_reads_answer_their_use_cases() {
    let mut client = CtxloomClient::new(serve(Arc::default()).await);
    let project = client
        .get_project(with_bearer(
            v1::GetProjectRequest {
                name: "projects/calm-otter".to_string(),
            },
            BEARER,
        ))
        .await
        .unwrap()
        .into_inner();
    assert_eq!(project.name, "projects/calm-otter");
    let names = |listed: v1::ListSessionsResponse| {
        listed
            .sessions
            .into_iter()
            .map(|session| session.name)
            .collect::<Vec<_>>()
    };
    let listed = client
        .list_sessions(with_bearer(
            v1::ListSessionsRequest {
                parent: "projects/calm-otter".to_string(),
                ..v1::ListSessionsRequest::default()
            },
            BEARER,
        ))
        .await
        .unwrap()
        .into_inner();
    assert_eq!(names(listed), ["projects/calm-otter/sessions/listed"]);
    let found = client
        .search_sessions(with_bearer(
            v1::SearchSessionsRequest {
                parent: "projects/calm-otter".to_string(),
                words: vec!["lock".to_string(), "file".to_string()],
                ..v1::SearchSessionsRequest::default()
            },
            BEARER,
        ))
        .await
        .unwrap()
        .into_inner();
    assert_eq!(names(found), ["projects/calm-otter/sessions/lock-file"]);
}

/// A service ready only when it says so.
#[derive(Clone)]
struct Readiness(bool);

impl tonic::codegen::Service<tonic::codegen::http::Request<tonic::body::Body>> for Readiness {
    type Response = tonic::codegen::http::Response<tonic::body::Body>;
    type Error = std::convert::Infallible;
    type Future = std::future::Ready<Result<Self::Response, Self::Error>>;

    fn poll_ready(
        &mut self,
        _: &mut std::task::Context<'_>,
    ) -> std::task::Poll<Result<(), Self::Error>> {
        match self.0 {
            true => std::task::Poll::Ready(Ok(())),
            false => std::task::Poll::Pending,
        }
    }

    fn call(&mut self, _: tonic::codegen::http::Request<tonic::body::Body>) -> Self::Future {
        std::future::ready(Ok(tonic::codegen::http::Response::new(
            tonic::body::Body::empty(),
        )))
    }
}

// The gate is ready when the service it guards is, and not before.
#[test]
fn the_gate_is_ready_when_its_service_is() {
    use tonic::codegen::Service;
    let mut cx = std::task::Context::from_waker(std::task::Waker::noop());
    let gated = |ready| super::Gated {
        inner: Readiness(ready),
        gate: Arc::new(UserGate::new(&run_token())),
    };
    assert!(gated(false).poll_ready(&mut cx).is_pending());
    assert!(matches!(
        gated(true).poll_ready(&mut cx),
        std::task::Poll::Ready(Ok(()))
    ));
}
