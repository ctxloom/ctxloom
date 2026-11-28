//! The reader's document: each event with its sequence, each change with
//! its oid, as library_changes.feature reads them.

use angzarr_embedded::v1::{event_page, EventPage};
use angzarr_embedded::Payload;
use library_tree::{
    Change, ChangeFailed, ChangeIntended, ChangeStatus, External, Intent, IntentSuperseded,
    LibraryEvent, StepApplied, TreeName, TreeObserved,
};
use serde_json::json;

use super::*;

const FILE: &str = "content/bundles/v2/demo/fragments/example.md";

fn page(sequence: u64, event: LibraryEvent) -> EventPage {
    EventPage {
        sequence,
        body: Some(event_page::Body::Event(event.to_any())),
        meta: None,
    }
}

fn change(path: &str, status: ChangeStatus, to: &str, from_path: &str) -> Change {
    Change {
        path: path.to_string(),
        status,
        from_oid: "old".to_string(),
        to_oid: to.to_string(),
        from_path: from_path.to_string(),
    }
}

fn observed(changes: Vec<Change>) -> LibraryEvent {
    LibraryEvent::TreeObserved(TreeObserved {
        name: "users/me/library".to_string(),
        external: Some(External {
            head_before: "h-0".to_string(),
            head_after: "h-1".to_string(),
        }),
        intended: None,
        changes,
        dirty_after: Vec::new(),
        tree_after: "t".to_string(),
        baseline: false,
        contents: Vec::new(),
    })
}

#[test]
fn an_observation_prints_its_sequence_commits_and_changes() {
    let tree = TreeName::home("brisk-copper-moth").unwrap();
    let renamed = "content/bundles/v2/demo/fragments/renamed.md";
    let doc = document(
        Some(&tree),
        &[page(
            3,
            observed(vec![
                change(FILE, ChangeStatus::Deleted, "", ""),
                change(renamed, ChangeStatus::Renamed, "f1", FILE),
                change("config.yaml", ChangeStatus::TypeChanged, "c1", ""),
                change("lock.yaml", ChangeStatus::Added, "l1", ""),
                change("remotes.yaml", ChangeStatus::Modified, "r1", ""),
            ]),
        )],
    );
    assert_eq!(
        doc,
        json!({
            "tree": "users/me/library",
            "events": [{
                "type": "TreeObserved",
                "sequence": 3,
                "baseline": false,
                "head_before": "h-0",
                "head_after": "h-1",
                "changes": [
                    {"path": FILE, "status": "deleted"},
                    {"path": renamed, "status": "renamed", "from_path": FILE, "oid": "f1"},
                    {"path": "config.yaml", "status": "type-changed", "oid": "c1"},
                    {"path": "lock.yaml", "status": "added", "oid": "l1"},
                    {"path": "remotes.yaml", "status": "modified", "oid": "r1"},
                ],
            }],
        })
    );
}

#[test]
fn an_observation_of_no_outside_cause_prints_empty_commits() {
    let LibraryEvent::TreeObserved(mut ours) = observed(Vec::new()) else {
        unreachable!()
    };
    ours.external = None;
    ours.intended = Some("r-1".to_string());
    ours.baseline = true;
    let row = event_row(&page(0, LibraryEvent::TreeObserved(ours))).unwrap();
    assert!(row.get("in_motion").is_none());
    assert_eq!(row["head_before"], json!(""));
    assert_eq!(row["head_after"], json!(""));
    assert_eq!(row["baseline"], json!(true));
    assert_eq!(row["intended"], json!("r-1"));
    assert_eq!(row["changes"], json!([]));
}

#[test]
fn an_intent_and_its_steps_print_their_request() {
    let intended = LibraryEvent::ChangeIntended(ChangeIntended {
        intent: Intent {
            foreign: Vec::new(),
            request_id: "r-1".to_string(),
            verb: "fragment edit".to_string(),
            steps: vec![library_tree::Step {
                replace: None,
                create: None,
                trash: None,
                r#move: Some(library_tree::MovePath {
                    name: String::new(),
                    verb: String::new(),
                    from: "content/bundles/v2/a".to_string(),
                    to: "content/bundles/v2/b".to_string(),
                    to_tree: String::new(),
                    seen_sequence: 0,
                    evidence: library_tree::Evidence {
                        tree_reads: true,
                        plan_rejection: None,
                        builtin: false,
                    },
                }),
                purge: None,
            }],
            base_cause: String::new(),
            done: 0,
        },
    });
    let applied = LibraryEvent::StepApplied(StepApplied {
        request_id: "r-1".to_string(),
        step: 2,
        observed: vec![library_tree::DirtyPath {
            path: "content/bundles/v2/b/bundle.yaml".to_string(),
            oid: "o".to_string(),
        }],
    });
    let failed = LibraryEvent::ChangeFailed(ChangeFailed {
        request_id: "r-2".to_string(),
        step: 1,
        ext_err_txt: "denied".to_string(),
    });
    let superseded = LibraryEvent::IntentSuperseded(IntentSuperseded {
        request_id: "r-3".to_string(),
        by_cause: "obs".to_string(),
    });
    let doc = document(
        None,
        &[
            page(1, intended),
            page(2, applied),
            page(3, failed),
            page(4, superseded),
        ],
    );
    assert_eq!(
        doc,
        json!({
            "tree": null,
            "events": [
                {"type": "ChangeIntended", "sequence": 1, "request_id": "r-1", "verb": "fragment edit",
                 "steps": [{"kind": "move", "path": "content/bundles/v2/a",
                            "from": "content/bundles/v2/a", "to": "content/bundles/v2/b"}]},
                {"type": "StepApplied", "sequence": 2, "request_id": "r-1", "step": 2,
                 "observed": [{"path": "content/bundles/v2/b/bundle.yaml", "oid": "o"}]},
                {"type": "ChangeFailed", "sequence": 3, "request_id": "r-2", "step": 1},
                {"type": "IntentSuperseded", "sequence": 4, "request_id": "r-3"},
            ],
        })
    );
}

#[test]
fn a_page_with_no_event_is_not_printed_and_another_domains_is_its_type_alone() {
    let empty = EventPage {
        sequence: 0,
        body: None,
        meta: None,
    };
    assert!(event_row(&empty).is_none());
    let other = EventPage {
        sequence: 5,
        body: Some(event_page::Body::Event(angzarr_embedded::Any {
            type_url: "type.googleapis.com/ctxloom.trust.v1.TrustGranted".to_string(),
            value: Vec::new(),
        })),
        meta: None,
    };
    assert_eq!(
        event_row(&other),
        Some(json!({"type": "TrustGranted", "sequence": 5}))
    );
}

// protos.md §1.4: a purge's step prints its kind and the slot it removes,
// the trash path of its request (G12).
#[test]
fn a_purge_s_step_prints_the_slot_it_removes() {
    let step = library_tree::Step {
        replace: None,
        create: None,
        trash: None,
        r#move: None,
        purge: Some(library_tree::PurgeSlot {
            request_id: "r-0".to_string(),
        }),
    };
    assert_eq!(
        super::step_row(&step),
        json!({"kind": "purge", "path": "state/trash/r-0"})
    );
}

// Step 10c U10 and U4: a foreign writer's intent lists the other tools'
// files it writes, by path (an intent naming none prints no list), and an
// installed tree prints its request, step, identity, commit and tree.
#[test]
fn a_foreign_writers_files_and_an_installed_tree_print_what_they_name() {
    let intended = LibraryEvent::ChangeIntended(ChangeIntended {
        intent: Intent {
            request_id: "r-1".to_string(),
            verb: "materialize".to_string(),
            steps: Vec::new(),
            base_cause: String::new(),
            done: 0,
            foreign: vec![
                library_tree::ForeignFile {
                    path: "CLAUDE.md".to_string(),
                    new_oid: "c1".to_string(),
                },
                library_tree::ForeignFile {
                    path: ".mcp.json".to_string(),
                    new_oid: String::new(),
                },
            ],
        },
    });
    let installed = LibraryEvent::TreeInstalled(library_tree::TreeInstalled {
        request_id: "r-2".to_string(),
        step: 1,
        identity: "ctxloom+git://h/r//bundles/demo".to_string(),
        sha: "0123".to_string(),
        tree: "t1".to_string(),
    });
    let doc = document(None, &[page(1, intended), page(2, installed)]);
    assert_eq!(
        doc["events"],
        json!([
            {"type": "ChangeIntended", "sequence": 1, "request_id": "r-1", "verb": "materialize",
             "steps": [], "foreign": [{"path": "CLAUDE.md"}, {"path": ".mcp.json"}]},
            {"type": "TreeInstalled", "sequence": 2, "request_id": "r-2", "step": 1,
             "identity": "ctxloom+git://h/r//bundles/demo", "sha": "0123", "tree": "t1"},
        ])
    );
}
