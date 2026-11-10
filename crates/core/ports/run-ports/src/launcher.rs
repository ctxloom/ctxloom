//! The runner launcher: one turn's command, run to its end.
//!
//! A child agent on claude runs turn-as-command: each turn is one command, a
//! later turn a new one, so a launch is a command and its end, with no
//! long-lived process in between. The launcher runs `command` under `limits`
//! and answers how it went; [`Launch::events`] is that answer as the
//! launcher's facts. This module is pure: a driven adapter (`host-launcher`)
//! does the running.
//!
//! A launch is two steps, so that the caller learns the pid at the real
//! spawn and not before: [`RunnerLauncher::launch`] spawns the command and
//! answers a [`Running`] (or why it could not start, and then nothing
//! started), the caller records what it must about a started runner, and
//! [`Running::finish`] waits for the command's end. The core has no thread of
//! its own to hear the pid from inside a blocking call, which is why the port
//! is not one call with a callback.
//!
//! Stopping a turn from outside, before its deadline, is not in this port yet:
//! the deadline is the only stop. The human's terminal session is ended by
//! her, or by a stop the adapter was given when it was made.

use std::collections::BTreeMap;
use std::fmt;
use std::time::Duration;

use crate::hook::RedactedEnv;

/// The command of one turn: the program, its arguments, its WHOLE
/// environment (the launcher adds none), what it is fed on stdin, and where
/// it runs.
#[derive(Clone, PartialEq, Eq)]
pub struct TurnCommand {
    pub program: String,
    pub args: Vec<String>,
    pub env: BTreeMap<String, String>,
    pub stdin: Vec<u8>,
    pub cwd: Option<String>,
    /// Names in `env` whose values its `Debug` withholds besides the bearer:
    /// the variables a permit set (owner ruling 78).
    pub withheld: Vec<String>,
}

impl fmt::Debug for TurnCommand {
    /// Without the bearer's value: a command is logged, the secret is not.
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("TurnCommand")
            .field("program", &self.program)
            .field("args", &self.args)
            .field("env", &RedactedEnv(&self.env))
            .field("stdin", &self.stdin)
            .field("cwd", &self.cwd)
            .finish()
    }
}

impl TurnCommand {
    /// `program`, with no arguments, no environment, no input, in the
    /// caller's directory.
    pub fn new(program: impl Into<String>) -> TurnCommand {
        TurnCommand {
            program: program.into(),
            args: Vec::new(),
            env: BTreeMap::new(),
            stdin: Vec::new(),
            cwd: None,
            withheld: Vec::new(),
        }
    }
}

/// How long a turn has, and how much it may say.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Limits {
    /// How long the command has to exit before its whole process tree is
    /// killed.
    pub deadline: Duration,
    /// How long, once it is over, its output is waited for: a descendant that
    /// outlives it can hold the pipes open.
    pub drain_grace: Duration,
    /// The most of each output stream that is kept.
    pub max_bytes: usize,
}

impl Limits {
    /// `deadline`, with two seconds of grace and 64 MiB of each stream.
    pub fn new(deadline: Duration) -> Limits {
        Limits {
            deadline,
            drain_grace: Duration::from_secs(2),
            max_bytes: 64 * 1024 * 1024,
        }
    }
}

/// How a started command ended: its exit code, or the signal that ended it.
/// Neither when it was killed at its deadline.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Exit {
    pub code: Option<i32>,
    pub signal: Option<i32>,
}

/// What a command that started left behind.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct TurnOutput {
    pub exit: Exit,
    pub stdout: Vec<u8>,
    pub stderr: Vec<u8>,
    /// The deadline passed and the tree was killed.
    pub timed_out: bool,
    /// Some output beyond the bound was discarded.
    pub truncated: bool,
    /// A descendant still held a pipe when the grace ended; later output is
    /// lost.
    pub drain_incomplete: bool,
}

/// A command that could not be started.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct LaunchFailure {
    /// Whether trying again would fail the same way (a missing program),
    /// rather than perhaps not (a resource limit).
    pub deterministic: bool,
    pub detail: String,
}

/// What came of launching a command.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Launch {
    NotStarted(LaunchFailure),
    /// It started, and is over.
    Over(TurnOutput),
}

/// The launcher's facts: what happened to the command, in order.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum LaunchEvent {
    LaunchFailed {
        deterministic: bool,
        detail: String,
    },
    RunExited {
        code: Option<i32>,
        signal: Option<i32>,
    },
}

impl Launch {
    /// The facts the answer itself makes, in order: a command that started
    /// and is over is `RunExited`; one that did not start is `LaunchFailed`.
    /// (`RunnerStarted`, with the pid, is the caller's to record between
    /// [`RunnerLauncher::launch`] and [`Running::finish`].)
    pub fn events(&self) -> Vec<LaunchEvent> {
        match self {
            Launch::NotStarted(failure) => vec![LaunchEvent::LaunchFailed {
                deterministic: failure.deterministic,
                detail: failure.detail.clone(),
            }],
            Launch::Over(output) => vec![LaunchEvent::RunExited {
                code: output.exit.code,
                signal: output.exit.signal,
            }],
        }
    }
}

/// A command that was spawned and is running.
pub trait Running: Send {
    /// The process id of the command, from the spawn.
    fn pid(&self) -> u32;

    /// Wait until the command is over (or killed at its deadline). A failure
    /// here is a wait that broke after the command started.
    fn finish(self: Box<Self>) -> Result<TurnOutput, LaunchFailure>;
}

/// Starts one turn's command. [`Running::finish`] blocks until it is over.
pub trait RunnerLauncher: Send + Sync {
    /// Spawn the command under `limits`. An `Err` started nothing.
    fn launch(
        &self,
        command: &TurnCommand,
        limits: &Limits,
    ) -> Result<Box<dyn Running>, LaunchFailure>;

    /// Spawn the human's terminal session: the command is given this
    /// process's terminal (its standard input, output and error) and its
    /// process group, with no deadline and nothing captured; the answer of
    /// [`Running::finish`] is how it ended (`TurnOutput`'s streams are empty).
    /// An `Err` started nothing.
    fn launch_terminal(&self, command: &TurnCommand) -> Result<Box<dyn Running>, LaunchFailure>;
}

#[cfg(test)]
#[path = "launcher.test.rs"]
mod tests;
