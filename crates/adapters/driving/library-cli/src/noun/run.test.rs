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
        assert_eq!(past.json()["findings"][0]["fatal"], false);
    }
}
