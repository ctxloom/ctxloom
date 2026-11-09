//! Hook capture (PROPOSAL, design/hook-capture): the hook records what it
//! observed and fails open. Owner ruling 2026-11-08: a fact that cannot be
//! delivered is dropped and said so on stderr; the engine's answer, the exit
//! and the bind are what they were without recording.

use std::cell::RefCell;

use setup_application::{HookAnswer, SessionBind};

use super::*;
use crate::{execute, HookError, HookWarning};

#[derive(Default)]
struct Service {
    answer: HookAnswer,
    refuse: bool,
    asked: RefCell<u32>,
}

impl HookService for Service {
    fn execute(
        &self,
        engine: &str,
        _native: &str,
        _payload: &str,
        _harp: &str,
    ) -> Result<HookAnswer, Box<dyn Diagnostic>> {
        *self.asked.borrow_mut() += 1;
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
struct Bound(Vec<String>);

impl Binding for Bound {
    fn bind(&mut self, harp: &str, _: &str, _: &str) -> Result<(), Box<dyn Diagnostic>> {
        self.0.push(harp.into());
        Ok(())
    }
}

/// Records nothing and says what it was asked, or says everything failed.
#[derive(Default)]
struct Recorder {
    fails: Vec<NotRecorded>,
    asked: RefCell<Vec<(String, String)>>,
}

impl Recording for Recorder {
    fn record(&self, native: &str, harp: &str, _answer: &HookAnswer) -> Vec<NotRecorded> {
        self.asked.borrow_mut().push((native.into(), harp.into()));
        self.fails.clone()
    }
}

fn answering(stdout: &str) -> Service {
    Service {
        answer: HookAnswer {
            stdout: stdout.into(),
            ..HookAnswer::default()
        },
        ..Service::default()
    }
}

fn not_recorded(what: &str) -> NotRecorded {
    NotRecorded {
        what: what.into(),
        why: "the listener is not running".into(),
    }
}

#[test]
fn the_hook_records_after_the_application_answers() {
    let service = answering("{}\n");
    let recorder = Recorder::default();
    let out = execute_recording(
        "claude-code",
        "UserPromptSubmit",
        "{}",
        "brisk-copper-moth",
        &service,
        &mut Bound::default(),
        &recorder,
    )
    .unwrap();
    assert_eq!(out.stdout, "{}\n");
    assert!(out.warnings.is_empty());
    assert_eq!(
        recorder.asked.borrow().as_slice(),
        [(
            "UserPromptSubmit".to_string(),
            "brisk-copper-moth".to_string()
        )]
    );
}

#[test]
fn a_failed_delivery_never_changes_the_engines_answer() {
    let service = answering("{\"decision\":\"x\"}\n");
    let plain = execute(
        "claude-code",
        "Stop",
        "{}",
        "brisk-copper-moth",
        &service,
        &mut Bound::default(),
    )
    .unwrap();
    let recorder = Recorder {
        fails: vec![not_recorded("hook:turn-end:s:p1")],
        ..Recorder::default()
    };
    let out = execute_recording(
        "claude-code",
        "Stop",
        "{}",
        "brisk-copper-moth",
        &service,
        &mut Bound::default(),
        &recorder,
    )
    .unwrap();
    assert_eq!(
        out.stdout, plain.stdout,
        "the same answer with or without recording"
    );
}

#[test]
fn each_unrecorded_observation_is_one_warning_on_the_diagnostic_channel() {
    let recorder = Recorder {
        fails: vec![not_recorded("hook:tool:s:a"), not_recorded("hook:tool:s:b")],
        ..Recorder::default()
    };
    let out = execute_recording(
        "claude-code",
        "PostToolBatch",
        "{}",
        "brisk-copper-moth",
        &answering("{}\n"),
        &mut Bound::default(),
        &recorder,
    )
    .unwrap();
    assert_eq!(
        out.warnings,
        [
            HookWarning::NotRecorded {
                what: "hook:tool:s:a".into(),
                why: "the listener is not running".into()
            },
            HookWarning::NotRecorded {
                what: "hook:tool:s:b".into(),
                why: "the listener is not running".into()
            },
        ]
    );
    assert!(
        !out.stdout.contains("hook:tool"),
        "stdout is the engine's only"
    );
}

#[test]
fn a_bind_is_still_made_when_recording_fails() {
    let service = Service {
        answer: HookAnswer {
            stdout: "{}\n".into(),
            bind: Some(SessionBind {
                harp: "brisk-copper-moth".into(),
                native_id: "n1".into(),
                transcript: "/t".into(),
            }),
            ..HookAnswer::default()
        },
        ..Service::default()
    };
    let recorder = Recorder {
        fails: vec![not_recorded("x")],
        ..Recorder::default()
    };
    let mut bound = Bound::default();
    execute_recording(
        "claude-code",
        "SessionStart",
        "{}",
        "brisk-copper-moth",
        &service,
        &mut bound,
        &recorder,
    )
    .unwrap();
    assert_eq!(bound.0, ["brisk-copper-moth"]);
}

#[test]
fn the_statusline_records_nothing() {
    let recorder = Recorder::default();
    execute_recording(
        "claude-code",
        "statusline",
        "{}",
        "brisk-copper-moth",
        &Service::default(),
        &mut Bound::default(),
        &recorder,
    )
    .unwrap();
    assert!(recorder.asked.borrow().is_empty());
}

#[test]
fn a_refused_hook_records_nothing_and_is_refused_as_before() {
    let service = Service {
        refuse: true,
        ..Service::default()
    };
    let recorder = Recorder::default();
    let err = execute_recording(
        "nosuch",
        "Stop",
        "{}",
        "h",
        &service,
        &mut Bound::default(),
        &recorder,
    )
    .unwrap_err();
    assert_eq!(err.code(), "hook-cli-unknown-engine");
    assert!(recorder.asked.borrow().is_empty());
}

// With no usable harp there is no session to address.
#[test]
fn a_hook_with_no_usable_harp_records_nothing() {
    let service = Service {
        answer: HookAnswer {
            harp_missing: true,
            ..HookAnswer::default()
        },
        ..Service::default()
    };
    let recorder = Recorder::default();
    let out = execute_recording(
        "claude-code",
        "SessionStart",
        "{}",
        "",
        &service,
        &mut Bound::default(),
        &recorder,
    )
    .unwrap();
    assert!(recorder.asked.borrow().is_empty());
    assert_eq!(
        out.warnings,
        [HookWarning::NoUsableHarp {
            harp: String::new()
        }]
    );
}
