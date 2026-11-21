//! The live frontends of a ctxloom home (owner, 2026-11-15): each
//! interactive session's frontend holds `<home>/.ctxloom/frontends/
//! <pid>.lock` for its life, so `ctxloom shutdown` can find every one, the
//! daemon's or none's (a frontend outlives its daemon), and ask it to stop.
//! The ask is a file, `<pid>.stop` beside the lock, which the frontend
//! watches: no signal and no process id is trusted, only the lock (a dead
//! frontend's reads free, and its files are taken away by the next look).
//!
//! What its human is TOLD before that (owner, 2026-11-15, OD-63: a drain
//! tells each live TUI at once, and asks it to stop only when the drain's
//! time ends) is a second file beside it, `<pid>.notice`
//! ([`FrontendNotice`]): its text, which the frontend shows on its terminal,
//! and when, if at all, the frontend stops itself (`stop_at`, seconds since
//! the epoch: a SIGTERM's drain, whose daemon may exit before its time
//! ends). A later notice replaces it.

use std::path::PathBuf;
use std::sync::Arc;

use file_system::{Fs, Perms};
use os::Mode;
use session_ports::{LockError, LockState};

/// The directory under ctxloom's home the frontends' locks are in.
const DIR: &str = "frontends";

/// The live frontends' locks under ctxloom's home.
pub struct FrontendLocks {
    fs: Arc<dyn Fs>,
    home: PathBuf,
}

impl FrontendLocks {
    /// The frontends of ctxloom's home `home`, through `fs`.
    pub fn new(fs: Arc<dyn Fs>, home: impl Into<PathBuf>) -> FrontendLocks {
        FrontendLocks {
            fs,
            home: home.into(),
        }
    }

    fn dir(&self) -> PathBuf {
        self.home.join(".ctxloom").join(DIR)
    }

    /// Where the frontend `pid`'s lock is.
    pub fn lock_path(&self, pid: u32) -> PathBuf {
        self.dir().join(format!("{pid}.lock"))
    }

    /// Where a stop is asked of the frontend `pid`.
    pub fn stop_path(&self, pid: u32) -> PathBuf {
        self.dir().join(format!("{pid}.stop"))
    }

    /// Where the frontend `pid` is told something for its human.
    pub fn notice_path(&self, pid: u32) -> PathBuf {
        self.dir().join(format!("{pid}.notice"))
    }

    /// Hold the frontend `pid`'s lock for its life; a stop asked of an
    /// earlier process of its id is taken away first.
    pub fn hold(&self, pid: u32) -> Result<FrontendHeld, LockError> {
        let _ = self.fs.remove_file(&self.stop_path(pid));
        let _ = self.fs.remove_file(&self.notice_path(pid));
        let locked = crate::take(&*self.fs, &self.lock_path(pid))?;
        Ok(FrontendHeld {
            fs: Arc::clone(&self.fs),
            lock: self.lock_path(pid),
            stop: self.stop_path(pid),
            notice: self.notice_path(pid),
            locked: Some(locked),
        })
    }

    /// The process ids of the frontends alive now, in order; a dead one's
    /// files are taken away.
    pub fn live(&self) -> Vec<u32> {
        let Ok(entries) = self.fs.read_dir(&self.dir()) else {
            return Vec::new();
        };
        let mut pids: Vec<u32> = entries
            .iter()
            .filter_map(|entry| {
                let name = entry.name.to_str()?;
                name.strip_suffix(".lock")?.parse().ok()
            })
            .filter(|pid| match crate::probe(&*self.fs, &self.lock_path(*pid)) {
                LockState::Held => true,
                LockState::Free => {
                    let _ = self.fs.remove_file(&self.lock_path(*pid));
                    let _ = self.fs.remove_file(&self.stop_path(*pid));
                    let _ = self.fs.remove_file(&self.notice_path(*pid));
                    false
                }
                LockState::Unproven => false,
            })
            .collect();
        pids.sort_unstable();
        pids
    }

    /// Ask the frontend `pid` to stop: its stop file written.
    pub fn ask_to_stop(&self, pid: u32) -> std::io::Result<()> {
        self.fs.write_atomic(
            &self.stop_path(pid),
            b"stop\n",
            Perms::Exact(Mode::from_bits(0o600)),
        )
    }

    /// Whether a stop was asked of the frontend `pid`.
    pub fn stop_asked(&self, pid: u32) -> bool {
        self.fs.exists(&self.stop_path(pid)).unwrap_or(false)
    }

    /// Tell the frontend `pid` `notice`: its notice file written, replacing
    /// any earlier one.
    pub fn tell(&self, pid: u32, notice: &FrontendNotice) -> std::io::Result<()> {
        self.fs.write_atomic(
            &self.notice_path(pid),
            notice.encode().as_bytes(),
            Perms::Exact(Mode::from_bits(0o600)),
        )
    }

    /// What the frontend `pid` was told last; none when nothing was, or the
    /// file does not read as a notice.
    pub fn notice(&self, pid: u32) -> Option<FrontendNotice> {
        let bytes = self.fs.read(&self.notice_path(pid)).ok()?;
        FrontendNotice::decode(std::str::from_utf8(&bytes).ok()?)
    }
}

/// What a frontend is told for its human: the text it shows, and when (in
/// seconds since the epoch) it stops itself, if at all.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct FrontendNotice {
    pub text: String,
    pub stop_at: Option<u64>,
}

impl FrontendNotice {
    /// Its file: `stop_at: <seconds>` (or `none`), then the text.
    fn encode(&self) -> String {
        let at = self
            .stop_at
            .map_or_else(|| "none".to_string(), |at| at.to_string());
        format!("stop_at: {at}\n{}\n", self.text)
    }

    fn decode(file: &str) -> Option<FrontendNotice> {
        let (first, text) = file.split_once('\n')?;
        let stop_at = match first.strip_prefix("stop_at: ")? {
            "none" => None,
            at => Some(at.parse().ok()?),
        };
        Some(FrontendNotice {
            text: text.trim_end_matches('\n').to_string(),
            stop_at,
        })
    }
}

/// A frontend's lock held: its stop, notice and lock files are taken away,
/// then the lock let go, when it is dropped.
pub struct FrontendHeld {
    fs: Arc<dyn Fs>,
    lock: PathBuf,
    stop: PathBuf,
    notice: PathBuf,
    locked: Option<file_lock::Locked>,
}

impl Drop for FrontendHeld {
    fn drop(&mut self) {
        let _ = self.fs.remove_file(&self.stop);
        let _ = self.fs.remove_file(&self.notice);
        let _ = self.fs.remove_file(&self.lock);
        drop(self.locked.take());
    }
}

#[cfg(test)]
#[path = "frontends.test.rs"]
mod tests;
