//! The hook's HOLD policy (s4-Q14, unit H2): whether a hook delivers what
//! it observed to the user's daemon or keeps it in its session's hold, and,
//! when the daemon is up to date for it, delivering what the session
//! already holds FIRST (owner, H-Q5 revised: the client side flushes, never
//! the daemon).
//!
//! [`Holding::record`] judges one hook invocation's observations together,
//! in [`observe`] order:
//!
//! - no daemon reached or started ([`Serving::Unreached`], H-Q2): every
//!   observation is HELD;
//! - a daemon of an older version than the hook's ([`up_to_date`] false,
//!   H-Q1): every observation is HELD;
//! - a daemon up to date for the hook: the session's hold, if it holds
//!   anything, is taken ([`HeldFacts::take`]) and delivered in held order,
//!   each record the daemon is up to date for, then its delivered records
//!   cut, again until nothing it can deliver is left; then the hook's own
//!   observations are delivered. A hold another process is taking, a
//!   delivery left unsettled or the hook's time spent leaves the rest of the
//!   hold where it is and holds the hook's own facts BEHIND it, so a
//!   session's facts reach its stream first-in, first-out. An own delivery
//!   left unsettled (the daemon died under it) is held, and every later one
//!   behind it.
//!
//! A final answer (`Refused`, `Parked`) is never held: a retry would be
//! answered the same. A held record the daemon is not up to date for (a
//! downgrade) is kept and passed over, so it blocks nothing after it; a gap
//! record is delivered as the gap fact in its place (`RecordCaptureGap`,
//! H3), so the session's record says what is missing; a record that is not
//! a fact of this session (its external id is not its book's, or its book is
//! another stream's), and one the daemon finally refused or parked, goes to
//! the hold's set-aside part (never a silent drop, H3) and is said. The hook's time is the
//! [`HookClock`]'s: the core reads no clock. Nothing here fails: whatever
//! went wrong is a [`Kept`] in the [`HoldReport`], for the hook to say.
//!
//! [`Flush`] is the delivery itself, the hook's of its own session and a
//! ctxloom command's of every session of its home on its way in (H3,
//! [`Flush::every`], when [`command_flushes`]).

use std::time::Duration;

use angzarr_embedded::v1::{command_page, CommandBook, Uuid};
use daemon_proto::google::protobuf::Timestamp;
use run_ports::{
    up_to_date, Bound, Delivery, FactSink, Fate, Held, HeldEntry, HeldFact, HeldFacts, HoldError,
};
use surface_ports::HookEvent;

use crate::capture::{observe, Observation};

/// How many times one hook takes its session's hold before it gives up and
/// holds its own facts behind what is left: facts held by other hooks while
/// it delivers are taken again, a bounded number of times.
pub const TAKE_ROUNDS: usize = 8;

/// The hook's time, as its driving adapter reads the clock.
pub trait HookClock {
    /// What is left of the hook's bound for delivering.
    fn remaining(&self) -> Duration;
    /// The time now, a held fact's `held_at` (an age; it orders nothing).
    fn now(&self) -> Option<Timestamp>;
}

/// Whom the hook would deliver to.
pub enum Serving<'a, S: FactSink + ?Sized> {
    /// The user's daemon, of the versionator stamp `stamp`, through `sink`.
    Daemon { stamp: &'a str, sink: &'a S },
    /// No daemon could be reached or started within the hook's bound:
    /// `why`, as the hook says it.
    Unreached { why: String },
}

/// Why a fact was held.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Why {
    /// The serving daemon, of `daemon`, is older than the hook's `own`.
    Older { daemon: String, own: String },
    /// No daemon could be reached or started.
    Unreached { why: String },
    /// Its delivery was not settled (the daemon died under it, or did not
    /// answer in time).
    Unsettled(Delivery),
    /// The hook's time was spent before it could be delivered.
    OutOfTime,
}

/// Why a fact was neither recorded nor held.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum NotHeld {
    /// The hold is at its bound: this fact was dropped (a gap marks it).
    Full(Bound),
    /// The hold could not be used.
    Failed(HoldError),
    /// A held record that is not a fact of this session: set aside (H3).
    Foreign,
}

/// What became of one fact.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Outcome {
    /// Delivered, and finally answered.
    Delivered(Delivery),
    /// Held, for `Why`.
    Held(Why),
    /// Held behind what the session's hold still holds.
    HeldBehind,
    /// Neither recorded nor held.
    NotHeld(NotHeld),
}

/// One fact, and what became of it.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Kept {
    pub external_id: String,
    pub outcome: Outcome,
}

impl Kept {
    /// Whether a flush set it aside (H3: no silent drop): the daemon
    /// finally refused or parked it, or it is no fact of its hold's session.
    pub fn set_aside(&self) -> bool {
        matches!(
            self.outcome,
            Outcome::Delivered(
                Delivery::Refused { .. } | Delivery::Parked | Delivery::NoSuchSession
            ) | Outcome::NotHeld(NotHeld::Foreign)
        )
    }

    /// Whether the hook has something to say of it: anything but a fact
    /// the daemon has (applied, or a duplicate).
    pub fn said(&self) -> bool {
        !matches!(
            self.outcome,
            Outcome::Delivered(Delivery::Applied | Delivery::Duplicate)
        )
    }
}

/// What a hook's holding did: the session's held facts it delivered (or
/// dropped), then its own facts.
#[derive(Clone, Debug, PartialEq, Eq, Default)]
pub struct HoldReport {
    pub flushed: Vec<Kept>,
    pub own: Vec<Kept>,
}

impl HoldReport {
    /// Every fact the hook has something to say of, the held ones first.
    pub fn said(&self) -> Vec<&Kept> {
        self.flushed
            .iter()
            .chain(&self.own)
            .filter(|kept| kept.said())
            .collect()
    }
}

/// A frontend's report of its session's run, as [`Holding::report`] takes
/// it: the fact's external id, its book (on the run stream of
/// `incarnation`), and that incarnation.
#[derive(Clone, Debug, PartialEq)]
pub struct RunReport {
    pub external_id: String,
    pub book: CommandBook,
    pub incarnation: u32,
}

/// One fact to deliver or hold: a hook's observation (incarnation 0, the
/// session's own stream) or a frontend's report (its run's).
struct Item {
    external_id: String,
    book: CommandBook,
    incarnation: u32,
}

/// The hook-side hold policy over a session's hold.
pub struct Holding<'a, H: HeldFacts + ?Sized, C: HookClock + ?Sized> {
    held: &'a H,
    clock: &'a C,
    own: &'a str,
    session: &'a str,
}

impl<'a, H: HeldFacts + ?Sized, C: HookClock + ?Sized> Holding<'a, H, C> {
    /// The policy of a hook of stamp `own` for the session named `session`
    /// (`CTXLOOM_SESSION`'s resource name), over `held` and `clock`.
    pub fn new(held: &'a H, clock: &'a C, own: &'a str, session: &'a str) -> Holding<'a, H, C> {
        Holding {
            held,
            clock,
            own,
            session,
        }
    }

    /// Deliver or hold `event`'s observations for `session` (the stream
    /// root), as `serving` allows. Infallible by type.
    pub fn record<S: FactSink + ?Sized>(
        &self,
        session: &Uuid,
        event: &HookEvent,
        serving: Serving<'_, S>,
    ) -> HoldReport {
        let observed: Vec<Item> = observe(event)
            .iter()
            .map(|observation| Item {
                external_id: observation.external_id(session),
                book: observation.book(session),
                incarnation: 0,
            })
            .collect();
        self.items(session, &observed, serving)
    }

    /// Deliver or hold a FRONTEND's reports of the session's run (slice 4,
    /// a daemon dying under a live TUI: `RecordRunnerStarted`, `RecordRunExited`,
    /// `RecordLaunchFailed`, each on the run stream of its incarnation), as a
    /// hook's observations are: behind what the session's hold holds, held
    /// when no up-to-date daemon takes them, so the next attempt (the
    /// frontend's next report, or any command's flush) delivers them in
    /// order. Infallible by type.
    pub fn report<S: FactSink + ?Sized>(
        &self,
        session: &Uuid,
        reports: &[RunReport],
        serving: Serving<'_, S>,
    ) -> HoldReport {
        let items: Vec<Item> = reports
            .iter()
            .map(|report| Item {
                external_id: report.external_id.clone(),
                book: report.book.clone(),
                incarnation: report.incarnation,
            })
            .collect();
        self.items(session, &items, serving)
    }

    /// Deliver or hold `observed`, in order, as `serving` allows.
    fn items<S: FactSink + ?Sized>(
        &self,
        session: &Uuid,
        observed: &[Item],
        serving: Serving<'_, S>,
    ) -> HoldReport {
        let mut report = HoldReport::default();
        if observed.is_empty() {
            return report;
        }
        let Some(root) = root_of(session) else {
            return report;
        };
        let (stamp, sink) = match serving {
            Serving::Unreached { why } => {
                report.own = self.hold_all(&root, observed, &Why::Unreached { why });
                return report;
            }
            Serving::Daemon { stamp, sink } => (stamp, sink),
        };
        if !up_to_date(stamp, self.own) {
            let why = Why::Older {
                daemon: stamp.to_string(),
                own: self.own.to_string(),
            };
            report.own = self.hold_all(&root, observed, &why);
            return report;
        }
        let clear = self.flush(&root, stamp, sink, &mut report.flushed);
        report.own = if clear {
            self.deliver_own(&root, observed, sink)
        } else {
            self.hold_behind(&root, observed)
        };
        report
    }

    /// Deliver what `root`'s hold holds that `daemon` is up to date for, in
    /// order: whether nothing it can deliver is left.
    fn flush<S: FactSink + ?Sized>(
        &self,
        root: &uuid::Uuid,
        daemon: &str,
        sink: &S,
        flushed: &mut Vec<Kept>,
    ) -> bool {
        let done = Flush::new(self.held, self.clock).session(root, daemon, sink);
        flushed.extend(done.flushed);
        done.left == Left::Nothing
    }

    /// Deliver the hook's own observations; one left unsettled is held, and
    /// every later one behind it.
    fn deliver_own<S: FactSink + ?Sized>(
        &self,
        root: &uuid::Uuid,
        observed: &[Item],
        sink: &S,
    ) -> Vec<Kept> {
        let mut kept = Vec::new();
        let mut holding = false;
        for observation in observed {
            if holding {
                kept.push(self.hold_one(root, observation, Outcome::HeldBehind));
                continue;
            }
            let within = self.clock.remaining();
            if within.is_zero() {
                holding = true;
                kept.push(self.hold_one(root, observation, Outcome::Held(Why::OutOfTime)));
                continue;
            }
            let delivery = sink.deliver(observation.book.clone(), within);
            if delivery.settled() {
                kept.push(Kept {
                    external_id: observation.external_id.clone(),
                    outcome: Outcome::Delivered(delivery),
                });
            } else {
                holding = true;
                kept.push(self.hold_one(
                    root,
                    observation,
                    Outcome::Held(Why::Unsettled(delivery)),
                ));
            }
        }
        kept
    }

    fn hold_all(&self, root: &uuid::Uuid, observed: &[Item], why: &Why) -> Vec<Kept> {
        observed
            .iter()
            .map(|o| self.hold_one(root, o, Outcome::Held(why.clone())))
            .collect()
    }

    fn hold_behind(&self, root: &uuid::Uuid, observed: &[Item]) -> Vec<Kept> {
        observed
            .iter()
            .map(|o| self.hold_one(root, o, Outcome::HeldBehind))
            .collect()
    }

    /// `observation` appended to the hold: `held` when it was kept.
    fn hold_one(&self, root: &uuid::Uuid, observation: &Item, held: Outcome) -> Kept {
        let external_id = observation.external_id.clone();
        let fact = HeldFact::of_book(
            self.own,
            self.session,
            external_id.clone(),
            &observation.book,
            self.clock.now(),
        )
        .of_run(observation.incarnation);
        let outcome = match self.held.hold(root, fact) {
            Ok(Held::Kept) => held,
            Ok(Held::Full { bound, .. }) => Outcome::NotHeld(NotHeld::Full(bound)),
            Err(error) => Outcome::NotHeld(NotHeld::Failed(error)),
        };
        Kept {
            external_id,
            outcome,
        }
    }
}

/// Whether a ctxloom command of stamp `own` delivers holds to a daemon of
/// stamp `daemon` on its way in (s4-Q14, H3): when its version is not older
/// than the daemon's ([`run_ports::newer`], the skew rule's own).
pub fn command_flushes(own: &str, daemon: &str) -> bool {
    !run_ports::newer(daemon, own)
}

/// Why a flush of one session's hold left something held, or nothing.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Left {
    /// Nothing the daemon is up to date for is left (what is left, if
    /// anything, waits for a newer daemon).
    Nothing,
    /// Another process is taking the hold: skipped, never waited for.
    Taken,
    /// A delivery was not settled: it and everything after it stay.
    Unsettled(Delivery),
    /// The time was spent before everything could be delivered.
    OutOfTime,
    /// Facts kept arriving while it was delivered: [`TAKE_ROUNDS`] takes
    /// were not enough.
    Rounds,
    /// The hold could not be used.
    Failed(HoldError),
}

/// What a flush did to one session's hold.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct SessionFlush {
    /// The session root the hold is named by.
    pub root: uuid::Uuid,
    /// The session's resource name, as the hold's first fact names it.
    pub session: Option<String>,
    /// Each record it delivered (or set aside), in held order.
    pub flushed: Vec<Kept>,
    /// What it left, and why.
    pub left: Left,
}

impl SessionFlush {
    /// The records it set aside, in held order: those the daemon finally
    /// refused or parked, and those that are no fact of the hold's session.
    pub fn set_aside(&self) -> Vec<&Kept> {
        self.flushed
            .iter()
            .filter(|kept| kept.set_aside())
            .collect()
    }
}

/// The client side's flush of holds (owner, H-Q5 revised): a hook's of its
/// own session, a command's of every session of its home on its way in
/// (H3). Each hold is taken at once or skipped, delivered in held order
/// within the clock's time, and its delivered records cut.
pub struct Flush<'a, H: HeldFacts + ?Sized, C: HookClock + ?Sized> {
    held: &'a H,
    clock: &'a C,
}

impl<'a, H: HeldFacts + ?Sized, C: HookClock + ?Sized> Flush<'a, H, C> {
    /// A flush of the holds `held` keeps, within `clock`'s time.
    pub fn new(held: &'a H, clock: &'a C) -> Flush<'a, H, C> {
        Flush { held, clock }
    }

    /// Deliver, through `sink` to a daemon of stamp `daemon`, what every
    /// session's hold holds, in the order [`HeldFacts::sessions`] lists
    /// them, until the time is spent: one [`SessionFlush`] per hold (one
    /// the time did not reach is [`Left::OutOfTime`], untouched).
    pub fn every<S: FactSink + ?Sized>(
        &self,
        daemon: &str,
        sink: &S,
    ) -> Result<Vec<SessionFlush>, HoldError> {
        let mut flushes = Vec::new();
        for root in self.held.sessions()? {
            if self.clock.remaining().is_zero() {
                flushes.push(SessionFlush {
                    root,
                    session: None,
                    flushed: Vec::new(),
                    left: Left::OutOfTime,
                });
                continue;
            }
            flushes.push(self.session(&root, daemon, sink));
        }
        Ok(flushes)
    }

    /// Deliver, through `sink` to a daemon of stamp `daemon`, what `root`'s
    /// hold holds that the daemon is up to date for, in order, then cut
    /// what went; again, while records arrive meanwhile, at most
    /// [`TAKE_ROUNDS`] times. A gap record is delivered as the gap fact in
    /// its place; a record that is not a fact of `root`'s stream is dropped
    /// ([`NotHeld::Foreign`]).
    pub fn session<S: FactSink + ?Sized>(
        &self,
        root: &uuid::Uuid,
        daemon: &str,
        sink: &S,
    ) -> SessionFlush {
        let session = Uuid::from_bytes(*root.as_bytes());
        let mut flushed = Vec::new();
        let mut name = None;
        let left = self.rounds(root, &session, daemon, sink, &mut flushed, &mut name);
        SessionFlush {
            root: *root,
            session: name,
            flushed,
            left,
        }
    }

    fn rounds<S: FactSink + ?Sized>(
        &self,
        root: &uuid::Uuid,
        session: &Uuid,
        daemon: &str,
        sink: &S,
        flushed: &mut Vec<Kept>,
        name: &mut Option<String>,
    ) -> Left {
        for _ in 0..TAKE_ROUNDS {
            match self.held.is_empty(root) {
                Ok(true) => return Left::Nothing,
                Ok(false) => {}
                Err(error) => return Left::Failed(error),
            }
            let batch = match self.held.take(root) {
                Ok(Some(batch)) => batch,
                Ok(None) => return Left::Taken,
                Err(error) => return Left::Failed(error),
            };
            if name.is_none() {
                *name = batch.entries().iter().find_map(|entry| match entry {
                    HeldEntry::Fact(fact) => Some(fact.session.clone()),
                    HeldEntry::Gap(_) => None,
                });
            }
            let mut fates = vec![Fate::Keep; batch.entries().len()];
            let mut removed = false;
            let mut stopped = None;
            for (i, entry) in batch.entries().iter().enumerate() {
                let (stamp, external_id, book) = match entry {
                    HeldEntry::Fact(fact) => (
                        &fact.stamp,
                        fact.external_id.clone(),
                        own_book(fact, session),
                    ),
                    // A fact the hold dropped: recorded in its place, on the
                    // hold's own stream (H3).
                    HeldEntry::Gap(gap) => {
                        let observation = Observation::gap(gap);
                        (
                            &gap.stamp,
                            observation.external_id(session),
                            Some(observation.book(session)),
                        )
                    }
                };
                if !up_to_date(daemon, stamp) {
                    continue;
                }
                // No fact of this session's stream: never delivered, never
                // dropped either (H3), but set aside.
                let Some(book) = book else {
                    fates[i] = Fate::SetAside;
                    removed = true;
                    flushed.push(Kept {
                        external_id,
                        outcome: Outcome::NotHeld(NotHeld::Foreign),
                    });
                    continue;
                };
                let within = self.clock.remaining();
                if within.is_zero() {
                    stopped = Some(Left::OutOfTime);
                    break;
                }
                let delivery = sink.deliver(book, within);
                if !delivery.settled() {
                    stopped = Some(Left::Unsettled(delivery));
                    break;
                }
                // A final answer that is no record (refused, parked) is set
                // aside, never retried and never dropped (H3).
                fates[i] = match delivery {
                    Delivery::Refused { .. } | Delivery::Parked | Delivery::NoSuchSession => {
                        Fate::SetAside
                    }
                    _ => Fate::Cut,
                };
                removed = true;
                flushed.push(Kept {
                    external_id,
                    outcome: Outcome::Delivered(delivery),
                });
            }
            if let Err(error) = batch.settle_fates(|i| fates[i]) {
                return Left::Failed(error);
            }
            if let Some(left) = stopped {
                return left;
            }
            if !removed {
                // What is left is held for a daemon newer than this one.
                return Left::Nothing;
            }
        }
        Left::Rounds
    }
}

/// The hold's name for the stream root `session`.
fn root_of(session: &Uuid) -> Option<uuid::Uuid> {
    uuid::Uuid::from_slice(&session.value).ok()
}

/// `fact`'s book, when it is a fact of `session`'s stream (or, held with
/// an incarnation, of the run stream of that incarnation of the session: a
/// frontend's report) whose origin is the external id it was held by; none
/// otherwise.
fn own_book(fact: &HeldFact, session: &Uuid) -> Option<CommandBook> {
    let book = fact.decode_book().ok()?;
    let cover = book.cover.as_ref()?;
    let (domain, root) = match fact.incarnation {
        0 => (sessions::SESSION_DOMAIN, session.clone()),
        incarnation => {
            let text = uuid::Uuid::from_slice(&session.value).ok()?.to_string();
            (runs::RUN_DOMAIN, runs::run_root(&text, incarnation))
        }
    };
    if cover.domain != domain || cover.root.as_ref() != Some(&root) {
        return None;
    }
    let Some(command_page::Body::Fact(body)) = book.page.as_ref()?.body.as_ref() else {
        return None;
    };
    (body.origin.as_ref()?.external_id == fact.external_id).then_some(book)
}

/// What a session's hold still holds that a daemon has not taken (owner,
/// 2026-11-16, "flush before install"): its live records, the facts and
/// gaps a flush would deliver, and whether it is in a format this ctxloom
/// cannot read. Its set-aside part is not counted: it is never delivered
/// again, so it holds nothing a daemon will decode.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Undelivered {
    /// The session root the hold is named by.
    pub root: uuid::Uuid,
    /// The session's resource name, as the hold's first fact names it.
    pub session: Option<String>,
    pub facts: u64,
    pub gaps: u64,
    /// The hold is in a format this ctxloom cannot read: what it holds is
    /// not known, and no flush of this ctxloom delivers it.
    pub unreadable: bool,
}

/// The holds of `survey` that still hold a live record, in its order: a
/// fact or a gap not yet delivered, or a hold this ctxloom cannot read.
/// None is the home's holds EMPTY, as `ctxloom shutdown` must find them
/// before an install replaces the binary; a set-aside part alone, or a
/// torn tail with no record before it, is empty.
pub fn undelivered(survey: &[run_ports::HoldSummary]) -> Vec<Undelivered> {
    survey
        .iter()
        .filter(|hold| hold.facts > 0 || hold.gaps > 0 || hold.unreadable)
        .map(|hold| Undelivered {
            root: hold.root,
            session: hold.session.clone(),
            facts: hold.facts,
            gaps: hold.gaps,
            unreadable: hold.unreadable,
        })
        .collect()
}

#[cfg(test)]
#[path = "hold.test.rs"]
mod tests;
