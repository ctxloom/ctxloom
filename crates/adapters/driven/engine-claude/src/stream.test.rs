//! The stream-json reader, on `fixtures/`. `stream_success.jsonl` is a real
//! `claude -p --output-format stream-json --verbose` run, trimmed (the ids,
//! paths, hooks, rate limits and usage are stripped or made up). The three
//! failure streams are synthetic: the real run's `result` event with the
//! `is_error` and `api_error_status` fields set as claude sets them.

use run_ports::{Exit, FailureClass, TurnOutcome, TurnOutput};

use crate::stream::read_turn;

const SUCCESS: &str = include_str!("fixtures/stream_success.jsonl");
const RATE_LIMITED: &str = include_str!("fixtures/stream_rate_limited.jsonl");
const REFUSED: &str = include_str!("fixtures/stream_credential_refused.jsonl");
const ENGINE_ERROR: &str = include_str!("fixtures/stream_engine_error.jsonl");

fn output(stdout: &str, code: Option<i32>) -> TurnOutput {
    TurnOutput {
        exit: Exit { code, signal: None },
        stdout: stdout.as_bytes().to_vec(),
        stderr: Vec::new(),
        timed_out: false,
        truncated: false,
        drain_incomplete: false,
    }
}

#[test]
fn a_successful_stream_answers_with_the_result_and_the_native_session_id() {
    assert_eq!(
        read_turn(&output(SUCCESS, Some(0))),
        TurnOutcome::Answered {
            answer: "DONE-MARKER".to_string(),
            native_id: Some("00000000-0000-4000-8000-000000000001".to_string()),
        }
    );
}

#[test]
fn an_error_result_is_a_failure_classed_by_its_status() {
    let native = Some("00000000-0000-4000-8000-000000000002".to_string());
    for (stream, class, detail) in [
        (
            RATE_LIMITED,
            FailureClass::RateLimited,
            "API Error: 429 rate limited",
        ),
        (REFUSED, FailureClass::CredentialRefused, "Invalid API key"),
        (ENGINE_ERROR, FailureClass::EngineError, "Execution error"),
    ] {
        assert_eq!(
            read_turn(&output(stream, Some(1))),
            TurnOutcome::Failed {
                class,
                detail: detail.to_string(),
                native_id: native.clone()
            }
        );
    }
}

#[test]
fn a_result_that_exits_nonzero_is_not_an_answer() {
    let outcome = read_turn(&output(SUCCESS, Some(2)));
    let TurnOutcome::Failed { class, detail, .. } = outcome else {
        panic!("{outcome:?}")
    };
    assert_eq!(class, FailureClass::EngineError);
    assert!(detail.contains("exit 2"), "{detail}");
}

#[test]
fn a_stream_with_no_result_is_a_failure_that_keeps_the_session_id() {
    let init_only = SUCCESS.lines().next().unwrap();
    let outcome = read_turn(&output(init_only, Some(0)));
    assert_eq!(
        outcome,
        TurnOutcome::Failed {
            class: FailureClass::EngineError,
            detail: "the stream ended with no result".to_string(),
            native_id: Some("00000000-0000-4000-8000-000000000001".to_string()),
        }
    );
}

#[test]
fn an_empty_stream_with_a_nonzero_exit_says_what_stderr_said() {
    let mut out = output("", Some(127));
    out.stderr = b"claude: not logged in\n".to_vec();
    assert_eq!(
        read_turn(&out),
        TurnOutcome::Failed {
            class: FailureClass::EngineError,
            detail: "exit 127: claude: not logged in".to_string(),
            native_id: None,
        }
    );
}

#[test]
fn a_turn_killed_at_its_deadline_or_by_a_signal_is_interrupted() {
    let mut out = output(SUCCESS.lines().next().unwrap(), None);
    out.timed_out = true;
    let TurnOutcome::Failed {
        class, native_id, ..
    } = read_turn(&out)
    else {
        panic!()
    };
    assert_eq!(class, FailureClass::Interrupted);
    assert!(native_id.is_some());

    let mut out = output("", None);
    out.exit.signal = Some(15);
    let TurnOutcome::Failed { class, detail, .. } = read_turn(&out) else {
        panic!()
    };
    assert_eq!(class, FailureClass::Interrupted);
    assert!(detail.contains("signal 15"), "{detail}");
}

#[test]
fn lines_that_are_not_events_are_skipped() {
    let noisy = format!("not json\n\n{{\"type\":\"result\"}}\n{SUCCESS}{{broken");
    assert_eq!(
        read_turn(&output(&noisy, Some(0))),
        TurnOutcome::Answered {
            answer: "DONE-MARKER".to_string(),
            native_id: Some("00000000-0000-4000-8000-000000000001".to_string()),
        }
    );
}

#[test]
fn the_last_result_wins_and_a_result_without_text_answers_empty() {
    let two = r#"{"type":"result","is_error":false,"result":"first","session_id":"s1"}
{"type":"result","is_error":false,"session_id":"s2"}
"#;
    assert_eq!(
        read_turn(&output(two, Some(0))),
        TurnOutcome::Answered {
            answer: String::new(),
            native_id: Some("s2".to_string())
        }
    );
}

#[test]
fn a_stderr_detail_is_bounded() {
    let mut out = output("", Some(1));
    out.stderr = "x".repeat(5000).into_bytes();
    let TurnOutcome::Failed { detail, .. } = read_turn(&out) else {
        panic!()
    };
    assert!(detail.len() < 600, "{}", detail.len());
}
