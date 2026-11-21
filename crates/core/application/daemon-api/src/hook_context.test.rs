//! The lean hook RPC (10c, owner budget 2026-11-18): one call, answered from
//! the projections with no round awaited. Message delivery (owner
//! 2026-11-19, "we can tolerate duplicates, not losses"): the call is a READ
//! that marks nothing; the hook's ack, after it printed, records the ids.

use angzarr_embedded::v1::Cover;
use daemon_proto::v1;
use run_ports::Edge;

use crate::server::tests::{fixture, Fixture};

const HARP: &str = "amber-quiet-heron";

fn name() -> String {
    format!("projects/calm-quiet-heron/sessions/{HARP}")
}

/// `text` sent to the session as the inform `id`.
async fn send(f: &Fixture, id: &str, text: &str) {
    let root = sessions::SessionName::parse(&name()).unwrap().root();
    let cover = Cover {
        domain: sessions::SESSION_DOMAIN.to_string(),
        root: Some(root),
        edition: None,
    };
    let send = sessions::SendMessage {
        message_id: id.to_string(),
        urgency: sessions::Urgency::Inform,
        text: text.to_string(),
        requester: None,
        supported: vec![sessions::Urgency::Inform, sessions::Urgency::Wake],
    };
    let book = sessions::keyed_book(cover, &send, &format!("send:{id}"));
    f.world.handle(book).await.unwrap();
}

/// A primary `HARP` holding the inform `m1`, every projection caught up.
async fn holding_one() -> Fixture {
    let f = fixture(vec![]);
    f.mint(HARP).await;
    send(&f, "m1", "the first words").await;
    f.catch_up().await;
    f.catch_up_pending().await;
    f
}

/// A prompt's hook: its informs and wakes, the prompt the wake `nonce`
/// (empty: a human's).
fn at_a_prompt(nonce: &str) -> v1::DeliverHookContextRequest {
    v1::DeliverHookContextRequest {
        name: name(),
        carried_by: "UserPromptSubmit".to_string(),
        urgencies: vec!["inform".to_string(), "wake".to_string()],
        wake_nonce: nonce.to_string(),
        ..v1::DeliverHookContextRequest::default()
    }
}

/// The ack of `ids` (and the wake `nonce`).
fn ack(ids: &[&str], nonce: &str) -> v1::AcknowledgeHookDeliveryRequest {
    v1::AcknowledgeHookDeliveryRequest {
        name: name(),
        message_ids: ids.iter().map(ToString::to_string).collect(),
        wake_nonce: nonce.to_string(),
        ..v1::AcknowledgeHookDeliveryRequest::default()
    }
}

// (a) A hook killed between the read and the ack: the read marked nothing,
// so the message is offered again; once acknowledged it is not.
#[tokio::test]
async fn test_a_hook_killed_before_its_ack_leaves_the_message_offered_again() {
    let f = holding_one().await;
    let first = f.api.deliver_hook_context(at_a_prompt("")).await.unwrap();
    assert_eq!(first.offered, ["m1"]);
    assert!(first.delivered.is_empty(), "a read delivers nothing");
    assert!(first.context.contains("the first words"), "{first:?}");
    // The hook died here: no ack. Even with every projection caught up.
    f.catch_up().await;
    f.catch_up_pending().await;
    let again = f.api.deliver_hook_context(at_a_prompt("")).await.unwrap();
    assert_eq!(again.offered, ["m1"], "still pending, offered again");
    assert_eq!(again.context, first.context);
    // The hook that lived: printed, then acknowledged.
    let acked = f
        .api
        .acknowledge_hook_delivery(ack(&["m1"], ""))
        .await
        .unwrap();
    assert_eq!(acked.delivered, ["m1"]);
    f.catch_up().await;
    f.catch_up_pending().await;
    let after = f.api.deliver_hook_context(at_a_prompt("")).await.unwrap();
    assert!(
        after.offered.is_empty() && after.context.is_empty(),
        "{after:?}"
    );
}

// (b) Two hooks within one round (no catch-up between, run at once): BOTH
// carry the message (a duplicate, tolerated: its id is visible), neither
// loses it; both acks are harmless, the aggregate records the id once.
#[tokio::test]
async fn test_two_hooks_within_one_round_may_both_carry_a_message_and_neither_loses_it() {
    let f = holding_one().await;
    let (first, second) = tokio::join!(
        f.api.deliver_hook_context(at_a_prompt("")),
        f.api.deliver_hook_context(at_a_prompt("")),
    );
    for read in [first.unwrap(), second.unwrap()] {
        assert_eq!(read.offered, ["m1"], "{read:?}");
        assert!(read.context.contains("the first words"), "{read:?}");
    }
    let (one, other) = tokio::join!(
        f.api.acknowledge_hook_delivery(ack(&["m1"], "")),
        f.api.acknowledge_hook_delivery(ack(&["m1"], "")),
    );
    let mut recorded = one.unwrap().delivered;
    recorded.extend(other.unwrap().delivered);
    assert_eq!(recorded, ["m1"], "recorded once, however many acked");
    f.catch_up().await;
    f.catch_up_pending().await;
    let after = f.api.deliver_hook_context(at_a_prompt("")).await.unwrap();
    assert!(after.offered.is_empty(), "{after:?}");
    assert_eq!(*f.hosting.freshened.lock().unwrap(), 0, "no round awaited");
}

// (c) A message the budget leaves out stays pending, its id unoffered and
// unacked: offered at the next prompt.
#[tokio::test]
async fn test_a_message_cut_by_the_budget_stays_pending_and_is_offered_next() {
    let f = fixture(vec![]);
    f.mint(HARP).await;
    send(&f, "m1", &"a".repeat(3000)).await;
    send(&f, "m2", &"b".repeat(3000)).await;
    f.catch_up().await;
    f.catch_up_pending().await;
    let tight = v1::DeliverHookContextRequest {
        budget: 4000,
        ..at_a_prompt("")
    };
    let first = f.api.deliver_hook_context(tight.clone()).await.unwrap();
    assert_eq!(first.offered, ["m1"]);
    assert!(
        first.context.contains("1 more waiting"),
        "{}",
        first.context
    );
    f.api
        .acknowledge_hook_delivery(ack(&["m1"], ""))
        .await
        .unwrap();
    f.catch_up().await;
    f.catch_up_pending().await;
    let next = f.api.deliver_hook_context(tight).await.unwrap();
    assert_eq!(next.offered, ["m2"], "the cut message was never marked");
}

// (d) The ack of ids already acked is a harmless no-op: a repeat of the
// whole ack, a superset (only the new id records), an unknown id.
#[tokio::test]
async fn test_the_ack_of_ids_already_acked_is_a_harmless_no_op() {
    let f = holding_one().await;
    send(&f, "m2", "second").await;
    f.catch_up().await;
    f.catch_up_pending().await;
    let first = f.api.acknowledge_hook_delivery(ack(&["m1"], "")).await;
    assert_eq!(first.unwrap().delivered, ["m1"]);
    let repeat = f.api.acknowledge_hook_delivery(ack(&["m1"], "")).await;
    assert!(repeat.unwrap().delivered.is_empty());
    let superset = f
        .api
        .acknowledge_hook_delivery(ack(&["m1", "m2"], ""))
        .await;
    assert_eq!(superset.unwrap().delivered, ["m2"], "only the new id");
    let unknown = f
        .api
        .acknowledge_hook_delivery(ack(&["nobody", "m2"], ""))
        .await;
    assert!(unknown.unwrap().delivered.is_empty());
}

// An ack for a session nobody knows is refused, as the read is.
#[tokio::test]
async fn test_an_ack_for_a_session_the_index_does_not_hold_is_refused() {
    let f = fixture(vec![]);
    f.mint(HARP).await;
    let refused = f.api.acknowledge_hook_delivery(ack(&["m1"], "")).await;
    assert!(
        matches!(refused, Err(crate::ApiError::Refused(_))),
        "{refused:?}"
    );
    let bad = v1::AcknowledgeHookDeliveryRequest {
        name: "nonsense".to_string(),
        ..ack(&[], "")
    };
    assert!(f.api.acknowledge_hook_delivery(bad).await.is_err());
}

// A tool call's hook delivers no message (a primary is offered no steer),
// and leaves it for the next prompt's.
#[tokio::test]
async fn test_a_tool_calls_hook_offers_no_message_and_leaves_it_pending() {
    let f = holding_one().await;
    let tool = f
        .api
        .deliver_hook_context(v1::DeliverHookContextRequest {
            name: name(),
            carried_by: "PostToolUse".to_string(),
            ..v1::DeliverHookContextRequest::default()
        })
        .await
        .unwrap();
    assert!(
        tool.context.is_empty() && tool.offered.is_empty(),
        "{tool:?}"
    );
    let prompt = f.api.deliver_hook_context(at_a_prompt("")).await.unwrap();
    assert_eq!(prompt.offered, ["m1"]);
}

// The wake: a prompt that IS a wake line is offered what waits, and its
// ack redeems the nonce (with no message too: a stale wake).
#[tokio::test]
async fn test_a_wake_prompts_ack_redeems_its_wake() {
    let f = holding_one().await;
    let nonce = "0123456789abcdef";
    let issue = sessions::IssueWake {
        nonce: nonce.to_string(),
    };
    let root = sessions::SessionName::parse(&name()).unwrap().root();
    let cover = Cover {
        domain: sessions::SESSION_DOMAIN.to_string(),
        root: Some(root),
        edition: None,
    };
    f.world
        .handle(sessions::keyed_book(cover, &issue, "wake:test"))
        .await
        .unwrap();
    let read = f
        .api
        .deliver_hook_context(at_a_prompt(nonce))
        .await
        .unwrap();
    assert_eq!(read.offered, ["m1"]);
    let acked = f
        .api
        .acknowledge_hook_delivery(ack(&["m1"], nonce))
        .await
        .unwrap();
    assert_eq!(acked.delivered, ["m1"]);
    // The wake was redeemed: a second wake may now be issued.
    let again = sessions::IssueWake {
        nonce: "fedcba9876543210".to_string(),
    };
    let cover = Cover {
        domain: sessions::SESSION_DOMAIN.to_string(),
        root: Some(sessions::SessionName::parse(&name()).unwrap().root()),
        edition: None,
    };
    f.world
        .handle(sessions::keyed_book(cover, &again, "wake:again"))
        .await
        .expect("the first wake was redeemed by the ack");
}

// No round is awaited: the index as it stands answers, so a session the
// projection does not hold yet is refused, never waited for.
#[tokio::test]
async fn test_a_session_the_index_does_not_hold_is_refused_without_a_round() {
    let f = fixture(vec![]);
    f.mint(HARP).await;
    let refused = f.api.deliver_hook_context(at_a_prompt("")).await;
    assert!(
        matches!(refused, Err(crate::ApiError::Refused(_))),
        "{refused:?}"
    );
    assert_eq!(*f.hosting.freshened.lock().unwrap(), 0, "no round awaited");
}

// Who a message is from (coordinator ruling 2026-11-19): a notice the
// daemon minted reads `from="ctxloom"`, never the user; the user's own send
// reads `the user`, and nothing else is ever labelled the user.
#[tokio::test]
async fn test_a_daemon_notice_is_offered_from_ctxloom_and_only_a_users_send_from_the_user() {
    let f = fixture(vec![]);
    f.mint(HARP).await;
    send(&f, "notice:kid:1", "child kid ended its run 1: done").await;
    send(&f, "drain:kid:1", "ctxloom is being upgraded").await;
    send(&f, "m1", "from the user").await;
    f.catch_up().await;
    f.catch_up_pending().await;
    let read = f.api.deliver_hook_context(at_a_prompt("")).await.unwrap();
    let c = &read.context;
    assert!(
        c.contains(r#"<message id="notice:kid:1" from="ctxloom" urgency="inform">"#),
        "{c}"
    );
    assert!(
        c.contains(r#"<message id="drain:kid:1" from="ctxloom" urgency="inform">"#),
        "{c}"
    );
    assert!(
        c.contains(r#"<message id="m1" from="the user" urgency="inform">"#),
        "{c}"
    );
    assert_eq!(c.matches(r#"from="the user""#).count(), 1, "{c}");
}

// A message with a real requester keeps it, whatever its id says.
#[test]
fn a_real_requester_is_kept_and_only_no_requester_is_the_user_or_the_daemon() {
    use super::hook_sender;
    let named = Some("projects/p/sessions/kid".to_string());
    assert_eq!(hook_sender(named.clone(), "m1"), "projects/p/sessions/kid");
    assert_eq!(
        hook_sender(named, "notice:x:1"),
        "projects/p/sessions/kid",
        "an id cannot make a session the daemon"
    );
    assert_eq!(
        hook_sender(None, "notice:x:1"),
        setup_application::DAEMON_SENDER
    );
    assert_eq!(
        hook_sender(None, "drain:x:1"),
        setup_application::DAEMON_SENDER
    );
    assert_eq!(hook_sender(None, "m1"), "", "the user");
}
