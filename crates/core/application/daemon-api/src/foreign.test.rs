//! A foreign writer's steps: the paths they name and the points they stop
//! at; the flow itself is atomic_writes.feature's.

use daemon_proto::v1;

use super::{after_step, step_path, ForeignAnswer};

#[test]
fn a_foreign_file_below_the_project_is_named_from_its_root_and_any_other_as_it_is() {
    assert_eq!(step_path("/work/demo", "/work/demo/CLAUDE.md"), "CLAUDE.md");
    assert_eq!(
        step_path("/work/demo/", "/work/demo/out/.claude/settings.json"),
        "out/.claude/settings.json"
    );
    assert_eq!(
        step_path("C:\\work\\demo", "C:\\work\\demo\\out\\CLAUDE.md"),
        "out/CLAUDE.md"
    );
    // A sibling whose name the root's is a prefix of is not below it.
    assert_eq!(
        step_path("/work/demo", "/work/demo-two/CLAUDE.md"),
        "/work/demo-two/CLAUDE.md"
    );
    // The home's record, and any file with no project, as it is.
    assert_eq!(
        step_path("/work/demo", "/home/a/.ctxloom/records/x.claims.yaml"),
        "/home/a/.ctxloom/records/x.claims.yaml"
    );
    assert_eq!(
        step_path("", "/work/demo/CLAUDE.md"),
        "/work/demo/CLAUDE.md"
    );
    assert_eq!(step_path("/work/demo", "/work/demo"), "/work/demo");
}

#[test]
fn the_point_after_a_foreign_step_is_named_by_its_place_from_one() {
    assert_eq!(after_step(1), "step-1");
    assert_eq!(after_step(2), "step-2");
}

#[test]
fn an_answer_says_the_reads_notices_before_its_own() {
    let notice = |code: &str| v1::Notice {
        code: code.to_string(),
        ..v1::Notice::default()
    };
    let fine = ForeignAnswer::Uninstalled(v1::UninstallManageResponse {
        notices: vec![notice("own")],
        ..v1::UninstallManageResponse::default()
    });
    let ForeignAnswer::Uninstalled(said) = fine.saying(vec![notice("read")]) else {
        panic!("not an uninstall")
    };
    let codes: Vec<&str> = said.notices.iter().map(|n| n.code.as_str()).collect();
    assert_eq!(codes, ["read", "own"]);
    for answer in [
        ForeignAnswer::Materialized(v1::MaterializeResponse::default()),
        ForeignAnswer::Fixed(v1::RunDoctorResponse::default()),
    ] {
        let notices = match answer.saying(vec![notice("read")]) {
            ForeignAnswer::Materialized(a) => a.notices,
            ForeignAnswer::Uninstalled(a) => a.notices,
            ForeignAnswer::Fixed(a) => a.notices,
        };
        assert_eq!(notices.len(), 1);
    }
}

#[test]
fn a_removal_resumed_from_its_intent_alone_empties_nothing_above_its_root() {
    let removed = |root: &str, path: &str| match super::removal(root, path) {
        ctxloom_ports::Operation::Outside {
            path,
            bytes,
            executable,
            keep,
        } => {
            assert!(bytes.is_none() && !executable);
            (path, keep)
        }
        other => panic!("not another tool's file: {other:?}"),
    };
    assert_eq!(
        removed("/work/demo/", "out/CLAUDE.md"),
        (
            "/work/demo/out/CLAUDE.md".to_string(),
            "/work/demo".to_string()
        )
    );
    assert_eq!(
        removed("/work/demo", "/home/a/.ctxloom/records/x.claims.yaml"),
        (
            "/home/a/.ctxloom/records/x.claims.yaml".to_string(),
            "/home/a/.ctxloom/records".to_string()
        )
    );
    assert_eq!(
        removed("C:\\work", "C:\\home\\records\\x.yaml"),
        (
            "C:\\home\\records\\x.yaml".to_string(),
            "C:\\home\\records".to_string()
        )
    );
    assert_eq!(
        removed("", "/work/demo/CLAUDE.md"),
        ("/work/demo/CLAUDE.md".to_string(), "/work/demo".to_string())
    );
    // With no project, a path is as the step named it, never put below
    // the filesystem's root.
    assert_eq!(
        removed("", "CLAUDE.md"),
        ("CLAUDE.md".to_string(), String::new())
    );
}
