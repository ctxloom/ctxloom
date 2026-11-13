//! The daemon's API use cases over the real projects and sessions
//! aggregates, an in-memory store, and the session index caught up from it
//! (the daemon keeps it warm in its kernel; here the test catches it up).

use std::sync::{Arc, Mutex};

use angzarr_embedded::testing::{MemoryStore, Samples};
use angzarr_embedded::v1::{CommandBook, Cover, EventBook, EventPage};
use angzarr_embedded::{
    Answered, Failure, Handled, Kernel, OnRetryable, Registry, Rejected, Sampled, StoreError,
    StreamRef, Timestamp,
};
use daemon_proto::v1;
use prost::Message;
use run_ports::{Edge, Unserved};
use session_index::{SessionIndex, SessionIndexes, State};
use sessions::{MintSession, Origin};

use super::{About, Api, Harps, Hosting, Parts, SessionStates};
pub(crate) use crate::destroy::tests::FakePlaces;
use crate::{ApiError, ApiRefusal};

pub(crate) const PROJECT: &str = "calm-otter";
pub(crate) const ROOT: &str = "/work/calm";
const COMMON: &str = "/work/calm/.git";
const COMMIT: &str = "1111111111111111111111111111111111111111";

/// The kernel over an in-memory store, with the projects and sessions
/// aggregates.
pub(crate) struct World {
    pub(crate) registry: Registry,
    pub(crate) store: MemoryStore,
    samples: Mutex<Samples>,
}

impl World {
    fn new() -> Arc<World> {
        let mut registry = Registry::new();
        registry
            .aggregate::<sessions::SessionAggregate>(sessions::SessionAggregate::default)
            .unwrap();
        registry
            .aggregate::<sessions::ProjectAggregate>(sessions::ProjectAggregate::default)
            .unwrap();
        registry
            .aggregate::<sessions::ProjectNameAggregate>(sessions::ProjectNameAggregate::default)
            .unwrap();
        registry
            .aggregate::<sessions::NameClaimAggregate>(sessions::NameClaimAggregate::default)
            .unwrap();
        registry
            .aggregate::<runs::RunAggregate>(runs::RunAggregate::default)
            .unwrap();
        Arc::new(World {
            registry,
            store: MemoryStore::default(),
            samples: Mutex::new(Samples::starting_at(1_000)),
        })
    }
}

impl Edge for World {
    async fn serve(&self, book: CommandBook, _: run_ports::Arrival) -> Result<Answered, Unserved> {
        self.handle(book).await.map_err(Unserved::Failed)
    }

    async fn handle(&self, book: CommandBook) -> Result<Answered, Failure<Rejected>> {
        let sampled: Sampled = self.samples.lock().unwrap().next_sample();
        let kernel = Kernel::new(&self.registry).on_retryable(OnRetryable {
            max_retries: 0,
            ..OnRetryable::default()
        });
        let cover = book.cover.clone();
        let handled: Handled = kernel.handle_any(&self.store, book, &sampled).await?;
        let events = handled
            .books()
            .into_iter()
            .find(|book| book.cover == cover)
            .unwrap_or(EventBook {
                cover,
                ..EventBook::default()
            });
        Ok(Answered {
            events: Some(events),
            delivery: handled.delivery(),
        })
    }

    async fn events(&self, cover: &Cover, from: u64) -> Result<Vec<EventPage>, StoreError> {
        let stream = StreamRef::try_from(cover)?;
        let slice =
            angzarr_embedded::angzarr_ports::EventStore::read(&self.store, stream, from, None)
                .await?;
        Ok(slice.pages)
    }
}

/// The loop's side, as a test sets it.
#[derive(Default)]
pub(crate) struct Loop {
    hosted: Mutex<Vec<v1::HostedRun>>,
    stopped: Mutex<Vec<bool>>,
    /// How many reads asked for a fresh round.
    freshened: Mutex<u32>,
}

impl Hosting for Loop {
    fn endpoint(&self) -> String {
        "https://127.0.0.1:5000".to_string()
    }

    fn hosted(&self) -> Vec<v1::HostedRun> {
        self.hosted.lock().unwrap().clone()
    }

    fn idle_seconds(&self) -> u64 {
        42
    }

    fn stop(&self, end_hosted: bool) {
        self.stopped.lock().unwrap().push(end_hosted);
    }

    fn fresh(&self) -> std::pin::Pin<Box<dyn std::future::Future<Output = ()> + Send + '_>> {
        *self.freshened.lock().unwrap() += 1;
        Box::pin(async {})
    }
}

/// Every session reads live but `ended-*` ones, unless a test set its
/// lock's state.
#[derive(Default)]
pub(crate) struct Locks(pub(crate) Mutex<std::collections::BTreeMap<String, State>>);

impl SessionStates for Locks {
    fn state(&self, _project_dir: &str, harp: &str) -> State {
        if let Some(state) = self.0.lock().unwrap().get(harp) {
            return *state;
        }
        match harp.starts_with("ended") {
            true => State::Ended,
            false => State::Live,
        }
    }
}

/// The harps a test offers, in order.
struct Offered(Vec<&'static str>);

impl Harps for Offered {
    fn fresh(&self) -> Vec<String> {
        self.0.iter().map(|h| h.to_string()).collect()
    }
}

pub(crate) struct Fixture {
    pub(crate) world: Arc<World>,
    pub(crate) index: SessionIndexes,
    pub(crate) board: daemon_board::Boards,
    pub(crate) hosting: Arc<Loop>,
    pub(crate) places: Arc<FakePlaces>,
    pub(crate) locks: Arc<Locks>,
    pub(crate) api: Api<World>,
}

pub(crate) fn fixture(harps: Vec<&'static str>) -> Fixture {
    let world = World::new();
    let index = SessionIndexes::default();
    let board = daemon_board::Boards::default();
    let hosting = Arc::new(Loop::default());
    let places = Arc::new(FakePlaces::default());
    let locks = Arc::new(Locks::default());
    let api = Api::new(Parts {
        edge: Arc::clone(&world),
        index: index.clone(),
        pending: pending_messages::PendingMessages::default(),
        board: board.clone(),
        hosting: Arc::clone(&hosting) as Arc<dyn Hosting>,
        states: Arc::clone(&locks) as Arc<dyn SessionStates>,
        harps: Arc::new(Offered(harps)),
        about: About {
            pid: 4242,
            version: "0.1.0+abc".to_string(),
            start_time: Timestamp {
                seconds: 7,
                nanos: 0,
            },
            idle_limit_seconds: 600,
        },
        places: Arc::new(Arc::clone(&places)) as Arc<dyn crate::Places>,
        purges: Arc::clone(&world) as Arc<dyn crate::Purges>,
    });
    Fixture {
        world,
        index,
        board,
        hosting,
        places,
        locks,
        api,
    }
}

impl Fixture {
    /// The index caught up with what the store holds (the kernel does this
    /// inline in the daemon).
    pub(crate) async fn catch_up(&self) {
        let mut caught = SessionIndex::caught_up(self.index.clone());
        caught
            .step(&self.world.store, self.world.registry.catalog())
            .await
            .unwrap();
        let mut board = daemon_board::caught_up(self.board.clone());
        board
            .step(&self.world.store, self.world.registry.catalog())
            .await
            .unwrap();
    }

    /// The daemon started again over the same store: a fresh API, its
    /// index caught up from what the store holds, remembering nothing.
    pub(crate) async fn restarted(&self) -> Api<World> {
        let index = SessionIndexes::default();
        let mut caught = SessionIndex::caught_up(index.clone());
        caught
            .step(&self.world.store, self.world.registry.catalog())
            .await
            .unwrap();
        Api::new(Parts {
            edge: Arc::clone(&self.world),
            index,
            pending: pending_messages::PendingMessages::default(),
            board: daemon_board::Boards::default(),
            hosting: Arc::clone(&self.hosting) as Arc<dyn Hosting>,
            states: Arc::clone(&self.locks) as Arc<dyn SessionStates>,
            harps: Arc::new(Offered(Vec::new())),
            about: About::default(),
            places: Arc::new(Arc::clone(&self.places)) as Arc<dyn crate::Places>,
            purges: Arc::clone(&self.world) as Arc<dyn crate::Purges>,
        })
    }

    /// Mint the session `harp` of the project.
    pub(crate) async fn mint(&self, harp: &str) {
        let name = format!("projects/calm-quiet-heron/sessions/{harp}");
        let root = sessions::SessionName::parse(&name).unwrap().root();
        let cover = Cover {
            domain: sessions::SESSION_DOMAIN.to_string(),
            root: Some(root),
            edition: None,
        };
        let mint = MintSession {
            harp: harp.to_string(),
            project_id: PROJECT.to_string(),
            project_dir: ROOT.to_string(),
            output_dir: format!("{ROOT}/ctxloom/{harp}"),
            engine: "claude-code".to_string(),
            origin: Origin::Session,
            started_at: None,
            depth: 0,
            owner: None,
            workspace: None,
        };
        let book = sessions::keyed_book(cover, &mint, &format!("mint:{harp}"));
        self.world.handle(book).await.unwrap();
    }

    /// The session `harp`'s last run ended: its record's end stamp.
    pub(crate) async fn end(&self, harp: &str) {
        let name = format!("projects/calm-quiet-heron/sessions/{harp}");
        let root = sessions::SessionName::parse(&name).unwrap().root();
        let cover = Cover {
            domain: sessions::SESSION_DOMAIN.to_string(),
            root: Some(root),
            edition: None,
        };
        let ended = sessions::RecordEnd {
            at: angzarr_embedded::Timestamp {
                seconds: 1_000,
                nanos: 0,
            },
        };
        let book = sessions::keyed_book(cover, &ended, &format!("ended:{harp}"));
        self.world.handle(book).await.unwrap();
    }
}

fn resolve(commit: &str, name: &str) -> v1::ResolveProjectRequest {
    v1::ResolveProjectRequest {
        name: name.to_string(),
        cwd: ROOT.to_string(),
        project_id: PROJECT.to_string(),
        root: ROOT.to_string(),
        git_common_dir: COMMON.to_string(),
        root_commit: commit.to_string(),
        env: None,
    }
}

/// The typed refusal of an error, decoded as `T`, and its code.
/// The refusal's status and message, held to be of type `T`: a refusal of
/// another type fails the test, never decoded as `T` by accident.
pub(crate) fn refused<T: Message + Default + prost::Name>(
    result: Result<impl std::fmt::Debug, ApiError>,
) -> (i32, T) {
    match result {
        Err(ApiError::Refused(ApiRefusal { code, refusal })) => {
            assert_eq!(refusal.type_url, T::type_url(), "the refusal's type");
            (
                code,
                T::decode(refusal.value.as_slice()).expect("the refusal's bytes"),
            )
        }
        other => panic!("not refused: {other:?}"),
    }
}

// GetDaemon: what the daemon says of itself, what it hosts, its idle time.
#[tokio::test]
async fn get_daemon_says_what_it_hosts_and_how_long_it_has_been_idle() {
    let f = fixture(vec![]);
    f.hosting.hosted.lock().unwrap().push(v1::HostedRun {
        session: "projects/calm-quiet-heron/sessions/brisk-copper-moth".to_string(),
        harp: "brisk-copper-moth".to_string(),
        run: String::new(),
        kind: v1::hosted_run::Kind::Child as i32,
    });
    let daemon = f.api.get_daemon().await.unwrap();
    assert_eq!(daemon.name, "daemon");
    assert_eq!(daemon.pid, 4242);
    assert_eq!(daemon.endpoint, "https://127.0.0.1:5000");
    assert_eq!(daemon.version, "0.1.0+abc");
    assert_eq!(daemon.start_time.map(|t| t.seconds), Some(7));
    assert_eq!(daemon.runs.len(), 1);
    assert_eq!(daemon.runs[0].harp, "brisk-copper-moth");
    assert_eq!((daemon.idle_seconds, daemon.idle_limit_seconds), (42, 600));
}

// StopDaemon with nothing hosted: asked to drain.
#[tokio::test]
async fn stop_daemon_hosting_nothing_stops_it() {
    let f = fixture(vec![]);
    let daemon = f
        .api
        .stop_daemon(v1::StopDaemonRequest::default())
        .await
        .unwrap();
    assert_eq!(daemon.pid, 4242);
    assert_eq!(*f.hosting.stopped.lock().unwrap(), [false]);
}

// S12: refused while it hosts a live run, naming it; --even-if-live ends
// them first.
#[tokio::test]
async fn stop_daemon_hosting_a_live_run_is_refused_unless_even_if_live() {
    let f = fixture(vec![]);
    f.hosting.hosted.lock().unwrap().push(v1::HostedRun {
        harp: "brisk-copper-moth".to_string(),
        ..v1::HostedRun::default()
    });
    let (code, hosting): (_, v1::DaemonHosting) =
        refused(f.api.stop_daemon(v1::StopDaemonRequest::default()).await);
    assert_eq!(
        code,
        <v1::DaemonHosting as daemon_proto::Refusal>::REFUSAL_STATUS
    );
    assert_eq!(hosting.runs, 1);
    assert_eq!(hosting.harps, ["brisk-copper-moth"]);
    assert!(f.hosting.stopped.lock().unwrap().is_empty());
    f.api
        .stop_daemon(v1::StopDaemonRequest {
            even_if_live: true,
            ..v1::StopDaemonRequest::default()
        })
        .await
        .unwrap();
    assert_eq!(*f.hosting.stopped.lock().unwrap(), [true]);
}

// Project registration: the first resolution names the project with the
// first free harp offered and registers its identity; it answers the
// project's resource name.
#[tokio::test]
async fn resolve_project_names_and_registers_on_first_sight() {
    let f = fixture(vec!["calm-quiet-heron", "far-heron"]);
    let project = f.api.resolve_project(resolve(COMMIT, "")).await.unwrap();
    assert_eq!(
        project,
        v1::Project {
            name: "projects/calm-quiet-heron".to_string(),
            root: ROOT.to_string(),
            uid: PROJECT.to_string(),
            git_common_dir: COMMON.to_string(),
            root_commit: COMMIT.to_string(),
        }
    );
    // Again, from a worktree and expecting its harp: the same project.
    let again = v1::ResolveProjectRequest {
        root: "/work/calm-wt".to_string(),
        cwd: "/work/calm-wt".to_string(),
        ..resolve(COMMIT, "projects/calm-quiet-heron")
    };
    assert_eq!(f.api.resolve_project(again).await.unwrap(), project);
}

// A harp another project holds is passed over for the next.
#[tokio::test]
async fn resolve_project_passes_over_a_taken_harp() {
    let f = fixture(vec!["calm-quiet-heron", "far-heron"]);
    f.api
        .resolve_project(v1::ResolveProjectRequest {
            project_id: "other-project".to_string(),
            ..resolve(COMMIT, "")
        })
        .await
        .unwrap();
    let project = f.api.resolve_project(resolve(COMMIT, "")).await.unwrap();
    assert_eq!(project.name, "projects/far-heron");
}

// A changed root commit is refused, naming the registered project.
#[tokio::test]
async fn resolve_project_with_another_root_commit_is_refused() {
    let f = fixture(vec!["calm-quiet-heron"]);
    f.api.resolve_project(resolve(COMMIT, "")).await.unwrap();
    let other = "2222222222222222222222222222222222222222";
    let (_, changed): (_, sessions_proto::v1::ProjectIdentityChanged) =
        refused(f.api.resolve_project(resolve(other, "")).await);
    assert_eq!(changed.project, "projects/calm-quiet-heron");
    assert_eq!(changed.found_commit, other);
}

// Owner, 2026-11-12 (after step 5): a project first resolved outside git
// adopts the identity it is next resolved with inside git, and is strict
// after: its empty identity again is refused, as is another commit.
#[tokio::test]
async fn resolve_project_outside_git_adopts_its_first_identity_then_is_strict() {
    let f = fixture(vec!["calm-quiet-heron"]);
    let outside = v1::ResolveProjectRequest {
        git_common_dir: String::new(),
        ..resolve("", "")
    };
    let first = f.api.resolve_project(outside.clone()).await.unwrap();
    assert_eq!(
        (first.git_common_dir.as_str(), first.root_commit.as_str()),
        ("", "")
    );
    let adopted = f.api.resolve_project(resolve(COMMIT, "")).await.unwrap();
    assert_eq!(
        (
            adopted.git_common_dir.as_str(),
            adopted.root_commit.as_str()
        ),
        (COMMON, COMMIT)
    );
    assert_eq!(adopted.name, first.name);
    let (_, changed): (_, sessions_proto::v1::ProjectIdentityChanged) =
        refused(f.api.resolve_project(outside).await);
    assert_eq!(changed.project, "projects/calm-quiet-heron");
    assert_eq!(changed.registered_commit, COMMIT);
    assert_eq!(changed.found_commit, "");
    let other = "2222222222222222222222222222222222222222";
    let (_, changed): (_, sessions_proto::v1::ProjectIdentityChanged) =
        refused(f.api.resolve_project(resolve(other, "")).await);
    assert_eq!(changed.found_commit, other);
}

// A client that expected another project is refused, naming the registered.
#[tokio::test]
async fn resolve_project_expecting_another_harp_is_refused() {
    let f = fixture(vec!["calm-quiet-heron"]);
    f.api.resolve_project(resolve(COMMIT, "")).await.unwrap();
    let (_, mismatch): (_, sessions_proto::v1::ProjectHarpMismatch) = refused(
        f.api
            .resolve_project(resolve(COMMIT, "projects/far-heron"))
            .await,
    );
    assert_eq!(mismatch.project, "projects/calm-quiet-heron");
    assert_eq!(mismatch.expected, "far-heron");
}

// A directory in no project, and a malformed expected name, are refused.
#[tokio::test]
async fn resolve_project_with_no_project_or_a_bad_name_is_refused() {
    let f = fixture(vec!["calm-quiet-heron"]);
    let none = v1::ResolveProjectRequest {
        project_id: String::new(),
        ..resolve(COMMIT, "")
    };
    let (code, unknown): (_, v1::ProjectUnknown) = refused(f.api.resolve_project(none).await);
    assert_eq!(
        code,
        <v1::ProjectUnknown as daemon_proto::Refusal>::REFUSAL_STATUS
    );
    assert_eq!(unknown.cwd, ROOT);
    let (code, invalid): (_, v1::NameInvalid) =
        refused(f.api.resolve_project(resolve(COMMIT, "calm/x")).await);
    assert_eq!(
        code,
        <v1::NameInvalid as daemon_proto::Refusal>::REFUSAL_STATUS
    );
    assert_eq!(invalid.name, "calm/x");
}

// GetProject reads the warm index: the project by its harp, its
// registration with it; one nothing holds is not found.
#[tokio::test]
async fn get_project_answers_the_registered_project() {
    let f = fixture(vec!["calm-quiet-heron"]);
    let resolved = f.api.resolve_project(resolve(COMMIT, "")).await.unwrap();
    f.catch_up().await;
    let got = f
        .api
        .get_project(v1::GetProjectRequest {
            name: "projects/calm-quiet-heron".to_string(),
        })
        .await
        .unwrap();
    assert_eq!(got, resolved);
    let (code, missing): (_, v1::ProjectNotFound) = refused(
        f.api
            .get_project(v1::GetProjectRequest {
                name: "projects/far-heron".to_string(),
            })
            .await,
    );
    assert_eq!(
        code,
        <v1::ProjectNotFound as daemon_proto::Refusal>::REFUSAL_STATUS
    );
    assert_eq!(missing.name, "projects/far-heron");
}

// GetSession: a session by its name, from the warm index, its state its
// lock's; one nothing holds is not found; a name that is none, invalid.
#[tokio::test]
async fn get_session_answers_the_session_by_its_name() {
    let f = fixture(vec!["calm-quiet-heron"]);
    f.api.resolve_project(resolve(COMMIT, "")).await.unwrap();
    f.mint("amber-swift-owl").await;
    f.catch_up().await;
    let name = "projects/calm-quiet-heron/sessions/amber-swift-owl";
    let session = f
        .api
        .get_session(v1::GetSessionRequest {
            name: name.to_string(),
            ..v1::GetSessionRequest::default()
        })
        .await
        .unwrap();
    assert_eq!(session.name, name);
    assert_eq!(session.state, v1::session::State::Live as i32);
    assert_eq!(session.engine, "claude-code");
    let (_, missing): (_, v1::SessionNotFound) = refused(
        f.api
            .get_session(v1::GetSessionRequest {
                name: "projects/calm-quiet-heron/sessions/far-heron".to_string(),
                ..v1::GetSessionRequest::default()
            })
            .await,
    );
    assert_eq!(missing.name, "projects/calm-quiet-heron/sessions/far-heron");
    let (_, invalid): (_, v1::NameInvalid) = refused(
        f.api
            .get_session(v1::GetSessionRequest {
                name: "amber-swift-owl".to_string(),
                ..v1::GetSessionRequest::default()
            })
            .await,
    );
    assert_eq!(invalid.reason, "shape");
}

// ListSessions: a project's sessions (or every project's), by state.
#[tokio::test]
async fn list_sessions_lists_a_projects_sessions_by_state() {
    let f = fixture(vec!["calm-quiet-heron"]);
    f.api.resolve_project(resolve(COMMIT, "")).await.unwrap();
    f.mint("amber-swift-owl").await;
    f.mint("ended-copper-moth").await;
    f.catch_up().await;
    let names = |response: v1::ListSessionsResponse| {
        let mut names: Vec<String> = response.sessions.into_iter().map(|s| s.name).collect();
        names.sort();
        names
    };
    let all = f
        .api
        .list_sessions(v1::ListSessionsRequest {
            parent: "projects/calm-quiet-heron".to_string(),
            ..v1::ListSessionsRequest::default()
        })
        .await
        .unwrap();
    assert_eq!(
        names(all),
        [
            "projects/calm-quiet-heron/sessions/amber-swift-owl",
            "projects/calm-quiet-heron/sessions/ended-copper-moth"
        ]
    );
    let live = f
        .api
        .list_sessions(v1::ListSessionsRequest {
            parent: "projects/-".to_string(),
            state: v1::session::State::Live as i32,
            ..v1::ListSessionsRequest::default()
        })
        .await
        .unwrap();
    assert_eq!(
        names(live),
        ["projects/calm-quiet-heron/sessions/amber-swift-owl"]
    );
    let other = f
        .api
        .list_sessions(v1::ListSessionsRequest {
            parent: "projects/far-heron".to_string(),
            ..v1::ListSessionsRequest::default()
        })
        .await
        .unwrap();
    assert!(other.sessions.is_empty());
}

// SearchSessions: every word, case-insensitively, in a session's harp or
// title.
#[tokio::test]
async fn search_sessions_matches_every_word() {
    let f = fixture(vec!["calm-quiet-heron"]);
    f.api.resolve_project(resolve(COMMIT, "")).await.unwrap();
    f.mint("amber-swift-owl").await;
    f.mint("ended-copper-moth").await;
    f.catch_up().await;
    let found = f
        .api
        .search_sessions(v1::SearchSessionsRequest {
            parent: "projects/-".to_string(),
            words: vec!["COPPER".to_string(), "moth".to_string()],
            ..v1::SearchSessionsRequest::default()
        })
        .await
        .unwrap();
    let names: Vec<String> = found.sessions.into_iter().map(|s| s.name).collect();
    assert_eq!(
        names,
        ["projects/calm-quiet-heron/sessions/ended-copper-moth"]
    );
}

// GetSession answers what the session's own record says (its native id,
// its project's root, its output folder), its pending messages, and asks
// the loop for a fresh round first, so it reads what was committed before.
#[tokio::test]
async fn get_session_answers_its_record_and_reads_fresh() {
    let f = fixture(vec!["calm-quiet-heron"]);
    f.api.resolve_project(resolve(COMMIT, "")).await.unwrap();
    f.mint("amber-swift-owl").await;
    f.catch_up().await;
    let session = f
        .api
        .get_session(v1::GetSessionRequest {
            name: "projects/calm-quiet-heron/sessions/amber-swift-owl".to_string(),
            ..v1::GetSessionRequest::default()
        })
        .await
        .unwrap();
    assert_eq!(session.project_root, ROOT);
    assert_eq!(
        session.output_dir,
        format!("{ROOT}/ctxloom/amber-swift-owl")
    );
    assert!(session.pending.is_empty());
    assert_eq!(*f.hosting.freshened.lock().unwrap(), 1);
    f.api
        .list_sessions(v1::ListSessionsRequest {
            parent: "projects/-".to_string(),
            ..v1::ListSessionsRequest::default()
        })
        .await
        .unwrap();
    assert_eq!(*f.hosting.freshened.lock().unwrap(), 2);
}

// A live session's end is blank, whatever its record's last end stamp.
#[tokio::test]
async fn a_live_sessions_end_is_blank() {
    let f = fixture(vec!["calm-quiet-heron"]);
    f.api.resolve_project(resolve(COMMIT, "")).await.unwrap();
    f.mint("amber-swift-owl").await;
    f.mint("ended-copper-moth").await;
    f.end("amber-swift-owl").await;
    f.end("ended-copper-moth").await;
    f.catch_up().await;
    let listed = f
        .api
        .list_sessions(v1::ListSessionsRequest {
            parent: "projects/-".to_string(),
            ..v1::ListSessionsRequest::default()
        })
        .await
        .unwrap();
    for session in listed.sessions {
        match session.state == v1::session::State::Live as i32 {
            true => assert_eq!(session.end_time, None, "{}", session.name),
            // An ended session's end is its record's.
            false => assert!(session.end_time.is_some(), "{}", session.name),
        }
    }
}

// A name whose segment is no harp is refused with the harp grammar's
// reason, not merely its shape.
#[tokio::test]
async fn a_name_whose_segment_is_no_harp_says_why() {
    let f = fixture(vec!["calm-quiet-heron"]);
    let name = "projects/calm-quiet-heron/sessions/ amber";
    let (_, invalid): (_, v1::NameInvalid) = refused(
        f.api
            .get_session(v1::GetSessionRequest {
                name: name.to_string(),
                ..v1::GetSessionRequest::default()
            })
            .await,
    );
    assert_eq!(invalid.name, name);
    assert_eq!(invalid.reason, "whitespace");
}
