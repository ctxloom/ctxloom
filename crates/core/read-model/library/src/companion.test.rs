use super::*;

#[test]
fn test_a_shipped_companion_is_its_own_program_and_any_other_is_prefixed() {
    assert_eq!(program("ltk"), "ltk");
    assert_eq!(program("taskloom"), "taskloom");
    assert_eq!(program("reprise"), "reprise");
    assert_eq!(program("acme"), "ctxloom-companion-acme");
    assert_eq!(program("ltk2"), "ctxloom-companion-ltk2");
}

#[test]
fn test_a_state_is_its_word_and_anything_but_disabled_is_enabled() {
    assert_eq!(State::Enabled.word(), "enabled");
    assert_eq!(State::Disabled.word(), "disabled");
    assert_eq!(State::of("disabled"), State::Disabled);
    assert_eq!(State::of("enabled"), State::Enabled);
    assert_eq!(State::of(""), State::Enabled);
    assert_eq!(State::default(), State::Enabled);
    assert_eq!(STATE_WORDS, ["enabled", "disabled"]);
}
