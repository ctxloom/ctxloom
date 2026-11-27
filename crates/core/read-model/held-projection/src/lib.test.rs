//! One fold fed by two drivers: each page reaches the model once (S32).

use std::sync::{Arc, Mutex};

use angzarr_embedded::angzarr_ports::{tombstone, BoxFuture};
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
        for page in &book.pages {
            if tombstone::is(page) {
                self.folded.retain(|(r, _)| *r != root);
                self.gone.push(root);
            } else {
                self.folded.push((root, page.sequence));
            }
        }
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

// A purge keeps the stream and leaves a tombstone: the book holding it
// restarts the stream there, the pages below it (deleted) owed nothing, and
// what follows is folded after it.
#[test]
fn a_stream_closed_by_a_tombstone_is_folded_afresh_from_it() {
    let held = Held::<Pages>::default();
    hand(&held, book(1, 0..3, "a"));
    let mut again = book(1, 3..3, "b");
    again.pages.push(tombstone::page(3));
    again.pages.push(page(4, "b"));
    again.next_sequence = 5;
    hand(&held, again);
    assert_eq!(held.lock().gone, [1], "the model was told of the tombstone");
    assert_eq!(folded(&held), [(1, 4)]);
}

// A tombstone ahead of where the fold is (the events between are deleted)
// owes no gap: it is the next event of the stream, handed with no fill.
#[test]
fn a_tombstone_past_the_fold_is_no_gap() {
    let store = Arc::new(Store {
        reads: Mutex::new(Vec::new()),
        fails: false,
    });
    let held = Held::<Pages>::new(Some(store.clone()));
    hand(&held, book(1, 0..2, "a"));
    let mut closed = book(1, 5..5, "a");
    closed.pages.push(tombstone::page(5));
    closed.next_sequence = 6;
    hand(&held, closed);
    assert!(store.reads.lock().unwrap().is_empty(), "nothing filled");
    assert_eq!(folded(&held), Vec::<(u8, u64)>::new());
    hand(&held, book(1, 6..7, "a"));
    assert_eq!(folded(&held), [(1, 6)], "folded after the tombstone");
}

// A tombstone already folded (the other driver handed it) is passed over,
// and resets nothing a second time.
#[test]
fn a_tombstone_folded_already_is_passed_over() {
    let held = Held::<Pages>::default();
    let mut closed = book(1, 0..0, "a");
    closed.pages.push(tombstone::page(0));
    closed.next_sequence = 1;
    hand(&held, closed.clone());
    hand(&held, book(1, 1..2, "a"));
    hand(&held, closed);
    assert_eq!(held.lock().gone, [1], "told once");
    assert_eq!(folded(&held), [(1, 1)]);
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

// A page no store recorded (no cause, no instant) is folded once: the
// sequence alone says where the fold is.
#[test]
fn a_book_of_pages_no_store_recorded_is_folded_once() {
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
    assert_eq!(folded(&held), [(1, 0), (1, 1)]);
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
// model only for what it was not handed.
#[test]
fn a_stream_handed_again_from_0_reaches_the_model_once() {
    let held = Held::<Counted>::default();
    hand_counted(&held, book(1, 0..2, "a"));
    hand_counted(&held, book(1, 0..3, "a"));
    assert_eq!(counted(&held), 3);
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

#[test]
fn a_watcher_is_woken_when_a_book_is_folded_and_not_before() {
    let held = Held::<Pages>::default();
    let woken = held.watch();
    assert!(
        woken.try_recv().is_err(),
        "nothing folded yet, nothing told"
    );
    hand(&held, book(1, 0..1, "a"));
    assert!(woken.try_recv().is_ok(), "a folded book wakes the watcher");
    assert!(woken.try_recv().is_err(), "one fold, one wake");
}

#[test]
fn a_notice_wakes_the_watchers_with_nothing_folded() {
    let held = Held::<Pages>::default();
    let woken = held.watch();
    held.notify();
    assert!(woken.try_recv().is_ok(), "a lock let go wakes the watcher");
    assert!(woken.try_recv().is_err(), "one notice, one wake");
}

#[test]
fn every_watcher_is_woken_and_one_gone_is_let_go() {
    let held = Held::<Pages>::default();
    let first = held.watch();
    let second = held.watch();
    drop(second);
    hand(&held, book(1, 0..1, "a"));
    assert!(first.try_recv().is_ok());
    assert_eq!(held.watching(), 1, "the dropped watcher is let go");
}

#[test]
fn a_book_the_model_already_has_wakes_nobody() {
    let held = Held::<Pages>::default();
    hand(&held, book(1, 0..2, "a"));
    let woken = held.watch();
    hand(&held, book(1, 0..2, "a"));
    assert!(
        woken.try_recv().is_err(),
        "a page folded already changes nothing"
    );
}
