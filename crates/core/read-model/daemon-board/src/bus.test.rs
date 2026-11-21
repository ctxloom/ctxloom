//! The hosting state on the Bus (step 10e Stage 2a): the board served as an
//! EPHEMERAL projector subscription (its marks in memory, every stream folded
//! from sequence 0 on each serve) answers what its streams say, whether it
//! is fed once, fed again from 0 by a process that starts later, or dropped
//! (a crash) between a restart's request and its turn's end. Tests only.

use std::future::Future;
use std::num::NonZeroUsize;
use std::pin::Pin;
use std::time::Duration;

use angzarr_embedded::angzarr_bus::{Bus as _, Credit, Following, Scope, Subscription};
use angzarr_embedded::angzarr_router::router::DynProjectorHandler;
use angzarr_embedded::testing::MemoryStore;
use angzarr_embedded::v1::EventBook;
use angzarr_embedded::{Bus, Host, Kernel, Registry, Store, StreamRef, Timestamp, Wait, Write};
use runs::{run_root, RUN_DOMAIN};
use sessions::{RestartReason, SESSION_DOMAIN};

use super::tests::{engine_ended, restart_owed, started, turn_completed, turn_queued};
use crate::tests::{book, minted, root, ID};
use crate::{Agent, Boards};

/// The longest a test waits for the bus to drain; never the pace of a pass.
const BOUND: Duration = Duration::from_secs(10);

/// A timer that never fires: the bus is drained once and stops.
struct Never;

impl Wait for Never {
    fn wait(&self, _: Duration) -> Pin<Box<dyn Future<Output = ()> + Send + '_>> {
        Box::pin(std::future::pending())
    }

    fn draw(&self) -> f64 {
        0.0
    }
}

fn clock() -> Timestamp {
    Timestamp {
        seconds: 50,
        nanos: 0,
    }
}

async fn append<S: Store + Sync>(store: &S, b: &EventBook) {
    let cover = b.cover.as_ref().expect("a cover");
    let id = uuid::Uuid::from_slice(&cover.root.as_ref().expect("a root").value).unwrap();
    store
        .write(Write::append(
            StreamRef::main(&cover.domain, id),
            b.pages[0].sequence,
            &b.pages,
        ))
        .await
        .expect("the append lands");
}

/// Serve `boards` from `store` as an ephemeral subscription until the bus is
/// idle once, then stop: what a process that starts now does.
async fn serve(store: &MemoryStore, boards: &Boards) {
    let registry = Registry::new();
    let kernel = Kernel::new(&registry);
    let host = Host::new(store, &kernel, &clock, &Never, BOUND, Following::FullScan);
    let mut bus = Bus::new(host);
    let projector: Box<dyn DynProjectorHandler> = Box::new(boards.clone());
    let credit = Credit::new(NonZeroUsize::new(2).expect("some instances"));
    let scope = Scope::of(SESSION_DOMAIN, &[RUN_DOMAIN]);
    bus.subscribe(Subscription::projector(scope, credit, projector).ephemeral())
        .expect("the subscription is accepted");
    tokio::time::timeout(BOUND, bus.serve(|_| false))
        .await
        .expect("the bus drained")
        .expect("the bus served");
}

/// The session minted and owing a restart; its run started with a turn in
/// flight.
async fn parked(store: &MemoryStore) {
    let mut session = minted(Some("owner"));
    session.push(restart_owed(RestartReason::Operator));
    append(store, &book(SESSION_DOMAIN, root(), 0, session)).await;
    let mut run = started();
    run.push(turn_queued());
    append(store, &book(RUN_DOMAIN, run_root(ID, 1), 0, run)).await;
}

#[tokio::test]
async fn a_restart_owed_is_parked_on_the_bus_until_the_turn_ends() {
    let store = MemoryStore::new();
    parked(&store).await;
    let boards = Boards::default();
    serve(&store, &boards).await;
    assert_eq!(
        boards.board().agent(ID),
        Agent::Parked {
            turn: 2,
            reason: RestartReason::Operator
        }
    );
    assert!(boards.board().restarts_due().is_empty());
    // The turn ends while the subscription is live (a serve of the same
    // process, its fold continuing or restarting from 0 alike).
    append(
        &store,
        &book(RUN_DOMAIN, run_root(ID, 1), 3, vec![turn_completed(2)]),
    )
    .await;
    serve(&store, &boards).await;
    assert_eq!(boards.board().restarts_due().len(), 1);
}

// The daemon restarted mid-park: the subscription is dropped (a crash keeps
// no mark), and a process that starts later folds every stream from 0 to the
// same state, the same restart due, once.
#[tokio::test]
async fn a_fresh_subscription_after_a_crash_finds_the_same_restart_owed() {
    let store = MemoryStore::new();
    parked(&store).await;
    let first = Boards::default();
    serve(&store, &first).await;
    drop(first);
    append(
        &store,
        &book(RUN_DOMAIN, run_root(ID, 1), 3, vec![turn_completed(2)]),
    )
    .await;
    let second = Boards::default();
    serve(&store, &second).await;
    let due = second.board().restarts_due();
    assert_eq!(due.len(), 1, "{due:?}");
    assert_eq!(due[0].reason, RestartReason::Operator);
    // The engine ended: the same fold says nothing is left to end.
    append(
        &store,
        &book(RUN_DOMAIN, run_root(ID, 1), 4, vec![engine_ended()]),
    )
    .await;
    let third = Boards::default();
    serve(&store, &third).await;
    assert!(third.board().restarts_due().is_empty());
    assert_eq!(
        third.board().agent(ID),
        Agent::Restarting {
            reason: RestartReason::Operator
        }
    );
}
