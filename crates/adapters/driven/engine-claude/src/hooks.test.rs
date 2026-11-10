use super::*;

fn hook(command: &str) -> Hook {
    Hook {
        command: command.into(),
        ..Hook::default()
    }
}

fn callback(verb: &str) -> Hook {
    Hook {
        command: "ctxloom".into(),
        args: vec!["hook".into(), verb.into()],
        timeout: 15,
        ..Hook::default()
    }
}

fn one(unified: &str, hook: Hook) -> JsonOp {
    let mut hooks = BTreeMap::new();
    hooks.insert(unified.to_string(), vec![hook]);
    ops(&hooks).unwrap().remove(0)
}

fn matcher_of_op(op: &JsonOp) -> Option<String> {
    match op {
        JsonOp::HookEntry { matcher, .. } => matcher.clone(),
        other => panic!("{other:?}"),
    }
}

// a callback is installed as `hook <engine> execute
// <native event>`, whatever verb its author wrote; the native event is the one
// the unified event routes to.
#[test]
fn test_a_ctxloom_callback_executes_the_native_hook_of_its_event() {
    for (unified, native) in [
        ("session_start", "SessionStart"),
        ("turn_end", "Stop"),
        ("post_tool", "PostToolUse"),
    ] {
        let JsonOp::HookEntry { event, value, .. } = one(unified, callback("any-verb")) else {
            panic!()
        };
        assert_eq!(event, native);
        assert_eq!(
            value.pretty(),
            Json::parse(&format!(
                r#"{{"type":"command","command":"ctxloom","args":["hook","claude-code","execute","{native}"],"timeout":15}}"#
            ))
            .unwrap()
            .pretty(),
            "{unified}"
        );
    }
}

// the two events the primary agent's data comes from have a hook file
// form, and the route and the codec's name mapping are reverses.
#[test]
fn test_every_unified_event_routes_to_the_native_one_the_codec_maps_back() {
    use surface_ports::HookCodec;
    for unified in [
        "pre_tool",
        "post_tool",
        "session_start",
        "session_end",
        "turn_end",
        "turn_start",
        "permission_ask",
        "post_tool_batch",
        "message_display",
        "compaction_before",
        "compaction_after",
    ] {
        let (native, _, _) = route(unified).unwrap_or_else(|| panic!("{unified} has no route"));
        assert_eq!(crate::Codec.unified_event(native), unified, "{native}");
    }
}

// has_callback keeps its first word and reads the engine and `execute`.
#[test]
fn test_a_callback_is_found_by_its_execute_form_only() {
    let settings = |args: &str| {
        Json::parse(&format!(
            r#"{{"hooks":{{"Stop":[{{"hooks":[{{"command":"ctxloom","args":{args}}}]}}]}}}}"#
        ))
        .unwrap()
    };
    assert!(has_callback(&settings(
        r#"["hook","claude-code","execute","Stop"]"#
    )));
    // The earlier forms are stale: the next surface apply rewrites them.
    assert!(!has_callback(&settings(r#"["hook","skill-mates"]"#)));
    assert!(!has_callback(&settings(
        r#"["hook","session-start","--engine","claude-code"]"#
    )));
    // Another command, or another verb of ours, is not a callback.
    assert!(!has_callback(&settings(
        r#"["hook","claude-code","other","Stop"]"#
    )));
    assert!(!has_callback(&settings(r#"["profile","list"]"#)));
}

#[test]
fn test_a_hook_of_the_users_is_not_given_an_engine_flag() {
    let JsonOp::HookEntry { value, .. } = one("session_start", hook("echo hi")) else {
        panic!()
    };
    assert_eq!(value.get("args"), None);
    assert_eq!(value.get("command").and_then(Json::as_str), Some("echo hi"));
}

#[test]
fn test_a_tool_class_becomes_claudes_matcher() {
    for (class, matcher) in [
        ("shell", "Bash|PowerShell"),
        ("file_edit", "Edit|Write|MultiEdit|NotebookEdit"),
        ("skill", "Skill"),
    ] {
        let mut h = hook("x");
        h.tool = class.into();
        assert_eq!(
            matcher_of_op(&one("post_tool", h)).as_deref(),
            Some(matcher)
        );
    }
}

#[test]
fn test_a_matcher_of_the_hooks_own_wins_over_its_class() {
    let mut h = hook("x");
    h.tool = "shell".into();
    h.matcher = "Mine".into();
    assert_eq!(matcher_of_op(&one("pre_tool", h)).as_deref(), Some("Mine"));
}

#[test]
fn test_a_class_claude_has_no_tool_for_is_refused_not_widened() {
    let mut h = hook("x");
    h.tool = "teleport".into();
    let mut hooks = BTreeMap::new();
    hooks.insert("pre_tool".to_string(), vec![h]);
    assert!(matches!(ops(&hooks), Err(SurfaceError::Unsupported { .. })));
}

#[test]
fn test_events_narrowed_by_name_carry_their_matcher_and_the_tool_free_ones_none() {
    assert_eq!(
        matcher_of_op(&one("pre_shell", hook("x"))).as_deref(),
        Some("Bash|PowerShell")
    );
    assert_eq!(
        matcher_of_op(&one("post_file_edit", hook("x"))).as_deref(),
        Some("Edit|Write|MultiEdit|NotebookEdit")
    );
    let mut h = hook("x");
    h.matcher = "Ignored".into();
    assert_eq!(matcher_of_op(&one("turn_end", h)), None);
    let JsonOp::HookEntry { event, .. } = one("turn_start", hook("x")) else {
        panic!()
    };
    assert_eq!(event, "UserPromptSubmit");
}

#[test]
fn test_every_unified_event_has_a_native_one_and_an_unknown_has_none() {
    for (unified, native) in [
        ("pre_tool", "PreToolUse"),
        ("post_tool", "PostToolUse"),
        ("session_end", "SessionEnd"),
        ("turn_end", "Stop"),
        ("permission_ask", "PermissionRequest"),
    ] {
        let JsonOp::HookEntry { event, .. } = one(unified, hook("x")) else {
            panic!()
        };
        assert_eq!(event, native);
    }
    let mut hooks = BTreeMap::new();
    hooks.insert("nonesuch".to_string(), vec![hook("x")]);
    assert!(ops(&hooks).unwrap().is_empty());
}

#[test]
fn test_a_hook_routed_twice_is_claimed_once() {
    let mut hooks = BTreeMap::new();
    hooks.insert("pre_tool".to_string(), vec![hook("x"), hook("x")]);
    assert_eq!(ops(&hooks).unwrap().len(), 1);
}

#[test]
fn test_the_optional_fields_are_written_only_when_set() {
    let h = Hook {
        kind: "prompt".into(),
        prompt: "check".into(),
        timeout: 5,
        is_async: true,
        ..Hook::default()
    };
    let JsonOp::HookEntry { value, .. } = one("pre_tool", h) else {
        panic!()
    };
    assert_eq!(
        value.pretty(),
        Json::parse(r#"{"type":"prompt","prompt":"check","timeout":5,"async":true}"#)
            .unwrap()
            .pretty()
    );
}

#[test]
fn test_a_settings_document_with_a_ctxloom_callback_is_wired() {
    let wired = Json::parse(
        r#"{"hooks":{"Stop":[{"hooks":[{"command":"ctxloom","args":["hook","claude-code","execute","Stop"]}]}]}}"#,
    )
    .unwrap();
    assert!(has_callback(&wired));
    for not in [
        r#"{}"#,
        r#"{"hooks":{"Stop":[{"hooks":[{"command":"ctxloom","args":["profile","list"]}]}]}}"#,
        r#"{"hooks":{"Stop":[{"hooks":[{"command":"mine","args":["hook"]}]}]}}"#,
        r#"{"hooks":{"Stop":[{"hooks":[{"command":"ctxloom"}]}]}}"#,
    ] {
        assert!(!has_callback(&Json::parse(not).unwrap()), "{not}");
    }
}

#[test]
fn test_a_timeout_is_written_only_when_it_is_positive() {
    let with = |timeout| {
        value(&Hook {
            timeout,
            ..hook("x")
        })
    };
    assert_eq!(with(1).get("timeout"), Some(&Json::Number("1".into())));
    assert_eq!(with(0).get("timeout"), None);
    assert_eq!(with(-1).get("timeout"), None);
}
