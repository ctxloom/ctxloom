//! The hook records what it observed, and
//! that never changes what it answers.
//!
//! [`execute_recording`] is [`crate::execute`] plus a [`Recording`] the
//! application supplies (the composition root's: decode-free, it is handed
//! the answer, whose `event` the codec decoded). Fail-open is by type:
//! `Recording::record` cannot fail, only report what it did not record
//! ([`Unrecorded`]: dropped, or HELD for later, s4-Q14), and each such report
//! is a stderr warning ([`crate::HookWarning::NotRecorded`],
//! [`crate::HookWarning::Held`], [`crate::HookWarning::HeldBehind`]), never
//! stdout and never the exit code.

use angzarr_diagnostic::Diagnostic;
use setup_application::HookAnswer;

use crate::{HookService, HookWarning, Outcome};

/// One observation that was not recorded.
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct NotRecorded {
    /// The fact's external id.
    pub what: String,
    /// The delivery's text (unavailable, timed out, refused ...).
    pub why: String,
}

/// One observation the hook did not record now (s4-Q14, H2).
#[derive(Clone, PartialEq, Eq, Debug)]
pub enum Unrecorded {
    /// Dropped: neither recorded nor held.
    Dropped(NotRecorded),
    /// Kept in the session's hold, for `why` (the delivery's or the
    /// daemon's text).
    Held {
        what: String,
        harp: String,
        why: String,
    },
    /// Kept in the session's hold behind the facts it already holds, so the
    /// session's facts are recorded in the order they happened.
    HeldBehind { what: String, harp: String },
}

impl From<NotRecorded> for Unrecorded {
    fn from(dropped: NotRecorded) -> Unrecorded {
        Unrecorded::Dropped(dropped)
    }
}

impl From<Unrecorded> for HookWarning {
    fn from(unrecorded: Unrecorded) -> HookWarning {
        match unrecorded {
            Unrecorded::Dropped(NotRecorded { what, why }) => {
                HookWarning::NotRecorded { what, why }
            }
            Unrecorded::Held { what, harp, why } => HookWarning::Held { what, harp, why },
            Unrecorded::HeldBehind { what, harp } => HookWarning::HeldBehind { what, harp },
        }
    }
}

/// What records the hook's observations: the application's, behind the hook.
pub trait Recording {
    /// Record what `answer.event` holds for the session `harp`, within the
    /// hook's own delivery budget for `native`. Returns what was not
    /// recorded now.
    fn record(&self, native: &str, harp: &str, answer: &HookAnswer) -> Vec<Unrecorded>;
}

/// [`crate::execute`], then record the observation. `statusline` and an
/// answer that failed record nothing.
pub fn execute_recording(
    engine: &str,
    hook_name: &str,
    input: &str,
    harp: &str,
    service: &dyn HookService,
    recording: &dyn Recording,
) -> Result<Outcome, Box<dyn Diagnostic>> {
    let (mut outcome, answered) = crate::execute::answer(engine, hook_name, input, harp, service)?;
    // With no harp there is no session to address: nothing is attempted.
    if let Some(answered) = answered.filter(|_| !harp.is_empty()) {
        outcome.warnings.extend(
            recording
                .record(hook_name, harp, &answered)
                .into_iter()
                .map(HookWarning::from),
        );
    }
    Ok(outcome)
}

#[cfg(test)]
#[path = "capture.test.rs"]
mod tests;
