//! A destroy's hold (s4-Q14, H4; H-Q8 (a)): read to warn, discarded once the
//! destroy is applied, over a fake of the client's holds.

use std::sync::Mutex;

use run_ports::{Batch, Held, HeldFact, HeldFacts, HoldError, HoldSummary};
use sessions::SessionName;
use uuid::Uuid;

use super::{destroys_held, HeldOf, Holds, SessionHolds};

const NAME: &str = "projects/calm-otter/sessions/amber-swift-owl";
const OTHER: &str = "projects/calm-otter/sessions/brisk-copper-moth";

fn root(name: &str) -> Uuid {
    SessionName::parse(name).unwrap().id()
}

fn summary(name: &str, facts: u64, set_aside: u32) -> HoldSummary {
    HoldSummary {
        root: root(name),
        session: Some(name.to_string()),
        facts,
        gaps: 0,
        bytes: 100,
        stamps: vec!["v0.1.0-abc1234".to_string()],
        oldest: None,
        set_aside,
        unreadable: false,
        bad_tail: false,
    }
}

/// The holds `held` surveys; each discard logged by root.
#[derive(Default)]
struct Fake {
    held: Vec<HoldSummary>,
    survey_fails: bool,
    discard_fails: bool,
    discarded: Mutex<Vec<Uuid>>,
}

fn failure() -> HoldError {
    HoldError::Io {
        path: "/h/.ctxloom/held".to_string(),
        ext_err_txt: "Permission denied (os error 13)".to_string(),
    }
}

impl HeldFacts for Fake {
    fn hold(&self, _: &Uuid, _: HeldFact) -> Result<Held, HoldError> {
        unreachable!("a destroy holds nothing")
    }

    fn is_empty(&self, _: &Uuid) -> Result<bool, HoldError> {
        unreachable!("a destroy asks no emptiness")
    }

    fn sessions(&self) -> Result<Vec<Uuid>, HoldError> {
        Ok(self.held.iter().map(|s| s.root).collect())
    }

    fn take(&self, _: &Uuid) -> Result<Option<Batch>, HoldError> {
        unreachable!("a destroy delivers nothing")
    }

    fn discard(&self, session: &Uuid) -> Result<u64, HoldError> {
        if self.discard_fails {
            return Err(failure());
        }
        self.discarded.lock().unwrap().push(*session);
        Ok(0)
    }

    fn survey(&self) -> Result<Vec<HoldSummary>, HoldError> {
        if self.survey_fails {
            return Err(failure());
        }
        Ok(self.held.clone())
    }
}

fn holds(held: Vec<HoldSummary>) -> Holds<Fake> {
    Holds(Fake {
        held,
        ..Fake::default()
    })
}

#[test]
fn a_sessions_hold_is_its_facts_and_set_aside_parts_by_its_name() {
    let holds = holds(vec![summary(OTHER, 9, 0), summary(NAME, 3, 2)]);
    assert_eq!(
        holds.held(NAME).unwrap(),
        Some(HeldOf {
            facts: 3,
            set_aside: 2
        })
    );
    assert_eq!(
        holds.held(OTHER).unwrap(),
        Some(HeldOf {
            facts: 9,
            set_aside: 0
        })
    );
}

#[test]
fn a_session_with_no_hold_or_a_name_that_is_no_session_holds_nothing() {
    let holds = holds(vec![summary(OTHER, 9, 0)]);
    assert_eq!(holds.held(NAME).unwrap(), None);
    assert_eq!(holds.held("not a name").unwrap(), None);
}

// A hold of set-aside parts alone (its facts all flushed or set aside) is
// still the session's, and goes with it.
#[test]
fn a_hold_of_set_aside_parts_alone_is_held() {
    let holds = holds(vec![summary(NAME, 0, 1)]);
    assert_eq!(
        holds.held(NAME).unwrap(),
        Some(HeldOf {
            facts: 0,
            set_aside: 1
        })
    );
}

#[test]
fn a_discard_removes_the_named_sessions_hold_alone() {
    let holds = holds(vec![summary(OTHER, 9, 0), summary(NAME, 3, 0)]);
    holds.discard(NAME).unwrap();
    assert_eq!(*holds.0.discarded.lock().unwrap(), [root(NAME)]);
}

#[test]
fn a_discard_of_a_name_that_is_no_session_touches_nothing() {
    let holds = holds(vec![summary(NAME, 3, 0)]);
    holds.discard("not a name").unwrap();
    assert!(holds.0.discarded.lock().unwrap().is_empty());
}

#[test]
fn the_holds_failures_are_said() {
    let broken = Holds(Fake {
        survey_fails: true,
        discard_fails: true,
        ..Fake::default()
    });
    assert_eq!(broken.held(NAME), Err(failure()));
    assert_eq!(broken.discard(NAME), Err(failure()));
}

// H-Q8 (a): a plan warns and keeps the hold.
#[test]
fn a_planned_destroy_says_what_is_held_and_keeps_it() {
    let holds = holds(vec![summary(NAME, 3, 0)]);
    let held = destroys_held(&holds, NAME, false).unwrap();
    assert_eq!(
        held,
        Some(HeldOf {
            facts: 3,
            set_aside: 0
        })
    );
    assert!(holds.0.discarded.lock().unwrap().is_empty());
}

// H-Q8 (a): an applied destroy warns and discards.
#[test]
fn an_applied_destroy_says_what_was_held_and_discards_it() {
    let holds = holds(vec![summary(NAME, 3, 1)]);
    let held = destroys_held(&holds, NAME, true).unwrap();
    assert_eq!(
        held,
        Some(HeldOf {
            facts: 3,
            set_aside: 1
        })
    );
    assert_eq!(*holds.0.discarded.lock().unwrap(), [root(NAME)]);
}

#[test]
fn a_destroy_of_a_session_with_no_hold_touches_none() {
    let holds = holds(vec![summary(OTHER, 3, 0)]);
    assert_eq!(destroys_held(&holds, NAME, true).unwrap(), None);
    assert!(holds.0.discarded.lock().unwrap().is_empty());
}

#[test]
fn a_destroy_whose_discard_fails_says_so() {
    let holds = Holds(Fake {
        held: vec![summary(NAME, 3, 0)],
        discard_fails: true,
        ..Fake::default()
    });
    assert_eq!(destroys_held(&holds, NAME, true), Err(failure()));
}
