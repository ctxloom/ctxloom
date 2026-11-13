//! The daemon's `Ctxloom` service (`proto/ctxloom/v1/daemon.proto`)
//! over gRPC, served beside angzarr's coordinator on the daemon's one
//! listener (slice 2 step 5): translation only.
//!
//! - GATE FIRST, EVERYTHING (owner, after step 5): every request to the
//!   service, served RPC or not, is admitted by the user bearer first,
//!   through the same [`UserGate`] as the coordinator ([`Gated`], around
//!   the whole generated server); anything else is the one bare
//!   UNAUTHENTICATED the gate answers, which says nothing of why (S11), nor
//!   whether the RPC is served: a caller that is not the user learns
//!   nothing of the API, not even its shape.
//! - Its use case runs on the runtime's BLOCKING pool (`spawn_blocking`,
//!   M10), never on the two async workers that serve the listener: a slow
//!   use case holds a blocking thread, never the sockets.
//! - A refusal is answered under the gRPC code its `(refusal_status)`
//!   declares, the typed refusal message packed in `google.rpc.Status.
//!   details` (S11); a failure is INTERNAL.
//! - `DeleteSession` and `SweepSessions` are served only in a
//!   `session-destroy` build (M5, the feature of the same name); every other
//!   RPC is the generated default, UNIMPLEMENTED, to the user bearer alone
//!   (a plain build's `DeleteSession` and `SweepSessions` among them).

use std::convert::Infallible;
use std::future::Future;
use std::sync::Arc;
use std::task::{Context, Poll};

use angzarr_coordinator::{Arrival, Credential, Gate, Unauthenticated};
use angzarr_coordinator_grpc::{bearer, status_of};
use daemon_api::{ApiError, Calls};
use daemon_proto::service::ctxloom_server::{Ctxloom, CtxloomServer};
use daemon_proto::v1;
use prost::Message;
use runs_application::UserGate;
use tonic::body::Body;
use tonic::codegen::{http, BoxFuture, Bytes, Service};
use tonic::metadata::MetadataMap;
use tonic::server::NamedService;
use tonic::{Code, Request, Response, Status};

/// The service's own name, as a router routes it.
pub use daemon_proto::service::ctxloom_server::SERVICE_NAME;

/// The service, ready to be added to a server: `calls` behind `gate`,
/// every request admitted before it reaches a handler or the generated
/// UNIMPLEMENTED.
pub fn service(calls: Arc<dyn Calls>, gate: UserGate) -> Gated<CtxloomServer<Served>> {
    Gated {
        inner: CtxloomServer::new(Served { calls }),
        gate: Arc::new(gate),
    }
}

/// The `Ctxloom` service's handlers.
pub struct Served {
    calls: Arc<dyn Calls>,
}

/// A gRPC service `inner` behind the user bearer: a request is handed on
/// only once `gate` admits the bearer it presents; any other (none, another
/// bearer, a malformed `authorization`) is answered the gate's one bare
/// UNAUTHENTICATED, whatever its path names.
#[derive(Clone)]
pub struct Gated<S> {
    inner: S,
    gate: Arc<UserGate>,
}

impl<S: NamedService> NamedService for Gated<S> {
    const NAME: &'static str = S::NAME;
}

impl<S> Service<http::Request<Body>> for Gated<S>
where
    S: Service<http::Request<Body>, Response = http::Response<Body>, Error = Infallible>
        + Clone
        + Send
        + 'static,
    S::Future: Send + 'static,
{
    type Response = http::Response<Body>;
    type Error = Infallible;
    type Future = BoxFuture<Self::Response, Self::Error>;

    fn poll_ready(&mut self, cx: &mut Context<'_>) -> Poll<Result<(), Self::Error>> {
        self.inner.poll_ready(cx)
    }

    fn call(&mut self, request: http::Request<Body>) -> Self::Future {
        let gate = Arc::clone(&self.gate);
        // The clone that was made ready is the one called (tower's rule);
        // the one left behind is readied again before the next call.
        let fresh = self.inner.clone();
        let mut inner = std::mem::replace(&mut self.inner, fresh);
        Box::pin(async move {
            let metadata = MetadataMap::from_headers(request.headers().clone());
            if admitted(&gate, &metadata).await {
                inner.call(request).await
            } else {
                Ok(status_of(&Unauthenticated.coded()).into_http())
            }
        })
    }
}

/// Whether `metadata` presents the bearer `gate` admits.
async fn admitted(gate: &UserGate, metadata: &MetadataMap) -> bool {
    let Ok(Some(presented)) = bearer(metadata) else {
        return false;
    };
    let credential = Credential::bearer(presented.secret().as_bytes());
    gate.identify(Arrival::Loopback, Some(&credential))
        .await
        .is_ok()
}

/// `google.rpc.Status`, as googleapis defines it: what a status's details
/// carry.
#[derive(Clone, PartialEq, Message)]
struct RpcStatus {
    #[prost(int32, tag = "1")]
    code: i32,
    #[prost(string, tag = "2")]
    message: String,
    #[prost(message, repeated, tag = "3")]
    details: Vec<prost_types::Any>,
}

/// `error` as the status the caller is answered.
fn status_of_error(error: ApiError) -> Status {
    match error {
        ApiError::Refused(refused) => {
            let code = Code::from_i32(refused.code);
            let message = refused
                .refusal
                .type_url
                .rsplit('/')
                .next()
                .unwrap_or_default()
                .to_string();
            let details = RpcStatus {
                code: refused.code,
                message: message.clone(),
                details: vec![refused.refusal],
            };
            Status::with_details(code, message, Bytes::from(details.encode_to_vec()))
        }
        ApiError::Failed { detail } => Status::internal(detail),
    }
}

impl Served {
    /// Answer `request` (admitted already, by [`Gated`]) with `call`'s use
    /// case, run on the blocking pool (M10).
    async fn answer<T, R, F, Fut>(
        &self,
        request: Request<T>,
        call: F,
    ) -> Result<Response<R>, Status>
    where
        T: Send + 'static,
        R: Send + 'static,
        F: FnOnce(Arc<dyn Calls>, T) -> Fut + Send + 'static,
        Fut: Future<Output = Result<R, ApiError>> + Send,
    {
        let calls = Arc::clone(&self.calls);
        let message = request.into_inner();
        let runtime = tokio::runtime::Handle::current();
        let answered = tokio::task::spawn_blocking(move || runtime.block_on(call(calls, message)))
            .await
            .map_err(|joined| Status::internal(joined.to_string()))?;
        answered.map(Response::new).map_err(status_of_error)
    }
}

#[tonic::async_trait]
impl Ctxloom for Served {
    async fn get_daemon(
        &self,
        request: Request<v1::GetDaemonRequest>,
    ) -> Result<Response<v1::Daemon>, Status> {
        self.answer(request, |calls, _| async move { calls.get_daemon().await })
            .await
    }

    async fn stop_daemon(
        &self,
        request: Request<v1::StopDaemonRequest>,
    ) -> Result<Response<v1::Daemon>, Status> {
        self.answer(request, |calls, message| async move {
            calls.stop_daemon(message).await
        })
        .await
    }

    async fn resolve_project(
        &self,
        request: Request<v1::ResolveProjectRequest>,
    ) -> Result<Response<v1::Project>, Status> {
        self.answer(request, |calls, message| async move {
            calls.resolve_project(message).await
        })
        .await
    }

    async fn get_project(
        &self,
        request: Request<v1::GetProjectRequest>,
    ) -> Result<Response<v1::Project>, Status> {
        self.answer(request, |calls, message| async move {
            calls.get_project(message).await
        })
        .await
    }

    async fn get_session(
        &self,
        request: Request<v1::GetSessionRequest>,
    ) -> Result<Response<v1::Session>, Status> {
        self.answer(request, |calls, message| async move {
            calls.get_session(message).await
        })
        .await
    }

    async fn list_sessions(
        &self,
        request: Request<v1::ListSessionsRequest>,
    ) -> Result<Response<v1::ListSessionsResponse>, Status> {
        self.answer(request, |calls, message| async move {
            calls.list_sessions(message).await
        })
        .await
    }

    async fn search_sessions(
        &self,
        request: Request<v1::SearchSessionsRequest>,
    ) -> Result<Response<v1::ListSessionsResponse>, Status> {
        self.answer(request, |calls, message| async move {
            calls.search_sessions(message).await
        })
        .await
    }

    #[cfg(feature = "session-destroy")]
    async fn delete_session(
        &self,
        request: Request<v1::DeleteSessionRequest>,
    ) -> Result<Response<v1::DeleteSessionResponse>, Status> {
        self.answer(request, |calls, message| async move {
            calls.delete_session(message).await
        })
        .await
    }

    #[cfg(feature = "session-destroy")]
    async fn sweep_sessions(
        &self,
        request: Request<v1::SweepSessionsRequest>,
    ) -> Result<Response<v1::SweepSessionsResponse>, Status> {
        self.answer(request, |calls, message| async move {
            calls.sweep_sessions(message).await
        })
        .await
    }
}

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
