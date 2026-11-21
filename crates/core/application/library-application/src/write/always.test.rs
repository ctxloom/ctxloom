//! session_approvals.feature's allow-always at the application port.

use std::sync::Arc;

use ctxloom_ports::Scope;

use crate::fakes::{plain, Mem, Setup, ENVELOPE};
use crate::Library;

const PROJECT: &str =
    "schema_version: 9\nagents:\n  default:\n    engine: claude-code\ndefault_agent: default\n";

fn world(layers: &'static [&'static str]) -> (Library, Arc<Mem>) {
    Setup {
        bundles: &[("project", &[ENVELOPE])],
        layers,
        ..Setup::default()
    }
    .fixture()
}

const RULE: &str = "Bash(rm -rf build/)";

#[test]
fn without_yes_it_previews_the_effective_document_with_the_rule_and_writes_nothing() {
    let (library, mem) = world(&[PROJECT]);
    let before = mem.config(Scope::Project);
    let shown = library
        .allow_always(&plain(), "default", "claude-code", RULE, false)
        .result
        .unwrap();
    assert_eq!(shown.key, "agents.default.engines.claude-code.permissions");
    assert_eq!(shown.rule, RULE);
    assert_eq!(
        shown.document,
        r#"{"defaultMode":"acceptEdits","allow":["Bash(rm -rf build/)"]}"#
    );
    assert!(!shown.applied);
    assert!(
        shown.path.ends_with(".ctxloom/config.yaml"),
        "{}",
        shown.path
    );
    assert_eq!(mem.config(Scope::Project), before);
}

#[test]
fn with_yes_it_writes_the_rule_over_the_engines_default_document_once() {
    let (library, mem) = world(&[PROJECT]);
    let written = library
        .allow_always(&plain(), "default", "claude-code", RULE, true)
        .result
        .unwrap();
    assert!(written.applied);
    let text = mem.config(Scope::Project).unwrap();
    assert!(text.contains("Bash(rm -rf build/)"), "{text}");
    assert!(text.contains("defaultMode: acceptEdits"), "{text}");
    let again = library
        .allow_always(&plain(), "default", "claude-code", RULE, true)
        .result
        .unwrap();
    assert!(!again.applied, "the rule is there already");
    assert_eq!(mem.config(Scope::Project).unwrap(), text);
}

const HOME: &str = "schema_version: 9\nagents:\n  default:\n    engine: claude-code\n    engines:\n      claude-code:\n        permissions:\n          defaultMode: default\n          allow:\n            - Read(a)\n";

/// A project that defines no agent: the home's `default` is the one a
/// launch uses.
const NO_AGENT: &str = "schema_version: 9\ndefault_agent: default\n";

#[test]
fn it_writes_into_the_homes_definition_when_the_project_defines_no_such_agent() {
    let (library, mem) = world(&[HOME, NO_AGENT]);
    let shown = library
        .allow_always(&plain(), "default", "claude-code", RULE, false)
        .result
        .unwrap();
    assert_eq!(shown.path, library_home_path(&mem), "the preview names it");
    let written = library
        .allow_always(&plain(), "default", "claude-code", RULE, true)
        .result
        .unwrap();
    assert_eq!(
        written.document,
        r#"{"defaultMode":"default","allow":["Read(a)","Bash(rm -rf build/)"]}"#
    );
    let home = mem.config(Scope::Home).unwrap();
    assert!(home.contains("Bash(rm -rf build/)"), "{home}");
    assert!(home.contains("defaultMode: default"), "{home}");
    assert!(home.contains("Read(a)"), "{home}");
    assert!(!mem.config(Scope::Project).unwrap().contains("Bash("));
    assert_eq!(written.path, library_home_path(&mem));
}

/// The home layer's path, as the fake names it.
fn library_home_path(mem: &Mem) -> String {
    use ctxloom_ports::ConfigFiles as _;
    mem.layer_path(Scope::Home)
}

/// The project's own `default`, which holds no block for the engine: it
/// replaces the home's whole, so the home's block is no launch's.
#[test]
fn it_writes_into_the_projects_definition_when_the_project_defines_the_agent() {
    let (library, mem) = world(&[HOME, PROJECT]);
    let shown = library
        .allow_always(&plain(), "default", "claude-code", RULE, false)
        .result
        .unwrap();
    assert!(
        shown.path.ends_with(".ctxloom/config.yaml") && shown.path != library_home_path(&mem),
        "{}",
        shown.path
    );
    let written = library
        .allow_always(&plain(), "default", "claude-code", RULE, true)
        .result
        .unwrap();
    // Not the home's document: the project's agent has none of its own, so
    // it starts from the engine's default, as its launch does.
    assert_eq!(
        written.document,
        r#"{"defaultMode":"acceptEdits","allow":["Bash(rm -rf build/)"]}"#
    );
    let project = mem.config(Scope::Project).unwrap();
    assert!(project.contains("Bash(rm -rf build/)"), "{project}");
    assert!(!project.contains("Read(a)"), "{project}");
    let home = mem.config(Scope::Home).unwrap();
    assert!(!home.contains("Bash("), "{home}");
}

const BOTH: &str = "schema_version: 9\nagents:\n  default:\n    engine: claude-code\n    engines:\n      claude-code:\n        model: haiku\n        permissions:\n          allow:\n            - Edit(b)\ndefault_agent: default\n";

#[test]
fn the_projects_block_is_the_start_when_it_holds_a_document() {
    let (library, mem) = world(&[HOME, BOTH]);
    let written = library
        .allow_always(&plain(), "default", "claude-code", RULE, true)
        .result
        .unwrap();
    assert_eq!(
        written.document,
        r#"{"allow":["Edit(b)","Bash(rm -rf build/)"]}"#
    );
    let project = mem.config(Scope::Project).unwrap();
    assert!(project.contains("Bash(rm -rf build/)"), "{project}");
    assert!(project.contains("model: haiku"), "{project}");
    assert!(!mem.config(Scope::Home).unwrap().contains("Bash("));
}

#[test]
fn an_agent_nobody_defined_is_refused() {
    let (library, _) = world(&[PROJECT]);
    let refused = library
        .allow_always(&plain(), "nobody", "claude-code", RULE, true)
        .result;
    assert!(refused.is_err(), "{refused:?}");
}
