//! The terminal wake's poster, decided (owner ruling 2026-11-18): one wake
//! outstanding at a time, disarmed after its time, each pending wake woken
//! for once. Every race (a redeem against the timer, a signal lagging the
//! issue) is forced by feeding the events in the race's order.

use daemon_proto::v1::SessionSignal;

use crate::waker::{WakeAct, Waker};

const N1: &str = "0123456789abcdef";
const N2: &str = "fedcba9876543210";

fn signal(wakes: &[&str], outstanding: &str) -> SessionSignal {
    SessionSignal {
        wakes: wakes.iter().map(ToString::to_string).collect(),
        outstanding_wake: outstanding.to_string(),
    }
}

fn mint(nonce: &'static str) -> impl FnOnce() -> String {
    move || nonce.to_string()
}

fn never() -> impl FnOnce() -> String {
    || panic!("no wake is due")
}

/// A waker that woke for `m1` with `N1`.
fn woken() -> Waker {
    let mut waker = Waker::new();
    assert_eq!(
        waker.heard(&signal(&["m1"], ""), mint(N1)),
        Some(WakeAct::Wake(N1.to_string()))
    );
    waker
}

#[test]
fn a_pending_wake_with_none_outstanding_is_woken_for() {
    let waker = woken();
    assert_eq!(waker.armed(), Some(N1));
}

#[test]
fn nothing_pending_wakes_nothing() {
    let mut waker = Waker::new();
    assert_eq!(waker.heard(&signal(&[], ""), never()), None);
    assert_eq!(waker.armed(), None);
}

// One outstanding at a time: while armed, a new wake waits.
#[test]
fn a_new_message_while_a_wake_is_armed_wakes_nothing() {
    let mut waker = woken();
    assert_eq!(waker.heard(&signal(&["m1", "m2"], N1), never()), None);
    assert_eq!(
        waker.heard(&signal(&["m1", "m2"], ""), never()),
        None,
        "lagging"
    );
}

// The signal lags the issue: one that does not show the wake yet does not
// clear it, so its timer still disarms it.
#[test]
fn a_signal_from_before_the_issue_leaves_the_wake_armed() {
    let mut waker = woken();
    assert_eq!(waker.heard(&signal(&["m1"], ""), never()), None);
    assert_eq!(waker.armed(), Some(N1));
    assert_eq!(waker.expired(N1), Some(WakeAct::Disarm(N1.to_string())));
}

// The race, one order: its turn started (the wake redeemed) before its time
// ran out; the timer then disarms nothing.
#[test]
fn a_wake_redeemed_before_its_time_is_not_disarmed() {
    let mut waker = woken();
    assert_eq!(waker.heard(&signal(&["m1"], N1), never()), None);
    assert_eq!(waker.heard(&signal(&[], ""), never()), None);
    assert_eq!(waker.armed(), None);
    assert_eq!(waker.expired(N1), None);
}

// The race, the other order: its time ran out first; it is disarmed, and
// what it woke for waits for the next prompt, never woken for again.
#[test]
fn a_wake_whose_time_ran_out_is_disarmed_and_its_messages_wait() {
    let mut waker = woken();
    assert_eq!(waker.heard(&signal(&["m1"], N1), never()), None);
    assert_eq!(waker.expired(N1), Some(WakeAct::Disarm(N1.to_string())));
    assert_eq!(waker.armed(), None);
    assert_eq!(waker.expired(N1), None, "once");
    assert_eq!(waker.heard(&signal(&["m1"], N1), never()), None, "lagging");
    assert_eq!(waker.heard(&signal(&["m1"], ""), never()), None, "m1 waits");
    assert_eq!(
        waker.heard(&signal(&["m1", "m2"], ""), mint(N2)),
        Some(WakeAct::Wake(N2.to_string())),
        "a new message is woken for"
    );
}

// A wake whose post failed is forgotten: nothing to disarm, and its
// messages wait for the next prompt (a socket that is gone is not tried
// at every signal).
#[test]
fn a_wake_not_posted_is_forgotten() {
    let mut waker = Waker::new();
    assert!(waker.heard(&signal(&["m1"], ""), mint(N1)).is_some());
    waker.failed(N1);
    assert_eq!(waker.armed(), None);
    assert_eq!(waker.expired(N1), None);
    assert_eq!(waker.heard(&signal(&["m1"], ""), never()), None);
}

// A wake outstanding when the poster starts is a gone poster's (the session
// had one poster, its MCP server, which started again): disarmed, so the
// session can be woken again.
#[test]
fn a_wake_outstanding_at_the_first_signal_is_disarmed() {
    let mut waker = Waker::new();
    assert_eq!(
        waker.heard(&signal(&["m1"], N2), never()),
        Some(WakeAct::Disarm(N2.to_string()))
    );
    assert_eq!(waker.heard(&signal(&["m1"], ""), never()), None, "m1 waits");
}

// After the first signal, a wake outstanding that is not this poster's is
// waited out, never disarmed or doubled.
#[test]
fn a_wake_outstanding_later_that_is_not_ours_is_waited_out() {
    let mut waker = Waker::new();
    assert_eq!(waker.heard(&signal(&[], ""), never()), None);
    assert_eq!(waker.heard(&signal(&["m1"], N2), never()), None);
}

// A wake the daemon could not issue (not reached in time) is tried again at
// the next signal (the keepalive repeats it), with a nonce of its own.
#[test]
fn a_wake_not_issued_is_tried_again_at_the_next_signal() {
    let mut waker = Waker::new();
    assert!(waker.heard(&signal(&["m1"], ""), mint(N1)).is_some());
    waker.not_issued(N1);
    assert_eq!(waker.armed(), None);
    assert_eq!(waker.expired(N1), None);
    assert_eq!(
        waker.heard(&signal(&["m1"], ""), mint(N2)),
        Some(WakeAct::Wake(N2.to_string()))
    );
}
