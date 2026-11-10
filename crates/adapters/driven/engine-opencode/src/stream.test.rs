//! `opencode run --format json`, read (the lines Go's codec read at 1.18.x:
//! `{type, timestamp, sessionID, part | error}`).

use run_ports::{Exit, FailureClass, TurnOutcome, TurnOutput};

use super::read_turn;

fn output(stdout: &str, code: i32) -> TurnOutput {
    TurnOutput {
        exit: Exit {
            code: Some(code),
            signal: None,
        },
        stdout: stdout.as_bytes().to_vec(),
        stderr: b"boom on stderr".to_vec(),
        timed_out: false,
        truncated: false,
        drain_incomplete: false,
    }
}

#[test]
fn test_a_turn_that_finished_answers_its_final_steps_text_and_its_session() {
    assert_eq!(
        read_turn(&output(include_str!("fixtures/run_simple.jsonl"), 0)),
        TurnOutcome::Answered {
            answer: "pong".to_string(),
            native_id: Some("ses_ee627e80bffeCZmKYTSJOyhyC6".to_string()),
        }
    );
}

// A step that called tools is not the turn's end: the reply is the final
// step's.
#[test]
fn test_a_turn_with_tool_steps_answers_the_text_of_its_last_step() {
    let with_preamble = include_str!("fixtures/run_tool.jsonl").replacen(
        r#"{"type":"tool_use""#,
        r#"{"type":"text","sessionID":"ses_ee627a9bcffeE1ZVWrG8E3jcs1","part":{"type":"text","text":"Looking."}}
{"type":"tool_use""#,
        1,
    );
    assert_eq!(
        read_turn(&output(&with_preamble, 0)),
        TurnOutcome::Answered {
            answer: "pong".to_string(),
            native_id: Some("ses_ee627a9bcffeE1ZVWrG8E3jcs1".to_string()),
        }
    );
}

// The probe's P3: a turn whose tool was refused ends after the step that
// called it, with no final step; that is the turn's end, not a death.
#[test]
fn test_a_turn_a_refused_tool_ended_is_answered() {
    assert_eq!(
        read_turn(&output(include_str!("fixtures/run_deny_ask.jsonl"), 0)),
        TurnOutcome::Answered {
            answer: String::new(),
            native_id: Some("ses_ee6271e38ffeJ70OhiufS40y1G".to_string()),
        }
    );
}

#[test]
fn test_an_error_is_classed_by_its_kind_and_status() {
    let class = |stdout: &str| match read_turn(&output(stdout, 1)) {
        TurnOutcome::Failed {
            class, native_id, ..
        } => (class, native_id),
        other => panic!("{other:?}"),
    };
    assert_eq!(
        class(include_str!("fixtures/run_401.jsonl")),
        (
            FailureClass::CredentialRefused,
            Some("ses_ee6278a7cffeG10husls0Zre15".to_string())
        )
    );
    let error = |name: &str, status: &str| {
        format!(
            r#"{{"type":"error","sessionID":"ses_1","error":{{"name":"{name}","data":{{"message":"said"{status}}}}}}}"#
        )
    };
    assert_eq!(
        class(&error("APIError", r#","statusCode":429"#)).0,
        FailureClass::RateLimited
    );
    assert_eq!(
        class(&error("APIError", r#","statusCode":403"#)).0,
        FailureClass::CredentialRefused
    );
    assert_eq!(
        class(&error("ProviderAuthError", "")).0,
        FailureClass::CredentialRefused
    );
    assert_eq!(
        class(&error("APIError", r#","statusCode":500"#)).0,
        FailureClass::EngineError
    );
    assert_eq!(
        class(&error("UnknownError", "")).0,
        FailureClass::EngineError
    );
    let TurnOutcome::Failed { detail, .. } = read_turn(&output(&error("UnknownError", ""), 1))
    else {
        panic!()
    };
    assert_eq!(detail, "UnknownError: said");
}

#[test]
fn test_a_turn_with_no_final_step_or_a_bad_exit_failed() {
    let half = include_str!("fixtures/run_tool.jsonl")
        .lines()
        .take(3)
        .collect::<Vec<_>>()
        .join("\n");
    let TurnOutcome::Failed { class, detail, .. } = read_turn(&output(&half, 0)) else {
        panic!()
    };
    assert_eq!(class, FailureClass::EngineError);
    assert_eq!(detail, "the stream ended with no final step");
    let TurnOutcome::Failed { class, detail, .. } = read_turn(&output("", 3)) else {
        panic!()
    };
    assert_eq!(class, FailureClass::EngineError);
    assert_eq!(detail, "exit 3: boom on stderr");
    let after = read_turn(&output(include_str!("fixtures/run_simple.jsonl"), 2));
    assert!(
        matches!(after, TurnOutcome::Failed { class: FailureClass::EngineError, ref detail, .. } if detail == "exit 2 after the final step"),
        "{after:?}"
    );
}

#[test]
fn test_a_killed_turn_was_interrupted() {
    let mut timed = output(include_str!("fixtures/run_simple.jsonl"), 0);
    timed.timed_out = true;
    assert!(matches!(
        read_turn(&timed),
        TurnOutcome::Failed {
            class: FailureClass::Interrupted,
            ..
        }
    ));
    let mut killed = output("", 0);
    killed.exit = Exit {
        code: None,
        signal: Some(9),
    };
    let TurnOutcome::Failed { class, detail, .. } = read_turn(&killed) else {
        panic!()
    };
    assert_eq!(class, FailureClass::Interrupted);
    assert_eq!(detail, "the turn was ended by signal 9");
}

// A failure's stderr is cut at 400 bytes, back to a character boundary: a
// two-byte character across the cut is left out whole.
#[test]
fn test_stderr_is_cut_at_a_character_boundary() {
    let mut out = output("", 3);
    out.stderr = format!("{}\u{e9}tail", "a".repeat(399)).into_bytes();
    let TurnOutcome::Failed { detail, .. } = read_turn(&out) else {
        panic!()
    };
    assert_eq!(detail, format!("exit 3: {}", "a".repeat(399)));
}
