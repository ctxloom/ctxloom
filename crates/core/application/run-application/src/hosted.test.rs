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
            surfaces: surface_ports::Inputs::default(),
            untrusted: vec!["engines.claude-code.command".to_string()],
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
    assert_eq!(evidence.untrusted_settings, ["engines.claude-code.command"]);
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

// Rulings 133-137: the agent's own permissions document for the engine is
// the child's, as the plan carries it, verbatim; none, the engine's default.
#[test]
fn the_child_carries_its_agents_permissions_document() {
    let mut plan = previewed("claude-code", builtin(), 0);
    let document = r#"{"defaultMode":"plan"}"#;
    plan.plan.permissions = Some(document.to_string());
    let child = hosted_child(&plan, "claude-code", &drivable(), CLAUDE, &env(&[]));
    assert_eq!(child.permissions.as_deref(), Some(document));
    let plain = hosted_child(
        &previewed("claude-code", builtin(), 0),
        "claude-code",
        &drivable(),
        CLAUDE,
        &env(&[]),
    );
    assert_eq!(plain.permissions, None);
}

// Merge item 3 (main into the daemon line, 2026-11-13): a hosted child's
// plan carries the run agent's surfaces, as every launch's plan does.
#[test]
fn the_child_carries_its_agents_surfaces() {
    let mut planned = previewed("claude-code", builtin(), 0);
    planned.plan.surfaces.deny_tools = vec!["Bash".to_string()];
    let child = hosted_child(&planned, "claude-code", &drivable(), CLAUDE, &env(&[]));
    assert_eq!(child.surfaces.0.deny_tools, ["Bash"]);
}

// OD-41 (T21): the engine's approval_timeout is read with the child's plan,
// at each launch and re-host, and carried in its settings; `now` is zero.
#[test]
fn the_child_is_started_with_its_engines_approval_timeout() {
    for (said, carried) in [
        (std::time::Duration::from_secs(3600), 3600),
        (std::time::Duration::ZERO, 0),
    ] {
        let mut plan = previewed("claude-code", builtin(), 0);
        plan.plan.drive.approval_timeout = said;
        let child = hosted_child(&plan, "claude-code", &drivable(), CLAUDE, &env(&[]));
        assert_eq!(child.settings.approval_timeout.as_secs(), carried);
    }
    // Unsaid, the config's default, which is the port's.
    let child = hosted_child(
        &previewed("claude-code", builtin(), 0),
        "claude-code",
        &drivable(),
        CLAUDE,
        &env(&[]),
    );
    assert_eq!(
        child.settings.approval_timeout,
        run_ports::APPROVAL_TIMEOUT_DEFAULT
    );
    assert_eq!(
        library_application::APPROVAL_TIMEOUT_DEFAULT,
        run_ports::APPROVAL_TIMEOUT_DEFAULT
    );
    // A child on an engine the plan did not resolve runs on that engine's
    // own settings, the default wait among them.
    let mut plan = previewed("claude-code", builtin(), 0);
    plan.plan.drive.approval_timeout = std::time::Duration::from_secs(9);
    let other = hosted_child(&plan, "opencode", &drivable(), CLAUDE, &env(&[]));
    assert_eq!(
        other.settings.approval_timeout,
        run_ports::APPROVAL_TIMEOUT_DEFAULT
    );
}

/// A carried server, its command and environment secret.
fn server(name: &str) -> surface_ports::McpServer {
    surface_ports::McpServer {
        name: name.to_string(),
        command: "/opt/secret-server".to_string(),
        args: vec!["--token".to_string(), "SECRET-ARG".to_string()],
        env: BTreeMap::from([("API_KEY".to_string(), "SECRET-VALUE".to_string())]),
        url: String::new(),
        headers: BTreeMap::new(),
    }
}

// Step 9 (owner ruling 2026-11-17): what a hosted child is given is its
// servers' NAMES, ctxloom's own among them, sorted, and its permissions
// document as the plan wrote it; never a server's command, arguments or
// environment (a trust rule).
#[test]
fn the_grant_names_the_servers_carried_and_ctxlooms_own_and_the_permissions_document() {
    let mut planned = previewed("claude-code", builtin(), 0);
    planned.plan.surfaces.mcp = vec![server("review-tools"), server("fix-tools")];
    planned.plan.permissions = Some(r#"{"defaultMode":"plan"}"#.to_string());
    let child = hosted_child(&planned, "claude-code", &drivable(), CLAUDE, &env(&[]));
    let grant = child.grant();
    assert_eq!(grant.mcp_servers, ["ctxloom", "fix-tools", "review-tools"]);
    assert_eq!(grant.permissions, r#"{"defaultMode":"plan"}"#);
    let recorded = format!("{grant:?}");
    for secret in [
        "/opt/secret-server",
        "SECRET-ARG",
        "SECRET-VALUE",
        "API_KEY",
    ] {
        assert!(!recorded.contains(secret), "{recorded}");
    }
}

// A carried server named `ctxloom` is shadowed by ctxloom's own (OD-6): one
// name in the record. A launch with no context carries no server at all
// (OD-38), and a plan with no document records none.
#[test]
fn the_grant_names_ctxloom_once_and_no_server_for_a_launch_with_no_context() {
    let mut planned = previewed("claude-code", builtin(), 0);
    planned.plan.surfaces.mcp = vec![server("ctxloom"), server("fix-tools")];
    let child = hosted_child(&planned, "claude-code", &drivable(), CLAUDE, &env(&[]));
    assert_eq!(child.grant().mcp_servers, ["ctxloom", "fix-tools"]);
    assert_eq!(child.grant().permissions, "");
    planned.plan.surfaces.no_context = true;
    let bare = hosted_child(&planned, "claude-code", &drivable(), CLAUDE, &env(&[]));
    assert!(bare.grant().mcp_servers.is_empty(), "{:?}", bare.grant());
}
