//! daemon-ports' [`DaemonApi`] over gRPC (owner M7, slice 2 step 5): the
//! `Ctxloom` service's client, built from what the discovery file names
//! (T12, S5). Every call goes over TLS pinning the daemon's certificate as
//! its SOLE root (no system roots, no plaintext), presents the user bearer,
//! and is bounded by a deadline. A status is read back as [`Called`]: a
//! refusal as its typed message from `google.rpc.Status.details` (S11).
//! A domain write goes to angzarr's command coordinator the daemon serves
//! beside the service, `HandleSync` (slice 3 step 4), the same way. The
//! daemon's standing warnings ride every answer's metadata (owner ruling
//! OD-55, [`daemon_proto::WARNING_METADATA`]), a refusal's too: a client
//! [`DaemonClient::hearing`] them is told each answer's, so no call is made
//! to ask for them.

use std::sync::Arc;
use std::time::Duration;

use angzarr_embedded::v1::{CommandBook, DeliveryOutcome};
use angzarr_proto_transport_distributed::v1::command_handler_coordinator_service_client::CommandHandlerCoordinatorServiceClient;
use angzarr_proto_transport_distributed::v1::HandleSyncRequest;
use daemon_ports::{Called, DaemonApi, LibraryChanges, RunEvents, SessionSignals, Synced};
use daemon_proto::service::ctxloom_client::CtxloomClient;
use daemon_proto::v1;
use prost::Message;
use run_ports::{parse_endpoint, Discovery, Infrastructure, PublicCert, Token};
use tonic::metadata::MetadataMap;
use tonic::transport::{Certificate, Channel, ClientTlsConfig, Endpoint};
use tonic::{Request, Status};

/// The service's own path, for a method a status names.
const SERVICE: &str = "/ctxloom.v1.Ctxloom/";

/// The daemon a discovery file names, as a client calls it.
#[derive(Clone)]
pub struct DaemonClient {
    endpoint: String,
    cert: PublicCert,
    bearer: Token,
    within: Duration,
    /// A destroy's deadline (`DeleteSession`, `SweepSessions`).
    destroying: Duration,
    /// What is told the warnings each answer carries (OD-55), if anything.
    hears: Option<Hears>,
    /// What every request made on the user's behalf carries of its client
    /// (owner, 2026-11-18): filled into each one's `Caller` where the
    /// request says none of its own.
    caller: Option<v1::Caller>,
}

/// What a client tells the daemon's warnings an answer carried: called
/// once per answer that carries any, with them in order.
pub type Hears = Arc<dyn Fn(Vec<v1::Notice>) + Send + Sync>;

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
            destroying: within,
            hears: None,
            caller: None,
        }
    }

    /// This client, every request that carries a `Caller` filled with
    /// `caller` where it names none of its own (its directory, environment,
    /// flags, presentable, client): the one place every request a process
    /// makes of the daemon is given its client, whatever path built it.
    pub fn filling(self, caller: v1::Caller) -> DaemonClient {
        DaemonClient {
            caller: Some(caller),
            ..self
        }
    }

    /// This client, the warnings each answer carries (OD-55) told to
    /// `hears`.
    pub fn hearing(self, hears: Hears) -> DaemonClient {
        DaemonClient {
            hears: Some(hears),
            ..self
        }
    }

    /// The warnings `metadata` carries told to what hears them, if any do.
    fn heard(&self, metadata: &MetadataMap) {
        if let Some(hears) = &self.hears {
            let warnings = warnings_in(metadata);
            if !warnings.is_empty() {
                hears(warnings);
            }
        }
    }

    /// This client, each destroy (`DeleteSession`, `SweepSessions`: its
    /// worktrees' git can outlast a read's deadline) bounded by `within`.
    pub fn destroying(self, within: Duration) -> DaemonClient {
        DaemonClient {
            destroying: within,
            ..self
        }
    }

    /// A client to the daemon, over a fresh channel (connected within
    /// `within`; each call is bounded by its own deadline, so a call past
    /// it is unreachable, never a cancelled status).
    async fn client(&self) -> Result<CtxloomClient<Channel>, Called> {
        Ok(CtxloomClient::new(self.channel().await?))
    }

    /// The coordinator's client, over a fresh channel.
    async fn coordinator(&self) -> Result<CommandHandlerCoordinatorServiceClient<Channel>, Called> {
        Ok(CommandHandlerCoordinatorServiceClient::new(
            self.channel().await?,
        ))
    }

    /// A fresh channel to the daemon, pinning its certificate.
    async fn channel(&self) -> Result<Channel, Called> {
        let unreachable = |detail: String| Called::Unreachable { detail };
        let dial = parse_endpoint(&self.endpoint).map_err(|e| unreachable(e.to_string()))?;
        let tls = ClientTlsConfig::new()
            .ca_certificate(Certificate::from_pem(self.cert.to_pem()))
            .domain_name(dial.host.clone());
        let channel = Endpoint::from_shared(dial.url())
            .map_err(|e| unreachable(chain(&e)))?
            .connect_timeout(self.within)
            .tls_config(tls)
            .map_err(|e| unreachable(chain(&e)))?
            .connect()
            .await
            .map_err(|e| unreachable(chain(&e)))?;
        Ok(channel)
    }

    /// `message`, a request of the `Ctxloom` service, its caller filled
    /// with this client's ([`DaemonClient::filling`]), with the user bearer.
    fn asked<T: daemon_proto::Carries>(&self, mut message: T) -> Result<Request<T>, Called> {
        if let Some(caller) = &self.caller {
            message.fill_caller_from(caller);
        }
        self.request(message)
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

/// The daemon's warnings `metadata` carries (OD-55), in order: each
/// [`daemon_proto::WARNING_METADATA`] value a prost-encoded `Notice`; one
/// that does not decode is passed over.
pub fn warnings_in(metadata: &MetadataMap) -> Vec<v1::Notice> {
    metadata
        .get_all_bin(daemon_proto::WARNING_METADATA)
        .iter()
        .filter_map(|value| value.to_bytes().ok())
        .filter_map(|bytes| v1::Notice::decode(bytes).ok())
        .collect()
}

/// The notices a library read's answer carries (step 10a U2,
/// [`daemon_proto::NOTICE_METADATA`]), in order; one that does not decode
/// is left out.
pub fn notices_in(metadata: &MetadataMap) -> Vec<v1::Notice> {
    metadata
        .get_all_bin(daemon_proto::NOTICE_METADATA)
        .iter()
        .filter_map(|value| value.to_bytes().ok())
        .filter_map(|bytes| v1::Notice::decode(bytes).ok())
        .collect()
}

/// `google.rpc.ErrorInfo`'s args (its `metadata`; its key, field 1, and
/// domain, field 2, are not read).
#[derive(Clone, PartialEq, Message)]
struct ErrorInfo {
    #[prost(string, tag = "1")]
    reason: String,
    #[prost(string, tag = "2")]
    domain: String,
    #[prost(map = "string, string", tag = "3")]
    metadata: std::collections::HashMap<String, String>,
}

/// The `ErrorInfo` domain of a failure the daemon says in a diagnostic of
/// its own (daemon-api-grpc's): its reason is the diagnostic's code.
const DIAGNOSED_DOMAIN: &str = run_ports::DIAGNOSED;

/// The diagnostic a failure's details say it in, if they do.
fn diagnosed(details: &[prost_types::Any]) -> Option<Called> {
    let info = details
        .iter()
        .filter(|any| any.type_url.ends_with("/google.rpc.ErrorInfo"))
        .filter_map(|any| ErrorInfo::decode(any.value.as_slice()).ok())
        .find(|info| info.domain == DIAGNOSED_DOMAIN && !info.reason.is_empty())?;
    let mut args: Vec<(String, String)> = info.metadata.into_iter().collect();
    args.sort();
    Some(Called::Diagnosed {
        code: info.reason,
        args,
    })
}

/// The typed refusal a coordinator's refused command names by its type
/// alone (its ErrorInfo's `refusal_type` arg: the status carries no
/// object, step 8 U1), as that type with no fields.
fn named_refusal(details: &[prost_types::Any]) -> Option<prost_types::Any> {
    let key = angzarr_embedded::angzarr_proto::v1::reason_args::REFUSAL_TYPE;
    details
        .iter()
        .filter(|any| any.type_url.ends_with("/google.rpc.ErrorInfo"))
        .filter_map(|any| ErrorInfo::decode(any.value.as_slice()).ok())
        .find_map(|info| info.metadata.get(key).cloned())
        .filter(|name| !name.is_empty())
        .map(|name| prost_types::Any {
            type_url: format!("type.googleapis.com/{name}"),
            value: Vec::new(),
        })
}

/// What `status`, answered to `method`, came to.
pub fn called(method: &str, status: &Status) -> Called {
    let details = RpcStatus::decode(status.details())
        .map(|rpc| rpc.details)
        .unwrap_or_default();
    // A typed refusal is the one detail that is no `google.rpc.ErrorInfo`
    // (angzarr's own statuses, UNAUTHENTICATED's among them, carry one);
    // the coordinator's refusal of a command names its type alone.
    let typed = details
        .iter()
        .find(|any| !any.type_url.ends_with("/google.rpc.ErrorInfo"))
        .cloned()
        .or_else(|| named_refusal(&details));
    if let Some(refusal) = typed {
        return Called::Refused {
            code: status.code() as i32,
            refusal,
        };
    }
    // A failure said in a diagnostic of its own is that diagnostic.
    if let Some(said) = diagnosed(&details) {
        return said;
    }
    // Any other status is read back through the infrastructure tree's one
    // root, which names it (by its number: no key is read).
    let code = status.code() as i32;
    match Infrastructure::read(code, &[]) {
        Some(Infrastructure::Unauthenticated) => Called::Unauthenticated,
        Some(Infrastructure::Unimplemented) => Called::Unimplemented {
            method: format!("{SERVICE}{method}"),
        },
        Some(Infrastructure::Unavailable | Infrastructure::TimedOut) => Called::Unreachable {
            detail: status.message().to_string(),
        },
        _ => Called::Failed {
            code,
            detail: status.message().to_string(),
        },
    }
}

/// What a call's `status` came to: one the transport raised (it has a
/// source: the connection dropped or reset mid-call, the daemon gone) is
/// unreachable, as a daemon not there is; one the daemon answered is
/// [`called`].
fn came_to(method: &str, status: &Status) -> Called {
    match std::error::Error::source(status) {
        Some(_) => Called::Unreachable {
            detail: chain(status),
        },
        None => called(method, status),
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

/// What is left until `until` by the system's clock; nothing once past.
fn remaining(until: daemon_ports::Until) -> Duration {
    let until = std::time::UNIX_EPOCH
        + Duration::new(
            u64::try_from(until.seconds).unwrap_or(0),
            u32::try_from(until.nanos).unwrap_or(0),
        );
    until
        .duration_since(std::time::SystemTime::now())
        .unwrap_or(Duration::ZERO)
}

/// One call: `$method` of the client with `$message`, its status read back,
/// within the client's deadline for a read (or `$within`).
macro_rules! call {
    ($self:ident, $method:ident, $name:expr, $message:expr) => {{
        call!($self, $method, $name, $message, $self.within)
    }};
    ($self:ident, $method:ident, $name:expr, $message:expr, $within:expr) => {{
        call!(@on $self, client, asked, $method, $name, $message, $within)
    }};
    // The coordinator's own messages carry no caller.
    ($self:ident, coordinator, $method:ident, $name:expr, $message:expr, $within:expr) => {{
        call!(@on $self, coordinator, request, $method, $name, $message, $within)
    }};
    (@on $self:ident, $client:ident, $made:ident, $method:ident, $name:expr, $message:expr, $within:expr) => {{
        let within: Duration = $within;
        let mut client = $self.$client().await?;
        let request = $self.$made($message)?;
        let answered = tokio::time::timeout(within, client.$method(request)).await;
        match answered {
            Ok(Ok(response)) => {
                $self.heard(response.metadata());
                Ok(response.into_inner())
            }
            Ok(Err(status)) => {
                $self.heard(status.metadata());
                Err(came_to($name, &status))
            }
            Err(_) => Err(Called::Unreachable {
                detail: format!("no answer within {within:?}"),
            }),
        }
    }};
}

impl DaemonApi for DaemonClient {
    async fn get_daemon(&self, request: v1::GetDaemonRequest) -> Result<v1::Daemon, Called> {
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

    async fn get_transcript(
        &self,
        request: v1::GetTranscriptRequest,
    ) -> Result<v1::Transcript, Called> {
        call!(self, get_transcript, "GetTranscript", request)
    }

    async fn assemble_context(
        &self,
        request: v1::AssembleContextRequest,
    ) -> Result<v1::AssembledContext, Called> {
        // An assembly runs companions in the daemon: a destroy's deadline.
        call!(
            self,
            assemble_context,
            "AssembleContext",
            request,
            self.destroying
        )
    }

    async fn get_trust(&self, request: v1::GetTrustRequest) -> Result<v1::Trust, Called> {
        // A preview reads the project's config in the daemon: a destroy's
        // deadline, as an assembly's.
        call!(self, get_trust, "GetTrust", request, self.destroying)
    }

    async fn list_trusts(
        &self,
        request: v1::ListTrustsRequest,
    ) -> Result<v1::ListTrustsResponse, Called> {
        call!(self, list_trusts, "ListTrusts", request)
    }

    async fn create_approval(
        &self,
        request: v1::CreateApprovalRequest,
    ) -> Result<v1::Approval, Called> {
        // An ask is minted, claimed and recorded before it is answered: a
        // write's deadline, as a run's.
        call!(
            self,
            create_approval,
            "CreateApproval",
            request,
            self.destroying
        )
    }

    async fn get_approval(&self, request: v1::GetApprovalRequest) -> Result<v1::Approval, Called> {
        call!(self, get_approval, "GetApproval", request)
    }

    async fn list_approvals(
        &self,
        request: v1::ListApprovalsRequest,
    ) -> Result<v1::ListApprovalsResponse, Called> {
        call!(self, list_approvals, "ListApprovals", request)
    }

    async fn wait_approval(
        &self,
        request: v1::WaitApprovalRequest,
        until: daemon_ports::Until,
    ) -> Result<v1::Approval, Called> {
        // The caller's own deadline (the ask's recorded expiry and a
        // margin), never a read's: an ask waits as long as the user takes
        // within its time, and a retry ends when the first call would have.
        call!(
            self,
            wait_approval,
            "WaitApproval",
            request,
            remaining(until)
        )
    }

    async fn create_run(&self, request: v1::CreateRunRequest) -> Result<v1::Run, Called> {
        // A run is planned (its dependencies pulled, when `sync.on_run`
        // asks), enqueued and placed before it is answered: a destroy's
        // deadline.
        call!(self, create_run, "CreateRun", request, self.destroying)
    }

    async fn get_run(&self, request: v1::GetRunRequest) -> Result<v1::Run, Called> {
        call!(self, get_run, "GetRun", request)
    }

    async fn watch_run(&self, request: v1::WatchRunRequest) -> Result<Box<dyn RunEvents>, Called> {
        // The stream is answered within the read's deadline; its events
        // come as the run goes, unbounded (a turn has its own deadline).
        let streaming = call!(self, watch_run, "WatchRun", request)?;
        Ok(Box::new(Watching(streaming)))
    }

    async fn redeliver_context(
        &self,
        request: v1::RedeliverContextRequest,
    ) -> Result<v1::RedeliveredContext, Called> {
        call!(self, redeliver_context, "RedeliverContext", request)
    }

    async fn deliver_hook_context(
        &self,
        request: v1::DeliverHookContextRequest,
    ) -> Result<v1::HookContext, Called> {
        call!(self, deliver_hook_context, "DeliverHookContext", request)
    }

    async fn acknowledge_hook_delivery(
        &self,
        request: v1::AcknowledgeHookDeliveryRequest,
    ) -> Result<v1::HookDeliveryAcknowledged, Called> {
        call!(
            self,
            acknowledge_hook_delivery,
            "AcknowledgeHookDelivery",
            request
        )
    }

    async fn watch_library(
        &self,
        request: v1::WatchLibraryRequest,
    ) -> Result<Box<dyn LibraryChanges>, Called> {
        // Answered within the read's deadline; its changes come as the
        // library's trees are changed, unbounded.
        let streaming = call!(self, watch_library, "WatchLibrary", request)?;
        Ok(Box::new(WatchingLibrary(streaming)))
    }

    async fn watch_session(
        &self,
        request: v1::WatchSessionRequest,
    ) -> Result<Box<dyn SessionSignals>, Called> {
        // The stream is answered within the read's deadline; its signals
        // come for as long as the watch is held, unbounded.
        let streaming = call!(self, watch_session, "WatchSession", request)?;
        Ok(Box::new(Signals(streaming)))
    }

    async fn watch_top(
        &self,
        request: v1::WatchTopRequest,
    ) -> Result<Box<dyn daemon_ports::TopEvents>, Called> {
        // Answered within the read's deadline; its events come for as long
        // as the watch is held, unbounded.
        let streaming = call!(self, watch_top, "WatchTop", request)?;
        Ok(Box::new(TopWatching(streaming)))
    }

    async fn handle(&self, book: CommandBook) -> Result<bool, Called> {
        let result: angzarr_proto_transport_distributed::v1::CommandResult =
            call!(self, coordinator, handle, "Handle", book, self.within)?;
        Ok(result.delivery != DeliveryOutcome::Duplicate as i32)
    }

    async fn create_hand_off(
        &self,
        request: v1::CreateHandOffRequest,
    ) -> Result<v1::HandOff, Called> {
        // A hand-off plans the child in the daemon, which may ask its
        // companions: a destroy's deadline.
        call!(
            self,
            create_hand_off,
            "CreateHandOff",
            request,
            self.destroying
        )
    }

    async fn delete_hand_off(
        &self,
        request: v1::DeleteHandOffRequest,
    ) -> Result<v1::HandOff, Called> {
        call!(self, delete_hand_off, "DeleteHandOff", request)
    }

    async fn handle_sync(
        &self,
        book: CommandBook,
        projectors: Vec<String>,
    ) -> Result<Synced, Called> {
        let request = HandleSyncRequest {
            command: Some(book),
            projectors,
        };
        let result: angzarr_proto_transport_distributed::v1::CommandSyncResult = call!(
            self,
            coordinator,
            handle_sync,
            "HandleSync",
            request,
            self.within
        )?;
        Ok(Synced {
            applied: result.delivery != DeliveryOutcome::Duplicate as i32,
            projections: result.projections,
        })
    }

    async fn delete_session(
        &self,
        request: v1::DeleteSessionRequest,
    ) -> Result<v1::DeleteSessionResponse, Called> {
        call!(
            self,
            delete_session,
            "DeleteSession",
            request,
            self.destroying
        )
    }

    async fn sweep_sessions(
        &self,
        request: v1::SweepSessionsRequest,
    ) -> Result<v1::SweepSessionsResponse, Called> {
        call!(
            self,
            sweep_sessions,
            "SweepSessions",
            request,
            self.destroying
        )
    }

    // The library's reads (step 10a U2, D12), from the one table. A read
    // observes its trees and may run companions in the daemon: an
    // assembly's deadline.
    daemon_proto::library_reads!(library_read_calls);

    // The library's writes (step 10b U3, D12), from their one table, under
    // the destroys' deadline: a write observes its trees, and its intent,
    // operation and record are the daemon's to finish.
    daemon_proto::library_writes!(library_write_calls);
}

/// [`DaemonClient`]'s library writes, from `daemon_proto::library_writes!`.
macro_rules! library_write_calls {
    ($($method:ident ($rpc:ident) $request:ident => $response:ident),* $(,)?) => {
        $(
            async fn $method(&self, request: v1::$request) -> Result<v1::$response, Called> {
                let within = self.destroying;
                let mut client = self.client().await?;
                let request = self.asked(request)?;
                match tokio::time::timeout(within, client.$method(request)).await {
                    Ok(Ok(response)) => {
                        self.heard(response.metadata());
                        Ok(response.into_inner())
                    }
                    Ok(Err(status)) => {
                        self.heard(status.metadata());
                        Err(came_to(stringify!($rpc), &status))
                    }
                    Err(_) => Err(Called::Unreachable {
                        detail: format!("no answer within {within:?}"),
                    }),
                }
            }
        )*
    };
}
use library_write_calls;

/// [`DaemonClient`]'s library reads, from `daemon_proto::library_reads!`.
macro_rules! library_read_calls {
    ($($method:ident ($rpc:ident) $request:ident . $target:ident => $response:ident),* $(,)?) => {
        $(
            async fn $method(
                &self,
                request: v1::$request,
            ) -> Result<daemon_ports::Read<v1::$response>, Called> {
                let within = self.destroying;
                let mut client = self.client().await?;
                let request = self.asked(request)?;
                match tokio::time::timeout(within, client.$method(request)).await {
                    Ok(Ok(response)) => {
                        self.heard(response.metadata());
                        let notices = notices_in(response.metadata());
                        Ok(daemon_ports::Read {
                            answer: response.into_inner(),
                            notices,
                        })
                    }
                    Ok(Err(status)) => {
                        self.heard(status.metadata());
                        Err(came_to(stringify!($rpc), &status))
                    }
                    Err(_) => Err(Called::Unreachable {
                        detail: format!("no answer within {within:?}"),
                    }),
                }
            }
        )*
    };
}
use library_read_calls;

/// A run's events as `WatchRun` streams them: a status that ends the
/// stream early (the daemon gone) is what it came to.
struct Watching(tonic::codec::Streaming<v1::RunEvent>);

impl RunEvents for Watching {
    fn next(&mut self) -> daemon_ports::NextEvent<'_> {
        Box::pin(async move {
            match self.0.message().await {
                Ok(Some(event)) => Some(Ok(event)),
                Ok(None) => None,
                Err(status) => Some(Err(came_to("WatchRun", &status))),
            }
        })
    }
}

/// A library's changes as `WatchLibrary` streams them: a status that ends
/// the stream early (the daemon gone) is what it came to.
struct WatchingLibrary(tonic::codec::Streaming<v1::LibraryChange>);

impl LibraryChanges for WatchingLibrary {
    fn next(&mut self) -> daemon_ports::NextChange<'_> {
        Box::pin(async move {
            match self.0.message().await {
                Ok(Some(change)) => Some(Ok(change)),
                Ok(None) => None,
                Err(status) => Some(Err(came_to("WatchLibrary", &status))),
            }
        })
    }
}

/// A watched session's signals as `WatchSession` streams them: a status
/// that ends the stream (the daemon gone) is what it came to.
struct Signals(tonic::codec::Streaming<v1::SessionSignal>);

impl SessionSignals for Signals {
    fn next(&mut self) -> daemon_ports::NextSignal<'_> {
        Box::pin(async move {
            match self.0.message().await {
                Ok(Some(signal)) => Some(Ok(signal)),
                Ok(None) => None,
                Err(status) => Some(Err(came_to("WatchSession", &status))),
            }
        })
    }
}

/// A `ctxloom top` watch's events as `WatchTop` streams them: a status that
/// ends the stream (the daemon gone) is what it came to.
struct TopWatching(tonic::codec::Streaming<v1::TopEvent>);

impl daemon_ports::TopEvents for TopWatching {
    fn next(&mut self) -> daemon_ports::NextTopEvent<'_> {
        Box::pin(async move {
            match self.0.message().await {
                Ok(Some(event)) => Some(Ok(event)),
                Ok(None) => None,
                Err(status) => Some(Err(came_to("WatchTop", &status))),
            }
        })
    }
}

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
