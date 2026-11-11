//! A hosted child is planned and judged as a one-shot child is (D18, rulings
//! 53, 73e, 73f), on the environment its launcher sent (S6).

use std::collections::BTreeMap;

use library_application::{AuthSource, DryRun, FindingRow, Previewed, Source, Sourced};
use run_ports::{AuthSetting, Credentials};

use super::hosted_child;

const CLAUDE: Credentials = Credentials {
    child_env: &["CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"],
    ..Credentials::NONE
};

fn previewed(engine: &str, auth: Sourced<AuthSource>, fatal: usize) -> Previewed {
    let row = FindingRow {
        kind: "k".to_string(),
        text: "t".to_string(),
        remedy: String::new(),
        fatal: true,
    };
    Previewed {
        plan: DryRun {
            agent: String::new(),
            workspace: String::new(),
            runtime: String::new(),
            engine: engine.to_string(),
            profiles: Vec::new(),
            fragments: Vec::new(),
            context: "THE-PROJECT-CONTEXT".to_string(),
            tokens: 0,
            prompt: String::new(),
            findings: Vec::new(),
            axes: (String::new(), "host".to_string()),
            model: "opus".to_string(),
            effort: "high".to_string(),
            permissions: None,
            drive: library_application::Drive {
                command: "/opt/claude".to_string(),
                env: vec!["EXTRA".to_string()],
                auth,
                ..library_application::Drive::default()
            },
            settings: None,
            settings_agent: String::new(),
        },
        fatal: vec![row; fatal],
    }
}

fn builtin() -> Sourced<AuthSource> {
    Sourced {
        value: AuthSource::Token,
        source: Source::Default,
    }
}

fn env(pairs: &[(&str, &str)]) -> BTreeMap<String, String> {
    pairs
        .iter()
        .map(|(k, v)| ((*k).to_string(), (*v).to_string()))
        .collect()
}

fn drivable() -> Vec<String> {
    vec!["claude-code".to_string()]
}

#[test]
fn the_child_is_started_with_the_plans_context_model_effort_and_settings() {
    let child = hosted_child(
        &previewed("claude-code", builtin(), 0),
        "claude-code",
        &drivable(),
        CLAUDE,
        &env(&[]),
    );
    assert_eq!(child.context, "THE-PROJECT-CONTEXT");
    assert_eq!(child.model, "opus");
    assert_eq!(child.effort, "high");
    assert_eq!(child.settings.command, "/opt/claude");
    assert_eq!(child.settings.env, ["EXTRA"]);
    assert_eq!(child.settings.auth, AuthSetting::Builtin);
}

#[test]
fn the_evidence_is_a_one_shot_childs_judged_on_the_environment_sent() {
    let child = hosted_child(
        &previewed("claude-code", builtin(), 2),
        "claude-code",
        &drivable(),
        CLAUDE,
        &env(&[("ANTHROPIC_API_KEY", "k")]),
    );
    let evidence = child.evidence;
    // A hosted child's prompts are its messages.
    assert!(evidence.prompt_given);
    assert_eq!(evidence.engine, "claude-code");
    assert_eq!(evidence.drivable_engines, drivable());
    assert_eq!(evidence.fatal_findings, 2);
    let credential = evidence.credential.expect("a child's credential is judged");
    assert!(credential.accepted_set);
    assert!(!credential.file_only);
    assert_eq!(
        credential.accepted_variables,
        ["CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"]
    );
}

#[test]
fn a_child_sent_no_credential_has_none_set() {
    let child = hosted_child(
        &previewed("claude-code", builtin(), 0),
        "claude-code",
        &drivable(),
        CLAUDE,
        &env(&[("ANTHROPIC_API_KEY", ""), ("PATH", "/bin")]),
    );
    let credential = child.evidence.credential.expect("judged");
    assert!(!credential.accepted_set);
}

#[test]
fn a_file_alone_is_a_file_only_credential() {
    let file = Sourced {
        value: AuthSource::Credential {
            env: Vec::new(),
            file: Some("~/.claude/.credentials.json".to_string()),
        },
        source: Source::Engine,
    };
    let child = hosted_child(
        &previewed("claude-code", file, 0),
        "claude-code",
        &drivable(),
        CLAUDE,
        &env(&[("ANTHROPIC_API_KEY", "k")]),
    );
    let credential = child.evidence.credential.expect("judged");
    assert!(credential.file_only);
}

#[test]
fn a_plan_for_another_engine_runs_on_the_engines_built_ins() {
    let child = hosted_child(
        &previewed("opencode", builtin(), 0),
        "claude-code",
        &drivable(),
        CLAUDE,
        &env(&[]),
    );
    assert_eq!(child.settings, run_ports::EngineSettings::default());
}
