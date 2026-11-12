//! The [`FactSink`] a hook process delivers through: a gRPC client of the run
//! process's `CommandHandlerCoordinatorService.Handle` over TCP
//! (`CTXLOOM_ENDPOINT`, a URL), the bearer (`CTXLOOM_BEARER`) in the `authorization`
//! metadata (owner rulings 2026-11-08 item 9, 2026-11-09 items 16 and 17).
//!
//! The endpoint is `https://` only (rulings 35-37): TLS authenticates the
//! SERVER, the run's self-signed certificate (`CTXLOOM_TLS_CERT`, public) is
//! the channel's sole root, and the bearer authenticates the child. A
//! plaintext endpoint is never dialed and nothing is sent (`Unavailable`, so
//! the hook says it did not record, never a silent refusal); a listener that does not
//! speak TLS, or whose certificate is not the pinned one, expired, or does not
//! name the host dialed, is unavailable, never retried in the clear.
//!
//! `deliver` is synchronous, so the sink owns a small current-thread runtime
//! (on a thread of its own, so it may be called from inside another runtime)
//! and blocks on the call. The deadline `within` bounds the WHOLE call, the
//! connect included: a dead or stuck listener costs a hook at most its budget
//! and the hook fails open.
//!
//! How the outcome maps to a [`Delivery`]:
//!
//! | outcome                                                | delivery      |
//! |--------------------------------------------------------|---------------|
//! | `CommandResult` APPLIED                                | `Applied`     |
//! | `CommandResult` DUPLICATE (an answer, not an error)    | `Duplicate`   |
//! | `CommandResult` with no outcome                        | `Refused`     |
//! | INTERNAL `ANGZARR_REASON_FACT_DEAD_LETTERED`           | `Parked`      |
//! | DEADLINE_EXCEEDED, or the deadline passing             | `TimedOut`    |
//! | connect or transport failure, UNAVAILABLE, ABORTED, RESOURCE_EXHAUSTED, UNKNOWN, INTERNAL, DATA_LOSS | `Unavailable` |
//! | a plaintext (`http://`) endpoint, never dialed           | `Unavailable` |
//! | any other status (unauthenticated, permission denied, not found, ...) | `Refused` |
//!
//! The bearer is a secret: the sink's `Debug` withholds it, and every detail
//! it produces has the bearer's text scrubbed out.
//!
//! [`serving`] is the liveness of a listener (T16): the standard
//! `grpc.health.v1.Health` Check, over the same pinned certificate, presenting
//! no bearer (angzarr's server answers it outside its gate). A listener
//! whose certificate is not the pinned one never hears a call, so a stale
//! port another process took is never handed anything.

// RED: used by `serving` once it is built.
#[allow(dead_code)]
mod health;

use std::fmt;
use std::time::Duration;

use angzarr_embedded::v1::{CommandBook, DeliveryOutcome};
use angzarr_proto_transport_distributed::v1::command_handler_coordinator_service_client::CommandHandlerCoordinatorServiceClient;
use angzarr_proto_transport_distributed::v1::CommandResult;
use prost::Message;
use run_ports::{parse_endpoint, Bearer, Delivery, FactSink, PublicCert};
use tonic::metadata::MetadataValue;
use tonic::transport::{Certificate, ClientTlsConfig, Endpoint};
use tonic::{Code, Request, Status};

/// The reason key of a fact the kernel dead-lettered.
const DEAD_LETTERED: &str = "ANGZARR_REASON_FACT_DEAD_LETTERED";

/// `google.rpc.Status`, the message a status's details travel in.
#[derive(Clone, PartialEq, Message)]
struct RpcStatus {
    #[prost(int32, tag = "1")]
    code: i32,
    #[prost(string, tag = "2")]
    message: String,
    #[prost(message, repeated, tag = "3")]
    details: Vec<prost_types::Any>,
}

/// `google.rpc.ErrorInfo`: the reason is the key a refusal is answered under.
#[derive(Clone, PartialEq, Message)]
struct ErrorInfo {
    #[prost(string, tag = "1")]
    reason: String,
}

/// Why a call did not answer.
enum Failed {
    /// No connection, or a broken one: said as text.
    Transport(String),
    /// The listener answered with a status.
    Status(Status),
}

/// The sink: where the run's listener is, and the credential it is given.
pub struct GrpcSink {
    endpoint: String,
    bearer: Bearer,
    cert: PublicCert,
}

impl GrpcSink {
    /// A sink delivering to the listener at the URL `endpoint`
    /// (`https://<host>:<port>`), authenticated by `bearer`, pinning `cert`.
    pub fn new(endpoint: impl Into<String>, bearer: Bearer, cert: PublicCert) -> GrpcSink {
        GrpcSink {
            endpoint: endpoint.into(),
            bearer,
            cert,
        }
    }

    async fn call(&self, book: CommandBook) -> Result<CommandResult, Failed> {
        let dial = parse_endpoint(&self.endpoint)
            .map_err(|e| Failed::Transport(format!("the endpoint is not usable: {e}")))?;
        let channel = Endpoint::from_shared(dial.url())
            .map_err(|e| Failed::Transport(format!("the endpoint is not usable: {e}")))?
            .tls_config(tls_for(&dial.host, &self.cert))
            .map_err(|e| {
                Failed::Transport(format!("the certificate is not usable: {}", chain(&e)))
            })?
            .connect()
            .await
            .map_err(|e| Failed::Transport(format!("cannot reach the listener: {}", chain(&e))))?;
        let mut request = Request::new(book);
        let mut value = MetadataValue::try_from(format!("Bearer {}", self.bearer.expose()))
            .map_err(|_| Failed::Transport("the bearer is not valid header text".to_string()))?;
        value.set_sensitive(true);
        request.metadata_mut().insert("authorization", value);
        CommandHandlerCoordinatorServiceClient::new(channel)
            .handle(request)
            .await
            .map(tonic::Response::into_inner)
            .map_err(Failed::Status)
    }

    fn scrub(&self, text: String) -> String {
        match self.bearer.expose() {
            "" => text,
            secret => text.replace(secret, "<redacted>"),
        }
    }

    fn block_on(&self, book: CommandBook, within: Duration) -> Delivery {
        let runtime = match tokio::runtime::Builder::new_current_thread()
            .enable_time()
            .enable_io()
            .build()
        {
            Ok(runtime) => runtime,
            Err(e) => {
                return Delivery::Unavailable {
                    detail: self.scrub(format!("no runtime to deliver on: {e}")),
                }
            }
        };
        runtime.block_on(async {
            match tokio::time::timeout(within, self.call(book)).await {
                Err(_) => Delivery::TimedOut,
                Ok(Ok(result)) => delivery_of_result(&result),
                Ok(Err(Failed::Status(status))) => self.delivery_of_status(&status),
                Ok(Err(Failed::Transport(text))) => Delivery::Unavailable {
                    detail: self.scrub(text),
                },
            }
        })
    }

    fn delivery_of_status(&self, status: &Status) -> Delivery {
        let reason = reason_of(status);
        match delivery_of_code(status.code(), status.message(), &reason) {
            Delivery::Unavailable { detail } => Delivery::Unavailable {
                detail: self.scrub(detail),
            },
            Delivery::Refused { detail } => Delivery::Refused {
                detail: self.scrub(detail),
            },
            other => other,
        }
    }
}

impl fmt::Debug for GrpcSink {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("GrpcSink")
            .field("endpoint", &self.endpoint)
            .field("bearer", &self.bearer)
            .field("cert", &self.cert)
            .finish()
    }
}

impl FactSink for GrpcSink {
    fn deliver(&self, book: CommandBook, within: Duration) -> Delivery {
        // A thread of its own: this may be called from inside another
        // runtime's thread, which cannot block on one.
        std::thread::scope(
            |scope| match scope.spawn(move || self.block_on(book, within)).join() {
                Ok(delivery) => delivery,
                Err(_) => Delivery::Unavailable {
                    detail: "the delivery panicked".to_string(),
                },
            },
        )
    }
}

/// The channel's TLS: `cert` is the ONLY root (no system roots, no webpki
/// roots), and the server's name is the URL's host (an IP literal or a runtime's
/// host alias), which the certificate must name.
fn tls_for(host: &str, cert: &PublicCert) -> ClientTlsConfig {
    ClientTlsConfig::new()
        .ca_certificate(Certificate::from_pem(cert.to_pem()))
        .domain_name(host)
}

/// What a `CommandResult` means to its sender.
fn delivery_of_result(result: &CommandResult) -> Delivery {
    match DeliveryOutcome::try_from(result.delivery) {
        Ok(DeliveryOutcome::Applied) => Delivery::Applied,
        Ok(DeliveryOutcome::Duplicate) => Delivery::Duplicate,
        _ => Delivery::Refused {
            detail: "the listener gave no delivery outcome".to_string(),
        },
    }
}

/// What a failed call's status means to its sender.
fn delivery_of_code(code: Code, message: &str, reason: &str) -> Delivery {
    let detail = format!("{code:?}: {message}");
    match code {
        Code::DeadlineExceeded => Delivery::TimedOut,
        Code::Internal if message == DEAD_LETTERED || reason == DEAD_LETTERED => Delivery::Parked,
        Code::Unavailable
        | Code::Aborted
        | Code::ResourceExhausted
        | Code::Unknown
        | Code::Internal
        | Code::DataLoss => Delivery::Unavailable { detail },
        _ => Delivery::Refused { detail },
    }
}

/// The reason key a status carries in its `google.rpc.ErrorInfo` detail.
fn reason_of(status: &Status) -> String {
    RpcStatus::decode(status.details())
        .ok()
        .and_then(|rpc| {
            rpc.details
                .iter()
                .find_map(|any| ErrorInfo::decode(any.value.as_slice()).ok())
        })
        .map(|info| info.reason)
        .unwrap_or_default()
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

/// Whether the listener at the URL `endpoint` answers the standard health
/// check SERVING within `within`, over TLS pinning `cert` as its sole root.
/// No bearer is presented. Anything else (no connection, another
/// certificate, no health service, NOT_SERVING, the deadline passing) is
/// not serving.
pub fn serving(endpoint: &str, cert: &PublicCert, within: Duration) -> bool {
    let _ = (endpoint, cert, within);
    false
}

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
