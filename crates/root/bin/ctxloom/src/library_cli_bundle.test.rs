//! The library's read side for `bundle` (S1), ported from Go's acceptance
//! scenarios as library_cli.test.rs describes; one file per noun (design
//! §8.4), its harness library_cli.test.rs's.

use super::library_cli::*;

// ---------------------------------------------------------------- cli/bundle.feature

// bundle.feature: Scenario Outline: The listing summarizes each bundle by
// version and by what it holds (the read half: counts, version; the edit that
// follows is S2's).
#[test]
fn test_bundle_listing_summarizes_each_bundle_by_version_and_content() {
    for flags in FORMATS {
        let project = Project::initialized().on_gos_catalog();
        project.bundle("demo");
        let ran = run_with_flags(&project, &["bundle", "list", "--no-companions"], flags);
        ran.succeeds();
        if is_text(flags) {
            ran.contains("Installed bundles (1):")
                .contains("demo (v1.0.0)")
                .contains("Contains: 1 fragment")
                .contains("1 command");
        } else {
            // OD-40: `ListBundlesResponse`, each bundle named under the project.
            let doc = ran.json();
            assert_eq!(entries(&doc, "bundles"), 1);
            assert_eq!(
                reports_as(&doc, "bundles[name$=/bundles/demo].version"),
                "1.0.0"
            );
            assert_eq!(
                reports_as(&doc, "bundles[name$=/bundles/demo].fragment_count"),
                "1"
            );
            assert_eq!(
                reports_as(&doc, "bundles[name$=/bundles/demo].command_count"),
                "1"
            );
            assert_eq!(
                reports_as(&doc, "bundles[name$=/bundles/demo].origin"),
                "PROJECT"
            );
        }
    }
}

// bundle.feature: Scenario Outline: Bare bundle lists what is installed
#[test]
fn test_bare_bundle_lists_what_is_installed() {
    for flags in FORMATS {
        let project = Project::initialized().on_gos_catalog();
        project.bundle("demo");
        let ran = run_with_flags(&project, &["bundle", "--no-companions"], flags);
        ran.succeeds().lacks("Available Commands:");
        if is_text(flags) {
            ran.contains("Installed bundles (1):")
                .contains("demo (v1.0.0)");
        } else {
            let doc = ran.json();
            assert_eq!(entries(&doc, "bundles"), 1);
            assert_eq!(
                reports_as(&doc, "bundles[name$=/bundles/demo].version"),
                "1.0.0"
            );
        }
    }
}

// bundle.feature: Scenario Outline: Showing a bundle names every section it carries
#[test]
fn test_showing_a_bundle_names_every_section_it_carries() {
    for flags in FORMATS {
        let project = demo_with_testing();
        let ran = run_with_flags(&project, &["bundle", "show", "demo"], flags);
        ran.succeeds();
        if is_text(flags) {
            ran.contains("Bundle: demo")
                .contains("Description: acceptance fixture bundle")
                .contains("Fragments (2):")
                .contains("- example [example] (no_distill)")
                .contains("- testing")
                .contains("Commands (1):")
                .contains("Example prompt");
        } else {
            // The `Bundle` (FULL), each child a summary naming it (OQ-14).
            let doc = ran.json();
            assert!(reports_as(&doc, "name").ends_with("/bundles/demo"), "{doc}");
            assert_eq!(reports_as(&doc, "description"), "acceptance fixture bundle");
            assert_eq!(entries(&doc, "fragments"), 2);
            assert_eq!(
                reports_as(&doc, "fragments[item$=/fragments/example].no_distill"),
                "true"
            );
            assert_eq!(
                reports_as(&doc, "fragments[item$=/fragments/testing].no_distill"),
                "false"
            );
            assert_eq!(entries(&doc, "commands"), 1);
            assert_eq!(
                reports_as(&doc, "commands[item$=/commands/example].description"),
                "Example prompt"
            );
        }
    }
}

// bundle.feature: Scenario Outline: Show does not take the item selector —
// that grammar belongs to view
#[test]
fn test_show_does_not_take_the_item_selector_but_view_does() {
    for flags in FORMATS {
        let project = demo_with_testing();
        let shown = run_with_flags(&project, &["bundle", "show", "demo"], flags);
        shown.succeeds();
        if is_text(flags) {
            shown.contains("Fragments (2):");
        } else {
            assert_eq!(entries(&shown.json(), "fragments"), 2);
        }
        project
            .run(&["bundle", "show", "demo#fragments/testing"])
            .fails()
            .contains("bundle not found: demo#fragments/testing");
        project
            .run(&["bundle", "view", "demo#fragments/testing"])
            .succeeds()
            .contains("FRAGMENT-BODY-testing");
    }
}

// bundle.feature: Scenario: Viewing a bundle dumps the manifest with its
// items' bodies in it
#[test]
fn test_viewing_a_bundle_dumps_the_manifest_with_its_items_bodies() {
    let project = demo_with_testing();
    project
        .run(&["bundle", "view", "demo"])
        .succeeds()
        .contains("testing")
        .contains("FRAGMENT-BODY-testing")
        .contains("Example prompt content. Describe what this prompt does.");
}

// bundle.feature: Scenario: A path selector returns one item's bytes and
// leaves the rest behind
#[test]
fn test_a_path_selector_returns_one_items_bytes() {
    let project = demo_with_testing();
    let fragment = project.run(&["bundle", "view", "demo#fragments/testing"]);
    fragment
        .succeeds()
        .contains("FRAGMENT-BODY-testing")
        .lacks("Example prompt content. Describe what this prompt does.");
    let command = project.run(&["bundle", "view", "demo#commands/example"]);
    command
        .succeeds()
        .contains("Example prompt content. Describe what this prompt does.")
        .lacks("FRAGMENT-BODY-testing");
}

// bundle.feature: Scenario: The selector reaches an MCP server's
// configuration too
#[test]
fn test_the_selector_reaches_an_mcp_servers_configuration() {
    let project = Project::initialized().on_gos_catalog();
    project.bundle("demo");
    project.write(
        ".ctxloom/content/bundles/v2/demo/mcp/tree-sitter.yaml",
        "command: tree-sitter\n",
    );
    project
        .run(&["bundle", "view", "demo#mcp/tree-sitter", "--format", "text"])
        .succeeds()
        .contains("# MCP Server: tree-sitter")
        .contains("command: tree-sitter");
}

// bundle.feature: Scenario: Asking for the distilled rendering of an item
// that has none falls back to the raw bytes
#[test]
fn test_asking_for_a_distilled_rendering_that_was_never_made_falls_back() {
    let project = demo_with_testing();
    project
        .run(&["bundle", "view", "demo#fragments/testing"])
        .succeeds()
        .contains("FRAGMENT-BODY-testing");
    project
        .run(&["bundle", "view", "demo#fragments/testing", "--distilled"])
        .succeeds()
        .contains("FRAGMENT-BODY-testing");
}

// bundle.feature: Scenario: A selector that is not `type/name`, or names a
// type that does not exist, is refused by name
#[test]
fn test_a_bad_selector_is_refused_by_name() {
    let project = demo_with_testing();
    project
        .run(&["bundle", "view", "demo#fragments"])
        .fails()
        .contains("invalid path format");
    project
        .run(&["bundle", "view", "demo#widgets/testing"])
        .fails()
        .contains("unknown item kind")
        .contains("widgets")
        .contains("expected fragments, commands, mcp, skills, or profiles");
    project
        .run(&["bundle", "view", "demo#fragments/testing"])
        .succeeds()
        .contains("FRAGMENT-BODY-testing");
}

// ---------------------------------------------------------------- environment.md: finding the project

// docs/environment.md (CTXLOOM_ROOT) and configload/appdir.go: with no
// CTXLOOM_ROOT the project is the nearest .ctxloom up from the working
// directory; nothing is created when a command only reads.
#[test]
fn test_the_project_is_the_nearest_ctxloom_up_from_the_working_directory() {
    let project = Project::initialized().on_gos_catalog();
    project.bundle("demo");
    let deeper = project.dir().join("sub/deeper");
    files::mkdirs(&deeper);
    let ran = project.run_in(&deeper, &["bundle", "list", "--format", "json"]);
    ran.succeeds();
    let doc = ran.json();
    assert_eq!(
        reports_as(&doc, "bundles[name$=/bundles/demo].version"),
        "1.0.0"
    );
    // Named under the root's project, as a read at the root names it.
    let at_root = project.run(&["bundle", "list", "--format", "json"]);
    at_root.succeeds();
    assert_eq!(at_root.json(), doc);
    assert!(
        !files::exists(deeper.join(".ctxloom")),
        "reading creates nothing"
    );
}

// docs/environment.md: the minimal config is all a project needs
#[test]
fn test_the_minimal_config_is_enough() {
    let project = Project::bare().on_gos_catalog();
    project.write(".ctxloom/config.yaml", MINIMAL_CONFIG);
    project
        .run(&["bundle", "list", "--format", "json"])
        .succeeds();
}
