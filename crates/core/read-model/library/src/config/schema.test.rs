//! The config schema (`resources/schema/input/config-schema.json` at
//! 860336c4e) as the load words it, on the cases the Go acceptance and
//! `configload` tests refuse: the messages are Go's.

use super::*;

fn parse(text: &str) -> Value {
    angzarr_yaml::parse(text).unwrap_or_else(|e| panic!("{e}"))
}

fn texts(doc: &str) -> Vec<String> {
    validate(&parse(doc))
        .iter()
        .map(|v| v.text("config.yaml"))
        .collect()
}

// the fixture `cfg/v8`: every key of the schema, correctly used
#[test]
fn test_a_config_using_every_key_correctly_has_no_violation() {
    let doc = "schema_version: 8\neditor:\n  command: vim\n  args: [-u, NONE]\nconfig:\n  content_form: original\n  statusline: none\nsync:\n  on_run: none\nworkspace: worktree\nruntime: host\ndelegation:\n  concurrency: 3\n  depth: 2\n  idle_timeout: 5m\nshell_timeout:\n  default: 30s\n  max: 1h\nisolation_images:\n  claude-code: example.com/img:1\nisolation_base: ctxloom\nisolation_memory_limit: 4g\nisolation_pids_limit: 512\noutput:\n  dir: /var/out\nisolation_engines: [claude-code]\ncompanions:\n  acme:\n    command: [acme-bin, --flag]\n  beta:\n    command: [beta-bin]\nui:\n  prefix_key: ctrl-b\n  list_window: 7d\nsession_reap_age: 30d\nsession_purge_age: 90d\nsession_transcript_retention: 365d\nnon_tty_format: json\nagents:\n  dev:\n    engine: claude-code\n    engines:\n      claude-code:\n        model: opus\n        effort: low\n        permissions:\n          defaultMode: plan\n    profiles: [p1]\n    runtime: host\n    driving: oneshot\n    env: [ALPHA]\ndefault_agent: dev\nengines:\n  claude-code:\n    auth: login\n";
    assert_eq!(texts(doc), Vec::<String>::new());
}

// fault_tolerance / config.feature: an unknown key at the top level is named,
// with the keys there are
#[test]
fn test_an_unknown_top_level_key_is_named_with_the_keys_there_are() {
    assert_eq!(
        texts("schema_version: 8\nzzz: 1\n"),
        ["unknown key `zzz` in config.yaml: ctxloom does not know it (known keys at the top level: agents, companions, config, default_agent, delegation, editor, engines, isolation_base, isolation_devcontainer_service, isolation_engines, isolation_images, isolation_memory_limit, isolation_pids_limit, non_tty_format, output, runtime, schema_version, session_purge_age, session_reap_age, session_transcript_retention, shell_timeout, sync, ui, workspace)"]
    );
    // The inline profiles block, the native mcp block and the retired turn cap
    // are keys the schema does not describe.
    for key in ["profiles", "mcp", "agent_turn_cap"] {
        let said = texts(&format!("schema_version: 8\n{key}:\n  x: 1\n"));
        assert!(
            said[0].starts_with(&format!("unknown key `{key}` in config.yaml")),
            "{said:?}"
        );
    }
}

// agent.feature: A binding still carrying auth is refused, naming the key
#[test]
fn test_an_unknown_nested_key_is_named_by_its_path_and_where_it_would_be_legal() {
    assert_eq!(
        texts("schema_version: 8\nagents:\n  developer:\n    profiles: [dev]\n    auth: login\n"),
        ["unknown key `agents.developer.auth` in config.yaml: ctxloom does not know it (known keys at `agents.developer`: driving, engine, engines, env, may_delegate, profiles, roots, runtime, surfaces)"]
    );
}

// the fixtures `cfg/bad-enum` and `cfg/bad-delegation`, and the Go messages
#[test]
fn test_a_value_the_schema_refuses_is_worded_as_go_words_it() {
    assert_eq!(
        texts("schema_version: 8\nruntime: laptop\n"),
        ["config.yaml: `runtime` breaks the schema: value must be one of \"host\", \"container-rootless\", \"container-rootful\""]
    );
    assert_eq!(
        texts("schema_version: 8\ndelegation:\n  concurrency: 0\n"),
        ["config.yaml: `delegation.concurrency` breaks the schema: must be >= 1 but found 0"]
    );
    assert_eq!(
        texts("schema_version: 8\nui:\n  prefix_key: xx\n"),
        ["config.yaml: `ui.prefix_key` breaks the schema: does not match pattern '^ctrl[-+].$'"]
    );
    assert_eq!(
        texts("schema_version: 8\nsession_reap_age: soon\n"),
        ["config.yaml: `session_reap_age` breaks the schema: does not match pattern '^[0-9]+(d|w|h|m|s)$'"]
    );
    assert_eq!(
        texts("schema_version: 8\nisolation_pids_limit: lots\n"),
        ["config.yaml: `isolation_pids_limit` breaks the schema: matches none of the shapes the schema allows here"]
    );
}

// the wrong-shape messages: what the key holds, and what it should
#[test]
fn test_a_value_of_the_wrong_shape_says_what_it_holds_and_what_was_expected() {
    assert_eq!(
        texts("schema_version: 8\ndelegation:\n  concurrency: abc\n"),
        ["config.yaml: `delegation.concurrency` holds a string, expected integer"]
    );
    assert_eq!(
        texts("schema_version: 8\nconfig:\n  content_form: [distilled]\n"),
        ["config.yaml: `config.content_form` holds a array, expected string"]
    );
    assert_eq!(
        texts("schema_version: 8\neditor:\n  args: x\n"),
        ["config.yaml: `editor.args` holds a string, expected array"]
    );
    assert_eq!(
        texts("schema_version: 9\ncompanions: foo\n"),
        ["config.yaml: `companions` holds a string, expected object"]
    );
    assert_eq!(
        texts("schema_version: 8\nagents:\n  d:\n    profiles: dev\n"),
        ["config.yaml: `agents.d.profiles` holds a string, expected array"]
    );
    assert_eq!(
        texts("schema_version: 8\nagents: [x]\n"),
        ["config.yaml: `agents` holds a array, expected object"]
    );
    assert!(validate(&parse("schema_version: 8\nagents: [x]\n"))[0].is_shape());
    assert!(!validate(&parse("schema_version: 8\nruntime: laptop\n"))[0].is_shape());
}

// the schema's four duration and age patterns, and the one that is any of two shapes
#[test]
fn test_durations_ages_and_limits_take_the_shapes_the_schema_gives_them() {
    assert!(texts("delegation:\n  idle_timeout: 1h30m\n").is_empty());
    assert!(texts("delegation:\n  idle_timeout: 1.5h\n").is_empty());
    assert_eq!(texts("delegation:\n  idle_timeout: soon\n").len(), 1);
    assert!(texts("ui:\n  list_window: all\n").is_empty());
    assert!(texts("ui:\n  list_window: 0\n").is_empty());
    assert!(texts("ui:\n  list_window: 12h\n").is_empty());
    assert_eq!(texts("ui:\n  list_window: lots\n").len(), 1);
    assert!(texts("isolation_memory_limit: unlimited\n").is_empty());
    assert!(texts("isolation_pids_limit: 0\n").is_empty());
    assert!(texts("isolation_pids_limit: unlimited\n").is_empty());
    assert!(texts("non_tty_format: json\n").is_empty());
    assert!(texts("non_tty_format: text\n").is_empty());
    assert_eq!(texts("non_tty_format: pdf\n").len(), 1);
}

// plan supersedes Go: D28 (json and text only); Go's schema also allows yaml, toml and markdown
#[test]
fn test_a_dropped_output_format_is_not_a_non_tty_format() {
    for dropped in ["yaml", "toml", "markdown"] {
        assert_eq!(
            texts(&format!("non_tty_format: {dropped}\n")).len(),
            1,
            "{dropped} is dropped"
        );
    }
}

// overlay.go KnownPath: the schema's paths, a label under a map is free
#[test]
fn test_known_paths() {
    let p = |s: &str| s.split('.').map(ToString::to_string).collect::<Vec<_>>();
    assert!(known_path(&p("editor.command")));
    assert!(known_path(&p("engines.claude-code.session_home")));
    assert!(known_path(&p("agents.MyCoder.runtime")));
    assert!(known_path(&p(
        "agents.anything.engines.claude-code.permissions.mode"
    )));
    assert!(known_path(&p("default_agent")));
    assert!(!known_path(&p("nonsense.x")));
    assert!(!known_path(&p("editor.nope")));
}

// overlay.go ValidateAt: an override value is checked at its path
#[test]
fn test_an_override_value_is_checked_at_its_path() {
    let p = |s: &str| s.split('.').map(ToString::to_string).collect::<Vec<_>>();
    assert!(validate_at(&p("runtime"), &Value::Scalar("host".into())).is_ok());
    let err = validate_at(&p("runtime"), &Value::Scalar("laptop".into())).expect_err("laptop");
    assert!(err.contains("value must be one of"), "{err}");
    assert!(validate_at(&p("delegation.concurrency"), &Value::Scalar("0".into())).is_err());
    assert!(validate_at(
        &p("companions.acme.command"),
        &Value::Seq(vec![Value::Scalar("a".into())])
    )
    .is_ok());
    assert!(validate_at(
        &p("companions"),
        &Value::Seq(vec![Value::Scalar("a".into())])
    )
    .is_err());
}

// config.go resolveConfigSection: the sections, in the order `config show
// nonsense` names them
#[test]
fn test_the_sections_are_the_top_level_keys_in_go_s_order() {
    assert_eq!(SECTIONS[0], "schema_version");
    assert_eq!(SECTIONS[1], "engines");
    // Owner ruling 134: `permissions` is no longer a section.
    assert_eq!(SECTIONS.len(), 24);
    assert_eq!(SECTIONS[23], "non_tty_format");
    assert!(!SECTIONS.contains(&"permissions"));
}

// the string patterns the schema writes, each accepting and refusing
#[test]
fn test_each_string_pattern_accepts_and_refuses() {
    let cases: [(Pat, &[&str], &[&str]); 5] = [
        (
            Pat::PrefixKey,
            &["ctrl-b", "ctrl+x"],
            &["xxxx-b", "ctrlxb", "ctrl-bb", "ctrl-", "ctrl-\n", "Ctrl-b"],
        ),
        (
            Pat::ListWindow,
            &["all", "0", "12h", "1d", "3w"],
            &["none", "00", "h", "12", "1y"],
        ),
        (
            Pat::Age,
            &["1d", "30m", "9s", "12w", "5h"],
            &["d", "12", "12x", "", "1.5d"],
        ),
        (
            Pat::PidsLimit,
            &["512", "  8 ", "unlimited", "UNLIMITED"],
            &["", " ", "a1", "-1", "1.5"],
        ),
        (Pat::NoEquals, &["a", "a b"], &["", "a=b", "a\nb"]),
    ];
    for (pat, good, bad) in cases {
        for s in good {
            assert!(pat.matches(s), "{pat:?} accepts {s:?}");
        }
        for s in bad {
            assert!(!pat.matches(s), "{pat:?} refuses {s:?}");
        }
    }
}

// the memory limit: a number, a binary or decimal unit, or `unlimited`
#[test]
fn test_a_memory_limit_is_a_number_with_a_unit_or_unlimited() {
    for ok in [
        "4g",
        "4G",
        "4GiB",
        "4 g",
        " 4g ",
        "1.5gb",
        "unlimited",
        "UNLIMITED",
        "512",
        "512b",
        "512B",
        "2kb",
        "3TIB",
    ] {
        assert!(Pat::MemoryLimit.matches(ok), "{ok:?}");
    }
    for bad in [
        "2Ki", "g", "4x", "4gbb", "4.", "4.g", "", "ib", "4 ii", "4bb", "-4g", "4gi b",
    ] {
        assert!(!Pat::MemoryLimit.matches(bad), "{bad:?}");
    }
}

// overlay.go KnownPath: where the schema describes a path
#[test]
fn test_the_schema_describes_exactly_the_paths_it_lists() {
    let path = |p: &[&str]| -> Vec<String> { p.iter().map(ToString::to_string).collect() };
    for known in [
        &["companions", "acme", "command", "0"][..],
        &["editor", "command"],
        &["engines", "claude-code", "session_home"],
        &["engines", "opencode", "env", "0"],
        &["agents", "dev", "env", "0"],
        &["agents", "dev", "engines", "claude-code", "model"],
        &[
            "agents",
            "dev",
            "engines",
            "opencode",
            "permissions",
            "mode",
        ],
    ] {
        assert!(known_path(&path(known)), "{known:?}");
    }
    for unknown in [
        &["companions", "acme", "x"][..],
        &["editor", "bogus"],
        &["agents", "dev", "nope"],
        &["engines", "claude-code", "model"],
        &["agents", "dev", "model"],
        &["agents", "dev", "effort"],
        &["agents", "dev", "permissions", "claude-code", "mode"],
        &["agents", "dev", "engines", "claude-code", "runtime"],
    ] {
        assert!(!known_path(&path(unknown)), "{unknown:?}");
    }
}

// the validator's names for what a scalar reads as
#[test]
fn test_a_scalar_reads_as_the_type_the_validator_names() {
    for (text, kind) in [
        ("1", "number"),
        ("-1", "number"),
        ("1.5", "number"),
        ("1e3", "number"),
        (".5", "number"),
        ("true", "boolean"),
        ("false", "boolean"),
        ("True", "string"),
        ("abc", "string"),
        ("NaN", "string"),
        ("inf", "string"),
    ] {
        assert_eq!(type_of_scalar(text), kind, "{text}");
    }
}

// generation 9: a registration is a name and its command.
#[test]
fn test_a_companion_is_registered_with_a_command_list() {
    assert_eq!(
        texts("schema_version: 9\ncompanions:\n  acme:\n    command: [acme-bin, loadout]\n"),
        Vec::<String>::new()
    );
    assert_eq!(
        texts("schema_version: 9\ncompanions:\n  acme:\n    command: acme-bin\n"),
        ["config.yaml: `companions.acme.command` holds a string, expected array"]
    );
    assert_eq!(
        texts("schema_version: 9\ncompanions:\n  acme:\n    command: [\"\"]\n"),
        ["config.yaml: `companions.acme.command.0` breaks the schema: length must be >= 1, but got 0"]
    );
    assert_eq!(
        texts("schema_version: 9\ncompanions:\n  acme:\n    cmd: [a]\n"),
        ["unknown key `companions.acme.cmd` in config.yaml: ctxloom does not know it (known keys at `companions.acme`: command)"]
    );
    assert_eq!(
        texts("schema_version: 9\ncompanions: [acme]\n"),
        ["config.yaml: `companions` holds a array, expected object"]
    );
}

// ---- RED (awaiting review): the engines schema, owner rulings 40-44 ----

const ENGINES_DOC: &str = "schema_version: 9\nengines:\n  claude-code:\n    command: claude\n    session_home: session\n    auth:\n      env: [CLAUDE_CODE_OAUTH_TOKEN, ANTHROPIC_API_KEY]\n    runtime: container-rootless\n    env: [HTTPS_PROXY]\n  opencode:\n    auth: none\nagents:\n  dev:\n    engine: claude-code\n    engines:\n      claude-code:\n        model: haiku\n        effort: low\n        permissions:\n          mode: plan\n      opencode:\n        model: opencode/big-pickle\n    profiles: [p1]\n    runtime: host\n    env: [ALPHA]\n";

#[test]
fn test_the_engines_block_and_an_agent_s_engine_model_and_effort_are_legal() {
    assert_eq!(texts(ENGINES_DOC), Vec::<String>::new());
}

#[test]
fn test_every_auth_form_is_legal_and_no_other_is() {
    for auth in [
        "none",
        "login",
        "token",
        "{env: A_TOKEN}",
        "{env: [A, B]}",
        "{file: /x}",
        "{env: [A], file: /x}",
    ] {
        let doc = format!("schema_version: 9\nengines:\n  claude-code:\n    auth: {auth}\n");
        assert_eq!(texts(&doc), Vec::<String>::new(), "{auth}");
    }
    for auth in ["sso", "{token: x}", "[A]", "{env: 7}"] {
        let doc = format!("schema_version: 9\nengines:\n  claude-code:\n    auth: {auth}\n");
        assert!(!texts(&doc).is_empty(), "{auth} was accepted");
    }
}

// the engines are a fixed set: a name outside it is refused, naming the set
#[test]
fn test_an_engine_nobody_built_is_refused_under_engines() {
    let said = texts("schema_version: 9\nengines:\n  gpt-9:\n    command: x\n");
    assert_eq!(said.len(), 1, "{said:?}");
    assert!(
        said[0].starts_with("unknown key `engines.gpt-9`"),
        "{said:?}"
    );
    assert!(
        said[0].contains("claude-code") && said[0].contains("opencode"),
        "{said:?}"
    );
}

#[test]
fn test_the_engine_block_refuses_values_the_schema_does_not_give() {
    for (key, value) in [
        ("session_home", "elsewhere"),
        ("runtime", "vm"),
        ("command", "''"),
    ] {
        let doc = format!("schema_version: 9\nengines:\n  claude-code:\n    {key}: {value}\n");
        assert_eq!(texts(&doc).len(), 1, "{key}: {value}: {:?}", texts(&doc));
    }
    let said = texts("schema_version: 9\nengines:\n  claude-code:\n    env: [\"A=b\"]\n");
    assert_eq!(said.len(), 1, "{said:?}");
}

// ruling 40: model, effort and permissions are the agent's, not the engine's
#[test]
fn test_model_effort_and_permissions_are_not_engine_block_keys() {
    for key in ["model", "effort", "permissions"] {
        let said = texts(&format!(
            "schema_version: 9\nengines:\n  claude-code:\n    {key}: x\n"
        ));
        assert!(
            said.iter()
                .any(|s| s.starts_with(&format!("unknown key `engines.claude-code.{key}`"))),
            "{key}: {said:?}"
        );
    }
}

// ruling 41: no versioning, no migration: the retired keys are unknown keys
#[test]
fn test_the_retired_llm_block_and_label_and_engine_home_are_unknown_keys() {
    let said = texts("schema_version: 9\nllm:\n  configs:\n    big:\n      type: claude-code\n");
    assert_eq!(said.len(), 1, "{said:?}");
    assert!(
        said[0].starts_with("unknown key `llm` in config.yaml"),
        "{said:?}"
    );
    let said = texts("schema_version: 9\nagents:\n  dev:\n    llm: big\n");
    assert!(
        said[0].starts_with("unknown key `agents.dev.llm`"),
        "{said:?}"
    );
    let said = texts("schema_version: 9\nagents:\n  dev:\n    engine_home: session\n");
    assert!(
        said[0].starts_with("unknown key `agents.dev.engine_home`"),
        "{said:?}"
    );
}

#[test]
fn test_the_top_level_keys_gain_engines_and_lose_llm() {
    let said = texts("schema_version: 9\nzzz: 1\n");
    assert!(said[0].contains("engines"), "{said:?}");
    assert!(!said[0].contains(" llm,"), "{said:?}");
}

// owner amendment (2026-11-09): hard migrate, one auth key. The top-level
// `auth: login|token` is an ordinary unknown key, like `llm`; no alias.
#[test]
fn test_the_top_level_auth_key_is_an_unknown_key() {
    let said = texts("schema_version: 9\nauth: token\n");
    assert_eq!(said.len(), 1, "{said:?}");
    assert!(
        said[0].starts_with("unknown key `auth` in config.yaml"),
        "{said:?}"
    );
    assert!(
        !said[0].contains(" auth,") && !said[0].ends_with(" auth)"),
        "{said:?}"
    );
}

// Owner ruling: no `env_host`. An engine gets its curated allowlist plus the
// names in `env`, and there is no mode that passes the host environment; the
// key is an ordinary unknown key (hard migrate, no alias).
#[test]
fn test_env_host_is_an_unknown_key_of_an_agent_and_of_an_engine_block() {
    let agent =
        texts("schema_version: 9\nagents:\n  dev:\n    engine: claude-code\n    env_host: true\n");
    assert!(
        agent
            .iter()
            .any(|s| s.starts_with("unknown key `agents.dev.env_host`")),
        "{agent:?}"
    );
    let engine = texts("schema_version: 9\nengines:\n  claude-code:\n    env_host: false\n");
    assert!(
        engine
            .iter()
            .any(|s| s.starts_with("unknown key `engines.claude-code.env_host`")),
        "{engine:?}"
    );
}

// An array's children are addressed by index alone.
#[test]
fn test_only_an_index_names_an_array_s_item() {
    let at = |tail: &str| {
        node_at(&[
            "agents".to_string(),
            "dev".to_string(),
            "profiles".to_string(),
            tail.to_string(),
        ])
    };
    assert!(at("0").is_some());
    assert!(at("first").is_none());
}

// ruling 52: a build without `test-support` (the one that ships) knows only
// the real engines; a config naming a mock is refused as an unknown engine.
#[cfg(not(feature = "test-support"))]
#[test]
fn test_a_plain_build_knows_only_the_real_engines() {
    assert_eq!(ENGINES.to_vec(), ["claude-code", "opencode"]);
    let said = texts("schema_version: 9\nagents:\n  a:\n    engine: mock\n");
    assert_eq!(said.len(), 1, "{said:?}");
    assert!(said[0].contains("engine"), "{said:?}");
}

// Under `test-support` the mock and the mock-* test engines are known too, in
// name order.
#[cfg(feature = "test-support")]
#[test]
fn test_a_test_build_knows_the_mock_engines_too() {
    assert_eq!(
        ENGINES.to_vec(),
        [
            "claude-code",
            "mock",
            "mock-launch",
            "mock-lossy",
            "mock-noskills",
            "opencode"
        ]
    );
}

// Ruling 57 (2026-11-09), hard migrate: an agent's model, effort and
// permission mode live in its per-engine block `agents.<n>.engines.<e>`; the
// flat `model` / `effort` and the old engine-keyed `permissions.<engine>` map
// are ordinary unknown keys (no alias).
#[test]
fn test_an_agent_s_flat_model_effort_and_engine_keyed_permissions_are_unknown_keys() {
    for (doc, key) in [
        ("agents:\n  dev:\n    model: haiku\n", "agents.dev.model"),
        ("agents:\n  dev:\n    effort: low\n", "agents.dev.effort"),
        (
            "agents:\n  dev:\n    permissions:\n      claude-code:\n        mode: plan\n",
            // Ruling 134: the agent has no `permissions` at all now.
            "agents.dev.permissions",
        ),
    ] {
        let said = texts(&format!("schema_version: 9\n{doc}"));
        assert_eq!(said.len(), 1, "{said:?}");
        assert!(
            said[0].starts_with(&format!("unknown key `{key}` in config.yaml")),
            "{said:?}"
        );
    }
}

// Owner ruling 134 (2026-11-11), hard migrate: the engine-neutral posture
// (`permissions.approver`, `approval_timeout`, `sandbox`, `network`) is gone
// at the project level and on an agent: its section is an unknown key.
#[test]
fn test_the_engine_neutral_posture_is_an_unknown_key_at_the_project_and_on_an_agent() {
    for key in [
        "approver: human",
        "approval_timeout: 90s",
        "sandbox: full",
        "network: open",
    ] {
        for (doc, path) in [
            (format!("permissions:\n  {key}\n"), "permissions"),
            (
                format!("agents:\n  dev:\n    permissions:\n      {key}\n"),
                "agents.dev.permissions",
            ),
        ] {
            let said = texts(&format!("schema_version: 9\n{doc}"));
            assert_eq!(said.len(), 1, "{said:?}");
            assert!(
                said[0].starts_with(&format!("unknown key `{path}` in config.yaml")),
                "{said:?}"
            );
        }
    }
}

// Owner rulings 135, 137: an agent's engine block's `permissions` is the
// engine's own document: any keys, any values, any depth are taken, and
// nothing inside it is checked.
#[test]
fn test_an_agent_s_engine_permissions_take_any_document() {
    for engine in ["claude-code", "opencode"] {
        let doc = format!(
            "schema_version: 9\nagents:\n  dev:\n    engines:\n      {engine}:\n        permissions:\n          defaultMode: anything\n          mode: plan\n          allow: [Read, \"Bash(git *)\"]\n          nested:\n            deep:\n              - x: 1\n          flag: true\n          n: 7\n"
        );
        assert_eq!(texts(&doc), Vec::<String>::new(), "{engine}");
    }
}

// The per-engine block names only engines ctxloom drives, and holds only
// model, effort and permissions (runtime and env are the agent's, engine-neutral).
#[test]
fn test_an_agent_s_engine_block_holds_model_effort_and_permissions_of_a_known_engine() {
    let said =
        texts("schema_version: 9\nagents:\n  dev:\n    engines:\n      gpt-9:\n        model: x\n");
    assert!(
        said[0].starts_with("unknown key `agents.dev.engines.gpt-9`"),
        "{said:?}"
    );
    let said = texts("schema_version: 9\nagents:\n  dev:\n    engines:\n      claude-code:\n        runtime: host\n");
    assert!(
        said[0].starts_with("unknown key `agents.dev.engines.claude-code.runtime`"),
        "{said:?}"
    );
    let said = texts("schema_version: 9\nagents:\n  dev:\n    engines:\n      claude-code:\n        model: \"\"\n");
    assert_eq!(said.len(), 1, "an empty model is refused: {said:?}");
}

// Owner rulings 59, 60 and 106 (2026-11-09/10), hard migrate: no boolean is
// left in what a person writes. The five booleans and the old output key are
// ordinary unknown keys (no alias, no reader, no migration, no warning).
#[test]
fn test_the_retired_boolean_keys_and_output_dir_are_unknown_keys() {
    for (doc, key) in [
        ("config:\n  use_distilled: true\n", "config.use_distilled"),
        (
            "config:\n  silence_unsupported: true\n",
            "config.silence_unsupported",
        ),
        ("sync:\n  auto_sync: true\n", "sync.auto_sync"),
        ("output_dir: /var/out\n", "output_dir"),
    ] {
        let said = texts(&format!("schema_version: 9\n{doc}"));
        assert_eq!(said.len(), 1, "{said:?}");
        assert!(
            said[0].starts_with(&format!("unknown key `{key}` in config.yaml")),
            "{said:?}"
        );
    }
    assert_eq!(
        texts("schema_version: 9\nconfig:\n  zzz: 1\n")[0],
        "unknown key `config.zzz` in config.yaml: ctxloom does not know it (known keys at `config`: content_form, statusline)"
    );
    assert_eq!(
        texts("schema_version: 9\nsync:\n  zzz: 1\n")[0],
        "unknown key `sync.zzz` in config.yaml: ctxloom does not know it (known keys at `sync`: on_run)"
    );
    assert_eq!(
        texts("schema_version: 9\noutput:\n  zzz: 1\n")[0],
        "unknown key `output.zzz` in config.yaml: ctxloom does not know it (known keys at `output`: dir)"
    );
}

// Each former boolean is a word now, and only its words are taken: the old
// `true` / `false` spellings are refused like any other value.
#[test]
fn test_each_former_boolean_takes_only_its_words() {
    for (key, words, refused) in [
        (
            "config.content_form",
            "\"distilled\", \"original\"",
            "false",
        ),
        ("config.statusline", "\"ctxloom\", \"none\"", "true"),
        ("sync.on_run", "\"none\", \"missing\"", "true"),
    ] {
        let doc = |value: &str| {
            let mut text = String::from("schema_version: 9\n");
            let parts: Vec<&str> = key.split('.').collect();
            for (depth, part) in parts.iter().enumerate() {
                text.push_str(&"  ".repeat(depth));
                text.push_str(part);
                text.push(':');
                if depth + 1 == parts.len() {
                    text.push_str(&format!(" {value}\n"));
                } else {
                    text.push('\n');
                }
            }
            text
        };
        for word in words.split(", ") {
            assert_eq!(
                texts(&doc(word.trim_matches('"'))),
                Vec::<String>::new(),
                "{key}: {word}"
            );
        }
        assert_eq!(
            texts(&doc(refused)),
            [format!(
                "config.yaml: `{key}` breaks the schema: value must be one of {words}"
            )]
        );
    }
}

// Ruling 106: `output.dir` is a path, absolute or relative to the project
// root; an empty one says nothing and is refused.
#[test]
fn test_output_dir_is_a_non_empty_path() {
    for dir in ["ctxloom/", "/var/out", "docs/sessions"] {
        assert_eq!(
            texts(&format!("schema_version: 9\noutput:\n  dir: {dir}\n")),
            Vec::<String>::new()
        );
    }
    assert_eq!(
        texts("schema_version: 9\noutput:\n  dir: \"\"\n"),
        ["config.yaml: `output.dir` breaks the schema: length must be >= 1, but got 0"]
    );
}

// With the booleans gone, nothing in the schema is typed boolean: a value that
// reads `true` at a schema'd string key is a string.
#[test]
fn test_no_key_of_the_schema_is_a_boolean() {
    for path in [
        "config.content_form",
        "config.statusline",
        "sync.on_run",
        "output.dir",
    ] {
        let path: Vec<String> = path.split('.').map(String::from).collect();
        assert_eq!(scalar_kind(&path), ScalarKind::Str, "{path:?}");
    }
}
