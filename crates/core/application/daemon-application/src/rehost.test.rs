//! The re-host's evidence: the re-hosts in the hour before the loss, by
//! their recorded instants (S47).

use sessions::{IncarnationEnded, IncarnationOwed, SessionEvent};

use super::{budget_notice, recent_rehosts, Recorded};

fn owed(reason: &str) -> SessionEvent {
    SessionEvent::IncarnationOwed(IncarnationOwed {
        incarnation: 2,
        reason: reason.to_string(),
    })
}

fn ended() -> SessionEvent {
    SessionEvent::IncarnationEnded(IncarnationEnded {
        incarnation: 1,
        cause: "orphaned".to_string(),
        notice_parent: true,
    })
}

// The hour before the last loss counts, its edges included; one older, a
// drain's and the first owe are not counted.
#[test]
fn the_rehosts_in_the_hour_before_the_loss_are_counted() {
    let lost = 10_000;
    let events: Recorded = vec![
        (lost - 3_601, owed("rehost")),
        (lost - 3_600, owed("rehost")),
        (lost - 10, owed("drain")),
        (lost - 5, owed("first")),
        (lost - 1, owed("rehost")),
        (lost, owed("rehost")),
        (lost, ended()),
    ];
    assert_eq!(recent_rehosts(&events), 3);
}

// The loss is the LAST end: re-hosts after an earlier end and before it
// count, and none is counted with no loss at all.
#[test]
fn the_window_ends_at_the_last_loss_and_no_loss_counts_none() {
    let events: Recorded = vec![(100, ended()), (5_000, owed("rehost")), (5_001, ended())];
    assert_eq!(recent_rehosts(&events), 1);
    let later: Recorded = vec![(5_002, owed("rehost")), (5_001, ended())];
    assert_eq!(recent_rehosts(&later), 0, "after the loss is not before it");
    assert_eq!(recent_rehosts(&vec![(1, owed("rehost"))]), 0);
}

#[test]
fn the_owners_notice_says_the_child_ended_and_why() {
    let said = budget_notice("kid", 60);
    assert!(said.starts_with("child kid ended"), "{said}");
    assert!(
        said.contains("60 times") && said.contains("limit 60"),
        "{said}"
    );
}
