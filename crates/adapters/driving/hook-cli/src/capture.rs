//! PROPOSAL (design/hook-capture): the hook records what it observed, and
//! that never changes what it answers.
//!
//! [`execute_recording`] is [`crate::execute`] plus a [`Recording`] the
//! application supplies (the composition root's: decode-free, it is handed
//! the answer, whose `event` the codec decoded). Fail-open is by type:
//! `Recording::record` cannot fail, only report what it did not record, and
//! each such report is a stderr warning ([`crate::HookWarning::NotRecorded`]),
//! never stdout and never the exit code.

use angzarr_diagnostic::Diagnostic;
use setup_application::HookAnswer;

use crate::{Binding, HookService, HookWarning, Outcome};

/// One observation that was not recorded.
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct NotRecorded {
    /// The fact's external id.
    pub what: String,
    /// The delivery's text (unavailable, timed out, refused ...).
    pub why: String,
}

/// What records the hook's observations: the application's, behind the hook.
pub trait Recording {
    /// Record what `answer.event` holds for the session `harp`, within the
    /// hook's own delivery budget for `native`. Returns what was not recorded.
    fn record(&self, native: &str, harp: &str, answer: &HookAnswer) -> Vec<NotRecorded>;
}

/// [`crate::execute`], then record the observation. `statusline` and an
/// answer that failed record nothing.
pub fn execute_recording(
    engine: &str,
    hook_name: &str,
    input: &str,
    harp: &str,
    service: &dyn HookService,
    binding: &mut impl Binding,
    recording: &dyn Recording,
) -> Result<Outcome, Box<dyn Diagnostic>> {
    let (mut outcome, answered) =
        crate::execute::answer(engine, hook_name, input, harp, service, binding)?;
    if let Some(answered) = answered {
        outcome.warnings.extend(
            recording
                .record(hook_name, harp, &answered)
                .into_iter()
                .map(|NotRecorded { what, why }| HookWarning::NotRecorded { what, why }),
        );
    }
    Ok(outcome)
}

#[cfg(test)]
#[path = "capture.test.rs"]
mod tests;
