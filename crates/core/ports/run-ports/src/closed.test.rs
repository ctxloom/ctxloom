use angzarr_embedded::angzarr_ports::tombstone;
use angzarr_embedded::v1::{event_page, EventPage};
use angzarr_embedded::Any;

use super::{is_closed, since_closed};

fn event(sequence: u64) -> EventPage {
    EventPage {
        sequence,
        body: Some(event_page::Body::Event(Any::default())),
        meta: None,
    }
}

fn sequences(pages: &[EventPage]) -> Vec<u64> {
    pages.iter().map(|page| page.sequence).collect()
}

#[test]
fn test_a_stream_with_no_tombstone_is_folded_whole_and_is_not_closed() {
    let pages = [event(0), event(1)];
    assert_eq!(sequences(since_closed(&pages)), [0, 1]);
    assert!(!is_closed(&pages));
    assert!(!is_closed(&[]), "never written is not destroyed");
}

#[test]
fn test_a_stream_is_folded_from_after_its_last_tombstone() {
    let pages = [tombstone::page(2), event(3), tombstone::page(4), event(5)];
    assert_eq!(sequences(since_closed(&pages)), [5]);
    assert!(!is_closed(&pages), "written again after the tombstone");
}

#[test]
fn test_a_stream_ending_in_a_tombstone_folds_to_nothing_and_is_closed() {
    let pages = [tombstone::page(2)];
    assert!(since_closed(&pages).is_empty());
    assert!(is_closed(&pages));
}
