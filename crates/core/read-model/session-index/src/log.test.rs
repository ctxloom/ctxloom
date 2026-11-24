//! The log folded from the session stream: typed entries, engine-neutral,
//! naming no session, harp or engine; each arm's fields; `seq` counted
//! here; a stream's repeat adds nothing; an engine's data is the entry's
//! `extra` and nothing else.

use std::collections::BTreeMap;

use angzarr_embedded::Timestamp;
use daemon_proto::v1::{log_compaction, log_entry::Entry as Arm, log_tool};
use sessions::{CompactionPhase, SessionEvent};

use super::*;

fn at(seconds: i64) -> Timestamp {
    Timestamp { seconds, nanos: 0 }
}

fn prompt(text: &str) -> SessionEvent {
    SessionEvent::PromptSubmitted(sessions::PromptSubmitted {
        prompt_id: "p1".to_string(),
        prompt: text.to_string(),
        ..Default::default()
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
        ..Default::default()
    })
}

fn tool() -> sessions::ToolCalled {
    sessions::ToolCalled {
        prompt_id: "p1".to_string(),
        tool_use_id: "toolu_1".to_string(),
        tool: "Bash".to_string(),
        input: r#"{"command":"echo hi"}"#.to_string(),
        response_bytes: 3,
        response_sha256: "ab".repeat(32),
        status: None,
        ..Default::default()
    }
}

fn turn_ended() -> SessionEvent {
    SessionEvent::TurnEnded(sessions::TurnEnded {
        prompt_id: "p1".to_string(),
        ..Default::default()
    })
}

fn not_a_log_event() -> SessionEvent {
    SessionEvent::KeptChanged(sessions::KeptChanged { kept: true })
}

fn arm(entry: Option<v1::LogEntry>) -> Arm {
    entry.expect("an entry").entry.expect("an arm")
}

#[test]
fn a_prompt_is_a_prompt_entry_at_its_events_instant() {
    let mut p = Projection::new();
    let entry = p
        .fold(3, at(1_700_000_000), &prompt("echo hi\nplease"))
        .expect("an entry");
    assert_eq!(entry.time.as_ref().map(|t| t.seconds), Some(1_700_000_000));
    assert_eq!(entry.seq, 0);
    assert_eq!(
        entry.entry,
        Some(Arm::Prompt(v1::LogPrompt {
            prompt_id: "p1".to_string(),
            text: "echo hi\nplease".to_string(),
        }))
    );
}

// Every delta is an entry of its own (owner ruling 2026-11-08); a reader
// joins a message's pieces (`conversation::join`).
#[test]
fn every_delta_is_a_words_entry() {
    let mut p = Projection::new();
    let first = arm(p.fold(4, at(10), &delta("m1", 0, false, "The command ")));
    let last = arm(p.fold(5, at(11), &delta("m1", 2, true, "and printed hi.")));
    assert_eq!(
        first,
        Arm::Words(v1::LogWords {
            prompt_id: "p1".to_string(),
            turn_id: "t1".to_string(),
            message_id: "m1".to_string(),
            index: 0,
            is_final: false,
            text: "The command ".to_string(),
        })
    );
    let Arm::Words(last) = last else {
        panic!("words")
    };
    assert!(last.is_final);
    assert_eq!(last.index, 2);
}

// Ruling 58: the entry says how the call ended when the engine said, and
// carries no status when it did not (claude's).
#[test]
fn a_tool_entry_carries_the_calls_status_when_it_is_known() {
    for (status, said) in [
        (
            sessions::ToolCallStatus::Completed,
            log_tool::Status::Completed,
        ),
        (sessions::ToolCallStatus::Error, log_tool::Status::Error),
    ] {
        let mut called = tool();
        called.status = Some(status);
        let Arm::Tool(entry) =
            arm(Projection::new().fold(7, at(0), &SessionEvent::ToolCalled(called)))
        else {
            panic!("tool")
        };
        assert_eq!(entry.status, Some(said as i32));
    }
    let Arm::Tool(unknown) =
        arm(Projection::new().fold(7, at(0), &SessionEvent::ToolCalled(tool())))
    else {
        panic!("tool")
    };
    assert_eq!(unknown.status, None, "no status where the engine said none");
}

// Ruling B: the tool's input, and its result as size and digest. No body.
#[test]
fn a_tool_entry_is_input_size_and_digest_and_no_result_body() {
    let Arm::Tool(entry) = arm(Projection::new().fold(7, at(0), &SessionEvent::ToolCalled(tool())))
    else {
        panic!("tool")
    };
    assert_eq!(
        entry,
        v1::LogTool {
            prompt_id: "p1".to_string(),
            tool_use_id: "toolu_1".to_string(),
            name: "Bash".to_string(),
            input: r#"{"command":"echo hi"}"#.to_string(),
            response_bytes: 3,
            response_sha256: "ab".repeat(32),
            status: None,
        }
    );
}

#[test]
fn a_turn_end_a_compaction_and_a_clear_have_their_own_arms() {
    let mut p = Projection::new();
    assert_eq!(
        arm(p.fold(1, at(0), &turn_ended())),
        Arm::TurnEnd(v1::LogTurnEnd {
            prompt_id: "p1".to_string()
        })
    );
    let compaction = arm(p.fold(
        3,
        at(0),
        &SessionEvent::Compacted(sessions::Compacted {
            prompt_id: "p1".to_string(),
            phase: CompactionPhase::After,
            trigger: "auto".to_string(),
            summary_bytes: 12,
            summary_sha256: "ab".to_string(),
            extra: Default::default(),
        }),
    ));
    // Of the summary, its size and digest and nothing of its text.
    assert_eq!(
        compaction,
        Arm::Compaction(v1::LogCompaction {
            prompt_id: "p1".to_string(),
            phase: log_compaction::Phase::After as i32,
            trigger: "auto".to_string(),
            summary_bytes: 12,
            summary_sha256: "ab".to_string(),
        })
    );
    let before = arm(p.fold(
        4,
        at(0),
        &SessionEvent::Compacted(sessions::Compacted {
            prompt_id: "p1".to_string(),
            phase: CompactionPhase::Before,
            trigger: String::new(),
            summary_bytes: 0,
            summary_sha256: String::new(),
            extra: Default::default(),
        }),
    ));
    let Arm::Compaction(before) = before else {
        panic!("compaction")
    };
    assert_eq!(before.phase, log_compaction::Phase::Before as i32);
    assert_eq!(
        arm(p.fold(
            5,
            at(0),
            &SessionEvent::ConversationCleared(sessions::ConversationCleared {
                native_session: "n2".to_string(),
                trigger: "clear".to_string(),
                ..Default::default()
            }),
        )),
        Arm::Clear(v1::LogClear {
            native_session: "n2".to_string(),
            trigger: "clear".to_string(),
        })
    );
}

// s4-Q14, H3 (PROPOSAL, the owner's to word): a fact a hook dropped is a
// gap entry in its place: which, why, and when the hook dropped it (its
// clock, not the entry's `time`, which is when the gap was recorded).
#[test]
fn a_capture_gap_is_a_gap_entry_in_its_place() {
    let mut p = Projection::new();
    p.fold(1, at(1_700_000_000), &prompt("hi"));
    let entry = p
        .fold(
            2,
            at(1_700_000_100),
            &SessionEvent::CaptureGapped(sessions::CaptureGapped {
                dropped: "hook:turn-end:s:p1".to_string(),
                why: "bound".to_string(),
                dropped_at: Timestamp {
                    seconds: 1_700_000_050,
                    nanos: 0,
                },
                stamp: "v0.2.0-aaaaaaa".to_string(),
            }),
        )
        .expect("an entry");
    assert_eq!(entry.time.as_ref().map(|t| t.seconds), Some(1_700_000_100));
    let Some(Arm::Gap(gap)) = entry.entry else {
        panic!("gap")
    };
    assert_eq!(gap.dropped, "hook:turn-end:s:p1");
    assert_eq!(gap.why, "bound");
    assert_eq!(gap.dropped_at.map(|t| t.seconds), Some(1_700_000_050));
}

// Owner 2026-11-15 (SessionEnded retired in place): a stream's SessionEnded
// makes no entry and takes no seq.
#[test]
fn a_retired_session_end_makes_no_entry_and_takes_no_seq() {
    let mut p = Projection::new();
    assert_eq!(p.fold(1, at(0), &turn_ended()).map(|e| e.seq), Some(0));
    let ended = p.fold(
        2,
        at(0),
        &SessionEvent::SessionEnded(sessions::SessionEnded {
            native_session: "n1".to_string(),
            reason: "prompt_input_exit".to_string(),
        }),
    );
    assert!(ended.is_none(), "{ended:?}");
    assert_eq!(p.fold(3, at(0), &prompt("again")).map(|e| e.seq), Some(1));
    assert_eq!(p.entries(), 2);
}

// seq is the projector's: the order entries were made in, by the stream's
// order, counting entries and not stream pages.
#[test]
fn seq_counts_entries_in_stream_order_not_pages() {
    let mut p = Projection::new();
    let mut seqs = Vec::new();
    for (page, event) in [
        (2, prompt("a")),
        (3, not_a_log_event()),
        (9, delta("m1", 0, true, "b")),
        (40, SessionEvent::ToolCalled(tool())),
    ] {
        seqs.extend(p.fold(page, at(0), &event).map(|e| e.seq));
    }
    assert_eq!(seqs, [0, 1, 2]);
    assert_eq!(p.entries(), 3);
}

#[test]
fn an_event_that_is_not_the_logs_adds_no_entry() {
    let mut p = Projection::new();
    assert!(p.fold(0, at(0), &not_a_log_event()).is_none());
    assert_eq!(p.entries(), 0);
}

// The store delivers a page again (a catch-up restarts): no entry is doubled.
#[test]
fn a_page_handed_again_adds_no_entry() {
    let mut p = Projection::new();
    assert!(p.fold(2, at(0), &prompt("a")).is_some());
    assert!(p.fold(2, at(0), &prompt("a")).is_none());
    assert_eq!(
        p.fold(3, at(0), &delta("m1", 0, true, "b")).map(|e| e.seq),
        Some(1),
        "the repeat took no seq"
    );
}

// The log names no session, harp or engine: the proto has no field for one,
// so the proto3 JSON of an entry cannot either.
#[test]
fn an_entrys_json_names_no_session_harp_or_engine() {
    let entry = Projection::new()
        .fold(1, at(0), &prompt("hi"))
        .expect("an entry");
    let json = serde_json::to_value(&entry).unwrap();
    for key in ["v", "harp", "session_id", "engine", "kind", "line"] {
        assert!(json.get(key).is_none(), "{key} in {json}");
    }
    assert!(json.get("prompt").is_some(), "{json}");
}

// The engine extension point: what an adapter attached reaches the entry's
// `extra` as it was, in every arm, and no common field changes.
#[test]
fn an_observations_engine_data_is_the_entrys_extra_and_nothing_else() {
    let extra: BTreeMap<String, String> =
        BTreeMap::from([("opencode.part_id".to_string(), "prt_1".to_string())]);
    let with = |event: SessionEvent| Projection::new().fold(1, at(0), &event).unwrap();
    let mut called = tool();
    called.extra = extra.clone();
    let tool_entry = with(SessionEvent::ToolCalled(called));
    assert_eq!(tool_entry.extra, extra);
    let plain = with(SessionEvent::ToolCalled(tool()));
    assert!(plain.extra.is_empty(), "none attached, none carried");
    assert_eq!(plain.entry, tool_entry.entry, "the arm is the same");
    let SessionEvent::AssistantTextShown(mut shown) = delta("m1", 0, true, "x") else {
        unreachable!()
    };
    shown.extra = extra.clone();
    assert_eq!(with(SessionEvent::AssistantTextShown(shown)).extra, extra);
    let SessionEvent::PromptSubmitted(mut asked) = (match prompt("x") {
        e @ SessionEvent::PromptSubmitted(_) => e,
        _ => unreachable!(),
    }) else {
        unreachable!()
    };
    asked.extra = extra.clone();
    assert_eq!(with(SessionEvent::PromptSubmitted(asked)).extra, extra);
}
