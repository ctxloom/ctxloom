//! `WatchSession` and what `GetSession` says of the terminal wake (owner
//! ruling 2026-11-18).

use std::sync::Arc;
use std::time::Duration;

use angzarr_embedded::v1::Cover;
use daemon_proto::v1;
use run_ports::Edge;

use crate::server::tests::{fixture, refused, resolve, Fixture, COMMIT};

const NAME: &str = "projects/calm-quiet-heron/sessions/amber-swift-owl";
const NONCE: &str = "0123456789abcdef";

/// `command` on the session `NAME` names, keyed `key`.
async fn on_session<M: angzarr_embedded::markup::DomainMessage>(
    f: &Fixture,
    command: &M,
    key: &str,
) {
    let root = sessions::SessionName::parse(NAME).unwrap().root();
    let cover = Cover {
        domain: sessions::SESSION_DOMAIN.to_string(),
        root: Some(root),
        edition: None,
    };
    f.world
        .handle(sessions::keyed_book(cover, command, key))
        .await
        .unwrap();
}

async fn watched() -> (Fixture, Arc<daemon_application::SessionWatches>) {
    let f = fixture(vec!["calm-quiet-heron"]);
    let watches = daemon_application::SessionWatches::new(Duration::from_secs(600));
    let f = Fixture {
        api: f.api.watching(Arc::clone(&watches)),
        ..f
    };
    f.api.resolve_project(resolve(COMMIT, "")).await.unwrap();
    f.mint("amber-swift-owl").await;
    f.catch_up().await;
    (f, watches)
}

fn watch_of(name: &str) -> v1::WatchSessionRequest {
    v1::WatchSessionRequest {
        name: name.to_string(),
    }
}

async fn shown(f: &Fixture) -> v1::Session {
    f.api
        .get_session(v1::GetSessionRequest {
            name: NAME.to_string(),
        })
        .await
        .unwrap()
}

#[tokio::test]
async fn a_watch_opens_with_the_sessions_signal_and_makes_it_wakeable_while_open() {
    let (f, watches) = watched().await;
    assert!(!shown(&f).await.wakeable);
    on_session(
        &f,
        &sessions::IssueWake {
            nonce: NONCE.to_string(),
        },
        "wake:1",
    )
    .await;
    f.catch_up().await;
    let mut stream = f.api.watch_session(watch_of(NAME)).await.unwrap();
    let first = stream.next().expect("the signal it opened with");
    assert_eq!(first.outstanding_wake, NONCE);
    assert!(shown(&f).await.wakeable);
    let uid = shown(&f).await.uid;
    assert!(watches.is_watched(&uid));
    drop(stream);
    assert!(!shown(&f).await.wakeable, "its watch closed");
}

#[tokio::test]
async fn a_watch_of_a_session_nobody_holds_or_of_no_name_is_refused() {
    let (f, _) = watched().await;
    let (_, missing): (_, v1::SessionNotFound) = refused(
        f.api
            .watch_session(watch_of("projects/calm-quiet-heron/sessions/far-heron"))
            .await
            .map(|_| ()),
    );
    assert_eq!(missing.name, "projects/calm-quiet-heron/sessions/far-heron");
    let (_, invalid): (_, v1::NameInvalid) = refused(
        f.api
            .watch_session(watch_of("amber-swift-owl"))
            .await
            .map(|_| ()),
    );
    assert_eq!(invalid.reason, "shape");
}

// `session show` says a wake was sent and not answered: disarmed, nothing
// issued or redeemed after it; and outstanding while it is.
#[tokio::test]
async fn get_session_says_a_wake_outstanding_and_one_not_answered() {
    let (f, _) = watched().await;
    assert_eq!(shown(&f).await.wake, "");
    on_session(
        &f,
        &sessions::IssueWake {
            nonce: NONCE.to_string(),
        },
        "wake:1",
    )
    .await;
    f.catch_up().await;
    assert_eq!(shown(&f).await.wake, "outstanding");
    on_session(
        &f,
        &sessions::DisarmWake {
            nonce: NONCE.to_string(),
        },
        "disarm:1",
    )
    .await;
    f.catch_up().await;
    assert_eq!(shown(&f).await.wake, "not answered");
}

// The daemon's loop holds the board and then reads the pending messages
// (`Daemon::plan`), so a watch that holds the pending messages while it
// waits for the board is the other half of a deadlock (2026-11-25: the
// loop, every watch and the kernel's projections all stuck). While the
// board is held elsewhere, a watch opening must not keep the pending
// messages locked: a reader of them must still get through.
#[tokio::test]
async fn a_watch_waiting_for_the_board_holds_no_pending_messages() {
    use std::sync::atomic::{AtomicBool, AtomicUsize, Ordering};

    let (f, _watches) = watched().await;
    let handle = tokio::runtime::Handle::current();
    let reads = AtomicUsize::new(0);
    let done = AtomicBool::new(false);
    let board = f.board.board();
    let (stalled, waited) = std::thread::scope(|scope| {
        scope.spawn(|| {
            // Opens the watch: blocks on the board we hold.
            let _ = handle.block_on(f.api.watch_session(watch_of(NAME)));
        });
        scope.spawn(|| {
            while !done.load(Ordering::SeqCst) {
                drop(f.pending.pending());
                reads.fetch_add(1, Ordering::SeqCst);
                std::thread::sleep(Duration::from_millis(2));
            }
        });
        // Bounded: the reader's progress is judged, never waited on.
        std::thread::sleep(Duration::from_millis(500));
        let before = reads.load(Ordering::SeqCst);
        std::thread::sleep(Duration::from_millis(500));
        let after = reads.load(Ordering::SeqCst);
        drop(board);
        done.store(true, Ordering::SeqCst);
        (after == before, after)
    });
    assert!(
        !stalled,
        "a reader of the pending messages stalled behind a watch waiting for the board (read {waited} times)"
    );
}
