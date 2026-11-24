//! The client against a fake daemon on a loopback TLS port: the pinned
//! certificate, the bearer it presents, each outcome read back.

use std::time::Duration;

use angzarr_embedded::v1::DeliveryOutcome;
use angzarr_proto_transport_distributed::v1::command_handler_coordinator_service_server::{
    CommandHandlerCoordinatorService, CommandHandlerCoordinatorServiceServer,
};
use angzarr_proto_transport_distributed::v1::{
    CommandResult, CommandSyncResult, HandleSyncRequest,
};
use daemon_ports::{Called, DaemonApi};
use daemon_proto::service::ctxloom_server::{Ctxloom, CtxloomServer};
use daemon_proto::v1;
use hook_tls::{Minter, SystemClock};
use prost::Message;
use run_ports::{CertMint, CertNames, Discovery, HookCertificate, Token};
use tonic::codegen::Bytes;
use tonic::transport::{Identity, Server, ServerTlsConfig};
use tonic::{Code, Request, Response, Status};

use super::{DaemonClient, RpcStatus};

fn token(byte: u8) -> Token {
    Token::from_bytes([byte; 32])
}

/// The warning the fake stands by, its `why` arg `why`.
fn standing(why: &str) -> v1::Notice {
    v1::Notice {
        code: "daemon-api-start-up-incomplete".to_string(),
        args: vec![v1::NeedArg {
            name: "why".to_string(),
            value: why.to_string(),
        }],
        text: String::new(),
    }
}

/// `metadata` carrying the daemon's warnings (OD-55): the standing one,
/// named `why`, and a value that is no notice, passed over.
fn warned(metadata: &mut tonic::metadata::MetadataMap, why: &str) {
    let key = daemon_proto::WARNING_METADATA;
    metadata.append_bin(
        key,
        tonic::metadata::MetadataValue::from_bytes(&standing(why).encode_to_vec()),
    );
    metadata.append_bin(key, tonic::metadata::MetadataValue::from_bytes(&[0xff]));
}

/// A daemon that answers the bearer it was made with, and refuses a stop.
struct Fake {
    bearer: String,
}

impl Fake {
    fn admit<T>(&self, request: &Request<T>) -> Result<(), Status> {
        let presented = request
            .metadata()
            .get("authorization")
            .and_then(|value| value.to_str().ok())
            .unwrap_or_default();
        match presented == format!("Bearer {}", self.bearer) {
            true => Ok(()),
            false => Err(Status::unauthenticated("")),
        }
    }
}

#[tonic::async_trait]
impl Ctxloom for Fake {
    async fn get_daemon(
        &self,
        request: Request<v1::GetDaemonRequest>,
    ) -> Result<Response<v1::Daemon>, Status> {
        self.admit(&request)?;
        let mut response = Response::new(v1::Daemon {
            name: request.into_inner().name,
            pid: 99,
            ..v1::Daemon::default()
        });
        warned(response.metadata_mut(), "answered");
        Ok(response)
    }

    async fn stop_daemon(
        &self,
        request: Request<v1::StopDaemonRequest>,
    ) -> Result<Response<v1::Daemon>, Status> {
        self.admit(&request)?;
        let refusal = prost_types::Any::from_msg(&v1::DaemonHosting {
            runs: 1,
            harps: vec!["brisk-copper-moth".to_string()],
        })
        .unwrap();
        let details = RpcStatus {
            code: Code::FailedPrecondition as i32,
            message: "ctxloom.v1.DaemonHosting".to_string(),
            details: vec![refusal],
        };
        let mut status = Status::with_details(
            Code::FailedPrecondition,
            "ctxloom.v1.DaemonHosting",
            Bytes::from(details.encode_to_vec()),
        );
        warned(status.metadata_mut(), "refused");
        Err(status)
    }

    /// A project resolved: named by the cwd it was asked from.
    async fn resolve_project(
        &self,
        request: Request<v1::ResolveProjectRequest>,
    ) -> Result<Response<v1::Project>, Status> {
        self.admit(&request)?;
        Ok(Response::new(v1::Project {
            name: "projects/calm-quiet-heron".to_string(),
            root: request
                .into_inner()
                .caller
                .map(|caller| caller.cwd)
                .unwrap_or_default(),
            ..v1::Project::default()
        }))
    }

    /// One session under the parent asked, with the page token asked.
    async fn list_sessions(
        &self,
        request: Request<v1::ListSessionsRequest>,
    ) -> Result<Response<v1::ListSessionsResponse>, Status> {
        self.admit(&request)?;
        let request = request.into_inner();
        Ok(Response::new(v1::ListSessionsResponse {
            sessions: vec![v1::Session {
                name: format!("{}/sessions/listed", request.parent),
                ..v1::Session::default()
            }],
            next_page_token: request.page_token,
        }))
    }

    /// One session named by the words asked.
    async fn search_sessions(
        &self,
        request: Request<v1::SearchSessionsRequest>,
    ) -> Result<Response<v1::ListSessionsResponse>, Status> {
        self.admit(&request)?;
        let request = request.into_inner();
        Ok(Response::new(v1::ListSessionsResponse {
            sessions: vec![v1::Session {
                name: format!("{}/sessions/{}", request.parent, request.words.join("-")),
                ..v1::Session::default()
            }],
            ..v1::ListSessionsResponse::default()
        }))
    }

    /// A daemon that goes mid-call: its connection is dropped unanswered.
    async fn get_project(
        &self,
        request: Request<v1::GetProjectRequest>,
    ) -> Result<Response<v1::Project>, Status> {
        self.admit(&request)?;
        panic!("the daemon is gone mid-call");
    }

    /// A destroy takes longer than a read's deadline (its worktrees' git).
    async fn delete_session(
        &self,
        request: Request<v1::DeleteSessionRequest>,
    ) -> Result<Response<v1::DeleteSessionResponse>, Status> {
        self.admit(&request)?;
        tokio::time::sleep(SLOW).await;
        Ok(Response::new(v1::DeleteSessionResponse {
            name: request.into_inner().name,
            applied: Some(true),
            ..v1::DeleteSessionResponse::default()
        }))
    }

    async fn create_run(
        &self,
        request: Request<v1::CreateRunRequest>,
    ) -> Result<Response<v1::Run>, Status> {
        self.admit(&request)?;
        Ok(Response::new(v1::Run {
            name: format!("{}/runs/r1", request.into_inner().parent),
            ..v1::Run::default()
        }))
    }

    async fn get_run(
        &self,
        request: Request<v1::GetRunRequest>,
    ) -> Result<Response<v1::Run>, Status> {
        self.admit(&request)?;
        Ok(Response::new(v1::Run {
            name: request.into_inner().name,
            ..v1::Run::default()
        }))
    }

    /// Two events, then (for a run named `broken`) the daemon's status
    /// ending the stream early, else its end.
    async fn watch_run(
        &self,
        request: Request<v1::WatchRunRequest>,
    ) -> Result<Response<tonic::codegen::BoxStream<v1::RunEvent>>, Status> {
        self.admit(&request)?;
        let broken = request.into_inner().name == "broken";
        let event = |event| Ok(v1::RunEvent { event: Some(event) });
        let mut events = vec![
            event(v1::run_event::Event::Reply("HELLO".to_string())),
            event(v1::run_event::Event::End(v1::RunEnd::default())),
        ];
        if broken {
            events[1] = Err(Status::internal("the run broke"));
        }
        Ok(Response::new(Box::pin(tokio_stream::iter(events))))
    }

    /// One change, then (for the parent `users/me`) the daemon's status
    /// ending the stream early, else its end (step 10c G7).
    async fn watch_library(
        &self,
        request: Request<v1::WatchLibraryRequest>,
    ) -> Result<Response<tonic::codegen::BoxStream<v1::LibraryChange>>, Status> {
        self.admit(&request)?;
        let parent = request.into_inner().parent;
        let mut changes = vec![Ok(v1::LibraryChange {
            tree: format!("{parent}/library"),
            sequence: 7,
            ..v1::LibraryChange::default()
        })];
        if parent == "users/me" {
            changes.push(Err(Status::internal("the watch broke")));
        }
        Ok(Response::new(Box::pin(tokio_stream::iter(changes))))
    }

    /// Two signals of the session watched, then the stream's end.
    async fn watch_session(
        &self,
        request: Request<v1::WatchSessionRequest>,
    ) -> Result<Response<tonic::codegen::BoxStream<v1::SessionSignal>>, Status> {
        self.admit(&request)?;
        let signal = |wakes: &[&str]| {
            Ok(v1::SessionSignal {
                wakes: wakes.iter().map(ToString::to_string).collect(),
                outstanding_wake: String::new(),
            })
        };
        let signals = vec![signal(&[]), signal(&["m1"])];
        Ok(Response::new(Box::pin(tokio_stream::iter(signals))))
    }

    async fn sweep_sessions(
        &self,
        request: Request<v1::SweepSessionsRequest>,
    ) -> Result<Response<v1::SweepSessionsResponse>, Status> {
        self.admit(&request)?;
        tokio::time::sleep(SLOW).await;
        Ok(Response::new(v1::SweepSessionsResponse {
            purge_cutoff: "2026-01-01".to_string(),
            ..v1::SweepSessionsResponse::default()
        }))
    }

    /// The transcript of the session asked: one line naming it.
    async fn get_transcript(
        &self,
        request: Request<v1::GetTranscriptRequest>,
    ) -> Result<Response<v1::Transcript>, Status> {
        self.admit(&request)?;
        let name = request.into_inner().name;
        Ok(Response::new(v1::Transcript {
            entries: vec![v1::LogEntry {
                entry: Some(v1::log_entry::Entry::Prompt(v1::LogPrompt {
                    prompt_id: "p".to_string(),
                    text: format!("of {name}"),
                })),
                ..v1::LogEntry::default()
            }],
            name,
        }))
    }

    /// The context of the agent asked.
    async fn assemble_context(
        &self,
        request: Request<v1::AssembleContextRequest>,
    ) -> Result<Response<v1::AssembledContext>, Status> {
        self.admit(&request)?;
        Ok(Response::new(v1::AssembledContext {
            context: format!("context of {}", request.into_inner().agent),
            tokens: 7,
            ..v1::AssembledContext::default()
        }))
    }

    /// One agent, the read's notice beside it (step 10a U2).
    async fn list_agents(
        &self,
        request: Request<v1::ListAgentsRequest>,
    ) -> Result<Response<v1::ListAgentsResponse>, Status> {
        self.admit(&request)?;
        let mut response = Response::new(v1::ListAgentsResponse {
            agents: vec![v1::Agent {
                name: format!("{}/agents/dev", request.into_inner().parent),
                ..<v1::Agent as Default>::default()
            }],
            next_page_token: String::new(),
        });
        let notice = v1::Notice {
            code: "library-api-notice-tree-in-motion".to_string(),
            ..v1::Notice::default()
        };
        response.metadata_mut().append_bin(
            daemon_proto::NOTICE_METADATA,
            tonic::metadata::MetadataValue::from_bytes(&notice.encode_to_vec()),
        );
        Ok(response)
    }
}

/// The coordinator beside the service: a sync answers each projector named,
/// a DUPLICATE when the first is `again`.
struct Coordinator {
    bearer: String,
}

#[tonic::async_trait]
impl CommandHandlerCoordinatorService for Coordinator {
    async fn handle(
        &self,
        _request: Request<angzarr_embedded::v1::CommandBook>,
    ) -> Result<Response<CommandResult>, Status> {
        Err(Status::unimplemented("handle"))
    }

    async fn handle_async(
        &self,
        _request: Request<angzarr_embedded::v1::CommandBook>,
    ) -> Result<Response<()>, Status> {
        Err(Status::unimplemented("handle_async"))
    }

    async fn handle_sync(
        &self,
        request: Request<HandleSyncRequest>,
    ) -> Result<Response<CommandSyncResult>, Status> {
        Fake {
            bearer: self.bearer.clone(),
        }
        .admit(&request)?;
        let projectors = request.into_inner().projectors;
        let delivery = match projectors.first().map(String::as_str) {
            Some("again") => DeliveryOutcome::Duplicate,
            _ => DeliveryOutcome::Applied,
        };
        Ok(Response::new(CommandSyncResult {
            delivery: delivery as i32,
            projections: projectors
                .into_iter()
                .map(|projector| angzarr_embedded::v1::Projection {
                    projector,
                    ..angzarr_embedded::v1::Projection::default()
                })
                .collect(),
            ..CommandSyncResult::default()
        }))
    }

    async fn deliver(
        &self,
        _request: Request<angzarr_embedded::v1::ReactionDelivery>,
    ) -> Result<Response<CommandResult>, Status> {
        Err(Status::unimplemented("deliver"))
    }
}

/// How long the fake's destroys take.
const SLOW: Duration = Duration::from_millis(600);

fn certificate() -> HookCertificate {
    Minter::new(SystemClock)
        .mint(&CertNames::new(
            vec!["127.0.0.1".parse().unwrap()],
            Vec::new(),
        ))
        .unwrap()
}

/// Serve the fake with `certificate`; the discovery a client reads.
async fn serve(certificate: &HookCertificate, bearer: &Token) -> Discovery {
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let port = listener.local_addr().unwrap().port();
    let identity = Identity::from_pem(certificate.public().to_pem(), certificate.key_pem());
    let server = Server::builder()
        .tls_config(ServerTlsConfig::new().identity(identity))
        .unwrap()
        .add_service(CtxloomServer::new(Fake {
            bearer: bearer.to_text(),
        }))
        .add_service(CommandHandlerCoordinatorServiceServer::new(Coordinator {
            bearer: bearer.to_text(),
        }))
        .serve_with_incoming(tokio_stream::wrappers::TcpListenerStream::new(listener));
    tokio::spawn(server);
    Discovery {
        stamp: "0.1.0+a".to_string(),
        pid: 99,
        endpoint: format!("https://127.0.0.1:{port}"),
        cert: certificate.public().clone(),
        bearer: bearer.clone(),
        home: "/home".to_string(),
    }
}

const WITHIN: Duration = Duration::from_secs(5);

// The pinned certificate and the user bearer: the call is answered.
#[tokio::test(flavor = "multi_thread", worker_threads = 1)]
async fn a_call_pins_the_certificate_and_presents_the_bearer() {
    let found = serve(&certificate(), &token(0xab)).await;
    let daemon = DaemonClient::new(&found, WITHIN)
        .get_daemon(v1::GetDaemonRequest {
            name: "daemon".to_string(),
        })
        .await
        .unwrap();
    assert_eq!((daemon.name.as_str(), daemon.pid), ("daemon", 99));
}

// A refusal comes back as its typed message (S11).
#[tokio::test(flavor = "multi_thread", worker_threads = 1)]
async fn a_refusal_comes_back_as_its_typed_message() {
    let found = serve(&certificate(), &token(0xab)).await;
    let called = DaemonClient::new(&found, WITHIN)
        .stop_daemon(v1::StopDaemonRequest::default())
        .await
        .unwrap_err();
    let Called::Refused { code, .. } = &called else {
        panic!("not refused: {called:?}")
    };
    assert_eq!(*code, Code::FailedPrecondition as i32);
    let hosting = called.refusal::<v1::DaemonHosting>().unwrap();
    assert_eq!(hosting.harps, ["brisk-copper-moth"]);
}

// Another bearer is unauthenticated; a method not served, unimplemented.
#[tokio::test(flavor = "multi_thread", worker_threads = 1)]
async fn another_bearer_and_an_unserved_method_are_said() {
    let found = serve(&certificate(), &token(0xab)).await;
    let other = Discovery {
        bearer: token(0xcd),
        ..found.clone()
    };
    assert_eq!(
        DaemonClient::new(&other, WITHIN)
            .get_daemon(v1::GetDaemonRequest::default())
            .await,
        Err(Called::Unauthenticated)
    );
    assert_eq!(
        DaemonClient::new(&found, WITHIN)
            .get_session(v1::GetSessionRequest::default())
            .await,
        Err(Called::Unimplemented {
            method: "/ctxloom.v1.Ctxloom/GetSession".to_string()
        })
    );
}

// A daemon presenting another certificate is not trusted: unreachable.
#[tokio::test(flavor = "multi_thread", worker_threads = 1)]
async fn another_certificate_is_not_trusted() {
    let found = serve(&certificate(), &token(0xab)).await;
    let pinned_elsewhere = Discovery {
        cert: certificate().public().clone(),
        ..found
    };
    let called = DaemonClient::new(&pinned_elsewhere, WITHIN)
        .get_daemon(v1::GetDaemonRequest::default())
        .await
        .unwrap_err();
    assert!(matches!(called, Called::Unreachable { .. }), "{called:?}");
}

// Nothing at the endpoint: unreachable.
#[tokio::test(flavor = "multi_thread", worker_threads = 1)]
async fn nothing_at_the_endpoint_is_unreachable() {
    let listener = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
    let port = listener.local_addr().unwrap().port();
    drop(listener);
    let found = Discovery {
        endpoint: format!("https://127.0.0.1:{port}"),
        ..serve(&certificate(), &token(0xab)).await
    };
    let called = DaemonClient::new(&found, WITHIN)
        .get_daemon(v1::GetDaemonRequest::default())
        .await
        .unwrap_err();
    assert!(matches!(called, Called::Unreachable { .. }), "{called:?}");
}

// The client's Debug withholds the bearer.
#[tokio::test(flavor = "multi_thread", worker_threads = 1)]
async fn the_debug_withholds_the_bearer() {
    let found = serve(&certificate(), &token(0xab)).await;
    let shown = format!("{:?}", DaemonClient::new(&found, WITHIN));
    assert!(!shown.contains(&token(0xab).to_text()), "{shown}");
    // It says which daemon, and that the bearer is withheld.
    assert!(shown.starts_with("DaemonClient {"), "{shown}");
    assert!(shown.contains(&found.endpoint), "{shown}");
    assert!(shown.contains("bearer: \"<redacted>\""), "{shown}");
}

// angzarr's own UNAUTHENTICATED carries a google.rpc.ErrorInfo detail: it is
// no typed refusal, and reads as unauthenticated.
#[test]
fn an_error_info_detail_is_no_typed_refusal() {
    let info = prost_types::Any {
        type_url: "type.googleapis.com/google.rpc.ErrorInfo".to_string(),
        value: vec![10, 1, 120],
    };
    let details = RpcStatus {
        code: Code::Unauthenticated as i32,
        message: "k".to_string(),
        details: vec![info],
    };
    let status = Status::with_details(
        Code::Unauthenticated,
        "k",
        Bytes::from(details.encode_to_vec()),
    );
    assert_eq!(super::called("GetDaemon", &status), Called::Unauthenticated);
}

// Step 8 U1: the coordinator refuses a command naming its refusal's TYPE
// alone, in its ErrorInfo's `refusal_type` (no object): read back as a
// typed refusal of that type, with no fields, under the status's code.
#[test]
fn a_refusal_the_coordinator_names_by_its_type_reads_back_as_that_refusal() {
    let info = |metadata: &[(&str, &str)]| {
        let info = super::ErrorInfo {
            metadata: metadata
                .iter()
                .map(|(k, v)| (k.to_string(), v.to_string()))
                .collect(),
            ..super::ErrorInfo::default()
        };
        prost_types::Any {
            type_url: "type.googleapis.com/google.rpc.ErrorInfo".to_string(),
            value: info.encode_to_vec(),
        }
    };
    let status_of = |details: Vec<prost_types::Any>| {
        let rpc = RpcStatus {
            code: Code::FailedPrecondition as i32,
            message: "ANGZARR_REASON_COMMAND_REFUSED".to_string(),
            details,
        };
        Status::with_details(
            Code::FailedPrecondition,
            "ANGZARR_REASON_COMMAND_REFUSED",
            Bytes::from(rpc.encode_to_vec()),
        )
    };
    let named = status_of(vec![info(&[(
        "refusal_type",
        "ctxloom.trust.v1.PlanStale",
    )])]);
    assert_eq!(
        super::called("HandleSync", &named),
        Called::Refused {
            code: Code::FailedPrecondition as i32,
            refusal: prost_types::Any {
                type_url: "type.googleapis.com/ctxloom.trust.v1.PlanStale".to_string(),
                value: Vec::new(),
            },
        }
    );
    // An ErrorInfo naming no refusal is no refusal.
    for unnamed in [info(&[]), info(&[("refusal_type", "")])] {
        assert!(
            matches!(
                super::called("HandleSync", &status_of(vec![unnamed])),
                Called::Failed { .. }
            ),
            "an ErrorInfo naming no type"
        );
    }
}

// A destroy has a deadline of its own, longer than a read's: one that takes
// longer than `within` is still answered within `destroying`, and one that
// outlasts that too is unreachable.
#[tokio::test(flavor = "multi_thread", worker_threads = 1)]
async fn a_destroy_is_bounded_by_its_own_deadline_not_a_reads() {
    let found = serve(&certificate(), &token(0xab)).await;
    let quick = Duration::from_millis(200);
    let client = DaemonClient::new(&found, quick).destroying(Duration::from_secs(10));
    let done = client
        .delete_session(v1::DeleteSessionRequest {
            name: "projects/p/sessions/s".to_string(),
            ..v1::DeleteSessionRequest::default()
        })
        .await
        .unwrap();
    assert_eq!(done.applied, Some(true));
    let swept = client
        .sweep_sessions(v1::SweepSessionsRequest::default())
        .await
        .unwrap();
    assert_eq!(swept.purge_cutoff, "2026-01-01");

    let short = DaemonClient::new(&found, Duration::from_secs(10)).destroying(quick);
    let late = short
        .delete_session(v1::DeleteSessionRequest::default())
        .await
        .unwrap_err();
    assert!(matches!(late, Called::Unreachable { .. }), "{late:?}");
    let late = short
        .sweep_sessions(v1::SweepSessionsRequest::default())
        .await
        .unwrap_err();
    assert!(matches!(late, Called::Unreachable { .. }), "{late:?}");
}

// A daemon gone mid-call (its connection dropped unanswered, a transport
// error) is unreachable, as one not there: a client asks again or says
// none serves, never a failed call.
#[tokio::test(flavor = "multi_thread", worker_threads = 1)]
async fn a_daemon_gone_mid_call_is_unreachable() {
    let found = serve(&certificate(), &token(0xab)).await;
    let gone = DaemonClient::new(&found, WITHIN)
        .get_project(v1::GetProjectRequest::default())
        .await
        .unwrap_err();
    assert!(matches!(gone, Called::Unreachable { .. }), "{gone:?}");
}

// A project, a listing and a search come back as the daemon answered them.
#[tokio::test(flavor = "multi_thread", worker_threads = 1)]
async fn a_project_a_listing_and_a_search_are_answered() {
    let found = serve(&certificate(), &token(0xab)).await;
    let client = DaemonClient::new(&found, WITHIN);
    let project = client
        .resolve_project(v1::ResolveProjectRequest {
            caller: Some(v1::Caller {
                cwd: "/work/calm".to_string(),
                ..v1::Caller::default()
            }),
            ..v1::ResolveProjectRequest::default()
        })
        .await
        .unwrap();
    assert_eq!(project.name, "projects/calm-quiet-heron");
    assert_eq!(project.root, "/work/calm");
    let listed = client
        .list_sessions(v1::ListSessionsRequest {
            parent: "projects/calm-quiet-heron".to_string(),
            page_token: "next".to_string(),
            ..v1::ListSessionsRequest::default()
        })
        .await
        .unwrap();
    assert_eq!(
        listed.sessions[0].name,
        "projects/calm-quiet-heron/sessions/listed"
    );
    assert_eq!(listed.next_page_token, "next");
    let searched = client
        .search_sessions(v1::SearchSessionsRequest {
            parent: "projects/calm-quiet-heron".to_string(),
            words: vec!["auth".to_string(), "refactor".to_string()],
            ..v1::SearchSessionsRequest::default()
        })
        .await
        .unwrap();
    assert_eq!(
        searched.sessions[0].name,
        "projects/calm-quiet-heron/sessions/auth-refactor"
    );
}

// Owner, 2026-11-18: a client filling its caller fills every request made
// on the user's behalf where it names none of its own: the daemon resolves
// a project from the filled directory, and from the request's own where it
// names one.
#[tokio::test(flavor = "multi_thread", worker_threads = 1)]
async fn a_filling_client_fills_each_requests_caller_where_it_names_none() {
    let found = serve(&certificate(), &token(0xab)).await;
    let client = DaemonClient::new(&found, WITHIN).filling(v1::Caller {
        cwd: "/work/filled".to_string(),
        ..v1::Caller::default()
    });
    let filled = client
        .resolve_project(v1::ResolveProjectRequest::default())
        .await
        .unwrap();
    assert_eq!(filled.root, "/work/filled");
    let own = client
        .resolve_project(v1::ResolveProjectRequest {
            caller: Some(v1::Caller {
                cwd: "/work/own".to_string(),
                ..v1::Caller::default()
            }),
            ..v1::ResolveProjectRequest::default()
        })
        .await
        .unwrap();
    assert_eq!(own.root, "/work/own");
}

/// An error with the cause it was raised from.
#[derive(Debug)]
struct Wrapped(&'static str, Option<Box<Wrapped>>);

impl std::fmt::Display for Wrapped {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(self.0)
    }
}

impl std::error::Error for Wrapped {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        self.1.as_deref().map(|cause| cause as _)
    }
}

// A transport error is told with every cause it came from, on one line.
#[test]
fn an_error_is_told_with_its_causes() {
    let error = Wrapped(
        "transport error",
        Some(Box::new(Wrapped(
            "connection reset",
            Some(Box::new(Wrapped("broken pipe", None))),
        ))),
    );
    assert_eq!(
        super::chain(&error),
        "transport error: connection reset: broken pipe"
    );
    assert_eq!(super::chain(&Wrapped("alone", None)), "alone");
}

// A transcript and an assembly come back as the daemon answered them.
#[tokio::test(flavor = "multi_thread", worker_threads = 1)]
async fn a_transcript_and_an_assembly_are_answered() {
    let found = serve(&certificate(), &token(0xab)).await;
    let client = DaemonClient::new(&found, WITHIN);
    let transcript = client
        .get_transcript(v1::GetTranscriptRequest {
            name: "projects/p/sessions/s".to_string(),
        })
        .await
        .unwrap();
    assert_eq!(transcript.name, "projects/p/sessions/s");
    assert_eq!(transcript.entries.len(), 1);
    let assembled = client
        .assemble_context(v1::AssembleContextRequest {
            agent: "reviewer".to_string(),
            ..v1::AssembleContextRequest::default()
        })
        .await
        .unwrap();
    assert_eq!(assembled.context, "context of reviewer");
    assert_eq!(assembled.tokens, 7);
}

// Step 10a U2: a library read comes back with the notices its metadata
// carried, in order.
#[tokio::test(flavor = "multi_thread", worker_threads = 1)]
async fn a_library_read_comes_back_with_its_notices() {
    let found = serve(&certificate(), &token(0xab)).await;
    let client = DaemonClient::new(&found, WITHIN);
    let read = client
        .list_agents(v1::ListAgentsRequest {
            parent: "projects/p".to_string(),
            ..v1::ListAgentsRequest::default()
        })
        .await
        .unwrap();
    assert_eq!(read.answer.agents[0].name, "projects/p/agents/dev");
    let codes: Vec<&str> = read.notices.iter().map(|n| n.code.as_str()).collect();
    assert_eq!(codes, ["library-api-notice-tree-in-motion"]);
}

// A sync is applied unless the coordinator found it a DUPLICATE, and carries
// each projector's projection.
#[tokio::test(flavor = "multi_thread", worker_threads = 1)]
async fn a_sync_says_applied_or_duplicate_with_its_projections() {
    let found = serve(&certificate(), &token(0xab)).await;
    let client = DaemonClient::new(&found, WITHIN);
    let book = angzarr_embedded::v1::CommandBook::default();
    let applied = client
        .handle_sync(book.clone(), vec!["session-index".to_string()])
        .await
        .unwrap();
    assert!(applied.applied);
    assert_eq!(applied.projections.len(), 1);
    assert_eq!(applied.projections[0].projector, "session-index");
    let again = client
        .handle_sync(book, vec!["again".to_string()])
        .await
        .unwrap();
    assert!(!again.applied);
}

// U4: a run is created and read as answered, and a watch gives each event
// in order, then none; a status that ends the stream early is what it came
// to, as any call's is.
#[tokio::test(flavor = "multi_thread", worker_threads = 1)]
async fn a_run_is_created_read_and_watched() {
    let found = serve(&certificate(), &token(0xab)).await;
    let client = DaemonClient::new(&found, WITHIN);
    let run = client
        .create_run(v1::CreateRunRequest {
            parent: "projects/calm-otter".to_string(),
            ..v1::CreateRunRequest::default()
        })
        .await
        .unwrap();
    assert_eq!(run.name, "projects/calm-otter/runs/r1");
    let got = client
        .get_run(v1::GetRunRequest {
            name: run.name.clone(),
        })
        .await
        .unwrap();
    assert_eq!(got.name, run.name);
    let mut events = client
        .watch_run(v1::WatchRunRequest { name: run.name })
        .await
        .unwrap();
    let first = events.next().await.unwrap().unwrap();
    assert_eq!(
        first.event,
        Some(v1::run_event::Event::Reply("HELLO".to_string()))
    );
    let end = events.next().await.unwrap().unwrap();
    assert!(matches!(end.event, Some(v1::run_event::Event::End(_))));
    assert!(events.next().await.is_none(), "nothing after the end");
    let mut broken = client
        .watch_run(v1::WatchRunRequest {
            name: "broken".to_string(),
        })
        .await
        .unwrap();
    assert!(broken.next().await.unwrap().is_ok());
    assert!(matches!(
        broken.next().await.unwrap(),
        Err(Called::Failed { code, .. }) if code == Code::Internal as i32
    ));
}

// The terminal wake: a session watched streams its signals as they come.
#[tokio::test(flavor = "multi_thread", worker_threads = 1)]
async fn a_session_is_watched_its_signals_as_they_come() {
    let found = serve(&certificate(), &token(0xab)).await;
    let mut signals = DaemonClient::new(&found, WITHIN)
        .watch_session(v1::WatchSessionRequest {
            name: "projects/calm-otter/sessions/amber-swift-owl".to_string(),
        })
        .await
        .unwrap();
    assert!(signals.next().await.unwrap().unwrap().wakes.is_empty());
    assert_eq!(signals.next().await.unwrap().unwrap().wakes, ["m1"]);
    assert!(signals.next().await.is_none(), "the stream ended");
}

// U4: a one-shot's stop is the coordinator's `Handle`; a daemon that serves
// no coordinator says so.
#[tokio::test(flavor = "multi_thread", worker_threads = 1)]
async fn a_handle_to_a_daemon_with_no_coordinator_is_unimplemented() {
    let found = serve(&certificate(), &token(0xab)).await;
    let called = DaemonClient::new(&found, WITHIN)
        .handle(daemon_ports::CommandBook::default())
        .await
        .unwrap_err();
    assert!(
        matches!(&called, Called::Unimplemented { method } if method.ends_with("/Handle")),
        "{called:?}"
    );
}

/// What a hearing client was told, call by call.
fn heard_by() -> (
    super::Hears,
    std::sync::Arc<std::sync::Mutex<Vec<Vec<v1::Notice>>>>,
) {
    let heard = std::sync::Arc::new(std::sync::Mutex::new(Vec::new()));
    let told = std::sync::Arc::clone(&heard);
    let hears: super::Hears = std::sync::Arc::new(move |notices| {
        told.lock().unwrap().push(notices);
    });
    (hears, heard)
}

// OD-55: the daemon's warnings ride its answers' metadata; a client
// hearing them is told each answer's, an answer's and a refusal's alike,
// a value that is no notice passed over; an answer with none tells
// nothing.
#[tokio::test(flavor = "multi_thread", worker_threads = 1)]
async fn a_hearing_client_is_told_each_answers_warnings() {
    let found = serve(&certificate(), &token(0xab)).await;
    let (hears, heard) = heard_by();
    let client = DaemonClient::new(&found, WITHIN).hearing(hears);
    client
        .get_daemon(v1::GetDaemonRequest::default())
        .await
        .unwrap();
    client
        .stop_daemon(v1::StopDaemonRequest::default())
        .await
        .unwrap_err();
    client
        .resolve_project(v1::ResolveProjectRequest::default())
        .await
        .unwrap();
    assert_eq!(
        *heard.lock().unwrap(),
        [vec![standing("answered")], vec![standing("refused")]]
    );
}

// A client nobody hears through says nothing of them, and its answer
// stands.
#[tokio::test(flavor = "multi_thread", worker_threads = 1)]
async fn a_client_not_hearing_is_answered_as_ever() {
    let found = serve(&certificate(), &token(0xab)).await;
    let daemon = DaemonClient::new(&found, WITHIN)
        .get_daemon(v1::GetDaemonRequest::default())
        .await
        .unwrap();
    assert_eq!(daemon.pid, 99);
}

#[test]
fn the_warnings_in_metadata_are_its_notices_in_order() {
    let mut metadata = tonic::metadata::MetadataMap::new();
    assert!(super::warnings_in(&metadata).is_empty());
    warned(&mut metadata, "one");
    warned(&mut metadata, "two");
    assert_eq!(
        super::warnings_in(&metadata),
        [standing("one"), standing("two")]
    );
}

// One root for infrastructure errors (owner, 2026-11-14): each error the
// daemon answers through `run_ports::Infrastructure` is read back from its
// status alone, as the client read it before the root.
#[test]
fn each_infrastructure_error_the_daemon_answers_is_read_back() {
    use run_ports::Infrastructure;
    for error in Infrastructure::ALL {
        let status = Status::new(Code::from_i32(error.status()), "said");
        let called = super::called("GetDaemon", &status);
        match error {
            Infrastructure::Unauthenticated => assert_eq!(called, Called::Unauthenticated),
            Infrastructure::Unimplemented => assert_eq!(
                called,
                Called::Unimplemented {
                    method: format!("{}GetDaemon", super::SERVICE)
                }
            ),
            Infrastructure::Unavailable | Infrastructure::TimedOut => assert_eq!(
                called,
                Called::Unreachable {
                    detail: "said".to_string()
                },
                "{error:?}"
            ),
            _ => assert_eq!(
                called,
                Called::Failed {
                    code: error.status(),
                    detail: "said".to_string()
                },
                "{error:?}"
            ),
        }
    }
}

// Step 10a U2: a failure the daemon said in a diagnostic of its own (its
// ErrorInfo's reason the code, its metadata the args) is that diagnostic,
// so a command says `library-application-bundle-unreadable` and exits 32. It travels
// failed-precondition (owner ruling 2026-11-17), and is never read as a
// typed refusal of that status.
#[test]
fn a_diagnosed_failure_is_its_diagnostic() {
    let info = super::ErrorInfo {
        reason: "library-application-bundle-unreadable".to_string(),
        domain: super::DIAGNOSED_DOMAIN.to_string(),
        metadata: [("ask".to_string(), "demo".to_string())]
            .into_iter()
            .collect(),
    };
    let rpc = RpcStatus {
        code: Code::FailedPrecondition as i32,
        message: "library-application-bundle-unreadable".to_string(),
        details: vec![prost_types::Any {
            type_url: "type.googleapis.com/google.rpc.ErrorInfo".to_string(),
            value: info.encode_to_vec(),
        }],
    };
    let status = Status::with_details(
        Code::FailedPrecondition,
        "library-application-bundle-unreadable",
        Bytes::from(rpc.encode_to_vec()),
    );
    let called = super::called("ListBundles", &status);
    assert_eq!(
        called,
        Called::Diagnosed {
            code: "library-application-bundle-unreadable".to_string(),
            args: vec![("ask".to_string(), "demo".to_string())],
        }
    );
    let Called::Diagnosed { code, .. } = &called else {
        panic!("{called:?}");
    };
    assert_eq!(code, "library-application-bundle-unreadable");
}

// Step 10c G7: a library's changes are streamed as the daemon sends them,
// until its end; a stream the daemon breaks ends in what it came to.
#[tokio::test(flavor = "multi_thread", worker_threads = 1)]
async fn a_librarys_changes_are_watched_until_the_stream_ends() {
    let found = serve(&certificate(), &token(0xab)).await;
    let client = DaemonClient::new(&found, WITHIN);
    let mut changes = client
        .watch_library(v1::WatchLibraryRequest {
            parent: "projects/calm-otter".to_string(),
            ..v1::WatchLibraryRequest::default()
        })
        .await
        .unwrap();
    let first = changes.next().await.unwrap().unwrap();
    assert_eq!(first.tree, "projects/calm-otter/library");
    assert_eq!(first.sequence, 7);
    assert!(changes.next().await.is_none(), "nothing after the end");
    let mut broken = client
        .watch_library(v1::WatchLibraryRequest {
            parent: "users/me".to_string(),
            ..v1::WatchLibraryRequest::default()
        })
        .await
        .unwrap();
    assert!(broken.next().await.unwrap().is_ok());
    assert!(matches!(
        broken.next().await.unwrap(),
        Err(Called::Failed { code, .. }) if code == Code::Internal as i32
    ));
}
