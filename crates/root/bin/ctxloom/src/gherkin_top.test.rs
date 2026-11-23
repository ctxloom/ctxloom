//! The steps of `cli/top.feature` and `journeys/j002900_watching_children.feature`
//! (`ctxloom top`, DRAFT): a stream of JSON lines started beside the
//! scenario's commands, read as it comes. Every step drives the real binary.
//! A step this file does not define is red by being undefined.

use std::io::{BufRead, BufReader};
use std::process::Child;
use std::sync::mpsc::{channel, Receiver};
use std::time::{Duration, Instant};

use cucumber::{given, then, when};
use serde_json::Value;

use super::daemon_spec;
use super::World;
use crate::tests::gherkin_common::split;
use crate::tests::library::at;

/// How long a line is waited for before it is said not to come.
const WAIT: Duration = Duration::from_secs(30);

/// One stream started beside the scenario.
struct Stream {
    child: Child,
    lines: Receiver<String>,
}

impl Drop for Stream {
    fn drop(&mut self) {
        let _ = self.child.kill();
        let _ = self.child.wait();
    }
}

/// What the `top` steps keep.
#[derive(Default)]
pub(super) struct State {
    stream: Option<Stream>,
    /// Every line read so far, parsed, oldest first.
    read: Vec<Value>,
    /// The line the last "a line has" step found.
    that: Option<Value>,
}

#[given(expr = "I start {string} and read its output as it streams")]
#[when(expr = "I start {string} and read its output as it streams")]
fn started(w: &mut World, line: String) {
    let words = split(&line);
    assert_eq!(words.first().map(String::as_str), Some("ctxloom"), "{line}");
    let args: Vec<&str> = words[1..].iter().map(String::as_str).collect();
    let env = daemon_spec::env_of(w);
    let env: Vec<(&str, &str)> = env.iter().map(|(k, v)| (k.as_str(), v.as_str())).collect();
    daemon_spec::hold_owner_live(w, "amber-quiet-heron");
    let mut child = w.common.project.start_with(&env, &args);
    let stdout = child.stdout.take().expect("stdout is piped");
    let (tx, lines) = channel();
    std::thread::spawn(move || {
        for line in BufReader::new(stdout).lines() {
            let Ok(line) = line else { break };
            if tx.send(line).is_err() {
                break;
            }
        }
    });
    w.top.read.clear();
    w.top.that = None;
    w.top.stream = Some(Stream { child, lines });
}

#[given(expr = "I start {string} with its output not a terminal and read it as it streams")]
#[when(expr = "I start {string} with its output not a terminal and read it as it streams")]
fn started_off_a_terminal(w: &mut World, line: String) {
    // The stream's stdout is a pipe: never a terminal.
    started(w, line);
}

/// The next line, parsed, within `within`; none if it did not come.
fn next_line(w: &mut World, within: Duration) -> Option<Value> {
    let stream = w.top.stream.as_ref().expect("a stream was started");
    let line = stream.lines.recv_timeout(within).ok()?;
    let parsed: Value = serde_json::from_str(&line)
        .unwrap_or_else(|e| panic!("a line that is no JSON object ({e}): {line:?}"));
    w.top.read.push(parsed.clone());
    Some(parsed)
}

/// The first line, read if it has not been.
fn first_line(w: &mut World) -> Value {
    if w.top.read.is_empty() {
        next_line(w, WAIT)
            .unwrap_or_else(|| panic!("no first line came: {}", daemon_spec::daemon_log_text(w)));
    }
    w.top.read[0].clone()
}

/// A value, as the other steps compare it: a string bare, else as JSON.
fn said(value: &Value) -> String {
    match value {
        Value::String(text) => text.clone(),
        other => other.to_string(),
    }
}

#[then(expr = "its first line has {string} set, and no other field")]
fn first_has_only(w: &mut World, field: String) {
    let line = first_line(w);
    let keys: Vec<&String> = line.as_object().expect("an object").keys().collect();
    assert_eq!(keys, [&field], "{line}");
    w.top.that = Some(line);
}

#[then(expr = "its first line is exactly {string}")]
fn first_is_exactly(w: &mut World, text: String) {
    let line = first_line(w);
    let wanted: Value = serde_json::from_str(&text).expect("the step's JSON");
    assert_eq!(line, wanted);
}

#[then(expr = "its first line reports {string} as {string}")]
#[then(expr = "the first line reports {string} as {string}")]
fn first_reports(w: &mut World, path: String, want: String) {
    let line = first_line(w);
    let got = at(&line, &path).unwrap_or_else(|| panic!("no {path} in {line}"));
    assert_eq!(said(got), want, "{path}: {line}");
}

#[then(expr = "its first line reports {string} containing {string}")]
#[then(expr = "the first line reports {string} containing {string}")]
fn first_reports_containing(w: &mut World, path: String, text: String) {
    let line = first_line(w);
    let got = at(&line, &path).unwrap_or_else(|| panic!("no {path} in {line}"));
    assert!(said(got).contains(&text), "{path} is {got}: {line}");
}

#[then(expr = "its first line reports {string} as present")]
fn first_reports_present(w: &mut World, path: String) {
    let line = first_line(w);
    assert!(at(&line, &path).is_some(), "no {path} in {line}");
}

#[then(expr = "its first line reports no {string}")]
fn first_reports_none(w: &mut World, path: String) {
    let line = first_line(w);
    assert!(at(&line, &path).is_none(), "{path} is in {line}");
}

#[then(regex = r#"^its first line (?:has|reports) "([^"]+)" holding (\d+) entr(?:y|ies)$"#)]
fn first_holds(w: &mut World, path: String, count: usize) {
    let line = first_line(w);
    let held = at(&line, &path)
        .and_then(Value::as_array)
        .map_or(0, Vec::len);
    assert_eq!(held, count, "{path}: {line}");
}

#[then("the command is still running a second later")]
fn still_running(w: &mut World) {
    std::thread::sleep(Duration::from_secs(1));
    let stream = w.top.stream.as_mut().expect("a stream was started");
    assert!(
        stream.child.try_wait().expect("waitable").is_none(),
        "it ended"
    );
}

#[then("the command is still running two seconds later, and has printed no other line")]
fn still_running_and_quiet(w: &mut World) {
    let _ = first_line(w);
    assert!(
        next_line(w, Duration::from_secs(2)).is_none(),
        "it printed another line: {:?}",
        w.top.read
    );
    let stream = w.top.stream.as_mut().expect("a stream was started");
    assert!(
        stream.child.try_wait().expect("waitable").is_none(),
        "it ended"
    );
}

/// Lines are read until `found` says one is the one, within `seconds`.
fn until_line(w: &mut World, seconds: u64, found: impl Fn(&Value) -> bool) -> Value {
    let deadline = Instant::now() + Duration::from_secs(seconds);
    let mut seen: Vec<Value> = w.top.read.clone();
    // Lines already read count: a step may name a line that came earlier.
    if let Some(line) = seen.iter().skip(1).find(|line| found(line)) {
        return line.clone();
    }
    loop {
        let left = deadline.saturating_duration_since(Instant::now());
        match next_line(w, left) {
            Some(line) if found(&line) => {
                w.top.that = Some(line.clone());
                return line;
            }
            Some(line) => seen.push(line),
            None => panic!(
                "no such line came within {seconds}s; read: {seen:?}; {}",
                daemon_spec::daemon_log_text(w)
            ),
        }
    }
}

#[then(expr = "within {int} seconds a line has {string} set")]
fn line_has(w: &mut World, seconds: u64, field: String) {
    let line = until_line(w, seconds, |line| line.get(&field).is_some());
    w.top.that = Some(line);
}

#[then(expr = "within {int} seconds a line has {string} containing {string}")]
fn line_has_containing(w: &mut World, seconds: u64, path: String, text: String) {
    let line = until_line(w, seconds, |line| {
        at(line, &path).is_some_and(|got| said(got).contains(&text))
    });
    w.top.that = Some(line);
}

/// Whether `got`, a resource name or a word, is `want`: whole, or its last
/// segment (an ask is noted by it).
fn is(got: &Value, want: &str) -> bool {
    let got = said(got);
    got == want || got.ends_with(&format!("/{want}"))
}

#[then(expr = "within {int} seconds a line has {string} as {string}")]
fn line_has_as(w: &mut World, seconds: u64, path: String, want: String) {
    let want = w.common.expanded(&want);
    let line = until_line(w, seconds, |line| {
        at(line, &path).is_some_and(|got| is(got, &want))
    });
    w.top.that = Some(line);
}

#[then(expr = "that line reports {string} as {string}")]
fn that_reports(w: &mut World, path: String, want: String) {
    let line = w.top.that.clone().expect("a step found a line");
    let got = at(&line, &path).unwrap_or_else(|| panic!("no {path} in {line}"));
    assert_eq!(said(got), want, "{path}: {line}");
}

#[then(expr = "that line reports {string} containing {string}")]
fn that_reports_containing(w: &mut World, path: String, text: String) {
    let line = w.top.that.clone().expect("a step found a line");
    let got = at(&line, &path).unwrap_or_else(|| panic!("no {path} in {line}"));
    assert!(said(got).contains(&text), "{path} is {got}: {line}");
}

/// The lines that come within `seconds` are read too (what a step after a
/// change says of "every line read").
fn read_what_comes(w: &mut World, seconds: u64) {
    let _ = first_line(w);
    let deadline = Instant::now() + Duration::from_secs(seconds);
    while let Some(_line) = next_line(w, deadline.saturating_duration_since(Instant::now())) {}
}

#[then("every line read is one JSON object on one line")]
fn every_line_is_an_object(w: &mut World) {
    read_what_comes(w, 4);
    assert!(!w.top.read.is_empty(), "no line was read");
    assert!(w.top.read.iter().all(Value::is_object), "{:?}", w.top.read);
}

#[then(regex = r#"^each line has exactly one of ("[a-z_]+"(?:, "[a-z_]+")*) set$"#)]
fn each_has_one_of(w: &mut World, fields: String) {
    read_what_comes(w, 1);
    let fields: Vec<String> = fields
        .split(", ")
        .map(|f| f.trim_matches('"').to_string())
        .collect();
    for line in &w.top.read {
        let set: Vec<&String> = line.as_object().expect("an object").keys().collect();
        assert!(
            set.len() == 1 && fields.contains(set[0]),
            "not exactly one of {fields:?}: {line}"
        );
    }
}

#[given("a ctxloom daemon is serving for this home, hosting nothing")]
fn serving_nothing(w: &mut World) {
    daemon_spec::start_daemon_idling(w, daemon_spec::SPAWNED_IDLE_SECONDS);
}

#[when(
    expr = "a ctxloom daemon starts serving for this home, hosting a child {string} of {string}"
)]
fn daemon_starts_hosting(w: &mut World, child: String, owner: String) {
    if (child.as_str(), owner.as_str()) == ("brisk-copper-moth", "amber-quiet-heron") {
        daemon_spec::idle_child(w);
    } else {
        daemon_spec::user_launches(w, &owner, &child, None);
    }
}

#[when(expr = "a child {string} of {string} is launched")]
fn child_launched(w: &mut World, child: String, owner: String) {
    daemon_spec::user_launches(w, &owner, &child, None);
}

/// A session of a running owner, as `a child ... that is open and idle` has
/// it: the owner's first turn told, the child hosted.
#[given(expr = "a child {string} of {string}")]
fn child_of(w: &mut World, child: String, owner: String) {
    daemon_spec::child_of_idle(w, child, owner);
}

#[when(regex = r#"^the engine of "([^"]+)" asks to run "([^"]+)" with "([^"]*)"$"#)]
fn engine_of_asks(w: &mut World, child: String, tool: String, input: String) {
    daemon_spec::ask(w, &child, &tool, &input);
}

#[given(expr = "I note the ask named by the first line's {string} as {string}")]
fn note_first_ask(w: &mut World, path: String, label: String) {
    let line = first_line(w);
    let name = at(&line, &path)
        .and_then(Value::as_str)
        .unwrap_or_else(|| panic!("no name at {path} in {line}"))
        .to_string();
    w.common
        .note(&label, name.rsplit('/').next().unwrap_or(&name));
}

/// SIGINT to the stream, and what it ends with as the scenario's last
/// command.
#[when("I send it SIGINT")]
fn sent_sigint(w: &mut World) {
    let mut stream = w.top.stream.take().expect("a stream was started");
    let pid = stream.child.id().to_string();
    let sent = std::process::Command::new("kill")
        .args(["-INT", &pid])
        .status()
        .expect("kill runs");
    assert!(sent.success());
    let status = stream.child.wait().expect("ctxloom finishes");
    let mut stderr = String::new();
    if let Some(mut pipe) = stream.child.stderr.take() {
        let _ = std::io::Read::read_to_string(&mut pipe, &mut stderr);
    }
    w.common.ran = Some(crate::tests::Ran {
        code: status.code().unwrap_or(-1),
        stdout: String::new(),
        stderr,
    });
}

#[then("the daemon is still serving")]
fn daemon_still_serving(w: &mut World) {
    let env = daemon_spec::env_of(w);
    let env: Vec<(&str, &str)> = env.iter().map(|(k, v)| (k.as_str(), v.as_str())).collect();
    let ran = w
        .common
        .project
        .run_with(&env, &["daemon", "status", "--format", "json"]);
    assert_eq!(ran.code, 0, "{}", ran.said());
    assert!(
        ran.stdout.contains("\"pid\""),
        "no daemon serves: {}",
        ran.stdout
    );
}

// ------------------------------------------------------------ fixtures

#[given(expr = "a child {string} of {string} launched on agent {string}")]
fn child_on_agent(w: &mut World, child: String, owner: String, agent: String) {
    daemon_spec::child_on_agent(w, &child, &owner, &agent);
}

/// The session's hook `native`, as its engine fires it, on `payload` with
/// `edit` applied.
fn hook_of(w: &mut World, harp: &str, native: &str, payload: &str, edit: impl FnOnce(&mut Value)) {
    let mut body: Value = serde_json::from_str(payload).expect("a fixture");
    edit(&mut body);
    let name = daemon_spec::session_name(w, harp);
    let (ran, _) = daemon_spec::hook_run(w, native, &body.to_string(), Some(&name));
    assert_eq!(ran.code, 0, "{}", ran.said());
}

#[given(expr = "a child {string} of {string} whose hooks delivered the prompt {string}")]
fn child_prompted(w: &mut World, child: String, owner: String, prompt: String) {
    daemon_spec::child_of_idle(w, child.clone(), owner);
    hook_of(
        w,
        &child,
        "UserPromptSubmit",
        daemon_spec::USER_PROMPT_SUBMIT,
        |body| body["prompt"] = Value::String(prompt),
    );
}

/// `harp` says `text`: its words shown, and its turn ended.
fn says(w: &mut World, harp: &str, text: &str) {
    // The turn's end fires first and its words after, as claude's hooks do:
    // the words are the log's last entry.
    hook_of(w, harp, "Stop", daemon_spec::STOP, |body| {
        body["last_assistant_message"] = Value::String(text.to_string());
    });
    hook_of(
        w,
        harp,
        "MessageDisplay",
        daemon_spec::MESSAGE_DISPLAY,
        |body| {
            body["delta"] = Value::String(text.to_string());
        },
    );
}

#[given(expr = "{string} says {string}")]
#[when(expr = "{string} says {string}")]
fn says_step(w: &mut World, harp: String, text: String) {
    says(w, &harp, &text);
}

#[when(expr = "{string} says {string} and its run then ends")]
fn says_then_ends(w: &mut World, harp: String, text: String) {
    says(w, &harp, &text);
    run_ends_of(w, &harp);
}

/// `harp`'s run ended, as the user stops it.
fn run_ends_of(w: &mut World, harp: &str) {
    let env = daemon_spec::env_of(w);
    let env: Vec<(&str, &str)> = env.iter().map(|(k, v)| (k.as_str(), v.as_str())).collect();
    let ran = w.common.project.run_with(&env, &["session", "stop", harp]);
    assert_eq!(ran.code, 0, "{}", ran.said());
}

#[when(regex = r#"^"([^"]+)"'s run ends$"#)]
fn run_ends(w: &mut World, harp: String) {
    run_ends_of(w, &harp);
}

#[when(regex = r#"^the daemon is stopped and the next daemon re-hosts "([^"]+)"$"#)]
fn rehosted(w: &mut World, _harp: String) {
    let env = daemon_spec::env_of(w);
    let env: Vec<(&str, &str)> = env.iter().map(|(k, v)| (k.as_str(), v.as_str())).collect();
    let stopped = w
        .common
        .project
        .run_with(&env, &["daemon", "stop", "--drain"]);
    assert_eq!(stopped.code, 0, "{}", stopped.said());
    let next = w.common.project.run_with(&env, &["daemon", "resolve"]);
    assert_eq!(next.code, 0, "{}", next.said());
}

#[when(expr = "the ask is answered {string}")]
fn ask_answered(w: &mut World, option: String) {
    let env = daemon_spec::env_of(w);
    let env: Vec<(&str, &str)> = env.iter().map(|(k, v)| (k.as_str(), v.as_str())).collect();
    let ran = w.common.project.run_with(
        &env,
        &[
            "session",
            "approvals",
            "answer",
            "brisk-copper-moth",
            &option,
        ],
    );
    assert_eq!(ran.code, 0, "{}", ran.said());
}

/// The log entries read so far, in the order they came.
fn log_lines(w: &World) -> Vec<Value> {
    w.top
        .read
        .iter()
        .filter_map(|line| line.get("log").cloned())
        .collect()
}

#[then(
    regex = r#"^within (\d+) seconds the log lines read are, in order, a transcript line holding "([^"]*)", then a daemon entry "([^"]*)"$"#
)]
fn log_in_order(w: &mut World, seconds: u64, text: String, event: String) {
    let deadline = Instant::now() + Duration::from_secs(seconds);
    loop {
        let logs = log_lines(w);
        let said = logs.iter().position(|l| {
            l["kind"] == "TRANSCRIPT" && l["line"].as_str().is_some_and(|x| x.contains(&text))
        });
        let ended = logs
            .iter()
            .position(|l| l["kind"] == "DAEMON" && l["event"] == event.as_str());
        if let (Some(said), Some(ended)) = (said, ended) {
            assert!(said < ended, "the entries came out of order: {logs:#?}");
            return;
        }
        let left = deadline.saturating_duration_since(Instant::now());
        assert!(!left.is_zero(), "the log never read so: {logs:#?}");
        next_line(w, left.min(Duration::from_secs(1)));
    }
}

/// An RFC 3339 instant as a pair that orders: the second, then its fraction
/// padded to nanoseconds.
fn instant(text: &str) -> (String, String) {
    let bare = text.trim_end_matches('Z');
    match bare.split_once('.') {
        Some((second, fraction)) => (second.to_string(), format!("{fraction:0<9}")),
        None => (bare.to_string(), "0".repeat(9)),
    }
}

#[then("the log lines' \"time\" never go back")]
fn log_times_ordered(w: &mut World) {
    let times: Vec<(String, String)> = log_lines(w)
        .iter()
        .filter_map(|l| l["time"].as_str().map(instant))
        .collect();
    assert!(!times.is_empty(), "no log line was read");
    assert!(times.windows(2).all(|pair| pair[0] <= pair[1]), "{times:?}");
}

#[then(expr = "its first line has {string} set")]
fn first_has(w: &mut World, field: String) {
    let line = first_line(w);
    assert!(line.get(&field).is_some(), "no {field} in {line}");
}

#[then(expr = "within {int} seconds no line has {string} set")]
fn no_line_has(w: &mut World, seconds: u64, field: String) {
    let deadline = Instant::now() + Duration::from_secs(seconds);
    loop {
        let left = deadline.saturating_duration_since(Instant::now());
        if left.is_zero() {
            return;
        }
        if let Some(line) = next_line(w, left.min(Duration::from_secs(1))) {
            assert!(line.get(&field).is_none(), "a line has {field}: {line}");
        }
    }
}

#[when(expr = "I run {string} with its output not a terminal")]
fn run_not_a_terminal(w: &mut World, line: String) {
    // The harness's commands write to pipes: never a terminal.
    daemon_spec::before_line(w, &line);
    w.common.run_lines(&line, "");
}
