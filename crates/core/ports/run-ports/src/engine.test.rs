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

// Ruling 113: an engine that keeps no state of its own in its home merges
// into no file there (the launcher reads none for it).
#[test]
fn an_engine_that_says_nothing_merges_into_no_home_file() {
    assert!(Plain.merges().is_empty());
}

#[test]
fn an_engine_that_says_nothing_asks_for_no_credential() {
    let none = Plain.credentials();
    assert!(none.child_env.is_empty() && none.primary_env.is_empty());
    assert_eq!((none.user_dir, none.login_file), ("", None));
}

// A request is logged by its variables' names alone: every value is withheld
// (credential values never render). The exact rendering.
#[test]
fn a_turn_request_renders_its_env_keys_and_never_the_bearer() {
    let request = TurnRequest {
        prompt: "p".to_string(),
        home: "/h".to_string(),
        context_path: "/h/c".to_string(),
        resume: None,
        env: BTreeMap::from([
            ("CTXLOOM_BEARER".to_string(), "run.TOPSECRET".to_string()),
            ("PATH".to_string(), "/bin".to_string()),
        ]),
        user_home: None,
        cwd: None,
        model: String::new(),
        effort: String::new(),
        settings: crate::EngineSettings::default(),
    };
    assert_eq!(
        format!("{request:?}"),
        r#"TurnRequest { prompt: "p", home: "/h", context_path: "/h/c", resume: None, env: {"CTXLOOM_BEARER": "<redacted>", "PATH": "<redacted>"}, user_home: None, cwd: None, model: "", effort: "", settings: EngineSettings { command: "", session_home: Session, auth: Builtin, env: [] } }"#
    );
}

// Ruling 58: an engine says its session ends with its exit only by saying so;
// one that does not (claude, whose SessionEnd hook says it) leaves it to hooks.
#[test]
fn an_engine_that_says_nothing_does_not_end_its_session_by_its_exit() {
    assert!(!Plain.session_ends_with_exit());
}

// Ruling 168: an engine with nothing to say of a plan advises nothing, a plan
// with no model included.
#[test]
fn an_engine_that_says_nothing_advises_nothing() {
    assert!(Plain.advise(&crate::Planned::default()).is_empty());
}
