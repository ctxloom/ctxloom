//! The texts: each said in English from the catalog, never as its code.

use coded_diagnostic::Diagnostic as _;

use super::{texts, Text};

#[test]
fn every_text_is_said_from_the_catalog_and_never_as_its_code() {
    for text in texts() {
        let said = text.said();
        assert!(
            !said.contains(text.code()),
            "{} said as {said:?}",
            text.code()
        );
        assert!(
            !said.contains('{') && !said.contains('$'),
            "{}: {said:?}",
            text.code()
        );
    }
}

#[test]
fn the_listing_and_the_trust_lines_are_said_as_before() {
    let t = |v: &str| v.to_string();
    assert_eq!(
        Text::AnswerWith {
            ask: t("ask-tidy-wren")
        }
        .said(),
        r#"answer: ctxloom session approvals answer ask-tidy-wren <option> ["<message>"]"#
    );
    assert_eq!(
        Text::LabelAllowAlways {
            rule: t("Bash(rm -rf build/)"),
            agent: t("default"),
            engine: t("claude-code"),
        }
        .said(),
        "Always allow Bash(rm -rf build/): writes it into agents.default.engines.claude-code.permissions (previewed first)"
    );
    assert_eq!(
        Text::AskUnderUntil {
            ask: t("ask-tidy-wren"),
            root: t("amber-quiet-heron"),
            seconds: 1_800_000_000,
        }
        .said(),
        "ask ask-tidy-wren, under amber-quiet-heron, denied at 2027-01-15 08:00:00 unless answered"
    );
    assert_eq!(
        Text::TrustedBySince {
            name: t("projects/x/trust"),
            actor: t("ben"),
            seconds: 5,
        }
        .said(),
        "projects/x/trust  by ben since 1970-01-01 00:00:05"
    );
    assert_eq!(
        Text::TrustNothing.said(),
        "This project's own config says nothing that reaches past it."
    );
}
