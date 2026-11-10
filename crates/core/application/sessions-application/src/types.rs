//! What the use cases take and answer with.

use crate::{DeadLetter, Refusal, State};

/// Which sessions a listing spans.
#[derive(Clone, PartialEq, Eq, Debug, Default)]
pub struct ListQuery {
    /// An explicit project id, overriding resolution (`--project`).
    pub project: Option<String>,
    /// Every project's sessions (`--all`), not just the current project's.
    pub all: bool,
    /// Only sessions in this state, from their locks.
    pub state: Option<State>,
}

/// A search: the listing's sessions that match every word.
#[derive(Clone, PartialEq, Eq, Debug, Default)]
pub struct SearchQuery {
    pub list: ListQuery,
    pub words: Vec<String>,
}

/// One session, by its harp.
#[derive(Clone, PartialEq, Eq, Debug, Default)]
pub struct ShowQuery {
    pub project: Option<String>,
    pub harp: String,
}

/// Mark a session's transcript kept, or release it (`--undo`).
#[derive(Clone, PartialEq, Eq, Debug, Default)]
pub struct KeepRequest {
    pub project: Option<String>,
    pub harp: String,
    /// The mark to leave: `true` keeps, `false` is `--undo`.
    pub keep: bool,
}

/// What a keep did.
#[derive(Clone, PartialEq, Debug)]
pub struct Kept {
    pub harp: String,
    /// The mark the session holds now.
    pub kept: bool,
    /// Whether it changed: an unchanged mark records nothing.
    pub changed: bool,
    pub dead_letters: Vec<DeadLetter>,
}

/// Remove a session: DESTROY it (owner rulings 94, 94b, 98; `session
/// remove`, the session-destroy feature's): every event of its stream and of
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

/// Rename a session (`session edit --name`).
#[derive(Clone, PartialEq, Eq, Debug, Default)]
pub struct RenameRequest {
    pub project: Option<String>,
    pub harp: String,
    pub to: String,
}

/// What a rename did.
#[derive(Clone, PartialEq, Debug)]
pub struct Renamed {
    pub from: String,
    pub to: String,
    pub output_dir: String,
    pub dead_letters: Vec<DeadLetter>,
}

/// Which sessions' transcripts a listing names.
#[derive(Clone, PartialEq, Eq, Debug, Default)]
pub struct TranscriptQuery {
    pub project: Option<String>,
    pub all: bool,
    /// Only this session; an unknown one is an error, not an empty listing.
    pub harp: Option<String>,
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

/// The engine's session and transcript a harp is bound to (the SessionStart
/// hook).
#[derive(Clone, PartialEq, Eq, Debug, Default)]
pub struct BindRequest {
    pub project: Option<String>,
    pub harp: String,
    pub native_id: String,
    pub transcript_path: String,
}

/// What a bind did.
#[derive(Clone, PartialEq, Debug)]
pub enum Bound {
    /// The binding changed.
    Recorded { dead_letters: Vec<DeadLetter> },
    /// A repeat, or a bind that displaces nothing: nothing recorded.
    Unchanged,
    /// No session of that harp: nothing to bind, and not a failure (Go:
    /// `operations.BindSession` no-ops a harp absent from the index).
    NoSuchSession,
}
