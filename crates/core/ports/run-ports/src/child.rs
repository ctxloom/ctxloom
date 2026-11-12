//! The child-engine port (daemon design D15, D23): a LONG-LIVED child agent
//! the caller converses with, one engine process carrying many turns.
//!
//! Where [`crate::Engine`] gives the command of ONE turn (a process per turn),
//! a [`ChildEngine`] starts a child that stays: the caller sends it messages
//! with an [`Urgency`] (D12), interrupts its turn, clears its conversation
//! (D16, D17: the engine's own clear, a native-id rotation in the same
//! session) and ends it, and hears what came of each turn as
//! [`ChildObservation`]s. Each engine's adapter speaks its engine's own
//! protocol behind it (claude: stream-json over stdin and stdout).
//!
//! The observations are the turn's OUTCOME, the engine's native id and its
//! rotation, never the conversation: a child's words are its hooks' to record
//! (owner ruling 51), and nothing here is a transcript.
//!
//! Turns are keyed by the ids of the messages they carry (D28's model sheet):
//! a turn's [`ChildObservation::TurnStarted`] and
//! [`ChildObservation::TurnEnded`] name every message it was given.
//!
//! The port is synchronous, as the rest of the run ports: the control is
//! usable from any thread while another reads the observations.

use std::collections::BTreeMap;
use std::fmt;
use std::time::Duration;

use angzarr_diagnostic::{code, Arg, Diagnostic};

use crate::engine::{EngineError, TurnOutcome};
use crate::launcher::Exit;

/// How a message reaches a child (D12: one message type, with an urgency).
/// The same urgency means the same outcome on every engine (D20).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Urgency {
    /// With the next turn: it starts none of its own, and rides on the next
    /// message that does.
    Inform,
    /// Start a turn when the child is idle; one that arrives mid-turn waits
    /// for the turn in flight to end.
    Wake,
    /// Redirect the turn in flight: it is interrupted, and this message is
    /// the next turn, before any that waited. To an idle child it is a wake.
    Steer,
}

impl Urgency {
    /// The urgency's name, as the design spells it.
    pub fn name(self) -> &'static str {
        match self {
            Urgency::Inform => "inform",
            Urgency::Wake => "wake",
            Urgency::Steer => "steer",
        }
    }
}

/// One message to a child: its id (the key its turn is known by) and its text.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ChildMessage {
    pub id: String,
    pub text: String,
}

impl ChildMessage {
    pub fn new(id: impl Into<String>, text: impl Into<String>) -> ChildMessage {
        ChildMessage {
            id: id.into(),
            text: text.into(),
        }
    }
}

/// How a child's requests for permission are answered (an engine that asks
/// over its protocol, as opencode's ACP `session/request_permission`): no
/// human is there to ask. Each answer is for the one request, never
/// "always". The agent's permission posture says which; none refuses.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub enum ChildPermissions {
    /// Refused (fail closed): the engine goes on without.
    #[default]
    Refuse,
    /// Allowed.
    Allow,
}

/// What a child is started with: a turn request's settings, with no prompt
/// (the child is given its messages later).
#[derive(Clone, PartialEq, Eq)]
pub struct ChildSpec {
    /// The session home's absolute path.
    pub home: String,
    /// The context file [`crate::Engine::prepare`] named.
    pub context_path: String,
    /// The caller's environment: the engine passes on only what it allows.
    pub env: BTreeMap<String, String>,
    /// The user's own home, as the composition root resolved it once (the
    /// platform's home variable); none when unset. What `session_home: host`
    /// gives the engine, as [`crate::TurnRequest::user_home`].
    pub user_home: Option<String>,
    pub cwd: Option<String>,
    /// The model to run; empty names none.
    pub model: String,
    /// The effort level to run at; empty names none.
    pub effort: String,
    /// How the config says the engine is run.
    pub settings: crate::settings::EngineSettings,
    /// The engine's own session to continue (a child started again after its
    /// process ended).
    pub resume: Option<String>,
    /// The native session id to start a NEW conversation under (D17a: a
    /// clear by restart, with a fresh id). Never with `resume`. An engine
    /// that names its own sessions (opencode) has none to take.
    pub session_id: Option<String>,
    /// How the engine's permission requests are answered.
    pub permissions: ChildPermissions,
}

impl fmt::Debug for ChildSpec {
    /// The environment's names alone: a spec is logged, no value of it is.
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("ChildSpec")
            .field("home", &self.home)
            .field("context_path", &self.context_path)
            .field("env", &crate::hook::RedactedEnv(&self.env))
            .field("user_home", &self.user_home)
            .field("cwd", &self.cwd)
            .field("model", &self.model)
            .field("effort", &self.effort)
            .field("settings", &self.settings)
            .field("resume", &self.resume)
            .field("session_id", &self.session_id)
            .field("permissions", &self.permissions)
            .finish()
    }
}

/// What became of a message sent to a child.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Queued {
    /// Handed to the engine: its turn starts now.
    Written,
    /// Held by the adapter: it goes with a later turn (an inform), or when
    /// the turn in flight ends (a wake, or anything behind a clear).
    Held,
    /// The turn in flight was asked to stop; this message is the next turn.
    Interrupting,
}

/// What the caller hears from a child, in the order it happened.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum ChildObservation {
    /// The engine is up and named its own session id.
    Started { native_id: String },
    /// The engine began the turn that carries these messages (claude: the
    /// echo of the message it was given).
    TurnStarted { message_ids: Vec<String> },
    /// The turn that carried these messages is over (an interrupted one is
    /// [`crate::FailureClass::Interrupted`]). A turn the engine began of its
    /// own names no message.
    TurnEnded {
        message_ids: Vec<String>,
        outcome: TurnOutcome,
    },
    /// The engine answered an interrupt: whether it took it.
    InterruptAnswered { accepted: bool },
    /// A clear is done: the conversation starts over under `native_id`, the
    /// engine's new session id (record it as the session's rotation, D17).
    /// `None` when the engine reported no reset.
    Cleared { native_id: Option<String> },
    /// The engine's process is over; nothing follows. `detail` is the start
    /// of what it wrote on stderr, bounded.
    Exited { exit: Exit, detail: String },
}

/// How an ended child went.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Ended {
    pub exit: Exit,
    /// The messages that were held and never handed to the engine: still
    /// owed, for the caller to deliver elsewhere or drop.
    pub undelivered: Vec<String>,
}

/// A running child, steered from any thread.
pub trait ChildControl: Send + Sync {
    /// The engine process's id, from the spawn.
    fn pid(&self) -> u32;

    /// Send `message` with `urgency`.
    fn send(&self, message: ChildMessage, urgency: Urgency) -> Result<Queued, ChildError>;

    /// Ask the turn in flight to stop. False when there was none to ask (or
    /// it was already asked).
    fn interrupt(&self) -> Result<bool, ChildError>;

    /// Clear the conversation: now when idle, else after the turn in flight
    /// and whatever waits for it.
    fn clear(&self) -> Result<Queued, ChildError>;

    /// End the child: a turn in flight is interrupted, the engine's input is
    /// closed, and it gets `grace` to exit before its whole tree is stopped.
    /// Later calls refuse [`ChildError::Closed`].
    fn end(&self, grace: Duration) -> Result<Ended, ChildError>;
}

/// A started child: its control, and what it says, until it exits.
pub struct StartedChild {
    pub control: Box<dyn ChildControl>,
    /// Ends after [`ChildObservation::Exited`].
    pub observations: Box<dyn Iterator<Item = ChildObservation> + Send>,
}

impl fmt::Debug for StartedChild {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("StartedChild")
            .field("pid", &self.control.pid())
            .finish_non_exhaustive()
    }
}

/// An engine that runs long-lived children.
pub trait ChildEngine: Send + Sync {
    /// Start a child. An `Err` started nothing.
    fn start(&self, spec: &ChildSpec) -> Result<StartedChild, ChildError>;
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum ChildError {
    /// The child could not be planned (an effort level the engine does not
    /// take, a host home that is not there).
    Engine(EngineError),
    /// The engine's program could not be started.
    NotStarted {
        program: String,
        /// Whether trying again would fail the same way (a missing program).
        deterministic: bool,
        ext_err_txt: String,
    },
    /// The child was ended, or its engine exited: it takes nothing more.
    Closed,
    /// Writing to the engine, or stopping it, failed.
    Io { ext_err_txt: String },
}

impl From<EngineError> for ChildError {
    fn from(error: EngineError) -> ChildError {
        ChildError::Engine(error)
    }
}

impl fmt::Display for ChildError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        // An engine error's code and args are its own, so its words are too.
        angzarr_diagnostic::display(self, f)
    }
}

impl Diagnostic for ChildError {
    fn code(&self) -> &str {
        match self {
            ChildError::Engine(error) => error.code(),
            ChildError::NotStarted { .. } => code!("child-not-started"),
            ChildError::Closed => code!("child-closed"),
            ChildError::Io { .. } => code!("child-io"),
        }
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        match self {
            ChildError::Engine(error) => error.args(),
            ChildError::NotStarted {
                program,
                ext_err_txt,
                ..
            } => vec![
                ("program", program.as_str().into()),
                ("ext_err_txt", ext_err_txt.as_str().into()),
            ],
            ChildError::Closed => Vec::new(),
            ChildError::Io { ext_err_txt } => vec![("ext_err_txt", ext_err_txt.as_str().into())],
        }
    }
}

/// One value of every error of this module, for the catalog test.
#[cfg(any(test, feature = "test-support"))]
pub(crate) fn diagnostic_samples() -> Vec<Box<dyn Diagnostic>> {
    vec![
        Box::new(ChildError::NotStarted {
            program: "claude".to_string(),
            deterministic: true,
            ext_err_txt: "No such file or directory (os error 2)".to_string(),
        }),
        Box::new(ChildError::Closed),
        Box::new(ChildError::Io {
            ext_err_txt: "Broken pipe (os error 32)".to_string(),
        }),
    ]
}

#[cfg(test)]
#[path = "child.test.rs"]
mod tests;
