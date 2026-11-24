//! The Bus proof (Unit 10e, Stage 1): a `PendingModel` fed by
//! angzarr-embedded's `Bus` (an EPHEMERAL projector subscription, its marks
//! in memory, folding every stream from sequence 0 each serve) equals the
//! one a `CatchUp` feeds, which is how the launcher holds it today. Run over
//! the in-memory store and over SQLite (angzarr-store-sqlite, the store
//! ctxloom runs), so the consumers can move onto the Bus with the model
//! unchanged. Tests only: nothing here is production behaviour.
//!
//! What each test proves:
//!
//! - `a_bus_fed_model_equals_a_catch_up_fed_one_*`: the same events, either
//!   fed, either credit, either way the bus follows the store.
//! - `a_purge_*`: a stream purged under a live subscription is handed gone
//!   and leaves the bus-fed model as it leaves the catch-up one; a fresh
//!   subscription after the purge agrees too.
//! - `a_subscription_dropped_mid_stream_*`: dropping the serve (a crash)
//!   leaves no mark behind (the store holds no position under the model's
//!   name), and a fresh subscription folds from 0 to the same model.
//! - `a_commit_after_the_subscription_is_live_*` (the stall probe): events
//!   appended AFTER the subscription is live, to a stream already delivered
//!   and to new ones, reach the model within a bounded wait, under every
//!   way a bus is woken (the store's push, its poll, both) and followed
//!   (the change feed, a full scan), from the served handle and from
//!   another handle on the file (another process sees only the poll).
//!
//! The race rule: a test that waits for the bus forces the interleaving it
//! is about. The driver of a live serve runs inside the serve's own
//! `select!`, wakes only on the bus's idle (`more` is asked each time it is
//! idle), and its wait is bounded (`BOUND`), so a bus that stalls with events
//! owed fails with what was owed, never hangs.

use std::future::Future;
use std::num::NonZeroUsize;
use std::pin::Pin;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::Arc;
use std::time::Duration;

use angzarr_embedded::angzarr_bus::{Bus as _, Credit, Following, Scope, Subscription};
use angzarr_embedded::angzarr_ports::Changes;
use angzarr_embedded::angzarr_router::error::CodedError;
use angzarr_embedded::angzarr_router::hpb::{ProjectRequest, ProjectResponse};
use angzarr_embedded::angzarr_router::router::{DynProjectorHandler, ProjectorHandler};
use angzarr_embedded::angzarr_store_sqlite::SqliteStore;
use angzarr_embedded::testing::MemoryStore;
use angzarr_embedded::v1::EventBook;
use angzarr_embedded::{
    Bus, BusPolicy, CatchUp, CommitNotify, Host, Kernel, Misconfigured, Outbox, Processed, Purge,
    ReadStore, Registry, Store, StreamRef, Timestamp, Wait, Watch, Write,
};
use runs::{EndCause, RunMode, RUN_DOMAIN};
use sessions::SESSION_DOMAIN;
use test_dir::TestDir;
use tokio::sync::Notify;

use super::*;
use crate::{Owed, PendingLaunch, PendingModel, StartedRun};

/// The longest a test waits for a bus to deliver what it owes. Never the
/// pace of a passing test (it waits on the bus's idle, not on a clock).
const BOUND: Duration = Duration::from_secs(10);

/// The model's name on the bus, and so the name its positions would be
/// stored under were they stored.
const NAME: &str = "pending-launches";

/// A store both the bus and the catch-up can run on, and a test can write,
/// purge and wake: what the in-memory store and SQLite both are.
trait Backend:
    Store + Outbox + ReadStore + Processed + Watch + Purge + CommitNotify + Changes + Sync
{
}

impl<T> Backend for T where
    T: Store + Outbox + ReadStore + Processed + Watch + Purge + CommitNotify + Changes + Sync
{
}

// ------------------------------------------------------------------ events

/// Session `n`'s `IncarnationOwed` at `sequence` of its stream.
fn owed_at(n: u8, incarnation: u32, sequence: u64) -> EventBook {
    let (root, _) = session_root(n);
    let event = SessionEvent::IncarnationOwed(IncarnationOwed {
        incarnation,
        reason: "new".to_string(),
    });
    book(SESSION_DOMAIN, root, vec![page(sequence, event.to_any())])
}

/// What exists before the subscription is live.
fn initial() -> Vec<EventBook> {
    vec![
        minted(1, "amber-swift-owl"),
        owed_at(1, 1, 1),
        enqueued(1, 1, RunMode::OneShot),
        minted(2, "brisk-calm-fox"),
        enqueued(2, 1, RunMode::Interactive),
        later(2, 1, 1, started()),
        owed(4, 1),
    ]
}

/// What is appended while it is live: to streams it has delivered
/// (run 1's start) and to new ones.
fn afterwards() -> Vec<EventBook> {
    vec![
        later(1, 1, 1, started()),
        minted(3, "dusty-keen-lynx"),
        enqueued(3, 1, RunMode::Structured),
        later(3, 1, 1, started()),
        later(3, 1, 2, ended_by(EndCause::RunnerExit)),
        enqueued(5, 1, RunMode::OneShot),
    ]
}

/// Every event of `books`.
fn events_of(books: &[EventBook]) -> usize {
    books.iter().map(|b| b.pages.len()).sum()
}

/// A book's stream as the store names it.
fn root_of_book(b: &EventBook) -> (String, uuid::Uuid) {
    let cover = b.cover.as_ref().expect("a cover");
    let root = cover.root.as_ref().expect("a root");
    (
        cover.domain.clone(),
        uuid::Uuid::from_slice(&root.value).expect("sixteen bytes"),
    )
}

/// Append `b`'s pages to its stream, at the length they start from.
async fn append<S: Backend>(store: &S, b: &EventBook) {
    let (domain, root) = root_of_book(b);
    let stream = StreamRef::main(&domain, root);
    let expected = b.pages[0].sequence;
    store
        .write(Write::append(stream, expected, &b.pages))
        .await
        .expect("the append lands");
}

async fn append_all<S: Backend>(store: &S, books: &[EventBook]) {
    for b in books {
        append(store, b).await;
    }
}

/// Purge the stream of `domain` rooted `root`, which holds `len` events.
async fn purge<S: Backend>(store: &S, domain: &str, root: &Uuid, len: u64) {
    let root = uuid::Uuid::from_slice(&root.value).expect("sixteen bytes");
    store
        .purge(StreamRef::main(domain, root), len)
        .await
        .expect("the purge lands");
}

// ------------------------------------------------------------------- views

/// Everything the model answers.
#[derive(Debug, PartialEq)]
struct Snap {
    launches: Vec<PendingLaunch>,
    started: Vec<StartedRun>,
    owed: Vec<Owed>,
}

fn snap(model: &PendingModel) -> Snap {
    let pending = model.pending();
    Snap {
        launches: pending.launches(),
        started: pending.started(),
        owed: pending.owed(),
    }
}

/// The model fed as the launcher feeds it today: `CatchUp` steps, held in
/// memory, the record kept across steps (it is what finds a purge).
struct Oracle {
    catch_up: CatchUp<PendingModel>,
    registry: Registry,
}

impl Oracle {
    fn new() -> Oracle {
        Oracle {
            catch_up: crate::catch_up(),
            registry: Registry::new(),
        }
    }

    /// One step over `store`, and what the model then answers.
    async fn step<S: Backend>(&mut self, store: &S) -> Snap {
        let caught = self
            .catch_up
            .step(store, self.registry.catalog())
            .await
            .expect("the step reads the store");
        assert!(caught.failures().is_empty(), "{:?}", caught.failures());
        snap(self.catch_up.projector())
    }
}

// --------------------------------------------------------------------- bus

/// How the bus follows the store's commits.
#[derive(Clone, Copy, Debug)]
enum Follow {
    /// The store's change feed names the streams a commit touched.
    Changes,
    /// A commit lists every stream of the scope.
    FullScan,
}

/// What wakes the bus.
#[derive(Clone, Copy, Debug)]
enum Wake {
    /// The store's push alone; the timer never fires.
    Push,
    /// The store's push and a poll.
    PushAndPoll,
    /// The poll alone: how a commit of another process is seen.
    Poll,
}

#[derive(Clone, Copy, Debug)]
struct Rig {
    follow: Follow,
    wake: Wake,
}

/// The rigs a model is served in, all of them.
fn rigs() -> Vec<Rig> {
    let mut all = Vec::new();
    for follow in [Follow::Changes, Follow::FullScan] {
        for wake in [Wake::Push, Wake::PushAndPoll, Wake::Poll] {
            all.push(Rig { follow, wake });
        }
    }
    all
}

/// A timer that never fires: only a commit wakes the bus.
struct Never;

impl Wait for Never {
    fn wait(&self, _: Duration) -> Pin<Box<dyn Future<Output = ()> + Send + '_>> {
        Box::pin(std::future::pending())
    }

    fn draw(&self) -> f64 {
        0.0
    }
}

/// A timer that fires after the interval it is asked for.
struct Ticks;

impl Wait for Ticks {
    fn wait(&self, after: Duration) -> Pin<Box<dyn Future<Output = ()> + Send + '_>> {
        Box::pin(tokio::time::sleep(after))
    }

    fn draw(&self) -> f64 {
        0.0
    }
}

/// How often a polling bus looks.
const POLL: Duration = Duration::from_millis(15);

fn clock() -> Timestamp {
    Timestamp {
        seconds: 50,
        nanos: 0,
    }
}

/// The model the bus feeds, counting what it was handed, and pausing (a
/// crash point) after the `pause_at`th event.
#[derive(Clone)]
struct Shared {
    model: Arc<PendingModel>,
    handled: Arc<AtomicUsize>,
    pause: Option<(usize, Arc<Notify>)>,
    /// After the `at`th event: say so on `parked`, and hold the handler
    /// until `release` (a commit lands meanwhile, with the bus busy).
    park: Option<Park>,
}

#[derive(Clone)]
struct Park {
    at: usize,
    parked: Arc<Notify>,
    release: Arc<Notify>,
}

impl Shared {
    fn new(model: &Arc<PendingModel>) -> Shared {
        Shared {
            model: Arc::clone(model),
            handled: Arc::new(AtomicUsize::new(0)),
            pause: None,
            park: None,
        }
    }

    fn handled(&self) -> usize {
        self.handled.load(Ordering::SeqCst)
    }
}

impl ProjectorHandler for Shared {
    fn name(&self) -> &str {
        NAME
    }

    async fn dispatch(&self, request: &ProjectRequest) -> Result<ProjectResponse, CodedError> {
        let answered = ProjectorHandler::dispatch(&*self.model, request).await;
        let handled = self.handled.fetch_add(1, Ordering::SeqCst) + 1;
        if let Some(park) = &self.park {
            if handled == park.at {
                park.parked.notify_one();
                park.release.notified().await;
            }
        }
        if let Some((at, note)) = &self.pause {
            if handled == *at {
                note.notify_one();
                // Hands the runtime back, so a driver waiting on the note
                // runs before the bus goes on.
                tokio::task::yield_now().await;
            }
        }
        answered
    }
}

/// `shared` on `bus`: an ephemeral projector subscription over both
/// domains.
fn subscribe(
    bus: &mut impl angzarr_embedded::angzarr_bus::Bus<
        Projector = Box<dyn DynProjectorHandler>,
        Policy = BusPolicy,
        Refused = Misconfigured,
    >,
    shared: &Shared,
    instances: usize,
) {
    let projector: Box<dyn DynProjectorHandler> = Box::new(shared.clone());
    let credit = Credit::new(NonZeroUsize::new(instances).expect("some instances"));
    let scope = Scope::of(SESSION_DOMAIN, &[RUN_DOMAIN]);
    bus.subscribe(Subscription::projector(scope, credit, projector).ephemeral())
        .expect("the subscription is accepted");
}

/// What the bus's idles told a driver.
#[derive(Default)]
struct Idles {
    count: AtomicUsize,
    note: Notify,
}

impl Idles {
    /// The bus is idle: nothing in flight, nothing owed but what failed.
    fn mark(&self) {
        self.count.fetch_add(1, Ordering::SeqCst);
        self.note.notify_one();
    }

    /// Wait, at the bus's idles and for at most `BOUND`, until `holds`. A
    /// bus that is idle with events owed and never woken again fails here,
    /// saying what it was owed.
    async fn until<T: std::fmt::Debug + PartialEq>(
        &self,
        what: &str,
        mut view: impl FnMut() -> T,
        want: &T,
    ) {
        let waited = tokio::time::timeout(BOUND, async {
            loop {
                if view() == *want {
                    return;
                }
                self.note.notified().await;
            }
        })
        .await;
        if waited.is_err() {
            panic!(
                "BUS STALL at {what}: after {} idles the bus never delivered what it was owed.\n  model: {:?}\n  owed:  {want:?}",
                self.count.load(Ordering::SeqCst),
                view()
            );
        }
    }
}

/// Serve `model` from `store` as `rig` says, while `driver` runs; ends when
/// the driver does (dropping the serve, as a crash does).
async fn live<S: Backend, D: Future>(
    store: &S,
    rig: Rig,
    shared: &Shared,
    instances: usize,
    driver: impl FnOnce(Arc<Idles>) -> D,
) -> D::Output {
    let registry = Registry::new();
    let kernel = Kernel::new(&registry);
    let wait: &dyn Wait = match rig.wake {
        Wake::Push => &Never,
        Wake::PushAndPoll | Wake::Poll => &Ticks,
    };
    let every = match rig.wake {
        Wake::Push => Duration::from_secs(3600),
        Wake::PushAndPoll | Wake::Poll => POLL,
    };
    let following = match rig.follow {
        Follow::Changes => Following::Changes(store),
        Follow::FullScan => Following::FullScan,
    };
    let mut host = Host::new(store, &kernel, &clock, wait, every, following);
    if !matches!(rig.wake, Wake::Poll) {
        host = host.pushed_by(store);
    }
    let mut bus = Bus::new(host);
    subscribe(&mut bus, shared, instances);
    let idles = Arc::new(Idles::default());
    let marks = Arc::clone(&idles);
    let serve = bus.serve(move |_| {
        marks.mark();
        true
    });
    tokio::select! {
        biased;
        out = driver(idles) => out,
        ended = serve => panic!("the bus ended while it was followed: {ended:?}"),
    }
}

/// Serve `shared` from `store` until the bus is idle once, then stop: what
/// a process does that reads the whole store into a model, once.
async fn drain<S: Backend>(store: &S, rig: Rig, shared: &Shared, instances: usize) {
    let registry = Registry::new();
    let kernel = Kernel::new(&registry);
    let wait: &dyn Wait = match rig.wake {
        Wake::Push => &Never,
        Wake::PushAndPoll | Wake::Poll => &Ticks,
    };
    let following = match rig.follow {
        Follow::Changes => Following::Changes(store),
        Follow::FullScan => Following::FullScan,
    };
    let mut host = Host::new(store, &kernel, &clock, wait, POLL, following);
    if !matches!(rig.wake, Wake::Poll) {
        host = host.pushed_by(store);
    }
    let mut bus = Bus::new(host);
    subscribe(&mut bus, shared, instances);
    tokio::time::timeout(BOUND, bus.serve(|_| false))
        .await
        .expect("the bus drained")
        .expect("the bus served");
}

// ------------------------------------------------------------------ stores

/// The SQLite store, in a directory of its own.
struct Sqlite {
    _dir: TestDir,
    path: std::path::PathBuf,
    store: SqliteStore,
}

impl Sqlite {
    async fn new() -> Sqlite {
        let dir = TestDir::new("pending-launches-bus");
        let path = dir.path().join("ctxloom.db");
        let store = SqliteStore::open(&path).await.expect("the store opens");
        Sqlite {
            _dir: dir,
            path,
            store,
        }
    }

    /// Another handle on the file: what another process is, to the push
    /// (which is shared in this process, so a poll-only bus is the other
    /// process's reader).
    async fn other(&self) -> SqliteStore {
        SqliteStore::open(&self.path)
            .await
            .expect("a second handle")
    }

    async fn close(self) {
        self.store.close().await.expect("the store closes");
    }
}

// ----------------------------------------------------------------- the 4 +

/// Every way the one event sequence is fed, equal to the catch-up's.
async fn equal_on<S: Backend>(store: &S) {
    append_all(store, &initial()).await;
    append_all(store, &afterwards()).await;
    let want = Oracle::new().step(store).await;
    assert!(
        !want.launches.is_empty() && !want.started.is_empty() && !want.owed.is_empty(),
        "the sequence leaves something of each kind: {want:?}"
    );
    for rig in rigs() {
        for instances in [1, 4] {
            let model = Arc::new(PendingModel::default());
            let shared = Shared::new(&model);
            drain(store, rig, &shared, instances).await;
            assert_eq!(
                shared.handled(),
                events_of(&initial()) + events_of(&afterwards()),
                "{rig:?} x{instances}: every event, from sequence 0"
            );
            assert_eq!(snap(&model), want, "{rig:?} x{instances}");
        }
    }
}

#[tokio::test]
async fn a_bus_fed_model_equals_a_catch_up_fed_one_on_the_memory_store() {
    equal_on(&MemoryStore::new()).await;
}

#[tokio::test]
async fn a_bus_fed_model_equals_a_catch_up_fed_one_on_sqlite() {
    let db = Sqlite::new().await;
    equal_on(&db.store).await;
    db.close().await;
}

/// The streams the purge case removes: a run owed a runner, a run started,
/// and an incarnation owed: each of the model's three answers loses one.
async fn purge_those<S: Backend>(store: &S) {
    let (_, one) = session_root(1);
    let (_, two) = session_root(2);
    let (four, _) = session_root(4);
    purge(store, RUN_DOMAIN, &run_root(&one, 1), 1).await;
    purge(store, RUN_DOMAIN, &run_root(&two, 1), 2).await;
    purge(store, SESSION_DOMAIN, &four, 1).await;
}

/// A purge under a live subscription, from the served handle or another.
async fn purge_live_on<S: Backend>(store: &S, writer: &S, rig: Rig) {
    append_all(store, &initial()).await;
    let mut oracle = Oracle::new();
    let before = oracle.step(store).await;
    assert_eq!(before.started.len(), 1);
    assert_eq!(before.owed.len(), 1);
    let model = Arc::new(PendingModel::default());
    let shared = Shared::new(&model);
    live(store, rig, &shared, 2, |idles| async move {
        idles.until("the history", || snap(&model), &before).await;
        purge_those(writer).await;
        let after = oracle.step(store).await;
        assert_ne!(after, before, "the purge changes what is pending");
        assert!(
            after.started.is_empty() && after.owed.is_empty(),
            "{after:?}"
        );
        idles.until("the purge", || snap(&model), &after).await;
    })
    .await;
}

/// A purge, then a subscription of a new process.
async fn purge_then_restart_on<S: Backend>(store: &S, rig: Rig) {
    append_all(store, &initial()).await;
    let mut oracle = Oracle::new();
    let before = oracle.step(store).await;
    purge_those(store).await;
    // What a process that starts now reads: a catch-up of its own, from 0.
    // (The long-lived catch-up above differs from it by one answer: the
    // session whose run was purged owes its incarnation again once the model
    // is rebuilt from the store, and not in a model that had already seen the
    // run take it. That is the model's history, not the feed's; the live
    // purge test below holds the long-lived pair equal.)
    let after = Oracle::new().step(store).await;
    assert_ne!(after, before);
    let model = Arc::new(PendingModel::default());
    let shared = Shared::new(&model);
    drain(store, rig, &shared, 2).await;
    assert_eq!(snap(&model), after, "{rig:?}");
    // The purged streams are not delivered (gone from the store), the rest
    // are: a fresh subscription has no mark to find them gone by.
    assert!(shared.handled() < events_of(&initial()), "{rig:?}");
}

#[tokio::test]
async fn a_purge_under_a_live_subscription_leaves_both_models_equal_on_the_memory_store() {
    for rig in rigs() {
        let store = MemoryStore::new();
        purge_live_on(&store, &store, rig).await;
    }
}

#[tokio::test]
async fn a_purge_under_a_live_subscription_leaves_both_models_equal_on_sqlite() {
    for rig in rigs() {
        let db = Sqlite::new().await;
        let other = db.other().await;
        // A poll-only bus is another process's reader: write from the other
        // handle there, from the served one otherwise.
        match rig.wake {
            Wake::Poll => purge_live_on(&db.store, &other, rig).await,
            _ => purge_live_on(&db.store, &db.store, rig).await,
        }
        other.close().await.expect("the other handle closes");
        db.close().await;
    }
}

#[tokio::test]
async fn a_new_subscription_after_a_purge_equals_a_catch_up_on_the_memory_store() {
    for rig in rigs() {
        purge_then_restart_on(&MemoryStore::new(), rig).await;
    }
}

#[tokio::test]
async fn a_new_subscription_after_a_purge_equals_a_catch_up_on_sqlite() {
    for rig in rigs() {
        let db = Sqlite::new().await;
        purge_then_restart_on(&db.store, rig).await;
        db.close().await;
    }
}

/// Drop the serve after its third event, then serve a fresh model.
async fn restart_on<S: Backend>(store: &S, rig: Rig) {
    append_all(store, &initial()).await;
    append_all(store, &afterwards()).await;
    let want = Oracle::new().step(store).await;
    let total = events_of(&initial()) + events_of(&afterwards());
    assert!(
        store.processed(NAME).await.unwrap().is_empty(),
        "nothing is stored under the model's name to begin with"
    );

    // The first process: crashes (its serve dropped) after three events.
    let crashed = Arc::new(PendingModel::default());
    let note = Arc::new(Notify::new());
    let mut first = Shared::new(&crashed);
    first.pause = Some((3, Arc::clone(&note)));
    live(store, rig, &first, 1, |_| async {
        note.notified().await;
    })
    .await;
    assert_eq!(first.handled(), 3, "dropped mid-stream, not drained");
    assert_ne!(
        snap(&crashed),
        want,
        "the crashed model is partial: {rig:?}"
    );
    assert!(
        store.processed(NAME).await.unwrap().is_empty(),
        "an ephemeral subscription leaves no mark: {rig:?}"
    );

    // The next process: a fresh model, a fresh subscription, from 0.
    let model = Arc::new(PendingModel::default());
    let second = Shared::new(&model);
    drain(store, rig, &second, 1).await;
    assert_eq!(second.handled(), total, "every event again: {rig:?}");
    assert_eq!(snap(&model), want, "{rig:?}");
    assert!(
        store.processed(NAME).await.unwrap().is_empty(),
        "and still no mark: {rig:?}"
    );
}

#[tokio::test]
async fn a_subscription_dropped_mid_stream_and_restarted_folds_from_0_and_ends_equal_on_the_memory_store(
) {
    for rig in rigs() {
        restart_on(&MemoryStore::new(), rig).await;
    }
}

#[tokio::test]
async fn a_subscription_dropped_mid_stream_and_restarted_folds_from_0_and_ends_equal_on_sqlite() {
    for rig in rigs() {
        let db = Sqlite::new().await;
        restart_on(&db.store, rig).await;
        db.close().await;
    }
}

/// THE STALL PROBE. Events appended once the subscription is live reach the
/// model: first the history, then each later book committed on its own,
/// through `writer`.
async fn stall_probe_on<S: Backend>(store: &S, writer: &S, rig: Rig) {
    append_all(store, &initial()).await;
    let mut oracle = Oracle::new();
    let history = oracle.step(store).await;
    let model = Arc::new(PendingModel::default());
    let shared = Shared::new(&model);
    live(store, rig, &shared, 2, |idles| async move {
        idles.until("the history", || snap(&model), &history).await;
        for b in afterwards() {
            append(writer, &b).await;
        }
        let want = oracle.step(store).await;
        assert_ne!(want, history, "the later events change what is pending");
        idles
            .until("the later events", || snap(&model), &want)
            .await;
        // And one more, after the bus has been idle with nothing owed.
        append(writer, &later(5, 1, 1, started())).await;
        let last = oracle.step(store).await;
        assert_ne!(last, want);
        idles.until("a last event", || snap(&model), &last).await;
    })
    .await;
}

#[tokio::test]
async fn a_commit_after_the_subscription_is_live_reaches_the_model_on_the_memory_store() {
    for rig in rigs() {
        let store = MemoryStore::new();
        stall_probe_on(&store, &store, rig).await;
    }
}

/// One SQLite probe: `other` writes through a second handle.
async fn stall_probe_sqlite(rig: Rig, other: bool) {
    let db = Sqlite::new().await;
    let second = db.other().await;
    match other {
        true => stall_probe_on(&db.store, &second, rig).await,
        false => stall_probe_on(&db.store, &db.store, rig).await,
    }
    second.close().await.expect("the other handle closes");
    db.close().await;
}

macro_rules! probes {
    ($($name:ident: $follow:ident, $wake:ident, $other:expr;)*) => {
        $(
            #[tokio::test]
            async fn $name() {
                let rig = Rig { follow: Follow::$follow, wake: Wake::$wake };
                stall_probe_sqlite(rig, $other).await;
            }
        )*
    };
}

probes! {
    a_commit_after_the_subscription_is_live_reaches_the_model_on_sqlite_by_change_feed_and_push:
        Changes, Push, false;
    a_commit_after_the_subscription_is_live_reaches_the_model_on_sqlite_by_change_feed_and_push_from_another_handle:
        Changes, Push, true;
    a_commit_after_the_subscription_is_live_reaches_the_model_on_sqlite_by_full_scan_and_push:
        FullScan, Push, false;
    a_commit_after_the_subscription_is_live_reaches_the_model_on_sqlite_by_full_scan_and_push_from_another_handle:
        FullScan, Push, true;
    a_commit_after_the_subscription_is_live_reaches_the_model_on_sqlite_by_change_feed_and_poll_from_another_handle:
        Changes, Poll, true;
    a_commit_after_the_subscription_is_live_reaches_the_model_on_sqlite_by_full_scan_and_poll_from_another_handle:
        FullScan, Poll, true;
    a_commit_after_the_subscription_is_live_reaches_the_model_on_sqlite_by_change_feed_push_and_poll:
        Changes, PushAndPoll, false;
    a_commit_after_the_subscription_is_live_reaches_the_model_on_sqlite_by_full_scan_push_and_poll:
        FullScan, PushAndPoll, false;
}

/// THE STALL PROBE, forced. A commit lands while the bus is BUSY: its only
/// credit is inside a handler, so no feed is asking for a delivery (the pump
/// asks only while a slot is free), and nothing is armed to hear the commit.
/// The handler is held until the commit has landed, then released; what the
/// commit appended must still reach the model. Nothing here depends on how
/// quickly the store answers.
async fn commit_while_busy_on<S: Backend>(store: &S, rig: Rig) {
    append_all(store, &initial()).await;
    let mut oracle = Oracle::new();
    let history = oracle.step(store).await;
    let model = Arc::new(PendingModel::default());
    let mut shared = Shared::new(&model);
    let park = Park {
        at: events_of(&initial()) + 1,
        parked: Arc::new(Notify::new()),
        release: Arc::new(Notify::new()),
    };
    shared.park = Some(park.clone());
    live(store, rig, &shared, 1, |idles| async move {
        idles.until("the history", || snap(&model), &history).await;
        // The trigger: its handler parks, holding the bus's one credit.
        let mut later_books = afterwards();
        let trigger = later_books.remove(0);
        append(store, &trigger).await;
        park.parked.notified().await;
        // Committed while the bus is busy and nothing is armed.
        for b in &later_books {
            append(store, b).await;
        }
        park.release.notify_one();
        let want = oracle.step(store).await;
        assert_ne!(want, history);
        idles
            .until(
                "a commit that landed while the bus was busy",
                || snap(&model),
                &want,
            )
            .await;
    })
    .await;
}

#[tokio::test]
async fn a_commit_while_the_bus_is_busy_reaches_the_model_on_the_memory_store() {
    for rig in rigs() {
        commit_while_busy_on(&MemoryStore::new(), rig).await;
    }
}

#[tokio::test]
async fn a_commit_while_the_bus_is_busy_reaches_the_model_on_sqlite() {
    for rig in rigs() {
        let db = Sqlite::new().await;
        commit_while_busy_on(&db.store, rig).await;
        db.close().await;
    }
}
