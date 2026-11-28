//! The positions of the trees a checkouts projection holds.

use std::sync::Arc;

use run_launcher::fakes::TestEdge;
use run_ports::Edge;

use super::positions;

const TREE: &str = "projects/p/checkouts/c/library";

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

async fn last_cause(edge: &TestEdge) -> String {
    let mut checkouts = library_checkout::caught_up(library_checkout::LibraryCheckouts::default());
    checkouts.step(&edge.store, edge.catalog()).await.unwrap();
    let held = checkouts.projector().checkouts();
    held.tree(&tree_name().root())
        .map(|tree| tree.last_cause.clone())
        .unwrap_or_default()
}

// A loadout's positions: each wanted tree at its latest sequence that
// changed a path; a tree not wanted is left out.
#[tokio::test]
async fn a_tree_s_position_is_its_latest_change() {
    let edge = Arc::new(TestEdge::new());
    observe(&edge, "", "config.yaml", "o0").await;
    observe(&edge, &last_cause(&edge).await, "a.md", "o1").await;
    observe(&edge, &last_cause(&edge).await, "config.yaml", "o2").await;
    let mut checkouts = library_checkout::caught_up(library_checkout::LibraryCheckouts::default());
    checkouts.step(&edge.store, edge.catalog()).await.unwrap();
    let held = checkouts.projector().checkouts();
    assert_eq!(
        positions(&held, |tree| tree == TREE),
        [sessions::TreePosition {
            tree: TREE.to_string(),
            sequence: 2,
        }]
    );
    assert!(positions(&held, |_| false).is_empty());
}
