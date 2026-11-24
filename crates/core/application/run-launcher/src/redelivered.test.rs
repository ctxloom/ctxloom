//! Step 10c U13 (texts.md §1): a re-assembled context, as an engine is
//! handed it.

use super::{redelivered_context, ContextRedelivered};

// The framing line names what changed (bundles, and `config`, comma
// separated: never a path or a value), says the context changed and that
// what follows replaces the first; then a blank line, then the context
// exactly as assembled.
#[test]
fn a_redelivered_context_is_framed_then_given_whole() {
    let changed = vec!["config".to_string(), "demo".to_string()];
    let text = redelivered_context(&changed, "# Changed while it ran\n\nmore");
    let (framing, rest) = text
        .split_once("\n\n")
        .expect("a blank line after the framing");
    assert!(
        framing.starts_with("ctxloom: your context changed while this session ran (config, demo)."),
        "{framing}"
    );
    assert!(
        framing.contains("replaces the context you were given"),
        "{framing}"
    );
    assert_eq!(rest, "# Changed while it ran\n\nmore");
}

// Its message is the catalog's, by its code, with its one arg.
#[test]
fn the_framing_is_the_catalogs_message() {
    use coded_diagnostic::Diagnostic;
    let framing = ContextRedelivered {
        changed: vec!["demo".to_string()],
    };
    assert_eq!(framing.code(), "run-launcher-context-redelivered");
    assert_eq!(framing.args().len(), 1);
}

/// `n` characters of `c`.
fn of(c: char, n: usize) -> String {
    std::iter::repeat_n(c, n).collect()
}

fn fragment(name: &str, body: &str) -> super::ChangedFragment {
    super::ChangedFragment {
        name: name.to_string(),
        body: body.to_string(),
    }
}

// Step 10c U13 rework (owner, 2026-11-18: claude caps a hook's context at
// 10,000 characters): within the budget the context is given whole; over
// it, a framing line and ONLY the changed fragments, whole; over it even
// so, a pointer to `ctxloom context assemble`; with no room even for that,
// nothing (the session stays owed).
#[test]
fn a_context_over_its_budget_is_its_changed_fragments_else_a_pointer() {
    use super::redelivered_within;
    let changed = vec!["demo".to_string()];
    let fragments = [fragment("projects/p/bundles/demo/fragments/a", "the new a")];
    let whole = redelivered_context(&changed, "small");
    assert_eq!(
        redelivered_within(&changed, "small", &fragments, whole.chars().count()),
        whole,
        "exactly at the budget, whole"
    );
    let big = of('#', 500);
    let only = redelivered_within(&changed, &big, &fragments, 400);
    assert!(only.chars().count() <= 400, "{only}");
    assert!(!only.contains('#'), "none of the unchanged context: {only}");
    assert!(
        only.starts_with("ctxloom: your context changed while this session ran (demo)."),
        "{only}"
    );
    assert!(
        only.contains("projects/p/bundles/demo/fragments/a\nthe new a"),
        "{only}"
    );
    assert!(only.contains("only the fragments that changed"), "{only}");
    // The changes alone over it: the pointer.
    let long = [fragment(
        "projects/p/bundles/demo/fragments/a",
        &of('~', 450),
    )];
    let pointer = redelivered_within(&changed, &big, &long, 400);
    assert!(pointer.chars().count() <= 400, "{pointer}");
    assert!(pointer.contains("ctxloom context assemble"), "{pointer}");
    assert!(!pointer.contains('~'), "{pointer}");
    // No changed fragment to give (a config change): the pointer.
    assert_eq!(redelivered_within(&changed, &big, &[], 400), pointer);
    // No room even for the pointer: nothing.
    assert_eq!(redelivered_within(&changed, &big, &long, 10), "");
    // The budget is counted in characters, never bytes.
    let wide = of('\u{e9}', 300);
    let at = redelivered_context(&changed, &wide).chars().count();
    assert_eq!(
        redelivered_within(&changed, &wide, &[], at),
        redelivered_context(&changed, &wide)
    );
}
