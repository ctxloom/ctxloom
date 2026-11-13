//! Go's acceptance scenarios for the library's read side (S1), ported: each
//! test names its feature file and scenario, and says what the scenario
//! asserts, in the order it asserts it. A scenario outline's Examples rows
//! are the `FORMATS` loop: no `--format` (off a terminal, json), json, text.
//!
//! The commands that WRITE (create, edit, remove, import, ...) are S2's; where
//! a scenario starts by running one, the project is put in place directly
//! (`Project::bundle` and the rest of `library.test.rs`).

pub(super) use serde_json::Value;

pub(super) use super::library::{contains_value, entries, reports_as, Project, MINIMAL_CONFIG};
pub(super) use crate::tests::files;

/// An outline's `<flags>` column: the three rows every `@json-default` table has.
pub(super) const FORMATS: [&[&str]; 3] = [&[], &["--format", "json"], &["--format", "text"]];

pub(super) fn run_with_flags(project: &Project, base: &[&str], flags: &[&str]) -> super::Ran {
    let mut args = base.to_vec();
    args.extend_from_slice(flags);
    project.run(&args)
}

pub(super) fn is_text(flags: &[&str]) -> bool {
    flags.contains(&"text")
}

/// `bundle "demo"` with `fragment "testing"`, as the steps make them.
pub(super) fn demo_with_testing() -> Project {
    let project = Project::initialized().on_gos_catalog();
    project.bundle("demo");
    project.fragment("demo", "testing");
    project
}

// ---------------------------------------------------------------- fault_tolerance.feature

// fault_tolerance.feature: Scenario: A malformed config is refused by name,
// not read around
#[test]
fn test_a_malformed_config_is_refused_by_name() {
    let project = Project::initialized().on_gos_catalog();
    project.bundle("demo");
    project.write(".ctxloom/config.yaml", "::: not: [valid yaml\n\tbroken");
    project
        .run(&["bundle", "list"])
        .fails()
        .contains("cannot be parsed")
        .contains("config.yaml");
}

// fault_tolerance.feature: Scenario: A profile referencing a missing bundle
// is refused like a run; --degraded previews past it
#[test]
fn test_a_profile_referencing_a_missing_bundle_is_refused_like_a_run() {
    let project = Project::initialized().on_gos_catalog();
    project.write(
        ".ctxloom/content/bundles/v2/project/profiles/broken.yaml",
        "description: references a missing bundle\nbundles:\n  - does-not-exist\n",
    );
    project.write(
        ".ctxloom/content/bundles/v2/project/bundle.yaml",
        "schema_version: 4\nversion: 1.0.0\n",
    );
    let token = [("CLAUDE_CODE_OAUTH_TOKEN", "sk-ant-oat01-fixture")];
    let refused = project.run_with(
        &token,
        &["run", "--dry-run", "--profile", "broken", "hello"],
    );
    assert_eq!(refused.code, 21, "{}", refused.said());
    refused
        .contains("failed to load bundle")
        .contains("does-not-exist");
    let degraded = project.run_with(
        &token,
        &[
            "--degraded",
            "run",
            "--dry-run",
            "--profile",
            "broken",
            "hello",
        ],
    );
    degraded
        .succeeds()
        .contains("warning")
        .contains("does-not-exist");
}

// fault_tolerance.feature: Scenario: An unknown fragment reference fails cleanly
#[test]
fn test_an_unknown_fragment_reference_fails_cleanly() {
    let project = Project::initialized().on_gos_catalog();
    project.bundle("demo");
    project
        .run(&["fragment", "show", "demo#fragments/does-not-exist"])
        .fails()
        .contains("item not found")
        .contains("Available fragments: example");
}

// fault_tolerance.feature: Scenario: A garbage reference fails cleanly
#[test]
fn test_a_garbage_reference_fails_cleanly() {
    let project = Project::initialized().on_gos_catalog();
    project.bundle("demo");
    project
        .run(&["fragment", "show", "not-a-real-reference"])
        .fails()
        .contains("invalid reference format: expected bundle#fragments/name");
}

// ---------------------------------------------------------------- config.premised

// Owner ruling of 2026-11-12: `config.premised` decides when a fragment with
// a premise loads. `on-demand` (the default) holds it back and ends the
// context with the premise index of what was held back; `always` loads it
// with the rest and renders no index.

/// demo's `testing`, and `golang`, which carries a premise; `dev` composes
/// the bundle.
pub(super) fn demo_with_a_premise() -> Project {
    let project = demo_with_testing();
    project.write(
        ".ctxloom/content/bundles/v2/demo/fragments/golang.md",
        "---\ndescription: when editing Go code\n---\nGO-RULES-BODY\n",
    );
    project.profile("dev", "demo");
    project
}

/// The index's first line.
pub(super) const INDEX_HEADING: &str = "# Conditional guidance";

/// A context held `golang` back and offered it in the index.
pub(super) fn held_back_and_indexed(context: &str) {
    assert!(context.contains("FRAGMENT-BODY-testing"), "{context}");
    assert!(!context.contains("GO-RULES-BODY"), "held back: {context}");
    assert!(context.contains(INDEX_HEADING), "indexed: {context}");
    assert!(
        context.contains("demo#fragments/golang: when editing Go code"),
        "the index names it and its premise: {context}"
    );
}

/// A context loaded `golang` with the rest and rendered no index.
pub(super) fn loaded_and_unindexed(context: &str) {
    assert!(context.contains("FRAGMENT-BODY-testing"), "{context}");
    assert!(context.contains("GO-RULES-BODY"), "loaded: {context}");
    assert!(!context.contains(INDEX_HEADING), "no index: {context}");
}
