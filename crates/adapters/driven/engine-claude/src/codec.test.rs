use super::*;
use surface_ports::MessageDisplay;

fn decode(payload: &str) -> HookEvent {
    Codec.decode("session_start", payload).unwrap()
}

#[test]
fn test_a_payload_is_read_into_the_neutral_event() {
    let ev = decode(
        r#"{"session_id":"s1","hook_event_name":"PostToolUse","transcript_path":"/t.jsonl","tool_name":"Skill","tool_input":{"skill":"admit","args":""},"prompt":"p","source":"startup"}"#,
    );
    assert_eq!(ev.event, "post_tool");
    assert_eq!(ev.native_session, "s1");
    assert_eq!(ev.transcript, "/t.jsonl");
    assert_eq!(ev.skill, "admit");
    assert_eq!(ev.tool, "Skill");
    assert_eq!(ev.prompt, "p");
    assert_eq!(ev.source, "startup");
}

#[test]
fn test_the_registered_event_is_used_when_the_payload_names_none() {
    assert_eq!(decode("{}").event, "session_start");
}

#[test]
fn test_every_native_event_has_its_unified_name() {
    for (native, unified) in [
        ("PreToolUse", "pre_tool"),
        ("SessionEnd", "session_end"),
        ("Stop", "turn_end"),
        ("UserPromptSubmit", "turn_start"),
        ("PermissionRequest", "permission_ask"),
        ("Unlisted", "Unlisted"),
    ] {
        let payload = format!(r#"{{"hook_event_name":"{native}"}}"#);
        assert_eq!(decode(&payload).event, unified);
    }
}

#[test]
fn test_only_the_skill_tool_runs_a_skill() {
    let other = decode(r#"{"tool_name":"Bash","tool_input":{"skill":"admit"}}"#);
    assert_eq!(other.skill, "");
    let empty = decode(r#"{"tool_name":"Skill","tool_input":{}}"#);
    assert_eq!(empty.skill, "");
}

#[test]
fn test_the_edited_file_is_the_file_path_or_the_notebook_path() {
    assert_eq!(
        decode(r#"{"tool_input":{"file_path":"a.md"}}"#).path,
        "a.md"
    );
    assert_eq!(
        decode(r#"{"tool_input":{"notebook_path":"n.ipynb"}}"#).path,
        "n.ipynb"
    );
    assert_eq!(decode(r#"{"tool_input":{"command":"ls"}}"#).path, "");
}

#[test]
fn test_a_payload_that_is_no_json_is_refused() {
    assert!(matches!(
        Codec.decode("session_start", "nope"),
        Err(SurfaceError::Payload { .. })
    ));
}

#[test]
fn test_context_rides_hook_specific_output_named_for_the_native_event() {
    let r = HookResponse {
        context: "hello".into(),
        ..HookResponse::default()
    };
    assert_eq!(
        Codec.encode("session_start", &r).unwrap(),
        "{\"hookSpecificOutput\":{\"hookEventName\":\"SessionStart\",\"additionalContext\":\"hello\"}}\n"
    );
    assert!(Codec
        .encode("post_tool", &r)
        .unwrap()
        .contains("PostToolUse"));
    assert!(Codec
        .encode("turn_start", &r)
        .unwrap()
        .contains("UserPromptSubmit"));
}

#[test]
fn test_a_notice_rides_system_message_and_nothing_is_an_empty_object() {
    let r = HookResponse {
        notice: "n".into(),
        ..HookResponse::default()
    };
    assert_eq!(
        Codec.encode("session_start", &r).unwrap(),
        "{\"systemMessage\":\"n\"}\n"
    );
    assert_eq!(
        Codec
            .encode("session_start", &HookResponse::default())
            .unwrap(),
        "{}\n"
    );
}

#[test]
fn test_context_on_an_event_that_cannot_carry_it_is_refused() {
    let r = HookResponse {
        context: "c".into(),
        ..HookResponse::default()
    };
    assert!(matches!(
        Codec.encode("pre_tool", &r),
        Err(SurfaceError::Unsupported { .. })
    ));
}

#[test]
fn test_a_block_is_a_decision_only_where_claude_lets_it() {
    let r = HookResponse {
        block: true,
        reason: "no".into(),
        ..HookResponse::default()
    };
    assert_eq!(
        Codec.encode("turn_start", &r).unwrap(),
        "{\"decision\":\"block\",\"reason\":\"no\"}\n"
    );
    assert!(Codec.encode("session_start", &r).is_err());
}

#[test]
fn test_the_skills_a_transcript_invoked_are_its_main_threads() {
    let main = r#"{"type":"assistant","isSidechain":false,"message":{"content":[{"type":"text","text":"x"},{"type":"tool_use","name":"Skill","input":{"skill":"one"}},{"type":"tool_use","name":"Bash","input":{"skill":"no"}}]}}"#;
    let side = r#"{"type":"assistant","isSidechain":true,"message":{"content":[{"type":"tool_use","name":"Skill","input":{"skill":"two"}}]}}"#;
    let again = r#"{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Skill","input":{"skill":"one"}}]}}"#;
    let user = r#"{"type":"user","message":{"content":[{"type":"tool_use","name":"Skill","input":{"skill":"three"}}]}}"#;
    let transcript = format!("{main}\nnot json\n{side}\n{again}\n{user}\n");
    assert_eq!(Codec.invoked_skills(&transcript), vec!["one".to_string()]);
}

// ---------------------------------------------------------------------------
// Real payloads from a live claude 2.1.319 run, in
// `fixtures/` (paths stripped): what the primary agent's hooks carry, which the
// codec must decode into the neutral event (plan sultry-coauthor, Q10).

const STOP: &str = include_str!("fixtures/stop.json");
const MESSAGE_DISPLAY: &str = include_str!("fixtures/message_display.json");
const POST_TOOL_BATCH: &str = include_str!("fixtures/post_tool_batch.json");
const USER_PROMPT_SUBMIT: &str = include_str!("fixtures/user_prompt_submit.json");
const SESSION_START: &str = include_str!("fixtures/session_start.json");
const SESSION_END: &str = include_str!("fixtures/session_end.json");

const SESSION: &str = "9d764b45-a56a-48d7-ac9d-c2a16c5669f1";
const PROMPT: &str = "6f73c98d-c5b4-4d59-b470-6bfaa2590443";

#[test]
fn test_a_stop_carries_the_final_answer_and_the_turn_it_ends() {
    let ev = decode(STOP);
    assert_eq!(ev.event, "turn_end");
    assert_eq!(ev.native_session, SESSION);
    assert_eq!(ev.prompt_id, PROMPT);
    assert_eq!(
        ev.last_assistant_message,
        "The command `echo hi` ran and printed `hi`."
    );
}

#[test]
fn test_a_user_prompt_starts_the_turn_it_names() {
    let ev = decode(USER_PROMPT_SUBMIT);
    assert_eq!(ev.event, "turn_start");
    assert_eq!(ev.prompt, "Run the shell command: echo hi");
    assert_eq!(ev.prompt_id, PROMPT);
}

#[test]
fn test_a_message_display_is_a_piece_of_the_assistants_text() {
    let ev = decode(MESSAGE_DISPLAY);
    assert_eq!(ev.event, "message_display");
    assert_eq!(ev.prompt_id, PROMPT);
    assert_eq!(
        ev.message,
        Some(MessageDisplay {
            turn_id: "55fb29f8-a419-4eeb-b58c-a00f05529c4e".into(),
            message_id: "546d2185-586c-49b2-b43d-d7b9cd4caa8b".into(),
            index: 0,
            is_final: true,
            delta: "The command `echo hi` ran and printed `hi`.".into(),
        })
    );
}

#[test]
fn test_a_post_tool_batch_lists_its_tool_calls_in_order() {
    let ev = decode(POST_TOOL_BATCH);
    assert_eq!(ev.event, "post_tool_batch");
    assert_eq!(ev.prompt_id, PROMPT);
    assert_eq!(ev.tool_calls.len(), 1);
    let call = &ev.tool_calls[0];
    assert_eq!(call.tool, "Bash");
    assert_eq!(call.tool_use_id, "toolu_014cEcZFXQnJtuEC6ZURCHLD");
    assert_eq!(call.response, "hi");
    // The input is carried as compact JSON, whole.
    let input = Json::parse(&call.input).unwrap();
    assert_eq!(input.get("command").and_then(Json::as_str), Some("echo hi"));
}

#[test]
fn test_a_session_end_says_why_it_ended() {
    let ev = decode(SESSION_END);
    assert_eq!(ev.event, "session_end");
    assert_eq!(ev.reason, "prompt_input_exit");
    assert_eq!(ev.native_session, SESSION);
}

// Not red: pins that the new fields do not disturb what the bind reads.
#[test]
fn test_a_session_start_still_carries_its_source_and_session() {
    let ev = decode(SESSION_START);
    assert_eq!(ev.event, "session_start");
    assert_eq!(ev.native_session, SESSION);
    assert_eq!(ev.source, "startup");
    assert_eq!(ev.last_assistant_message, "");
    assert_eq!(ev.message, None);
    assert!(ev.tool_calls.is_empty());
}

// The seam the hook command dispatches through: the native hook name to the
// unified event, with no payload in hand (`hook claude-code execute Stop`).
#[test]
fn test_a_native_hook_name_maps_to_its_unified_event() {
    for (native, unified) in [
        ("SessionStart", "session_start"),
        ("SessionEnd", "session_end"),
        ("UserPromptSubmit", "turn_start"),
        ("Stop", "turn_end"),
        ("PreToolUse", "pre_tool"),
        ("PostToolUse", "post_tool"),
        ("PostToolBatch", "post_tool_batch"),
        ("MessageDisplay", "message_display"),
        ("PermissionRequest", "permission_ask"),
        ("Unlisted", "Unlisted"),
    ] {
        assert_eq!(Codec.unified_event(native), unified, "{native}");
    }
}

// The two tables agree: decoding the payload of a native event and mapping
// its bare name give the same unified event.
#[test]
fn test_decode_and_the_name_mapping_agree() {
    for native in [
        "SessionStart",
        "Stop",
        "UserPromptSubmit",
        "PostToolBatch",
        "MessageDisplay",
    ] {
        let payload = format!(r#"{{"hook_event_name":"{native}"}}"#);
        assert_eq!(
            decode(&payload).event,
            Codec.unified_event(native),
            "{native}"
        );
    }
}

// Live probe (2026-11-09): the payloads of the compaction hooks, as claude
// sent them.
#[test]
fn test_a_compaction_names_its_trigger_and_post_compact_carries_the_summary() {
    let pre = decode(
        r#"{"session_id":"s1","prompt_id":"08f6beb1","hook_event_name":"PreCompact","trigger":"manual","custom_instructions":null}"#,
    );
    assert_eq!(pre.event, "PreCompact");
    assert_eq!(pre.prompt_id, "08f6beb1");
    assert_eq!(pre.trigger, "manual");
    assert_eq!(pre.compact_summary, "");
    let post = decode(
        r#"{"session_id":"s1","prompt_id":"08f6beb1","hook_event_name":"PostCompact","trigger":"auto","compact_summary":"The user asked."}"#,
    );
    assert_eq!(post.event, "PostCompact");
    assert_eq!(post.trigger, "auto");
    assert_eq!(post.compact_summary, "The user asked.");
}
