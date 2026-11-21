//! A ctxloom command's flush of its home's HOLDS on the way in (s4-Q14,
//! unit H3; owner, H-Q5 revised: the client side pushes a hold over the
//! daemon's API, and the daemon reads no hold file, but for ONE named
//! exception: its start-up sweep's flush of a judged run's session,
//! [`StartFlush`], OD-59 (2)).
//!
//! A command of an up-to-date version (not older than the serving daemon,
//! [`runs_application::command_flushes`]) that reaches the daemon delivers
//! what EVERY session's hold under its home holds, through the daemon's
//! listener with the user bearer, exactly as a hook delivers (the same
//! [`runs_application::Flush`]): each hold taken at once or skipped (its
//! flush lock), in held order, its delivered prefix cut, a gap record
//! delivered as the gap fact in its place. Every session's, not only the
//! command's project's: the daemon and its one store serve the whole home,
//! and a hold names its session only inside its records. The whole flush is
//! bounded by [`FLUSH_BOUND`], so a command never stalls on it; what the
//! bound leaves is the next command's (or the session's next hook's).
//!
//! What it could not deliver stays held, unsaid. What it can never deliver
//! (the daemon finally refused or parked it, or it is no fact of its hold's
//! session) goes to the hold's set-aside part and is said ONCE a session,
//! [`HeldSetAside`], on stderr when the command ends ([`take_said`]); never
//! a silent drop (owner, H3).

use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};

use angzarr_diagnostic::{code, Arg, Diagnostic};
use std::time::{Duration, Instant};

use run_ports::{Delivery, FactSink, HeldFacts, HoldError};
use runs_application::{command_flushes, Flush, HookClock, Outcome, SessionFlush};

use super::{files_of, own_stamp, serving_here};
use crate::capture::{daemon_sink, Deadline, EdgeSink};
use crate::Wiring;

/// The most a command's flush of its home's holds may take, in all.
pub(crate) const FLUSH_BOUND: Duration = Duration::from_secs(2);

/// What a command's flush did.
#[derive(Debug, PartialEq, Eq)]
pub(crate) enum Flushed {
    /// No live daemon serves this home: nothing was delivered (none is
    /// started for a flush).
    NotServing,
    /// The serving daemon, of `daemon`, is newer than this command: it is
    /// not this command's to deliver to.
    Older { daemon: String },
    /// Each hold it found, and what became of it.
    Done(Vec<SessionFlush>),
    /// The holds could not be listed.
    Failed(HoldError),
}

/// Deliver every session's hold under `wiring`'s home to the daemon now
/// serving it (none is started), as a command of stamp `stamp` (the
/// binary's; a test's own under test-support), within [`FLUSH_BOUND`].
/// The frontend calls it at its start and its exit report (slice 4 U5);
/// a command on its way in calls it through [`on_the_way_in`].
pub(crate) fn flush_holds(wiring: &Wiring, stamp: &str) -> Flushed {
    let own = own_stamp(wiring.env(), stamp);
    match serving_here(wiring, &files_of(wiring)) {
        Ok(Some(found)) => flush_to(wiring, own, &found),
        Ok(None) | Err(_) => Flushed::NotServing,
    }
}

/// [`flush_holds`] to the daemon `found` names, live and this home's.
pub(crate) fn flush_to(wiring: &Wiring, own: &str, found: &run_ports::Discovery) -> Flushed {
    flush_within(wiring, own, found, FLUSH_BOUND)
}

/// [`flush_to`] within `bound` in all (`ctxloom shutdown`'s is longer than
/// a command's on its way in).
pub(crate) fn flush_within(
    wiring: &Wiring,
    own: &str,
    found: &run_ports::Discovery,
    bound: Duration,
) -> Flushed {
    let held = fact_spool::Spool::at_home(wiring.fs(), wiring.home());
    let sink = daemon_sink(found.clone());
    let clock = Deadline(Instant::now() + bound);
    flush_with(&held, &clock, own, &found.stamp, &sink)
}

/// A command of stamp `own`'s flush of `held` to a daemon of stamp
/// `daemon`, through `sink`, within `clock`'s time.
pub(crate) fn flush_with<H, C, S>(held: &H, clock: &C, own: &str, daemon: &str, sink: &S) -> Flushed
where
    H: HeldFacts + ?Sized,
    C: HookClock + ?Sized,
    S: FactSink + ?Sized,
{
    if !command_flushes(own, daemon) {
        return Flushed::Older {
            daemon: daemon.to_string(),
        };
    }
    match Flush::new(held, clock).every(daemon, sink) {
        Ok(done) => Flushed::Done(done),
        Err(error) => Flushed::Failed(error),
    }
}

/// THE ONE NAMED EXCEPTION to "the daemon never reads a hold" (OD-59 (2),
/// owner 2026-11-16, "ALLOW the check"; `daemon_application::StartHolds`):
/// the daemon's start-up sweep delivers the hold of each session whose run
/// it is about to judge, so a TUI frontend's exit report, held while no
/// daemon served, ends its run as it says instead of `runner-lost`. It is
/// the client side's own flush of one session ([`Flush::session`]: in held
/// order, only what this daemon of `stamp` is up to date for, the delivered
/// prefix cut, a final refusal set aside), through the daemon's in-process
/// edge ([`EdgeSink`]) instead of its listener; no other reader of a hold's
/// record exists. Each session's flush is bounded by [`FLUSH_BOUND`]. What
/// it cannot deliver (a record of a newer ctxloom, a hold in a format this
/// one cannot read, one another process is taking) stays, and the sweep
/// judges the run as before, said in the log.
pub(crate) struct StartFlush<E: run_ports::Edge + 'static> {
    pub(crate) fs: Arc<dyn file_system::Fs>,
    pub(crate) home: std::path::PathBuf,
    pub(crate) edge: Arc<E>,
    /// The daemon's own stamp (a test's under test-support).
    pub(crate) stamp: String,
}

impl<E: run_ports::Edge + 'static> daemon_application::StartHolds for StartFlush<E> {
    fn flush<'a>(
        &'a self,
        session: &'a str,
    ) -> std::pin::Pin<
        Box<dyn std::future::Future<Output = daemon_application::HoldFlushed> + Send + 'a>,
    > {
        let fs = Arc::clone(&self.fs);
        let home = self.home.clone();
        let edge = Arc::clone(&self.edge);
        let stamp = self.stamp.clone();
        let session = session.to_string();
        Box::pin(async move {
            // The flush blocks (the hold's files, the sink's own runtime):
            // off the daemon's workers.
            tokio::task::spawn_blocking(move || start_flush(fs, &home, edge, &stamp, &session))
                .await
                .unwrap_or_else(|e| daemon_application::HoldFlushed {
                    left: Some(format!("its flush panicked: {e}")),
                    ..daemon_application::HoldFlushed::default()
                })
        })
    }
}

/// [`StartFlush`]'s flush of `session`'s hold under `home`, to the daemon
/// of `stamp` behind `edge`.
fn start_flush<E: run_ports::Edge + 'static>(
    fs: Arc<dyn file_system::Fs>,
    home: &std::path::Path,
    edge: Arc<E>,
    stamp: &str,
    session: &str,
) -> daemon_application::HoldFlushed {
    let Ok(root) = uuid::Uuid::parse_str(session) else {
        return daemon_application::HoldFlushed {
            left: Some(format!("{session} is no session id")),
            ..daemon_application::HoldFlushed::default()
        };
    };
    let held = fact_spool::Spool::at_home(fs, home);
    let sink = EdgeSink::new(edge);
    let clock = Deadline(Instant::now() + FLUSH_BOUND);
    let flushed = Flush::new(&held, &clock).session(&root, stamp, &sink);
    held_flushed(&flushed, held.is_empty(&root))
}

/// What a start-up flush did, as the daemon judges it: `empty`, whether
/// the hold holds nothing now (a record a newer ctxloom held is left by a
/// flush that says [`runs_application::Left::Nothing`]).
pub(crate) fn held_flushed(
    flushed: &SessionFlush,
    empty: Result<bool, HoldError>,
) -> daemon_application::HoldFlushed {
    let set_aside = flushed.set_aside().len();
    let delivered = flushed.flushed.len() - set_aside;
    let left = match (&flushed.left, empty) {
        (runs_application::Left::Nothing, Ok(true)) => None,
        (runs_application::Left::Nothing, Ok(false)) => {
            Some("records held by a newer ctxloom".to_string())
        }
        (runs_application::Left::Nothing, Err(e)) => Some(e.to_string()),
        (runs_application::Left::Failed(e), _) => Some(e.to_string()),
        (runs_application::Left::Taken, _) => Some("another process is taking it".to_string()),
        (runs_application::Left::Unsettled(delivery), _) => {
            Some(format!("a delivery was not settled: {delivery:?}"))
        }
        (runs_application::Left::OutOfTime, _) => Some("its time was spent".to_string()),
        (runs_application::Left::Rounds, _) => Some("facts kept arriving".to_string()),
    };
    daemon_application::HoldFlushed {
        delivered,
        set_aside,
        left,
    }
}

/// Whether this process has flushed on its way in: once a process, however
/// many times it reaches the daemon.
static FLUSHED_ON_THE_WAY_IN: WayIn = WayIn::new();

/// A once-a-process latch.
pub(crate) struct WayIn(AtomicBool);

impl WayIn {
    pub(crate) const fn new() -> WayIn {
        WayIn(AtomicBool::new(false))
    }

    /// Whether this is the first time it is asked.
    pub(crate) fn first(&self) -> bool {
        !self.0.swap(true, Ordering::SeqCst)
    }
}

/// A command reaching the daemon `found` names, of stamp `own`: its flush
/// of the home's holds, the first time this process reaches it; what it
/// set aside is said when the command ends ([`take_said`]).
pub(crate) fn on_the_way_in(wiring: &Wiring, own: &str, found: &run_ports::Discovery) {
    if FLUSHED_ON_THE_WAY_IN.first() {
        let flushed = flush_to(wiring, own, found);
        to_say(notices(&flushed));
    }
}

/// What this process's flush set aside, to be said once on stderr.
static SAID: Mutex<Vec<HeldSetAside>> = Mutex::new(Vec::new());

pub(crate) fn to_say(notices: Vec<HeldSetAside>) {
    if let Ok(mut said) = SAID.lock() {
        said.extend(notices);
    }
}

/// What this process's flush set aside and has not said yet, taken: the
/// command says each once, on stderr, as it ends.
pub(crate) fn take_said() -> Vec<HeldSetAside> {
    SAID.lock()
        .map(|mut said| std::mem::take(&mut *said))
        .unwrap_or_default()
}

/// What a flush set aside, one notice a session (PROPOSAL, the owner's to
/// word): how many of its held facts, and why, the reasons in held order,
/// each once.
pub(crate) fn notices(flushed: &Flushed) -> Vec<HeldSetAside> {
    let Flushed::Done(done) = flushed else {
        return Vec::new();
    };
    done.iter()
        .filter_map(|session| {
            let aside = session.set_aside();
            if aside.is_empty() {
                return None;
            }
            let mut whys: Vec<String> = Vec::new();
            for kept in &aside {
                let why = why_set_aside(&kept.outcome);
                if !whys.contains(&why) {
                    whys.push(why);
                }
            }
            Some(HeldSetAside {
                session: session_said(session),
                count: aside.len() as u64,
                why: whys.join("; "),
            })
        })
        .collect()
}

/// The session a notice names: its harp, as its hold's first fact names
/// it, else its session root.
fn session_said(flushed: &SessionFlush) -> String {
    harp_or_root(flushed.session.as_deref(), &flushed.root)
}

/// A hold's session as it is said: the harp of `session` (its resource
/// name, as the hold's first fact names it), else the hold's `root`.
pub(crate) fn harp_or_root(session: Option<&str>, root: &uuid::Uuid) -> String {
    session
        .and_then(|name| sessions::SessionName::parse(name).ok())
        .map_or_else(|| root.to_string(), |name| name.harp().to_string())
}

/// Why a record was set aside, as the notice says it (PROPOSAL, the
/// owner's to word).
fn why_set_aside(outcome: &Outcome) -> String {
    match outcome {
        Outcome::Delivered(Delivery::Refused { detail }) => {
            format!("the daemon refused it: {detail}")
        }
        Outcome::Delivered(Delivery::Parked) => {
            "the daemon parked it, as a fact it can never record".to_string()
        }
        Outcome::Delivered(Delivery::NoSuchSession) => {
            "the daemon has no record of this session".to_string()
        }
        _ => "it is not a fact of this session's stream".to_string(),
    }
}

/// A session's held facts a flush set aside: never delivered again, kept in
/// the hold's set-aside part (H3). `session` is its harp where the hold
/// names it, else its session root.
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct HeldSetAside {
    pub(crate) session: String,
    pub(crate) count: u64,
    pub(crate) why: String,
}

impl std::fmt::Display for HeldSetAside {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        angzarr_diagnostic::display(self, f)
    }
}

impl std::error::Error for HeldSetAside {}

impl Diagnostic for HeldSetAside {
    fn code(&self) -> &str {
        code!("held-set-aside")
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        vec![
            ("session", self.session.clone().into()),
            ("count", self.count.into()),
            ("why", self.why.clone().into()),
        ]
    }
}

#[cfg(test)]
#[path = "flush.test.rs"]
mod tests;
