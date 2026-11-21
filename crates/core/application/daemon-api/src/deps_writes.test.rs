//! A pull's sequence and its kill points.

use super::*;

fn tree() -> library_tree::TreeName {
    library_tree::TreeName::home("brisk-copper-moth").expect("a tree")
}

fn installed(sha: &str) -> DepsTree {
    DepsTree {
        path: format!("cache/bundles/example.test/r/demo/{sha}"),
        files: vec![ctxloom_ports::Put::file("bundle.yaml", "version: 1\n")],
        identity: "ctxloom+git://example.test/r//bundles/demo".to_string(),
        sha: sha.to_string(),
        tree: format!("tree-{sha}"),
    }
}

// Step 10c U4 (protos.md §1.2): a pull is a new directory per tree (no
// entries: the cache is presence and identity), then the lock on the
// token it was planned against, then each tree it no longer keeps into
// the trash and the purge of its own slot, then each engine file, foreign,
// listed in the intent; every step under the pull's verb, on its tree.
#[test]
fn a_pull_is_its_trees_then_its_lock_then_what_it_takes_away_then_other_tools_files() {
    let evidence = evidence();
    let sequence = pull_sequence(
        &tree(),
        "deps pull",
        (
            &[installed("a1"), installed("b2")],
            Some(("lock-oid".to_string(), 7)),
            &[("cache/bundles/example.test/r/demo/old".to_string(), 3)],
        ),
        &[("CLAUDE.md".to_string(), "claude-oid".to_string())],
        &evidence,
    );
    assert_eq!(sequence.verb, "deps pull");
    assert_eq!(sequence.name, tree().to_string());
    let kinds: Vec<&str> = sequence
        .steps
        .iter()
        .map(|step| {
            if step.create.is_some() {
                "create"
            } else if step.replace.as_ref().is_some_and(|r| r.foreign) {
                "foreign"
            } else if step.replace.is_some() {
                "replace"
            } else if step.trash.is_some() {
                "trash"
            } else if step.purge.is_some() {
                "purge"
            } else {
                "none"
            }
        })
        .collect();
    assert_eq!(
        kinds,
        ["create", "create", "replace", "trash", "purge", "foreign"]
    );
    let create = sequence.steps[1].create.as_ref().expect("a create");
    assert_eq!(create.path, "cache/bundles/example.test/r/demo/b2");
    assert!(create.entries.is_empty());
    let lock = sequence.steps[2].replace.as_ref().expect("the lock");
    assert_eq!(
        (
            lock.path.as_str(),
            lock.new_oid.as_str(),
            lock.seen_sequence
        ),
        ("lock.yaml", "lock-oid", 7)
    );
    let trash = sequence.steps[3].trash.as_ref().expect("a trash");
    assert_eq!(trash.seen_sequence, 3);
    assert!(!trash.keep, "a sequence's own trash is not kept");
    let foreign = sequence.steps[5].replace.as_ref().expect("a foreign file");
    assert_eq!(foreign.path, "CLAUDE.md");
    assert_eq!(
        sequence.foreign,
        [ForeignFile {
            path: "CLAUDE.md".to_string(),
            new_oid: "claude-oid".to_string(),
        }]
    );
    for step in &sequence.steps {
        let verb = [
            step.create.as_ref().map(|c| c.verb.as_str()),
            step.replace.as_ref().map(|r| r.verb.as_str()),
            step.trash.as_ref().map(|t| t.verb.as_str()),
        ];
        assert!(verb.iter().flatten().all(|verb| *verb == "deps pull"));
    }
}

// A pull that takes nothing away purges nothing; one that writes no lock
// has no lock step.
#[test]
fn a_pull_with_nothing_to_take_away_has_no_purge_and_one_with_no_lock_no_lock_step() {
    let sequence = pull_sequence(
        &tree(),
        "deps upgrade",
        (&[installed("a1")], None, &[]),
        &[],
        &evidence(),
    );
    assert_eq!(sequence.steps.len(), 1);
    assert!(sequence.steps[0].create.is_some());
    assert!(sequence.foreign.is_empty());
}

// The kill points after a pull's trees (atomic_writes.feature's): the
// first tree's is `tree-installed`, the last's `trees-installed` too, so
// a pull of one tree reaches both.
#[test]
fn the_first_tree_and_the_last_are_each_a_kill_point() {
    assert_eq!(tree_points(0, 2), ["tree-installed"]);
    assert_eq!(tree_points(1, 2), ["trees-installed"]);
    assert_eq!(tree_points(0, 1), ["tree-installed", "trees-installed"]);
    assert!(tree_points(1, 3).is_empty());
}
