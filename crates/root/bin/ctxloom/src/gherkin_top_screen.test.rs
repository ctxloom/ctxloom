//! The steps of `cli/top.feature`'s screen rule: `ctxloom top --format text`
//! on a stood-in terminal (`CTXLOOM_TEST_TERMINAL`) draws each frame as
//! plain lines ended by a line `---` and takes its keys one name a line on
//! stdin. Every step drives the real binary.

use std::io::{BufRead, BufReader, Write};
use std::process::{Child, ChildStdin};
use std::sync::mpsc::{channel, Receiver};
use std::time::{Duration, Instant};

use cucumber::{given, then, when};

use super::{daemon_spec, World};
use crate::tests::gherkin_common::split;
use crate::tests::{files, Ran};

/// How long a frame is waited for.
const WAIT: Duration = Duration::from_secs(30);

/// A screen started beside the scenario.
pub(super) struct Screen {
    child: Child,
    keys: ChildStdin,
    frames: Receiver<Vec<String>>,
    /// Every frame read so far, oldest first.
    seen: Vec<Vec<String>>,
}

impl Drop for Screen {
    fn drop(&mut self) {
        let _ = self.child.kill();
        let _ = self.child.wait();
    }
}

/// What the screen steps keep.
#[derive(Default)]
pub(super) struct State {
    screen: Option<Screen>,
    pty: Option<Pty>,
}

/// A frame cut into its parts.
struct Parts {
    header: String,
    status: Vec<String>,
    columns: String,
    /// Each row as drawn, its marker first.
    rows: Vec<String>,
    asks_title: String,
    asks: Vec<String>,
    log_title: String,
    log: Vec<String>,
}

fn parts(frame: &[String]) -> Parts {
    let at = |prefix: &str| {
        frame
            .iter()
            .position(|l| l.starts_with(prefix))
            .unwrap_or_else(|| panic!("no {prefix:?} line in {frame:#?}"))
    };
    let columns = frame
        .iter()
        .position(|l| l.trim_start().starts_with("NAME "))
        .unwrap_or_else(|| panic!("no header row in {frame:#?}"));
    let asks = at("--- asks of");
    let log = at("--- log of");
    Parts {
        header: frame[0].clone(),
        status: frame[1..columns].to_vec(),
        columns: frame[columns].clone(),
        rows: frame[columns + 1..asks].to_vec(),
        asks_title: frame[asks].clone(),
        asks: frame[asks + 1..log].to_vec(),
        log_title: frame[log].clone(),
        log: frame[log + 1..].to_vec(),
    }
}

/// A row's name cell, its indent kept, and its other cells.
fn name_of(row: &str) -> String {
    let body = &row[2..];
    let indent = body.len() - body.trim_start().len();
    let word = body.split_whitespace().next().unwrap_or_default();
    format!("{}{word}", " ".repeat(indent))
}

fn harp_of(row: &str) -> String {
    name_of(row).trim().to_string()
}

fn marked(parts: &Parts) -> Vec<String> {
    parts
        .rows
        .iter()
        .filter(|row| row.starts_with("> "))
        .map(|row| harp_of(row))
        .collect()
}

fn has_row(parts: &Parts, harp: &str) -> bool {
    parts.rows.iter().any(|row| harp_of(row) == harp)
}

fn row_of<'a>(parts: &'a Parts, harp: &str) -> &'a str {
    parts
        .rows
        .iter()
        .find(|row| harp_of(row) == harp)
        .unwrap_or_else(|| panic!("no row for {harp}: {:#?}", parts.rows))
}

fn screen(w: &mut World) -> &mut Screen {
    w.top_screen.screen.as_mut().expect("a screen was started")
}

/// Read the frames that came, without waiting.
fn drain(w: &mut World) {
    let screen = screen(w);
    while let Ok(frame) = screen.frames.try_recv() {
        screen.seen.push(frame);
    }
}

/// The first frame, waited for.
fn first(w: &mut World) -> Vec<String> {
    if screen(w).seen.is_empty() {
        let frame = screen(w)
            .frames
            .recv_timeout(WAIT)
            .unwrap_or_else(|_| panic!("no frame came: {}", daemon_spec::daemon_log_text(w)));
        screen(w).seen.push(frame);
    }
    screen(w).seen[0].clone()
}

/// The newest frame that satisfies `ok`, waiting for more frames until one
/// does.
fn until(w: &mut World, what: &str, ok: impl Fn(&Parts) -> bool) -> Parts {
    let deadline = Instant::now() + WAIT;
    loop {
        drain(w);
        if let Some(frame) = screen(w).seen.last() {
            let found = parts(frame);
            if ok(&found) {
                return found;
            }
        }
        let left = deadline.saturating_duration_since(Instant::now());
        match screen(w).frames.recv_timeout(left) {
            Ok(frame) => screen(w).seen.push(frame),
            Err(_) => {
                let last = screen(w).seen.last().cloned().unwrap_or_default();
                panic!(
                    "no frame {what}: the last was {last:#?}\n{}",
                    daemon_spec::daemon_log_text(w)
                )
            }
        }
    }
}

fn press_key(w: &mut World, key: &str) {
    let screen = screen(w);
    writeln!(screen.keys, "{key}")
        .and_then(|()| screen.keys.flush())
        .expect("the screen takes its keys");
}

fn joined(parts: &Parts) -> String {
    [
        vec![parts.header.clone()],
        parts.status.clone(),
        vec![parts.columns.clone()],
        parts.rows.clone(),
        vec![parts.asks_title.clone()],
        parts.asks.clone(),
        vec![parts.log_title.clone()],
        parts.log.clone(),
    ]
    .concat()
    .join("\n")
}

fn squeezed(text: &str) -> String {
    text.split_whitespace().collect::<Vec<_>>().join(" ")
}

#[given(expr = "I start {string} in a terminal and read its frames")]
#[when(expr = "I start {string} in a terminal and read its frames")]
fn started(w: &mut World, line: String) {
    let words = split(&line);
    assert_eq!(words.first().map(String::as_str), Some("ctxloom"), "{line}");
    let args: Vec<&str> = words[1..].iter().map(String::as_str).collect();
    let mut env = daemon_spec::env_of(w);
    env.push(("CTXLOOM_TEST_TERMINAL".to_string(), "1".to_string()));
    let env: Vec<(&str, &str)> = env.iter().map(|(k, v)| (k.as_str(), v.as_str())).collect();
    daemon_spec::hold_owner_live(w, "amber-quiet-heron");
    let mut child = w.common.project.start_with_input(&env, &args);
    let stdout = child.stdout.take().expect("stdout is piped");
    let keys = child.stdin.take().expect("stdin is piped");
    let (tx, frames) = channel();
    std::thread::spawn(move || {
        let mut frame = Vec::new();
        for line in BufReader::new(stdout).lines() {
            let Ok(line) = line else { break };
            if line == "---" {
                if tx.send(std::mem::take(&mut frame)).is_err() {
                    break;
                }
            } else {
                frame.push(line);
            }
        }
    });
    w.top_screen.screen = Some(Screen {
        child,
        keys,
        frames,
        seen: Vec::new(),
    });
}

#[when(expr = "I press {string}")]
fn pressed(w: &mut World, key: String) {
    drain(w);
    let seen = screen(w).seen.len();
    press_key(w, &key);
    // An answer, a confirmation or a refusal changes the frame: it is waited
    // for, so what a step asserts or reads on disk is after the key's effect.
    if key.len() == 1 && "123456789yn".contains(key.as_str()) {
        let deadline = Instant::now() + WAIT;
        while screen(w).seen.len() <= seen {
            let left = deadline.saturating_duration_since(Instant::now());
            let frame = screen(w)
                .frames
                .recv_timeout(left)
                .unwrap_or_else(|_| panic!("the key {key} changed nothing on the screen"));
            screen(w).seen.push(frame);
        }
        // A confirmed answer says it was made, or why not.
        if key == "y" {
            let frame = screen(w).seen.last().cloned().unwrap_or_default();
            assert!(
                frame.iter().any(|line| line.contains("answered")),
                "the confirmed answer was not made; the screen says:\n{}",
                frame.join("\n")
            );
        }
    }
    if key == "q" {
        let mut screen = w.top_screen.screen.take().expect("a screen was started");
        let status = screen.child.wait().expect("ctxloom finishes");
        let mut stderr = String::new();
        if let Some(mut pipe) = screen.child.stderr.take() {
            let _ = std::io::Read::read_to_string(&mut pipe, &mut stderr);
        }
        // The command's end is the scenario's last command.
        w.common.ran = Some(Ran {
            code: status.code().unwrap_or(-1),
            stdout: String::new(),
            stderr,
        });
    }
}

#[when(expr = "I press {string} until {string} is marked")]
fn pressed_until(w: &mut World, key: String, harp: String) {
    for _ in 0..20 {
        let frame = until(w, "with a row marked", |p| !marked(p).is_empty());
        let before = marked(&frame);
        if before == [harp.clone()] {
            return;
        }
        press_key(w, &key);
        // The key's effect is the frame that marks another row. A frame the
        // daemon's own news drew meanwhile (a log line) is not it, and a key
        // pressed again on it would move the mark twice.
        let deadline = Instant::now() + WAIT;
        loop {
            let left = deadline.saturating_duration_since(Instant::now());
            match screen(w).frames.recv_timeout(left) {
                Ok(next) => {
                    let moved = moves_the_mark(&before, &next);
                    screen(w).seen.push(next);
                    if moved {
                        break;
                    }
                }
                Err(e) => {
                    let log = daemon_spec::daemon_log_text(w);
                    let frames = screen(w).seen.clone();
                    panic!(
                        "the key {key} moved no mark ({e}); the frames so far: {frames:#?}\n{log}"
                    )
                }
            }
        }
    }
    panic!("{harp} was never marked");
}

/// Whether `frame` is the effect of a key that moved the mark off `before`:
/// a frame the daemon's own news drew (a log line) marks what it marked.
fn moves_the_mark(before: &[String], frame: &[String]) -> bool {
    marked_in(frame) != before
}

#[test]
fn test_a_frame_drawn_by_news_is_not_the_effect_of_a_key() {
    let frame = |marked: &str, log: &str| -> Vec<String> {
        [
            "ctxloom top  scope: this project  filter: none  sort: tree".to_string(),
            "  NAME              STATE AGENT OWNER ASKS".to_string(),
            format!(
                "{} amber-quiet-heron live 0",
                if marked == "a" { ">" } else { " " }
            ),
            format!(
                "{} brisk-copper-moth live 0",
                if marked == "b" { ">" } else { " " }
            ),
            "--- asks of x".to_string(),
            "--- log of x".to_string(),
            log.to_string(),
        ]
        .to_vec()
    };
    let before = marked_in(&frame("a", ""));
    assert!(
        !moves_the_mark(&before, &frame("a", "  06:40:30 started")),
        "the log line's frame is the news, not the key"
    );
    assert!(moves_the_mark(&before, &frame("b", "")));
}

/// The rows a frame marks.
fn marked_in(frame: &[String]) -> Vec<String> {
    marked(&parts(frame))
}

#[when(expr = "I press {string} and type {string}")]
fn pressed_and_typed(w: &mut World, key: String, text: String) {
    press_key(w, &key);
    press_key(w, &text);
}

#[when(expr = "I press {string} and type nothing")]
fn pressed_and_typed_nothing(w: &mut World, key: String) {
    press_key(w, &key);
    press_key(w, "");
}

#[when(expr = "the ask is answered from another terminal with {string}")]
fn answered_elsewhere(w: &mut World, option: String) {
    let frame = until(w, "with a row marked", |p| !marked(p).is_empty());
    let harp = marked(&frame).remove(0);
    let env = daemon_spec::env_of(w);
    let env: Vec<(&str, &str)> = env.iter().map(|(k, v)| (k.as_str(), v.as_str())).collect();
    let ran = w
        .common
        .project
        .run_with(&env, &["session", "approvals", "answer", &harp, &option]);
    assert_eq!(ran.code, 0, "{}", ran.said());
    // The screen is told by the push: it shows the ask settled.
    until(w, "with the ask settled", |p| {
        p.asks.iter().any(|l| l.contains("answered"))
    });
}

#[then(expr = "the first frame's header row is {string}")]
fn header_row(w: &mut World, want: String) {
    let frame = parts(&first(w));
    assert_eq!(squeezed(&frame.columns), squeezed(&want));
}

#[then(expr = "the first frame has a row for {string} and a row for {string}")]
fn first_rows(w: &mut World, a: String, b: String) {
    let frame = parts(&first(w));
    assert!(
        has_row(&frame, &a) && has_row(&frame, &b),
        "{:#?}",
        frame.rows
    );
}

#[then(expr = "the first frame has no row for {string} and none for {string}")]
fn first_no_rows(w: &mut World, a: String, b: String) {
    let frame = parts(&first(w));
    assert!(
        !has_row(&frame, &a) && !has_row(&frame, &b),
        "{:#?}",
        frame.rows
    );
}

#[then(expr = "the first frame has one row, {string}, marked \"> \"")]
fn first_one_row(w: &mut World, harp: String) {
    let frame = parts(&first(w));
    assert_eq!(frame.rows.len(), 1, "{:#?}", frame.rows);
    assert_eq!(marked(&frame), [harp]);
}

#[then(expr = "the first frame has no row, and a line {string}")]
fn first_no_row_and_line(w: &mut World, line: String) {
    let frame = parts(&first(w));
    assert!(frame.rows.is_empty(), "{:#?}", frame.rows);
    assert!(frame.status.contains(&line), "{:#?}", frame.status);
}

#[then(expr = "a later frame has a row for {string} and no line {string}")]
fn later_row_no_line(w: &mut World, harp: String, line: String) {
    until(w, "with the row and no waiting line", |p| {
        has_row(p, &harp) && !p.status.contains(&line)
    });
}

#[then("exactly one row is marked \"> \", the first row until a key moves it")]
fn one_marked(w: &mut World) {
    let frame = parts(&first(w));
    assert_eq!(marked(&frame).len(), 1, "{:#?}", frame.rows);
    assert!(frame.rows[0].starts_with("> "), "{:#?}", frame.rows);
}

#[then(
    "the table comes before the line \"--- asks of <the selected row's harp>\", which comes before the line \"--- log of <the selected row's harp>\""
)]
fn sections_in_order(w: &mut World) {
    let frame = parts(&first(w));
    let harp = marked(&frame).remove(0);
    assert_eq!(frame.asks_title, format!("--- asks of {harp}"));
    assert_eq!(frame.log_title, format!("--- log of {harp}"));
}

#[then("nothing but the table's rows, those two sections and the header is in the frame")]
fn nothing_else(w: &mut World) {
    let frame = parts(&first(w));
    assert!(frame.status.is_empty(), "{:#?}", frame.status);
    for line in frame.asks.iter().chain(&frame.log) {
        assert!(
            line.starts_with("  "),
            "{line:?} is no entry of its section"
        );
    }
}

#[then(expr = "the first frame's rows are, in order, {string}, {string}, {string}")]
fn rows_in_order(w: &mut World, a: String, b: String, c: String) {
    let frame = parts(&first(w));
    let names: Vec<String> = frame.rows.iter().map(|r| name_of(r)).collect();
    assert_eq!(names, [a, b, c]);
}

#[then("the next frame's rows are in the sort's order, none indented")]
fn flat_rows(w: &mut World) {
    let frame = until(w, "sorted flat", |p| !p.header.contains("sort: tree"));
    assert!(
        frame.rows.iter().all(|r| name_of(r) == harp_of(r)),
        "{:#?}",
        frame.rows
    );
}

#[then(regex = r#"^the frame has "(--- asks of [^"]+)" and its asks section holds "([^"]*)"$"#)]
fn asks_section(w: &mut World, title: String, holds: String) {
    until(w, "with that asks section", |p| {
        p.asks_title == title && p.asks.iter().any(|l| l.contains(&holds))
    });
}

#[then(regex = r#"^the frame has "(--- asks of [^"]+)" and its asks section holds no ask$"#)]
fn asks_section_empty(w: &mut World, title: String) {
    until(w, "with that empty asks section", |p| {
        p.asks_title == title && p.asks.is_empty()
    });
}

#[then(regex = r#"^the frame's row for "([^"]+)" has "([^"]+)"(?: in its ASKS column)?$"#)]
fn asks_column(w: &mut World, harp: String, count: String) {
    until(w, "with that ASKS count", |p| {
        p.rows
            .iter()
            .any(|r| harp_of(r) == harp && r.split_whitespace().last() == Some(count.as_str()))
    });
}

#[then(
    regex = r#"^the asks section holds the ask's name, "Bash", "rm -rf build/" and the options "1 allow-once", "2 allow-session", "3 allow-always", "4 deny"$"#
)]
fn asks_listed(w: &mut World) {
    until(w, "with the ask and its options", |p| {
        let text = p.asks.join("\n");
        text.contains("ask-")
            && text.contains("Bash")
            && text.contains("rm -rf build/")
            && [
                "1 allow-once",
                "2 allow-session",
                "3 allow-always",
                "4 deny",
            ]
            .iter()
            .all(|o| text.contains(o))
    });
}

#[then(expr = "a later frame's asks section for {string} says the ask was answered {string}")]
fn asks_answered(w: &mut World, harp: String, option: String) {
    until(w, "with the ask answered", |p| {
        p.asks_title == format!("--- asks of {harp}")
            && p.asks
                .iter()
                .any(|l| l.contains(&format!("answered {option}")))
    });
}

#[then(regex = r#"^the frame shows "([^"]+)" and "([^"]+)" and "([^"]+)"$"#)]
fn frame_shows(w: &mut World, a: String, b: String, c: String) {
    until(w, "showing all three", |p| {
        let text = joined(p);
        [&a, &b, &c].iter().all(|t| text.contains(t.as_str()))
    });
}

#[then(regex = r#"^the frame says the ask was already settled, answered "([^"]*)"$"#)]
fn already_settled(w: &mut World, option: String) {
    until(w, "saying the ask was settled", |p| {
        let text = joined(p);
        text.contains("already settled") && text.contains(&option)
    });
}

#[then("the child's engine was not told to run the tool")]
fn not_told(w: &mut World) {
    daemon_spec::engine_not_told_to_run(w);
}

#[then(expr = "the log section has {string} and then {string}")]
fn log_has_then(w: &mut World, a: String, b: String) {
    until(w, "with that log", |p| {
        let text = p.log.join("\n");
        matches!((text.find(&a), text.find(&b)), (Some(x), Some(y)) if x < y)
    });
}

#[then(expr = "a later frame's log section ends with {string}")]
fn log_ends_with(w: &mut World, text: String) {
    until(w, "with that log end", |p| {
        p.log.last().is_some_and(|l| l.contains(&text))
    });
}

#[then(expr = "the frame's header names the filter {string}")]
fn header_filter(w: &mut World, filter: String) {
    until(w, "naming the filter", |p| p.header.contains(&filter));
}

#[then(expr = "the frame's header names the sort {string}")]
fn header_sort(w: &mut World, sort: String) {
    until(w, "naming the sort", |p| p.header.contains(&sort));
}

#[then(expr = "the frame has a row for {string} and no row for {string}")]
fn row_and_no_row(w: &mut World, yes: String, no: String) {
    until(w, "with and without", |p| {
        has_row(p, &yes) && !has_row(p, &no)
    });
}

#[then(expr = "the frame has a row for {string} again")]
fn row_again(w: &mut World, harp: String) {
    until(w, "with the row", |p| has_row(p, &harp));
}

#[then(expr = "the row for {string} is above the row for {string}")]
fn above(w: &mut World, upper: String, lower: String) {
    until(w, "with the rows in order", |p| {
        let at = |h: &str| p.rows.iter().position(|r| harp_of(r) == h);
        matches!((at(&upper), at(&lower)), (Some(a), Some(b)) if a < b)
    });
    let _ = row_of;
}

// ------------------------------------------------- the journey's screen

/// Alice's `ctxloom top`, at her terminal, once her orchestrator's launches
/// are made.
fn alice_opens(w: &mut World) {
    super::top::launch_pending(w);
    started(w, "ctxloom top --format text".to_string());
}

#[when("Alice opens `ctxloom top` at her terminal")]
fn alice_opens_top(w: &mut World) {
    alice_opens(w);
}

#[given("Alice has `ctxloom top` open at her terminal")]
fn alice_has_top_open(w: &mut World) {
    alice_opens(w);
}

#[given(expr = "Alice has `ctxloom top` open at her terminal, with {string} selected")]
fn alice_has_top_open_selected(w: &mut World, harp: String) {
    alice_opens(w);
    pressed_until(w, "down".to_string(), harp);
}

#[when(expr = "Alice selects {string}")]
fn alice_selects(w: &mut World, harp: String) {
    pressed_until(w, "down".to_string(), harp);
}

#[given(regex = r#"^the engine of "([^"]+)" has asked to run "([^"]+)" with "([^"]*)"$"#)]
fn engine_has_asked(w: &mut World, child: String, tool: String, input: String) {
    daemon_spec::ask_holding_alarms(w, &child, &tool, &input);
}

#[then(
    regex = r#"^the table's top part has a row for "([^"]+)" with its children "([^"]+)" and "([^"]+)" under it$"#
)]
fn tree_rows(w: &mut World, owner: String, a: String, b: String) {
    let frame = until(w, "with the tree", |p| {
        [&owner, &a, &b].iter().all(|h| has_row(p, h))
    });
    let indent = |harp: &str| name_of(row_of(&frame, harp)).len() - harp.len();
    let at = |harp: &str| frame.rows.iter().position(|r| harp_of(r) == harp);
    for child in [&a, &b] {
        assert!(
            indent(child) > indent(&owner),
            "{child} is not under {owner}"
        );
        assert!(at(child) > at(&owner), "{child} is above {owner}");
    }
}

#[then("each row shows its state, its agent, its engine and its owner")]
fn rows_show_columns(w: &mut World) {
    let frame = until(w, "with the rows", |p| p.rows.len() >= 3);
    for (harp, agent, owner) in [
        ("amber-quiet-heron", None, None),
        (
            "brisk-copper-moth",
            Some("reviewer"),
            Some("amber-quiet-heron"),
        ),
        (
            "quiet-silver-fern",
            Some("fixer"),
            Some("amber-quiet-heron"),
        ),
    ] {
        let row = row_of(&frame, harp);
        for want in ["live", "claude-code"]
            .into_iter()
            .chain(agent)
            .chain(owner)
        {
            assert!(row.contains(want), "{harp}'s row lacks {want}: {row}");
        }
    }
}

#[then("one row is selected, and the bottom part shows that session's asks and log")]
fn one_selected_with_details(w: &mut World) {
    let frame = until(w, "with a row marked", |p| !marked(p).is_empty());
    let selected = marked(&frame);
    assert_eq!(selected.len(), 1, "{selected:?}");
    assert_eq!(frame.asks_title, format!("--- asks of {}", selected[0]));
    assert_eq!(frame.log_title, format!("--- log of {}", selected[0]));
}

#[then(expr = "the row of {string} shows one ask waiting")]
fn row_shows_one_ask(w: &mut World, harp: String) {
    asks_column(w, harp, "1".to_string());
}

#[then(
    expr = "the bottom part lists the ask, what it would run, and the options {string}, {string}, {string} and {string}"
)]
fn bottom_lists_ask(w: &mut World, a: String, b: String, c: String, d: String) {
    until(w, "with the ask and its options", |p| {
        let text = p.asks.join("\n");
        text.contains("ask-")
            && text.contains("Bash")
            && text.contains("rm -rf build/")
            && [&a, &b, &c, &d].iter().all(|o| text.contains(o.as_str()))
    });
}

#[then(
    expr = "her first screen shows the ask settled, answered {string}, and no ask waiting on the row"
)]
fn first_screen_settled(w: &mut World, option: String) {
    until(w, "with the ask settled", |p| {
        p.asks
            .iter()
            .any(|l| l.contains(&format!("answered {option}")))
            && marked(p)
                .first()
                .is_some_and(|harp| row_of(p, harp).split_whitespace().last() == Some("0"))
    });
}

#[when(expr = "Alice chooses {string} for the ask on the screen")]
fn alice_chooses(w: &mut World, option: String) {
    // The reading pane's asks take the choice: tab to them, then its number.
    let frame = until(w, "with the ask offered", |p| {
        p.asks.iter().any(|l| l.contains(&format!(" {option}")))
    });
    let number = frame
        .asks
        .iter()
        .flat_map(|l| {
            l.split_whitespace()
                .collect::<Vec<_>>()
                .windows(2)
                .map(|w| (w[0].to_string(), w[1].to_string()))
                .collect::<Vec<_>>()
        })
        .find(|(n, name)| name == &option && n.len() == 1)
        .map(|(n, _)| n)
        .unwrap_or_else(|| panic!("{option} is not numbered: {:#?}", frame.asks));
    pressed(w, "tab".to_string());
    pressed(w, number);
}

#[then(expr = "the bottom part shows the ask settled, answered {string}, by her")]
fn bottom_settled_by_her(w: &mut World, option: String) {
    until(w, "with the ask settled by her", |p| {
        let text = p.asks.join("\n");
        text.contains(&format!("answered {option}"))
    });
}

#[then("the log pane shows what it said, then that its run ended, in that order")]
fn log_said_then_ended(w: &mut World) {
    // An ended session leaves a live view (its default state), so the pane
    // that showed both may be a frame the screen has moved on from: every
    // frame the scenario has seen is looked at, the selected session's.
    let (said, ended) = ("the migration is reversible", "exited");
    let deadline = Instant::now() + WAIT;
    loop {
        drain(w);
        let shown = |frame: &Vec<String>| {
            let p = parts(frame);
            let text = p.log.join("\n");
            p.log_title == "--- log of quiet-silver-fern"
                && matches!((text.find(said), text.find(ended)), (Some(x), Some(y)) if x < y)
        };
        if screen(w).seen.iter().any(shown) {
            return;
        }
        let left = deadline.saturating_duration_since(Instant::now());
        match screen(w).frames.recv_timeout(left) {
            Ok(frame) => screen(w).seen.push(frame),
            Err(_) => panic!("no frame showed the log in order: {:#?}", screen(w).seen),
        }
    }
}

#[then(expr = "her screen shows the ask expired, and no ask waiting on the row of {string}")]
fn screen_ask_expired(w: &mut World, harp: String) {
    // The reading pane is the selected row's: her screen shows the child's
    // ask where she looks at that child.
    pressed_until(w, "down".to_string(), harp.clone());
    until(w, "with the ask expired", |p| {
        p.asks.iter().any(|l| l.contains("expired"))
            && row_of(p, &harp).split_whitespace().last() == Some("0")
    });
}

#[then("it says it is waiting for a ctxloom daemon, and no ctxloom daemon was started")]
fn says_waiting(w: &mut World) {
    let frame = first(w);
    assert!(
        frame
            .iter()
            .any(|l| l.contains("waiting for a ctxloom daemon")),
        "{frame:#?}"
    );
    daemon_spec::nothing_started(w);
}

#[then(expr = "her screen shows {string} and {string}, and no longer says it is waiting")]
fn screen_shows_both(w: &mut World, a: String, b: String) {
    until(w, "with both rows and no waiting", |p| {
        has_row(p, &a) && has_row(p, &b) && !joined(p).contains("waiting for a ctxloom daemon")
    });
}

// ---------------------------------------------------------------- pty

/// Runs its arguments on a pseudo-terminal of the size asked: the program's
/// output goes to a file as it comes, its stdin is the terminal's keys, and
/// its end, with the terminal's local modes before and after, is a JSON
/// file. (A real pty is the point; nothing here draws.)
const PTY_HELPER: &str = r#"
import fcntl, json, os, pty, select, struct, sys, termios

out, status, cols, rows = sys.argv[1], sys.argv[2], int(sys.argv[3]), int(sys.argv[4])
argv = sys.argv[5:]
pid, fd = pty.fork()
if pid == 0:
    os.execvp(argv[0], argv)


def modes():
    try:
        return termios.tcgetattr(fd)[3]
    except Exception:
        return None


before = modes()
fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))
log = open(out, "wb", buffering=0)
keys = True
while True:
    ready, _, _ = select.select([fd] + ([0] if keys else []), [], [], 0.2)
    if fd in ready:
        try:
            data = os.read(fd, 65536)
        except OSError:
            break
        if not data:
            break
        log.write(data)
    if 0 in ready:
        data = os.read(0, 1024)
        if data:
            os.write(fd, data)
        else:
            keys = False
_, state = os.waitpid(pid, 0)
with open(status, "w") as done:
    json.dump({"code": os.waitstatus_to_exitcode(state), "before": before, "after": modes()}, done)
"#;

/// The alternate screen's switches and the cursor's.
const ENTER_ALTERNATE: &str = "\x1b[?1049h";
const LEAVE_ALTERNATE: &str = "\x1b[?1049l";

/// A real terminal's run, beside the scenario.
pub(super) struct Pty {
    child: Child,
    keys: ChildStdin,
    out: std::path::PathBuf,
    status: std::path::PathBuf,
}

impl Drop for Pty {
    fn drop(&mut self) {
        let _ = self.child.kill();
        let _ = self.child.wait();
    }
}

fn shown(pty: &Pty) -> String {
    files::read(&pty.out).unwrap_or_default()
}

#[given("a unix host with a pseudo-terminal")]
fn unix_host(_w: &mut World) {
    let ok = std::process::Command::new("python3")
        .args(["-I", "-c", "import pty"])
        .status()
        .is_ok_and(|s| s.success());
    assert!(ok, "python3 with its pty module is needed on PATH");
}

#[when(regex = r#"^I start "([^"]+)" on a pseudo-terminal of (\d+) columns and (\d+) rows$"#)]
fn started_on_pty(w: &mut World, line: String, columns: String, rows: String) {
    let words = split(&line);
    assert_eq!(words.first().map(String::as_str), Some("ctxloom"), "{line}");
    let args: Vec<&str> = words[1..].iter().map(String::as_str).collect();
    let env = daemon_spec::env_of(w);
    let env: Vec<(&str, &str)> = env.iter().map(|(k, v)| (k.as_str(), v.as_str())).collect();
    let dir = w.common.project.home().join("pty");
    files::mkdirs(&dir);
    let helper = dir.join("pty-helper.py");
    files::put(&helper, PTY_HELPER);
    let (out, status) = (dir.join("out"), dir.join("status.json"));
    let mut child = w.common.project.start_on_pty(
        &env,
        &args,
        [helper.as_path(), out.as_path(), status.as_path()],
        (&columns, &rows),
    );
    let keys = child.stdin.take().expect("stdin is piped");
    w.top_screen.pty = Some(Pty {
        child,
        keys,
        out,
        status,
    });
}

#[then(
    regex = r#"^within (\d+) seconds the terminal shows the row "([^"]+)" and the line "([^"]+)"$"#
)]
fn terminal_shows(w: &mut World, seconds: u64, row: String, line: String) {
    let deadline = Instant::now() + Duration::from_secs(seconds);
    loop {
        let pty = w.top_screen.pty.as_ref().expect("a terminal was started");
        let text = shown(pty);
        if text.contains(&row) && text.contains(&line) {
            return;
        }
        assert!(
            Instant::now() < deadline,
            "the terminal never showed {row:?} and {line:?}: {text:?}"
        );
        std::thread::sleep(Duration::from_millis(100));
    }
}

#[then("the terminal is on its alternate screen")]
fn on_alternate(w: &mut World) {
    let text = shown(w.top_screen.pty.as_ref().expect("a terminal was started"));
    let entered = text
        .rfind(ENTER_ALTERNATE)
        .expect("it entered the alternate screen");
    assert!(
        text.rfind(LEAVE_ALTERNATE)
            .is_none_or(|left| left < entered),
        "it left it already"
    );
}

#[when(expr = "I type {string} on it")]
fn typed_on_pty(w: &mut World, text: String) {
    let pty = w.top_screen.pty.as_mut().expect("a terminal was started");
    pty.keys
        .write_all(text.as_bytes())
        .and_then(|()| pty.keys.flush())
        .expect("the terminal takes keys");
    if text == "q" {
        // The command's end is the scenario's last command.
        let done = ended(w);
        w.common.ran = Some(Ran {
            code: done["code"].as_i64().unwrap_or(-1) as i32,
            stdout: String::new(),
            stderr: String::new(),
        });
    }
}

/// The run's end: its status file, waited for.
fn ended(w: &mut World) -> serde_json::Value {
    let pty = w.top_screen.pty.as_mut().expect("a terminal was started");
    let deadline = Instant::now() + WAIT;
    loop {
        if let Ok(text) = files::read(&pty.status) {
            if let Ok(value) = serde_json::from_str(&text) {
                return value;
            }
        }
        assert!(
            Instant::now() < deadline,
            "the run never ended: {:?}",
            shown(pty)
        );
        std::thread::sleep(Duration::from_millis(100));
    }
}

#[then("the terminal is back on its main screen, its modes as they were before")]
fn back_on_main(w: &mut World) {
    let done = ended(w);
    let text = shown(w.top_screen.pty.as_ref().expect("a terminal was started"));
    let entered = text
        .rfind(ENTER_ALTERNATE)
        .expect("it entered the alternate screen");
    let left = text
        .rfind(LEAVE_ALTERNATE)
        .expect("it left the alternate screen");
    assert!(entered < left, "{text:?}");
    assert!(
        text.rfind("\x1b[?25h").is_some_and(|at| at > entered),
        "the cursor was not shown again"
    );
    // The line discipline's local modes (echo, canonical input) are back,
    // where the kernel still tells them.
    if !done["before"].is_null() && !done["after"].is_null() {
        assert_eq!(done["before"], done["after"], "{done}");
    }
}
