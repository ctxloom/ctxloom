//! `run --no-context` at the application port (owner OD-38, 2026-11-13).

use crate::fakes::{plain, Setup, ENVELOPE};
use crate::{AppError, RunRequest};

const DEMO: &[(&str, &str)] = &[
    ENVELOPE,
    ("fragments/rules.md", "RULES-BODY\n"),
    ("mcp/ledger.yaml", "command: ledger\n"),
    ("prompts/review.md", "REVIEW-BODY\n"),
];

const PROJECT: &[(&str, &str)] = &[ENVELOPE, ("profiles/dev.yaml", "bundles: [demo]\n")];

const AGENT: &str = "schema_version: 9\nagents:\n  dev:\n    engine: opencode\n    profiles: [dev]\n    engines:\n      opencode:\n        model: anthropic/haiku\n        permissions:\n          edit: ask\ndefault_agent: dev\n";

fn library(layers: &[&str]) -> crate::Library {
    Setup {
        bundles: &[("demo", DEMO), ("project", PROJECT)],
        layers,
        ..Setup::default()
    }
    .library()
}

fn no_context(request: RunRequest) -> RunRequest {
    RunRequest {
        no_context: true,
        prompt: "hello".to_string(),
        ..request
    }
}

// OD-38: the default agent's profile would carry a fragment, a command and
// an MCP server; with no context the plan carries none of them, and the
// agent's own engine, model and permissions stand.
#[test]
fn test_a_run_with_no_context_carries_no_context_and_no_server_on_the_agent_s_engine() {
    let library = library(&[AGENT]);
    let with = library
        .dry_run(&plain(), &RunRequest::default())
        .result
        .unwrap()
        .plan;
    assert!(with.context.contains("RULES-BODY"), "{}", with.context);
    assert!(!with.surfaces.mcp.is_empty());
    let planned = library.dry_run(&plain(), &no_context(RunRequest::default()));
    let plan = planned.result.unwrap().plan;
    assert_eq!(plan.context, "");
    assert!(plan.fragments.is_empty(), "{:?}", plan.fragments);
    assert!(plan.profiles.is_empty(), "{:?}", plan.profiles);
    assert!(plan.surfaces.mcp.is_empty(), "{:?}", plan.surfaces.mcp);
    assert!(plan.surfaces.commands.is_empty());
    assert!(plan.surfaces.skills.is_empty());
    assert!(plan.surfaces.hooks.is_empty());
    assert!(plan.surfaces.deny_tools.is_empty());
    assert_eq!(plan.tokens, 0);
    assert_eq!(plan.prompt, "hello");
    assert_eq!(plan.engine, "opencode");
    assert_eq!(plan.model, "anthropic/haiku");
    assert_eq!(plan.permissions.as_deref(), Some(r#"{"edit":"ask"}"#));
    // The statusline is the engine's own setup: it stays as configured.
    assert_eq!(plan.surfaces.statusline, with.surfaces.statusline);
    // A named agent is the one planned.
    let named = library
        .dry_run(
            &plain(),
            &no_context(RunRequest {
                agent: "dev".to_string(),
                ..RunRequest::default()
            }),
        )
        .result
        .unwrap()
        .plan;
    assert_eq!(
        (named.engine.as_str(), named.context.as_str()),
        ("opencode", "")
    );
}

// With no agent configured at all, a run with no context is planned on the
// default engine (an ordinary run would be refused for want of an agent).
#[test]
fn test_a_run_with_no_context_needs_no_agent() {
    let library = library(&["schema_version: 9\n"]);
    assert!(matches!(
        library.dry_run(&plain(), &RunRequest::default()).result,
        Err(AppError::LaunchNoAgents)
    ));
    let plan = library
        .dry_run(&plain(), &no_context(RunRequest::default()))
        .result
        .unwrap()
        .plan;
    assert_eq!(plan.engine, "claude-code");
    assert_eq!(plan.context, "");
    assert!(plan.surfaces.mcp.is_empty());
}

// OD-38: no context beside a selection of context is refused, naming both.
#[test]
fn test_no_context_beside_a_selection_of_context_is_refused() {
    let library = library(&[AGENT]);
    let s = |text: &str| vec![text.to_string()];
    for (request, flag) in [
        (
            RunRequest {
                profile: "dev".to_string(),
                ..RunRequest::default()
            },
            "--profile",
        ),
        (
            RunRequest {
                fragments: s("demo#fragments/rules"),
                ..RunRequest::default()
            },
            "--fragment",
        ),
        (
            RunRequest {
                tags: s("x"),
                ..RunRequest::default()
            },
            "--tag",
        ),
    ] {
        match library.dry_run(&plain(), &no_context(request)).result {
            Err(AppError::NoContextExclusive { other }) => assert_eq!(other, flag),
            other => panic!("{flag}: {:?}", other.err()),
        }
    }
}
