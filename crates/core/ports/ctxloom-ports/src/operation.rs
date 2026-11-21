//! One atomic operation on one of ctxloom's own trees (step 10b U8; S50,
//! D3(a), D4, L12): what a library write does to the files once its intent
//! is recorded. Each leaves the tree as it was or the change made whole:
//!
//! - [`Operation::Replace`]: one file, written under the tree's staging and
//!   renamed over its target;
//! - [`Operation::CreateDir`]: one new directory, built whole under the
//!   staging, then renamed in WITHOUT replacing anything there (NOREPLACE);
//! - [`Operation::Trash`]: one path renamed into the trash slot of its
//!   request, `state/trash/<request id>/<path>`;
//! - [`Operation::Move`]: one path renamed to another, never over one;
//! - [`Operation::Purge`]: the request's own trash slot removed whole (a
//!   sequence's last step, T28, so it leaves no trash; G12(a));
//! - [`Operation::Outside`]: ANOTHER tool's file (step 10c U10, L15), at
//!   its absolute path outside every tree, written whole (atomically,
//!   beside it, as that tool's own files always were) or removed.
//!
//! Nothing is ever staged beside its target: staging and trash are below
//! the tree's `state/` (gitignored, outside the library's pathspec). Every
//! path is relative to the tree's directory (its `.ctxloom`), `/`-separated.

use crate::{Failure, Put};

/// The tree's private working state, below its `.ctxloom`.
pub const STATE_DIR: &str = "state";

/// Where a write's files are built before they are renamed in: one
/// directory per request, `state/staging/<request id>/`. Swept when a
/// daemon starts (S50).
pub const STAGING_DIR: &str = "state/staging";

/// Where a removal's paths are kept: one slot per request,
/// `state/trash/<request id>/` (L12). Never swept at start.
pub const TRASH_DIR: &str = "state/trash";

/// One atomic operation on a tree.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Operation {
    /// The file `Put::path` written whole, replacing what is there.
    Replace(Put),
    /// The new directory `path`, holding `files` (each below it).
    CreateDir { path: String, files: Vec<Put> },
    /// The path taken into the trash slot of the request.
    Trash { path: String },
    /// `from` renamed to `to`: below this tree, or (`into`, step 10b
    /// QUESTIONS 1(a)) below ANOTHER tree, named by its directory.
    Move {
        from: String,
        to: String,
        into: Option<String>,
    },
    /// The trash slot of the request removed whole; none there is done.
    Purge,
    /// Another tool's file at the ABSOLUTE `path`: `bytes` written whole
    /// (`executable` gives it 0755), or (none) removed with the directories
    /// that leaves empty, up to and never including `keep`. What is there
    /// already is done: a re-run converges.
    Outside {
        path: String,
        bytes: Option<Vec<u8>>,
        executable: bool,
        keep: String,
    },
}

/// A point an operation reaches, where a test-support switch may stop the
/// process (atomic_writes.feature's kill points).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Point {
    /// The files are built under the staging, not yet renamed in.
    Staged,
    /// The path is in the trash slot.
    Trashed,
}

impl Point {
    /// The point's word, as the `test-die-at` switch names it.
    pub fn word(self) -> &'static str {
        match self {
            Point::Staged => "staged",
            Point::Trashed => "trashed",
        }
    }
}

/// What an operation came to, when it did not fail.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Operated {
    /// The change is in the tree (made now, or already by an earlier try
    /// of the same request).
    Done,
    /// Something is at the path the operation would create (a new
    /// directory's, a move's target): nothing was changed.
    Exists { path: String },
    /// Nothing is at the path the operation would take or move: nothing
    /// was changed.
    Missing { path: String },
}

/// The atomic operations on ctxloom's trees, each tree named by its
/// directory (its `.ctxloom`).
pub trait Operations: Send + Sync {
    /// Carry out `operation` in `tree` for the request `request_id` (its
    /// staging and its trash slot are the request's), telling `at` each
    /// point it reaches. A request's earlier staging is dropped first, so
    /// a retry builds afresh.
    fn operate(
        &self,
        tree: &str,
        request_id: &str,
        operation: &Operation,
        at: &dyn Fn(Point),
    ) -> Result<Operated, Failure>;

    /// Drop everything staged in `tree` (a daemon's start: what a killed
    /// write left there).
    fn sweep_staging(&self, tree: &str) -> Result<(), Failure>;

    /// Drop the trash slot of `request_id` in `tree`, whole; none there is
    /// done.
    fn purge_slot(&self, tree: &str, request_id: &str) -> Result<(), Failure>;
}

#[cfg(test)]
#[path = "operation.test.rs"]
mod tests;
