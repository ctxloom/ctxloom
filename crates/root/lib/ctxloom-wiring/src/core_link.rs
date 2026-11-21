//! The core served in process (S5): the kernel's `App` over a store, the
//! exposure map, and the `embedded-edge` adapter over it, so `Facts`
//! and `Feeds` run against the real
//! `App::serve_in`. No transport: UDS, TCP and gRPC are not here.
//! `core_link.test.rs` runs the wiring.
//!
//! The core keeps its store in an `Arc` of its own ([`CoreStores`]), not
//! angzarr-embedded-host's `OneStore`, so the store can be had back once
//! every holder is let go and closed ([`InProcessCore::close`], rulings
//! 131, 140): a store dropped is closed on the driver's thread whenever that
//! runs, which a process that exits first never lets it do.

use std::sync::Arc;

use std::collections::BTreeMap;
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use angzarr_embedded::angzarr_store_sqlite::SqliteStore;
use angzarr_embedded::v1::Uuid as WireUuid;
use angzarr_embedded::{
    Catalog, Kernel, ListStreams, OnRetryable, Processed, Registry, Sampled, Store, StoreError,
    Timestamp,
};
use angzarr_embedded_host::config::Expose;
use angzarr_embedded_host::stores::Stores;
use angzarr_embedded_host::App;
use embedded_edge::EmbeddedEdge;
use library_tree::LibraryAggregate;
use runs::RunAggregate;
use scheduler::SchedulerAggregate;
use sessions::{NameClaimAggregate, ProjectAggregate, ProjectNameAggregate, SessionAggregate};
use std::num::NonZeroU32;
use trees::TreeAggregate;
use trust::TrustAggregate;

/// How many times the kernel runs a fact that keeps losing its race before it
/// parks it. In process nothing is far away, so no backoff separates the runs.
/// Parking writes the fact's dead letter, itself a store write: a store that
/// refuses every write parks nothing (the write of the letter fails too), and a
/// test that scripts losses scripts exactly this many.
pub(crate) const RACE_RUNS: NonZeroU32 = match NonZeroU32::new(3) {
    Some(runs) => runs,
    None => NonZeroU32::MIN,
};

/// The instant now, the system's: this is the composition root, where the
/// clock lives.
fn system_now() -> Timestamp {
    let since = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default();
    Timestamp {
        seconds: since.as_secs() as i64,
        nanos: since.subsec_nanos() as i32,
    }
}

/// The instant and the fresh command id each call is handled at: this is the
/// composition root, where the clock and the ids live.
fn sample() -> Sampled {
    Sampled {
        now: system_now(),
        command_id: WireUuid::from_bytes(*uuid::Uuid::now_v7().as_bytes()),
    }
}

/// The scheduler's clock (step 8 U5a): the system's, as each call's sample.
pub struct SystemClock;

impl scheduler_ports::Clock for SystemClock {
    fn now(&self) -> Timestamp {
        system_now()
    }
}

/// What registers one domain that arms alarms (step 8 U5a): its aggregate
/// if the core has not, its policy towards the scheduler (arming and
/// cancelling, `ArmAlarm`/`CancelAlarm`) and its delivery policy
/// (`scheduler::delivery!`, which hands it `RecordAlarmReached`).
pub type AlarmConsumer = fn(&mut Registry);

/// Every domain that arms alarms, registered in the daemon's core alone
/// (owner ruling 2026-11-16: the daemon commits every trigger, an ask and
/// its answer or withdrawal alike) and resumed at its start: the approvals'
/// expiry (step 8 U5, D12). A CLI command's core registers none.
pub const ALARM_CONSUMERS: &[AlarmConsumer] = &[approvals_expiry];

/// The approvals' expiry (step 8 U5): an ask arms `approval-expiry:<ask>`
/// for its time, its answer or withdrawal cancels it, and the alarm reached
/// is delivered to the ask, which expires if it still waits. Its aggregate
/// is the core's own.
fn approvals_expiry(registry: &mut Registry) {
    registry
        .policy(approvals::ArmsExpiry)
        .expect("the approvals' expiry arming registers")
        .policy(approvals::DeliverToApprovals)
        .expect("the approvals' expiry delivery registers");
}

/// The core's one store, as the application resolves it: every call's key
/// is `()`, shared with the core that keeps it, so it can be closed once
/// the application is let go. The daemon's is resumed at start (as
/// `OneStore`); a CLI command's never is (owner ruling 2026-11-16).
pub struct CoreStores<S> {
    store: Arc<S>,
    resumed: Resume,
}

/// Whether a core resumes, at its start, the policy work its store owes.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
enum Resume {
    /// The daemon's core: every policy source scanned, the owed steps run.
    AtStart,
    /// A CLI command's core: nothing scanned. It registers no policy and
    /// commits nothing a policy's source holds, so it owes none.
    Never,
}

impl<S: Store + Processed + ListStreams + Send + Sync> Stores for CoreStores<S> {
    type Key = ();
    type Store = Arc<S>;

    async fn store(&self, _key: &()) -> Result<Arc<S>, StoreError> {
        Ok(Arc::clone(&self.store))
    }

    // The daemon's resume at start runs the policy work a crash (or a
    // policy new to a domain with history) left owed: an alarm fired and
    // not yet delivered is delivered (`scheduling.test.rs`). Its cost grows
    // with the sources' history, which a CLI command's core never pays.
    fn at_start(&self) -> Vec<()> {
        match self.resumed {
            Resume::AtStart => vec![()],
            Resume::Never => Vec::new(),
        }
    }
}

/// The read models a core registers in its kernel's registry (S32, T16):
/// each a handle on a fold another driver (a catch-up) may share, run inline
/// after every commit of its sources, and what a `HandleSync` names. A core
/// a CLI command runs registers none (its folds would be partial, and it
/// serves no `HandleSync`); the daemon's registers every one.
#[derive(Clone, Default)]
pub struct Projectors {
    pub board: Option<daemon_board::Boards>,
    pub pending: Option<pending_messages::PendingMessages>,
    pub index: Option<session_index::SessionIndexes>,
    /// The projects' trusts (step 8 U1).
    pub trusts: Option<trust_index::TrustIndexes>,
    /// The permission asks (step 8 U4).
    pub approvals: Option<pending_approvals::PendingApprovals>,
    /// The alarms armed, which the daemon's scheduler reads (step 8 U5a).
    pub due: Option<crate::scheduling::SchedulerDues>,
    /// The library trees as last observed (step 10a U0).
    pub checkouts: Option<library_checkout::LibraryCheckouts>,
}

impl Projectors {
    /// Every read model the daemon registers, each empty, its gaps read from
    /// `store`.
    pub fn filled<S>(store: &Arc<S>) -> Projectors
    where
        S: Store + Send + Sync + 'static,
    {
        let fill = || -> Arc<dyn held_projection::Fill> { Arc::new(StoreFill(Arc::clone(store))) };
        Projectors {
            board: Some(daemon_board::Boards::filled(fill())),
            pending: Some(pending_messages::PendingMessages::filled(fill())),
            index: Some(session_index::SessionIndexes::filled(fill())),
            trusts: Some(trust_index::TrustIndexes::filled(fill())),
            approvals: Some(pending_approvals::PendingApprovals::filled(fill())),
            due: Some(crate::scheduling::SchedulerDues::filled(fill())),
            checkouts: Some(library_checkout::LibraryCheckouts::filled(fill())),
        }
    }

    /// Register each read model in `registry`, on its sources.
    fn register(self, registry: &mut Registry) {
        // The registrations are fixed here, so a failure is a bug of this
        // function, found by the first start.
        if let Some(board) = self.board {
            registry
                .projector_handler(&daemon_board::SOURCES, board)
                .expect("the board registers");
        }
        if let Some(pending) = self.pending {
            registry
                .projector_handler(&pending_messages::SOURCES, pending)
                .expect("the pending messages register");
        }
        if let Some(index) = self.index {
            registry
                .projector_handler(&session_index::SOURCES, index)
                .expect("the session index registers");
        }
        if let Some(trusts) = self.trusts {
            registry
                .projector_handler(&trust_index::SOURCES, trusts)
                .expect("the trust index registers");
        }
        if let Some(approvals) = self.approvals {
            registry
                .projector_handler(&pending_approvals::SOURCES, approvals)
                .expect("the pending approvals register");
        }
        if let Some(due) = self.due {
            registry
                .projector_handler(&[scheduler::SCHEDULER_DOMAIN], due)
                .expect("the due alarms register");
        }
        if let Some(checkouts) = self.checkouts {
            registry
                .projector_handler(&library_checkout::SOURCES, checkouts)
                .expect("the library checkouts register");
        }
    }
}

/// A read model's gaps, read from the core's store.
struct StoreFill<S>(Arc<S>);

impl<S: Store + Send + Sync> held_projection::Fill for StoreFill<S> {
    fn pages<'a>(
        &'a self,
        cover: &'a angzarr_embedded::v1::Cover,
        from: u64,
        to: u64,
    ) -> angzarr_embedded::angzarr_ports::BoxFuture<
        'a,
        Result<Vec<angzarr_embedded::v1::EventPage>, StoreError>,
    > {
        Box::pin(async move {
            let stream = angzarr_embedded::StreamRef::try_from(cover)?;
            Ok(self.0.read(stream, from, Some(to)).await?.pages)
        })
    }
}

/// The in-process core over one store.
pub struct InProcessCore<S>
where
    S: Store + Processed + ListStreams + Send + Sync,
{
    app: Arc<App<CoreStores<S>>>,
    edge: Arc<EmbeddedEdge<CoreStores<S>>>,
    store: Arc<S>,
}

/// Why a core's store was not closed: either is an error the run reports.
#[derive(Debug)]
pub enum Unclosed {
    /// Something outside the core still holds the store at the close: an
    /// edge leaked past the run's end, or a task still running with one.
    /// Never expected: everything the run made is consumed or joined before
    /// the close (see [`InProcessCore::close`]).
    StillHeld { holders: usize },
    /// The close failed.
    Failed(StoreError),
}

impl std::fmt::Display for Unclosed {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Unclosed::StillHeld { holders } => write!(
                f,
                "the run's store is still held ({holders} holders past the run's end) and was not closed"
            ),
            Unclosed::Failed(e) => write!(f, "the run's store did not close: {e}"),
        }
    }
}

impl std::error::Error for Unclosed {}

impl InProcessCore<SqliteStore> {
    /// Close the core's store, once nothing else can hold it (ruling 131),
    /// and wait for it: when this returns its file holds every commit with
    /// no log beside it.
    ///
    /// The holders are let go first, each by a move: the edges the use
    /// cases took (`Runs::into_core`), and the hook listener's, whose stop
    /// (`HookListener::stop`, angzarr's `Running::shutdown_within`, ruling
    /// 140) ends every connection and returns only once every copy of its
    /// service, and so of the edge, is released; the read models a caller
    /// kept of those registered (`start_with`), each holding the store
    /// through its gap fill; then the core's own
    /// application and edge (the core is consumed). Only then is the store
    /// taken by value and closed. The runtime the core served on is not
    /// shut down for it: nothing it runs may still hold the store. A holder
    /// that does is [`Unclosed::StillHeld`], an error.
    pub async fn close(self) -> Result<(), Unclosed> {
        let InProcessCore { app, edge, store } = self;
        drop(edge);
        drop(app);
        let store = Arc::try_unwrap(store).map_err(|still| Unclosed::StillHeld {
            holders: Arc::strong_count(&still),
        })?;
        store.close().await.map_err(Unclosed::Failed)
    }
}

impl<S> InProcessCore<S>
where
    S: Store + Processed + ListStreams + Send + Sync + 'static,
{
    /// A CLI command's core over `store`: the sessions, runs, trees, trust,
    /// library, approvals and scheduler aggregates registered; sessions and
    /// runs exposed to the network (the container bridge reaches them) and
    /// trees, trust, library and approvals to loopback; the kernel's retries off, so
    /// a fact at the race cap is parked at once. No read model, no policy
    /// (no alarm consumer), and no resume at start (owner ruling
    /// 2026-11-16): the daemon's core alone pays for owed policy work.
    pub async fn start(store: S) -> InProcessCore<S> {
        InProcessCore::begin(store, |_| Projectors::default(), &[], Resume::Never).await
    }

    /// The daemon's core: [`InProcessCore::start`]'s, with the read models
    /// `projectors` builds (given the store it will keep) registered in its
    /// kernel (S32), every domain that arms alarms ([`ALARM_CONSUMERS`]),
    /// and the policy work its store owes resumed at start.
    pub async fn start_with(
        store: S,
        projectors: impl FnOnce(&Arc<S>) -> Projectors,
    ) -> InProcessCore<S> {
        InProcessCore::start_consuming(store, projectors, ALARM_CONSUMERS).await
    }

    /// [`InProcessCore::start_with`], `consumers` the domains that arm
    /// alarms (production's are [`ALARM_CONSUMERS`]; a test's its own).
    pub(crate) async fn start_consuming(
        store: S,
        projectors: impl FnOnce(&Arc<S>) -> Projectors,
        consumers: &[AlarmConsumer],
    ) -> InProcessCore<S> {
        InProcessCore::begin(store, projectors, consumers, Resume::AtStart).await
    }

    async fn begin(
        store: S,
        projectors: impl FnOnce(&Arc<S>) -> Projectors,
        consumers: &[AlarmConsumer],
        resumed: Resume,
    ) -> InProcessCore<S> {
        let store = Arc::new(store);
        let mut registry = Registry::new();
        projectors(&store).register(&mut registry);
        // The registrations are fixed here, so a failure is a bug of this
        // function, found by the first start.
        registry
            .aggregate::<SessionAggregate>(SessionAggregate::default)
            .expect("the sessions aggregate registers");
        registry
            .aggregate::<NameClaimAggregate>(NameClaimAggregate::default)
            .expect("the session names aggregate registers");
        registry
            .aggregate::<RunAggregate>(RunAggregate::default)
            .expect("the runs aggregate registers");
        registry
            .aggregate::<TreeAggregate>(TreeAggregate::default)
            .expect("the trees aggregate registers");
        // A project's harp (S54): the project and its harp's claim.
        registry
            .aggregate::<ProjectAggregate>(ProjectAggregate::default)
            .expect("the projects aggregate registers");
        registry
            .aggregate::<ProjectNameAggregate>(ProjectNameAggregate::default)
            .expect("the project-names aggregate registers");
        // A project's trust (step 8 U1): the user's, on loopback alone.
        registry
            .aggregate::<TrustAggregate>(TrustAggregate::default)
            .expect("the trust aggregate registers");
        // A library tree (step 10a U0): what the daemon observed of it, and
        // what it intends to change there; the daemon's and the user's, on
        // loopback alone.
        registry
            .aggregate::<LibraryAggregate>(LibraryAggregate::default)
            .expect("the library aggregate registers");
        // The permission asks and their names (step 8 U4): an answer and a
        // withdrawal are the user's, on loopback; the ask and its name's
        // claim are the daemon's own.
        registry
            .aggregate::<approvals::ApprovalAggregate>(approvals::ApprovalAggregate::default)
            .expect("the approvals aggregate registers");
        registry
            .aggregate::<approvals::ApprovalNameAggregate>(
                approvals::ApprovalNameAggregate::default,
            )
            .expect("the approval names aggregate registers");
        // The scheduler (step 8 U5a): its streams are written by policies
        // and the daemon's clock loop alone, so it is exposed to nobody.
        registry
            .aggregate::<SchedulerAggregate>(SchedulerAggregate::default)
            .expect("the scheduler aggregate registers");
        for consumer in consumers {
            consumer(&mut registry);
        }
        let exposed = BTreeMap::from([
            (sessions::SESSION_DOMAIN.to_string(), Expose::Ip),
            (runs::RUN_DOMAIN.to_string(), Expose::Ip),
            (trees::TREE_DOMAIN.to_string(), Expose::Loopback),
            (trust::TRUST_DOMAIN.to_string(), Expose::Loopback),
            (approvals::APPROVALS_DOMAIN.to_string(), Expose::Loopback),
            (library_tree::LIBRARY_DOMAIN.to_string(), Expose::Loopback),
        ]);
        let retries = OnRetryable {
            max_retries: 0,
            max_race_runs: RACE_RUNS,
            initial_backoff: Duration::ZERO,
            multiplier: 1.0,
            max_backoff: Duration::ZERO,
            jitter: 0.0,
        };
        let stores = CoreStores {
            store: Arc::clone(&store),
            resumed,
        };
        let app = App::builder(stores, registry)
            .exposed(exposed.clone())
            .on_retryable(retries)
            .start()
            .await;
        let app = Arc::new(app);
        InProcessCore {
            edge: Arc::new(EmbeddedEdge::new(Arc::clone(&app), sample).exposed(exposed)),
            app,
            store,
        }
    }

    /// The edge the use cases take.
    pub fn edge(&self) -> Arc<EmbeddedEdge<CoreStores<S>>> {
        Arc::clone(&self.edge)
    }

    /// The store the application keeps its streams in: what a projector
    /// catches up from.
    pub fn store(&self) -> &S {
        &self.store
    }

    /// The store, shared: a holder (the daemon's destroy, which purges
    /// it) that lets it go before the core is closed (ruling 131).
    pub fn store_handle(&self) -> Arc<S> {
        Arc::clone(&self.store)
    }

    /// How many hold the core's edge: the core itself, and every use case,
    /// listener or task still reaching the store through it (ruling 131's
    /// tests force a holder past the run's end and look).
    #[cfg(test)]
    pub(crate) fn edge_holders(&self) -> usize {
        Arc::strong_count(&self.edge)
    }

    /// The catalog of the registered domains, which a catch-up upcasts by.
    pub fn catalog(&self) -> &Catalog {
        self.app.registry().catalog()
    }

    /// The core, shared: what a one-shot the daemon hosts runs over (U4),
    /// on a thread of its own, beside the daemon's own use of it. Each copy
    /// holds the store, so every one is let go before the core is closed
    /// (ruling 131: the daemon waits for its one-shots to end first).
    pub fn share(&self) -> SharedCore<S> {
        SharedCore {
            app: Arc::clone(&self.app),
            edge: Arc::clone(&self.edge),
            store: Arc::clone(&self.store),
        }
    }
}

/// A copy of an [`InProcessCore`]: its application, edge and store, shared.
pub struct SharedCore<S>
where
    S: Store + Processed + ListStreams + Send + Sync,
{
    app: Arc<App<CoreStores<S>>>,
    edge: Arc<EmbeddedEdge<CoreStores<S>>>,
    store: Arc<S>,
}

impl<S> Clone for SharedCore<S>
where
    S: Store + Processed + ListStreams + Send + Sync,
{
    fn clone(&self) -> SharedCore<S> {
        SharedCore {
            app: Arc::clone(&self.app),
            edge: Arc::clone(&self.edge),
            store: Arc::clone(&self.store),
        }
    }
}

impl<S> SharedCore<S>
where
    S: Store + Processed + ListStreams + Send + Sync + 'static,
{
    pub fn edge(&self) -> Arc<EmbeddedEdge<CoreStores<S>>> {
        Arc::clone(&self.edge)
    }

    pub fn store(&self) -> &S {
        &self.store
    }

    pub fn catalog(&self) -> &Catalog {
        self.app.registry().catalog()
    }

    /// The kernel the application runs, its settings the application's:
    /// what the daemon's scheduler sends its clock facts through.
    pub(crate) fn kernel(&self) -> Kernel<'_> {
        self.app.kernel()
    }
}

#[cfg(test)]
#[path = "core_link.test.rs"]
mod tests;
