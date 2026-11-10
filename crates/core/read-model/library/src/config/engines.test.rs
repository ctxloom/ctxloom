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

fn names(list: &[&str]) -> Vec<String> {
    list.iter().map(ToString::to_string).collect()
}

// ruling 42: unset model and effort pass no flag; permissions is an explicit
// ctxloom default; nothing else is invented
#[test]
fn test_an_agent_that_says_nothing_runs_on_the_built_in_defaults() {
    let got = of("agents:\n  dev:\n    profiles: [p]\n", "dev");
    assert_eq!(got.engine, text("claude-code", Source::Default));
    assert_eq!(got.model, None);
    assert_eq!(got.effort, None);
    assert_eq!(got.permissions, text("acceptEdits", Source::Default));
    assert_eq!(got.runtime, text("host", Source::Default));
    assert_eq!(got.runtime_override, Override::Inherited);
    assert_eq!(got.env_host, sourced(false, Source::Default));
    assert_eq!(got.env, sourced(Vec::new(), Source::Default));
    assert_eq!(got.command, text("claude", Source::Default));
    assert_eq!(got.session_home, text("session", Source::Default));
    assert_eq!(
        got.auth,
        sourced(AuthSource::builtin("claude-code"), Source::Default)
    );
}

// ruling 40: model, effort and permissions are the agent's
#[test]
fn test_an_agent_s_model_effort_and_permissions_are_its_own() {
    let got = of(
        "agents:\n  dev:\n    engine: claude-code\n    model: haiku\n    effort: low\n    permissions:\n      claude-code:\n        mode: plan\n",
        "dev",
    );
    assert_eq!(got.engine, text("claude-code", Source::Agent));
    assert_eq!(got.model, Some(text("haiku", Source::Agent)));
    assert_eq!(got.effort, Some(text("low", Source::Agent)));
    assert_eq!(got.permissions, text("plan", Source::Agent));
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

// env_host and env: resolved key by key, agent > engine > default; a list
// replaces, it is never the union (a union would let an engine's curated list
// leak into an agent that narrowed it)
#[test]
fn test_env_host_and_env_are_the_agent_s_over_the_engine_s_and_a_list_replaces() {
    let engine =
        "engines:\n  claude-code:\n    env_host: false\n    env: [HTTPS_PROXY, NPM_TOKEN]\n";
    let got = of(
        &format!("{engine}agents:\n  dev:\n    engine: claude-code\n"),
        "dev",
    );
    assert_eq!(got.env_host, sourced(false, Source::Engine));
    assert_eq!(
        got.env,
        sourced(names(&["HTTPS_PROXY", "NPM_TOKEN"]), Source::Engine)
    );

    let got = of(
        &format!("{engine}agents:\n  dev:\n    engine: claude-code\n    env: [ALPHA]\n"),
        "dev",
    );
    assert_eq!(got.env, sourced(names(&["ALPHA"]), Source::Agent));
    assert_eq!(got.env_host, sourced(false, Source::Engine));

    let got = of(
        &format!("{engine}agents:\n  dev:\n    engine: claude-code\n    env_host: true\n"),
        "dev",
    );
    assert_eq!(got.env_host, sourced(true, Source::Agent));
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
fn test_an_engine_without_a_permission_model_has_no_permission_mode() {
    let got = of("agents:\n  o:\n    engine: opencode\n", "o");
    assert_eq!(got.permissions, text("", Source::Default));
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
