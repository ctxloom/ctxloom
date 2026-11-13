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
        method: "/ctxloom.daemon.v1.Ctxloom/ListSessions".to_string(),
    }));
    assert_eq!(
        declined.to_string(),
        "daemon-ports-unimplemented method=/ctxloom.daemon.v1.Ctxloom/ListSessions"
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
