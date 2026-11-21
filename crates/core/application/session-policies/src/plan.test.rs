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

use crate::{plan, supported, Fed, RunAt, Scene, Step, CHILD_ENGINE, CHILD_ENGINES};

const PROJECT: &str = "calm-otter";
const OWNER: &str = "0a0a0a0a-0a0a-0a0a-0a0a-0a0a0a0a0a0a";
const CHILD: &str = "0c0c0c0c-0c0c-0c0c-0c0c-0c0c0c0c0c0c";

fn at(session: &str) -> SessionRef {
    SessionRef {
        project_id: PROJECT.to_string(),
        session: session.to_string(),
    }
}

/// Another project, the one a cross-project child works in.
const FAR: &str = "far-heron";

fn far(session: &str) -> SessionRef {
    SessionRef {
        project_id: FAR.to_string(),
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
            workspace: None,
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
        launcher_lock: String::new(),
        grant: None,
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
    /// The sessions whose ctxloom MCP server watches them (the terminal
    /// wake).
    wakeable: BTreeSet<String>,
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
        self.plan_for(PROJECT)
    }

    /// The child in [`FAR`], the owner here: the owner's launch of it and
    /// the child's mint name that project (step 8 Q1).
    fn across(mut self) -> World {
        for event in self.owner.iter_mut() {
            if let SessionEvent::ChildLaunched(launched) = event {
                launched.child = Some(far(CHILD));
                launched.child_root = "/far".to_string();
            }
        }
        if let Some(SessionEvent::SessionMinted(minted)) = self.child.first_mut() {
            minted.project_id = FAR.to_string();
            minted.project_dir = "/far".to_string();
        }
        self
    }

    /// The plan of `project`, over the one board.
    fn plan_for(&self, project: &str) -> Vec<Step> {
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
            project_id: project,
            board: &board,
            pending: &pending,
            hosted: &hosted,
            done: &self.done,
            wakeable: &self.wakeable,
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
    // Its own folder, never its owner's (`/work/out`, owner ruling
    // 2026-11-16): the daemon reads it from its project's config.
    assert_eq!(mint.mint.command.output_dir, "");
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

// A child in another project (step 8 Q1) is launched by its owner's plan,
// minted in its own project at its own root: one store holds both (T14),
// and nothing in its project's plan holds its launch.
#[test]
fn a_child_in_another_project_is_minted_there_by_its_owners_plan() {
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
    let steps = world.plan();
    assert_eq!(keys(&steps), [format!("mint:{OWNER}:{CHILD}")]);
    let Step::Launch { mint, failed, .. } = &steps[0] else {
        panic!("{steps:?}");
    };
    assert_eq!(mint.mint.project_id, "far-heron");
    assert_eq!(mint.claim.project_id, "far-heron");
    assert_eq!(mint.mint.command.project_id, "far-heron");
    assert_eq!(mint.mint.command.project_dir, "/far");
    assert_eq!(mint.mint.command.owner, Some(at(OWNER)));
    // The owner is told, in its own project, if it fails.
    assert_eq!(failed.project_id, PROJECT);
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
    // Owner 2026-11-15: no stop latch; a child whose stream holds a retired
    // stop order is started like any other.
    let mut stopped = World::with_child();
    stopped.child.push(SessionEvent::StopOrdered(StopOrdered {
        reason: "x".to_string(),
    }));
    assert!(
        keys(&stopped.plan())
            .iter()
            .any(|k| k.starts_with("start:")),
        "a retired stop order keeps no child from its start"
    );
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

// Step 9 (S19, OD-57): a hosted child whose run was asked to stop
// (`session stop`, the run's RequestStop) has its run stopped, once; the child
// stays open, so nothing else is done on it, and a run asked nothing is not.
#[test]
fn a_hosted_childs_run_asked_to_stop_is_stopped() {
    let world = World::with_child().hosting(vec![
        enqueued(),
        RunEvent::StopRequested(runs::StopRequested {
            reason: "stop".to_string(),
        }),
    ]);
    let steps = world.plan();
    let stop = format!("stop:{}", run_text(CHILD, 1));
    assert!(
        steps
            .iter()
            .any(|s| matches!(s, Step::Stop { key, at } if *key == stop && *at == child_run())),
        "{steps:?}"
    );
    assert!(
        !keys(&steps).iter().any(|k| k.starts_with("end:")),
        "{steps:?}"
    );
    let asked_nothing = World::with_child().hosting(vec![enqueued()]);
    assert!(!keys(&asked_nothing.plan())
        .iter()
        .any(|k| k.starts_with("stop:")));
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

// OD-58: a daemon child on an engine with a child adapter (claude-code and
// opencode) takes every urgency; one on any other engine only an inform. A
// primary takes an inform, and a wake only while its ctxloom MCP server
// watches it (the terminal wake, owner ruling 2026-11-18); never a steer.
#[test]
fn the_urgencies_a_session_takes() {
    let every = [Urgency::Inform, Urgency::Wake, Urgency::Steer];
    let mut child = sessions::SessionState {
        engine: CHILD_ENGINE.to_string(),
        owner: Some(at(OWNER)),
        ..sessions::SessionState::default()
    };
    assert_eq!(supported(&child, false), every);
    child.engine = "opencode".to_string();
    assert_eq!(supported(&child, false), every, "opencode's child adapter");
    child.engine = "mock".to_string();
    assert_eq!(
        supported(&child, false),
        [Urgency::Inform],
        "no child adapter"
    );
    assert_eq!(
        supported(&child, true),
        [Urgency::Inform],
        "a watch wakes a primary alone"
    );
    assert_eq!(CHILD_ENGINES, ["claude-code", "opencode"]);
    for engine in CHILD_ENGINES {
        let primary = sessions::SessionState {
            engine: engine.to_string(),
            ..sessions::SessionState::default()
        };
        assert_eq!(supported(&primary, false), [Urgency::Inform], "{engine}");
        assert_eq!(
            supported(&primary, true),
            [Urgency::Inform, Urgency::Wake],
            "{engine}, watched"
        );
    }
}

// The terminal wake: a child's turn's end is told to a primary owner its
// ctxloom MCP server watches as a WAKE, so it is woken.
#[test]
fn a_watched_primary_owner_is_woken_by_its_childs_notice() {
    let mut world = World::with_child().hosting(vec![
        enqueued(),
        queued(&["m1"], TurnUrgency::Wake),
        RunEvent::TurnCompletedRecorded(TurnCompletedRecorded {
            turn: 1,
            native_key: String::new(),
            answer_ref: String::new(),
            exit_code: 0,
            message_ids: vec!["m1".to_string()],
        }),
    ]);
    world.wakeable.insert(OWNER.to_string());
    let key = format!("notice:{CHILD}:1:1");
    let steps = world.plan();
    let notice: SendMessage = command(
        steps
            .iter()
            .find(|s| s.key() == key)
            .unwrap_or_else(|| panic!("{:?}", keys(&steps))),
    );
    assert_eq!(notice.urgency, Urgency::Wake);
    assert_eq!(notice.supported, [Urgency::Inform, Urgency::Wake]);
}

// OD-58: an owner that is itself a hosted child on opencode, re-hosted (its
// host was lost), is WOKEN by its child's notice, as one on claude-code is.
#[test]
fn a_rehosted_opencode_owner_is_woken_by_its_childs_notice() {
    let mut world = World::with_child().hosting(vec![
        enqueued(),
        queued(&["m1"], TurnUrgency::Wake),
        RunEvent::TurnCompletedRecorded(TurnCompletedRecorded {
            turn: 1,
            native_key: String::new(),
            answer_ref: String::new(),
            exit_code: 0,
            message_ids: vec!["m1".to_string()],
        }),
    ]);
    let grand = "0b0b0b0b-0b0b-0b0b-0b0b-0b0b0b0b0b0b";
    if let SessionEvent::SessionMinted(owner) = &mut world.owner[0] {
        owner.engine = "opencode".to_string();
        owner.depth = 1;
        owner.owner = Some(at(grand));
    }
    world.owner.extend([
        incarnation_ended(1, "orphaned"),
        SessionEvent::IncarnationOwed(IncarnationOwed {
            incarnation: 2,
            reason: sessions::REHOSTED.to_string(),
        }),
    ]);
    let key = format!("notice:{CHILD}:1:1");
    let steps = world.plan();
    let notice: SendMessage = command(
        steps
            .iter()
            .find(|s| s.key() == key)
            .unwrap_or_else(|| panic!("{:?}", keys(&steps))),
    );
    assert_eq!(notice.urgency, Urgency::Wake);
    assert_eq!(
        notice.supported,
        [Urgency::Inform, Urgency::Wake, Urgency::Steer]
    );
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

/// The child's engine bound its native session `native-1`.
fn bound() -> SessionEvent {
    SessionEvent::EngineSessionBound(sessions::EngineSessionBound {
        native_id: "native-1".to_string(),
        transcript_path: String::new(),
        engine_version: String::new(),
        displaced: None,
    })
}

/// The child's `incarnation` ended for `cause`.
fn incarnation_ended(incarnation: u32, cause: &str) -> SessionEvent {
    SessionEvent::IncarnationEnded(sessions::IncarnationEnded {
        incarnation,
        cause: cause.to_string(),
        notice_parent: true,
    })
}

/// A child bound to its engine's session whose first incarnation ended
/// `cause`, its run started and ended elsewhere.
fn ended_child(cause: &str) -> World {
    let mut world = World::with_child();
    world.child.push(bound());
    world.child.push(incarnation_ended(1, cause));
    world.run = vec![
        enqueued(),
        RunEvent::RunEnded(runs::RunEnded {
            cause: runs::EndCause::RunnerLoss,
            exit_code: None,
            detail: String::new(),
        }),
    ];
    world
}

// T19: a child whose last incarnation was lost (`orphaned`) is re-hosted as
// its next, keyed by it; its owner is named for a refusal's notice.
#[test]
fn a_child_whose_host_was_lost_is_rehosted_as_its_next_incarnation() {
    let steps = ended_child("orphaned").plan();
    assert_eq!(keys(&steps), [format!("rehost:{CHILD}:2")]);
    let Step::Rehost {
        at, harp, owner, ..
    } = &steps[0]
    else {
        panic!("{steps:?}");
    };
    assert_eq!(
        at,
        &RunAt {
            session: CHILD.to_string(),
            incarnation: 2
        }
    );
    assert_eq!(harp, "kid");
    assert_eq!(owner, &at_owner());
}

// s4-Q4, s4-Q5: a child a draining daemon handed over (its incarnation
// ended `drained`) is re-hosted as its next incarnation as a lost one is,
// but for the cause `drain`, which its re-host budget never counts; a lost
// one's cause is `rehost`.
#[test]
fn a_child_a_draining_daemon_handed_over_is_rehosted_for_the_cause_drain() {
    let cause_of = |steps: &[Step]| match steps {
        [Step::Rehost { cause, at, .. }] if at.incarnation == 2 => cause.clone(),
        other => panic!("{other:?}"),
    };
    assert_eq!(
        cause_of(&ended_child(crate::ENDED_DRAINED).plan()),
        sessions::DRAINED
    );
    assert_eq!(
        cause_of(&ended_child("orphaned").plan()),
        sessions::REHOSTED
    );
    assert_eq!(crate::ENDED_DRAINED, "drained");
}

fn at_owner() -> SessionRef {
    at(OWNER)
}

// Step 9: each incarnation a child's run ended is told to its open owner
// (`notice:<child>:<incarnation>`, the child's run ended and why: a start that
// failed, a stop, an exit), once; one a re-host continues (its host lost, or
// handed over by a draining daemon) is no end to tell, and a closed owner is
// told nothing.
#[test]
fn each_incarnation_a_child_ended_is_told_to_its_owner() {
    for cause in ["runner-exit", "launch-failed", "stopped"] {
        let steps = ended_child(cause).plan();
        let key = format!("notice:{CHILD}:1");
        let Some(found) = steps.iter().find(|s| s.key() == key) else {
            panic!("{cause}: no notice in {:?}", keys(&steps));
        };
        let notice: SendMessage = command(found);
        assert_eq!(notice.text, format!("child kid ended its run 1: {cause}"));
        assert_eq!(notice.urgency, Urgency::Inform, "a primary owner");
        let Step::Send(sent) = found else {
            unreachable!()
        };
        assert_eq!(sent.book.cover.as_ref().unwrap().root, Some(root(OWNER)));
    }
    for cause in ["orphaned", "drained", "restarted"] {
        let steps = ended_child(cause).plan();
        assert!(
            !keys(&steps).contains(&format!("notice:{CHILD}:1").as_str()),
            "{cause}: {:?}",
            keys(&steps)
        );
    }
    let mut owner_closed = ended_child("runner-exit");
    owner_closed.owner.push(closed());
    assert!(
        !keys(&owner_closed.plan()).contains(&format!("notice:{CHILD}:1").as_str()),
        "a closed owner is told nothing"
    );
}

// One that exited, closed, stopped, never bound a native session, or owes
// its next already is not re-hosted.
#[test]
fn a_child_that_exited_closed_or_owes_its_next_is_not_rehosted() {
    let rehosted = |world: World| keys(&world.plan()).iter().any(|k| k.starts_with("rehost:"));
    assert!(!rehosted(ended_child("runner-exit")), "it exited");
    let mut closed_child = ended_child("orphaned");
    closed_child.child.push(closed());
    assert!(!rehosted(closed_child), "closed");
    // Owner 2026-11-15: no stop latch; a retired stop order keeps no lost
    // child from its re-host.
    let mut stopped = ended_child("orphaned");
    stopped.child.push(SessionEvent::StopOrdered(StopOrdered {
        reason: "x".to_string(),
    }));
    assert!(rehosted(stopped), "a retired stop order");
    let mut unbound = World::with_child();
    unbound.child.push(incarnation_ended(1, "orphaned"));
    assert!(!rehosted(unbound), "no native session");
    let mut owed = ended_child("orphaned");
    owed.child
        .push(SessionEvent::IncarnationOwed(IncarnationOwed {
            incarnation: 2,
            reason: "rehost".to_string(),
        }));
    assert!(!rehosted(owed), "its next is owed already");
    let mut primary = World {
        owner: minted("boss", None),
        ..World::default()
    };
    primary.owner.push(bound());
    primary.owner.push(incarnation_ended(1, "orphaned"));
    assert!(!rehosted(primary), "a primary is never re-hosted");
}

// `session stop` (MVP, owner 2026-11-20): a run asked to stop stays stopped.
// A host lost after the request (the sweep ends the incarnation `orphaned`)
// loses the child no more of its stop than the engine's exit does: it is
// left ended, not re-hosted.
#[test]
fn a_child_whose_run_was_asked_to_stop_is_not_rehosted_when_its_host_is_lost() {
    let mut stopped = ended_child("orphaned");
    stopped.run.insert(
        1,
        RunEvent::StopRequested(runs::StopRequested {
            reason: "stop".to_string(),
        }),
    );
    let steps = stopped.plan();
    assert!(
        !keys(&steps).iter().any(|k| k.starts_with("rehost:")),
        "{steps:?}"
    );
    assert!(
        !keys(&steps).iter().any(|k| k.starts_with("start:")),
        "{steps:?}"
    );
}

// T19: the re-hosted incarnation is started resuming its own conversation;
// a first start resumes nothing.
#[test]
fn a_later_incarnation_is_started_resuming_its_native_session() {
    let first = World::with_child().plan();
    assert!(matches!(&first[0], Step::Start { resume: None, .. }));
    let mut owed = ended_child("orphaned");
    owed.child
        .push(SessionEvent::IncarnationOwed(IncarnationOwed {
            incarnation: 2,
            reason: "rehost".to_string(),
        }));
    owed.run.clear();
    let steps = owed.plan();
    let Some(Step::Start { at, resume, .. }) =
        steps.iter().find(|s| matches!(s, Step::Start { .. }))
    else {
        panic!("{steps:?}");
    };
    assert_eq!(at.incarnation, 2);
    assert_eq!(resume.as_deref(), Some("native-1"));
}

/// The send of `steps` keyed `key`, or a panic naming every key.
fn sent_keyed<'a>(steps: &'a [Step], key: &str) -> &'a crate::Sent {
    match steps.iter().find(|s| s.key() == key) {
        Some(Step::Send(sent)) => sent,
        _ => panic!("no send {key}: {:?}", keys(steps)),
    }
}

// Step 8 U2b: a closed owner closes the child it holds in ANOTHER project
// (the one board holds both, T14), sent to that project's store, and the
// child's project plans nothing of it.
#[test]
fn a_closed_owner_closes_its_child_in_another_project() {
    let mut world = World::with_child().across();
    world.owner.push(closed());
    let steps = world.plan();
    let key = format!("cascade:{OWNER}:{CHILD}");
    let sent = sent_keyed(&steps, &key);
    assert_eq!(sent.project_id, FAR);
    assert_eq!(sent.book.cover.as_ref().unwrap().root, Some(root(CHILD)));
    let close: CloseSession = command(steps.iter().find(|s| s.key() == key).unwrap());
    assert_eq!(close.cause, "owner-closed");
    assert!(
        !keys(&world.plan_for(FAR)).contains(&key.as_str()),
        "planned once, by the owner's project"
    );
}

// Step 8 U2b: a closed child in another project is ended on its owner,
// sent to the owner's project's store, by the child's project's plan alone.
#[test]
fn a_closed_child_in_another_project_is_ended_on_its_owner() {
    let mut world = World::with_child().across();
    world.child.push(closed());
    let key = format!("child-ended:{OWNER}:{CHILD}");
    let steps = world.plan_for(FAR);
    let sent = sent_keyed(&steps, &key);
    assert_eq!(sent.project_id, PROJECT);
    assert_eq!(sent.book.cover.as_ref().unwrap().root, Some(root(OWNER)));
    let end: EndChild = command(steps.iter().find(|s| s.key() == key).unwrap());
    assert_eq!(end.child, Some(far(CHILD)));
    assert!(!keys(&world.plan()).contains(&key.as_str()));
}

// Step 8 U2b: each turn a child in another project ended is told to its
// owner, in the owner's project's store, by the child's project's plan.
#[test]
fn each_turn_a_child_in_another_project_ended_is_told_to_its_owner() {
    let world = World::with_child().across().hosting(vec![
        enqueued(),
        queued(&["m1"], TurnUrgency::Wake),
        RunEvent::TurnCompletedRecorded(TurnCompletedRecorded {
            turn: 1,
            native_key: String::new(),
            answer_ref: String::new(),
            exit_code: 0,
            message_ids: vec!["m1".to_string()],
        }),
    ]);
    let key = format!("notice:{CHILD}:1:1");
    let steps = world.plan_for(FAR);
    let sent = sent_keyed(&steps, &key);
    assert_eq!(sent.project_id, PROJECT);
    assert_eq!(sent.book.cover.as_ref().unwrap().root, Some(root(OWNER)));
    let notice: SendMessage = command(steps.iter().find(|s| s.key() == key).unwrap());
    assert_eq!(notice.text, "child kid finished its turn for m1");
    assert_eq!(notice.urgency, Urgency::Inform);
    assert!(!keys(&world.plan()).contains(&key.as_str()));
}

// Step 8 U2b: a child in another project is minted with no output folder of
// its owner's: its folder is its own project's to say (the daemon reads it
// from that project's config), never the owner's.
#[test]
fn a_child_in_another_project_is_minted_with_its_output_folder_left_to_its_project() {
    let world = World::launched("port it").across();
    let steps = world.plan();
    let Some(Step::Launch { mint, .. }) = steps.first() else {
        panic!("{:?}", keys(&steps));
    };
    assert_eq!(mint.mint.command.project_id, FAR);
    assert_eq!(mint.mint.command.output_dir, "");
}

// ------------------------------------------ a restart owed (step 10e 2a)
//
// The session decided a restart is owed (`RestartOwed`); the plan holds the
// agent's deliveries while it waits for the Active to Idle edge, and owes the
// resume once the incarnation has ended, however it ended.

/// The child's open incarnation 1 owes a restart, the operator's.
fn restart_owed() -> SessionEvent {
    SessionEvent::RestartOwed(sessions::RestartOwed {
        reason: sessions::RestartReason::Operator,
        incarnation: 1,
        note: String::new(),
    })
}

// An incarnation the daemon ended for a restart is re-hosted for the cause
// `restart`, which the re-host budget never counts, as a drain's is not.
#[test]
fn a_child_ended_for_a_restart_is_rehosted_for_the_cause_restart() {
    let mut world = ended_child(crate::ENDED_RESTARTED);
    world.child.insert(world.child.len() - 1, restart_owed());
    let steps = world.plan();
    let [Step::Rehost { cause, at, .. }] = &steps[..] else {
        panic!("{:?}", keys(&steps));
    };
    assert_eq!((cause.as_str(), at.incarnation), (sessions::RESTARTED, 2));
    assert_eq!(crate::ENDED_RESTARTED, "restarted");
}

// The engine exiting on its own with a restart owed does not cancel it: the
// restart proceeds (an engine exit forces Idle).
#[test]
fn a_restart_owed_proceeds_when_the_engine_exited_on_its_own() {
    for cause in ["runner-exit", "orphaned", "stopped"] {
        let mut world = ended_child(cause);
        world.child.insert(world.child.len() - 1, restart_owed());
        // The engine's own exit is told to the owner as any exit is; the
        // restart proceeds beside it.
        let steps = world.plan();
        let rehosts: Vec<&Step> = steps
            .iter()
            .filter(|s| matches!(s, Step::Rehost { .. }))
            .collect();
        assert!(
            matches!(&rehosts[..], [Step::Rehost { cause, .. }] if cause == sessions::RESTARTED),
            "{cause}: {:?}",
            keys(&steps)
        );
    }
}

// What an incarnation's end for a restart is told is nothing: it continues.
#[test]
fn a_restart_is_no_end_to_tell_the_owner() {
    let mut world = ended_child(crate::ENDED_RESTARTED);
    world.child.insert(world.child.len() - 1, restart_owed());
    assert!(
        !keys(&world.plan()).contains(&format!("notice:{CHILD}:1").as_str()),
        "{:?}",
        keys(&world.plan())
    );
}

// A restart closed by the next incarnation owed re-hosts nothing more.
#[test]
fn a_restart_closed_by_the_next_incarnation_is_not_rehosted_again() {
    let mut world = ended_child(crate::ENDED_RESTARTED);
    world.child.insert(world.child.len() - 1, restart_owed());
    world
        .child
        .push(SessionEvent::IncarnationOwed(IncarnationOwed {
            incarnation: 2,
            reason: "restart".to_string(),
        }));
    world.run.clear();
    let steps = world.plan();
    assert!(
        !keys(&steps).iter().any(|k| k.starts_with("rehost:")),
        "{:?}",
        keys(&steps)
    );
    assert!(steps.iter().any(|s| matches!(
        s,
        Step::Start {
            resume: Some(_),
            ..
        }
    )));
}

// While a restart waits for the turn in flight: a steer still reaches the
// turn, a wake and an inform stay pending (they are delivered into the
// resumed session).
#[test]
fn a_restart_waiting_for_the_turn_lets_a_steer_through_and_holds_the_rest() {
    let mut world =
        World::with_child().hosting(vec![enqueued(), queued(&["m1"], TurnUrgency::Wake)]);
    world.child.push(bound());
    world.child.push(sent("m1", Urgency::Wake, "one"));
    world.child.push(restart_owed());
    world.done.insert(format!("feed:{}:1", run_text(CHILD, 1)));
    world.child.push(sent("m2", Urgency::Wake, "two"));
    world.child.push(sent("m3", Urgency::Inform, "fyi"));
    assert!(world.plan().is_empty(), "{:?}", keys(&world.plan()));
    world.child.push(sent("m4", Urgency::Steer, "stop"));
    assert_eq!(keys(&world.plan()), ["send:m4"]);
}

// Idle with a restart owed, a wake is not given a turn: the restart is at
// hand, and the wake goes to the resumed session. Without the restart it is.
#[test]
fn an_idle_agent_with_a_restart_owed_is_given_no_turn() {
    let mut world = World::with_child().hosting(vec![enqueued()]);
    world.child.push(bound());
    world.child.push(sent("m1", Urgency::Wake, "go"));
    assert_eq!(keys(&world.plan()), ["send:m1"]);
    world.child.push(restart_owed());
    assert!(world.plan().is_empty(), "{:?}", keys(&world.plan()));
}
