//! The user daemon end to end (D18, D2, D25, D28, D29): the real binary, a
//! temporary home, a fake claude that speaks the probe's stream-json. An
//! owner session launches a child; the daemon mints it, starts it (a
//! long-lived streaming child), hands it its first task; the child's turn
//! ends and the owner is told (a pending inform, D30); a wake to the child
//! is its second turn; the owner's close cascades to the child, which the
//! daemon ends. Nothing is timed: each step waits for what the store, or the
//! fake's own log, says happened (bounded, so a hang fails), and the daemon
//! is stopped and seen gone at the end.

use std::time::{Duration, Instant};

use serde_json::Value;

use super::files;
use super::library::Project;

/// The fake claude (engine-claude's child stub): an init, the echo of each
/// message given, a result; every line it is given appended to `input.log`
/// beside it; `exited` written when its input ends (the daemon's end
/// closes it).
const STUB: &str = r#"#!/bin/sh
here=$(dirname "$0")
sid=S1
printf '{"type":"system","subtype":"init","cwd":"/work","session_id":"%s","tools":[]}\n' "$sid"
while IFS= read -r line; do
  printf '%s\n' "$line" >> "$here/input.log"
  text=$(printf '%s' "$line" | sed 's/.*"content":"\([^"]*\)".*/\1/')
  printf '{"type":"user","message":{"role":"user","content":"%s"},"session_id":"%s","isReplay":true}\n' "$text" "$sid"
  printf '{"type":"result","subtype":"success","is_error":false,"result":"re: %s","session_id":"%s"}\n' "$text" "$sid"
done
printf 'done' > "$here/exited"
"#;

const OWNER: &str = "amber-swift-owl";
const CHILD: &str = "lone-wolf-pup";

/// The environment every command of the scenario runs with: a short idle
/// for the daemon it spawns (a daemon left behind exits on its own).
const ENV: [(&str, &str); 1] = [("CTXLOOM_DAEMON_IDLE_SECONDS", "60")];

struct World {
    project: Project,
    stub_dir: std::path::PathBuf,
}

impl World {
    fn new() -> World {
        let project = Project::initialized();
        let stub_dir = project.home().join("fake-claude");
        files::put_program(stub_dir.join("claude"), STUB);
        project.write_home(
            ".ctxloom/config.yaml",
            &format!(
                "schema_version: 9\neditor:\n  command: \"true\"\nengines:\n  claude-code:\n    command: {}\n",
                stub_dir.join("claude").display()
            ),
        );
        World { project, stub_dir }
    }

    fn run(&self, args: &[&str]) -> super::Ran {
        self.project.run_with(&ENV, args)
    }

    fn ok(&self, args: &[&str]) -> Value {
        let ran = self.run(args);
        assert_eq!(ran.code, 0, "{args:?}: {}\n{}", ran.said(), self.log());
        ran.json()
    }

    fn show(&self, harp: &str) -> Value {
        self.ok(&["session", "show", harp, "--format", "json"])
    }

    fn log(&self) -> String {
        files::read(self.project.home().join(".ctxloom/daemon/daemon.log")).unwrap_or_default()
    }

    fn input(&self) -> String {
        files::read(self.stub_dir.join("input.log")).unwrap_or_default()
    }

    /// Wait for `ready` to answer, at most a minute.
    fn until<T>(&self, what: &str, mut ready: impl FnMut(&World) -> Option<T>) -> T {
        let deadline = Instant::now() + Duration::from_secs(60);
        loop {
            if let Some(found) = ready(self) {
                return found;
            }
            assert!(
                Instant::now() < deadline,
                "timed out waiting for {what}; the daemon's log:\n{}",
                self.log()
            );
            std::thread::sleep(Duration::from_millis(50));
        }
    }

    /// The notices pending on the owner: their texts, in the order sent.
    fn notices(&self) -> Vec<String> {
        self.show(OWNER)["pending"]
            .as_array()
            .map(|pending| {
                pending
                    .iter()
                    .filter(|m| {
                        m["message_id"]
                            .as_str()
                            .unwrap_or("")
                            .starts_with("notice:")
                    })
                    .map(|m| m["text"].as_str().unwrap_or("").to_string())
                    .collect()
            })
            .unwrap_or_default()
    }

    fn serving(&self) -> bool {
        self.ok(&["daemon", "status", "--format", "json"])["serving"] == Value::Bool(true)
    }
}

impl Drop for World {
    /// A failing test still leaves no daemon behind: it is asked to stop,
    /// and seen gone (bounded) before the home is removed.
    fn drop(&mut self) {
        let _ = self.run(&["daemon", "stop"]);
        let deadline = Instant::now() + Duration::from_secs(20);
        while Instant::now() < deadline && self.serving() {
            std::thread::sleep(Duration::from_millis(50));
        }
    }
}

#[test]
fn an_owners_child_runs_its_turns_tells_its_owner_and_ends_with_the_owners_close() {
    let world = World::new();
    let seeded = world.run(&["test", "seed-session", OWNER]);
    assert_eq!(seeded.code, 0, "{}", seeded.said());
    let owner_id = seeded.stdout.trim().to_string();
    assert!(!world.serving(), "no daemon before a client needs one");

    // K2: the user launches a child with its first task; the daemon is
    // spawned on demand.
    let launched = world.ok(&[
        "daemon",
        "launch",
        "--owner",
        OWNER,
        "--name",
        CHILD,
        "--prompt",
        "first task",
        "--format",
        "json",
    ]);
    assert_eq!(launched["harp"], OWNER);
    assert_eq!(launched["recorded"], Value::Bool(true));
    let child_id = launched["id"].as_str().unwrap().to_string();
    assert!(world.serving(), "the launch spawned the daemon");

    // The child is minted for its owner, started, and runs its first turn;
    // its turn's end is told to the owner, who takes no wake yet (D7): an
    // inform, pending (D30).
    let first = world.until("the first notice", |w| {
        let notices = w.notices();
        (notices.len() == 1).then(|| notices[0].clone())
    });
    assert_eq!(
        first,
        format!("child {CHILD} finished its turn for launch:{child_id}")
    );
    assert!(world.input().contains("first task"), "{}", world.input());
    let child = world.show(CHILD);
    assert_eq!(child["owner"], Value::String(owner_id.clone()));
    assert_eq!(
        child["native_session"], "S1",
        "the child's native id is bound"
    );
    assert_eq!(
        world.show(OWNER)["children"],
        Value::Array(vec![Value::String(child_id.clone())])
    );

    // D12: a wake to the child is its second turn, in the same process.
    let sent = world.ok(&[
        "daemon",
        "send",
        CHILD,
        "second task",
        "--urgency",
        "wake",
        "--id",
        "m2",
        "--format",
        "json",
    ]);
    assert_eq!(sent["recorded"], Value::Bool(true));
    let second = world.until("the second notice", |w| {
        let notices = w.notices();
        (notices.len() == 2).then(|| notices[1].clone())
    });
    assert_eq!(second, format!("child {CHILD} finished its turn for m2"));
    let input = world.input();
    let lines: Vec<&str> = input.lines().collect();
    assert_eq!(lines.len(), 2, "one process carried both turns: {input}");
    assert!(lines[0].contains("first task") && lines[1].contains("second task"));

    // D29: the owner's close cascades: the child is closed, the daemon ends
    // it, and the owner holds it no more.
    world.ok(&["daemon", "close", OWNER, "--format", "json"]);
    world.until("the child closed and released", |w| {
        let closed = w.show(CHILD)["closed"] == Value::Bool(true);
        let released = w.show(OWNER).get("children").is_none();
        (closed && released).then_some(())
    });
    world.until("the child's engine gone", |w| {
        files::exists(w.stub_dir.join("exited")).then_some(())
    });
    assert_eq!(world.show(OWNER)["closed"], Value::Bool(true));
    let refused = world.run(&["daemon", "send", CHILD, "too late", "--format", "json"]);
    // A refusal against state exits 1, as every session command's does.
    assert_eq!(refused.code, 1, "{}", refused.said());
    assert!(
        refused.stderr.contains("sessions-session-closed"),
        "{}",
        refused.said()
    );

    // The daemon drains and exits when asked.
    assert_eq!(world.run(&["daemon", "stop"]).code, 0);
    world.until("the daemon gone", |w| (!w.serving()).then_some(()));
}

#[test]
fn the_daemon_noun_is_hidden_and_a_stop_with_no_daemon_does_nothing() {
    let world = World::new();
    let help = world.run(&["--help"]);
    assert!(!help.stdout.contains("daemon"), "{}", help.stdout);
    assert_eq!(world.run(&["daemon", "stop"]).code, 0);
    assert!(!world.serving());
    let status = world.run(&["daemon", "status", "--format", "text"]);
    assert_eq!(status.stdout.trim(), "not serving");
    let bad = world.run(&["daemon", "send", OWNER, "x", "--urgency", "shout"]);
    assert_eq!(bad.code, 2, "{}", bad.said());
}
