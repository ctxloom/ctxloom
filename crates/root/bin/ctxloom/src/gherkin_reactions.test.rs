//! The steps of step 10c's reactions (`cli/library_changes.feature`'s U13
//! and G7 Rules): the daemon's next round, and a watcher of the library
//! (`ctxloom test watch-library`, a client of `WatchLibrary`) run beside
//! the scenario's commands. Every step drives the real binary.

use std::io::{BufRead, BufReader};
use std::process::Child;
use std::sync::mpsc::{channel, Receiver};
use std::time::{Duration, Instant};

use cucumber::gherkin::Step;
use cucumber::{given, then, when};
use serde_json::Value;

use super::World;
use crate::tests::gherkin_common::split;
use crate::tests::library::at;

/// How long a watcher, or a round, is waited for.
const WAIT: Duration = Duration::from_secs(30);

/// What the reaction steps keep.
#[derive(Default)]
pub(super) struct State {
    /// The watcher started beside the scenario, and its stdout's lines.
    watcher: Option<(Child, Receiver<String>)>,
    /// The watcher's first line, once it said it watches.
    first: Option<String>,
    /// Every line the watcher printed, once it ended.
    printed: Vec<String>,
}

/// `ctxloom args` in the project now, in the scenario's environment.
fn run_now(w: &World, args: &[&str]) -> crate::tests::Ran {
    let env: Vec<(&str, &str)> = w
        .common
        .env
        .iter()
        .map(|(k, v)| (k.as_str(), v.as_str()))
        .collect();
    w.common.project.run_with(&env, args)
}

/// The serving daemon's round count, as `daemon status` reports it
/// (test-support, step 10 G5(b)); none when it reports none.
fn rounds(w: &World) -> Option<u64> {
    let status = run_now(w, &["daemon", "status", "--format", "json"]);
    assert_eq!(status.code, 0, "{}", status.said());
    status.json()["daemons"][0]["rounds"].as_u64()
}

/// The serving daemon has run a whole round since the step began: its
/// count has moved on by two (the round running when it was asked may
/// have begun before the change), no read made meanwhile.
#[when("the daemon's next round runs")]
fn next_round(w: &mut World) {
    let from = rounds(w).unwrap_or_else(|| {
        panic!("the serving daemon reports no round count (`rounds`, test-support; step 10 G5(b))")
    });
    let deadline = Instant::now() + WAIT;
    while rounds(w).is_none_or(|now| now < from + 2) {
        assert!(Instant::now() < deadline, "no round ran within {WAIT:?}");
        std::thread::sleep(Duration::from_millis(50));
    }
}

/// `line` started beside the scenario, awaited until it says it watches
/// (its first line), or until it ends: a watcher that could not start is
/// the next step's to report.
#[given(expr = "{string} is running")]
fn watcher_running(w: &mut World, line: String) {
    let words = split(&line);
    assert_eq!(words.first().map(String::as_str), Some("ctxloom"), "{line}");
    let args: Vec<&str> = words[1..].iter().map(String::as_str).collect();
    let env: Vec<(&str, &str)> = w
        .common
        .env
        .iter()
        .map(|(k, v)| (k.as_str(), v.as_str()))
        .collect();
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
    // Its first line, or its end (the channel closed): kept for the end.
    w.reactions.first = lines.recv_timeout(WAIT).ok();
    w.reactions.watcher = Some((child, lines));
}

/// The watcher's whole output once it has exited: its status, its stdout's
/// lines and its stderr.
fn watcher_ended(w: &mut World) -> (Option<i32>, Vec<String>, String) {
    let (mut child, lines) = w.reactions.watcher.take().expect("a watcher was started");
    let deadline = Instant::now() + WAIT;
    let status = loop {
        if let Some(status) = child.try_wait().expect("waitable") {
            break status;
        }
        if Instant::now() > deadline {
            let _ = child.kill();
            break child.wait().expect("reaped");
        }
        std::thread::sleep(Duration::from_millis(50));
    };
    let mut out: Vec<String> = w.reactions.first.take().into_iter().collect();
    while let Ok(line) = lines.recv_timeout(Duration::from_millis(500)) {
        out.push(line);
    }
    let mut err = String::new();
    if let Some(mut stderr) = child.stderr.take() {
        let _ = std::io::Read::read_to_string(&mut stderr, &mut err);
    }
    (status.code(), out, err)
}

/// Whether `actual` is `expected`, where each `{name}` in `expected`
/// stands for one segment of a resource name (no `/`).
fn matches_name(expected: &str, actual: &str) -> bool {
    let mut rest = actual;
    let mut parts = expected.split('{');
    let Some(head) = parts.next() else {
        return expected == actual;
    };
    let Some(tail) = rest.strip_prefix(head) else {
        return false;
    };
    rest = tail;
    for part in parts {
        let Some((_, literal)) = part.split_once('}') else {
            return false;
        };
        let end = rest.find('/').unwrap_or(rest.len());
        if end == 0 {
            return false;
        }
        let Some(tail) = rest[end..].strip_prefix(literal) else {
            return false;
        };
        rest = tail;
    }
    rest.is_empty()
}

/// The watcher exited 0, and its first change (the line after it said it
/// watches) has each field of the table, by its path, as the table says.
#[then("the watcher exits 0, having printed")]
fn watcher_printed(w: &mut World, step: &Step) {
    let (code, out, err) = watcher_ended(w);
    assert_eq!(code, Some(0), "the watcher: {err}{out:?}");
    let change: Value = out
        .iter()
        .filter_map(|line| serde_json::from_str::<Value>(line).ok())
        .find(|line| line.get("watching").is_none())
        .unwrap_or_else(|| panic!("no change was printed: {out:?}"));
    let table = step.table.as_ref().expect("a table");
    for row in table.rows.iter().skip(1) {
        let (path, want) = (&row[0], &row[1]);
        let got = match at(&change, path) {
            Some(Value::String(s)) => s.clone(),
            Some(other) => other.to_string(),
            None => panic!("no {path} in {change}"),
        };
        assert!(
            matches_name(want, &got),
            "{path} is {got:?}, not {want:?}: {change}"
        );
    }
    w.reactions.printed = out;
}

#[then(expr = "the watcher printed nothing containing {string}")]
fn watcher_withheld(w: &mut World, text: String) {
    let printed = &w.reactions.printed;
    assert!(
        printed.iter().all(|line| !line.contains(&text)),
        "{text:?} was printed: {printed:?}"
    );
}

/// A resource name's `{name}` is one segment, never more or none; every
/// other character is itself.
#[test]
fn a_placeholder_stands_for_one_segment_of_a_name() {
    let name = "projects/{project}/checkouts/{checkout}/library";
    assert!(matches_name(name, "projects/p1/checkouts/c-2/library"));
    assert!(!matches_name(name, "projects/p1/x/checkouts/c/library"));
    assert!(!matches_name(name, "projects//checkouts/c/library"));
    assert!(!matches_name(name, "projects/p/checkouts/c/library/more"));
    assert!(!matches_name(name, "users/me/library"));
    assert!(matches_name("external", "external"));
    assert!(!matches_name("external", "intended"));
}
