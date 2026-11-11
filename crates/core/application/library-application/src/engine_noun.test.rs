//! `engine.feature` at the application port: the listing, the show, and the
//! sources of what is shown.

use crate::fakes::{plain, Setup};
use crate::{EngineRow, Library};

fn library(config: &'static str, environ: &'static [(&'static str, &'static str)]) -> Library {
    Setup {
        layers: &[config],
        environ,
        ..Setup::default()
    }
    .library()
}

/// The home layer's config beneath the project's.
fn layered(home: &'static str, project: &'static str) -> Library {
    Setup {
        layers: &[home, project],
        ..Setup::default()
    }
    .library()
}

fn show(config: &'static str, engine: &str) -> EngineRow {
    library(config, &[])
        .engine_show(&plain(), engine)
        .result
        .unwrap_or_else(|e| panic!("{e:?}"))
}

// engine.feature: A project that configures nothing lists the engines on their defaults
#[test]
fn test_the_listing_names_the_public_engines_on_their_defaults() {
    let rows = library("schema_version: 9\n", &[])
        .engine_list(&plain())
        .result
        .unwrap();
    let names: Vec<&str> = rows.iter().map(|r| r.name.as_str()).collect();
    for engine in ["claude-code", "opencode"] {
        assert!(names.contains(&engine), "{names:?}");
    }
    let claude = rows.iter().find(|r| r.name == "claude-code").unwrap();
    assert_eq!(claude.command.value, "claude");
    assert_eq!(claude.command.source, "default");
    assert_eq!(claude.runtime.value, "host");
    assert_eq!(claude.runtime.source, "default");
}

// engine.feature: Each shared setting carries the layer that said it
#[test]
fn test_each_shared_setting_carries_the_layer_that_said_it() {
    // the program an engine runs is the home layer's to name (ruling 46)
    let row = layered(
        "schema_version: 9\nengines:\n  claude-code:\n    command: /opt/claude/bin/claude\n",
        "schema_version: 9\nruntime: host\nengines:\n  claude-code:\n    env: [HTTPS_PROXY]\n",
    )
    .engine_show(&plain(), "claude-code")
    .result
    .unwrap();
    assert_eq!(row.command.value, "/opt/claude/bin/claude");
    assert_eq!(row.command.source, "engine");
    assert_eq!(row.session_home.source, "default");
    assert_eq!(row.runtime.source, "project");
    assert_eq!(row.env.value, ["HTTPS_PROXY"]);
}

// engine.feature: An agent's runtime that replaces the engine's is shown as an override
#[test]
fn test_an_agent_s_settings_are_listed_with_sources_and_a_weaker_runtime_says_so() {
    let row = show(
        "schema_version: 9\nengines:\n  claude-code:\n    runtime: container-rootless\nagents:\n  loose:\n    engine: claude-code\n    runtime: host\n    engines:\n      claude-code:\n        model: haiku\n  other:\n    engine: claude-code\n    runtime: container-rootful\n  kept:\n    engine: claude-code\n",
        "claude-code",
    );
    let agent = |name: &str| row.agents.iter().find(|a| a.name == name).unwrap();
    let loose = &agent("loose").settings;
    assert_eq!(loose.model.value.as_deref(), Some("haiku"));
    assert_eq!(loose.model.source, "agent");
    assert_eq!(loose.runtime.value, "host");
    assert_eq!(
        loose.runtime.overrides.as_deref(),
        Some("container-rootless")
    );
    assert_eq!(loose.runtime.change, Some("weakens"));
    assert_eq!(
        agent("other").settings.runtime.change,
        Some("ownership-swap")
    );
    let kept = &agent("kept").settings;
    assert_eq!(kept.runtime.value, "container-rootless");
    assert_eq!(kept.runtime.source, "engine");
    assert_eq!(kept.model.value, None);
    assert_eq!(kept.model.source, "default");
    assert_eq!(kept.permissions.value, "acceptEdits");
}

// Ruling 57: an engine's agents are those bound to it and those with a block
// for it (they run on it under `run --engine`), each with the values its block
// for THIS engine gives (agent), else the engine's defaults (default).
#[test]
fn test_an_engine_lists_the_agents_with_a_block_for_it_with_that_blocks_values() {
    let config = "schema_version: 9\nagents:\n  dev:\n    engine: claude-code\n    engines:\n      claude-code:\n        model: haiku\n      opencode:\n        model: opencode/big-pickle\n        permissions:\n          mode: plan\n  plain:\n    engine: claude-code\n  own:\n    engine: claude-code\n    engines:\n      claude-code:\n        model: sonnet\n";
    let row = show(config, "opencode");
    let names: Vec<&str> = row.agents.iter().map(|a| a.name.as_str()).collect();
    assert_eq!(names, ["dev"]);
    let dev = &row.agents[0].settings;
    assert_eq!(dev.model.value.as_deref(), Some("opencode/big-pickle"));
    assert_eq!(dev.model.source, "agent");
    assert_eq!(dev.effort.value, None);
    assert_eq!(dev.effort.source, "default");
    assert_eq!(dev.permissions.value, "plan");
    assert_eq!(dev.permissions.source, "agent");
    let row = show(config, "claude-code");
    let dev = row.agents.iter().find(|a| a.name == "dev").unwrap();
    assert_eq!(dev.settings.model.value.as_deref(), Some("haiku"));
    assert_eq!(dev.settings.permissions.source, "default");
}

// engine.feature: Showing an engine ctxloom does not drive is refused, naming the engines it does
#[test]
fn test_an_engine_nobody_drives_is_refused_naming_the_engines() {
    let refused = library("schema_version: 9\n", &[])
        .engine_show(&plain(), "gpt-9")
        .result
        .err()
        .map(|e| format!("{e:?}"))
        .unwrap();
    assert!(refused.contains("gpt-9"), "{refused}");
    assert!(refused.contains("claude-code"), "{refused}");
    assert!(refused.contains("opencode"), "{refused}");
}

// engine.feature: auth is a source, and status reads it without revealing it
#[test]
fn test_auth_state_reads_the_source_and_names_the_variable_never_its_value() {
    let config = "schema_version: 9\n";
    let row = library(
        config,
        &[
            ("CLAUDE_CODE_OAUTH_TOKEN", ""),
            ("ANTHROPIC_API_KEY", "sk-fixture"),
        ],
    )
    .engine_show(&plain(), "claude-code")
    .result
    .unwrap();
    assert_eq!(row.auth.source, "default");
    assert_eq!(row.auth.state, "present");
    assert_eq!(row.auth.via.as_deref(), Some("ANTHROPIC_API_KEY"));
    assert!(!format!("{row:?}").contains("sk-fixture"));
    let none = show(
        "schema_version: 9\nengines:\n  claude-code:\n    auth: none\n",
        "claude-code",
    );
    assert_eq!(none.auth.state, "not-needed");
    let missing = library(
        "schema_version: 9\nengines:\n  claude-code:\n    auth:\n      env: [TEAM_TOKEN]\n",
        &[("CLAUDE_CODE_OAUTH_TOKEN", "sk-other")],
    )
    .engine_show(&plain(), "claude-code")
    .result
    .unwrap();
    assert_eq!(missing.auth.source, "engine");
    assert_eq!(missing.auth.state, "missing");
}

// hand-test 2026-11-10: opencode's built-in keys are optional (rulings 69,
// 79), so with none exported it needs none, as `auth status` and doctor say;
// one exported is present by its name; a source the config names is required.
#[test]
fn test_opencode_with_no_key_exported_needs_none() {
    let keyless = show(
        "schema_version: 9
",
        "opencode",
    );
    assert_eq!(keyless.auth.source, "default");
    assert_eq!(keyless.auth.state, "not-needed");
    assert_eq!(keyless.auth.via, None);
    let keyed = library(
        "schema_version: 9
",
        &[("OPENAI_API_KEY", "sk-fixture")],
    )
    .engine_show(&plain(), "opencode")
    .result
    .unwrap();
    assert_eq!(keyed.auth.state, "present");
    assert_eq!(keyed.auth.via.as_deref(), Some("OPENAI_API_KEY"));
    let named = show(
        "schema_version: 9
engines:
  opencode:
    auth:
      env: [TEAM_KEY]
",
        "opencode",
    );
    assert_eq!(named.auth.state, "missing");
}

// A credential file named under `~/` is the user's, found beneath HOME (and
// never a value): the human's own session may share it.
#[test]
fn test_a_login_file_under_home_is_found_beneath_home() {
    let (library, mem) = Setup {
        layers: &[
            "schema_version: 9\nengines:\n  claude-code:\n    auth:\n      env: [ACME_TOKEN]\n      file: ~/.acme/c.json\n",
            "schema_version: 9\n",
        ],
        environ: &[("PATH", "/bin"), ("HOME", "/home/u")],
        ..Setup::default()
    }
    .fixture();
    let missing = library
        .engine_show(&plain(), "claude-code")
        .result
        .unwrap()
        .auth;
    assert_eq!(missing.state, "missing");
    mem.put_user("/home/u/.acme/c.json", b"{}", false);
    let present = library
        .engine_show(&plain(), "claude-code")
        .result
        .unwrap()
        .auth;
    assert_eq!(present.state, "present");
    assert_eq!(present.via.as_deref(), Some("~/.acme/c.json"));
}

// An auth source prints as the word it was written as, or as the names it
// reads (never a value).
#[test]
fn test_an_auth_source_is_shown_as_its_word_or_its_names() {
    let value = |home: &'static str| {
        let row = layered(home, "schema_version: 9\n")
            .engine_show(&plain(), "claude-code")
            .result
            .unwrap();
        serde_json::to_value(&row.auth.value).unwrap()
    };
    assert_eq!(
        value("schema_version: 9\nengines:\n  claude-code:\n    auth: none\n"),
        serde_json::json!("none")
    );
    assert_eq!(
        value("schema_version: 9\nengines:\n  claude-code:\n    auth: login\n"),
        serde_json::json!("login")
    );
    assert_eq!(
        value("schema_version: 9\nengines:\n  claude-code:\n    auth:\n      env: [A]\n      file: ~/f\n"),
        serde_json::json!({"env": ["A"], "file": "~/f"})
    );
}

// Ruling 66: an agent's env is the engine's merged with its own, and each
// name says the layer it came from (engine show, agent show, the dry run).
#[test]
fn test_an_agent_s_env_is_merged_and_each_name_says_its_layer() {
    let row = show(
        "schema_version: 9\nengines:\n  claude-code:\n    env: [HTTPS_PROXY]\nagents:\n  dev:\n    engine: claude-code\n    env: [ALPHA]\n",
        "claude-code",
    );
    let env = &row
        .agents
        .iter()
        .find(|a| a.name == "dev")
        .unwrap()
        .settings
        .env;
    assert_eq!(env.value, ["HTTPS_PROXY", "ALPHA"]);
    assert_eq!(env.source, "agent");
    let keys: Vec<(&str, &str)> = env
        .keys
        .iter()
        .map(|k| (k.name.as_str(), k.source))
        .collect();
    assert_eq!(keys, [("HTTPS_PROXY", "engine"), ("ALPHA", "agent")]);
}
