//! A destroyed session's HOLD (s4-Q14, unit H4; owner H-Q8 (a)): the
//! capture facts its hooks kept that no daemon has recorded yet go with the
//! session, said first, never a refusal.
//!
//! The hold is the CLIENT'S (owner, H-Q5 revised: the daemon never reads a
//! client's files; inside a container the hold is the container's), so the
//! command that asked the daemon for the destroy reads and discards it, by
//! the session's resource name, once the daemon answers: a plan (no
//! `--yes`) reads it to warn and keeps it; an applied destroy reads it to
//! warn, then discards it, its set-aside parts with it. A refused removal
//! destroys nothing and touches no hold. A hook that appends after the
//! discard starts a hold of a session with no stream, which the next flush
//! sets aside and says (unit H3), and doctor reports.

use run_ports::{HeldFacts, HoldError};

/// What one session's hold keeps, for the destroy's warning.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct HeldOf {
    /// The facts its hold keeps, not yet recorded.
    pub facts: u64,
    /// Its set-aside parts: what a flush could never record, kept beside it.
    pub set_aside: u32,
}

/// The client's holds, as a destroy reaches one session's by its resource
/// name (`projects/{p}/sessions/{s}`).
pub trait SessionHolds {
    /// What the session `name` holds; none when it has no hold (or `name`
    /// names no session).
    fn held(&self, name: &str) -> Result<Option<HeldOf>, HoldError>;

    /// Remove the session `name`'s hold and its set-aside parts.
    fn discard(&self, name: &str) -> Result<(), HoldError>;
}

/// The holds `0` keeps, as a destroy reaches them.
pub struct Holds<H>(pub H);

impl<H: HeldFacts> SessionHolds for Holds<H> {
    fn held(&self, name: &str) -> Result<Option<HeldOf>, HoldError> {
        // A hold is named by its session's stream root.
        let Ok(session) = sessions::SessionName::parse(name) else {
            return Ok(None);
        };
        let root = session.id();
        Ok(self
            .0
            .survey()?
            .into_iter()
            .find(|summary| summary.root == root)
            .map(|summary| HeldOf {
                facts: summary.facts,
                set_aside: summary.set_aside,
            }))
    }

    fn discard(&self, name: &str) -> Result<(), HoldError> {
        match sessions::SessionName::parse(name) {
            Ok(session) => self.0.discard(&session.id()).map(|_| ()),
            Err(_) => Ok(()),
        }
    }
}

/// What the destroy of the session `name` does with its hold: what it
/// holds (to warn of), and, once the destroy was `applied`, the hold
/// discarded. None held, nothing is touched.
pub fn destroys_held(
    holds: &dyn SessionHolds,
    name: &str,
    applied: bool,
) -> Result<Option<HeldOf>, HoldError> {
    let held = holds.held(name)?;
    if applied && held.is_some() {
        holds.discard(name)?;
    }
    Ok(held)
}

#[cfg(test)]
#[path = "held.test.rs"]
mod tests;
