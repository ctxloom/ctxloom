//! A session's liveness, asked of its lock. Go: `internal/shared/sessionlock`
//! (release/0.7).
//!
//! The signal is an EXCLUSIVE FILE LOCK that the session-owning process holds
//! on `<project>/.ctxloom/sessions/<harp>.lock` for as long as it runs, BESIDE the
//! session directory and never inside it. A probe tries the lock:
//!
//! - it acquires it: the owner is DEAD, the kernel having released the lock
//!   however the owner ended (a kill, a crash, power loss): [`LockState::Free`];
//! - it fails: the owner is ALIVE: [`LockState::Held`];
//! - anything else (no lock file, an unreadable one, an error) is
//!   [`LockState::Unproven`], never read as free.
//!
//! A probe never creates a lock file (one it minted would be unlocked and
//! read as free on the next probe), and holds nothing past its answer.
//!
//! The run holds its session's lock for its life ([`SessionLock`], defect 1),
//! taken with the platform's `file-lock`, at once or not at all. A run's own
//! lock, `<home>/.ctxloom/runs/<run>.lock` ([`RunLockFiles`], rulings 86, 92,
//! 93), is held by its enqueuer until its runner is started; its file is
//! removed when it is let go, so it is a dead holder's alone that reads free.

use std::path::{Path, PathBuf};
use std::sync::Arc;

use angzarr_diagnostic::Report;
use file_system::{Fs, Kind, Open};
use os::LockKind;
use session_ports::{Held, Liveness, LockState, SessionLock};

/// Liveness over the lock files under one project.
pub struct LockFiles {
    fs: Arc<dyn Fs>,
    project: PathBuf,
}

impl LockFiles {
    /// The locks under the project directory `project`, read through `fs`.
    pub fn new(fs: Arc<dyn Fs>, project: impl Into<PathBuf>) -> LockFiles {
        LockFiles {
            fs,
            project: project.into(),
        }
    }

    /// Where `harp`'s lock is: `<project>/.ctxloom/sessions/<harp>.lock`.
    pub fn path(&self, harp: &str) -> PathBuf {
        self.project
            .join(".ctxloom")
            .join("sessions")
            .join(format!("{harp}.lock"))
    }
}

impl Liveness for LockFiles {
    fn lock_state(&mut self, harp: &str) -> LockState {
        probe(&*self.fs, &self.path(harp))
    }
}

/// What the lock at `path` says, asked without holding it.
fn probe(fs: &dyn Fs, path: &Path) -> LockState {
    // Only a regular file is a lock; one a probe cannot see is no proof.
    if !fs.metadata(path).is_ok_and(|meta| meta.kind == Kind::File) {
        return LockState::Unproven;
    }
    // Read-only: a lock needs no write access, and the open never creates.
    let Ok(file) = fs.open(path, Open::Read) else {
        return LockState::Unproven;
    };
    // The lock taken is let go when `file` is dropped, with this answer.
    match fs.os().try_lock(&file, LockKind::Exclusive) {
        Ok(true) => LockState::Free,
        Ok(false) => LockState::Held,
        Err(_) => LockState::Unproven,
    }
}

/// Take the lock at `path`, at once or not at all: one another holds is
/// refused, never waited for.
fn take(fs: &dyn Fs, path: &Path) -> Result<file_lock::Locked, Report> {
    let path_text = path.display().to_string();
    let mut once = move || -> Result<(), file_lock::LockError> {
        Err(file_lock::LockError::Timeout {
            path: path_text.clone(),
            waited_ms: 0,
        })
    };
    file_lock::lock(fs, path, &mut once).map_err(|e| Report::of(&e))
}

/// The lock a run holds for its life (defect 1), taken with the platform's
/// `file-lock` (the one way every ctxloom binary takes a lock).
///
impl SessionLock for LockFiles {
    fn hold(&mut self, harp: &str) -> Result<Held, Report> {
        take(&*self.fs, &self.path(harp)).map(Held::new)
    }
}

/// The run locks (rulings 86, 92, 93): a run's own lock at
/// `<home>/.ctxloom/runs/<run>.lock` (machine-local, beside the projects'
/// stores, so a refused run leaves nothing in the project), and a session's
/// lock as [`LockFiles`] has it.
///
pub struct RunLockFiles {
    fs: Arc<dyn Fs>,
    home: PathBuf,
}

impl RunLockFiles {
    /// The run locks under ctxloom's home `home`, through `fs`.
    pub fn new(fs: Arc<dyn Fs>, home: impl Into<PathBuf>) -> RunLockFiles {
        RunLockFiles {
            fs,
            home: home.into(),
        }
    }

    /// Where run `run`'s lock is.
    pub fn path(&self, run: &str) -> PathBuf {
        self.home
            .join(".ctxloom")
            .join("runs")
            .join(format!("{run}.lock"))
    }
}

/// A run's lock held: its file is removed, then the lock let go, when it is
/// dropped (so it is only a dead holder's lock file that is ever left).
struct RunHeld {
    fs: Arc<dyn Fs>,
    path: PathBuf,
    locked: Option<file_lock::Locked>,
}

impl Drop for RunHeld {
    fn drop(&mut self) {
        // Removed while still held: no other taker can lock the file between
        // the two (a taker re-checks the file is still at its path).
        let _ = self.fs.remove_file(&self.path);
        drop(self.locked.take());
    }
}

fn probe_of(state: LockState) -> run_ports::LockProbe {
    match state {
        LockState::Held => run_ports::LockProbe::Held,
        LockState::Free => run_ports::LockProbe::Free,
        LockState::Unproven => run_ports::LockProbe::Unproven,
    }
}

impl run_ports::RunLocks for RunLockFiles {
    fn hold_run(&self, run: &str) -> Result<run_ports::HeldLock, Report> {
        let path = self.path(run);
        let locked = take(&*self.fs, &path)?;
        Ok(run_ports::HeldLock::new(
            path.display().to_string(),
            RunHeld {
                fs: Arc::clone(&self.fs),
                path,
                locked: Some(locked),
            },
        ))
    }

    fn probe(&self, path: &str) -> run_ports::LockProbe {
        probe_of(probe(&*self.fs, Path::new(path)))
    }

    fn session(&self, project_dir: &str, harp: &str) -> run_ports::LockProbe {
        let locks = LockFiles::new(Arc::clone(&self.fs), project_dir);
        probe_of(probe(&*self.fs, &locks.path(harp)))
    }
}

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
