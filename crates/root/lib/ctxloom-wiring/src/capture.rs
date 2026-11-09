//! Hook capture's composition: what a hook's observations are delivered
//! through, and how a hook finds the session its harp names.
//!
//! - [`EdgeSink`]: the in-process [`FactSink`], over any [`Edge`] (the
//!   `EmbeddedEdge` of [`crate::InProcessCore`]): a fact book is served at the
//!   edge and the answer is the [`Delivery`]. It is not a hook's transport: a
//!   hook is a separate process, and appending to the kernel's stream from a
//!   second process is the listener's to arbitrate (see [`Unreachable`]).
//! - [`Unreachable`]: the sink a hook process has until the listener exists.
//!   Every delivery is `Unavailable`, so a hook fails open: the engine is
//!   answered as usual and each observation is said, on stderr, to be not
//!   recorded.
//! - [`HookSink`]: what a hook process delivers through: [`GrpcSink`] to the
//!   run's listener when `CTXLOOM_ENDPOINT` and `CTXLOOM_BEARER` are set and
//!   well formed, else [`Unreachable`].
//! - [`SessionIds`] / [`DirIds`]: a harp to its session's id. A session's id
//!   is minted at random (it is not derived from the harp), and its directory
//!   records it in `metadata.json`, so that is where a hook reads it.
//! - [`HookRecording`]: [`hook_cli::Recording`] over a sink and the ids.

use std::cell::RefCell;
use std::sync::Arc;
use std::time::Duration;

use angzarr_diagnostic::Diagnostic;
use angzarr_embedded::v1::{DeliveryOutcome, Uuid};
use angzarr_embedded::{Failure, KernelError};
use hook_cli::{NotRecorded, Recording};
use hook_sink_grpc::GrpcSink;
use run_launcher::METADATA;
use run_ports::{
    Arrival, Bearer, CertError, Delivery, Edge, FactSink, PublicCert, SessionRecords, Unserved,
};
use runs_application::{delivery_budget, Capture};
use setup_application::HookAnswer;
use surface_ports::Json;

/// A sink over an in-process edge.
pub struct EdgeSink<E: Edge + 'static> {
    edge: Arc<E>,
}

impl<E: Edge + 'static> EdgeSink<E> {
    /// A sink that serves the books it is handed at `edge`.
    pub fn new(edge: Arc<E>) -> EdgeSink<E> {
        EdgeSink { edge }
    }
}

impl<E: Edge + 'static> FactSink for EdgeSink<E> {
    fn deliver(&self, book: angzarr_embedded::v1::CommandBook, within: Duration) -> Delivery {
        // A runtime of its own, on a thread of its own: this may be called
        // from inside another runtime's thread, which cannot block on one.
        let edge = &self.edge;
        std::thread::scope(|scope| {
            let served = scope.spawn(move || {
                let runtime = tokio::runtime::Builder::new_current_thread()
                    .enable_time()
                    .build()
                    .map_err(|e| Delivery::Unavailable {
                        detail: e.to_string(),
                    })?;
                runtime.block_on(async {
                    tokio::time::timeout(within, edge.serve(book, Arrival::Loopback))
                        .await
                        .map_err(|_| Delivery::TimedOut)
                })
            });
            match served.join() {
                Ok(Ok(served)) => delivery_of(served),
                Ok(Err(delivery)) => delivery,
                Err(_) => Delivery::Unavailable {
                    detail: "the delivery panicked".to_string(),
                },
            }
        })
    }
}

/// What the edge's answer to a fact means to its sender.
fn delivery_of(served: Result<angzarr_embedded::Answered, Unserved>) -> Delivery {
    match served {
        Ok(answered) => match answered.delivery {
            DeliveryOutcome::Applied => Delivery::Applied,
            DeliveryOutcome::Duplicate => Delivery::Duplicate,
            DeliveryOutcome::Unspecified => Delivery::Refused {
                detail: "the kernel gave no delivery outcome".to_string(),
            },
        },
        Err(Unserved::Hidden { domain }) => Delivery::Refused {
            detail: format!("the domain {domain} is not served"),
        },
        Err(Unserved::Failed(Failure::Kernel(KernelError::FactDeadLettered { .. }))) => {
            Delivery::Parked
        }
        Err(Unserved::Failed(Failure::Rejected(rejected))) => Delivery::Refused {
            detail: format!("{rejected:?}"),
        },
        Err(Unserved::Failed(other)) => Delivery::Unavailable {
            detail: format!("{other:?}"),
        },
    }
}

/// The sink of a hook process while no listener exists to deliver through.
pub struct Unreachable;

impl FactSink for Unreachable {
    fn deliver(&self, _: angzarr_embedded::v1::CommandBook, _: Duration) -> Delivery {
        Delivery::Unavailable {
            detail: "the listener is not running".to_string(),
        }
    }
}

/// The sink of a hook process: the run's listener when the environment names
/// one, else [`Unreachable`].
pub enum HookSink {
    Listener(GrpcSink),
    /// A listener is named but its certificate is not usable: refused, and
    /// nothing is sent (never plaintext, never the system's roots).
    Refused(CertError),
    Unreachable(Unreachable),
}

impl HookSink {
    /// The sink the hook environment makes: the endpoint (a URL, `https://<host>:<port>`), the certificate (`CTXLOOM_TLS_CERT`)
    /// and the bearer (`<run>.<token>`). A missing, empty or malformed one
    /// gives [`Unreachable`]: the hook fails open, as it did before there
    /// was a listener.
    pub fn from_environment(
        endpoint: Option<&str>,
        bearer: Option<&str>,
        cert: Option<&str>,
    ) -> HookSink {
        let named = endpoint.filter(|e| !e.is_empty()).and_then(|endpoint| {
            let (run, token) = Bearer::parse(bearer?).ok()?;
            Some((endpoint, Bearer::compose(&run, &token)))
        });
        match named {
            None => HookSink::Unreachable(Unreachable),
            Some((endpoint, bearer)) => match PublicCert::from_environment(cert) {
                Ok(cert) => HookSink::Listener(GrpcSink::new(endpoint, bearer, cert)),
                Err(error) => HookSink::Refused(error),
            },
        }
    }
}

/// Why nothing was delivered for want of a usable certificate, as the hook
/// says it: the code, what is wrong, and that nothing was sent.
fn refusal_text(error: &CertError) -> String {
    let wrong = match error {
        CertError::Missing => "CTXLOOM_TLS_CERT is not set",
        CertError::Malformed { .. } => "CTXLOOM_TLS_CERT is not a certificate",
    };
    format!(
        "{}: {wrong}; nothing was sent (the hook never falls back to plaintext)",
        error.code()
    )
}

impl FactSink for HookSink {
    fn deliver(&self, book: angzarr_embedded::v1::CommandBook, within: Duration) -> Delivery {
        match self {
            HookSink::Listener(sink) => sink.deliver(book, within),
            HookSink::Refused(error) => Delivery::Unavailable {
                detail: refusal_text(error),
            },
            HookSink::Unreachable(sink) => sink.deliver(book, within),
        }
    }
}

/// A harp's session.
pub trait SessionIds {
    /// The id of the session named `harp`, if its directory says one.
    fn session_of(&self, harp: &str) -> Option<Uuid>;
}

/// Ids read from the session directories' `metadata.json`; none where there
/// are no directories to read (no home).
pub struct DirIds<R: SessionRecords>(RefCell<Option<R>>);

impl<R: SessionRecords> DirIds<R> {
    /// Ids read through `records`, if there are any.
    pub fn new(records: Option<R>) -> DirIds<R> {
        DirIds(RefCell::new(records))
    }
}

impl<R: SessionRecords> SessionIds for DirIds<R> {
    fn session_of(&self, harp: &str) -> Option<Uuid> {
        let held = self.0.borrow_mut().as_mut()?.read(harp, METADATA).ok()??;
        let metadata = Json::parse(std::str::from_utf8(&held).ok()?).ok()?;
        let id = uuid::Uuid::parse_str(metadata.get("id")?.as_str()?).ok()?;
        Some(Uuid::from_bytes(*id.as_bytes()))
    }
}

/// A hook's recording: its observations, delivered through a sink to the
/// session its harp names.
pub struct HookRecording<S: FactSink, I: SessionIds> {
    capture: Capture<S>,
    ids: I,
}

impl<S: FactSink, I: SessionIds> HookRecording<S, I> {
    pub fn new(sink: S, ids: I) -> HookRecording<S, I> {
        HookRecording {
            capture: Capture::new(sink),
            ids,
        }
    }
}

impl<S: FactSink, I: SessionIds> Recording for HookRecording<S, I> {
    fn record(&self, native: &str, harp: &str, answer: &HookAnswer) -> Vec<NotRecorded> {
        let Some(event) = &answer.event else {
            return Vec::new();
        };
        let observed = runs_application::observe(event);
        if observed.is_empty() {
            return Vec::new();
        }
        let Some(session) = self.ids.session_of(harp) else {
            return observed
                .iter()
                .map(|observation| NotRecorded {
                    what: observation.type_url(),
                    why: format!("no session directory for {harp}"),
                })
                .collect();
        };
        self.capture
            .record(&session, event, delivery_budget(native))
            .unrecorded()
            .into_iter()
            .map(|attempt| NotRecorded {
                what: attempt.external_id.clone(),
                why: match &attempt.delivery {
                    Some(Delivery::Unavailable { detail }) => detail.clone(),
                    Some(Delivery::TimedOut) => "the delivery timed out".to_string(),
                    Some(other) => format!("{other:?}"),
                    None => "not tried: an earlier delivery found no one answering".to_string(),
                },
            })
            .collect()
    }
}

#[cfg(test)]
#[path = "capture.test.rs"]
mod tests;
