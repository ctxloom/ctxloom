//! The one definition of a stale plan (L-staleness): a write planned
//! against the token `seen` is current only while the path's token `now`
//! is that same number. [`Library::token`](crate::Library::token) says
//! `now`; the aggregate's every write judges through [`judge`].

/// What [`judge`] found.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Judged {
    /// The caller saw the path as it is.
    Fresh,
    /// The path moved since the caller saw it (either way).
    Stale { seen: u64, now: u64 },
}

/// A plan against token `seen`, judged against the path's token `now`.
pub fn judge(seen: u64, now: u64) -> Judged {
    if seen == now {
        Judged::Fresh
    } else {
        Judged::Stale { seen, now }
    }
}

#[cfg(test)]
#[path = "staleness.test.rs"]
mod tests;
