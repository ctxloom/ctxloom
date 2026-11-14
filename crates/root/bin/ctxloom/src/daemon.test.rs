//! The user daemon end to end (D18, D2, D25, D28, D29): the real binary, a
//! temporary home, a fake claude that speaks the probe's stream-json. An
//! owner session launches a child; the daemon mints it, starts it (a
//! long-lived streaming child) with the environment its LAUNCHER sent (S6)
//! and its project's context (D18), hands it its first task; the child's turn
//! ends and the owner is told (a pending inform, D30); a wake to the child
//! is its second turn; a clear rotates its conversation (D17) and the next
//! wake runs on the new one; the owner's close cascades to the child, which
//! the daemon ends. A child launched with no credential is refused as any
//! child is (ruling 53). Nothing is timed: each step waits for what the
//! store, or the fake's own logs, says happened (bounded, so a hang fails),
//! and the daemon is stopped and seen gone at the end.

use std::time::{Duration, Instant};

use serde_json::Value;

use super::files;
use super::library::Project;

/// The fake claude (engine-claude's child stub): an init, the echo of each
/// message given, a result; a `/clear` rotates its session id (S1, then S2)
/// as claude's does. Beside it: every line it is given, after the session id
/// it was given under, in `input.log`; its environment in `env-<harp>.log`
/// and its context file in `context-<harp>.log`; `exited` once its input
/// ends (the daemon's end closes it).
const STUB: &str = r#"#!/bin/sh
here=$(dirname "$0")
harp=${CTXLOOM_SESSION##*/}
sid=S1
env | sort > "$here/env-$harp.log"
ctx=""
prev=""
for a in "$@"; do
  [ "$prev" = "--append-system-prompt-file" ] && ctx=$a
  prev=$a
done
[ -n "$ctx" ] && cat "$ctx" > "$here/context-$harp.log"
printf '{"type":"system","subtype":"init","cwd":"/work","session_id":"%s","tools":[]}\n' "$sid"
while IFS= read -r line; do
  printf '%s %s\n' "$sid" "$line" >> "$here/input.log"
  case "$line" in
    *'"content":"/clear"'*)
      sid=S2
      printf '{"type":"conversation_reset","new_conversation_id":"c2","trigger":"clear","session_id":"S1"}\n'
      printf '{"type":"system","subtype":"init","cwd":"/work","session_id":"%s","tools":[]}\n' "$sid"
      printf '{"type":"result","subtype":"success","is_error":false,"num_turns":0,"result":"","session_id":"%s"}\n' "$sid"
      ;;
    *)
      text=$(printf '%s' "$line" | sed 's/.*"content":"\([^"]*\)".*/\1/')
      printf '{"type":"user","message":{"role":"user","content":"%s"},"session_id":"%s","isReplay":true}\n' "$text" "$sid"
      printf '{"type":"result","subtype":"success","is_error":false,"result":"re: %s","session_id":"%s"}\n' "$text" "$sid"
      ;;
  esac
done
printf 'done' > "$here/exited"
"#;

/// The project's config: a default agent whose profile selects the demo
/// bundle, so a child's context is that project's (D18).
const PROJECT_CONFIG: &str = "schema_version: 9\nagents:\n  dev:\n    engine: claude-code\n    profiles:\n      - dev\ndefault_agent: dev\n";

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
        project.write(".ctxloom/config.yaml", PROJECT_CONFIG);
        project.bundle("demo");
        project.fragment("demo", "testing");
        project.profile("dev", "demo");
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

    /// Run `args` with `more` in the environment beside [`ENV`].
    fn run_also(&self, more: &[(&str, &str)], args: &[&str]) -> super::Ran {
        let mut env = ENV.to_vec();
        env.extend_from_slice(more);
        self.project.run_with(&env, args)
    }

    /// What the fake claude of `harp` wrote to `what-<harp>.log`.
    fn of(&self, what: &str, harp: &str) -> Option<String> {
        files::read(self.stub_dir.join(format!("{what}-{harp}.log"))).ok()
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
        // The daemon as it says itself (its proto3 JSON has a pid), or the
        // empty message when none serves.
        self.ok(&["daemon", "status", "--format", "json"])
            .get("pid")
            .is_some()
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

/// A client whose environment is not the launcher's spawns the daemon (an
/// inform to the owner, which takes one); then the user launches a child
/// with its first task (K2), from an environment of its own. The child's id.
fn spawn_then_launch(world: &World) -> String {
    // A client whose environment is not the launcher's spawns the daemon
    // (an inform to the owner, which takes one).
    let spawned = world.run_also(
        &[
            ("DAEMON_ONLY", "spawner"),
            ("ANTHROPIC_API_KEY", "spawners-key"), // gitleaks:allow
        ],
        &["daemon", "send", OWNER, "hello", "--urgency", "inform"],
    );
    assert_eq!(spawned.code, 0, "{}", spawned.said());
    assert!(world.serving(), "the send spawned the daemon");

    // K2: the user launches a child with its first task.
    let launched = world.run_also(
        &[
            ("ANTHROPIC_API_KEY", "launchers-key"), // gitleaks:allow
            ("SECRET_ELSE", "not-for-a-child"),
            ("CTXLOOM_PERMIT_ACME_TOKEN", "permitted"),
        ],
        &[
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
        ],
    );
    assert_eq!(launched.code, 0, "{}\n{}", launched.said(), world.log());
    let launched = launched.json();
    assert_eq!(launched["harp"], OWNER);
    assert_eq!(launched["recorded"], Value::Bool(true));
    launched["id"].as_str().unwrap().to_string()
}

/// S6 and D18: what the child started with.
fn sees_its_launchers_environment_and_its_projects_context(world: &World) {
    // S6: the child's environment is its launcher's (what a child may be
    // given, and its permits), never the daemon's own.
    let env = world
        .of("env", CHILD)
        .expect("the child wrote its environment");
    assert!(env.contains("ANTHROPIC_API_KEY=launchers-key\n"), "{env}"); // gitleaks:allow
    assert!(env.contains("ACME_TOKEN=permitted\n"), "{env}");
    for never in ["spawner", "spawners-key", "SECRET_ELSE", "CTXLOOM_PERMIT_"] {
        assert!(!env.contains(never), "{never}: {env}");
    }
    // Nothing the launcher sent is left at rest for the daemon to read.
    let handed = world.project.home().join(".ctxloom/daemon/launch");
    assert!(files::children(&handed).is_empty(), "the hand-off is taken");
    // D18: the child's context is its project's, as a one-shot child's.
    let context = world.of("context", CHILD).expect("the child had a context");
    assert!(context.contains("FRAGMENT-BODY-testing"), "{context}");
}

/// M1, D17: a clear, its rotation, and the wake after it.
fn clears_and_runs_on_the_new_conversation(world: &World) {
    // M1, D17: a clear is the engine's own `/clear`; its rotation is bound
    // to the session, and the next wake runs on the new conversation.
    world.ok(&["daemon", "clear", CHILD, "--format", "json"]);
    world.until("the clear's rotation bound", |w| {
        (w.show(CHILD)["native_session"] == "S2").then_some(())
    });
    world.ok(&[
        "daemon",
        "send",
        CHILD,
        "third task",
        "--id",
        "m3",
        "--format",
        "json",
    ]);
    let third = world.until("the third notice", |w| {
        let notices = w.notices();
        (notices.len() == 3).then(|| notices[2].clone())
    });
    assert_eq!(third, format!("child {CHILD} finished its turn for m3"));
    let input = world.input();
    let lines: Vec<&str> = input.lines().collect();
    assert_eq!(lines.len(), 4, "{input}");
    assert!(
        lines[2].starts_with("S1 ") && lines[2].contains("/clear"),
        "{input}"
    );
    assert!(
        lines[3].starts_with("S2 ") && lines[3].contains("third task"),
        "{input}"
    );
}

#[test]
fn an_owners_child_runs_its_turns_tells_its_owner_and_ends_with_the_owners_close() {
    let world = World::new();
    let seeded = world.run(&["test", "seed-session", OWNER]);
    assert_eq!(seeded.code, 0, "{}", seeded.said());
    let owner_id = seeded.stdout.trim().to_string();
    assert!(!world.serving(), "no daemon before a client needs one");

    let child_id = spawn_then_launch(&world);

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
    // Each named by its resource name (ruling 146's proto3 JSON).
    let owner = world.show(OWNER);
    assert_eq!(owner["uid"], Value::String(owner_id.clone()));
    assert_eq!(child["owner"], owner["name"]);
    assert_eq!(child["uid"], Value::String(child_id.clone()));
    assert_eq!(
        child["native_session"], "S1",
        "the child's native id is bound"
    );
    assert_eq!(owner["children"], Value::Array(vec![child["name"].clone()]));

    sees_its_launchers_environment_and_its_projects_context(&world);

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

    clears_and_runs_on_the_new_conversation(&world);

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
    // A refusal against state exits 20, as every session command's does
    // (ruling 152).
    assert_eq!(refused.code, 20, "{}", refused.said());
    assert!(
        refused.stderr.contains("sessions-session-closed"),
        "{}",
        refused.said()
    );

    // The daemon drains and exits when asked.
    assert_eq!(world.run(&["daemon", "stop"]).code, 0);
    world.until("the daemon gone", |w| (!w.serving()).then_some(()));
}

// Rulings 53, 69, 88: a child the daemon hosts is judged at its enqueue on
// the credential its launcher sent, as any child is: with none, the run is
// refused (`runs-no-credential`), nothing is started, and its owner holds it
// no longer (K2's compensation, naming the refusal).
#[test]
fn a_child_launched_with_no_credential_is_refused_and_its_owner_released() {
    let world = World::new();
    let seeded = world.run(&["test", "seed-session", OWNER]);
    assert_eq!(seeded.code, 0, "{}", seeded.said());
    world.ok(&[
        "daemon", "launch", "--owner", OWNER, "--name", CHILD, "--prompt", "a task", "--format",
        "json",
    ]);
    world.until("the launch refused", |w| {
        let released = w.show(OWNER).get("children").is_none();
        (released && w.log().contains("runs-no-credential")).then_some(())
    });
    assert!(world.of("env", CHILD).is_none(), "nothing was started");
    assert!(world.input().is_empty(), "{}", world.input());
}

#[test]
fn the_daemon_noun_is_hidden_and_a_stop_with_no_daemon_does_nothing() {
    let world = World::new();
    let help = world.run(&["--help"]);
    assert!(!help.stdout.contains("daemon"), "{}", help.stdout);
    assert_eq!(world.run(&["daemon", "stop"]).code, 0);
    assert!(!world.serving());
    let status = world.run(&["daemon", "status", "--format", "text"]);
    assert_eq!(status.stdout.trim(), "no ctxloom daemon is serving");
    let bad = world.run(&["daemon", "send", OWNER, "x", "--urgency", "shout"]);
    // A usage error is refused, 20 (ruling 152).
    assert_eq!(bad.code, 20, "{}", bad.said());
}

/// A protobuf varint at `at`: its value and the index after it.
fn varint(bytes: &[u8], mut at: usize) -> Option<(u64, usize)> {
    let mut value = 0u64;
    for shift in (0..64).step_by(7) {
        let byte = *bytes.get(at)?;
        at += 1;
        value |= u64::from(byte & 0x7f) << shift;
        if byte & 0x80 == 0 {
            return Some((value, at));
        }
    }
    None
}

/// The bytes of the length-delimited field `number` of a protobuf message
/// (varint and length-delimited fields only, as the discovery holds).
fn field(bytes: &[u8], number: u64) -> Option<Vec<u8>> {
    let mut at = 0;
    while at < bytes.len() {
        let (tag, next) = varint(bytes, at)?;
        at = match tag & 7 {
            0 => varint(bytes, next)?.1,
            2 => {
                let (len, start) = varint(bytes, next)?;
                let end = start + usize::try_from(len).ok()?;
                if tag >> 3 == number {
                    return bytes.get(start..end).map(<[u8]>::to_vec);
                }
                end
            }
            _ => return None,
        };
    }
    None
}

// The discovery file (slice 2: T12, S5, M3): protobuf, 0600 in a 0700
// directory, naming the binary's own full stamp (never the package
// version alone) and a fresh 64-hex user bearer at each start; the old
// `daemon.endpoint` line file is gone.
#[test]
fn a_serving_daemon_advertises_its_stamp_and_a_new_bearer_in_a_private_discovery_file() {
    let world = World::new();
    let stamp = world.ok(&["version", "--format", "json"])["version"]
        .as_str()
        .unwrap()
        .to_string();
    let dir = world.project.home().join(".ctxloom/daemon");
    let path = dir.join("daemon.discovery");
    let started = |world: &World| {
        let daemon = world
            .project
            .start_with(&ENV, &["daemon", "serve", "--idle-seconds", "30"]);
        let bytes = world.until("the discovery file", |_| {
            files::read_bytes(&path).ok().filter(|b| !b.is_empty())
        });
        (daemon, bytes)
    };
    let stop = |world: &World, mut daemon: std::process::Child| {
        let _ = world.run(&["daemon", "stop"]);
        let _ = daemon.wait();
    };
    let (daemon, first) = started(&world);
    assert_eq!(
        field(&first, 7).as_deref(),
        Some(stamp.as_bytes()),
        "the stamp {stamp} is named"
    );
    assert!(
        !files::exists(dir.join("daemon.endpoint")),
        "no line file is written"
    );
    if os::host().flavor() == os::Flavor::Unix {
        assert_eq!(files::mode_of(&path), Some(0o600));
        assert_eq!(files::mode_of(&dir), Some(0o700));
    }
    let bearer_of = |bytes: &[u8]| field(bytes, 5).expect("a bearer field");
    let bearer = bearer_of(&first);
    assert!(bearer.iter().all(|b| b.is_ascii_hexdigit()), "{bearer:?}");
    let log = world.log();
    assert!(
        !log.contains(std::str::from_utf8(&bearer).unwrap()),
        "the log never holds the bearer"
    );
    stop(&world, daemon);
    world.until("the daemon gone", |w| (!w.serving()).then_some(()));
    let (daemon, second) = started(&world);
    assert_ne!(bearer_of(&second), bearer, "each start mints a new bearer");
    stop(&world, daemon);
}

/// A daemon started by hand (`daemon serve`) and its discovery file's bytes,
/// once it has advertised itself.
fn served(world: &World) -> (std::process::Child, Vec<u8>) {
    let path = world
        .project
        .home()
        .join(".ctxloom/daemon/daemon.discovery");
    let daemon = world
        .project
        .start_with(&ENV, &["daemon", "serve", "--idle-seconds", "30"]);
    let bytes = world.until("the discovery file", |_| {
        files::read_bytes(&path).ok().filter(|b| !b.is_empty())
    });
    (daemon, bytes)
}

/// `daemon`'s exit, waited for at most `within`; none if it still runs.
fn exited_within(daemon: &mut std::process::Child, within: Duration) -> Option<i32> {
    let deadline = Instant::now() + within;
    while Instant::now() < deadline {
        if let Ok(Some(status)) = daemon.try_wait() {
            return Some(status.code().unwrap_or(-1));
        }
        std::thread::sleep(Duration::from_millis(50));
    }
    None
}

/// Send `signal` (`-STOP`, `-CONT`) to `pid`.
fn signal(pid: u32, signal: &str) {
    let sent = std::process::Command::new("kill")
        .args([signal, &pid.to_string()])
        .status()
        .expect("kill runs");
    assert!(sent.success(), "kill {signal} {pid}");
}

// T16, M11: liveness is the health check over the pinned certificate, not
// the single-instance lock: a daemon that holds the lock but does not answer
// (stopped) is not serving.
#[test]
fn a_daemon_that_holds_the_instance_lock_but_does_not_answer_is_not_serving() {
    if !files::runs_scripts() {
        return;
    }
    let world = World::new();
    let (mut daemon, _) = served(&world);
    assert!(world.serving(), "it answers");
    signal(daemon.id(), "-STOP");
    let status = world.run(&["daemon", "status", "--format", "json"]);
    signal(daemon.id(), "-CONT");
    assert_eq!(status.code, 0, "{}", status.said());
    assert_eq!(status.json(), serde_json::json!({}), "{}", status.said());
    let _ = world.run(&["daemon", "stop"]);
    let _ = daemon.wait();
}

// M11: the lock is the single-instance lock alone. A daemon whose discovery
// file is removed still holds the lock, so it advertises itself again (it no
// longer exits for want of one).
#[test]
fn a_daemon_whose_discovery_file_is_removed_advertises_itself_again() {
    let world = World::new();
    let (mut daemon, first) = served(&world);
    let path = world
        .project
        .home()
        .join(".ctxloom/daemon/daemon.discovery");
    files::remove(&path);
    let again = world.until("the discovery file again", |_| {
        files::read_bytes(&path).ok().filter(|b| !b.is_empty())
    });
    assert_eq!(again, first, "the same daemon, advertised again");
    assert_eq!(
        exited_within(&mut daemon, Duration::ZERO),
        None,
        "it serves on"
    );
    assert!(world.serving());
    let _ = world.run(&["daemon", "stop"]);
    let _ = daemon.wait();
}

// M11: a daemon whose discovery file names ANOTHER daemon has lost the
// single-instance lock, so it exits, and leaves the other's file as it is.
#[test]
fn a_daemon_whose_discovery_file_names_another_exits_and_leaves_it() {
    let world = World::new();
    let (mut daemon, first) = served(&world);
    let path = world
        .project
        .home()
        .join(".ctxloom/daemon/daemon.discovery");
    // Another daemon's: the same file with another bearer.
    let bearer = field(&first, 5).expect("a bearer");
    let at = first
        .windows(bearer.len())
        .position(|w| w == bearer.as_slice())
        .expect("the bearer's bytes");
    let mut another = first.clone();
    another[at] = if another[at] == b'0' { b'1' } else { b'0' };
    files::put(&path, &another);
    let code = exited_within(&mut daemon, Duration::from_secs(20));
    assert_eq!(code, Some(0), "it exits; its log:\n{}", world.log());
    assert_eq!(
        files::read_bytes(&path).unwrap(),
        another,
        "the other's is left"
    );
    assert!(world.log().contains("another daemon"), "{}", world.log());
}

/// A daemon child killed when dropped, so a failing test leaves none
/// behind where `World`'s own stop cannot reach it.
struct Killed(std::process::Child);

impl Drop for Killed {
    fn drop(&mut self) {
        let _ = self.0.kill();
        let _ = self.0.wait();
    }
}

/// The test-support variable naming the single-instance lock's directory.
const INSTANCE_DIR: &str = "CTXLOOM_TEST_INSTANCE_DIR";

// One daemon per OS user: a client whose ctxloom home is not the serving
// daemon's (two homes on one account, the account's lock shared) is refused,
// exit 20, naming both homes, and starts no second daemon; the daemon serves
// on.
#[test]
fn a_client_on_another_ctxloom_home_is_refused_naming_both_and_starts_no_daemon() {
    let world = World::new();
    let account = world.project.home().join("account");
    let other = world.project.home().join("other-home");
    files::put(other.join(".keep"), b"");
    let account_text = account.to_str().unwrap();
    let other_text = other.to_str().unwrap();
    let mut env = ENV.to_vec();
    env.push((INSTANCE_DIR, account_text));
    let mut daemon = Killed(
        world
            .project
            .start_with(&env, &["daemon", "serve", "--idle-seconds", "30"]),
    );
    world.until("the discovery file at the account's place", |_| {
        files::read_bytes(account.join("daemon.discovery"))
            .ok()
            .filter(|b| !b.is_empty())
    });
    let home = world.project.home().to_str().unwrap().to_string();
    let mut elsewhere = env.clone();
    elsewhere.push(("CTXLOOM_HOME", other_text));
    for verb in ["status", "stop"] {
        let ran = world
            .project
            .run_with(&elsewhere, &["daemon", verb, "--format", "json"]);
        assert_eq!(ran.code, 20, "{verb}: {}", ran.said());
        assert!(
            ran.stderr.contains(&home) && ran.stderr.contains(other_text),
            "{verb} names both homes: {}",
            ran.said()
        );
    }
    assert!(
        !files::exists(other.join(".ctxloom/daemon/daemon.discovery")),
        "no second daemon"
    );
    let status = world
        .project
        .run_with(&env, &["daemon", "status", "--format", "json"]);
    assert!(status.json().get("pid").is_some(), "{}", status.said());
    let _ = world.project.run_with(&env, &["daemon", "stop"]);
    assert_eq!(
        exited_within(&mut daemon.0, Duration::from_secs(20)),
        Some(0),
        "its log:\n{}",
        world.log()
    );
}

// The replaced-lock gap closed: a daemon whose single-instance lock file is
// removed and made again under it no longer excludes anyone, so it exits
// (and a client then starts one that holds the new file).
#[test]
fn a_daemon_whose_lock_file_is_replaced_exits() {
    let world = World::new();
    let (mut daemon, _) = served(&world);
    let lock = world.project.home().join(".ctxloom/daemon/daemon.lock");
    assert!(files::exists(&lock), "the lock file is made");
    files::remove(&lock);
    files::put(&lock, b"");
    let code = exited_within(&mut daemon, Duration::from_secs(20));
    assert_eq!(code, Some(0), "it exits; its log:\n{}", world.log());
    assert!(
        world.log().contains("single-instance lock"),
        "{}",
        world.log()
    );
}

// Ruling 131: a store is closed, never dropped. A daemon's exit lets go of
// everything that holds its store (the listener's tasks, the launcher, the
// read models it registered) before it closes it, so its log never says it
// did not close, and the store's file is left with no log beside it.
#[test]
fn a_daemons_exit_closes_its_store() {
    let world = World::new();
    let (mut daemon, _) = served(&world);
    let _ = world.run(&["daemon", "stop"]);
    let code = exited_within(&mut daemon, Duration::from_secs(20));
    assert_eq!(code, Some(0), "it exits; its log:\n{}", world.log());
    let log = world.log();
    assert!(log.contains("exited"), "{log}");
    assert!(!log.contains("did not close"), "{log}");
    let store = world.project.home().join(".ctxloom/ctxloom.db");
    assert!(files::exists(&store), "the daemon's store");
    for beside in ["ctxloom.db-wal", "ctxloom.db-shm"] {
        assert!(
            !files::exists(store.with_file_name(beside)),
            "{beside} is left beside the store; the log:\n{log}"
        );
    }
}

/// `future`, on a runtime of its own.
fn block_on<T>(future: impl std::future::Future<Output = T>) -> T {
    tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()
        .unwrap()
        .block_on(future)
}

/// `git args` in `dir`, which must succeed.
fn git(dir: &std::path::Path, args: &[&str]) -> String {
    let out = std::process::Command::new("git")
        .args(args)
        .current_dir(dir)
        .env("GIT_AUTHOR_NAME", "A")
        .env("GIT_AUTHOR_EMAIL", "a@x")
        .env("GIT_COMMITTER_NAME", "A")
        .env("GIT_COMMITTER_EMAIL", "a@x")
        .env("GIT_CONFIG_GLOBAL", "/dev/null")
        .output()
        .unwrap();
    assert!(out.status.success(), "git {args:?}: {out:?}");
    String::from_utf8_lossy(&out.stdout).trim().to_string()
}

/// A repository of one commit in the project's directory: its root commit.
fn repository(world: &World, message: &str) -> String {
    let dir = world.project.dir();
    git(dir, &["init", "-q"]);
    git(dir, &["commit", "-q", "--allow-empty", "-m", message]);
    git(dir, &["rev-list", "--max-parents=0", "HEAD"])
}

// Slice 2 step 5: the daemon serves the `Ctxloom` service beside its
// coordinator, on the one listener, to the user bearer alone: GetDaemon says
// its pid, endpoint and full stamp; another bearer is a bare UNAUTHENTICATED.
#[test]
fn a_daemon_serves_its_api_to_the_user_bearer_alone() {
    use daemon_ports::{Called, DaemonApi};
    let world = World::new();
    let (mut daemon, bytes) = served(&world);
    let found = run_ports::Discovery::decode(&bytes).expect("a discovery file");
    let within = Duration::from_secs(5);
    let about = block_on(
        daemon_client::DaemonClient::new(&found, within)
            .get_daemon(daemon_proto::v1::GetDaemonRequest::default()),
    )
    .unwrap_or_else(|e| panic!("{e:?}; the log:\n{}", world.log()));
    assert_eq!(about.pid, found.pid);
    assert_eq!(about.endpoint, found.endpoint);
    assert_eq!(about.version, found.stamp);
    assert_eq!(about.idle_limit_seconds, 30);
    assert!(about.runs.is_empty(), "it hosts nothing");
    let other = run_ports::Discovery {
        bearer: run_ports::Token::from_bytes([7; 32]),
        ..found
    };
    assert_eq!(
        block_on(
            daemon_client::DaemonClient::new(&other, within)
                .get_daemon(daemon_proto::v1::GetDaemonRequest::default())
        ),
        Err(Called::Unauthenticated)
    );
    let _ = world.run(&["daemon", "stop"]);
    assert_eq!(exited_within(&mut daemon, Duration::from_secs(20)), Some(0));
}

// Ruling 131 on a failed start: a daemon that cannot publish its discovery
// file (here a directory stands at its path) closes its store before it
// exits, as every exit does.
#[test]
fn a_daemon_whose_start_fails_closes_its_store() {
    let world = World::new();
    let dir = world
        .project
        .home()
        .join(".ctxloom/daemon/daemon.discovery");
    files::put(dir.join("in-the-way"), "");
    let mut daemon = world
        .project
        .start_with(&ENV, &["daemon", "serve", "--idle-seconds", "30"]);
    let code = exited_within(&mut daemon, Duration::from_secs(20));
    assert_ne!(code, Some(0), "the start fails; its log:\n{}", world.log());
    let log = world.log();
    assert!(log.contains("the start failed"), "{log}");
    assert!(log.contains("the store is closed"), "{log}");
    let store = world.project.home().join(".ctxloom/ctxloom.db");
    for beside in ["ctxloom.db-wal", "ctxloom.db-shm"] {
        assert!(
            !files::exists(store.with_file_name(beside)),
            "{beside}: {log}"
        );
    }
}

// Project registration (owner, 2026-11-12): the first `daemon resolve` names
// and registers the project with its repository's root commit; again, it
// answers the same; another repository at its place is refused (exit 20),
// naming the registered project.
#[test]
fn a_project_is_registered_then_rechecked_by_its_root_commit() {
    let world = World::new();
    let root = repository(&world, "first");
    let first = world.run(&["daemon", "resolve", "--format", "json"]);
    assert_eq!(first.code, 0, "{}\n{}", first.said(), world.log());
    let project = first.json();
    let name = project["name"].as_str().unwrap().to_string();
    assert!(name.starts_with("projects/"), "{project}");
    assert_eq!(project["root_commit"], root.as_str());
    assert!(
        project["git_common_dir"]
            .as_str()
            .unwrap()
            .ends_with(".git"),
        "{project}"
    );
    let again = world.run(&["daemon", "resolve", "--format", "json"]);
    assert_eq!(again.code, 0, "{}", again.said());
    assert_eq!(again.json()["name"], name.as_str());
    files::remove_tree(world.project.dir().join(".git"));
    let other = repository(&world, "another");
    assert_ne!(other, root);
    let refused = world.run(&["daemon", "resolve"]);
    assert_eq!(refused.code, 20, "{}", refused.said());
    assert!(refused.said().contains(&name), "{}", refused.said());
    assert!(
        refused.said().contains("not the repository"),
        "{}",
        refused.said()
    );
    let _ = world.run(&["daemon", "stop"]);
}

// M3: a client of another version finds an idle daemon, asks it, stops it
// and starts its own.
#[test]
fn a_client_of_another_version_stops_an_idle_daemon_and_starts_its_own() {
    let world = World::new();
    let (mut daemon, bytes) = served(&world);
    let old = run_ports::Discovery::decode(&bytes).unwrap();
    let ran = world.run_also(
        &[("CTXLOOM_TEST_STAMP", "0.0.0-another+build")],
        &["daemon", "resolve"],
    );
    assert_eq!(ran.code, 0, "{}\n{}", ran.said(), world.log());
    assert_eq!(
        exited_within(&mut daemon, Duration::from_secs(20)),
        Some(0),
        "the old daemon was stopped; its log:\n{}",
        world.log()
    );
    let path = world
        .project
        .home()
        .join(".ctxloom/daemon/daemon.discovery");
    let new = run_ports::Discovery::decode(&files::read_bytes(&path).unwrap()).unwrap();
    assert_ne!(new.pid, old.pid, "another daemon serves");
    let _ = world.run(&["daemon", "stop"]);
}

/// The test-support variable naming the OS account's home (the system's
/// otherwise; under test-support, HOME when none is named).
const ACCOUNT_HOME: &str = "CTXLOOM_TEST_ACCOUNT_HOME";

// M4, one daemon per OS USER: the daemon's home is the ACCOUNT's, never the
// HOME it was started with: its discovery file, its log and its files are
// the account's, and it names the account's home as the one it serves.
#[test]
fn a_daemons_home_is_its_accounts_never_the_home_it_was_started_with() {
    let world = World::new();
    let account = world.project.home().to_path_buf();
    let moved = account.join("moved-home");
    files::put(moved.join(".keep"), b"");
    let account_text = account.to_str().unwrap();
    let moved_text = moved.to_str().unwrap();
    let mut env = ENV.to_vec();
    env.extend([
        ("HOME", moved_text),
        ("USERPROFILE", moved_text),
        (ACCOUNT_HOME, account_text),
    ]);
    let mut daemon = Killed(
        world
            .project
            .start_with(&env, &["daemon", "serve", "--idle-seconds", "30"]),
    );
    let bytes = world.until("the discovery file at the account's home", |_| {
        files::read_bytes(account.join(".ctxloom/daemon/daemon.discovery"))
            .ok()
            .filter(|b| !b.is_empty())
    });
    assert_eq!(
        field(&bytes, 8).as_deref(),
        Some(account_text.as_bytes()),
        "it serves the account's home"
    );
    assert!(
        !files::exists(moved.join(".ctxloom")),
        "nothing under the HOME it was started with"
    );
    let _ = world.run(&["daemon", "stop"]);
    assert_eq!(
        exited_within(&mut daemon.0, Duration::from_secs(20)),
        Some(0),
        "its log:\n{}",
        world.log()
    );
}

/// What procfs says of process `pid`: its session id, and the names of its
/// environment's variables. None off Linux.
fn proc_of(pid: u32) -> Option<(u32, Vec<String>)> {
    let stat = files::read(format!("/proc/{pid}/stat")).ok()?;
    // `pid (comm) state ppid pgrp session ...`; comm may hold spaces.
    let after = &stat[stat.rfind(')')? + 2..];
    let session = after.split(' ').nth(3)?.parse().ok()?;
    let environ = files::read_bytes(format!("/proc/{pid}/environ")).ok()?;
    let names = environ
        .split(|b| *b == 0)
        .filter_map(|pair| std::str::from_utf8(pair).ok())
        .filter_map(|pair| pair.split_once('=').map(|(name, _)| name.to_string()))
        .collect();
    Some((session, names))
}

// T3, M8: a daemon a client starts is DETACHED, in a session of its own
// (it leads it: no terminal's hangup or the client's group reaches it), and
// is given no HOME or USERPROFILE of its spawner's (its home is the
// account's). Linux alone: procfs is how it is seen.
#[test]
fn a_daemon_a_client_starts_is_detached_and_given_none_of_its_homes() {
    if !files::exists("/proc/self/stat") {
        return;
    }
    let world = World::new();
    repository(&world, "first");
    let ran = world.run(&["daemon", "resolve"]);
    assert_eq!(ran.code, 0, "{}\n{}", ran.said(), world.log());
    let bytes = files::read_bytes(
        world
            .project
            .home()
            .join(".ctxloom/daemon/daemon.discovery"),
    )
    .unwrap();
    let pid = run_ports::Discovery::decode(&bytes).unwrap().pid;
    let (session, names) = proc_of(pid).expect("the daemon's procfs");
    assert_eq!(session, pid, "it leads a session of its own");
    for name in ["HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA"] {
        assert!(!names.iter().any(|n| n == name), "{name} in {names:?}");
    }
    let _ = world.run(&["daemon", "stop"]);
}

// One session-setup path (owner, 2026-11-12): a hosted child's session home
// is set up by the same code as a CLI run's: her git identity in the project
// is its `.gitconfig`, claude's own state is seeded (`.claude.json`, ruling
// 113) and its settings start from hers (ruling 169).
#[test]
fn a_hosted_childs_home_is_set_up_as_a_cli_runs_is() {
    let world = World::new();
    repository(&world, "first");
    let dir = world.project.dir().to_path_buf();
    git(&dir, &["config", "user.name", "Alice Example"]);
    git(&dir, &["config", "user.email", "alice@example.com"]);
    files::put(
        world.project.home().join(".claude/settings.json"),
        br#"{"theme": "hers-alone"}"#,
    );
    let seeded = world.run(&["test", "seed-session", OWNER]);
    assert_eq!(seeded.code, 0, "{}", seeded.said());
    spawn_then_launch(&world);
    world.until("the first notice", |w| {
        (!w.notices().is_empty()).then_some(())
    });
    let home = dir.join(format!(".ctxloom/sessions/{CHILD}/home"));
    let read = |rel: &str| files::read(home.join(rel)).unwrap_or_default();
    let git = read(".gitconfig");
    assert!(
        git.contains("Alice Example") && git.contains("alice@example.com"),
        "{git}"
    );
    assert!(
        read(".claude.json").contains("hasCompletedOnboarding"),
        "{}",
        read(".claude.json")
    );
    assert!(
        read("settings.json").contains("hers-alone"),
        "{}",
        read("settings.json")
    );
    let _ = world.run(&["daemon", "stop", "--even-if-live"]);
}

// T21, S7: a hosted child's settings start from HER claude settings as they
// are at its launch, where her launcher finds them (its CLAUDE_CONFIG_DIR),
// never where the daemon was started, nor as they were then.
#[test]
fn a_hosted_childs_settings_are_read_at_its_launch_from_its_launchers_place() {
    if !files::runs_scripts() {
        return;
    }
    let world = World::new();
    files::put(
        world.project.home().join(".claude/settings.json"),
        br#"{"theme": "the-default-place"}"#,
    );
    let seeded = world.run(&["test", "seed-session", OWNER]);
    assert_eq!(seeded.code, 0, "{}", seeded.said());
    let spawned = world.run(&["daemon", "send", OWNER, "hello", "--urgency", "inform"]);
    assert_eq!(spawned.code, 0, "{}", spawned.said());
    assert!(world.serving(), "the send spawned the daemon");
    // Written after the daemon started, in a place only the launcher names.
    let place = world.project.home().join(".claude-work");
    files::put(
        place.join("settings.json"),
        br#"{"theme": "hers-at-launch"}"#,
    );
    let place = place.display().to_string();
    let launched = world.run_also(
        &[
            ("ANTHROPIC_API_KEY", "launchers-key"), // gitleaks:allow
            ("CLAUDE_CONFIG_DIR", place.as_str()),
        ],
        &[
            "daemon",
            "launch",
            "--owner",
            OWNER,
            "--name",
            CHILD,
            "--prompt",
            "first task",
        ],
    );
    assert_eq!(launched.code, 0, "{}\n{}", launched.said(), world.log());
    world.until("the first notice", |w| {
        (!w.notices().is_empty()).then_some(())
    });
    let settings = files::read(
        world
            .project
            .dir()
            .join(format!(".ctxloom/sessions/{CHILD}/home/settings.json")),
    )
    .unwrap_or_default();
    assert!(settings.contains("hers-at-launch"), "{settings}");
    assert!(!settings.contains("the-default-place"), "{settings}");
    let _ = world.run(&["daemon", "stop", "--even-if-live"]);
}
