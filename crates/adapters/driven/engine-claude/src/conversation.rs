#![allow(unused_imports, dead_code, unused_variables)] // RED: the implementation follows
//! A long-lived claude child's conversation (D15): what is written to
//! `claude -p --input-format stream-json` and what its
//! `--output-format stream-json --verbose --replay-user-messages` stream
//! means. Pure: the process is [`crate::child`]'s; this says, for each thing
//! asked and each line read, the lines to write and what was observed.
//!
//! One turn is in claude at a time. claude would queue a message written
//! mid-turn as its own next turn (the probe), but then a steer could not
//! overtake it; so a message that cannot start now is HELD here, and the
//! next held item is written when the turn in flight ends (its `result`).
//! Hence the turn a line belongs to is always the one in flight.
//!
//! - wake: written when idle, else held, in order;
//! - steer: to a turn in flight, the interrupt control request is written
//!   and the message is held FIRST (after any earlier steer); idle, a wake;
//! - inform: held, and rides on the next turn that is written (its text
//!   first, the turn names its id);
//! - clear: `/clear` as a user message, when idle, else held in order; claude
//!   runs it locally (`conversation_reset`, a new `session_id`, a `result`
//!   with no model turn).
//!
//! Read: the first `system`/`init` is [`ChildObservation::Started`]; the
//! echo of the message given (`isReplay`) starts its turn; `result` ends the
//! turn in flight (or the clear); `control_response` answers an interrupt.
//! Everything else (deltas, assistant and tool messages, hook and rate-limit
//! notices) is skipped: a child's words are its hooks' (owner ruling 51).

use std::collections::VecDeque;

use run_ports::{
    ChildError, ChildMessage, ChildObservation, Exit, FailureClass, Queued, TurnOutcome, Urgency,
};
use surface_ports::Json;

/// What claude's `/clear` is, sent as a user message.
const CLEAR: &str = "/clear";

/// The text between messages that ride on one turn.
const JOIN: &str = "\n\n";

/// The `terminal_reason` of a turn the interrupt stopped.
const ABORTED: &str = "aborted_streaming";

/// What reading a line said, and what to write for it.
#[derive(Debug, Default, PartialEq, Eq)]
pub(crate) struct Step {
    pub observations: Vec<ChildObservation>,
    pub writes: Vec<String>,
}

/// Something waiting to be written.
#[derive(Debug)]
enum Item {
    Turn {
        messages: Vec<ChildMessage>,
        steer: bool,
    },
    Clear,
}

/// What claude is working on.
#[derive(Debug)]
enum InFlight {
    Turn { ids: Vec<String>, echoed: bool },
    Clear { reset: bool },
}

#[derive(Debug, Default)]
pub(crate) struct Conversation {
    held: VecDeque<Item>,
    informs: Vec<ChildMessage>,
    in_flight: Option<InFlight>,
    interrupting: bool,
    interrupts: u64,
    native_id: Option<String>,
    started: bool,
    closed: bool,
}

impl Conversation {
    pub(crate) fn send(
        &mut self,
        _message: ChildMessage,
        _urgency: Urgency,
    ) -> Result<(Queued, Vec<String>), ChildError> {
        unimplemented!("RED: diabolic-karate")
    }

    pub(crate) fn interrupt(&mut self) -> Result<(bool, Vec<String>), ChildError> {
        unimplemented!("RED: diabolic-karate")
    }

    pub(crate) fn clear(&mut self) -> Result<(Queued, Vec<String>), ChildError> {
        unimplemented!("RED: diabolic-karate")
    }

    pub(crate) fn end(&mut self) -> Result<(Vec<String>, Vec<String>), ChildError> {
        unimplemented!("RED: diabolic-karate")
    }

    pub(crate) fn read(&mut self, _line: &str) -> Step {
        unimplemented!("RED: diabolic-karate")
    }

    pub(crate) fn exited(&mut self, _exit: Exit, _detail: String) -> Vec<ChildObservation> {
        unimplemented!("RED: diabolic-karate")
    }
}

#[cfg(test)]
#[path = "conversation.test.rs"]
mod tests;
