//! opencode's drive half. Not yet built.

use run_ports::{
    Engine, EngineError, PrepareRequest, Prepared, TurnCommand, TurnOutcome, TurnOutput,
    TurnRequest,
};

use crate::{Opencode, NAME};

/// The variables of the caller's environment a child's turn is given.
pub const ENV_ALLOWLIST: &[&str] = &[];

/// The variables of the caller's environment the human's session is given.
pub const INTERACTIVE_ENV_ALLOWLIST: &[&str] = &[];

/// opencode's endpoint key: optional (a built-in provider's free models need
/// none), passed on when set.
pub const OPTIONAL_KEY: &str = "CTXLOOM_OPENCODE_API_KEY";

fn not_yet(operation: &str) -> EngineError {
    EngineError::NotYet {
        engine: NAME.to_string(),
        operation: operation.to_string(),
    }
}

impl Engine for Opencode {
    fn name(&self) -> &str {
        NAME
    }

    fn prepare(&self, _request: &PrepareRequest) -> Result<Prepared, EngineError> {
        Err(not_yet("prepare"))
    }

    fn drive_turn(&self, _request: &TurnRequest) -> Result<TurnCommand, EngineError> {
        Err(not_yet("drive_turn"))
    }

    fn finish_turn(&self, _output: &TurnOutput) -> TurnOutcome {
        TurnOutcome::Failed {
            class: run_ports::FailureClass::EngineError,
            detail: "not yet".to_string(),
            native_id: None,
        }
    }

    fn drive_interactive(&self, _request: &TurnRequest) -> Result<TurnCommand, EngineError> {
        Err(not_yet("drive_interactive"))
    }
}
