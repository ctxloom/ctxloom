//! The conversation, fed the stream of real `claude -p --input-format
//! stream-json --output-format stream-json --verbose --replay-user-messages`
//! runs (`fixtures/child_*.jsonl`: the 2026-11-10 probe's logs, with the
//! hook notices dropped, the init events trimmed, and the paths, ids and
//! signatures made up).
//! The races are forced by WHERE in the stream a message is sent, never by
//! time.

use run_ports::{
    ChildError, ChildMessage, ChildObservation, Exit, FailureClass, Queued, TurnOutcome, Urgency,
};
use surface_ports::Json;

use crate::conversation::Conversation;

const TWO_TURNS: &str = include_str!("fixtures/child_two_turns.jsonl");
const INTERRUPT: &str = include_str!("fixtures/child_interrupt.jsonl");
const CLEAR: &str = include_str!("fixtures/child_clear.jsonl");

fn message(id: &str, text: &str) -> ChildMessage {
    ChildMessage::new(id, text)
}

/// The lines of `stream` up to (not including) the first that holds `marker`,
/// and the rest.
fn split<'a>(stream: &'a str, marker: &str) -> (Vec<&'a str>, Vec<&'a str>) {
    let lines: Vec<&str> = stream.lines().collect();
    let at = lines
        .iter()
        .position(|line| line.contains(marker))
        .unwrap_or_else(|| panic!("{marker} is in the stream"));
    (lines[..at].to_vec(), lines[at..].to_vec())
}

/// `lines` around the first that holds `marker`: before it, it, after it.
fn around<'a>(lines: &[&'a str], marker: &str) -> (Vec<&'a str>, &'a str, Vec<&'a str>) {
    let at = lines
        .iter()
        .position(|line| line.contains(marker))
        .unwrap_or_else(|| panic!("{marker} is in the stream"));
    (lines[..at].to_vec(), lines[at], lines[at + 1..].to_vec())
}

/// What reading `lines` said, and what it wrote.
fn feed(conversation: &mut Conversation, lines: &[&str]) -> (Vec<ChildObservation>, Vec<String>) {
    let mut observations = Vec::new();
    let mut writes = Vec::new();
    for line in lines {
        let step = conversation.read(line);
        observations.extend(step.observations);
        writes.extend(step.writes);
    }
    (observations, writes)
}

/// The user message line claude is given for `text`.
fn user_line(text: &str) -> String {
    Json::object([
        ("type", Json::text("user")),
        (
            "message",
            Json::object([("role", Json::text("user")), ("content", Json::text(text))]),
        ),
    ])
    .compact()
}

fn answered(answer: &str, native: &str) -> TurnOutcome {
    TurnOutcome::Answered {
        answer: answer.to_string(),
        native_id: Some(native.to_string()),
    }
}

fn session_of(stream: &str) -> String {
    let first = Json::parse(stream.lines().next().unwrap()).unwrap();
    first
        .get("session_id")
        .and_then(Json::as_str)
        .unwrap()
        .to_string()
}

#[test]
fn a_wake_to_an_idle_child_is_written_as_one_user_message_line() {
    let mut conversation = Conversation::default();
    let (queued, writes) = conversation
        .send(
            message("m-1", "Reply only with the word ONE."),
            Urgency::Wake,
        )
        .unwrap();
    assert_eq!(queued, Queued::Written);
    assert_eq!(writes, vec![user_line("Reply only with the word ONE.")]);
    assert_eq!(
        writes[0],
        r#"{"type":"user","message":{"role":"user","content":"Reply only with the word ONE."}}"#
    );
}

#[test]
fn the_first_init_names_the_child_and_the_echo_starts_the_turn() {
    let session = session_of(TWO_TURNS);
    assert_eq!(session.len(), 36);
    let mut conversation = Conversation::default();
    conversation
        .send(
            message("m-1", "Reply only with the word ONE."),
            Urgency::Wake,
        )
        .unwrap();
    let (before, rest) = split(TWO_TURNS, r#""type":"result""#);
    let (observations, writes) = feed(&mut conversation, &before);
    assert!(writes.is_empty(), "{writes:?}");
    assert_eq!(
        observations,
        vec![
            ChildObservation::Started {
                native_id: session.clone()
            },
            ChildObservation::TurnStarted {
                message_ids: vec!["m-1".to_string()]
            },
        ]
    );
    let (observations, _) = feed(&mut conversation, &rest[..1]);
    assert_eq!(
        observations,
        vec![ChildObservation::TurnEnded {
            message_ids: vec!["m-1".to_string()],
            outcome: answered("ONE", &session),
        }]
    );
}

#[test]
fn a_wake_sent_mid_turn_is_held_and_written_when_the_turn_ends() {
    let session = session_of(TWO_TURNS);
    let mut conversation = Conversation::default();
    conversation
        .send(
            message("m-1", "Reply only with the word ONE."),
            Urgency::Wake,
        )
        .unwrap();
    let (before, rest) = split(TWO_TURNS, r#""type":"result""#);
    feed(&mut conversation, &before);
    // Mid-turn: the turn's echo is read, its result is not.
    let second = "What word did you reply with last time? Reply with it and the word TWO.";
    let (queued, writes) = conversation
        .send(message("m-2", second), Urgency::Wake)
        .unwrap();
    assert_eq!(queued, Queued::Held);
    assert!(writes.is_empty(), "nothing is written mid-turn: {writes:?}");
    let (observations, writes) = feed(&mut conversation, &rest);
    assert_eq!(writes, vec![user_line(second)]);
    assert_eq!(
        observations,
        vec![
            ChildObservation::TurnEnded {
                message_ids: vec!["m-1".to_string()],
                outcome: answered("ONE", &session),
            },
            ChildObservation::TurnStarted {
                message_ids: vec!["m-2".to_string()]
            },
            ChildObservation::TurnEnded {
                message_ids: vec!["m-2".to_string()],
                outcome: answered("ONE TWO", &session),
            },
        ]
    );
}

#[test]
fn a_steer_mid_turn_interrupts_it_and_is_the_next_turn_before_a_held_wake() {
    let session = session_of(INTERRUPT);
    let mut conversation = Conversation::default();
    conversation
        .send(message("m-1", "Write the numbers..."), Urgency::Wake)
        .unwrap();
    // Mid-delta: the text block has started streaming, nothing has answered.
    let (before, rest) = split(INTERRUPT, r#""type":"control_response""#);
    let (observations, _) = feed(&mut conversation, &before);
    assert!(observations.contains(&ChildObservation::TurnStarted {
        message_ids: vec!["m-1".to_string()]
    }));
    let (queued, writes) = conversation
        .send(message("m-2", "a held wake"), Urgency::Wake)
        .unwrap();
    assert_eq!((queued, writes.len()), (Queued::Held, 0));
    let (queued, writes) = conversation
        .send(
            message("m-3", "Reply only with the word BANANA."),
            Urgency::Steer,
        )
        .unwrap();
    assert_eq!(queued, Queued::Interrupting);
    assert_eq!(
        writes,
        vec![
            r#"{"type":"control_request","request_id":"ctxloom-interrupt-1","request":{"subtype":"interrupt"}}"#
        ]
    );
    // The aborted turn's result writes the steer, never the held wake.
    let (head, result, after) = around(&rest, r#""subtype":"error_during_execution""#);
    let (observations, writes) = feed(&mut conversation, &head);
    assert_eq!(
        observations,
        vec![ChildObservation::InterruptAnswered { accepted: true }]
    );
    assert!(writes.is_empty(), "{writes:?}");
    let (observations, writes) = feed(&mut conversation, &[result]);
    assert_eq!(writes, vec![user_line("Reply only with the word BANANA.")]);
    assert_eq!(
        observations,
        vec![ChildObservation::TurnEnded {
            message_ids: vec!["m-1".to_string()],
            outcome: TurnOutcome::Failed {
                class: FailureClass::Interrupted,
                detail: "the turn was interrupted (aborted_streaming)".to_string(),
                native_id: Some(session.clone()),
            },
        }]
    );
    // The probe's queued BANANA turn is the steer's: echo, then its answer;
    // and its end writes the held wake.
    let (observations, writes) = feed(&mut conversation, &after);
    assert_eq!(writes, vec![user_line("a held wake")]);
    assert_eq!(
        observations,
        vec![
            ChildObservation::TurnStarted {
                message_ids: vec!["m-3".to_string()]
            },
            ChildObservation::TurnEnded {
                message_ids: vec!["m-3".to_string()],
                outcome: answered("BANANA", &session),
            },
        ]
    );
}

#[test]
fn a_second_steer_goes_after_the_first_and_asks_no_second_interrupt() {
    let mut conversation = Conversation::default();
    conversation
        .send(message("m-1", "long"), Urgency::Wake)
        .unwrap();
    conversation
        .send(message("w", "wake"), Urgency::Wake)
        .unwrap();
    let (_, first) = conversation
        .send(message("s-1", "first"), Urgency::Steer)
        .unwrap();
    let (queued, second) = conversation
        .send(message("s-2", "second"), Urgency::Steer)
        .unwrap();
    assert_eq!(first.len(), 1);
    assert_eq!(queued, Queued::Interrupting);
    assert!(second.is_empty(), "{second:?}");
    assert_eq!(conversation.interrupt().unwrap(), (false, Vec::new()));
    let ends = |c: &mut Conversation| {
        c.read(r#"{"type":"result","subtype":"success","is_error":false,"result":"x","session_id":"n"}"#)
            .writes
    };
    assert_eq!(ends(&mut conversation), vec![user_line("first")]);
    assert_eq!(ends(&mut conversation), vec![user_line("second")]);
    assert_eq!(ends(&mut conversation), vec![user_line("wake")]);
    assert_eq!(ends(&mut conversation), Vec::<String>::new());
}

#[test]
fn a_steer_to_an_idle_child_is_a_wake() {
    let mut conversation = Conversation::default();
    let (queued, writes) = conversation
        .send(message("m-1", "go"), Urgency::Steer)
        .unwrap();
    assert_eq!(queued, Queued::Written);
    assert_eq!(writes, vec![user_line("go")]);
}

#[test]
fn an_interrupt_asks_only_a_turn_in_flight() {
    let mut conversation = Conversation::default();
    assert_eq!(conversation.interrupt().unwrap(), (false, Vec::new()));
    conversation
        .send(message("m-1", "go"), Urgency::Wake)
        .unwrap();
    let (asked, writes) = conversation.interrupt().unwrap();
    assert!(asked);
    assert_eq!(writes.len(), 1);
    assert!(writes[0].contains(r#""subtype":"interrupt""#), "{writes:?}");
    // The next one has a request id of its own.
    conversation.read(r#"{"type":"result","subtype":"success","result":"","session_id":"n"}"#);
    conversation
        .send(message("m-2", "go"), Urgency::Wake)
        .unwrap();
    let (_, writes) = conversation.interrupt().unwrap();
    assert!(writes[0].contains("ctxloom-interrupt-2"), "{writes:?}");
}

#[test]
fn an_inform_rides_on_the_next_turn_and_starts_none() {
    let mut conversation = Conversation::default();
    let (queued, writes) = conversation
        .send(message("i-1", "the build is red"), Urgency::Inform)
        .unwrap();
    assert_eq!((queued, writes.len()), (Queued::Held, 0));
    conversation
        .send(message("i-2", "main moved"), Urgency::Inform)
        .unwrap();
    let (queued, writes) = conversation
        .send(message("m-1", "carry on"), Urgency::Wake)
        .unwrap();
    assert_eq!(queued, Queued::Written);
    assert_eq!(
        writes,
        vec![user_line("the build is red\n\nmain moved\n\ncarry on")]
    );
    let observations = conversation
        .read(r#"{"type":"user","message":{"role":"user","content":"x"},"session_id":"n","isReplay":true}"#)
        .observations;
    assert_eq!(
        observations,
        vec![ChildObservation::TurnStarted {
            message_ids: vec!["i-1".to_string(), "i-2".to_string(), "m-1".to_string()]
        }]
    );
}

#[test]
fn an_inform_sent_mid_turn_rides_on_the_held_wake_when_it_is_written() {
    let mut conversation = Conversation::default();
    conversation
        .send(message("m-1", "one"), Urgency::Wake)
        .unwrap();
    conversation
        .send(message("m-2", "two"), Urgency::Wake)
        .unwrap();
    conversation
        .send(message("i-1", "late news"), Urgency::Inform)
        .unwrap();
    let step =
        conversation.read(r#"{"type":"result","subtype":"success","result":"","session_id":"n"}"#);
    assert_eq!(step.writes, vec![user_line("late news\n\ntwo")]);
}

#[test]
fn a_clear_rotates_the_native_id_and_the_next_turn_runs_under_it() {
    let first = session_of(CLEAR);
    let mut conversation = Conversation::default();
    conversation
        .send(
            message("m-1", "Remember the word PELICAN. Reply only OK."),
            Urgency::Wake,
        )
        .unwrap();
    let (turn, rest) = split(CLEAR, r#""type":"conversation_reset""#);
    let (observations, _) = feed(&mut conversation, &turn);
    assert_eq!(
        observations.last(),
        Some(&ChildObservation::TurnEnded {
            message_ids: vec!["m-1".to_string()],
            outcome: answered("OK", &first),
        })
    );
    let (queued, writes) = conversation.clear().unwrap();
    assert_eq!(queued, Queued::Written);
    assert_eq!(writes, vec![user_line("/clear")]);
    let (head, cleared, after) = around(&rest, r#""num_turns":0"#);
    let (observations, _) = feed(&mut conversation, &head);
    assert!(observations.is_empty(), "{observations:?}");
    let (observations, _) = feed(&mut conversation, &[cleared]);
    let second = "00000000-0000-4000-8000-000000000002".to_string();
    assert_ne!(first, second);
    assert_eq!(
        observations,
        vec![ChildObservation::Cleared {
            native_id: Some(second.clone())
        }]
    );
    conversation
        .send(message("m-2", "what word?"), Urgency::Wake)
        .unwrap();
    let (observations, _) = feed(&mut conversation, &after);
    assert_eq!(
        observations,
        vec![
            ChildObservation::TurnStarted {
                message_ids: vec!["m-2".to_string()]
            },
            ChildObservation::TurnEnded {
                message_ids: vec!["m-2".to_string()],
                outcome: answered("NONE", &second),
            },
        ]
    );
}

#[test]
fn a_clear_mid_turn_waits_for_the_turn_and_goes_before_a_later_wake() {
    let mut conversation = Conversation::default();
    conversation
        .send(message("m-1", "one"), Urgency::Wake)
        .unwrap();
    assert_eq!(conversation.clear().unwrap(), (Queued::Held, Vec::new()));
    conversation
        .send(message("m-2", "two"), Urgency::Wake)
        .unwrap();
    let end = r#"{"type":"result","subtype":"success","result":"","session_id":"n"}"#;
    assert_eq!(conversation.read(end).writes, vec![user_line("/clear")]);
    // A steer during a clear waits for it: a clear is not interrupted.
    let (queued, writes) = conversation
        .send(message("s-1", "steer"), Urgency::Steer)
        .unwrap();
    assert_eq!((queued, writes.len()), (Queued::Held, 0));
    let step = conversation.read(end);
    assert_eq!(
        step.observations,
        vec![ChildObservation::Cleared { native_id: None }]
    );
    assert_eq!(step.writes, vec![user_line("steer")]);
    assert_eq!(conversation.read(end).writes, vec![user_line("two")]);
}

#[test]
fn an_unasked_turn_ends_naming_no_message() {
    let mut conversation = Conversation::default();
    let step = conversation.read(
        r#"{"type":"result","subtype":"success","is_error":false,"result":"hi","session_id":"n"}"#,
    );
    assert_eq!(
        step.observations,
        vec![ChildObservation::TurnEnded {
            message_ids: Vec::new(),
            outcome: answered("hi", "n"),
        }]
    );
}

#[test]
fn an_error_result_is_classed_by_its_status() {
    let mut conversation = Conversation::default();
    conversation
        .send(message("m-1", "go"), Urgency::Wake)
        .unwrap();
    let step = conversation.read(
        r#"{"type":"result","subtype":"success","is_error":true,"api_error_status":429,"result":"API Error: 429","session_id":"n"}"#,
    );
    assert_eq!(
        step.observations,
        vec![ChildObservation::TurnEnded {
            message_ids: vec!["m-1".to_string()],
            outcome: TurnOutcome::Failed {
                class: FailureClass::RateLimited,
                detail: "API Error: 429".to_string(),
                native_id: Some("n".to_string()),
            },
        }]
    );
}

#[test]
fn an_unparsable_line_and_an_unknown_event_say_nothing() {
    let mut conversation = Conversation::default();
    for line in ["not json", "", r#"{"type":"rate_limit_event"}"#, "[1,2]"] {
        let step = conversation.read(line);
        assert!(step.observations.is_empty(), "{line}");
        assert!(step.writes.is_empty(), "{line}");
    }
}

#[test]
fn a_refused_interrupt_is_heard_as_refused() {
    let mut conversation = Conversation::default();
    let step = conversation.read(
        r#"{"type":"control_response","response":{"subtype":"error","request_id":"ctxloom-interrupt-1","error":"no turn"}}"#,
    );
    assert_eq!(
        step.observations,
        vec![ChildObservation::InterruptAnswered { accepted: false }]
    );
}

#[test]
fn ending_interrupts_the_turn_in_flight_and_answers_what_was_never_written() {
    let mut conversation = Conversation::default();
    conversation
        .send(message("m-1", "one"), Urgency::Wake)
        .unwrap();
    conversation
        .send(message("m-2", "two"), Urgency::Wake)
        .unwrap();
    conversation
        .send(message("i-1", "news"), Urgency::Inform)
        .unwrap();
    let (undelivered, writes) = conversation.end().unwrap();
    assert_eq!(undelivered, vec!["i-1".to_string(), "m-2".to_string()]);
    assert_eq!(writes.len(), 1);
    assert!(writes[0].contains("interrupt"), "{writes:?}");
    // Ended: nothing more is taken, and the end writes nothing more.
    assert_eq!(
        conversation.send(message("m-3", "three"), Urgency::Wake),
        Err(ChildError::Closed)
    );
    assert_eq!(conversation.clear(), Err(ChildError::Closed));
    assert_eq!(conversation.interrupt(), Err(ChildError::Closed));
    assert_eq!(conversation.end(), Err(ChildError::Closed));
    let step =
        conversation.read(r#"{"type":"result","subtype":"success","result":"","session_id":"n"}"#);
    assert!(step.writes.is_empty(), "{:?}", step.writes);
    assert_eq!(step.observations.len(), 1);
}

#[test]
fn an_idle_end_asks_no_interrupt() {
    let mut conversation = Conversation::default();
    assert_eq!(conversation.end().unwrap(), (Vec::new(), Vec::new()));
}

#[test]
fn an_exit_mid_turn_ends_the_turn_failed_then_the_child() {
    let mut conversation = Conversation::default();
    conversation
        .send(message("m-1", "one"), Urgency::Wake)
        .unwrap();
    let exit = Exit {
        code: Some(3),
        signal: None,
    };
    let observations = conversation.exited(exit, "boom".to_string());
    assert_eq!(
        observations,
        vec![
            ChildObservation::TurnEnded {
                message_ids: vec!["m-1".to_string()],
                outcome: TurnOutcome::Failed {
                    class: FailureClass::EngineError,
                    detail: "the engine exited mid-turn: boom".to_string(),
                    native_id: None,
                },
            },
            ChildObservation::Exited {
                exit,
                detail: "boom".to_string()
            },
        ]
    );
    assert_eq!(
        conversation.send(message("m-2", "two"), Urgency::Wake),
        Err(ChildError::Closed)
    );
}

#[test]
fn an_idle_exit_is_the_exit_alone() {
    let mut conversation = Conversation::default();
    let exit = Exit {
        code: Some(0),
        signal: None,
    };
    assert_eq!(
        conversation.exited(exit, String::new()),
        vec![ChildObservation::Exited {
            exit,
            detail: String::new()
        }]
    );
}
