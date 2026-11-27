//! What the use cases take and answer with.

use crate::{DeadLetter, Refusal};

/// Remove a session: DESTROY it (owner rulings 94, 94b, 98; `session
/// remove`, the purge feature's): every event of its stream and of
/// each of its runs' purged from the store, its directory and lock removed.
/// Its name stays claimed (94c).
#[derive(Clone, PartialEq, Eq, Debug, Default)]
pub struct RemoveRequest {
    pub project: Option<String>,
    pub harp: String,
    /// Apply the plan (`--yes`); without it the use case only reports.
    pub yes: bool,
    /// Destroy a session whose owner is not proven dead (`--even-if-live`).
    pub even_if_live: bool,
}

/// The command that removes a session, as the person spells it.
pub const REMOVE_VERB: &str = "session remove";

/// What a file in a session's directory is to a removal (Go's purge classes):
/// both are destroyed, and the plan names which is which.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Class {
    /// The machine-written conversation.
    Machine,
    /// Anything else: written by a person, destroyed with the session.
    Authored,
}

/// One file in a removal's plan: every file of the session's directory is
/// destroyed with it.
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct PlanItem {
    pub path: String,
    pub rel: String,
    pub class: Class,
    pub bytes: u64,
}

/// The files a removal destroys: the session's whole directory.
#[derive(Clone, PartialEq, Eq, Debug, Default)]
pub struct FilePlan {
    pub destroy: Vec<PlanItem>,
    /// The bytes freed: zero on a report-only run.
    pub bytes_freed: u64,
}

/// What a removal came to: the plan, whether it was applied (the session
/// destroyed), and why it refused if it did.
#[derive(Clone, PartialEq, Debug)]
pub struct Removal {
    pub harp: String,
    pub applied: bool,
    pub files: FilePlan,
    /// How many events the destroy purged from the store (its session's and
    /// its runs'); zero on a report.
    pub events_destroyed: u64,
    pub refusal: Option<Refusal>,
    pub dead_letters: Vec<DeadLetter>,
}

impl Removal {
    /// The files a person wrote that the destroy takes, relative to the
    /// session's directory, in the plan's order (owner ruling 104: each is
    /// named in a warning before it goes).
    pub fn authored(&self) -> Vec<String> {
        self.files
            .destroy
            .iter()
            .filter(|item| item.class == Class::Authored)
            .map(|item| item.rel.clone())
            .collect()
    }
}

/// One session's transcript, captured or not: absence is a row, not an
/// omission.
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct TranscriptRow {
    pub harp: String,
    /// Whether the session's events hold any line of a transcript.
    pub captured: bool,
    /// Its transcript's size, as `session transcript show` prints it.
    pub bytes: u64,
}
