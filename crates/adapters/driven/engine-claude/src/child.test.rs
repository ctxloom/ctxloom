//! A child runs a fake claude (`fixture_program::install_program`) that
//! speaks the probe's stream-json: a system init, the echo of each message
//! given, a result. A message holding `LONG` starts a turn that streams a
//! delta and then waits for the next line on its input, so a test sends its
//! interrupt or its mid-turn message exactly while that turn is in flight,
//! told so by the turn's own `TurnStarted`, never by a sleep.

use std::collections::BTreeMap;
use std::time::Duration;

use file_system::{Fs, Rooted};
use run_ports::{
    ChildEngine, ChildError, ChildMessage, ChildObservation, ChildSpec, EngineSettings, Exit,
    FailureClass, Queued, StartedChild, TurnOutcome, Urgency,
};

use crate::child::child_command;
use crate::Claude;

/// The fake claude. Its session id is `S1`, `S2` after a `/clear`; it
/// writes every line it is given to `input.log` beside it.
const STUB: &str = r#"#!/bin/sh
here=$(dirname "$0")
sid=S1
printf '{"type":"system","subtype":"init","cwd":"/work","session_id":"%s","tools":[]}\n' "$sid"
while IFS= read -r line; do
  printf '%s\n' "$line" >> "$here/input.log"
  case "$line" in
    *'"control_request"'*)
      printf '{"type":"control_response","response":{"subtype":"success","request_id":"ctxloom-interrupt-1","response":{"still_queued":[]}}}\n'
      printf '{"type":"user","message":{"role":"user","content":[{"type":"text","text":"[Request interrupted by user]"}]},"session_id":"%s"}\n' "$sid"
      printf '{"type":"result","subtype":"error_during_execution","is_error":true,"terminal_reason":"aborted_streaming","session_id":"%s"}\n' "$sid"
      ;;
    *'"content":"/clear"'*)
      sid=S2
      printf '{"type":"conversation_reset","new_conversation_id":"c2","trigger":"clear","session_id":"S1"}\n'
      printf '{"type":"system","subtype":"init","cwd":"/work","session_id":"%s","tools":[]}\n' "$sid"
      printf '{"type":"result","subtype":"success","is_error":false,"num_turns":0,"result":"","session_id":"%s"}\n' "$sid"
      ;;
    *EXIT*)
      text=$(printf '%s' "$line" | sed 's/.*"content":"\([^"]*\)".*/\1/')
      printf '{"type":"user","message":{"role":"user","content":"%s"},"session_id":"%s","isReplay":true}\n' "$text" "$sid"
      echo "the engine fell over" >&2
      exit 7
      ;;
    *)
      text=$(printf '%s' "$line" | sed 's/.*"content":"\([^"]*\)".*/\1/')
      printf '{"type":"system","subtype":"init","cwd":"/work","session_id":"%s","tools":[]}\n' "$sid"
      printf '{"type":"user","message":{"role":"user","content":"%s"},"session_id":"%s","isReplay":true}\n' "$text" "$sid"
      case "$text" in
        *LONG*)
          printf '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"one\\ntwo\\n"}},"session_id":"%s"}\n' "$sid"
          ;;
        *)
          printf '{"type":"result","subtype":"success","is_error":false,"result":"re: %s","session_id":"%s"}\n' "$text" "$sid"
          ;;
      esac
      ;;
  esac
done
"#;

/// A fake claude that never ends on its input's end.
const DEAF: &str = "#!/bin/sh\nprintf '{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"D\"}\\n'\nwhile :; do read -r line || sleep 1000; done\n";

struct Running {
    // The fake claude's directory: its input log is there.
    dir: Rooted,
    child: StartedChild,
}

fn spec(program: &str) -> ChildSpec {
    ChildSpec {
        home: "/nowhere/home".to_string(),
        context_path: "/nowhere/home/ctxloom-context.md".to_string(),
        env: BTreeMap::from([("PATH".to_string(), "/usr/bin:/bin".to_string())]),
        cwd: None,
        model: String::new(),
        effort: String::new(),
        settings: EngineSettings {
            command: program.to_string(),
            ..EngineSettings::default()
        },
        resume: None,
        session_id: None,
    }
}

fn run(body: &str) -> Running {
    let dir = Rooted::temp().unwrap();
    let path = fixture_program::install_program(&dir, &os::fixture("/claude"), body).unwrap();
    let child = Claude::new()
        .start(&spec(path.to_str().unwrap()))
        .expect("the fake claude starts");
    Running { dir, child }
}

fn message(id: &str, text: &str) -> ChildMessage {
    ChildMessage::new(id, text)
}

impl Running {
    /// The next observation.
    fn next(&mut self) -> ChildObservation {
        self.child
            .observations
            .next()
            .expect("the child says more before it exits")
    }

    /// The observations up to and including the first `until` accepts.
    fn until(&mut self, until: impl Fn(&ChildObservation) -> bool) -> Vec<ChildObservation> {
        let mut seen = Vec::new();
        loop {
            let observation = self.next();
            let done = until(&observation);
            seen.push(observation);
            if done {
                return seen;
            }
        }
    }

    /// What the fake claude was given, a line each.
    fn input(&self) -> Vec<String> {
        self.dir
            .read_to_string(&os::fixture("/input.log"))
            .unwrap_or_default()
            .lines()
            .map(String::from)
            .collect()
    }
}

fn ended<'a>(ids: &'a [&'a str]) -> impl Fn(&ChildObservation) -> bool + 'a {
    move |o| {
        matches!(o, ChildObservation::TurnEnded { message_ids, .. }
            if message_ids.iter().map(String::as_str).eq(ids.iter().copied()))
    }
}

fn answered(id: &str, answer: &str, native: &str) -> ChildObservation {
    ChildObservation::TurnEnded {
        message_ids: vec![id.to_string()],
        outcome: TurnOutcome::Answered {
            answer: answer.to_string(),
            native_id: Some(native.to_string()),
        },
    }
}

fn started(id: &str) -> ChildObservation {
    ChildObservation::TurnStarted {
        message_ids: vec![id.to_string()],
    }
}

#[test]
fn the_child_command_streams_both_ways_and_echoes_what_it_is_given() {
    let command = child_command(&spec("")).unwrap();
    assert_eq!(command.program, "claude");
    assert_eq!(
        command.args,
        vec![
            "-p",
            "--input-format",
            "stream-json",
            "--output-format",
            "stream-json",
            "--verbose",
            "--replay-user-messages",
            "--append-system-prompt-file",
            "/nowhere/home/ctxloom-context.md",
            "--settings",
            "/nowhere/home/settings.json",
        ]
    );
    assert!(command.stdin.is_empty());
    assert_eq!(
        command.env.get("CLAUDE_CONFIG_DIR").map(String::as_str),
        Some("/nowhere/home")
    );
}

#[test]
fn the_child_command_resumes_or_starts_a_given_session_and_never_both() {
    let mut fresh = spec("");
    fresh.session_id = Some("fresh-1".to_string());
    let args = child_command(&fresh).unwrap().args;
    let at = args.iter().position(|a| a == "--session-id").unwrap();
    assert_eq!(args[at + 1], "fresh-1");
    fresh.resume = Some("native-1".to_string());
    let args = child_command(&fresh).unwrap().args;
    let at = args.iter().position(|a| a == "--resume").unwrap();
    assert_eq!(args[at + 1], "native-1");
    assert!(!args.contains(&"--session-id".to_string()), "{args:?}");
}

#[test]
fn the_child_command_takes_a_childs_environment_and_its_model() {
    let mut asked = spec("");
    asked.env.insert(
        "ANTHROPIC_AUTH_TOKEN".to_string(),
        "primary-only".to_string(),
    );
    asked
        .env
        .insert("CLAUDE_CODE_OAUTH_TOKEN".to_string(), "child".to_string());
    asked.env.insert("SECRET_ELSE".to_string(), "x".to_string());
    asked.model = "haiku".to_string();
    asked.effort = "low".to_string();
    let command = child_command(&asked).unwrap();
    assert!(!command.env.contains_key("ANTHROPIC_AUTH_TOKEN"));
    assert!(!command.env.contains_key("SECRET_ELSE"));
    assert!(command.env.contains_key("CLAUDE_CODE_OAUTH_TOKEN"));
    let model = command.args.iter().position(|a| a == "--model").unwrap();
    assert_eq!(command.args[model + 1], "haiku");
    asked.effort = "extreme".to_string();
    assert!(matches!(
        Claude::new().start(&asked),
        Err(ChildError::Engine(_))
    ));
}

#[test]
fn a_child_carries_many_turns_in_one_process() {
    let mut running = run(STUB);
    assert_eq!(
        running.next(),
        ChildObservation::Started {
            native_id: "S1".to_string()
        }
    );
    let control = &running.child.control;
    assert_eq!(
        control
            .send(message("m-1", "first"), Urgency::Wake)
            .unwrap(),
        Queued::Written
    );
    let seen = running.until(ended(&["m-1"]));
    assert_eq!(
        seen,
        vec![started("m-1"), answered("m-1", "re: first", "S1")]
    );
    running
        .child
        .control
        .send(message("m-2", "second"), Urgency::Wake)
        .unwrap();
    let seen = running.until(ended(&["m-2"]));
    assert_eq!(
        seen,
        vec![started("m-2"), answered("m-2", "re: second", "S1")]
    );
    let pid = running.child.control.pid();
    assert!(pid > 0);
    let ended = running.child.control.end(Duration::from_secs(10)).unwrap();
    assert_eq!(
        ended.exit,
        Exit {
            code: Some(0),
            signal: None
        }
    );
    assert!(ended.undelivered.is_empty());
    assert!(matches!(
        running
            .until(|o| matches!(o, ChildObservation::Exited { .. }))
            .as_slice(),
        [ChildObservation::Exited {
            exit: Exit { code: Some(0), .. },
            ..
        }]
    ));
    assert!(running.child.observations.next().is_none());
    assert_eq!(
        running
            .child
            .control
            .send(message("m-3", "third"), Urgency::Wake),
        Err(ChildError::Closed)
    );
}

#[test]
fn a_steer_during_a_delta_interrupts_and_overtakes_a_held_wake() {
    let mut running = run(STUB);
    running.next();
    running
        .child
        .control
        .send(message("m-1", "LONG count"), Urgency::Wake)
        .unwrap();
    // The turn is in flight: its echo is heard, and the fake claude is
    // waiting on its input after streaming a delta.
    assert_eq!(running.next(), started("m-1"));
    assert_eq!(
        running
            .child
            .control
            .send(message("m-2", "held"), Urgency::Wake)
            .unwrap(),
        Queued::Held
    );
    assert_eq!(
        running
            .child
            .control
            .send(message("m-3", "steered"), Urgency::Steer)
            .unwrap(),
        Queued::Interrupting
    );
    let seen = running.until(ended(&["m-2"]));
    assert_eq!(
        seen,
        vec![
            ChildObservation::InterruptAnswered { accepted: true },
            ChildObservation::TurnEnded {
                message_ids: vec!["m-1".to_string()],
                outcome: TurnOutcome::Failed {
                    class: FailureClass::Interrupted,
                    detail: "the turn was interrupted (aborted_streaming)".to_string(),
                    native_id: Some("S1".to_string()),
                },
            },
            started("m-3"),
            answered("m-3", "re: steered", "S1"),
            started("m-2"),
            answered("m-2", "re: held", "S1"),
        ]
    );
    // The fake claude was never given the held wake mid-turn.
    let input = running.input();
    assert_eq!(input.len(), 4, "{input:?}");
    assert!(input[0].contains("LONG count"));
    assert!(input[1].contains("control_request"));
    assert!(input[2].contains("steered"));
    assert!(input[3].contains("held"));
}

#[test]
fn a_clear_rotates_the_native_id() {
    let mut running = run(STUB);
    running.next();
    running
        .child
        .control
        .send(message("m-1", "remember"), Urgency::Wake)
        .unwrap();
    running.until(ended(&["m-1"]));
    assert_eq!(running.child.control.clear().unwrap(), Queued::Written);
    assert_eq!(
        running.next(),
        ChildObservation::Cleared {
            native_id: Some("S2".to_string())
        }
    );
    running
        .child
        .control
        .send(message("m-2", "recall"), Urgency::Wake)
        .unwrap();
    assert_eq!(
        running.until(ended(&["m-2"])),
        vec![started("m-2"), answered("m-2", "re: recall", "S2")]
    );
}

#[test]
fn an_end_mid_turn_interrupts_it_and_answers_what_was_held() {
    let mut running = run(STUB);
    running.next();
    running
        .child
        .control
        .send(message("m-1", "LONG work"), Urgency::Wake)
        .unwrap();
    assert_eq!(running.next(), started("m-1"));
    running
        .child
        .control
        .send(message("m-2", "later"), Urgency::Wake)
        .unwrap();
    let ended = running.child.control.end(Duration::from_secs(10)).unwrap();
    assert_eq!(ended.undelivered, vec!["m-2".to_string()]);
    assert_eq!(ended.exit.code, Some(0));
    let rest: Vec<ChildObservation> = running.child.observations.by_ref().collect();
    assert!(
        rest.iter().any(|o| matches!(
            o,
            ChildObservation::TurnEnded {
                outcome: TurnOutcome::Failed {
                    class: FailureClass::Interrupted,
                    ..
                },
                ..
            }
        )),
        "{rest:?}"
    );
    assert!(matches!(rest.last(), Some(ChildObservation::Exited { .. })));
    assert_eq!(
        running.child.control.end(Duration::from_secs(1)),
        Err(ChildError::Closed)
    );
}

#[test]
fn an_engine_that_exits_mid_turn_fails_the_turn_with_what_it_said() {
    let mut running = run(STUB);
    running.next();
    running
        .child
        .control
        .send(message("m-1", "EXIT now"), Urgency::Wake)
        .unwrap();
    assert_eq!(running.next(), started("m-1"));
    let seen = running.until(|o| matches!(o, ChildObservation::Exited { .. }));
    assert_eq!(
        seen,
        vec![
            ChildObservation::TurnEnded {
                message_ids: vec!["m-1".to_string()],
                outcome: TurnOutcome::Failed {
                    class: FailureClass::EngineError,
                    detail: "the engine exited mid-turn: the engine fell over".to_string(),
                    native_id: Some("S1".to_string()),
                },
            },
            ChildObservation::Exited {
                exit: Exit {
                    code: Some(7),
                    signal: None
                },
                detail: "the engine fell over".to_string(),
            },
        ]
    );
    assert!(running.child.observations.next().is_none());
    assert_eq!(
        running
            .child
            .control
            .send(message("m-2", "again"), Urgency::Wake),
        Err(ChildError::Closed)
    );
}

#[test]
fn an_engine_deaf_to_its_input_end_is_stopped_after_the_grace() {
    let mut running = run(DEAF);
    assert_eq!(
        running.next(),
        ChildObservation::Started {
            native_id: "D".to_string()
        }
    );
    let ended = running
        .child
        .control
        .end(Duration::from_millis(200))
        .unwrap();
    assert_eq!(ended.exit.code, None, "{ended:?}");
    assert!(ended.exit.signal.is_some(), "{ended:?}");
}

#[test]
fn a_missing_program_starts_nothing() {
    let dir = Rooted::temp().unwrap();
    let missing = fixture_program::install_program(&dir, &os::fixture("/claude"), "")
        .unwrap()
        .with_file_name("no-claude");
    let error = Claude::new()
        .start(&spec(missing.to_str().unwrap()))
        .unwrap_err();
    assert!(
        matches!(&error, ChildError::NotStarted { deterministic: true, program, .. }
            if program.ends_with("no-claude")),
        "{error:?}"
    );
}

#[test]
fn a_dropped_control_closes_the_engines_input_and_it_exits() {
    let mut running = run(STUB);
    running.next();
    let StartedChild {
        control,
        observations,
    } = running.child;
    drop(control);
    let rest: Vec<ChildObservation> = observations.collect();
    assert!(
        matches!(
            rest.as_slice(),
            [ChildObservation::Exited {
                exit: Exit { code: Some(0), .. },
                ..
            }]
        ),
        "{rest:?}"
    );
}
