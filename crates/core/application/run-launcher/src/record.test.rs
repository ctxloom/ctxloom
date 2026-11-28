//! An order written as a spec and read back: the record holds everything a
//! launch is made from, and nothing in the clear that is a secret.

use std::collections::BTreeMap;

use pending_launches::PendingLaunch;
use run_ports::{
    AuthSetting, EngineSettings, GitUser, LaunchSurfaces, Sealing, SessionHome, TestSealing,
};
use surface_ports::{Command, Hook, Inputs, McpServer, Skill, SkillFile};

use super::{order_of, spec_of, RecordError, Recording};
use crate::order::Order;
use crate::workspace::{Axis, Workspace};

fn hook(command: &str, tool: &str) -> Hook {
    Hook {
        kind: "command".to_string(),
        command: command.to_string(),
        args: vec!["hook".to_string(), "x".to_string()],
        prompt: String::new(),
        timeout: 30,
        is_async: true,
        tool: tool.to_string(),
        matcher: "Bash".to_string(),
    }
}

/// An order with every part the record holds set to something.
fn order() -> Order {
    let env: BTreeMap<String, String> = [
        ("HOME", "/home/alice"),
        ("PATH", "/opt/ctxloom:/bin"),
        ("ANTHROPIC_API_KEY", "sk-ant-the-secret"),
    ]
    .into_iter()
    .map(|(k, v)| (k.to_string(), v.to_string()))
    .collect();
    Order {
        harp: "amber-swift-owl".to_string(),
        engine: "claude-code".to_string(),
        context: "THE-CONTEXT".to_string(),
        prompt: "THE-PROMPT".to_string(),
        env,
        user_home: Some("/home/alice".to_string()),
        cwd: Some("/work/proj".to_string()),
        workspace: Workspace {
            axis: Axis::Worktree,
            work_dir: "/home/alice/.ctxloom/worktrees/p/amber-swift-owl".to_string(),
            agent: "reviewer".to_string(),
        },
        interactive: true,
        model: "opus".to_string(),
        effort: "high".to_string(),
        permissions: Some(r#"{"defaultMode":"plan"}"#.to_string()),
        settings: EngineSettings {
            command: "/opt/claude".to_string(),
            session_home: SessionHome::Host,
            auth: AuthSetting::Source {
                env: vec!["ANTHROPIC_API_KEY".to_string()],
                file: Some("/home/alice/token".to_string()),
            },
            env: vec!["LANG".to_string()],
            approval_timeout: std::time::Duration::from_secs(120),
        },
        owned: vec!["HOME".to_string(), "PATH".to_string()],
        git_user: Some(GitUser {
            name: "Alice".to_string(),
            email: "alice@example.com".to_string(),
        }),
        resume: Some("native-7".to_string()),
        surfaces: LaunchSurfaces(Inputs {
            context: String::new(),
            context_file: None,
            mcp: vec![McpServer {
                name: "docs".to_string(),
                command: "docs-mcp".to_string(),
                args: vec!["--stdio".to_string()],
                env: BTreeMap::from([("DOCS_TOKEN".to_string(), "docs-secret".to_string())]),
                url: "https://docs.example/mcp".to_string(),
                headers: BTreeMap::from([(
                    "Authorization".to_string(),
                    "Bearer header-secret".to_string(),
                )]),
            }],
            hooks: BTreeMap::from([(
                "pre_tool".to_string(),
                vec![hook("ltk", "shell"), hook("ctxloom", "")],
            )]),
            deny_tools: vec!["Bash(rm:*)".to_string()],
            statusline: true,
            commands: vec![Command {
                name: "demo/go".to_string(),
                description: "go".to_string(),
                body: "Go!".to_string(),
            }],
            skills: vec![Skill {
                name: "pdf".to_string(),
                description: "reads pdfs".to_string(),
                files: vec![
                    SkillFile {
                        path: "SKILL.md".to_string(),
                        bytes: b"# pdf".to_vec(),
                        executable: false,
                    },
                    SkillFile {
                        path: "run.sh".to_string(),
                        bytes: b"#!/bin/sh".to_vec(),
                        executable: true,
                    },
                ],
            }],
            no_context: false,
        }),
    }
}

fn recording() -> Recording<'static> {
    Recording {
        program_dir: "/opt/ctxloom",
        workspace_dir: "/home/alice/.ctxloom/worktrees/p/amber-swift-owl",
        runtime: "host",
        distill: None,
        project: "demo",
    }
}

/// The launch the stream's events describe for `spec`.
fn launch(order: &Order, spec: Option<runs::LaunchSpec>) -> PendingLaunch {
    PendingLaunch {
        run: angzarr_embedded::v1::Uuid::from_bytes([1; 16]),
        session: "5f0c7a52-1111-5111-8111-000000000001".to_string(),
        incarnation: 2,
        mode: runs::RunMode::Interactive,
        launcher_lock: String::new(),
        project_dir: order.cwd.clone().unwrap(),
        project_id: "demo".to_string(),
        harp: order.harp.clone(),
        spec,
        grant: Some(runs::Grant {
            mcp_servers: vec!["ctxloom".to_string()],
            permissions: order.permissions.clone().unwrap(),
        }),
        engine: order.engine.clone(),
        workspace: Some("worktree".to_string()),
        agent: order.workspace.agent.clone(),
        resume: order.resume.clone(),
    }
}

#[test]
fn an_order_written_as_a_spec_reads_back_whole() {
    let order = order();
    let spec = spec_of(&order, recording(), &TestSealing).unwrap();
    assert_eq!(
        order_of(&launch(&order, Some(spec)), &TestSealing),
        Ok(order)
    );
}

#[test]
fn no_secret_of_the_order_is_in_the_spec_in_the_clear() {
    let spec = spec_of(&order(), recording(), &TestSealing).unwrap();
    let wire = format!("{spec:?}");
    for secret in ["sk-ant-the-secret", "docs-secret", "header-secret"] {
        assert!(!wire.contains(secret), "{secret} is in the spec");
        let hex: String = secret.bytes().map(|b| format!("{b:02x}")).collect();
        assert!(!wire.contains(&hex));
    }
    // The names are the record's: a reader sees which variables a launch had.
    let names: Vec<&str> = spec.env.iter().map(|e| e.name.as_str()).collect();
    assert_eq!(names, ["ANTHROPIC_API_KEY", "HOME", "PATH"]);
    assert_eq!(
        spec.surfaces.mcp_servers[0].headers[0].name,
        "Authorization"
    );
}

#[test]
fn a_spec_records_what_the_plan_decided_beside_the_order() {
    let spec = spec_of(&order(), recording(), &TestSealing).unwrap();
    assert_eq!(
        (spec.runtime.as_str(), spec.workspace_dir.as_str()),
        ("host", "/home/alice/.ctxloom/worktrees/p/amber-swift-owl")
    );
    assert_eq!(spec.client_program.directory, "/opt/ctxloom");
    assert_eq!(spec.approval_timeout_seconds, Some(120));
    assert_eq!(spec.auth, "source");
    assert_eq!(spec.engine_env, ["LANG"]);
    assert!(spec.distill.is_none());
}

#[test]
fn a_distills_purpose_rides_the_spec() {
    let purpose = runs::DistillPurpose {
        reference: "demo#fragments/x".to_string(),
        kind: "fragment".to_string(),
        digest: "d1".to_string(),
        etag: "7".to_string(),
        cwd: "/work".to_string(),
    };
    let spec = spec_of(
        &order(),
        Recording {
            distill: Some(purpose.clone()),
            ..recording()
        },
        &TestSealing,
    )
    .unwrap();
    assert_eq!(spec.distill, Some(purpose));
}

#[test]
fn a_run_enqueued_with_no_spec_is_not_made_into_an_order() {
    assert_eq!(
        order_of(&launch(&order(), None), &TestSealing),
        Err(RecordError::NoSpec)
    );
}

#[test]
fn a_sealed_value_that_does_not_open_is_an_error_not_an_empty_value() {
    let mut spec = spec_of(&order(), recording(), &TestSealing).unwrap();
    spec.env[0].value.sealed = TestSealing.seal("demo", "OTHER", b"v").unwrap();
    assert!(matches!(
        order_of(&launch(&order(), Some(spec)), &TestSealing),
        Err(RecordError::Sealing(_))
    ));
}

#[test]
fn hooks_that_are_not_a_document_are_an_error() {
    let mut spec = spec_of(&order(), recording(), &TestSealing).unwrap();
    spec.surfaces.hooks_json = "[1]".to_string();
    assert!(matches!(
        order_of(&launch(&order(), Some(spec)), &TestSealing),
        Err(RecordError::Hooks(_))
    ));
}

#[test]
fn a_spec_with_no_workspace_axis_launches_in_the_project() {
    let mut order = order();
    order.workspace = Workspace::default();
    order.resume = None;
    let spec = spec_of(&order, recording(), &TestSealing).unwrap();
    let mut launch = launch(&order, Some(spec));
    launch.workspace = None;
    let read = order_of(&launch, &TestSealing).unwrap();
    assert_eq!(read.workspace.axis, Axis::None);
}
