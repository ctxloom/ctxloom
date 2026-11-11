use angzarr_diagnostic::{Arg, Diagnostic};
use exit_code::{Exit, ExitClass};
use library_application::AppError as PlanError;
use run_ports::FailureClass;
use runs_application::AppError as CoreError;

use runs::RunRejection;

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

// The conflict of flags is the library's, with its own code and its exit 20;
// the startup abort its exit 21.
#[test]
fn a_plan_error_keeps_its_own_code_and_exit() {
    let conflict = RunError::Plan(Box::new(PlanError::AgentExclusive {
        other: "profile".to_string(),
    }));
    assert_eq!(
        conflict.code(),
        PlanError::AgentExclusive {
            other: "profile".to_string()
        }
        .code()
    );
    assert_eq!(conflict.exit(), Exit::REFUSED);
    let aborted = RunError::Plan(Box::new(PlanError::StartupAborted {
        count: 1,
        findings: "x".to_string(),
    }));
    assert_eq!(aborted.exit(), Exit::FATAL_FINDINGS);
}

#[test]
fn what_cannot_be_run_is_refused_and_what_went_wrong_in_a_run_failed() {
    assert_eq!(
        RunError::Refused(RunRejection::NoPrompt).exit(),
        Exit::REFUSED
    );
    let unknown = RunError::Refused(RunRejection::UnknownEngine {
        name: "x".to_string(),
        known: "y".to_string(),
    });
    assert_eq!(unknown.exit(), Exit::REFUSED);
    let refused = RunError::Session(CoreError::Refused {
        code: "sessions-names-taken".to_string(),
        args: Default::default(),
    });
    assert_eq!(refused.exit(), Exit::REFUSED);
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
        assert_eq!(failed.exit(), Exit::FAILED, "{failed:?}");
    }
}

// A run a signal stopped (ruling 114) exits as a shell reports a process that
// signal ended, and says which signal it was.
#[test]
fn a_run_stopped_by_a_signal_exits_with_its_status_and_names_it() {
    let stopped = RunError::Stopped {
        signal: "SIGHUP".to_string(),
        status: 129,
    };
    assert_eq!(stopped.exit(), Exit::signal(1));
    assert_eq!(stopped.code(), "run-application-stopped");
    assert!(matches!(&stopped.args()[..], [("signal", Arg::Str(s))] if s == "SIGHUP"));
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

// The samples are the catalog test's only input: one of every code, none empty.
#[test]
fn the_samples_hold_a_value_of_every_code() {
    let codes: Vec<String> = diagnostic_samples()
        .iter()
        .map(|s| s.code().to_string())
        .collect();
    for want in ["not-started", "empty-answer", "not-ended"] {
        assert!(
            codes.contains(&format!("run-application-{want}")),
            "{want} in {codes:?}"
        );
    }
}

// An error displays as its code and its args.
#[test]
fn an_error_displays_as_its_code_and_args() {
    assert_eq!(
        RunError::Refused(RunRejection::NoPrompt).to_string(),
        "runs-no-prompt"
    );
    assert_eq!(
        RunError::NotEnded { run: "r1".into() }.to_string(),
        "run-application-not-ended run=r1"
    );
}

// Ruling 73c, hard migrate: the prompt and the engine are the runs domain's
// refusals now, and no code of this crate's names them.
#[test]
fn no_prompt_and_an_unknown_engine_are_no_codes_of_this_crate() {
    let codes: Vec<String> = diagnostic_samples()
        .iter()
        .map(|s| s.code().to_string())
        .collect();
    for gone in [
        "run-application-no-prompt",
        "run-application-unknown-engine",
    ] {
        assert!(!codes.iter().any(|c| c == gone), "{gone} in {codes:?}");
        assert!(
            !EN_US.1.lines().any(|l| l.starts_with(&format!("{gone} ="))),
            "{gone} in the catalog"
        );
    }
}

// Ruling 73a: most severe wins. An aggregate with the plan's fatal findings
// among its failures is exit 21, without them exit 20; every refusal it holds
// is said, in check order. A single refusal holds no list (ruling 73b).
#[test]
fn an_aggregate_is_exit_21_with_fatal_findings_and_says_each_refusal_in_order() {
    let aggregate = |fatal_findings| {
        RunError::Refused(RunRejection::LaunchRefused {
            refusals: vec![
                RunRejection::NoPrompt,
                RunRejection::UnknownEngine {
                    name: "x".to_string(),
                    known: "y".to_string(),
                },
            ],
            fatal_findings,
        })
    };
    assert_eq!(aggregate(0).exit(), Exit::REFUSED);
    assert_eq!(aggregate(1).exit(), Exit::FATAL_FINDINGS);
    let codes: Vec<String> = aggregate(1)
        .refusals()
        .iter()
        .map(|r| r.code().to_string())
        .collect();
    assert_eq!(codes, ["runs-no-prompt", "runs-unknown-engine"]);
    // A single refusal is its own plain envelope: it holds no list.
    assert!(RunError::Refused(RunRejection::NoPrompt)
        .refusals()
        .is_empty());
    assert!(RunError::EmptyAnswer.refusals().is_empty());
}

// Owner ruling 115: a resume refused is the session domain's own refusal, its
// code and args as the domain says them, a refusal's exit.
#[test]
fn a_resume_refused_is_the_session_domains_own_refusal() {
    let error = RunError::Resume(sessions::SessionRejection::SessionUnknown {
        harp: "no-such-heron".to_string(),
    });
    assert_eq!(error.code(), "sessions-session-unknown");
    assert_eq!(error.exit(), Exit::REFUSED);
    let args: Vec<String> = error.args().iter().map(|(n, _)| n.to_string()).collect();
    assert_eq!(args, ["harp"]);
}

// Ruling 152: a lock another process holds declines the run (the session or
// run is live: 20); a lock file that cannot be made is a failure (10).
#[test]
fn a_lock_held_elsewhere_is_refused_and_one_that_cannot_be_made_failed() {
    use angzarr_diagnostic::Report;
    use run_ports::LockError;
    let held = RunError::Lock(LockError::Held(Report::new("file-lock-timeout")));
    assert_eq!(held.exit(), Exit::REFUSED);
    let failed = RunError::Lock(LockError::Failed(Report::new("file-lock-io")));
    assert_eq!(failed.exit(), Exit::FAILED);
    assert_eq!(held.code(), failed.code(), "one diagnostic, two kinds");
}
