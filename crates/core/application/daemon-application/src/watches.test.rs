//! The sessions watched for the terminal wake (owner ruling 2026-11-18).
//! A watcher that waits is forced to wait before the change it is woken by:
//! the test sees it parked ([`SessionWatches::waiting`]) first, never a
//! sleep.

use std::sync::Arc;
use std::time::Duration;

use daemon_proto::v1::SessionSignal;
use pending_messages::PendingMessage;
use sessions::{SessionState, Urgency};

use crate::watches::{signal_of, SessionWatches};

const S: &str = "07070707-0707-0707-0707-070707070707";
const LONG: Duration = Duration::from_secs(600);

fn signal(wakes: &[&str], outstanding: &str) -> SessionSignal {
    SessionSignal {
        wakes: wakes.iter().map(ToString::to_string).collect(),
        outstanding_wake: outstanding.to_string(),
    }
}

#[test]
fn a_watch_yields_the_signal_it_opened_with_then_only_a_change() {
    let watches = SessionWatches::new(LONG);
    let mut watch = watches.watch(S, signal(&["m1"], ""));
    assert_eq!(watch.poll(), Some(signal(&["m1"], "")));
    assert_eq!(watch.poll(), None, "nothing changed");
    watches.publish(S, signal(&["m1"], ""));
    assert_eq!(watch.poll(), None, "the same signal is no change");
    watches.publish(S, signal(&["m1"], "0123456789abcdef"));
    assert_eq!(watch.poll(), Some(signal(&["m1"], "0123456789abcdef")));
}

#[test]
fn a_session_is_watched_while_a_watch_of_it_is_open() {
    let watches = SessionWatches::new(LONG);
    assert!(!watches.is_watched(S));
    let first = watches.watch(S, signal(&[], ""));
    let second = watches.watch(S, signal(&[], ""));
    assert!(watches.is_watched(S));
    assert_eq!(watches.watched(), [S.to_string()].into());
    drop(first);
    assert!(watches.is_watched(S), "another watch is still open");
    drop(second);
    assert!(!watches.is_watched(S));
    assert!(watches.watched().is_empty());
}

#[test]
fn a_publish_to_a_session_nobody_watches_holds_nothing() {
    let watches = SessionWatches::new(LONG);
    watches.publish(S, signal(&["m1"], ""));
    assert!(!watches.is_watched(S));
    let mut watch = watches.watch(S, signal(&[], ""));
    assert_eq!(watch.poll(), Some(signal(&[], "")), "what it opened with");
}

// The race: the watcher is waiting when the change is published, and the
// publish wakes it.
#[test]
fn a_waiting_watcher_is_woken_by_a_change() {
    let watches = SessionWatches::new(LONG);
    let mut watch = watches.watch(S, signal(&[], ""));
    assert_eq!(watch.next(), Some(signal(&[], "")));
    std::thread::scope(|scope| {
        let waiter = scope.spawn(move || watch.next());
        while watches.waiting() == 0 {
            std::thread::yield_now();
        }
        watches.publish(S, signal(&["m2"], ""));
        assert_eq!(waiter.join().unwrap(), Some(signal(&["m2"], "")));
    });
}

// Unchanged, the signal is sent again each keepalive: a watcher whose
// client is gone is noticed when that send fails.
#[test]
fn an_unchanged_signal_is_sent_again_after_the_keepalive() {
    let watches = SessionWatches::new(Duration::ZERO);
    let mut watch = watches.watch(S, signal(&["m1"], ""));
    assert_eq!(watch.next(), Some(signal(&["m1"], "")));
    assert_eq!(watch.next(), Some(signal(&["m1"], "")), "the keepalive");
}

fn pending(id: &str, urgency: Urgency) -> PendingMessage {
    PendingMessage {
        message_id: id.to_string(),
        urgency,
        text: String::new(),
        sender: None,
    }
}

#[test]
fn the_signal_is_the_pending_wakes_in_order_and_the_outstanding_wake() {
    let state = SessionState {
        wake_outstanding: "0123456789abcdef".to_string(),
        ..SessionState::default()
    };
    let messages = [
        pending("m1", Urgency::Wake),
        pending("m2", Urgency::Inform),
        pending("m3", Urgency::Steer),
        pending("m4", Urgency::Wake),
    ];
    assert_eq!(
        signal_of(&messages, Some(&state)),
        signal(&["m1", "m4"], "0123456789abcdef")
    );
    assert_eq!(signal_of(&messages, None), signal(&["m1", "m4"], ""));
}

#[test]
fn the_watches_are_shared() {
    let watches: Arc<SessionWatches> = SessionWatches::new(LONG);
    let again = Arc::clone(&watches);
    let _watch = watches.watch(S, signal(&[], ""));
    assert!(again.is_watched(S));
}
