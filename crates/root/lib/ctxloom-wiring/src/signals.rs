//! A run's signals (ruling 114): what ctxloom does when it is asked to stop
//! while a run goes on. It hears SIGHUP, SIGINT and SIGTERM for the whole run
//! (`os::Os::signals`), so none of them ends ctxloom by its default
//! disposition and leaves the engine orphaned. A signal that stops the run
//! requests the launcher's [`Stop`]: a turn's tree is asked to stop and killed
//! after the grace, a terminal session's direct child is killed
//! (`host-launcher`). The run then ends as any run whose engine exited does
//! (its end facts recorded, its listener stopped, its locks released), and the
//! command exits with the signal's status (128 plus its number), said as
//! [`RunError::Stopped`].
//!
//! Which signals stop a run depends on who holds the terminal. A one-shot's
//! turns run in a process group of their own, which no terminal signal
//! reaches, so every one of the three stops it. The human's session shares
//! ctxloom's group, so a Ctrl-C at the terminal reaches the engine itself,
//! which decides what it means (claude cancels a generation): a SIGINT is
//! heard and let pass, and only a SIGHUP or a SIGTERM stops the session.

use std::sync::{Arc, OnceLock};

use host_launcher::Stop;
use os::{Signal, Signals};
use run_application::RunError;

/// Every signal a run hears, so none ends ctxloom by its default.
pub(crate) const HEARD: [Signal; 3] = Signal::ALL;

/// The signals that stop a one-shot: all of them.
pub(crate) const STOP_ONE_SHOT: [Signal; 3] = Signal::ALL;

/// The signals that stop the human's session: not SIGINT, which the
/// terminal gives the engine itself.
pub(crate) const STOP_SESSION: [Signal; 2] = [Signal::Hangup, Signal::Terminate];

/// The first signal of `stop_on` heard, once one is.
#[derive(Clone, Debug, Default)]
pub(crate) struct Heard(Arc<OnceLock<Signal>>);

impl Heard {
    /// The signal that stopped the run, if one did.
    pub(crate) fn signal(&self) -> Option<Signal> {
        self.0.get().copied()
    }

    /// Make the run's result what the signal that stopped it says; leave it
    /// as it is when none did.
    pub(crate) fn overrule<T>(&self, result: &mut Result<T, RunError>) {
        if let Some(signal) = self.signal() {
            *result = Err(RunError::Stopped {
                signal: signal.name().to_string(),
                status: signal.exit_status(),
            });
        }
    }
}

/// Hear `signals` until one of `stop_on` arrives: then record it in `heard`
/// and request `stop`. A signal outside `stop_on` is let pass.
pub(crate) async fn forward(
    mut signals: Signals,
    stop_on: &'static [Signal],
    stop: Stop,
    heard: Heard,
) {
    loop {
        let signal = signals.recv().await;
        // Red: nothing is stopped yet.
        let _ = (stop_on.contains(&signal), &stop, &heard);
    }
}

#[cfg(test)]
#[path = "signals.test.rs"]
mod tests;
