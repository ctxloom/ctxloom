//! A child runs a fake `opencode acp` (`fixture_program::install_program`)
//! that speaks the spike's JSON-RPC lines: `initialize`, `session/new` (S1,
//! then S2 ...), `session/load`, `session/set_model`, and a `session/prompt`
//! answered by an agent message chunk and its `end_turn` reply. A prompt
//! holding `LONG` stays in flight until a cancel (every pending prompt
//! replies `cancelled`) or another prompt (the JOIN: both reply `end_turn`
//! with the joined turn's answer), so a test sends its interrupt or its
//! mid-turn message while that turn is in flight, told so by the turn's own
//! `TurnStarted` and by the fake's input, never by a sleep. A prompt holding
//! `PERMIT` asks a permission first and answers with the option chosen.

use std::collections::BTreeMap;
use std::time::Duration;

use agent_client_protocol::v1::schema::{
    RequestPermissionOutcome, RequestPermissionRequest, SelectedPermissionOutcome,
    SessionNotification, StopReason,
};
use file_system::{Fs, Rooted};
use run_ports::{
    ChildEngine, ChildError, ChildMessage, ChildObservation, ChildPermissions, ChildSpec,
    EngineSettings, Exit, FailureClass, Queued, StartedChild, TurnOutcome, Urgency,
};

use crate::child::{
    child_command, detail_of, permission_answer, said_of, stop_of, PASSWORD_VARIABLE,
};
use crate::conversation::{Heard, Stop};
use crate::Opencode;

/// The spike's own lines (opencode 1.18.4 over ACP, the paths made up).
const SPIKE: &str = include_str!("fixtures/acp_spike.jsonl");

/// The fake `opencode acp`. It writes every line it is given to
/// `input.log` beside it, its pid to `pid`.
const STUB: &str = r#"#!/bin/sh
here=$(dirname "$0")
printf '%s' "$$" > "$here/pid"
n=0
sid=
pending=
field() { printf '%s' "$2" | sed -n "s/^.*\"$1\":\"\([^\"]*\)\".*\$/\1/p"; }
reply() { printf '{"jsonrpc":"2.0","id":%s,"result":%s}\n' "$1" "$2"; }
chunk() { printf '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"%s","update":{"sessionUpdate":"agent_message_chunk","messageId":"%s","content":{"type":"text","text":"%s"}}}}\n' "$sid" "$1" "$2"; }
while IFS= read -r line; do
  printf '%s\n' "$line" >> "$here/input.log"
  id=$(printf '%s' "$line" | sed -n 's/^.*"id":\("[^"]*"\|[0-9][0-9]*\).*$/\1/p')
  case "$(field method "$line")" in
    initialize) reply "$id" '{"protocolVersion":1,"agentCapabilities":{"loadSession":true}}' ;;
    session/new) n=$((n+1)); sid="S$n"; reply "$id" "{\"sessionId\":\"$sid\"}" ;;
    session/load) sid=$(field sessionId "$line"); chunk msg_old "from before"; reply "$id" '{}' ;;
    session/set_model) reply "$id" '{}' ;;
    session/cancel)
      for p in $pending; do reply "$p" '{"stopReason":"cancelled"}'; done
      pending= ;;
    session/prompt)
      text=$(field text "$line")
      case "$text" in
        *LONG*) pending="$pending $id"; chunk msg_long "working" ;;
        *EXIT*) echo "the engine fell over" >&2; exit 7 ;;
        *PERMIT*)
          printf '{"jsonrpc":"2.0","id":"perm-1","method":"session/request_permission","params":{"sessionId":"%s","toolCall":{"toolCallId":"t1"},"options":[{"optionId":"always","name":"Always allow","kind":"allow_always"},{"optionId":"once","name":"Allow once","kind":"allow_once"},{"optionId":"no","name":"Reject","kind":"reject_once"}]}}\n' "$sid"
          IFS= read -r answer
          printf '%s\n' "$answer" >> "$here/input.log"
          chosen=$(field optionId "$answer")
          chunk msg_permit "chose ${chosen:-nothing}"
          reply "$id" '{"stopReason":"end_turn"}' ;;
        *)
          chunk msg_reply "re: $text"
          for p in $pending $id; do reply "$p" '{"stopReason":"end_turn"}'; done
          pending= ;;
      esac ;;
  esac
done
"#;

/// A fake opencode that opens, then never ends on its input's end.
const DEAF: &str = r#"#!/bin/sh
while :; do
  if IFS= read -r line; then
    id=$(printf '%s' "$line" | sed -n 's/^.*"id":\("[^"]*"\|[0-9][0-9]*\).*$/\1/p')
    case "$line" in
      *'"initialize"'*) printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1}}\n' "$id" ;;
      *'"session/new"'*) printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"D"}}\n' "$id" ;;
    esac
  else
    sleep 1000
  fi
done
"#;

struct Running {
    // The fake opencode's directory: its input log is there.
    dir: Rooted,
    child: StartedChild,
}

fn spec(program: &str) -> ChildSpec {
    ChildSpec {
        home: "/nowhere/home".to_string(),
        context_path: "/nowhere/home/AGENTS.md".to_string(),
        env: BTreeMap::from([("PATH".to_string(), "/usr/bin:/bin".to_string())]),
        user_home: None,
        cwd: None,
        model: String::new(),
        effort: String::new(),
        settings: EngineSettings {
            command: program.to_string(),
            ..EngineSettings::default()
        },
        resume: None,
        session_id: None,
        permissions: ChildPermissions::default(),
    }
}

fn run_with(body: &str, adjust: impl FnOnce(&mut ChildSpec)) -> Running {
    let dir = Rooted::temp().unwrap();
    let path = fixture_program::install_program(&dir, &os::fixture("/opencode"), body).unwrap();
    let mut spec = spec(path.to_str().unwrap());
    adjust(&mut spec);
    let child = Opencode::new()
        .start(&spec)
        .expect("the fake opencode starts");
    Running { dir, child }
}

fn run(body: &str) -> Running {
    run_with(body, |_| {})
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

    /// What the fake opencode was given, a line each.
    fn input(&self) -> Vec<String> {
        self.dir
            .read_to_string(&os::fixture("/input.log"))
            .unwrap_or_default()
            .lines()
            .map(String::from)
            .collect()
    }

    /// The prompts the fake was given, by their text.
    fn prompts(&self) -> Vec<String> {
        self.input()
            .into_iter()
            .filter(|line| line.contains("\"session/prompt\""))
            .collect()
    }

    fn send(&self, id: &str, text: &str, urgency: Urgency) -> Queued {
        self.child
            .control
            .send(message(id, text), urgency)
            .expect("the child takes it")
    }
}

fn ended<'a>(ids: &'a [&'a str]) -> impl Fn(&ChildObservation) -> bool + 'a {
    move |o| {
        matches!(o, ChildObservation::TurnEnded { message_ids, .. }
            if message_ids.iter().map(String::as_str).eq(ids.iter().copied()))
    }
}

fn answered(ids: &[&str], answer: &str, native: &str) -> ChildObservation {
    ChildObservation::TurnEnded {
        message_ids: ids.iter().map(|i| i.to_string()).collect(),
        outcome: TurnOutcome::Answered {
            answer: answer.to_string(),
            native_id: Some(native.to_string()),
        },
    }
}

fn started(ids: &[&str]) -> ChildObservation {
    ChildObservation::TurnStarted {
        message_ids: ids.iter().map(|i| i.to_string()).collect(),
    }
}

fn opened(native: &str) -> ChildObservation {
    ChildObservation::Started {
        native_id: native.to_string(),
    }
}

#[test]
fn the_child_command_is_acp_on_a_loopback_listener_with_a_password() {
    let mut asked = spec("");
    asked.cwd = Some("/work/tree".to_string());
    asked
        .env
        .insert(PASSWORD_VARIABLE.to_string(), "the-callers-own".to_string());
    asked
        .env
        .insert("OPENCODE_PURE".to_string(), "1".to_string());
    asked.model = "opencode/big-pickle".to_string();
    let command = child_command(&asked, "pw-1").unwrap();
    assert_eq!(command.program, "opencode");
    assert_eq!(
        command.args,
        vec![
            "acp",
            "--cwd",
            "/work/tree",
            "--hostname",
            "127.0.0.1",
            "--port",
            "0"
        ]
    );
    assert!(!command.args.iter().any(|a| a == "--mdns" || a == "--pure"));
    assert!(command.stdin.is_empty());
    assert_eq!(command.cwd.as_deref(), Some("/work/tree"));
    // The child's own password, never the caller's; no OPENCODE_PURE.
    assert_eq!(
        command.env.get(PASSWORD_VARIABLE).map(String::as_str),
        Some("pw-1")
    );
    assert!(!command.env.contains_key("OPENCODE_PURE"));
    // The relocation of a turn.
    assert_eq!(
        command.env.get("OPENCODE_CONFIG_DIR").map(String::as_str),
        Some("/nowhere/home")
    );
    assert_eq!(
        command.env.get("HOME").map(String::as_str),
        Some("/nowhere/home")
    );
    assert_eq!(
        command.env.get("XDG_DATA_HOME").map(String::as_str),
        Some("/nowhere/home/xdg/data")
    );
    // A logged command shows the password's name, never its value.
    let logged = format!("{command:?}");
    assert!(logged.contains(PASSWORD_VARIABLE), "{logged}");
    assert!(!logged.contains("pw-1"), "{logged}");
    // No worktree: the child works in the session home.
    assert_eq!(
        child_command(&spec(""), "pw").unwrap().args[2],
        "/nowhere/home"
    );
}

#[test]
fn a_host_home_with_no_home_starts_nothing() {
    let mut asked = spec("");
    asked.settings.session_home = run_ports::SessionHome::Host;
    assert!(matches!(
        Opencode::new().start(&asked),
        Err(ChildError::Engine(_))
    ));
}

#[test]
fn every_child_gets_a_password_of_its_own() {
    let first = super::password().unwrap();
    let second = super::password().unwrap();
    assert_eq!(first.len(), 64);
    assert!(first.chars().all(|c| c.is_ascii_hexdigit()), "{first}");
    assert_ne!(first, second);
}

#[test]
fn the_spikes_updates_keep_the_agents_words_alone() {
    let mut said = Vec::new();
    let mut stops = Vec::new();
    for line in SPIKE.lines() {
        let message: serde_json::Value = serde_json::from_str(line).unwrap();
        if message["method"] == "session/update" {
            let notification: SessionNotification =
                serde_json::from_value(message["params"].clone()).unwrap();
            if let Some(heard) = said_of(notification) {
                said.push(heard);
            }
        } else if let Some(reason) = message["result"].get("stopReason") {
            let reason: StopReason = serde_json::from_value(reason.clone()).unwrap();
            stops.push(stop_of(reason));
        }
    }
    // Thoughts, tool calls and usage are not words.
    assert_eq!(
        said.first(),
        Some(&Heard::Said {
            message_id: Some("msg_1c7772a92001HKj7gKGyHYViwz".to_string()),
            text: "acp-ok".to_string()
        })
    );
    assert_eq!(said.len(), 5, "{said:?}");
    assert_eq!(stops, vec![Stop::EndTurn, Stop::Cancelled]);
}

#[test]
fn every_stop_reason_is_named() {
    for (reason, stop) in [
        (StopReason::EndTurn, Stop::EndTurn),
        (StopReason::MaxTokens, Stop::MaxTokens),
        (StopReason::MaxTurnRequests, Stop::MaxTurnRequests),
        (StopReason::Refusal, Stop::Refusal),
        (StopReason::Cancelled, Stop::Cancelled),
    ] {
        assert_eq!(stop_of(reason), stop);
    }
}

#[test]
fn a_permission_is_answered_once_by_the_posture_else_cancelled() {
    let request: RequestPermissionRequest = serde_json::from_str(
        r#"{"sessionId":"S1","toolCall":{"toolCallId":"t1"},"options":[
            {"optionId":"always","name":"Always","kind":"allow_always"},
            {"optionId":"once","name":"Once","kind":"allow_once"},
            {"optionId":"never","name":"Never","kind":"reject_always"},
            {"optionId":"no","name":"No","kind":"reject_once"}]}"#,
    )
    .unwrap();
    let selected = |id: &str| {
        RequestPermissionOutcome::Selected(SelectedPermissionOutcome::new(id.to_string()))
    };
    assert_eq!(
        permission_answer(&request, ChildPermissions::Allow),
        selected("once")
    );
    assert_eq!(
        permission_answer(&request, ChildPermissions::Refuse),
        selected("no")
    );
    let always_only: RequestPermissionRequest = serde_json::from_str(
        r#"{"sessionId":"S1","toolCall":{"toolCallId":"t1"},"options":[
            {"optionId":"always","name":"Always","kind":"allow_always"},
            {"optionId":"never","name":"Never","kind":"reject_always"}]}"#,
    )
    .unwrap();
    for posture in [ChildPermissions::Allow, ChildPermissions::Refuse] {
        assert_eq!(
            permission_answer(&always_only, posture),
            RequestPermissionOutcome::Cancelled
        );
    }
}

#[test]
fn a_child_carries_many_turns_in_one_process() {
    let mut running = run_with(STUB, |spec| spec.model = "opencode/big-pickle".to_string());
    assert_eq!(running.next(), opened("S1"));
    assert_eq!(running.send("m-1", "first", Urgency::Wake), Queued::Written);
    assert_eq!(
        running.until(ended(&["m-1"])),
        vec![started(&["m-1"]), answered(&["m-1"], "re: first", "S1")]
    );
    running.send("m-2", "second", Urgency::Wake);
    assert_eq!(
        running.until(ended(&["m-2"])),
        vec![started(&["m-2"]), answered(&["m-2"], "re: second", "S1")]
    );
    let input = running.input();
    assert!(input[0].contains("\"initialize\""), "{input:?}");
    assert!(input[0].contains("\"terminal\":false"), "{input:?}");
    assert!(input[1].contains("\"session/new\""), "{input:?}");
    assert!(
        input[2].contains("\"session/set_model\"")
            && input[2].contains("\"modelId\":\"opencode/big-pickle\""),
        "{input:?}"
    );
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
    // The end-of-input marker is never written.
    assert!(!running.input().iter().any(|l| l.contains("_ctxloom")));
    assert_eq!(
        running
            .child
            .control
            .send(message("m-3", "third"), Urgency::Wake),
        Err(ChildError::Closed)
    );
}

#[test]
fn a_steer_mid_turn_joins_it_and_a_held_wake_follows() {
    let mut running = run(STUB);
    running.next();
    running.send("m-1", "LONG count", Urgency::Wake);
    assert_eq!(running.next(), started(&["m-1"]));
    assert_eq!(running.send("m-2", "held", Urgency::Wake), Queued::Held);
    assert_eq!(
        running.send("m-3", "steered", Urgency::Steer),
        Queued::Written
    );
    assert_eq!(
        running.until(ended(&["m-2"])),
        vec![
            started(&["m-3"]),
            answered(&["m-1"], "re: steered", "S1"),
            answered(&["m-3"], "re: steered", "S1"),
            started(&["m-2"]),
            answered(&["m-2"], "re: held", "S1"),
        ]
    );
    // The fake opencode was never given the held wake mid-turn.
    let prompts = running.prompts();
    assert_eq!(prompts.len(), 3, "{prompts:?}");
    assert!(prompts[0].contains("LONG count"));
    assert!(prompts[1].contains("steered"));
    assert!(prompts[2].contains("held"));
}

#[test]
fn an_interrupt_cancels_the_turn_and_a_steer_after_it_is_the_next_turn() {
    let mut running = run(STUB);
    running.next();
    assert!(!running.child.control.interrupt().unwrap());
    running.send("m-1", "LONG work", Urgency::Wake);
    assert_eq!(running.next(), started(&["m-1"]));
    running.send("m-2", "held", Urgency::Wake);
    assert!(running.child.control.interrupt().unwrap());
    assert!(!running.child.control.interrupt().unwrap());
    assert_eq!(
        running.send("m-3", "instead", Urgency::Steer),
        Queued::Interrupting
    );
    assert_eq!(
        running.until(ended(&["m-2"])),
        vec![
            ChildObservation::InterruptAnswered { accepted: true },
            ChildObservation::TurnEnded {
                message_ids: vec!["m-1".to_string()],
                outcome: TurnOutcome::Failed {
                    class: FailureClass::Interrupted,
                    detail: "the turn was cancelled".to_string(),
                    native_id: Some("S1".to_string()),
                },
            },
            started(&["m-3"]),
            answered(&["m-3"], "re: instead", "S1"),
            started(&["m-2"]),
            answered(&["m-2"], "re: held", "S1"),
        ]
    );
    let input = running.input();
    let cancel = input
        .iter()
        .position(|l| l.contains("\"session/cancel\""))
        .expect("a cancel");
    assert!(input[cancel].contains("\"sessionId\":\"S1\""), "{input:?}");
    assert!(input[cancel + 1].contains("instead"), "{input:?}");
}

#[test]
fn a_clear_makes_a_new_session_on_the_same_process() {
    let mut running = run(STUB);
    running.next();
    running.send("m-1", "remember", Urgency::Wake);
    running.until(ended(&["m-1"]));
    let pid = running.child.control.pid();
    assert_eq!(running.child.control.clear().unwrap(), Queued::Written);
    assert_eq!(running.send("m-2", "recall", Urgency::Wake), Queued::Held);
    assert_eq!(
        running.until(ended(&["m-2"])),
        vec![
            ChildObservation::Cleared {
                native_id: Some("S2".to_string())
            },
            started(&["m-2"]),
            answered(&["m-2"], "re: recall", "S2"),
        ]
    );
    assert_eq!(running.child.control.pid(), pid);
    let prompts = running.prompts();
    assert!(prompts[1].contains("\"sessionId\":\"S2\""), "{prompts:?}");
}

#[test]
fn a_resumed_child_loads_its_session() {
    let mut running = run_with(STUB, |spec| spec.resume = Some("OLD".to_string()));
    assert_eq!(running.next(), opened("OLD"));
    running.send("m-1", "again", Urgency::Wake);
    assert_eq!(
        running.until(ended(&["m-1"])),
        vec![started(&["m-1"]), answered(&["m-1"], "re: again", "OLD")]
    );
    let input = running.input();
    assert!(
        input[1].contains("\"session/load\"") && input[1].contains("\"sessionId\":\"OLD\""),
        "{input:?}"
    );
}

#[test]
fn a_permission_request_is_answered_from_the_posture() {
    for (posture, chosen) in [
        (ChildPermissions::Refuse, "chose no"),
        (ChildPermissions::Allow, "chose once"),
    ] {
        let mut running = run_with(STUB, |spec| spec.permissions = posture);
        running.next();
        running.send("m-1", "PERMIT a tool", Urgency::Wake);
        assert_eq!(
            running.until(ended(&["m-1"])),
            vec![started(&["m-1"]), answered(&["m-1"], chosen, "S1")]
        );
        let input = running.input();
        assert!(
            input
                .iter()
                .any(|l| l.contains("\"id\":\"perm-1\"") && l.contains("\"outcome\":\"selected\"")),
            "{input:?}"
        );
    }
}

#[test]
fn an_end_mid_turn_cancels_it_and_answers_what_was_held() {
    let mut running = run(STUB);
    running.next();
    running.send("m-1", "LONG work", Urgency::Wake);
    assert_eq!(running.next(), started(&["m-1"]));
    running.send("m-2", "later", Urgency::Wake);
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
    // The cancel was written before the input ended.
    let input = running.input();
    assert!(
        input
            .last()
            .is_some_and(|l| l.contains("\"session/cancel\"")),
        "{input:?}"
    );
    assert_eq!(
        running.child.control.end(Duration::from_secs(1)),
        Err(ChildError::Closed)
    );
}

#[test]
fn an_engine_that_exits_mid_turn_fails_the_turn_with_what_it_said() {
    let mut running = run(STUB);
    running.next();
    running.send("m-1", "EXIT now", Urgency::Wake);
    assert_eq!(running.next(), started(&["m-1"]));
    assert_eq!(
        running.until(|o| matches!(o, ChildObservation::Exited { .. })),
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
    assert_eq!(running.next(), opened("D"));
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
    let missing = fixture_program::install_program(&dir, &os::fixture("/opencode"), "")
        .unwrap()
        .with_file_name("no-opencode");
    let error = Opencode::new()
        .start(&spec(missing.to_str().unwrap()))
        .unwrap_err();
    assert!(
        matches!(&error, ChildError::NotStarted { deterministic: true, program, .. }
            if program.ends_with("no-opencode")),
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

#[test]
fn the_exit_detail_is_a_failed_wait_else_stderr() {
    assert_eq!(detail_of(String::new(), Some("said".to_string())), "said");
    assert_eq!(detail_of(String::new(), None), "");
    assert_eq!(
        detail_of("wait broke".to_string(), Some("said".to_string())),
        "wait broke"
    );
}

#[test]
fn the_pid_is_the_engines_own() {
    let mut running = run(STUB);
    // The session is made after the pid file is written.
    assert!(matches!(running.next(), ChildObservation::Started { .. }));
    let written = running.dir.read_to_string(&os::fixture("/pid")).unwrap();
    let pid = running.child.control.pid();
    assert_eq!(written, pid.to_string());
    let shown = format!("{:?}", running.child);
    assert!(shown.contains(&format!("pid: {pid}")), "{shown}");
}
