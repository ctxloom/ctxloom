//! opencode's surface half: its context at a directory's root, as a section
//! of `AGENTS.md`; nothing else is carried at rest.

use surface_ports::{Claim, EngineSurfaces, Inputs, Kind};

use super::*;

#[test]
fn test_the_context_is_a_section_of_agents_md_or_the_file_asked_for() {
    let inputs = Inputs {
        context: "ctx".into(),
        ..Inputs::default()
    };
    assert_eq!(
        Opencode::new().plan(Kind::Context, &inputs).unwrap().claims,
        vec![Claim::Section {
            path: "AGENTS.md".into(),
            text: "ctx".into()
        }]
    );
    let named = Inputs {
        context_file: Some("docs/CONTEXT.md".into()),
        ..inputs
    };
    assert_eq!(
        Opencode::new().plan(Kind::Context, &named).unwrap().claims[0].path(),
        "docs/CONTEXT.md"
    );
}

#[test]
fn test_it_carries_the_context_alone_and_a_launch_carries_it_itself() {
    let engine = Opencode::new();
    assert_eq!(engine.name(), "opencode");
    for kind in [
        Kind::Mcp,
        Kind::Settings,
        Kind::Hooks,
        Kind::Commands,
        Kind::Skills,
    ] {
        assert!(!engine.carries(kind), "{kind:?}");
        assert!(engine
            .plan(kind, &Inputs::default())
            .unwrap()
            .claims
            .is_empty());
    }
    assert!(engine.carries(Kind::Context));
    // The session home's AGENTS.md: a copy at rest would double it.
    assert!(engine.context_rides_launch());
    assert_eq!(engine.codec().unified_event("chat.message"), "turn_start");
}

#[test]
fn test_its_losses_are_the_events_the_plugin_has_no_trigger_for_and_say_why() {
    let losses = Opencode::new().hook_losses();
    let events: Vec<&str> = losses.iter().map(|(e, _)| e.as_str()).collect();
    assert_eq!(
        events,
        [
            "session_end",
            "permission_ask",
            "post_tool_batch",
            "message_display",
            "compaction_before",
            "compaction_after",
        ]
    );
    let end = losses
        .iter()
        .find(|(e, _)| e == "session_end")
        .expect("session_end is lost");
    assert_eq!(end.1, "opencode has no native session_end event");
    let batch = losses.iter().find(|(e, _)| e == "post_tool_batch").unwrap();
    assert_eq!(
        batch.1,
        "ctxloom's opencode plugin runs no post_tool_batch hook"
    );
}
