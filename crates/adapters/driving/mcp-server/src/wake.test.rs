//! The terminal wake's poster at work (owner ruling 2026-11-18), against a
//! fake daemon whose watch the test feeds and a fake socket that records.
//! Time is tokio's paused clock: a wake's 60 s pass when the test advances
//! it, never by waiting; each race is forced by the order of the feed.

use std::sync::{Arc, Mutex};
use std::time::Duration;

use daemon_api::{Called, CommandBook, Synced};
use daemon_proto::v1;

use crate::wake::{keep_awake, Wakes};

const ME: &str = "projects/calm-quiet-heron/sessions/amber-quiet-heron";
const N1: &str = "0123456789abcdef";

#[derive(Default)]
struct Seen {
    /// Each command's type name, in order.
    commands: Vec<String>,
    /// Each line posted.
    posted: Vec<String>,
}

#[derive(Clone)]
struct Daemon {
    seen: Arc<Mutex<Seen>>,
    feed: Arc<Mutex<Option<tokio::sync::mpsc::UnboundedReceiver<v1::SessionSignal>>>>,
    refusing: bool,
}

impl daemon_api::Daemons for Daemon {
    type Api = Daemon;

    async fn serving(&self) -> Result<Option<Daemon>, daemon_api::Unreached> {
        Ok(Some(self.clone()))
    }

    async fn reach(&self) -> Result<Daemon, daemon_api::Unreached> {
        Ok(self.clone())
    }
}

struct Fed(tokio::sync::mpsc::UnboundedReceiver<v1::SessionSignal>);

impl daemon_api::SessionSignals for Fed {
    fn next(&mut self) -> daemon_api::NextSignal<'_> {
        Box::pin(async move { self.0.recv().await.map(Ok) })
    }
}

macro_rules! never {
    ($($method:ident($request:ident) -> $response:ident),* $(,)?) => {
        $(
            async fn $method(&self, _: v1::$request) -> Result<v1::$response, Called> {
                unreachable!()
            }
        )*
    };
}

impl daemon_api::DaemonApi for Daemon {
    never!(
        get_daemon(GetDaemonRequest) -> Daemon,
        stop_daemon(StopDaemonRequest) -> Daemon,
        resolve_project(ResolveProjectRequest) -> Project,
        get_project(GetProjectRequest) -> Project,
        list_sessions(ListSessionsRequest) -> ListSessionsResponse,
        search_sessions(SearchSessionsRequest) -> ListSessionsResponse,
    );

    /// A write with no projection back is read back (as the session).
    async fn get_session(&self, r: v1::GetSessionRequest) -> Result<v1::Session, Called> {
        Ok(v1::Session {
            name: r.name,
            ..v1::Session::default()
        })
    }

    async fn watch_session(
        &self,
        request: v1::WatchSessionRequest,
    ) -> Result<Box<dyn daemon_api::SessionSignals>, Called> {
        assert_eq!(request.name, ME, "its own session");
        match self.feed.lock().unwrap().take() {
            Some(feed) => Ok(Box::new(Fed(feed))),
            None => Err(Called::Unreachable {
                detail: "watched once".to_string(),
            }),
        }
    }

    async fn handle_sync(&self, book: CommandBook, _: Vec<String>) -> Result<Synced, Called> {
        let Some(angzarr_embedded::v1::command_page::Body::Command(command)) =
            book.page.as_ref().and_then(|page| page.body.clone())
        else {
            panic!("a command");
        };
        let name = command
            .payload
            .unwrap()
            .type_url
            .rsplit('.')
            .next()
            .unwrap()
            .to_string();
        self.seen.lock().unwrap().commands.push(name);
        if self.refusing {
            return Err(Called::Refused {
                code: 9,
                refusal: prost_types::Any::default(),
            });
        }
        Ok(Synced {
            applied: true,
            projections: Vec::new(),
        })
    }
}

/// The socket claude exported: records each post, or fails each.
struct Socket {
    seen: Arc<Mutex<Seen>>,
    failing: bool,
}

impl daemon_api::WakePost for Socket {
    fn post(&self, line: &str) -> Result<(), String> {
        if self.failing {
            return Err("no listener".to_string());
        }
        self.seen.lock().unwrap().posted.push(line.to_string());
        Ok(())
    }
}

struct World {
    seen: Arc<Mutex<Seen>>,
    feed: tokio::sync::mpsc::UnboundedSender<v1::SessionSignal>,
}

impl World {
    fn commands(&self) -> Vec<String> {
        self.seen.lock().unwrap().commands.clone()
    }

    fn posted(&self) -> Vec<String> {
        self.seen.lock().unwrap().posted.clone()
    }

    fn signal(&self, wakes: &[&str], outstanding: &str) {
        self.feed
            .send(v1::SessionSignal {
                wakes: wakes.iter().map(ToString::to_string).collect(),
                outstanding_wake: outstanding.to_string(),
            })
            .unwrap();
    }
}

/// Let the poster's task run until it waits again.
async fn settle() {
    for _ in 0..50 {
        tokio::task::yield_now().await;
    }
}

/// A poster for `ME` started, over a daemon that refuses every write when
/// `refusing` and a socket that fails every post when `failing`.
fn started(refusing: bool, failing: bool) -> World {
    let seen = Arc::new(Mutex::new(Seen::default()));
    let (feed, fed) = tokio::sync::mpsc::unbounded_channel();
    let daemon = Daemon {
        seen: Arc::clone(&seen),
        feed: Arc::new(Mutex::new(Some(fed))),
        refusing,
    };
    let caller = daemon_api::Caller::new(daemon, ME, v1::Caller::default(), Box::new(String::new))
        .unwrap_or_else(|_| panic!("a name"));
    let wakes = Wakes {
        post: Arc::new(Socket {
            seen: Arc::clone(&seen),
            failing,
        }),
        mint: Box::new(|| N1.to_string()),
    };
    tokio::spawn(keep_awake(Arc::new(caller), wakes));
    World { seen, feed }
}

#[tokio::test(start_paused = true)]
async fn a_pending_wake_is_issued_then_posted() {
    let world = started(false, false);
    world.signal(&["m1"], "");
    settle().await;
    assert_eq!(world.commands(), ["IssueWake"]);
    assert_eq!(world.posted(), [daemon_api::wake_line(N1)]);
}

// Not answered in its time: disarmed, its messages left for the next
// prompt.
#[tokio::test(start_paused = true)]
async fn a_wake_not_answered_in_its_time_is_disarmed() {
    let world = started(false, false);
    world.signal(&["m1"], "");
    settle().await;
    world.signal(&["m1"], N1);
    settle().await;
    tokio::time::advance(daemon_api::DISARM_AFTER - Duration::from_secs(1)).await;
    settle().await;
    assert_eq!(world.commands(), ["IssueWake"], "not yet");
    tokio::time::advance(Duration::from_secs(1)).await;
    settle().await;
    assert_eq!(world.commands(), ["IssueWake", "DisarmWake"]);
}

// The race, its turn first: redeemed before its time, never disarmed.
#[tokio::test(start_paused = true)]
async fn a_wake_answered_before_its_time_is_not_disarmed() {
    let world = started(false, false);
    world.signal(&["m1"], "");
    settle().await;
    world.signal(&["m1"], N1);
    settle().await;
    world.signal(&[], "");
    settle().await;
    tokio::time::advance(daemon_api::DISARM_AFTER * 2).await;
    settle().await;
    assert_eq!(world.commands(), ["IssueWake"]);
}

// A wake the daemon refuses is never posted, and is tried again at the
// next signal (its keepalive).
#[tokio::test(start_paused = true)]
async fn a_wake_the_daemon_refuses_is_never_posted_and_is_tried_again() {
    let world = started(true, false);
    world.signal(&["m1"], "");
    settle().await;
    assert_eq!(world.commands(), ["IssueWake"]);
    assert!(world.posted().is_empty());
    world.signal(&["m1"], "");
    settle().await;
    assert_eq!(world.commands(), ["IssueWake", "IssueWake"]);
    assert!(world.posted().is_empty());
}

// A post that fails: the wake issued for it is disarmed at once.
#[tokio::test(start_paused = true)]
async fn a_wake_whose_post_fails_is_disarmed_at_once() {
    let world = started(false, true);
    world.signal(&["m1"], "");
    settle().await;
    assert_eq!(world.commands(), ["IssueWake", "DisarmWake"]);
}
