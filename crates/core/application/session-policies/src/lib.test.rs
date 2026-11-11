//! The daemon's policies as pure functions (D28). NEW-DESIGN. The races (two
//! policies on one stream, a redelivery) are forced by sending what the
//! policies answer through the session aggregate in the race's order.

use angzarr_embedded::testing::{Harness, Samples};
use angzarr_embedded::{Handled, Payload, Registry};
use pending_messages::PendingMessage;
use runs::{QueueTurn, TurnUrgency};
use sessions::{
    Child, ChildLaunched, CloseSession, IncarnationEnded, MintSession, Origin, SessionAggregate,
    SessionClosed, SessionCommand, SessionEvent, SessionRef, Urgency,
};

use crate::{
    cascade, child_ended, deliver, destroy, launch, launch_failed, notice, run_cover,
    session_cover, RunGate,
};

const OWNER: &str = "0a0a0a0a-0a0a-0a0a-0a0a-0a0a0a0a0a0a";
const CHILD: &str = "0c0c0c0c-0c0c-0c0c-0c0c-0c0c0c0c0c0c";
const OTHER: &str = "0d0d0d0d-0d0d-0d0d-0d0d-0d0d0d0d0d0d";

fn at(session: &str) -> SessionRef {
    SessionRef {
        project_id: "calm-otter".to_string(),
        session: session.to_string(),
    }
}

fn message(id: &str, urgency: Urgency) -> PendingMessage {
    PendingMessage {
        message_id: id.to_string(),
        urgency,
        text: String::new(),
        sender: None,
    }
}

fn gate(live: bool, in_flight: bool) -> RunGate {
    RunGate {
        session: at(CHILD),
        incarnation: 2,
        live,
        in_flight,
    }
}

fn carried(turn: &QueueTurn) -> Vec<&str> {
    turn.message_ids.iter().map(String::as_str).collect()
}

fn children(of: &[&str]) -> Vec<Child> {
    of.iter()
        .map(|session| Child {
            session: Some(at(session)),
            root: format!("/w/{session}"),
        })
        .collect()
}

// -------------------------------------------------------------- delivery

// S-a: an idle run takes every pending message in one turn, keyed by the
// message that starts it.
#[test]
fn an_idle_run_is_given_every_pending_message_keyed_by_the_first_wake() {
    let pending = [
        message("i1", Urgency::Inform),
        message("w1", Urgency::Wake),
        message("w2", Urgency::Wake),
    ];
    let turn = deliver(&gate(true, false), &pending).unwrap();
    assert_eq!(turn.key, "send:w1");
    assert_eq!(turn.cover, run_cover(CHILD, 2));
    assert_eq!(turn.project_id, "calm-otter");
    assert_eq!(carried(&turn.command), ["i1", "w1", "w2"]);
    assert_eq!(turn.command.urgency, TurnUrgency::Wake);
    assert_eq!(turn.command_id(), sessions::command_id("send:w1"));
}

// D30: informs alone start no turn; they wait for the next one.
#[test]
fn informs_alone_start_no_turn() {
    let pending = [
        message("i1", Urgency::Inform),
        message("i2", Urgency::Inform),
    ];
    assert_eq!(deliver(&gate(true, false), &pending), None);
    assert_eq!(deliver(&gate(true, true), &pending), None);
}

// S-b: a turn in flight takes a steer (with the informs before it) and no
// wake; the steer is the turn's urgency.
#[test]
fn a_turn_in_flight_is_given_the_steers_and_the_informs_not_the_wakes() {
    let pending = [
        message("w1", Urgency::Wake),
        message("i1", Urgency::Inform),
        message("s1", Urgency::Steer),
    ];
    let turn = deliver(&gate(true, true), &pending).unwrap();
    assert_eq!(turn.key, "send:s1");
    assert_eq!(carried(&turn.command), ["i1", "s1"]);
    assert_eq!(turn.command.urgency, TurnUrgency::Steer);
    let waiting = [message("w1", Urgency::Wake)];
    assert_eq!(deliver(&gate(true, true), &waiting), None);
}

// S-a: no live run, no turn (the wake owed an incarnation; its start runs the
// policy again).
#[test]
fn no_turn_is_queued_without_a_live_run() {
    let pending = [message("w1", Urgency::Wake)];
    assert_eq!(deliver(&gate(false, false), &pending), None);
}

// S-d: re-run from 0 on the same state, the policy answers the same key, so
// the run answers it DUPLICATE.
#[test]
fn the_same_state_gives_the_same_keyed_turn() {
    let pending = [message("w1", Urgency::Wake), message("s1", Urgency::Steer)];
    assert_eq!(
        deliver(&gate(true, false), &pending),
        deliver(&gate(true, false), &pending)
    );
    assert_eq!(
        deliver(&gate(true, false), &pending)
            .unwrap()
            .command
            .urgency,
        TurnUrgency::Steer
    );
}

// --------------------------------------------------------------- cascade

// D29: the owner's close closes every child it holds, keyed by both.
#[test]
fn an_owners_close_closes_each_child_it_holds() {
    let closes = cascade(&at(OWNER), &children(&[CHILD, OTHER]));
    let keys: Vec<&str> = closes.iter().map(|c| c.key.as_str()).collect();
    assert_eq!(
        keys,
        [
            format!("cascade:{OWNER}:{CHILD}"),
            format!("cascade:{OWNER}:{OTHER}")
        ]
    );
    assert_eq!(closes[0].cover, session_cover(CHILD).unwrap());
    assert_eq!(
        closes[0].command,
        CloseSession {
            cause: "owner-closed".to_string(),
            requester: None,
        }
    );
}

#[test]
fn a_childs_close_ends_it_on_its_owner() {
    let end = child_ended(
        &at(CHILD),
        &at(OWNER),
        &SessionClosed {
            cause: "owner-closed".to_string(),
        },
    )
    .unwrap();
    assert_eq!(end.key, format!("child-ended:{OWNER}:{CHILD}"));
    assert_eq!(end.cover, session_cover(OWNER).unwrap());
    assert_eq!(end.command.child, Some(at(CHILD)));
    assert_eq!(end.command.cause, "owner-closed");
}

// ---------------------------------------------------------------- notice

fn ended(notice_parent: bool) -> IncarnationEnded {
    IncarnationEnded {
        incarnation: 3,
        cause: "runner-exit".to_string(),
        notice_parent,
    }
}

// The notice: a child's end that notices its parent mails the owner a wake,
// its message id its key.
#[test]
fn a_childs_end_wakes_its_owner_keyed_by_the_child_and_incarnation() {
    let every = [Urgency::Inform, Urgency::Wake, Urgency::Steer];
    let sent = notice(&at(CHILD), "kid", &at(OWNER), &ended(true), &every).unwrap();
    assert_eq!(sent.key, format!("notice:{CHILD}:3"));
    assert_eq!(sent.command.message_id, sent.key);
    assert_eq!(sent.command.urgency, Urgency::Wake);
    assert_eq!(sent.command.requester, None);
    assert_eq!(sent.cover, session_cover(OWNER).unwrap());
    assert!(sent.command.text.contains("kid"));
    let inform_only = notice(
        &at(CHILD),
        "kid",
        &at(OWNER),
        &ended(true),
        &[Urgency::Inform],
    )
    .unwrap();
    assert_eq!(inform_only.command.urgency, Urgency::Inform);
    assert_eq!(
        notice(&at(CHILD), "kid", &at(OWNER), &ended(true), &[]),
        None
    );
    assert_eq!(
        notice(&at(CHILD), "kid", &at(OWNER), &ended(false), &every),
        None
    );
}

// ---------------------------------------------------------------- launch

fn launched() -> ChildLaunched {
    ChildLaunched {
        child: Some(SessionRef {
            project_id: "far-heron".to_string(),
            session: CHILD.to_string(),
        }),
        child_root: "/work/far".to_string(),
        harp: "kid".to_string(),
        engine: "opencode".to_string(),
        depth: 1,
        prompt: String::new(),
    }
}

// K2, D19: the child is claimed and minted in the TARGET store, for its owner.
#[test]
fn a_launch_mints_the_child_in_the_target_store_for_its_owner() {
    let mint = launch(&at(OWNER), &launched(), "/out/kid").unwrap();
    assert_eq!(mint.claim.project_id, "far-heron");
    assert_eq!(mint.claim.key, format!("claim:{OWNER}:{CHILD}"));
    assert_eq!(
        mint.claim.cover.root,
        Some(sessions::claim_root("far-heron", "kid"))
    );
    assert_eq!(mint.claim.command.session, CHILD);
    assert_eq!(mint.mint.key, format!("mint:{OWNER}:{CHILD}"));
    assert_eq!(mint.mint.cover, session_cover(CHILD).unwrap());
    assert_eq!(
        mint.mint.command,
        MintSession {
            harp: "kid".to_string(),
            project_id: "far-heron".to_string(),
            project_dir: "/work/far".to_string(),
            output_dir: "/out/kid".to_string(),
            engine: "opencode".to_string(),
            origin: Origin::Agent,
            started_at: None,
            depth: 1,
            owner: Some(at(OWNER)),
        }
    );
}

#[test]
fn a_refused_launch_is_recorded_on_the_owner_keyed_by_both() {
    let failed = launch_failed(&at(OWNER), &at(CHILD), "sessions-names-taken").unwrap();
    assert_eq!(failed.external_id, format!("launch-failed:{OWNER}:{CHILD}"));
    assert_eq!(failed.cover, session_cover(OWNER).unwrap());
    assert_eq!(failed.fact.child, Some(at(CHILD)));
    assert_eq!(failed.fact.reason, "sessions-names-taken");
}

// --------------------------------------------------------------- destroy

// M3: before the purge, each child is closed and the owner forgets the
// destroyed session.
#[test]
fn a_destroy_closes_the_children_and_tells_the_owner_to_forget() {
    let plan = destroy(&at(CHILD), Some(&at(OWNER)), &children(&[OTHER]));
    assert_eq!(plan.close.len(), 1);
    assert_eq!(
        plan.close[0].key,
        format!("owner-destroyed:{CHILD}:{OTHER}")
    );
    assert_eq!(plan.close[0].command.cause, "owner-destroyed");
    let forget = plan.forget.unwrap();
    assert_eq!(forget.key, format!("destroy:{CHILD}"));
    assert_eq!(forget.cover, session_cover(OWNER).unwrap());
    assert_eq!(forget.command.child, Some(at(CHILD)));
    assert_eq!(destroy(&at(OWNER), None, &[]).forget, None);
}

// ------------------------------------------------------------- the races

/// A child session's stream, minted for `OWNER`.
fn child_stream() -> Harness<SessionAggregate> {
    let mut registry = Registry::new();
    registry
        .aggregate::<SessionAggregate>(SessionAggregate::default)
        .unwrap();
    let mut harness = Harness::new(registry, session_cover(CHILD).unwrap())
        .with_samples(Samples::starting_at(100));
    let mint: SessionCommand = MintSession {
        harp: "kid".to_string(),
        project_id: "calm-otter".to_string(),
        project_dir: "/w".to_string(),
        output_dir: String::new(),
        engine: "claude-code".to_string(),
        origin: Origin::Agent,
        started_at: None,
        depth: 1,
        owner: Some(at(OWNER)),
    }
    .into();
    harness.when(mint).result().unwrap();
    harness
}

/// What `book` recorded on the stream: its events, or `None` for a
/// DUPLICATE.
fn sent(
    harness: &mut Harness<SessionAggregate>,
    book: angzarr_embedded::v1::CommandBook,
) -> Option<Vec<SessionEvent>> {
    match harness.when_book(book).handled().expect("not refused") {
        Handled::Applied(recorded) => Some(
            recorded
                .books()
                .iter()
                .flat_map(|book| &book.pages)
                .filter_map(|page| match &page.body {
                    Some(angzarr_embedded::v1::event_page::Body::Event(any)) => {
                        Some(SessionEvent::from_any(any).unwrap())
                    }
                    _ => None,
                })
                .collect(),
        ),
        Handled::Duplicate(..) => None,
    }
}

// Two policies race to close one child (the owner's cascade and its
// destroy), and the cascade is redelivered: the child is closed once, the
// second policy records nothing, the redelivery is a DUPLICATE.
#[test]
fn a_cascade_racing_a_destroy_and_its_redelivery_close_the_child_once() {
    let mut child = child_stream();
    let held = children(&[CHILD]);
    let by_cascade = cascade(&at(OWNER), &held).remove(0);
    let by_destroy = destroy(&at(OWNER), None, &held).close.remove(0);
    let first = sent(&mut child, by_cascade.book()).unwrap();
    assert!(
        matches!(first.as_slice(), [SessionEvent::SessionClosed(c)] if c.cause == "owner-closed")
    );
    assert_eq!(sent(&mut child, by_destroy.book()), Some(Vec::new()));
    assert_eq!(sent(&mut child, by_cascade.book()), None, "a duplicate");
}

// A notice redelivered (the daemon restarted and re-ran the policy from 0)
// is one message.
#[test]
fn a_notice_redelivered_is_one_message() {
    let mut owner = child_stream();
    let every = [Urgency::Inform, Urgency::Wake, Urgency::Steer];
    // The notice goes to the harness's stream (the child's own here: the
    // stream does not matter to the key; its sender is the user).
    let mut sent_notice = notice(&at(OTHER), "kid", &at(OWNER), &ended(true), &every).unwrap();
    sent_notice.cover = session_cover(CHILD).unwrap();
    let first = sent(&mut owner, sent_notice.book()).unwrap();
    assert!(
        matches!(first.as_slice(), [SessionEvent::MessageSent(m)] if m.message_id == sent_notice.key)
    );
    assert_eq!(sent(&mut owner, sent_notice.book()), None);
}
