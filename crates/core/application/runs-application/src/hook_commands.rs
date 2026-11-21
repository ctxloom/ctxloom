//! The command half the hook listener serves: core's [`Commands`] and
//! [`Served`] over the [`Edge`] port (owner ruling 2026-11-09, item 17). The
//! coordinator's gate has already admitted the book (the user, what she may
//! send, the streams it reaches); what is left is to hand it to the kernel
//! through the edge's exposure seam, as a request from this machine, and to
//! answer what the kernel answers: applied or duplicate, or a failure as its
//! gRPC code and key.
//!
//! `handle` and `handle_sync` are served (the trait's other operations
//! answer UNIMPLEMENTED by its defaults), and only the session, run,
//! trust and approvals domains are reachable. `handle_sync` (angzarr's SIMPLE mode, T16) is the kernel's:
//! answered once the projectors it names (the read models registered in the
//! kernel, S32) have projected its events, with what each answered.

use std::sync::Arc;

use angzarr_coordinator::angzarr_coordinator_base::{Answered, Code, Coded, Refusal};
use angzarr_coordinator::angzarr_ports::BoxFuture;
use angzarr_coordinator::{Caller, Commands, Reach, Served, Synced};
use angzarr_embedded::angzarr_router::error::CodedError;
use angzarr_embedded::v1::CommandBook;
use angzarr_embedded::{Failure, Recovery, Rejected};
use run_ports::{Arrival, Edge, Infrastructure, Unserved};

/// The kernel behind the edge, as the coordinator's command half.
pub struct EdgeCommands<E: Edge> {
    edge: Arc<E>,
}

impl<E: Edge> EdgeCommands<E> {
    pub fn new(edge: Arc<E>) -> EdgeCommands<E> {
        EdgeCommands { edge }
    }
}

/// Reach is the session stream's domain, the run stream's and a project's
/// trust's, on this machine: a hook sends its facts to its session, a hosted
/// one-shot's command its stop to its run (U4), the user her trust in a
/// project (step 8 U1), and her answer to an ask, or an engine's withdrawal
/// of one (step 8 U4), and a library tree's operations and observations
/// (step 10a U0); what each caller may send there is its gate's
/// allowlist's to say.
impl<E: Edge> Served for EdgeCommands<E> {
    fn reach(&self, domain: &str) -> Reach {
        let reached = [
            sessions::SESSION_DOMAIN,
            runs::RUN_DOMAIN,
            trust::TRUST_DOMAIN,
            approvals::APPROVALS_DOMAIN,
            library_tree::LIBRARY_DOMAIN,
        ];
        match reached.contains(&domain) {
            true => Reach::Loopback,
            false => Reach::None,
        }
    }
}

/// How a command failed, as the coordinator answers it: the kernel's own
/// failure, or one of the edge's (a `Coded`).
#[derive(Clone, Debug, PartialEq)]
pub enum EdgeFailure {
    Kernel(Failure<Rejected>),
    Coded(Coded),
}

impl From<Coded> for EdgeFailure {
    fn from(coded: Coded) -> EdgeFailure {
        EdgeFailure::Coded(coded)
    }
}

impl Refusal for EdgeFailure {
    fn coded(&self) -> Coded {
        match self {
            EdgeFailure::Coded(coded) => coded.clone(),
            EdgeFailure::Kernel(failure) => coded_of(failure),
        }
    }
}

/// A kernel failure as its key, args and status: a refusal under its own
/// (the status the domain's refusal declared); a failure the kernel codes under a
/// spec key as that key; a malformed book an invalid-argument status; anything else
/// INTERNAL under its own code. Every status this side names is an
/// infrastructure error's ([`Infrastructure`], the tree's one root).
fn coded_of(failure: &Failure<Rejected>) -> Coded {
    let from_router = |coded: CodedError| {
        Coded::new(
            Code::from_number(coded.grpc as i32).unwrap_or(Infrastructure::Failed.code()),
            coded.code,
            coded.extras,
        )
    };
    match failure {
        Failure::Rejected(rejected) => from_router(rejected.as_coded()),
        Failure::Kernel(kernel) => match kernel.as_coded() {
            Some(coded) => from_router(coded),
            None => {
                let error = match kernel.recovery() {
                    Recovery::Invalid => Infrastructure::Malformed,
                    _ => Infrastructure::Failed,
                };
                Coded::from_diagnostic(error.code(), failure)
            }
        },
        other => Coded::from_diagnostic(Infrastructure::Failed.code(), other),
    }
}

/// The answer to a capture fact addressed to a session the daemon has no
/// record of: capture.proto's `FactSessionUnknown`, its full name the key
/// ([`run_ports::NO_SUCH_SESSION`]), the session's root its arg, under
/// the status [`Infrastructure::SessionUnknown`] names (a fact is never
/// refused, so no `(refusal_status)` declares it: it is an infrastructure
/// error, the listener's own answer, as the kernel's spec keys are).
fn no_such_session_answer(book: &CommandBook) -> Coded {
    let session = book
        .cover
        .as_ref()
        .and_then(|cover| cover.root.as_ref())
        .and_then(|root| uuid::Uuid::from_slice(&root.value).ok())
        .map(|root| root.to_string())
        .unwrap_or_default();
    Coded::new(
        Infrastructure::SessionUnknown.code(),
        run_ports::NO_SUCH_SESSION,
        [("session".to_string(), session)],
    )
}

impl<E: Edge> Commands for EdgeCommands<E> {
    type Failure = EdgeFailure;

    /// The domain the kernel does not serve, as the kernel answers it, so a
    /// hidden domain and a missing one are one answer.
    fn unknown_domain(&self, domain: &str) -> EdgeFailure {
        EdgeFailure::Kernel(
            Unserved::Hidden {
                domain: domain.to_string(),
            }
            .answer(),
        )
    }

    fn handle<'a>(
        &'a self,
        book: CommandBook,
        caller: Caller,
    ) -> BoxFuture<'a, Result<Answered, EdgeFailure>> {
        // A hook is a client; the framework caller has no business here.
        let _ = caller;
        Box::pin(async move {
            let asked = book.clone();
            match self.edge.serve(book, Arrival::Loopback).await {
                // A capture fact recorded nowhere: the session has no record
                // (s4-Q14, H3 follow-up), a distinct answer, never "applied".
                Ok(answered) if crate::no_such_session(&asked, &answered) => {
                    Err(EdgeFailure::Coded(no_such_session_answer(&asked)))
                }
                Ok(answered) => Ok(Answered {
                    events: answered.events,
                    delivery: answered.delivery,
                }),
                Err(unserved) => Err(EdgeFailure::Kernel(unserved.answer())),
            }
        })
    }

    fn handle_sync<'a>(
        &'a self,
        book: CommandBook,
        projectors: Vec<String>,
        caller: Caller,
    ) -> BoxFuture<'a, Result<Synced, EdgeFailure>> {
        let _ = caller;
        Box::pin(async move {
            match self
                .edge
                .serve_sync(book, projectors, Arrival::Loopback)
                .await
            {
                Ok(synced) => Ok(Synced {
                    answered: Answered {
                        events: synced.answered.events,
                        delivery: synced.answered.delivery,
                    },
                    projections: synced.projections,
                }),
                Err(unserved) => Err(EdgeFailure::Kernel(unserved.answer())),
            }
        })
    }
}

#[cfg(test)]
#[path = "hook_commands.test.rs"]
mod tests;
