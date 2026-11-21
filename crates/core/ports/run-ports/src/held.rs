//! A session's HOLD (s4-Q14, H1): the capture facts a hook keeps, in order,
//! because no daemon up to date for it took them, until a ctxloom process
//! of an up-to-date version delivers them and cuts what it delivered.
//!
//! The port says nothing of WHO holds or delivers (owner, H-Q5 revised:
//! the client side, any process of an up-to-date version, pushes a hold
//! over the API; the daemon reads no hold file, but for the ONE named
//! exception of OD-59 (2): its start-up sweep flushes a judged run's
//! session's hold, `daemon_application::StartHolds`). It is the hold alone:
//!
//! - [`HeldFacts::hold`] appends one fact, under the session's short
//!   APPEND lock, within the session's and the home's bound; past either,
//!   the NEWEST fact is dropped ([`Held::Full`]) and a gap recorded in its
//!   place, every earlier fact kept;
//! - [`HeldFacts::take`] snapshots the hold in order under the session's
//!   FLUSH lock, taken at once or not at all: of several processes taking
//!   one hold at once exactly one gets a [`Batch`], the others `None`
//!   (skip, never wait). The batch keeps the flush lock until it is
//!   settled or dropped, so nothing else cuts the hold meanwhile;
//! - [`Batch::cut`] (or [`Batch::settle`]) removes what was delivered,
//!   keeping anything appended since the snapshot. A batch dropped unsettled
//!   (a crash between delivering and cutting) changes nothing: the next
//!   taker delivers the same facts again, each a DUPLICATE by its external
//!   id;
//! - [`HeldFacts::discard`] removes a session's hold (its destroy);
//!   [`HeldFacts::sessions`] and [`HeldFacts::survey`] read what is held.
//!
//! A hold is `<ctxloom home>/.ctxloom/held/<session root>.held` ([`HELD_DIR`]):
//! 0600 files in a 0700 directory, on Windows the profile's ACL (M8), as
//! sensitive as the store. Nothing here renders a held book.

use std::fmt;
use std::time::Duration;

use angzarr_diagnostic::{code, Arg, Diagnostic};
use angzarr_embedded::v1::CommandBook;
use daemon_proto::google::protobuf::Timestamp;
use prost::Message;
use uuid::Uuid;

use crate::version::newer;

/// The holds' directory under the ctxloom home.
pub const HELD_DIR: &str = ".ctxloom/held";

/// The most one session's hold keeps, in bytes, the record being appended
/// included (H-Q4): 32 MiB.
pub const SESSION_BOUND: u64 = 32 << 20;

/// The most every hold of a home keeps together, in bytes (H-Q4): 256 MiB.
pub const HOME_BOUND: u64 = 256 << 20;

/// How far gap records may take a session's hold past [`SESSION_BOUND`]:
/// 64 KiB of them, so a full hold still says what it dropped.
pub const GAP_SLACK: u64 = 64 << 10;

/// How long a hook waits for a session's append lock before it gives up
/// (and fails open, said): one second.
pub const APPEND_WAIT: Duration = Duration::from_secs(1);

/// Whether a daemon of stamp `daemon` is up to date for a fact held by a
/// hook of stamp `fact` (H-Q1 (b)): its VERSION is not older by semver
/// ([`newer`], the skew rule's own). Two builds of one version are up to
/// date for each other; a stamp not of the shape is never newer, so it
/// holds nothing back.
pub fn up_to_date(daemon: &str, fact: &str) -> bool {
    !newer(fact, daemon)
}

/// A fact kept for later: the hook's stamp, its session, the fact's
/// external id and its `CommandBook`'s exact bytes. Its `Debug` shows the
/// external id, the stamp and the book's size, never the book.
#[derive(Clone, PartialEq)]
pub struct HeldFact {
    /// The holding hook's full versionator stamp.
    pub stamp: String,
    /// `CTXLOOM_SESSION`'s resource name.
    pub session: String,
    /// The fact's `FactOrigin.external_id`.
    pub external_id: String,
    /// The `CommandBook`, prost-encoded.
    pub book: Vec<u8>,
    /// The hook's clock when it held the fact (an age; orders nothing).
    pub held_at: Option<Timestamp>,
    /// A fact on the session's RUN stream (a frontend's report): the
    /// incarnation whose run it is; 0, a fact of the session's own stream.
    pub incarnation: u32,
}

impl HeldFact {
    /// `book`, encoded, held by a hook of `stamp` for `session`.
    pub fn of_book(
        stamp: impl Into<String>,
        session: impl Into<String>,
        external_id: impl Into<String>,
        book: &CommandBook,
        held_at: Option<Timestamp>,
    ) -> HeldFact {
        HeldFact {
            stamp: stamp.into(),
            session: session.into(),
            external_id: external_id.into(),
            book: book.encode_to_vec(),
            held_at,
            incarnation: 0,
        }
    }

    /// The fact, as one on the run stream of the session's `incarnation`
    /// (a frontend's report, held as a hook's fact is).
    #[must_use]
    pub fn of_run(mut self, incarnation: u32) -> HeldFact {
        self.incarnation = incarnation;
        self
    }

    /// The book as it was held; an error when its bytes do not decode.
    pub fn decode_book(&self) -> Result<CommandBook, prost::DecodeError> {
        CommandBook::decode(self.book.as_slice())
    }
}

impl fmt::Debug for HeldFact {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("HeldFact")
            .field("stamp", &self.stamp)
            .field("external_id", &self.external_id)
            .field("incarnation", &self.incarnation)
            .field("book_bytes", &self.book.len())
            .finish_non_exhaustive()
    }
}

/// Why a fact could not be held.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum GapWhy {
    /// Past the session's or the home's bound.
    Bound,
    /// The disk refused it.
    Disk,
}

impl GapWhy {
    /// Its word in the record (`HeldGap.why`).
    pub fn word(self) -> &'static str {
        match self {
            GapWhy::Bound => "bound",
            GapWhy::Disk => "disk",
        }
    }

    /// The word's meaning; none for a word this ctxloom does not know.
    pub fn of_word(word: &str) -> Option<GapWhy> {
        match word {
            "bound" => Some(GapWhy::Bound),
            "disk" => Some(GapWhy::Disk),
            _ => None,
        }
    }
}

/// Where a hold has a hole: the fact that could not be kept.
#[derive(Clone, Debug, PartialEq)]
pub struct HeldGap {
    pub stamp: String,
    pub external_id: String,
    pub held_at: Option<Timestamp>,
    /// Why, as the record says it (`bound`, `disk`, or a later word).
    pub why: String,
}

/// One record of a hold, in the order held.
#[derive(Clone, Debug, PartialEq)]
pub enum HeldEntry {
    Fact(HeldFact),
    Gap(HeldGap),
}

/// The bound a fact was dropped at.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Bound {
    /// The session's hold, at this many bytes.
    Session(u64),
    /// Every hold of the home together, at this many bytes.
    Home(u64),
}

/// What became of a fact handed to [`HeldFacts::hold`].
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Held {
    /// Appended, after everything the hold already kept.
    Kept,
    /// Dropped, past `bound`: the earlier facts are kept, and `gap` says
    /// whether a gap record now marks the hole (none once even the gaps'
    /// slack is spent).
    Full { bound: Bound, gap: bool },
}

/// What [`Batch::settle`] did to the hold.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Settled {
    /// The hold is what was kept of the snapshot, then what was appended
    /// since; `kept` of the snapshot's records remain.
    Cut { kept: usize },
    /// Nothing was left: the hold is gone.
    Emptied,
    /// The hold is no longer the one snapshotted (discarded, and perhaps
    /// held anew, meanwhile): it was left as it is.
    Replaced,
}

/// What becomes of one record of a taken hold when its batch is settled.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Fate {
    /// It stays in the hold, where it was.
    Keep,
    /// It goes: delivered.
    Cut,
    /// It goes from the hold to its set-aside part (as corruption does),
    /// never delivered again: the daemon finally refused or parked it, or
    /// it is not a fact of the hold's session (s4-Q14, H3: no silent drop).
    SetAside,
}

/// The adapter's half of a [`Batch`]: what rewrites the hold once the
/// caller says what becomes of each record.
pub trait Settle: Send {
    /// Rewrite the hold to the snapshot's records `fates` keeps (one fate
    /// per record, in order), then everything appended since; the records
    /// it sets aside are kept in the hold's set-aside part.
    fn settle(self: Box<Self>, fates: &[Fate]) -> Result<Settled, HoldError>;
}

/// A hold's snapshot, in the order held, owned by the one process that
/// took it (it holds the session's flush lock until settled or dropped).
pub struct Batch {
    entries: Vec<HeldEntry>,
    settle: Box<dyn Settle>,
}

impl Batch {
    /// A batch of `entries`, settled through `settle`.
    pub fn new(entries: Vec<HeldEntry>, settle: Box<dyn Settle>) -> Batch {
        Batch { entries, settle }
    }

    /// The snapshot's records, in the order held.
    pub fn entries(&self) -> &[HeldEntry] {
        &self.entries
    }

    /// Keep the records `keep(i)` says, remove the rest. A record appended
    /// after the snapshot is always kept.
    pub fn settle(self, keep: impl Fn(usize) -> bool) -> Result<Settled, HoldError> {
        self.settle_fates(|i| if keep(i) { Fate::Keep } else { Fate::Cut })
    }

    /// Settle each record as `fate(i)` says: kept, cut, or set aside. A
    /// record appended after the snapshot is always kept.
    pub fn settle_fates(self, fate: impl Fn(usize) -> Fate) -> Result<Settled, HoldError> {
        let fates: Vec<Fate> = (0..self.entries.len()).map(fate).collect();
        self.settle.settle(&fates)
    }

    /// Cut the delivered prefix: the snapshot's first `delivered` records
    /// go, the rest stay.
    pub fn cut(self, delivered: usize) -> Result<Settled, HoldError> {
        self.settle(|i| i >= delivered)
    }
}

impl fmt::Debug for Batch {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Batch")
            .field("entries", &self.entries)
            .finish_non_exhaustive()
    }
}

/// What one session's hold holds, for a report (doctor's).
#[derive(Clone, Debug, PartialEq)]
pub struct HoldSummary {
    /// The session root the hold is named by.
    pub root: Uuid,
    /// The session's resource name, as its first fact says it.
    pub session: Option<String>,
    pub facts: u64,
    pub gaps: u64,
    /// The hold's size on disk.
    pub bytes: u64,
    /// The distinct stamps its facts were held by, sorted.
    pub stamps: Vec<String>,
    /// The earliest `held_at` of its records.
    pub oldest: Option<Timestamp>,
    /// How many set-aside (corrupt) files of the session there are.
    pub set_aside: u32,
    /// The hold is in a format this ctxloom cannot read (left untouched).
    pub unreadable: bool,
    /// Past its valid records the hold has bytes that are not a record (a
    /// torn append or corruption, cut or set aside at the next take).
    pub bad_tail: bool,
}

/// Each session's hold, kept in order.
pub trait HeldFacts: Send + Sync {
    /// Append `fact` to `session`'s hold, within the bounds.
    fn hold(&self, session: &Uuid, fact: HeldFact) -> Result<Held, HoldError>;

    /// Whether `session` holds nothing, judged under its append lock.
    fn is_empty(&self, session: &Uuid) -> Result<bool, HoldError>;

    /// Every session root with a hold, sorted.
    fn sessions(&self) -> Result<Vec<Uuid>, HoldError>;

    /// `session`'s hold, in order, to deliver; `None` when another process
    /// is taking it (its flush lock is held), never waited for.
    fn take(&self, session: &Uuid) -> Result<Option<Batch>, HoldError>;

    /// Remove `session`'s hold and its set-aside files: how many facts it
    /// held.
    fn discard(&self, session: &Uuid) -> Result<u64, HoldError>;

    /// What every hold holds, by session root.
    fn survey(&self) -> Result<Vec<HoldSummary>, HoldError>;
}

/// Why a hold could not be used. A hook that meets one fails open, said.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum HoldError {
    /// The system refused.
    Io { path: String, ext_err_txt: String },
    /// The hold's owner-only mode could not be set.
    Restrict { path: String, ext_err_txt: String },
    /// Its lock could not be had (another held it past the wait, or it
    /// could not be made).
    Lock { path: String, ext_err_txt: String },
    /// A file in a format this ctxloom cannot read, left untouched.
    Format { path: String },
}

impl fmt::Display for HoldError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        angzarr_diagnostic::display(self, f)
    }
}

impl std::error::Error for HoldError {}

impl Diagnostic for HoldError {
    fn code(&self) -> &str {
        match self {
            HoldError::Io { .. } => code!("hold-io"),
            HoldError::Restrict { .. } => code!("hold-restrict"),
            HoldError::Lock { .. } => code!("hold-lock"),
            HoldError::Format { .. } => code!("hold-format"),
        }
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        match self {
            HoldError::Io { path, ext_err_txt }
            | HoldError::Restrict { path, ext_err_txt }
            | HoldError::Lock { path, ext_err_txt } => vec![
                ("path", path.clone().into()),
                ("ext_err_txt", ext_err_txt.clone().into()),
            ],
            HoldError::Format { path } => vec![("path", path.clone().into())],
        }
    }
}

/// One value of every error, for the catalog test.
#[cfg(any(test, feature = "test-support"))]
pub(crate) fn diagnostic_samples() -> Vec<Box<dyn Diagnostic>> {
    let path = "/home/a/.ctxloom/held/0.held".to_string();
    let ext_err_txt = "Permission denied (os error 13)".to_string();
    vec![
        Box::new(HoldError::Io {
            path: path.clone(),
            ext_err_txt: ext_err_txt.clone(),
        }),
        Box::new(HoldError::Restrict {
            path: path.clone(),
            ext_err_txt: ext_err_txt.clone(),
        }),
        Box::new(HoldError::Lock {
            path: path.clone(),
            ext_err_txt,
        }),
        Box::new(HoldError::Format { path }),
    ]
}

#[cfg(test)]
#[path = "held.test.rs"]
mod tests;
