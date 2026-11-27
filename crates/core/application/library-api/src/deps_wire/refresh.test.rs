//! The skipped refresh as words and as its API message.

use deps_application::{AppError, Refresh, RefreshEngine, RefusedKind};
use presentation::Catalog;

use super::*;

fn catalog() -> Catalog {
    Catalog::en_us(&[deps_application::EN_US]).expect("a catalog")
}

fn refresh(wrote: &[&str], released: &[&str], refused: &[&str]) -> Refresh {
    Refresh {
        target: "/p".to_string(),
        engines: vec![RefreshEngine {
            engine: "claude-code".to_string(),
            wrote: wrote.iter().map(ToString::to_string).collect(),
            released: released.iter().map(ToString::to_string).collect(),
            refused: refused
                .iter()
                .map(|kind| RefusedKind {
                    kind: kind.to_string(),
                    cause: Box::new(AppError::RefreshAborted {
                        detail: "not json".to_string(),
                    }),
                })
                .collect(),
        }],
    }
}

// OD-40, B4: `ProjectRefresh`, each engine by name, a refused kind with
// its reason and fix in words.
#[test]
fn test_the_message_carries_the_reason_and_fix_of_a_refused_kind() {
    let skipped = view(&refresh(&["hooks"], &[], &["settings"]), &catalog());
    let message = message("projects/p", &skipped);
    let engine = &message.engines[0];
    assert_eq!(engine.engine, "projects/p/engines/claude-code");
    assert_eq!(engine.written_kinds, ["hooks"]);
    assert!(engine.released_kinds.is_empty());
    assert_eq!(engine.refused_kinds[0].kind, "settings");
    assert_eq!(
        engine.refused_kinds[0].reason,
        "aborting project refresh: not json"
    );
    assert!(engine.refused_kinds[0].fix.contains("deps pull --yes"));
    assert_eq!(message.target, "/p");
}

#[test]
fn test_it_applies_only_when_it_would_write_or_release() {
    let applies_to = |wrote: &[&str], released: &[&str], refused: &[&str]| {
        applies(&view(&refresh(wrote, released, refused), &catalog()))
    };
    assert!(applies_to(&["hooks"], &[], &[]));
    assert!(applies_to(&[], &["skills"], &[]));
    assert!(!applies_to(&[], &[], &["hooks"]));
}
