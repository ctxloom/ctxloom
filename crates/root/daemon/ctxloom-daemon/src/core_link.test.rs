//! The use cases over the real `App::serve_in`, in process. RED (S5): the
//! wiring is a stub. These are the in-process Facts / Feeds
//! checks of the task (scope 5) on the real exposure seam; the finer admission
//! rules are `runs-application`'s, over a kernel double.

use std::sync::Arc;

use angzarr_embedded::testing::{Fault, FaultyStore, MemoryStore};
use angzarr_embedded::v1::{Cover, Uuid};
use angzarr_embedded::{CommandBookExt, Failure, KernelError, Malformed};
use embedded_edge::EmbeddedEdge;
use run_ports::{Arrival, Edge, Unserved};
use runs_application::{EnqueueRequest, Enqueuer, Facts, Identity, Recorded, RunnerFact};

use crate::{run_id, InProcessCore, Projectors};

const SESSION: &str = "5f0c7a52-1111-5111-8111-000000000001";

type Core = InProcessCore<FaultyStore<MemoryStore>>;
type TheEdge = EmbeddedEdge<crate::CoreStores<FaultyStore<MemoryStore>>>;

struct World {
    core: Core,
    store: FaultyStore<MemoryStore>,
    /// The run, as the use cases address it.
    identity: Identity,
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
            workspace: None,
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
        let enqueued = Enqueuer::new(Arc::clone(&edge))
            .enqueue(EnqueueRequest {
                session: SESSION.to_string(),
                incarnation: 1,
                mode: runs::RunMode::Interactive,
                evidence: runs_application::Evidence::passing("claude-code"),
                launcher_lock: String::new(),
                grant: None,
            })
            .await
            .unwrap();
        let identity = Identity {
            run: enqueued.run,
            session: Uuid::from_bytes(*uuid::Uuid::parse_str(SESSION).unwrap().as_bytes()),
            incarnation: 1,
            harp: "amber-swift-owl".to_string(),
            depth: 0,
        };
        World {
            core,
            store,
            identity,
        }
    }

    fn edge(&self) -> Arc<TheEdge> {
        self.core.edge()
    }
}

use angzarr_embedded::v1::CommandBook;

fn exited() -> RunnerFact {
    RunnerFact::RunExited(runs::RecordRunExited { code: 0, signal: 0 })
}

// NEW-DESIGN (plan 6.2 "one code path": run_id in S4 and the run domain's
// root agree): the run domain's root is the id `ctxloom_compose::run_id` derives.
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
    let hidden = edge.serve(book("trust"), Arrival::Ip).await.unwrap_err();
    let missing = edge.serve(book("nowhere"), Arrival::Ip).await.unwrap_err();
    assert!(matches!(hidden, Unserved::Hidden { .. }));
    let unknown = |domain: &str| {
        Failure::Kernel(KernelError::Malformed(Malformed::UnknownDomain {
            domain: domain.to_string(),
        }))
    };
    assert_eq!(hidden.answer(), unknown("trust"));
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

// The edge's own behaviours over the real App and a ctxloom domain (trust,
// exposed to loopback only). `embedded-edge` tests the same wrappers with a
// toy aggregate, since a driven adapter links no domain; here, the real ones.
mod the_edge_over_trust {
    use super::*;

    fn tree() -> Cover {
        Cover {
            domain: trust::TRUST_DOMAIN.to_string(),
            root: Some(Uuid::from_bytes([9; 16])),
            edition: None,
        }
    }

    fn found() -> CommandBook {
        CommandBook::command_of(
            tree(),
            &trust::TrustCommand::from(trust::GrantTrust {
                name: "p".to_string(),
                project_id: "id".to_string(),
                etag: "e".to_string(),
                current_etag: "e".to_string(),
                actor: trust::Actor {
                    user: "u".to_string(),
                },
            }),
        )
    }

    fn revoke() -> CommandBook {
        CommandBook::command_of(
            tree(),
            &trust::TrustCommand::from(trust::RevokeTrust {
                name: "p".to_string(),
                project_id: "id".to_string(),
                actor: trust::Actor {
                    user: "u".to_string(),
                },
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
        let book = edge.handle(found()).await.unwrap().events.expect("events");
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
                domain: trust::TRUST_DOMAIN.to_string()
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
        edge.handle(revoke()).await.unwrap();
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
// a runner, and then (the launcher's RecordRunnerStarted recorded through the same App)
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
            RunnerFact::RunnerStarted(runs::RecordRunnerStarted {
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
#[allow(
    clippy::disallowed_methods,
    reason = "a test kernel or store of its own"
)]
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
            workspace: None,
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

// Step 8 U1: the daemon's core holds a project's trust, on loopback, and a
// SIMPLE request naming `trust-index` is answered the project's trust.
#[tokio::test]
async fn a_trust_granted_through_the_edge_is_answered_by_the_trust_index() {
    use prost::Message as _;
    let mut kept = Projectors::default();
    let core = InProcessCore::start_with(MemoryStore::default(), |store| {
        kept = Projectors::filled(store);
        kept.clone()
    })
    .await;
    let name = "projects/calm-quiet-heron/trust";
    let grant = trust::GrantTrust {
        name: name.to_string(),
        project_id: "calm-otter".to_string(),
        etag: "e".to_string(),
        current_etag: "e".to_string(),
        actor: trust::Actor {
            user: "alice".to_string(),
        },
    };
    let cover = Cover {
        domain: trust::TRUST_DOMAIN.to_string(),
        root: Some(trust::trust_root(name)),
        edition: None,
    };
    let book = sessions::keyed_book(cover, &grant, "grant");
    let synced = core
        .edge()
        .serve_sync(
            book.clone(),
            vec!["trust-index".to_string()],
            Arrival::Loopback,
        )
        .await
        .unwrap();
    let trust = daemon_proto::v1::Trust::decode(
        synced.projections[0]
            .projection
            .as_ref()
            .expect("the index's view")
            .value
            .as_slice(),
    )
    .unwrap();
    assert_eq!(trust.name, name);
    assert!(trust.trusted);
    assert_eq!(kept.trusts.unwrap().index().trusted_now().len(), 1);
    // The network never reaches a trust.
    let hidden = core
        .edge()
        .serve_sync(book, vec!["trust-index".to_string()], Arrival::Ip)
        .await
        .unwrap_err();
    assert!(matches!(hidden, Unserved::Hidden { .. }), "{hidden:?}");
}

// Step 10a U0: the daemon's core holds a library tree, on loopback: an
// observation recorded through the edge is answered by `library-checkout`
// as the tree's state, each path at its event's sequence; the same
// observation again is a duplicate; the network never reaches a tree.
#[tokio::test]
async fn an_observation_through_the_edge_is_answered_by_the_library_checkout() {
    use prost::Message as _;
    let mut kept = Projectors::default();
    let core = InProcessCore::start_with(MemoryStore::default(), |store| {
        kept = Projectors::filled(store);
        kept.clone()
    })
    .await;
    let project = sessions::ProjectName::parse("projects/calm-quiet-heron").unwrap();
    let tree = library_tree::TreeName::checkout(project, "brisk-copper-moth").unwrap();
    let path = "content/bundles/v2/demo/bundle.yaml";
    let fact = library_tree::RecordTreeObserved {
        name: tree.to_string(),
        kind: library_tree::TreeKind::Checkout,
        base_cause: String::new(),
        head_before: String::new(),
        head_after: "h-0".to_string(),
        tree_after: "t-0".to_string(),
        changes: vec![library_tree::Change {
            path: path.to_string(),
            status: library_tree::ChangeStatus::Added,
            from_oid: String::new(),
            to_oid: "y1".to_string(),
            from_path: String::new(),
        }],
        dirty_after: vec![library_tree::DirtyPath {
            path: path.to_string(),
            oid: "y1".to_string(),
        }],
        baseline: true,
    };
    let book = library_tree::observation_book(&tree, &fact);
    let synced = core
        .edge()
        .serve_sync(
            book.clone(),
            vec!["library-checkout".to_string()],
            Arrival::Loopback,
        )
        .await
        .unwrap();
    let state = library_domain_proto::v1::LibraryState::decode(
        synced.projections[0]
            .projection
            .as_ref()
            .expect("the checkout's view")
            .value
            .as_slice(),
    )
    .unwrap();
    assert_eq!(state.name, tree.to_string());
    assert_eq!(state.head, "h-0");
    assert_eq!(
        state
            .resources
            .get(path)
            .map(|r| (r.sequence, r.oid.as_str())),
        Some((0, "y1"))
    );
    assert_eq!(
        kept.checkouts
            .as_ref()
            .unwrap()
            .checkouts()
            .token(&tree.root(), path),
        Some(0)
    );
    let cover = book.cover.clone().unwrap();
    let again = core
        .edge()
        .serve_sync(
            book.clone(),
            vec!["library-checkout".to_string()],
            Arrival::Loopback,
        )
        .await;
    assert!(again.is_ok(), "{again:?}");
    assert_eq!(core.edge().events(&cover, 0).await.unwrap().len(), 1);
    let hidden = core
        .edge()
        .serve_sync(book, vec!["library-checkout".to_string()], Arrival::Ip)
        .await
        .unwrap_err();
    assert!(matches!(hidden, Unserved::Hidden { .. }), "{hidden:?}");
}

/// An ask of the session `c` under the root `o`.
fn an_ask() -> approvals::RecordApprovalAsked {
    let reference = |session: &str| approvals::SessionRef {
        project_id: "calm-otter".to_string(),
        session: session.to_string(),
    };
    approvals::RecordApprovalAsked {
        ask_id: "ask-tidy-wren".to_string(),
        child: reference("c"),
        run: "r".to_string(),
        engine: "claude-code".to_string(),
        root: reference("o"),
        tool_name: "Bash".to_string(),
        input_excerpt: "{}".to_string(),
        input_bytes: 2,
        input_sha256: "x".to_string(),
        timeout_seconds: 300,
        engine_request_id: "toolu_1".to_string(),
        options: approvals::claude_options("Bash", r#"{"command":"ls"}"#),
        name: "projects/calm-quiet-heron/approvals/ask-tidy-wren".to_string(),
        child_name: "projects/calm-quiet-heron/sessions/c".to_string(),
        root_name: "projects/calm-quiet-heron/sessions/o".to_string(),
        agent: "default".to_string(),
    }
}

// Step 8 U4: the daemon's core holds the asks and their names; the ask is
// recorded in process, projected into the pending approvals, and its answer
// is a loopback SIMPLE request answered the ask as the pending approvals
// have it; the network reaches no ask, and no one outside reaches a name.
#[tokio::test]
async fn an_ask_recorded_in_process_is_answered_over_loopback_and_projected() {
    use prost::Message as _;
    let mut kept = Projectors::default();
    let core = InProcessCore::start_with(MemoryStore::default(), |store| {
        kept = Projectors::filled(store);
        kept.clone()
    })
    .await;
    let asked = an_ask();
    let cover = Cover {
        domain: approvals::APPROVALS_DOMAIN.to_string(),
        root: Some(angzarr_embedded::v1::Uuid::from_bytes([5; 16])),
        edition: None,
    };
    core.edge()
        .handle(approvals::fact_book(
            cover.clone(),
            &asked,
            "approval:r:toolu_1",
        ))
        .await
        .unwrap();
    let pending = kept.approvals.clone().unwrap();
    assert_eq!(
        pending
            .index()
            .listed("projects/calm-quiet-heron", None, true)
            .len(),
        1
    );
    let answer = approvals::AnswerApproval {
        ask_id: "ask-tidy-wren".to_string(),
        option: "allow-once".to_string(),
        message: String::new(),
    };
    let book = sessions::keyed_book(cover, &answer, "answer:1");
    let synced = core
        .edge()
        .serve_sync(
            book.clone(),
            vec!["pending-approvals".to_string()],
            Arrival::Loopback,
        )
        .await
        .unwrap();
    let answered = daemon_proto::v1::Approval::decode(
        synced.projections[0]
            .projection
            .as_ref()
            .expect("the pending approvals' view")
            .value
            .as_slice(),
    )
    .unwrap();
    assert_eq!(answered.state, "answered");
    assert_eq!(answered.option, "allow-once");
    let hidden = core
        .edge()
        .serve_sync(book, vec!["pending-approvals".to_string()], Arrival::Ip)
        .await
        .unwrap_err();
    assert!(matches!(hidden, Unserved::Hidden { .. }), "{hidden:?}");
    let claim = approvals::ClaimApprovalName {
        project_id: "calm-otter".to_string(),
        ask_id: "ask-pale-moth".to_string(),
        approval: "a".to_string(),
    };
    let names = Cover {
        domain: approvals::APPROVAL_NAMES_DOMAIN.to_string(),
        root: Some(angzarr_embedded::v1::Uuid::from_bytes([6; 16])),
        edition: None,
    };
    let unexposed = core
        .edge()
        .serve(
            sessions::keyed_book(names, &claim, "claim"),
            Arrival::Loopback,
        )
        .await
        .unwrap_err();
    assert!(
        matches!(unexposed, Unserved::Hidden { .. }),
        "{unexposed:?}"
    );
}

// Owner ruling 2026-11-16: only the daemon's core resumes owed policy work
// at start, and only it registers the alarm consumers' policies; a CLI
// command's core (`InProcessCore::start`) scans no policy source.
mod resume_at_start {
    use std::sync::{Arc, Mutex};

    use angzarr_embedded::angzarr_ports::Uuid as CommandId;
    use angzarr_embedded::angzarr_ports::{
        ListStreams, PositionKey, PositionStore, Processed, Slice, SnapshotStore, StoreError,
        StreamRef, Write, Written,
    };
    use angzarr_embedded::testing::MemoryStore;
    use angzarr_embedded::v1::{Cover, EventPage, Snapshot, Uuid};
    use angzarr_embedded::EventStore;
    use run_ports::Edge as _;

    use super::an_ask;
    use crate::{InProcessCore, Projectors};

    /// What a start asked of the store that only a resume asks: every
    /// stream listed, each domain's streams, each handler's record.
    #[derive(Default, Debug)]
    struct Asked {
        listed: usize,
        covered: Vec<String>,
        processed: Vec<String>,
    }

    /// A memory store that counts the scans a resume makes; clones share
    /// the store and the count.
    #[derive(Clone, Default)]
    struct Counting {
        inner: MemoryStore,
        asked: Arc<Mutex<Asked>>,
    }

    impl Counting {
        fn asked(&self) -> std::sync::MutexGuard<'_, Asked> {
            self.asked.lock().unwrap()
        }

        fn forget(&self) {
            *self.asked() = Asked::default();
        }
    }

    impl EventStore for Counting {
        async fn write(&self, write: Write<'_>) -> Result<Written, StoreError> {
            self.inner.write(write).await
        }

        async fn read(
            &self,
            stream: StreamRef<'_>,
            from: u64,
            to: Option<u64>,
        ) -> Result<Slice, StoreError> {
            self.inner.read(stream, from, to).await
        }

        async fn find_command(
            &self,
            stream: StreamRef<'_>,
            command_id: CommandId,
        ) -> Result<Vec<EventPage>, StoreError> {
            self.inner.find_command(stream, command_id).await
        }

        async fn covers(&self, domain: &str) -> Result<Vec<Cover>, StoreError> {
            self.asked().covered.push(domain.to_string());
            self.inner.covers(domain).await
        }
    }

    impl SnapshotStore for Counting {
        async fn read_snapshot(
            &self,
            stream: StreamRef<'_>,
        ) -> Result<Option<Snapshot>, StoreError> {
            self.inner.read_snapshot(stream).await
        }

        async fn write_snapshot(
            &self,
            stream: StreamRef<'_>,
            snapshot: &Snapshot,
        ) -> Result<(), StoreError> {
            self.inner.write_snapshot(stream, snapshot).await
        }
    }

    impl PositionStore for Counting {
        async fn position(&self, key: PositionKey<'_>) -> Result<u64, StoreError> {
            self.inner.position(key).await
        }
    }

    impl ListStreams for Counting {
        async fn list_streams(&self) -> Result<Vec<Cover>, StoreError> {
            self.asked().listed += 1;
            self.inner.list_streams().await
        }
    }

    impl Processed for Counting {
        async fn processed(&self, handler: &str) -> Result<Vec<(Cover, u64)>, StoreError> {
            self.asked().processed.push(handler.to_string());
            self.inner.processed(handler).await
        }
    }

    fn ask_cover() -> Cover {
        Cover {
            domain: approvals::APPROVALS_DOMAIN.to_string(),
            root: Some(Uuid::from_bytes([5; 16])),
            edition: None,
        }
    }

    /// A store holding one ask whose arming is owed: recorded by a core
    /// with no alarm consumer, so no policy has handled it yet.
    async fn an_owed_arming() -> Counting {
        let store = Counting::default();
        let core =
            InProcessCore::start_consuming(store.clone(), |_| Projectors::default(), &[]).await;
        core.edge()
            .handle(approvals::fact_book(
                ask_cover(),
                &an_ask(),
                "approval:r:toolu_1",
            ))
            .await
            .unwrap();
        drop(core);
        assert!(
            store
                .covers(scheduler::SCHEDULER_DOMAIN)
                .await
                .unwrap()
                .is_empty(),
            "nothing armed yet"
        );
        store.forget();
        store
    }

    // A CLI command's core starts without a resume: it lists no stream,
    // scans no domain and reads no handler's record.
    #[tokio::test]
    async fn a_command_cores_start_scans_no_policy_source() {
        let store = an_owed_arming().await;
        let core = InProcessCore::start(store.clone()).await;
        let asked = store.asked();
        assert_eq!(asked.listed, 0, "{asked:?}");
        assert!(asked.covered.is_empty(), "{asked:?}");
        assert!(asked.processed.is_empty(), "{asked:?}");
        drop(asked);
        drop(core);
    }

    // So the owed arming stays owed through a command core's start, and the
    // daemon's next start pays it: the ask's alarm is armed.
    #[tokio::test]
    async fn owed_policy_work_waits_for_the_daemons_start() {
        let store = an_owed_arming().await;
        drop(InProcessCore::start(store.clone()).await);
        assert!(
            store
                .covers(scheduler::SCHEDULER_DOMAIN)
                .await
                .unwrap()
                .is_empty(),
            "a command core's start armed nothing"
        );
        let daemon = InProcessCore::start_with(store.clone(), |_| Projectors::default()).await;
        assert_eq!(
            store
                .covers(scheduler::SCHEDULER_DOMAIN)
                .await
                .unwrap()
                .len(),
            1,
            "the daemon's start armed the ask's alarm"
        );
        drop(daemon);
    }

    // The daemon's core resumes at start: every policy source is scanned by
    // its covers, the approvals' (the arming) and the scheduler's (the
    // delivery); the resume lists no stream of the whole store.
    #[tokio::test]
    async fn the_daemons_core_start_scans_every_policy_source() {
        let store = an_owed_arming().await;
        let core = InProcessCore::start_with(store.clone(), |_| Projectors::default()).await;
        let asked = store.asked();
        assert_eq!(asked.listed, 0, "the resume asks each source's covers");
        assert!(
            asked
                .covered
                .contains(&approvals::APPROVALS_DOMAIN.to_string()),
            "{asked:?}"
        );
        assert!(
            asked
                .covered
                .contains(&scheduler::SCHEDULER_DOMAIN.to_string()),
            "{asked:?}"
        );
        drop(asked);
        drop(core);
    }

    // A command core registers no alarm consumer either: an ask committed
    // through it (none is, in production: every ask is the daemon's) arms
    // nothing in process.
    #[tokio::test]
    async fn a_command_core_registers_no_alarm_consumer() {
        let store = Counting::default();
        let core = InProcessCore::start(store.clone()).await;
        core.edge()
            .handle(approvals::fact_book(
                ask_cover(),
                &an_ask(),
                "approval:r:toolu_1",
            ))
            .await
            .unwrap();
        assert!(store
            .covers(scheduler::SCHEDULER_DOMAIN)
            .await
            .unwrap()
            .is_empty());
        drop(core);
    }
}
