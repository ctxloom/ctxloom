//! The transcript folded from the session stream (PROPOSAL,
//! design/hook-capture). The envelope is D27's: `v, harp, session_id, engine,
//! seq, ts, kind`. The kinds beyond `entry` are proposed here: `assistant_delta`,
//! `tool`, `turn_end`, `session_end`, `compaction`.

use serde_json::{json, Value};
use sessions::{CompactionPhase, SessionEvent};

use super::*;
use crate::transcript::{entry_line, Speaker};

const SESSION: &str = "0b2c9a52-1111-4222-8333-444455556666";

fn projection() -> Projection {
    Projection::new("amber-swift-owl", SESSION, "claude-code")
}

fn prompt(text: &str) -> SessionEvent {
    SessionEvent::PromptSubmitted(sessions::PromptSubmitted {
        prompt_id: "p1".to_string(),
        prompt: text.to_string(),
    })
}

fn delta(message: &str, index: u32, is_final: bool, text: &str) -> SessionEvent {
    SessionEvent::AssistantTextShown(sessions::AssistantTextShown {
        prompt_id: "p1".to_string(),
        turn_id: "t1".to_string(),
        message_id: message.to_string(),
        index,
        is_final,
        text: text.to_string(),
    })
}

fn tool() -> SessionEvent {
    SessionEvent::ToolCalled(sessions::ToolCalled {
        prompt_id: "p1".to_string(),
        tool_use_id: "toolu_1".to_string(),
        tool: "Bash".to_string(),
        input: r#"{"command":"echo hi"}"#.to_string(),
        response_bytes: 3,
        response_sha256: "ab".repeat(32),
        status: None,
    })
}

fn turn_ended() -> SessionEvent {
    SessionEvent::TurnEnded(sessions::TurnEnded {
        prompt_id: "p1".to_string(),
    })
}

fn minted() -> SessionEvent {
    SessionEvent::KeptChanged(sessions::KeptChanged { kept: true })
}

fn parse(lines: &[String]) -> Vec<Value> {
    lines
        .iter()
        .map(|line| {
            assert!(line.ends_with('\n'), "{line:?}");
            assert_eq!(line.matches('\n').count(), 1, "one line: {line:?}");
            serde_json::from_str(line.trim_end()).unwrap()
        })
        .collect()
}

#[test]
fn a_prompt_is_the_users_entry_in_d27s_envelope() {
    let mut p = projection();
    let lines = p.fold(3, 1_700_000_000, &prompt("echo hi\nplease"));
    assert_eq!(
        parse(&lines),
        [json!({
            "v": 1,
            "harp": "amber-swift-owl",
            "session_id": SESSION,
            "engine": "claude-code",
            "seq": 0,
            "ts": "2023-11-14T22:13:20Z",
            "kind": "entry",
            "entry": {"type": "user", "content": "echo hi\nplease"},
        })]
    );
}

// Every delta is a line of its own (owner ruling 2026-11-08); a reader joins
// a message's pieces by message_id and index.
#[test]
fn a_message_display_stream_is_one_line_per_delta() {
    let mut p = projection();
    let mut all = Vec::new();
    all.extend(p.fold(4, 10, &delta("m1", 0, false, "The command ")));
    all.extend(p.fold(5, 11, &delta("m1", 1, false, "`echo hi` ran ")));
    all.extend(p.fold(6, 12, &delta("m1", 2, true, "and printed hi.")));
    let lines = parse(&all);
    assert_eq!(lines.len(), 3);
    assert_eq!(lines[0]["kind"], "assistant_delta");
    assert_eq!(
        lines[2]["delta"],
        json!({"message_id": "m1", "index": 2, "final": true, "text": "and printed hi."})
    );
    assert_eq!(lines[1]["delta"]["final"], false);
}

// Ruling 58: the line says how the call ended when the engine said, and
// carries no status when it did not (claude's).
#[test]
fn a_tool_line_carries_the_calls_status_when_it_is_known() {
    for (status, said) in [
        (sessions::ToolCallStatus::Completed, "completed"),
        (sessions::ToolCallStatus::Error, "error"),
    ] {
        let SessionEvent::ToolCalled(mut called) = tool() else {
            unreachable!()
        };
        called.status = Some(status);
        let lines = parse(&projection().fold(7, 0, &SessionEvent::ToolCalled(called)));
        assert_eq!(lines[0]["tool"]["status"], said);
    }
    let unknown = parse(&projection().fold(7, 0, &tool()));
    assert!(
        unknown[0]["tool"].get("status").is_none(),
        "no status where the engine said none: {}",
        unknown[0]
    );
}

// Ruling B: the tool's input, and its result as size and digest. No body.
#[test]
fn a_tool_call_is_a_line_with_input_size_and_digest_and_no_result_body() {
    let mut p = projection();
    let lines = parse(&p.fold(7, 0, &tool()));
    assert_eq!(lines[0]["kind"], "tool");
    assert_eq!(
        lines[0]["tool"],
        json!({
            "id": "toolu_1",
            "name": "Bash",
            "input": r#"{"command":"echo hi"}"#,
            "response_bytes": 3,
            "response_sha256": "ab".repeat(32),
        })
    );
    assert!(lines[0]["tool"].get("response").is_none());
}

#[test]
fn the_other_observations_have_their_own_kinds() {
    let mut p = projection();
    let ended = parse(&p.fold(1, 0, &turn_ended()));
    assert_eq!(ended[0]["kind"], "turn_end");
    assert_eq!(ended[0]["prompt_id"], "p1");
    let session_end = parse(&p.fold(
        2,
        0,
        &SessionEvent::SessionEnded(sessions::SessionEnded {
            native_session: "n1".to_string(),
            reason: "prompt_input_exit".to_string(),
        }),
    ));
    assert_eq!(session_end[0]["kind"], "session_end");
    assert_eq!(session_end[0]["reason"], "prompt_input_exit");
    let compaction = parse(&p.fold(
        3,
        0,
        &SessionEvent::Compacted(sessions::Compacted {
            prompt_id: "p1".to_string(),
            phase: CompactionPhase::After,
            trigger: "auto".to_string(),
            summary_bytes: 12,
            summary_sha256: "ab".to_string(),
        }),
    ));
    assert_eq!(compaction[0]["kind"], "compaction");
    assert_eq!(compaction[0]["phase"], "after");
    // Of the summary, its size and digest and nothing of its text.
    assert_eq!(compaction[0]["trigger"], "auto");
    assert_eq!(compaction[0]["summary_bytes"], 12);
    assert_eq!(compaction[0]["summary_sha256"], "ab");
}

// seq is the projector's: the order lines were made in, by the stream's order,
// counting lines and not stream pages.
#[test]
fn seq_counts_lines_in_stream_order_not_pages() {
    let mut p = projection();
    let mut all = Vec::new();
    all.extend(p.fold(2, 0, &prompt("a")));
    all.extend(p.fold(3, 0, &minted()));
    all.extend(p.fold(9, 0, &delta("m1", 0, true, "b")));
    all.extend(p.fold(40, 0, &tool()));
    let seqs: Vec<u64> = parse(&all)
        .iter()
        .map(|l| l["seq"].as_u64().unwrap())
        .collect();
    assert_eq!(seqs, [0, 1, 2]);
    assert_eq!(p.lines(), 3);
}

#[test]
fn an_event_that_is_not_the_transcripts_adds_no_line() {
    let mut p = projection();
    assert!(p.fold(0, 0, &minted()).is_empty());
    assert_eq!(p.lines(), 0);
}

// The store delivers a page again (a catch-up restarts): no line is doubled.
#[test]
fn a_page_handed_again_adds_no_line() {
    let mut p = projection();
    assert_eq!(p.fold(2, 0, &prompt("a")).len(), 1);
    assert!(p.fold(2, 0, &prompt("a")).is_empty());
    let next = parse(&p.fold(3, 0, &delta("m1", 0, true, "b")));
    assert_eq!(next[0]["seq"], 1, "the repeat took no seq");
}

// Two interleaved messages of one turn keep the stream's order, not their
// message ids'.
#[test]
fn interleaved_facts_keep_the_stream_order() {
    let mut p = projection();
    let mut all = Vec::new();
    all.extend(p.fold(1, 0, &delta("m2", 0, false, "second-message-first")));
    all.extend(p.fold(2, 0, &tool()));
    all.extend(p.fold(3, 0, &delta("m1", 0, true, "after")));
    let kinds: Vec<String> = parse(&all)
        .iter()
        .map(|l| l["kind"].as_str().unwrap().to_string())
        .collect();
    assert_eq!(kinds, ["assistant_delta", "tool", "assistant_delta"]);
}

// A killed engine never fires Stop: the projector invents no end. The next
// prompt just follows.
#[test]
fn a_turn_without_a_stop_is_left_open() {
    let mut p = projection();
    let mut all = Vec::new();
    all.extend(p.fold(1, 0, &prompt("first")));
    all.extend(p.fold(2, 0, &delta("m1", 0, false, "partial")));
    all.extend(p.fold(3, 0, &prompt("second")));
    let kinds: Vec<String> = parse(&all)
        .iter()
        .map(|l| l["kind"].as_str().unwrap().to_string())
        .collect();
    assert_eq!(kinds, ["entry", "assistant_delta", "entry"]);
}

// The title of a session is the first prompt of its transcript: the user's
// entry keeps the shape the launcher already writes.
#[test]
fn a_prompt_line_is_the_line_the_launcher_writes() {
    let mut p = projection();
    let line = p.fold(1, 86_400, &prompt("hello")).remove(0);
    assert_eq!(
        line,
        entry_line(
            "amber-swift-owl",
            SESSION,
            "claude-code",
            0,
            86_400,
            Speaker::User,
            "hello"
        )
    );
}
