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

// U4: the user may stop a run (a hosted one-shot's command, on a signal),
// and send the runs domain nothing else.
#[test]
fn the_user_may_ask_a_run_to_stop_and_nothing_else_of_the_runs_domain() {
    let list = user_allowlist();
    assert!(list
        .permits_body(BodyKind::Command, &url::<runs::RequestStop>())
        .is_ok());
    assert!(list
        .permits_body(BodyKind::Command, &url::<runs::QueueTurn>())
        .is_err());
    assert!(list
        .permits_body(BodyKind::Fact, &url::<runs::RequestStop>())
        .is_err());
}

// U5: a frontend reports its terminal session's start and exit (or that it
// never started), as facts of the run; no other runner fact, and none as a
// command.
#[test]
fn the_user_may_report_a_terminal_s_start_and_exit_and_no_other_runner_fact() {
    let list = user_allowlist();
    for type_url in [
        url::<runs::RecordRunnerStarted>(),
        url::<runs::RecordRunExited>(),
        url::<runs::RecordLaunchFailed>(),
    ] {
        assert!(
            list.permits_body(BodyKind::Fact, &type_url).is_ok(),
            "{type_url}"
        );
        assert!(
            list.permits_body(BodyKind::Command, &type_url).is_err(),
            "{type_url}"
        );
    }
    for type_url in [
        url::<runs::RecordTurnCompleted>(),
        url::<runs::RecordRunnerLost>(),
        url::<runs::RecordLaunchOrphaned>(),
    ] {
        assert!(
            list.permits_body(BodyKind::Fact, &type_url).is_err(),
            "{type_url}"
        );
    }
}

// Step 8 U1: the user gives and takes back a project's trust, as commands
// of the trust domain, and as nothing else.
#[test]
fn the_user_may_grant_and_revoke_a_projects_trust_as_commands() {
    let list = user_allowlist();
    for type_url in [url::<trust::GrantTrust>(), url::<trust::RevokeTrust>()] {
        assert!(
            list.permits_body(BodyKind::Command, &type_url).is_ok(),
            "{type_url}"
        );
        assert!(
            list.permits_body(BodyKind::Fact, &type_url).is_err(),
            "{type_url}"
        );
    }
}

// Step 8 U4: the user answers an ask (a command) and an engine withdraws
// one (a fact); the ask itself and its name's claim are the daemon's own.
#[test]
fn the_user_may_answer_an_ask_and_withdraw_one_and_never_ask_or_claim() {
    let list = user_allowlist();
    let answer = url::<approvals::AnswerApproval>();
    assert!(list.permits_body(BodyKind::Command, &answer).is_ok());
    assert!(list.permits_body(BodyKind::Fact, &answer).is_err());
    let withdrawn = url::<approvals::RecordApprovalWithdrawn>();
    assert!(list.permits_body(BodyKind::Fact, &withdrawn).is_ok());
    assert!(list.permits_body(BodyKind::Command, &withdrawn).is_err());
    for kind in [BodyKind::Command, BodyKind::Fact] {
        for type_url in [
            url::<approvals::RecordApprovalAsked>(),
            url::<approvals::ClaimApprovalName>(),
        ] {
            assert!(list.permits_body(kind, &type_url).is_err(), "{type_url}");
        }
    }
}

// Step 10a U0: the library domain's commands and facts (protos.md §1's
// gate): an observation and a step's outcome as facts, the five operations
// as commands, and none of them the other way round.
#[test]
fn the_user_may_send_the_library_domains_commands_and_facts() {
    let list = user_allowlist();
    let commands = [
        url::<library_tree::ReplaceFile>(),
        url::<library_tree::CreateDirectory>(),
        url::<library_tree::TrashPath>(),
        url::<library_tree::MovePath>(),
        url::<library_tree::RunSequence>(),
    ];
    let facts = [
        url::<library_tree::RecordTreeObserved>(),
        url::<library_tree::RecordStepDone>(),
        url::<library_tree::RecordStepFailed>(),
    ];
    for type_url in commands {
        assert!(
            list.permits_body(BodyKind::Command, &type_url).is_ok(),
            "{type_url}"
        );
        assert!(
            list.permits_body(BodyKind::Fact, &type_url).is_err(),
            "{type_url}"
        );
    }
    for type_url in facts {
        assert!(
            list.permits_body(BodyKind::Fact, &type_url).is_ok(),
            "{type_url}"
        );
        assert!(
            list.permits_body(BodyKind::Command, &type_url).is_err(),
            "{type_url}"
        );
    }
}

// The gate keeps the digest alone: nothing it renders is the bearer.
#[test]
fn the_gate_shows_no_bearer() {
    let shown = format!("{:?}", UserGate::new(&bearer()));
    assert!(!shown.contains(&bearer().to_text()), "{shown}");
}
