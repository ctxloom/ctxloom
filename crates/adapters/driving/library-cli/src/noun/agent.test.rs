//! `agent`'s tests, beside its verbs.

mod read {
    use crate::tests::*;

    // agent.feature; OD-40 (design §5.6): a list is `ListAgentsResponse`, each
    // agent named under the project the daemon names, the default agent
    // saying so; `show` is the `Agent` resource itself, its definition at the
    // top and what it resolves to under `resolved`.
    #[test]
    fn test_agent_list_and_show() {
        let agents = json_of(&["agent", "list"]);
        assert_eq!(
            agents["agents"][0],
            serde_json::json!({
                "name": "projects/calm-otter/agents/dev",
                "engine": "claude-code",
                "profiles": ["dev"],
                "runtime": "host",
                "default": true,
            })
        );
        assert_eq!(json_of(&["agent"]), agents);
        let show = json_of(&["agent", "show", "dev"]);
        assert_eq!(show["name"], "projects/calm-otter/agents/dev");
        assert_eq!(show["engine"], "claude-code");
        assert_eq!(show["default"], true);
        assert!(show.get("definition").is_none(), "{show}");
        assert_eq!(show["resolved"]["engine"], "claude-code");
        assert!(text_of(&["agent", "show", "dev"])
            .starts_with("Agent: dev\nEngine (declared): claude-code\n"));
        assert!(text_of(&["agent"]).starts_with("Agents (1):\n"));
    }

    // OD-40 (design §3.7): `agent show`'s json is the `Agent` message's proto3
    // JSON, exactly: the engine blocks keyed by engine, a default left out (an
    // unset effort is no `value`), `default` printed false, an env key's name
    // `variable` (AIP-122), a permissions document the object itself.
    #[test]
    fn test_agent_show_json_is_the_agent_resource() {
        let show = go(
            &["agent", "show", "dev"],
            JSON,
            &[ENGINE_HOME, ENGINE_PROJECT],
        );
        assert_eq!(show.code(), 0, "{}", show.err);
        let agent = show.json();
        assert_eq!(agent["name"], "projects/calm-otter/agents/dev");
        assert_eq!(
            agent["engines"],
            serde_json::json!({"claude-code": {"model": "haiku"}})
        );
        assert_eq!(agent["default"], false);
        let resolved = &agent["resolved"];
        assert_eq!(
            resolved["model"],
            serde_json::json!({"value": "haiku", "source": "agent"})
        );
        assert_eq!(resolved["effort"], serde_json::json!({"source": "default"}));
        assert_eq!(
            resolved["permissions"],
            serde_json::json!({"value": {"defaultMode": "acceptEdits"}, "source": "default"})
        );
        assert_eq!(
            resolved["env"],
            serde_json::json!({
                "value": ["HTTPS_PROXY"],
                "source": "engine",
                "keys": [{"variable": "HTTPS_PROXY", "source": "engine"}],
            })
        );
        assert_eq!(resolved["command"], "/opt/claude");
        assert!(resolved.get("name").is_none(), "{resolved}");
    }

    // An engine block's own document is printed as written, whatever its
    // keys; a listing with no agents is the empty `ListAgentsResponse`.
    #[test]
    fn test_an_engine_block_is_printed_as_written_and_no_agents_is_an_empty_list() {
        let layers = ["schema_version: 9\nagents:\n  rev:\n    engine: claude-code\n    engines:\n      claude-code:\n        effort: low\n        permissions:\n          defaultMode: plan\n          deny: [WebFetch]\n      opencode:\n        model: opencode/big-pickle\n"];
        let listed = go(&["agent", "list"], JSON, &layers);
        assert_eq!(
            listed.json()["agents"][0]["engines"],
            serde_json::json!({
                "claude-code": {"effort": "low", "permissions": {"defaultMode": "plan", "deny": ["WebFetch"]}},
                "opencode": {"model": "opencode/big-pickle"},
            })
        );
        let none = go(&["agent", "list"], JSON, &["schema_version: 9\n"]);
        assert_eq!(none.out, "{}\n");
    }

    // Outside a project an agent is named under the home (OQ-3).
    #[test]
    fn test_agent_names_are_rooted_where_the_names_say() {
        let home = go_named(&["agent", "show", "dev"], JSON, &Home);
        assert_eq!(home.json()["name"], "users/me/agents/dev");
    }
}

mod write {
    use crate::tests::write_verbs::*;
    use crate::tests::TEXT;
    use library_application::fakes::ConfigScope as Scope;
    use library_application::fakes::{Setup, ENVELOPE};

    // agent.feature
    #[test]
    fn test_agent_verbs() {
        let (library, mem) = world();
        assert_eq!(
            text(
                &library,
                &[
                    "agent",
                    "create",
                    "dev",
                    "--engine",
                    "claude-code",
                    "--profiles",
                    "dev",
                    "--runtime",
                    "container-rootless"
                ]
            ),
            "Created agent \"dev\" (engine: claude-code, profiles: dev, runtime: container-rootless)\n"
        );
        assert_eq!(
            text(
                &library,
                &[
                    "agent", "edit", "dev", "--profiles", "dev,dev", "--runtime", "host",
                    "--model", "haiku",
                    "--env", "A", "--env", "B", "--surface", "context=system-prompt", "--root", "mcp=project-root",
                ]
            ),
            "Updated agent \"dev\" (engine: claude-code, profiles: dev, dev, runtime: host, engines.claude-code: {model: haiku}, env: A, B)\n"
        );
        // OD-40 (design §4): a write answers its method's response, the
        // resource after the write and `applied` printed.
        let doc = json(&library, &["agent", "edit", "dev", "--env", ""]);
        assert_eq!(doc["agent"]["name"], "projects/calm-otter/agents/dev");
        assert_eq!(doc["agent"]["engine"], "claude-code");
        assert_eq!(doc["applied"], true);
        let bad = refused_code(
            &library,
            &["agent", "edit", "dev", "--surface", "nope"],
            TEXT,
        );
        assert!(bad.contains("flag-pair"), "{bad}");
        let bad = refused_code(&library, &["agent", "edit", "dev", "--root", "nope"], TEXT);
        assert!(bad.contains("flag-pair"), "{bad}");
        assert_eq!(
            text(&library, &["agent", "default"]),
            "No default agent set.\n"
        );
        assert_eq!(
            text(&library, &["agent", "default", "dev"]),
            "Set default agent to \"dev\".\n"
        );
        assert_eq!(
            text(&library, &["agent", "default"]),
            "Default agent: dev\n"
        );
        let ran = run_on(&library, &["agent", "default", "ghost"], TEXT);
        assert!(ran.err.contains("not defined yet"), "{}", ran.err);
        assert!(mem
            .config(Scope::Project)
            .unwrap()
            .contains("default_agent: ghost"));
        let ran = run_on(&library, &["agent", "default", "dev"], TEXT);
        assert!(!ran.err.contains("not defined yet"), "{}", ran.err);
        assert_eq!(
            text(&library, &["agent", "remove", "dev"]),
            "Would remove agent \"dev\".\n\nNothing was removed. Re-run with --yes to apply:\n  ctxloom agent remove dev --yes\n"
        );
        // OD-40: a removal's preview and its outcome are `DeleteAgentResponse`,
        // `applied` false and the command that applies, then true.
        assert_eq!(
            json(&library, &["agent", "remove", "dev"]),
            serde_json::json!({
                "agent": {"name": "projects/calm-otter/agents/dev"},
                "applied": false,
                "apply_command": "ctxloom agent remove dev --yes",
            })
        );
        assert_eq!(
            json(&library, &["agent", "remove", "dev", "--yes"]),
            serde_json::json!({
                "agent": {"name": "projects/calm-otter/agents/dev"},
                "applied": true,
            })
        );
        let made = json(
            &library,
            &["agent", "create", "dev", "--engine", "claude-code"],
        );
        assert_eq!(
            made,
            serde_json::json!({
                "agent": {
                    "name": "projects/calm-otter/agents/dev",
                    "engine": "claude-code",
                    "default": true,
                },
                "applied": true,
            })
        );
        assert_eq!(
            text(&library, &["agent", "remove", "dev", "--yes"]),
            "Removed agent \"dev\"\n"
        );
    }

    // j002000: a swap to an engine that cannot carry a profile's hook is shown
    // in text as what it gives up and the command that applies it, and writes
    // nothing.
    #[test]
    fn test_an_engine_swap_that_gives_something_up_previews_in_text() {
        let house: &[(&str, &str)] = &[
            ENVELOPE,
            ("fragments/guide.md", "GUIDE"),
            (
                "hooks/session_end/h0.yaml",
                "type: command\ncommand: echo END\n",
            ),
        ];
        let project: &[(&str, &str)] = &[ENVELOPE, ("profiles/team.yaml", "bundles: [house]\n")];
        let (library, mem) = Setup {
            bundles: &[("house", house), ("project", project)],
            layers: &[
                "schema_version: 9\nagents:\n  dev:\n    engine: claude-code\n    profiles: [team]\n",
            ],
            ..Setup::default()
        }
        .fixture();
        let library = library.with_hook_losses(
            [(
                "opencode".to_string(),
                vec![("session_end".to_string(), "no session_end".to_string())],
            )]
            .into_iter()
            .collect(),
        );
        let before = mem.config(Scope::Project);
        let ran = run_on(
            &library,
            &["agent", "edit", "dev", "--engine", "opencode"],
            TEXT,
        );
        assert_eq!(
            ran.out,
            "Moving agent \"dev\" from claude-code to opencode gives up what opencode cannot carry:\n  hooks: session_end (no session_end)\n\nNothing was changed. Re-run with --yes to apply:\n  ctxloom agent edit dev --engine opencode --yes\n"
        );
        assert_eq!(mem.config(Scope::Project), before, "nothing is written");
        // OD-40: the swap's preview is `UpdateAgentResponse`, `applied` false,
        // the engines it moves between and what it gives up.
        let ran = run_on(
            &library,
            &["agent", "edit", "dev", "--engine", "opencode"],
            crate::tests::JSON,
        );
        assert_eq!(ran.code(), 1, "a preview would change: {}", ran.err);
        assert_eq!(
            ran.json(),
            serde_json::json!({
                "agent": {"name": "projects/calm-otter/agents/dev"},
                "applied": false,
                "apply_command": "ctxloom agent edit dev --engine opencode --yes",
                "from": "claude-code",
                "to": "opencode",
                "not_carried": [{
                    "engine": "opencode",
                    "surface": "hooks",
                    "event": "session_end",
                    "reason": "no session_end",
                }],
            })
        );
        assert_eq!(mem.config(Scope::Project), before, "nothing is written");
    }
}
