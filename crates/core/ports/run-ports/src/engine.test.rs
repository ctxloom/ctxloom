use super::*;

#[test]
fn the_copy_in_is_the_allowlist_met_with_what_is_offered() {
    let offered = vec![
        "a.json".to_string(),
        "secret".to_string(),
        "b.json".to_string(),
    ];
    let allowed = ["b.json", "a.json", "absent.json"];
    assert_eq!(copy_in(&allowed, &offered), vec!["a.json", "b.json"]);
}

#[test]
fn nothing_is_copied_in_that_is_not_allowed() {
    let offered = vec!["../escape".to_string(), "x".to_string()];
    assert!(copy_in(&["x/y"], &offered).is_empty());
    assert!(copy_in(&[], &offered).is_empty());
}

#[test]
fn a_failure_class_is_named() {
    assert_eq!(FailureClass::RateLimited.name(), "rate_limited");
    assert_eq!(FailureClass::CredentialRefused.name(), "credential_refused");
    assert_eq!(FailureClass::EngineError.name(), "engine_error");
    assert_eq!(FailureClass::Interrupted.name(), "interrupted");
}

#[test]
fn an_engine_that_cannot_yet_says_so_with_its_code() {
    let error = EngineError::NotYet {
        engine: "claude-code".to_string(),
        operation: "drive_interactive".to_string(),
    };
    assert_eq!(
        error.to_string(),
        "run-ports-engine-not-yet engine=claude-code operation=drive_interactive"
    );
}

/// An engine that says nothing of credentials.
struct Plain;

impl Engine for Plain {
    fn name(&self) -> &str {
        "plain"
    }
    fn prepare(&self, _: &PrepareRequest) -> Result<Prepared, EngineError> {
        unreachable!()
    }
    fn drive_turn(&self, _: &TurnRequest) -> Result<crate::TurnCommand, EngineError> {
        unreachable!()
    }
    fn finish_turn(&self, _: &crate::TurnOutput) -> TurnOutcome {
        unreachable!()
    }
    fn drive_interactive(&self, _: &TurnRequest) -> Result<crate::TurnCommand, EngineError> {
        unreachable!()
    }
}

#[test]
fn an_engine_that_says_nothing_asks_for_no_credential() {
    let none = Plain.credentials();
    assert!(none.child_env.is_empty() && none.primary_env.is_empty());
    assert_eq!((none.user_dir, none.login_file), ("", None));
}
