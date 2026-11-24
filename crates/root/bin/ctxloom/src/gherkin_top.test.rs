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
    /// What the scenario writes to its input.
    input: Option<std::process::ChildStdin>,
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
    /// What the line found by the last "a line has" step, and each "that
    /// line reports" after it, must satisfy together: path, text, whether
    /// the whole value is that text (else it holds it).
    wanted: Vec<(String, String, bool)>,
    /// The stream "that line" was read from: one called so, else the first.
    that_from: Option<String>,
    /// How many lines had been read when an action was taken: a step that
    /// names a line "within N seconds" after it looks past them.
    mark: usize,
    /// Where the scenario's commands run when it is not the project: a
    /// directory that is in no ctxloom project.
    elsewhere: Option<std::path::PathBuf>,
    /// Streams started beside the first, by the name the scenario gave
    /// them, each with the lines read from it.
    named: std::collections::BTreeMap<String, (Stream, Vec<Value>)>,
    /// What Alice's orchestrator has been said to launch (owner, child,
    /// agent), made when her first look at `top` needs it: a scenario that
    /// says no daemon serves must not find one the Background started.
    pending: Vec<(String, String, String)>,
}

/// The Background's launches are forgotten: no daemon serves, so nothing was
/// launched yet.
pub(super) fn forget_pending(w: &mut World) {
    w.top.pending.clear();
}

/// The launches the journey has said, made now.
pub(super) fn launch_pending(w: &mut World) {
    for (owner, child, agent) in std::mem::take(&mut w.top.pending) {
        daemon_spec::child_on_agent(w, &child, &owner, &agent, None, "claude-code");
    }
}

#[given(
    regex = r#"^Alice's orchestrator "([^"]+)" has launched "([^"]+)" on "([^"]+)" and "([^"]+)" on "([^"]+)"$"#
)]
fn alice_has_launched_two(
    w: &mut World,
    owner: String,
    first: String,
    first_agent: String,
    second: String,
    second_agent: String,
) {
    w.top.pending.push((owner.clone(), first, first_agent));
    w.top.pending.push((owner, second, second_agent));
}

#[when(regex = r#"^Alice's orchestrator "([^"]+)" has launched "([^"]+)" on "([^"]+)"$"#)]
fn alice_has_launched(w: &mut World, owner: String, child: String, agent: String) {
    w.top.pending.push((owner.clone(), child, agent));
    launch_pending(w);
    // Her orchestrator's engine is running: its row is live, as the
    // screen's own start makes it where a daemon already serves.
    daemon_spec::hold_owner_live(w, &owner);
    // Taking a session's lock is no fold, and a watch is woken by folds and
    // by a lock's release: her engine naming its session is the news that
    // makes an open screen look again.
    let env = daemon_spec::env_of(w);
    let env: Vec<(&str, &str)> = env.iter().map(|(k, v)| (k.as_str(), v.as_str())).collect();
    let ran = w
        .common
        .project
        .run_with(&env, &["session", "rename", &owner, "orchestrator"]);
    assert_eq!(ran.code, 0, "{}", ran.said());
}

#[given(expr = "the engine {string} is set to ask for at most {int} seconds")]
fn engine_asks_for_at_most(w: &mut World, engine: String, seconds: u64) {
    daemon_spec::says_in_project(w, format!("engines.{engine}.approval_timeout: {seconds}s"));
}

#[when(regex = r#"^her editor's plugin runs `([^`]+)` and reads it as it streams:$"#)]
fn plugin_runs(w: &mut World, line: String) {
    launch_pending(w);
    started(w, line);
}

#[then(expr = "its first line is a snapshot holding {string} alone")]
fn first_is_snapshot_of_one(w: &mut World, harp: String) {
    let line = first_line(w);
    let sessions = at(&line, "snapshot.sessions")
        .and_then(Value::as_array)
        .unwrap_or_else(|| panic!("no snapshot.sessions in {line}"))
        .clone();
    assert_eq!(sessions.len(), 1, "{line}");
    assert!(
        sessions[0]["name"]
            .as_str()
            .is_some_and(|n| n.ends_with(&format!("/sessions/{harp}"))),
        "{line}"
    );
}

#[then("its first line is a snapshot of her sessions")]
fn first_is_snapshot(w: &mut World) {
    let line = first_line(w);
    let sessions = at(&line, "snapshot.sessions")
        .and_then(Value::as_array)
        .unwrap_or_else(|| panic!("no snapshot.sessions in {line}"))
        .clone();
    for harp in [
        "amber-quiet-heron",
        "brisk-copper-moth",
        "quiet-silver-fern",
    ] {
        assert!(
            sessions.iter().any(|s| s["name"]
                .as_str()
                .is_some_and(|n| n.ends_with(&format!("/sessions/{harp}")))),
            "no {harp} in {line}"
        );
    }
}

#[then(expr = "the next line says an ask arrived for {string}")]
fn an_ask_arrived(w: &mut World, harp: String) {
    // The daemon says other news of the same moment first (the row's count,
    // the log): the ask's own line is waited for.
    let wanted = format!("/sessions/{harp}");
    until_line(w, 30, |line| {
        at(line, "approval_asked.approval.child").is_some_and(|c| said(c).contains(&wanted))
    });
}

#[then(expr = "the next line says an ask arrived for {string}, and none was said of {string}")]
fn an_ask_arrived_not_of(w: &mut World, harp: String, other: String) {
    an_ask_arrived(w, harp);
    no_line_names(w, other);
}

#[then(expr = "the next line says the ask was answered {string}")]
fn the_ask_was_answered(w: &mut World, option: String) {
    until_line(w, 30, |line| {
        at(line, "approval_settled.approval.state").is_some_and(|s| said(s) == "answered")
            && at(line, "approval_settled.approval.option").is_some_and(|o| said(o) == option)
    });
}

#[given(expr = "I start {string} and read its output as it streams")]
#[when(expr = "I start {string} and read its output as it streams")]
fn started(w: &mut World, line: String) {
    let stream = spawn_stream(w, &line);
    w.top.read.clear();
    w.top.mark = 0;
    w.top.that = None;
    w.top.stream = Some(stream);
}

#[given(expr = "I start {string} and read its output as it streams, calling it {string}")]
#[when(expr = "I start {string} and read its output as it streams, calling it {string}")]
fn started_named(w: &mut World, line: String, name: String) {
    let stream = spawn_stream(w, &line);
    w.top.named.insert(name, (stream, Vec::new()));
}

/// `line`'s command started, its stdout read on a thread of its own.
fn spawn_stream(w: &mut World, line: &str) -> Stream {
    let watches_before = watches_served(w);
    let stream = spawn_command(w, line);
    // The stream is the daemon's once its watch is called: an action the
    // scenario takes next (a removal, an ask) is then news to it, not
    // something that happened before it looked.
    if w.top.elsewhere.is_none() && daemon_spec::daemon_log_text(w).contains("serving at") {
        let deadline = Instant::now() + Duration::from_secs(10);
        while watches_served(w) <= watches_before && Instant::now() < deadline {
            std::thread::sleep(Duration::from_millis(20));
        }
    }
    stream
}

/// How many `WatchTop` calls the daemon's log says it served.
fn watches_served(w: &World) -> usize {
    daemon_spec::daemon_log_text(w)
        .matches("served /ctxloom.v1.Ctxloom/WatchTop")
        .count()
}

fn spawn_command(w: &mut World, line: &str) -> Stream {
    let line = &expanded_projects(w, line);
    let words = split(line);
    assert_eq!(words.first().map(String::as_str), Some("ctxloom"), "{line}");
    let args: Vec<&str> = words[1..].iter().map(String::as_str).collect();
    let env = daemon_spec::env_of(w);
    let env: Vec<(&str, &str)> = env.iter().map(|(k, v)| (k.as_str(), v.as_str())).collect();
    let mut child = match w.top.elsewhere.clone() {
        Some(dir) => w.common.project.start_in_with(&dir, &env, &args),
        None => {
            daemon_spec::hold_owner_live(w, "amber-quiet-heron");
            w.common.project.start_with_input(&env, &args)
        }
    };
    let stdout = child.stdout.take().expect("stdout is piped");
    let input = child.stdin.take();
    let (tx, lines) = channel();
    std::thread::spawn(move || {
        for line in BufReader::new(stdout).lines() {
            let Ok(line) = line else { break };
            if tx.send(line).is_err() {
                break;
            }
        }
    });
    Stream {
        child,
        lines,
        input,
    }
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
    // Lines already read count: a step may name a line that came earlier,
    // unless an action since marked where the news begins.
    let from = w.top.mark.max(1);
    if let Some(line) = seen.iter().skip(from).find(|line| found(line)) {
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
            None => {
                // Why it said nothing, when it has ended.
                let said = w.top.stream.as_mut().map_or(String::new(), |stream| {
                    match stream.child.try_wait() {
                        Ok(Some(status)) => {
                            let mut err = String::new();
                            if let Some(mut pipe) = stream.child.stderr.take() {
                                let _ = std::io::Read::read_to_string(&mut pipe, &mut err);
                            }
                            format!("it ended with {status:?}: {err}")
                        }
                        _ => "it still runs".to_string(),
                    }
                });
                panic!(
                    "no such line came within {seconds}s; read: {seen:?}; {said}; {}",
                    daemon_spec::daemon_log_text(w)
                )
            }
        }
    }
}

#[then(expr = "within {int} seconds a line has {string} set")]
fn line_has(w: &mut World, seconds: u64, field: String) {
    let line = until_line(w, seconds, |line| line.get(&field).is_some());
    w.top.wanted.clear();
    w.top.that_from = None;
    w.top.that = Some(line);
}

#[then(expr = "within {int} seconds a line has {string} containing {string}")]
fn line_has_containing(w: &mut World, seconds: u64, path: String, text: String) {
    let line = until_line(w, seconds, |line| {
        at(line, &path).is_some_and(|got| said(got).contains(&text))
    });
    w.top.wanted = vec![(path, text, false)];
    w.top.that_from = None;
    w.top.that = Some(line);
}

/// Whether `got`, a resource name or a word, is `want`: whole, or its last
/// segment (an ask is noted by it).
fn is(got: &Value, want: &str) -> bool {
    let got = said(got);
    got == want || got.ends_with(&format!("/{want}"))
}

#[then(expr = "within {int} seconds a line has {string} as {string}")]
#[then(expr = "within {int} seconds a line reports {string} as {string}")]
fn line_has_as(w: &mut World, seconds: u64, path: String, want: String) {
    let want = w.common.expanded(&want);
    let line = until_line(w, seconds, |line| {
        at(line, &path).is_some_and(|got| is(got, &want))
    });
    w.top.wanted = vec![(path, want, true)];
    w.top.that_from = None;
    w.top.that = Some(line);
}

/// Whether `line` satisfies every check.
fn satisfies(line: &Value, checks: &[(String, String, bool)]) -> bool {
    checks.iter().all(|(path, text, exact)| {
        at(line, path).is_some_and(|got| match exact {
            true => is(got, text),
            false => said(got).contains(text),
        })
    })
}

/// "That line" also says this: the line found, or another read after the
/// action that is what the steps before and this one say together (a
/// step's "a line" is no one line when the log holds several of its kind).
fn that_line_is(w: &mut World, check: (String, String, bool)) {
    w.top.wanted.push(check);
    let deadline = Instant::now() + Duration::from_secs(10);
    loop {
        // The line found says it itself, or one read since the action does.
        let from = w.top.mark;
        let named = w.top.that_from.clone();
        let read = match &named {
            Some(name) => w.top.named.get(name).map(|(_, read)| read.clone()),
            None => Some(w.top.read.clone()),
        }
        .unwrap_or_default();
        let found = w
            .top
            .that
            .iter()
            .chain(read.iter().skip(from))
            .find(|line| satisfies(line, &w.top.wanted))
            .cloned();
        if let Some(line) = found {
            w.top.that = Some(line);
            return;
        }
        let left = deadline.saturating_duration_since(Instant::now());
        if left.is_zero() {
            let that = w.top.that.clone().expect("a step found a line");
            panic!(
                "no line is {:?}: the line found was {that}; read: {:?}",
                w.top.wanted, w.top.read
            );
        }
        match &named {
            Some(name) => next_named(w, name, left.min(Duration::from_secs(1))),
            None => next_line(w, left.min(Duration::from_secs(1))),
        };
    }
}

#[then(expr = "that line reports {string} as {string}")]
fn that_reports(w: &mut World, path: String, want: String) {
    that_line_is(w, (path, want, true));
}

#[then(expr = "that line reports {string} containing {string}")]
fn that_reports_containing(w: &mut World, path: String, text: String) {
    that_line_is(w, (path, text, false));
}

#[then(expr = "a later line reports {string} containing {string}")]
fn later_reports_containing(w: &mut World, path: String, text: String) {
    let line = until_line(w, 30, |line| {
        at(line, &path).is_some_and(|got| said(got).contains(&text))
    });
    w.top.wanted = vec![(path, text, false)];
    w.top.that_from = None;
    w.top.that = Some(line);
}

#[then("the stream then ends and the command exits with code 0")]
fn stream_ends(w: &mut World) {
    let mut stream = w.top.stream.take().expect("a stream was started");
    let deadline = Instant::now() + Duration::from_secs(15);
    let status = loop {
        match stream.child.try_wait().expect("the stream is waited for") {
            Some(status) => break status,
            None => {
                assert!(
                    Instant::now() < deadline,
                    "the stream never ended: {}",
                    daemon_spec::daemon_log_text(w)
                );
                std::thread::sleep(Duration::from_millis(50));
            }
        }
    };
    assert_eq!(status.code(), Some(0), "the stream ended with {status:?}");
}

/// The lines that come within `seconds` are read too (what a step after a
/// change says of "every line read").
fn read_what_comes(w: &mut World, seconds: u64) {
    let _ = first_line(w);
    let deadline = Instant::now() + Duration::from_secs(seconds);
    while let Some(_line) = next_line(w, deadline.saturating_duration_since(Instant::now())) {}
}

#[then("every line read is one JSON object on one line")]
#[then("every line it printed is one whole JSON object")]
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
    settle_mark(w);
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
    settle_mark(w);
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
    // Its first line says it listens: a signal sent while it starts is still
    // taken by the default, as any command's is.
    first_line(w);
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
    // `top` starts no daemon: where the scenario had none, it has none.
    if !daemon_spec::started_a_daemon(w) {
        daemon_spec::nothing_started(w);
        return;
    }
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
    daemon_spec::child_on_agent(w, &child, &owner, &agent, None, "claude-code");
}

#[given(
    regex = r#"^a child "([^"]+)" of "([^"]+)" launched on agent "([^"]+)" with profile "([^"]+)", whose loadout loaded the bundle "([^"]+)"$"#
)]
fn child_with_profile(
    w: &mut World,
    child: String,
    owner: String,
    agent: String,
    profile: String,
    bundle: String,
) {
    daemon_spec::child_on_agent(
        w,
        &child,
        &owner,
        &agent,
        Some((&profile, &bundle)),
        "claude-code",
    );
}

#[given(
    regex = r#"^a child "([^"]+)" of "([^"]+)" launched on agent "([^"]+)" with profile "([^"]+)", whose loadout loaded the bundle "([^"]+)", on engine "([^"]+)"$"#
)]
fn child_with_profile_on_engine(
    w: &mut World,
    child: String,
    owner: String,
    agent: String,
    profile: String,
    bundle: String,
    engine: String,
) {
    daemon_spec::child_on_agent(
        w,
        &child,
        &owner,
        &agent,
        Some((&profile, &bundle)),
        &engine,
    );
}

/// Another child must start in a second of its own: the start is told in
/// seconds, and an order of starts is what the scenario sorts by.
fn a_second_apart() {
    std::thread::sleep(Duration::from_millis(1100));
}

#[given(
    regex = r#"^a child "([^"]+)" of "([^"]+)" launched on agent "([^"]+)" on engine "([^"]+)", started (first|second)$"#
)]
fn child_on_engine_started(
    w: &mut World,
    child: String,
    owner: String,
    agent: String,
    engine: String,
    when: String,
) {
    if when == "second" {
        a_second_apart();
    }
    daemon_spec::child_on_agent(w, &child, &owner, &agent, None, &engine);
}

#[given(regex = r#"^a child "([^"]+)" of "([^"]+)", started (first|second)$"#)]
fn child_started(w: &mut World, child: String, owner: String, when: String) {
    if when == "second" {
        a_second_apart();
    }
    daemon_spec::child_of_idle(w, child, owner);
}

#[when(regex = r#"^I start "([^"]+)", "([^"]+)" and "([^"]+)", and read their first lines$"#)]
fn started_three(w: &mut World, a: String, b: String, c: String) {
    for (at, line) in [a, b, c].into_iter().enumerate() {
        let stream = spawn_stream(w, &line);
        w.top
            .named
            .insert(format!("watch {}", at + 1), (stream, Vec::new()));
    }
    for at in 1..=3 {
        let name = format!("watch {at}");
        next_named(w, &name, WAIT).unwrap_or_else(|| panic!("no first line of {name}"));
    }
}

/// The watch RPCs the daemon's log says it served, by name.
fn served_watches(w: &World) -> Vec<String> {
    daemon_spec::daemon_log_text(w)
        .lines()
        .filter_map(|line| line.split("served /ctxloom.v1.Ctxloom/").nth(1))
        .filter(|rpc| rpc.starts_with("Watch"))
        .map(str::to_string)
        .collect()
}

#[then(expr = "the daemon's log says it served {string} three times")]
fn served_three_times(w: &mut World, rpc: String) {
    let served = served_watches(w);
    assert_eq!(
        served.iter().filter(|r| **r == rpc).count(),
        3,
        "{served:?}"
    );
}

#[then(expr = "it served no other watch RPC for them but {string} for the one-session watch")]
fn served_no_other(w: &mut World, allowed: String) {
    let served = served_watches(w);
    assert!(
        served.iter().all(|r| r == "WatchTop" || *r == allowed),
        "{served:?}"
    );
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
    // Its words, as the engine shows them: the first transcript entry after
    // the action, and the log's last until something else happens.
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
    settle_mark(w);
    says(w, &harp, &text);
}

#[when(expr = "{string} says {string} and its run then ends")]
fn says_then_ends(w: &mut World, harp: String, text: String) {
    settle_mark(w);
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
    settle_mark(w);
    run_ends_of(w, &harp);
}

#[when(regex = r#"^the daemon is stopped and the next daemon re-hosts "([^"]+)"$"#)]
fn rehosted(w: &mut World, _harp: String) {
    settle_mark(w);
    let env = daemon_spec::env_of(w);
    let env: Vec<(&str, &str)> = env.iter().map(|(k, v)| (k.as_str(), v.as_str())).collect();
    let stopped = w
        .common
        .project
        .run_with(&env, &["daemon", "stop", "--drain"]);
    assert_eq!(stopped.code, 0, "{}", stopped.said());
    // The next daemon is started once this one has exited (it hands its
    // children over first).
    daemon_spec::drained_daemon_gone(w);
    let next = w.common.project.run_with(&env, &["daemon", "resolve"]);
    assert_eq!(next.code, 0, "{}", next.said());
}

#[when(expr = "the ask is answered {string}")]
fn ask_answered(w: &mut World, option: String) {
    // "The ask": the child's engine asks first when none waits.
    daemon_spec::ensure_an_ask(w, "brisk-copper-moth");
    settle_mark(w);
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

#[then(expr = "the next line reports {string} containing {string}")]
fn next_reports_containing(w: &mut World, path: String, text: String) {
    let line = next_line(w, WAIT).unwrap_or_else(|| panic!("no next line came"));
    let got = at(&line, &path).unwrap_or_else(|| panic!("no {path} in {line}"));
    assert!(said(got).contains(&text), "{path} is {got}: {line}");
    w.top.that = Some(line);
}

#[then(expr = "a later line reports {string} as {string}")]
fn later_reports(w: &mut World, path: String, want: String) {
    let found = until_line(w, 30, |line| {
        at(line, &path).is_some_and(|got| is(got, &want))
    });
    w.top.wanted = vec![(path, want, true)];
    w.top.that_from = None;
    w.top.that = Some(found);
}

#[then(expr = "that line's {string} holds {string}")]
fn that_holds(w: &mut World, path: String, text: String) {
    let line = w.top.that.clone().expect("a step found a line");
    let got = at(&line, &path).unwrap_or_else(|| panic!("no {path} in {line}"));
    assert!(said(got).contains(&text), "{path} is {got}: {line}");
}

/// The next line of the stream called `name`, within `within`.
fn next_named(w: &mut World, name: &str, within: Duration) -> Option<Value> {
    let (stream, read) = w.top.named.get_mut(name).expect("a stream of that name");
    let line = stream.lines.recv_timeout(within).ok()?;
    let parsed: Value = serde_json::from_str(&line)
        .unwrap_or_else(|e| panic!("a line that is no JSON object ({e}): {line:?}"));
    read.push(parsed.clone());
    Some(parsed)
}

#[then(expr = "within {int} seconds a line of {string} has {string} containing {string}")]
fn named_line_has(w: &mut World, seconds: u64, name: String, path: String, text: String) {
    let deadline = Instant::now() + Duration::from_secs(seconds);
    loop {
        let read = w
            .top
            .named
            .get(&name)
            .expect("a stream of that name")
            .1
            .clone();
        if let Some(found) = read
            .iter()
            .find(|line| at(line, &path).is_some_and(|got| said(got).contains(&text)))
        {
            w.top.that = Some(found.clone());
            w.top.wanted = vec![(path, text, false)];
            w.top.that_from = Some(name);
            return;
        }
        let left = deadline.saturating_duration_since(Instant::now());
        assert!(
            !left.is_zero(),
            "no line of {name} had {path} containing {text}: {read:?}"
        );
        next_named(w, &name, left.min(Duration::from_secs(1)));
    }
}

#[then(expr = "no line read names {string}")]
fn no_line_names(w: &mut World, text: String) {
    for line in &w.top.read {
        assert!(!names_a_session(line, &text), "a line names {text}: {line}");
    }
}

/// Whether `value` holds, under a key that names a session (`name`, `child`,
/// `session`), a name containing `text`: a session in a list of children
/// is not a line about it.
fn names_a_session(value: &Value, text: &str) -> bool {
    match value {
        Value::Object(fields) => fields.iter().any(|(key, inner)| {
            let names = matches!(key.as_str(), "name" | "child" | "session")
                && inner.as_str().is_some_and(|s| s.contains(text));
            names || names_a_session(inner, text)
        }),
        Value::Array(items) => items.iter().any(|inner| names_a_session(inner, text)),
        _ => false,
    }
}

/// What the scenario has read is the past: lines pending now are drained
/// first, and a line "within N seconds" of the next step is one that comes
/// after.
fn settle_mark(w: &mut World) {
    if w.top.stream.is_some() {
        while next_line(w, Duration::from_millis(300)).is_some() {}
    }
    w.top.mark = w.top.read.len();
}

#[then(expr = "within {int} seconds no line names {string}")]
fn within_no_line_names(w: &mut World, seconds: u64, text: String) {
    let deadline = Instant::now() + Duration::from_secs(seconds);
    loop {
        let left = deadline.saturating_duration_since(Instant::now());
        if left.is_zero() {
            break;
        }
        next_line(w, left.min(Duration::from_secs(1)));
    }
    no_line_names(w, text);
}

#[then(expr = "no line read has {string} containing {string}")]
fn no_line_has_containing(w: &mut World, path: String, text: String) {
    for line in &w.top.read {
        if let Some(got) = at(line, &path) {
            assert!(!said(got).contains(&text), "{path} is {got}: {line}");
        }
    }
}

#[when(expr = "I write {string} to its input")]
fn wrote_to_input(w: &mut World, text: String) {
    use std::io::Write;
    let stream = w.top.stream.as_mut().expect("a stream was started");
    let input = stream.input.as_mut().expect("its input is open");
    // The stream ignores what it is told; a broken pipe is the same.
    let _ = writeln!(input, "{text}").and_then(|()| input.flush());
}

#[given(expr = "the daemon's round period is an hour")]
fn round_period_an_hour(w: &mut World) {
    daemon_spec::set_round_period(w, 3_600_000);
}

#[given(expr = "an ended session {string} that nothing keeps")]
fn ended_session(w: &mut World, harp: String) {
    daemon_spec::ended_session(w, &harp);
}

#[given(expr = "a second project {string} with a session {string} running")]
fn second_project(w: &mut World, name: String, harp: String) {
    daemon_spec::second_project_running(w, &name, &harp);
}

#[given(
    expr = "a ctxloom daemon is serving for this home, draining, and hosting a child {string} that does not end its turn"
)]
fn serving_draining(w: &mut World, child: String) {
    daemon_spec::serving_draining(w, child);
}

#[when("the daemon is killed outright")]
fn daemon_killed(w: &mut World) {
    daemon_spec::kill_daemon(w);
}

#[when("a ctxloom daemon starts serving for this home")]
fn daemon_starts_serving(w: &mut World) {
    daemon_spec::serve_again(w);
}

/// `projects/{name}/...` in a command line: `{name}` is the harp of the
/// project beside this one of that name.
fn expanded_projects(w: &World, line: &str) -> String {
    let mut out = line.to_string();
    while let Some(open) = out.find("projects/{") {
        let from = open + "projects/{".len();
        let Some(len) = out[from..].find('}') else {
            break;
        };
        let name = out[from..from + len].to_string();
        let harp = daemon_spec::beside_name(w, &name);
        out.replace_range(open..from + len + 1, &harp);
    }
    out
}

#[given("a directory that is in no ctxloom project")]
fn directory_in_no_project(w: &mut World) {
    let dir = w.adopt_scratch().join("elsewhere");
    crate::tests::files::mkdirs(&dir);
    w.common.elsewhere = Some(dir.clone());
    w.top.elsewhere = Some(dir);
}

#[when(regex = r#"^Alice sets ctxloom up in that directory and a session "([^"]+)" starts there$"#)]
fn alice_sets_up_there(w: &mut World, harp: String) {
    let dir = w.top.elsewhere.clone().expect("a directory in no project");
    daemon_spec::set_up_and_running(w, &dir, &harp);
}
