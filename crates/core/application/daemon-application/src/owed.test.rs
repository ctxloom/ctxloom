//! The owed contexts (step 10c U13 rework; owner ruling 2026-11-27): the
//! runner tells each live session a library change on a tree it draws on
//! (`RecordLibraryChanged`), and the SESSION decides, on its own loadout and
//! handed positions, whether it owes it. Driven over the real session
//! aggregate on an in-memory store.

use std::sync::Arc;

use angzarr_embedded::{CatchUp, Payload, Timestamp};
use run_launcher::fakes::{session_cover, session_events, TestEdge};
use run_ports::Edge;
use sessions::{SessionEvent, SessionLoadout};

use super::{caught_up, change_of, OwedContexts, SessionTrees};

const TREE: &str = "projects/p/checkouts/c/library";
const OTHER: &str = "projects/p/checkouts/d/library";
const DEMO: &str = "content/bundles/v2/demo/fragments/example.md";
const SESSION: &str = "6e6f7765-6400-4000-8000-000000000001";

fn loadout() -> SessionLoadout {
    SessionLoadout {
        agent: "dev".to_string(),
        profiles: vec!["dev".to_string()],
        bundles: vec!["demo".to_string()],
    }
}

// A library event's change: an observation's paths (a rename's both), a
// step's; another event changes nothing.
#[test]
fn an_event_s_change_names_its_paths() {
    let observed = library_tree::LibraryEvent::TreeObserved(library_tree::TreeObserved {
        name: TREE.to_string(),
        external: None,
        intended: None,
        changes: vec![library_tree::Change {
            path: "b.md".to_string(),
            status: library_tree::ChangeStatus::Renamed,
            from_oid: String::new(),
            to_oid: String::new(),
            from_path: "a.md".to_string(),
        }],
        dirty_after: Vec::new(),
        tree_after: String::new(),
        baseline: false,
        contents: Vec::new(),
    });
    let at = Timestamp {
        seconds: 3,
        nanos: 0,
    };
    assert_eq!(
        change_of(TREE, &observed, 7, at).map(|c| (c.sequence, c.paths)),
        Some((7, vec!["b.md".to_string(), "a.md".to_string()]))
    );
    let applied = library_tree::LibraryEvent::StepApplied(library_tree::StepApplied {
        request_id: "r".to_string(),
        step: 0,
        observed: vec![library_tree::DirtyPath {
            path: DEMO.to_string(),
            oid: String::new(),
        }],
    });
    assert_eq!(
        change_of(TREE, &applied, 2, at).map(|c| c.paths),
        Some(vec![DEMO.to_string()])
    );
    let failed = library_tree::LibraryEvent::ChangeFailed(library_tree::ChangeFailed {
        request_id: "r".to_string(),
        step: 0,
        ext_err_txt: String::new(),
    });
    assert_eq!(change_of(TREE, &failed, 2, at), None);
}

/// Every session draws on the trees it is built with.
struct OnTrees(Vec<&'static str>);

impl SessionTrees for OnTrees {
    fn trees(&self, _: &sessions::SessionState, _: Option<&str>) -> Vec<String> {
        self.0.iter().map(|tree| tree.to_string()).collect()
    }
}

/// The tree's name.
fn tree_name() -> library_tree::TreeName {
    library_tree::TreeName::parse_checkout(TREE).expect("a checkout's tree")
}

/// An outside change of `path` recorded on [`TREE`], against `base`.
async fn observe(edge: &TestEdge, base: &str, path: &str, oid: &str) {
    let fact = library_tree::RecordTreeObserved {
        name: TREE.to_string(),
        kind: library_tree::TreeKind::Checkout,
        base_cause: base.to_string(),
        head_before: "h".to_string(),
        head_after: "h".to_string(),
        tree_after: "t".to_string(),
        changes: vec![library_tree::Change {
            path: path.to_string(),
            status: library_tree::ChangeStatus::Modified,
            from_oid: String::new(),
            to_oid: oid.to_string(),
            from_path: String::new(),
        }],
        dirty_after: vec![library_tree::DirtyPath {
            path: path.to_string(),
            oid: oid.to_string(),
        }],
        baseline: base.is_empty(),
    };
    edge.handle(library_tree::observation_book(&tree_name(), &fact))
        .await
        .unwrap();
}

/// The session minted, its loadout recorded covering `covered` on [`TREE`].
async fn minted(edge: &TestEdge, covered: Option<u64>) {
    let mint = sessions::SessionCommand::from(sessions::MintSession {
        harp: "amber-swift-owl".to_string(),
        project_id: "calm-otter".to_string(),
        project_dir: "/work".to_string(),
        output_dir: "/work/out".to_string(),
        engine: "claude-code".to_string(),
        origin: sessions::Origin::Agent,
        started_at: None,
        depth: 0,
        owner: None,
        workspace: None,
    });
    let book = angzarr_embedded::v1::CommandBook {
        cover: Some(session_cover(SESSION)),
        page: Some(angzarr_embedded::v1::CommandPage {
            body: Some(angzarr_embedded::v1::command_page::Body::Command(
                angzarr_embedded::v1::Command {
                    payload: Some(mint.to_any()),
                    handling: None,
                },
            )),
            ..angzarr_embedded::v1::CommandPage::default()
        }),
    };
    edge.handle(book).await.unwrap();
    let fact = sessions::RecordLoadout {
        loadout: loadout(),
        incarnation: 1,
        covered: covered
            .map(|sequence| sessions::TreePosition {
                tree: TREE.to_string(),
                sequence,
            })
            .into_iter()
            .collect(),
    };
    edge.handle(sessions::fact_book(
        session_cover(SESSION),
        &fact,
        &sessions::loadout_key(SESSION, 1),
    ))
    .await
    .unwrap();
}

/// The owed changes the session's stream records: tree, sequence, bundles.
async fn owed(edge: &TestEdge) -> Vec<(String, u64, Vec<String>, Vec<String>)> {
    session_events(edge, SESSION)
        .await
        .into_iter()
        .filter_map(|event| match event {
            SessionEvent::ContextOwed(owed) => {
                Some((owed.tree, owed.sequence, owed.bundles, owed.paths))
            }
            _ => None,
        })
        .collect()
}

/// The handler over a catch-up, as a daemon's round runs it.
struct Round {
    edge: Arc<TestEdge>,
    owed: CatchUp<OwedContexts<TestEdge>>,
}

impl Round {
    fn on(edge: &Arc<TestEdge>, trees: &[&'static str]) -> Round {
        Round {
            edge: Arc::clone(edge),
            owed: caught_up(OwedContexts::new(
                Arc::clone(edge),
                Arc::new(OnTrees(trees.to_vec())),
            )),
        }
    }

    fn new(edge: &Arc<TestEdge>) -> Round {
        Round::on(edge, &[TREE])
    }

    /// One round: the handler over what the stores hold, as
    /// `Daemon::tick` runs it.
    async fn run(&mut self) -> usize {
        let caught = self
            .owed
            .step(&self.edge.store, self.edge.catalog())
            .await
            .unwrap();
        assert!(caught.failures().is_empty(), "{:?}", caught.failures());
        caught.handed().len()
    }
}

/// The tree's last cause, which the next observation is made against.
async fn tree_cause(edge: &TestEdge) -> String {
    let mut checkouts = library_checkout::caught_up(library_checkout::LibraryCheckouts::default());
    checkouts.step(&edge.store, edge.catalog()).await.unwrap();
    let held = checkouts.projector().checkouts();
    held.tree(&tree_name().root())
        .map(|tree| tree.last_cause.clone())
        .unwrap_or_default()
}

// The handler tells each live session a change touches; the SESSION records
// what it is owed, once, though a daemon after it replays everything (every
// fact is keyed, so a repeat is a DUPLICATE).
#[tokio::test]
async fn a_change_is_recorded_owed_once_on_the_session_it_touches() {
    let edge = Arc::new(TestEdge::new());
    minted(&edge, None).await;
    let mut round = Round::new(&edge);
    round.run().await;
    observe(&edge, "", DEMO, "o1").await;
    round.run().await;
    let first = owed(&edge).await;
    assert_eq!(first.len(), 1, "{first:?}");
    assert_eq!(
        (first[0].0.as_str(), &first[0].2),
        (TREE, &vec!["demo".to_string()])
    );
    assert_eq!(first[0].3, vec![DEMO.to_string()]);
    // The same round again, then another daemon's replay of the whole store:
    // nothing is recorded twice.
    round.run().await;
    let mut again = Round::new(&edge);
    again.run().await;
    assert_eq!(owed(&edge).await, first, "nothing recorded twice");
    // A change of a path no loadout names: nothing owed.
    observe(
        &edge,
        &tree_cause(&edge).await,
        "content/bundles/v2/other/a.md",
        "o2",
    )
    .await;
    again.run().await;
    assert_eq!(owed(&edge).await, first);
}

// A session on another tree, one that is ended and one with no loadout are
// told nothing it records.
#[tokio::test]
async fn a_change_on_a_tree_a_session_does_not_draw_on_owes_it_nothing() {
    let edge = Arc::new(TestEdge::new());
    minted(&edge, None).await;
    let mut round = Round::on(&edge, &[OTHER]);
    round.run().await;
    observe(&edge, "", DEMO, "o1").await;
    round.run().await;
    assert!(owed(&edge).await.is_empty());
}

// A change the session's context covers is owed nothing: the session
// decides it on the positions its own events hold.
#[tokio::test]
async fn a_change_the_context_covers_is_not_owed() {
    let edge = Arc::new(TestEdge::new());
    observe(&edge, "", DEMO, "o1").await;
    // Planned past the tree's first sequence: it covers it.
    minted(&edge, Some(0)).await;
    let mut round = Round::new(&edge);
    round.run().await;
    assert!(owed(&edge).await.is_empty(), "{:?}", owed(&edge).await);
}

// A change recorded between a session's plan and its loadout (past the
// positions the plan was assembled at) is owed at the loadout, whichever of
// the two the handler meets first.
#[tokio::test]
async fn a_change_between_the_plan_and_the_loadout_is_owed_at_the_loadout() {
    for library_first in [true, false] {
        let edge = Arc::new(TestEdge::new());
        let mut round = Round::new(&edge);
        observe(&edge, "", "config.yaml", "o0").await;
        round.run().await;
        observe(&edge, &tree_cause(&edge).await, DEMO, "o1").await;
        round.run().await;
        // Planned at the tree's first sequence, recorded after the second.
        minted(&edge, Some(0)).await;
        if !library_first {
            // A daemon that starts after both: it replays the store whole.
            round = Round::new(&edge);
        }
        round.run().await;
        let owed = owed(&edge).await;
        assert_eq!(owed.len(), 1, "{library_first}: {owed:?}");
        assert_eq!(owed[0].0, TREE);
        assert!(owed[0].1 > 0, "{owed:?}");
        assert_eq!(owed[0].2, vec!["demo".to_string()]);
        assert_eq!(owed[0].3, vec![DEMO.to_string()], "the paths it draws on");
    }
}
