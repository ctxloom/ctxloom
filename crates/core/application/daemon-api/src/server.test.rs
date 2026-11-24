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

use super::{About, Api, Calls, Harps, Hosting, Parts, SessionStates};
pub(crate) use crate::destroy::tests::FakePlaces;
use crate::{ApiError, ApiRefusal};

pub(crate) const PROJECT: &str = "calm-otter";
pub(crate) const ROOT: &str = "/work/calm";
const COMMON: &str = "/work/calm/.git";
pub(crate) const COMMIT: &str = "1111111111111111111111111111111111111111";

/// The kernel over an in-memory store, with the projects and sessions
/// aggregates.
pub(crate) struct World {
    pub(crate) registry: Registry,
    pub(crate) store: MemoryStore,
    samples: Mutex<Samples>,
    /// The library trees as last observed, projected inline (step 10a U2).
    pub(crate) checkouts: library_checkout::LibraryCheckouts,
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
        registry
            .aggregate::<trust::TrustAggregate>(trust::TrustAggregate::default)
            .unwrap();
        registry
            .aggregate::<approvals::ApprovalAggregate>(approvals::ApprovalAggregate::default)
            .unwrap();
        registry
            .aggregate::<approvals::ApprovalNameAggregate>(
                approvals::ApprovalNameAggregate::default,
            )
            .unwrap();
        registry
            .aggregate::<library_tree::LibraryAggregate>(library_tree::LibraryAggregate::default)
            .unwrap();
        let checkouts = library_checkout::LibraryCheckouts::default();
        registry
            .projector_handler(&library_checkout::SOURCES, checkouts.clone())
            .unwrap();
        Arc::new(World {
            registry,
            store: MemoryStore::default(),
            samples: Mutex::new(Samples::starting_at(1_000)),
            checkouts,
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
    /// How many times it was asked to drain.
    drained: Mutex<u32>,
    /// The timeouts each drain was asked with, in order.
    within: Mutex<Vec<Option<std::time::Duration>>>,
    /// How many reads asked for a fresh round.
    pub(crate) freshened: Mutex<u32>,
    /// Why its start-up catch-up failed, while it stands (OD-52).
    start_up: Mutex<Option<String>>,
    /// The sessions it hosts, as their asks are recorded (step 8 U4).
    pub(crate) askers: Mutex<Vec<daemon_application::Asker>>,
    /// What each read asking for a fresh round brings, in turn (a round's
    /// catch-up, as a test stands it in).
    pub(crate) rounds: Mutex<Vec<Box<dyn FnOnce() + Send>>>,
    /// The daemon's home's ctxloom directory, once a test removed it.
    pub(crate) home_gone: Mutex<Option<String>>,
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

    fn drain(&self, within: Option<std::time::Duration>) {
        *self.drained.lock().unwrap() += 1;
        self.within.lock().unwrap().push(within);
    }

    fn draining(&self) -> bool {
        *self.drained.lock().unwrap() > 0
    }

    fn hand_over_seconds(&self) -> u64 {
        300
    }

    fn asker(&self, session: &str) -> Option<daemon_application::Asker> {
        self.askers
            .lock()
            .unwrap()
            .iter()
            .find(|asker| asker.session == session)
            .cloned()
    }

    fn fresh(&self) -> std::pin::Pin<Box<dyn std::future::Future<Output = ()> + Send + '_>> {
        *self.freshened.lock().unwrap() += 1;
        let round = {
            let mut rounds = self.rounds.lock().unwrap();
            (!rounds.is_empty()).then(|| rounds.remove(0))
        };
        if let Some(round) = round {
            round();
        }
        Box::pin(async {})
    }

    fn start_up_failed(&self) -> Option<String> {
        self.start_up.lock().unwrap().clone()
    }

    fn home_gone(&self) -> Option<String> {
        self.home_gone.lock().unwrap().clone()
    }

    fn rounds(&self) -> u32 {
        17
    }
}

/// Every session reads live but `ended-*` ones, unless a test set its
/// lock's state.
#[derive(Default)]
pub(crate) struct Locks(pub(crate) Mutex<std::collections::BTreeMap<String, State>>);

impl SessionStates for Locks {
    fn lock(&self, project_dir: &str, harp: &str) -> String {
        format!("{project_dir}/.ctxloom/sessions/{harp}/session.lock")
    }

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
    /// The pending messages the API reads, caught up only by
    /// [`Fixture::catch_up_pending`] (a round's, in the daemon).
    pub(crate) pending: pending_messages::PendingMessages,
    /// The sessions' timelines the watches' logs read, caught up with the
    /// index.
    pub(crate) timelines: session_timeline::SessionTimelines,
    pub(crate) api: Api<World>,
}

pub(crate) fn fixture(harps: Vec<&'static str>) -> Fixture {
    let world = World::new();
    let index = SessionIndexes::default();
    let board = daemon_board::Boards::default();
    let hosting = Arc::new(Loop::default());
    let places = Arc::new(FakePlaces::default());
    let locks = Arc::new(Locks::default());
    let pending = pending_messages::PendingMessages::default();
    let timelines = session_timeline::SessionTimelines::default();
    let api = Api::new(Parts {
        edge: Arc::clone(&world),
        index: index.clone(),
        pending: pending.clone(),
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
    })
    .timelining(timelines.clone());
    Fixture {
        world,
        index,
        board,
        hosting,
        places,
        locks,
        pending,
        timelines,
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
        self.catch_up_timelines().await;
    }

    /// The timelines alone caught up with what the store holds.
    pub(crate) async fn catch_up_timelines(&self) {
        let mut timelines = session_timeline::caught_up(self.timelines.clone());
        timelines
            .step(&self.world.store, self.world.registry.catalog())
            .await
            .unwrap();
    }

    /// The pending messages caught up with what the store holds: a round.
    pub(crate) async fn catch_up_pending(&self) {
        let mut caught = pending_messages::caught_up(self.pending.clone());
        caught
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

    /// The session `harp` is closed: a line of its timeline.
    pub(crate) async fn close(&self, harp: &str) {
        let name = format!("projects/calm-quiet-heron/sessions/{harp}");
        let root = sessions::SessionName::parse(&name).unwrap().root();
        let cover = Cover {
            domain: sessions::SESSION_DOMAIN.to_string(),
            root: Some(root),
            edition: None,
        };
        let closed = sessions::CloseSession {
            cause: "done".to_string(),
            requester: None,
        };
        let book = sessions::keyed_book(cover, &closed, &format!("closed:{harp}"));
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

pub(crate) fn resolve(commit: &str, name: &str) -> v1::ResolveProjectRequest {
    v1::ResolveProjectRequest {
        name: name.to_string(),
        caller: Some(v1::Caller {
            cwd: ROOT.to_string(),
            env: None,
            ..v1::Caller::default()
        }),
        project_id: PROJECT.to_string(),
        root: ROOT.to_string(),
        git_common_dir: COMMON.to_string(),
        root_commit: commit.to_string(),
    }
}

/// At most this many of a watch's events are read: more is a watch that
/// never ends.
const WATCH_BOUND: usize = 16;

/// `future`'s output, polled once: what a scripted host awaits (a
/// one-shot's end) never waits on anything.
pub(crate) fn now<F: std::future::Future>(future: F) -> F::Output {
    let mut future = std::pin::pin!(future);
    let mut cx = std::task::Context::from_waker(std::task::Waker::noop());
    match future.as_mut().poll(&mut cx) {
        std::task::Poll::Ready(output) => output,
        std::task::Poll::Pending => panic!("waited on something"),
    }
}

/// A watch's events, read to its end, which must come within
/// [`WATCH_BOUND`] of them. Bounded, as daemon-application's tests read
/// theirs, so a watch that never ends fails its test at once instead of
/// growing a `Vec` until the machine has no memory left (a mutant did that
/// twice on 2026-11-15, to about 70 GB).
pub(crate) fn watched(watch: impl Iterator<Item = v1::RunEvent>) -> Vec<v1::RunEvent> {
    let events: Vec<v1::RunEvent> = watch.take(WATCH_BOUND).collect();
    assert!(
        events.len() < WATCH_BOUND,
        "the watch never ended: {events:?}"
    );
    events
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
    // The rounds its loop has finished (step 10c U13, G5(b)).
    assert_eq!(daemon.rounds, 17);
}

// OD-52: a daemon whose start-up catch-up failed says so in GetDaemon, as
// the notice `daemon-api-start-up-incomplete` naming why; one whole says
// nothing.
#[tokio::test]
async fn get_daemon_warns_while_its_start_up_catch_up_has_failed() {
    let f = fixture(vec![]);
    assert!(f.api.get_daemon().await.unwrap().warnings.is_empty());
    let why = "the board's catch-up: the store is locked".to_string();
    *f.hosting.start_up.lock().unwrap() = Some(why.clone());
    let daemon = f.api.get_daemon().await.unwrap();
    assert_eq!(daemon.warnings, [crate::StartUpIncomplete { why }.notice()]);
    assert_eq!(daemon.warnings[0].code, "daemon-api-start-up-incomplete");
    // Cleared once a later catch-up succeeded.
    *f.hosting.start_up.lock().unwrap() = None;
    assert!(f.api.get_daemon().await.unwrap().warnings.is_empty());
}

// OD-55: what the edge attaches to every answer is the daemon's standing
// warnings as GetDaemon says them: the start-up's and the hand-over's, in
// that order, and none once neither stands.
#[tokio::test]
async fn the_calls_warnings_are_the_daemons_standing_ones() {
    let f = hosting_a_child();
    assert!(Calls::warnings(&f.api).is_empty());
    let why = "the board's catch-up: the store is locked".to_string();
    *f.hosting.start_up.lock().unwrap() = Some(why.clone());
    f.api.stop_daemon(drain_asked()).await.unwrap();
    let standing = Calls::warnings(&f.api);
    assert_eq!(standing, f.api.get_daemon().await.unwrap().warnings);
    let codes: Vec<&str> = standing.iter().map(|n| n.code.as_str()).collect();
    assert_eq!(
        codes,
        ["daemon-api-start-up-incomplete", "daemon-api-handing-over"]
    );
    *f.hosting.start_up.lock().unwrap() = None;
    f.hosting.hosted.lock().unwrap().clear();
    assert!(Calls::warnings(&f.api).is_empty());
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

/// The fixture's daemon hosting the child `brisk-copper-moth`.
fn hosting_a_child() -> Fixture {
    let f = fixture(vec!["calm-quiet-heron"]);
    f.hosting.hosted.lock().unwrap().push(v1::HostedRun {
        harp: "brisk-copper-moth".to_string(),
        ..v1::HostedRun::default()
    });
    f
}

/// A drain asked (`daemon stop --drain`).
fn drain_asked() -> v1::StopDaemonRequest {
    v1::StopDaemonRequest {
        drain: true,
        ..v1::StopDaemonRequest::default()
    }
}

// OD-54: asked to drain, a daemon that hosts live runs is not refused, and
// ends none of them: it answers itself draining, with what it still hosts.
#[tokio::test]
async fn stop_daemon_asked_to_drain_hands_over_what_it_hosts_and_ends_none() {
    let f = hosting_a_child();
    let daemon = f.api.stop_daemon(drain_asked()).await.unwrap();
    assert_eq!(*f.hosting.drained.lock().unwrap(), 1);
    assert!(
        f.hosting.stopped.lock().unwrap().is_empty(),
        "nothing ended"
    );
    assert!(daemon.draining);
    assert_eq!(daemon.runs.len(), 1, "it still hosts its child");
    assert_eq!(
        *f.hosting.within.lock().unwrap(),
        [None],
        "the configured timeout"
    );
}

// Owner, 2026-11-15 (`ctxloom shutdown <duration>`): a drain asked with a
// timeout hands it to the loop, which waits that long for what it hosts.
#[tokio::test]
async fn a_drain_asked_with_a_timeout_hands_it_to_the_loop() {
    let f = hosting_a_child();
    let asked = v1::StopDaemonRequest {
        drain_timeout_seconds: Some(900),
        ..drain_asked()
    };
    f.api.stop_daemon(asked).await.unwrap();
    assert_eq!(
        *f.hosting.within.lock().unwrap(),
        [Some(std::time::Duration::from_secs(900))]
    );
}

// OD-54 (2): the drain warns AT ONCE: its answer (and GetDaemon's, while it
// drains) carries the notice `daemon-api-handing-over`, naming its stamp,
// what it hands over and the period a busy child is waited for.
#[tokio::test]
async fn a_drain_of_a_daemon_hosting_children_warns_at_once() {
    let f = hosting_a_child();
    let expected = crate::HandingOver {
        daemon: "0.1.0+abc".to_string(),
        harps: vec!["brisk-copper-moth".to_string()],
        seconds: 300,
    }
    .notice();
    let daemon = f.api.stop_daemon(drain_asked()).await.unwrap();
    assert_eq!(daemon.warnings, std::slice::from_ref(&expected));
    assert_eq!(f.api.get_daemon().await.unwrap().warnings, [expected]);
}

// OD-54: a drain with nothing hosted (it exits at once) warns of nothing,
// and a daemon not draining says no hand-over.
#[tokio::test]
async fn a_drain_hosting_nothing_warns_of_nothing() {
    let f = hosting_a_child();
    assert!(f.api.get_daemon().await.unwrap().warnings.is_empty());
    let f = fixture(vec![]);
    let daemon = f.api.stop_daemon(drain_asked()).await.unwrap();
    assert!(daemon.warnings.is_empty(), "{:?}", daemon.warnings);
}

// `even_if_live` wins over a drain: what it hosts is ended, not handed over.
#[tokio::test]
async fn stop_daemon_even_if_live_wins_over_a_drain() {
    let f = hosting_a_child();
    f.api
        .stop_daemon(v1::StopDaemonRequest {
            even_if_live: true,
            ..drain_asked()
        })
        .await
        .unwrap();
    assert_eq!(*f.hosting.stopped.lock().unwrap(), [true]);
    assert_eq!(*f.hosting.drained.lock().unwrap(), 0);
}

// s4-Q4: once asked to drain, every new call but GetDaemon, StopDaemon and
// the project's names is refused `DaemonDraining` (a precondition), naming
// the daemon's stamp and the sessions it still hosts; GetDaemon
// answers, and StopDaemon judges as it always does (an escalation's
// `even_if_live` still reaches it). The names (ResolveProject, GetProject)
// are served (owner, 2026-11-15: a config change lands while it drains), as
// are the calls on work already started (`WatchRun`, `DeleteHandOff`: runs'
// and hand_off's tests).
#[tokio::test]
async fn a_draining_daemon_refuses_every_new_call_but_its_own_two_and_the_names() {
    use crate::Calls;
    let f = hosting_a_child();
    let calls: &dyn Calls = &f.api;
    assert!(
        calls.resolve_project(resolve(COMMIT, "")).await.is_ok(),
        "open"
    );
    calls.stop_daemon(drain_asked()).await.unwrap();
    let expected = v1::DaemonDraining {
        version: "0.1.0+abc".to_string(),
        harps: vec!["brisk-copper-moth".to_string()],
    };
    assert!(
        calls.resolve_project(resolve(COMMIT, "")).await.is_ok(),
        "the project's names are served while it drains"
    );
    let listed = calls
        .list_sessions(v1::ListSessionsRequest {
            parent: "projects/-".to_string(),
            ..v1::ListSessionsRequest::default()
        })
        .await;
    let (code, draining): (_, v1::DaemonDraining) = refused(listed);
    assert_eq!(
        code,
        <v1::DaemonDraining as daemon_proto::Refusal>::REFUSAL_STATUS
    );
    assert_eq!(draining, expected);
    let created = calls.create_run(v1::CreateRunRequest::default()).await;
    assert_eq!(refused::<v1::DaemonDraining>(created).1, expected);
    let handed = calls
        .create_hand_off(v1::CreateHandOffRequest::default())
        .await;
    assert_eq!(refused::<v1::DaemonDraining>(handed).1, expected);
    let about = calls.get_daemon().await.unwrap();
    assert!(about.draining);
    let (_, hosting): (_, v1::DaemonHosting) =
        refused(calls.stop_daemon(v1::StopDaemonRequest::default()).await);
    assert_eq!(hosting.harps, ["brisk-copper-moth"]);
}

// The owner's ruling of 2026-11-17: once the daemon's home is gone, a call
// that would create files there (`CreateRun`, `CreateHandOff`) is refused
// `DaemonHomeGone` (a precondition), naming the home; a read is served.
#[tokio::test]
async fn a_daemon_whose_home_is_gone_refuses_every_call_that_would_create_there() {
    use crate::Calls;
    let f = hosting_a_child();
    let calls: &dyn Calls = &f.api;
    *f.hosting.home_gone.lock().unwrap() = Some("/home/u/.ctxloom".to_string());
    let expected = v1::DaemonHomeGone {
        home: "/home/u/.ctxloom".to_string(),
    };
    let created = calls.create_run(v1::CreateRunRequest::default()).await;
    let (code, gone): (_, v1::DaemonHomeGone) = refused(created);
    assert_eq!(
        code,
        <v1::DaemonHomeGone as daemon_proto::Refusal>::REFUSAL_STATUS
    );
    assert_eq!(gone, expected);
    let handed = calls
        .create_hand_off(v1::CreateHandOffRequest::default())
        .await;
    assert_eq!(refused::<v1::DaemonHomeGone>(handed).1, expected);
    assert!(calls.get_daemon().await.is_ok(), "a read is served");
}

// A refusal for a gone home is told in this crate's words
// (`daemon-api-home-gone`), a decline.
#[test]
fn a_home_gone_refusal_is_told_in_its_words() {
    let refusal = ApiRefusal::of(&v1::DaemonHomeGone {
        home: "/home/u/.ctxloom".to_string(),
    });
    let rejection = crate::rejection_of(&refusal.refusal).expect("known");
    assert_eq!(
        rejection,
        crate::Rejection::Api(crate::ApiRejection::HomeGone {
            home: "/home/u/.ctxloom".to_string(),
        })
    );
    assert_eq!(
        rejection.report().to_string(),
        "daemon-api-home-gone home=/home/u/.ctxloom"
    );
}

// A daemon's refusal of a call while it drains is told in this crate's
// words (`daemon-api-draining`), a decline.
#[test]
fn a_draining_refusal_is_told_in_its_words() {
    let refusal = ApiRefusal::of(&v1::DaemonDraining {
        version: "v0.1.0-a".to_string(),
        harps: vec!["brisk-copper-moth".to_string()],
    });
    let rejection = crate::rejection_of(&refusal.refusal).expect("known");
    assert_eq!(
        rejection,
        crate::Rejection::Api(crate::ApiRejection::Draining {
            daemon: "v0.1.0-a".to_string(),
            harps: vec!["brisk-copper-moth".to_string()],
        })
    );
    assert_eq!(
        rejection.report().to_string(),
        "daemon-api-draining daemon=v0.1.0-a harps=brisk-copper-moth"
    );
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
        caller: Some(v1::Caller {
            cwd: "/work/calm-wt".to_string(),
            ..v1::Caller::default()
        }),
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
    let (_, changed): (_, sessions_proto::v1::ProjectIdentityMismatch) =
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
    let (_, changed): (_, sessions_proto::v1::ProjectIdentityMismatch) =
        refused(f.api.resolve_project(outside).await);
    assert_eq!(changed.project, "projects/calm-quiet-heron");
    assert_eq!(changed.registered_commit, COMMIT);
    assert_eq!(changed.found_commit, "");
    let other = "2222222222222222222222222222222222222222";
    let (_, changed): (_, sessions_proto::v1::ProjectIdentityMismatch) =
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
    // Read fresh (slice 3: a session's call retried on a daemon just
    // started finds what was registered before it).
    assert_eq!(*f.hosting.freshened.lock().unwrap(), 1);
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
            })
            .await,
    );
    assert_eq!(missing.name, "projects/calm-quiet-heron/sessions/far-heron");
    let (_, invalid): (_, v1::NameInvalid) = refused(
        f.api
            .get_session(v1::GetSessionRequest {
                name: "amber-swift-owl".to_string(),
            })
            .await,
    );
    assert_eq!(invalid.reason, "shape");
}

/// Append `events` to the run of `session` (its id text) for `incarnation`,
/// from its stream's start.
async fn a_run(f: &Fixture, session: &str, incarnation: u32, events: Vec<runs::RunEvent>) {
    use angzarr_embedded::{Payload, Write};
    let root = runs::run_root(session, incarnation);
    let pages: Vec<EventPage> = (0..)
        .zip(events)
        .map(|(sequence, event)| EventPage {
            sequence,
            body: Some(angzarr_embedded::v1::event_page::Body::Event(
                event.to_any(),
            )),
            meta: None,
        })
        .collect();
    let stream = angzarr_embedded::StreamRef::main(
        runs::RUN_DOMAIN,
        uuid::Uuid::from_slice(&root.value).unwrap(),
    );
    angzarr_embedded::EventStore::write(&f.world.store, Write::append(stream, 0, &pages))
        .await
        .unwrap();
}

// Step 9 (owner ruling 2026-11-17): GetSession says what the session's
// latest run was launched with, as its enqueue journaled it, and how its
// last turn failed.
#[tokio::test]
async fn get_session_says_what_its_latest_run_was_given_and_how_its_last_turn_failed() {
    let f = fixture(vec!["calm-quiet-heron"]);
    f.api.resolve_project(resolve(COMMIT, "")).await.unwrap();
    f.mint("amber-swift-owl").await;
    f.catch_up().await;
    let name = "projects/calm-quiet-heron/sessions/amber-swift-owl";
    let get = || {
        f.api.get_session(v1::GetSessionRequest {
            name: name.to_string(),
        })
    };
    let before = get().await.unwrap();
    assert_eq!(before.grant, None, "no run yet");
    assert_eq!(before.last_failure, None);
    a_run(
        &f,
        &before.uid,
        1,
        vec![
            runs::RunEvent::RunEnqueued(runs::RunEnqueued {
                session: before.uid.clone(),
                incarnation: 1,
                mode: runs::RunMode::Structured,
                launcher_lock: String::new(),
                grant: Some(runs::Grant {
                    mcp_servers: vec!["ctxloom".to_string(), "review-tools".to_string()],
                    permissions: r#"{"defaultMode":"plan"}"#.to_string(),
                }),
            }),
            runs::RunEvent::TurnQueued(runs::TurnQueued {
                prompt_ref: String::new(),
                resume: true,
                message_ids: vec!["m1".to_string()],
                urgency: runs::TurnUrgency::Wake,
            }),
            runs::RunEvent::TurnFailedRecorded(runs::TurnFailedRecorded {
                turn: 1,
                class: runs::TurnFailureClass::RateLimited,
                detail: "429".to_string(),
                message_ids: vec!["m1".to_string()],
            }),
        ],
    )
    .await;
    f.catch_up().await;
    let shown = get().await.unwrap();
    assert_eq!(
        shown.grant.map(|g| g.mcp_servers),
        Some(vec!["ctxloom".to_string(), "review-tools".to_string()])
    );
    let failure = shown.last_failure.expect("its last turn failed");
    assert_eq!(failure.kind, "rate-limited");
    assert_eq!(failure.detail, "429");
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
            })
            .await,
    );
    assert_eq!(invalid.name, name);
    assert_eq!(invalid.reason, "whitespace");
}

// Slice 3 step 5: GetTranscript of a session whose hooks delivered nothing
// is refused `TranscriptEmpty`; one nothing holds, not found; a name that is
// no session's transcript, invalid.
#[tokio::test]
async fn get_transcript_refuses_what_has_no_lines() {
    let f = fixture(vec!["calm-quiet-heron"]);
    f.api.resolve_project(resolve(COMMIT, "")).await.unwrap();
    f.mint("amber-swift-owl").await;
    f.catch_up().await;
    let name = "projects/calm-quiet-heron/sessions/amber-swift-owl";
    let (code, empty): (_, v1::TranscriptEmpty) = refused(
        f.api
            .get_transcript(v1::GetTranscriptRequest {
                name: format!("{name}/transcript"),
            })
            .await,
    );
    assert_eq!(
        code,
        <v1::TranscriptEmpty as daemon_proto::Refusal>::REFUSAL_STATUS
    );
    assert_eq!(empty.name, name);
    let (_, missing): (_, v1::SessionNotFound) = refused(
        f.api
            .get_transcript(v1::GetTranscriptRequest {
                name: "projects/calm-quiet-heron/sessions/far-heron/transcript".to_string(),
            })
            .await,
    );
    assert_eq!(missing.name, "projects/calm-quiet-heron/sessions/far-heron");
    let (_, invalid): (_, v1::NameInvalid) = refused(
        f.api
            .get_transcript(v1::GetTranscriptRequest {
                name: "amber-swift-owl/transcript".to_string(),
            })
            .await,
    );
    assert_eq!(invalid.reason, "shape");
}

/// The prompt `prompt` the session `harp`'s hooks delivered: a capture fact
/// on its stream, keyed as a hook keys it.
async fn prompted(f: &Fixture, harp: &str, prompt_id: &str, prompt: &str) {
    use angzarr_embedded::markup::DomainMessage;
    use angzarr_embedded::v1::{command_page, CommandPage, Fact, FactOrigin};
    let name = format!("projects/calm-quiet-heron/sessions/{harp}");
    let root = sessions::SessionName::parse(&name).unwrap().root();
    let cover = Cover {
        domain: sessions::SESSION_DOMAIN.to_string(),
        root: Some(root),
        edition: None,
    };
    let fact = sessions::RecordPromptSubmitted {
        prompt_id: prompt_id.to_string(),
        prompt: prompt.to_string(),
        ..Default::default()
    };
    let book = CommandBook {
        cover: Some(cover),
        page: Some(CommandPage {
            body: Some(command_page::Body::Fact(Fact {
                payload: Some(angzarr_embedded::Any::from_msg(&fact.to_wire()).unwrap()),
                origin: Some(FactOrigin {
                    external_id: format!("hook:prompt:{harp}:{prompt_id}"),
                    description: String::new(),
                }),
            })),
            ..CommandPage::default()
        }),
    };
    f.world.handle(book).await.unwrap();
}

// GetTranscript answers each entry the session's hooks delivered, and no
// blank one.
#[tokio::test]
async fn get_transcript_answers_each_captured_line() {
    let f = fixture(vec!["calm-quiet-heron"]);
    f.api.resolve_project(resolve(COMMIT, "")).await.unwrap();
    f.mint("amber-swift-owl").await;
    prompted(&f, "amber-swift-owl", "p1", "first words").await;
    prompted(&f, "amber-swift-owl", "p2", "second words").await;
    f.catch_up().await;
    let name = "projects/calm-quiet-heron/sessions/amber-swift-owl";
    let transcript = f
        .api
        .get_transcript(v1::GetTranscriptRequest {
            name: format!("{name}/transcript"),
        })
        .await
        .unwrap();
    assert_eq!(transcript.name, format!("{name}/transcript"));
    let prompts: Vec<&str> = transcript
        .entries
        .iter()
        .filter_map(|entry| match &entry.entry {
            Some(v1::log_entry::Entry::Prompt(prompt)) => Some(prompt.text.as_str()),
            _ => None,
        })
        .collect();
    assert_eq!(prompts, ["first words", "second words"]);
    assert_eq!(transcript.entries.len(), 2, "{:?}", transcript.entries);
}

/// The trees a read sees, from a table: no files, each scan counted.
#[derive(Default)]
pub(crate) struct Gits {
    /// How many times its scanner scanned a tree.
    pub(crate) scans: Arc<std::sync::atomic::AtomicU32>,
    /// How many times its scanner was told to forget what it saw.
    pub(crate) forgot: Arc<std::sync::atomic::AtomicU32>,
}

impl library_api::Observers for Gits {
    fn scanner(&self) -> Arc<dyn ctxloom_ports::LibraryScanner> {
        Arc::new(NoFiles {
            scans: Arc::clone(&self.scans),
            forgot: Arc::clone(&self.forgot),
        })
    }

    fn home_tree(&self) -> Option<String> {
        None
    }
}

/// A scanner that finds no file (these tests' trees are empty), counting
/// each scan and each time it is told to forget.
struct NoFiles {
    scans: Arc<std::sync::atomic::AtomicU32>,
    forgot: Arc<std::sync::atomic::AtomicU32>,
}

impl ctxloom_ports::LibraryScanner for NoFiles {
    fn scan(
        &self,
        _: &str,
    ) -> Result<std::collections::BTreeMap<String, String>, ctxloom_ports::Failure> {
        self.scans.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
        Ok(Default::default())
    }
    fn content_ids(
        &self,
        _: &str,
        _: &[&str],
    ) -> Result<std::collections::BTreeMap<String, String>, ctxloom_ports::Failure> {
        Ok(Default::default())
    }
    fn forget(&self) {
        self.forgot
            .fetch_add(1, std::sync::atomic::Ordering::SeqCst);
    }
}

struct OneHarp;

impl library_api::CheckoutHarps for OneHarp {
    fn harp(&self, _: &str) -> Result<String, ctxloom_ports::Failure> {
        Ok("brisk-copper-moth".to_string())
    }

    fn fresh(&self) -> String {
        "brisk-copper-moth".to_string()
    }
}

pub(crate) fn observing(world: &World, gits: &Arc<Gits>) -> crate::Observing {
    crate::Observing {
        checkouts: world.checkouts.clone(),
        harps: Arc::new(OneHarp),
        observers: Arc::clone(gits) as Arc<dyn library_api::Observers>,
        operations: Arc::new(NoOperations),
        clock: Arc::new(|| 0),
        caches: Arc::new(NoCaches),
    }
}

/// No cache anywhere: these tests' cleans find nothing to take.
struct NoCaches;

impl crate::Caches for NoCaches {
    fn cache(&self, tree: &str) -> Arc<dyn ctxloom_ports::ProjectCache> {
        Arc::new(NoCache(tree.to_string()))
    }
}

struct NoCache(String);

impl ctxloom_ports::ProjectCache for NoCache {
    fn app_dir(&self) -> String {
        self.0.clone()
    }

    fn measure(&self, _: &str) -> Result<Option<u64>, ctxloom_ports::Failure> {
        Ok(None)
    }

    fn remove(&self, _: &str) -> Result<(), ctxloom_ports::Failure> {
        Ok(())
    }
}

/// Operations that make nothing and sweep nothing: these tests' writes
/// are never carried out.
struct NoOperations;

impl ctxloom_ports::Operations for NoOperations {
    fn operate(
        &self,
        _: &str,
        _: &str,
        _: &ctxloom_ports::Operation,
        _: &dyn Fn(ctxloom_ports::Point),
    ) -> Result<ctxloom_ports::Operated, ctxloom_ports::Failure> {
        Ok(ctxloom_ports::Operated::Done)
    }

    fn sweep_staging(&self, _: &str) -> Result<(), ctxloom_ports::Failure> {
        Ok(())
    }

    fn purge_slot(&self, _: &str, _: &str) -> Result<(), ctxloom_ports::Failure> {
        Ok(())
    }
}

// Step 10c U13 rework (`context refresh`): an assembly asked to refresh
// has the daemon forget what it saw of the trees before its read observes
// them; a plain one forgets nothing.
#[tokio::test]
async fn test_an_assembly_asked_to_refresh_forgets_what_was_seen_first() {
    let f = fixture(vec![]);
    let gits = Arc::new(Gits::default());
    let api = f.api.observing(observing(&f.world, &gits));
    let forgot = || gits.forgot.load(std::sync::atomic::Ordering::SeqCst);
    let _ = api
        .assemble_context(v1::AssembleContextRequest {
            name: "projects/calm-quiet-heron".to_string(),
            ..v1::AssembleContextRequest::default()
        })
        .await;
    assert_eq!(forgot(), 0);
    let _ = api
        .assemble_context(v1::AssembleContextRequest {
            name: "projects/calm-quiet-heron".to_string(),
            refresh: true,
            ..v1::AssembleContextRequest::default()
        })
        .await;
    assert_eq!(forgot(), 1);
}
