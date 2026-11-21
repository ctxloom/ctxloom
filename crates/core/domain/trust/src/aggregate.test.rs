//! A trust through the kernel (`Harness`): granted on a matching etag,
//! refused stale on another, once only; revoked once only.

use angzarr_embedded::testing::{Harness, Samples};
use angzarr_embedded::v1::Cover;
use angzarr_embedded::{Failure, Registry};

use crate::{
    trust_root, Actor, GrantTrust, RevokeTrust, TrustAggregate, TrustCommand, TrustEvent,
    TrustGranted, TrustRevoked, TRUST_DOMAIN,
};

const NAME: &str = "projects/amber-quiet-heron/trust";

fn trust() -> Harness<TrustAggregate> {
    let mut registry = Registry::new();
    registry
        .aggregate::<TrustAggregate>(TrustAggregate::default)
        .unwrap();
    let cover = Cover {
        domain: TRUST_DOMAIN.to_string(),
        root: Some(trust_root(NAME)),
        edition: None,
    };
    Harness::new(registry, cover).with_samples(Samples::starting_at(100))
}

fn alice() -> Actor {
    Actor {
        user: "alice".to_string(),
    }
}

fn grant(etag: &str, current: &str) -> GrantTrust {
    GrantTrust {
        name: NAME.to_string(),
        project_id: "p-1".to_string(),
        etag: etag.to_string(),
        current_etag: current.to_string(),
        actor: alice(),
    }
}

fn revoke() -> RevokeTrust {
    RevokeTrust {
        name: NAME.to_string(),
        project_id: "p-1".to_string(),
        actor: alice(),
    }
}

fn run(
    harness: &mut Harness<TrustAggregate>,
    command: impl Into<TrustCommand>,
) -> Result<Vec<TrustEvent>, String> {
    match harness.when(command).result() {
        Ok(events) => Ok(events),
        Err(Failure::Rejected(rejected)) => {
            let typed = rejected
                .rejection::<angzarr_embedded::markup::TypedRefusal>()
                .expect("a typed refusal");
            let rejection = crate::TrustRejection::try_from(typed).expect("a trust refusal");
            Err(angzarr_diagnostic::Diagnostic::code(&rejection).to_string())
        }
        Err(other) => panic!("not a rejection: {other:?}"),
    }
}

fn granted() -> TrustEvent {
    TrustEvent::TrustGranted(TrustGranted {
        name: NAME.to_string(),
        project_id: "p-1".to_string(),
        actor: alice(),
    })
}

#[test]
fn a_grant_naming_what_is_there_now_trusts_the_project_with_its_actor() {
    let mut harness = trust();
    assert_eq!(run(&mut harness, grant("e-1", "e-1")), Ok(vec![granted()]));
    let state = harness.state().state().clone();
    assert!(state.trusted);
    assert_eq!(state.user, "alice");
    assert_eq!(state.name, NAME);
    assert_eq!(state.project_id, "p-1");
}

#[test]
fn a_grant_whose_preview_is_not_what_is_there_now_is_refused_stale_and_records_nothing() {
    let mut harness = trust();
    assert_eq!(
        run(&mut harness, grant("e-1", "e-2")),
        Err("trust-plan-stale".to_string())
    );
    assert!(!harness.state().state().trusted);
}

#[test]
fn a_stale_grant_of_a_trusted_project_is_refused_too() {
    let mut harness = trust();
    run(&mut harness, grant("e-1", "e-1")).unwrap();
    assert_eq!(
        run(&mut harness, grant("e-1", "e-2")),
        Err("trust-plan-stale".to_string())
    );
}

#[test]
fn trusting_what_is_trusted_records_nothing_more() {
    let mut harness = trust();
    run(&mut harness, grant("e-1", "e-1")).unwrap();
    assert_eq!(run(&mut harness, grant("e-2", "e-2")), Ok(vec![]));
}

#[test]
fn a_revoke_takes_trust_back_once_and_a_second_records_nothing() {
    let mut harness = trust();
    run(&mut harness, grant("e-1", "e-1")).unwrap();
    assert_eq!(
        run(&mut harness, revoke()),
        Ok(vec![TrustEvent::TrustRevoked(TrustRevoked {
            name: NAME.to_string(),
            project_id: "p-1".to_string(),
            actor: alice(),
        })])
    );
    let state = harness.state().state().clone();
    assert!(!state.trusted);
    assert_eq!(state.user, "alice");
    assert_eq!(run(&mut harness, revoke()), Ok(vec![]));
}

#[test]
fn revoking_what_was_never_trusted_records_nothing() {
    let mut harness = trust();
    assert_eq!(run(&mut harness, revoke()), Ok(vec![]));
}

#[test]
fn a_project_trusted_again_after_a_revoke_is_trusted() {
    let mut harness = trust();
    run(&mut harness, grant("e-1", "e-1")).unwrap();
    run(&mut harness, revoke()).unwrap();
    assert_eq!(
        run(&mut harness, grant("e-2", "e-2")).map(|e| e.len()),
        Ok(1)
    );
    assert!(harness.state().state().trusted);
}
