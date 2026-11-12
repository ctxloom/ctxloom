//! The daemon's `Ctxloom` service (`proto/ctxloom/daemon/v1/daemon.proto`)
//! over gRPC, served beside angzarr's coordinator on the daemon's one
//! listener (slice 2 step 5): translation only.
//!
//! - Every served RPC is admitted by the user bearer first, through the
//!   same [`UserGate`] as the coordinator; anything else is the bare
//!   UNAUTHENTICATED the gate answers, which says nothing of why (S11).
//! - Its use case runs on the runtime's BLOCKING pool (`spawn_blocking`,
//!   M10), never on the two async workers that serve the listener: a slow
//!   use case holds a blocking thread, never the sockets.
//! - A refusal is answered under the gRPC code its `(refusal_status)`
//!   declares, the typed refusal message packed in `google.rpc.Status.
//!   details` (S11); a failure is INTERNAL.
//! - Every other RPC is the generated default: UNIMPLEMENTED, answered
//!   before any credential is looked at (the API's surface is no secret; a
//!   plain build's `DeleteSession` and `SweepSessions` among them, M5).

use std::future::Future;
use std::sync::Arc;

use angzarr_coordinator::{Arrival, Credential, Gate, Unauthenticated};
use angzarr_coordinator_grpc::{bearer, status_of};
use daemon_api::{ApiError, Calls};
use daemon_proto::service::ctxloom_server::{Ctxloom, CtxloomServer};
use daemon_proto::v1;
use prost::Message;
use runs_application::UserGate;
use tonic::codegen::Bytes;
use tonic::{Code, Request, Response, Status};

/// The service's own name, as a router routes it.
pub use daemon_proto::service::ctxloom_server::SERVICE_NAME;

/// The service, ready to be added to a server: `calls` behind `gate`.
pub fn service(calls: Arc<dyn Calls>, gate: UserGate) -> CtxloomServer<Served> {
    CtxloomServer::new(Served { calls, gate })
}

/// The `Ctxloom` service's handlers.
pub struct Served {
    calls: Arc<dyn Calls>,
    gate: UserGate,
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
    /// Admit `request` by the user bearer, or answer the gate's bare
    /// UNAUTHENTICATED.
    async fn admit<T>(&self, request: &Request<T>) -> Result<(), Status> {
        let presented = bearer(request.metadata())?;
        let credential = presented.map(|bearer| Credential::bearer(bearer.secret().as_bytes()));
        self.gate
            .identify(Arrival::Loopback, credential.as_ref())
            .await
            .map(|_| ())
            .map_err(|_| status_of(&Unauthenticated.coded()))
    }

    /// Admit `request`, then answer it with `call`'s use case, run on the
    /// blocking pool (M10).
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
        self.admit(&request).await?;
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
}

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
