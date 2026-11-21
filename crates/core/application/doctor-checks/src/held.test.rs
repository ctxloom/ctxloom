//! `HELD-FACTS-h1` (s4-Q14, H4): what doctor says of this home's holds.

use daemon_proto::google::protobuf::Timestamp;
use run_ports::{HoldError, HoldSummary};
use sessions::SessionName;

use crate::checks::Status;
use crate::held::{held_facts, MARKER, STALE_AFTER_SECONDS};

const OWN: &str = "v0.4.0-abc1234";
// 2026-11-15T00:00:00Z.
const NOW: i64 = 1_794_700_800;
const NAME: &str = "projects/calm-otter/sessions/brisk-copper-moth";

fn at(seconds: i64) -> Option<Timestamp> {
    Some(Timestamp { seconds, nanos: 0 })
}

/// A hold of `facts` facts of `name`, held by `stamps`, the oldest `ago`
/// seconds before [`NOW`].
fn hold(name: &str, facts: u64, stamps: &[&str], ago: i64) -> HoldSummary {
    HoldSummary {
        root: SessionName::parse(name).unwrap().id(),
        session: Some(name.to_string()),
        facts,
        gaps: 0,
        bytes: 4_096,
        stamps: stamps.iter().map(|s| s.to_string()).collect(),
        oldest: at(NOW - ago),
        set_aside: 0,
        unreadable: false,
        bad_tail: false,
    }
}

#[test]
fn nothing_held_is_ok() {
    let row = held_facts(&Ok(Vec::new()), OWN, NOW);
    assert_eq!(row.marker, MARKER);
    assert_eq!(row.status, Status::Ok);
    assert!(row.detail.contains("no fact is held"), "{}", row.detail);
    assert!(row.remedy.is_empty());
}

// Facts a daemon of their version takes, held a moment: the next command
// delivers them, so the row informs and warns of nothing.
#[test]
fn facts_a_daemon_of_their_version_takes_held_a_moment_are_info() {
    let row = held_facts(&Ok(vec![hold(NAME, 3, &[OWN], 60)]), OWN, NOW);
    assert_eq!(row.status, Status::Info, "{}", row.detail);
    for said in [
        "brisk-copper-moth",
        "3 fact",
        "4096 byte",
        OWN,
        "2026-11-14T23:59:00Z",
    ] {
        assert!(row.detail.contains(said), "{said}: {}", row.detail);
    }
    assert!(row.remedy.is_empty(), "{}", row.remedy);
}

// An older version's facts are this version's to deliver too (H-Q1 (b)).
#[test]
fn facts_of_an_older_ctxloom_are_info() {
    let row = held_facts(&Ok(vec![hold(NAME, 1, &["v0.3.9-def5678"], 60)]), OWN, NOW);
    assert_eq!(row.status, Status::Info, "{}", row.detail);
}

// Held more than an hour: something keeps them from a daemon.
#[test]
fn facts_held_more_than_an_hour_warn() {
    let row = held_facts(
        &Ok(vec![hold(NAME, 2, &[OWN], STALE_AFTER_SECONDS + 1)]),
        OWN,
        NOW,
    );
    assert_eq!(row.status, Status::Warn);
    assert!(row.detail.contains("more than an hour"), "{}", row.detail);
    assert!(!row.remedy.is_empty());
    let fresh = held_facts(
        &Ok(vec![hold(NAME, 2, &[OWN], STALE_AFTER_SECONDS)]),
        OWN,
        NOW,
    );
    assert_eq!(
        fresh.status,
        Status::Info,
        "an hour exactly: {}",
        fresh.detail
    );
}

// H-Q7 (a): facts of a newer ctxloom than this one, which no daemon of this
// version takes (a downgrade), warn, naming the newer version.
#[test]
fn facts_of_a_newer_ctxloom_warn_naming_it() {
    let row = held_facts(&Ok(vec![hold(NAME, 3, &["v0.5.0-fed4321"], 60)]), OWN, NOW);
    assert_eq!(row.status, Status::Warn);
    for said in ["brisk-copper-moth", "3 fact", "v0.5.0-fed4321", "newer"] {
        assert!(row.detail.contains(said), "{said}: {}", row.detail);
    }
}

#[test]
fn gaps_set_aside_parts_an_unreadable_hold_and_a_bad_tail_each_warn() {
    let gapped = HoldSummary {
        gaps: 2,
        ..hold(NAME, 1, &[OWN], 60)
    };
    let aside = HoldSummary {
        set_aside: 1,
        ..hold(NAME, 0, &[], 60)
    };
    let unreadable = HoldSummary {
        unreadable: true,
        session: None,
        oldest: None,
        ..hold(NAME, 0, &[], 0)
    };
    let torn = HoldSummary {
        bad_tail: true,
        ..hold(NAME, 1, &[OWN], 60)
    };
    for (summary, said) in [
        (gapped, "2 fact(s) dropped"),
        (aside, "1 set-aside part"),
        (unreadable, "cannot read"),
        (torn, "tail"),
    ] {
        let row = held_facts(&Ok(vec![summary]), OWN, NOW);
        assert_eq!(row.status, Status::Warn, "{said}: {}", row.detail);
        assert!(row.detail.contains(said), "{said}: {}", row.detail);
        assert!(!row.remedy.is_empty(), "{said}");
    }
}

// A hold whose session no fact names is named by its root.
#[test]
fn a_hold_no_fact_names_is_named_by_its_root() {
    let summary = HoldSummary {
        session: None,
        set_aside: 1,
        ..hold(NAME, 0, &[], 60)
    };
    let root = summary.root.to_string();
    let row = held_facts(&Ok(vec![summary]), OWN, NOW);
    assert!(row.detail.contains(&root), "{}", row.detail);
}

// Every hold is named; one warning makes the row a warning.
#[test]
fn each_hold_is_named_and_one_warning_warns() {
    let other = "projects/calm-otter/sessions/amber-swift-owl";
    let row = held_facts(
        &Ok(vec![
            hold(NAME, 3, &[OWN], 60),
            hold(other, 1, &["v0.5.0-fed4321"], 60),
        ]),
        OWN,
        NOW,
    );
    assert_eq!(row.status, Status::Warn);
    assert!(row.detail.contains("brisk-copper-moth"), "{}", row.detail);
    assert!(row.detail.contains("amber-swift-owl"), "{}", row.detail);
}

#[test]
fn holds_that_cannot_be_read_warn_saying_why() {
    let row = held_facts(
        &Err(HoldError::Io {
            path: "/h/.ctxloom/held".to_string(),
            ext_err_txt: "Permission denied (os error 13)".to_string(),
        }),
        OWN,
        NOW,
    );
    assert_eq!(row.status, Status::Warn);
    assert!(row.detail.contains("Permission denied"), "{}", row.detail);
}
