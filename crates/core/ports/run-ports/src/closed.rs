//! A purge keeps its stream and leaves a tombstone: the stream is dense from
//! 0 or from its latest tombstone, a fold restarts from its default state at
//! one, and a stream whose last event is one is destroyed. What reads a
//! stream's pages through [`crate::Edge::events`] folds them through these.

use angzarr_embedded::angzarr_ports::tombstone;
use angzarr_embedded::v1::EventPage;

/// The pages of a stream a fold starts from: those after its last tombstone
/// (all of them where it has none), the tombstone itself not among them.
pub fn since_closed(pages: &[EventPage]) -> &[EventPage] {
    match pages.iter().rposition(tombstone::is) {
        Some(at) => &pages[at + 1..],
        None => pages,
    }
}

/// Whether the stream these `pages` are is destroyed: its last event is a
/// tombstone. A stream with no event was never written, and is not.
pub fn is_closed(pages: &[EventPage]) -> bool {
    pages.last().is_some_and(tombstone::is)
}

#[cfg(test)]
#[path = "closed.test.rs"]
mod tests;
