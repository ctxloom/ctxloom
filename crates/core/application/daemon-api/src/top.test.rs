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
