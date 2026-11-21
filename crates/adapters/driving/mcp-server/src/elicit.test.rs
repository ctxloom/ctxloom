//! A need as an elicitation, and an elicitation's answer as the apply's
//! (step 10c U11).

use super::*;

fn need(kind: v1::need::Kind, optional: bool) -> v1::Need {
    v1::Need {
        id: "n-1".to_string(),
        kind: Some(kind),
        optional,
    }
}

fn confirm() -> v1::need::Kind {
    v1::need::Kind::Confirm(v1::Confirm {
        text: "Remove session \"amber-swift-owl\"?".to_string(),
        ..v1::Confirm::default()
    })
}

fn choose() -> v1::need::Kind {
    v1::need::Kind::Choose(v1::Choose {
        text: "Which engine?".to_string(),
        choices: vec!["claude-code".to_string(), "opencode".to_string()],
        ..v1::Choose::default()
    })
}

fn ask() -> v1::need::Kind {
    v1::need::Kind::Ask(v1::Ask {
        text: "Where do outputs go?".to_string(),
        suggestion: "ctxloom/".to_string(),
        ..v1::Ask::default()
    })
}

fn edit() -> v1::need::Kind {
    v1::need::Kind::Edit(v1::Edit {
        file_name: "brief.md".to_string(),
        text: "# Brief\n".to_string(),
        launch: None,
    })
}

fn answer(answer: v1::answer::Answer) -> Answered {
    Answered::Answer(v1::Answer {
        need_id: "n-1".to_string(),
        answer: Some(answer),
    })
}

// Each kind is a form of one field, the need's question its message, its
// suggestion or document its default; a required need's field required.
#[test]
fn each_need_is_a_form_of_one_field() {
    let cases = [
        (
            confirm(),
            "Remove session \"amber-swift-owl\"?",
            "confirmed",
            json!({"type": "boolean", "title": "Remove session \"amber-swift-owl\"?"}),
        ),
        (
            choose(),
            "Which engine?",
            "choice",
            json!({"type": "string", "enum": ["claude-code", "opencode"]}),
        ),
        (
            ask(),
            "Where do outputs go?",
            "text",
            json!({"type": "string", "default": "ctxloom/"}),
        ),
        (
            edit(),
            "Edit brief.md: answer with its new text.",
            "text",
            json!({"type": "string", "default": "# Brief\n"}),
        ),
    ];
    for (kind, message, field, schema) in cases {
        let asked = request_of(&need(kind, false)).unwrap();
        assert_eq!(asked["mode"], "form");
        assert_eq!(asked["message"], message);
        assert_eq!(asked["requestedSchema"]["type"], "object");
        assert_eq!(asked["requestedSchema"]["properties"][field], schema);
        assert_eq!(asked["requestedSchema"]["required"], json!([field]));
        // What MCP's own types read it as: a form elicitation.
        let parsed: rmcp::model::ElicitRequestParams =
            serde_json::from_value(asked.clone()).unwrap_or_else(|e| panic!("{asked}: {e}"));
        assert!(
            matches!(
                parsed,
                rmcp::model::ElicitRequestParams::FormElicitationParams { .. }
            ),
            "{asked}"
        );
    }
}

// An optional need's field is not required; a need of no kind is asked
// nothing.
#[test]
fn an_optional_need_requires_nothing_and_a_kindless_one_is_not_asked() {
    let asked = request_of(&need(ask(), true)).unwrap();
    assert_eq!(asked["requestedSchema"]["required"], json!([]));
    let kindless = v1::Need {
        id: "n-1".to_string(),
        kind: None,
        optional: false,
    };
    assert_eq!(request_of(&kindless), None);
}

// An accepted form answers its need: a yes, a choice among the choices,
// the text typed, the document's new text.
#[test]
fn an_accepted_form_answers_its_need() {
    let cases = [
        (
            confirm(),
            json!({"confirmed": true}),
            answer(v1::answer::Answer::Confirmed(true)),
        ),
        (
            choose(),
            json!({"choice": "opencode"}),
            answer(v1::answer::Answer::Chosen("opencode".to_string())),
        ),
        (
            ask(),
            json!({"text": "out/"}),
            answer(v1::answer::Answer::Text("out/".to_string())),
        ),
        (
            edit(),
            json!({"text": "# New\n"}),
            answer(v1::answer::Answer::Edited("# New\n".to_string())),
        ),
    ];
    for (kind, content, answered) in cases {
        assert_eq!(
            answer_of(&need(kind, false), "accept", Some(&content)),
            answered,
            "{content}"
        );
    }
}

// What does not answer a required need declines the write: a decline, a
// cancel, a confirmation not given, a choice not among its choices, a
// field missing. An optional one is skipped instead.
#[test]
fn what_does_not_answer_a_required_need_declines_the_write() {
    let cases = [
        (confirm(), "decline", Some(json!({"confirmed": true}))),
        (confirm(), "cancel", None),
        (confirm(), "accept", None),
        (confirm(), "accept", Some(json!({"confirmed": false}))),
        (choose(), "accept", Some(json!({"choice": "vim"}))),
        (ask(), "accept", Some(json!({}))),
        (edit(), "accept", Some(json!({"text": 7}))),
    ];
    for (kind, action, content) in cases {
        assert_eq!(
            answer_of(&need(kind.clone(), false), action, content.as_ref()),
            Answered::Declined,
            "{action} {content:?}"
        );
        assert_eq!(
            answer_of(&need(kind, true), action, content.as_ref()),
            Answered::Skipped,
            "{action} {content:?}"
        );
    }
}
