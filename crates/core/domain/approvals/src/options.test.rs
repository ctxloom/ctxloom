use super::*;

fn ids(options: &[ApprovalOption]) -> Vec<&str> {
    options.iter().map(|o| o.id.as_str()).collect()
}

#[test]
fn a_bash_ask_offers_four_options_about_its_command() {
    let options = claude_options("Bash", r#"{"command":"rm -rf build/","description":"x"}"#);
    assert_eq!(
        ids(&options),
        ["allow-once", "allow-session", "allow-always", "deny"]
    );
    let rules: Vec<&str> = options.iter().map(|o| o.rule.as_str()).collect();
    assert_eq!(
        rules,
        ["", "Bash(rm -rf build/)", "Bash(rm -rf build/)", ""]
    );
    let kinds: Vec<OptionKind> = options.iter().map(|o| o.kind).collect();
    assert_eq!(
        kinds,
        [
            OptionKind::AllowOnce,
            OptionKind::AllowSession,
            OptionKind::AllowAlways,
            OptionKind::RejectOnce
        ]
    );
    assert!(options
        .iter()
        .all(|o| o.label.is_empty() && o.engine_option_id.is_empty()));
}

#[test]
fn a_path_tools_rule_is_its_path() {
    for (tool, input, rule) in [
        (
            "Edit",
            r#"{"file_path":"src/main.rs"}"#,
            "Edit(src/main.rs)",
        ),
        ("Write", r#"{"file_path":"NOTES.md"}"#, "Write(NOTES.md)"),
        ("Read", r#"{"file_path":"a"}"#, "Read(a)"),
        ("MultiEdit", r#"{"file_path":"b"}"#, "MultiEdit(b)"),
        (
            "NotebookEdit",
            r#"{"notebook_path":"n.ipynb"}"#,
            "NotebookEdit(n.ipynb)",
        ),
    ] {
        let options = claude_options(tool, input);
        assert_eq!(options.len(), 4, "{tool}");
        assert_eq!(options[1].rule, rule, "{tool}");
    }
}

#[test]
fn any_other_tool_or_an_input_without_its_key_offers_allow_once_and_deny_alone() {
    for (tool, input) in [
        ("mcp__github__create_issue", r#"{"title":"flaky test"}"#),
        ("Bash", r#"{"description":"no command"}"#),
        ("Bash", "not json"),
        ("Edit", r#"{"notebook_path":"n"}"#),
        ("Bash", r#"{"command":7}"#),
    ] {
        assert_eq!(
            ids(&claude_options(tool, input)),
            ["allow-once", "deny"],
            "{tool} {input}"
        );
    }
}

#[test]
fn a_kind_is_worded_as_the_api_says_it() {
    assert_eq!(kind_word(OptionKind::AllowOnce), "allow_once");
    assert_eq!(kind_word(OptionKind::AllowSession), "allow_session");
    assert_eq!(kind_word(OptionKind::AllowAlways), "allow_always");
    assert_eq!(kind_word(OptionKind::RejectOnce), "reject_once");
    assert_eq!(kind_word(OptionKind::RejectAlways), "reject_always");
}

#[test]
fn an_input_is_bounded_on_a_character_with_its_size_and_digest() {
    let small = bounded("abc");
    assert_eq!(small.excerpt, "abc");
    assert_eq!(small.bytes, 3);
    assert_eq!(
        small.sha256,
        "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
    );
    let big = "\u{e9}".repeat(EXCERPT_BYTES);
    let cut = bounded(&big);
    assert_eq!(cut.bytes, (EXCERPT_BYTES * 2) as u64);
    assert_eq!(cut.excerpt.len(), EXCERPT_BYTES);
    let odd = format!("a{}", "\u{e9}".repeat(EXCERPT_BYTES));
    assert_eq!(bounded(&odd).excerpt.len(), EXCERPT_BYTES - 1);
    let exact = "x".repeat(EXCERPT_BYTES + 1);
    assert_eq!(bounded(&exact).excerpt.len(), EXCERPT_BYTES);
}
