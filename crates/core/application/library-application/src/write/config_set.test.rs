//! `config set`: one key, schema-checked, its section rewritten.

use ctxloom_ports::Scope;

use crate::fakes::{plain, Setup, CONFIG};

fn world() -> (crate::Library, std::sync::Arc<crate::fakes::Mem>) {
    Setup {
        layers: &[CONFIG],
        ..Setup::default()
    }
    .fixture()
}

#[test]
fn test_a_key_is_set_in_the_projects_layer_and_the_rest_kept() {
    let (library, mem) = world();
    let set = library
        .config_set(&plain(), "editor.command", "vim", false)
        .result
        .unwrap();
    assert_eq!(set.scope, Scope::Project);
    let text = mem.config(Scope::Project).unwrap();
    assert!(text.contains("editor:\n  command: vim"), "{text}");
    assert!(
        text.contains("schema_version: 9"),
        "the rest is kept: {text}"
    );
}

#[test]
fn test_home_sets_the_homes_layer() {
    let (library, mem) = world();
    library
        .config_set(&plain(), "config.premised", "always", true)
        .result
        .unwrap();
    let text = mem.config(Scope::Home).unwrap();
    assert!(text.contains("premised: always"), "{text}");
}

#[test]
fn test_an_unknown_key_or_a_value_the_schema_refuses_writes_nothing() {
    let (library, mem) = world();
    let before = mem.config(Scope::Project);
    let unknown = library.config_set(&plain(), "engines.claude-code.model", "opus", false);
    let error = format!("{:?}", unknown.result.err().unwrap());
    assert!(error.contains("ConfigBreaks"), "{error}");
    let refused = library.config_set(&plain(), "config.premised", "sometimes", false);
    assert!(refused.result.is_err());
    let empty = library.config_set(&plain(), "editor..command", "vim", false);
    assert!(empty.result.is_err());
    assert_eq!(mem.config(Scope::Project), before);
}
