//! `clean`'s write: what it takes, and how it answers.

use reclaim_application::{CleanReport, CleanTarget};

use super::{response, shape_of, APPLY_COMMAND};
use library_api::writes::Shape;

fn target(rel: &str, present: bool) -> CleanTarget {
    CleanTarget {
        rel: rel.to_string(),
        path: format!("/work/demo/{rel}"),
        present,
        bytes: if present { 3 } else { 0 },
        rebuild: "ctxloom deps pull".to_string(),
        removed: false,
    }
}

fn report(present: &[bool; 3]) -> CleanReport {
    let rels = [
        ".ctxloom/cache/bundles",
        ".ctxloom/cache/repos",
        ".ctxloom/cache/context",
    ];
    let targets: Vec<CleanTarget> = rels
        .iter()
        .zip(present)
        .map(|(rel, there)| target(rel, *there))
        .collect();
    CleanReport {
        applied: false,
        bytes: targets.iter().map(|t| t.bytes).sum(),
        targets,
    }
}

fn trash(path: &str) -> Shape {
    Shape::Trash {
        path: path.to_string(),
        files: Vec::new(),
    }
}

// Step 10b U9: a clean takes each cache tree present, each relative to its
// tree; one alone is a lone trash, several one sequence, none nothing.
#[test]
fn test_a_clean_takes_each_cache_tree_present() {
    assert_eq!(shape_of(&report(&[false, false, false])), None);
    assert_eq!(
        shape_of(&report(&[false, false, true])),
        Some(trash("cache/context"))
    );
    assert_eq!(
        shape_of(&report(&[true, false, true])),
        Some(Shape::Sequence(vec![
            trash("cache/bundles"),
            trash("cache/context")
        ]))
    );
}

// Its answer says each target as its report always has, those present as
// the spec's targets; an apply took each present, a preview names the
// command that applies it.
#[test]
fn test_a_clean_answers_its_targets_taken_or_to_take() {
    let preview = response(&report(&[true, false, true]), false);
    assert_eq!(
        preview.targets,
        [".ctxloom/cache/bundles", ".ctxloom/cache/context"]
    );
    assert_eq!(preview.apply_command, APPLY_COMMAND);
    assert!(!preview.applied);
    assert!(preview.items.iter().all(|item| !item.removed));
    assert_eq!((preview.items.len(), preview.bytes), (3, 6));
    let applied = response(&report(&[true, false, true]), true);
    assert!(applied.applied && applied.apply_command.is_empty());
    let removed: Vec<bool> = applied.items.iter().map(|item| item.removed).collect();
    assert_eq!(removed, [true, false, true]);
}
