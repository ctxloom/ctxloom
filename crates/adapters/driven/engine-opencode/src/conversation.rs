//! A long-lived opencode child's conversation (D15, D20, D23): what is asked
//! of `opencode acp` and what its answers mean. Pure: the process and the
//! ACP connection are [`crate::child`]'s; this says, for each thing asked and
//! each thing heard, the ACP calls to make ([`Act`]) and what was observed.
//!
//! The protocol is ACP v1 (the spike, opencode 1.18.4): `initialize`,
//! `session/new` (or `session/load` to resume), `session/set_model`, then
//! `session/prompt`s, whose REPLY is the end of the turn (`stopReason`), and
//! the `session/cancel` notification, which makes every pending prompt reply
//! `cancelled`.
//!
//! The urgencies (D20; the same outcome as claude's where the port says so):
//! - wake: a prompt when idle; mid-turn it is HELD until every prompt in
//!   flight has replied (opencode would join it to the turn, so it cannot be
//!   written early), then written in order;
//! - inform: held, and rides on the next prompt written (its text first, the
//!   prompt names its id);
//! - steer: idle, a wake; mid-turn the NATIVE JOIN: written at once, opencode
//!   takes it at the turn's next step boundary, and each prompt's reply ends
//!   its own messages' turn (both answered as the joined turn ended). After
//!   an interrupt was asked (the hard stop, `session/cancel`), a steer is held
//!   FIRST (after any earlier steer) and written once the cancelled prompts
//!   have replied;
//! - clear: `session/new` on the same process (opencode has no `/clear`),
//!   when idle, else held in order; its new session id is the rotation.
//!
//! Heard: a prompt's reply ends the turn of the messages it carried; the
//! text of the turn's LAST agent message (`agent_message_chunk`s, by message
//! id) is its answer. Nothing else of `session/update` is kept: a child's
//! words are its plugin's to record (owner ruling 58).

use std::collections::VecDeque;

use run_ports::{
    ChildError, ChildMessage, ChildObservation, FailureClass, Queued, TurnOutcome, Urgency,
};

/// The text between messages that ride on one prompt.
const JOIN: &str = "\n\n";

/// How a child is opened: where it works, the model it is set to, and the
/// opencode session it continues (none starts a new one).
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub(crate) struct Opening {
    pub cwd: String,
    pub model: String,
    pub resume: Option<String>,
}

/// An ACP call to make, in the order decided.
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) enum Act {
    Initialize,
    NewSession {
        cwd: String,
    },
    LoadSession {
        session: String,
        cwd: String,
    },
    SetModel {
        session: String,
        model: String,
    },
    /// `session/prompt`; `key` names its reply.
    Prompt {
        key: u64,
        session: String,
        ids: Vec<String>,
        text: String,
    },
    Cancel {
        session: String,
    },
    /// The engine's input ends (after everything decided before it).
    CloseInput,
}

/// How a prompt's turn stopped (ACP's `stopReason`).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum Stop {
    EndTurn,
    MaxTokens,
    MaxTurnRequests,
    Refusal,
    Cancelled,
    /// A reason this schema does not name.
    Other,
}

impl Stop {
    fn name(self) -> &'static str {
        match self {
            Stop::EndTurn => "end_turn",
            Stop::MaxTokens => "max_tokens",
            Stop::MaxTurnRequests => "max_turn_requests",
            Stop::Refusal => "refusal",
            Stop::Cancelled => "cancelled",
            Stop::Other => "another reason",
        }
    }
}

/// What the engine answered. An `Err` is the JSON-RPC error's text.
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) enum Heard {
    Initialized(Result<(), String>),
    /// `session/new` or `session/load` answered: the session's id.
    SessionReady(Result<String, String>),
    ModelSet(Result<(), String>),
    Answered {
        key: u64,
        stop: Result<Stop, String>,
    },
    /// An `agent_message_chunk`'s text.
    Said {
        message_id: Option<String>,
        text: String,
    },
}

/// Something waiting to be asked.
#[derive(Debug)]
enum Item {
    Turn {
        messages: Vec<ChildMessage>,
        steer: bool,
    },
    Clear,
}

/// A prompt in flight: its reply's key and the messages it carried.
#[derive(Debug)]
struct Prompting {
    key: u64,
    ids: Vec<String>,
}

/// What opencode is working on.
#[derive(Debug)]
enum Busy {
    /// Opening (`clear` false) or clearing: initialize, the session, its model.
    Setup { clear: bool },
    /// The prompts of the turn in flight, more than one after a join.
    Turn {
        prompts: Vec<Prompting>,
        cancelling: bool,
        /// Whether the cancel's outcome was said.
        answered: bool,
    },
}

#[derive(Debug)]
pub(crate) struct Conversation {
    opening: Opening,
    held: VecDeque<Item>,
    informs: Vec<ChildMessage>,
    busy: Option<Busy>,
    session: Option<String>,
    prompts: u64,
    /// The turn's last agent message: its id and its text so far.
    reply_id: Option<String>,
    reply: String,
    /// Why the opening or a clear failed: the exit's detail.
    failure: Option<String>,
    closed: bool,
}

impl Conversation {
    /// A conversation being opened, and its first call.
    pub(crate) fn new(opening: Opening) -> (Conversation, Vec<Act>) {
        let conversation = Conversation {
            opening,
            held: VecDeque::new(),
            informs: Vec::new(),
            busy: Some(Busy::Setup { clear: false }),
            session: None,
            prompts: 0,
            reply_id: None,
            reply: String::new(),
            failure: None,
            closed: false,
        };
        (conversation, vec![Act::Initialize])
    }

    /// Send `message` with `urgency`.
    pub(crate) fn send(
        &mut self,
        message: ChildMessage,
        urgency: Urgency,
    ) -> Result<(Queued, Vec<Act>), ChildError> {
        self.open()?;
        let item = Item::Turn {
            messages: vec![message],
            steer: urgency == Urgency::Steer,
        };
        match urgency {
            Urgency::Inform => {
                if let Item::Turn { messages, .. } = item {
                    self.informs.extend(messages);
                }
                Ok((Queued::Held, Vec::new()))
            }
            Urgency::Wake => Ok(self.queue(item)),
            Urgency::Steer => match &self.busy {
                None => Ok(self.queue(item)),
                Some(Busy::Setup { .. }) => {
                    self.hold_steer(item);
                    Ok((Queued::Held, Vec::new()))
                }
                Some(Busy::Turn {
                    cancelling: true, ..
                }) => {
                    self.hold_steer(item);
                    Ok((Queued::Interrupting, Vec::new()))
                }
                Some(Busy::Turn { .. }) => Ok((Queued::Written, vec![self.ask(item)])),
            },
        }
    }

    /// Ask the turn in flight to stop: whether one was asked, and the call.
    pub(crate) fn interrupt(&mut self) -> Result<(bool, Vec<Act>), ChildError> {
        self.open()?;
        let acts = self.cancel();
        Ok((!acts.is_empty(), acts))
    }

    /// Clear the conversation, now or after what is ahead of it.
    pub(crate) fn clear(&mut self) -> Result<(Queued, Vec<Act>), ChildError> {
        self.open()?;
        Ok(self.queue(Item::Clear))
    }

    /// End: the ids never asked, and the calls (a turn in flight cancelled,
    /// then the input's end).
    pub(crate) fn end(&mut self) -> Result<(Vec<String>, Vec<Act>), ChildError> {
        self.open()?;
        let mut acts = self.cancel();
        acts.push(Act::CloseInput);
        self.closed = true;
        Ok((self.undelivered(), acts))
    }

    /// The ids held and never asked: still owed.
    fn undelivered(&mut self) -> Vec<String> {
        let mut undelivered: Vec<String> = self.informs.drain(..).map(|m| m.id).collect();
        for item in self.held.drain(..) {
            if let Item::Turn { messages, .. } = item {
                undelivered.extend(messages.into_iter().map(|m| m.id));
            }
        }
        undelivered
    }

    /// The input's end, when the caller lets go of the child.
    pub(crate) fn release(&mut self) -> Vec<Act> {
        if self.closed {
            return Vec::new();
        }
        self.closed = true;
        vec![Act::CloseInput]
    }

    /// Something the engine answered.
    pub(crate) fn hear(&mut self, heard: Heard) -> (Vec<ChildObservation>, Vec<Act>) {
        match heard {
            Heard::Initialized(Ok(())) => (Vec::new(), self.open_session()),
            Heard::SessionReady(Ok(session)) => {
                self.session = Some(session.clone());
                if self.opening.model.is_empty() {
                    self.ready()
                } else {
                    let model = self.opening.model.clone();
                    (Vec::new(), vec![Act::SetModel { session, model }])
                }
            }
            Heard::ModelSet(Ok(())) => self.ready(),
            Heard::Initialized(Err(error))
            | Heard::SessionReady(Err(error))
            | Heard::ModelSet(Err(error)) => (Vec::new(), self.fail(error)),
            Heard::Said { message_id, text } => {
                if matches!(self.busy, Some(Busy::Turn { .. })) {
                    if message_id != self.reply_id {
                        self.reply_id = message_id;
                        self.reply.clear();
                    }
                    self.reply.push_str(&text);
                }
                (Vec::new(), Vec::new())
            }
            Heard::Answered { key, stop } => self.answered(key, stop),
        }
    }

    /// The engine's process is over: a turn in flight ends failed, then the
    /// child. Nothing is taken after. `detail` is what it said on stderr; an
    /// opening that failed says why, when stderr said nothing.
    pub(crate) fn exited(
        &mut self,
        exit: run_ports::Exit,
        detail: String,
    ) -> Vec<ChildObservation> {
        self.closed = true;
        let detail = match (detail.is_empty(), self.failure.take()) {
            (true, Some(failure)) => failure,
            _ => detail,
        };
        let mut observations = Vec::new();
        if let Some(Busy::Turn { prompts, .. }) = self.busy.take() {
            for prompt in prompts {
                observations.push(ChildObservation::TurnEnded {
                    message_ids: prompt.ids,
                    outcome: TurnOutcome::Failed {
                        class: FailureClass::EngineError,
                        detail: format!("the engine exited mid-turn: {detail}"),
                        native_id: self.session.clone(),
                    },
                });
            }
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

    /// The session call of an opening or a clear.
    fn open_session(&mut self) -> Vec<Act> {
        let cwd = self.opening.cwd.clone();
        match (&self.busy, &self.opening.resume) {
            (Some(Busy::Setup { clear: false }), Some(session)) => vec![Act::LoadSession {
                session: session.clone(),
                cwd,
            }],
            _ => vec![Act::NewSession { cwd }],
        }
    }

    /// The session is ready: the opening or the clear is said, and the next
    /// held item asked.
    fn ready(&mut self) -> (Vec<ChildObservation>, Vec<Act>) {
        let native_id = self.session.clone().unwrap_or_default();
        let observation = match self.busy.take() {
            Some(Busy::Setup { clear: true }) => ChildObservation::Cleared {
                native_id: Some(native_id),
            },
            _ => ChildObservation::Started { native_id },
        };
        (vec![observation], self.next())
    }

    /// The opening or a clear failed: the child cannot go on, so its input
    /// ends; the exit says why.
    fn fail(&mut self, error: String) -> Vec<Act> {
        self.failure = Some(error);
        self.busy = None;
        self.release()
    }

    /// A prompt's reply: its turn is over.
    fn answered(
        &mut self,
        key: u64,
        stop: Result<Stop, String>,
    ) -> (Vec<ChildObservation>, Vec<Act>) {
        let mut observations = Vec::new();
        let Some(Busy::Turn {
            prompts,
            cancelling,
            answered,
        }) = &mut self.busy
        else {
            return (observations, Vec::new());
        };
        let Some(at) = prompts.iter().position(|p| p.key == key) else {
            return (observations, Vec::new());
        };
        let prompt = prompts.remove(at);
        if *cancelling && !*answered {
            *answered = true;
            observations.push(ChildObservation::InterruptAnswered {
                accepted: stop == Ok(Stop::Cancelled),
            });
        }
        let done = prompts.is_empty();
        observations.push(ChildObservation::TurnEnded {
            message_ids: prompt.ids,
            outcome: self.outcome(stop),
        });
        if !done {
            return (observations, Vec::new());
        }
        self.busy = None;
        self.reply_id = None;
        self.reply.clear();
        (observations, self.next())
    }

    fn outcome(&self, stop: Result<Stop, String>) -> TurnOutcome {
        let native_id = self.session.clone();
        match stop {
            Ok(Stop::EndTurn) => TurnOutcome::Answered {
                answer: self.reply.clone(),
                native_id,
            },
            Ok(Stop::Cancelled) => TurnOutcome::Failed {
                class: FailureClass::Interrupted,
                detail: "the turn was cancelled".to_string(),
                native_id,
            },
            Ok(stop) => TurnOutcome::Failed {
                class: FailureClass::EngineError,
                detail: format!("the turn stopped: {}", stop.name()),
                native_id,
            },
            Err(error) => TurnOutcome::Failed {
                class: FailureClass::EngineError,
                detail: error,
                native_id,
            },
        }
    }

    /// The next held item, asked, unless the child is closed.
    fn next(&mut self) -> Vec<Act> {
        if self.closed {
            return Vec::new();
        }
        self.held
            .pop_front()
            .map(|item| self.ask(item))
            .into_iter()
            .collect()
    }

    /// Ask `item` now when idle, else hold it last.
    fn queue(&mut self, item: Item) -> (Queued, Vec<Act>) {
        if self.busy.is_none() {
            (Queued::Written, vec![self.ask(item)])
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

    /// The cancel of the turn in flight, once per turn.
    fn cancel(&mut self) -> Vec<Act> {
        let Some(Busy::Turn { cancelling, .. }) = &mut self.busy else {
            return Vec::new();
        };
        if *cancelling {
            return Vec::new();
        }
        *cancelling = true;
        vec![Act::Cancel {
            session: self.session.clone().unwrap_or_default(),
        }]
    }

    /// `item`'s call; it is in flight from now (a prompt joins a turn in
    /// flight).
    fn ask(&mut self, item: Item) -> Act {
        match item {
            Item::Clear => {
                self.busy = Some(Busy::Setup { clear: true });
                self.session = None;
                Act::NewSession {
                    cwd: self.opening.cwd.clone(),
                }
            }
            Item::Turn { messages, .. } => {
                let messages: Vec<ChildMessage> = self.informs.drain(..).chain(messages).collect();
                self.prompts += 1;
                let key = self.prompts;
                let ids: Vec<String> = messages.iter().map(|m| m.id.clone()).collect();
                let prompting = Prompting {
                    key,
                    ids: ids.clone(),
                };
                match &mut self.busy {
                    Some(Busy::Turn { prompts, .. }) => prompts.push(prompting),
                    _ => {
                        self.busy = Some(Busy::Turn {
                            prompts: vec![prompting],
                            cancelling: false,
                            answered: false,
                        })
                    }
                }
                Act::Prompt {
                    key,
                    session: self.session.clone().unwrap_or_default(),
                    ids,
                    text: messages
                        .into_iter()
                        .map(|m| m.text)
                        .collect::<Vec<_>>()
                        .join(JOIN),
                }
            }
        }
    }
}

#[cfg(test)]
#[path = "conversation.test.rs"]
mod tests;
