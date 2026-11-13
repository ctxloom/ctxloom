//! The capability-loss report: what an agent's engine cannot carry of its
//! profiles' hooks (j002000), in `agent show`, the swap preview of `agent
//! edit --engine` and the doctor's query.

use std::sync::Arc;

use ctxloom_ports::Scope;

use crate::fakes::{plain, Mem, Setup, ENVELOPE};
use crate::write::requests::AgentRequest;
use crate::write::views::AgentWritten;
use crate::{HookLosses, Library};

const CONFIG: &str = "schema_version: 9\nagents:\n  dev:\n    engine: claude-code\n    profiles: [team]\ndefault_agent: dev\n";

const HOUSE: &[(&str, &str)] = &[
    ENVELOPE,
    ("fragments/guide.md", "GUIDE"),
    (
        "hooks/session_end/h0.yaml",
        "type: command\ncommand: echo END\n",
    ),
    (
        "hooks/session_start/h1.yaml",
        "type: command\ncommand: echo START\n",
    ),
];

const PROJECT: &[(&str, &str)] = &[ENVELOPE, ("profiles/team.yaml", "bundles: [house]\n")];

/// opencode loses the session's end and its tool hooks; mock-lossy its end
/// alone; claude-code nothing.
fn losses() -> HookLosses {
    let lost = |events: &[&str]| -> Vec<(String, String)> {
        events
            .iter()
            .map(|e| ((*e).to_string(), format!("no {e}")))
            .collect()
    };
    [
        ("claude-code".to_string(), Vec::new()),
        ("opencode".to_string(), lost(&["pre_tool", "session_end"])),
        ("mock-lossy".to_string(), lost(&["session_end"])),
    ]
    .into_iter()
    .collect()
}

fn world_with(config: &'static str) -> (Library, Arc<Mem>) {
    let (library, mem) = Setup {
        bundles: &[("house", HOUSE), ("project", PROJECT)],
        layers: &[config],
        ..Setup::default()
    }
    .fixture();
    (library.with_hook_losses(losses()), mem)
}

fn world() -> (Library, Arc<Mem>) {
    world_with(CONFIG)
}

fn layer(mem: &Mem) -> String {
    mem.config(Scope::Project).expect("the project's config")
}

fn swap(to: &str) -> AgentRequest {
    AgentRequest {
        engine: Some(to.to_string()),
        ..AgentRequest::default()
    }
}

#[test]
fn test_show_names_each_hook_its_engine_cannot_carry_and_none_where_it_can() {
    let (library, _) = world_with(
        "schema_version: 9\nagents:\n  dev:\n    engine: opencode\n    profiles: [team]\n  old:\n    profiles: [team]\n",
    );
    let say = |_: &dyn angzarr_diagnostic::Diagnostic| String::new();
    let shown = library.agent_show(&plain(), "dev", &say).result.unwrap();
    let events: Vec<(&str, &str, &str)> = shown
        .not_carried
        .iter()
        .map(|l| (l.engine.as_str(), l.surface.as_str(), l.event.as_str()))
        .collect();
    // Its profiles hook the session's start and end; opencode loses the end
    // (and would its tool hooks, which nothing configures).
    assert_eq!(events, vec![("opencode", "hooks", "session_end")]);
    assert_eq!(shown.not_carried[0].reason, "no session_end");
    assert_eq!(
        shown.not_carried[0].line(),
        "hooks: session_end (no session_end)"
    );
    // An agent naming no engine is on the default, which loses nothing.
    let old = library.agent_show(&plain(), "old", &say).result.unwrap();
    assert!(old.not_carried.is_empty(), "{:?}", old.not_carried);
}

#[test]
fn test_an_engine_nobody_said_anything_of_loses_nothing() {
    let (library, _) = Setup {
        bundles: &[("house", HOUSE), ("project", PROJECT)],
        layers: &[CONFIG],
        ..Setup::default()
    }
    .fixture();
    let found = library.agents_losses(&plain()).result.unwrap();
    assert!(found.is_empty());
    let edited = library
        .agent_edit(&plain(), "dev", &swap("opencode"), false)
        .result
        .unwrap();
    assert!(matches!(edited, AgentWritten::Done(_)));
}

#[test]
fn test_a_swap_that_gives_something_up_is_previewed_and_writes_nothing() {
    let (library, mem) = world();
    let before = layer(&mem);
    let out = library.agent_edit(&plain(), "dev", &swap("opencode"), false);
    let AgentWritten::Preview(preview) = out.result.unwrap() else {
        panic!("a preview")
    };
    assert_eq!(layer(&mem), before, "nothing is written");
    assert_eq!(preview.status, "preview");
    assert!(!preview.applied);
    assert_eq!(
        (preview.from.as_str(), preview.to.as_str()),
        ("claude-code", "opencode")
    );
    let events: Vec<&str> = preview
        .not_carried
        .iter()
        .map(|l| l.event.as_str())
        .collect();
    assert_eq!(events, vec!["session_end"]);
    assert_eq!(
        preview.apply,
        "ctxloom agent edit dev --engine opencode --yes"
    );
}

#[test]
fn test_yes_applies_the_swap_and_an_edit_that_loses_nothing_needs_no_yes() {
    let (library, mem) = world();
    let out = library.agent_edit(&plain(), "dev", &swap("opencode"), true);
    let AgentWritten::Done(row) = out.result.unwrap() else {
        panic!("written")
    };
    assert_eq!(row.engine, "opencode");
    assert!(layer(&mem).contains("engine: opencode"));
    // Back to claude-code loses nothing: written at once.
    let back = library.agent_edit(&plain(), "dev", &swap("claude-code"), false);
    assert!(matches!(back.result.unwrap(), AgentWritten::Done(_)));
    assert!(layer(&mem).contains("engine: claude-code"));
    // The same engine again, and an edit of another axis, are no swap.
    let same = library.agent_edit(&plain(), "dev", &swap("claude-code"), false);
    assert!(matches!(same.result.unwrap(), AgentWritten::Done(_)));
    let runtime = AgentRequest {
        runtime: Some("host".to_string()),
        ..AgentRequest::default()
    };
    let other = library.agent_edit(&plain(), "dev", &runtime, false);
    assert!(matches!(other.result.unwrap(), AgentWritten::Done(_)));
}

#[test]
fn test_what_the_old_engine_lost_too_is_not_given_up_by_the_swap() {
    let (library, _) = world_with(
        "schema_version: 9\nagents:\n  dev:\n    engine: mock-lossy\n    profiles: [team]\n",
    );
    // mock-lossy already lost the session's end: opencode gives up nothing
    // more of what the profiles configure.
    let out = library.agent_edit(&plain(), "dev", &swap("opencode"), false);
    assert!(matches!(out.result.unwrap(), AgentWritten::Done(_)));
}

#[test]
fn test_the_preview_is_refused_as_the_write_would_be_and_names_every_flag() {
    let (library, mem) = world();
    let before = layer(&mem);
    let bad = AgentRequest {
        runtime: Some("submarine".to_string()),
        ..swap("opencode")
    };
    let out = library.agent_edit(&plain(), "dev", &bad, false);
    assert!(out.result.is_err(), "refused, not previewed");
    assert_eq!(layer(&mem), before);
    let many = AgentRequest {
        model: Some("big pickle".to_string()),
        profiles: Some(vec!["team".to_string()]),
        env: Some(Vec::new()),
        surfaces: Some(vec![("context".to_string(), "file".to_string())]),
        ..swap("opencode")
    };
    let out = library.agent_edit(&plain(), "dev", &many, false);
    let AgentWritten::Preview(preview) = out.result.unwrap() else {
        panic!("a preview")
    };
    assert_eq!(
        preview.apply,
        "ctxloom agent edit dev --engine opencode --model 'big pickle' --profiles team --surface context=file --env '' --yes"
    );
    // An unknown agent is the write's refusal, not a preview.
    let ghost = library.agent_edit(&plain(), "ghost", &swap("opencode"), false);
    assert!(ghost.result.is_err());
}

#[test]
fn test_the_doctor_s_query_names_each_agent_that_loses_something() {
    let (library, _) = world_with(
        "schema_version: 9\nagents:\n  dev:\n    engine: opencode\n    profiles: [team]\n  fine:\n    engine: claude-code\n    profiles: [team]\n",
    );
    let found = library.agents_losses(&plain()).result.unwrap();
    assert_eq!(found.len(), 1, "{found:?}");
    assert_eq!(
        (found[0].agent.as_str(), found[0].engine.as_str()),
        ("dev", "opencode")
    );
    assert_eq!(found[0].not_carried[0].event, "session_end");
}

// An agent with no profiles configures nothing, so it loses nothing, even on
// an engine that cannot carry a registered companion's hook.
#[test]
fn test_an_agent_with_no_profiles_loses_nothing() {
    use ctxloom_ports::CompanionLoadout::Loaded;
    const GUARD: &str = "run:\n  version: 1.0.0\n  hooks:\n    pre_tool:\n      - command: guard\n        type: command\n";
    let (library, _) = Setup {
        bundles: &[("house", HOUSE), ("project", PROJECT)],
        layers: &["schema_version: 9\nagents:\n  bare:\n    engine: opencode\ncompanions:\n  guard:\n    command: [guard]\n"],
        companions: &[("guard", Loaded(GUARD.to_string()))],
        ..Setup::default()
    }
    .fixture();
    let library = library.with_hook_losses(losses());
    let found = library.agents_losses(&plain()).result.unwrap();
    assert!(found.is_empty(), "{found:?}");
}
