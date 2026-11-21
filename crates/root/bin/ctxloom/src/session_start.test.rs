//! Plain tests beside session_hooks.feature (which runs in
//! `gherkin_surfaces.test.rs`): the notice `hook claude-code execute SessionStart` hands a project
//! with profiles and no agents. (the bind, the statusline and
//! the retired `turn-changed` are S4's and are tested in `hook.test.rs`;
//! `stamp-plan` is taskloom's, D17.)

use super::library::Project;
use super::surfaces::additional_context;

/// The session the hook runs in: the notice rides the SessionStart answer that
/// also names the harp, so a session without one is silent: its resource name,
/// as `CTXLOOM_SESSION` carries it (s3-Q3).
const NAMED: &str = "projects/amber-quiet-heron/sessions/brisk-copper-moth";

// Go's `operations.AgentSetupNudge` (agents.go, release/0.7): a project with
// profiles but no agents is nudged, on the user-notice channel of the
// envelope (`systemMessage`), never as model context.
#[test]
fn test_the_session_start_hook_nudges_a_project_with_profiles_and_no_agents() {
    let project = Project::initialized();
    project.profile("dev", "demo");
    let ran = project.run_input(
        &["hook", "claude-code", "execute", "SessionStart"],
        r#"{"session_id":"s","hook_event_name":"SessionStart","source":"startup"}"#,
        &[("CTXLOOM_SESSION", NAMED)],
    );
    ran.succeeds();
    let doc: serde_json::Value = serde_json::from_str(ran.stdout.trim()).expect("one JSON object");
    assert!(
        doc["systemMessage"]
            .as_str()
            .is_some_and(|m| m.contains("profiles but no agents configured")),
        "{}",
        ran.said()
    );
    // The context is the harp's marker, never the project's.
    assert_eq!(
        additional_context(&ran),
        r#"<ctxloom name="brisk-copper-moth" kind="harp" />"#
    );
}

// Go's `AgentSetupNudge`: once an agent exists the nudge goes silent.
#[test]
fn test_the_session_start_hook_is_silent_once_an_agent_exists() {
    let project = Project::initialized();
    project.profile("dev", "demo");
    project
        .run(&[
            "agent",
            "create",
            "default",
            "--engine",
            "claude-code",
            "--profiles",
            "dev",
        ])
        .succeeds();
    let ran = project.run_input(
        &["hook", "claude-code", "execute", "SessionStart"],
        r#"{"session_id":"s","hook_event_name":"SessionStart","source":"startup"}"#,
        &[("CTXLOOM_SESSION", NAMED)],
    );
    ran.succeeds();
    assert!(!ran.stdout.contains("systemMessage"), "{}", ran.said());
}
