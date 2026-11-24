//! `hook <engine> execute <hook-name>` over a fake
//! application.

use std::cell::RefCell;

use setup_application::{HookAnswer, Offered};

use super::*;
use crate::{HookError, HookWarning};

const HUD_PAYLOAD: &str =
    r#"{"model":{"display_name":"Opus 5"},"context_window":{"used_percentage":42}}"#;

#[derive(Default)]
struct Service {
    answer: HookAnswer,
    refuse: bool,
    asked: RefCell<Vec<(String, String, String, String)>>,
    /// Each ack received, and whether the service records it.
    acked: RefCell<Vec<Offered>>,
    ack_fails: bool,
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

    fn acknowledge(&self, offered: &Offered) -> bool {
        self.acked.borrow_mut().push(offered.clone());
        !self.ack_fails
    }
}

#[test]
fn the_statusline_is_rendered_here_and_the_application_is_not_asked() {
    let service = Service::default();
    let out = execute(
        "claude-code",
        "statusline",
        HUD_PAYLOAD,
        "brisk-copper-moth",
        &service,
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
}

#[test]
fn a_statusline_that_does_not_parse_is_the_sentinel_never_an_error() {
    let out = execute(
        "claude-code",
        "statusline",
        "not json",
        "",
        &Service::default(),
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
    let out = execute(
        "claude-code",
        "Stop",
        "{\"x\":1}",
        "brisk-copper-moth",
        &service,
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
}

#[test]
fn a_session_with_no_usable_harp_warns_and_emits_nothing() {
    let service = Service {
        answer: HookAnswer {
            stdout: "{\"notice\":1}\n".into(),
            harp_missing: true,
            event: None,
            offered: Offered::default(),
        },
        ..Service::default()
    };
    let out = execute("claude-code", "SessionStart", "{}", "", &service).unwrap();
    assert_eq!(out.stdout, "");
    assert_eq!(
        out.warnings,
        [HookWarning::NoUsableHarp {
            harp: String::new()
        }]
    );
}

#[test]
fn the_applications_refusal_is_returned() {
    let service = Service {
        refuse: true,
        ..Service::default()
    };
    let err = execute("nosuch", "Stop", "{}", "x", &service).unwrap_err();
    assert_eq!(err.code(), "hook-cli-unknown-engine");
}

fn offering() -> Service {
    Service {
        answer: HookAnswer {
            stdout: "{\"context\":1}\n".into(),
            offered: Offered {
                ids: vec!["m1".into()],
                wake: "n1".into(),
            },
            ..HookAnswer::default()
        },
        ..Service::default()
    }
}

// Message delivery (2026-11-19): executing a hook acknowledges NOTHING (the
// caller prints first); the outcome carries what to acknowledge, and the
// ack is a step of its own.
#[test]
fn an_outcome_carries_what_it_offered_and_executing_acknowledges_nothing() {
    let service = offering();
    let out = execute("claude-code", "UserPromptSubmit", "{}", "h", &service).unwrap();
    assert_eq!(out.offered, service.answer.offered);
    assert!(service.acked.borrow().is_empty());
    assert!(acknowledge(&out, &service));
    assert_eq!(
        *service.acked.borrow(),
        std::slice::from_ref(&service.answer.offered)
    );
}

#[test]
fn an_ack_the_service_did_not_record_is_reported() {
    let service = Service {
        ack_fails: true,
        ..offering()
    };
    let out = execute("claude-code", "UserPromptSubmit", "{}", "h", &service).unwrap();
    assert!(!acknowledge(&out, &service));
}

#[test]
fn an_outcome_that_offered_nothing_acknowledges_nothing() {
    let service = Service::default();
    let out = execute("claude-code", "Stop", "{}", "h", &service).unwrap();
    assert!(acknowledge(&out, &service));
    assert!(service.acked.borrow().is_empty());
}

/// A writer and a service that log into one list, to see their order.
struct Log(std::rc::Rc<RefCell<Vec<String>>>);

impl std::io::Write for Log {
    fn write(&mut self, bytes: &[u8]) -> std::io::Result<usize> {
        self.0
            .borrow_mut()
            .push(format!("write {}", String::from_utf8_lossy(bytes).trim()));
        Ok(bytes.len())
    }
    fn flush(&mut self) -> std::io::Result<()> {
        self.0.borrow_mut().push("flush".to_string());
        Ok(())
    }
}

struct Acking(std::rc::Rc<RefCell<Vec<String>>>);

impl HookService for Acking {
    fn execute(
        &self,
        _: &str,
        _: &str,
        _: &str,
        _: &str,
    ) -> Result<HookAnswer, Box<dyn Diagnostic>> {
        unreachable!("only the ack is asked")
    }
    fn acknowledge(&self, _: &Offered) -> bool {
        self.0.borrow_mut().push("ack".to_string());
        true
    }
}

// The order, forced: the answer is written, then flushed, and ONLY THEN
// acknowledged.
#[test]
fn the_ack_is_sent_after_the_answer_is_written_and_flushed() {
    let log = std::rc::Rc::new(RefCell::new(Vec::new()));
    let outcome = Outcome {
        stdout: "{\"context\":1}\n".into(),
        offered: Offered {
            ids: vec!["m1".into()],
            wake: String::new(),
        },
        ..Outcome::default()
    };
    let recorded = deliver(&outcome, &mut Log(log.clone()), &Acking(log.clone())).unwrap();
    assert!(recorded);
    assert_eq!(*log.borrow(), ["write {\"context\":1}", "flush", "ack"]);
}

// A hook whose answer could not be written sends no ack: its messages stay
// pending and are offered again.
#[test]
fn a_failed_write_sends_no_ack() {
    struct Broken;
    impl std::io::Write for Broken {
        fn write(&mut self, _: &[u8]) -> std::io::Result<usize> {
            Err(std::io::Error::other("closed"))
        }
        fn flush(&mut self) -> std::io::Result<()> {
            Ok(())
        }
    }
    struct FailsFlush;
    impl std::io::Write for FailsFlush {
        fn write(&mut self, bytes: &[u8]) -> std::io::Result<usize> {
            Ok(bytes.len())
        }
        fn flush(&mut self) -> std::io::Result<()> {
            Err(std::io::Error::other("closed"))
        }
    }
    let outcome = Outcome {
        stdout: "x".into(),
        offered: Offered {
            ids: vec!["m1".into()],
            wake: String::new(),
        },
        ..Outcome::default()
    };
    let log = std::rc::Rc::new(RefCell::new(Vec::new()));
    assert!(deliver(&outcome, &mut Broken, &Acking(log.clone())).is_err());
    assert!(deliver(&outcome, &mut FailsFlush, &Acking(log.clone())).is_err());
    assert!(log.borrow().is_empty(), "no ack: {:?}", log.borrow());
}

#[test]
fn the_statusline_entry_says_the_waiting_asks_and_records_nothing() {
    let out = statusline(HUD_PAYLOAD, "brisk-copper-moth", 2);
    assert!(out.stdout.contains("Opus 5"), "{}", out.stdout);
    assert!(
        out.stdout.contains("2 asks waiting: ctxloom top"),
        "{}",
        out.stdout
    );
    assert!(out.warnings.is_empty() && out.offered.is_empty());
}
