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
        "schema_version: 9\ncompanions:\n  acme: {command: [acme-bin]}\n",
    );
    project.write(
        ".ctxloom/config.yaml",
        "schema_version: 9\ncompanions:\n  beta: {command: [beta-bin]}\n",
    );
    let ran = project.run(&["companion", "list", "--format", "json"]);
    ran.succeeds();
    let doc = ran.json();
    assert_eq!(reports_as(&doc, "0.name"), "acme");
    assert_eq!(reports_as(&doc, "1.name"), "beta");
}

// companion.feature: Scenario: An empty project list does not unregister a
// home companion
#[test]
fn test_an_empty_project_list_does_not_unregister_a_home_companion() {
    let project = Project::initialized().on_gos_catalog();
    project.write_home(
        ".ctxloom/config.yaml",
        "schema_version: 9\ncompanions:\n  acme: {command: [acme-bin]}\n",
    );
    project.write(
        ".ctxloom/config.yaml",
        "schema_version: 9\ncompanions: {}\n",
    );
    let ran = project.run(&["companion", "list", "--format", "json"]);
    ran.succeeds();
    assert_eq!(reports_as(&ran.json(), "0.name"), "acme");
}

// companion.feature: Scenario: List answers which program each registered
// name runs, and whether it is there (and Scenario: Bare companion lists the
// registered companions): the command is what the registration holds, and the
// program is only looked for, never run.
#[test]
fn test_list_answers_the_command_each_registered_name_runs_and_whether_it_is_there() {
    if !files::runs_scripts() {
        return;
    }
    let project = Project::initialized().on_gos_catalog();
    let exe = project.dir().join("bin").join("acme-bin");
    files::put_program(&exe, "#!/bin/sh\nexit 9\n");
    project.write_home(
        ".ctxloom/config.yaml",
        &format!(
            "schema_version: 9\ncompanions:\n  acme:\n    command: [{}, --as, acme]\n  gone:\n    command: [/no/such/program]\n",
            exe.display()
        ),
    );
    let ran = project.run(&["companion", "list", "--format", "json"]);
    ran.succeeds();
    let doc = ran.json();
    assert_eq!(reports_as(&doc, "0.name"), "acme");
    assert_eq!(reports_as(&doc, "0.command.0"), exe.display().to_string());
    assert_eq!(reports_as(&doc, "0.command.2"), "acme");
    assert_eq!(reports_as(&doc, "0.exists"), "true");
    assert_eq!(reports_as(&doc, "1.name"), "gone");
    assert_eq!(reports_as(&doc, "1.exists"), "false");
    project
        .run(&["companion", "--format", "text"])
        .succeeds()
        .contains("acme")
        .contains("--as acme")
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
