//! What a launch is made from: the engine, the assembled context, the prompt
//! and the environment of the caller. An [`Order`] is READ from the stream
//! (`crate::record::order_of`: the run's recorded spec and its session's
//! events), never handed to the launcher; it is a value, not a table.

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
    /// The user's own home, as the composition root resolved it (the
    /// platform's home variable); none when unset.
    pub user_home: Option<String>,
    /// The live project: where the turn's command runs, unless the
    /// workspace axis places it in a checkout of its own (`workspace`).
    pub cwd: Option<String>,
    /// What the run asks of the workspace axis; the launcher places the
    /// engine (`crate::place_workspace`).
    pub workspace: crate::Workspace,
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
    /// What the launch carries besides the context, from the run's
    /// selection (`DryRun.surfaces`).
    pub surfaces: run_ports::LaunchSurfaces,
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
            .field("user_home", &self.user_home)
            .field("cwd", &self.cwd)
            .field("workspace", &self.workspace)
            .field("interactive", &self.interactive)
            .field("model", &self.model)
            .field("effort", &self.effort)
            .field("permissions", &self.permissions)
            .field("settings", &self.settings)
            .field("owned", &self.owned)
            .field("git_user", &self.git_user)
            .field("resume", &self.resume)
            .field("surfaces", &self.surfaces)
            .finish()
    }
}

/// The runs this process enqueued and has yet to start, by session id text
/// and incarnation, with the run's own lock when this process holds it
/// (rulings 86, 92). A run enqueued here is launched here, once the record
/// says it may be (its mint and loadout); one not enqueued here is another
/// process's and is left to the sweep. The lock is the process's own file
/// lock, not a message: it lives until the runner is started. This is the
/// lease P5 replaces with recorded facts.
#[derive(Debug, Default)]
pub struct RunLockTable {
    held: Mutex<BTreeMap<(String, u32), Option<run_ports::HeldLock>>>,
}

impl RunLockTable {
    pub fn new() -> RunLockTable {
        RunLockTable::default()
    }

    /// This process enqueued `incarnation` of `session`: it holds `lock`,
    /// the run's own lock, when it took one (a terminal's is its
    /// frontend's), until [`RunLockTable::release`]: the launcher lets it go
    /// once the runner is started, or the launch failed.
    pub fn claim(&self, session: &str, incarnation: u32, lock: Option<run_ports::HeldLock>) {
        self.held
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
            .insert((session.to_string(), incarnation), lock);
    }

    /// Let go of the run lock held for `incarnation` of `session`, if any.
    pub fn release(&self, session: &str, incarnation: u32) {
        let held = self
            .held
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
            .remove(&(session.to_string(), incarnation));
        drop(held);
    }

    /// Whether this process enqueued `incarnation` of `session` and has yet
    /// to start it.
    pub fn holds(&self, session: &str, incarnation: u32) -> bool {
        self.held
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
            .contains_key(&(session.to_string(), incarnation))
    }
}

#[cfg(test)]
#[path = "order.test.rs"]
mod tests;
