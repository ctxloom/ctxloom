//! The locks a run's owner is judged by (owner rulings 86, 92, 93): no pid,
//! no process table, the host's advisory file locks alone, as a session's
//! liveness is (they work on every platform the platform's `file-lock` does).
//!
//! - A run's own lock, which the process that enqueues it holds from before
//!   the enqueue is visible until its runner is started: a run still owed a
//!   runner whose lock is free has lost its launcher (an orphan).
//! - A session's lock (`<project>/.ctxloom/sessions/<harp>.lock`), which the
//!   run holds from its mint until its end: a STARTED run whose session's
//!   lock is free has lost its owner too.
//!
//! Synchronous: a probe of the filesystem, as a session's liveness is.

use std::fmt;

pub use session_ports::LockError;

/// What a probe of a lock says.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum LockProbe {
    /// A running process holds it.
    Held,
    /// The lock file is there and nothing holds it: its holder ended.
    Free,
    /// No lock file, or one that cannot be tried: proves nothing, and is
    /// never read as free.
    Unproven,
}

/// A lock held: let go (and its file removed, for a run's) when dropped.
pub struct HeldLock {
    path: String,
    _guard: Box<dyn Send>,
}

impl HeldLock {
    /// The lock at `path`, held until `guard` is dropped.
    pub fn new(path: String, guard: impl Send + 'static) -> HeldLock {
        HeldLock {
            path,
            _guard: Box::new(guard),
        }
    }

    /// Its file's absolute path: what a run's enqueue journals.
    pub fn path(&self) -> &str {
        &self.path
    }
}

impl fmt::Debug for HeldLock {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("HeldLock")
            .field("path", &self.path)
            .finish()
    }
}

/// The run and session locks of this host.
pub trait RunLocks: Send + Sync {
    /// Take run `run`'s own lock (its id's text), at once or not at all.
    fn hold_run(&self, run: &str) -> Result<HeldLock, LockError>;

    /// What the lock at `path` (a run's, as its enqueue journaled it) says.
    fn probe(&self, path: &str) -> LockProbe;

    /// What the lock of session `harp`, in the project at `project_dir`,
    /// says.
    fn session(&self, project_dir: &str, harp: &str) -> LockProbe;
}

#[cfg(test)]
#[path = "locks.test.rs"]
mod tests;
