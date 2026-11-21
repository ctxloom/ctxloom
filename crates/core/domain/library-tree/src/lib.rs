//! The library domain (step 10 U0; `library-domain.design.md` and its
//! AMENDMENT, "git holds the content"): what ctxloom observed of a library
//! TREE in git, and (from 10b) what it intended to change there. One stream
//! per tree (L1): a project checkout's `.ctxloom/`, named
//! `projects/{p}/checkouts/{c}/library` (`{c}` a harp in its
//! `.ctxloom/state/checkout`, L2), or the home's, `users/me/library` (L10).
//!
//! An observation is a FACT (`RecordTreeObserved`, L5), keyed by
//! [`observation_key`] (L4), and carries no sequence; the `TreeObserved` it
//! records ties each changed path's git id to the event's own sequence,
//! and [`Library`] folds `{last sequence, oid}` per path, the path's token
//! (G4 superseded). Commands (ReplaceFile, CreateDirectory, TrashPath,
//! MovePath, RunSequence, L17) are decided on evidence (L14) and record an
//! intent; nothing sends them before 10b (U3). Nothing here holds a
//! file's text, a config value or a key.

// The generated command enum: `RunSequence` carries steps and the others
// one change, so its variants differ in size; a command is built once per
// change, so the size costs nothing.
#[allow(dead_code, clippy::large_enum_variant)]
mod generated {
    include!(concat!(env!("OUT_DIR"), "/ctxloom.library.v1.domain.rs"));
}
mod aggregate;
mod key;
mod name;
mod refusal;
mod rejection;
mod staleness;
mod state;

/// The library aggregate's commands, one variant per command type.
pub type LibraryCommand = LibraryStateCommand;
/// The library aggregate's events, one variant per event type.
pub type LibraryEvent = LibraryStateEvent;

pub use aggregate::{LibraryAggregate, FOREIGN_OUTSIDE_WRITER, FOREIGN_WRITERS, TREE_UNREADABLE};
pub use generated::*;
pub use key::{content_id, listing_book, listing_key, observation_book, observation_key};
pub use name::{kind_of, tree_root, TreeName, CHECKOUT_FILE, HOME_TREE};
pub use refusal::NotOurs;
#[cfg(any(test, feature = "test-support"))]
pub use rejection::diagnostic_samples;
pub use rejection::{LibraryTreeRejection, EN_US};
pub use staleness::{judge, Judged};
pub use state::{cause_of, Library, Resource, Slot, KEEP_DAYS, LIBRARY_DOMAIN};

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
