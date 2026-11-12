//! The daemon's gate: the user bearer, and nothing else.

use angzarr_coordinator::{Arrival, BodyKind, Credential, Gate};
use angzarr_embedded::v1::{Cover, Uuid};
use angzarr_embedded::{Domain, Payload};
use run_ports::{Bearer, Token};

use crate::gate::url;
use crate::user_gate::{user_allowlist, User, UserGate};

fn bearer() -> Token {
    Token::from_bytes([42; 32])
}

fn presented(text: &str) -> Credential {
    Credential::bearer(text.as_bytes())
}

#[tokio::test]
async fn the_minted_bearer_is_the_user() {
    let gate = UserGate::new(&bearer());
    let principal = gate
        .identify(Arrival::Loopback, Some(&presented(&bearer().to_text())))
        .await
        .expect("the user");
    assert_eq!(*principal.identity(), User);
    assert_eq!(*principal.allowlist(), user_allowlist());
}

// S11: whatever is wrong, the answer is the same bare refusal.
#[tokio::test]
async fn anything_but_the_minted_bearer_is_a_bare_unauthenticated() {
    let gate = UserGate::new(&bearer());
    let other = Token::from_bytes([7; 32]);
    let run = "5f0c7a52-1111-5111-8111-000000000001";
    let cases: Vec<(&str, Option<Credential>)> = vec![
        ("no credential", None),
        ("another bearer", Some(presented(&other.to_text()))),
        (
            "a run's bearer of the same secret",
            Some(presented(Bearer::compose(run, &bearer()).expose())),
        ),
        (
            "upper case",
            Some(presented(&bearer().to_text().to_uppercase())),
        ),
        ("not hex", Some(presented("garbage"))),
        ("empty", Some(presented(""))),
        (
            "another scheme",
            Some(Credential::peer_certificate(
                bearer().to_text().into_bytes(),
            )),
        ),
    ];
    for (what, credential) in cases {
        let refused = gate.identify(Arrival::Loopback, credential.as_ref()).await;
        assert!(refused.is_err(), "{what}");
    }
}

// Authenticated means the user: every command and fact the session domain
// handles, on any of its streams.
#[test]
fn the_user_may_send_everything_the_session_domain_handles_to_any_session() {
    let list = user_allowlist();
    let commands = <sessions::SessionAggregate as Domain>::Command::type_urls();
    let facts = <sessions::SessionAggregate as Domain>::Fact::type_urls();
    assert!(commands.len() > 5 && facts.len() > 5);
    for type_url in &commands {
        assert!(
            list.permits_body(BodyKind::Command, type_url).is_ok(),
            "{type_url}"
        );
    }
    for type_url in &facts {
        assert!(
            list.permits_body(BodyKind::Fact, type_url).is_ok(),
            "{type_url}"
        );
    }
    for type_url in [
        url::<sessions::RecordTurnEnded>(),
        url::<sessions::RecordPromptSubmitted>(),
        url::<sessions::BindEngineSession>(),
    ] {
        assert!(
            list.permits_body(BodyKind::Fact, &type_url).is_ok(),
            "{type_url}"
        );
    }
    for type_url in [
        url::<sessions::SendMessage>(),
        url::<sessions::LaunchChild>(),
    ] {
        assert!(
            list.permits_body(BodyKind::Command, &type_url).is_ok(),
            "{type_url}"
        );
    }
    let gate = UserGate::new(&bearer());
    let any = Cover {
        domain: sessions::SESSION_DOMAIN.to_string(),
        root: Some(Uuid::from_bytes([9; 16])),
        edition: None,
    };
    assert!(gate.owns(&User, &any));
}

// The gate keeps the digest alone: nothing it renders is the bearer.
#[test]
fn the_gate_shows_no_bearer() {
    let shown = format!("{:?}", UserGate::new(&bearer()));
    assert!(!shown.contains(&bearer().to_text()), "{shown}");
}
