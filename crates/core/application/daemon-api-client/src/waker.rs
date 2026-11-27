//! The terminal wake's poster, decided (owner ruling 2026-11-18): what a
//! primary terminal session's ctxloom MCP server does with each signal its
//! `WatchSession` gives, and when its wake's time runs out. Pure: the
//! server's loop issues, posts, disarms and times what this answers.
//!
//! - A wake is due when a pending wake is one it never woke for, and no
//!   wake is outstanding (one at a time). It wakes for every pending wake
//!   at once: each is woken for once, so one whose wake was disarmed waits
//!   for the next prompt.
//! - Its wake is armed from when it is due until its time runs out
//!   ([`DISARM_AFTER`], disarmed then) or a signal shows it no longer
//!   outstanding (its turn redeemed it). The daemon's signal can lag the
//!   issue, so the wake is cleared by a signal only once one has shown it
//!   outstanding.
//! - A wake the daemon did not issue is tried again at the next signal; one
//!   whose post failed is not.
//! - A wake outstanding at the first signal is a gone poster's (a session
//!   has one, its MCP server, started again): disarmed.

use std::collections::BTreeSet;
use std::time::Duration;

use daemon_proto::v1::SessionSignal;

/// How long a wake waits for its turn before it is disarmed.
pub const DISARM_AFTER: Duration = Duration::from_secs(60);

/// What the poster does next.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum WakeAct {
    /// Issue the wake of this nonce, then post it and time it.
    Wake(String),
    /// Disarm the wake of this nonce.
    Disarm(String),
}

/// The armed wake: its nonce, and whether a signal has shown it outstanding.
#[derive(Debug)]
struct Armed {
    nonce: String,
    seen: bool,
    /// The ids it was the first wake for.
    ids: Vec<String>,
}

/// One session's poster.
#[derive(Debug, Default)]
pub struct Waker {
    armed: Option<Armed>,
    woken_for: BTreeSet<String>,
    heard_once: bool,
}

impl Waker {
    /// A poster that has heard nothing.
    pub fn new() -> Waker {
        Waker::default()
    }

    /// The nonce of its armed wake.
    pub fn armed(&self) -> Option<&str> {
        self.armed.as_ref().map(|armed| armed.nonce.as_str())
    }

    /// What `signal` asks of it; `mint` mints the nonce of a wake that is
    /// due.
    pub fn heard(
        &mut self,
        signal: &SessionSignal,
        mint: impl FnOnce() -> String,
    ) -> Option<WakeAct> {
        let first = !self.heard_once;
        self.heard_once = true;
        let outstanding = signal.outstanding_wake.as_str();
        if let Some(armed) = self.armed.as_mut() {
            match outstanding == armed.nonce {
                true => armed.seen = true,
                // Shown outstanding before, now not: its turn redeemed it.
                false if armed.seen => self.armed = None,
                false => {}
            }
            return None;
        }
        if !outstanding.is_empty() {
            // A gone poster's, at the first signal; else waited out.
            self.woken_for.extend(signal.wakes.iter().cloned());
            return first.then(|| WakeAct::Disarm(outstanding.to_string()));
        }
        let due = signal.wakes.iter().any(|id| !self.woken_for.contains(id));
        if !due {
            return None;
        }
        let ids: Vec<String> = signal
            .wakes
            .iter()
            .filter(|id| !self.woken_for.contains(*id))
            .cloned()
            .collect();
        self.woken_for.extend(ids.iter().cloned());
        let nonce = mint();
        self.armed = Some(Armed {
            nonce: nonce.clone(),
            seen: false,
            ids,
        });
        Some(WakeAct::Wake(nonce))
    }

    /// The wake `nonce` was not issued (the daemon not reached, or it
    /// refused): forgotten, and what it was for woken for again at the
    /// next signal (the keepalive repeats it).
    pub fn not_issued(&mut self, nonce: &str) {
        if self.armed() != Some(nonce) {
            return;
        }
        if let Some(armed) = self.armed.take() {
            for id in &armed.ids {
                self.woken_for.remove(id);
            }
        }
    }

    /// The wake `nonce` could not be posted: forgotten, its messages left
    /// for the next prompt (a socket that is gone is not tried again).
    pub fn failed(&mut self, nonce: &str) {
        if self.armed() == Some(nonce) {
            self.armed = None;
        }
    }

    /// The time of the wake `nonce` ran out: disarmed, if it is still armed.
    pub fn expired(&mut self, nonce: &str) -> Option<WakeAct> {
        if self.armed() != Some(nonce) {
            return None;
        }
        self.armed = None;
        Some(WakeAct::Disarm(nonce.to_string()))
    }
}

#[cfg(test)]
#[path = "waker.test.rs"]
mod tests;
