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
        let seen = screen(w).seen.len();
        let frame = until(w, "with a row marked", |p| !marked(p).is_empty());
        if marked(&frame) == [harp.clone()] {
            return;
        }
        press_key(w, &key);
        let deadline = Instant::now() + WAIT;
        while screen(w).seen.len() <= seen {
            let left = deadline.saturating_duration_since(Instant::now());
            let next = screen(w)
                .frames
                .recv_timeout(left)
                .expect("the key changed the frame");
            screen(w).seen.push(next);
        }
    }
    panic!("{harp} was never marked");
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
