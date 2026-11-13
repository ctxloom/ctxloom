//! `agent`'s tests, beside its verbs.

mod read {
    use crate::tests::*;

    // agent.feature (moved from the four-noun test of lib.test.rs, unchanged)
    #[test]
    fn test_agent_list_and_show() {
        let agents = json_of(&["agent", "list"]);
        assert_eq!(agents[0]["name"], "dev");
        assert_eq!(json_of(&["agent"]), agents);
        let show = json_of(&["agent", "show", "dev"]);
        assert_eq!(show["resolved"]["engine"], "claude-code");
        assert!(text_of(&["agent", "show", "dev"])
            .starts_with("Agent: dev\nEngine (declared): claude-code\n"));
        assert!(text_of(&["agent"]).starts_with("Agents (1):\n"));
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
        let doc = json(&library, &["agent", "edit", "dev", "--env", ""]);
        assert_eq!(doc["name"], "dev");
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
    }
}
