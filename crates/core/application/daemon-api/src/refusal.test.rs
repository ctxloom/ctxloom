//! A typed refusal, as the diagnostic a person is told.

use angzarr_diagnostic::Diagnostic;
use angzarr_embedded::Any;
use daemon_proto::v1;

use super::{rejection_of, ApiError, ApiRefusal, ApiRejection, Rejection, RemovalRejection};

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

// A configuration's refusals are the daemon's own, told in its words.
#[test]
fn a_configurations_refusals_are_the_daemons() {
    let age = Any::from_msg(&v1::AgeInvalid {
        key: "session_purge_age".to_string(),
        value: "soon".to_string(),
    })
    .unwrap();
    assert_eq!(
        rejection_of(&age),
        Some(Rejection::Api(ApiRejection::AgeInvalid {
            key: "session_purge_age".to_string(),
            value: "soon".to_string(),
        }))
    );
    let unreadable = Any::from_msg(&v1::ConfigUnreadable {
        root: "/work/calm".to_string(),
        detail: "it does not parse".to_string(),
    })
    .unwrap();
    assert_eq!(
        rejection_of(&unreadable),
        Some(Rejection::Api(ApiRejection::ConfigUnreadable {
            root: "/work/calm".to_string(),
            detail: "it does not parse".to_string(),
        }))
    );
}

// A project with no name is the sessions domain's refusal.
#[test]
fn an_unnamed_project_is_the_sessions_domains() {
    let unnamed = Any::from_msg(&sessions_proto::v1::UnnamedProject {
        project_id: "p-1".to_string(),
    })
    .unwrap();
    assert_eq!(
        rejection_of(&unnamed),
        Some(Rejection::Sessions(
            sessions::SessionRejection::ProjectNotNamed {
                project_id: "p-1".to_string(),
            }
        ))
    );
}

fn kept(plan_harp: &str) -> Any {
    Any::from_msg(&v1::RemovalRefused {
        name: "projects/calm-otter/sessions/amber-swift-owl".to_string(),
        reason: v1::RemovalRefusal::Kept as i32,
        plan: Some(v1::DeleteSessionResponse {
            harp: plan_harp.to_string(),
            ..v1::DeleteSessionResponse::default()
        }),
        ..v1::RemovalRefused::default()
    })
    .unwrap()
}

// A refused removal names the session its plan names, else the one its
// resource name ends with.
#[test]
fn a_refused_removal_names_its_session() {
    let removal = |harp: &str| {
        Some(Rejection::Removal(RemovalRejection::Session(
            sessions_application::Refusal::Kept {
                harp: harp.to_string(),
            },
        )))
    };
    assert_eq!(
        rejection_of(&kept("brisk-copper-moth")),
        removal("brisk-copper-moth")
    );
    assert_eq!(rejection_of(&kept("")), removal("amber-swift-owl"));
}

// Each diagnostic displays as its code and its args.
#[test]
fn the_diagnostics_display_their_code_and_args() {
    let said = |d: &dyn std::fmt::Display| d.to_string();
    assert_eq!(
        said(&ApiError::Failed {
            detail: "the store".to_string()
        }),
        "daemon-api-failed ext_err_txt=\"the store\""
    );
    assert_eq!(
        said(&ApiError::Refused(ApiRefusal::of(
            &v1::ProjectNotFound::default()
        ))),
        "daemon-api-refused ext_err_txt=/ctxloom.v1.ProjectNotFound"
    );
    for (rejection, shown) in [
        (
            ApiRejection::NameInvalid {
                name: "x".to_string(),
                reason: "shape".to_string(),
            },
            "daemon-api-name-invalid name=x reason=shape",
        ),
        (
            ApiRejection::SessionNotFound {
                name: "s".to_string(),
            },
            "daemon-api-session-not-found name=s",
        ),
        (
            ApiRejection::ProjectNotFound {
                name: "p".to_string(),
            },
            "daemon-api-project-not-found name=p",
        ),
        (
            ApiRejection::ProjectUnknown {
                cwd: "/tmp".to_string(),
            },
            "daemon-api-project-unknown cwd=/tmp",
        ),
        (
            ApiRejection::DaemonHosting {
                runs: 2,
                harps: vec!["a".to_string(), "b".to_string()],
            },
            "daemon-api-daemon-hosting runs=2 harps=\"a, b\"",
        ),
        (
            ApiRejection::AgeInvalid {
                key: "k".to_string(),
                value: "soon".to_string(),
            },
            "daemon-api-age-invalid key=k value=soon",
        ),
        (
            ApiRejection::ConfigUnreadable {
                root: "/work".to_string(),
                detail: "bad".to_string(),
            },
            "daemon-api-config-unreadable root=/work ext_err_txt=bad",
        ),
    ] {
        assert_eq!(said(&rejection), shown);
    }
    let kept = RemovalRejection::Session(sessions_application::Refusal::Kept {
        harp: "amber".to_string(),
    });
    assert_eq!(kept.code(), "sessions-application-refused-kept");
    assert_eq!(said(&kept), "sessions-application-refused-kept harp=amber");
    let keeps = RemovalRejection::KeepsSession {
        harp: "amber".to_string(),
        spared: 1,
        skipped: 2,
    };
    assert_eq!(keeps.code(), "isolation-application-keeps-session");
    assert_eq!(
        said(&keeps),
        "isolation-application-keeps-session harp=amber spared=1 skipped=2"
    );
}
