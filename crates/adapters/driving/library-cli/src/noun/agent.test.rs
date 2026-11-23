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
                "engine": "projects/calm-otter/engines/claude-code",
                "profiles": ["projects/calm-otter/bundles/project/profiles/dev"],
                "runtime": "host",
                "default": true,
            })
        );
        assert_eq!(json_of(&["agent"]), agents);
        let show = json_of(&["agent", "show", "dev"]);
        assert_eq!(show["name"], "projects/calm-otter/agents/dev");
        assert_eq!(show["engine"], "projects/calm-otter/engines/claude-code");
        assert_eq!(show["default"], true);
        assert!(show.get("definition").is_none(), "{show}");
        assert_eq!(
            show["resolved"]["engine"],
            "projects/calm-otter/engines/claude-code"
        );
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
        assert_eq!(show.code(), 16, "{}", show.err);
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

    // Owner ruling of 2026-11-13 (superseding 44(b) for this key): an agent's
    // binding says its own session_home, and it resolves with the layer
    // that said it.
    #[test]
    fn test_an_agent_s_session_home_is_shown_as_said_and_resolved_with_its_source() {
        let home = "schema_version: 9\nengines:\n  claude-code:\n    session_home: host\nagents:\n  own:\n    engine: claude-code\n    session_home: session\n  kept:\n    engine: claude-code\n";
        let layers = [home, "schema_version: 9\n"];
        let listed = go(&["agent", "list"], JSON, &layers).json();
        let row = |name: &str| {
            listed["agents"]
                .as_array()
                .unwrap()
                .iter()
                .find(|a| a["name"].as_str().unwrap().ends_with(name))
                .cloned()
                .unwrap()
        };
        assert_eq!(row("/own")["session_home"], "session");
        assert!(row("/kept").get("session_home").is_none());
        let own = go(&["agent", "show", "own"], JSON, &layers).json();
        assert_eq!(
            own["resolved"]["session_home"],
            serde_json::json!({"value": "session", "source": "agent"})
        );
        let kept = go(&["agent", "show", "kept"], JSON, &layers).json();
        assert_eq!(
            kept["resolved"]["session_home"],
            serde_json::json!({"value": "host", "source": "engine"})
        );
        let text = go(&["agent", "show", "kept"], TEXT, &layers).out;
        assert!(text.contains("Session home: host (engine)\n"), "{text}");
        let listing = go(&["agent", "list"], TEXT, &layers).out;
        assert!(listing.contains("    session_home: session\n"), "{listing}");
    }

    // Outside a project an agent is named under the home (OQ-3), and so are
    // the resources it references.
    #[test]
    fn test_agent_names_are_rooted_where_the_names_say() {
        let home = go_named(&["agent", "show", "dev"], JSON, &Home);
        assert_eq!(home.json()["name"], "users/me/agents/dev");
        assert_eq!(home.json()["engine"], "users/me/engines/claude-code");
    }

    // Design §2 (AIP-122): an agent's references are resource names, the
    // engine's and each profile's; a reference that names no place keeps its
    // configured text (OQ-16), and an agent that names no engine names none.
    #[test]
    fn test_an_agent_s_references_are_resource_names() {
        let layers = ["schema_version: 9\nagents:\n  dev:\n    engine: opencode\n    profiles: [dev, acme/base, \"demo#profiles/team\"]\n  bare:\n    profiles: [dev]\n"];
        let listed = go(&["agent", "list"], JSON, &layers).json();
        let row = |name: &str| {
            listed["agents"]
                .as_array()
                .unwrap()
                .iter()
                .find(|a| a["name"].as_str().unwrap().ends_with(name))
                .cloned()
                .unwrap()
        };
        assert_eq!(
            row("/dev")["engine"],
            "projects/calm-otter/engines/opencode"
        );
        assert_eq!(
            row("/dev")["profiles"],
            serde_json::json!([
                "projects/calm-otter/bundles/project/profiles/dev",
                "acme/base",
                "projects/calm-otter/bundles/demo/profiles/team",
            ])
        );
        assert!(row("/bare").get("engine").is_none(), "{listed}");
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
            "Created agent \"dev\" (engine: claude-code, profiles: project#profiles/dev, runtime: container-rootless)\n"
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
            "Updated agent \"dev\" (engine: claude-code, profiles: project#profiles/dev, project#profiles/dev, runtime: host, engines.claude-code: {model: haiku}, env: A, B)\n"
        );
        // OD-40 (design §4): a write answers its method's response, the
        // resource after the write and `applied` printed.
        let doc = json(&library, &["agent", "edit", "dev", "--env", ""]);
        let (named, engine) = (&doc["agent"]["name"], &doc["agent"]["engine"]);
        assert_eq!(named, "projects/calm-otter/agents/dev");
        assert_eq!(engine, "projects/calm-otter/engines/claude-code");
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
        // OD-40: its preview, then its outcome, are `DeleteAgentResponse`s.
        assert_eq!(
            json(&library, &["agent", "remove", "dev"]),
            serde_json::json!({
                "agent": {"name": "projects/calm-otter/agents/dev"}, "applied": false,
                "apply_command": "ctxloom agent remove dev --yes",
                "needs": [crate::tests::confirm_need("agent \"dev\"")],
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
                    "engine": "projects/calm-otter/engines/claude-code",
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

    // Owner ruling of 2026-11-13: `--session-home` writes the agent's own
    // word (a config word, never a boolean) into its binding.
    #[test]
    fn test_session_home_is_written_to_the_agent() {
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
                    "--session-home",
                    "session"
                ]
            ),
            "Created agent \"dev\" (engine: claude-code, session_home: session)\n"
        );
        assert!(mem
            .config(Scope::Project)
            .unwrap()
            .contains("session_home: session"));
        let doc = json(
            &library,
            &["agent", "edit", "dev", "--session-home", "session"],
        );
        assert_eq!(doc["agent"]["session_home"], "session");
        // an empty word clears it
        json(&library, &["agent", "edit", "dev", "--session-home", ""]);
        assert!(!mem.config(Scope::Project).unwrap().contains("session_home"));
        let bad = refused_code(
            &library,
            &["agent", "edit", "dev", "--session-home", "elsewhere"],
            TEXT,
        );
        assert!(bad.contains("session-home"), "{bad}");
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
        assert_eq!(ran.code(), 17, "a preview would change: {}", ran.err);
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
                "needs": [{"id": "confirm", "confirm": {
                    "text": "Apply the move of agent \"dev\" from claude-code to opencode, giving up what opencode cannot carry?",
                    "args": [
                        {"name": "agent", "value": "dev"},
                        {"name": "from", "value": "claude-code"},
                        {"name": "to", "value": "opencode"},
                    ],
                }}],
            })
        );
        assert_eq!(mem.config(Scope::Project), before, "nothing is written");
    }
}

// Each view's mapping to its message, field by field (OD-40): what the
// binary's tests alone reached.
mod mapping {
    use library_api::resources::agent;
    use library_application::{AgentRow, AgentShow, NotCarried, Requirement, RequirementSource};
    use serde_json::json;

    const PARENT: &str = "projects/p";

    fn json_of<M: serde::Serialize>(message: &M) -> serde_json::Value {
        let mut out = Vec::new();
        crate::api::print(&mut out, message).unwrap();
        serde_json::from_slice(&out).unwrap()
    }

    fn row() -> AgentRow {
        AgentRow {
            name: "dev".to_string(),
            engine: String::new(),
            profiles: Vec::new(),
            runtime: String::new(),
            driving: "tasks".to_string(),
            env: vec!["HTTPS_PROXY".to_string()],
            session_home: String::new(),
            engines: serde_json::Value::Null,
            engine_lines: Vec::new(),
        }
    }

    #[test]
    fn test_an_agent_keeps_its_driving_and_its_env() {
        assert_eq!(
            json_of(&agent(PARENT, &row(), "")),
            json!({
                "name": "projects/p/agents/dev",
                "driving": "tasks",
                "env": ["HTTPS_PROXY"],
                "default": false,
            })
        );
    }

    #[test]
    fn test_a_shown_agent_carries_what_it_needs_and_what_it_loses() {
        let show = AgentShow {
            definition: row(),
            resolved: None,
            resolution_error: "no such profile".to_string(),
            requirements: vec![Requirement {
                command: "gopls".to_string(),
                sources: vec![RequirementSource {
                    kind: "mcp".to_string(),
                    reference: "demo#mcp/go".to_string(),
                }],
            }],
            requirements_error: "unreadable".to_string(),
            not_carried: vec![NotCarried {
                engine: "opencode".to_string(),
                surface: "hooks".to_string(),
                event: "session_end".to_string(),
                reason: "none native".to_string(),
            }],
        };
        let shown = json_of(&library_api::resources::agent_shown(PARENT, &show, ""));
        assert_eq!(shown["resolution_error"], "no such profile", "{shown}");
        assert_eq!(
            shown["requirements"],
            json!([{
                "command": "gopls",
                "sources": [{"kind": "mcp", "reference": "demo#mcp/go"}],
            }])
        );
        assert_eq!(shown["requirements_error"], "unreadable");
        assert_eq!(
            shown["not_carried"],
            json!([{
                "engine": "opencode",
                "surface": "hooks",
                "event": "session_end",
                "reason": "none native",
            }])
        );
    }
}

// Step 10a U2: each read's request carries the command's global flags.
mod asks {
    use crate::tests::{asked_once, degraded};
    use daemon_proto::v1;
    #[allow(unused_imports)]
    use daemon_proto::Carries as _;

    #[test]
    fn test_agent_reads_carry_the_globals() {
        let (under, show) = asked_once::<v1::GetAgentRequest>(&["agent", "show", "dev"]);
        assert_eq!(under, "/agents/dev");
        assert_eq!(show.caller_globals().cloned(), degraded());
        let (under, list) = asked_once::<v1::ListAgentsRequest>(&["agent", "list"]);
        assert_eq!(under, "");
        assert_eq!(list.caller_globals().cloned(), degraded());
    }
}

// Step 10b U3: `agent create` asks the daemon's `CreateAgent` with the
// fields it was given; `agent edit` asks `UpdateAgent` with the fields it
// was given named by its mask (one given empty, a clear), previewed first
// and applied with the preview's etag, or in one call with `--yes`.
mod writes_asked {
    use crate::tests::{asked_once, degraded};
    use daemon_proto::v1;
    #[allow(unused_imports)]
    use daemon_proto::Carries as _;

    #[test]
    fn test_agent_create_asks_create_agent_with_its_fields() {
        let (under, made) = asked_once::<v1::CreateAgentRequest>(&[
            "agent",
            "create",
            "dev",
            "--engine",
            "claude-code",
            "--model",
            "haiku",
            "--effort",
            "low",
            "--profiles",
            "a,b",
            "--runtime",
            "host",
            "--surface",
            "context=system-prompt",
            "--root",
            "mcp=project-root",
            "--env",
            "A",
            "--session-home",
            "session",
            "--home",
        ]);
        assert_eq!(under, "");
        assert_eq!(made.agent_id, "dev");
        assert!(made.home);
        assert!(!made.validate_only);
        assert_eq!(made.caller_globals().cloned(), degraded());
        let agent = made.agent.unwrap();
        assert_eq!(agent.engine, "claude-code");
        assert_eq!(
            (agent.model.as_str(), agent.effort.as_str()),
            ("haiku", "low")
        );
        assert_eq!(agent.profiles, ["a", "b"]);
        assert_eq!(agent.runtime, "host");
        assert_eq!(
            agent.surfaces.get("context").map(String::as_str),
            Some("system-prompt")
        );
        assert_eq!(
            agent.roots.get("mcp").map(String::as_str),
            Some("project-root")
        );
        assert_eq!(agent.env, ["A"]);
        assert_eq!(agent.session_home, "session");
    }

    #[test]
    fn test_agent_edit_asks_its_given_fields_by_mask_previewed_then_applied() {
        use crate::tests::{go_served, Asks, Project, BARE, DEGRADED};
        let previewed = v1::UpdateAgentResponse {
            applied: Some(false),
            etag: "7".to_string(),
            ..v1::UpdateAgentResponse::default()
        };
        let asks = Asks::answering(vec![Box::new(previewed)], None);
        go_served(
            &[
                "agent",
                "edit",
                "dev",
                "--runtime",
                "host",
                "--model",
                "",
                "--env",
                "",
            ],
            DEGRADED,
            BARE,
            &Project,
            Some(&asks),
        );
        let sent = asks.requests::<v1::UpdateAgentRequest>();
        assert_eq!(sent.len(), 2, "a preview, then its apply");
        let preview = &sent[0];
        assert!(preview.validate_only);
        assert_eq!(preview.etag, "");
        assert_eq!(
            preview.update_mask.clone().unwrap().paths,
            ["model", "runtime", "env"]
        );
        let agent = preview.agent.clone().unwrap();
        assert_eq!(agent.runtime, "host");
        assert!(agent.model.is_empty() && agent.env.is_empty());
        assert_eq!(preview.caller_globals().cloned(), degraded());
        let apply = &sent[1];
        assert!(!apply.validate_only);
        assert_eq!(apply.etag, "7");
        assert_eq!(apply.update_mask, preview.update_mask);
        let (under, yes) = asked_once::<v1::UpdateAgentRequest>(&[
            "agent", "edit", "dev", "--engine", "opencode", "--home", "--yes",
        ]);
        assert_eq!(under, library_api::names::named_target("agents", "dev"));
        assert!(!yes.validate_only && yes.home);
        assert_eq!(yes.update_mask.unwrap().paths, ["engine"]);
    }

    // A swap's preview is printed, and nothing applied.
    #[test]
    fn test_agent_edit_whose_preview_is_a_swap_applies_nothing() {
        use crate::tests::{go_served, Asks, Project, BARE, TEXT};
        let swap = v1::UpdateAgentResponse {
            agent: Some(v1::Agent {
                name: "projects/p/agents/dev".to_string(),
                ..<v1::Agent as Default>::default()
            }),
            applied: Some(false),
            from: "claude-code".to_string(),
            to: "opencode".to_string(),
            apply_command: "ctxloom agent edit dev --engine opencode --yes".to_string(),
            ..v1::UpdateAgentResponse::default()
        };
        let asks = Asks::answering(vec![Box::new(swap)], None);
        let ran = go_served(
            &["agent", "edit", "dev", "--engine", "opencode"],
            TEXT,
            BARE,
            &Project,
            Some(&asks),
        );
        assert_eq!(ran.exit, Ok(17), "{}", ran.err);
        assert!(
            ran.out
                .starts_with("Moving agent \"dev\" from claude-code to opencode"),
            "{}",
            ran.out
        );
        assert_eq!(asks.requests::<v1::UpdateAgentRequest>().len(), 1);
    }

    /// A swap's preview as the daemon answers it: its Confirm need asked.
    fn swap_asking() -> v1::UpdateAgentResponse {
        v1::UpdateAgentResponse {
            agent: Some(v1::Agent {
                name: "projects/p/agents/dev".to_string(),
                ..<v1::Agent as Default>::default()
            }),
            applied: Some(false),
            from: "claude-code".to_string(),
            to: "opencode".to_string(),
            apply_command: "ctxloom agent edit dev --engine opencode --yes".to_string(),
            needs: vec![v1::Need {
                optional: false,
                id: "confirm".to_string(),
                kind: Some(v1::need::Kind::Confirm(v1::Confirm {
                    text: "Apply the move of agent \"dev\"?".to_string(),
                    code: String::new(),
                    args: Vec::new(),
                })),
            }],
            etag: "7".to_string(),
            ..v1::UpdateAgentResponse::default()
        }
    }

    fn applied() -> v1::UpdateAgentResponse {
        v1::UpdateAgentResponse {
            agent: Some(v1::Agent {
                name: "projects/p/agents/dev".to_string(),
                engine: "projects/p/engines/opencode".to_string(),
                ..<v1::Agent as Default>::default()
            }),
            applied: Some(true),
            ..v1::UpdateAgentResponse::default()
        }
    }

    fn swap_at_terminal(
        answers: Vec<Box<dyn std::any::Any>>,
        yes: Option<bool>,
    ) -> (crate::tests::Ran, Vec<v1::UpdateAgentRequest>) {
        use crate::tests::{go_served, Asks, Project, BARE, TEXT};
        let asks = Asks::answering(answers, yes);
        let ran = go_served(
            &["agent", "edit", "dev", "--engine", "opencode"],
            TEXT,
            BARE,
            &Project,
            Some(&asks),
        );
        (ran, asks.requests::<v1::UpdateAgentRequest>())
    }

    // Owner ruling 2026-11-18 (evening): at a terminal the swap's preview is
    // its Confirm need; on a yes the apply is sent with the answer and the
    // preview's etag.
    #[test]
    fn test_a_swap_confirmed_at_the_terminal_is_applied_with_the_previews_etag() {
        let (ran, sent) = swap_at_terminal(
            vec![Box::new(swap_asking()), Box::new(applied())],
            Some(true),
        );
        assert_eq!(ran.exit, Ok(16), "{}", ran.err);
        assert!(ran.out.contains("Updated agent \"dev\""), "{}", ran.out);
        assert_eq!(sent.len(), 2);
        assert!(sent[0].validate_only && !sent[1].validate_only);
        assert_eq!(sent[1].etag, "7");
        assert_eq!(
            sent[1].answers,
            [v1::Answer {
                need_id: "confirm".to_string(),
                answer: Some(v1::answer::Answer::Confirmed(true)),
            }]
        );
    }

    // A no at the terminal is the preview, printed, and nothing applied.
    #[test]
    fn test_a_swap_declined_at_the_terminal_is_its_preview_and_applies_nothing() {
        let (ran, sent) = swap_at_terminal(vec![Box::new(swap_asking())], Some(false));
        assert_eq!(ran.exit, Ok(17), "{}", ran.err);
        assert!(
            ran.out
                .starts_with("Moving agent \"dev\" from claude-code to opencode"),
            "{}",
            ran.out
        );
        assert_eq!(sent.len(), 1);
    }

    // What she confirmed changed before her answer reached the daemon: the
    // apply is refused stale, and she is shown the swap again, as it is now.
    #[test]
    fn test_a_confirmed_swap_refused_stale_is_previewed_again() {
        let (ran, sent) = swap_at_terminal(
            vec![
                Box::new(swap_asking()),
                Box::new(crate::tests::Stale),
                Box::new(swap_asking()),
            ],
            Some(true),
        );
        assert!(
            matches!(&ran.exit, Err(why) if why.starts_with("library-tree-plan-stale:48")),
            "{:?}",
            ran.exit
        );
        assert!(
            ran.out
                .starts_with("Moving agent \"dev\" from claude-code to opencode"),
            "{}",
            ran.out
        );
        assert_eq!(
            sent.iter().map(|r| r.validate_only).collect::<Vec<_>>(),
            [true, false, true]
        );
    }
}

// Step 10b U3: `agent default <name>` asks the daemon's `SetDefaultAgent`
// with the name, once; bare, it asks the daemon nothing (the current
// default is still read in process).
mod default_asked {
    use crate::tests::{go_served, Asks, Project, BARE, TEXT};
    use daemon_proto::v1;
    #[allow(unused_imports)]
    use daemon_proto::Carries as _;

    fn asked(line: &[&str]) -> Vec<v1::SetDefaultAgentRequest> {
        let asks = Asks::default();
        go_served(line, TEXT, BARE, &Project, Some(&asks));
        asks.requests::<v1::SetDefaultAgentRequest>()
    }

    #[test]
    fn test_agent_default_with_a_name_asks_set_default_agent() {
        let sent = asked(&["agent", "default", "reviewer"]);
        let [one] = &sent[..] else { panic!("{sent:?}") };
        assert_eq!(one.agent, "reviewer");
        assert!(!one.validate_only);
        assert!(asked(&["agent", "default"]).is_empty());
    }
}
