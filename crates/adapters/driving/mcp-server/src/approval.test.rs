use serde_json::json;

use super::*;

fn said() -> Say {
    Box::new(|d| {
        format!(
            "{} {:?}",
            d.code(),
            d.args().iter().map(|(k, _)| *k).collect::<Vec<_>>()
        )
    })
}

fn approval(state: &str, kind: &str, option: &str) -> v1::Approval {
    v1::Approval {
        engine: "claude-code".to_string(),
        state: state.to_string(),
        option: option.to_string(),
        option_kind: kind.to_string(),
        timeout_seconds: 300,
        options: vec![
            v1::ApprovalOption {
                id: "allow-session".to_string(),
                rule: "Bash(rm -rf build/)".to_string(),
                ..v1::ApprovalOption::default()
            },
            v1::ApprovalOption {
                id: "allow-always".to_string(),
                rule: "Bash(rm -rf build/)".to_string(),
                ..v1::ApprovalOption::default()
            },
            v1::ApprovalOption {
                id: "odd".to_string(),
                rule: "no rule".to_string(),
                ..v1::ApprovalOption::default()
            },
        ],
        ..v1::Approval::default()
    }
}

#[test]
fn an_allow_once_runs_the_input_unchanged() {
    let input = json!({"command": "rm -rf build/"});
    assert_eq!(
        answer_of(
            &approval("answered", "allow_once", "allow-once"),
            &input,
            &said()
        ),
        json!({"behavior": "allow", "updatedInput": input})
    );
}

#[test]
fn a_session_or_always_allow_carries_the_session_rule() {
    let input = json!({"command": "rm -rf build/"});
    let rule = json!([{
        "type": "addRules",
        "rules": [{"toolName": "Bash", "ruleContent": "rm -rf build/"}],
        "behavior": "allow",
        "destination": "session",
    }]);
    for (kind, option) in [
        ("allow_session", "allow-session"),
        ("allow_always", "allow-always"),
    ] {
        assert_eq!(
            answer_of(&approval("answered", kind, option), &input, &said()),
            json!({"behavior": "allow", "updatedInput": input, "updatedPermissions": rule}),
            "{kind}"
        );
    }
    // An option whose rule has no shape carries none.
    assert_eq!(
        answer_of(
            &approval("answered", "allow_session", "odd"),
            &input,
            &said()
        ),
        json!({"behavior": "allow", "updatedInput": input})
    );
}

#[test]
fn a_deny_says_the_users_message_else_ctxlooms_words() {
    let input = json!({});
    let mut with = approval("answered", "reject_once", "deny");
    with.message = "not yet".to_string();
    assert_eq!(
        answer_of(&with, &input, &said()),
        json!({"behavior": "deny", "message": "not yet"})
    );
    assert_eq!(
        answer_of(
            &approval("answered", "reject_once", "deny"),
            &input,
            &said()
        ),
        json!({"behavior": "deny", "message": "mcp-server-approval-denied []"})
    );
    assert_eq!(
        answer_of(&approval("withdrawn", "", ""), &input, &said()),
        json!({"behavior": "deny", "message": "mcp-server-approval-denied []"})
    );
}

#[test]
fn an_expired_ask_is_denied_for_its_time_or_at_once() {
    let say: Say = Box::new(|d| format!("{d}"));
    let input = json!({});
    assert_eq!(
        answer_of(&approval("expired", "", ""), &input, &say),
        json!({"behavior": "deny", "message": "mcp-server-approval-expired timeout=5m"})
    );
    let mut now = approval("expired", "", "");
    now.timeout_seconds = 0;
    assert_eq!(
        answer_of(&now, &input, &say),
        json!({"behavior": "deny", "message": "mcp-server-approval-denied-at-once engine=claude-code"})
    );
}

#[test]
fn an_expired_asks_wait_is_named_as_the_one_grammar_writes_a_span() {
    let say: Say = Box::new(|d| format!("{d}"));
    let mut asked = approval("expired", "", "");
    for (seconds, text) in [
        (300, "5m"),
        (90, "1m 30s"),
        (3600, "1h"),
        (3660, "1h 1m"),
        (59, "59s"),
    ] {
        asked.timeout_seconds = seconds;
        assert_eq!(
            answer_of(&asked, &json!({}), &say),
            json!({"behavior": "deny", "message": format!("mcp-server-approval-expired timeout={}", coded_diagnostic::quote(text))}),
            "{seconds}"
        );
    }
}

#[test]
fn a_session_rule_is_its_tool_and_content() {
    assert_eq!(
        session_rule("Edit(src/main.rs)"),
        Some(json!({
            "type": "addRules",
            "rules": [{"toolName": "Edit", "ruleContent": "src/main.rs"}],
            "behavior": "allow",
            "destination": "session",
        }))
    );
    assert_eq!(session_rule("Bash"), None);
    assert_eq!(session_rule("Bash(x"), None);
}

#[test]
fn the_transport_answers_discovery_hears_a_cancel_and_passes_the_rest() {
    assert_eq!(
        intercepted(
            r#"{"jsonrpc":"2.0","id":"server-discover-probe-1","method":"server/discover","params":{}}"#
        ),
        Intercepted::Answer(
            r#"{"jsonrpc":"2.0","id":"server-discover-probe-1","result":{}}"#.to_string()
        )
    );
    assert_eq!(
        intercepted(r#"{"jsonrpc":"2.0","method":"server/discover"}"#),
        Intercepted::Pass
    );
    assert_eq!(
        intercepted(
            r#"{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":2,"reason":"AbortError"}}"#
        ),
        Intercepted::Cancelled {
            id: "2".to_string(),
            reason: "AbortError".to_string()
        }
    );
    assert_eq!(
        intercepted(
            r#"{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":"x"}}"#
        ),
        Intercepted::Cancelled {
            id: "\"x\"".to_string(),
            reason: String::new()
        }
    );
    assert_eq!(
        intercepted(r#"{"jsonrpc":"2.0","method":"notifications/cancelled","params":{}}"#),
        Intercepted::Pass
    );
    assert_eq!(
        intercepted(r#"{"jsonrpc":"2.0","id":1,"method":"initialize"}"#),
        Intercepted::Pass
    );
    assert_eq!(intercepted("not json"), Intercepted::Pass);
}

#[test]
fn a_cancels_reason_is_read_once() {
    let cancels = Cancels::default();
    cancels.heard("2".to_string(), "AbortError".to_string());
    assert_eq!(cancels.reason("3"), "");
    assert_eq!(cancels.reason("2"), "AbortError");
    assert_eq!(cancels.reason("2"), "");
}

#[test]
fn each_denial_reports_its_args() {
    let expired = Denied::Expired {
        timeout: "5m".to_string(),
    };
    assert_eq!(
        expired.to_string(),
        "mcp-server-approval-expired timeout=5m"
    );
    assert_eq!(Denied::ByUser.args().len(), 0);
}

// U5: the wait ends at the ask's recorded expiry and the margin, an
// instant, so a retry on a new daemon cannot wait a second span.
#[test]
fn the_wait_ends_at_the_recorded_expiry_and_the_margin() {
    let mut asked = approval("waiting", "", "");
    asked.timeout_seconds = 300;
    asked.expire_time = Some(daemon_api_client::Until {
        seconds: 1_300,
        nanos: 7,
    });
    assert_eq!(
        wait_until(&asked),
        daemon_api_client::Until {
            seconds: 1_360,
            nanos: 7
        }
    );
}
