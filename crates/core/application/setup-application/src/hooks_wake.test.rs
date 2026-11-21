//! A primary's hooks deliver its pending messages (the terminal wake, owner
//! ruling 2026-11-18), in ONE daemon call (the lean hook RPC): the prompt's
//! informs and wakes; a tool call's delivers none (a primary takes no
//! steer); a stale wake line is blocked, a human's prompt never.

use std::sync::{Arc, Mutex};

use crate::fakes::{world, Asked, Deliveries, Inbox, CONFIG};
use crate::HookMessage;

const HARP: &str = "amber-quiet-heron";
const NONCE: &str = "0123456789abcdef";

fn message(id: &str, urgency: &str, text: &str) -> HookMessage {
    HookMessage {
        id: id.to_string(),
        urgency: urgency.to_string(),
        text: text.to_string(),
        sender: String::new(),
    }
}

/// The setup, its hooks reaching `pending` (`None`: no daemon answered),
/// each delivery recorded unless `refusing`.
fn hooked(pending: Option<Vec<HookMessage>>, refusing: bool) -> (crate::Setup, Deliveries) {
    let (setup, delivered, _) = asking(pending, refusing);
    (setup, delivered)
}

/// [`hooked`], with each call a hook made.
fn asking(pending: Option<Vec<HookMessage>>, refusing: bool) -> (crate::Setup, Deliveries, Asked) {
    let inbox = Inbox {
        pending: Mutex::new(pending),
        refusing,
        ..Inbox::default()
    };
    let delivered = Arc::clone(&inbox.delivered);
    let asked = Arc::clone(&inbox.asked);
    (
        world(CONFIG).setup.with_hook_contexts(Box::new(inbox)),
        delivered,
        asked,
    )
}

fn answer(setup: &crate::Setup, text: &str) -> crate::HookAnswer {
    let payload = serde_json::json!({ "prompt": text }).to_string();
    setup
        .execute_hook("claude-code", "UserPromptSubmit", &payload, HARP)
        .unwrap()
}

fn prompt(setup: &crate::Setup, text: &str) -> String {
    answer(setup, text).stdout
}

fn mixed() -> Vec<HookMessage> {
    vec![
        message("m1", "inform", "the tests pass"),
        message("m2", "steer", "stop and rebase"),
        message("m3", "wake", "child kid finished its turn"),
    ]
}

// A human's prompt: its informs and wakes are OFFERED, each in an element
// with its id, and NOTHING is recorded until the hook acknowledges them
// (after it printed): then they are, with no wake. Never blocked.
#[test]
fn a_prompt_offers_the_informs_and_wakes_and_records_them_only_when_acknowledged() {
    let (setup, delivered) = hooked(Some(mixed()), false);
    let answered = answer(&setup, "go on");
    let out = &answered.stdout;
    assert!(
        out.contains(
            r#"<message id="m1" from="the user" urgency="inform">the tests pass</message>"#
        ),
        "{out}"
    );
    assert!(
        out.contains(
            r#"<message id="m3" from="the user" urgency="wake">child kid finished its turn</message>"#
        ),
        "{out}"
    );
    assert!(
        !out.contains("stop and rebase"),
        "a steer is a tool call's: {out}"
    );
    assert!(!out.contains(";block="), "{out}");
    assert!(
        delivered.lock().unwrap().is_empty(),
        "an answer marks nothing delivered"
    );
    assert_eq!(
        answered.offered,
        crate::Offered {
            ids: vec!["m1".to_string(), "m3".to_string()],
            wake: String::new(),
        }
    );
    assert!(setup.acknowledge(&answered.offered));
    assert_eq!(
        *delivered.lock().unwrap(),
        [(vec!["m1".to_string(), "m3".to_string()], String::new())]
    );
}

// The wake line: what it woke the session for offered, its nonce carried
// to the ack that redeems it.
#[test]
fn a_wake_line_offers_and_its_ack_redeems_its_wake() {
    let (setup, delivered) = hooked(Some(mixed()), false);
    let answered = answer(&setup, &run_ports::wake_line(NONCE));
    assert!(answered.stdout.contains("child kid finished its turn"));
    assert!(!answered.stdout.contains(";block="), "{}", answered.stdout);
    assert!(delivered.lock().unwrap().is_empty());
    setup.acknowledge(&answered.offered);
    assert_eq!(
        *delivered.lock().unwrap(),
        [(vec!["m1".to_string(), "m3".to_string()], NONCE.to_string())]
    );
}

// A stale wake line (nothing waits for it): blocked, so no model call is
// spent on it; its nonce is still acknowledged (redeemed with no message).
#[test]
fn a_stale_wake_line_is_blocked() {
    let (setup, delivered) = hooked(Some(vec![message("m2", "steer", "x")]), false);
    let answered = answer(&setup, &run_ports::wake_line(NONCE));
    assert!(answered.stdout.contains(";block="), "{}", answered.stdout);
    assert!(answered.stdout.starts_with("context=;"));
    setup.acknowledge(&answered.offered);
    assert_eq!(
        *delivered.lock().unwrap(),
        [(Vec::new(), NONCE.to_string())]
    );
}

// A human's prompt is never blocked: with nothing waiting it is answered
// silence, and nothing is offered or acknowledged.
#[test]
fn a_human_prompt_with_nothing_waiting_is_never_blocked() {
    let (setup, delivered) = hooked(Some(Vec::new()), false);
    let quoting = format!("why did I see {}?", run_ports::wake_line(NONCE));
    for text in ["go on", quoting.as_str()] {
        let answered = answer(&setup, text);
        assert_eq!(answered.stdout, "context=;notice=\n", "{text}");
        assert!(answered.offered.is_empty(), "{text}");
        setup.acknowledge(&answered.offered);
    }
    assert!(delivered.lock().unwrap().is_empty());
}

// An ack the daemon did not record is reported: the messages stay pending
// and are offered again (a duplicate is tolerated, a loss is not).
#[test]
fn an_ack_not_recorded_is_reported_and_the_messages_are_offered_again() {
    let (setup, delivered) = hooked(Some(mixed()), true);
    let first = answer(&setup, "go on");
    assert!(!setup.acknowledge(&first.offered));
    assert!(delivered.lock().unwrap().is_empty());
    let again = answer(&setup, "go on");
    assert_eq!(again.offered.ids, ["m1", "m3"]);
    assert_eq!(again.stdout, first.stdout);
}

// Nothing to acknowledge asks nothing of the daemon.
#[test]
fn an_empty_offer_is_acknowledged_without_asking_the_daemon() {
    // A daemon that refuses every ack: an empty offer never reaches it.
    let (setup, delivered) = hooked(Some(Vec::new()), true);
    assert!(setup.acknowledge(&crate::Offered::default()));
    assert!(delivered.lock().unwrap().is_empty());
    let offer = crate::Offered {
        ids: vec!["m1".to_string()],
        wake: String::new(),
    };
    assert!(!setup.acknowledge(&offer), "a refused ack is reported");
}

// No daemon answered: a human's prompt goes on as ever; a wake line, which
// carries nothing for the model, is blocked.
#[test]
fn with_no_daemon_a_prompt_goes_on_and_a_wake_line_is_blocked() {
    let (setup, delivered) = hooked(None, false);
    assert_eq!(prompt(&setup, "go on"), "context=;notice=\n");
    assert!(prompt(&setup, &run_ports::wake_line(NONCE)).contains(";block="));
    assert!(delivered.lock().unwrap().is_empty());
}

// A tool call delivers no message: it would deliver a steer alone, and a
// primary is offered none (owner ruling 2026-11-18; a child's are fed as
// turns). Its one call asks for no urgency (it carries the owed context
// alone); the skill mates line is answered as ever. A prompt asks for its
// informs and wakes, in one call too.
#[test]
fn a_tool_call_asks_for_no_messages_and_delivers_none() {
    let (setup, delivered, asked) = asking(Some(mixed()), false);
    let out = setup
        .execute_hook("claude-code", "PostToolUse", r#"{"skill":"admit"}"#, HARP)
        .unwrap()
        .stdout;
    assert!(
        out.contains("Skill admit completed"),
        "the mates line: {out}"
    );
    assert!(!out.contains("stop and rebase"), "{out}");
    assert!(!out.contains("the tests pass"), "{out}");
    assert!(delivered.lock().unwrap().is_empty());
    prompt(&setup, "go on");
    let asked = asked.lock().unwrap();
    let calls: Vec<(&str, Vec<String>)> = asked
        .iter()
        .map(|(hook, urgencies, _)| (hook.as_str(), urgencies.clone()))
        .collect();
    assert_eq!(
        calls,
        [
            ("PostToolUse", Vec::new()),
            (
                "UserPromptSubmit",
                vec!["inform".to_string(), "wake".to_string()]
            ),
        ],
        "one call per hook"
    );
}
