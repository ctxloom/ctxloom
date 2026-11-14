//! The deps noun's messages, from views made by hand.

use std::collections::BTreeMap;

use deps_application::{ExecDelta, FileChange, ItemChange};

use super::*;

fn spec(command: &str, env: &[(&str, &str)]) -> ExecSpec {
    ExecSpec {
        command: command.to_string(),
        args: vec!["--serve".to_string()],
        env: env
            .iter()
            .map(|(k, v)| (k.to_string(), v.to_string()))
            .collect::<BTreeMap<_, _>>(),
        url: "https://mcp.example.test".to_string(),
        headers: [("Authorization".to_string(), "<0badf00d>".to_string())]
            .into_iter()
            .collect(),
    }
}

fn change(compat: Option<Compat>) -> PinChange {
    PinChange {
        identity: "ctxloom+git://h/r//bundles/kit".to_string(),
        url: "https://h/r".to_string(),
        from_sha: "a".repeat(40),
        to_sha: "b".repeat(40),
        from_version: "v1.0.0".to_string(),
        to_version: "v2.0.0".to_string(),
        compat,
        items: vec![
            ItemChange {
                kind: "mcp",
                name: "srv".to_string(),
                change: Change::Modified,
                exec: Some(ExecDelta {
                    before: Some(spec("one", &[("K", "<11111111>")])),
                    after: Some(spec("two", &[("K", "<22222222>")])),
                }),
            },
            ItemChange {
                kind: "hook",
                name: "guard".to_string(),
                change: Change::Removed,
                exec: Some(ExecDelta {
                    before: Some(spec("guard.sh", &[])),
                    after: None,
                }),
            },
        ],
        files: vec![FileChange {
            path: "scripts/guard.sh".to_string(),
            change: Change::Added,
            diff: "+echo\n".to_string(),
        }],
    }
}

// OD-40, B4: `PinChange`, `old_`/`new_` (AIP-140), the spec of what a hook
// or server runs before and after, its values the view's fingerprints.
#[test]
fn test_a_pin_change_is_its_message_old_and_new() {
    let message = pin_change(&change(Some(Compat::Breaking)));
    assert_eq!(message.reference, "ctxloom+git://h/r//bundles/kit");
    assert_eq!(message.url, "https://h/r");
    assert_eq!(
        (message.old_commit.as_str(), message.new_commit.as_str()),
        ("a".repeat(40).as_str(), "b".repeat(40).as_str())
    );
    assert_eq!(
        (message.old_version.as_str(), message.new_version.as_str()),
        ("v1.0.0", "v2.0.0")
    );
    assert_eq!(
        message.compatibility,
        v1::pin_change::Compatibility::Breaking as i32
    );
    let server = &message.items[0];
    assert_eq!(server.kind, v1::ItemKind::McpServer as i32);
    assert_eq!(server.item, "srv");
    assert_eq!(server.change, v1::ChangeKind::Modified as i32);
    let exec = server.exec.as_ref().expect("what it runs");
    let old = exec.old_spec.as_ref().expect("old");
    let new = exec.new_spec.as_ref().expect("new");
    assert_eq!((old.command.as_str(), new.command.as_str()), ("one", "two"));
    assert_eq!(old.args, ["--serve"]);
    assert_eq!(old.env["K"], "<11111111>");
    assert_eq!(new.env["K"], "<22222222>");
    assert_eq!(new.url, "https://mcp.example.test");
    assert_eq!(new.headers["Authorization"], "<0badf00d>");
    let hook = &message.items[1];
    assert_eq!(hook.kind, v1::ItemKind::Hook as i32);
    assert_eq!(hook.change, v1::ChangeKind::Removed as i32);
    assert!(hook.exec.as_ref().expect("exec").new_spec.is_none());
    let file = &message.files[0];
    assert_eq!(file.path, "scripts/guard.sh");
    assert_eq!(file.change, v1::ChangeKind::Added as i32);
    assert_eq!(file.diff, "+echo\n");
}

#[test]
fn test_each_classification_is_its_value_and_a_first_pin_none() {
    use v1::pin_change::Compatibility;
    let cases = [
        (None, Compatibility::Unspecified),
        (Some(Compat::Breaking), Compatibility::Breaking),
        (Some(Compat::Compatible), Compatibility::Compatible),
        (Some(Compat::Unknown), Compatibility::Unknown),
    ];
    for (compat, want) in cases {
        assert_eq!(pin_change(&change(compat)).compatibility, want as i32);
    }
}

#[test]
fn test_each_pull_status_is_its_state() {
    use v1::pull_result::State;
    for (status, state) in [
        ("installed", State::Installed),
        ("reinstalled", State::Reinstalled),
        ("skipped", State::Kept),
        ("failed", State::Failed),
        ("other", State::Unspecified),
    ] {
        assert_eq!(pull_state(status), state as i32, "{status}");
    }
}

#[test]
fn test_each_selector_and_reason_word_is_its_value() {
    use v1::dependency_check::{Selector, UncheckedReason};
    for (word, want) in [
        ("sha", Selector::Sha),
        ("tag", Selector::Tag),
        ("version", Selector::Version),
        ("branch", Selector::Branch),
        ("other", Selector::Unspecified),
    ] {
        assert_eq!(selector(word), want as i32, "{word}");
    }
    for (word, want) in [
        ("unparseable", UncheckedReason::Unparseable),
        ("unresolvable", UncheckedReason::Unresolvable),
        ("not_refreshed", UncheckedReason::NotRefreshed),
        ("other", UncheckedReason::Unspecified),
    ] {
        assert_eq!(unchecked_reason(word), want as i32, "{word}");
    }
}

// The counts of a whole check: its entries and those with no commit.
#[test]
fn test_a_whole_check_counts_its_entries_and_the_skipped() {
    let report = CheckReport {
        entries: 3,
        updates: Vec::new(),
        unchecked: Vec::new(),
        refresh_failures: Vec::new(),
        skipped_empty: 2,
        missing_default_profiles: Vec::new(),
        missing_defaults_error: String::new(),
    };
    let message = checked("projects/p", &report);
    assert_eq!((message.dependency_count, message.skipped_count), (3, 2));
    assert!(message.checks.is_empty());
}

// An upgrade that applied its moves has nothing left to apply.
#[test]
fn test_an_applied_upgrade_names_no_apply_command() {
    let report = UpgradeReport {
        applied: true,
        changes: vec![change(None)],
        incomplete: false,
        nothing_declared: false,
        removed: vec!["gone".to_string()],
    };
    let message = upgraded(&report, None);
    assert_eq!(message.applied, Some(true));
    assert_eq!(message.apply_command, "");
    assert_eq!(message.changes.len(), 1);
    assert_eq!(message.removed_references, ["gone"]);
    let preview = UpgradeReport {
        applied: false,
        ..report
    };
    assert_eq!(
        upgraded(&preview, Some("origin/kit")).apply_command,
        "ctxloom deps upgrade origin/kit --yes"
    );
}
