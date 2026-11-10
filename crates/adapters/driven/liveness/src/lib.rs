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

use std::path::PathBuf;
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
        let path = self.path(harp);
        // Only a regular file is a lock; one a probe cannot see is no proof.
        if !self
            .fs
            .metadata(&path)
            .is_ok_and(|meta| meta.kind == Kind::File)
        {
            return LockState::Unproven;
        }
        // Read-only: a lock needs no write access, and the open never creates.
        let Ok(file) = self.fs.open(&path, Open::Read) else {
            return LockState::Unproven;
        };
        // The lock taken is let go when `file` is dropped, with this answer.
        match self.fs.os().try_lock(&file, LockKind::Exclusive) {
            Ok(true) => LockState::Free,
            Ok(false) => LockState::Held,
            Err(_) => LockState::Unproven,
        }
    }
}

/// The lock a run holds for its life (defect 1), taken with the platform's
/// `file-lock` (the one way every ctxloom binary takes a lock).
///
/// RED (defect 1): a stub that holds nothing.
impl SessionLock for LockFiles {
    fn hold(&mut self, harp: &str) -> Result<Held, Report> {
        let _ = self.path(harp);
        Ok(Held::new(()))
    }
}

/// This host's processes, read through the file-system seam from `/proc`
/// (Linux): a process's start is the `starttime` of `/proc/<pid>/stat`
/// (ruling 86). Elsewhere every probe is unproven, never gone.
///
/// RED (ruling 86): a stub that knows no process.
pub struct ProcFiles {
    fs: Arc<dyn Fs>,
}

impl ProcFiles {
    pub fn new(fs: Arc<dyn Fs>) -> ProcFiles {
        ProcFiles { fs }
    }
}

impl run_ports::Processes for ProcFiles {
    fn current(&self) -> run_ports::ProcessIdentity {
        let _ = &self.fs;
        run_ports::ProcessIdentity::default()
    }

    fn state(&self, who: &run_ports::ProcessIdentity) -> run_ports::ProcessState {
        let _ = who;
        run_ports::ProcessState::Unproven
    }
}

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
