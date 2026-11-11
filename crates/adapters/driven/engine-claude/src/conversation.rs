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
    /// Send `message` with `urgency`.
    pub(crate) fn send(
        &mut self,
        message: ChildMessage,
        urgency: Urgency,
    ) -> Result<(Queued, Vec<String>), ChildError> {
        self.open()?;
        match urgency {
            Urgency::Inform => {
                self.informs.push(message);
                Ok((Queued::Held, Vec::new()))
            }
            Urgency::Wake => Ok(self.queue(Item::Turn {
                messages: vec![message],
                steer: false,
            })),
            Urgency::Steer => {
                let item = Item::Turn {
                    messages: vec![message],
                    steer: true,
                };
                match &self.in_flight {
                    None => Ok(self.queue(item)),
                    Some(InFlight::Clear { .. }) => {
                        self.hold_steer(item);
                        Ok((Queued::Held, Vec::new()))
                    }
                    Some(InFlight::Turn { .. }) => {
                        self.hold_steer(item);
                        Ok((Queued::Interrupting, self.ask_interrupt()))
                    }
                }
            }
        }
    }

    /// Ask the turn in flight to stop: whether one was asked, and the line.
    pub(crate) fn interrupt(&mut self) -> Result<(bool, Vec<String>), ChildError> {
        self.open()?;
        let writes = self.ask_interrupt();
        Ok((!writes.is_empty(), writes))
    }

    /// Clear the conversation, now or after what is ahead of it.
    pub(crate) fn clear(&mut self) -> Result<(Queued, Vec<String>), ChildError> {
        self.open()?;
        Ok(self.queue(Item::Clear))
    }

    /// End: the ids never written, and the interrupt of a turn in flight.
    pub(crate) fn end(&mut self) -> Result<(Vec<String>, Vec<String>), ChildError> {
        self.open()?;
        let writes = self.ask_interrupt();
        self.closed = true;
        let mut undelivered: Vec<String> = self.informs.drain(..).map(|m| m.id).collect();
        for item in self.held.drain(..) {
            if let Item::Turn { messages, .. } = item {
                undelivered.extend(messages.into_iter().map(|m| m.id));
            }
        }
        Ok((undelivered, writes))
    }

    /// One line of claude's stdout.
    pub(crate) fn read(&mut self, line: &str) -> Step {
        let mut step = Step::default();
        let Ok(event) = Json::parse(line) else {
            return step;
        };
        if let Some(id) = event.get("session_id").and_then(Json::as_str) {
            self.native_id = Some(id.to_string());
        }
        match event.get("type").and_then(Json::as_str) {
            Some("system") => {
                if event.get("subtype").and_then(Json::as_str) == Some("init") && !self.started {
                    if let Some(native_id) = self.native_id.clone() {
                        self.started = true;
                        step.observations
                            .push(ChildObservation::Started { native_id });
                    }
                }
            }
            Some("user") => {
                if matches!(event.get("isReplay"), Some(Json::Bool(true))) {
                    if let Some(InFlight::Turn { ids, echoed }) = &mut self.in_flight {
                        if !*echoed {
                            *echoed = true;
                            step.observations.push(ChildObservation::TurnStarted {
                                message_ids: ids.clone(),
                            });
                        }
                    }
                }
            }
            Some("control_response") => {
                let accepted = event
                    .get("response")
                    .and_then(|r| r.get("subtype"))
                    .and_then(Json::as_str)
                    == Some("success");
                step.observations
                    .push(ChildObservation::InterruptAnswered { accepted });
            }
            Some("conversation_reset") => {
                if let Some(InFlight::Clear { reset }) = &mut self.in_flight {
                    *reset = true;
                }
            }
            Some("result") => {
                step.observations.push(self.result(&event));
                self.interrupting = false;
                if !self.closed {
                    if let Some(item) = self.held.pop_front() {
                        step.writes.push(self.write(item));
                    }
                }
            }
            _ => {}
        }
        step
    }

    /// The engine's process is over: a turn in flight ends failed, then the
    /// child. Nothing is taken after.
    pub(crate) fn exited(&mut self, exit: Exit, detail: String) -> Vec<ChildObservation> {
        self.closed = true;
        let mut observations = Vec::new();
        if let Some(InFlight::Turn { ids, .. }) = self.in_flight.take() {
            observations.push(ChildObservation::TurnEnded {
                message_ids: ids,
                outcome: TurnOutcome::Failed {
                    class: FailureClass::EngineError,
                    detail: format!("the engine exited mid-turn: {detail}"),
                    native_id: self.native_id.clone(),
                },
            });
        }
        observations.push(ChildObservation::Exited { exit, detail });
        observations
    }

    fn open(&self) -> Result<(), ChildError> {
        if self.closed {
            return Err(ChildError::Closed);
        }
        Ok(())
    }

    /// Write `item` now when idle, else hold it last.
    fn queue(&mut self, item: Item) -> (Queued, Vec<String>) {
        if self.in_flight.is_none() {
            (Queued::Written, vec![self.write(item)])
        } else {
            self.held.push_back(item);
            (Queued::Held, Vec::new())
        }
    }

    /// Hold a steer before everything but the steers ahead of it.
    fn hold_steer(&mut self, item: Item) {
        let at = self
            .held
            .iter()
            .take_while(|held| matches!(held, Item::Turn { steer: true, .. }))
            .count();
        self.held.insert(at, item);
    }

    /// The interrupt's line, once per turn in flight.
    fn ask_interrupt(&mut self) -> Vec<String> {
        if !matches!(self.in_flight, Some(InFlight::Turn { .. })) || self.interrupting {
            return Vec::new();
        }
        self.interrupting = true;
        self.interrupts += 1;
        vec![Json::object([
            ("type", Json::text("control_request")),
            (
                "request_id",
                Json::text(format!("ctxloom-interrupt-{}", self.interrupts)),
            ),
            (
                "request",
                Json::object([("subtype", Json::text("interrupt"))]),
            ),
        ])
        .compact()]
    }

    /// `item`'s line; it is in flight from now.
    fn write(&mut self, item: Item) -> String {
        let text = match item {
            Item::Clear => {
                self.in_flight = Some(InFlight::Clear { reset: false });
                CLEAR.to_string()
            }
            Item::Turn { messages, .. } => {
                let messages: Vec<ChildMessage> = self.informs.drain(..).chain(messages).collect();
                self.in_flight = Some(InFlight::Turn {
                    ids: messages.iter().map(|m| m.id.clone()).collect(),
                    echoed: false,
                });
                messages
                    .into_iter()
                    .map(|m| m.text)
                    .collect::<Vec<_>>()
                    .join(JOIN)
            }
        };
        user_message(&text)
    }

    /// What a `result` ends.
    fn result(&mut self, result: &Json) -> ChildObservation {
        match self.in_flight.take() {
            Some(InFlight::Clear { reset }) => ChildObservation::Cleared {
                native_id: if reset { self.native_id.clone() } else { None },
            },
            Some(InFlight::Turn { ids, .. }) => ChildObservation::TurnEnded {
                message_ids: ids,
                outcome: self.outcome(result),
            },
            None => ChildObservation::TurnEnded {
                message_ids: Vec::new(),
                outcome: self.outcome(result),
            },
        }
    }

    fn outcome(&self, result: &Json) -> TurnOutcome {
        let native_id = self.native_id.clone();
        if result.get("terminal_reason").and_then(Json::as_str) == Some(ABORTED) {
            return TurnOutcome::Failed {
                class: FailureClass::Interrupted,
                detail: format!("the turn was interrupted ({ABORTED})"),
                native_id,
            };
        }
        let said = result
            .get("result")
            .and_then(Json::as_str)
            .unwrap_or_default()
            .to_string();
        if matches!(result.get("is_error"), Some(Json::Bool(true))) {
            return TurnOutcome::Failed {
                class: crate::stream::class_of(result),
                detail: said,
                native_id,
            };
        }
        TurnOutcome::Answered {
            answer: said,
            native_id,
        }
    }
}

/// claude's stream-json user message.
fn user_message(text: &str) -> String {
    Json::object([
        ("type", Json::text("user")),
        (
            "message",
            Json::object([("role", Json::text("user")), ("content", Json::text(text))]),
        ),
    ])
    .compact()
}

#[cfg(test)]
#[path = "conversation.test.rs"]
mod tests;
