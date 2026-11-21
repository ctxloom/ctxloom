//! `ctxloom test watch-library`'s lines.

use daemon_proto::v1;
use serde_json::json;

use super::*;

// Each change prints its tree, its cause in a word, its sequence and its
// paths (a rename's old path too); a write's request and verb only when it
// was one.
#[test]
fn a_change_prints_its_tree_cause_sequence_and_paths() {
    let external = v1::LibraryChange {
        tree: "projects/p/checkouts/c/library".to_string(),
        cause: v1::library_change::Cause::External as i32,
        changes: vec![
            v1::ChangeSummary {
                path: "content/a.md".to_string(),
                status: "modified".to_string(),
                from_path: String::new(),
            },
            v1::ChangeSummary {
                path: "content/b.md".to_string(),
                status: "renamed".to_string(),
                from_path: "content/old.md".to_string(),
            },
        ],
        sequence: 4,
        ..v1::LibraryChange::default()
    };
    assert_eq!(
        change_line(&external),
        json!({"tree": "projects/p/checkouts/c/library", "cause": "external", "sequence": 4,
               "changes": [{"path": "content/a.md", "status": "modified"},
                           {"path": "content/b.md", "status": "renamed", "from_path": "content/old.md"}]})
    );
    let intended = v1::LibraryChange {
        tree: "users/me/library".to_string(),
        cause: v1::library_change::Cause::Intended as i32,
        request_id: "r-1".to_string(),
        verb: "fragment create".to_string(),
        sequence: 9,
        ..v1::LibraryChange::default()
    };
    assert_eq!(
        change_line(&intended),
        json!({"tree": "users/me/library", "cause": "intended", "sequence": 9,
               "request_id": "r-1", "verb": "fragment create", "changes": []})
    );
    let unsaid = v1::LibraryChange::default();
    assert_eq!(change_line(&unsaid)["cause"], json!(""));
}
