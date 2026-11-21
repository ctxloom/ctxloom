//! The library's read side for `companion` (S1), ported from Go's acceptance
//! scenarios as library_cli.test.rs describes; one file per noun (design
//! §8.4), its harness library_cli.test.rs's.

use super::library_cli::*;

// ---------------------------------------------------------------- cli/companion.feature

// companion.feature: Scenario: A project's companions are added to the home
// registrations
#[test]
fn test_a_projects_companions_are_added_to_the_home_registrations() {
    let project = Project::initialized().on_gos_catalog();
    project.write_home(
        ".ctxloom/config.yaml",
        "schema_version: 9\ncompanions:\n  acme: {}\n",
    );
    project.write(
        ".ctxloom/config.yaml",
        "schema_version: 9\ncompanions:\n  beta: {}\n",
    );
    let ran = project.run(&["companion", "list", "--format", "json"]);
    ran.succeeds();
    let doc = ran.json();
    assert!(reports_as(&doc, "companions.0.name").ends_with("/companions/acme"));
    assert!(reports_as(&doc, "companions.1.name").ends_with("/companions/beta"));
}

// companion.feature: Scenario: An empty project list does not unregister a
// home companion
#[test]
fn test_an_empty_project_list_does_not_unregister_a_home_companion() {
    let project = Project::initialized().on_gos_catalog();
    project.write_home(
        ".ctxloom/config.yaml",
        "schema_version: 9\ncompanions:\n  acme: {}\n",
    );
    project.write(
        ".ctxloom/config.yaml",
        "schema_version: 9\ncompanions: {}\n",
    );
    let ran = project.run(&["companion", "list", "--format", "json"]);
    ran.succeeds();
    assert!(reports_as(&ran.json(), "companions.0.name").ends_with("/companions/acme"));
}

// companion.feature: Scenario: List answers which program each registered
// name resolves to, and whether it is on the PATH (and Scenario: Bare
// companion lists the registered companions): the program is the name's
// (OD-10), and it is only looked for, never run.
#[test]
fn test_list_answers_the_program_each_registered_name_resolves_to_and_whether_it_is_there() {
    if !files::runs_scripts() {
        return;
    }
    let project = Project::initialized().on_gos_catalog();
    let bin = project.dir().join("bin");
    files::put_program(bin.join("ctxloom-companion-acme"), "#!/bin/sh\nexit 9\n");
    project.put_on_path(&bin);
    project.write_home(
        ".ctxloom/config.yaml",
        "schema_version: 9\ncompanions:\n  acme: {}\n  gone: {}\n",
    );
    let ran = project.run(&["companion", "list", "--format", "json"]);
    ran.succeeds();
    let doc = ran.json();
    assert!(reports_as(&doc, "companions.0.name").ends_with("/companions/acme"));
    assert_eq!(
        reports_as(&doc, "companions.0.bin"),
        "ctxloom-companion-acme"
    );
    assert_eq!(reports_as(&doc, "companions.0.resolves"), "true");
    assert!(reports_as(&doc, "companions.1.name").ends_with("/companions/gone"));
    assert_eq!(reports_as(&doc, "companions.1.resolves"), "false");
    project
        .run(&["companion", "--format", "text"])
        .succeeds()
        .contains("acme")
        .contains("ctxloom-companion-acme")
        .contains("NOT FOUND")
        .lacks("Available Commands:");
}

// companion.feature: Scenario: A name with nothing on PATH is refused,
// naming the binary it looked for (the listing half: it says none is
// registered)
#[test]
fn test_no_registered_companion_is_said_in_prose() {
    let project = Project::initialized().on_gos_catalog();
    project
        .run(&["companion", "list", "--format", "text"])
        .succeeds()
        .contains("No companions registered");
}
