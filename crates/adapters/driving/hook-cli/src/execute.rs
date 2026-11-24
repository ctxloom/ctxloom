//! `hook <engine> execute <hook-name>`: the one callback every engine fires
//! (the owner's ruling, 2026-11-08). Translation only: it knows no engine.
//! `statusline` is the engine's statusline command, rendered here; every other
//! name is the engine's native hook event, answered by the application through
//! the registry's engine codec.

use coded_diagnostic::Diagnostic;
use setup_application::{HookAnswer, Offered};

use crate::{hud, HookService, HookWarning};

/// The hook name every engine's statusline command is installed as.
pub const STATUSLINE: &str = "statusline";

/// What the hook answered: the engine's input, and what it warned of.
#[derive(Clone, PartialEq, Eq, Debug, Default)]
pub struct Outcome {
    pub stdout: String,
    pub warnings: Vec<HookWarning>,
    /// What to acknowledge to the daemon once `stdout` is printed and
    /// flushed ([`acknowledge`]); empty: nothing.
    pub offered: Offered,
}

/// Print the outcome to the engine, FLUSH, and only then acknowledge what it
/// offered: the one place the order lives (message delivery, owner
/// 2026-11-19). A write or flush that fails sends no ack, so the messages
/// stay pending and are offered again. The error is the write's; the
/// answer is whether the ack was recorded (see [`acknowledge`]).
pub fn deliver(
    outcome: &Outcome,
    out: &mut dyn std::io::Write,
    service: &dyn HookService,
) -> std::io::Result<bool> {
    out.write_all(outcome.stdout.as_bytes())?;
    out.flush()?;
    Ok(acknowledge(outcome, service))
}

/// The ack of an outcome's offer: call it ONLY after `outcome.stdout` has
/// been written to the engine and flushed (message delivery, owner
/// 2026-11-19: never marked delivered before it has been). The engine gives
/// no acknowledgement, so printed and flushed is the best proof of delivery
/// there is. A hook that dies before this leaves the messages pending, to
/// be offered again. Whether the daemon recorded it (an unrecorded ack is a
/// repeat later, never a loss).
pub fn acknowledge(outcome: &Outcome, service: &dyn HookService) -> bool {
    outcome.offered.is_empty() || service.acknowledge(&outcome.offered)
}

/// The statusline command's answer with `asks` permission asks waiting in
/// the session's project (the caller asked a daemon already serving, within
/// its own bound; 0 when it could not): rendering only, nothing recorded.
pub fn statusline(input: &str, harp: &str, asks: u32) -> Outcome {
    Outcome {
        stdout: hud(input, harp, 0, asks),
        ..Outcome::default()
    }
}

/// Run the callback. `input` is stdin, `harp` the session the hook runs in
/// (may be empty).
///
/// - `statusline` renders the statusline and records nothing (the service is
///   not asked);
/// - any other name is the service's answer; a session with no usable harp
///   is the `NoUsableHarp` warning and empty stdout;
/// - the service's refusal (an engine the registry does not know, a payload
///   the codec cannot read) is returned as it is.
pub fn execute(
    engine: &str,
    hook_name: &str,
    input: &str,
    harp: &str,
    service: &dyn HookService,
) -> Result<Outcome, Box<dyn Diagnostic>> {
    answer(engine, hook_name, input, harp, service).map(|(outcome, _)| outcome)
}

/// [`execute`], and with the outcome the application's answer when there is
/// one to act on further (not for the statusline, nor a harp-less session).
pub(crate) fn answer(
    engine: &str,
    hook_name: &str,
    input: &str,
    harp: &str,
    service: &dyn HookService,
) -> Result<(Outcome, Option<HookAnswer>), Box<dyn Diagnostic>> {
    if hook_name == STATUSLINE {
        return Ok((
            Outcome {
                stdout: hud(input, harp, 0, 0),
                ..Outcome::default()
            },
            None,
        ));
    }
    let answer = service.execute(engine, hook_name, input, harp)?;
    if answer.harp_missing {
        return Ok((
            Outcome {
                stdout: String::new(),
                warnings: vec![HookWarning::NoUsableHarp {
                    harp: harp.to_string(),
                }],
                ..Outcome::default()
            },
            None,
        ));
    }
    let outcome = Outcome {
        stdout: answer.stdout.clone(),
        warnings: Vec::new(),
        offered: answer.offered.clone(),
    };
    Ok((outcome, Some(answer)))
}

#[cfg(test)]
#[path = "execute.test.rs"]
mod tests;
