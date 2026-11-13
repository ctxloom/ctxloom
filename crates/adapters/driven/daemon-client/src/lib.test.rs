//! The client against a fake daemon on a loopback TLS port: the pinned
//! certificate, the bearer it presents, each outcome read back.

use std::time::Duration;

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
        Ok(Response::new(v1::Daemon {
            name: request.into_inner().name,
            pid: 99,
            ..v1::Daemon::default()
        }))
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
        Err(Status::with_details(
            Code::FailedPrecondition,
            "ctxloom.v1.DaemonHosting",
            Bytes::from(details.encode_to_vec()),
        ))
    }

    /// A project resolved: named by the cwd it was asked from.
    async fn resolve_project(
        &self,
        request: Request<v1::ResolveProjectRequest>,
    ) -> Result<Response<v1::Project>, Status> {
        self.admit(&request)?;
        Ok(Response::new(v1::Project {
            name: "projects/calm-quiet-heron".to_string(),
            root: request.into_inner().cwd,
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
            applied: true,
            ..v1::DeleteSessionResponse::default()
        }))
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
            ..v1::GetDaemonRequest::default()
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
    assert!(done.applied);
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
            cwd: "/work/calm".to_string(),
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
