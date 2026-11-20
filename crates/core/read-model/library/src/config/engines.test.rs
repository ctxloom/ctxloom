//! RED: the engines schema's resolution, isolation guard and auth source
//! (owner rulings 40-44; `engines-schema.proposal.md`). Every test here that
//! is not about a default fails against the stubs by assertion.

use super::*;

fn cfg(text: &str) -> Value {
    angzarr_yaml::parse(text).unwrap_or_else(|e| panic!("{e}"))
}

fn of(text: &str, agent: &str) -> Effective {
    effective(&cfg(text), agent).unwrap_or_else(|e| panic!("{e:?}"))
}

fn sourced<T>(value: T, source: Source) -> Sourced<T> {
    Sourced { value, source }
}

fn text(value: &str, source: Source) -> Sourced<String> {
    sourced(value.to_string(), source)
}

/// ctxloom's default document for claude, said by nobody.
fn claude_default() -> Option<Sourced<Value>> {
    Some(sourced(cfg("defaultMode: acceptEdits\n"), Source::Default))
}

fn names(list: &[&str]) -> Vec<String> {
    list.iter().map(ToString::to_string).collect()
}

// ruling 42: unset model and effort pass no flag; claude's permissions are
// ctxloom's default document (rulings 133-139 keep acceptEdits); nothing else
// is invented
#[test]
fn test_an_agent_that_says_nothing_runs_on_the_built_in_defaults() {
    let got = of("agents:\n  dev:\n    profiles: [p]\n", "dev");
    assert_eq!(got.engine, text("claude-code", Source::Default));
    assert_eq!(got.model, None);
    assert_eq!(got.effort, None);
    assert_eq!(got.permissions, claude_default());
    assert_eq!(got.runtime, text("host", Source::Default));
    assert_eq!(got.runtime_override, Override::Inherited);
    assert_eq!(got.env, sourced(Vec::new(), Source::Default));
    assert_eq!(got.command, text("claude", Source::Default));
    assert_eq!(got.session_home, text("session", Source::Default));
    assert_eq!(
        got.auth,
        sourced(AuthSource::builtin("claude-code"), Source::Default)
    );
}

// ruling 40, in ruling 57's shape: model, effort and permissions are the
// agent's, in its block for the engine it runs on; the permissions are the
// engine's own document, taken whole and unread (rulings 135, 137): a key
// ctxloom does not know, the retired `mode` among them, is kept as written
#[test]
fn test_an_agent_s_model_effort_and_permissions_are_its_own() {
    let got = of(
        "agents:\n  dev:\n    engine: claude-code\n    engines:\n      claude-code:\n        model: haiku\n        effort: low\n        permissions:\n          mode: plan\n          allow: [Read, Grep]\n          deny:\n            - WebFetch\n",
        "dev",
    );
    assert_eq!(got.engine, text("claude-code", Source::Agent));
    assert_eq!(got.model, Some(text("haiku", Source::Agent)));
    assert_eq!(got.effort, Some(text("low", Source::Agent)));
    assert_eq!(
        got.permissions,
        Some(sourced(
            cfg("mode: plan\nallow: [Read, Grep]\ndeny:\n  - WebFetch\n"),
            Source::Agent
        ))
    );
}

// Ruling 57, hard migrate: the flat keys and the engine-keyed permissions
// map are not read (the schema refuses them; the resolver must not honour
// them either).
#[test]
fn test_an_agent_s_flat_model_effort_and_permissions_map_are_not_read() {
    let got = of(
        "agents:\n  dev:\n    engine: claude-code\n    model: haiku\n    effort: low\n    permissions:\n      claude-code:\n        mode: plan\n",
        "dev",
    );
    assert_eq!(got.model, None);
    assert_eq!(got.effort, None);
    assert_eq!(got.permissions, claude_default());
}

// Ruling 57: only the block of the engine run on is read; another engine's
// block says nothing of this one.
#[test]
fn test_only_the_block_of_the_engine_run_on_is_read() {
    let got = of(
        "agents:\n  dev:\n    engine: claude-code\n    engines:\n      opencode:\n        model: opencode/big-pickle\n",
        "dev",
    );
    assert_eq!(got.model, None);
    assert_eq!(got.permissions, claude_default());
}

/// An agent bound to claude-code, with a block for each engine and a runtime
/// and env of its own.
const TWO_BLOCKS: &str = "engines:\n  opencode:\n    runtime: container-rootless\n    env: [OPENCODE_ONLY]\nagents:\n  dev:\n    engine: claude-code\n    runtime: host\n    env: [ALPHA]\n    engines:\n      claude-code:\n        model: haiku\n        effort: low\n        permissions:\n          mode: plan\n      opencode:\n        model: opencode/big-pickle\n";

// Ruling 57 (amends 54): `run --engine X` runs the agent on X with its block
// for X; the agent's runtime and env (engine-neutral) are kept; X's own
// command, home and auth drive it.
#[test]
fn test_an_agent_on_another_engine_takes_its_block_for_that_engine() {
    let config = cfg(TWO_BLOCKS);
    let got = effective_on(&config, "dev", "opencode").unwrap_or_else(|e| panic!("{e:?}"));
    assert_eq!(got.engine.value, "opencode");
    assert_eq!(got.model, Some(text("opencode/big-pickle", Source::Agent)));
    assert_eq!(got.effort, None);
    assert_eq!(got.permissions, None);
    assert_eq!(got.runtime, text("host", Source::Agent));
    // Ruling 66: the engine's env applies to any run on it, and the agent's
    // own names are added to it (agent > engine, key by key).
    assert_eq!(
        got.env,
        sourced(names(&["OPENCODE_ONLY", "ALPHA"]), Source::Agent)
    );
    assert_eq!(
        got.env_keys,
        vec![
            text("OPENCODE_ONLY", Source::Engine),
            text("ALPHA", Source::Agent)
        ]
    );
    assert_eq!(got.command, text("opencode", Source::Default));
    assert_eq!(
        got.auth,
        sourced(AuthSource::builtin("opencode"), Source::Default)
    );
}

// Ruling 57: with no block for X, X's defaults apply: no model, no effort,
// X's own default permissions document.
#[test]
fn test_an_agent_with_no_block_for_the_engine_runs_on_its_defaults() {
    let config = cfg(
        "agents:\n  dev:\n    engine: opencode\n    engines:\n      opencode:\n        model: m\n",
    );
    let got = effective_on(&config, "dev", "claude-code").unwrap_or_else(|e| panic!("{e:?}"));
    assert_eq!(got.engine.value, "claude-code");
    assert_eq!(got.model, None);
    assert_eq!(got.effort, None);
    assert_eq!(got.permissions, claude_default());
}

// Its own engine, asked for by name, is the same as asked for by none.
#[test]
fn test_an_agent_on_its_own_engine_by_name_is_its_effective_settings() {
    let config = cfg(TWO_BLOCKS);
    assert_eq!(
        effective_on(&config, "dev", "claude-code").unwrap(),
        effective(&config, "dev").unwrap()
    );
}

#[test]
fn test_an_agent_on_an_engine_nobody_drives_is_refused() {
    assert!(matches!(
        effective_on(&cfg(TWO_BLOCKS), "dev", "gpt-9"),
        Err(EngineError::UnknownEngine { .. })
    ));
}

// ruling 40: an engines block has no model: a model there is not read (the
// schema refuses the key; the resolver must not honour it either)
#[test]
fn test_the_engine_block_never_supplies_a_model_effort_or_permissions() {
    let got = of(
        "engines:\n  claude-code:\n    model: opus\n    effort: high\nagents:\n  dev:\n    engine: claude-code\n",
        "dev",
    );
    assert_eq!(got.model, None);
    assert_eq!(got.effort, None);
}

// ruling 44: command, session_home, auth are the engine's, shared by its agents
#[test]
fn test_the_engine_block_supplies_command_session_home_and_auth_to_its_agents() {
    let doc = "engines:\n  claude-code:\n    command: /opt/claude/bin/claude\n    session_home: host\n    auth:\n      env: [MY_CLAUDE_TOKEN]\nagents:\n  a:\n    engine: claude-code\n  b:\n    engine: claude-code\n";
    for agent in ["a", "b"] {
        let got = of(doc, agent);
        assert_eq!(got.command, text("/opt/claude/bin/claude", Source::Engine));
        assert_eq!(got.session_home, text("host", Source::Engine));
        assert_eq!(
            got.auth,
            sourced(
                AuthSource::Credential {
                    env: names(&["MY_CLAUDE_TOKEN"]),
                    file: None
                },
                Source::Engine
            )
        );
    }
}

// the engine block of one engine is not read for an agent on another
#[test]
fn test_an_engine_s_block_does_not_reach_an_agent_on_another_engine() {
    let got = of(
        "engines:\n  claude-code:\n    runtime: container-rootless\n    command: other\nagents:\n  o:\n    engine: opencode\n",
        "o",
    );
    assert_eq!(got.engine, text("opencode", Source::Agent));
    assert_eq!(got.runtime, text("host", Source::Default));
    assert_eq!(got.command, text("opencode", Source::Default));
}

// precedence for runtime: agent > engine > project `runtime:` > host
#[test]
fn test_runtime_is_the_agent_s_else_the_engine_s_else_the_project_s_else_host() {
    let agents = "agents:\n  dev:\n    engine: claude-code\n";
    let with_agent = "agents:\n  dev:\n    engine: claude-code\n    runtime: container-rootless\n";
    let engine = "engines:\n  claude-code:\n    runtime: container-rootful\n";
    let project = "runtime: container-rootless\n";

    assert_eq!(of(agents, "dev").runtime, text("host", Source::Default));
    assert_eq!(
        of(&format!("{project}{agents}"), "dev").runtime,
        text("container-rootless", Source::Project)
    );
    assert_eq!(
        of(&format!("{project}{engine}{agents}"), "dev").runtime,
        text("container-rootful", Source::Engine)
    );
    assert_eq!(
        of(&format!("{project}{engine}{with_agent}"), "dev").runtime,
        text("container-rootless", Source::Agent)
    );
}

// the isolation guard: an agent's override is exact, never merged, never a
// fallback, and says what it did to the default it replaced
#[test]
fn test_an_agent_override_of_runtime_is_exact_and_says_what_it_replaced() {
    let engine = "engines:\n  claude-code:\n    runtime: container-rootless\n";
    let doc = |agent_runtime: &str| {
        format!("{engine}agents:\n  dev:\n    engine: claude-code\n    runtime: {agent_runtime}\n")
    };
    let replaced = text("container-rootless", Source::Engine);

    let got = of(&doc("host"), "dev");
    assert_eq!(got.runtime, text("host", Source::Agent));
    assert_eq!(
        got.runtime_override,
        Override::Replaced {
            replaced: replaced.clone(),
            change: Change::Weakens
        }
    );

    let got = of(&doc("container-rootful"), "dev");
    assert_eq!(got.runtime, text("container-rootful", Source::Agent));
    assert_eq!(
        got.runtime_override,
        Override::Replaced {
            replaced,
            change: Change::OwnershipSwap
        }
    );

    let got = of(&doc("container-rootless"), "dev");
    assert_eq!(got.runtime_override, Override::Restated);
}

#[test]
fn test_an_agent_silent_about_runtime_inherits_the_engine_default_not_the_host() {
    let got = of(
        "engines:\n  claude-code:\n    runtime: container-rootless\nagents:\n  dev:\n    engine: claude-code\n",
        "dev",
    );
    assert_eq!(got.runtime, text("container-rootless", Source::Engine));
    assert_eq!(got.runtime_override, Override::Inherited);
}

#[test]
fn test_moving_between_runtimes_is_weaker_stronger_or_an_ownership_swap() {
    assert_eq!(
        runtime_change("container-rootless", "host"),
        Some(Change::Weakens)
    );
    assert_eq!(
        runtime_change("container-rootful", "host"),
        Some(Change::Weakens)
    );
    assert_eq!(
        runtime_change("host", "container-rootless"),
        Some(Change::Strengthens)
    );
    assert_eq!(
        runtime_change("host", "container-rootful"),
        Some(Change::Strengthens)
    );
    // never interchangeable, in either direction
    assert_eq!(
        runtime_change("container-rootless", "container-rootful"),
        Some(Change::OwnershipSwap)
    );
    assert_eq!(
        runtime_change("container-rootful", "container-rootless"),
        Some(Change::OwnershipSwap)
    );
    assert_eq!(runtime_change("host", "host"), None);
    assert_eq!(
        runtime_change("container-rootless", "container-rootless"),
        None
    );
}

// Ruling 66: env MERGES, agent > engine > default, key by key: the engine's
// names apply to every agent on it, the agent's own are added, and a name
// both say is the agent's. The list's source is the highest layer that said
// any of it; each name keeps its own.
#[test]
fn test_env_is_the_engine_s_merged_with_the_agent_s_key_by_key() {
    let engine = "engines:\n  claude-code:\n    env: [HTTPS_PROXY, NPM_TOKEN]\n";
    let got = of(
        &format!("{engine}agents:\n  dev:\n    engine: claude-code\n"),
        "dev",
    );
    assert_eq!(
        got.env,
        sourced(names(&["HTTPS_PROXY", "NPM_TOKEN"]), Source::Engine)
    );
    assert_eq!(
        got.env_keys,
        vec![
            text("HTTPS_PROXY", Source::Engine),
            text("NPM_TOKEN", Source::Engine)
        ]
    );

    let got = of(
        &format!("{engine}agents:\n  dev:\n    engine: claude-code\n    env: [ALPHA, NPM_TOKEN]\n"),
        "dev",
    );
    assert_eq!(
        got.env,
        sourced(names(&["HTTPS_PROXY", "NPM_TOKEN", "ALPHA"]), Source::Agent)
    );
    assert_eq!(
        got.env_keys,
        vec![
            text("HTTPS_PROXY", Source::Engine),
            text("NPM_TOKEN", Source::Agent),
            text("ALPHA", Source::Agent)
        ]
    );

    // an agent's empty list adds nothing: the engine's names still apply
    let got = of(
        &format!("{engine}agents:\n  dev:\n    engine: claude-code\n    env: []\n"),
        "dev",
    );
    assert_eq!(
        got.env,
        sourced(names(&["HTTPS_PROXY", "NPM_TOKEN"]), Source::Engine)
    );

    // an agent's names with no engine list are the agent's alone
    let got = of("agents:\n  dev:\n    env: [ALPHA, ALPHA]\n", "dev");
    assert_eq!(got.env, sourced(names(&["ALPHA"]), Source::Agent));
    assert_eq!(got.env_keys, vec![text("ALPHA", Source::Agent)]);
}

// Ruling 66: the plain path and `run --engine X` layer env the same way.
#[test]
fn test_env_merges_the_same_on_the_agent_s_engine_and_on_another() {
    let config = cfg("engines:\n  claude-code:\n    env: [A]\n  opencode:\n    env: [O]\nagents:\n  dev:\n    engine: claude-code\n    env: [B]\n");
    let own = effective(&config, "dev").unwrap();
    let named = effective_on(&config, "dev", "claude-code").unwrap();
    let other = effective_on(&config, "dev", "opencode").unwrap();
    assert_eq!(own.env.value, names(&["A", "B"]));
    assert_eq!(named.env, own.env);
    assert_eq!(other.env.value, names(&["O", "B"]));
}

#[test]
fn test_an_agent_that_does_not_exist_or_binds_an_unknown_engine_is_refused() {
    assert_eq!(
        effective(&cfg("agents:\n  dev:\n    profiles: [p]\n"), "nope"),
        Err(EngineError::AgentNotFound {
            name: "nope".to_string()
        })
    );
    let refused = effective(&cfg("agents:\n  dev:\n    engine: gpt-9\n"), "dev");
    match refused {
        Err(EngineError::UnknownEngine { engine, known }) => {
            assert_eq!(engine, "gpt-9");
            assert!(known.contains(&"claude-code".to_string()), "{known:?}");
            assert!(known.contains(&"opencode".to_string()), "{known:?}");
        }
        other => panic!("{other:?}"),
    }
}

#[test]
fn test_a_refused_auth_in_the_engine_block_refuses_the_agents_on_it() {
    let refused = effective(
        &cfg("engines:\n  claude-code:\n    auth:\n      env: [\"sk-ant-oat01-NOT-A-REAL-KEY\"]\nagents:\n  dev:\n    engine: claude-code\n"),
        "dev",
    );
    match refused {
        Err(EngineError::Auth { engine, why }) => {
            assert_eq!(engine, "claude-code");
            assert_eq!(why, AuthError::NotAName { index: 0 });
            assert!(!format!("{why:?}").contains("sk-ant"));
        }
        other => panic!("{other:?}"),
    }
}

#[test]
fn test_an_engine_without_a_default_document_passes_none() {
    let got = of("agents:\n  o:\n    engine: opencode\n", "o");
    assert_eq!(got.permissions, None);
    assert_eq!(default_permissions("opencode"), None);
}

// An empty block is the agent's (it replaces the default whole), never the
// default.
#[test]
fn test_an_empty_block_replaces_the_default_document() {
    let got = of(
        "agents:\n  dev:\n    engine: claude-code\n    engines:\n      claude-code:\n        permissions: {}\n",
        "dev",
    );
    assert_eq!(
        got.permissions,
        Some(sourced(Value::Map(Vec::new()), Source::Agent))
    );
}

// ---- auth: a source, never a value ----

fn auth(text_: &str) -> Result<AuthSource, AuthError> {
    AuthSource::parse(
        &cfg(&format!("auth: {text_}\n"))
            .get("auth")
            .cloned()
            .unwrap(),
    )
}

#[test]
fn test_auth_is_none_or_a_set_of_variable_names_or_a_file() {
    assert_eq!(auth("none"), Ok(AuthSource::None));
    assert_eq!(
        auth("{env: MY_TOKEN}"),
        Ok(AuthSource::Credential {
            env: names(&["MY_TOKEN"]),
            file: None
        })
    );
    assert_eq!(
        auth("{env: [A_TOKEN, B_KEY]}"),
        Ok(AuthSource::Credential {
            env: names(&["A_TOKEN", "B_KEY"]),
            file: None
        })
    );
    assert_eq!(
        auth("{file: ~/.claude/.credentials.json}"),
        Ok(AuthSource::Credential {
            env: Vec::new(),
            file: Some("~/.claude/.credentials.json".to_string())
        })
    );
    assert_eq!(
        auth("{env: [A_TOKEN], file: /x/creds.json}"),
        Ok(AuthSource::Credential {
            env: names(&["A_TOKEN"]),
            file: Some("/x/creds.json".to_string())
        })
    );
}

// owner amendment (2026-11-09): the top-level `auth: login|token` is folded
// into the engine's key; `login` and `token` are values of it
#[test]
fn test_auth_also_takes_the_login_and_token_words() {
    assert_eq!(auth("login"), Ok(AuthSource::Login));
    assert_eq!(auth("token"), Ok(AuthSource::Token));
}

// `login` shares the human's own login in place and reads nothing from the
// environment; `token` is the engine's built-in variables, spelled out
#[test]
fn test_login_needs_no_variable_and_token_is_the_built_in_variables() {
    let nothing = |_: &str| false;
    assert_eq!(
        AuthSource::Login.status(true, &nothing, &nothing),
        AuthState::NotNeeded
    );
    let doc = |word: &str| {
        format!("engines:\n  claude-code:\n    auth: {word}\nagents:\n  dev:\n    engine: claude-code\n")
    };
    assert_eq!(
        of(&doc("login"), "dev").auth,
        sourced(AuthSource::Login, Source::Engine)
    );
    assert_eq!(
        of(&doc("token"), "dev").auth,
        sourced(AuthSource::builtin("claude-code"), Source::Engine)
    );
}

#[test]
fn test_auth_that_is_not_a_source_is_refused() {
    assert_eq!(auth("sso"), Err(AuthError::Shape));
    assert_eq!(auth("[A_TOKEN]"), Err(AuthError::Shape));
    assert_eq!(auth("{}"), Err(AuthError::Empty));
    assert_eq!(
        auth("{token: abc}"),
        Err(AuthError::UnknownKey {
            key: "token".to_string()
        })
    );
    assert_eq!(auth("{file: ''}"), Err(AuthError::EmptyFile));
    assert_eq!(
        auth("{env: [GOOD_NAME, 2BAD]}"),
        Err(AuthError::NotAName { index: 1 })
    );
}

// a secret pasted where a variable name belongs is refused, and the refusal
// does not repeat it
#[test]
fn test_a_secret_where_a_name_belongs_is_refused_without_being_echoed() {
    let secret = "sk-ant-oat01-acceptance-fixture-not-real"; // gitleaks:allow
    for spelled in [
        format!("{{env: [{secret}]}}"),
        format!("{{env: [\"A=b{secret}\"]}}"),
    ] {
        let refused = auth(&spelled);
        assert_eq!(refused, Err(AuthError::NotAName { index: 0 }), "{spelled}");
        assert!(!format!("{refused:?}").contains("acceptance-fixture"));
    }
}

#[test]
fn test_the_built_in_sources_per_engine() {
    assert_eq!(
        AuthSource::builtin("claude-code"),
        AuthSource::Credential {
            env: names(&["CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"]),
            file: None
        }
    );
    assert_eq!(AuthSource::builtin("mock"), AuthSource::None);
}

// fixes `auth status` / doctor requiring CLAUDE_CODE_OAUTH_TOKEN alone: any
// one of the source's variables satisfies it
#[test]
fn test_any_one_exported_variable_of_the_source_satisfies_it() {
    let source = AuthSource::builtin("claude-code");
    let none_file = |_: &str| false;
    let only = |name: &'static str| move |n: &str| n == name;
    assert_eq!(
        source.status(false, &only("ANTHROPIC_API_KEY"), &none_file),
        AuthState::Present {
            via: "ANTHROPIC_API_KEY".to_string()
        }
    );
    assert_eq!(
        source.status(false, &only("CLAUDE_CODE_OAUTH_TOKEN"), &none_file),
        AuthState::Present {
            via: "CLAUDE_CODE_OAUTH_TOKEN".to_string()
        }
    );
    assert_eq!(
        source.status(false, &|_: &str| false, &none_file),
        AuthState::Missing
    );
}

#[test]
fn test_none_needs_nothing() {
    assert_eq!(
        AuthSource::None.status(false, &|_: &str| false, &|_: &str| false),
        AuthState::NotNeeded
    );
}

// ruling 8: a child is satisfied by the environment alone; only the human's
// own primary session may share a credentials file
#[test]
fn test_a_credentials_file_satisfies_the_primary_session_and_never_a_child() {
    let source = AuthSource::Credential {
        env: names(&["MY_TOKEN"]),
        file: Some("/home/a/.claude/.credentials.json".to_string()),
    };
    let nothing = |_: &str| false;
    let file = |p: &str| p == "/home/a/.claude/.credentials.json";
    assert_eq!(source.status(false, &nothing, &file), AuthState::Missing);
    assert_eq!(
        source.status(true, &nothing, &file),
        AuthState::Present {
            via: "/home/a/.claude/.credentials.json".to_string()
        }
    );
    // the environment still wins for the primary
    assert_eq!(
        source.status(true, &|n: &str| n == "MY_TOKEN", &file),
        AuthState::Present {
            via: "MY_TOKEN".to_string()
        }
    );
}

#[test]
fn test_the_source_words_are_stable() {
    assert_eq!(Source::Agent.word(), "agent");
    assert_eq!(Source::Engine.word(), "engine");
    assert_eq!(Source::Project.word(), "project");
    assert_eq!(Source::Default.word(), "default");
}

// ruling 46: a project layer cannot name the program to run, a credentials
// file, or the host's own session home
fn stripped(text: &str) -> (Vec<String>, Value) {
    let mut layer = cfg(text);
    let removed = strip_project_only(&mut layer);
    (removed, layer)
}

#[test]
fn a_project_layer_loses_the_command_the_auth_file_and_a_host_session_home() {
    let (removed, layer) = stripped(
        "engines:\n  claude-code:\n    command: /tmp/evil\n    session_home: host\n    auth:\n      env: [A_TOKEN]\n      file: /x/creds.json\n    runtime: container-rootless\n    env: [HTTPS_PROXY]\n",
    );
    assert_eq!(
        removed,
        [
            "engines.claude-code.command",
            "engines.claude-code.session_home",
            "engines.claude-code.auth.file"
        ]
    );
    assert_eq!(
        layer,
        cfg("engines:\n  claude-code:\n    auth:\n      env: [A_TOKEN]\n    runtime: container-rootless\n    env: [HTTPS_PROXY]\n")
    );
}

#[test]
fn a_file_only_auth_leaves_no_empty_auth_behind() {
    let (removed, layer) = stripped("engines:\n  claude-code:\n    auth:\n      file: /x\n");
    assert_eq!(removed, ["engines.claude-code.auth.file"]);
    assert!(layer
        .get("engines")
        .and_then(|e| e.get("claude-code"))
        .and_then(|b| b.get("auth"))
        .is_none());
}

#[test]
fn what_a_project_may_say_is_left_alone() {
    let text = "engines:\n  claude-code:\n    session_home: session\n    auth: none\n    runtime: container-rootful\nagents:\n  dev:\n    engines:\n      claude-code:\n        model: haiku\n    runtime: host\n";
    let (removed, layer) = stripped(text);
    assert!(removed.is_empty(), "{removed:?}");
    assert_eq!(layer, cfg(text));
    assert!(stripped("agents: {}\n").0.is_empty());
}

// ---- the drive: what a launch is told of how to run the engine ----

#[test]
fn test_a_drive_of_an_agent_carries_its_engine_block_and_its_own_passthrough() {
    let config = cfg("engines:\n  claude-code:\n    command: /x/claude\n    session_home: host\n    env: [A]\nagents:\n  dev:\n    env: [B]\n");
    let drive = Drive::from(&effective(&config, "dev").unwrap());
    assert_eq!(drive.command, "/x/claude");
    assert_eq!(drive.session_home, "host");
    // ruling 66: the engine's names, then the agent's
    assert_eq!(drive.env, names(&["A", "B"]));
}

#[test]
fn test_a_drive_of_an_engine_alone_reads_its_block() {
    let config = cfg("engines:\n  claude-code:\n    command: /x/claude\n    session_home: host\n    auth: none\n    env: [A]\n");
    let drive = engine_drive(&config, "claude-code").unwrap();
    assert_eq!(drive.command, "/x/claude");
    assert_eq!(drive.session_home, "host");
    assert_eq!(drive.auth, sourced(AuthSource::None, Source::Engine));
    assert_eq!(drive.env, names(&["A"]));
}

#[test]
fn test_a_drive_of_an_engine_with_no_block_is_the_built_ins() {
    let drive = engine_drive(&cfg("schema_version: 9\n"), "claude-code").unwrap();
    assert_eq!(drive.command, "claude");
    assert_eq!(drive.session_home, "session");
    assert_eq!(
        drive.auth,
        sourced(AuthSource::builtin("claude-code"), Source::Default)
    );
    assert!(drive.env.is_empty());
}

// engine show: what an engine's agents share, with the layer that said each
// (an agent that says nothing itself, so no `agent` source anywhere)
#[test]
fn test_an_engine_s_settings_are_the_block_then_the_project_then_the_defaults() {
    let config = cfg(
        "runtime: container-rootful\nengines:\n  claude-code:\n    command: /opt/claude\n    env: [HTTPS_PROXY]\n  opencode:\n    runtime: host\nagents:\n  dev:\n    engine: claude-code\n    runtime: host\n    engines:\n      claude-code:\n        model: haiku\n",
    );
    let got = engine_settings(&config, "claude-code").unwrap_or_else(|e| panic!("{e:?}"));
    assert_eq!(got.command, text("/opt/claude", Source::Engine));
    assert_eq!(got.session_home, text("session", Source::Default));
    assert_eq!(got.runtime, text("container-rootful", Source::Project));
    assert_eq!(got.env, sourced(names(&["HTTPS_PROXY"]), Source::Engine));
    assert_eq!(got.model, None, "an agent's model is not the engine's");
    let other = engine_settings(&config, "opencode").unwrap_or_else(|e| panic!("{e:?}"));
    assert_eq!(other.runtime, text("host", Source::Engine));
    assert_eq!(other.command, text("opencode", Source::Default));
}

#[test]
fn test_an_engine_nobody_drives_has_no_settings() {
    assert!(matches!(
        engine_settings(&cfg("schema_version: 9\n"), "gpt-9"),
        Err(EngineError::UnknownEngine { .. })
    ));
}

// ---- mutation: the built-in sources, which are required, who is in use ----

#[test]
fn test_each_engine_has_its_own_builtin_source() {
    let env = |list: &[&str]| AuthSource::Credential {
        env: names(list),
        file: None,
    };
    assert_eq!(
        AuthSource::builtin("claude-code"),
        env(&["CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"])
    );
    // Rulings 69 and 79: opencode's provider keys alone.
    assert_eq!(
        AuthSource::builtin("opencode"),
        env(&["ANTHROPIC_API_KEY", "OPENAI_API_KEY"])
    );
    assert_eq!(AuthSource::builtin("mock"), AuthSource::None);
}

#[test]
fn test_every_source_is_required_but_opencode_s() {
    assert!(AuthSource::required("claude-code"));
    assert!(AuthSource::required("mock"));
    assert!(!AuthSource::required("opencode"));
}

#[test]
fn test_the_engines_in_use_are_each_once_in_agent_name_order() {
    let config = cfg(
        "agents:\n  c:\n    engine: opencode\n  b:\n    engine: opencode\n  a:\n    engine: mock\n  d:\n    profiles: [p]\n",
    );
    assert_eq!(
        engines_in_use(&config),
        names(&["mock", "opencode", "claude-code"])
    );
}

#[test]
fn test_with_no_agent_the_default_engine_is_in_use() {
    assert_eq!(
        engines_in_use(&cfg("schema_version: 9\n")),
        names(&["claude-code"])
    );
    assert_eq!(
        engines_in_use(&cfg("agents: {}\n")),
        names(&["claude-code"])
    );
}

// The engines an agent has a block for, in the order the config holds them;
// none for an agent with no block, or whose `engines` is not a mapping.
#[test]
fn test_an_agent_s_blocks_are_named_in_the_config_s_order() {
    let config = cfg("agents:\n  dev:\n    engines:\n      opencode:\n        model: m\n      claude-code:\n        model: haiku\n  bare:\n    engine: claude-code\n  odd:\n    engines: [claude-code]\n");
    assert_eq!(
        agent_blocks(&config, "dev"),
        names(&["opencode", "claude-code"])
    );
    assert!(agent_blocks(&config, "bare").is_empty());
    assert!(agent_blocks(&config, "odd").is_empty());
    assert!(agent_blocks(&config, "nobody").is_empty());
}

// The engine a run asks for is said as the agent's, except the default engine
// asked for by an agent that names none, which is still the default's.
#[test]
fn test_the_engine_asked_for_is_the_agent_s_unless_it_is_the_default_it_already_had() {
    let config = cfg("agents:\n  silent:\n    profiles: [p]\n  named:\n    engine: opencode\n");
    let on = |agent: &str, engine: &str| effective_on(&config, agent, engine).unwrap().engine;
    assert_eq!(
        on("silent", "claude-code"),
        text("claude-code", Source::Default)
    );
    assert_eq!(on("silent", "opencode"), text("opencode", Source::Agent));
    assert_eq!(
        on("named", "claude-code"),
        text("claude-code", Source::Agent)
    );
    assert_eq!(on("named", "opencode"), text("opencode", Source::Agent));
}
