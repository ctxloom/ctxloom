//! The daemon's plan (D28 S-d, S-e): from owed state, each step keyed, none
//! twice in one process.

use std::collections::BTreeSet;

use angzarr_embedded::v1::{command_page, event_page, Cover, EventBook, EventPage};
use angzarr_embedded::{Any, Payload};
use daemon_board::{run_text, Board};
use pending_messages::Pending;
use runs::{
    run_root, QueueTurn, RunEnqueued, RunEvent, RunMode, TurnCompletedRecorded, TurnFailedRecorded,
    TurnFailureClass, TurnQueued, TurnUrgency, RUN_DOMAIN,
};
use sessions::{
    ChildLaunched, ClearRequested, CloseSession, EndChild, IncarnationOwed, MessageSent, Origin,
    SendMessage, SessionClosed, SessionEvent, SessionMinted, SessionRef, StopOrdered, Urgency,
    SESSION_DOMAIN,
};

use crate::{plan, supported, Fed, RunAt, Scene, Step, CHILD_ENGINE};

const PROJECT: &str = "calm-otter";
const OWNER: &str = "0a0a0a0a-0a0a-0a0a-0a0a-0a0a0a0a0a0a";
const CHILD: &str = "0c0c0c0c-0c0c-0c0c-0c0c-0c0c0c0c0c0c";

fn at(session: &str) -> SessionRef {
    SessionRef {
        project_id: PROJECT.to_string(),
        session: session.to_string(),
    }
}

fn root(session: &str) -> angzarr_embedded::v1::Uuid {
    let id = uuid::Uuid::parse_str(session).unwrap();
    angzarr_embedded::v1::Uuid::from_bytes(*id.as_bytes())
}

fn book(domain: &str, root: angzarr_embedded::v1::Uuid, events: Vec<Any>) -> EventBook {
    let pages: Vec<EventPage> = (0..)
        .zip(events)
        .map(|(sequence, any)| EventPage {
            sequence,
            body: Some(event_page::Body::Event(any)),
            meta: None,
        })
        .collect();
    EventBook {
        cover: Some(Cover {
            domain: domain.to_string(),
            root: Some(root),
            edition: None,
        }),
        next_sequence: pages.len() as u64,
        pages,
        snapshot: None,
    }
}

fn minted(harp: &str, owner: Option<&str>) -> Vec<SessionEvent> {
    vec![
        SessionEvent::SessionMinted(SessionMinted {
            harp: harp.to_string(),
            project_id: PROJECT.to_string(),
            project_dir: "/work".to_string(),
            output_dir: "/work/out".to_string(),
            engine: CHILD_ENGINE.to_string(),
            origin: Origin::Agent,
            started_at: None,
            depth: u32::from(owner.is_some()),
            owner: owner.map(at),
        }),
        SessionEvent::IncarnationOwed(IncarnationOwed {
            incarnation: 1,
            reason: "first".to_string(),
        }),
    ]
}

fn launched(prompt: &str) -> SessionEvent {
    SessionEvent::ChildLaunched(ChildLaunched {
        child: Some(at(CHILD)),
        child_root: "/work".to_string(),
        harp: "kid".to_string(),
        engine: CHILD_ENGINE.to_string(),
        depth: 1,
        prompt: prompt.to_string(),
    })
}

fn sent(id: &str, urgency: Urgency, text: &str) -> SessionEvent {
    SessionEvent::MessageSent(MessageSent {
        message_id: id.to_string(),
        urgency,
        text: text.to_string(),
        sender: None,
    })
}

fn closed() -> SessionEvent {
    SessionEvent::SessionClosed(SessionClosed {
        cause: "done".to_string(),
    })
}

fn enqueued() -> RunEvent {
    RunEvent::RunEnqueued(RunEnqueued {
        session: CHILD.to_string(),
        incarnation: 1,
        mode: RunMode::Structured,
        hook_credential_hash: vec![3; 32],
        launcher_lock: String::new(),
    })
}

fn queued(ids: &[&str], urgency: TurnUrgency) -> RunEvent {
    RunEvent::TurnQueued(TurnQueued {
        prompt_ref: String::new(),
        resume: true,
        message_ids: ids.iter().map(|id| id.to_string()).collect(),
        urgency,
    })
}

/// A world: the owner's and the child's streams, the child's run's, and
/// what the daemon hosts and has done.
#[derive(Default)]
struct World {
    owner: Vec<SessionEvent>,
    child: Vec<SessionEvent>,
    run: Vec<RunEvent>,
    hosted: bool,
    done: BTreeSet<String>,
}

impl World {
    fn launched(prompt: &str) -> World {
        let mut owner = minted("boss", None);
        owner.push(launched(prompt));
        World {
            owner,
            ..World::default()
        }
    }

    /// The owner holding the child, the child minted for it.
    fn with_child() -> World {
        let mut world = World::launched("");
        world.child = minted("kid", Some(OWNER));
        world
    }

    fn hosting(mut self, run: Vec<RunEvent>) -> World {
        self.run = run;
        self.hosted = true;
        self
    }

    fn plan(&self) -> Vec<Step> {
        let mut board = Board::new();
        let mut pending = Pending::new();
        let streams = [(OWNER, &self.owner), (CHILD, &self.child)];
        for (id, events) in streams {
            if events.is_empty() {
                continue;
            }
            let b = book(
                SESSION_DOMAIN,
                root(id),
                events.iter().map(Payload::to_any).collect(),
            );
            board.project(&b).unwrap();
            pending.project(&b).unwrap();
        }
        if !self.run.is_empty() {
            let b = book(
                RUN_DOMAIN,
                run_root(CHILD, 1),
                self.run.iter().map(Payload::to_any).collect(),
            );
            board.project(&b).unwrap();
            pending.project(&b).unwrap();
        }
        let hosted: BTreeSet<String> = match self.hosted {
            true => [run_text(CHILD, 1)].into(),
            false => BTreeSet::new(),
        };
        plan(&Scene {
            project_id: PROJECT,
            board: &board,
            pending: &pending,
            hosted: &hosted,
            done: &self.done,
        })
    }
}

fn keys(steps: &[Step]) -> Vec<&str> {
    steps.iter().map(Step::key).collect()
}

fn command<M: angzarr_embedded::markup::DomainMessage>(step: &Step) -> M {
    let Step::Send(sent) = step else {
        panic!("not a send: {step:?}");
    };
    let Some(command_page::Body::Command(command)) =
        sent.book.page.as_ref().and_then(|p| p.body.as_ref())
    else {
        panic!("not a command: {step:?}");
    };
    let wire = command
        .payload
        .as_ref()
        .unwrap()
        .to_msg::<M::Wire>()
        .unwrap();
    M::from_wire(wire).unwrap()
}

fn child_run() -> RunAt {
    RunAt {
        session: CHILD.to_string(),
        incarnation: 1,
    }
}

// K2: a child held and not yet minted is claimed and minted; the failure is
// the owner's compensation fact.
#[test]
fn a_launched_child_not_yet_minted_is_launched() {
    let steps = World::launched("port it").plan();
    assert_eq!(keys(&steps), [format!("mint:{OWNER}:{CHILD}")]);
    let Step::Launch { mint, failed, .. } = &steps[0] else {
        panic!("{steps:?}");
    };
    assert_eq!(mint.claim.key, format!("claim:{OWNER}:{CHILD}"));
    assert_eq!(mint.mint.command.owner, Some(at(OWNER)));
    assert_eq!(mint.mint.command.output_dir, "/work/out");
    assert_eq!(failed.key, format!("launch-failed:{OWNER}:{CHILD}"));
    assert_eq!(failed.project_id, PROJECT);
}

// A child the owner no longer holds (it failed or ended) is not launched.
#[test]
fn a_child_the_owner_no_longer_holds_is_not_launched() {
    let mut world = World::launched("port it");
    world.owner.push(SessionEvent::ChildLaunchFailed(
        sessions::ChildLaunchFailed {
            child: Some(at(CHILD)),
            reason: "x".to_string(),
        },
    ));
    assert!(world.plan().is_empty());
}

// A child in another project is that project's to launch.
#[test]
fn a_child_in_another_project_is_not_launched_here() {
    let mut world = World::launched("port it");
    world.owner[2] = SessionEvent::ChildLaunched(ChildLaunched {
        child: Some(SessionRef {
            project_id: "far-heron".to_string(),
            session: CHILD.to_string(),
        }),
        child_root: "/far".to_string(),
        harp: "kid".to_string(),
        engine: CHILD_ENGINE.to_string(),
        depth: 1,
        prompt: "x".to_string(),
    });
    assert!(world.plan().is_empty());
}

// The launch's prompt is the minted child's first wake.
#[test]
fn a_minted_child_is_sent_its_launch_prompt_as_a_wake() {
    let mut world = World::launched("port the parser");
    world.child = minted("kid", Some(OWNER));
    let steps = world.plan();
    let key = format!("launch:{CHILD}");
    assert_eq!(keys(&steps)[0], key);
    let send: SendMessage = command(&steps[0]);
    assert_eq!(send.message_id, key);
    assert_eq!(send.urgency, Urgency::Wake);
    assert_eq!(send.text, "port the parser");
    assert_eq!(
        send.supported,
        [Urgency::Inform, Urgency::Wake, Urgency::Steer]
    );
    let Step::Send(sent) = &steps[0] else {
        unreachable!()
    };
    assert_eq!(
        sent.book.page.as_ref().unwrap().command_id,
        Some(sessions::command_id(&key))
    );
    world.done.insert(key.clone());
    assert!(
        !keys(&world.plan()).contains(&key.as_str()),
        "done: not again"
    );
}

// D2: a child owed an incarnation with no run is started; a primary never.
#[test]
fn a_child_owed_an_incarnation_is_started_and_a_primary_is_not() {
    let steps = World::with_child().plan();
    assert_eq!(keys(&steps), [format!("start:{}", run_text(CHILD, 1))]);
    let Step::Start {
        at,
        harp,
        engine,
        project_dir,
        ..
    } = &steps[0]
    else {
        panic!("{steps:?}");
    };
    assert_eq!(at, &child_run());
    assert_eq!(
        (harp.as_str(), engine.as_str(), project_dir.as_str()),
        ("kid", CHILD_ENGINE, "/work")
    );
    let mut closed_child = World::with_child();
    closed_child.child.push(closed());
    assert!(
        !keys(&closed_child.plan())
            .iter()
            .any(|k| k.starts_with("start:")),
        "a closed child is not started"
    );
    let mut stopped = World::with_child();
    stopped.child.push(SessionEvent::StopOrdered(StopOrdered {
        reason: "x".to_string(),
    }));
    assert!(stopped.plan().is_empty(), "a stopped child is not started");
}

// A run already enqueued that this daemon does not host is not started again.
#[test]
fn a_run_enqueued_elsewhere_is_not_started_again() {
    let mut world = World::with_child();
    world.run = vec![enqueued()];
    assert!(world.plan().is_empty());
}

// S-a: a hosted child with a wake pending is given its turn.
#[test]
fn a_hosted_child_with_a_wake_pending_is_given_its_turn() {
    let mut world = World::with_child().hosting(vec![enqueued()]);
    world.child.push(sent("m1", Urgency::Inform, "fyi"));
    assert!(world.plan().is_empty(), "an inform alone waits (D30)");
    world.child.push(sent("m2", Urgency::Wake, "go"));
    let steps = world.plan();
    assert_eq!(keys(&steps), ["send:m2"]);
    let turn: QueueTurn = command(&steps[0]);
    assert_eq!(turn.message_ids, ["m1", "m2"]);
    assert_eq!(turn.urgency, TurnUrgency::Wake);
    let Step::Send(sent) = &steps[0] else {
        unreachable!()
    };
    assert_eq!(
        sent.book.cover.as_ref().unwrap().root,
        Some(run_root(CHILD, 1))
    );
}

// A turn queued on a hosted run is handed to the child once, with its texts.
#[test]
fn a_queued_turn_is_fed_to_the_hosted_child_once() {
    let mut world =
        World::with_child().hosting(vec![enqueued(), queued(&["m1", "m2"], TurnUrgency::Wake)]);
    world.child.push(sent("m1", Urgency::Inform, "fyi"));
    world.child.push(sent("m2", Urgency::Wake, "go"));
    let steps = world.plan();
    let key = format!("feed:{}:1", run_text(CHILD, 1));
    assert_eq!(keys(&steps), [key.as_str()]);
    assert_eq!(
        steps[0],
        Step::Feed {
            key: key.clone(),
            at: child_run(),
            turn: 1,
            urgency: TurnUrgency::Wake,
            messages: vec![
                Fed {
                    message_id: "m1".to_string(),
                    urgency: Urgency::Inform,
                    text: "fyi".to_string()
                },
                Fed {
                    message_id: "m2".to_string(),
                    urgency: Urgency::Wake,
                    text: "go".to_string()
                },
            ],
        }
    );
    world.done.insert(key);
    assert!(world.plan().is_empty());
}

// S-b: a wake waits while a turn is in flight; a steer goes.
#[test]
fn a_wake_waits_for_the_turn_in_flight_and_a_steer_does_not() {
    let mut world =
        World::with_child().hosting(vec![enqueued(), queued(&["m1"], TurnUrgency::Wake)]);
    world.child.push(sent("m1", Urgency::Wake, "one"));
    world.done.insert(format!("feed:{}:1", run_text(CHILD, 1)));
    world.child.push(sent("m2", Urgency::Wake, "two"));
    assert!(world.plan().is_empty());
    world.child.push(sent("m3", Urgency::Steer, "stop"));
    assert_eq!(keys(&world.plan()), ["send:m3"]);
}

// A message no stream holds is fed with no text rather than lost.
#[test]
fn a_turn_whose_message_is_not_on_the_board_is_fed_without_text() {
    let world =
        World::with_child().hosting(vec![enqueued(), queued(&["gone"], TurnUrgency::Steer)]);
    let Step::Feed {
        messages, urgency, ..
    } = &world.plan()[0]
    else {
        panic!()
    };
    assert_eq!(*urgency, TurnUrgency::Steer);
    assert_eq!(messages[0].text, "");
    assert_eq!(messages[0].urgency, Urgency::Wake);
}

// M1: a clear asked of a hosted child is handed to it once.
#[test]
fn a_clear_is_handed_to_the_hosted_child() {
    let mut world = World::with_child().hosting(vec![enqueued()]);
    world
        .child
        .push(SessionEvent::ClearRequested(ClearRequested {
            clear_id: "c1".to_string(),
        }));
    let steps = world.plan();
    assert_eq!(keys(&steps), [format!("clear-child:{CHILD}:c1")]);
    assert_eq!(
        steps[0],
        Step::Clear {
            key: format!("clear-child:{CHILD}:c1"),
            at: child_run()
        }
    );
}

// D13, D14: a hosted child whose session closed is ended, and nothing else
// is done on it.
#[test]
fn a_closed_hosted_child_is_ended() {
    let mut world = World::with_child().hosting(vec![enqueued()]);
    world.child.push(sent("m1", Urgency::Wake, "go"));
    world.child.push(closed());
    let steps = world.plan();
    let end = format!("end:{}", run_text(CHILD, 1));
    assert!(keys(&steps).contains(&end.as_str()), "{steps:?}");
    assert!(!keys(&steps).iter().any(|k| k.starts_with("send:")));
    // An ended run is nothing to end.
    let mut ended = World::with_child().hosting(vec![
        enqueued(),
        RunEvent::RunEnded(runs::RunEnded {
            cause: runs::EndCause::RunnerExit,
            exit_code: Some(0),
            detail: String::new(),
        }),
    ]);
    ended.child.push(closed());
    assert!(!keys(&ended.plan()).iter().any(|k| k.starts_with("end:")));
}

// D29: a closed owner closes the child it holds; the closed child is ended on
// its owner.
#[test]
fn a_closed_owner_cascades_and_a_closed_child_is_ended_on_its_owner() {
    let mut world = World::with_child();
    world.owner.push(closed());
    world.child.clear();
    let steps = world.plan();
    let cascade = format!("cascade:{OWNER}:{CHILD}");
    assert!(keys(&steps).contains(&cascade.as_str()), "{steps:?}");
    let close: CloseSession = command(steps.iter().find(|s| s.key() == cascade).unwrap());
    assert_eq!(close.cause, "owner-closed");
    let mut child_closed = World::with_child();
    child_closed.child.push(closed());
    let steps = child_closed.plan();
    let ended = format!("child-ended:{OWNER}:{CHILD}");
    let end: EndChild = command(steps.iter().find(|s| s.key() == ended).unwrap());
    assert_eq!(end.child, Some(at(CHILD)));
    assert_eq!(end.cause, "done");
}

// A closed child its owner no longer holds is told nothing.
#[test]
fn a_closed_child_its_owner_released_is_not_ended_again() {
    let mut world = World::with_child();
    world.child.push(closed());
    world
        .owner
        .push(SessionEvent::ChildEnded(sessions::ChildEnded {
            child: Some(at(CHILD)),
            cause: "done".to_string(),
        }));
    assert!(world.plan().is_empty());
}

// Each turn a child ended is told to its owner, an inform to a primary.
#[test]
fn each_turn_a_child_ended_is_told_to_its_owner() {
    let world = World::with_child().hosting(vec![
        enqueued(),
        queued(&["m1"], TurnUrgency::Wake),
        RunEvent::TurnCompletedRecorded(TurnCompletedRecorded {
            turn: 1,
            native_key: String::new(),
            answer_ref: String::new(),
            exit_code: 0,
            message_ids: vec!["m1".to_string()],
        }),
        queued(&["m2"], TurnUrgency::Wake),
        RunEvent::TurnFailedRecorded(TurnFailedRecorded {
            turn: 3,
            class: TurnFailureClass::EngineError,
            detail: String::new(),
            message_ids: vec!["m2".to_string()],
        }),
    ]);
    let steps = world.plan();
    let first = format!("notice:{CHILD}:1:1");
    let second = format!("notice:{CHILD}:1:3");
    let notice: SendMessage = command(steps.iter().find(|s| s.key() == first).unwrap());
    assert_eq!(notice.message_id, first);
    assert_eq!(notice.urgency, Urgency::Inform);
    assert_eq!(notice.text, "child kid finished its turn for m1");
    assert_eq!(notice.supported, [Urgency::Inform]);
    let Step::Send(sent) = steps.iter().find(|s| s.key() == first).unwrap() else {
        unreachable!()
    };
    assert_eq!(sent.book.cover.as_ref().unwrap().root, Some(root(OWNER)));
    let failed: SendMessage = command(steps.iter().find(|s| s.key() == second).unwrap());
    assert_eq!(failed.text, "child kid failed its turn for m2");
    let mut owner_closed = World::with_child().hosting(world.run.clone());
    owner_closed.owner.push(closed());
    assert!(
        !keys(&owner_closed.plan())
            .iter()
            .any(|k| k.starts_with("notice:")),
        "a closed owner is told nothing"
    );
}

// A daemon child takes every urgency; a primary only an inform for now.
#[test]
fn the_urgencies_a_session_takes() {
    let mut child = sessions::SessionState {
        engine: CHILD_ENGINE.to_string(),
        owner: Some(at(OWNER)),
        ..sessions::SessionState::default()
    };
    assert_eq!(
        supported(&child),
        [Urgency::Inform, Urgency::Wake, Urgency::Steer]
    );
    child.engine = "opencode".to_string();
    assert_eq!(supported(&child), [Urgency::Inform]);
    let primary = sessions::SessionState {
        engine: CHILD_ENGINE.to_string(),
        ..sessions::SessionState::default()
    };
    assert_eq!(supported(&primary), [Urgency::Inform]);
}

// One board holds every project's sessions (T14): a plan is its project's
// alone, so another project's owner, closed with a child it holds, is
// planned nothing here, and its project's own plan has its steps.
#[test]
fn another_projects_sessions_on_the_one_board_are_not_planned_here() {
    let mut world = World::with_child();
    for events in [&mut world.owner, &mut world.child] {
        if let SessionEvent::SessionMinted(minted) = &mut events[0] {
            minted.project_id = "far-heron".to_string();
        }
    }
    world.owner.push(closed());
    assert!(world.plan().is_empty(), "{:?}", keys(&world.plan()));
}
