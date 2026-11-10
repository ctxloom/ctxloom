use angzarr_coordinator_grpc::ListenError;
use angzarr_diagnostic::Diagnostic;

use crate::{diagnostic_samples, ListenerError, EN_US};

#[test]
fn every_diagnostic_has_a_message_using_exactly_its_args() {
    let (name, catalog) = EN_US;
    assert_eq!(name, "hook-listener");
    for sample in diagnostic_samples() {
        let code = sample.code();
        assert!(code.starts_with("hook-listener-"), "{code} is prefixed");
        let line = catalog
            .lines()
            .find(|l| l.starts_with(&format!("{code} =")))
            .unwrap_or_else(|| panic!("{code} has no message"));
        let mut args: Vec<&str> = sample.args().into_iter().map(|(n, _)| n).collect();
        args.sort_unstable();
        let mut used: Vec<&str> = line
            .split('$')
            .skip(1)
            .map(|rest| {
                rest.split(|c: char| !(c.is_ascii_alphanumeric() || c == '_' || c == '-'))
                    .next()
                    .unwrap()
            })
            .collect();
        used.sort_unstable();
        used.dedup();
        assert_eq!(args, used, "{code}");
    }
}

#[test]
fn a_bind_failure_names_the_address_and_carries_the_listeners_cause() {
    let cause = ListenError::Io {
        op: "bind",
        target: "127.0.0.1:9".to_string(),
        error: "in use".to_string(),
    };
    let error = ListenerError::bind_addr("127.0.0.1:9".parse().unwrap(), &cause);
    assert_eq!(error.code(), "hook-listener-bind");
    let rendered = error.to_string();
    assert!(rendered.contains("127.0.0.1:9"), "{rendered}");
}

// The listener's limits are angzarr's defaults (the listener sets none), so
// its grace is never zero; were a bind refused for one, the refusal is the
// listener's own cause, said by the listener's catalog, which CATALOGS carries.
#[test]
fn a_bind_refused_for_a_zero_connection_age_grace_carries_the_listeners_cause_and_message() {
    let cause = ListenError::ZeroConnectionAgeGrace;
    let error = ListenerError::bind_addr("127.0.0.1:9".parse().unwrap(), &cause);
    assert_eq!(error.code(), "hook-listener-bind");
    assert_eq!(
        error,
        ListenerError::Bind {
            endpoint: "127.0.0.1:9".to_string(),
            cause: ListenError::ZeroConnectionAgeGrace,
        }
    );
    let id = format!("{} =", cause.code());
    let (_, listener_catalog) = crate::CATALOGS[1];
    assert!(
        listener_catalog.lines().any(|l| l.starts_with(&id)),
        "{id} is in the listener's catalog"
    );
}

// The samples hold a value of every code of the crate: a catalog test that
// reads none would pass without checking anything.
#[test]
fn the_samples_hold_a_value_of_every_code() {
    let mut codes: Vec<String> = diagnostic_samples()
        .iter()
        .map(|s| s.code().to_string())
        .collect();
    codes.sort();
    assert_eq!(
        codes,
        [
            "hook-listener-bind",
            "hook-listener-bind-not-reported",
            "hook-listener-bind-unspecified",
            "hook-listener-stop",
            "hook-listener-stop-timed-out"
        ]
    );
}

#[test]
fn a_timeout_names_its_grace_in_milliseconds() {
    let error = ListenerError::timed_out("/s", std::time::Duration::from_millis(1500));
    assert_eq!(error.code(), "hook-listener-stop-timed-out");
    let rendered = error.to_string();
    assert!(rendered.contains("1500"), "{rendered}");
}

#[test]
fn the_identity_debugs_as_a_fixed_text_without_the_key_or_certificate() {
    let identity = crate::TlsIdentity::from_pem("CERT-PEM-TEXT", "KEY-PEM-SECRET");
    let shown = format!("{identity:?}");
    assert_eq!(shown, "TlsIdentity(key redacted)");
    assert!(!shown.contains("KEY-PEM-SECRET") && !shown.contains("CERT-PEM-TEXT"));
}
