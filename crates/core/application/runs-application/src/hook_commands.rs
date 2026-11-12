//! The command half the hook listener serves: core's [`Commands`] and
//! [`Served`] over the [`Edge`] port (owner ruling 2026-11-09, item 17). The
//! coordinator's gate has already admitted the book (identity, the six fact
//! types, the caller's own session); what is left is to hand it to the kernel
//! through the edge's exposure seam, as a request from this machine, and to
//! answer what the kernel answers: applied or duplicate, or a failure as its
//! gRPC code and key.
//!
//! `handle` and `handle_sync` are served (the trait's other operations
//! answer UNIMPLEMENTED by its defaults), and only the session domain is
//! reachable. `handle_sync` (angzarr's SIMPLE mode, T16) is the kernel's:
//! answered once the projectors it names (the read models registered in the
//! kernel, S32) have projected its events, with what each answered.

use std::sync::Arc;

use angzarr_coordinator::angzarr_coordinator_base::{Answered, Code, Coded, Refusal};
use angzarr_coordinator::angzarr_ports::BoxFuture;
use angzarr_coordinator::{Caller, Commands, Reach, Served, Synced};
use angzarr_embedded::angzarr_router::error::CodedError;
use angzarr_embedded::v1::CommandBook;
use angzarr_embedded::{Failure, Recovery, Rejected};
use run_ports::{Arrival, Edge, Unserved};

/// The kernel behind the edge, as the coordinator's command half.
pub struct EdgeCommands<E: Edge> {
    edge: Arc<E>,
}

impl<E: Edge> EdgeCommands<E> {
    pub fn new(edge: Arc<E>) -> EdgeCommands<E> {
        EdgeCommands { edge }
    }
}

/// Reach is the session stream's domain, on this machine: the hook sends its
/// facts there and nowhere else.
impl<E: Edge> Served for EdgeCommands<E> {
    fn reach(&self, domain: &str) -> Reach {
        match domain == sessions::SESSION_DOMAIN {
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
/// INTERNAL under its own code.
fn coded_of(failure: &Failure<Rejected>) -> Coded {
    let from_router = |coded: CodedError| {
        Coded::new(
            Code::from_number(coded.grpc as i32).unwrap_or(Code::Internal),
            coded.code,
            coded.extras,
        )
    };
    match failure {
        Failure::Rejected(rejected) => from_router(rejected.as_coded()),
        Failure::Kernel(kernel) => match kernel.as_coded() {
            Some(coded) => from_router(coded),
            None => {
                let code = match kernel.recovery() {
                    Recovery::Invalid => Code::InvalidArgument,
                    _ => Code::Internal,
                };
                Coded::from_diagnostic(code, failure)
            }
        },
        other => Coded::from_diagnostic(Code::Internal, other),
    }
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
            match self.edge.serve(book, Arrival::Loopback).await {
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
