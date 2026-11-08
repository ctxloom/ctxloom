use angzarr_diagnostic::{Arg, Diagnostic};
use library_application::AppError as PlanError;
use run_ports::FailureClass;
use runs_application::AppError as CoreError;

use crate::{diagnostic_samples, RunError, EN_US};

#[test]
fn every_diagnostic_has_a_message_using_exactly_its_args() {
    let (name, catalog) = EN_US;
    assert_eq!(name, "run-application");
    for sample in diagnostic_samples() {
        let code = sample.code();
        assert!(code.starts_with("run-application-"), "{code} is prefixed");
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

// The conflict of flags is the library's, with its own code and its exit 2;
// the startup abort its exit 3.
#[test]
fn a_plan_error_keeps_its_own_code_and_exit() {
    let conflict = RunError::Plan(PlanError::AgentExclusive {
        other: "profile".to_string(),
    });
    assert_eq!(
        conflict.code(),
        PlanError::AgentExclusive {
            other: "profile".to_string()
        }
        .code()
    );
    assert_eq!(conflict.exit_code(), 2);
    let aborted = RunError::Plan(PlanError::StartupAborted {
        count: 1,
        findings: "x".to_string(),
    });
    assert_eq!(aborted.exit_code(), 3);
}

#[test]
fn what_cannot_be_run_is_refused_and_what_went_wrong_in_a_run_failed() {
    assert_eq!(RunError::NoPrompt.exit_code(), 2);
    let unknown = RunError::UnknownEngine {
        name: "x".to_string(),
        known: "y".to_string(),
    };
    assert_eq!(unknown.exit_code(), 2);
    let refused = RunError::Session(CoreError::Refused {
        code: "sessions-names-taken".to_string(),
        args: Default::default(),
    });
    assert_eq!(refused.exit_code(), 2);
    assert!(matches!(&refused.args()[0].1, Arg::Str(reason) if reason == "sessions-names-taken"));
    for failed in [
        RunError::EmptyAnswer,
        RunError::NotStarted {
            detail: String::new(),
        },
        RunError::Turn {
            class: FailureClass::RateLimited,
            detail: String::new(),
        },
        RunError::Session(CoreError::Failed {
            reason: "r".to_string(),
        }),
    ] {
        assert_eq!(failed.exit_code(), 1, "{failed:?}");
    }
}

#[test]
fn each_turn_failure_class_has_a_code_of_its_own() {
    let code_of = |class| {
        RunError::Turn {
            class,
            detail: String::new(),
        }
        .code()
        .to_string()
    };
    let codes = [
        code_of(FailureClass::RateLimited),
        code_of(FailureClass::CredentialRefused),
        code_of(FailureClass::EngineError),
        code_of(FailureClass::Interrupted),
    ];
    let mut unique = codes.to_vec();
    unique.sort();
    unique.dedup();
    assert_eq!(unique.len(), 4, "{codes:?}");
}
