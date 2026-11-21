//! The port a hook process delivers a fact
//! book through. One method, no transport: the in-process `Edge` is one
//! implementation, a gRPC client of `CommandHandlerCoordinatorService.Handle`
//! (a `CommandBook` whose page body is a `Fact` carrying
//! `origin.external_id`) is another, and neither changes the caller.
//!
//! Invariant: delivery never panics and never blocks past `within`; every way
//! it can go wrong is a [`Delivery`] value.

use std::time::Duration;

use angzarr_embedded::v1::CommandBook;

/// What became of one delivered fact book.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Delivery {
    /// The handler recorded it.
    Applied,
    /// The handler had it already (same `origin.external_id`).
    Duplicate,
    /// The handler dead-lettered it: final, a retry would be parked again.
    Parked,
    /// Nobody answered (no listener, connection refused, no credential).
    Unavailable { detail: String },
    /// Nobody answered within the deadline.
    TimedOut,
    /// The admission gate or the handler refused it: final.
    Refused { detail: String },
    /// The daemon has no record of the session it is addressed to (its
    /// stream holds no session: never minted, or destroyed), so it was
    /// recorded nowhere: final (s4-Q14, H3 follow-up). Answered under the
    /// reason key [`NO_SUCH_SESSION`].
    NoSuchSession,
}

/// The reason key a capture fact addressed to a session the daemon has no
/// record of is answered with: the full name of the typed answer
/// `ctxloom.sessions.v1.FactSessionUnknown` (capture.proto).
pub const NO_SUCH_SESSION: &str = "ctxloom.sessions.v1.FactSessionUnknown";

impl Delivery {
    /// Whether nothing more can come of delivering this book again: the
    /// handler has it, or has finally refused or parked it. `Unavailable` and
    /// `TimedOut` are not settled (a later delivery may succeed).
    pub fn settled(&self) -> bool {
        !matches!(self, Delivery::Unavailable { .. } | Delivery::TimedOut)
    }
}

/// Where a hook process sends the facts it observed.
pub trait FactSink {
    /// Deliver `book` (a fact), waiting at most `within` for the answer.
    fn deliver(&self, book: CommandBook, within: Duration) -> Delivery;
}

#[cfg(test)]
#[path = "sink.test.rs"]
mod tests;
