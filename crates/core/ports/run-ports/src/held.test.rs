//! The hold's port: `up_to_date`'s table (H-Q1 (b)), a held fact's Debug,
//! and what a batch hands its adapter to keep.

use std::sync::{Arc, Mutex};

use angzarr_embedded::v1::{CommandBook, Cover};

use super::*;

// Up to date is "not older by semver": the skew rule's newer, reversed. A
// hook newer than the daemon holds; an older or equal VERSION delivers,
// whatever the hash or a dirty build's time; a stamp not of the shape is
// never newer, so it holds nothing back.
#[test]
fn up_to_date_is_a_version_not_older_than_the_facts() {
    for (daemon, fact) in [
        ("v0.1.0-bbbbbbb", "v0.2.0-aaaaaaa"),
        ("v1.0.0-rc.1-bbbbbbb", "v1.0.0-aaaaaaa"),
        ("v0.1.9-bbbbbbb", "v0.1.10-aaaaaaa"),
        ("v0.1.0-bbbbbbb-20261114T101010-dirty", "v0.2.0-aaaaaaa"),
    ] {
        assert!(!up_to_date(daemon, fact), "{daemon} is older than {fact}");
        assert!(up_to_date(fact, daemon), "{fact} is newer than {daemon}");
    }
    for (daemon, fact) in [
        ("v0.2.0-aaaaaaa", "v0.2.0-bbbbbbb"),
        (
            "v0.2.0-aaaaaaa-20261114T101010-dirty",
            "v0.2.0-aaaaaaa-20261115T101010-dirty",
        ),
        ("not a stamp", "v0.2.0-aaaaaaa"),
        ("v0.2.0-aaaaaaa", ""),
    ] {
        assert!(up_to_date(daemon, fact), "{daemon} for {fact}");
        assert!(up_to_date(fact, daemon), "{fact} for {daemon}");
    }
}

fn book() -> CommandBook {
    CommandBook {
        cover: Some(Cover {
            domain: "sessions".to_string(),
            ..Cover::default()
        }),
        ..CommandBook::default()
    }
}

#[test]
fn a_held_fact_keeps_its_books_exact_bytes() {
    let fact = HeldFact::of_book(
        "v0.1.0-aaaaaaa",
        "projects/p/sessions/h",
        "hook:x",
        &book(),
        None,
    );
    assert_eq!(fact.book, book().encode_to_vec());
    assert_eq!(fact.decode_book().unwrap(), book());
    let garbled = HeldFact {
        book: vec![0xff],
        ..fact
    };
    assert!(garbled.decode_book().is_err());
}

#[test]
fn a_held_facts_debug_never_shows_its_book() {
    let fact = HeldFact {
        stamp: "v0.1.0-aaaaaaa".to_string(),
        session: "projects/p/sessions/h".to_string(),
        external_id: "hook:prompt:s:p".to_string(),
        book: b"SECRET PROMPT TEXT".to_vec(),
        held_at: None,
        incarnation: 0,
    };
    let shown = format!("{fact:?}");
    assert!(!shown.contains("SECRET"), "{shown}");
    assert!(
        shown.contains("hook:prompt:s:p") && shown.contains("v0.1.0-aaaaaaa"),
        "{shown}"
    );
    assert!(shown.contains("18"), "the size: {shown}");
    let batch = Batch::new(vec![HeldEntry::Fact(fact)], Box::new(Told::default()));
    assert!(!format!("{batch:?}").contains("SECRET"));
}

#[test]
fn a_gaps_why_is_a_word() {
    for why in [GapWhy::Bound, GapWhy::Disk] {
        assert_eq!(GapWhy::of_word(why.word()), Some(why));
    }
    assert_eq!(GapWhy::Bound.word(), "bound");
    assert_eq!(GapWhy::Disk.word(), "disk");
    assert_eq!(GapWhy::of_word("later"), None);
}

/// A settle that records the flags it was handed.
#[derive(Default, Clone)]
struct Told(Arc<Mutex<Option<Vec<bool>>>>, Arc<Mutex<Vec<Fate>>>);

impl Settle for Told {
    fn settle(self: Box<Self>, fates: &[Fate]) -> Result<Settled, HoldError> {
        let keep: Vec<bool> = fates.iter().map(|f| *f == Fate::Keep).collect();
        *self.0.lock().unwrap() = Some(keep.clone());
        *self.1.lock().unwrap() = fates.to_vec();
        Ok(Settled::Cut {
            kept: keep.iter().filter(|k| **k).count(),
        })
    }
}

// s4-Q14, H3: a batch hands its adapter each record's fate: a keep flag's
// `false` is a cut, and a set-aside is its own fate.
#[test]
fn a_settle_hands_each_records_fate() {
    let told = Told::default();
    let batch = Batch::new((0..3).map(gap).collect(), Box::new(told.clone()));
    batch
        .settle_fates(|i| [Fate::Cut, Fate::SetAside, Fate::Keep][i])
        .unwrap();
    assert_eq!(
        *told.1.lock().unwrap(),
        vec![Fate::Cut, Fate::SetAside, Fate::Keep]
    );
    let told = Told::default();
    let batch = Batch::new((0..2).map(gap).collect(), Box::new(told.clone()));
    batch.settle(|i| i == 1).unwrap();
    assert_eq!(*told.1.lock().unwrap(), vec![Fate::Cut, Fate::Keep]);
}

fn gap(n: usize) -> HeldEntry {
    HeldEntry::Gap(HeldGap {
        stamp: String::new(),
        external_id: format!("x{n}"),
        held_at: None,
        why: "bound".to_string(),
    })
}

#[test]
fn a_cut_keeps_what_follows_the_delivered_prefix() {
    let told = Told::default();
    let batch = Batch::new((0..4).map(gap).collect(), Box::new(told.clone()));
    assert_eq!(batch.entries().len(), 4);
    assert_eq!(batch.cut(3).unwrap(), Settled::Cut { kept: 1 });
    assert_eq!(
        *told.0.lock().unwrap(),
        Some(vec![false, false, false, true])
    );
}

#[test]
fn a_settle_keeps_what_its_caller_marks() {
    let told = Told::default();
    let batch = Batch::new((0..3).map(gap).collect(), Box::new(told.clone()));
    batch.settle(|i| i == 1).unwrap();
    assert_eq!(*told.0.lock().unwrap(), Some(vec![false, true, false]));
}

#[test]
fn the_bounds_are_the_rulings() {
    assert_eq!(SESSION_BOUND, 32 * 1024 * 1024);
    assert_eq!(HOME_BOUND, 256 * 1024 * 1024);
    assert_eq!(GAP_SLACK, 64 * 1024);
    assert_eq!(APPEND_WAIT, Duration::from_secs(1));
    assert_eq!(HELD_DIR, ".ctxloom/held");
}
