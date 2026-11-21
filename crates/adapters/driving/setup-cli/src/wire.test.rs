//! What the daemon says of doctor and `auth status` reads back as the
//! report the CLI has always printed (step 10c U5).

use auth_application::Row;
use doctor_checks::{Check, Report, Status};

use super::{auth_rows, checks, report, rows_of, status_of, status_word};

// Every verdict says itself as its JSON word, and reads back as itself; a
// word no verdict has is never read as a pass.
#[test]
fn a_verdict_reads_back_as_itself_and_an_unknown_word_is_no_pass() {
    for status in [Status::Ok, Status::Warn, Status::Fail, Status::Info] {
        assert_eq!(status_of(status_word(status)), status);
        assert_eq!(
            serde_json::to_value(status).unwrap(),
            serde_json::json!(status_word(status))
        );
    }
    assert_eq!(status_of("bogus"), Status::Info);
}

// doctor's report, said by the daemon, is the report the CLI prints.
#[test]
fn a_doctor_report_reads_back_as_it_was_made() {
    let made = Report {
        checks: vec![
            Check {
                marker: "DOCTOR-CHECK-GITIGNORE-f6".to_string(),
                name: ".gitignore posture".to_string(),
                status: Status::Fail,
                detail: "blanket".to_string(),
                remedy: "ctxloom manage gitignore install".to_string(),
            },
            Check {
                marker: "DOCTOR-CHECK-VERSION-c3".to_string(),
                name: "version".to_string(),
                status: Status::Ok,
                detail: "v1".to_string(),
                remedy: String::new(),
            },
        ],
    };
    assert_eq!(report(&checks(&made)), made);
}

// auth status's rows, said by the daemon, are the rows the CLI prints.
#[test]
fn auth_rows_read_back_as_they_were_made() {
    let made = vec![
        Row {
            engine: "claude-code".to_string(),
            mode: "file".to_string(),
            present: None,
            vars: Vec::new(),
            via: "~/.claude/.credentials.json".to_string(),
            message: "file only".to_string(),
            remedy: "name a variable".to_string(),
            usable_by: "primary".to_string(),
        },
        Row {
            engine: "opencode".to_string(),
            mode: "token".to_string(),
            present: Some(true),
            vars: vec!["ANTHROPIC_API_KEY".to_string()],
            via: "ANTHROPIC_API_KEY".to_string(),
            message: String::new(),
            remedy: String::new(),
            usable_by: String::new(),
        },
    ];
    assert_eq!(rows_of(&auth_rows(&made)), made);
}
