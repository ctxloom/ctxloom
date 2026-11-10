//! `opencode run --format json`, read. Not yet built.

use run_ports::{FailureClass, TurnOutcome, TurnOutput};

/// What a finished turn's output meant.
pub(crate) fn read_turn(_output: &TurnOutput) -> TurnOutcome {
    TurnOutcome::Failed {
        class: FailureClass::EngineError,
        detail: String::new(),
        native_id: None,
    }
}

#[cfg(test)]
#[path = "stream.test.rs"]
mod tests;
