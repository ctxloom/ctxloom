//! The command half over the fixtures' kernel, behind the hook gate: the
//! coordinator's own order (identify, allowlist, reach, owns, serve), the
//! kernel's own dedupe, and how a failure is answered.

use std::sync::Arc;

use angzarr_coordinator::angzarr_coordinator_base::{Code, Refusal};
use angzarr_coordinator::{Commands, Coordinator, Credential, Served};
use angzarr_embedded::v1::{Cover, DeliveryOutcome, Uuid};
use run_ports::{Bearer, Edge, Token};

use crate::fakes::{command_book, fact_book, KernelEdge, World};
use crate::{EdgeCommands, EdgeFailure, HookGate};

type Hooks = Coordinator<EdgeCommands<KernelEdge>, (), HookGate<KernelEdge>>;

fn coordinator(world: &World) -> Hooks {
    Coordinator::new(
        EdgeCommands::new(Arc::clone(&world.edge)),
        (),
        HookGate::new(Arc::clone(&world.edge)),
    )
}

fn credential(run: &Uuid, token: &Token) -> Credential {
    let run = uuid::Uuid::from_slice(&run.value).unwrap().to_string();
    Credential::bearer(Bearer::compose(&run, token).expose().as_bytes())
}

fn mine(world: &World) -> Credential {
    credential(&world.me.identity.run, &world.me.hook_token)
}

fn session_cover(session: &Uuid) -> Cover {
    Cover {
        domain: sessions::SESSION_DOMAIN.to_string(),
        root: Some(session.clone()),
        edition: None,
    }
}

fn turn_ended(cover: Cover, external_id: &str) -> angzarr_embedded::v1::CommandBook {
    fact_book(
        cover,
        sessions::RecordTurnEnded {
            prompt_id: "p1".to_string(),
        },
        external_id,
    )
}

async fn pages(world: &World, cover: &Cover) -> usize {
    world.edge.events(cover, 0).await.unwrap().len()
}

#[tokio::test]
async fn a_capture_fact_on_the_callers_session_is_applied_to_the_stream() {
    let world = World::new().await;
    let coordinator = coordinator(&world);
    let cover = session_cover(&world.me.identity.session);
    let before = pages(&world, &cover).await;
    let credential = mine(&world);
    let session = coordinator
        .open(angzarr_coordinator::Arrival::Loopback, Some(&credential))
        .await
        .unwrap();
    let answered = session
        .handle(turn_ended(cover.clone(), "hook:turn-end:s:p1"))
        .await
        .unwrap();
    assert_eq!(answered.delivery, DeliveryOutcome::Applied);
    assert_eq!(pages(&world, &cover).await, before + 1);
}

#[tokio::test]
async fn the_same_origin_is_a_duplicate_and_writes_no_page() {
    let world = World::new().await;
    let coordinator = coordinator(&world);
    let cover = session_cover(&world.me.identity.session);
    let credential = mine(&world);
    let session = coordinator
        .open(angzarr_coordinator::Arrival::Loopback, Some(&credential))
        .await
        .unwrap();
    session
        .handle(turn_ended(cover.clone(), "hook:turn-end:s:p1"))
        .await
        .unwrap();
    let after_first = pages(&world, &cover).await;
    let again = session
        .handle(turn_ended(cover.clone(), "hook:turn-end:s:p1"))
        .await
        .unwrap();
    assert_eq!(again.delivery, DeliveryOutcome::Duplicate);
    assert_eq!(pages(&world, &cover).await, after_first);
}

#[tokio::test]
async fn what_the_gate_forbids_is_answered_as_an_unknown_domain_and_writes_nothing() {
    let world = World::new().await;
    let coordinator = coordinator(&world);
    let credential = mine(&world);
    let session = coordinator
        .open(angzarr_coordinator::Arrival::Loopback, Some(&credential))
        .await
        .unwrap();
    let mine_cover = session_cover(&world.me.identity.session);
    let theirs = session_cover(&world.other.identity.session);
    let served = world.edge.served();
    let before = pages(&world, &theirs).await;
    let hidden = |domain: &str| coordinator.commands().unknown_domain(domain).coded();
    // Another session's stream.
    let other = session
        .handle(turn_ended(theirs.clone(), "hook:turn-end:o:p1"))
        .await
        .unwrap_err();
    assert_eq!(other.coded(), hidden(sessions::SESSION_DOMAIN));
    // A command body, of a type a fact could have been.
    let command = session
        .handle(command_book(
            mine_cover.clone(),
            sessions::RecordTurnEnded {
                prompt_id: "p".to_string(),
            },
        ))
        .await
        .unwrap_err();
    assert_eq!(command.coded(), hidden(sessions::SESSION_DOMAIN));
    // A runner-lifecycle fact, on the run's own stream.
    let lifecycle = session
        .handle(fact_book(
            world.me.identity.run_cover(),
            runs::RunExited { code: 0, signal: 0 },
            "exit:forged",
        ))
        .await
        .unwrap_err();
    assert_eq!(lifecycle.coded(), hidden(runs::RUN_DOMAIN));
    // A runner-lifecycle fact on the session stream.
    let bound = session
        .handle(fact_book(
            mine_cover.clone(),
            sessions::BindEngineSession {
                native_id: "forged".to_string(),
                transcript_path: "/t".to_string(),
                engine_version: None,
            },
            "bind:forged",
        ))
        .await
        .unwrap_err();
    assert_eq!(bound.coded(), hidden(sessions::SESSION_DOMAIN));
    assert_eq!(world.edge.served(), served, "nothing reached the kernel");
    assert_eq!(pages(&world, &theirs).await, before);
}

#[tokio::test]
async fn a_run_that_has_ended_is_unauthenticated_on_its_very_next_request() {
    let world = World::new().await;
    let coordinator = coordinator(&world);
    let cover = session_cover(&world.me.identity.session);
    let credential = mine(&world);
    let session = coordinator
        .open(angzarr_coordinator::Arrival::Loopback, Some(&credential))
        .await
        .unwrap();
    session
        .handle(turn_ended(cover.clone(), "hook:turn-end:s:p1"))
        .await
        .unwrap();
    world.end(&world.me).await;
    let refused = session
        .handle(turn_ended(cover, "hook:turn-end:s:p2"))
        .await
        .unwrap_err();
    assert_eq!(refused.coded().code, Code::Unauthenticated);
}

#[tokio::test]
async fn a_fact_the_kernel_parks_is_answered_with_its_spec_key() {
    let world = World::new().await;
    let coordinator = coordinator(&world);
    let cover = session_cover(&world.me.identity.session);
    let credential = mine(&world);
    let session = coordinator
        .open(angzarr_coordinator::Arrival::Loopback, Some(&credential))
        .await
        .unwrap();
    world.edge.lose_every_race();
    let parked = session
        .handle(turn_ended(cover, "hook:turn-end:s:p1"))
        .await
        .unwrap_err();
    let coded = parked.coded();
    assert_eq!(coded.key, "ANGZARR_REASON_FACT_DEAD_LETTERED");
    assert_eq!(coded.code, Code::Internal);
}

#[tokio::test]
async fn an_unknown_domain_is_answered_the_way_the_kernel_answers_a_malformed_book() {
    let world = World::new().await;
    let commands = EdgeCommands::new(Arc::clone(&world.edge));
    let coded = commands.unknown_domain("nowhere").coded();
    assert_eq!(coded.code, Code::InvalidArgument);
    assert!(coded.key.contains("unknown-domain"), "{}", coded.key);
    assert_eq!(
        coded.args.get("domain").map(String::as_str),
        Some("nowhere")
    );
}

#[tokio::test]
async fn only_the_session_domain_is_reached_and_on_this_machine() {
    let world = World::new().await;
    let commands = EdgeCommands::new(Arc::clone(&world.edge));
    use angzarr_coordinator::Reach;
    assert_eq!(commands.reach(sessions::SESSION_DOMAIN), Reach::Loopback);
    assert_eq!(commands.reach(runs::RUN_DOMAIN), Reach::None);
    assert_eq!(commands.reach(trees::TREE_DOMAIN), Reach::None);
}

#[test]
fn an_edge_failure_made_of_a_coded_answers_that_coded() {
    let coded = angzarr_coordinator::angzarr_coordinator_base::Coded::new(
        Code::Unimplemented,
        "k",
        [("a".to_string(), "b".to_string())],
    );
    assert_eq!(EdgeFailure::from(coded.clone()).coded(), coded);
}
