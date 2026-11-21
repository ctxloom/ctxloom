//! The library's read side for `item` (S1), ported from Go's acceptance
//! scenarios as library_cli.test.rs describes; one file per noun (design
//! §8.4), its harness library_cli.test.rs's.

use super::library_cli::*;

// ---------------------------------------------------------------- cli/fragment.feature, read_extras.feature

// fragment.feature: Scenario: Search finds a fragment by name
#[test]
fn test_search_finds_a_fragment_by_name() {
    let project = demo_with_testing();
    project
        .run(&["search", "--local", "testing"])
        .succeeds()
        .contains("testing");
}

// read_extras.feature: Scenario: Show a fragment's content
#[test]
fn test_show_a_fragments_content() {
    let project = demo_with_testing();
    project
        .run(&["fragment", "show", "demo#fragments/testing"])
        .succeeds()
        .contains("testing")
        .contains("FRAGMENT-BODY-testing");
}

// read_extras.feature: Scenario: Show a prompt's content
#[test]
fn test_show_a_prompts_content() {
    let project = Project::initialized().on_gos_catalog();
    project.bundle("demo");
    project.command("demo", "review");
    project
        .run(&["command", "show", "demo#commands/review"])
        .succeeds()
        .contains("review")
        .contains("COMMAND-BODY-review");
}

// fragment.feature: Scenario: Alice creates a fragment and it lands in the
// manifest, the listing, and the agent's resource (the listing half; the
// create is S2's and the MCP resource is the MCP slice's)
#[test]
fn test_a_fragment_in_a_bundle_is_in_the_listing() {
    let project = demo_with_testing();
    project
        .run(&["fragment", "list"])
        .succeeds()
        .contains("testing");
}

// ---------------------------------------------------------------- cli/command.feature

// command.feature: Scenario: Listing commands with a misspelled --bundle is
// refused, not answered empty
#[test]
fn test_a_command_listing_with_a_misspelled_bundle_is_refused() {
    let project = Project::initialized().on_gos_catalog();
    project.bundle("demo");
    project.command("demo", "review");
    project
        .run(&["command", "list", "--bundle", "nosuchbundle"])
        .fails()
        .contains("no bundle named")
        .contains("nosuchbundle");
}

// command.feature: Scenario: Alice creates a command and it is listed (the
// listing half)
#[test]
fn test_a_command_in_a_bundle_is_listed() {
    let project = Project::initialized().on_gos_catalog();
    project.bundle("demo");
    project.command("demo", "review");
    project
        .run(&["command", "list"])
        .succeeds()
        .contains("review");
}
