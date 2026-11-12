//! The daemon's board (D18, S-d): sessions and runs folded as their
//! aggregates fold them, in whatever order a catch-up hands their streams.

use angzarr_embedded::angzarr_router::hpb::ProjectRequest;
use angzarr_embedded::testing::block_on_ready as ready;
use angzarr_embedded::v1::{event_page, Cover, EventBook, EventPage, Uuid};
use angzarr_embedded::{Any, Payload, ProjectorHandler};
use runs::{
    run_root, RunEnqueued, RunEvent, RunMode, TurnCompletedRecorded, TurnFailedRecorded,
    TurnFailureClass, TurnQueued, TurnUrgency, RUN_DOMAIN,
};
use sessions::{
    ChildLaunched, ClearRequested, IncarnationOwed, MessageSent, Origin, SessionClosed,
    SessionEvent, SessionMinted, SessionRef, Urgency, SESSION_DOMAIN,
};

use crate::{catch_up, run_text, Board, Boards, EndedTurn, Message};

const ID: &str = "0b0b0b0b-0b0b-0b0b-0b0b-0b0b0b0b0b0b";

fn root() -> Uuid {
    Uuid::from_bytes([0x0b; 16])
}

fn page(sequence: u64, any: Any) -> EventPage {
    EventPage {
        sequence,
        body: Some(event_page::Body::Event(any)),
        meta: None,
    }
}

fn book(domain: &str, root: Uuid, first: u64, events: Vec<Any>) -> EventBook {
    let pages: Vec<EventPage> = (first..)
        .zip(events)
        .map(|(sequence, any)| page(sequence, any))
        .collect();
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

fn minted(owner: Option<&str>) -> Vec<Any> {
    vec![
        SessionEvent::SessionMinted(SessionMinted {
            harp: "kid-harp".to_string(),
            project_id: "calm-otter".to_string(),
            project_dir: "/work".to_string(),
            output_dir: "/work/out".to_string(),
            engine: "claude-code".to_string(),
            origin: Origin::Agent,
            started_at: None,
            depth: 1,
            owner: owner.map(|session| SessionRef {
                project_id: "calm-otter".to_string(),
                session: session.to_string(),
            }),
        })
        .to_any(),
        SessionEvent::IncarnationOwed(IncarnationOwed {
            incarnation: 1,
            reason: "first".to_string(),
        })
        .to_any(),
    ]
}

fn run_events() -> Vec<Any> {
    vec![
        RunEvent::RunEnqueued(RunEnqueued {
            session: ID.to_string(),
            incarnation: 1,
            mode: RunMode::Structured,
            hook_credential_hash: vec![3; 32],
            launcher_lock: String::new(),
        })
        .to_any(),
        RunEvent::TurnQueued(TurnQueued {
            prompt_ref: String::new(),
            resume: true,
            message_ids: vec!["m1".to_string()],
            urgency: TurnUrgency::Wake,
        })
        .to_any(),
        RunEvent::TurnCompletedRecorded(TurnCompletedRecorded {
            turn: 1,
            native_key: String::new(),
            answer_ref: String::new(),
            exit_code: 0,
            message_ids: vec!["m1".to_string()],
        })
        .to_any(),
        RunEvent::TurnQueued(TurnQueued {
            prompt_ref: String::new(),
            resume: true,
            message_ids: vec!["m2".to_string()],
            urgency: TurnUrgency::Wake,
        })
        .to_any(),
        RunEvent::TurnFailedRecorded(TurnFailedRecorded {
            turn: 3,
            class: TurnFailureClass::Interrupted,
            detail: String::new(),
            message_ids: vec!["m2".to_string()],
        })
        .to_any(),
    ]
}

#[test]
fn a_session_is_folded_as_its_aggregate_folds_it() {
    let mut board = Board::new();
    board
        .project(&book(SESSION_DOMAIN, root(), 0, minted(Some("owner"))))
        .unwrap();
    let state = board.session(ID).expect("the session is on the board");
    assert_eq!(state.harp, "kid-harp");
    assert_eq!(state.open_incarnation, Some(1));
    assert_eq!(state.owner.as_ref().unwrap().session, "owner");
    assert_eq!(board.find("kid-harp").unwrap().0, ID);
    assert!(board.find("nobody").is_none());
    assert_eq!(board.sessions().count(), 1);
}

#[test]
fn a_later_book_continues_the_stream_and_one_from_zero_starts_it_afresh() {
    let mut board = Board::new();
    board
        .project(&book(SESSION_DOMAIN, root(), 0, minted(None)))
        .unwrap();
    let closed = SessionEvent::SessionClosed(SessionClosed {
        cause: "done".to_string(),
    })
    .to_any();
    board
        .project(&book(SESSION_DOMAIN, root(), 2, vec![closed]))
        .unwrap();
    let state = board.session(ID).unwrap();
    assert!(state.closed);
    assert_eq!(state.harp, "kid-harp", "the earlier events still hold");
    board
        .project(&book(SESSION_DOMAIN, root(), 0, minted(None)))
        .unwrap();
    assert!(!board.session(ID).unwrap().closed, "folded afresh from 0");
}

#[test]
fn a_run_keeps_its_state_and_the_turns_it_ended() {
    let mut board = Board::new();
    let run = run_root(ID, 1);
    let events = run_events();
    board
        .project(&book(RUN_DOMAIN, run.clone(), 0, events[..3].to_vec()))
        .unwrap();
    board
        .project(&book(RUN_DOMAIN, run, 3, events[3..].to_vec()))
        .unwrap();
    let fold = board.run(ID, 1).expect("the run is on the board");
    assert!(fold.live());
    assert_eq!(fold.state.session, ID);
    assert!(fold.state.turns.is_empty(), "both turns ended");
    assert_eq!(
        fold.ended_turns,
        vec![
            EndedTurn {
                turn: 1,
                message_ids: vec!["m1".to_string()],
                failed: false
            },
            EndedTurn {
                turn: 3,
                message_ids: vec!["m2".to_string()],
                failed: true
            },
        ]
    );
    assert!(board.run(ID, 2).is_none());
    assert_eq!(
        run_text(ID, 1),
        uuid::Uuid::from_slice(&run_root(ID, 1).value)
            .unwrap()
            .to_string()
    );
}

#[test]
fn an_unenqueued_run_is_not_live() {
    let mut board = Board::new();
    let queued = run_events()[1].clone();
    board
        .project(&book(RUN_DOMAIN, run_root(ID, 1), 0, vec![queued]))
        .unwrap();
    assert!(!board.run(ID, 1).unwrap().live());
}

#[test]
fn a_purged_stream_leaves_the_board() {
    let mut board = Board::new();
    board
        .project(&book(SESSION_DOMAIN, root(), 0, minted(None)))
        .unwrap();
    board
        .project(&book(RUN_DOMAIN, run_root(ID, 1), 0, run_events()))
        .unwrap();
    board
        .project(&book(SESSION_DOMAIN, root(), 0, Vec::new()))
        .unwrap();
    board
        .project(&book(RUN_DOMAIN, run_root(ID, 1), 0, Vec::new()))
        .unwrap();
    assert!(board.session(ID).is_none());
    assert!(board.run(ID, 1).is_none());
}

#[test]
fn an_empty_book_later_in_a_stream_changes_nothing() {
    let mut board = Board::new();
    board
        .project(&book(SESSION_DOMAIN, root(), 0, minted(None)))
        .unwrap();
    let mut later = book(SESSION_DOMAIN, root(), 0, Vec::new());
    later.next_sequence = 2;
    board.project(&later).unwrap();
    assert!(board.session(ID).is_some());
}

#[test]
fn another_domain_is_passed_over_and_garble_is_an_error() {
    let mut board = Board::new();
    board
        .project(&book("trees", root(), 0, minted(None)))
        .unwrap();
    assert!(board.session(ID).is_none());
    let garbled = Any {
        type_url: "type.googleapis.com/nothing.Known".to_string(),
        value: vec![1, 2, 3],
    };
    assert!(board
        .project(&book(SESSION_DOMAIN, root(), 0, vec![garbled]))
        .is_err());
}

#[test]
fn the_port_folds_what_a_catch_up_hands_it() {
    let boards = Boards::default();
    assert_eq!(boards.name(), "daemon-board");
    let request = ProjectRequest {
        events: Some(book(SESSION_DOMAIN, root(), 0, minted(None))),
    };
    ready(boards.dispatch(&request)).unwrap();
    assert!(boards.board().session(ID).is_some());
    let garbled = ProjectRequest {
        events: Some(book(
            SESSION_DOMAIN,
            root(),
            0,
            vec![Any {
                type_url: "type.googleapis.com/nothing.Known".to_string(),
                value: vec![1],
            }],
        )),
    };
    let error = ready(boards.dispatch(&garbled)).unwrap_err();
    assert!(!error.code.is_empty());
    // The bug names the type it could not read, among its args.
    assert!(
        error.extras.values().any(|v| v.contains("nothing.Known")),
        "{:?}",
        error.extras
    );
    let _ = catch_up();
}

#[test]
fn a_session_keeps_its_launches_messages_and_clears() {
    let launched = ChildLaunched {
        child: Some(SessionRef {
            project_id: "calm-otter".to_string(),
            session: "kid".to_string(),
        }),
        child_root: "/work".to_string(),
        harp: "kid-harp".to_string(),
        engine: "claude-code".to_string(),
        depth: 1,
        prompt: "port it".to_string(),
    };
    let mut events = minted(None);
    events.extend([
        SessionEvent::ChildLaunched(launched.clone()).to_any(),
        SessionEvent::MessageSent(MessageSent {
            message_id: "m1".to_string(),
            urgency: Urgency::Wake,
            text: "hello".to_string(),
            sender: None,
        })
        .to_any(),
        SessionEvent::ClearRequested(ClearRequested {
            clear_id: "c1".to_string(),
        })
        .to_any(),
    ]);
    let mut board = Board::new();
    board
        .project(&book(SESSION_DOMAIN, root(), 0, events))
        .unwrap();
    assert_eq!(board.launched(ID), [launched]);
    assert_eq!(
        board.message(ID, "m1"),
        Some(&Message {
            urgency: Urgency::Wake,
            text: "hello".to_string()
        })
    );
    assert_eq!(board.message(ID, "m2"), None);
    assert_eq!(board.message("nobody", "m1"), None);
    assert_eq!(board.clears(ID), ["c1".to_string()]);
    assert!(board.clears("nobody").is_empty());
    assert!(board.launched("nobody").is_empty());
    board
        .project(&book(SESSION_DOMAIN, root(), 0, minted(None)))
        .unwrap();
    assert!(board.launched(ID).is_empty(), "folded afresh from 0");
}

// S32: the board's view of a session stream is its state as the session
// aggregate folds it, the wire message.
#[test]
fn the_projection_of_a_session_stream_is_its_state() {
    use prost::Message as _;
    let boards = Boards::default();
    let request = ProjectRequest {
        events: Some(book(SESSION_DOMAIN, root(), 0, minted(None))),
    };
    let view = ready(boards.dispatch(&request))
        .unwrap()
        .projection
        .expect("a view");
    let state = sessions_proto::v1::SessionState::decode(view.value.as_slice()).unwrap();
    assert_eq!(state.harp, boards.board().session(ID).unwrap().harp);
}

/// A fill that records each gap it is asked and reads nothing.
#[derive(Default)]
struct Recording(std::sync::Mutex<Vec<(u64, u64)>>);

impl held_projection::Fill for Recording {
    fn pages<'a>(
        &'a self,
        _: &'a angzarr_embedded::v1::Cover,
        from: u64,
        to: u64,
    ) -> angzarr_embedded::angzarr_ports::BoxFuture<
        'a,
        Result<Vec<angzarr_embedded::v1::EventPage>, angzarr_embedded::StoreError>,
    > {
        self.0.lock().unwrap().push((from, to));
        Box::pin(async { Ok(Vec::new()) })
    }
}

// S32: a model made filled reads a gap through its fill: a session's book
// that starts past where the model is asks for the pages before it.
#[test]
fn a_filled_model_reads_a_gap_through_its_fill() {
    use angzarr_embedded::angzarr_router::hpb::ProjectRequest;
    use angzarr_embedded::ProjectorHandler;
    let fill = std::sync::Arc::new(Recording::default());
    let model = crate::Boards::filled(fill.clone());
    let late = angzarr_embedded::v1::EventBook {
        cover: Some(angzarr_embedded::v1::Cover {
            domain: sessions::SESSION_DOMAIN.to_string(),
            root: Some(angzarr_embedded::v1::Uuid::from_bytes([7; 16])),
            edition: None,
        }),
        pages: vec![angzarr_embedded::v1::EventPage {
            sequence: 2,
            body: None,
            meta: None,
        }],
        snapshot: None,
        next_sequence: 3,
    };
    angzarr_embedded::testing::block_on_ready(
        model.dispatch(&ProjectRequest { events: Some(late) }),
    )
    .unwrap();
    assert_eq!(*fill.0.lock().unwrap(), [(0, 2)]);
}
