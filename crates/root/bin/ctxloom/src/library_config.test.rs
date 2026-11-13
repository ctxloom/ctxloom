//! `config.feature` (the read half: `show`, the bare noun, the sections),
//! and the strict decode of a config: schema 8 and older, an unknown key
//! named, a layer that cannot be parsed refused by name.
//!
//! WITHHELD, and why. `config.feature` has two scenarios about "a config value
//! of the wrong type" (`session_reap_age: 5`, "expected string", refused; and
//! `--degraded`, warned). Go reads YAML's own types, so `5` is a number where
//! a string was expected. D15 puts every config through `angzarr-yaml`, which
//! keeps each scalar as its text (it cannot tell `5` from `"5"`), so this
//! build cannot raise that refusal. The two scenarios are not ported; the
//! question is in the S1 report.

use super::library::{contains_value, reports_as, Project};
use crate::tests::files;

/// A project whose default agent is bound to an engine.
const AGENT_CONFIG: &str = "schema_version: 9\nagents:\n  default:\n    engine: claude-code\n    profiles:\n      - default\n    runtime: host\ndefault_agent: default\n";

const FORMATS: [&[&str]; 3] = [&[], &["--format", "json"], &["--format", "text"]];

fn with_flags(project: &Project, base: &[&str], flags: &[&str]) -> super::Ran {
    let mut args = base.to_vec();
    args.extend_from_slice(flags);
    project.run(&args)
}

fn is_text(flags: &[&str]) -> bool {
    flags.contains(&"text")
}

// config.feature: Scenario Outline: Show renders the whole configuration document
#[test]
fn test_show_renders_the_whole_configuration_document() {
    for flags in FORMATS {
        let project = Project::initialized();
        project.write(".ctxloom/config.yaml", AGENT_CONFIG);
        let ran = with_flags(&project, &["config", "show"], flags);
        ran.succeeds().contains("claude-code");
        if is_text(flags) {
            ran.contains("agents:")
                .contains("engine: claude-code")
                .contains("default_agent: default");
        } else {
            let doc = ran.json();
            assert_eq!(
                reports_as(&doc, "document.agents.default.engine"),
                "claude-code"
            );
            assert_eq!(reports_as(&doc, "document.default_agent"), "default");
        }
    }
}

// config.feature: Scenario Outline: Bare config shows the document instead of
// printing help
#[test]
fn test_bare_config_shows_the_document_instead_of_printing_help() {
    for flags in FORMATS {
        let project = Project::initialized();
        project.write(".ctxloom/config.yaml", AGENT_CONFIG);
        let ran = with_flags(&project, &["config"], flags);
        ran.succeeds().lacks("Available Commands:");
        if is_text(flags) {
            ran.contains("agents:").contains("default_agent:");
        } else {
            let doc = ran.json();
            assert_eq!(
                reports_as(&doc, "document.agents.default.engine"),
                "claude-code"
            );
            assert_eq!(reports_as(&doc, "document.default_agent"), "default");
        }
    }
}

// config.feature: Scenario: The machine-readable rendering carries the
// document, not an empty object
#[test]
fn test_the_machine_readable_rendering_carries_the_document_not_an_empty_object() {
    let project = Project::initialized();
    project.write(".ctxloom/config.yaml", AGENT_CONFIG);
    let ran = project.run(&["--format", "json", "config", "show"]);
    ran.succeeds().contains("claude-code");
    // OD-40: the `Config` singleton, its document under `document`.
    assert!(ran.json()["document"]
        .as_object()
        .is_some_and(|o| !o.is_empty()));
    let name = ran.json()["name"].as_str().unwrap_or_default().to_string();
    assert!(
        name.starts_with("projects/") && name.ends_with("/config"),
        "{}",
        ran.stdout
    );
}

// config.feature: Scenario Outline: The agents section carries the engine
// bindings and nothing above it (the retired registry section's scenario, ported: the
// engine an agent names is in `agents`, the registry is gone)
#[test]
fn test_the_agents_section_carries_the_engine_bindings_and_nothing_above_it() {
    for flags in FORMATS {
        let project = Project::initialized();
        project.write(".ctxloom/config.yaml", AGENT_CONFIG);
        let ran = with_flags(&project, &["config", "show", "agents"], flags);
        ran.succeeds()
            .contains("claude-code")
            .lacks("editor:")
            .lacks("agents:");
        if is_text(flags) {
            ran.contains("engine: claude-code")
                .contains("runtime: host");
        } else {
            let doc = ran.json();
            assert_eq!(reports_as(&doc, "document.default.engine"), "claude-code");
            assert_eq!(reports_as(&doc, "document.default.runtime"), "host");
        }
    }
}

// config.feature: Scenario: A config carrying an inline profiles block is
// refused, naming the key
#[test]
fn test_a_config_carrying_an_inline_profiles_block_is_refused_naming_the_key() {
    let project = Project::initialized();
    project.bundle("demo");
    project.write(
        ".ctxloom/config.yaml",
        "schema_version: 9\nprofiles:\n  definitions:\n    dev:\n      bundles:\n        - demo\n",
    );
    project
        .run(&["config", "show", "agents"])
        .fails()
        .contains("unknown key `profiles`");
}

// config.feature: Scenario: An unknown section is refused and the real ones
// are named
#[test]
fn test_an_unknown_section_is_refused_and_the_real_ones_are_named() {
    let project = Project::initialized();
    project
        .run(&["config", "show", "nonsense"])
        .fails()
        .contains("nonsense")
        .contains("config")
        .contains("engines");
}

// config.feature: Scenario: A misspelled subcommand fails instead of printing help
#[test]
fn test_a_misspelled_subcommand_fails_instead_of_printing_help() {
    let project = Project::initialized();
    project
        .run(&["config", "shwo"])
        .fails()
        .contains("unknown command")
        .contains("shwo");
}

// fault_tolerance.feature (the config half): a key the schema does not
// describe is refused naming it, with the keys there are; --degraded warns
// and carries on without it.
#[test]
fn test_an_unknown_key_is_refused_naming_it_and_degraded_carries_on() {
    let project = Project::initialized();
    project.write(".ctxloom/config.yaml", "schema_version: 9\nzzz: 1\n");
    let refused = project.run(&["bundle", "list"]);
    refused
        .fails()
        .contains("unknown key `zzz`")
        .contains("known keys at the top level: agents, companions, config");
    project
        .run(&["--degraded", "bundle", "list"])
        .succeeds()
        .contains("warning");
}

// Owner rulings 59, 60, 106 and 108 (hard migrate): a config still holding a
// retired boolean, or the old top-level `output_dir`, is refused (exit 20)
// naming the key, as any unknown key is; its replacement reads.
#[test]
fn test_the_retired_config_booleans_are_refused_and_their_words_read() {
    let project = Project::initialized();
    for (doc, key) in [
        ("config:\n  use_distilled: true\n", "config.use_distilled"),
        (
            "config:\n  silence_unsupported: true\n",
            "config.silence_unsupported",
        ),
        ("config:\n  statusline: false\n", "config.statusline"),
        ("sync:\n  auto_sync: true\n", "sync.auto_sync"),
        // Owner ruling 134: the neutral posture is gone whole, so its
        // section is the unknown key.
        ("permissions:\n  network: none\n", "permissions"),
        ("output_dir: /var/out\n", "output_dir"),
    ] {
        project.write(".ctxloom/config.yaml", &format!("schema_version: 9\n{doc}"));
        let refused = project.run(&["config", "show", "--format", "json"]);
        assert_eq!(refused.code, 20, "{key}: {}", refused.said());
        assert!(
            refused.stderr.contains(&format!("`{key}`")),
            "{key}: {}",
            refused.said()
        );
    }
    project.write(
        ".ctxloom/config.yaml",
        "schema_version: 9\nconfig:\n  content_form: original\n  statusline: none\nsync:\n  on_run: none\noutput:\n  dir: docs/sessions\n",
    );
    let doc = project
        .run(&["config", "show", "--format", "json"])
        .succeeds()
        .json();
    assert_eq!(reports_as(&doc, "document.config.content_form"), "original");
    assert_eq!(reports_as(&doc, "document.config.statusline"), "none");
    assert_eq!(reports_as(&doc, "document.sync.on_run"), "none");
    assert_eq!(reports_as(&doc, "document.output.dir"), "docs/sessions");
}

// hand-test 2026-11-10: a `--config-set` saying what a config file may not
// (a retired key, a word the schema does not allow) is refused as the file
// is, with the same diagnostic and exit 20, naming the flag; it was a warning
// and the command ran on. `--degraded` still carries on, as for a file.
#[test]
fn test_a_config_set_the_schema_refuses_is_refused_as_the_file_is() {
    let project = Project::initialized();
    for (set, code, key) in [
        (
            "sync.auto_sync=true",
            "library-application-config-unknown-key",
            "unknown key `sync.auto_sync` in --config-set",
        ),
        (
            "sync.on_run=true",
            "library-application-config-breaks-schema",
            "--config-set: `sync.on_run` breaks the schema",
        ),
    ] {
        let ran = project.run(&["--config-set", set, "config", "show", "--format", "json"]);
        assert_eq!(ran.code, 20, "{set}: {}", ran.said());
        assert!(
            ran.stdout.is_empty(),
            "{set}: no answer is given: {}",
            ran.stdout
        );
        assert!(ran.stderr.contains(code), "{set}: {}", ran.said());
        assert!(ran.stderr.contains(key), "{set}: {}", ran.said());
        project
            .run(&[
                "--degraded",
                "--config-set",
                set,
                "config",
                "show",
                "--format",
                "json",
            ])
            .succeeds();
    }
}

// internal/migrations/config/internal/v8/step_test.go: TestStep_DropsEssenceMaxChars
// and TestStep_EmptiedSectionLeavesAndAbsentKeyIsUntouched, through the
// binary: a schema 7 config loads, and shows as 9 without the key.
#[test]
fn test_a_schema_7_config_migrates_in_memory_dropping_essence_max_chars() {
    let project = Project::initialized();
    project.write(
        ".ctxloom/config.yaml",
        "schema_version: 7\nconfig:\n  content_form: original\n  essence_max_chars: 4096\n",
    );
    let ran = project.run(&["config", "show", "--format", "json"]);
    ran.succeeds().lacks("essence_max_chars");
    let doc = ran.json();
    assert_eq!(reports_as(&doc, "document.schema_version"), "9");
    assert_eq!(reports_as(&doc, "document.config.content_form"), "original");
    // The file is not touched: persisting an upgrade is --write-upgrades'.
    let kept = files::read(project.dir().join(".ctxloom/config.yaml")).unwrap();
    assert!(kept.contains("essence_max_chars"), "{kept}");

    project.write(
        ".ctxloom/config.yaml",
        "schema_version: 7\nconfig:\n  essence_max_chars: 4096\n",
    );
    let emptied = project.run(&["config", "show", "--format", "json"]);
    emptied.succeeds();
    assert!(
        emptied.json()["document"].get("config").is_none(),
        "{}",
        emptied.stdout
    );
}

// config_version_refusal_test.go (the stamp): owner override of Go, D15,
// 2026-11-03. Go warned about a layer of another generation and read it as
// written; a config newer than this binary reads, older than it migrates,
// unstamped or stamped with no number is REFUSED, naming the file and the
// generation, with exit 20 and a fix.
#[test]
fn test_a_config_of_another_generation_is_refused() {
    for (config, said) in [
        (
            "schema_version: 10\neditor:\n  command: vim\n",
            "schema_version 10: written by a newer format than this binary reads",
        ),
        (
            "schema_version: 6\neditor:\n  command: vim\n",
            "schema_version 6: older than the oldest format this binary can migrate (the oldest is 7)",
        ),
        (
            "editor:\n  command: vim\n",
            "schema_version 0: older than the oldest format this binary can migrate",
        ),
        (
            "schema_version: abc\n",
            "format version cannot be read: schema_version is not an integer",
        ),
    ] {
        let project = Project::initialized();
        project.write(".ctxloom/config.yaml", config);
        let ran = project.run(&["config", "show", "editor", "--format", "json"]);
        assert_eq!(ran.code, 20, "{config:?}: {}", ran.said());
        ran.contains(said);
        assert!(ran.stdout.is_empty(), "no answer is given: {}", ran.stdout);
        project
            .run(&["config", "show", "--format", "text"])
            .contains("  fix: ");
    }
}

// cli/config.feature, configload/merge.go: layering is home < project, an
// agent binding replaces the home's of the same name wholesale, companions
// merge by name (a name's state the project's where both say one), a scalar
// of the project wins.
#[test]
fn test_layers_merge_home_under_project() {
    let project = Project::from_fixture("cfg/layers9");
    let doc = project
        .run(&["config", "show", "--format", "json"])
        .succeeds()
        .json();
    assert_eq!(
        reports_as(&doc, "document.editor.command"),
        "project-editor"
    );
    assert!(contains_value(&doc, "document.editor.args", "-h"), "{doc}");
    assert_eq!(reports_as(&doc, "document.workspace"), "worktree");
    assert_eq!(
        reports_as(&doc, "document.agents.proj.engine"),
        "claude-code"
    );
    assert!(
        contains_value(&doc, "document.agents.proj.profiles", "x"),
        "{doc}"
    );
    assert!(
        contains_value(&doc, "document.agents.home.profiles", "h"),
        "{doc}"
    );
    // Names union; a name's state is the project's where both say one.
    assert_eq!(
        doc["document"]["companions"],
        serde_json::json!({
            "acme": {},
            "beta": {"state": "enabled"},
            "gamma": {},
        })
    );
}

// confload/overlay.go: --config-set <path>=<value> beats the files, and
// CTXLOOM_CONFIG_<PATH> beats them too; the flag beats the variable.
#[test]
fn test_overrides_beat_the_files_and_the_flag_beats_the_variable() {
    let project = Project::initialized();
    let env = [("CTXLOOM_CONFIG_EDITOR_COMMAND", "nano")];
    let from_env = project.run_with(&env, &["config", "show", "editor", "--format", "json"]);
    assert_eq!(
        reports_as(&from_env.succeeds().json(), "document.command"),
        "nano"
    );
    let both = project.run_with(
        &env,
        &[
            "--config-set",
            "editor.command=vim",
            "config",
            "show",
            "editor",
            "--format",
            "json",
        ],
    );
    assert_eq!(
        reports_as(&both.succeeds().json(), "document.command"),
        "vim"
    );
    // An entry with no "=" is warned about and the rest is read.
    project
        .run(&[
            "--config-set",
            "noequals",
            "config",
            "show",
            "--format",
            "json",
        ])
        .succeeds()
        .contains("expected <path>=<value>");
}

// configload/appdir.go + docs/environment.md: a project with no .ctxloom of
// its own anywhere up the path reads the home's config alone, and nothing is
// created.
#[test]
fn test_without_a_project_the_home_config_is_the_whole_configuration() {
    let project = Project::bare();
    project.write_home(
        ".ctxloom/config.yaml",
        "schema_version: 9\neditor:\n  command: nano\n",
    );
    let ran = project.run_in(
        project.dir(),
        &["config", "show", "editor", "--format", "json"],
    );
    assert_eq!(
        reports_as(&ran.succeeds().json(), "document.command"),
        "nano"
    );
    assert!(
        !files::exists(project.dir().join(".ctxloom")),
        "reading creates nothing"
    );
}

// OD-10 (owner, 2026-11-13): a companion is registered by name. Go's
// bare-name list (schema 7 or 8) is read as the names it registers, each
// enabled; the file is not rewritten.
#[test]
fn test_a_bare_name_companions_list_is_read_as_the_names_it_registers() {
    for generation in [7, 8] {
        let project = Project::initialized();
        project.write(
            ".ctxloom/config.yaml",
            &format!("schema_version: {generation}\ncompanions: [acme, ltk]\n"),
        );
        let ran = project.run(&["companion", "list", "--format", "json"]);
        let doc = ran.succeeds().json();
        assert!(reports_as(&doc, "companions.0.name").ends_with("/companions/acme"));
        assert_eq!(reports_as(&doc, "companions.1.bin"), "ltk");
        let kept = files::read(project.dir().join(".ctxloom/config.yaml")).unwrap();
        assert!(kept.contains("companions: [acme, ltk]"), "not rewritten");
    }
    // A config registering none, or the empty list, loads as before.
    let project = Project::initialized();
    project.write(
        ".ctxloom/config.yaml",
        "schema_version: 8\ncompanions: []\neditor:\n  command: vim\n",
    );
    project
        .run(&["config", "show", "editor", "--format", "json"])
        .succeeds();
}

// A command in a registration is an unknown key (OD-10, a hard migrate).
#[test]
fn test_a_registration_holding_a_command_is_refused_as_an_unknown_key() {
    let project = Project::initialized();
    project.write_home(
        ".ctxloom/config.yaml",
        "schema_version: 9\ncompanions:\n  acme:\n    command: [acme-bin]\n",
    );
    let ran = project.run(&["companion", "list", "--format", "text"]);
    assert_eq!(ran.code, 20, "{}", ran.said());
    ran.contains("unknown key `companions.acme.command`");
}

// config show on the registration: the text form is the YAML Go prints, a
// map of name to its state.
#[test]
fn test_config_show_prints_the_registration_as_a_map_of_names() {
    let project = Project::from_fixture("cfg/layers9");
    let ran = project.run(&["config", "show", "companions", "--format", "text"]);
    ran.succeeds();
    ran.contains("acme: {}\n")
        .contains("beta:\n  state: enabled\n")
        .contains("gamma: {}\n");
}

// OD-40 (OQ-3): outside a project a json answer is named under the home,
// `users/me/config`, with no project to resolve, so no identity is minted
// and nothing is written where the command ran.
#[test]
fn test_outside_a_project_a_json_read_is_named_under_the_home_and_writes_nothing() {
    let project = Project::bare();
    project.write_home(".ctxloom/config.yaml", "schema_version: 9\n");
    let ran = project.run_in(project.dir(), &["config", "show", "--format", "json"]);
    assert_eq!(
        reports_as(&ran.succeeds().json(), "name"),
        "users/me/config"
    );
    assert!(project.tree("").is_empty(), "{:?}", project.tree("").keys());
}

/// The project's name (`projects/<harp>`) as a json read from `dir` gives it.
fn named_from(project: &Project, dir: &std::path::Path) -> String {
    let ran = project.run_in(dir, &["config", "show", "--format", "json"]);
    let name = reports_as(&ran.succeeds().json(), "name");
    name.strip_suffix("/config")
        .unwrap_or_else(|| panic!("{name} is no config's name"))
        .to_string()
}

// OD-40 (OQ-2): inside a project, a json read from a subdirectory names the
// project of the nearest `.ctxloom` up from it, the one its root names, and
// writes nothing: no identity of the subdirectory's own.
#[test]
fn test_a_json_read_from_a_subdirectory_names_the_nearest_project_and_writes_nothing() {
    let project = Project::initialized();
    let sub = project.dir().join("src").join("deep");
    files::mkdirs(&sub);
    let at_root = named_from(&project, project.dir());
    assert!(at_root.starts_with("projects/"), "{at_root}");
    let before = project.tree("");
    assert_eq!(named_from(&project, &sub), at_root);
    assert_eq!(project.tree(""), before, "a read wrote in the project");
    assert!(
        !files::exists(sub.join(".ctxloom")),
        "the subdirectory got an identity"
    );
}

// OD-40: a project no run has named yet is named at its root, from wherever
// in it the first json read is made: the identity is the project's
// (`.ctxloom/project-id` beside its config), never a subdirectory's, so the
// root then names the same project.
#[test]
fn test_a_first_json_read_from_a_subdirectory_names_the_project_at_its_root() {
    let project = Project::initialized();
    let sub = project.dir().join("src");
    files::mkdirs(&sub);
    let from_sub = named_from(&project, &sub);
    assert!(
        !files::exists(sub.join(".ctxloom")),
        "the subdirectory got an identity"
    );
    assert!(
        project.exists(".ctxloom/project-id"),
        "{:?}",
        project.tree("").keys()
    );
    assert_eq!(named_from(&project, project.dir()), from_sub);
}
