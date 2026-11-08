//! The engines' callbacks (cli/session_hooks.feature, cli/post_tool_hooks.feature).

use super::*;
use crate::fakes::{world, CONFIG};

// session_hooks.feature: the hook answers through the codec of the engine it
// was registered under; an engine it does not know is refused by name.
#[test]
fn test_a_hook_under_an_unknown_engine_is_refused_by_name() {
    let w = world(CONFIG);
    let err = w.setup.session_start("nosuch", "{}").unwrap_err();
    let SetupError::UnknownHookEngine { engine, known } = err else {
        panic!()
    };
    assert_eq!(
        (engine.as_str(), known.as_str()),
        ("nosuch", "claude-code, mock")
    );
}

// Go's session-start: a payload it cannot read is an error (an engine shows a
// failed hook, which is the truth).
#[test]
fn test_a_payload_that_does_not_read_fails_the_hook() {
    let w = world(CONFIG);
    assert!(w.setup.session_start("claude-code", "not json").is_err());
    assert!(w.setup.skill_mates("claude-code", "not json").is_err());
}

// session_hooks.feature: Scenario: The SessionStart hook never hands the
// engine the project's context - what it hands is a notice, and only when the
// project has profiles and no agents.
#[test]
fn test_session_start_nudges_only_a_project_with_profiles_and_no_agents() {
    let none = world("schema_version: 8\n");
    let out = none.setup.session_start("claude-code", "{}").unwrap();
    assert!(
        out.starts_with("context=;notice=ctxloom: this project has profiles but no agents"),
        "{out}"
    );
    assert!(!out.contains("FRAGMENT-BODY"));
    let agents = world(CONFIG);
    assert_eq!(
        agents.setup.session_start("claude-code", "{}").unwrap(),
        "context=;notice=\n"
    );
}

// post_tool_hooks.feature: Scenario: Completing one skill of a linked pair
// names the other
#[test]
fn test_completing_one_skill_names_the_linked_skill_not_yet_invoked() {
    let w = world(CONFIG);
    let out = w
        .setup
        .skill_mates("claude-code", r#"{"skill":"admit"}"#)
        .unwrap();
    assert_eq!(
        out,
        "context=Skill admit completed; linked skills not yet invoked this session: unattended;notice=\n"
    );
}

// post_tool_hooks.feature: the transcript is where "already invoked" comes
// from, so nothing is persisted to answer it.
#[test]
fn test_a_mate_the_transcript_shows_invoked_is_not_named() {
    let w = world(CONFIG);
    w.store
        .state()
        .files
        .insert("/t/turn.jsonl".to_string(), b"unattended\n".to_vec());
    let out = w
        .setup
        .skill_mates(
            "claude-code",
            r#"{"skill":"admit","transcript":"/t/turn.jsonl"}"#,
        )
        .unwrap();
    assert_eq!(out, "context=;notice=\n");
}

// post_tool_hooks.feature: Scenario: A tool call that is not a skill is passed
// over in silence
#[test]
fn test_a_call_that_ran_no_skill_is_silent() {
    let w = world(CONFIG);
    assert_eq!(
        w.setup.skill_mates("claude-code", "{}").unwrap(),
        "context=;notice=\n"
    );
}

// A skill in no group, or a skill the profile does not deliver, has no mates.
#[test]
fn test_a_skill_in_no_group_has_no_mates() {
    let w = world(CONFIG);
    assert_eq!(
        w.setup
            .skill_mates("claude-code", r#"{"skill":"nosuch"}"#)
            .unwrap(),
        "context=;notice=\n"
    );
}

// The hooks run degraded: a config the strict load refuses (an unknown key)
// must not silence the nudge or the mates.
#[test]
fn test_the_hooks_answer_from_a_config_that_a_strict_load_would_refuse() {
    let nudged = world("schema_version: 8\nbogus_key: 1\n");
    let out = nudged.setup.session_start("claude-code", "{}").unwrap();
    assert!(
        out.contains("notice=ctxloom: this project has profiles"),
        "{out}"
    );

    let strict_refused: &'static str =
        Box::leak(format!("{CONFIG}bogus_key: 1\n").into_boxed_str());
    let mates = world(strict_refused);
    let out = mates
        .setup
        .skill_mates("claude-code", r#"{"skill":"admit"}"#)
        .unwrap();
    assert!(
        out.contains("linked skills not yet invoked this session: unattended"),
        "{out}"
    );
}

// ---------------------------------------------------------------------------
// RED (port/run-one-shot): `hook <engine> execute <native hook name>`.

const MARKER: &str = r#"<ctxloom name="brisk-copper-moth" kind="harp" />"#;

#[test]
fn test_executing_a_hook_of_an_unknown_engine_is_refused_listing_the_known_ones() {
    let w = world(CONFIG);
    let err = w
        .setup
        .execute_hook("nosuch", "Stop", "{}", "brisk-copper-moth")
        .unwrap_err();
    let SetupError::UnknownHookEngine { engine, known } = err else {
        panic!()
    };
    assert_eq!(
        (engine.as_str(), known.as_str()),
        ("nosuch", "claude-code, mock")
    );
}

#[test]
fn test_a_session_start_answers_the_marker_and_the_notice_and_asks_for_the_bind() {
    let w = world("schema_version: 8\n");
    let answer = w
        .setup
        .execute_hook(
            "claude-code",
            "SessionStart",
            r#"{"session":"vendor-42","transcript":"/t/a.jsonl"}"#,
            "brisk-copper-moth",
        )
        .unwrap();
    // The fake wire spells an answer `context=..;notice=..`.
    assert!(
        answer.stdout.starts_with(&format!(
            "context={MARKER};notice=ctxloom: this project has profiles but no agents"
        )),
        "{}",
        answer.stdout
    );
    assert_eq!(
        answer.bind,
        Some(SessionBind {
            harp: "brisk-copper-moth".into(),
            native_id: "vendor-42".into(),
            transcript: "/t/a.jsonl".into(),
        })
    );
    assert!(!answer.harp_missing);
}

#[test]
fn test_a_session_start_with_no_usable_harp_says_nothing_and_binds_nothing() {
    let w = world(CONFIG);
    for harp in ["", "has\"quote", "has>angle"] {
        let answer = w
            .setup
            .execute_hook("claude-code", "SessionStart", "{}", harp)
            .unwrap();
        assert!(!answer.stdout.contains("kind=\"harp\""), "{harp:?}");
        assert_eq!(answer.bind, None, "{harp:?}");
        assert!(answer.harp_missing, "{harp:?}");
    }
}

#[test]
fn test_a_post_tool_call_answers_what_skill_mates_does() {
    let w = world(CONFIG);
    let answer = w
        .setup
        .execute_hook("claude-code", "PostToolUse", r#"{"skill":"admit"}"#, "")
        .unwrap();
    assert_eq!(
        answer.stdout,
        w.setup
            .skill_mates("claude-code", r#"{"skill":"admit"}"#)
            .unwrap()
    );
    assert_eq!(answer.bind, None);
}

// The primary agent's data events are recorded elsewhere (M2); here they are
// silence on the wire and no bind.
#[test]
fn test_the_events_that_carry_data_answer_silence() {
    let w = world(CONFIG);
    for native in ["Stop", "MessageDisplay", "PostToolBatch", "SessionEnd"] {
        let answer = w
            .setup
            .execute_hook("claude-code", native, "{}", "brisk-copper-moth")
            .unwrap();
        assert_eq!(answer.stdout, "context=;notice=\n", "{native}");
        assert_eq!(answer.bind, None, "{native}");
        assert!(!answer.harp_missing, "{native}");
    }
}

#[test]
fn test_a_payload_that_does_not_read_fails_the_executed_hook() {
    let w = world(CONFIG);
    assert!(w
        .setup
        .execute_hook("claude-code", "Stop", "not json", "brisk-copper-moth")
        .is_err());
}

// Go: harpmarker TestFormat.
#[test]
fn test_the_marker_names_the_harp() {
    assert_eq!(
        harp_marker("plump-loose-sash").as_deref(),
        Some(r#"<ctxloom name="plump-loose-sash" kind="harp" />"#)
    );
    assert_eq!(harp_marker(""), None);
}

// Go: "A '\"' closes the name attribute early, so the element reads back as a
// DIFFERENT, possibly real harp; a '>' closes the element before kind=\"harp\"
// is reached ... a marker that names the wrong harp is worse than no marker".
#[test]
fn test_a_harp_the_marker_cannot_carry_has_no_marker() {
    assert_eq!(harp_marker("a\"b"), None);
    assert_eq!(harp_marker("a>b"), None);
}
