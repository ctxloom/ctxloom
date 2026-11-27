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
//! - `GetTranscript` and `AssembleContext` are served (slice 3), and
//!   `CreateRun`, `GetRun` and `WatchRun` (U4): a watch's events pumped from
//!   a blocking thread into its stream; `GetTrust` and `ListTrusts` (step 8
//!   U1).
//! - `DeleteSession` and `SweepSessions` are served only in a
//!   `purge` build (M5, the feature of the same name); every other
//!   RPC is the generated default, UNIMPLEMENTED, to the user bearer alone
//!   (a plain build's `DeleteSession` and `SweepSessions` among them).

use std::convert::Infallible;
use std::future::Future;
use std::sync::Arc;
use std::task::{Context, Poll};

use angzarr_coordinator::{Arrival, Credential, Gate, Unauthenticated};
use angzarr_coordinator_grpc::{bearer, status_of};
use daemon_api::{ApiError, Calls, Infrastructure};
use daemon_proto::service::ctxloom_server::{Ctxloom, CtxloomServer};
use daemon_proto::v1;
use prost::Message;
use runs_application::UserGate;
use tonic::body::Body;
use tonic::codegen::tokio_stream::wrappers::ReceiverStream;
use tonic::codegen::{http, BoxFuture, BoxStream, Bytes, Service};
use tonic::metadata::{MetadataMap, MetadataValue};
use tonic::server::NamedService;
use tonic::{Code, Request, Response, Status};

/// How many of a watched run's events wait for its caller to read them.
const WATCH_BUFFER: usize = 16;

/// The service's own name, as a router routes it.
pub use daemon_proto::service::ctxloom_server::SERVICE_NAME;

/// The service, ready to be added to a server: `calls` behind `gate`,
/// every request admitted before it reaches a handler or the generated
/// UNIMPLEMENTED.
pub fn service(calls: Arc<dyn Calls>, gate: UserGate) -> Gated<Warned<CtxloomServer<Served>>> {
    Gated {
        inner: Warned {
            inner: CtxloomServer::new(Served {
                calls: Arc::clone(&calls),
            }),
            calls,
        },
        gate: Arc::new(gate),
    }
}

/// A gRPC service `inner` whose every answer carries the daemon's standing
/// warnings (owner ruling OD-55) as response metadata
/// ([`daemon_proto::WARNING_METADATA`], one prost-encoded `Notice` per
/// value), a refusal's or failure's status too: a command learns them from
/// the answer it already has, never by a call of its own. Behind [`Gated`],
/// so a caller that is not the user is told none. The warnings are read
/// once the answer is made, so a drain's answer carries its hand-over.
#[derive(Clone)]
pub struct Warned<S> {
    inner: S,
    calls: Arc<dyn Calls>,
}

impl<S: NamedService> NamedService for Warned<S> {
    const NAME: &'static str = S::NAME;
}

impl<S> Service<http::Request<Body>> for Warned<S>
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
        let calls = Arc::clone(&self.calls);
        // As [`Gated`]: the clone made ready is the one called.
        let fresh = self.inner.clone();
        let mut inner = std::mem::replace(&mut self.inner, fresh);
        Box::pin(async move {
            let mut response = inner.call(request).await?;
            warn(response.headers_mut(), &calls.warnings());
            Ok(response)
        })
    }
}

/// A gRPC service `inner` that tells `tell` each request's path (its
/// method, `/ctxloom.v1.Ctxloom/GetDaemon`) as it arrives: the daemon's
/// own record of the calls made of it, for a test under test-support to
/// count them (OD-55: no command asks `GetDaemon` for the warnings).
#[derive(Clone)]
pub struct Told<S> {
    inner: S,
    tell: Arc<dyn Fn(&str) + Send + Sync>,
}

/// `inner`, each request's path told to `tell` ([`Told`]).
pub fn telling<S>(inner: S, tell: Arc<dyn Fn(&str) + Send + Sync>) -> Told<S> {
    Told { inner, tell }
}

impl<S: NamedService> NamedService for Told<S> {
    const NAME: &'static str = S::NAME;
}

impl<S> Service<http::Request<Body>> for Told<S>
where
    S: Service<http::Request<Body>, Response = http::Response<Body>, Error = Infallible>,
{
    type Response = http::Response<Body>;
    type Error = Infallible;
    type Future = S::Future;

    fn poll_ready(&mut self, cx: &mut Context<'_>) -> Poll<Result<(), Self::Error>> {
        self.inner.poll_ready(cx)
    }

    fn call(&mut self, request: http::Request<Body>) -> Self::Future {
        (self.tell)(request.uri().path());
        self.inner.call(request)
    }
}

/// `warnings` added to `headers`, one [`daemon_proto::WARNING_METADATA`]
/// value each, in order.
pub fn warn(headers: &mut http::HeaderMap, warnings: &[v1::Notice]) {
    let mut metadata = MetadataMap::new();
    for warning in warnings {
        metadata.append_bin(
            daemon_proto::WARNING_METADATA,
            MetadataValue::from_bytes(&warning.encode_to_vec()),
        );
    }
    headers.extend(metadata.into_headers());
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

/// `google.rpc.ErrorInfo`, as googleapis defines it.
#[derive(Clone, PartialEq, Message)]
struct ErrorInfo {
    #[prost(string, tag = "1")]
    reason: String,
    #[prost(string, tag = "2")]
    domain: String,
    #[prost(btree_map = "string, string", tag = "3")]
    metadata: std::collections::BTreeMap<String, String>,
}

/// The `ErrorInfo` domain of a failure the daemon says in a diagnostic of
/// its own: its reason is the diagnostic's code.
pub const DIAGNOSED_DOMAIN: &str = daemon_api::DIAGNOSED;

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
        ApiError::Failed { detail } => infrastructure(Infrastructure::Failed, detail),
        // A failure in a diagnostic of its own (step 10a U2): its code as a
        // `google.rpc.ErrorInfo`'s reason, its args its metadata.
        ApiError::Diagnosed { code, args } => {
            let info = ErrorInfo {
                reason: code.clone(),
                domain: DIAGNOSED_DOMAIN.to_string(),
                metadata: args.into_iter().map(|arg| (arg.name, arg.value)).collect(),
            };
            let status = Code::from_i32(Infrastructure::Unmet.status());
            let details = RpcStatus {
                code: status as i32,
                message: code.clone(),
                details: vec![prost_types::Any {
                    type_url: "type.googleapis.com/google.rpc.ErrorInfo".to_string(),
                    value: info.encode_to_vec(),
                }],
            };
            Status::with_details(status, code, Bytes::from(details.encode_to_vec()))
        }
        ApiError::Unimplemented { method } => infrastructure(
            Infrastructure::Unimplemented,
            format!("{method} is not served yet"),
        ),
    }
}

/// An infrastructure error, said `message`, as the status the tree's one
/// root ([`Infrastructure`]) names for it: a status this service never
/// spells itself.
fn infrastructure(error: Infrastructure, message: impl Into<String>) -> Status {
    Status::new(Code::from_i32(error.status()), message)
}

impl Served {
    /// Answer `request` (admitted already, by [`Gated`]) with `call`'s use
    /// case, run on the blocking pool (M10). A request that carries a
    /// `Caller` is first admitted by its client's version
    /// ([`Calls::admit`]): one of another version is refused, and its use
    /// case never runs.
    async fn answer<T, R, F, Fut>(
        &self,
        request: Request<T>,
        call: F,
    ) -> Result<Response<R>, Status>
    where
        T: daemon_proto::Carries + Send + 'static,
        R: Send + 'static,
        F: FnOnce(Arc<dyn Calls>, T) -> Fut + Send + 'static,
        Fut: Future<Output = Result<R, ApiError>> + Send,
    {
        let calls = Arc::clone(&self.calls);
        let message = request.into_inner();
        if T::CALLER {
            calls.admit(message.caller()).map_err(status_of_error)?;
        }
        let runtime = tokio::runtime::Handle::current();
        let answered = tokio::task::spawn_blocking(move || runtime.block_on(call(calls, message)))
            .await
            .map_err(|joined| infrastructure(Infrastructure::Failed, joined.to_string()))?;
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

    async fn get_transcript(
        &self,
        request: Request<v1::GetTranscriptRequest>,
    ) -> Result<Response<v1::Transcript>, Status> {
        self.answer(request, |calls, message| async move {
            calls.get_transcript(message).await
        })
        .await
    }

    async fn assemble_context(
        &self,
        request: Request<v1::AssembleContextRequest>,
    ) -> Result<Response<v1::AssembledContext>, Status> {
        self.answer(request, |calls, message| async move {
            calls.assemble_context(message).await
        })
        .await
    }

    async fn create_approval(
        &self,
        request: Request<v1::CreateApprovalRequest>,
    ) -> Result<Response<v1::Approval>, Status> {
        self.answer(request, |calls, message| async move {
            calls.create_approval(message).await
        })
        .await
    }

    async fn get_approval(
        &self,
        request: Request<v1::GetApprovalRequest>,
    ) -> Result<Response<v1::Approval>, Status> {
        self.answer(request, |calls, message| async move {
            calls.get_approval(message).await
        })
        .await
    }

    async fn list_approvals(
        &self,
        request: Request<v1::ListApprovalsRequest>,
    ) -> Result<Response<v1::ListApprovalsResponse>, Status> {
        self.answer(request, |calls, message| async move {
            calls.list_approvals(message).await
        })
        .await
    }

    /// Awaited here, never on a blocking thread: the wait lasts as long as
    /// the user takes, and a caller that hangs up (its deadline passed, its
    /// call cancelled) drops it, so no wait outlives its caller.
    async fn wait_approval(
        &self,
        request: Request<v1::WaitApprovalRequest>,
    ) -> Result<Response<v1::Approval>, Status> {
        let calls = Arc::clone(&self.calls);
        calls
            .wait_approval(request.into_inner())
            .await
            .map(Response::new)
            .map_err(status_of_error)
    }

    async fn get_trust(
        &self,
        request: Request<v1::GetTrustRequest>,
    ) -> Result<Response<v1::Trust>, Status> {
        self.answer(request, |calls, message| async move {
            calls.get_trust(message).await
        })
        .await
    }

    async fn list_trusts(
        &self,
        request: Request<v1::ListTrustsRequest>,
    ) -> Result<Response<v1::ListTrustsResponse>, Status> {
        self.answer(request, |calls, message| async move {
            calls.list_trusts(message).await
        })
        .await
    }

    async fn create_run(
        &self,
        request: Request<v1::CreateRunRequest>,
    ) -> Result<Response<v1::Run>, Status> {
        self.answer(request, |calls, message| async move {
            calls.create_run(message).await
        })
        .await
    }

    async fn create_hand_off(
        &self,
        request: Request<v1::CreateHandOffRequest>,
    ) -> Result<Response<v1::HandOff>, Status> {
        self.answer(request, |calls, message| async move {
            calls.create_hand_off(message).await
        })
        .await
    }

    async fn redeliver_context(
        &self,
        request: Request<v1::RedeliverContextRequest>,
    ) -> Result<Response<v1::RedeliveredContext>, Status> {
        self.answer(request, |calls, message| async move {
            calls.redeliver_context(message).await
        })
        .await
    }

    async fn deliver_hook_context(
        &self,
        request: Request<v1::DeliverHookContextRequest>,
    ) -> Result<Response<v1::HookContext>, Status> {
        self.answer(request, |calls, message| async move {
            calls.deliver_hook_context(message).await
        })
        .await
    }

    async fn acknowledge_hook_delivery(
        &self,
        request: Request<v1::AcknowledgeHookDeliveryRequest>,
    ) -> Result<Response<v1::HookDeliveryAcknowledged>, Status> {
        self.answer(request, |calls, message| async move {
            calls.acknowledge_hook_delivery(message).await
        })
        .await
    }

    async fn get_run(
        &self,
        request: Request<v1::GetRunRequest>,
    ) -> Result<Response<v1::Run>, Status> {
        self.answer(request, |calls, message| async move {
            calls.get_run(message).await
        })
        .await
    }

    async fn delete_hand_off(
        &self,
        request: Request<v1::DeleteHandOffRequest>,
    ) -> Result<Response<v1::HandOff>, Status> {
        self.answer(request, |calls, message| async move {
            calls.delete_hand_off(message).await
        })
        .await
    }

    /// The run's events, pumped from a blocking thread (each waited for
    /// there, M10) into the response stream, until its end or until the
    /// caller is gone; a caller that goes changes nothing of the run (owner
    /// s4-Q7).
    async fn watch_run(
        &self,
        request: Request<v1::WatchRunRequest>,
    ) -> Result<Response<BoxStream<v1::RunEvent>>, Status> {
        let events = self
            .answer(request, |calls, message| async move {
                calls.watch_run(message).await
            })
            .await?
            .into_inner();
        let (tell, heard) = tokio::sync::mpsc::channel(WATCH_BUFFER);
        tokio::task::spawn_blocking(move || {
            for event in events {
                if tell.blocking_send(Ok(event)).is_err() {
                    return;
                }
            }
        });
        Ok(Response::new(Box::pin(ReceiverStream::new(heard))))
    }

    /// The named trees' changes (step 10c G7), drained on a task of their
    /// own into the response stream each time the catch-up signals one
    /// recorded (step 10c U13 rework: no poll), until the caller is gone.
    async fn watch_library(
        &self,
        request: Request<v1::WatchLibraryRequest>,
    ) -> Result<Response<BoxStream<v1::LibraryChange>>, Status> {
        let signal = std::sync::Arc::new(tokio::sync::Notify::new());
        let woken = std::sync::Arc::clone(&signal);
        let wake: daemon_api::Wake = std::sync::Arc::new(move || woken.notify_one());
        let mut poll = self
            .answer(request, |calls, message| async move {
                calls.watch_library(message, wake).await
            })
            .await?
            .into_inner();
        let (tell, heard) = tokio::sync::mpsc::channel(WATCH_BUFFER);
        tokio::spawn(async move {
            loop {
                for change in poll() {
                    if tell.send(Ok(change)).await.is_err() {
                        return;
                    }
                }
                tokio::select! {
                    () = signal.notified() => {}
                    () = tell.closed() => return,
                }
            }
        });
        Ok(Response::new(Box::pin(ReceiverStream::new(heard))))
    }

    /// The session's signals (the terminal wake), pumped from a blocking
    /// thread as a run's events are, until the caller is gone: the next
    /// signal (a keepalive at the latest) that cannot be sent ends the
    /// pump, and so the watch.
    async fn watch_session(
        &self,
        request: Request<v1::WatchSessionRequest>,
    ) -> Result<Response<BoxStream<v1::SessionSignal>>, Status> {
        let signals = self
            .answer(request, |calls, message| async move {
                calls.watch_session(message).await
            })
            .await?
            .into_inner();
        let (tell, heard) = tokio::sync::mpsc::channel(WATCH_BUFFER);
        tokio::task::spawn_blocking(move || {
            for signal in signals {
                if tell.blocking_send(Ok(signal)).is_err() {
                    return;
                }
            }
        });
        Ok(Response::new(Box::pin(ReceiverStream::new(heard))))
    }

    /// `ctxloom top`'s watch (DRAFT): the snapshot, then each change as the
    /// daemon's models push it, pumped from a blocking thread until the
    /// caller is gone or the watch ends (its one session destroyed).
    async fn watch_top(
        &self,
        request: Request<v1::WatchTopRequest>,
    ) -> Result<Response<BoxStream<v1::TopEvent>>, Status> {
        let events = self
            .answer(request, |calls, message| async move {
                calls.watch_top(message).await
            })
            .await?
            .into_inner();
        let (tell, heard) = tokio::sync::mpsc::channel(WATCH_BUFFER);
        // A watch with nothing to say never fails a send: it is ended when
        // the client's stream is dropped, not left parked on its models.
        let (stop, closed) = (events.stopper(), tell.clone());
        let pump = tokio::task::spawn_blocking(move || {
            for event in events {
                if tell.blocking_send(Ok(event)).is_err() {
                    return;
                }
            }
        });
        // The watch is ended when the client's stream is dropped; and the
        // stream is ended when the watch is, the sender here let go with it.
        tokio::spawn(async move {
            tokio::select! {
                () = closed.closed() => stop.stop(),
                _ = pump => {}
            }
        });
        Ok(Response::new(Box::pin(ReceiverStream::new(heard))))
    }

    #[cfg(feature = "purge")]
    async fn delete_session(
        &self,
        request: Request<v1::DeleteSessionRequest>,
    ) -> Result<Response<v1::DeleteSessionResponse>, Status> {
        self.answer(request, |calls, message| async move {
            calls.delete_session(message).await
        })
        .await
    }

    async fn import_sessions(
        &self,
        request: Request<v1::ImportSessionsRequest>,
    ) -> Result<Response<v1::ImportSessionsResponse>, Status> {
        self.answer(request, |calls, message| async move {
            calls.import_sessions(message).await
        })
        .await
    }

    #[cfg(feature = "purge")]
    async fn sweep_sessions(
        &self,
        request: Request<v1::SweepSessionsRequest>,
    ) -> Result<Response<v1::SweepSessionsResponse>, Status> {
        self.answer(request, |calls, message| async move {
            calls.sweep_sessions(message).await
        })
        .await
    }

    // The library's reads (step 10a U2, D12), from the one table: each
    // answered with its message, the notices the read said riding as
    // `NOTICE_METADATA` (owner ruling 2026-11-17).
    daemon_proto::library_reads!(library_read_handlers);

    // The library's writes (step 10b U3, D12), from their one table: each
    // answered with its message, its notices inside it.
    daemon_proto::library_writes!(library_write_handlers);
}

/// [`Served`]'s library writes, from `daemon_proto::library_writes!`, in
/// the shape `tonic::async_trait` gives the generated trait's methods.
macro_rules! library_write_handlers {
    ($($method:ident ($rpc:ident) $request:ident => $response:ident),* $(,)?) => {
        $(
            fn $method<'life0, 'async_trait>(
                &'life0 self,
                request: Request<v1::$request>,
            ) -> std::pin::Pin<
                Box<
                    dyn Future<Output = Result<Response<v1::$response>, Status>>
                        + Send
                        + 'async_trait,
                >,
            >
            where
                'life0: 'async_trait,
                Self: 'async_trait,
            {
                Box::pin(async move {
                    self.answer(request, |calls, message| async move {
                        calls.$method(message).await
                    })
                    .await
                })
            }
        )*
    };
}
use library_write_handlers;

/// [`Served`]'s library reads, from `daemon_proto::library_reads!`, in the
/// shape `tonic::async_trait` gives the generated trait's methods.
macro_rules! library_read_handlers {
    ($($method:ident ($rpc:ident) $request:ident . $target:ident => $response:ident),* $(,)?) => {
        $(
            fn $method<'life0, 'async_trait>(
                &'life0 self,
                request: Request<v1::$request>,
            ) -> std::pin::Pin<
                Box<
                    dyn Future<Output = Result<Response<v1::$response>, Status>>
                        + Send
                        + 'async_trait,
                >,
            >
            where
                'life0: 'async_trait,
                Self: 'async_trait,
            {
                Box::pin(async move {
                    let read = self
                        .answer(request, |calls, message| async move {
                            calls.$method(message).await
                        })
                        .await?
                        .into_inner();
                    let mut response = Response::new(read.answer);
                    noticed(response.metadata_mut(), &read.notices);
                    Ok(response)
                })
            }
        )*
    };
}
use library_read_handlers;

/// `notices` added to `metadata`, one [`daemon_proto::NOTICE_METADATA`]
/// value each, in order: a library read's own (step 10a U2).
pub fn noticed(metadata: &mut MetadataMap, notices: &[v1::Notice]) {
    for notice in notices {
        metadata.append_bin(
            daemon_proto::NOTICE_METADATA,
            MetadataValue::from_bytes(&notice.encode_to_vec()),
        );
    }
}

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
