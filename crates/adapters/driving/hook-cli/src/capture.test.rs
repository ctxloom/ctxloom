//! Hook capture (PROPOSAL, design/hook-capture): the hook records what it
//! observed and fails open. Owner ruling 2026-11-08: a fact that cannot be
//! delivered is dropped and said so on stderr; the engine's answer and the
//! exit are what they were without recording.

use std::cell::RefCell;

use setup_application::HookAnswer;

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

/// Records nothing and says what it was asked, or says everything failed.
#[derive(Default)]
struct Recorder {
    fails: Vec<Unrecorded>,
    asked: RefCell<Vec<(String, String)>>,
}

impl Recording for Recorder {
    fn record(&self, native: &str, harp: &str, _answer: &HookAnswer) -> Vec<Unrecorded> {
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

fn not_recorded(what: &str) -> Unrecorded {
    NotRecorded {
        what: what.into(),
        why: "the listener is not running".into(),
    }
    .into()
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
    let plain = execute("claude-code", "Stop", "{}", "brisk-copper-moth", &service).unwrap();
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
fn the_statusline_records_nothing() {
    let recorder = Recorder::default();
    execute_recording(
        "claude-code",
        "statusline",
        "{}",
        "brisk-copper-moth",
        &Service::default(),
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
    let err = execute_recording("nosuch", "Stop", "{}", "h", &service, &recorder).unwrap_err();
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
    let out =
        execute_recording("claude-code", "SessionStart", "{}", "", &service, &recorder).unwrap();
    assert!(recorder.asked.borrow().is_empty());
    assert_eq!(
        out.warnings,
        [HookWarning::NoUsableHarp {
            harp: String::new()
        }]
    );
}

// With no harp there is no session to address: the recording is not asked, and
// nothing is warned (the answer is still the engine's).
#[test]
fn a_hook_with_an_empty_harp_is_not_recorded_and_warns_of_nothing() {
    let recorder = Recorder {
        fails: vec![not_recorded("x")],
        ..Recorder::default()
    };
    let out = execute_recording(
        "claude-code",
        "Stop",
        "{}",
        "",
        &answering("{}\n"),
        &recorder,
    )
    .unwrap();
    assert!(recorder.asked.borrow().is_empty());
    assert!(out.warnings.is_empty());
    assert_eq!(out.stdout, "{}\n");
}

// s4-Q14 (H2): a fact held for later is said as held, and one held behind
// its session's hold as held behind, each a warning on its own; the
// engine's answer and the exit are what they were.
#[test]
fn a_held_fact_is_said_as_held_and_never_changes_the_answer() {
    let service = answering("{}\n");
    let recorder = Recorder {
        fails: vec![
            Unrecorded::Held {
                what: "hook:prompt:s:p1".into(),
                harp: "brisk-copper-moth".into(),
                why: "no ctxloom daemon could be reached or started".into(),
            },
            Unrecorded::HeldBehind {
                what: "hook:turn-end:s:p1".into(),
                harp: "brisk-copper-moth".into(),
            },
        ],
        ..Recorder::default()
    };
    let out = execute_recording(
        "claude-code",
        "Stop",
        "{}",
        "brisk-copper-moth",
        &service,
        &recorder,
    )
    .unwrap();
    assert_eq!(out.stdout, "{}\n");
    assert_eq!(
        out.warnings,
        vec![
            HookWarning::Held {
                what: "hook:prompt:s:p1".into(),
                harp: "brisk-copper-moth".into(),
                why: "no ctxloom daemon could be reached or started".into(),
            },
            HookWarning::HeldBehind {
                what: "hook:turn-end:s:p1".into(),
                harp: "brisk-copper-moth".into(),
            },
        ]
    );
    assert_eq!(out.warnings[0].code(), "hook-cli-held");
    assert_eq!(out.warnings[1].code(), "hook-cli-held-behind");
}
