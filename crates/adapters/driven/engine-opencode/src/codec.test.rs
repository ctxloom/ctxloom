//! The plugin's payloads (opencode 1.18.4's bus events as the live probe saw
//! them, opencode-probe.report.md, plus what the plugin alone knows under
//! `ctxloom`) read into the unified hook event, by the mapping of ruling 58 as
//! the probe corrected it.

use surface_ports::{HookCodec, HookEvent, HookResponse, MessageDisplay, ToolCall, ToolStatus};

use super::Codec;

const SESSION: &str = "ses_e3c648397ffesrvA00TD1hAwdC";
/// The prompt's user message: `chat.message`'s `output.message.id`.
const PROMPT_ID: &str = "msg_1c39b7ca0001Vu5HlXKcT90BCg";

fn fixture(name: &str) -> &'static str {
    match name {
        "chat_message" => include_str!("fixtures/chat_message.json"),
        "text_part_end" => include_str!("fixtures/text_part_end.json"),
        "text_part_open" => include_str!("fixtures/text_part_open.json"),
        "reasoning_part_end" => include_str!("fixtures/reasoning_part_end.json"),
        "assistant_open" => include_str!("fixtures/assistant_open.json"),
        "assistant_completed" => include_str!("fixtures/assistant_completed.json"),
        "tool_steps_completed" => include_str!("fixtures/tool_steps_completed.json"),
        "tool_completed" => include_str!("fixtures/tool_completed.json"),
        "tool_running" => include_str!("fixtures/tool_running.json"),
        "tool_error" => include_str!("fixtures/tool_error.json"),
        "session_idle" => include_str!("fixtures/session_idle.json"),
        "compaction_part" => include_str!("fixtures/compaction_part.json"),
        "summary_completed" => include_str!("fixtures/summary_completed.json"),
        "aborted" => include_str!("fixtures/aborted.json"),
        other => panic!("no fixture {other}"),
    }
}

/// `payload` as `hook opencode execute <native>` decodes it.
fn decode(native: &str, payload: &str) -> HookEvent {
    let codec = Codec;
    codec
        .decode(&codec.unified_event(native), payload)
        .unwrap_or_else(|e| panic!("{native}: {e:?}"))
}

/// The unified events that `observe` makes no observation of.
fn observes_nothing(event: &HookEvent) -> bool {
    !matches!(
        event.event.as_str(),
        "turn_start"
            | "message_display"
            | "post_tool_batch"
            | "turn_end"
            | "session_end"
            | "compaction_before"
            | "compaction_after"
    )
}

#[test]
fn test_each_native_name_maps_to_its_unified_event() {
    let codec = Codec;
    for (native, unified) in [
        ("chat.message", "turn_start"),
        ("message.part.updated", "message_display"),
        ("message.updated", "turn_end"),
        ("session.idle", "turn_end"),
        ("something.new", "something.new"),
    ] {
        assert_eq!(codec.unified_event(native), unified, "{native}");
    }
}

// P1: the prompt is `chat.message`; its id is `output.message.id` (under `run`
// `input.messageID` is undefined), its text the parts that are text and not
// synthetic (ctxloom's own injected context is synthetic), joined by lines.
#[test]
fn test_a_chat_message_is_the_prompt_by_its_user_message_id() {
    let event = decode("chat.message", fixture("chat_message"));
    assert_eq!(event.event, "turn_start");
    assert_eq!(event.prompt_id, PROMPT_ID);
    assert_eq!(event.prompt, "Reply with exactly: ok");
    assert_eq!(event.native_session, SESSION);
}

#[test]
fn test_a_prompts_text_parts_are_joined_by_lines_and_an_empty_one_is_no_prompt() {
    let two = r#"{"type":"chat.message","properties":{"sessionID":"ses_1","message":{"id":"msg_1"},"parts":[{"type":"text","text":"one"},{"type":"text","text":"ignored","ignored":true},{"type":"text","text":"two"}]},"ctxloom":{}}"#;
    assert_eq!(decode("chat.message", two).prompt, "one\ntwo");
    let none = r#"{"type":"chat.message","properties":{"sessionID":"ses_1","message":{"id":"msg_1"},"parts":[{"type":"text","text":"x","synthetic":true}]},"ctxloom":{}}"#;
    assert!(observes_nothing(&decode("chat.message", none)));
}

// P1: a text part's last update has `time.end`; it is one text, whole: its own
// id the message, the step message its turn, the prompt the plugin's map of
// the step message's parent.
#[test]
fn test_a_finished_text_part_is_one_whole_text_of_its_prompt() {
    let event = decode("message.part.updated", fixture("text_part_end"));
    assert_eq!(event.event, "message_display");
    assert_eq!(event.prompt_id, PROMPT_ID);
    assert_eq!(event.native_session, SESSION);
    assert_eq!(
        event.message,
        Some(MessageDisplay {
            turn_id: "msg_1c39b7cc8001hR2pQx7Lm4Nb9V".to_string(),
            message_id: "prt_1c39b7d0e001Ls1aH8dEy6GbZm".to_string(),
            index: 0,
            is_final: true,
            delta: "ok".to_string(),
        })
    );
}

// An unfinished text part, a reasoning part (ruling 10: no thinking) and a
// synthetic or ignored text part hold nothing.
#[test]
fn test_an_open_text_a_reasoning_part_and_a_synthetic_text_hold_nothing() {
    for name in ["text_part_open", "reasoning_part_end"] {
        let event = decode("message.part.updated", fixture(name));
        assert!(observes_nothing(&event), "{name}: {event:?}");
        assert_eq!(event.message, None, "{name}");
    }
    for flag in ["synthetic", "ignored"] {
        let payload = fixture("text_part_end").replace(
            r#""type":"text","text":"ok""#,
            &format!(r#""type":"text","{flag}":true,"text":"ok""#),
        );
        assert!(
            observes_nothing(&decode("message.part.updated", &payload)),
            "{flag}"
        );
    }
}

// P2: a tool part that completed is one call: the part id (stable across its
// states, unlike the provider's callID) its id, its input compact JSON, its
// output the response, and its status.
#[test]
fn test_a_completed_tool_part_is_one_call_with_its_status() {
    let event = decode("message.part.updated", fixture("tool_completed"));
    assert_eq!(event.event, "post_tool_batch");
    assert_eq!(event.prompt_id, "msg_1c39f73f0001Sf5oV2rSm0UpNa");
    assert_eq!(event.tool, "bash");
    assert_eq!(
        event.tool_calls,
        [ToolCall {
            tool: "bash".to_string(),
            tool_use_id: "prt_1c39f745b001c5IdBJ9I7M5n07".to_string(),
            input: r#"{"command":"echo hi"}"#.to_string(),
            response: "hi\n".to_string(),
            status: ToolStatus::Completed,
        }]
    );
}

// P3: a denied tool ends in `error`, its text the response.
#[test]
fn test_a_failed_tool_part_is_one_call_whose_response_is_its_error() {
    let event = decode("message.part.updated", fixture("tool_error"));
    assert_eq!(event.event, "post_tool_batch");
    assert_eq!(
        event.tool_calls,
        [ToolCall {
            tool: "read".to_string(),
            tool_use_id: "prt_1c3a0fe20001Yr7aH4dEy2GbZm".to_string(),
            input: r#"{"filePath":"/nonexistent_xyz/a.txt"}"#.to_string(),
            response: "The user rejected permission to use this specific tool call.".to_string(),
            status: ToolStatus::Error,
        }]
    );
}

#[test]
fn test_a_tool_part_still_running_holds_nothing() {
    let event = decode("message.part.updated", fixture("tool_running"));
    assert!(observes_nothing(&event), "{event:?}");
    assert!(event.tool_calls.is_empty());
}

// P1: the turn ends at the assistant message that finished (`stop`) and was
// completed, for its parent, the prompt; an aborted one (an error) ends it too.
#[test]
fn test_a_completed_final_assistant_message_ends_its_prompts_turn() {
    let event = decode("message.updated", fixture("assistant_completed"));
    assert_eq!(event.event, "turn_end");
    assert_eq!(event.prompt_id, PROMPT_ID);
    assert_eq!(event.native_session, SESSION);
    let aborted = decode("message.updated", fixture("aborted"));
    assert_eq!(aborted.event, "turn_end");
    assert_eq!(aborted.prompt_id, "msg_1c3a1fe00001Il7uB4xYs8AvTg");
}

// P1, P2, P5: `finish:"stop"` first comes without `time.completed`; a step that
// called tools is not the turn's end; a compaction's summary is not a turn.
#[test]
fn test_an_open_a_tool_step_and_a_summary_message_end_no_turn() {
    for name in ["assistant_open", "tool_steps_completed"] {
        let event = decode("message.updated", fixture(name));
        assert!(observes_nothing(&event), "{name}: {event:?}");
    }
    let summary = decode("message.updated", fixture("summary_completed"));
    assert_ne!(summary.event, "turn_end");
}

// P3: a turn whose tools were all refused ends with `finish:"tool-calls"` and
// then idles: `session.idle` ends the turn the plugin last saw prompted (a
// second end of a turn already ended is the same fact, a DUPLICATE).
#[test]
fn test_an_idle_session_ends_its_last_prompts_turn() {
    let event = decode("session.idle", fixture("session_idle"));
    assert_eq!(event.event, "turn_end");
    assert_eq!(event.prompt_id, "msg_1c3a0fd00001Wn3wD0zAu8CxVi");
    assert_eq!(event.native_session, SESSION);
    let unprompted = r#"{"type":"session.idle","properties":{"sessionID":"ses_1"},"ctxloom":{}}"#;
    assert!(observes_nothing(&decode("session.idle", unprompted)));
}

// P5: `/compact` makes a user message with a compaction part (`auto` the
// trigger), then an assistant message with `summary:true`: the compaction's
// before and after, both of the compaction message, the summary as text the
// capture keeps only the size and digest of.
#[test]
fn test_a_compaction_is_its_part_before_and_its_summary_after() {
    let before = decode("message.part.updated", fixture("compaction_part"));
    assert_eq!(before.event, "compaction_before");
    assert_eq!(before.prompt_id, "msg_1c3a14b1f001Bx3gN0jKe8MhFs");
    assert_eq!(before.trigger, "manual");
    assert_eq!(before.compact_summary, "");
    let auto = fixture("compaction_part").replace(r#""auto":false"#, r#""auto":true"#);
    assert_eq!(decode("message.part.updated", &auto).trigger, "auto");
    let after = decode("message.updated", fixture("summary_completed"));
    assert_eq!(after.event, "compaction_after");
    assert_eq!(after.prompt_id, "msg_1c3a14b1f001Bx3gN0jKe8MhFs");
    assert_eq!(after.trigger, "manual");
    assert_eq!(
        after.compact_summary,
        "The user asked for exactly ok, and the assistant replied ok."
    );
}

// The plugin forwards opencode's bus; anything it has no mapping for, and a
// payload with no type, holds nothing. A payload that is not JSON is refused.
#[test]
fn test_an_unmapped_or_untyped_payload_holds_nothing_and_garbage_is_refused() {
    for payload in [
        r#"{"type":"permission.asked","properties":{"sessionID":"ses_1"},"ctxloom":{}}"#,
        r#"{"properties":{"sessionID":"ses_1"}}"#,
        r#"{"type":"message.part.updated","properties":{"sessionID":"ses_1","part":{"type":"step-finish","id":"prt_1"}}}"#,
    ] {
        assert!(
            observes_nothing(&decode("message.updated", payload)),
            "{payload}"
        );
    }
    assert!(Codec.decode("turn_end", "not json").is_err());
}

// The plugin reads no answer: the hook answers an empty object, and anything
// it would have to carry (a block, context, a notice) is refused as unsupported.
#[test]
fn test_the_answer_is_an_empty_object_and_nothing_more_can_be_carried() {
    assert_eq!(
        Codec.encode("turn_end", &HookResponse::default()).unwrap(),
        "{}\n"
    );
    for response in [
        HookResponse {
            block: true,
            ..HookResponse::default()
        },
        HookResponse {
            context: "x".to_string(),
            ..HookResponse::default()
        },
        HookResponse {
            notice: "x".to_string(),
            ..HookResponse::default()
        },
    ] {
        assert!(
            Codec.encode("turn_start", &response).is_err(),
            "{response:?}"
        );
    }
    assert_eq!(Codec.invoked_skills("{}"), Vec::<String>::new());
}
