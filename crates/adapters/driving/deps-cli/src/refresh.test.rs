//! The skipped refresh as words.

use deps_application::{AppError, Refresh, RefreshEngine, RefusedKind};
use library_api::deps_wire::refresh::view;
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

fn said(refresh: &Refresh) -> String {
    let mut out = Vec::new();
    text(&mut out, &view(refresh, &catalog()), "rerun --yes").expect("written");
    String::from_utf8(out).expect("utf-8")
}

#[test]
fn test_writes_and_releases_are_named_and_the_apply_line_limits_materialize_to_them() {
    assert_eq!(
        said(&refresh(&["hooks", "mcp"], &["skills"], &[])),
        "  Project refresh skipped: without --yes nothing was written into /p.\n    \
         claude-code: would write hooks, mcp; would release skills\n    \
         Apply it with 'rerun --yes', or 'ctxloom materialize --yes --surface hooks --surface mcp --surface skills'.\n"
    );
}

#[test]
fn test_a_refused_kind_is_named_with_its_reason_and_fix_and_has_no_apply_line() {
    let said = said(&refresh(&[], &[], &["hooks"]));
    assert_eq!(
        said,
        "  Project refresh skipped: without --yes nothing was written into /p.\n    \
         claude-code: hooks refused: aborting project refresh: not json\n      \
         fix: the pull itself landed and wrote nothing into the project; fix what is named, then run `ctxloom deps pull --yes` again\n"
    );
}
