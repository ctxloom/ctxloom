//! The terminal wake's journal on the session (owner ruling 2026-11-18): one
//! wake outstanding at a time, disarmed or redeemed, and the messages a
//! primary's hook delivered. The races (a disarm and a redeem of one wake)
//! are forced by running the transitions in each order, never by waiting.

use crate::aggregate::tests::Session;
use crate::{
    CloseSession, DeliverMessages, DisarmWake, IssueWake, MessageSent, MessagesDelivered,
    SessionEvent, WakeDisarmed, WakeIssued, WakeRedeemed,
};

const NONCE: &str = "0123456789abcdef";
const LATER: &str = "fedcba9876543210";

fn issue(nonce: &str) -> IssueWake {
    IssueWake {
        nonce: nonce.to_string(),
    }
}

fn disarm(nonce: &str) -> DisarmWake {
    DisarmWake {
        nonce: nonce.to_string(),
    }
}

fn deliver(ids: &[&str], nonce: &str) -> DeliverMessages {
    DeliverMessages {
        message_ids: ids.iter().map(ToString::to_string).collect(),
        wake_nonce: nonce.to_string(),
    }
}

fn issued() -> Session {
    let mut session = Session::minted();
    session.run(issue(NONCE)).unwrap();
    session
}

#[test]
fn a_wake_issued_is_recorded_and_is_the_outstanding_one() {
    let mut session = Session::minted();
    let events = session.run(issue(NONCE)).unwrap();
    assert_eq!(
        events,
        vec![SessionEvent::WakeIssued(WakeIssued {
            nonce: NONCE.to_string()
        })]
    );
    assert_eq!(session.state().wake_outstanding, NONCE);
}

// One outstanding at a time.
#[test]
fn a_second_wake_while_one_is_outstanding_is_refused_naming_it() {
    let mut session = issued();
    let (code, args) = session.run(issue(LATER)).unwrap_err();
    assert_eq!(code, "sessions-wake-outstanding");
    assert_eq!(args["nonce"], NONCE);
    assert_eq!(session.state().wake_outstanding, NONCE);
}

#[test]
fn a_wake_to_a_closed_session_or_no_session_is_refused() {
    let mut closed = Session::minted();
    closed
        .run(CloseSession {
            cause: "done".to_string(),
            requester: None,
        })
        .unwrap();
    assert_eq!(
        closed.run(issue(NONCE)).unwrap_err().0,
        "sessions-session-closed"
    );
    let mut none = Session::new();
    assert_eq!(none.run(issue(NONCE)).unwrap_err().0, "sessions-no-session");
}

// A wake not answered in time: disarmed, unanswered, and the next may go.
#[test]
fn a_disarmed_wake_is_unanswered_and_lets_the_next_wake_be_issued() {
    let mut session = issued();
    let events = session.run(disarm(NONCE)).unwrap();
    assert_eq!(
        events,
        vec![SessionEvent::WakeDisarmed(WakeDisarmed {
            nonce: NONCE.to_string()
        })]
    );
    assert_eq!(session.state().wake_outstanding, "");
    assert_eq!(session.state().wake_unanswered, NONCE);
    assert_eq!(session.run(issue(LATER)).unwrap().len(), 1);
    assert_eq!(session.state().wake_outstanding, LATER);
    assert_eq!(session.state().wake_unanswered, "", "a new wake is awaited");
}

#[test]
fn a_disarm_of_a_wake_not_outstanding_records_nothing() {
    let mut session = issued();
    assert!(session.run(disarm(LATER)).unwrap().is_empty());
    assert_eq!(session.state().wake_outstanding, NONCE);
}

// The race, one order: the prompt's hook redeems the wake, then its timer
// fires.
#[test]
fn a_redeem_then_a_disarm_leaves_the_wake_answered() {
    let mut session = issued();
    sent(&mut session, &["m1"]);
    let events = session.run(deliver(&["m1"], NONCE)).unwrap();
    assert_eq!(
        events,
        vec![
            SessionEvent::MessagesDelivered(MessagesDelivered {
                message_ids: vec!["m1".to_string()]
            }),
            SessionEvent::WakeRedeemed(WakeRedeemed {
                nonce: NONCE.to_string()
            }),
        ]
    );
    assert!(session.run(disarm(NONCE)).unwrap().is_empty());
    assert_eq!(session.state().wake_outstanding, "");
    assert_eq!(session.state().wake_unanswered, "");
}

// The race, the other order: the timer disarms the wake, then the turn it
// started arrives late and redeems it: answered after all.
#[test]
fn a_disarm_then_a_late_redeem_leaves_the_wake_answered() {
    let mut session = issued();
    session.run(disarm(NONCE)).unwrap();
    let events = session.run(deliver(&[], NONCE)).unwrap();
    assert_eq!(
        events,
        vec![SessionEvent::WakeRedeemed(WakeRedeemed {
            nonce: NONCE.to_string()
        })]
    );
    assert_eq!(session.state().wake_outstanding, "");
    assert_eq!(session.state().wake_unanswered, "");
}

// A human's prompt delivers what waits and redeems nothing.
/// `ids` sent to the session, each an inform.
fn sent(session: &mut Session, ids: &[&str]) {
    for id in ids {
        session.given([SessionEvent::MessageSent(MessageSent {
            message_id: id.to_string(),
            urgency: crate::Urgency::Inform,
            text: format!("text of {id}"),
            sender: None,
        })]);
    }
}

#[test]
fn a_delivery_with_no_wake_records_the_messages_alone() {
    let mut session = issued();
    sent(&mut session, &["m1", "m2"]);
    let events = session.run(deliver(&["m1", "m2"], "")).unwrap();
    assert_eq!(
        events,
        vec![SessionEvent::MessagesDelivered(MessagesDelivered {
            message_ids: vec!["m1".to_string(), "m2".to_string()]
        })]
    );
    assert_eq!(session.state().wake_outstanding, NONCE, "still awaited");
}

// A stale wake line (a nonce neither outstanding nor last unanswered) with
// nothing to deliver records nothing.
#[test]
fn a_delivery_of_nothing_for_a_stale_wake_records_nothing() {
    let mut session = issued();
    assert!(session.run(deliver(&[], LATER)).unwrap().is_empty());
    assert_eq!(session.state().wake_outstanding, NONCE);
}

#[test]
fn a_delivery_to_no_session_is_refused() {
    let mut session = Session::new();
    assert_eq!(
        session.run(deliver(&["m1"], "")).unwrap_err().0,
        "sessions-no-session"
    );
}

// The lean hook RPC (owner budget 2026-11-18): a message is delivered ONCE
// whatever asks again. Two hooks within one round both read it pending
// from a projection not yet caught up; the second's delivery records
// nothing of it. Forced by running both deliveries against one state.
#[test]
fn test_a_message_is_delivered_once_whatever_asks_again() {
    let mut session = Session::minted();
    sent(&mut session, &["m1"]);
    assert_eq!(session.state().pending_messages, ["m1"]);
    let first = session.run(deliver(&["m1"], "")).unwrap();
    assert_eq!(
        first,
        vec![SessionEvent::MessagesDelivered(MessagesDelivered {
            message_ids: vec!["m1".to_string()]
        })]
    );
    assert!(session.state().pending_messages.is_empty());
    assert!(
        session.run(deliver(&["m1"], "")).unwrap().is_empty(),
        "delivered already: nothing"
    );
    sent(&mut session, &["m2"]);
    let both = session.run(deliver(&["m1", "m2"], "")).unwrap();
    assert_eq!(
        both,
        vec![SessionEvent::MessagesDelivered(MessagesDelivered {
            message_ids: vec!["m2".to_string()]
        })],
        "only what is still pending"
    );
}

// An id never sent is not delivered; a wake it carries is still redeemed.
#[test]
fn test_an_id_never_sent_is_not_delivered() {
    let mut session = issued();
    let events = session.run(deliver(&["never"], NONCE)).unwrap();
    assert_eq!(
        events,
        vec![SessionEvent::WakeRedeemed(WakeRedeemed {
            nonce: NONCE.to_string()
        })]
    );
}
