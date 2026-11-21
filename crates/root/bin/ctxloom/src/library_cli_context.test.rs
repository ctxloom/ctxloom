//! The library's read side for `context` (S1), ported from Go's acceptance
//! scenarios as library_cli.test.rs describes; one file per noun (design
//! §8.4), its harness library_cli.test.rs's.

use super::library_cli::*;

/// The parent the daemon names `project` by (OD-40, OQ-2): `projects/<harp>`.
fn parent(project: &Project) -> String {
    let ran = project.run(&["test", "project-name"]);
    ran.succeeds();
    ran.stdout.trim().to_string()
}

// ---------------------------------------------------------------- context assemble

// There is no Go CLI for `context assemble`: Go serves it as the MCP tool
// `assemble_context` over `operations.AssembleContext`, whose behaviour these
// hold the command to (operations/premise_test.go: TestAssembleContext_
// ExplicitAskLoadsPremisedFragment, _UnknownAskIsReportedMissing,
// _PremisedFragmentIsWithheldAndIndexed; operations/context.go
// refuseEmptySelection). The command's own shape (the verb, `-p -f -t`, the
// result's fields) is a question for the owner, in the S1 report.
#[test]
fn test_context_assemble_holds_a_premised_fragment_back_unless_it_is_named() {
    let project = Project::initialized().on_gos_catalog();
    project.bundle("demo");
    project.write(
        ".ctxloom/content/bundles/v2/demo/fragments/golang.md",
        "---\ndescription: when editing Go code\n---\nGO-RULES-BODY\n",
    );
    project.fragment("demo", "testing");
    project.profile("dev", "demo");
    let by_profile = project.run(&["context", "assemble", "-p", "dev", "--format", "json"]);
    by_profile.succeeds();
    let doc = by_profile.json();
    // OD-40 (design §5.9): `AssembledContext`, its profiles named.
    assert_eq!(
        doc["profiles"],
        serde_json::json!([format!("{}/bundles/project/profiles/dev", parent(&project))])
    );
    let context = doc["context"].as_str().unwrap();
    assert!(context.contains("FRAGMENT-BODY-testing"), "{doc}");
    assert!(
        !context.contains("GO-RULES-BODY"),
        "a premised fragment is held back: {doc}"
    );

    let named = project.run(&[
        "context",
        "assemble",
        "-f",
        "demo#fragments/golang",
        "--format",
        "json",
    ]);
    named.succeeds();
    assert!(named.json()["context"]
        .as_str()
        .unwrap()
        .contains("GO-RULES-BODY"));
}

// ---------------------------------------------------------------- config.premised (its helpers are library_cli.test.rs's)

#[test]
fn test_context_assemble_on_demand_ends_with_the_index_of_what_it_held_back() {
    let project = demo_with_a_premise();
    for prefix in [&[][..], &["--config-set", "config.premised=on-demand"][..]] {
        let args = [
            prefix,
            &["context", "assemble", "-p", "dev", "--format", "json"],
        ]
        .concat();
        let ran = project.run(&args);
        ran.succeeds();
        held_back_and_indexed(ran.json()["context"].as_str().unwrap());
    }
}

// The index tells the agent to run `ctxloom fragment show <name>` for each
// name it chose (owner ruling of 2026-11-12): each name it gives is one that
// command shows.
#[test]
fn test_each_name_in_the_index_is_one_fragment_show_shows() {
    let project = demo_with_a_premise();
    let ran = project.run(&["context", "assemble", "-p", "dev", "--format", "json"]);
    ran.succeeds();
    let context = ran.json()["context"].as_str().unwrap().to_string();
    assert!(
        context.contains("`ctxloom fragment show <name>`"),
        "{context}"
    );
    let names: Vec<&str> = context
        .lines()
        .filter_map(|l| l.strip_prefix("- "))
        .filter_map(|l| l.split_once(": ").map(|(name, _)| name))
        .collect();
    assert_eq!(names, ["ctxloom+local:demo#fragments/golang"], "{context}");
    for name in names {
        let shown = project.run(&["fragment", "show", name]);
        shown.succeeds();
        assert!(shown.stdout.contains("GO-RULES-BODY"), "{}", shown.said());
    }
}

#[test]
fn test_context_assemble_with_premised_always_loads_every_fragment_and_no_index() {
    let project = demo_with_a_premise();
    let ran = project.run(&[
        "--config-set",
        "config.premised=always",
        "context",
        "assemble",
        "-p",
        "dev",
        "--format",
        "json",
    ]);
    ran.succeeds();
    loaded_and_unindexed(ran.json()["context"].as_str().unwrap());
}

#[test]
fn test_a_premised_word_nobody_knows_is_refused() {
    let project = demo_with_a_premise();
    let ran = project.run(&[
        "--config-set",
        "config.premised=sometimes",
        "context",
        "assemble",
        "-p",
        "dev",
    ]);
    assert_eq!(ran.code, 20, "{}", ran.said());
    ran.contains("config.premised");
}

// premise_test.go: an unknown ask is reported missing, not silently empty; an
// ask that loads nothing at all is refused (context.go refuseEmptySelection).
#[test]
fn test_context_assemble_reports_a_missing_ask_and_refuses_one_that_loads_nothing() {
    let project = demo_with_testing();
    let partly = project.run(&[
        "context",
        "assemble",
        "-f",
        "demo#fragments/testing",
        "-f",
        "demo#fragments/nope",
        "--format",
        "json",
    ]);
    partly.succeeds();
    let doc = partly.json();
    assert_eq!(
        doc["missing_fragments"],
        serde_json::json!(["ctxloom+local:demo#fragments/nope"])
    );
    // OD-40: each fragment it used, named; one asked for and not found keeps
    // the text it was asked by (OQ-16).
    let testing = format!("{}/bundles/demo/fragments/testing", parent(&project));
    assert!(
        doc["fragments"]
            .as_array()
            .unwrap()
            .iter()
            .any(|f| f["fragment"] == testing.as_str()),
        "{doc}"
    );
    assert!(doc.get("fragments_loaded").is_none(), "{doc}");
    project
        .run(&["context", "assemble", "-f", "nosuch"])
        .fails()
        .contains("requested fragments not found: nosuch");
    project
        .run(&["context", "assemble", "-t", "no-such-tag-anywhere"])
        .fails()
        .contains("no fragment matches tag(s): no-such-tag-anywhere");
}
