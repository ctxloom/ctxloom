//! The login-link port: how the human's primary session is handed her own
//! login (owner ruling 2026-11-08, item 8).
//!
//! The file an engine keeps its login in is NEVER read, copied or printed by
//! ctxloom: the only things this port can say of it are whether it is there,
//! and the only thing it can do is make a session home's file a link to it.

use crate::RecordsError;

/// What is at a path of a session's directory, a link not followed.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum LinkState {
    /// Nothing.
    Absent,
    /// A link, to this target as it was made.
    LinkTo(String),
    /// Anything that is not a link.
    Other,
}

/// A session's directory, for the one link it may hold.
pub trait LoginLinks: Send {
    /// Whether a file is at the absolute `path`, links followed (a dangling
    /// link is none). Its contents are never read.
    fn present(&self, path: &str) -> bool;

    /// What is at `rel` of `harp`'s directory, a link not followed.
    fn state(&self, harp: &str, rel: &str) -> LinkState;

    /// Make `rel` of `harp`'s directory a link to the absolute `target`,
    /// creating the directories above it. It replaces nothing.
    fn link(&mut self, harp: &str, rel: &str, target: &str) -> Result<(), RecordsError>;

    /// Remove the link at `rel` of `harp`'s directory, itself and never what
    /// it points at. Anything there that is not a link is refused.
    fn unlink(&mut self, harp: &str, rel: &str) -> Result<(), RecordsError>;

    /// The absolute path of `harp`'s directory.
    fn dir(&self, harp: &str) -> String;
}
