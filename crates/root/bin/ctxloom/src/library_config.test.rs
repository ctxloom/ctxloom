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
            assert_eq!(reports_as(&doc, "agents.default.engine"), "claude-code");
            assert_eq!(reports_as(&doc, "default_agent"), "default");
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
            assert_eq!(reports_as(&doc, "agents.default.engine"), "claude-code");
            assert_eq!(reports_as(&doc, "default_agent"), "default");
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
    assert!(ran.json().as_object().is_some_and(|o| !o.is_empty()));
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
            assert_eq!(reports_as(&doc, "default.engine"), "claude-code");
            assert_eq!(reports_as(&doc, "default.runtime"), "host");
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
    assert_eq!(reports_as(&doc, "config.content_form"), "original");
    assert_eq!(reports_as(&doc, "config.statusline"), "none");
    assert_eq!(reports_as(&doc, "sync.on_run"), "none");
    assert_eq!(reports_as(&doc, "output.dir"), "docs/sessions");
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
    assert_eq!(reports_as(&doc, "schema_version"), "9");
    assert_eq!(reports_as(&doc, "config.content_form"), "original");
    // The file is not touched: persisting an upgrade is --write-upgrades'.
    let kept = files::read(project.dir().join(".ctxloom/config.yaml")).unwrap();
    assert!(kept.contains("essence_max_chars"), "{kept}");

    project.write(
        ".ctxloom/config.yaml",
        "schema_version: 7\nconfig:\n  essence_max_chars: 4096\n",
    );
    let emptied = project.run(&["config", "show", "--format", "json"]);
    emptied.succeeds();
    assert!(emptied.json().get("config").is_none(), "{}", emptied.stdout);
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
// merge by name, a scalar of the project wins.
#[test]
fn test_layers_merge_home_under_project() {
    let project = Project::from_fixture("cfg/layers9");
    let doc = project
        .run(&["config", "show", "--format", "json"])
        .succeeds()
        .json();
    assert_eq!(reports_as(&doc, "editor.command"), "project-editor");
    assert!(contains_value(&doc, "editor.args", "-h"), "{doc}");
    assert_eq!(reports_as(&doc, "workspace"), "worktree");
    assert_eq!(reports_as(&doc, "agents.proj.engine"), "claude-code");
    assert!(contains_value(&doc, "agents.proj.profiles", "x"), "{doc}");
    assert!(contains_value(&doc, "agents.home.profiles", "h"), "{doc}");
    // Names union; a name's command is the project's where both register it.
    assert_eq!(
        doc["companions"],
        serde_json::json!({
            "acme": {"command": ["acme-bin"]},
            "beta": {"command": ["other-beta"]},
            "gamma": {"command": ["gamma-bin"]},
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
    assert_eq!(reports_as(&from_env.succeeds().json(), "command"), "nano");
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
    assert_eq!(reports_as(&both.succeeds().json(), "command"), "vim");
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
    assert_eq!(reports_as(&ran.succeeds().json(), "command"), "nano");
    assert!(
        !files::exists(project.dir().join(".ctxloom")),
        "reading creates nothing"
    );
}

// virtuous-word (owner ruling 2026-11-06): schema 9 registers a companion with
// the command that runs it. A bare-name list, as Go and schema 8 wrote it, is
// refused with what to do; nothing is migrated.
#[test]
fn test_a_bare_name_companions_list_is_refused_telling_how_to_register_again() {
    for generation in [7, 8] {
        let project = Project::initialized();
        project.write(
            ".ctxloom/config.yaml",
            &format!("schema_version: {generation}\ncompanions: [acme, ltk]\n"),
        );
        let ran = project.run(&["config", "show", "editor", "--format", "text"]);
        assert_eq!(ran.code, 20, "{}", ran.said());
        ran.contains("companions acme, ltk are registered by name alone")
            .contains("ctxloom companion add <name> --command <program>");
        assert!(ran.stdout.is_empty(), "no answer is given: {}", ran.stdout);
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

// config show on the registration: the text form is the YAML Go prints, now a
// map of name to command.
#[test]
fn test_config_show_prints_the_registration_as_a_map_of_commands() {
    let project = Project::from_fixture("cfg/layers9");
    let ran = project.run(&["config", "show", "companions", "--format", "text"]);
    ran.succeeds();
    ran.contains("acme:\n  command:\n    - acme-bin\n")
        .contains("beta:\n  command:\n    - other-beta\n")
        .contains("gamma:\n  command:\n    - gamma-bin\n");
}
