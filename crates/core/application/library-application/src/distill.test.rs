//! `fragment|command|bundle distill` at the application port (plan step
//! 5; step 10b: the daemon asks the model as a run and writes what follows
//! from it, through these two halves): content_distill.feature and
//! atomic_writes.feature's distilled Rule.

use std::sync::Arc;

use super::*;
use crate::fakes::{plain, Mem, Setup, CONFIG, ENVELOPE};

angzarr_presentation::english_for!(&[crate::EN_US.1]);

/// The source: long enough that a short answer clears the 2% floor.
fn long() -> String {
    "Retry a dead-letter queue with backoff, and tell every tenant. ".repeat(20)
}

fn world(files: &[(&str, &str)]) -> (Library, Arc<Mem>) {
    let source = long();
    let mut tree: Vec<(&str, &str)> = vec![
        ENVELOPE,
        ("fragments/rules.md", source.as_str()),
        ("prompts/guidance.md", source.as_str()),
        ("fragments/kept.md", "---\nno_distill: true\n---\nkept\n"),
    ];
    tree.extend_from_slice(files);
    Setup {
        bundles: &[("lore", &tree)],
        layers: &[CONFIG],
        builtins: true,
        ..Setup::default()
    }
    .fixture()
}

const ANSWER: &str = "DISTILLED: retries, tenants, dead-letter queues.";

fn digest() -> String {
    library::distill::source_digest(long().as_bytes())
}

/// The model's ask of `reference`, which must be one.
fn model(library: &Library, kind: Kind, reference: &str, force: bool) -> ModelAsk {
    match library
        .item_distill_ask(&plain(), kind, reference, force)
        .result
        .unwrap()
    {
        DistillAsk::Model(ask) => ask,
        DistillAsk::Done(done) => panic!("nothing asked: {done:?}"),
    }
}

/// What was answered of `reference` without asking, which must be so.
fn answered(library: &Library, kind: Kind, reference: &str, force: bool) -> ItemDistilled {
    match library
        .item_distill_ask(&plain(), kind, reference, force)
        .result
        .unwrap()
    {
        DistillAsk::Done(done) => done,
        DistillAsk::Model(ask) => panic!("asked: {ask:?}"),
    }
}

// content_distill.feature: distilling a fragment sends its content inside
// the distill prompt (ruling 128), recording its source's digest; asking
// writes nothing.
#[test]
fn test_a_fragment_s_ask_is_its_content_in_the_distill_prompt_with_its_digest() {
    let (library, mem) = world(&[]);
    let ask = model(&library, Kind::Fragment, "lore#fragments/rules", false);
    assert_eq!(
        (
            ask.done.status.as_str(),
            ask.done.kind.as_str(),
            ask.done.bundle.as_str(),
            ask.done.name.as_str(),
            ask.done.path.as_str(),
        ),
        (
            "distilled",
            "fragment",
            "lore",
            "rules",
            "fragments/rules.distilled.md"
        )
    );
    assert!(ask
        .request
        .starts_with("You are a text compression assistant."));
    assert!(ask.request.contains(&format!(
        "<content_to_compress>\n{}\n</content_to_compress>",
        long().trim()
    )));
    assert_eq!(ask.digest, digest());
    assert_eq!(mem.text("lore", "fragments/rules.distilled.md"), None);
}

// A command is asked through the same seam, its form in its own directory.
#[test]
fn test_a_command_s_ask_names_its_own_distilled_form() {
    let (library, _) = world(&[]);
    let ask = model(&library, Kind::Command, "lore#commands/guidance", true);
    assert_eq!(ask.done.path, "prompts/guidance.distilled.md");
    assert_eq!(ask.done.kind, "command");
}

// A fresh form is answered without asking unless forced; a stale one is
// asked again (atomic_writes.feature: distilling again records the
// source's digest); one marked no_distill is said and nothing is asked.
#[test]
fn test_a_fresh_form_is_kept_unless_forced_and_no_distill_asks_nothing() {
    let fresh = library::distill::distilled_file(&digest(), "OLD");
    let (library, _) = world(&[("fragments/rules.distilled.md", &fresh)]);
    let kept = answered(&library, Kind::Fragment, "lore#fragments/rules", false);
    assert_eq!(kept.status, "fresh");
    assert_eq!(kept.path, "fragments/rules.distilled.md");
    let forced = model(&library, Kind::Fragment, "lore#fragments/rules", true);
    assert_eq!(forced.done.status, "distilled");
    let marked = answered(&library, Kind::Fragment, "lore#fragments/kept", true);
    assert_eq!(marked.status, "no_distill");

    let (library, _) = world(&[("fragments/rules.distilled.md", "OLD\n")]);
    let stale = model(&library, Kind::Fragment, "lore#fragments/rules", false);
    assert_eq!(stale.digest, digest());
}

// The run's answer is written as the item's distilled form, with the
// digest it was asked under, and is the form a read then uses.
#[test]
fn test_an_answer_is_written_with_its_digest_and_used() {
    let (library, mem) = world(&[]);
    let done = library
        .item_distilled(
            &plain(),
            Kind::Fragment,
            "lore#fragments/rules",
            (&digest(), ANSWER),
        )
        .result
        .unwrap();
    assert_eq!(done.status, "distilled");
    assert_eq!(done.path, "fragments/rules.distilled.md");
    assert_eq!(
        mem.text("lore", "fragments/rules.distilled.md").unwrap(),
        library::distill::distilled_file(&digest(), ANSWER)
    );
    let shown = library
        .item_show(&plain(), Kind::Fragment, "lore#fragments/rules", true)
        .result
        .unwrap();
    assert_eq!(shown.state, DistilledState::Fresh);
    assert_eq!(shown.content.trim(), ANSWER);
    let command = library
        .item_distilled(
            &plain(),
            Kind::Command,
            "lore#commands/guidance",
            (&digest(), ANSWER),
        )
        .result
        .unwrap();
    assert!(mem
        .text("lore", &command.path)
        .unwrap()
        .ends_with(&format!("{ANSWER}\n")));
}

// content_distill.feature: an implausibly short answer is refused, the
// good distillation surviving (a failure, exit 10).
#[test]
fn test_a_short_answer_is_refused_and_writes_nothing() {
    let good = "---\nsource_sha256: x\n---\nGOOD\n";
    let (library, mem) = world(&[("fragments/rules.distilled.md", good)]);
    let refused = library.item_distilled(
        &plain(),
        Kind::Fragment,
        "lore#fragments/rules",
        (&digest(), "tiny"),
    );
    let error = refused.result.unwrap_err();
    assert!(
        matches!(error, AppError::DistillTruncated { answer: 4, .. }),
        "{error:?}"
    );
    assert_eq!(exit_code::ExitClass::exit(&error), exit_code::Exit::FAILED);
    assert!(
        error.english().contains("rejecting as truncated"),
        "{}",
        error.english()
    );
    assert_eq!(
        mem.text("lore", "fragments/rules.distilled.md").unwrap(),
        good
    );
}

// The digest rule (atomic_writes.feature's distilled Rule; step 10b): an
// answer asked of a source that moved while the model was asked is stale,
// refused (a decline, exit 20), and writes nothing.
#[test]
fn test_an_answer_whose_source_moved_since_it_was_asked_is_refused_stale() {
    let (library, mem) = world(&[]);
    let ask = model(&library, Kind::Fragment, "lore#fragments/rules", false);
    mem.put(
        "lore",
        "fragments/rules.md",
        "EDITED while the model was asked\n",
    );
    let refused = library.item_distilled(
        &plain(),
        Kind::Fragment,
        "lore#fragments/rules",
        (&ask.digest, ANSWER),
    );
    let error = refused.result.unwrap_err();
    assert!(
        matches!(&error, AppError::DistillSourceMoved { reference } if reference == "lore#fragments/rules"),
        "{error:?}"
    );
    assert_eq!(
        angzarr_diagnostic::Diagnostic::code(&error),
        "library-application-distill-source-moved"
    );
    assert_eq!(exit_code::ExitClass::exit(&error), exit_code::Exit::REFUSED);
    assert_eq!(mem.text("lore", "fragments/rules.distilled.md"), None);
}

// The built-in ctxloom bundle is read-only (rulings 110, 111): nothing is
// asked of it, and nothing written.
#[test]
fn test_the_built_in_bundle_is_not_distilled() {
    let (library, _) = world(&[]);
    let asked = library.item_distill_ask(&plain(), Kind::Command, "ctxloom#commands/distill", true);
    assert!(asked.result.is_err());
    let written = library.item_distilled(
        &plain(),
        Kind::Command,
        "ctxloom#commands/distill",
        (&digest(), ANSWER),
    );
    assert!(written.result.is_err());
}

// A missing item is said, with what the bundle has.
#[test]
fn test_a_missing_item_is_refused_naming_what_there_is() {
    let (library, _) = world(&[]);
    let refused = library.item_distill_ask(&plain(), Kind::Fragment, "lore#fragments/nosuch", true);
    let Err(AppError::ItemMissing { available, .. }) = refused.result else {
        panic!("missing: {:?}", refused.result);
    };
    assert_eq!(available, "kept, rules");
}
