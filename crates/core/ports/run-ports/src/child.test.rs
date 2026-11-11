use std::collections::BTreeMap;

use angzarr_diagnostic::Diagnostic;

use crate::{ChildError, ChildMessage, ChildSpec, EngineError, EngineSettings, Urgency};

fn spec() -> ChildSpec {
    ChildSpec {
        home: "/p/.ctxloom/sessions/h/home".to_string(),
        context_path: "/p/.ctxloom/sessions/h/home/ctxloom-context.md".to_string(),
        env: BTreeMap::from([(
            "CLAUDE_CODE_OAUTH_TOKEN".to_string(),
            "sk-ant-oat01-secret".to_string(), // gitleaks:allow
        )]),
        cwd: Some("/p".to_string()),
        model: String::new(),
        effort: String::new(),
        settings: EngineSettings::default(),
        resume: None,
        session_id: None,
    }
}

#[test]
fn the_urgencies_are_named_as_the_design_names_them() {
    assert_eq!(Urgency::Inform.name(), "inform");
    assert_eq!(Urgency::Wake.name(), "wake");
    assert_eq!(Urgency::Steer.name(), "steer");
}

#[test]
fn a_message_is_its_id_and_its_text() {
    let message = ChildMessage::new("m-1", "look at the build");
    assert_eq!(message.id, "m-1");
    assert_eq!(message.text, "look at the build");
}

#[test]
fn a_spec_logs_its_environment_by_name_alone() {
    let logged = format!("{:?}", spec());
    assert!(logged.contains("CLAUDE_CODE_OAUTH_TOKEN"), "{logged}");
    assert!(!logged.contains("sk-ant-oat01-secret"), "{logged}");
    assert!(logged.contains("/p/.ctxloom/sessions/h/home"), "{logged}");
}

#[test]
fn an_engine_error_keeps_its_own_code_and_words() {
    let error = ChildError::from(EngineError::NoHostHome {
        engine: "claude-code".to_string(),
    });
    assert_eq!(error.code(), "run-ports-engine-no-host-home");
    assert_eq!(error.args().len(), 1);
    assert_eq!(
        error.to_string(),
        EngineError::NoHostHome {
            engine: "claude-code".to_string()
        }
        .to_string()
    );
}

#[test]
fn the_child_errors_have_codes_of_their_own() {
    let codes: Vec<String> = super::diagnostic_samples()
        .iter()
        .map(|d| d.code().to_string())
        .collect();
    assert_eq!(
        codes,
        vec![
            "run-ports-child-not-started",
            "run-ports-child-closed",
            "run-ports-child-io"
        ]
    );
}

#[test]
fn a_spawn_failure_names_the_program_and_the_system_text() {
    let error = ChildError::NotStarted {
        program: "claude".to_string(),
        deterministic: true,
        ext_err_txt: "No such file or directory (os error 2)".to_string(),
    };
    let names: Vec<&str> = error.args().iter().map(|(name, _)| *name).collect();
    assert_eq!(names, vec!["program", "ext_err_txt"]);
    assert!(error.to_string().contains("claude"), "{error}");
    assert!(ChildError::Closed.args().is_empty());
    let io = ChildError::Io {
        ext_err_txt: "Broken pipe".to_string(),
    };
    assert!(io.to_string().contains("Broken pipe"), "{io}");
}
