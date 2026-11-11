#![allow(unused_imports, dead_code, unused_variables)] // RED: the implementation follows
//! A long-lived claude child (D15): one `claude -p --input-format stream-json
//! --output-format stream-json --verbose --replay-user-messages` process,
//! many turns, on pipes run-within hands over (`run_piped`).
//!
//! The command is a turn's ([`crate::drive`]: the context file, the session
//! home's `--settings` and its hooks, the model and effort, a child's
//! environment), with streaming input and the echo of each message given.
//! [`crate::conversation`] says what to write and what a line means; this
//! module is its process: the control writes under one lock, a reader thread
//! reads stdout, writes what a line asks (the next held turn) and passes the
//! observations on, and a stderr thread keeps the start of stderr for the
//! exit's detail.
//!
//! The control's `Drop` closes the engine's input, so a child no one holds
//! ends after the turn it is on (claude exits on its input's end).

use std::io::{BufRead, BufReader, Read, Write};
use std::process::{ChildStdin, ExitStatus};
use std::sync::mpsc::{self, RecvTimeoutError};
use std::sync::{Arc, Mutex, MutexGuard, PoisonError};
use std::thread;
use std::time::{Duration, Instant};

use run_ports::{
    ChildControl, ChildEngine, ChildError, ChildMessage, ChildSpec, Ended, EngineError, Exit,
    Queued, StartedChild, TurnCommand, TurnRequest, Urgency,
};
use run_within::{run_piped, Piped, RunError, Spec};

use crate::conversation::Conversation;
use crate::{drive, stream, Claude};

/// How long, once the engine has exited, its stdout is read for: a
/// descendant that outlives it can hold the pipe open, and is then killed.
const DRAIN_GRACE: Duration = Duration::from_secs(2);

/// How often [`ChildControl::end`] looks whether the engine has exited.
const POLL: Duration = Duration::from_millis(10);

/// The most of stderr kept for the exit's detail.
const STDERR_KEPT: usize = 4096;

impl ChildEngine for Claude {
    fn start(&self, spec: &ChildSpec) -> Result<StartedChild, ChildError> {
        start(&child_command(spec)?)
    }
}

/// The child's command: a turn's, streaming. Its stdin is the conversation.
pub(crate) fn child_command(_spec: &ChildSpec) -> Result<TurnCommand, EngineError> {
    unimplemented!("RED: diabolic-karate")
}

fn start(_command: &TurnCommand) -> Result<StartedChild, ChildError> {
    unimplemented!("RED: diabolic-karate")
}

#[cfg(test)]
#[path = "child.test.rs"]
mod tests;
