//! A session that cannot be launched ends `init` as it ends `ctxloom run`:
//! with the failure's own exit code (a refusal 2, another failure 1), not a
//! flattened 1. Home: `features/journeys/j000200_setup.feature` (scenario 2)
//! and `features/cli/init.feature`.
//!
//! The terminal is the binary's test seam (`CTXLOOM_TEST_TERMINAL`), so no
//! pty is needed. `init` on a project that already has a `.ctxloom` reads
//! nothing of its config itself, so a config error is the launch's own: the
//! session's plan reads it. A newer `schema_version` is a refusal (2); an
//! agent naming an engine ctxloom does not have is an ordinary failure (1).

use serde_json::Value;

use super::library::Project;

/// The terminal stood in, and the credential `init` looks for exported as a
/// fixture, so its token guide (a warning line on stderr) is not said
/// whatever the host exports: the test run's child gets exactly this
/// environment.
const TERMINAL: [(&str, &str); 2] = [
    ("CTXLOOM_TEST_TERMINAL", "1"),
    ("CLAUDE_CODE_OAUTH_TOKEN", "sk-ant-oat01-fixture-not-real"), // gitleaks:allow
];

fn project(config: &str) -> Project {
    let project = Project::initialized();
    project.write(".ctxloom/config.yaml", config);
    project
}

/// The error envelope on `stderr`: the json document that has `code` and
/// `message`, whatever warnings are said beside it.
fn envelope(stderr: &str) -> Value {
    let mut documents = serde_json::Deserializer::from_str(stderr).into_iter::<Value>();
    documents
        .by_ref()
        .map_while(Result::ok)
        .find(|v| v.get("code").is_some() && v.get("message").is_some())
        .unwrap_or_else(|| panic!("an envelope on stderr: {stderr}"))
}

/// `run` and `init` at a terminal on `config`: their exit codes, and the
/// codes of the errors they said.
fn both(config: &str) -> [(i32, Value); 2] {
    let project = project(config);
    let run = project.run_with(&TERMINAL, &["run"]);
    let init = project.run_input(&["init"], "y\n", &TERMINAL);
    [run, init].map(|ran| (ran.code, envelope(&ran.stderr)))
}

#[test]
fn test_a_refused_launch_exits_2_from_run_and_from_init_at_a_terminal() {
    let [(run_exit, run), (init_exit, init)] = both("schema_version: 99\n");
    assert_eq!((run_exit, init_exit), (2, 2), "{run} {init}");
    assert_eq!(run["code"], "library-application-config-newer");
    assert_eq!(init["code"], run["code"], "the same refusal");
}

#[test]
fn test_a_failed_launch_exits_1_from_run_and_from_init_at_a_terminal() {
    let [(run_exit, run), (init_exit, init)] = both(
        "schema_version: 9\nagents:\n  dev:\n    engine: claude-code\n    profiles: [ghost]\n    runtime: host\ndefault_agent: dev\n",
    );
    assert_eq!((run_exit, init_exit), (1, 1), "{run} {init}");
    assert_eq!(init["code"], run["code"], "the same failure");
}
