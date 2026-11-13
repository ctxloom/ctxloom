//! The library's read side for `search` (S1), ported from Go's acceptance
//! scenarios as library_cli.test.rs describes; one file per noun (design
//! §8.4), its harness library_cli.test.rs's.

use super::library_cli::*;

// ---------------------------------------------------------------- cli/search.feature

// search.feature: Scenario Outline: A search with neither a query nor a tag
// is refused, not answered with everything
#[test]
fn test_a_search_with_neither_a_query_nor_a_tag_is_refused() {
    for flags in FORMATS {
        let project = demo_with_testing();
        let ran = run_with_flags(&project, &["search", "--local", "testing"], flags);
        ran.succeeds();
        if is_text(flags) {
            ran.contains("Results (1):");
        } else {
            assert_eq!(reports_as(&ran.json(), "count"), "1");
        }
        project
            .run(&["search", "--local"])
            .fails()
            .contains("please provide a search query or tags")
            .lacks("Results (");
    }
}

// search.feature: Scenario Outline: A --type nobody recognizes is refused,
// and the message names the vocabulary
#[test]
fn test_an_unknown_search_type_is_refused_naming_the_vocabulary() {
    for flags in FORMATS {
        let project = demo_with_testing();
        let ran = run_with_flags(
            &project,
            &["search", "--local", "--type", "fragment", "testing"],
            flags,
        );
        ran.succeeds();
        if is_text(flags) {
            ran.contains("Results (1):");
        } else {
            assert_eq!(reports_as(&ran.json(), "count"), "1");
        }
        project
            .run(&["search", "--local", "--type", "widget", "testing"])
            .fails()
            .contains("unknown type")
            .contains("valid: fragment, command, skill, profile, bundle, mcp_server");
    }
}

// search.feature: Scenario Outline: One query finds matching content of more
// than one kind
#[test]
fn test_one_query_finds_matching_content_of_more_than_one_kind() {
    for flags in FORMATS {
        let project = demo_with_testing();
        project.command("demo", "testing-review");
        let ran = run_with_flags(&project, &["search", "--local", "testing"], flags);
        ran.succeeds();
        if is_text(flags) {
            ran.contains("Results (2):")
                .contains("- testing")
                .contains("- testing-review");
        } else {
            let doc = ran.json();
            assert_eq!(reports_as(&doc, "count"), "2");
            assert_eq!(reports_as(&doc, "local[name=testing].type"), "fragment");
            assert_eq!(
                reports_as(&doc, "local[name=testing-review].type"),
                "command"
            );
        }
    }
}

// search.feature: Scenario Outline: Restricting to fragments drops the
// command the same query matched; Restricting to commands drops the fragment
#[test]
fn test_restricting_to_a_kind_drops_the_other_kinds() {
    for (kind, survivor) in [("fragment", "testing"), ("command", "testing-review")] {
        let project = demo_with_testing();
        project.command("demo", "testing-review");
        assert_eq!(
            reports_as(
                &project
                    .run(&["search", "--local", "testing"])
                    .succeeds()
                    .json(),
                "count"
            ),
            "2"
        );
        let ran = project.run(&["search", "--local", "--type", kind, "testing"]);
        ran.succeeds();
        let doc = ran.json();
        assert_eq!(reports_as(&doc, "count"), "1");
        assert_eq!(
            reports_as(&doc, &format!("local[name={survivor}].type")),
            kind
        );
    }
}

// search.feature: Scenario Outline: Restricting to profiles finds a
// composition and nothing else
#[test]
fn test_restricting_to_profiles_finds_a_composition_and_nothing_else() {
    for flags in FORMATS {
        let project = Project::initialized().on_gos_catalog();
        project.bundle("demo");
        project.fragment("demo", "dev-notes");
        project.profile("dev", "demo");
        assert_eq!(
            reports_as(
                &project.run(&["search", "--local", "dev"]).succeeds().json(),
                "count"
            ),
            "2"
        );
        let ran = run_with_flags(
            &project,
            &["search", "--local", "--type", "profile", "dev"],
            flags,
        );
        ran.succeeds();
        if is_text(flags) {
            ran.contains("Results (1):").contains("- dev");
        } else {
            let doc = ran.json();
            assert_eq!(reports_as(&doc, "count"), "1");
            assert_eq!(reports_as(&doc, "local[name=dev].type"), "profile");
        }
    }
}

// search.feature: Scenario Outline: A tag search returns what carries the tag
// and leaves out what does not
#[test]
fn test_a_tag_search_returns_what_carries_the_tag() {
    for flags in FORMATS {
        let project = demo_with_testing();
        let by_name = project.run(&["search", "--local", "testing"]);
        assert_eq!(
            reports_as(&by_name.succeeds().json(), "local[name=testing].type"),
            "fragment"
        );
        let ran = run_with_flags(&project, &["search", "--local", "--tag", "example"], flags);
        ran.succeeds();
        if is_text(flags) {
            ran.contains("Results (1):").contains("- example [example]");
        } else {
            let doc = ran.json();
            assert_eq!(reports_as(&doc, "count"), "1");
            assert!(contains_value(&doc, "local[name=example].tags", "example"));
        }
    }
}
