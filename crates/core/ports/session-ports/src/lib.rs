//! The ports the sessions application needs from outside, beyond taskloom's
//! `ports` (which project a call is about, the clock, the delay, command ids)
//! and angzarr-ports' stores. Each is a trait a driven adapter implements;
//! nothing here does I/O.
//!
//! - [`ProjectStores`]: a project's ctxloom store, opened where its project
//!   says (`~/.ctxloom/projects/<id>/ctxloom.db`, D26).
//! - [`Liveness`]: whether a session's owner is running, asked of its lock.
//! - [`SessionFiles`]: a session's directory (its session home, the engine's
//!   native history): no transcript file is in it (owner ruling 87a: the
//!   transcript is read from the session's events).
//! - [`SessionLock`]: the session's liveness lock, held by its run.
//! - [`GoSessions`]: Go's `~/.ctxloom/sessions/<harp>/session.yaml`, read by
//!   `session import` alone.

use std::future::Future;

use angzarr_diagnostic::Report;
use angzarr_embedded::{Processed, Purge, ReadStore, Store, StoreError};
use ports::Project;

/// An instant, as every recorded event carries one.
pub use angzarr_embedded::Timestamp;

/// A project's ctxloom store, opened where its project says.
pub trait ProjectStores {
    /// A project's store: the base ports, one-moment reads, the positions it
    /// holds, listed (what a catch-up reads when it starts), and the purge a
    /// session's destroy runs (owner ruling 94b).
    type Store: Store + ReadStore + Processed + Purge + Send + Sync;

    /// The project's store, to write: opened (and created) on first use and
    /// held after.
    fn open(
        &mut self,
        project: &Project,
    ) -> impl Future<Output = Result<&Self::Store, StoreError>> + Send;

    /// The project's store, to read: `None` if it has none yet, and nothing
    /// is created. A store already held for a write serves it too.
    fn open_existing(
        &mut self,
        project: &Project,
    ) -> impl Future<Output = Result<Option<&Self::Store>, StoreError>> + Send;
}

/// What a session's liveness lock says: the lock and nothing else, never the
/// session's end stamp (Go: `sessionlock.Inspect`).
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum LockState {
    /// The lock is held by a running process.
    Held,
    /// The lock file is there and nothing holds it: the owner ended.
    Free,
    /// No lock file, an unreadable one, or a filesystem whose locks cannot be
    /// trusted: the lock proves nothing, and is never read as free.
    Unproven,
}

/// A session's liveness, asked of its lock without holding anything.
///
/// Synchronous, and it touches the filesystem, as the project identity does.
pub trait Liveness {
    fn lock_state(&mut self, harp: &str) -> LockState;
}

/// A session's liveness lock, HELD: what a run takes for its life so its
/// session reads live (defect 1: no production code took it, so every
/// session read unknown). Let go when dropped, however the run ends; the
/// kernel lets go of it however the process ends.
pub struct Held(#[allow(dead_code)] Box<dyn Send>);

impl Held {
    /// A held lock, let go when `guard` is dropped.
    pub fn new(guard: impl Send + 'static) -> Held {
        Held(Box::new(guard))
    }
}

impl std::fmt::Debug for Held {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str("Held")
    }
}

/// Taking a session's liveness lock (the one [`Liveness`] probes), to hold.
///
/// Synchronous, and it touches the filesystem, as the probe does.
pub trait SessionLock: Send {
    /// Take `harp`'s lock and hold it until the answer is dropped. Refused at
    /// once, never waited for, when another process holds it.
    fn hold(&mut self, harp: &str) -> Result<Held, Report>;
}

/// One file under a session's directory.
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct FileItem {
    /// Absolute path.
    pub path: String,
    /// Relative to the session's directory, slash-separated.
    pub rel: String,
    pub bytes: u64,
}

/// A session's files: its directory, `<project>/.ctxloom/sessions/<harp>/`,
/// and the lock beside it. No transcript file is among them (owner ruling
/// 87a).
///
/// Synchronous: file I/O through the file-system seam.
pub trait SessionFiles {
    /// When `harp`'s engine last wrote its native history: the newest mtime
    /// of a file under it (directories and symlinks are not the
    /// conversation). `None` when it holds nothing. The session's own
    /// capture is in its events (`SessionRecord::last_captured`).
    fn last_active(&mut self, harp: &str) -> Result<Option<Timestamp>, Report>;

    /// Every regular file under `harp`'s directory that a remove could name,
    /// sorted by `rel`: the persisted members (native history and anything
    /// authored), never the ephemeral ones (the engine homes, the locks, the
    /// worktrees and the scratch).
    fn survey(&mut self, harp: &str) -> Result<Vec<FileItem>, Report>;

    /// Remove the files at `rels` under `harp`'s directory; the bytes freed.
    /// A file already gone is not a failure.
    fn destroy(&mut self, harp: &str, rels: &[String]) -> Result<u64, Report>;

    /// Move a session's output directory from `from` to `to` (a rename
    /// moves it beside itself, to the new name): `false` when there is
    /// nothing at `from` to move. A move, never a copy then a delete, and
    /// refused whole when something is already at `to`.
    fn move_output(&mut self, from: &str, to: &str) -> Result<bool, Report>;

    /// Move the session directory of `from` (a harp), and its lock file
    /// beside it, to `to`'s (defect 2: a rename leaves no directory behind
    /// for a later session to claim): `false` when there is no directory at
    /// `from`. A move, never a copy; refused whole when something is already
    /// at `to`.
    fn move_session(&mut self, from: &str, to: &str) -> Result<bool, Report>;

    /// Remove `harp`'s session directory whole, and its lock file beside it
    /// (owner ruling 94b: a destroy leaves nothing of the session); the bytes
    /// freed. Nothing there is not a failure.
    fn destroy_session(&mut self, harp: &str) -> Result<u64, Report>;
}

/// One of Go's session directories, as `session import` reads it.
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct GoSession {
    /// The directory's name: the harp.
    pub harp: String,
    /// The sidecar's text (`session.yaml`), or why it could not be read: one
    /// unreadable session must not hide the others.
    pub sidecar: Result<String, Report>,
    /// Whether the directory held a `keep` marker file.
    pub keep_marker: bool,
}

/// Go's session directories under a home, read-only. NEVER the real
/// `~/.ctxloom` in a test.
pub trait GoSessions {
    /// Every real directory of the sessions root that carries a regular
    /// sidecar, by directory name, whatever the name: the reader judges the
    /// file system's shape, never the name. Whether a name is a usable harp
    /// is the domain's rule, which the import applies to what this returns.
    fn go_sessions(&mut self) -> Result<Vec<GoSession>, Report>;
}

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
