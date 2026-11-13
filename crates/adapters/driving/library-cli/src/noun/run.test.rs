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
        assert_eq!(plan["profiles"], serde_json::json!(["dev"]));
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
            (Some("dev"), Some("worktree"))
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
    // json the `Run` it would create: no name (nothing is created), the plan's
    // engine, profiles and context, the token estimate a number (uint32,
    // OQ-12), and the agent's `settings` an `AgentSettings`, each value with
    // the layer that said it, an unset one no `value`, an env key's name
    // `variable` (AIP-122).
    #[test]
    fn test_a_dry_run_is_the_run_it_would_create() {
        let plan = go(
            &["run", "--dry-run", "--agent", "dev", "hello"],
            JSON,
            &[ENGINE_HOME, ENGINE_PROJECT],
        );
        assert_eq!(plan.code(), 1, "{}", plan.err);
        let run = plan.json();
        assert!(run.get("name").is_none(), "{run}");
        assert_eq!(run["agent"], "dev");
        assert_eq!(run["engine"], "claude-code");
        assert_eq!(run["runtime"], "host");
        assert_eq!(run["profiles"], serde_json::json!(["dev"]));
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
}
