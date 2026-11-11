//! The [`RunnerLauncher`] port on the host (step 1b-i): one turn's command run
//! to its end through ctxloom-lib's `run-within`. The command runs under a
//! deadline, its whole process tree is killed when the deadline passes, its
//! output is bounded per stream and the wait for that output is bounded too
//! (`drain_grace`). Its environment is exactly the command's: nothing of this
//! process's leaks in.
//!
//! Each launch runs `run-within` on a thread of its own, so that the caller
//! hears the pid at the real spawn (`run_within_started`) while the command is
//! still running: [`RunnerLauncher::launch`] returns once the spawn has
//! happened (or failed), and [`Running::finish`] joins the thread. A caller
//! that drops the [`Running`] without finishing it leaves the command to its
//! deadline (a turn) or to the human (a terminal session).
//!
//! The human's terminal session is `run_within::run_terminal`: the command
//! has this process's stdin, stdout and stderr, stays in this process's
//! process group (so a Ctrl-C at the terminal reaches it as it reaches any
//! program she started), and has no deadline. A [`Stop`] given at
//! [`HostLauncher::stopped_by`] ends it: only the DIRECT child is killed, not
//! its descendants. Unix is what is tested; macOS and Windows are not.
//!
//! The same [`Stop`] ends a turn (ruling 114): its whole process tree is
//! asked to stop (SIGTERM to its group), given `run-within`'s stop grace
//! (five seconds) to exit, then killed, so a caller that heard a signal asking
//! it to stop leaves no engine behind.
//!
//! Limits (`run-within`'s): a descendant of a turn that calls `setsid` or
//! `setpgid` escapes the kill, and a turn runs in a process group of its own,
//! so a Ctrl-C at the terminal does not reach it: the caller hears it and
//! requests the [`Stop`].

use std::cell::Cell;
use std::sync::mpsc;
use std::thread::JoinHandle;

use run_ports::{Exit, LaunchFailure, Limits, RunnerLauncher, Running, TurnCommand, TurnOutput};
pub use run_within::Stop;
use run_within::{run_terminal, run_within_stopped_by, RunError, Spec, Within};

/// Launches a command on this host.
#[derive(Clone, Debug, Default)]
pub struct HostLauncher {
    stop: Stop,
}

impl HostLauncher {
    /// A launcher whose turns and terminal sessions end when `stop` is
    /// requested: a turn's tree asked to stop, then killed after the grace; a
    /// terminal session's direct child killed.
    pub fn stopped_by(stop: Stop) -> HostLauncher {
        HostLauncher { stop }
    }
}

/// A command running on a thread of its own.
struct Host {
    pid: u32,
    thread: JoinHandle<Result<TurnOutput, LaunchFailure>>,
}

impl Running for Host {
    fn pid(&self) -> u32 {
        self.pid
    }

    fn finish(self: Box<Self>) -> Result<TurnOutput, LaunchFailure> {
        self.thread.join().unwrap_or_else(|_| {
            Err(LaunchFailure {
                deterministic: false,
                detail: "the thread running the command panicked".to_string(),
            })
        })
    }
}

/// Run `work` on a thread of its own and return once it has said the pid (the
/// spawn happened) or failed to spawn.
fn spawn_on_thread(
    work: impl FnOnce(&dyn Fn(u32)) -> Result<TurnOutput, RunError> + Send + 'static,
) -> Result<Box<dyn Running>, LaunchFailure> {
    let (tell, hear) = mpsc::channel::<Result<u32, LaunchFailure>>();
    let thread = std::thread::spawn(move || {
        let started = Cell::new(false);
        let result = work(&|pid| {
            started.set(true);
            let _ = tell.send(Ok(pid));
        })
        .map_err(|error| failure(&error));
        if let (false, Err(failure)) = (started.get(), &result) {
            let _ = tell.send(Err(failure.clone()));
        }
        result
    });
    match hear.recv() {
        Ok(Ok(pid)) => Ok(Box::new(Host { pid, thread })),
        Ok(Err(failure)) => {
            let _ = thread.join();
            Err(failure)
        }
        Err(_) => Err(LaunchFailure {
            deterministic: false,
            detail: "the thread running the command ended before it spawned".to_string(),
        }),
    }
}

fn failure(error: &RunError) -> LaunchFailure {
    LaunchFailure {
        // A program that is not there is not there next time either.
        deterministic: error.io().kind() == std::io::ErrorKind::NotFound,
        detail: error.io().to_string(),
    }
}

fn spec(command: &TurnCommand) -> Spec {
    let mut spec = Spec::new(&command.program)
        .args(&command.args)
        .envs(&command.env);
    if !command.stdin.is_empty() {
        spec = spec.stdin(command.stdin.clone());
    }
    if let Some(cwd) = &command.cwd {
        spec = spec.current_dir(cwd);
    }
    spec
}

fn exit_of(status: std::process::ExitStatus) -> Exit {
    Exit {
        code: status.code(),
        signal: signal_of(&status),
    }
}

impl RunnerLauncher for HostLauncher {
    // Its only mutant, a Default `Running`, does not type-check.
    #[cfg_attr(test, mutants::skip)]
    fn launch(
        &self,
        command: &TurnCommand,
        limits: &Limits,
    ) -> Result<Box<dyn Running>, LaunchFailure> {
        let spec = spec(command);
        let within = Within::new(limits.deadline)
            .drain_grace(limits.drain_grace)
            .max_bytes(limits.max_bytes);
        let stop = self.stop.clone();
        spawn_on_thread(move |started| {
            run_within_stopped_by(&spec, &within, &stop, started).map(|outcome| TurnOutput {
                exit: outcome.status.map_or(
                    Exit {
                        code: None,
                        signal: None,
                    },
                    exit_of,
                ),
                stdout: outcome.stdout,
                stderr: outcome.stderr,
                timed_out: outcome.timed_out,
                truncated: outcome.truncated,
                drain_incomplete: outcome.drain_incomplete,
            })
        })
    }

    fn launch_terminal(&self, command: &TurnCommand) -> Result<Box<dyn Running>, LaunchFailure> {
        let spec = spec(command);
        let stop = self.stop.clone();
        spawn_on_thread(move |started| {
            run_terminal(&spec, &stop, started).map(|outcome| TurnOutput {
                exit: exit_of(outcome.status),
                stdout: Vec::new(),
                stderr: Vec::new(),
                timed_out: false,
                truncated: false,
                drain_incomplete: false,
            })
        })
    }
}

/// The signal that ended a process, when one did: std spells it in the
/// status's text ("signal: 9 (SIGKILL)"), and the os crate has no accessor for
/// it, so that text is read rather than naming the platform here.
fn signal_of(status: &std::process::ExitStatus) -> Option<i32> {
    status
        .to_string()
        .strip_prefix("signal: ")?
        .split(|c: char| !c.is_ascii_digit())
        .next()?
        .parse()
        .ok()
}

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
