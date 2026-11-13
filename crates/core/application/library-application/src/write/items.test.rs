//! `fragment.feature`, `command.feature` and `editing.feature` at the
//! application port.

use std::sync::Arc;

use crate::fakes::{plain, Mem, Setup, CONFIG, ENVELOPE};
use crate::items::Kind;
use crate::write::views::Removal;
use crate::Library;

const DEMO: &[(&str, &str)] = &[
    ENVELOPE,
    ("fragments/testing.md", "FRAGMENT-BODY-testing"),
    ("fragments/testing.distilled.md", "DISTILLED"),
    ("fragments/other.md", "OTHER"),
    (
        "fragments/meta.md",
        "---\ntags:\n  - t\ncontent_hash: sha256:abc\nno_distill: true\n---\nBODY",
    ),
    (
        "prompts/review.md",
        "---\ndescription: Review\n---\nCOMMAND-BODY-review",
    ),
    ("prompts/review.distilled.md", "CMD-DISTILLED"),
];

fn world() -> (Library, Arc<Mem>) {
    let (library, mem) = Setup {
        bundles: &[("demo", DEMO)],
        layers: &[CONFIG],
        ..Setup::default()
    }
    .fixture();
    mem.set_editor(|_, content| Ok(format!("{content}\nEDITED-BY-TEST\n")));
    (library, mem)
}

fn err<T>(out: crate::Outcome<T>) -> String {
    format!("{:?}", out.result.err().expect("an error"))
}

// fragment.feature: Alice creates a fragment and it lands in the manifest
#[test]
fn test_create_writes_go_s_placeholder_and_answers_the_manifest_path() {
    let (library, mem) = world();
    // An editor that saves what it was given: the template is stored.
    mem.set_editor(|_, content| Ok(content.to_string()));
    let made = library
        .item_create(&plain(), Kind::Fragment, "demo", "fresh")
        .result
        .unwrap();
    assert_eq!(
        (
            made.status.as_str(),
            made.bundle.as_str(),
            made.name.as_str(),
            made.path.as_str(),
            made.kind.as_str()
        ),
        (
            "created",
            "demo",
            "fresh",
            "/p/.ctxloom/content/bundles/v2/demo/bundle.yaml",
            "fragment"
        )
    );
    assert_eq!(
        mem.text("demo", "fragments/fresh.md").unwrap(),
        "# fresh\n\nAdd content here."
    );
    let command = library
        .item_create(&plain(), Kind::Command, "demo", "go")
        .result
        .unwrap();
    assert_eq!(command.kind, "command");
    assert_eq!(
        mem.text("demo", "prompts/go.md").unwrap(),
        "# go\n\nAdd content here."
    );
    assert_eq!(mem.log(), ["apply demo", "apply demo"]);
}

#[test]
fn test_create_refuses_a_name_that_is_there_or_no_name_or_a_missing_bundle() {
    let (library, mem) = world();
    assert!(
        err(library.item_create(&plain(), Kind::Fragment, "demo", "testing"))
            .contains("ItemExists")
    );
    assert!(
        err(library.item_create(&plain(), Kind::Command, "demo", "review")).contains("ItemExists")
    );
    for bad in ["a/b", ".x", "", "a\\b"] {
        assert!(
            err(library.item_create(&plain(), Kind::Fragment, "demo", bad)).contains("BadItemName"),
            "{bad:?}"
        );
    }
    assert!(
        err(library.item_create(&plain(), Kind::Fragment, "nobundle", "x"))
            .contains("BundleNotFound")
    );
    assert_eq!(mem.writes(), 0);
    assert_eq!(
        mem.text("demo", "fragments/testing.md").unwrap(),
        "FRAGMENT-BODY-testing"
    );
}

// j001900: `fragment create <bundle>#fragments/<name>` opens the editor on
// the template and stores what she wrote.
#[test]
fn test_create_by_reference_stores_what_the_editor_wrote() {
    let (library, mem) = world();
    let made = library
        .item_create_at(&plain(), Kind::Fragment, "demo#fragments/fresh")
        .result
        .unwrap();
    assert_eq!(
        (made.bundle.as_str(), made.name.as_str()),
        ("demo", "fresh")
    );
    assert!(!made.bundle_created);
    assert_eq!(
        mem.text("demo", "fragments/fresh.md").unwrap(),
        "# fresh\n\nAdd content here.\nEDITED-BY-TEST\n"
    );
    let command = library
        .item_create_at(&plain(), Kind::Command, "demo#commands/go")
        .result
        .unwrap();
    assert_eq!(command.kind, "command");
    assert!(mem
        .text("demo", "prompts/go.md")
        .unwrap()
        .contains("EDITED-BY-TEST"));
}

// j000700: the editor is handed the template under the item's file name,
// and what it gives back is stored, a command's as a fragment's.
#[test]
fn test_create_by_reference_hands_the_editor_the_template_and_stores_its_answer() {
    let (library, mem) = world();
    mem.set_editor(|_, _| Ok("HOUSE-STYLE: short functions.\n".to_string()));
    library
        .item_create_at(&plain(), Kind::Fragment, "demo#fragments/house-style")
        .result
        .unwrap();
    assert_eq!(
        mem.text("demo", "fragments/house-style.md").unwrap(),
        "HOUSE-STYLE: short functions.\n"
    );
    assert_eq!(
        mem.edits(),
        [(
            "house-style.md".to_string(),
            "# house-style\n\nAdd content here.".to_string()
        )]
    );
    library
        .item_create_at(&plain(), Kind::Command, "demo#commands/commits")
        .result
        .unwrap();
    assert_eq!(
        mem.text("demo", "prompts/commits.md").unwrap(),
        "HOUSE-STYLE: short functions.\n"
    );
}

// j001900: an editor that changes nothing leaves the template, as the
// two-word form writes it.
#[test]
fn test_create_by_reference_with_an_editor_that_changes_nothing_keeps_the_template() {
    let (library, mem) = world();
    mem.set_editor(|_, content| Ok(content.to_string()));
    library
        .item_create_at(&plain(), Kind::Fragment, "demo#fragments/fresh")
        .result
        .unwrap();
    assert_eq!(
        mem.text("demo", "fragments/fresh.md").unwrap(),
        "# fresh\n\nAdd content here."
    );
}

// j001900: packaging loose notes into a bundle nobody made yet makes it.
#[test]
fn test_create_by_reference_into_a_bundle_nobody_has_makes_the_bundle() {
    let (library, mem) = world();
    let made = library
        .item_create_at(&plain(), Kind::Fragment, "runbook#fragments/deploy")
        .result
        .unwrap();
    assert!(made.bundle_created);
    assert_eq!(made.bundle, "runbook");
    assert!(mem.text("runbook", "bundle.yaml").is_some());
    assert!(mem
        .text("runbook", "fragments/deploy.md")
        .unwrap()
        .contains("EDITED-BY-TEST"));
    assert!(
        mem.text("runbook", "fragments/example.md").is_none(),
        "a bundle made for one fragment carries that fragment alone"
    );
}

// j001900: what the reference form refuses, it refuses before anything is
// written, the editor's failure and an emptied file included.
#[test]
fn test_create_by_reference_refuses_and_writes_nothing() {
    let (library, mem) = world();
    for (reference, want) in [
        ("demo#fragments/testing", "ItemExists"),
        ("demo#commands/x", "WrongKind"),
        ("demo", "BadReference"),
        ("demo#fragments/a.b/c", ""),
        ("../x#fragments/y", "BadBundleName"),
    ] {
        let got = err(library.item_create_at(&plain(), Kind::Fragment, reference));
        assert!(got.contains(want), "{reference}: {got}");
    }
    // A refusal is said before the editor is opened (j000700).
    assert!(mem.edits().is_empty(), "{:?}", mem.edits());
    mem.set_editor(|_, _| Ok(String::new()));
    assert!(
        err(library.item_create_at(&plain(), Kind::Fragment, "runbook#fragments/deploy"))
            .contains("EmptyEdit")
    );
    mem.set_editor(|_, _| Err("editor exited with error".to_string()));
    assert!(
        !err(library.item_create_at(&plain(), Kind::Fragment, "runbook#fragments/deploy"))
            .is_empty()
    );
    assert_eq!(mem.writes(), 0);
    assert!(mem.text("runbook", "bundle.yaml").is_none());
}

// fragment.feature: Bare fragment remove reports and destroys nothing
#[test]
fn test_remove_without_yes_says_what_would_go_and_writes_nothing() {
    let (library, mem) = world();
    let Removal::Preview(p) = library
        .item_remove(&plain(), Kind::Fragment, "demo#fragments/testing", false)
        .result
        .unwrap()
    else {
        panic!("a preview");
    };
    assert_eq!(p.target, "fragment \"testing\" from bundle \"demo\"");
    assert!(p.detail.is_empty());
    assert_eq!(
        p.apply,
        "ctxloom fragment remove demo#fragments/testing --yes"
    );
    let Removal::Preview(c) = library
        .item_remove(&plain(), Kind::Command, "demo#commands/review", false)
        .result
        .unwrap()
    else {
        panic!("a preview");
    };
    assert_eq!(c.apply, "ctxloom command remove demo#commands/review --yes");
    assert_eq!(mem.writes(), 0);
}

#[test]
fn test_remove_with_yes_takes_the_item_and_its_distilled_form_only() {
    let (library, mem) = world();
    let Removal::Done(done) = library
        .item_remove(&plain(), Kind::Fragment, "demo#fragments/testing", true)
        .result
        .unwrap()
    else {
        panic!("done");
    };
    assert_eq!(
        (
            done.status.as_str(),
            done.bundle.as_str(),
            done.name.as_str(),
            done.kind.as_str()
        ),
        ("deleted", "demo", "testing", "fragment")
    );
    assert_eq!(done.path, "/p/.ctxloom/content/bundles/v2/demo/bundle.yaml");
    assert_eq!(mem.text("demo", "fragments/testing.md"), None);
    assert_eq!(mem.text("demo", "fragments/testing.distilled.md"), None);
    assert_eq!(mem.text("demo", "fragments/other.md").unwrap(), "OTHER");
    assert!(mem.text("demo", "prompts/review.md").is_some());
    library
        .item_remove(&plain(), Kind::Command, "demo#commands/review", true)
        .result
        .unwrap();
    assert_eq!(mem.text("demo", "prompts/review.md"), None);
    assert_eq!(mem.text("demo", "prompts/review.distilled.md"), None);
}

#[test]
fn test_remove_of_an_item_nobody_has_is_refused_with_and_without_yes() {
    let (library, mem) = world();
    for yes in [false, true] {
        assert!(
            err(library.item_remove(&plain(), Kind::Fragment, "demo#fragments/nope", yes))
                .contains("ItemNotFound")
        );
    }
    assert!(
        err(library.item_remove(&plain(), Kind::Fragment, "demo#commands/review", true))
            .contains("WrongKind")
    );
    assert!(
        err(library.item_remove(&plain(), Kind::Fragment, "demo", true)).contains("BadReference")
    );
    assert!(
        err(library.item_remove(&plain(), Kind::Fragment, "nobundle#fragments/x", true))
            .contains("BundleNotFound")
    );
    assert_eq!(mem.writes(), 0);
}

// command.feature: Editing with --no-distill warns instead of leaving a stale distillation unmentioned
#[test]
fn test_edit_with_no_distill_saves_the_body_and_drops_the_stale_distillation() {
    let (library, mem) = world();
    let done = library
        .item_edit(&plain(), Kind::Command, "demo#commands/review", true)
        .result
        .unwrap();
    assert_eq!(
        (
            done.status.as_str(),
            done.bundle.as_str(),
            done.name.as_str(),
            done.kind.as_str()
        ),
        ("updated", "demo", "review", "command")
    );
    assert!(!done.distilled);
    assert!(
        done.distill_skipped,
        "the flag skipped a distillation the item would have had"
    );
    assert_eq!(done.reference, "demo#commands/review");
    assert_eq!(
        mem.text("demo", "prompts/review.md").unwrap(),
        "---\ndescription: Review\n---\nCOMMAND-BODY-review\nEDITED-BY-TEST\n"
    );
    assert_eq!(mem.text("demo", "prompts/review.distilled.md"), None);
    assert_eq!(
        mem.edits(),
        [("review.md".to_string(), "COMMAND-BODY-review".to_string())]
    );
}

#[test]
fn test_the_editor_is_given_the_body_and_the_front_matter_stays_minus_the_hash() {
    let (library, mem) = world();
    let done = library
        .item_edit(&plain(), Kind::Fragment, "demo#fragments/meta", false)
        .result
        .unwrap();
    assert_eq!(done.status, "updated");
    assert!(
        !done.distill_skipped,
        "no_distill was the item's own, not the flag's"
    );
    assert_eq!(mem.edits(), [("meta.md".to_string(), "BODY".to_string())]);
    assert_eq!(
        mem.text("demo", "fragments/meta.md").unwrap(),
        "---\ntags:\n  - t\nno_distill: true\n---\nBODY\nEDITED-BY-TEST\n"
    );
}

// editing.feature: Editing a fragment lands the change (the distillation backend cannot start)
#[test]
fn test_an_edit_that_would_be_distilled_is_saved_raw_and_says_so() {
    let (library, mem) = world();
    let said = err(library.item_edit(&plain(), Kind::Fragment, "demo#fragments/testing", false));
    assert!(said.contains("DistillUnavailable"), "{said}");
    assert_eq!(
        mem.text("demo", "fragments/testing.md").unwrap(),
        "FRAGMENT-BODY-testing\nEDITED-BY-TEST\n",
        "the content is saved RAW"
    );
    assert_eq!(
        mem.text("demo", "fragments/testing.distilled.md"),
        None,
        "never stale"
    );
}

// editing: nothing to change says so
#[test]
fn test_an_editor_that_changes_nothing_is_no_edit() {
    let (library, mem) = world();
    mem.set_editor(|_, content| Ok(content.to_string()));
    let done = library
        .item_edit(&plain(), Kind::Fragment, "demo#fragments/testing", true)
        .result
        .unwrap();
    assert_eq!(done.status, "unchanged");
    assert!(!done.distilled);
    assert_eq!(mem.writes(), 0);
    assert_eq!(
        mem.text("demo", "fragments/testing.distilled.md").unwrap(),
        "DISTILLED"
    );
}

// command.feature: An editor that returns nothing is refused rather than emptying the command
#[test]
fn test_an_editor_that_returns_nothing_is_refused() {
    let (library, mem) = world();
    for empty in ["", "  \n\t\n"] {
        let empty = empty.to_string();
        mem.set_editor(move |_, _| Ok(empty.clone()));
        let said = err(library.item_edit(&plain(), Kind::Command, "demo#commands/review", false));
        assert!(said.contains("EmptyEdit"), "{said}");
    }
    assert_eq!(mem.writes(), 0);
}

#[test]
fn test_an_editor_that_fails_leaves_the_item_alone() {
    let (library, mem) = world();
    mem.set_editor(|_, _| Err("editor exited with error".to_string()));
    assert!(library
        .item_edit(&plain(), Kind::Fragment, "demo#fragments/testing", true)
        .result
        .is_err());
    assert_eq!(mem.writes(), 0);
}

#[test]
fn test_edit_of_an_item_nobody_has_is_refused_before_the_editor_runs() {
    let (library, mem) = world();
    assert!(
        err(library.item_edit(&plain(), Kind::Fragment, "demo#fragments/nope", true))
            .contains("ItemMissing")
    );
    assert!(
        err(library.item_edit(&plain(), Kind::Fragment, "demo#commands/review", true))
            .contains("WrongKind")
    );
    assert!(
        err(library.item_edit(&plain(), Kind::Fragment, "nobundle#fragments/x", true))
            .contains("BundleNotFound")
    );
    assert!(mem.edits().is_empty());
    assert_eq!(mem.writes(), 0);
}

// A bundle that is there and cannot be read is never made again by a create
// into it: the create is refused for what the bundle is, and writes nothing.
#[test]
fn test_create_by_reference_into_an_unreadable_bundle_is_refused_and_writes_nothing() {
    let broken: &[(&str, &str)] = &[("bundle.yaml", "")];
    let (library, mem) = Setup {
        bundles: &[("demo", DEMO), ("broken", broken)],
        layers: &[CONFIG],
        ..Setup::default()
    }
    .fixture();
    mem.set_editor(|_, content| Ok(format!("{content}\nEDITED-BY-TEST\n")));
    let got = err(library.item_create_at(&plain(), Kind::Fragment, "broken#fragments/deploy"));
    assert!(got.contains("BundleUnreadable"), "{got}");
    assert!(mem.edits().is_empty(), "{:?}", mem.edits());
    assert_eq!(mem.writes(), 0);
    assert_eq!(mem.text("broken", "bundle.yaml").as_deref(), Some(""));
}

// OQ-6 (design §1.3): a created item's name is an ID,
// `^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`; one that breaks it is refused, in
// either form, and nothing is written.
#[test]
fn test_create_refuses_a_name_that_is_no_id_and_writes_nothing() {
    let (library, mem) = world();
    for bad in [
        "Upper",
        "under_score",
        "dot.ted",
        "two words",
        "9lead",
        "trail-",
        "a-very-long-name-that-runs-past-sixty-three-characters-xxxxxxxxxx",
    ] {
        let got = err(library.item_create(&plain(), Kind::Fragment, "demo", bad));
        assert!(got.contains("BadId"), "{bad:?}: {got}");
        let got =
            err(library.item_create_at(&plain(), Kind::Command, &format!("demo#commands/{bad}")));
        assert!(got.contains("BadId"), "{bad:?}: {got}");
    }
    assert!(mem.edits().is_empty(), "{:?}", mem.edits());
    assert_eq!(mem.writes(), 0);
    for good in [
        "a",
        "x9",
        "house-rules",
        "a-very-long-name-that-runs-to-sixty-three-characters-exactly-xx",
    ] {
        library
            .item_create(&plain(), Kind::Fragment, "demo", good)
            .result
            .unwrap();
    }
}

// OQ-5, OQ-6: a bundle the reference form makes is an ID too, never nested.
#[test]
fn test_create_by_reference_refuses_a_bundle_that_is_no_id_or_nested() {
    let (library, mem) = world();
    for bundle in ["Team", "team_rules", "team/rules"] {
        let got =
            err(library.item_create_at(&plain(), Kind::Fragment, &format!("{bundle}#fragments/x")));
        assert!(got.contains("BadId"), "{bundle:?}: {got}");
    }
    assert_eq!(mem.writes(), 0);
}
