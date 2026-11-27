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

use coded_diagnostic::Diagnostic;
use setup_application::{NotRecorded, Recording, Unrecorded};

use crate::{HookService, HookWarning, Outcome};

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
