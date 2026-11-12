//! The pending messages (D28 K3, D30). NEW-DESIGN. The streams are handed in
//! the orders a catch-up can hand them (no global position, S-d), to force the
//! cross-stream races.

use angzarr_embedded::angzarr_router::hpb::ProjectRequest;
use angzarr_embedded::testing::block_on_ready as ready;
use angzarr_embedded::v1::{event_page, Cover, EventBook, EventPage, Uuid};
use angzarr_embedded::{Any, Payload, ProjectorHandler};
use runs::{run_root, RunEnqueued, RunEvent, RunMode, TurnQueued, TurnUrgency, RUN_DOMAIN};
use sessions::{MessageSent, SessionClosed, SessionEvent, SessionRef, Urgency, SESSION_DOMAIN};

use crate::{catch_up, Pending, PendingMessage, PendingMessages};

/// The session stream root `n`, and its text as a run names its session.
fn session(n: u8) -> (Uuid, String) {
    let id = uuid::Uuid::from_bytes([n; 16]);
    (Uuid::from_bytes([n; 16]), id.hyphenated().to_string())
}

fn page(sequence: u64, any: Any) -> EventPage {
    EventPage {
        sequence,
        body: Some(event_page::Body::Event(any)),
        meta: None,
    }
}

fn book(domain: &str, root: Uuid, pages: Vec<EventPage>) -> EventBook {
    let next = pages.last().map_or(0, |p| p.sequence + 1);
    EventBook {
        cover: Some(Cover {
            domain: domain.to_string(),
            root: Some(root),
            edition: None,
        }),
        pages,
        next_sequence: next,
        snapshot: None,
    }
}

fn message(id: &str, urgency: Urgency) -> PendingMessage {
    PendingMessage {
        message_id: id.to_string(),
        urgency,
        text: format!("text of {id}"),
        sender: Some(SessionRef {
            project_id: "calm-otter".to_string(),
            session: "owner".to_string(),
        }),
    }
}

/// Session `n`'s stream: `events` from `first` on.
fn on_session(n: u8, first: u64, events: Vec<SessionEvent>) -> EventBook {
    let (root, _) = session(n);
    let pages = (first..)
        .zip(events)
        .map(|(sequence, event)| page(sequence, event.to_any()))
        .collect();
    book(SESSION_DOMAIN, root, pages)
}

fn sent(of: &PendingMessage) -> SessionEvent {
    SessionEvent::MessageSent(MessageSent {
        message_id: of.message_id.clone(),
        urgency: of.urgency,
        text: of.text.clone(),
        sender: of.sender.clone(),
    })
}

/// The run of session `n`'s `incarnation`: enqueued, then a turn per entry
/// of `turns`, from `first` on (the enqueue only at 0).
fn on_run(n: u8, incarnation: u32, first: u64, turns: &[&[&str]]) -> EventBook {
    let (_, text) = session(n);
    let mut events = Vec::new();
    if first == 0 {
        events.push(RunEvent::RunEnqueued(RunEnqueued {
            session: text.clone(),
            incarnation,
            mode: RunMode::OneShot,
            runner_credential_hash: vec![1; 32],
            model_credential_hash: vec![2; 32],
            hook_credential_hash: vec![3; 32],
            launcher_lock: String::new(),
        }));
    }
    for ids in turns {
        events.push(RunEvent::TurnQueued(TurnQueued {
            prompt_ref: String::new(),
            resume: true,
            message_ids: ids.iter().map(|id| id.to_string()).collect(),
            urgency: TurnUrgency::Wake,
        }));
    }
    let pages = (first..)
        .zip(events)
        .map(|(sequence, event)| page(sequence, event.to_any()))
        .collect();
    book(RUN_DOMAIN, run_root(&text, incarnation), pages)
}

// K3: a message is pending from its MessageSent, in the order sent.
#[test]
fn a_message_sent_is_pending_in_the_order_sent() {
    let (m1, m2) = (message("m1", Urgency::Inform), message("m2", Urgency::Wake));
    let mut pending = Pending::new();
    pending
        .project(&on_session(1, 0, vec![sent(&m1), sent(&m2)]))
        .unwrap();
    assert_eq!(pending.pending(&session(1).1), vec![m1, m2]);
    assert!(pending.pending(&session(2).1).is_empty());
}

// K3: a turn that carries a message consumes it.
#[test]
fn a_turn_queued_with_a_message_consumes_it() {
    let (m1, m2) = (message("m1", Urgency::Inform), message("m2", Urgency::Wake));
    let mut pending = Pending::new();
    pending
        .project(&on_session(1, 0, vec![sent(&m1), sent(&m2)]))
        .unwrap();
    pending.project(&on_run(1, 1, 0, &[&["m2"]])).unwrap();
    assert_eq!(pending.pending(&session(1).1), vec![m1]);
}

// S-d, the race: the run's TurnQueued is caught up before the MessageSent it
// consumes; the message is still not pending.
#[test]
fn a_turn_folded_before_the_message_it_carries_still_consumes_it() {
    let m1 = message("m1", Urgency::Wake);
    let mut pending = Pending::new();
    pending.project(&on_run(1, 1, 0, &[&["m1"]])).unwrap();
    pending.project(&on_session(1, 0, vec![sent(&m1)])).unwrap();
    assert!(pending.pending(&session(1).1).is_empty());
}

// A run of another session consumes nothing of this one's.
#[test]
fn a_turn_of_another_session_consumes_nothing() {
    let m1 = message("m1", Urgency::Wake);
    let mut pending = Pending::new();
    pending.project(&on_session(1, 0, vec![sent(&m1)])).unwrap();
    pending.project(&on_run(2, 1, 0, &[&["m1"]])).unwrap();
    assert_eq!(pending.pending(&session(1).1), vec![m1]);
}

// A later page of a run stream adds to what it carried; a later run of the
// session consumes as the first did.
#[test]
fn every_turn_of_every_run_of_the_session_consumes() {
    let (m1, m2, m3) = (
        message("m1", Urgency::Wake),
        message("m2", Urgency::Wake),
        message("m3", Urgency::Wake),
    );
    let mut pending = Pending::new();
    pending
        .project(&on_session(1, 0, vec![sent(&m1), sent(&m2), sent(&m3)]))
        .unwrap();
    pending.project(&on_run(1, 1, 0, &[&["m1"]])).unwrap();
    pending.project(&on_run(1, 1, 2, &[&["m2"]])).unwrap();
    pending.project(&on_run(1, 2, 0, &[&["m3"]])).unwrap();
    assert!(pending.pending(&session(1).1).is_empty());
}

// D-N: a closed session has nothing pending (no turn will carry it).
#[test]
fn a_closed_session_has_nothing_pending() {
    let m1 = message("m1", Urgency::Inform);
    let mut pending = Pending::new();
    pending
        .project(&on_session(
            1,
            0,
            vec![
                sent(&m1),
                SessionEvent::SessionClosed(SessionClosed {
                    cause: "done".to_string(),
                }),
            ],
        ))
        .unwrap();
    assert!(pending.pending(&session(1).1).is_empty());
    assert!(pending.owed().is_empty());
}

// Rebuildable like pending-launches: a book from 0 starts the stream afresh,
// and a purged stream (no page, next_sequence 0) leaves.
#[test]
fn a_stream_rebuilt_or_purged_is_folded_afresh() {
    let (m1, m2) = (message("m1", Urgency::Wake), message("m2", Urgency::Wake));
    let mut pending = Pending::new();
    pending
        .project(&on_session(1, 0, vec![sent(&m1), sent(&m2)]))
        .unwrap();
    pending.project(&on_run(1, 1, 0, &[&["m1"]])).unwrap();
    pending.project(&on_session(1, 0, vec![sent(&m1)])).unwrap();
    assert!(pending.pending(&session(1).1).is_empty(), "m1 carried");
    pending
        .project(&book(RUN_DOMAIN, run_root(&session(1).1, 1), Vec::new()))
        .unwrap();
    assert_eq!(pending.pending(&session(1).1), vec![m1.clone()]);
    pending
        .project(&book(SESSION_DOMAIN, session(1).0, Vec::new()))
        .unwrap();
    assert!(pending.pending(&session(1).1).is_empty());
}

// K3: what a driver acts on is the sessions with something pending.
#[test]
fn the_owed_are_the_sessions_with_something_pending() {
    let (m1, m2) = (message("m1", Urgency::Wake), message("m2", Urgency::Steer));
    let mut pending = Pending::new();
    pending.project(&on_session(1, 0, vec![sent(&m1)])).unwrap();
    pending.project(&on_session(2, 0, vec![sent(&m2)])).unwrap();
    pending.project(&on_run(1, 1, 0, &[&["m1"]])).unwrap();
    assert_eq!(pending.owed(), vec![(session(2).1, vec![m2])]);
}

// The model on the projector port folds what it is handed.
#[test]
fn the_projector_port_folds_the_book_it_is_handed() {
    let m1 = message("m1", Urgency::Wake);
    let model = PendingMessages::default();
    let request = ProjectRequest {
        events: Some(on_session(1, 0, vec![sent(&m1)])),
    };
    ready(model.dispatch(&request)).unwrap();
    assert_eq!(model.pending().pending(&session(1).1), vec![m1]);
    assert_eq!(model.name(), "pending-messages");
    let _ = catch_up();
}

// S32: what the port answers for a stream is the session's pending messages,
// as the resource message (`ctxloom.daemon.v1.Session`); a run stream's is
// its run's session's.
#[test]
fn the_projection_is_the_sessions_pending_messages_as_its_resource() {
    use prost::Message as _;
    let m1 = message("m1", Urgency::Wake);
    let m2 = message("m2", Urgency::Inform);
    let model = PendingMessages::default();
    let hand = |book| {
        ready(model.dispatch(&ProjectRequest { events: Some(book) }))
            .unwrap()
            .projection
            .expect("a view")
    };
    hand(on_session(1, 0, vec![sent(&m1), sent(&m2)]));
    let view = hand(on_run(1, 1, 0, &[&["m1"]]));
    assert_eq!(
        view.type_url,
        <daemon_proto::v1::Session as prost::Name>::type_url()
    );
    let resource = daemon_proto::v1::Session::decode(view.value.as_slice()).unwrap();
    assert_eq!(resource.uid, session(1).1);
    let pending: Vec<(&str, &str)> = resource
        .pending
        .iter()
        .map(|p| (p.message_id.as_str(), p.urgency.as_str()))
        .collect();
    assert_eq!(pending, [("m2", "inform")]);
}
