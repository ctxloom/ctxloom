//! The owed contexts (step 10c U13 rework): the pure judgement, and the
//! handler under a stored catch-up over an in-memory store.

use std::collections::BTreeMap;
use std::sync::Arc;

use angzarr_embedded::{CatchUp, Payload, Timestamp};
use library_checkout::LibraryCheckouts;
use run_launcher::fakes::{session_cover, session_events, uuid, TestEdge};
use run_ports::Edge;
use session_index::{Handed, SessionIndexes, SessionRecord};
use sessions::{SessionEvent, SessionLoadout};

use super::{caught_up, change_of, owed_at_load, owed_by, Change, OwedContexts, SessionTrees};

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

fn change(tree: &str, sequence: u64, paths: &[&str]) -> Change {
    Change {
        tree: tree.to_string(),
        sequence,
        paths: paths.iter().map(|p| p.to_string()).collect(),
        recorded: Timestamp {
            seconds: 50,
            nanos: 0,
        },
    }
}

/// A live session with its loadout, covering `covered`, loaded at 10.
fn live(id: &str, covered: &[(&str, u64)]) -> SessionRecord {
    SessionRecord {
        id: id.to_string(),
        loadout: Some(loadout()),
        context: Some(Handed {
            loaded_at: Timestamp {
                seconds: 10,
                nanos: 0,
            },
            covered: covered.iter().map(|(t, s)| (t.to_string(), *s)).collect(),
            owed: Vec::new(),
        }),
        ..SessionRecord::default()
    }
}

// A change is owed to each live session on its tree whose loadout it
// touched and whose context does not cover it, with the bundles it touched.
#[test]
fn a_change_is_owed_to_the_live_sessions_it_touches_past_their_context() {
    let on_tree = vec![TREE.to_string()];
    let ended = SessionRecord {
        ended_at: Some(Timestamp::default()),
        ..live("ended", &[])
    };
    let sessions = vec![
        (live("a", &[(TREE, 3)]), on_tree.clone()),
        (live("covered", &[(TREE, 4)]), on_tree.clone()),
        (live("elsewhere", &[]), vec![OTHER.to_string()]),
        (ended, on_tree.clone()),
        (
            SessionRecord {
                loadout: None,
                ..live("unloaded", &[])
            },
            on_tree.clone(),
        ),
        // Covering no position there: owed when recorded after its loadout.
        (live("fresh", &[]), on_tree),
    ];
    assert_eq!(
        owed_by(
            &change(
                TREE,
                4,
                &[
                    DEMO,
                    "config.yaml",
                    "content/bundles/v2/other/fragments/x.md"
                ]
            ),
            &sessions
        ),
        vec![
            (
                "a".to_string(),
                vec!["config".to_string(), "demo".to_string()],
                vec!["config.yaml".to_string(), DEMO.to_string()],
            ),
            (
                "fresh".to_string(),
                vec!["config".to_string(), "demo".to_string()],
                vec!["config.yaml".to_string(), DEMO.to_string()],
            ),
        ]
    );
    // A change none of its loadout names is owed to nobody.
    assert!(owed_by(
        &change(TREE, 5, &["content/bundles/v2/other/fragments/x.md"]),
        &sessions
    )
    .is_empty());
    // Recorded before the loadout of one covering no position: not owed.
    let early = Change {
        recorded: Timestamp {
            seconds: 5,
            nanos: 0,
        },
        ..change(TREE, 4, &[DEMO])
    };
    assert_eq!(
        owed_by(&early, &sessions),
        vec![(
            "a".to_string(),
            vec!["demo".to_string()],
            vec![DEMO.to_string()]
        )]
    );
}

// A loadout is owed, at once, each path its trees changed past the
// positions it covers that it touched: at the latest such sequence.
// The order a hosted child's launch makes (CreateHandOff observes its
// tree first): its config's change is observed apart and covered by its
// loadout, then a change to its bundle ALONE is recorded past it. The
// loadout is its plan's (canonical references), and that change is still
// owed, with the bundle it touched.
#[test]
fn a_bundle_change_alone_is_owed_to_a_plans_loadout_past_a_covered_config_change() {
    let plan = crate::reactions::loadout_of_plan(
        "dev",
        &["dev".to_string()],
        &["ctxloom+local:demo#fragments/example".to_string()],
    );
    let record = SessionRecord {
        loadout: Some(plan),
        ..live("child", &[(TREE, 1)])
    };
    let sessions = vec![(record, vec![TREE.to_string()])];
    assert!(owed_by(&change(TREE, 1, &["config.yaml"]), &sessions).is_empty());
    let owed = owed_by(&change(TREE, 2, &[DEMO]), &sessions);
    assert_eq!(
        owed,
        vec![(
            "child".to_string(),
            vec!["demo".to_string()],
            vec![DEMO.to_string()]
        )]
    );
}

#[test]
fn a_loadout_is_owed_what_changed_past_its_positions() {
    let rows = |tree: &str| match tree {
        TREE => vec![
            (DEMO.to_string(), 6),
            ("content/bundles/v2/other/fragments/x.md".to_string(), 9),
            ("config.yaml".to_string(), 8),
            ("content/bundles/v2/demo/fragments/old.md".to_string(), 2),
        ],
        _ => vec![(DEMO.to_string(), 1)],
    };
    let covered = BTreeMap::from([(TREE.to_string(), 4), (OTHER.to_string(), 1)]);
    assert_eq!(
        owed_at_load(&loadout(), &covered, rows),
        vec![(
            TREE.to_string(),
            8,
            vec!["config".to_string(), "demo".to_string()],
            vec!["config.yaml".to_string(), DEMO.to_string()],
        )],
        "the latest touched path's sequence; an untouched later one counts not"
    );
    let all = BTreeMap::from([(TREE.to_string(), 9)]);
    assert!(owed_at_load(&loadout(), &all, rows).is_empty());
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

/// Every session draws on [`TREE`].
struct OnTree;

impl SessionTrees for OnTree {
    fn trees(&self, _: &SessionRecord) -> Vec<String> {
        vec![TREE.to_string()]
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

/// The folds a daemon's round catches up, and the handler over them.
struct Round {
    edge: Arc<TestEdge>,
    index: CatchUp<SessionIndexes>,
    checkouts: CatchUp<LibraryCheckouts>,
    owed: CatchUp<OwedContexts<TestEdge>>,
}

impl Round {
    fn new(edge: &Arc<TestEdge>) -> Round {
        let index = session_index::SessionIndex::caught_up(SessionIndexes::default());
        let checkouts = library_checkout::caught_up(LibraryCheckouts::default());
        let owed = caught_up(OwedContexts::new(
            Arc::clone(edge),
            index.projector().clone(),
            checkouts.projector().clone(),
            Arc::new(OnTree),
        ));
        Round {
            edge: Arc::clone(edge),
            index,
            checkouts,
            owed,
        }
    }

    /// One round: the folds, then the handler, as `Daemon::tick` runs them.
    async fn run(&mut self) -> usize {
        let (store, catalog) = (&self.edge.store, self.edge.catalog());
        self.index.step(store, catalog).await.unwrap();
        self.checkouts.step(store, catalog).await.unwrap();
        let caught = self.owed.step(store, catalog).await.unwrap();
        assert!(caught.failures().is_empty(), "{:?}", caught.failures());
        self.index.step(store, catalog).await.unwrap();
        caught.handed().len()
    }
}

// The handler records, on each live session a change touches, what it is
// owed: once, though a daemon after it catches up again (its positions are
// the store's, so nothing is handed twice), and a change its context covers
// is owed nothing. The session's index folds what is owed.
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
    let record = round
        .index
        .projector()
        .index()
        .find("amber-swift-owl")
        .unwrap();
    assert_eq!(record.context.unwrap().owed.len(), 1);
    // Its own owed fact handed (it judges nothing of it); then another
    // daemon's catch-up over the same store: its positions are the store's,
    // so nothing is handed again.
    round.run().await;
    let mut again = Round::new(&edge);
    assert_eq!(again.run().await, 0, "nothing handed twice");
    assert_eq!(owed(&edge).await, first);
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

/// The tree's last cause, which the next observation is made against.
async fn tree_cause(edge: &TestEdge) -> String {
    let checkouts = library_checkout::caught_up(LibraryCheckouts::default());
    let mut checkouts = checkouts;
    checkouts.step(&edge.store, edge.catalog()).await.unwrap();
    let held = checkouts.projector().checkouts();
    held.tree(&uuid_of_tree())
        .map(|tree| tree.last_cause.clone())
        .unwrap_or_default()
}

fn uuid_of_tree() -> angzarr_embedded::v1::Uuid {
    tree_name().root()
}

// A change recorded between a session's plan and its loadout (past the
// positions the plan was assembled at) is owed at the loadout, though the
// change's own book was handed before the session had a loadout.
#[tokio::test]
async fn a_change_between_the_plan_and_the_loadout_is_owed_at_the_loadout() {
    let edge = Arc::new(TestEdge::new());
    let mut round = Round::new(&edge);
    observe(&edge, "", "config.yaml", "o0").await;
    round.run().await;
    observe(&edge, &tree_cause(&edge).await, DEMO, "o1").await;
    round.run().await;
    // Planned at the tree's first sequence, recorded after the second.
    minted(&edge, Some(0)).await;
    round.run().await;
    let owed = owed(&edge).await;
    assert_eq!(owed.len(), 1, "{owed:?}");
    assert_eq!(owed[0].0, TREE);
    assert!(owed[0].1 > 0, "{owed:?}");
    assert_eq!(owed[0].2, vec!["demo".to_string()]);
    assert_eq!(owed[0].3, vec![DEMO.to_string()], "the paths it draws on");
    let _ = uuid(SESSION);
}

// A loadout's positions: each wanted tree at its latest sequence that
// changed a path; a tree not wanted is left out.
#[tokio::test]
async fn a_tree_s_position_is_its_latest_change() {
    let edge = Arc::new(TestEdge::new());
    let mut round = Round::new(&edge);
    observe(&edge, "", "config.yaml", "o0").await;
    round.run().await;
    observe(&edge, &tree_cause(&edge).await, DEMO, "o1").await;
    observe(&edge, &tree_cause(&edge).await, "config.yaml", "o2").await;
    round.run().await;
    let checkouts = round.checkouts.projector().checkouts();
    let at = super::positions(&checkouts, |tree| tree == TREE);
    assert_eq!(
        at,
        [sessions::TreePosition {
            tree: TREE.to_string(),
            sequence: 2,
        }]
    );
    assert!(super::positions(&checkouts, |_| false).is_empty());
}
