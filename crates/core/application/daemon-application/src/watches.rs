//! The sessions watched for the terminal wake (owner ruling 2026-11-18): a
//! primary terminal session's ctxloom MCP server holds a `WatchSession` of
//! its own session, and posts a wake on its engine's messaging socket when
//! a message would wake it. The daemon keeps, for each watched session, the
//! signal it last published ([`signal_of`]: the pending wakes and the wake
//! outstanding); each watch yields the signal it opened with, then each
//! change, and the same signal again every keepalive, so a watcher whose
//! client is gone is noticed when the API's edge cannot send it. A session
//! is watched while a watch of it is open: what `supported` offers a wake
//! to a primary on.

use std::collections::{BTreeMap, BTreeSet};
use std::sync::{Arc, Condvar, Mutex, MutexGuard, PoisonError};
use std::time::Duration;

use daemon_proto::v1::SessionSignal;
use pending_messages::PendingMessage;
use sessions::{SessionState, Urgency};

/// How often an unchanged signal is sent again.
pub const KEEPALIVE: Duration = Duration::from_secs(5);

/// The signal of a session with `pending` messages, as its `state` stands:
/// the ids of its pending wakes, in the order sent, and its outstanding
/// wake.
pub fn signal_of(pending: &[PendingMessage], state: Option<&SessionState>) -> SessionSignal {
    SessionSignal {
        wakes: pending
            .iter()
            .filter(|message| message.urgency == Urgency::Wake)
            .map(|message| message.message_id.clone())
            .collect(),
        outstanding_wake: state
            .map(|state| state.wake_outstanding.clone())
            .unwrap_or_default(),
    }
}

/// One watched session: how many watches of it are open, and the signal
/// last published, by its version.
#[derive(Default)]
struct Slot {
    watchers: usize,
    signal: SessionSignal,
    version: u64,
}

#[derive(Default)]
struct Table {
    slots: BTreeMap<String, Slot>,
    /// How many watchers are waiting for a change now.
    waiting: usize,
}

/// The watched sessions, shared by the daemon's loop (which publishes)
/// and its API (whose `WatchSession` watches).
pub struct SessionWatches {
    table: Mutex<Table>,
    changed: Condvar,
    keepalive: Duration,
}

impl SessionWatches {
    /// No session watched; an unchanged signal sent again each `keepalive`.
    pub fn new(keepalive: Duration) -> Arc<SessionWatches> {
        Arc::new(SessionWatches {
            table: Mutex::new(Table::default()),
            changed: Condvar::new(),
            keepalive,
        })
    }

    fn table(&self) -> MutexGuard<'_, Table> {
        self.table.lock().unwrap_or_else(PoisonError::into_inner)
    }

    /// A watch of `session` (its stable id), opened with `now`.
    pub fn watch(self: &Arc<Self>, session: &str, now: SessionSignal) -> SessionWatch {
        let mut table = self.table();
        let slot = table.slots.entry(session.to_string()).or_default();
        slot.watchers += 1;
        if slot.watchers == 1 || slot.signal != now {
            slot.signal = now;
            slot.version += 1;
        }
        drop(table);
        self.changed.notify_all();
        SessionWatch {
            watches: Arc::clone(self),
            session: session.to_string(),
            seen: None,
        }
    }

    /// Whether a watch of `session` is open.
    pub fn is_watched(&self, session: &str) -> bool {
        self.table().slots.contains_key(session)
    }

    /// Every session a watch is open of.
    pub fn watched(&self) -> BTreeSet<String> {
        self.table().slots.keys().cloned().collect()
    }

    /// How many watchers wait for a change now (a test's forcing point).
    pub fn waiting(&self) -> usize {
        self.table().waiting
    }

    /// `signal` is `session`'s now: its watchers are woken when it changed.
    /// Nothing for a session nobody watches.
    pub fn publish(&self, session: &str, signal: SessionSignal) {
        let mut table = self.table();
        let Some(slot) = table.slots.get_mut(session) else {
            return;
        };
        if slot.signal == signal {
            return;
        }
        slot.signal = signal;
        slot.version += 1;
        drop(table);
        self.changed.notify_all();
    }
}

/// One open watch of a session: closed when dropped.
pub struct SessionWatch {
    watches: Arc<SessionWatches>,
    session: String,
    /// The version of the signal it last yielded.
    seen: Option<u64>,
}

impl SessionWatch {
    /// The session's signal if it changed since this watch last yielded
    /// one (or it never did); none otherwise. Never waits.
    pub fn poll(&mut self) -> Option<SessionSignal> {
        let watches = Arc::clone(&self.watches);
        let table = watches.table();
        self.changed_in(&table)
    }

    /// The signal of `table`'s slot for this session, when it is a version
    /// this watch has not yielded, marked yielded.
    fn changed_in(&mut self, table: &Table) -> Option<SessionSignal> {
        let slot = table.slots.get(&self.session)?;
        if self.seen == Some(slot.version) {
            return None;
        }
        self.seen = Some(slot.version);
        Some(slot.signal.clone())
    }
}

impl Iterator for SessionWatch {
    type Item = SessionSignal;

    /// The next signal: a change, waited for, or the same again once the
    /// keepalive passed. Blocks (the API's edge runs it on a blocking
    /// thread); never ends.
    fn next(&mut self) -> Option<SessionSignal> {
        let watches = Arc::clone(&self.watches);
        let mut table = watches.table();
        if let Some(signal) = self.changed_in(&table) {
            return Some(signal);
        }
        table.waiting += 1;
        let (mut table, _) = watches
            .changed
            .wait_timeout_while(table, watches.keepalive, |table| {
                table
                    .slots
                    .get(&self.session)
                    .is_some_and(|slot| self.seen == Some(slot.version))
            })
            .unwrap_or_else(PoisonError::into_inner);
        table.waiting -= 1;
        let slot = table.slots.get(&self.session)?;
        self.seen = Some(slot.version);
        Some(slot.signal.clone())
    }
}

impl Drop for SessionWatch {
    fn drop(&mut self) {
        let mut table = self.watches.table();
        let Some(slot) = table.slots.get_mut(&self.session) else {
            return;
        };
        slot.watchers = slot.watchers.saturating_sub(1);
        if slot.watchers == 0 {
            table.slots.remove(&self.session);
        }
    }
}

#[cfg(test)]
#[path = "watches.test.rs"]
mod tests;
