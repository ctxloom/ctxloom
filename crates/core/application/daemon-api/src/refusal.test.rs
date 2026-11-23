//! A typed refusal, as the diagnostic a person is told.

use angzarr_diagnostic::Diagnostic;
use angzarr_embedded::Any;
use daemon_proto::v1;

use super::{rejection_of, ApiError, ApiRefusal, ApiRejection, Rejection, RemovalRejection};
use crate::CallError;

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
            Any::from_msg(&v1::TranscriptEmpty::default()).unwrap(),
            "daemon-api-transcript-empty",
        ),
        (
            Any::from_msg(&v1::ProjectNotFound::default()).unwrap(),
            "daemon-api-project-not-found",
        ),
        (
            Any::from_msg(&v1::ProjectUnknown::default()).unwrap(),
            "daemon-api-project-unknown",
        ),
        (
            Any::from_msg(&v1::RunNotFound::default()).unwrap(),
            "daemon-api-run-not-found",
        ),
        (
            Any::from_msg(&v1::SessionNotHosted::default()).unwrap(),
            "daemon-api-session-not-hosted",
        ),
    ] {
        assert_eq!(code_of(&refusal), code);
    }
}

// A registration's refusals are the sessions domain's, told in its words.
#[test]
fn a_registrations_refusals_are_the_sessions_domains() {
    let changed = Any::from_msg(&sessions_proto::v1::ProjectIdentityMismatch {
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

// Step 10c U11: an apply's refusals, stale or unanswered, are the
// daemon's own, carrying what they name.
#[test]
fn an_applys_refusals_are_the_daemons() {
    let stale = Any::from_msg(&v1::PlanStale {
        seen: "a1".to_string(),
        now: "b2".to_string(),
    })
    .unwrap();
    assert_eq!(
        rejection_of(&stale),
        Some(Rejection::Api(ApiRejection::PlanStale {
            seen: "a1".to_string(),
            now: "b2".to_string(),
        }))
    );
    let unanswered = Any::from_msg(&v1::NeedsUnanswered {
        need_ids: vec!["confirm".to_string()],
    })
    .unwrap();
    assert_eq!(
        rejection_of(&unanswered),
        Some(Rejection::Api(ApiRejection::NeedsUnanswered {
            needs: vec!["confirm".to_string()],
        }))
    );
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
    Any::from_msg(&v1::RemovalRefusal {
        name: "projects/calm-otter/sessions/amber-swift-owl".to_string(),
        reason: v1::removal_refusal::Reason::Kept as i32,
        plan: Some(v1::DeleteSessionResponse {
            harp: plan_harp.to_string(),
            ..v1::DeleteSessionResponse::default()
        }),
        ..v1::RemovalRefusal::default()
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
            ApiRejection::RunNotFound {
                name: "r".to_string(),
            },
            "daemon-api-run-not-found name=r",
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
        (
            ApiRejection::PlanStale {
                seen: "a1".to_string(),
                now: "b2".to_string(),
            },
            "daemon-api-plan-stale seen=a1 now=b2",
        ),
        (
            ApiRejection::NeedsUnanswered {
                needs: vec!["confirm".to_string(), "dir".to_string()],
            },
            "daemon-api-needs-unanswered needs=\"confirm, dir\"",
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

// Step 10a U2: a library read's refusal is the library domain's own typed
// refusal, under its message's status, and the client maps it back by its
// type to the same rejection (PlanRejected: what a read refuses); every
// rejection packs.
#[test]
fn a_library_refusal_travels_as_the_domains_own_and_comes_back_by_its_type() {
    use library_tree::LibraryTreeRejection as Why;
    let read = [Why::PlanRejected {
        code: "library-application-profile-unknown".to_string(),
        args: std::collections::BTreeMap::from([("name".to_string(), "x".to_string())]),
    }];
    for rejection in read {
        let packed = crate::library_refusal(&rejection);
        assert_eq!(packed.code, precondition_status(), "{rejection:?}");
        assert_eq!(
            crate::rejection_of(&packed.refusal),
            Some(crate::Rejection::Library(rejection.clone()))
        );
        let called = CallError::Refused(crate::Rejection::Library(rejection.clone()));
        assert_eq!(called.code(), rejection.code());
        assert_eq!(
            exit_code::ExitClass::exit(&called),
            exit_code::Exit::REFUSED
        );
    }
    for rejection in [
        Why::PlanStale {
            path: "p".to_string(),
            seen: 1,
            now: 2,
        },
        Why::ResourceExists {
            path: "p".to_string(),
        },
        Why::NoSuchResource {
            path: "p".to_string(),
        },
        Why::BuiltinReadOnly {
            bundle: "ctxloom".to_string(),
        },
    ] {
        let packed = crate::library_refusal(&rejection);
        assert!(
            packed.refusal.type_url.contains("ctxloom.library.v1."),
            "{packed:?}"
        );
    }
}

/// The status `library_tree::PlanRejected` declares, as the gRPC code: what
/// a read refusal of the library domain is answered under.
pub(crate) fn precondition_status() -> i32 {
    use library_tree::Refusal;
    angzarr_embedded::angzarr_router::error::GrpcCode::from(
        <library_tree::PlanRejected as Refusal>::REFUSAL_STATUS,
    ) as i32
}

/// The code of the cause [`a_library_decline_is_refused_and_a_failure_failed`]
/// wraps.
fn cause_code() -> String {
    library_application::AppError::NotViewable {
        dir: String::new(),
        path: String::new(),
    }
    .code()
    .to_string()
}

// Step 10a U2 (E5): a library read's decline is its typed refusal; an
// adapter's failure is a failure, said in its own words (step 10b U9: its
// code AND args, so a served import of a source that is not there names
// it, as in process).
#[test]
fn a_library_decline_is_refused_and_a_failure_failed() {
    let declined = super::app_error(&library_application::AppError::NoBundlesDir);
    let ApiError::Refused(refused) = declined else {
        panic!("{declined:?}");
    };
    assert!(
        refused.refusal.type_url.ends_with("LibraryNoBundlesDir"),
        "{}",
        refused.refusal.type_url
    );
    let cause = library_application::AppError::NotViewable {
        dir: "nowhere".to_string(),
        path: "x".to_string(),
    };
    let failed = super::app_error(&library_application::AppError::Cause(Box::new(cause)));
    let ApiError::Diagnosed { code, args } = &failed else {
        panic!("{failed:?}");
    };
    assert_eq!(code, &cause_code());
    assert!(args.iter().any(|arg| arg.value == "nowhere"), "{args:?}");
}

// A deps error that is no read model's decline, but declines, rides as
// `LibraryDeclined` with its own code; one that failed is said in its own
// words.
#[test]
fn a_deps_decline_rides_as_its_cause_and_a_failure_keeps_its_words() {
    let declined = super::deps_error(&deps_application::AppError::DefaultNeedsName);
    let ApiError::Refused(refused) = declined else {
        panic!("{declined:?}");
    };
    assert!(
        refused
            .refusal
            .type_url
            .ends_with("ctxloom.v1.LibraryDeclined"),
        "{}",
        refused.refusal.type_url
    );
    let failed = super::deps_error(&deps_application::AppError::DiscoverFailed);
    assert!(matches!(failed, ApiError::Diagnosed { .. }), "{failed:?}");
}

// An etag that is not a number is told in the daemon's own words, exit 48
// (a decline), never a failure.
#[test]
fn an_etag_that_is_not_a_number_is_the_daemons_refusal() {
    let invalid = Any::from_msg(&v1::EtagInvalid {
        etag: "abc".to_string(),
    })
    .unwrap();
    let rejection = rejection_of(&invalid);
    assert_eq!(
        rejection,
        Some(Rejection::Api(ApiRejection::EtagInvalid {
            etag: "abc".to_string()
        }))
    );
    assert_eq!(
        exit_code::ExitClass::exit(&ApiRejection::EtagInvalid {
            etag: "abc".to_string()
        }),
        exit_code::Exit::REFUSED
    );
}

// Step 10c U10: a setup use case's error is the call's, never in band: a
// decline its typed refusal `SetupDeclined`, fatal findings
// `SetupFatalFindings`, each its cause's own code and args; a failure said
// in its own words. Back at the client each is its own diagnostic again,
// at the exit its class says: 48, 49, 32.
#[test]
fn a_setup_error_is_the_calls_typed_refusal_or_failure_at_its_own_exit() {
    use exit_code::{Exit, ExitClass};
    let cause = library_application::AppError::NotViewable {
        dir: "nowhere".to_string(),
        path: "x".to_string(),
    };
    let client_exit = |error: ApiError| -> (Exit, String) {
        let called = match error {
            ApiError::Refused(refused) => daemon_ports::Called::Refused {
                code: refused.code,
                refusal: refused.refusal,
            },
            ApiError::Diagnosed { code, args } => daemon_ports::Called::Diagnosed {
                code,
                args: args.into_iter().map(|a| (a.name, a.value)).collect(),
            },
            other => panic!("{other:?}"),
        };
        let error = CallError::from(called);
        (error.exit(), error.code().to_string())
    };
    for (exit, type_url) in [
        (Exit::REFUSED, Some("ctxloom.v1.SetupDeclined")),
        (Exit::FATAL_FINDINGS, Some("ctxloom.v1.SetupFatalFindings")),
        (Exit::FAILED, None),
    ] {
        let answered = super::setup_error(&cause, exit);
        match (&answered, type_url) {
            (ApiError::Refused(refused), Some(url)) => {
                assert!(refused.refusal.type_url.ends_with(url), "{refused:?}");
            }
            (ApiError::Diagnosed { code, args }, None) => {
                assert_eq!(code, &cause_code());
                assert!(args.iter().any(|arg| arg.value == "nowhere"), "{args:?}");
            }
            other => panic!("{exit:?}: {other:?}"),
        }
        assert_eq!(client_exit(answered), (exit, cause_code()), "{exit:?}");
    }
}
