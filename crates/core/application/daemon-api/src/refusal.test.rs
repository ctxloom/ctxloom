//! A typed refusal, as the diagnostic a person is told.

use angzarr_diagnostic::Diagnostic;
use angzarr_embedded::Any;
use daemon_proto::v1;

use super::{rejection_of, ApiRefusal, ApiRejection, Rejection};

fn code_of(refusal: &Any) -> String {
    rejection_of(refusal)
        .map(|rejection| rejection.report().code().to_string())
        .unwrap_or_default()
}

// The daemon's own refusals, each under the status its proto declares, and
// told in this crate's words.
#[test]
fn the_daemons_own_refusals_are_told_in_its_words() {
    let hosting = ApiRefusal::of(&v1::DaemonHosting {
        runs: 2,
        harps: vec!["a".to_string(), "b".to_string()],
    });
    assert_eq!(
        hosting.code,
        <v1::DaemonHosting as daemon_proto::Refusal>::REFUSAL_STATUS
    );
    assert_eq!(
        rejection_of(&hosting.refusal),
        Some(Rejection::Api(ApiRejection::DaemonHosting {
            runs: 2,
            harps: vec!["a".to_string(), "b".to_string()],
        }))
    );
    for (refusal, code) in [
        (
            Any::from_msg(&v1::NameInvalid::default()).unwrap(),
            "daemon-api-name-invalid",
        ),
        (
            Any::from_msg(&v1::SessionNotFound::default()).unwrap(),
            "daemon-api-session-not-found",
        ),
        (
            Any::from_msg(&v1::ProjectNotFound::default()).unwrap(),
            "daemon-api-project-not-found",
        ),
        (
            Any::from_msg(&v1::ProjectUnknown::default()).unwrap(),
            "daemon-api-project-unknown",
        ),
    ] {
        assert_eq!(code_of(&refusal), code);
    }
}

// A registration's refusals are the sessions domain's, told in its words.
#[test]
fn a_registrations_refusals_are_the_sessions_domains() {
    let changed = Any::from_msg(&sessions_proto::v1::ProjectIdentityChanged {
        project: "projects/calm-quiet-heron".to_string(),
        root: "/work/calm".to_string(),
        registered_commit: "a".to_string(),
        found_commit: "b".to_string(),
    })
    .unwrap();
    assert_eq!(code_of(&changed), "sessions-project-identity-changed");
    let mismatch = Any::from_msg(&sessions_proto::v1::ProjectHarpMismatch::default()).unwrap();
    assert_eq!(code_of(&mismatch), "sessions-project-harp-mismatch");
}

// A refusal of a type no crate here knows is none of these.
#[test]
fn an_unknown_refusal_is_none() {
    let other = Any {
        type_url: "type.googleapis.com/some.Other".to_string(),
        value: Vec::new(),
    };
    assert_eq!(rejection_of(&other), None);
}
