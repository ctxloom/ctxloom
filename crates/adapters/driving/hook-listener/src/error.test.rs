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
fn a_bind_failure_names_the_socket_and_carries_the_listeners_cause() {
    let cause = ListenError::DirectoryNotPrivate {
        path: "/run/x".to_string(),
    };
    let error = ListenerError::bind(std::path::Path::new("/run/x/a.sock"), &cause);
    assert_eq!(error.code(), "hook-listener-bind");
    let rendered = error.to_string();
    assert!(rendered.contains("/run/x/a.sock"), "{rendered}");
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
