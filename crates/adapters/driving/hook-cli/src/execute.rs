//! `hook <engine> execute <hook-name>`: the one callback every engine fires
//! (the owner's ruling, 2026-11-08). Translation only: it knows no engine.
//! `statusline` is the engine's statusline command, rendered here; every other
//! name is the engine's native hook event, answered by the application through
//! the registry's engine codec.

use angzarr_diagnostic::Diagnostic;

use crate::{hud, Binding, HookService, HookWarning, Outcome};

/// The hook name every engine's statusline command is installed as.
pub const STATUSLINE: &str = "statusline";

/// Run the callback. `input` is stdin, `harp` the session the hook runs in
/// (may be empty).
///
/// - `statusline` renders the statusline and records nothing (the service is
///   not asked);
/// - any other name is the service's answer, and a bind it asks for is made
///   through `binding`; a session with no usable harp is the
///   `NoUsableHarp` warning and empty stdout;
/// - the service's refusal (an engine the registry does not know, a payload
///   the codec cannot read) is returned as it is.
pub fn execute(
    engine: &str,
    hook_name: &str,
    input: &str,
    harp: &str,
    service: &dyn HookService,
    binding: &mut impl Binding,
) -> Result<Outcome, Box<dyn Diagnostic>> {
    if hook_name == STATUSLINE {
        return Ok(Outcome {
            stdout: hud(input, harp, 0),
            warnings: Vec::new(),
        });
    }
    let answer = service.execute(engine, hook_name, input, harp)?;
    if answer.harp_missing {
        return Ok(Outcome {
            stdout: String::new(),
            warnings: vec![HookWarning::NoUsableHarp {
                harp: harp.to_string(),
            }],
        });
    }
    if let Some(bind) = &answer.bind {
        binding.bind(&bind.harp, &bind.native_id, &bind.transcript)?;
    }
    Ok(Outcome {
        stdout: answer.stdout,
        warnings: Vec::new(),
    })
}

#[cfg(test)]
#[path = "execute.test.rs"]
mod tests;
