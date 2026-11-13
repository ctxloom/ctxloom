//! The library's read side for `agent` (S1), ported from Go's acceptance
//! scenarios as library_cli.test.rs describes; one file per noun (design
//! §8.4), its harness library_cli.test.rs's.

use super::library_cli::*;

// ---------------------------------------------------------------- cli/agent.feature

// agent.feature: Scenario Outline: A project with no bindings says so, and
// says where to define one
#[test]
fn test_a_project_with_no_bindings_says_so_and_where_to_define_one() {
    for flags in FORMATS {
        let project = Project::initialized().on_gos_catalog();
        let ran = run_with_flags(&project, &["agent", "list"], flags);
        ran.succeeds();
        if is_text(flags) {
            ran.contains("No agents defined")
                .contains("'agents:' in .ctxloom/config.yaml");
        } else {
            assert_eq!(ran.json(), Value::Array(vec![]));
        }
    }
}

// agent.feature: Scenario Outline: Creating a binding records every axis, and
// the listing reads each one back (the listing half, from a binding written
// in the config)
#[test]
fn test_the_listing_reads_each_axis_of_a_binding_back() {
    for flags in FORMATS {
        let project = Project::initialized().on_gos_catalog();
        project.bundle("dev-base");
        project.profile("dev", "dev-base");
        project.write(
            ".ctxloom/config.yaml",
            "schema_version: 9\nagents:\n  developer:\n    engine: claude-code\n    profiles:\n      - dev\n    runtime: container-rootless\n",
        );
        let ran = run_with_flags(&project, &["agent", "list"], flags);
        ran.succeeds();
        if is_text(flags) {
            ran.contains("engine: claude-code")
                .contains("profiles: dev")
                .contains("runtime: container-rootless");
        } else {
            let doc = ran.json();
            assert_eq!(reports_as(&doc, "[name=developer].engine"), "claude-code");
            assert!(contains_value(&doc, "[name=developer].profiles", "dev"));
            assert_eq!(
                reports_as(&doc, "[name=developer].runtime"),
                "container-rootless"
            );
        }
    }
}

// agent.feature: Scenario Outline: Bare agent lists the bindings
#[test]
fn test_bare_agent_lists_the_bindings() {
    for flags in FORMATS {
        let project = Project::initialized().on_gos_catalog();
        project.bundle("dev-base");
        project.profile("dev", "dev-base");
        project.write(
            ".ctxloom/config.yaml",
            "schema_version: 9\nagents:\n  developer:\n    profiles: [dev]\n",
        );
        let ran = run_with_flags(&project, &["agent"], flags);
        ran.succeeds().lacks("Available Commands:");
        if is_text(flags) {
            ran.contains("developer");
        } else {
            assert_eq!(
                reports_as(&ran.json(), "[name=developer].name"),
                "developer"
            );
        }
    }
}

// agent.feature: Scenario Outline: Showing one binding reports what was
// declared and what it resolves to
#[test]
fn test_showing_one_binding_reports_what_was_declared_and_what_it_resolves_to() {
    for flags in FORMATS {
        let project = Project::initialized().on_gos_catalog();
        project.bundle("dev-base");
        project.profile("dev", "dev-base");
        project.write(
            ".ctxloom/config.yaml",
            "schema_version: 9\nagents:\n  developer:\n    engine: claude-code\n    profiles: [dev]\n    runtime: container-rootless\n",
        );
        let ran = run_with_flags(&project, &["agent", "show", "developer"], flags);
        ran.succeeds();
        if is_text(flags) {
            ran.contains("Agent: developer")
                .contains("Engine (declared): claude-code")
                .contains("Runtime: container-rootless")
                .contains("Resolved engine:")
                .contains("Composed fragments:");
        } else {
            let doc = ran.json();
            assert_eq!(reports_as(&doc, "definition.name"), "developer");
            assert_eq!(reports_as(&doc, "definition.engine"), "claude-code");
            assert_eq!(reports_as(&doc, "definition.runtime"), "container-rootless");
            assert!(contains_value(&doc, "definition.profiles", "dev"));
            assert_eq!(reports_as(&doc, "resolved.engine"), "claude-code");
            assert!(contains_value(
                &doc,
                "resolved.fragments",
                "ctxloom+local:dev-base#fragments/example"
            ));
        }
    }
}

// agent.feature: Scenario: Showing a binding nobody defined fails, naming
// what was asked for
#[test]
fn test_showing_a_binding_nobody_defined_fails_naming_it() {
    let project = Project::initialized().on_gos_catalog();
    project
        .run(&["agent", "show", "nosuchagent"])
        .fails()
        .contains("nosuchagent");
}

// agent.feature: Scenario: A binding still carrying auth is refused, naming the key
#[test]
fn test_a_binding_still_carrying_auth_is_refused_naming_the_key() {
    let project = Project::initialized().on_gos_catalog();
    project.write(
        ".ctxloom/config.yaml",
        "schema_version: 9\nagents:\n  developer:\n    profiles: [dev]\n    auth: login\n",
    );
    project
        .run(&["agent", "list"])
        .fails()
        .contains("unknown key `agents.developer.auth`");
}
