use angzarr_diagnostic::Diagnostic;
use daemon_proto::v1;
use prost::Name;

use crate::{diagnostic_samples, Any, Called};

// The catalog test of the binary reads these: one value of every way a call
// can come to nothing, each a code of this crate's.
#[test]
fn the_samples_hold_a_value_of_every_outcome() {
    let codes: Vec<String> = diagnostic_samples()
        .iter()
        .map(|s| s.code().to_string())
        .collect();
    assert_eq!(
        codes,
        [
            "daemon-ports-refused",
            "daemon-ports-unauthenticated",
            "daemon-ports-unimplemented",
            "daemon-ports-unreachable",
            "daemon-ports-failed",
            "daemon-ports-not-started",
            "daemon-ports-exited",
        ]
    );
}

// S11: a refused call holds the typed refusal the daemon packed, read back
// as its own message; any other outcome holds none.
#[test]
fn a_refused_call_holds_its_typed_refusal() {
    let hosting = v1::DaemonHosting {
        runs: 1,
        harps: vec!["brisk-copper-moth".to_string()],
    };
    let refused = Called::Refused {
        code: 9,
        refusal: Any::from_msg(&hosting).unwrap(),
    };
    assert_eq!(refused.refusal::<v1::DaemonHosting>(), Some(hosting));
    assert_eq!(refused.refusal::<v1::SessionNotFound>(), None);
    assert_eq!(Called::Unauthenticated.refusal::<v1::DaemonHosting>(), None);
    let Called::Refused { refusal, .. } = refused else {
        unreachable!()
    };
    assert_eq!(refusal.type_url, v1::DaemonHosting::type_url());
}

// An unauthenticated call says nothing of why, as the daemon said nothing.
#[test]
fn an_unauthenticated_call_has_no_args() {
    assert!(Called::Unauthenticated.args().is_empty());
}

// A daemon that could not be started names its log; one that declines is
// told in its own crate's words.
#[test]
fn an_unreached_daemon_says_why_in_its_words() {
    use crate::Unreached;
    let not_started = Unreached::NotStarted {
        log: "~/.ctxloom/daemon/daemon.log".to_string(),
    };
    assert_eq!(not_started.code(), "daemon-ports-not-started");
    let declined = Unreached::Declined(std::sync::Arc::new(Called::Unauthenticated));
    assert_eq!(declined.code(), "daemon-ports-unauthenticated");
    assert_eq!(declined, declined.clone());
    assert_ne!(declined, not_started);
}

// A daemon started that exited at once is told by how it exited and the end
// of its log, and not shown by that log in a Debug.
#[test]
fn an_exited_daemon_names_its_status_and_its_logs_end() {
    use crate::Unreached;
    let exited = Unreached::Exited {
        log: "~/.ctxloom/daemon/daemon.log".to_string(),
        status: "signal: 11 (SIGSEGV)".to_string(),
        tail: "has overflowed its stack".to_string(),
    };
    assert_eq!(exited.code(), "daemon-ports-exited");
    assert_eq!(
        exited.to_string(),
        "daemon-ports-exited log=~/.ctxloom/daemon/daemon.log status=\"signal: 11 (SIGSEGV)\" tail=\"has overflowed its stack\""
    );
    assert_eq!(
        format!("{exited:?}"),
        "Exited { log: \"~/.ctxloom/daemon/daemon.log\", status: \"signal: 11 (SIGSEGV)\", .. }"
    );
    assert_ne!(
        exited,
        Unreached::NotStarted {
            log: "~/.ctxloom/daemon/daemon.log".to_string()
        }
    );
}

// An outcome is shown as its code and its args, so a log names what the
// call came to and with what.
#[test]
fn an_unreached_daemon_is_shown_by_its_code_and_args() {
    use crate::Unreached;
    let not_started = Unreached::NotStarted {
        log: "/home/u/.ctxloom/daemon/daemon.log".to_string(),
    };
    assert_eq!(
        not_started.to_string(),
        "daemon-ports-not-started log=/home/u/.ctxloom/daemon/daemon.log"
    );
    assert_eq!(
        format!("{not_started:?}"),
        "NotStarted { log: \"/home/u/.ctxloom/daemon/daemon.log\" }"
    );
    let declined = Unreached::Declined(std::sync::Arc::new(Called::Unimplemented {
        method: "/ctxloom.v1.Ctxloom/ListSessions".to_string(),
    }));
    assert_eq!(
        declined.to_string(),
        "daemon-ports-unimplemented method=/ctxloom.v1.Ctxloom/ListSessions"
    );
    assert_eq!(
        format!("{declined:?}"),
        "Declined(\"daemon-ports-unimplemented\")"
    );
}

#[test]
fn a_call_that_came_to_nothing_is_shown_by_its_code_and_args() {
    let refused = Called::Refused {
        code: 9,
        refusal: Any::from_msg(&v1::DaemonHosting::default()).unwrap(),
    };
    assert_eq!(
        refused.to_string(),
        format!(
            "daemon-ports-refused code={}",
            v1::DaemonHosting::type_url()
        )
    );
    assert_eq!(
        Called::Unreachable {
            detail: "refused".to_string()
        }
        .to_string(),
        "daemon-ports-unreachable ext_err_txt=refused"
    );
    assert_eq!(
        Called::Failed {
            code: 13,
            detail: "boom".to_string()
        }
        .to_string(),
        "daemon-ports-failed code=13 ext_err_txt=boom"
    );
    assert_eq!(
        Called::Unauthenticated.to_string(),
        "daemon-ports-unauthenticated"
    );
}

fn projected(projector: &str, name: &str) -> crate::Projection {
    crate::Projection {
        projector: projector.to_string(),
        projection: Some(
            Any::from_msg(&v1::Session {
                name: name.to_string(),
                ..v1::Session::default()
            })
            .unwrap(),
        ),
        ..crate::Projection::default()
    }
}

// A sync's projection is the named projector's, decoded; one nobody answered
// is none.
#[test]
fn a_synced_projection_is_the_named_projectors() {
    let synced = crate::Synced {
        applied: true,
        projections: vec![
            projected("other", "first"),
            projected("session-index", "mine"),
        ],
    };
    let session: Option<v1::Session> = synced.projection("session-index");
    assert_eq!(session.map(|s| s.name), Some("mine".to_string()));
    assert_eq!(synced.projection::<v1::Session>("absent"), None);
}
