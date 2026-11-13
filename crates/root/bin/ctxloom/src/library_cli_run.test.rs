//! The library's read side for `run` (S1), ported from Go's acceptance
//! scenarios as library_cli.test.rs describes; one file per noun (design
//! §8.4), its harness library_cli.test.rs's.

use super::library_cli::*;

// ---------------------------------------------------------------- cli/run.feature

// run.feature: Scenario: An agent binding and a hand-assembled profile cannot
// both decide the context
#[test]
fn test_an_agent_and_a_profile_cannot_both_decide_the_context() {
    let project = Project::initialized().on_gos_catalog();
    project.bundle("dev-base");
    project.profile("dev", "dev-base");
    let ran = project.run(&[
        "run",
        "--dry-run",
        "--agent",
        "developer",
        "--profile",
        "dev",
        "hello",
    ]);
    assert_eq!(
        ran.code,
        20,
        "a usage refusal is exit 20 (D28, ruling 152): {}",
        ran.said()
    );
    ran.contains("none of the others can be");
}

// Ruling 153: a distinguishing success is returned only under
// `--detailed-exitcode`: a dry run that would launch is 1 with it and 0
// without; a read is 0 either way, and a refusal is 20 either way.
#[test]
fn test_a_dry_run_that_would_launch_is_one_only_under_detailed_exitcode() {
    let project = demo_with_testing();
    project.profile("dev", "demo");
    let dry = ["run", "--dry-run", "--profile", "dev", "hello"];
    let plain = project.run(&dry);
    assert_eq!(plain.code, 0, "{}", plain.said());
    let detailed = project.run(&[&dry[..], &["--detailed-exitcode"]].concat());
    assert_eq!(detailed.code, 1, "{}", detailed.said());
    let read = project.run(&["--detailed-exitcode", "bundle", "list"]);
    assert_eq!(read.code, 0, "{}", read.said());
    let refused = project.run(&["--detailed-exitcode", "test", "refuse"]);
    assert_eq!(refused.code, 20, "{}", refused.said());
}

// run.feature: Scenario: A dry run shows the composed context without
// launching a model
#[test]
fn test_a_dry_run_shows_the_composed_context_without_launching_a_model() {
    let project = demo_with_testing();
    project.profile("dev", "demo");
    let ran = project.run(&["run", "--dry-run", "--profile", "dev", "hello"]);
    ran.succeeds();
    let doc = ran.json();
    assert_eq!(doc["profiles"], serde_json::json!(["dev"]));
    assert!(
        contains_value(&doc, "fragments", "ctxloom+local:demo#fragments/testing"),
        "{doc}"
    );
    assert!(doc["context"]
        .as_str()
        .unwrap()
        .contains("FRAGMENT-BODY-testing"));
    let text = project.run(&[
        "run",
        "--dry-run",
        "--profile",
        "dev",
        "hello",
        "--format",
        "text",
    ]);
    text.succeeds()
        .contains("dev")
        .contains("demo#fragments/testing")
        .contains("FRAGMENT-BODY-testing");
}

// run.feature: Scenario: An explicit tag selection matching nothing fails
// loudly, not silently
#[test]
fn test_an_explicit_tag_selection_matching_nothing_fails_loudly() {
    let project = demo_with_testing();
    project
        .run(&["run", "--dry-run", "-t", "no-such-tag-anywhere", "hello"])
        .fails()
        .contains("no-such-tag-anywhere");
}

// run.feature: `ctxloom run [prompt...]` is a session: the engine is launched
// in her terminal with the plan's context and the prompt as its opening. The
// engine here is a stub first on PATH, never the real one.
#[test]
fn test_a_run_that_is_not_a_dry_run_launches_the_engine_with_the_context_and_the_prompt() {
    if !files::runs_scripts() {
        return;
    }
    let project = demo_with_testing();
    project.profile("dev", "demo");
    let bin = project.home().join("stub-bin");
    files::put_program(
        bin.join("claude"),
        "for a in \"$@\"; do printf '%s\\n' \"$a\" >> \"$(dirname \"$0\")/launch\"; [ -f \"$a\" ] && cat \"$a\" >> \"$(dirname \"$0\")/launch\"; done\nexit 0\n",
    );
    let path = format!(
        "{}:{}",
        bin.display(),
        crate::tests::host_var("PATH")
            .unwrap_or_default()
            .to_string_lossy()
    );
    let ran = project.run_with(&[("PATH", &path)], &["run", "--profile", "dev", "hello"]);
    ran.succeeds();
    let launch = files::read(bin.join("launch")).expect("the stub was launched");
    assert!(launch.contains("--append-system-prompt-file"), "{launch}");
    assert!(launch.contains("FRAGMENT-BODY-testing"), "{launch}");
    assert!(launch.contains("hello"), "{launch}");
    assert!(
        !launch.contains("-p\n"),
        "a session is not print mode: {launch}"
    );
}

// ---------------------------------------------------------------- config.premised (its helpers are library_cli.test.rs's)

#[test]
fn test_a_dry_run_honours_premised_from_the_config_file() {
    let project = demo_with_a_premise();
    let dry = [
        "run",
        "--dry-run",
        "--profile",
        "dev",
        "--format",
        "json",
        "hello",
    ];
    let on_demand = project.run(&dry);
    on_demand.succeeds();
    held_back_and_indexed(on_demand.json()["context"].as_str().unwrap());
    project.write(
        ".ctxloom/config.yaml",
        &format!("{MINIMAL_CONFIG}config:\n  premised: always\n"),
    );
    let always = project.run(&dry);
    always.succeeds();
    loaded_and_unindexed(always.json()["context"].as_str().unwrap());
}

/// A claude child that keeps every file it was given (the appended system
/// prompt among them) in `given`, then answers its turn.
const RECORDING_CHILD: &str = r#"here="$(dirname "$0")"
for a in "$@"; do
  [ -f "$a" ] && cat "$a" >> "$here/given"
done
cat > /dev/null
echo '{"type":"system","subtype":"init","session_id":"9d764b45-a56a-48d7-ac9d-c2a16c5669f1"}'
echo '{"type":"result","subtype":"success","is_error":false,"result":"PREMISED-REPLY","session_id":"9d764b45-a56a-48d7-ac9d-c2a16c5669f1"}'
"#;

#[test]
fn test_a_one_shot_hands_the_engine_the_context_premised_decides() {
    if !files::runs_scripts() {
        return;
    }
    let project = demo_with_a_premise();
    let bin = project.home().join("bin");
    files::put_program(bin.join("claude"), RECORDING_CHILD);
    let path = match crate::tests::host_var("PATH") {
        Some(host) => format!("{}:{}", bin.display(), host.to_string_lossy()),
        None => bin.display().to_string(),
    };
    let env = [
        ("PATH", path.as_str()),
        ("CLAUDE_CODE_OAUTH_TOKEN", "sk-ant-oat01-fixture-not-real"), // gitleaks:allow
    ];
    // What this run gave: the record past what the runs before it left.
    let mut seen = 0;
    let mut given = || {
        let text = files::read(bin.join("given")).unwrap_or_default();
        let fresh = text[seen..].to_string();
        seen = text.len();
        fresh
    };
    for (word, judge) in [
        ("on-demand", held_back_and_indexed as fn(&str)),
        ("always", loaded_and_unindexed),
    ] {
        let set = format!("config.premised={word}");
        let ran = project.run_with(
            &env,
            &[
                "--config-set",
                &set,
                "run",
                "--profile",
                "dev",
                "--one-shot",
                "hello",
                "--format",
                "json",
            ],
        );
        assert_eq!(ran.code, 0, "{word}: {}", ran.said());
        assert!(ran.stdout.contains("PREMISED-REPLY"), "{}", ran.said());
        judge(&given());
    }
}
