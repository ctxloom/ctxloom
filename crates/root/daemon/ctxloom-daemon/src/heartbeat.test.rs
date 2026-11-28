use std::sync::{Arc, Mutex};
use std::time::Duration;

use hook_listener::HealthStatus;

use super::{stale_after, status, stopped, watch, Heartbeat, ROUND_ALLOWANCE, STALE_POLLS};

// The bound follows the round period, so a daemon polled slowly is not
// called stale between its polls, and a quiet one is called stale at last.
#[test]
fn the_stale_bound_is_a_few_polls_and_an_allowance_for_a_slow_round() {
    let poll = Duration::from_secs(2);
    assert_eq!(
        stale_after(poll),
        poll * STALE_POLLS + ROUND_ALLOWANCE,
        "polls and the allowance"
    );
    assert!(stale_after(Duration::from_secs(10)) > stale_after(poll));
}

#[test]
fn a_beat_older_than_the_bound_is_not_serving_and_one_at_it_is_serving() {
    let bound = Duration::from_secs(5);
    assert_eq!(status(Duration::ZERO, bound), HealthStatus::Serving);
    assert_eq!(status(bound, bound), HealthStatus::Serving);
    assert_eq!(
        status(bound + Duration::from_millis(1), bound),
        HealthStatus::NotServing
    );
}

#[test]
fn a_heartbeat_ages_until_it_beats_again() {
    let beat = Heartbeat::new();
    std::thread::sleep(Duration::from_millis(30));
    assert!(beat.age() >= Duration::from_millis(30));
    beat.beat();
    assert!(beat.age() < Duration::from_millis(30));
}

// The reporter is the health service's: a loop that stops beating (a
// deadlock) turns it NOT_SERVING, and one that beats again turns it back.
#[tokio::test]
async fn a_loop_that_stops_beating_turns_the_reporter_not_serving_and_back() {
    let beat = Arc::new(Heartbeat::new());
    let said: Arc<Mutex<Vec<HealthStatus>>> = Arc::default();
    let reporter = {
        let (beat, said) = (Arc::clone(&beat), Arc::clone(&said));
        tokio::spawn(async move {
            watch(
                &beat,
                Duration::from_millis(80),
                Duration::from_millis(10),
                |now| {
                    let said = Arc::clone(&said);
                    async move { said.lock().unwrap().push(now) }
                },
            )
            .await;
        })
    };
    // Beating: serving, said once.
    for _ in 0..5 {
        tokio::time::sleep(Duration::from_millis(20)).await;
        beat.beat();
    }
    assert_eq!(*said.lock().unwrap(), [HealthStatus::Serving]);
    // The loop is stuck: no beat for longer than the bound.
    tokio::time::sleep(Duration::from_millis(250)).await;
    assert_eq!(
        *said.lock().unwrap(),
        [HealthStatus::Serving, HealthStatus::NotServing]
    );
    // It beats again.
    beat.beat();
    tokio::time::sleep(Duration::from_millis(40)).await;
    reporter.abort();
    assert_eq!(
        *said.lock().unwrap(),
        [
            HealthStatus::Serving,
            HealthStatus::NotServing,
            HealthStatus::Serving
        ]
    );
}

// The daemon's store closes only once everything that can reach it is let
// go (CLAUDE.md, "Storage is angzarr-store-sqlite"): the watch task is
// stopped before the daemon lets its listener and store go, and a stopped
// watch has dropped everything it held (its heartbeat, and whatever its
// reporter captured, as the listener's health handle is), none of it left
// for the close to find.
#[tokio::test]
async fn a_stopped_watch_has_let_go_of_everything_it_held() {
    let beat = Arc::new(Heartbeat::new());
    let held = Arc::new(());
    let task = {
        let (beat, held) = (Arc::clone(&beat), Arc::clone(&held));
        tokio::spawn(async move {
            watch(
                &beat,
                Duration::from_millis(50),
                Duration::from_millis(5),
                |_| {
                    let held = Arc::clone(&held);
                    async move {
                        let _kept = held;
                    }
                },
            )
            .await;
        })
    };
    tokio::time::sleep(Duration::from_millis(30)).await;
    stopped(task).await;
    assert_eq!(Arc::strong_count(&beat), 1, "the heartbeat is let go");
    assert_eq!(
        Arc::strong_count(&held),
        1,
        "the reporter's captures are let go"
    );
}
