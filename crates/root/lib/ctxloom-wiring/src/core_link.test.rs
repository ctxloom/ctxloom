//! The use cases over the real `App::serve_in`, in process. RED (S5): the
//! wiring is a stub. These are the in-process Facts / AgentRequests / Feeds
//! checks of the task (scope 5) on the real exposure seam; the finer admission
//! rules are `runs-application`'s, over a kernel double.

use std::sync::atomic::{AtomicU32, Ordering};
use std::sync::Arc;

use angzarr_embedded::testing::{Fault, FaultyStore, MemoryStore};
use angzarr_embedded::v1::{Cover, Uuid};
use angzarr_embedded::{CommandBookExt, Failure, KernelError, Malformed};
use embedded_edge::EmbeddedEdge;
use run_ports::{Arrival, Edge, Token, TokenSource, Unserved};
use runs_application::{
    Admission, AppError, Credential, EnqueueRequest, Enqueuer, Facts, Gate, Identity, Recorded,
    RunnerFact, RunsGate, Why,
};

use crate::{run_id, InProcessCore, Projectors};

const SESSION: &str = "5f0c7a52-1111-5111-8111-000000000001";

#[derive(Default)]
struct Counting(AtomicU32);

impl TokenSource for Counting {
    fn mint(&self) -> Token {
        let n = self.0.fetch_add(1, Ordering::SeqCst) + 1;
        Token::from_bytes([n as u8; 32])
    }
}

type Core = InProcessCore<FaultyStore<MemoryStore>>;
type TheEdge = EmbeddedEdge<crate::CoreStores<FaultyStore<MemoryStore>>>;

struct World {
    core: Core,
    store: FaultyStore<MemoryStore>,
    /// The runner principal's credential and identity.
    credential: Credential,
    identity: Identity,
    /// The model principal's (owner ruling 2026-11-04, item 16).
    model_credential: Credential,
}

impl World {
    /// One session, minted, with its first run enqueued, through the real App.
    async fn new() -> World {
        let store = FaultyStore::<MemoryStore>::default();
        let core = InProcessCore::start(store.clone()).await;
        let edge = core.edge();
        let mint = sessions::MintSession {
            harp: "amber-swift-owl".to_string(),
            project_id: "calm-otter".to_string(),
            project_dir: "/w".to_string(),
            output_dir: "/o".to_string(),
            engine: "claude-code".to_string(),
            origin: sessions::Origin::Session,
            started_at: None,
            depth: 0,
            owner: None,
        };
        let cover = Cover {
            domain: sessions::SESSION_DOMAIN.to_string(),
            root: Some(Uuid::from_bytes(
                *uuid::Uuid::parse_str(SESSION).unwrap().as_bytes(),
            )),
            edition: None,
        };
        edge.handle(CommandBook::command_of(
            cover,
            &sessions::SessionCommand::from(mint),
        ))
        .await
        .unwrap();
        let enqueued = Enqueuer::new(Arc::clone(&edge), Arc::new(Counting::default()))
            .enqueue(EnqueueRequest {
                session: SESSION.to_string(),
                incarnation: 1,
                mode: runs::RunMode::Interactive,
                evidence: runs_application::Evidence::passing("claude-code"),
                launcher_lock: String::new(),
            })
            .await
            .unwrap();
        let credential = Credential {
            token: enqueued.runner_token,
            run: enqueued.run.clone(),
        };
        let model_credential = Credential {
            token: enqueued.model_token,
            run: enqueued.run,
        };
        let identity = RunsGate::new(Arc::clone(&edge))
            .identify(&credential)
            .await
            .unwrap();
        World {
            core,
            store,
            credential,
            identity,
            model_credential,
        }
    }

    fn edge(&self) -> Arc<TheEdge> {
        self.core.edge()
    }
}

use angzarr_embedded::v1::CommandBook;

fn exited() -> RunnerFact {
    RunnerFact::RunExited(runs::RunExited { code: 0, signal: 0 })
}

// NEW-DESIGN (plan 6.2 "one code path": run_id in S4 and the run domain's
// root agree): the run domain's root is the id `ctxloom_wiring::run_id` derives.
#[test]
fn the_run_domains_root_is_the_id_the_library_derives() {
    let derived = run_id(SESSION, 3).unwrap();
    assert_eq!(
        runs::run_root(SESSION, 3).value,
        derived.as_bytes().to_vec()
    );
}

// PORTED-FROM-GO: S5 plan red test "a fact on the caller's own run is APPLIED,
// and the same fact again is DUPLICATE" -- over the real `App::serve_in`.
#[tokio::test]
async fn a_fact_is_applied_and_again_duplicate_over_app_serve_in() {
    let world = World::new().await;
    let facts = Facts::new(world.edge());
    assert_eq!(
        facts.record(&world.identity, exited()).await,
        Ok(Recorded::Applied)
    );
    assert_eq!(
        facts.record(&world.identity, exited()).await,
        Ok(Recorded::Duplicate)
    );
}

// NEW-DESIGN (exposure seam, `Unserved::Hidden`): a domain not exposed to the
// arrival is answered exactly as a domain that is not registered is.
#[tokio::test]
async fn an_unknown_domain_is_hidden_and_answered_like_a_missing_one() {
    let world = World::new().await;
    let edge = world.edge();
    let book = |domain: &str| {
        let cover = Cover {
            domain: domain.to_string(),
            root: Some(world.identity.run.clone()),
            edition: None,
        };
        CommandBook::command_of(
            cover,
            &sessions::SessionCommand::from(sessions::SetKept { kept: true }),
        )
    };
    let hidden = edge.serve(book("trees"), Arrival::Ip).await.unwrap_err();
    let missing = edge.serve(book("nowhere"), Arrival::Ip).await.unwrap_err();
    assert!(matches!(hidden, Unserved::Hidden { .. }));
    let unknown = |domain: &str| {
        Failure::Kernel(KernelError::Malformed(Malformed::UnknownDomain {
            domain: domain.to_string(),
        }))
    };
    assert_eq!(hidden.answer(), unknown("trees"));
    assert_eq!(missing.answer(), unknown("nowhere"));
}

// PORTED-FROM-GO: S5 plan red test "a fact at the race cap is PARKED, and the
// client stops retrying it" -- over the real App with a store that loses every
// race; nothing is persisted.
#[tokio::test]
async fn a_fact_at_the_race_cap_is_parked_over_app_serve_in() {
    let world = World::new().await;
    let facts = Facts::new(world.edge());
    let before = world
        .edge()
        .events(&world.identity.run_cover(), 0)
        .await
        .unwrap()
        .len();
    world.store.fail(std::iter::repeat_n(
        Fault::Conflict,
        crate::core_link::RACE_RUNS.get() as usize,
    ));
    assert_eq!(
        facts.record(&world.identity, exited()).await,
        Ok(Recorded::Parked)
    );
    assert_eq!(
        world
            .edge()
            .events(&world.identity.run_cover(), 0)
            .await
            .unwrap()
            .len(),
        before
    );
}

// NEW-DESIGN (D29; item 16): the run's end, recorded by the fact, revokes BOTH
// principals' tokens over the real App: the next request with either is refused.
#[tokio::test]
async fn the_token_is_refused_once_the_runs_end_is_recorded() {
    let world = World::new().await;
    let facts = Facts::new(world.edge());
    facts.record(&world.identity, exited()).await.unwrap();
    let admission = Admission::new(world.edge());
    let book = exited().book(&world.identity);
    for credential in [&world.credential, &world.model_credential] {
        let refused = admission
            .admit(Arrival::Ip, credential, book.clone())
            .await
            .unwrap_err();
        assert_eq!(refused, AppError::Unauthenticated(Why::Revoked));
    }
}

// NEW-DESIGN, owner ruling 2026-11-04 (item 16, fail closed): over the real
// App, the model's token cannot record the runner's exit, and the run lives on.
#[tokio::test]
async fn the_models_token_cannot_record_an_exit_over_app_serve_in() {
    let world = World::new().await;
    let admission = Admission::new(world.edge());
    let before = world
        .edge()
        .events(&world.identity.run_cover(), 0)
        .await
        .unwrap()
        .len();
    let denied = admission
        .admit(
            Arrival::Ip,
            &world.model_credential,
            exited().book(&world.identity),
        )
        .await
        .unwrap_err();
    assert!(matches!(denied, AppError::Denied(Unserved::Hidden { .. })));
    assert_eq!(
        world
            .edge()
            .events(&world.identity.run_cover(), 0)
            .await
            .unwrap()
            .len(),
        before
    );
}

// The edge's own behaviours over the real App and a ctxloom domain (trees,
// exposed to loopback only). `embedded-edge` tests the same wrappers with a
// toy aggregate, since a driven adapter links no domain; here, the real ones.
mod the_edge_over_trees {
    use super::*;

    fn tree() -> Cover {
        Cover {
            domain: trees::TREE_DOMAIN.to_string(),
            root: Some(Uuid::from_bytes([9; 16])),
            edition: None,
        }
    }

    fn found() -> CommandBook {
        CommandBook::command_of(
            tree(),
            &trees::TreeCommand::from(trees::FoundTree {
                owner_session: "s".to_string(),
                ephemeral: false,
                concurrency: 1,
                depth: 1,
            }),
        )
    }

    fn occupy(session: &str) -> CommandBook {
        CommandBook::command_of(
            tree(),
            &trees::TreeCommand::from(trees::OccupyTree {
                session: session.to_string(),
            }),
        )
    }

    async fn edge() -> Arc<TheEdge> {
        InProcessCore::start(FaultyStore::<MemoryStore>::default())
            .await
            .edge()
    }

    // handle answers the addressed stream's events and the delivery outcome;
    // a command that records nothing is answered for its stream with no events.
    #[tokio::test]
    async fn handle_answers_the_addressed_stream() {
        let edge = edge().await;
        let answered = edge.handle(found()).await.unwrap();
        let book = answered.events.expect("events");
        assert_eq!(book.cover, Some(tree()));
        assert_eq!(book.pages.len(), 1);
        assert_eq!(
            answered.delivery,
            angzarr_embedded::v1::DeliveryOutcome::Applied
        );
        let book = edge
            .handle(occupy("s"))
            .await
            .unwrap()
            .events
            .expect("events");
        assert_eq!(book.cover, Some(tree()));
        assert!(book.pages.is_empty());
    }

    // serve admits the arrival a domain is exposed to (loopback) and hides
    // another, naming the domain.
    #[tokio::test]
    async fn serve_admits_the_exposed_arrival_only() {
        let edge = edge().await;
        assert_eq!(
            edge.serve(found(), Arrival::Ip).await.unwrap_err(),
            Unserved::Hidden {
                domain: trees::TREE_DOMAIN.to_string()
            }
        );
        assert!(edge.events(&tree(), 0).await.unwrap().is_empty());
        let answered = edge.serve(found(), Arrival::Loopback).await.unwrap();
        assert_eq!(answered.events.expect("events").pages.len(), 1);
        assert_eq!(edge.events(&tree(), 0).await.unwrap().len(), 1);
    }

    // events reads from a sequence, oldest first, none for an absent stream.
    #[tokio::test]
    async fn events_read_from_a_sequence() {
        let edge = edge().await;
        assert!(edge.events(&tree(), 0).await.unwrap().is_empty());
        edge.handle(found()).await.unwrap();
        edge.handle(occupy("next")).await.unwrap();
        let sequences = |pages: Vec<angzarr_embedded::v1::EventPage>| {
            pages.into_iter().map(|p| p.sequence).collect::<Vec<_>>()
        };
        assert_eq!(sequences(edge.events(&tree(), 0).await.unwrap()), [0, 1]);
        assert_eq!(sequences(edge.events(&tree(), 1).await.unwrap()), [1]);
        assert!(edge.events(&tree(), 2).await.unwrap().is_empty());
    }
}

// Step 1b-ii: the launcher and the recorder catch up from the core's own
// store, through its own catalog: a run enqueued through the real App is owed
// a runner, and then (the launcher's RunnerStarted recorded through the same App)
// is not.
#[tokio::test]
async fn the_pending_launches_catch_up_over_the_cores_store_and_catalog() {
    let world = World::new().await;
    let mut pending = pending_launches::catch_up();
    let caught = pending
        .step(world.core.store(), world.core.catalog())
        .await
        .unwrap();
    assert!(caught.failures().is_empty());
    let owed = pending.projector().pending().launches();
    assert_eq!(owed.len(), 1);
    assert_eq!(owed[0].session, SESSION);
    assert_eq!(owed[0].incarnation, 1);
    Facts::new(world.edge())
        .record(
            &world.identity,
            RunnerFact::RunnerStarted(runs::RunnerStarted {
                runtime: "claude-code".to_string(),
                workspace: "/work".to_string(),
                pid: 0,
            }),
        )
        .await
        .unwrap();
    pending
        .step(world.core.store(), world.core.catalog())
        .await
        .unwrap();
    assert!(pending.projector().pending().launches().is_empty());
}

// The core's catalog is its registry's: the domains it registered, which the
// sessions are among (a default catalog holds none).
#[tokio::test]
async fn the_core_serves_the_catalog_of_its_registered_domains() {
    let world = World::new().await;
    assert!(world
        .core
        .catalog()
        .domains()
        .any(|d| d == sessions::SESSION_DOMAIN));
}

// ------------------------------------------------ the store closed (rulings 131, 140)
//
// A run's store is closed once the core is consumed and nothing else holds
// it, so the run exits with its log checkpointed into the file. Looked at the
// moment the close returns, with no wait. The holder a run's end must outlast
// (a hook call still in the listener past its grace) is forced in
// `oneshot.test.rs`, with the runtime left running.

type SqliteCore = InProcessCore<angzarr_embedded::angzarr_store_sqlite::SqliteStore>;

/// A project's real store under a temporary home, its core started on a
/// runtime of the run's kind (multi-thread), and the runtime.
fn sqlite_core() -> (
    Arc<file_system::Rooted>,
    SqliteCore,
    tokio::runtime::Runtime,
) {
    let runtime = tokio::runtime::Builder::new_multi_thread()
        .worker_threads(1)
        .enable_all()
        .build()
        .unwrap();
    let fs = Arc::new(file_system::Rooted::temp().unwrap());
    let core = runtime.block_on(async {
        let store = ctxloom_store_sqlite::Stores::new(fs.clone(), "/home/u")
            .open()
            .await
            .unwrap();
        InProcessCore::start(store).await
    });
    (fs, core, runtime)
}

fn beside(fs: &file_system::Rooted) -> Vec<&'static str> {
    use file_system::Fs;
    let dir = std::path::Path::new("/home/u/.ctxloom");
    ["ctxloom.db-wal", "ctxloom.db-shm"]
        .into_iter()
        .filter(|name| fs.exists(&dir.join(name)).unwrap())
        .collect()
}

#[test]
fn test_the_core_s_store_is_closed_once_the_core_is_consumed() {
    let (fs, core, runtime) = sqlite_core();
    // An edge a use case took, let go with the use case.
    drop(core.edge());
    runtime.block_on(core.close()).unwrap();
    assert_eq!(beside(&fs), Vec::<&str>::new(), "closed: nothing beside");
}

/// A task still running with an edge at the close (its runtime is not shut
/// down for the close) is a holder: the close refuses it, said, and never
/// drops the store quietly.
#[test]
fn test_a_task_still_holding_an_edge_is_a_close_error_not_a_drop() {
    let (_fs, core, runtime) = sqlite_core();
    let edge = core.edge();
    runtime.spawn(async move {
        let _held = edge;
        std::future::pending::<()>().await;
    });
    assert_eq!(core.edge_holders(), 2, "the task holds the edge");
    let unclosed = runtime.block_on(core.close());
    assert!(
        matches!(unclosed, Err(crate::Unclosed::StillHeld { holders: 2 })),
        "{unclosed:?}"
    );
}

/// An edge leaked past the run is the same error.
#[test]
fn test_an_edge_leaked_past_the_run_is_a_close_error_not_a_drop() {
    let (_fs, core, runtime) = sqlite_core();
    let leaked = core.edge();
    let unclosed = runtime.block_on(core.close());
    assert!(
        matches!(unclosed, Err(crate::Unclosed::StillHeld { holders: 2 })),
        "{unclosed:?}"
    );
    assert_eq!(
        crate::Unclosed::StillHeld { holders: 2 }.to_string(),
        "the run's store is still held (2 holders past the run's end) and was not closed"
    );
    drop(leaked);
}

// ------------------------------------------------ the read models registered (S32, T16)

fn mint_book(harp: &str) -> CommandBook {
    CommandBook::command_of(
        Cover {
            domain: sessions::SESSION_DOMAIN.to_string(),
            root: Some(Uuid::from_bytes(
                *uuid::Uuid::parse_str(SESSION).unwrap().as_bytes(),
            )),
            edition: None,
        },
        &sessions::SessionCommand::from(sessions::MintSession {
            harp: harp.to_string(),
            project_id: "calm-otter".to_string(),
            project_dir: "/w".to_string(),
            output_dir: "/o".to_string(),
            engine: "claude-code".to_string(),
            origin: sessions::Origin::Session,
            started_at: None,
            depth: 0,
            owner: None,
        }),
    )
}

// S32: the daemon's core registers the read models, so a SIMPLE request
// through its edge waits for the ones it names and answers each one's view;
// the same instances, caught up after, fold nothing twice.
#[tokio::test]
async fn a_simple_request_answers_the_named_read_models_views() {
    use prost::Message as _;
    let mut kept = Projectors::default();
    let core = InProcessCore::start_with(MemoryStore::default(), |store| {
        kept = Projectors::filled(store);
        kept.clone()
    })
    .await;
    let synced = core
        .edge()
        .serve_sync(
            mint_book("amber-swift-owl"),
            vec!["session-index".to_string(), "daemon-board".to_string()],
            Arrival::Loopback,
        )
        .await
        .unwrap();
    let names: Vec<&str> = synced
        .projections
        .iter()
        .map(|p| p.projector.as_str())
        .collect();
    assert_eq!(names, ["session-index", "daemon-board"]);
    let session = daemon_proto::v1::Session::decode(
        synced.projections[0]
            .projection
            .as_ref()
            .expect("the index's view")
            .value
            .as_slice(),
    )
    .unwrap();
    assert_eq!(session.uid, SESSION);
    assert_eq!(session.engine, "claude-code");
    let index = kept.index.clone().unwrap();
    let mut caught = session_index::SessionIndex::caught_up(index.clone());
    caught.step(core.store(), core.catalog()).await.unwrap();
    assert_eq!(index.index().records().len(), 1, "folded once");
    let board = kept.board.unwrap();
    assert!(
        board.board().session(SESSION).is_some(),
        "the board folded it"
    );
}

// A core a command runs registers no read model: a SIMPLE request naming
// one is refused before anything runs, and records nothing.
#[tokio::test]
async fn a_core_with_no_read_model_refuses_a_simple_request_and_records_nothing() {
    let core = InProcessCore::start(MemoryStore::default()).await;
    let refused = core
        .edge()
        .serve_sync(
            mint_book("amber-swift-owl"),
            vec!["session-index".to_string()],
            Arrival::Loopback,
        )
        .await
        .unwrap_err();
    assert!(
        matches!(
            refused,
            Unserved::Failed(Failure::Kernel(
                KernelError::SyncProjectorNotSubscribed { .. }
            ))
        ),
        "{refused:?}"
    );
    let cover = mint_book("x").cover.unwrap();
    assert!(core.edge().events(&cover, 0).await.unwrap().is_empty());
}

// The exposure holds for a SIMPLE request as for any: a domain not exposed
// to where it arrived from is hidden.
#[tokio::test]
async fn a_simple_request_to_a_domain_not_exposed_is_hidden() {
    let core = InProcessCore::start_with(MemoryStore::default(), Projectors::filled).await;
    let mut book = mint_book("amber-swift-owl");
    book.cover.as_mut().unwrap().domain = sessions::PROJECTS_DOMAIN.to_string();
    let refused = core
        .edge()
        .serve_sync(book, vec!["session-index".to_string()], Arrival::Loopback)
        .await
        .unwrap_err();
    assert!(matches!(refused, Unserved::Hidden { .. }), "{refused:?}");
}
