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
    /// The agent's permissions document for the engine, JSON text in the
    /// order it was written (owner rulings 135, 137: the engine's own
    /// document, handed to it verbatim, never checked); none, no document.
    pub permissions: Option<String>,
    /// How the config says the engine is run: program, session home,
    /// credential source, environment passthrough (names and paths only).
    pub settings: run_ports::EngineSettings,
    /// The names the run itself set in `env` for the engine (owner ruling
    /// 78b): no permit replaces them (`run_ports::apply_permits`).
    pub owned: Vec<String>,
    /// The user's git identity, seeded into the session home's `.gitconfig`
    /// when it has none (owner ruling 112: the engine's HOME is the session
    /// home); none known, nothing is seeded.
    pub git_user: Option<run_ports::GitUser>,
    /// The engine's own session the launch resumes (owner ruling 115, `run
    /// --session`); none, the engine starts one.
    pub resume: Option<String>,
}

impl fmt::Debug for Order {
    /// The environment's names alone: an order is logged, no value of it is.
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
            .field("permissions", &self.permissions)
            .field("settings", &self.settings)
            .field("owned", &self.owned)
            .field("git_user", &self.git_user)
            .field("resume", &self.resume)
            .finish()
    }
}

/// The orders placed and not yet taken, by session id text and incarnation,
/// and the run locks held for them (rulings 86, 92).
#[derive(Debug, Default)]
pub struct Orders {
    placed: Mutex<BTreeMap<(String, u32), Order>>,
    locks: Mutex<BTreeMap<(String, u32), run_ports::HeldLock>>,
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

    /// Hold `lock`, the run's own lock, for `incarnation` of `session`, until
    /// [`Orders::release`]: the launcher lets it go once the runner is
    /// started, or the launch failed.
    pub fn hold(&self, session: &str, incarnation: u32, lock: run_ports::HeldLock) {
        self.locks
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
            .insert((session.to_string(), incarnation), lock);
    }

    /// Let go of the run lock held for `incarnation` of `session`, if any.
    pub fn release(&self, session: &str, incarnation: u32) {
        let held = self
            .locks
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
            .remove(&(session.to_string(), incarnation));
        drop(held);
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
