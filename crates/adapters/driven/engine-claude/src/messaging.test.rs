//! claude's messaging socket as the terminal wake posts to it (owner ruling
//! 2026-11-18; the wire measured live by `just validate-wake-claude`).

use run_ports::WakePost;

use crate::messaging::{messaging_wake, wire, SOCKET_VARIABLE, TOKEN_VARIABLE};

const LINE: &str = "ctxloom: wake 0123456789abcdef (messages delivered at turn start)";

fn exported<'a>(pairs: &'a [(&'a str, &'a str)]) -> impl Fn(&str) -> Option<String> + 'a {
    move |name| {
        pairs
            .iter()
            .find(|(n, _)| *n == name)
            .map(|(_, v)| v.to_string())
    }
}

// Go's wire, as the probe measured it on claude 2.1.325: the auth line when
// claude exported a token, then the one user line, each on its own line.
#[test]
fn the_post_is_the_auth_line_then_the_user_line() {
    assert_eq!(
        wire("tok-1", LINE),
        format!(
            "{{\"type\":\"auth\",\"token\":\"tok-1\"}}\n{{\"type\":\"user\",\"message\":{{\"role\":\"user\",\"content\":\"{LINE}\"}}}}\n"
        )
    );
    assert_eq!(
        wire("", LINE),
        format!("{{\"type\":\"user\",\"message\":{{\"role\":\"user\",\"content\":\"{LINE}\"}}}}\n"),
        "no token, no auth line"
    );
}

#[test]
fn a_wake_is_bound_only_where_claude_exported_its_socket() {
    assert!(messaging_wake(&exported(&[])).is_none());
    assert!(messaging_wake(&exported(&[(SOCKET_VARIABLE, "")])).is_none());
    assert!(messaging_wake(&exported(&[(SOCKET_VARIABLE, "/tmp/cc-socks/1.sock")])).is_some());
}

// The token never renders: the wake's Debug names its endpoint alone.
#[test]
fn a_bound_wake_never_shows_its_token() {
    let wake = messaging_wake(&exported(&[
        (SOCKET_VARIABLE, "/tmp/cc-socks/1.sock"),
        (TOKEN_VARIABLE, "secret-token-value"),
    ]))
    .unwrap();
    let shown = format!("{wake:?}");
    assert!(shown.contains("/tmp/cc-socks/1.sock"), "{shown}");
    assert!(!shown.contains("secret-token-value"), "{shown}");
}

// A socket nobody listens on is a failure said, never a panic or a token.
#[test]
fn a_post_to_no_listener_fails_saying_where() {
    let wake = messaging_wake(&exported(&[
        (SOCKET_VARIABLE, "/nonexistent/ctxloom-wake-test.sock"),
        (TOKEN_VARIABLE, "secret-token-value"),
    ]))
    .unwrap();
    let failed = wake.post(LINE).unwrap_err();
    assert!(
        failed.contains("/nonexistent/ctxloom-wake-test.sock"),
        "{failed}"
    );
    assert!(!failed.contains("secret-token-value"), "{failed}");
}
