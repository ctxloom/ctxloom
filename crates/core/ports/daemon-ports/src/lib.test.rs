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
