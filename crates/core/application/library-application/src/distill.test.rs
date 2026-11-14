//! `fragment|command|bundle distill` at the application port (plan step
//! 5): content_distill.feature and atomic_writes.feature's distilled Rule.

use std::cell::RefCell;
use std::rc::Rc;
use std::sync::Arc;

use super::*;
use crate::fakes::{plain, Mem, Setup, CONFIG, ENVELOPE};

angzarr_presentation::english_for!(&[crate::EN_US.1]);

/// The source: long enough that a short answer clears the 2% floor.
fn long() -> String {
    "Retry a dead-letter queue with backoff, and tell every tenant. ".repeat(20)
}

/// A distiller that answers as it was set up, and keeps what it was asked.
struct Canned {
    answer: Result<String, String>,
    asked: RefCell<Vec<String>>,
}

#[derive(Debug)]
struct Down(String);

impl std::fmt::Display for Down {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "{}", self.0)
    }
}

impl Diagnostic for Down {
    fn code(&self) -> &str {
        "fake-down"
    }

    fn args(&self) -> Vec<(&str, angzarr_diagnostic::Arg)> {
        vec![("ext_err_txt", self.0.as_str().into())]
    }
}

impl Distiller for Canned {
    fn distill(&self, _: &Flags, request: &str) -> Distilled {
        self.asked.borrow_mut().push(request.to_string());
        Distilled {
            answer: self
                .answer
                .clone()
                .map_err(|why| Box::new(Down(why)) as Box<dyn Diagnostic>),
            notices: vec![Notice::text("the run said something")],
        }
    }
}

fn world(answer: Result<&str, &str>, files: &[(&str, &str)]) -> (Library, Arc<Mem>, Rc<Canned>) {
    let source = long();
    let mut tree: Vec<(&str, &str)> = vec![
        ENVELOPE,
        ("fragments/rules.md", source.as_str()),
        ("prompts/guidance.md", source.as_str()),
        ("fragments/kept.md", "---\nno_distill: true\n---\nkept\n"),
    ];
    tree.extend_from_slice(files);
    let (library, mem) = Setup {
        bundles: &[("lore", &tree)],
        layers: &[CONFIG],
        builtins: true,
        ..Setup::default()
    }
    .fixture();
    let canned = Rc::new(Canned {
        answer: answer.map(str::to_string).map_err(str::to_string),
        asked: RefCell::new(Vec::new()),
    });
    (library.with_distiller(canned.clone()), mem, canned)
}

const ANSWER: &str = "DISTILLED: retries, tenants, dead-letter queues.";

// content_distill.feature: distilling a fragment sends its content (inside
// the distill prompt, ruling 128) and stores the answer, with its source's
// digest (T30); the run's notices are said.
#[test]
fn test_distilling_a_fragment_sends_its_content_and_stores_the_answer_with_its_digest() {
    let (library, mem, canned) = world(Ok(ANSWER), &[]);
    let done = library.item_distill(&plain(), Kind::Fragment, "lore#fragments/rules", false);
    assert!(done
        .notices
        .iter()
        .any(|n| format!("{n:?}").contains("said something")));
    let done = done.result.unwrap();
    assert_eq!(
        (
            done.status.as_str(),
            done.kind.as_str(),
            done.bundle.as_str()
        ),
        ("distilled", "fragment", "lore")
    );
    assert_eq!(done.path, "fragments/rules.distilled.md");
    let asked = canned.asked.borrow();
    assert_eq!(asked.len(), 1);
    assert!(asked[0].starts_with("You are a text compression assistant."));
    assert!(asked[0].contains(&format!(
        "<content_to_compress>\n{}\n</content_to_compress>",
        long().trim()
    )));
    assert_eq!(
        mem.text("lore", "fragments/rules.distilled.md").unwrap(),
        library::distill::distilled_file(
            &library::distill::source_digest(long().as_bytes()),
            ANSWER
        )
    );
    let shown = library
        .item_show(&plain(), Kind::Fragment, "lore#fragments/rules", true)
        .result
        .unwrap();
    assert_eq!(shown.state, DistilledState::Fresh);
    assert_eq!(shown.content.trim(), ANSWER);
}

// A command distills through the same seam, into its own directory.
#[test]
fn test_distilling_a_command_stores_its_own_distilled_form() {
    let (library, mem, _) = world(Ok(ANSWER), &[]);
    let done = library
        .item_distill(&plain(), Kind::Command, "lore#commands/guidance", true)
        .result
        .unwrap();
    assert_eq!(done.path, "prompts/guidance.distilled.md");
    assert!(mem
        .text("lore", "prompts/guidance.distilled.md")
        .unwrap()
        .ends_with(&format!("{ANSWER}\n")));
}

// A fresh form is left without asking unless forced; a stale one is
// distilled again (atomic_writes.feature: distilling again records the
// source's digest); one marked no_distill is said and nothing is asked.
#[test]
fn test_a_fresh_form_is_kept_unless_forced_and_no_distill_asks_nothing() {
    let fresh = library::distill::distilled_file(
        &library::distill::source_digest(long().as_bytes()),
        "OLD",
    );
    let (library, mem, canned) = world(Ok(ANSWER), &[("fragments/rules.distilled.md", &fresh)]);
    let kept = library
        .item_distill(&plain(), Kind::Fragment, "lore#fragments/rules", false)
        .result
        .unwrap();
    assert_eq!(kept.status, "fresh");
    assert!(canned.asked.borrow().is_empty());
    assert_eq!(
        mem.text("lore", "fragments/rules.distilled.md").unwrap(),
        fresh
    );
    let forced = library
        .item_distill(&plain(), Kind::Fragment, "lore#fragments/rules", true)
        .result
        .unwrap();
    assert_eq!(forced.status, "distilled");
    assert!(mem
        .text("lore", "fragments/rules.distilled.md")
        .unwrap()
        .contains(ANSWER));
    let marked = library
        .item_distill(&plain(), Kind::Fragment, "lore#fragments/kept", true)
        .result
        .unwrap();
    assert_eq!(marked.status, "no_distill");
    assert_eq!(canned.asked.borrow().len(), 1);
    assert_eq!(mem.text("lore", "fragments/kept.distilled.md"), None);

    let (library, _, canned) = world(Ok(ANSWER), &[("fragments/rules.distilled.md", "OLD\n")]);
    let stale = library
        .item_distill(&plain(), Kind::Fragment, "lore#fragments/rules", false)
        .result
        .unwrap();
    assert_eq!(stale.status, "distilled");
    assert_eq!(canned.asked.borrow().len(), 1);
}

// content_distill.feature: an implausibly short answer is refused and the
// good distillation survives; a distiller that cannot run fails and leaves
// the item raw. Both fail the command (exit 10) and write nothing.
#[test]
fn test_a_short_answer_and_a_failed_distiller_fail_and_write_nothing() {
    let good = "---\nsource_sha256: x\n---\nGOOD\n";
    let (library, mem, _) = world(Ok("tiny"), &[("fragments/rules.distilled.md", good)]);
    let refused = library.item_distill(&plain(), Kind::Fragment, "lore#fragments/rules", true);
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

    let (library, mem, _) = world(Err("the engine process exited 1"), &[]);
    let failed = library.item_distill(&plain(), Kind::Fragment, "lore#fragments/rules", true);
    let error = failed.result.unwrap_err();
    assert_eq!(exit_code::ExitClass::exit(&error), exit_code::Exit::FAILED);
    assert_eq!(error.code(), "library-application-distill-failed");
    assert!(format!("{error:?}").contains("exited 1"), "{error:?}");
    assert_eq!(mem.text("lore", "fragments/rules.distilled.md"), None);
}

// A library with no distiller fails rather than pretending.
#[test]
fn test_no_distiller_fails() {
    let (library, _) = Setup {
        bundles: &[("lore", &[ENVELOPE, ("fragments/rules.md", "x\n")])],
        layers: &[CONFIG],
        builtins: true,
        ..Setup::default()
    }
    .fixture();
    let failed = library.item_distill(&plain(), Kind::Fragment, "lore#fragments/rules", true);
    assert!(matches!(failed.result, Err(AppError::DistillFailed { .. })));
}

// content_distill.feature (OD-39, by name): `bundle distill` reaches every
// item, fragments and commands, and passes over the one marked no_distill.
#[test]
fn test_distilling_a_bundle_reaches_every_item() {
    let (library, mem, canned) = world(Ok(ANSWER), &[]);
    let done = library
        .bundle_distill(&plain(), "lore", true)
        .result
        .unwrap();
    assert_eq!(done.bundle, "lore");
    let statuses: Vec<(&str, &str)> = done
        .items
        .iter()
        .map(|i| (i.name.as_str(), i.status.as_str()))
        .collect();
    assert_eq!(
        statuses,
        [
            ("kept", "no_distill"),
            ("rules", "distilled"),
            ("guidance", "distilled")
        ]
    );
    assert_eq!(canned.asked.borrow().len(), 2);
    assert!(mem.text("lore", "fragments/rules.distilled.md").is_some());
    assert!(mem.text("lore", "prompts/guidance.distilled.md").is_some());
    assert!(matches!(
        library.bundle_distill(&plain(), "nosuch", true).result,
        Err(AppError::Declined(_))
    ));
    // The first failure fails the whole command.
    let (library, _, _) = world(Ok("tiny"), &[]);
    assert!(library
        .bundle_distill(&plain(), "lore", true)
        .result
        .is_err());
}

// The built-in ctxloom bundle is read-only (rulings 110, 111).
#[test]
fn test_the_built_in_bundle_is_not_distilled() {
    let (library, _, canned) = world(Ok(ANSWER), &[]);
    let refused = library.item_distill(&plain(), Kind::Command, "ctxloom#commands/distill", true);
    assert!(refused.result.is_err());
    assert!(canned.asked.borrow().is_empty());
}
