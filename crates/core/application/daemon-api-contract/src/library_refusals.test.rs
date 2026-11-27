use coded_diagnostic::{Diagnostic, Report};

use super::{every, rejection, typed};

// E5: every decline of a library use case is a typed refusal of its own,
// and its type alone maps it back to the same code and args, a wrapped
// cause included.
#[test]
fn every_decline_round_trips_by_its_type_to_its_own_diagnostic() {
    let all = every();
    let mut types = std::collections::BTreeSet::new();
    for error in &all {
        let refused = typed(error).unwrap_or_else(|| panic!("{} is typed", error.code()));
        assert!(
            types.insert(refused.refusal.type_url.clone()),
            "one type per cause: {}",
            refused.refusal.type_url
        );
        let back = rejection(&refused.refusal).expect("mapped back by its type");
        let want = Report::of(error);
        assert_eq!(back.code(), want.code(), "{}", refused.refusal.type_url);
        assert_eq!(back.to_string(), want.to_string(), "{}", error.code());
    }
    assert_eq!(types.len(), all.len());
}

#[test]
fn a_cause_that_is_no_decline_is_no_refusal() {
    let failed =
        library_application::AppError::Cause(Box::new(library_application::AppError::NoBundlesDir));
    assert!(typed(&failed).is_none());
    let aborted = library_application::AppError::StartupAborted {
        count: 1,
        findings: String::new(),
    };
    assert!(typed(&aborted).is_none());
}

#[test]
fn a_status_is_the_message_s_own() {
    let refused = typed(&library_application::AppError::AgentNotFound {
        name: "dev".to_string(),
    })
    .unwrap();
    // google.rpc.Code 5, the status the message itself declares.
    assert_eq!(refused.code, 5);
    let refused = typed(&library_application::AppError::SearchNothing).unwrap();
    assert_eq!(refused.code, 3);
}

#[test]
fn another_message_is_none_of_these() {
    let other = prost_types::Any::from_msg(&daemon_proto::v1::NameInvalid::default()).unwrap();
    assert!(rejection(&other).is_none());
}

/// The deps read models' declines a deps read can refuse with (step 10a
/// U2, E5): the registry's and the lock's, as they are read.
const DEPS_READ_DECLINES: [&str; 10] = [
    "deps-unreadable",
    "deps-too-new",
    "deps-too-old",
    "deps-bad-shape",
    "deps-forge-type",
    "deps-unknown-forge",
    "deps-no-repository",
    "deps-lock-empty",
    "deps-lock-key",
    "deps-lock-unreadable",
];

// E5 (coordinator, following it for the deps reads): each decline of a
// deps read is a typed refusal of its own (`ctxloom.v1.Deps*`), not
// `LibraryDeclined`, and its type alone maps it back to the same code and
// args, a wrapped cause included.
#[test]
fn every_deps_read_decline_is_typed_and_round_trips_by_its_type() {
    let samples: Vec<_> = deps_application::deps_diagnostic_samples()
        .into_iter()
        .filter(|d| DEPS_READ_DECLINES.contains(&d.code()))
        .collect();
    assert_eq!(samples.len(), DEPS_READ_DECLINES.len());
    let mut types = std::collections::BTreeSet::new();
    for sample in samples {
        let want = Report::of(sample.as_ref());
        let code = sample.code().to_string();
        let error = deps_application::AppError::Declined(sample);
        let crate::ApiError::Refused(refused) = crate::refusal::deps_error(&error) else {
            panic!("{code} is refused");
        };
        assert!(
            refused.refusal.type_url.contains("ctxloom.v1.Deps"),
            "{code}: {}",
            refused.refusal.type_url
        );
        assert!(types.insert(refused.refusal.type_url.clone()), "{code}");
        // google.rpc.Code 9, the status every message declares.
        assert_eq!(refused.code, 9, "{code}");
        let back = rejection(&refused.refusal).expect("mapped back by its type");
        assert_eq!(back.code(), code);
        assert_eq!(back.to_string(), want.to_string(), "{code}");
    }
}

// What no deps read declines with (a write's) still rides as its cause.
#[test]
fn a_deps_write_decline_still_rides_as_its_cause() {
    let sample = deps_application::deps_diagnostic_samples()
        .into_iter()
        .find(|d| d.code() == "deps-remote-exists")
        .unwrap();
    let error = deps_application::AppError::Declined(sample);
    let crate::ApiError::Refused(refused) = crate::refusal::deps_error(&error) else {
        panic!("refused");
    };
    assert!(
        refused
            .refusal
            .type_url
            .ends_with("ctxloom.v1.LibraryDeclined"),
        "{}",
        refused.refusal.type_url
    );
}

// Only a decline is typed: every error `typed` maps exits refused, so a
// read answers no failure as a refusal (`refusal::app_error`).
#[test]
fn every_typed_decline_exits_refused() {
    use exit_code::ExitClass;
    for error in every() {
        assert_eq!(error.exit(), exit_code::Exit::REFUSED, "{}", error.code());
    }
}

#[test]
fn an_arg_is_read_as_its_text_or_its_count_whatever_its_number() {
    use coded_diagnostic::Arg;
    let args = [
        ("text", Arg::Str("words".to_string())),
        ("uint", Arg::Uint(3)),
        ("int", Arg::Int(-2)),
        ("positive", Arg::Int(4)),
    ];
    assert_eq!(super::arg_text(&args, "text"), "words");
    assert_eq!(super::arg_text(&args, "uint"), "3");
    assert_eq!(super::arg_text(&args, "int"), "-2");
    assert_eq!(super::arg_text(&args, "none"), "");
    assert_eq!(super::arg_count(&args, "uint"), 3);
    assert_eq!(super::arg_count(&args, "positive"), 4);
    assert_eq!(
        super::arg_count(&args, "int"),
        0,
        "a negative count is none"
    );
    assert_eq!(super::arg_count(&args, "text"), 0);
}
