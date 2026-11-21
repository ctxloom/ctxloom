//! One fold fed by two drivers: each page reaches the model once (S32).

use std::sync::{Arc, Mutex};

use angzarr_embedded::angzarr_ports::BoxFuture;
use angzarr_embedded::angzarr_proto::v1::event_meta::Causation;
use angzarr_embedded::angzarr_proto::v1::{EventMeta, FactCause};
use angzarr_embedded::angzarr_router::hpb::ProjectRequest;
use angzarr_embedded::testing::block_on_ready;
use angzarr_embedded::v1::{event_page, Cover, EventBook, EventPage, Uuid};
use angzarr_embedded::{Any, PayloadError, ProjectorHandler, StoreError, Timestamp};

use super::{Fill, Held, Model};

/// A model that keeps every page it was handed, by stream root's first
/// byte, and counts its resets.
#[derive(Default)]
struct Pages {
    folded: Vec<(u8, u64)>,
    gone: Vec<u8>,
}

impl Model for Pages {
    const NAME: &'static str = "pages";

    fn project(&mut self, book: &EventBook) -> Result<(), PayloadError> {
        let root = book.cover.as_ref().unwrap().root.as_ref().unwrap().value[0];
        if book.pages.is_empty() {
            self.gone.push(root);
        }
        if book.pages.first().is_some_and(|p| p.sequence == 0) {
            self.folded.retain(|(r, _)| *r != root);
        }
        self.folded
            .extend(book.pages.iter().map(|page| (root, page.sequence)));
        Ok(())
    }

    fn view(&self, cover: &Cover) -> Option<Any> {
        let root = cover.root.as_ref()?.value[0];
        let count = self.folded.iter().filter(|(r, _)| *r == root).count();
        Some(Any {
            type_url: "count".to_string(),
            value: vec![u8::try_from(count).unwrap()],
        })
    }
}

fn cover(root: u8) -> Cover {
    Cover {
        domain: "d".to_string(),
        root: Some(Uuid::from_bytes([root; 16])),
        edition: None,
    }
}

fn page(sequence: u64, cause: &str) -> EventPage {
    EventPage {
        sequence,
        body: Some(event_page::Body::Event(Any::default())),
        meta: Some(EventMeta {
            causation: (sequence == 0).then(|| {
                Causation::Fact(FactCause {
                    command_id: Some(Uuid::from_bytes([cause.as_bytes()[0]; 16])),
                    origin: None,
                })
            }),
            recorded_at: Some(Timestamp {
                seconds: 1,
                nanos: 0,
            }),
            ..EventMeta::default()
        }),
    }
}

fn book(root: u8, sequences: std::ops::Range<u64>, cause: &str) -> EventBook {
    let next = sequences.end;
    EventBook {
        cover: Some(cover(root)),
        pages: sequences.map(|s| page(s, cause)).collect(),
        snapshot: None,
        next_sequence: next,
    }
}

fn hand(held: &Held<Pages>, book: EventBook) -> Option<Any> {
    block_on_ready(held.dispatch(&ProjectRequest { events: Some(book) }))
        .unwrap()
        .projection
}

fn folded(held: &Held<Pages>) -> Vec<(u8, u64)> {
    held.lock().folded.clone()
}

// The kernel hands a commit inline, then the catch-up hands the stream from
// 0: the pages already folded are passed over, so each is folded once.
#[test]
fn a_page_both_drivers_hand_is_folded_once() {
    let held = Held::<Pages>::default();
    let catch_up = held.clone();
    hand(&held, book(1, 0..2, "a"));
    hand(&catch_up, book(1, 0..3, "a"));
    hand(&held, book(1, 2..4, "a"));
    assert_eq!(folded(&held), [(1, 0), (1, 1), (1, 2), (1, 3)]);
}

// The view answered is the model's, of the book's stream, after the fold.
#[test]
fn the_projection_is_the_models_view_of_the_books_stream() {
    let held = Held::<Pages>::default();
    hand(&held, book(2, 0..1, "a"));
    let view = hand(&held, book(1, 0..3, "a")).unwrap();
    assert_eq!(view.value, [3]);
}

// A stream purged and written again starts at 0 with another first event:
// it is folded afresh.
#[test]
fn a_stream_written_again_is_folded_afresh() {
    let held = Held::<Pages>::default();
    hand(&held, book(1, 0..3, "a"));
    hand(&held, book(1, 0..1, "b"));
    assert_eq!(folded(&held), [(1, 0)]);
}

// A gone stream (no page, next 0) is forgotten, and a stream written on its
// cover after it is folded from 0.
#[test]
fn a_gone_stream_is_forgotten() {
    let held = Held::<Pages>::default();
    hand(&held, book(1, 0..2, "a"));
    hand(&held, book(1, 0..0, "a"));
    assert_eq!(held.lock().gone, [1]);
    hand(&held, book(1, 0..1, "a"));
    assert_eq!(held.lock().folded.last(), Some(&(1, 0)));
}

// Without a fill, a book past where the fold is waits for the catch-up,
// which hands the stream in order.
#[test]
fn a_gap_with_no_fill_waits_for_the_catch_up() {
    let held = Held::<Pages>::default();
    hand(&held, book(1, 0..1, "a"));
    hand(&held, book(1, 3..4, "a"));
    assert_eq!(folded(&held), [(1, 0)], "nothing out of order");
    hand(&held, book(1, 0..4, "a"));
    assert_eq!(folded(&held), [(1, 0), (1, 1), (1, 2), (1, 3)]);
}

/// The store's pages, as a fill reads them; each read is recorded.
struct Store {
    reads: Mutex<Vec<(u64, u64)>>,
    fails: bool,
}

impl Fill for Store {
    fn pages<'a>(
        &'a self,
        _cover: &'a Cover,
        from: u64,
        to: u64,
    ) -> BoxFuture<'a, Result<Vec<EventPage>, StoreError>> {
        self.reads.lock().unwrap().push((from, to));
        let fails = self.fails;
        Box::pin(async move {
            if fails {
                return Err(StoreError::Unavailable(
                    angzarr_embedded::angzarr_ports::Cause::new("down", []),
                ));
            }
            Ok((from..to).map(|s| page(s, "a")).collect())
        })
    }
}

// With a fill, the pages a gap is owed are read and folded first.
#[test]
fn a_gap_is_filled_from_the_store_first() {
    let store = Arc::new(Store {
        reads: Mutex::new(Vec::new()),
        fails: false,
    });
    let held = Held::<Pages>::new(Some(store.clone()));
    hand(&held, book(1, 0..1, "a"));
    let view = hand(&held, book(1, 3..4, "a")).unwrap();
    assert_eq!(*store.reads.lock().unwrap(), [(1, 3)]);
    assert_eq!(folded(&held), [(1, 0), (1, 1), (1, 2), (1, 3)]);
    assert_eq!(view.value, [4]);
}

// A fill that fails folds nothing out of order, and fails nothing.
#[test]
fn a_fill_that_fails_waits_for_the_catch_up() {
    let store = Arc::new(Store {
        reads: Mutex::new(Vec::new()),
        fails: true,
    });
    let held = Held::<Pages>::new(Some(store));
    hand(&held, book(1, 3..4, "a"));
    assert_eq!(folded(&held), Vec::<(u8, u64)>::new());
}

#[test]
fn the_projector_is_named_by_its_model() {
    assert_eq!(Held::<Pages>::default().name(), "pages");
}

// A first page no store recorded (no cause, no instant) tells nothing: a
// book from 0 of it is folded afresh, as the projector port says.
#[test]
fn a_book_from_0_with_no_record_of_its_first_event_is_folded_afresh() {
    let held = Held::<Pages>::default();
    let bare = |n: u64| EventBook {
        cover: Some(cover(1)),
        pages: (0..n)
            .map(|sequence| EventPage {
                sequence,
                body: None,
                meta: None,
            })
            .collect(),
        snapshot: None,
        next_sequence: n,
    };
    hand(&held, bare(2));
    hand(&held, bare(1));
    assert_eq!(folded(&held), [(1, 0)]);
}

/// A model that counts every page it is handed and never starts afresh.
#[derive(Default)]
struct Counted(usize);

impl Model for Counted {
    const NAME: &'static str = "counted";

    fn project(&mut self, book: &EventBook) -> Result<(), PayloadError> {
        self.0 += book.pages.len();
        Ok(())
    }

    fn view(&self, _: &Cover) -> Option<Any> {
        None
    }
}

fn hand_counted(held: &Held<Counted>, book: EventBook) {
    block_on_ready(held.dispatch(&ProjectRequest { events: Some(book) })).unwrap();
}

// A book from 0 handed again (the catch-up after the kernel) reaches the
// model only for what it was not handed: its first event's record tells it
// is the same stream.
#[test]
fn a_stream_handed_again_from_0_reaches_the_model_once() {
    let held = Held::<Counted>::default();
    hand_counted(&held, book(1, 0..2, "a"));
    hand_counted(&held, book(1, 0..3, "a"));
    assert_eq!(counted(&held), 3);
}

// A first event with a cause but no instant (or the other way) still tells
// the stream apart: only one with neither is unknown.
#[test]
fn a_first_event_with_either_record_is_known() {
    for (cause, instant) in [(true, false), (false, true)] {
        let held = Held::<Counted>::default();
        let mut first = book(1, 0..2, "a");
        let meta = first.pages[0].meta.as_mut().unwrap();
        if !cause {
            meta.causation = None;
        }
        if !instant {
            meta.recorded_at = None;
        }
        hand_counted(&held, first.clone());
        hand_counted(&held, first);
        assert_eq!(counted(&held), 2, "cause {cause}, instant {instant}");
    }
}

// An event the model cannot read is the port's bug, naming its type.
#[test]
fn a_bug_names_the_type_the_model_could_not_read() {
    let error = PayloadError::UnknownType {
        type_url: "/nothing.Known".to_string(),
    };
    let coded = super::bug(&error);
    assert!(format!("{coded:?}").contains("/nothing.Known"), "{coded:?}");
}

fn counted(held: &Held<Counted>) -> usize {
    let lock = held.lock();
    let model: &Counted = &lock;
    model.0
}
