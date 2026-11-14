//! `engine`'s tests, beside its verbs.

mod read {
    use crate::tests::*;

    #[test]
    fn test_engine_show_prints_the_settings_their_layers_and_each_agents_resolution() {
        let ran = go(
            &["engine", "show", "claude-code"],
            TEXT,
            &[ENGINE_HOME, ENGINE_PROJECT],
        );
        assert_eq!(ran.code(), 0);
        assert_eq!(
            ran.out,
            "claude-code\n  command: /opt/claude (engine)\n  session_home: session (default)\n  runtime: container-rootless (project)\n  env: HTTPS_PROXY (engine)\n  auth: not-needed (engine)\n  agents:\n    dev\n      model: haiku (agent)\n      effort: engine default, no flag passed (default)\n      permissions: {defaultMode: acceptEdits} (default)\n        passed to claude-code verbatim; ctxloom does not check it\n      runtime: host (agent; replaces project container-rootless: weakens isolation)\n      env: HTTPS_PROXY (engine)\n      session_home: session (default)\n    same\n      model: engine default, no flag passed (default)\n      effort: engine default, no flag passed (default)\n      permissions: {defaultMode: acceptEdits} (default)\n        passed to claude-code verbatim; ctxloom does not check it\n      runtime: container-rootless (agent)\n      env: HTTPS_PROXY (engine)\n      session_home: session (default)\n    strict\n      model: engine default, no flag passed (default)\n      effort: engine default, no flag passed (default)\n      permissions: {defaultMode: acceptEdits} (default)\n        passed to claude-code verbatim; ctxloom does not check it\n      runtime: container-rootful (agent; replaces project container-rootless: ownership differs, not interchangeable)\n      env: HTTPS_PROXY (engine)\n      session_home: session (default)\n"
        );
    }

    #[test]
    fn test_engine_list_prints_every_engine_and_only_the_agents_it_has() {
        let ran = go(&["engine", "list"], TEXT, &[ENGINE_HOME, ENGINE_PROJECT]);
        assert_eq!(ran.code(), 0);
        assert_eq!(
            ran.out,
            "claude-code\n  command: /opt/claude (engine)\n  session_home: session (default)\n  runtime: container-rootless (project)\n  env: HTTPS_PROXY (engine)\n  auth: not-needed (engine)\n  agents: dev, same, strict\nmock\n  command: mock (default)\n  session_home: session (default)\n  runtime: container-rootless (project)\n  env: none (default)\n  auth: not-needed (default)\nmock-launch\n  command: mock-launch (default)\n  session_home: session (default)\n  runtime: container-rootless (project)\n  env: none (default)\n  auth: not-needed (default)\nmock-lossy\n  command: mock-lossy (default)\n  session_home: session (default)\n  runtime: container-rootless (project)\n  env: none (default)\n  auth: not-needed (default)\nmock-noskills\n  command: mock-noskills (default)\n  session_home: session (default)\n  runtime: container-rootless (project)\n  env: none (default)\n  auth: not-needed (default)\nopencode\n  command: opencode (default)\n  session_home: host (engine)\n  runtime: container-rootless (project)\n  env: none (default)\n  auth: not-needed (default)\n  agents: plain\n"
        );
    }

    #[test]
    fn test_a_bare_engine_lists_and_json_carries_the_same_rows() {
        let bare = go(&["engine"], JSON, &[ENGINE_HOME, ENGINE_PROJECT]);
        let list = go(&["engine", "list"], JSON, &[ENGINE_HOME, ENGINE_PROJECT]);
        assert_eq!(bare.out, list.out);
        // OD-40: the AIP resources, named under the project the daemon names.
        assert_eq!(
            bare.json()["engines"][0]["name"],
            "projects/calm-otter/engines/claude-code"
        );
        let show = go(
            &["engine", "show", "opencode"],
            JSON,
            &[ENGINE_HOME, ENGINE_PROJECT],
        );
        assert_eq!(show.json()["name"], "projects/calm-otter/engines/opencode");
        assert_eq!(show.json()["session_home"]["value"], "host");
    }

    // OD-40 (design §5.6): `engine show`'s json is the `Engine` message's proto3
    // JSON, exactly: a default left out (an unset model is no `value`), the
    // auth state the enum's name, an agent row's name `agent`, a permissions
    // document the object itself.
    #[test]
    fn test_engine_show_json_is_the_engine_resource() {
        let show = go(
            &["engine", "show", "claude-code"],
            JSON,
            &[ENGINE_HOME, ENGINE_PROJECT],
        );
        let engine = show.json();
        assert_eq!(engine["name"], "projects/calm-otter/engines/claude-code");
        assert_eq!(
            engine["command"],
            serde_json::json!({"value": "/opt/claude", "source": "engine"})
        );
        assert_eq!(
            engine["auth"],
            serde_json::json!({"value": {"word": "none"}, "source": "engine", "state": "NOT_NEEDED"})
        );
        assert_eq!(
            engine["agents"][0],
            serde_json::json!({
                "agent": "dev",
                "model": {"value": "haiku", "source": "agent"},
                "effort": {"source": "default"},
                "permissions": {"value": {"defaultMode": "acceptEdits"}, "source": "default"},
                "runtime": {
                    "value": "host",
                    "source": "agent",
                    "overrides": "container-rootless",
                    "overrides_source": "project",
                    "change": "weakens",
                },
                "env": {
                    "value": ["HTTPS_PROXY"],
                    "source": "engine",
                    "keys": [{"variable": "HTTPS_PROXY", "source": "engine"}],
                },
                "session_home": {"value": "session", "source": "default"},
            })
        );
    }

    // Outside a project the names are rooted at the home (OQ-3); a text answer
    // asks for no name at all, so it reaches no daemon (OQ-2).
    #[test]
    fn test_engine_names_are_rooted_where_the_names_say_and_text_asks_for_none() {
        let home = go_named(&["engine", "show", "opencode"], JSON, &Home);
        assert_eq!(home.json()["name"], "users/me/engines/opencode");
        let text = go_named(&["engine", "list"], TEXT, &Unreachable);
        assert_eq!(text.exit, Ok(0), "{}", text.err);
        let json = go_named(&["engine", "list"], JSON, &Unreachable);
        assert_eq!(json.exit, Err("daemon-api-unreached:10".to_string()));
    }

    // An agent that makes a launch more contained than the layers below says so; an
    // engine no agent is bound to says it has none.
    #[test]
    fn test_engine_show_says_when_an_agent_strengthens_isolation_and_when_there_is_none() {
        let layers = [
            "schema_version: 9\nruntime: host\n",
            "schema_version: 9\nagents:\n  tight:\n    engine: claude-code\n    runtime: container-rootless\n    profiles: [dev]\n",
        ];
        let ran = go(&["engine", "show", "claude-code"], TEXT, &layers);
        assert!(
            ran.out.contains(
                "      runtime: container-rootless (agent; replaces project host: strengthens isolation)\n"
            ),
            "{}",
            ran.out
        );
        let none = go(&["engine", "show", "opencode"], TEXT, &layers);
        assert!(none.out.ends_with("  agents: none\n"), "{}", none.out);
    }
}

mod api {
    use super::super::*;
    use crate::api::tests::json_of;
    use serde_json::json;

    fn setting(value: serde_json::Value, state: &'static str) -> AuthSetting {
        AuthSetting {
            value,
            source: "engine",
            state,
            via: None,
        }
    }

    // A word is the word; a source its variables and file; each state the
    // enum's value, an unknown one unspecified (left out).
    #[test]
    fn test_an_auth_source_is_its_value_and_its_state_the_enum() {
        assert_eq!(
            json_of(&auth(&setting(json!("login"), "present"))),
            json!({"value": {"word": "login"}, "source": "engine", "state": "PRESENT"})
        );
        let mut source = setting(json!({"env": ["A", "B"], "file": "~/k"}), "missing");
        source.via = Some("A".to_string());
        assert_eq!(
            json_of(&auth(&source)),
            json!({"value": {"env": ["A", "B"], "file": "~/k"}, "source": "engine", "state": "MISSING", "via": "A"})
        );
        assert_eq!(
            json_of(&auth(&setting(json!({"env": ["A"]}), "not-needed"))),
            json!({"value": {"env": ["A"]}, "source": "engine", "state": "NOT_NEEDED"})
        );
        assert_eq!(
            json_of(&auth(&setting(json!(7), "unheard-of"))),
            json!({"value": {}, "source": "engine"})
        );
    }
}
