use daemon_proto::v1;

use super::{render_premise_index, Shown};

fn held_back() -> v1::AssembledContext {
    v1::AssembledContext {
        context: "BODY".to_string(),
        premised: vec![v1::PremiseEntry {
            fragment: "demo#fragments/golang".to_string(),
            premise: "You are about to write Go.".to_string(),
        }],
        ..v1::AssembledContext::default()
    }
}

#[test]
fn an_index_held_back_as_data_is_rendered_after_the_context_at_rest() {
    let mut assembled = held_back();
    render_premise_index(&mut assembled, Shown::AtRest);
    let (body, index) = assembled.context.split_once("\n\n---\n\n").unwrap();
    assert_eq!(body, "BODY");
    assert!(index.starts_with("# Conditional guidance"), "{index}");
    assert!(index.contains("`ctxloom fragment show <name>`"), "{index}");
    assert!(index.ends_with("\n- demo#fragments/golang: You are about to write Go."));
}

#[test]
fn an_index_on_a_launch_names_the_context_assemble_tool() {
    let mut assembled = held_back();
    render_premise_index(&mut assembled, Shown::Launch);
    assert!(assembled.context.contains("`context_assemble` tool"));
    assert!(!assembled.context.contains("fragment show"));
}

#[test]
fn nothing_held_back_renders_nothing() {
    let mut assembled = v1::AssembledContext {
        context: "BODY".to_string(),
        ..v1::AssembledContext::default()
    };
    render_premise_index(&mut assembled, Shown::AtRest);
    assert_eq!(assembled.context, "BODY");
}

#[test]
fn an_empty_context_is_the_index_alone() {
    let mut assembled = held_back();
    assembled.context.clear();
    render_premise_index(&mut assembled, Shown::AtRest);
    assert!(assembled.context.starts_with("# Conditional guidance"));
}
