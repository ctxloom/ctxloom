use run_ports::{Engine, Planned};

use super::{diagnostic_samples, ClaudeNotice, EN_US};
use crate::Claude;

fn launching(os: &str, interactive: bool, credential_set: bool) -> Planned {
    Planned {
        os: os.to_string(),
        interactive,
        credential_set,
        ..Planned::default()
    }
}

// Owner ruling 2026-11-12: on Windows claude is token-only; the terminal
// session with no token is told so through the engine's notice seam.
#[test]
fn test_a_windows_terminal_session_with_no_token_is_told_it_is_handed_no_login() {
    let said = Claude::new().advise(&launching("windows", true, false));
    assert_eq!(said.len(), 1);
    assert_eq!(said[0].code(), "engine-claude-windows-token-only");
    assert!(said[0].args().is_empty());
}

#[test]
fn test_a_token_another_os_a_child_or_the_plan_is_told_nothing() {
    for planned in [
        launching("windows", true, true),
        launching("linux", true, false),
        launching("macos", true, false),
        launching("windows", false, false),
        Planned::default(),
    ] {
        assert!(Claude::new().advise(&planned).is_empty(), "{planned:?}");
    }
}

// The text is user-facing: it says why (a workaround for claude's own
// credential handling, one session's rotation signing the others out) and
// what to do (`claude setup-token`, then export the token).
#[test]
fn test_the_notice_says_why_and_what_to_do_in_the_catalog() {
    let (name, catalog) = EN_US;
    assert_eq!(name, "engine-claude");
    for sample in diagnostic_samples() {
        let code = sample.code();
        assert!(code.starts_with("engine-claude-"), "{code}");
        for id in [code.to_string(), format!("{code}-fix")] {
            assert!(
                catalog.lines().any(|l| l.starts_with(&format!("{id} ="))),
                "{id} has no message"
            );
        }
    }
    let message = catalog
        .lines()
        .find(|l| l.starts_with("engine-claude-windows-token-only ="))
        .unwrap();
    assert!(message.contains("workaround for claude's own credential handling"));
    assert!(message.contains("sign the others out"));
    let fix = catalog
        .lines()
        .find(|l| l.starts_with("engine-claude-windows-token-only-fix ="))
        .unwrap();
    assert!(fix.contains("claude setup-token"));
    assert!(fix.contains("export CLAUDE_CODE_OAUTH_TOKEN"));
    assert!(!catalog.contains("{ $"), "the notice has no args");
}

// A notice displays as its code and args, as every diagnostic does.
#[test]
fn test_a_notice_displays_as_its_code() {
    assert_eq!(
        ClaudeNotice::WindowsTokenOnly.to_string(),
        "engine-claude-windows-token-only"
    );
}
