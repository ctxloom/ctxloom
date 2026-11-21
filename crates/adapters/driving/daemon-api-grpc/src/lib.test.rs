//! The service on a loopback port, over use cases a test scripts: the
//! user bearer admits, anything else is a bare UNAUTHENTICATED, a refusal
//! travels in the status's details, an RPC not served is UNIMPLEMENTED,
//! and a use case runs off the async workers (M10).

use std::sync::{mpsc, Arc, Mutex};
use std::time::Duration;

use daemon_api::{Answer, ApiError, ApiRefusal, Calls, Infrastructure};
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
    /// The daemon's standing warnings (OD-55).
    warnings: Vec<v1::Notice>,
    /// The daemon's own version: a request's caller is admitted by it
    /// (`daemon_api::admit`); none, every caller is.
    version: Option<String>,
    /// Each caller the edge asked to be admitted, in order.
    admitted: Mutex<Vec<Option<v1::Caller>>>,
    /// How many assemblies ran.
    assembled: Mutex<u32>,
}

impl Calls for Scripted {
    fn warnings(&self) -> Vec<v1::Notice> {
        self.warnings.clone()
    }

    fn admit(&self, caller: Option<&v1::Caller>) -> Result<(), ApiError> {
        self.admitted.lock().unwrap().push(caller.cloned());
        match &self.version {
            Some(version) => daemon_api::admit(version, caller),
            None => Ok(()),
        }
    }

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

    fn create_run(&self, request: v1::CreateRunRequest) -> Answer<'_, v1::Run> {
        Box::pin(async move { Scripted::create(request) })
    }

    fn get_run(&self, request: v1::GetRunRequest) -> Answer<'_, v1::Run> {
        Box::pin(async move {
            Ok(v1::Run {
                name: request.name,
                ..v1::Run::default()
            })
        })
    }

    fn watch_run(&self, _: v1::WatchRunRequest) -> Answer<'_, daemon_api::RunStream> {
        Box::pin(async { Ok(Box::new(run_events().into_iter()) as daemon_api::RunStream) })
    }

    fn watch_session(&self, _: v1::WatchSessionRequest) -> Answer<'_, daemon_api::SessionStream> {
        Box::pin(async { Ok(Box::new(session_signals().into_iter()) as daemon_api::SessionStream) })
    }

    fn get_transcript(&self, request: v1::GetTranscriptRequest) -> Answer<'_, v1::Transcript> {
        Box::pin(async move {
            Ok(v1::Transcript {
                name: request.name,
                ..v1::Transcript::default()
            })
        })
    }

    fn assemble_context(
        &self,
        request: v1::AssembleContextRequest,
    ) -> Answer<'_, v1::AssembledContext> {
        *self.assembled.lock().unwrap() += 1;
        Box::pin(async move {
            Ok(v1::AssembledContext {
                context: request.fragments.join(","),
                ..v1::AssembledContext::default()
            })
        })
    }

    fn list_agents(
        &self,
        request: v1::ListAgentsRequest,
    ) -> Answer<'_, daemon_api::Read<v1::ListAgentsResponse>> {
        Box::pin(async move {
            Ok(daemon_api::Read {
                answer: v1::ListAgentsResponse {
                    agents: vec![v1::Agent {
                        name: format!("{}/agents/dev", request.parent),
                        ..<v1::Agent as Default>::default()
                    }],
                    next_page_token: String::new(),
                },
                notices: vec![
                    v1::Notice {
                        code: "library-api-notice-tree-in-motion".to_string(),
                        ..v1::Notice::default()
                    },
                    v1::Notice {
                        code: "second".to_string(),
                        ..v1::Notice::default()
                    },
                ],
            })
        })
    }

    fn create_hand_off(&self, request: v1::CreateHandOffRequest) -> Answer<'_, v1::HandOff> {
        Box::pin(async move {
            Ok(v1::HandOff {
                name: format!("{}/handOffs/{}", request.parent, request.hand_off_id),
                engine: String::new(),
                project: request.project,
            })
        })
    }

    fn delete_hand_off(&self, request: v1::DeleteHandOffRequest) -> Answer<'_, v1::HandOff> {
        Box::pin(async move {
            Ok(v1::HandOff {
                name: request.name,
                ..v1::HandOff::default()
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
                false => Err(daemon_api::ApiRefusal::of(&v1::RemovalRefusal {
                    name: request.name,
                    reason: v1::removal_refusal::Reason::Kept as i32,
                    plan: Some(v1::DeleteSessionResponse {
                        authored: vec!["notes.md".to_string()],
                        ..v1::DeleteSessionResponse::default()
                    }),
                    ..v1::RemovalRefusal::default()
                })
                .into()),
            }
        })
    }

    fn get_trust(&self, request: v1::GetTrustRequest) -> Answer<'_, v1::Trust> {
        Box::pin(async move {
            Ok(v1::Trust {
                name: request.name,
                ..v1::Trust::default()
            })
        })
    }

    fn list_trusts(&self, _: v1::ListTrustsRequest) -> Answer<'_, v1::ListTrustsResponse> {
        Box::pin(async {
            Ok(v1::ListTrustsResponse {
                trusts: vec![v1::Trust {
                    name: "projects/calm-otter/trust".to_string(),
                    ..v1::Trust::default()
                }],
                ..v1::ListTrustsResponse::default()
            })
        })
    }

    fn create_approval(&self, request: v1::CreateApprovalRequest) -> Answer<'_, v1::Approval> {
        Box::pin(async move {
            Ok(v1::Approval {
                name: format!("created/{}", request.engine_request_id),
                ..v1::Approval::default()
            })
        })
    }

    fn get_approval(&self, request: v1::GetApprovalRequest) -> Answer<'_, v1::Approval> {
        Box::pin(async move {
            Ok(v1::Approval {
                name: format!("got/{}", request.name),
                ..v1::Approval::default()
            })
        })
    }

    fn list_approvals(
        &self,
        request: v1::ListApprovalsRequest,
    ) -> Answer<'_, v1::ListApprovalsResponse> {
        Box::pin(async move {
            Ok(v1::ListApprovalsResponse {
                approvals: vec![v1::Approval {
                    name: format!("listed/{}", request.root),
                    ..v1::Approval::default()
                }],
                ..v1::ListApprovalsResponse::default()
            })
        })
    }

    fn wait_approval(&self, request: v1::WaitApprovalRequest) -> Answer<'_, v1::Approval> {
        Box::pin(async move {
            Ok(v1::Approval {
                name: format!("waited/{}", request.name),
                ..v1::Approval::default()
            })
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

/// At most this many of a watch's events are read: more is a watch that
/// never ends.
const WATCH_BOUND: usize = 16;

/// A watch's events, read to its end, which must come within
/// [`WATCH_BOUND`] of them. Bounded, as daemon-application's tests read
/// theirs, so a stream that never ends fails its test at once instead of
/// growing a `Vec` until the machine has no memory left (a mutant did that
/// twice on 2026-11-15, to about 70 GB).
async fn watched(stream: &mut tonic::Streaming<v1::RunEvent>) -> Vec<v1::RunEvent> {
    let mut events = Vec::new();
    while let Some(event) = stream.message().await.unwrap() {
        events.push(event);
        assert!(
            events.len() < WATCH_BOUND,
            "the watch never ended: {events:?}"
        );
    }
    events
}

/// A run's events as the scripted `WatchRun` streams them: two, then its
/// end, each as the stream is read.
fn run_events() -> Vec<v1::RunEvent> {
    let event = |event| v1::RunEvent { event: Some(event) };
    vec![
        event(v1::run_event::Event::Notice(v1::Notice {
            code: "n".to_string(),
            ..v1::Notice::default()
        })),
        event(v1::run_event::Event::Reply("HELLO".to_string())),
        event(v1::run_event::Event::End(v1::RunEnd::default())),
    ]
}

/// The run RPCs' use cases (U4): a run created as asked, read, and watched;
/// a TERMINAL run not served yet.
impl Scripted {
    fn create(request: v1::CreateRunRequest) -> Result<v1::Run, ApiError> {
        match v1::run::Mode::try_from(request.mode) {
            Ok(v1::run::Mode::OneShot) => Ok(v1::Run {
                name: format!("{}/runs/r1", request.parent),
                ..v1::Run::default()
            }),
            _ => Err(ApiError::Unimplemented {
                method: "CreateRun(TERMINAL)".to_string(),
            }),
        }
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
            v1::GetSessionRequest { name: name.clone() },
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
    assert_eq!(
        Infrastructure::read(status.code() as i32, &[]),
        Some(Infrastructure::Failed),
        "answered through the infrastructure root"
    );
}

// Every RPC not served is UNIMPLEMENTED, to the user bearer alone (a plain
// build's DeleteSession and SweepSessions too, M5: plain-destroy-check.sh
// probes that build's daemon).
#[tokio::test(flavor = "multi_thread", worker_threads = 1)]
async fn an_rpc_not_served_is_unimplemented_to_the_user_bearer() {
    let mut client = CtxloomClient::new(serve(Arc::default()).await);
    let status = client
        .list_worktrees(with_bearer(v1::ListWorktreesRequest::default(), BEARER))
        .await
        .unwrap_err();
    assert_eq!(status.code(), Code::Unimplemented);
    assert_eq!(
        Infrastructure::read(status.code() as i32, &[]),
        Some(Infrastructure::Unimplemented),
        "answered through the infrastructure root"
    );
}

// Slice 3: GetTranscript and AssembleContext are served, each its use
// case's answer.
#[tokio::test(flavor = "multi_thread", worker_threads = 1)]
async fn the_transcript_and_the_assembly_are_served() {
    let mut client = CtxloomClient::new(serve(Arc::default()).await);
    let transcript = client
        .get_transcript(with_bearer(
            v1::GetTranscriptRequest {
                name: "projects/calm-otter/sessions/amber-swift-owl/transcript".to_string(),
            },
            BEARER,
        ))
        .await
        .unwrap()
        .into_inner();
    assert_eq!(
        transcript.name,
        "projects/calm-otter/sessions/amber-swift-owl/transcript"
    );
    let assembled = client
        .assemble_context(with_bearer(
            v1::AssembleContextRequest {
                fragments: vec!["a".to_string(), "b".to_string()],
                ..v1::AssembleContextRequest::default()
            },
            BEARER,
        ))
        .await
        .unwrap()
        .into_inner();
    assert_eq!(assembled.context, "a,b");
}

/// A caller of the client stamped `version`.
fn caller_of(version: &str) -> v1::Caller {
    v1::Caller {
        client: Some(v1::Client {
            kind: v1::client::Kind::Mcp as i32,
            version: version.to_string(),
            ..v1::Client::default()
        }),
        ..v1::Caller::default()
    }
}

// Owner, 2026-11-18: a request made on a user's behalf is admitted by its
// client's version before its use case: one of another version (or none)
// is refused failed-precondition with its typed `ClientVersionMismatch`,
// and its use case never runs; one of the daemon's own is answered.
#[tokio::test(flavor = "multi_thread", worker_threads = 1)]
async fn a_client_of_another_version_is_refused_before_its_use_case() {
    let calls = Arc::new(Scripted {
        version: Some("v1.2.3-abc".to_string()),
        ..Scripted::default()
    });
    let mut client = CtxloomClient::new(serve(Arc::clone(&calls)).await);
    let assemble = |caller: Option<v1::Caller>| {
        with_bearer(
            v1::AssembleContextRequest {
                fragments: vec!["a".to_string()],
                caller,
                ..v1::AssembleContextRequest::default()
            },
            BEARER,
        )
    };
    for caller in [Some(caller_of("v1.2.4-def")), None] {
        let status = client.assemble_context(assemble(caller)).await.unwrap_err();
        assert_eq!(status.code(), Code::FailedPrecondition);
        let details = super::RpcStatus::decode(status.details()).unwrap();
        let mismatch =
            v1::ClientVersionMismatch::decode(details.details[0].value.as_slice()).unwrap();
        assert_eq!(mismatch.daemon_version, "v1.2.3-abc");
    }
    assert_eq!(*calls.assembled.lock().unwrap(), 0, "no use case ran");
    let answered = client
        .assemble_context(assemble(Some(caller_of("v1.2.3-abc"))))
        .await
        .unwrap()
        .into_inner();
    assert_eq!(answered.context, "a");
    assert_eq!(*calls.assembled.lock().unwrap(), 1);
}

// A read by name carries no caller, and the edge asks nothing of its
// client: a session of a client of any version is read.
#[tokio::test(flavor = "multi_thread", worker_threads = 1)]
async fn a_read_by_name_is_admitted_whatever_its_client() {
    let calls = Arc::new(Scripted {
        version: Some("v1.2.3-abc".to_string()),
        ..Scripted::default()
    });
    let mut client = CtxloomClient::new(serve(Arc::clone(&calls)).await);
    let name = "projects/calm-otter/sessions/amber-swift-owl".to_string();
    let session = client
        .get_session(with_bearer(
            v1::GetSessionRequest { name: name.clone() },
            BEARER,
        ))
        .await
        .unwrap()
        .into_inner();
    assert_eq!(session.name, name);
    assert!(calls.admitted.lock().unwrap().is_empty());
}

// Step 10a U2 (owner ruling 2026-11-17): a library read answers its
// message, and the notices the read said ride its response metadata
// (`NOTICE_METADATA`), in order, apart from the standing warnings.
#[tokio::test(flavor = "multi_thread", worker_threads = 1)]
async fn a_library_read_s_notices_ride_its_metadata() {
    let mut client = CtxloomClient::new(serve(Arc::default()).await);
    let response = client
        .list_agents(with_bearer(
            v1::ListAgentsRequest {
                parent: "projects/calm-otter".to_string(),
                ..v1::ListAgentsRequest::default()
            },
            BEARER,
        ))
        .await
        .unwrap();
    let codes: Vec<String> = response
        .metadata()
        .get_all_bin(daemon_proto::NOTICE_METADATA)
        .iter()
        .map(|value| v1::Notice::decode(value.to_bytes().unwrap()).unwrap().code)
        .collect();
    assert_eq!(codes, ["library-api-notice-tree-in-motion", "second"]);
    assert_eq!(
        response
            .metadata()
            .get_all_bin(daemon_proto::WARNING_METADATA)
            .iter()
            .count(),
        0
    );
    assert_eq!(
        response.into_inner().agents[0].name,
        "projects/calm-otter/agents/dev"
    );
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
        ..Scripted::default()
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
    let refused: v1::RemovalRefusal = details.details[0].to_msg().unwrap();
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

// So are the warnings' and the told calls' wrappers: each is ready when the
// service it wraps is, never before (in the daemon that is tonic's generated
// server, ready always, but a wrapper answers for the service it is given).
#[test]
fn the_warned_and_told_wrappers_are_ready_when_their_service_is() {
    use tonic::codegen::Service;
    let mut cx = std::task::Context::from_waker(std::task::Waker::noop());
    let warned = |ready| super::Warned {
        inner: Readiness(ready),
        calls: Arc::<Scripted>::default(),
    };
    let told = |ready| crate::telling(Readiness(ready), Arc::new(|_: &str| {}));
    assert!(warned(false).poll_ready(&mut cx).is_pending());
    assert!(matches!(
        warned(true).poll_ready(&mut cx),
        std::task::Poll::Ready(Ok(()))
    ));
    assert!(told(false).poll_ready(&mut cx).is_pending());
    assert!(matches!(
        told(true).poll_ready(&mut cx),
        std::task::Poll::Ready(Ok(()))
    ));
}

// U4: CreateRun and GetRun are their use cases' answers, a TERMINAL run is
// UNIMPLEMENTED until U5, and WatchRun streams every event its use case
// gives, in order, then ends.
#[tokio::test(flavor = "multi_thread", worker_threads = 1)]
async fn the_runs_are_served_and_a_watch_streams_every_event() {
    let mut client = CtxloomClient::new(serve(Arc::default()).await);
    let run = client
        .create_run(with_bearer(
            v1::CreateRunRequest {
                parent: "projects/calm-otter".to_string(),
                mode: v1::run::Mode::OneShot as i32,
                ..v1::CreateRunRequest::default()
            },
            BEARER,
        ))
        .await
        .unwrap()
        .into_inner();
    assert_eq!(run.name, "projects/calm-otter/runs/r1");
    let terminal = client
        .create_run(with_bearer(
            v1::CreateRunRequest {
                mode: v1::run::Mode::Terminal as i32,
                ..v1::CreateRunRequest::default()
            },
            BEARER,
        ))
        .await
        .unwrap_err();
    assert_eq!(terminal.code(), Code::Unimplemented);
    let got = client
        .get_run(with_bearer(
            v1::GetRunRequest {
                name: run.name.clone(),
            },
            BEARER,
        ))
        .await
        .unwrap()
        .into_inner();
    assert_eq!(got.name, run.name);
    let mut stream = client
        .watch_run(with_bearer(v1::WatchRunRequest { name: run.name }, BEARER))
        .await
        .unwrap()
        .into_inner();
    assert_eq!(watched(&mut stream).await, run_events());
}

// A watch is gated as every call is: without the user bearer, a bare
// UNAUTHENTICATED, never a stream.
#[tokio::test(flavor = "multi_thread", worker_threads = 1)]
async fn a_watch_without_the_user_bearer_is_unauthenticated() {
    let mut client = CtxloomClient::new(serve(Arc::default()).await);
    let status = client
        .watch_run(Request::new(v1::WatchRunRequest::default()))
        .await
        .unwrap_err();
    assert_eq!(status.code(), Code::Unauthenticated);
}

/// A session's signals as the scripted `WatchSession` streams them.
fn session_signals() -> Vec<v1::SessionSignal> {
    vec![
        v1::SessionSignal::default(),
        v1::SessionSignal {
            wakes: vec!["m1".to_string()],
            outstanding_wake: String::new(),
        },
    ]
}

// The terminal wake: WatchSession streams each signal its use case gives,
// behind the user bearer as every call is.
#[tokio::test(flavor = "multi_thread", worker_threads = 1)]
async fn a_session_watch_streams_its_signals_behind_the_bearer() {
    let mut client = CtxloomClient::new(serve(Arc::default()).await);
    let request = || v1::WatchSessionRequest {
        name: "projects/calm-otter/sessions/amber-swift-owl".to_string(),
    };
    let mut stream = client
        .watch_session(with_bearer(request(), BEARER))
        .await
        .unwrap()
        .into_inner();
    let mut heard = Vec::new();
    while let Some(signal) = stream.message().await.unwrap() {
        heard.push(signal);
    }
    assert_eq!(heard, session_signals());
    let status = client
        .watch_session(Request::new(request()))
        .await
        .unwrap_err();
    assert_eq!(status.code(), Code::Unauthenticated);
}

// Slice 4 (U3, S10): CreateHandOff and DeleteHandOff are served, each its
// use case's answer.
#[tokio::test(flavor = "multi_thread", worker_threads = 1)]
async fn the_hand_offs_are_served() {
    let mut client = CtxloomClient::new(serve(Arc::default()).await);
    let created = client
        .create_hand_off(with_bearer(
            v1::CreateHandOffRequest {
                parent: "projects/calm-otter".to_string(),
                hand_off_id: "c1".to_string(),
                ..v1::CreateHandOffRequest::default()
            },
            BEARER,
        ))
        .await
        .unwrap()
        .into_inner();
    assert_eq!(created.name, "projects/calm-otter/handOffs/c1");
    let deleted = client
        .delete_hand_off(with_bearer(
            v1::DeleteHandOffRequest {
                name: "projects/calm-otter/handOffs/c1".to_string(),
                ..v1::DeleteHandOffRequest::default()
            },
            BEARER,
        ))
        .await
        .unwrap()
        .into_inner();
    assert_eq!(deleted.name, "projects/calm-otter/handOffs/c1");
}

/// Scripted use cases whose daemon stands warning of `codes`, each a
/// notice with one arg.
fn warning(codes: &[&str]) -> Arc<Scripted> {
    Arc::new(Scripted {
        warnings: codes
            .iter()
            .map(|code| v1::Notice {
                code: code.to_string(),
                args: vec![v1::NeedArg {
                    name: "why".to_string(),
                    value: format!("{code} why"),
                }],
                text: String::new(),
            })
            .collect(),
        ..Scripted::default()
    })
}

/// The warnings `metadata` carries, decoded, in order.
fn warnings_in(metadata: &tonic::metadata::MetadataMap) -> Vec<v1::Notice> {
    metadata
        .get_all_bin(daemon_proto::WARNING_METADATA)
        .iter()
        .map(|value| v1::Notice::decode(value.to_bytes().unwrap()).unwrap())
        .collect()
}

// OD-55: every answer carries the daemon's standing warnings as response
// metadata, in order, each its code and args: an answer, a refusal, a
// failure and a watch alike, so no command calls again to learn them.
#[tokio::test(flavor = "multi_thread", worker_threads = 1)]
async fn every_answer_carries_the_standing_warnings() {
    let codes = ["daemon-api-start-up-incomplete", "daemon-api-handing-over"];
    let mut client = CtxloomClient::new(serve(warning(&codes)).await);
    let said = |notices: Vec<v1::Notice>| -> Vec<(String, String)> {
        notices
            .into_iter()
            .map(|n| (n.code, n.args[0].value.clone()))
            .collect()
    };
    let expected: Vec<(String, String)> = codes
        .iter()
        .map(|code| (code.to_string(), format!("{code} why")))
        .collect();
    let answered = client
        .get_session(with_bearer(v1::GetSessionRequest::default(), BEARER))
        .await
        .unwrap();
    assert_eq!(said(warnings_in(answered.metadata())), expected);
    let refused = client
        .stop_daemon(with_bearer(v1::StopDaemonRequest::default(), BEARER))
        .await
        .unwrap_err();
    assert_eq!(refused.code(), Code::FailedPrecondition);
    assert_eq!(said(warnings_in(refused.metadata())), expected);
    let failed = client
        .resolve_project(with_bearer(v1::ResolveProjectRequest::default(), BEARER))
        .await
        .unwrap_err();
    assert_eq!(said(warnings_in(failed.metadata())), expected);
    let watched = client
        .watch_run(with_bearer(v1::WatchRunRequest::default(), BEARER))
        .await
        .unwrap();
    assert_eq!(said(warnings_in(watched.metadata())), expected);
}

// A daemon with no standing warning attaches nothing; a caller that is not
// the user is told none of them (the gate answers first).
#[tokio::test(flavor = "multi_thread", worker_threads = 1)]
async fn no_warning_or_no_user_bearer_carries_none() {
    let mut client = CtxloomClient::new(serve(Arc::default()).await);
    let answered = client
        .get_session(with_bearer(v1::GetSessionRequest::default(), BEARER))
        .await
        .unwrap();
    assert!(answered
        .metadata()
        .get_bin(daemon_proto::WARNING_METADATA)
        .is_none());
    let mut client = CtxloomClient::new(serve(warning(&["daemon-api-start-up-incomplete"])).await);
    let status = client
        .get_session(Request::new(v1::GetSessionRequest::default()))
        .await
        .unwrap_err();
    assert_eq!(status.code(), Code::Unauthenticated);
    assert!(warnings_in(status.metadata()).is_empty());
}

// Under test-support the daemon tells its log each call made of it: the
// path of each request, as it arrives, answered as ever.
#[tokio::test(flavor = "multi_thread", worker_threads = 1)]
async fn a_told_service_tells_each_requests_path() {
    let token = run_token();
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    let told = Arc::new(Mutex::new(Vec::<String>::new()));
    let tell = Arc::clone(&told);
    let service = crate::telling(
        crate::service(Arc::<Scripted>::default(), UserGate::new(&token)),
        Arc::new(move |path: &str| tell.lock().unwrap().push(path.to_string())),
    );
    tokio::spawn(
        Server::builder()
            .add_service(service)
            .serve_with_incoming(tokio_stream::wrappers::TcpListenerStream::new(listener)),
    );
    let channel = Channel::from_shared(format!("http://{addr}"))
        .unwrap()
        .connect()
        .await
        .unwrap();
    let mut client = CtxloomClient::new(channel);
    let daemon = client
        .get_daemon(with_bearer(v1::GetDaemonRequest::default(), BEARER))
        .await
        .unwrap()
        .into_inner();
    assert_eq!(daemon.pid, 7);
    client
        .get_session(with_bearer(v1::GetSessionRequest::default(), BEARER))
        .await
        .unwrap();
    assert_eq!(
        *told.lock().unwrap(),
        [
            "/ctxloom.v1.Ctxloom/GetDaemon",
            "/ctxloom.v1.Ctxloom/GetSession"
        ]
    );
}

// One root for infrastructure errors (owner, 2026-11-14): an error that is
// no refusal is answered under the status `Infrastructure` names for it, its
// message unchanged.
#[test]
fn an_error_that_is_no_refusal_is_answered_through_the_infrastructure_root() {
    let failed = super::status_of_error(ApiError::Failed {
        detail: "the store broke".to_string(),
    });
    assert_eq!(failed.code() as i32, Infrastructure::Failed.status());
    assert_eq!(failed.message(), "the store broke");
    let unserved = super::status_of_error(ApiError::Unimplemented {
        method: "CreateRun".to_string(),
    });
    assert_eq!(
        unserved.code() as i32,
        Infrastructure::Unimplemented.status()
    );
    assert_eq!(unserved.message(), "CreateRun is not served yet");
}

// Step 8: the trust and approvals RPCs are served, each its use case's
// answer.
#[tokio::test(flavor = "multi_thread", worker_threads = 1)]
async fn the_trust_and_approvals_rpcs_are_served() {
    let mut client = CtxloomClient::new(serve(Arc::default()).await);
    let trust = client
        .get_trust(with_bearer(
            v1::GetTrustRequest {
                name: "projects/calm-otter/trust".to_string(),
            },
            BEARER,
        ))
        .await
        .unwrap()
        .into_inner();
    assert_eq!(trust.name, "projects/calm-otter/trust");
    let trusts = client
        .list_trusts(with_bearer(v1::ListTrustsRequest::default(), BEARER))
        .await
        .unwrap()
        .into_inner();
    assert_eq!(trusts.trusts.len(), 1, "{trusts:?}");
    let created = client
        .create_approval(with_bearer(
            v1::CreateApprovalRequest {
                engine_request_id: "toolu_1".to_string(),
                ..v1::CreateApprovalRequest::default()
            },
            BEARER,
        ))
        .await
        .unwrap()
        .into_inner();
    assert_eq!(created.name, "created/toolu_1");
    let got = client
        .get_approval(with_bearer(
            v1::GetApprovalRequest {
                name: "ask-tidy-wren".to_string(),
            },
            BEARER,
        ))
        .await
        .unwrap()
        .into_inner();
    assert_eq!(got.name, "got/ask-tidy-wren");
    let listed = client
        .list_approvals(with_bearer(
            v1::ListApprovalsRequest {
                root: "amber-quiet-heron".to_string(),
                ..v1::ListApprovalsRequest::default()
            },
            BEARER,
        ))
        .await
        .unwrap()
        .into_inner();
    assert_eq!(listed.approvals[0].name, "listed/amber-quiet-heron");
    let waited = client
        .wait_approval(with_bearer(
            v1::WaitApprovalRequest {
                name: "ask-tidy-wren".to_string(),
            },
            BEARER,
        ))
        .await
        .unwrap()
        .into_inner();
    assert_eq!(waited.name, "waited/ask-tidy-wren");
}

// Step 10a U2: a failure the daemon says in a diagnostic of its own (a
// bundle that does not read) travels as its code and args in a `google.rpc.ErrorInfo`, so the
// client says it in that diagnostic's words.
#[test]
fn a_diagnosed_failure_carries_its_code_and_args() {
    let status = super::status_of_error(ApiError::Diagnosed {
        code: "library-application-bundle-unreadable".to_string(),
        args: vec![v1::NeedArg {
            name: "ask".to_string(),
            value: "demo".to_string(),
        }],
    });
    // Owner ruling 2026-11-17: a diagnosed failure is a precondition of the
    // host unmet, failed-precondition; not INTERNAL, and not UNAVAILABLE,
    // which the client reads as an unreachable daemon.
    assert_eq!(status.code() as i32, Infrastructure::Unmet.status());
    assert_eq!(status.code(), tonic::Code::FailedPrecondition);
    let details = super::RpcStatus::decode(status.details()).unwrap().details;
    assert_eq!(details.len(), 1);
    assert!(details[0].type_url.ends_with("/google.rpc.ErrorInfo"));
    let info = super::ErrorInfo::decode(details[0].value.as_slice()).unwrap();
    assert_eq!(info.reason, "library-application-bundle-unreadable");
    assert_eq!(info.domain, super::DIAGNOSED_DOMAIN);
    assert_eq!(info.metadata["ask"], "demo");
}
