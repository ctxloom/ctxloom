//! `run`'s tests, beside its verbs.

mod read {
    use crate::tests::*;

    // Ruling 153: a dry run whose plan found nothing fatal would launch, the
    // distinguishing success 1 (said only under `--detailed-exitcode`).
    #[test]
    fn test_a_dry_run_that_would_launch_would_change() {
        assert_eq!(go(&["run", "--dry-run", "hello"], JSON, BARE).code(), 1);
        assert_eq!(go(&["run", "--dry-run", "hello"], TEXT, BARE).code(), 1);
        assert_eq!(go(&["context"], JSON, BARE).code(), 0, "a read is ok");
    }

    // run.feature: `run --dry-run` previews; without it this build refuses.
    #[test]
    fn test_a_dry_run_carries_its_arguments_into_the_plan() {
        let plan = json_of(&["run", "--dry-run", "hello", "big", "world"]);
        assert_eq!(plan["prompt"], "hello big world");
        assert_eq!(
            plan["profiles"],
            serde_json::json!(["projects/calm-otter/bundles/project/profiles/dev"])
        );
        assert_eq!(
            json_of(&["run", "--dry-run", "--prompt", "pp", "hello"])["prompt"],
            "pp"
        );
        assert_eq!(
            json_of(&["run", "--dry-run", "-r", "review"])["prompt"],
            "REVIEW-BODY\n"
        );
        let agent = json_of(&[
            "run",
            "--dry-run",
            "--workspace",
            "worktree",
            "--agent",
            "dev",
        ]);
        assert_eq!(
            (agent["agent"].as_str(), agent["workspace"].as_str()),
            (Some("projects/calm-otter/agents/dev"), Some("worktree"))
        );
        let only = json_of(&[
            "run",
            "--dry-run",
            "-f",
            "demo#fragments/golang",
            "-t",
            "go",
        ]);
        assert_eq!(
            only["fragments"],
            serde_json::json!(["ctxloom+local:demo#fragments/golang"])
        );
        assert!(
            text_of(&["run", "--dry-run", "hello"]).starts_with("=== Engine ===\nclaude-code\n")
        );
        assert!(
            text_of(&["run", "--dry-run", "--agent", "dev"]).starts_with("=== Agent ===\ndev (")
        );
        assert_eq!(
            go(&["run"], JSON, BARE).refused(),
            "library-application-run-needs-dry-run:20"
        );
    }

    // Owner ruling of 2026-11-13: the dry run's Settings say the agent's
    // session_home with the place that said it.
    #[test]
    fn test_a_dry_run_s_settings_say_the_session_home_and_its_place() {
        let home = "schema_version: 9\nengines:\n  opencode:\n    session_home: host\nagents:\n  own:\n    engine: claude-code\n    session_home: host\n  kept:\n    engine: opencode\n";
        let layers = [home, "schema_version: 9\n"];
        let own = go(&["run", "--dry-run", "--agent", "own", "hi"], TEXT, &layers);
        assert!(
            own.out.contains("  session_home  host  (agents.own)\n"),
            "{}",
            own.out
        );
        let kept = go(
            &["run", "--dry-run", "--agent", "kept", "hi"],
            TEXT,
            &layers,
        );
        assert!(
            kept.out
                .contains("  session_home  host  (engines.opencode)\n"),
            "{}",
            kept.out
        );
        let json = go(&["run", "--dry-run", "--agent", "own", "hi"], JSON, &layers);
        assert_eq!(
            json.json()["settings"]["session_home"],
            serde_json::json!({"value": "host", "source": "agent"})
        );
    }

    // run.feature (OD-38): `--no-context` reaches the plan, which then holds
    // none of the default agent's profile; beside a profile it is refused.
    #[test]
    fn test_a_dry_run_with_no_context_holds_none_of_the_profile() {
        let plan = json_of(&["run", "--dry-run", "--no-context", "hello"]);
        assert_eq!(plan["prompt"], "hello");
        assert!(plan.get("profiles").is_none(), "{plan}");
        assert!(plan.get("context").is_none(), "{plan}");
        assert_eq!(
            go(
                &["run", "--dry-run", "--no-context", "-p", "dev", "hello"],
                JSON,
                BARE
            )
            .refused(),
            "library-application-no-context-exclusive:20"
        );
    }

    // fault_tolerance.feature: fatal findings abort a preview (exit 21) after the
    // plan is printed; --degraded previews past them.
    #[test]
    fn test_fatal_findings_print_the_plan_and_abort() {
        let ran = go(&["run", "--dry-run", "-p", "broken"], JSON, BARE);
        assert_eq!(ran.refused(), "library-application-startup-aborted:21");
        assert_eq!(ran.json()["findings"][0]["fatal"], true);
        // hand-test 2 2026-11-11: the refusal alone names the fatal finding; no
        // warning says it first.
        assert!(!ran.err.contains("\"warning\""), "{}", ran.err);
        let degraded = How {
            degraded: true,
            ..JSON
        };
        let past = go(&["run", "--dry-run", "-p", "broken"], degraded, BARE);
        assert_eq!(past.code(), 1, "past its findings it would launch");
        // OD-40: proto3 JSON leaves a finding's false `fatal` out (OQ-7).
        let finding = &past.json()["findings"][0];
        assert!(finding.get("kind").is_some(), "{finding}");
        assert!(finding.get("fatal").is_none(), "{finding}");
    }

    // OD-40 (design §5.9): `run --dry-run` is `CreateRun(validate_only)`, its
    // json the `Run` it would create, named under the project the daemon
    // names (owner ruling 2026-11-14: "dry run *absolutely* calls (and
    // spawns, if necessary) a daemon"), its ID not yet given (`-`: nothing is
    // created); its agent, engine and profiles resource names (design §2);
    // the token estimate a number (uint32, OQ-12), and the agent's
    // `settings` an `AgentSettings`, each value with the layer that said it,
    // an unset one no `value`, an env key's name `variable` (AIP-122).
    #[test]
    fn test_a_dry_run_is_the_run_it_would_create() {
        let plan = go(
            &["run", "--dry-run", "--agent", "dev", "hello"],
            JSON,
            &[ENGINE_HOME, ENGINE_PROJECT],
        );
        assert_eq!(plan.code(), 1, "{}", plan.err);
        let run = plan.json();
        assert_eq!(run["name"], "projects/calm-otter/runs/-", "{run}");
        assert_eq!(run["agent"], "projects/calm-otter/agents/dev");
        assert_eq!(run["engine"], "projects/calm-otter/engines/claude-code");
        assert_eq!(run["runtime"], "host");
        assert_eq!(
            run["profiles"],
            serde_json::json!(["projects/calm-otter/bundles/project/profiles/dev"])
        );
        assert_eq!(run["prompt"], "hello");
        assert!(run["tokens"].as_u64().is_some_and(|n| n > 0), "{run}");
        assert!(run["context"].is_string(), "{run}");
        assert_eq!(
            run["settings"],
            serde_json::json!({
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
                "approval_timeout": {"value": "5m", "source": "default"},
            })
        );
        // A run with no agent has no settings, and an empty selection prints
        // no list at all.
        let bare = go(
            &["run", "--dry-run", "-f", "demo#fragments/testing", "hello"],
            JSON,
            &["schema_version: 9\n"],
        );
        assert_eq!(bare.code(), 1, "{}", bare.err);
        let run = bare.json();
        assert!(run.get("settings").is_none(), "{run}");
        assert!(run.get("profiles").is_none(), "{run}");
    }

    // Owner rulings 2026-11-14: a dry run never mints a project id, so in a
    // project nothing has named its `Run.name` is unresolved and prints as
    // null, never left out, and its references keep their configured text
    // (ruling: "its references print as configured text"; OQ-16).
    #[test]
    fn test_an_unnamed_projects_dry_run_prints_a_null_name_and_configured_references() {
        let plan = go_in(
            &["run", "--dry-run", "--agent", "dev", "hello"],
            JSON,
            &[ENGINE_HOME, ENGINE_PROJECT],
            &Unnamed,
        );
        assert_eq!(plan.code(), 1, "{}", plan.err);
        let run = plan.json();
        let name = run.as_object().and_then(|run| run.get("name"));
        assert_eq!(name, Some(&serde_json::Value::Null), "{run}");
        assert_eq!(run["agent"], "dev");
        assert_eq!(run["engine"], "claude-code");
        assert_eq!(run["profiles"], serde_json::json!(["dev"]));
    }

    // Owner ruling 2026-11-14: every dry run calls the daemon, text or json
    // (a text one prints no name, but asks all the same), before it plans,
    // and its failure is the command's.
    #[test]
    fn test_every_dry_run_asks_the_daemon_text_or_json() {
        let line = ["run", "--dry-run", "--agent", "dev", "hello"];
        let layers = [ENGINE_HOME, ENGINE_PROJECT];
        for how in [JSON, TEXT] {
            assert_eq!(
                go_in(&line, how, &layers, &Unreachable).refused(),
                "daemon-api-unreached:10"
            );
        }
        // Asked first: a plan the library refuses reaches it all the same.
        let refused = ["run", "--dry-run", "--agent", "nobody", "hello"];
        assert_eq!(
            go_in(&refused, TEXT, &layers, &Unreachable).refused(),
            "daemon-api-unreached:10"
        );
        let text = go_in(&line, TEXT, &layers, &Unnamed);
        assert_eq!(text.code(), 1, "{}", text.err);
    }
}

// A named project's dry run with no `--agent` names none (its plan's agent
// is the one asked for): an empty reference stays empty, never a name below
// the parent.
mod unset {
    use crate::tests::*;

    #[test]
    fn test_a_dry_run_asking_no_agent_names_no_agent() {
        let plan = go(&["run", "--dry-run", "hello"], JSON, BARE);
        assert_eq!(plan.code(), 1, "{}", plan.err);
        let run = plan.json();
        assert_eq!(run["name"], "projects/calm-otter/runs/-", "{run}");
        assert!(run.get("agent").is_none(), "{run}");
    }
}

// Step 10a U2: the dry run is `CreateRun` validating only, a terminal
// session's, every flag carried.
mod asks {
    use crate::tests::{asked_once, degraded};
    use daemon_proto::v1;
    #[allow(unused_imports)]
    use daemon_proto::Carries as _;

    #[test]
    fn test_a_dry_run_asks_create_run_validating_only_with_every_flag() {
        let (_, run) = asked_once::<v1::CreateRunRequest>(&[
            "run",
            "--dry-run",
            "--engine",
            "opencode",
            "--tag",
            "go",
            "hello",
        ]);
        assert_eq!(run.mode, v1::run::Mode::Terminal as i32);
        assert!(run.validate_only);
        assert_eq!(run.engine, "opencode");
        assert_eq!(run.tags, ["go"]);
        assert_eq!(run.prompt, "hello");
        assert_eq!(run.caller_globals().cloned(), degraded());
    }
}
