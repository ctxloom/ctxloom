//! A run's signals (ruling 114): what ctxloom does when it is asked to stop
//! while a run goes on. It hears SIGHUP, SIGINT and SIGTERM for the whole run
//! (`os::Os::signals`), so none of them ends ctxloom by its default
//! disposition and leaves the engine orphaned. A signal that stops the run
//! requests the launcher's [`Stop`]: a turn's tree is asked to stop and killed
//! after the grace, a terminal session's direct child is asked to stop and
//! killed after its grace (`host-launcher`). The run then ends as any run whose engine exited does
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
pub const HEARD: [Signal; 3] = Signal::ALL;

/// The signals that stop a one-shot: all of them.
pub const STOP_ONE_SHOT: [Signal; 3] = Signal::ALL;

/// The signals that stop the human's session: not SIGINT, which the
/// terminal gives the engine itself.
pub const STOP_SESSION: [Signal; 2] = [Signal::Hangup, Signal::Terminate];

/// The first signal of `stop_on` heard, once one is.
#[derive(Clone, Debug, Default)]
pub struct Heard(Arc<OnceLock<Signal>>);

impl Heard {
    /// The signal that stopped the run, if one did.
    pub fn signal(&self) -> Option<Signal> {
        self.0.get().copied()
    }

    /// Make the run's result what the signal that stopped it says; leave it
    /// as it is when none did.
    pub fn overrule<T>(&self, result: &mut Result<T, RunError>) {
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
pub async fn forward(mut signals: Signals, stop_on: &'static [Signal], stop: Stop, heard: Heard) {
    loop {
        let signal = signals.recv().await;
        if stop_on.contains(&signal) {
            let _ = heard.0.set(signal);
            stop.request();
            return;
        }
    }
}

/// The stops a command that follows a daemon's run hears for its WHOLE
/// life (ruling 114), from before its first call to the daemon: a distill,
/// whose run the daemon starts before the command follows it. Heard on a
/// thread and a runtime of their own, so a signal that arrives before the
/// follow begins is not taken by its default disposition, and is told to
/// the follow as heard already (which then asks the daemon to stop the
/// run). The first of [`STOP_ONE_SHOT`] counts; a later one changes
/// nothing.
pub struct CommandStops {
    first: Arc<OnceLock<Signal>>,
    told: Arc<tokio::sync::Notify>,
    /// Why none can be heard, when none can.
    unheard: Option<String>,
}

impl CommandStops {
    /// Hear the signals `listen` makes (in the listener's runtime), once it
    /// listens: none of them ends the command by its default from now on.
    pub fn hearing(
        listen: impl FnOnce() -> std::io::Result<Signals> + Send + 'static,
    ) -> CommandStops {
        let first = Arc::new(OnceLock::new());
        let told = Arc::new(tokio::sync::Notify::new());
        let (listening, listens) = std::sync::mpsc::channel::<Result<(), String>>();
        let (set, tell) = (Arc::clone(&first), Arc::clone(&told));
        let spawned = std::thread::Builder::new()
            .name("ctxloom-stops".to_string())
            .spawn(move || {
                let runtime = match tokio::runtime::Builder::new_current_thread()
                    .enable_all()
                    .build()
                {
                    Ok(runtime) => runtime,
                    Err(e) => {
                        let _ = listening.send(Err(e.to_string()));
                        return;
                    }
                };
                runtime.block_on(async move {
                    let mut signals = match listen() {
                        Ok(signals) => signals,
                        Err(e) => {
                            let _ = listening.send(Err(e.to_string()));
                            return;
                        }
                    };
                    let _ = listening.send(Ok(()));
                    loop {
                        let signal = signals.recv().await;
                        if STOP_ONE_SHOT.contains(&signal) && set.set(signal).is_ok() {
                            tell.notify_waiters();
                        }
                    }
                });
            });
        let unheard = match spawned {
            Err(e) => Some(e.to_string()),
            Ok(_) => match listens.recv() {
                Ok(Ok(())) => None,
                Ok(Err(why)) => Some(why),
                Err(_) => Some("the signal listener ended".to_string()),
            },
        };
        CommandStops {
            first,
            told,
            unheard,
        }
    }

    /// The stop heard so far, if one was.
    pub fn signal(&self) -> Option<Signal> {
        self.first.get().copied()
    }

    /// Why no signal can be heard, when none can.
    pub fn unheard(&self) -> Option<&str> {
        self.unheard.as_deref()
    }

    /// The first stop's name, once one is heard (at once if it was).
    pub fn heard(&self) -> run_application::client::Heard<'static> {
        let (first, told) = (Arc::clone(&self.first), Arc::clone(&self.told));
        Box::pin(async move {
            loop {
                // Asked to be told before looking, so a stop set between
                // the look and the wait is not missed.
                let notified = told.notified();
                tokio::pin!(notified);
                notified.as_mut().enable();
                if let Some(signal) = first.get() {
                    return signal.name().to_string();
                }
                notified.await;
            }
        })
    }
}

#[cfg(test)]
#[path = "signals.test.rs"]
mod tests;
