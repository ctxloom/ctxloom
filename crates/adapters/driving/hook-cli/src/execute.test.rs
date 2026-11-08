//! RED (port/run-one-shot): `hook <engine> execute <hook-name>` over a fake
//! application.

use std::cell::RefCell;

use setup_application::{HookAnswer, SessionBind};

use super::*;
use crate::{HookError, HookWarning};

const HUD_PAYLOAD: &str =
    r#"{"model":{"display_name":"Opus 5"},"context_window":{"used_percentage":42}}"#;

#[derive(Default)]
struct Service {
    answer: HookAnswer,
    refuse: bool,
    asked: RefCell<Vec<(String, String, String, String)>>,
}

impl HookService for Service {
    fn execute(
        &self,
        engine: &str,
        native: &str,
        payload: &str,
        harp: &str,
    ) -> Result<HookAnswer, Box<dyn Diagnostic>> {
        self.asked
            .borrow_mut()
            .push((engine.into(), native.into(), payload.into(), harp.into()));
        if self.refuse {
            return Err(Box::new(HookError::UnknownEngine {
                engine: engine.into(),
                known: "claude-code".into(),
            }));
        }
        Ok(self.answer.clone())
    }
}

#[derive(Default)]
struct Bound(Vec<(String, String, String)>);

impl Binding for Bound {
    fn bind(
        &mut self,
        harp: &str,
        native_id: &str,
        transcript: &str,
    ) -> Result<(), Box<dyn Diagnostic>> {
        self.0
            .push((harp.into(), native_id.into(), transcript.into()));
        Ok(())
    }
}

#[test]
fn the_statusline_is_rendered_here_and_the_application_is_not_asked() {
    let service = Service::default();
    let mut bound = Bound::default();
    let out = execute(
        "claude-code",
        "statusline",
        HUD_PAYLOAD,
        "brisk-copper-moth",
        &service,
        &mut bound,
    )
    .unwrap();
    assert!(
        out.stdout.contains("Opus 5") && out.stdout.contains("42%"),
        "{}",
        out.stdout
    );
    assert!(out.stdout.contains("brisk-copper-moth"));
    assert!(out.warnings.is_empty());
    assert!(service.asked.borrow().is_empty());
    assert!(bound.0.is_empty());
}

#[test]
fn a_statusline_that_does_not_parse_is_the_sentinel_never_an_error() {
    let mut bound = Bound::default();
    let out = execute(
        "claude-code",
        "statusline",
        "not json",
        "",
        &Service::default(),
        &mut bound,
    )
    .unwrap();
    assert_eq!(out.stdout, "ctxloom");
}

#[test]
fn any_other_name_is_the_applications_answer_for_that_engine() {
    let service = Service {
        answer: HookAnswer {
            stdout: "{}\n".into(),
            ..HookAnswer::default()
        },
        ..Service::default()
    };
    let mut bound = Bound::default();
    let out = execute(
        "claude-code",
        "Stop",
        "{\"x\":1}",
        "brisk-copper-moth",
        &service,
        &mut bound,
    )
    .unwrap();
    assert_eq!(out.stdout, "{}\n");
    assert_eq!(
        service.asked.borrow().as_slice(),
        [(
            "claude-code".to_string(),
            "Stop".to_string(),
            "{\"x\":1}".to_string(),
            "brisk-copper-moth".to_string()
        )]
    );
    assert!(bound.0.is_empty());
}

#[test]
fn a_bind_the_application_asks_for_is_made() {
    let service = Service {
        answer: HookAnswer {
            stdout: "{\"m\":1}\n".into(),
            bind: Some(SessionBind {
                harp: "brisk-copper-moth".into(),
                native_id: "vendor-42".into(),
                transcript: "/t/a.jsonl".into(),
            }),
            harp_missing: false,
        },
        ..Service::default()
    };
    let mut bound = Bound::default();
    let out = execute(
        "claude-code",
        "SessionStart",
        "{}",
        "brisk-copper-moth",
        &service,
        &mut bound,
    )
    .unwrap();
    assert_eq!(out.stdout, "{\"m\":1}\n");
    assert_eq!(
        bound.0,
        [(
            "brisk-copper-moth".into(),
            "vendor-42".into(),
            "/t/a.jsonl".into()
        )]
    );
}

#[test]
fn a_session_with_no_usable_harp_warns_and_emits_nothing() {
    let service = Service {
        answer: HookAnswer {
            stdout: "{\"notice\":1}\n".into(),
            bind: None,
            harp_missing: true,
        },
        ..Service::default()
    };
    let mut bound = Bound::default();
    let out = execute(
        "claude-code",
        "SessionStart",
        "{}",
        "",
        &service,
        &mut bound,
    )
    .unwrap();
    assert_eq!(out.stdout, "");
    assert_eq!(
        out.warnings,
        [HookWarning::NoUsableHarp {
            harp: String::new()
        }]
    );
    assert!(bound.0.is_empty());
}

#[test]
fn the_applications_refusal_is_returned_and_nothing_is_bound() {
    let service = Service {
        refuse: true,
        ..Service::default()
    };
    let mut bound = Bound::default();
    let err = execute("nosuch", "Stop", "{}", "x", &service, &mut bound).unwrap_err();
    assert_eq!(err.code(), "hook-cli-unknown-engine");
    assert!(bound.0.is_empty());
}
