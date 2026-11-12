//! The hook gate's rules over the real aggregates (the fixtures' kernel).

use std::sync::Arc;

use angzarr_coordinator::{Credential, Gate};
use angzarr_embedded::v1::{Cover, Uuid};
use run_ports::{Bearer, Token};

use crate::fakes::World;
use crate::hook_gate::{HookGate, HookIdentity};
use crate::user_gate::user_allowlist;

const ARRIVAL: angzarr_coordinator::Arrival = angzarr_coordinator::Arrival::Loopback;

fn run_text(run: &Uuid) -> String {
    uuid::Uuid::from_slice(&run.value)
        .unwrap()
        .hyphenated()
        .to_string()
}

fn bearer(run: &Uuid, token: &Token) -> Credential {
    Credential::bearer(Bearer::compose(&run_text(run), token).expose().as_bytes())
}

fn gate(world: &World) -> HookGate<crate::fakes::KernelEdge> {
    HookGate::new(Arc::clone(&world.edge))
}

fn session_cover(session: &Uuid) -> Cover {
    Cover {
        domain: sessions::SESSION_DOMAIN.to_string(),
        root: Some(session.clone()),
        edition: None,
    }
}

#[tokio::test]
async fn a_live_runs_hook_bearer_identifies_its_run_and_session() {
    let world = World::new().await;
    let credential = bearer(&world.me.identity.run, &world.me.hook_token);
    let principal = gate(&world)
        .identify(ARRIVAL, Some(&credential))
        .await
        .unwrap();
    assert_eq!(
        *principal.identity(),
        HookIdentity {
            run: world.me.identity.run.clone(),
            session: world.me.identity.session.clone(),
        }
    );
}

// One principal: whoever presents the run's hook bearer is the user, and
// may send what she may.
#[tokio::test]
async fn the_hook_bearer_is_the_user_with_her_allowlist() {
    let world = World::new().await;
    let credential = bearer(&world.me.identity.run, &world.me.hook_token);
    let principal = gate(&world)
        .identify(ARRIVAL, Some(&credential))
        .await
        .unwrap();
    assert_eq!(principal.allowlist(), &user_allowlist());
}

#[tokio::test]
async fn a_wrong_secret_does_not_identify() {
    let world = World::new().await;
    let credential = bearer(&world.me.identity.run, &Token::from_bytes([0xee; 32]));
    assert!(gate(&world)
        .identify(ARRIVAL, Some(&credential))
        .await
        .is_err());
}

#[tokio::test]
async fn the_runners_and_the_models_tokens_are_not_the_hooks() {
    let world = World::new().await;
    let gate = gate(&world);
    for token in [&world.me.token, &world.me.model_token] {
        let credential = bearer(&world.me.identity.run, token);
        assert!(gate.identify(ARRIVAL, Some(&credential)).await.is_err());
    }
}

#[tokio::test]
async fn another_runs_hook_secret_does_not_identify_for_this_run() {
    let world = World::new().await;
    let credential = bearer(&world.me.identity.run, &world.other.hook_token);
    assert!(gate(&world)
        .identify(ARRIVAL, Some(&credential))
        .await
        .is_err());
}

#[tokio::test]
async fn an_unknown_run_does_not_identify() {
    let world = World::new().await;
    let unknown = Uuid::from_bytes([9; 16]);
    let credential = bearer(&unknown, &world.me.hook_token);
    assert!(gate(&world)
        .identify(ARRIVAL, Some(&credential))
        .await
        .is_err());
}

#[tokio::test]
async fn a_malformed_or_absent_bearer_does_not_identify() {
    let world = World::new().await;
    let gate = gate(&world);
    let good = Bearer::compose(&run_text(&world.me.identity.run), &world.me.hook_token);
    let secret = good.expose().to_string();
    let hex_only = secret.rsplit_once('.').unwrap().1.to_string();
    let not_a_uuid = format!("not-a-uuid.{hex_only}");
    let short = format!("{}.abcd", run_text(&world.me.identity.run));
    for text in [hex_only.as_str(), "", not_a_uuid.as_str(), short.as_str()] {
        let credential = Credential::bearer(text.as_bytes());
        assert!(gate.identify(ARRIVAL, Some(&credential)).await.is_err());
    }
    let not_utf8 = Credential::bearer(vec![0xff, 0xfe, 0x2e]);
    assert!(gate.identify(ARRIVAL, Some(&not_utf8)).await.is_err());
    let certificate = Credential::peer_certificate(secret.as_bytes());
    assert!(gate.identify(ARRIVAL, Some(&certificate)).await.is_err());
    assert!(gate.identify(ARRIVAL, None).await.is_err());
}

#[tokio::test]
async fn an_ended_run_no_longer_identifies_and_a_held_identity_is_no_longer_valid() {
    let world = World::new().await;
    let gate = gate(&world);
    let credential = bearer(&world.me.identity.run, &world.me.hook_token);
    let principal = gate.identify(ARRIVAL, Some(&credential)).await.unwrap();
    assert!(gate.still_valid(principal.identity()).await);
    world.end(&world.me).await;
    assert!(!gate.still_valid(principal.identity()).await);
    assert!(gate.identify(ARRIVAL, Some(&credential)).await.is_err());
    // The other session's run is untouched.
    let other = bearer(&world.other.identity.run, &world.other.hook_token);
    assert!(gate.identify(ARRIVAL, Some(&other)).await.is_ok());
}

#[tokio::test]
async fn a_run_the_store_does_not_hold_is_not_valid() {
    let world = World::new().await;
    let gone = HookIdentity {
        run: Uuid::from_bytes([7; 16]),
        session: Uuid::from_bytes([8; 16]),
    };
    assert!(!gate(&world).still_valid(&gone).await);
}

#[tokio::test]
async fn the_hook_owns_its_own_sessions_stream_and_no_other() {
    let world = World::new().await;
    let gate = gate(&world);
    let identity = HookIdentity {
        run: world.me.identity.run.clone(),
        session: world.me.identity.session.clone(),
    };
    assert!(gate.owns(&identity, &session_cover(&world.me.identity.session)));
    // Another session's stream.
    assert!(!gate.owns(&identity, &session_cover(&world.other.identity.session)));
    // Its own run's stream is not its session's.
    assert!(!gate.owns(&identity, &world.me.identity.run_cover()));
    // The session's root under another domain.
    let mut cover = session_cover(&world.me.identity.session);
    cover.domain = runs::RUN_DOMAIN.to_string();
    assert!(!gate.owns(&identity, &cover));
    // The session's stream in an edition other than the main timeline.
    let mut cover = session_cover(&world.me.identity.session);
    cover.edition = Some(Default::default());
    assert!(!gate.owns(&identity, &cover));
    // No root at all.
    let mut cover = session_cover(&world.me.identity.session);
    cover.root = None;
    assert!(!gate.owns(&identity, &cover));
}

// Owner additions (1): each run has its own hook token, and one run's token
// is nothing on another run's stream, whichever run stays live.
#[test]
fn two_runs_hook_tokens_are_distinct_from_each_other_and_from_the_runs_other_tokens() {
    let world = tokio::runtime::Builder::new_current_thread()
        .build()
        .unwrap()
        .block_on(World::new());
    let all = [
        world.me.hook_token.to_text(),
        world.other.hook_token.to_text(),
        world.me.token.to_text(),
        world.other.token.to_text(),
        world.me.model_token.to_text(),
        world.other.model_token.to_text(),
    ];
    let distinct: std::collections::BTreeSet<_> = all.iter().collect();
    assert_eq!(distinct.len(), all.len());
}

#[tokio::test]
async fn run_as_token_names_run_as_session_stream_only() {
    let world = World::new().await;
    let gate = gate(&world);
    let a = gate
        .identify(
            ARRIVAL,
            Some(&bearer(&world.me.identity.run, &world.me.hook_token)),
        )
        .await
        .unwrap();
    let b = gate
        .identify(
            ARRIVAL,
            Some(&bearer(&world.other.identity.run, &world.other.hook_token)),
        )
        .await
        .unwrap();
    assert!(gate.owns(a.identity(), &session_cover(&world.me.identity.session)));
    assert!(!gate.owns(a.identity(), &session_cover(&world.other.identity.session)));
    assert!(gate.owns(b.identity(), &session_cover(&world.other.identity.session)));
    assert!(!gate.owns(b.identity(), &session_cover(&world.me.identity.session)));
}

#[tokio::test]
async fn a_token_dies_with_its_run_while_the_other_run_stays_live_in_both_orders() {
    for ended_first_is_me in [true, false] {
        let world = World::new().await;
        let gate = gate(&world);
        let (ended, live) = match ended_first_is_me {
            true => (&world.me, &world.other),
            false => (&world.other, &world.me),
        };
        let dead = bearer(&ended.identity.run, &ended.hook_token);
        let alive = bearer(&live.identity.run, &live.hook_token);
        let held = gate.identify(ARRIVAL, Some(&dead)).await.unwrap();
        world.end(ended).await;
        assert!(gate.identify(ARRIVAL, Some(&dead)).await.is_err());
        assert!(!gate.still_valid(held.identity()).await);
        let kept = gate.identify(ARRIVAL, Some(&alive)).await.unwrap();
        assert!(gate.still_valid(kept.identity()).await);
    }
}
