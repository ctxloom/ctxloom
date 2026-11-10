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
