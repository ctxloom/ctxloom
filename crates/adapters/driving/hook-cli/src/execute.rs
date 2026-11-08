//! `hook <engine> execute <hook-name>`: the one callback every engine fires
//! (the owner's ruling, 2026-11-08). Translation only: it knows no engine.
//! `statusline` is the engine's statusline command, rendered here; every other
//! name is the engine's native hook event, answered by the application through
//! the registry's engine codec.

use angzarr_diagnostic::Diagnostic;

use crate::{Binding, HookService, Outcome};

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
// Red-test stub (port/run-one-shot): the green step writes it.
pub fn execute(
    engine: &str,
    hook_name: &str,
    input: &str,
    harp: &str,
    service: &dyn HookService,
    binding: &mut impl Binding,
) -> Result<Outcome, Box<dyn Diagnostic>> {
    let _ = (engine, hook_name, input, harp, service, binding);
    todo!("execute")
}

#[cfg(test)]
#[path = "execute.test.rs"]
mod tests;
