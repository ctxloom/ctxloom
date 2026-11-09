//! PROPOSAL (design/hook-capture): the engine's hooks as facts of the
//! session.
//!
//! - [`observe`]: a unified [`HookEvent`] to the typed observations it holds,
//!   engine-neutral (no native payload shape reaches here);
//! - [`Observation::external_id`]: the fact's idempotency key, derived from
//!   the event alone (never a counter, never a clock);
//! - [`Capture`]: the hook-side policy. It delivers each observation through
//!   a [`FactSink`] and never fails: a delivery that did not happen is a
//!   [`Report`] entry, and the hook's answer to its engine is not this
//!   module's to change.
//!
//! Every observation is a RUNNER-authored fact on the SESSION stream (the
//! hook is launched by the runner and holds its credential).

use std::time::Duration;

use angzarr_embedded::v1::{CommandBook, Uuid};
use run_ports::{Delivery, FactSink};
use surface_ports::HookEvent;

/// One thing a hook observed, as the fact that records it.
#[derive(Clone, Debug, PartialEq)]
pub enum Observation {
    PromptSubmitted(sessions::RecordPromptSubmitted),
    AssistantText(sessions::RecordAssistantText),
    ToolCall(sessions::RecordToolCall),
    TurnEnded(sessions::RecordTurnEnded),
    SessionEnded(sessions::RecordSessionEnded),
    Compaction(sessions::RecordCompaction),
}

/// The observations `event` holds, in the order they happened. Pure.
///
/// Invariants: every MessageDisplay piece is one observation (none is
/// dropped or merged); a PostToolBatch is one observation per call, in call
/// order, carrying the call's input whole and its result as size and digest
/// only; a turn's end is one observation without text; only PostToolBatch
/// reports tool calls (a single PostToolUse reports none, so a call is never
/// recorded twice); an event of no observable kind holds none.
pub fn observe(event: &HookEvent) -> Vec<Observation> {
    let _ = event;
    todo!("design/hook-capture")
}

impl Observation {
    /// The fact's `origin.external_id` for `session`: deterministic from the
    /// event's own identifiers, and distinct across kinds.
    pub fn external_id(&self, session: &Uuid) -> String {
        let _ = session;
        todo!("design/hook-capture")
    }

    /// The type URL of the fact's wire message.
    pub fn type_url(&self) -> String {
        todo!("design/hook-capture")
    }

    /// The fact as the `CommandBook` the edge (or a gRPC `Handle`) takes: on
    /// `session`'s stream, a Fact body with its origin.
    pub fn book(&self, session: &Uuid) -> CommandBook {
        let _ = session;
        todo!("design/hook-capture")
    }
}

/// How long a hook may spend delivering, by the engine's native hook name:
/// strictly under the engine's own timeout for it, so the engine never kills
/// the hook mid-answer (UserPromptSubmit 30s, MessageDisplay 10s).
pub fn delivery_budget(native: &str) -> Duration {
    let _ = native;
    todo!("design/hook-capture")
}

/// One delivery tried (or skipped). A delivery that did not happen is dropped,
/// never kept for later: the report is its only trace.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Attempt {
    pub external_id: String,
    /// `None`: not attempted, because an earlier one found the sink
    /// unavailable or out of time (the budget is shared, not multiplied).
    pub delivery: Option<Delivery>,
}

/// What a hook's capture did.
#[derive(Clone, Debug, PartialEq, Eq, Default)]
pub struct Report {
    pub attempts: Vec<Attempt>,
}

impl Report {
    /// The attempts that left a fact unrecorded and unsettled.
    pub fn unrecorded(&self) -> Vec<&Attempt> {
        todo!("design/hook-capture")
    }
}

/// The hook-side policy over a sink.
pub struct Capture<S: FactSink> {
    sink: S,
}

impl<S: FactSink> Capture<S> {
    pub fn new(sink: S) -> Capture<S> {
        Capture { sink }
    }

    /// Deliver `event`'s observations to `session`'s stream, within `budget`
    /// in all. Infallible by type: no panic, no error, no wait past the
    /// budget.
    pub fn record(&self, session: &Uuid, event: &HookEvent, budget: Duration) -> Report {
        let _ = (&self.sink, session, event, budget);
        todo!("design/hook-capture")
    }
}

#[cfg(test)]
#[path = "capture.test.rs"]
mod tests;
