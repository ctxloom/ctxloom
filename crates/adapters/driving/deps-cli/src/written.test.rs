//! A served write's message read back is the view it was made from.

use std::collections::BTreeMap;

use deps_application::{
    Change, Compat, ConstraintChangeView, ExecDelta, ExecSpec, FailedItem, FileChange, ItemChange,
    PinChange, PullReport, ReconcileView, SyncItem, UncheckedRemote, UpgradeReport,
};

use super::*;

fn first_pin() -> PinChange {
    PinChange {
        identity: "ctxloom+file:///r.git//bundles/demo".to_string(),
        url: "file:///r.git".to_string(),
        from_sha: "a".repeat(40),
        to_sha: "b".repeat(40),
        from_version: "v1.0.0".to_string(),
        to_version: "v2.0.0".to_string(),
        compat: Some(Compat::Breaking),
        items: vec![
            ItemChange {
                kind: "mcp",
                name: "tools".to_string(),
                change: Change::Modified,
                exec: Some(ExecDelta {
                    before: Some(ExecSpec {
                        command: "old".to_string(),
                        args: vec!["-x".to_string()],
                        env: BTreeMap::from([("K".to_string(), "sha256:1".to_string())]),
                        url: String::new(),
                        headers: BTreeMap::new(),
                    }),
                    after: None,
                }),
            },
            ItemChange {
                kind: "hook",
                name: "pre".to_string(),
                change: Change::Removed,
                exec: None,
            },
            ItemChange {
                kind: "fragment",
                name: "rules".to_string(),
                change: Change::Added,
                exec: None,
            },
        ],
        files: vec![FileChange {
            path: "scripts/run.sh".to_string(),
            change: Change::Modified,
            diff: "-a\n+b\n".to_string(),
        }],
    }
}

fn sync(reference: &str, status: &'static str) -> SyncItem {
    SyncItem {
        reference: reference.to_string(),
        kind: "bundle",
        status,
        error: String::new(),
    }
}

// Step 10c U4: a pull's answer, made from its report as the CLI always
// printed it in json, reads back as that report line for line (the
// counts and status from its rows; the message, which no text says,
// dropped).
#[test]
fn a_pulls_answer_reads_back_as_its_report() {
    let report = PullReport {
        status: "completed_with_errors",
        total: 4,
        installed: 1,
        reinstalled: 1,
        errors: 1,
        synced: vec![sync("i", "installed"), sync("r", "reinstalled")],
        skipped: vec![sync("k", "skipped")],
        failed: vec![FailedItem {
            reference: "f".to_string(),
            kind: "bundle",
            status: "failed",
            error: "gone".to_string(),
            fix: "pull again".to_string(),
        }],
        removed: vec!["old".to_string()],
        pruned_checkouts: vec!["/c/old".to_string()],
        incomplete: true,
        unreachable: vec!["parent".to_string()],
        constraint_changes: vec![ConstraintChangeView {
            identity: "k".to_string(),
            pinned: "v1".to_string(),
            declared: "v2".to_string(),
            sha: "c".repeat(40),
        }],
        changes: vec![first_pin()],
        message: String::new(),
        reconcile: Some(ReconcileView {
            gone: vec!["g".to_string()],
            unreachable: vec![UncheckedRemote {
                url: "file:///u.git".to_string(),
                reason: "down".to_string(),
                refs: vec!["u".to_string()],
            }],
        }),
    };
    let answered = library_api::deps_wire::dependency::pulled("projects/p", &report, None);
    assert_eq!(pulled(&answered), report);
    let empty = PullReport {
        status: "empty",
        total: 0,
        installed: 0,
        reinstalled: 0,
        errors: 0,
        synced: Vec::new(),
        skipped: Vec::new(),
        failed: Vec::new(),
        removed: Vec::new(),
        pruned_checkouts: Vec::new(),
        incomplete: false,
        unreachable: Vec::new(),
        constraint_changes: Vec::new(),
        changes: Vec::new(),
        message: String::new(),
        reconcile: None,
    };
    let answered = library_api::deps_wire::dependency::pulled("projects/p", &empty, None);
    assert_eq!(pulled(&answered), empty);
    let done = PullReport {
        status: "completed",
        total: 1,
        installed: 0,
        reinstalled: 0,
        skipped: vec![sync("k", "skipped")],
        ..empty
    };
    let answered = library_api::deps_wire::dependency::pulled("projects/p", &done, None);
    assert_eq!(pulled(&answered), done);
}

// An upgrade's answer reads back as its report, applied or previewed.
#[test]
fn an_upgrades_answer_reads_back_as_its_report() {
    for applied in [true, false] {
        let report = UpgradeReport {
            applied,
            changes: vec![first_pin()],
            incomplete: !applied,
            nothing_declared: applied,
            removed: vec!["gone".to_string()],
        };
        let answered = library_api::deps_wire::dependency::upgraded(&report, None);
        assert_eq!(upgraded(&answered), report, "applied {applied}");
    }
}

#[test]
fn a_remotes_id_is_the_last_segment_of_its_name() {
    assert_eq!(remote_id("projects/p/remotes/origin"), "origin");
    assert_eq!(remote_id("origin"), "origin");
}
