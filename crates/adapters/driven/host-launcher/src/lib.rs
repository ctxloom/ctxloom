//! The [`RunnerLauncher`] port on the host (step 1b-i): one turn's command run
//! to its end through ctxloom-lib's `run-within`. The command runs under a
//! deadline, its whole process tree is killed when the deadline passes, its
//! output is bounded per stream and the wait for that output is bounded too
//! (`drain_grace`). Its environment is exactly the command's: nothing of this
//! process's leaks in.
//!
//! Limits (`run-within`'s): a descendant that calls `setsid` or `setpgid`
//! escapes the kill, and the command runs in a process group of its own, so a
//! Ctrl-C at the terminal does not reach it.

use run_ports::{Exit, Launch, LaunchFailure, Limits, RunnerLauncher, TurnCommand, TurnOutput};
use run_within::{run_within, Spec, Within};

/// Launches a command on this host.
#[derive(Clone, Copy, Debug, Default)]
pub struct HostLauncher;

impl RunnerLauncher for HostLauncher {
    fn launch(&self, command: &TurnCommand, limits: &Limits) -> Launch {
        let mut spec = Spec::new(&command.program)
            .args(&command.args)
            .envs(&command.env);
        if !command.stdin.is_empty() {
            spec = spec.stdin(command.stdin.clone());
        }
        if let Some(cwd) = &command.cwd {
            spec = spec.current_dir(cwd);
        }
        let within = Within::new(limits.deadline)
            .drain_grace(limits.drain_grace)
            .max_bytes(limits.max_bytes);
        match run_within(&spec, &within) {
            Err(error) => Launch::NotStarted(LaunchFailure {
                // A program that is not there is not there next time either.
                deterministic: error.io().kind() == std::io::ErrorKind::NotFound,
                detail: error.io().to_string(),
            }),
            Ok(outcome) => Launch::Over(TurnOutput {
                exit: outcome.status.map_or(
                    Exit {
                        code: None,
                        signal: None,
                    },
                    |status| Exit {
                        code: status.code(),
                        signal: signal_of(&status),
                    },
                ),
                stdout: outcome.stdout,
                stderr: outcome.stderr,
                timed_out: outcome.timed_out,
                truncated: outcome.truncated,
                drain_incomplete: outcome.drain_incomplete,
            }),
        }
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
