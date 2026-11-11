//! The daemon's gate (D25, S-f) over the real aggregates: the orchestration
//! scope, the streams it reaches, and the requester it stamps.

use std::sync::Arc;

use angzarr_coordinator::{BodyKind, Credential, Gate};
use angzarr_embedded::markup::DomainMessage;
use angzarr_embedded::v1::{command_page, Cover, Uuid};
use run_ports::{Bearer, Edge, Token};
use sessions::{
    ClearChild, CloseSession, LaunchChild, SendMessage, SessionRef, TargetTrust, Urgency,
};

use crate::fakes::{command_book, fact_book, uuid, World, SESSION_ME};
use crate::gate::url;
use crate::orchestrate_gate::{orchestration_scope, stamp_book, OrchestratorGate};

const ARRIVAL: angzarr_coordinator::Arrival = angzarr_coordinator::Arrival::Loopback;
const CHILD: &str = "5f0c7a52-4444-5444-8444-000000000004";

fn run_text(run: &Uuid) -> String {
    uuid::Uuid::from_slice(&run.value)
        .unwrap()
        .hyphenated()
        .to_string()
}

fn bearer(run: &Uuid, token: &Token) -> Credential {
    Credential::bearer(Bearer::compose(&run_text(run), token).expose().as_bytes())
}

fn cover(session: &Uuid) -> Cover {
    Cover {
        domain: sessions::SESSION_DOMAIN.to_string(),
        root: Some(session.clone()),
        edition: None,
    }
}

fn me() -> SessionRef {
    SessionRef {
        project_id: "calm-otter".to_string(),
        session: SESSION_ME.to_string(),
    }
}

fn forged() -> Option<SessionRef> {
    Some(SessionRef {
        project_id: "calm-otter".to_string(),
        session: "someone-else".to_string(),
    })
}

/// `me` launches a child (as the user), so it holds one.
async fn with_child() -> World {
    let world = World::new().await;
    world
        .edge
        .handle(command_book(
            cover(&world.me.identity.session),
            LaunchChild {
                child_project_id: "calm-otter".to_string(),
                child_session: CHILD.to_string(),
                child_root: "/work/proj".to_string(),
                harp: "kid".to_string(),
                engine: "claude-code".to_string(),
                trust: TargetTrust {
                    same_project: true,
                    trusted: true,
                },
                delegation_limit: 4,
                requester: None,
                prompt: String::new(),
            },
        ))
        .await
        .unwrap();
    world
}

#[tokio::test]
async fn the_owners_hook_bearer_identifies_it_with_its_children() {
    let world = with_child().await;
    let gate = OrchestratorGate::new(Arc::clone(&world.edge));
    let principal = gate
        .identify(
            ARRIVAL,
            Some(&bearer(&world.me.identity.run, &world.me.hook_token)),
        )
        .await
        .unwrap();
    let identity = principal.identity();
    assert_eq!(identity.hook.run, world.me.identity.run);
    assert_eq!(identity.requester, me());
    assert_eq!(identity.children, vec![uuid(CHILD)]);
    assert!(gate.still_valid(identity).await);
    world.end(&world.me).await;
    assert!(!gate.still_valid(identity).await, "it dies with its run");
}

#[tokio::test]
async fn a_bearer_the_hook_gate_refuses_is_refused() {
    let world = World::new().await;
    let gate = OrchestratorGate::new(Arc::clone(&world.edge));
    let wrong = bearer(&world.me.identity.run, &world.me.token);
    assert!(gate.identify(ARRIVAL, Some(&wrong)).await.is_err());
    assert!(gate.identify(ARRIVAL, None).await.is_err());
}

#[tokio::test]
async fn the_scope_is_the_capture_facts_and_the_four_orchestration_commands() {
    let world = World::new().await;
    let gate = OrchestratorGate::new(Arc::clone(&world.edge));
    let principal = gate
        .identify(
            ARRIVAL,
            Some(&bearer(&world.me.identity.run, &world.me.hook_token)),
        )
        .await
        .unwrap();
    let list = principal.allowlist();
    for command in [
        url::<SendMessage>(),
        url::<CloseSession>(),
        url::<ClearChild>(),
        url::<LaunchChild>(),
    ] {
        assert!(
            list.permits_body(BodyKind::Command, &command).is_ok(),
            "{command}"
        );
        assert!(
            list.permits_body(BodyKind::Fact, &command).is_err(),
            "{command}"
        );
    }
    assert!(list
        .permits_body(BodyKind::Fact, &url::<sessions::RecordTurnEnded>())
        .is_ok());
    for refused in [
        url::<sessions::MintSession>(),
        url::<sessions::StopSession>(),
        url::<runs::EnqueueRun>(),
        url::<runs::QueueTurn>(),
    ] {
        assert!(
            list.permits_body(BodyKind::Command, &refused).is_err(),
            "{refused}"
        );
    }
    assert!(list
        .permits_body(BodyKind::Fact, &url::<runs::TurnCompleted>())
        .is_err());
    assert_eq!(list, &orchestration_scope().allowlist());
}

#[tokio::test]
async fn it_reaches_its_own_stream_and_its_childrens_and_no_other() {
    let world = with_child().await;
    let gate = OrchestratorGate::new(Arc::clone(&world.edge));
    let principal = gate
        .identify(
            ARRIVAL,
            Some(&bearer(&world.me.identity.run, &world.me.hook_token)),
        )
        .await
        .unwrap();
    let identity = principal.identity();
    assert!(gate.owns(identity, &cover(&world.me.identity.session)));
    assert!(gate.owns(identity, &cover(&uuid(CHILD))));
    assert!(!gate.owns(identity, &cover(&world.other.identity.session)));
    assert!(!gate.owns(identity, &world.me.identity.run_cover()));
    let mut edition = cover(&uuid(CHILD));
    edition.edition = Some(Default::default());
    assert!(!gate.owns(identity, &edition));
    let mut rootless = cover(&uuid(CHILD));
    rootless.root = None;
    assert!(!gate.owns(identity, &rootless));
    let mut domain = cover(&uuid(CHILD));
    domain.domain = runs::RUN_DOMAIN.to_string();
    assert!(!gate.owns(identity, &domain));
}

/// The command a book holds, decoded as `M`.
fn held<M: DomainMessage>(book: &angzarr_embedded::v1::CommandBook) -> M {
    let Some(command_page::Body::Command(command)) =
        book.page.as_ref().and_then(|p| p.body.as_ref())
    else {
        panic!("no command");
    };
    M::from_wire(
        command
            .payload
            .as_ref()
            .unwrap()
            .to_msg::<M::Wire>()
            .unwrap(),
    )
    .unwrap()
}

// S-f: the requester is the identity's, whatever the payload said.
#[tokio::test]
async fn it_stamps_the_identitys_session_over_a_forged_requester() {
    let world = with_child().await;
    let gate = OrchestratorGate::new(Arc::clone(&world.edge));
    let principal = gate
        .identify(
            ARRIVAL,
            Some(&bearer(&world.me.identity.run, &world.me.hook_token)),
        )
        .await
        .unwrap();
    let send = command_book(
        cover(&uuid(CHILD)),
        SendMessage {
            message_id: "m1".to_string(),
            urgency: Urgency::Wake,
            text: "go".to_string(),
            requester: forged(),
            supported: vec![Urgency::Wake],
        },
    );
    let stamped = gate.stamp(principal.identity(), send);
    let send: SendMessage = held(&stamped);
    assert_eq!(send.requester, Some(me()));
    assert_eq!(send.text, "go");
    let close: CloseSession = held(&stamp_book(
        command_book(
            cover(&uuid(CHILD)),
            CloseSession {
                cause: "done".to_string(),
                requester: forged(),
            },
        ),
        &me(),
    ));
    assert_eq!(close.requester, Some(me()));
    let clear: ClearChild = held(&stamp_book(
        command_book(
            cover(&uuid(CHILD)),
            ClearChild {
                clear_id: "c".to_string(),
                requester: forged(),
            },
        ),
        &me(),
    ));
    assert_eq!(clear.requester, Some(me()));
    let launch: LaunchChild = held(&stamp_book(
        command_book(
            cover(&world.me.identity.session),
            LaunchChild {
                child_project_id: String::new(),
                child_session: String::new(),
                child_root: String::new(),
                harp: String::new(),
                engine: String::new(),
                trust: TargetTrust {
                    same_project: true,
                    trusted: true,
                },
                delegation_limit: 1,
                requester: forged(),
                prompt: String::new(),
            },
        ),
        &me(),
    ));
    assert_eq!(launch.requester, Some(me()));
}

#[test]
fn a_book_with_no_guarded_command_is_left_as_it_was() {
    let fact = fact_book(
        cover(&uuid(CHILD)),
        sessions::RecordTurnEnded {
            prompt_id: "p".to_string(),
        },
        "x",
    );
    assert_eq!(stamp_book(fact.clone(), &me()), fact);
    let keep = command_book(cover(&uuid(CHILD)), sessions::SetKept { kept: true });
    assert_eq!(stamp_book(keep.clone(), &me()), keep);
    let empty = angzarr_embedded::v1::CommandBook::default();
    assert_eq!(stamp_book(empty.clone(), &me()), empty);
}
