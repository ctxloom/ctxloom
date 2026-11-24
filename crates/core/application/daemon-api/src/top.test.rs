//! `WatchTop` (`ctxloom top`, DRAFT): a snapshot, then what changes, pushed
//! from the session index when it folds and never polled.

use daemon_proto::v1;

use crate::server::tests::{fixture, refused, resolve, Fixture, COMMIT};
use session_index::State;

const PROJECT: &str = "projects/calm-quiet-heron";

async fn opened(harps: &[&str]) -> Fixture {
    let f = fixture(vec!["calm-quiet-heron"]);
    f.api.resolve_project(resolve(COMMIT, "")).await.unwrap();
    for harp in harps {
        f.mint(harp).await;
    }
    f.catch_up().await;
    f
}

fn request() -> v1::WatchTopRequest {
    v1::WatchTopRequest {
        parent: PROJECT.to_string(),
        ..v1::WatchTopRequest::default()
    }
}

fn session_name(harp: &str) -> String {
    format!("{PROJECT}/sessions/{harp}")
}

fn snapshot_of(event: v1::TopEvent) -> v1::TopSnapshot {
    match event.event {
        Some(v1::top_event::Event::Snapshot(snapshot)) => snapshot,
        other => panic!("not a snapshot: {other:?}"),
    }
}

fn names(snapshot: &v1::TopSnapshot) -> Vec<String> {
    snapshot.sessions.iter().map(|s| s.name.clone()).collect()
}

#[tokio::test]
async fn the_first_event_is_a_snapshot_of_the_projects_live_sessions() {
    let f = opened(&["amber-swift-owl", "ended-quiet-wren"]).await;
    let mut stream = f.api.watch_top(request()).await.unwrap();
    let snapshot = snapshot_of(stream.next().unwrap());
    assert_eq!(names(&snapshot), vec![session_name("amber-swift-owl")]);
    assert_eq!(snapshot.sessions[0].state, v1::session::State::Live as i32);
}

#[tokio::test]
async fn a_session_that_is_minted_after_arrives_as_session_changed_when_the_index_folds() {
    let f = opened(&["amber-swift-owl"]).await;
    let mut stream = f.api.watch_top(request()).await.unwrap();
    snapshot_of(stream.next().unwrap());
    f.mint("brisk-copper-moth").await;
    f.catch_up().await;
    match stream.next().unwrap().event {
        Some(v1::top_event::Event::SessionChanged(changed)) => {
            assert_eq!(
                changed.session.unwrap().name,
                session_name("brisk-copper-moth")
            );
        }
        other => panic!("not session_changed: {other:?}"),
    }
}

#[tokio::test]
async fn a_lock_let_go_just_after_the_fold_that_woke_the_watch_is_still_seen() {
    let f = opened(&["amber-swift-owl"]).await;
    let mut stream = f.api.watch_top(request()).await.unwrap();
    let stop = stream.stopper();
    snapshot_of(stream.next().unwrap());
    let (told, events) = std::sync::mpsc::channel();
    let reading = tokio::task::spawn_blocking(move || {
        for event in stream {
            if told.send(event).is_err() {
                return;
            }
        }
    });
    // A fold wakes it; the lock of the other session is let go after.
    f.mint("brisk-copper-moth").await;
    f.catch_up().await;
    tokio::time::sleep(std::time::Duration::from_millis(30)).await;
    f.locks
        .0
        .lock()
        .unwrap()
        .insert("amber-swift-owl".to_string(), State::Ended);
    let deadline = tokio::time::Instant::now() + std::time::Duration::from_secs(5);
    let mut removed = false;
    while !removed && tokio::time::Instant::now() < deadline {
        if let Ok(event) = events.recv_timeout(std::time::Duration::from_millis(100)) {
            removed = matches!(
                event.event,
                Some(v1::top_event::Event::SessionRemoved(ref gone))
                    if gone.name == session_name("amber-swift-owl")
            );
        }
    }
    drop(events);
    stop.stop();
    reading.await.unwrap();
    assert!(removed, "the released lock was never seen");
}

#[tokio::test]
async fn a_lock_let_go_long_after_every_fold_is_seen_when_the_index_is_told() {
    let f = opened(&["amber-swift-owl"]).await;
    let mut stream = f.api.watch_top(request()).await.unwrap();
    let stop = stream.stopper();
    snapshot_of(stream.next().unwrap());
    let (told, events) = std::sync::mpsc::channel();
    let reading = tokio::task::spawn_blocking(move || {
        for event in stream {
            if told.send(event).is_err() {
                return;
            }
        }
    });
    // Past the looks the watch takes after a wake: only a notice can tell it.
    tokio::time::sleep(std::time::Duration::from_millis(2300)).await;
    f.locks
        .0
        .lock()
        .unwrap()
        .insert("amber-swift-owl".to_string(), State::Ended);
    assert!(
        events
            .recv_timeout(std::time::Duration::from_millis(300))
            .is_err(),
        "nothing told it yet"
    );
    f.index.notify();
    let event = events
        .recv_timeout(std::time::Duration::from_secs(3))
        .expect("the notice woke the watch");
    assert!(
        matches!(
            event.event,
            Some(v1::top_event::Event::SessionRemoved(ref gone))
                if gone.name == session_name("amber-swift-owl")
        ),
        "{event:?}"
    );
    drop(events);
    stop.stop();
    reading.await.unwrap();
}

#[tokio::test]
async fn a_session_that_stops_matching_is_removed_with_the_reason_left_filter() {
    let f = opened(&["amber-swift-owl"]).await;
    let mut stream = f.api.watch_top(request()).await.unwrap();
    snapshot_of(stream.next().unwrap());
    f.locks
        .0
        .lock()
        .unwrap()
        .insert("amber-swift-owl".to_string(), State::Ended);
    f.mint("brisk-copper-moth").await;
    f.catch_up().await;
    let mut removed = None;
    for _ in 0..2 {
        if let Some(v1::top_event::Event::SessionRemoved(gone)) = stream.next().unwrap().event {
            removed = Some(gone);
        }
    }
    let gone = removed.expect("the ended session left the live view");
    assert_eq!(gone.name, session_name("amber-swift-owl"));
    assert_eq!(
        gone.reason,
        v1::top_session_removed::Reason::LeftFilter as i32
    );
}

#[tokio::test]
async fn a_view_that_asks_for_ended_sessions_has_them() {
    let f = opened(&["amber-swift-owl", "ended-quiet-wren"]).await;
    let mut stream = f
        .api
        .watch_top(v1::WatchTopRequest {
            states: vec![v1::session::State::Ended as i32],
            ..request()
        })
        .await
        .unwrap();
    let snapshot = snapshot_of(stream.next().unwrap());
    assert_eq!(names(&snapshot), vec![session_name("ended-quiet-wren")]);
}

#[tokio::test]
async fn a_one_session_scope_holds_that_session_alone() {
    let f = opened(&["amber-swift-owl", "brisk-copper-moth"]).await;
    let mut stream = f
        .api
        .watch_top(v1::WatchTopRequest {
            session: session_name("brisk-copper-moth"),
            ..request()
        })
        .await
        .unwrap();
    let snapshot = snapshot_of(stream.next().unwrap());
    assert_eq!(names(&snapshot), vec![session_name("brisk-copper-moth")]);
}

#[tokio::test]
async fn a_watch_whose_client_is_gone_ends_though_nothing_changes() {
    let f = opened(&["amber-swift-owl"]).await;
    let mut stream = f.api.watch_top(request()).await.unwrap();
    let stop = stream.stopper();
    let _snapshot = stream.next().unwrap();
    // It parks on the models, which say nothing more.
    let parked = tokio::task::spawn_blocking(move || stream.next());
    tokio::time::sleep(std::time::Duration::from_millis(200)).await;
    assert!(!parked.is_finished(), "it ended with nothing said");
    stop.stop();
    let ended = tokio::time::timeout(std::time::Duration::from_secs(5), parked)
        .await
        .expect("the stopped watch ends")
        .unwrap();
    assert!(ended.is_none());
}

#[tokio::test]
async fn an_owner_filter_naming_a_session_nobody_has_is_refused() {
    let f = opened(&["amber-swift-owl"]).await;
    let (_, missing): (_, v1::SessionNotFound) = refused(
        f.api
            .watch_top(v1::WatchTopRequest {
                owners: vec!["far-heron".to_string()],
                ..request()
            })
            .await
            .map(|_| ()),
    );
    assert!(
        missing.name.ends_with("/sessions/far-heron"),
        "{}",
        missing.name
    );
}

#[tokio::test]
async fn a_scope_naming_a_session_nobody_has_is_refused() {
    let f = opened(&["amber-swift-owl"]).await;
    let (_, missing): (_, v1::SessionNotFound) = refused(
        f.api
            .watch_top(v1::WatchTopRequest {
                session: session_name("far-heron"),
                ..request()
            })
            .await
            .map(|_| ()),
    );
    assert_eq!(missing.name, session_name("far-heron"));
}

#[tokio::test]
async fn a_log_asked_for_a_session_nobody_has_is_refused_and_not_waited_for() {
    let f = opened(&["amber-swift-owl"]).await;
    let (_, missing): (_, v1::SessionNotFound) = refused(
        f.api
            .watch_top(v1::WatchTopRequest {
                log_session: session_name("far-heron"),
                ..request()
            })
            .await
            .map(|_| ()),
    );
    assert_eq!(missing.name, session_name("far-heron"));
}

#[tokio::test]
async fn a_filter_on_an_agent_keeps_only_sessions_whose_loadout_names_it() {
    let f = opened(&["amber-swift-owl"]).await;
    let mut stream = f
        .api
        .watch_top(v1::WatchTopRequest {
            agents: vec!["reviewer".to_string()],
            ..request()
        })
        .await
        .unwrap();
    let snapshot = snapshot_of(stream.next().unwrap());
    assert!(snapshot.sessions.is_empty(), "no loadout, no agent");
}

#[tokio::test]
async fn the_snapshot_of_a_project_with_nothing_hosted_is_empty() {
    let f = opened(&[]).await;
    let mut stream = f.api.watch_top(request()).await.unwrap();
    let snapshot = snapshot_of(stream.next().unwrap());
    assert!(snapshot.sessions.is_empty() && snapshot.approvals.is_empty());
}

use v1::log_lifecycle::Kind;

/// The kind of lifecycle entry `log` holds, if it holds one.
fn lifecycle_of(log: &v1::TopLog) -> Option<Kind> {
    match log.entry.as_ref()?.entry.as_ref()? {
        v1::log_entry::Entry::Lifecycle(l) => Kind::try_from(l.kind).ok(),
        _ => None,
    }
}

fn log_event(event: &Option<v1::top_event::Event>) -> Option<&v1::TopLog> {
    match event {
        Some(v1::top_event::Event::Log(log)) => Some(log),
        _ => None,
    }
}

/// A watch read on a thread of its own, so a news that never comes fails the
/// test instead of hanging it.
struct Read {
    events: std::sync::mpsc::Receiver<v1::TopEvent>,
    stop: crate::TopStop,
    reading: tokio::task::JoinHandle<()>,
}

impl Read {
    fn of(stream: crate::TopStream) -> Read {
        let stop = stream.stopper();
        let (told, events) = std::sync::mpsc::channel();
        let reading = tokio::task::spawn_blocking(move || {
            for event in stream {
                if told.send(event).is_err() {
                    return;
                }
            }
        });
        Read {
            events,
            stop,
            reading,
        }
    }

    fn next(&self) -> v1::TopEvent {
        self.events
            .recv_timeout(std::time::Duration::from_secs(5))
            .expect("the watch told nothing more")
    }

    async fn end(self) {
        drop(self.events);
        self.stop.stop();
        self.reading.await.unwrap();
    }
}

fn watching_every_log() -> v1::WatchTopRequest {
    v1::WatchTopRequest {
        all_logs: true,
        ..request()
    }
}

#[tokio::test]
async fn a_leaving_sessions_pending_log_entries_are_told_before_its_removal() {
    let f = opened(&["amber-swift-owl"]).await;
    let mut stream = f.api.watch_top(watching_every_log()).await.unwrap();
    snapshot_of(stream.next().unwrap());
    let read = Read::of(stream);
    let started = read.next();
    assert!(log_event(&started.event).is_some_and(|log| lifecycle_of(log) == Some(Kind::Started)));
    // Its last word is folded into the timeline in the very look that finds
    // its lock let go: the entry is pending as the session leaves.
    f.close("amber-swift-owl").await;
    f.locks
        .0
        .lock()
        .unwrap()
        .insert("amber-swift-owl".to_string(), State::Ended);
    f.catch_up_timelines().await;
    let told = [read.next().event, read.next().event];
    read.end().await;
    assert!(
        log_event(&told[0]).is_some_and(|log| lifecycle_of(log) == Some(Kind::Closed)),
        "its last entry comes first: {told:?}"
    );
    assert!(
        matches!(&told[1], Some(v1::top_event::Event::SessionRemoved(gone))
            if gone.name == session_name("amber-swift-owl")),
        "then its removal: {told:?}"
    );
}

#[tokio::test]
async fn a_log_entry_folded_after_its_session_left_is_still_told() {
    let f = opened(&["amber-swift-owl"]).await;
    let mut stream = f.api.watch_top(watching_every_log()).await.unwrap();
    snapshot_of(stream.next().unwrap());
    stream.next().unwrap();
    let read = Read::of(stream);
    // The lock is let go first; the word that explains it is folded later.
    f.locks
        .0
        .lock()
        .unwrap()
        .insert("amber-swift-owl".to_string(), State::Ended);
    f.index.notify();
    let gone = read.next();
    assert!(matches!(
        gone.event,
        Some(v1::top_event::Event::SessionRemoved(_))
    ));
    f.close("amber-swift-owl").await;
    f.catch_up_timelines().await;
    let log = read.next();
    read.end().await;
    let told = log_event(&log.event).unwrap_or_else(|| panic!("not a log line: {log:?}"));
    assert_eq!(lifecycle_of(told), Some(Kind::Closed));
    assert!(told.session.ends_with("/sessions/amber-swift-owl"));
}

fn stamped(seconds: i64) -> Option<daemon_proto::google::protobuf::Timestamp> {
    Some(daemon_proto::google::protobuf::Timestamp { seconds, nanos: 0 })
}

fn session_at(state: v1::session::State, start: i64, end: Option<i64>) -> v1::Session {
    v1::Session {
        state: state as i32,
        start_time: stamped(start),
        end_time: end.and_then(stamped),
        ..v1::Session::default()
    }
}

#[test]
fn test_the_timelines_latest_status_entry_is_the_state_change_time() {
    let session = session_at(v1::session::State::Live, 100, None);
    let latest = Some(angzarr_embedded::Timestamp {
        seconds: 250,
        nanos: 0,
    });
    assert_eq!(super::state_change_time(latest, &session), stamped(250));
}

#[test]
fn test_with_no_status_entry_a_live_session_changed_state_when_it_started() {
    let session = session_at(v1::session::State::Live, 100, None);
    assert_eq!(super::state_change_time(None, &session), stamped(100));
}

#[test]
fn test_with_no_status_entry_an_ended_session_changed_state_when_it_ended() {
    let session = session_at(v1::session::State::Ended, 100, Some(400));
    assert_eq!(super::state_change_time(None, &session), stamped(400));
}

#[test]
fn test_an_ended_session_with_no_end_time_falls_back_to_its_start() {
    let session = session_at(v1::session::State::Ended, 100, None);
    assert_eq!(super::state_change_time(None, &session), stamped(100));
}

#[tokio::test]
async fn test_the_snapshot_carries_the_time_of_the_sessions_started_entry() {
    let f = opened(&["amber-swift-owl"]).await;
    f.catch_up_timelines().await;
    let mut stream = f.api.watch_top(watching_every_log()).await.unwrap();
    let snapshot = snapshot_of(stream.next().unwrap());
    let read = Read::of(stream);
    let started = read.next();
    let log = log_event(&started.event)
        .expect("the started entry")
        .clone();
    read.end().await;
    assert_eq!(lifecycle_of(&log), Some(Kind::Started));
    assert_eq!(snapshot.sessions[0].state_change_time, log.time);
}
