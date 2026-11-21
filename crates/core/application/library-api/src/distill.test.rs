//! A distill call's answer (step 10b), field by field.

use super::*;

fn done(status: &str, kind: &str) -> ItemDistilled {
    ItemDistilled {
        status: status.to_string(),
        kind: kind.to_string(),
        bundle: "demo".to_string(),
        name: "review".to_string(),
        path: "x/review.distilled.md".to_string(),
    }
}

const NAME: &str = "projects/p/bundles/demo/commands/review";

#[test]
fn test_an_item_s_resource_name_is_its_reference() {
    assert_eq!(reference_of(NAME), "demo#commands/review");
    assert_eq!(
        reference_of("users/me/bundles/lore/fragments/rules"),
        "lore#fragments/rules"
    );
    assert_eq!(reference_of("projects/p/bundles/demo"), "demo");
}

#[test]
fn test_an_item_marked_no_distill_is_answered_with_why_and_no_run() {
    let kept = command_response(
        NAME,
        &done("no_distill", "command"),
        String::new(),
        Vec::new(),
    );
    let command = kept.command.unwrap();
    assert_eq!(command.name, NAME);
    assert_eq!(command.distilled, v1::DistilledState::None as i32);
    assert_eq!(command.no_distill, Some(true));
    assert_eq!(kept.path, "x/review.distilled.md");
    assert_eq!(kept.applied, Some(false));
    assert_eq!(kept.detail, ["no_distill"]);
    assert!(kept.run.is_empty());
}

#[test]
fn test_a_distill_that_asks_names_its_run_and_applies_nothing_yet() {
    let notice = v1::Notice {
        code: "c".to_string(),
        ..v1::Notice::default()
    };
    let asked = fragment_response(
        "projects/p/bundles/demo/fragments/review",
        &done("distilled", "fragment"),
        "projects/p/runs/r".to_string(),
        vec![notice.clone()],
    );
    let fragment = asked.fragment.unwrap();
    assert_eq!(fragment.distilled, v1::DistilledState::Fresh as i32);
    assert_eq!(fragment.no_distill, None);
    assert_eq!(asked.applied, Some(false));
    assert!(asked.detail.is_empty());
    assert_eq!(asked.run, "projects/p/runs/r");
    assert_eq!(asked.notices, [notice]);
    let fresh = fragment_response("n", &done("fresh", "fragment"), String::new(), Vec::new());
    assert_eq!(fresh.detail, ["fresh"]);
    assert_eq!(
        fresh.fragment.unwrap().distilled,
        v1::DistilledState::Fresh as i32
    );
}
