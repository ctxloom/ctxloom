//! What a launch is told that the run's events do not say: the engine, the
//! assembled context, the prompt and the environment of the caller. In one
//! process the caller that enqueued the run places the order; the launcher,
//! finding the run owed a runner, takes it. A run with no order here is
//! another process's, and is left.

use std::collections::BTreeMap;
use std::fmt;
use std::sync::{Mutex, PoisonError};

/// One launch's inputs.
#[derive(Clone, PartialEq, Eq)]
pub struct Order {
    /// The session's name, the directory it is recorded in.
    pub harp: String,
    /// The engine that drives the turn, by name.
    pub engine: String,
    /// The assembled context the engine is handed.
    pub context: String,
    pub prompt: String,
    /// The caller's environment: the engine passes on only what it allows.
    pub env: BTreeMap<String, String>,
    /// Where the turn's command runs.
    pub cwd: Option<String>,
    /// The human's terminal session, not a turn: the engine's command is
    /// given her terminal and the prompt may be empty.
    pub interactive: bool,
    /// The agent's model; empty names none (the engine's own default).
    pub model: String,
    /// The agent's effort level; empty names none.
    pub effort: String,
    /// How the config says the engine is run: program, session home,
    /// credential source, environment passthrough (names and paths only).
    pub settings: run_ports::EngineSettings,
}

impl fmt::Debug for Order {
    /// Without the bearer's value: an order is logged, the secret is not.
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Order")
            .field("harp", &self.harp)
            .field("engine", &self.engine)
            .field("context", &self.context)
            .field("prompt", &self.prompt)
            .field("env", &run_ports::RedactedEnv(&self.env))
            .field("cwd", &self.cwd)
            .field("interactive", &self.interactive)
            .field("model", &self.model)
            .field("effort", &self.effort)
            .field("settings", &self.settings)
            .finish()
    }
}

/// The orders placed and not yet taken, by session id text and incarnation.
#[derive(Debug, Default)]
pub struct Orders {
    placed: Mutex<BTreeMap<(String, u32), Order>>,
}

impl Orders {
    pub fn new() -> Orders {
        Orders::default()
    }

    /// Place the order for `incarnation` of `session`, replacing one placed.
    pub fn place(&self, session: &str, incarnation: u32, order: Order) {
        self.placed
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
            .insert((session.to_string(), incarnation), order);
    }

    /// Take the order, once: the launch that has it is the one that runs it.
    pub fn take(&self, session: &str, incarnation: u32) -> Option<Order> {
        self.placed
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
            .remove(&(session.to_string(), incarnation))
    }
}

#[cfg(test)]
#[path = "orders.test.rs"]
mod tests;
