//! daemon-ports' [`DaemonApi`] over gRPC (owner M7, slice 2 step 5): the
//! `Ctxloom` service's client, built from what the discovery file names
//! (T12, S5). Every call goes over TLS pinning the daemon's certificate as
//! its SOLE root (no system roots, no plaintext), presents the user bearer,
//! and is bounded by a deadline. A status is read back as [`Called`]: a
//! refusal as its typed message from `google.rpc.Status.details` (S11).

// RED: the calls are not made yet.
#![allow(
    dead_code,
    unused_imports,
    unused_variables,
    unused_macros,
    clippy::all
)]

use std::time::Duration;

use daemon_ports::{Called, DaemonApi};
use daemon_proto::service::ctxloom_client::CtxloomClient;
use daemon_proto::v1;
use prost::Message;
use run_ports::{parse_endpoint, Discovery, PublicCert, Token};
use tonic::transport::{Certificate, Channel, ClientTlsConfig, Endpoint};
use tonic::{Code, Request, Status};

/// The service's own path, for a method a status names.
const SERVICE: &str = "/ctxloom.daemon.v1.Ctxloom/";

/// The daemon a discovery file names, as a client calls it.
#[derive(Clone)]
pub struct DaemonClient {
    endpoint: String,
    cert: PublicCert,
    bearer: Token,
    within: Duration,
}

impl std::fmt::Debug for DaemonClient {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("DaemonClient")
            .field("endpoint", &self.endpoint)
            .field("bearer", &"<redacted>")
            .finish_non_exhaustive()
    }
}

impl DaemonClient {
    /// The daemon `found` names, each call within `within`.
    pub fn new(found: &Discovery, within: Duration) -> DaemonClient {
        DaemonClient {
            endpoint: found.endpoint.clone(),
            cert: found.cert.clone(),
            bearer: found.bearer.clone(),
            within,
        }
    }

    /// A client to the daemon, over a fresh channel.
    async fn client(&self) -> Result<CtxloomClient<Channel>, Called> {
        let unreachable = |detail: String| Called::Unreachable { detail };
        let dial = parse_endpoint(&self.endpoint).map_err(|e| unreachable(e.to_string()))?;
        let tls = ClientTlsConfig::new()
            .ca_certificate(Certificate::from_pem(self.cert.to_pem()))
            .domain_name(dial.host.clone());
        let channel = Endpoint::from_shared(dial.url())
            .map_err(|e| unreachable(chain(&e)))?
            .connect_timeout(self.within)
            .timeout(self.within)
            .tls_config(tls)
            .map_err(|e| unreachable(chain(&e)))?
            .connect()
            .await
            .map_err(|e| unreachable(chain(&e)))?;
        Ok(CtxloomClient::new(channel))
    }

    /// `message`, with the user bearer.
    fn request<T>(&self, message: T) -> Result<Request<T>, Called> {
        let mut request = Request::new(message);
        let value = format!("Bearer {}", self.bearer.to_text())
            .parse()
            .map_err(|_| Called::Unauthenticated)?;
        request.metadata_mut().insert("authorization", value);
        Ok(request)
    }
}

/// `google.rpc.Status`: what a status's details carry.
#[derive(Clone, PartialEq, Message)]
struct RpcStatus {
    #[prost(int32, tag = "1")]
    code: i32,
    #[prost(string, tag = "2")]
    message: String,
    #[prost(message, repeated, tag = "3")]
    details: Vec<prost_types::Any>,
}

/// What `status`, answered to `method`, came to.
pub fn called(method: &str, status: &Status) -> Called {
    if let Some(refusal) = RpcStatus::decode(status.details())
        .ok()
        .and_then(|rpc| rpc.details.into_iter().next())
    {
        return Called::Refused {
            code: status.code() as i32,
            refusal,
        };
    }
    match status.code() {
        Code::Unauthenticated => Called::Unauthenticated,
        Code::Unimplemented => Called::Unimplemented {
            method: format!("{SERVICE}{method}"),
        },
        Code::Unavailable | Code::DeadlineExceeded => Called::Unreachable {
            detail: status.message().to_string(),
        },
        code => Called::Failed {
            code: code as i32,
            detail: status.message().to_string(),
        },
    }
}

/// An error with its sources, as one line.
fn chain(error: &dyn std::error::Error) -> String {
    let mut text = error.to_string();
    let mut source = error.source();
    while let Some(cause) = source {
        text.push_str(": ");
        text.push_str(&cause.to_string());
        source = cause.source();
    }
    text
}

/// One call: `$method` of the client with `$message`, its status read back.
macro_rules! call {
    ($self:ident, $method:ident, $name:literal, $message:expr) => {{
        // RED: not called yet.
        let _ = (&$message, called, chain, $name);
        let _ = |c: CtxloomClient<Channel>| c;
        let _ = $self.request(());
        Err::<_, Called>(Called::Failed {
            code: 2,
            detail: String::new(),
        })
    }};
}

impl DaemonApi for DaemonClient {
    async fn get_daemon(&self) -> Result<v1::Daemon, Called> {
        let request = v1::GetDaemonRequest {
            name: "daemon".to_string(),
        };
        call!(self, get_daemon, "GetDaemon", request)
    }

    async fn stop_daemon(&self, request: v1::StopDaemonRequest) -> Result<v1::Daemon, Called> {
        call!(self, stop_daemon, "StopDaemon", request)
    }

    async fn resolve_project(
        &self,
        request: v1::ResolveProjectRequest,
    ) -> Result<v1::Project, Called> {
        call!(self, resolve_project, "ResolveProject", request)
    }

    async fn get_project(&self, request: v1::GetProjectRequest) -> Result<v1::Project, Called> {
        call!(self, get_project, "GetProject", request)
    }

    async fn get_session(&self, request: v1::GetSessionRequest) -> Result<v1::Session, Called> {
        call!(self, get_session, "GetSession", request)
    }

    async fn list_sessions(
        &self,
        request: v1::ListSessionsRequest,
    ) -> Result<v1::ListSessionsResponse, Called> {
        call!(self, list_sessions, "ListSessions", request)
    }

    async fn search_sessions(
        &self,
        request: v1::SearchSessionsRequest,
    ) -> Result<v1::ListSessionsResponse, Called> {
        call!(self, search_sessions, "SearchSessions", request)
    }
}

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
