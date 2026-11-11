//! The core served in process (S5): the kernel's `App` over a store, the
//! exposure map, and the `embedded-edge` adapter over it, so `Facts`,
//! `AgentRequests`, `Feeds` and `Admission` run against the real
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
    Catalog, ListStreams, OnRetryable, Processed, Registry, Sampled, Store, StoreError, Timestamp,
};
use angzarr_embedded_host::config::Expose;
use angzarr_embedded_host::stores::Stores;
use angzarr_embedded_host::App;
use embedded_edge::EmbeddedEdge;
use runs::RunAggregate;
use sessions::{NameClaimAggregate, SessionAggregate};
use std::num::NonZeroU32;
use trees::TreeAggregate;

/// How many times the kernel runs a fact that keeps losing its race before it
/// parks it. In process nothing is far away, so no backoff separates the runs.
/// Parking writes the fact's dead letter, itself a store write: a store that
/// refuses every write parks nothing (the write of the letter fails too), and a
/// test that scripts losses scripts exactly this many.
pub(crate) const RACE_RUNS: NonZeroU32 = match NonZeroU32::new(3) {
    Some(runs) => runs,
    None => NonZeroU32::MIN,
};

/// The instant and the fresh command id each call is handled at: this is the
/// composition root, where the clock and the ids live.
fn sample() -> Sampled {
    let since = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default();
    Sampled {
        now: Timestamp {
            seconds: since.as_secs() as i64,
            nanos: since.subsec_nanos() as i32,
        },
        command_id: WireUuid::from_bytes(*uuid::Uuid::now_v7().as_bytes()),
    }
}

/// The core's one store, as the application resolves it: every call's key
/// is `()`, and it is resumed at start (as `OneStore`), shared with the core
/// that keeps it, so it can be closed once the application is let go.
pub struct CoreStores<S>(Arc<S>);

impl<S: Store + Processed + ListStreams + Send + Sync> Stores for CoreStores<S> {
    type Key = ();
    type Store = Arc<S>;

    async fn store(&self, _key: &()) -> Result<Arc<S>, StoreError> {
        Ok(Arc::clone(&self.0))
    }

    // Equivalent to none here: the resume at start runs the policies' owed
    // work, and the core registers aggregates alone.
    #[cfg_attr(test, mutants::skip)]
    fn at_start(&self) -> Vec<()> {
        vec![()]
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
    /// service, and so of the edge, is released; then the core's own
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
    /// The application over `store`: the sessions, runs and trees aggregates
    /// registered; sessions and runs exposed to the network (the container
    /// bridge reaches them) and trees to loopback; the kernel's retries off,
    /// so a fact at the race cap is parked at once.
    pub async fn start(store: S) -> InProcessCore<S> {
        let mut registry = Registry::new();
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
        let exposed = BTreeMap::from([
            (sessions::SESSION_DOMAIN.to_string(), Expose::Ip),
            (runs::RUN_DOMAIN.to_string(), Expose::Ip),
            (trees::TREE_DOMAIN.to_string(), Expose::Loopback),
        ]);
        let retries = OnRetryable {
            max_retries: 0,
            max_race_runs: RACE_RUNS,
            initial_backoff: Duration::ZERO,
            multiplier: 1.0,
            max_backoff: Duration::ZERO,
            jitter: 0.0,
        };
        let store = Arc::new(store);
        let app = App::builder(CoreStores(Arc::clone(&store)), registry)
            .exposed(exposed)
            .on_retryable(retries)
            .start()
            .await;
        let app = Arc::new(app);
        InProcessCore {
            edge: Arc::new(EmbeddedEdge::new(Arc::clone(&app), sample)),
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
}

#[cfg(test)]
#[path = "core_link.test.rs"]
mod tests;
