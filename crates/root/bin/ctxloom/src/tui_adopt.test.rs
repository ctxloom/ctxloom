//! A daemon dying under a live TUI (owner rulings, 2026-11-14), end to end:
//! the real binary, a temporary home, the terminal stood in
//! (`CTXLOOM_TEST_TERMINAL`) by a stub claude that records itself and runs
//! until it is released.
//!
//! - U5's start-report race, FORCED: the frontend waits at a gate (test
//!   support) between the daemon's answer to its plan request and its start
//!   report; the daemon is killed there and the next command starts a new
//!   one. The frontend took the run's own lock BEFORE it asked, so the new
//!   daemon's start reads the run's owner alive: nothing is orphaned, the
//!   run is adopted, and the TUI's exit ends its incarnation `runner-exit`.
//! - A daemon killed under a live TUI: the next daemon adopts the session,
//!   and the TUI's exit report ends the incarnation and lets its checkout go
//!   WIP-safe, as the daemon that prepared it would have.
//! - The frontend's reports held: no daemon can start while the start is
//!   reported, so it is held in the session's hold; the exit's attempt
//!   reaches a new daemon and delivers the held start, then the exit, in
//!   order.
//! - A TUI's exit held while no daemon serves (OD-59 (2)): the frontend
//!   exits, its exit report in the session's hold and its locks let go; the
//!   next daemon's start delivers that hold before its sweep judges the run,
//!   so the run ends with the TUI's own exit code, never `runner-lost`.
//!
//! Nothing is timed: each step waits, bounded, for what the store, the stub
//! or the files say happened.

use std::path::{Path, PathBuf};
use std::process::Child;
use std::time::{Duration, Instant};

use serde_json::Value;

use super::library::Project;
use crate::tests::{files, procs};

/// The TUI: it records its harp and directory, says it started, and runs
/// until `release` is beside it, then exits with the code `tui-exit` beside
/// it says (none: 0).
const TUI: &str = r#"here="$(dirname "$0")"
echo "${CTXLOOM_SESSION##*/}" > "$here/tui-harp"
pwd -P > "$here/tui-cwd"
: > "$here/tui-started"
i=0
while [ ! -e "$here/release" ] && [ "$i" -lt 1200 ]; do sleep 0.1; i=$((i+1)); done
exit "$(cat "$here/tui-exit" 2>/dev/null || echo 0)"
"#;

/// What holds the daemon's single-instance lock while `unblock` is not
/// beside it, so no daemon can start (the platform's lock).
fn blocks_daemons() -> String {
    procs::holds_lock(20, 1200)
}

/// How long any one thing is waited for.
const WAIT: Duration = Duration::from_secs(60);

struct Rig {
    project: Project,
    bin: PathBuf,
    path: String,
}

/// A project with a `dev` profile, the TUI stub as `claude` leading the
/// run's `PATH`, in a git repository of one commit; none where this host
/// runs no scripts (said).
fn rig() -> Option<Rig> {
    if !files::runs_scripts() {
        return None;
    }
    let project = Project::initialized();
    project.bundle("demo");
    project.profile("dev", "demo");
    let bin = project.home().join("bin");
    files::put_program(bin.join("claude"), TUI);
    let path = match crate::tests::host_var("PATH") {
        Some(host) => format!("{}:{}", bin.display(), host.to_string_lossy()),
        None => bin.display().to_string(),
    };
    let rig = Rig { project, bin, path };
    rig.git(&["init", "-q"]);
    rig.git(&["add", "-A"]);
    rig.git(&["commit", "-q", "-m", "the project"]);
    Some(rig)
}

impl Rig {
    fn env(&self) -> Vec<(&str, &str)> {
        vec![
            ("PATH", self.path.as_str()),
            ("CTXLOOM_TEST_TERMINAL", "1"),
            // A daemon a command starts idles out soon after the test.
            ("CTXLOOM_DAEMON_IDLE_SECONDS", "30"),
        ]
    }

    fn git(&self, args: &[&str]) -> String {
        let output = std::process::Command::new("git")
            .args([
                "-c",
                "user.name=Alice",
                "-c",
                "user.email=alice@example.com",
            ])
            .args([
                "-c",
                "commit.gpgsign=false",
                "-c",
                "init.defaultBranch=main",
            ])
            .args(args)
            .current_dir(self.project.dir())
            .env_clear()
            .env("PATH", crate::tests::host_var("PATH").unwrap_or_default())
            .env("HOME", self.project.home())
            .env("TMPDIR", crate::tests::temp_root())
            .env(crate::tests::LIFELINE, crate::tests::lifeline())
            .env(crate::tests::SEAL_KEY, crate::tests::SEAL_KEY_VALUE)
            .output()
            .expect("git runs");
        assert!(output.status.success(), "git {args:?}: {output:?}");
        String::from_utf8_lossy(&output.stdout).into_owned()
    }

    fn home_file(&self, rel: &str) -> PathBuf {
        self.project.home().join(".ctxloom").join(rel)
    }

    /// `ctxloom run` with `args`, started, the TUI stood in.
    fn start_session(&self, args: &[&str]) -> Child {
        let mut all = vec!["run", "--profile", "dev"];
        all.extend_from_slice(args);
        self.project.start_with(&self.env(), &all)
    }

    fn run(&self, args: &[&str]) -> super::Ran {
        self.project.run_with(&self.env(), args)
    }

    /// The launch the frontend was handed, once it was.
    fn launch(&self) -> Value {
        let path = self.home_file("test-frontend-launch.json");
        wait(&self.log(), "the frontend's launch", || {
            files::exists(&path)
        });
        serde_json::from_str(&files::read(&path).expect("a launch")).expect("json")
    }

    /// The session's harp, as its launch's lock names it: the directory
    /// the lock is inside (`sessions/<harp>/session.lock`).
    fn harp(&self) -> String {
        let launch = self.launch();
        let lock = launch["session_lock"].as_str().expect("its lock");
        Path::new(lock)
            .parent()
            .and_then(Path::file_name)
            .expect("a lock in its session's directory")
            .to_string_lossy()
            .into_owned()
    }

    /// The daemon serving now: its pid.
    fn daemon_pid(&self) -> Option<u64> {
        let status = self.run(&["daemon", "status", "--format", "json"]).json();
        status["daemons"][0]["pid"].as_u64().or_else(|| {
            status["daemons"][0]["pid"]
                .as_str()
                .and_then(|pid| pid.parse().ok())
        })
    }

    /// Kill the serving daemon outright, and wait it gone.
    fn kill_daemon(&self) -> u64 {
        let pid = self.daemon_pid().expect("a daemon serves");
        let killed = std::process::Command::new("kill")
            .args(["-KILL", &pid.to_string()])
            .status()
            .expect("kill runs");
        assert!(killed.success(), "the daemon was killed");
        wait(&self.log(), "the killed daemon's end", || !alive(pid));
        pid
    }

    /// The events of `harp`'s session (or of its first run), each its type
    /// and (for an end) its cause.
    fn events(&self, harp: &str, run: bool) -> Vec<(String, String)> {
        let mut args = vec!["test", "session-events", harp];
        if run {
            args.extend(["--run", "1"]);
        }
        let ran = self.run(&args);
        if ran.code != 0 {
            return Vec::new();
        }
        ran.json()["events"]
            .as_array()
            .cloned()
            .unwrap_or_default()
            .iter()
            .map(|e| {
                (
                    e["type"].as_str().unwrap_or_default().to_string(),
                    e["cause"].as_str().unwrap_or_default().to_string(),
                )
            })
            .collect()
    }

    /// The events of `harp`'s first run, each as `test session-events`
    /// prints it.
    fn run_rows(&self, harp: &str) -> Vec<Value> {
        let ran = self.run(&["test", "session-events", harp, "--run", "1"]);
        assert_eq!(ran.code, 0, "{}", ran.said());
        ran.json()["events"].as_array().cloned().unwrap_or_default()
    }

    fn types(&self, harp: &str, run: bool) -> Vec<String> {
        self.events(harp, run).into_iter().map(|(t, _)| t).collect()
    }

    /// The incarnation ends of `harp`'s session, each its cause.
    fn incarnations_ended(&self, harp: &str) -> Vec<String> {
        self.events(harp, false)
            .into_iter()
            .filter(|(t, _)| t == "IncarnationEnded")
            .map(|(_, cause)| cause)
            .collect()
    }

    /// Wait for `harp`'s incarnation to end, its cause.
    fn ended(&self, harp: &str) -> String {
        let mut cause = Vec::new();
        wait(&self.log(), "the incarnation's end", || {
            cause = self.incarnations_ended(harp);
            !cause.is_empty()
        });
        assert_eq!(cause.len(), 1, "{cause:?}");
        cause.remove(0)
    }

    /// The blocker: what holds the daemon's single-instance lock until
    /// `unblock` is beside it, so no daemon can start, started now.
    fn block_daemons(&self) -> Child {
        let blocker = self.bin.join("blocks-daemons");
        files::put_program(&blocker, &blocks_daemons());
        std::process::Command::new(&blocker)
            .args([
                self.home_file("daemon/daemon.lock").as_os_str(),
                self.bin.join("unblock").as_os_str(),
            ])
            .spawn()
            .expect("the blocker starts")
    }

    /// Wait for the single-instance lock to be held: by the blocker, as
    /// nothing else can have started since the daemon died (the caller
    /// holds back what could).
    fn daemons_blocked(&self) {
        let lock = self.home_file("daemon/daemon.lock");
        wait(&self.log(), "the daemon's lock held", || {
            procs::lock_held(&lock)
        });
    }

    fn release_tui(&self) {
        files::put(self.bin.join("release"), "go");
    }

    fn log(&self) -> String {
        files::read(self.home_file("daemon/daemon.log")).unwrap_or_default()
    }

    fn stop(&self) {
        let _ = self.run(&["daemon", "stop", "--even-if-live"]);
    }
}

impl Drop for Rig {
    /// A failing test lets its TUI and its blocker end (each runs until a
    /// file beside it says so); its project then ends what it started that
    /// still runs (`Project`'s drop) and removes the home, on which any
    /// daemon left exits.
    fn drop(&mut self) {
        files::put(self.bin.join("release"), "go");
        files::put(self.bin.join("unblock"), "go");
    }
}

/// Wait, bounded, for `ready`; `what` names it when it never comes.
fn wait(log: &str, what: &str, mut ready: impl FnMut() -> bool) {
    let deadline = Instant::now() + WAIT;
    while !ready() {
        assert!(
            Instant::now() < deadline,
            "{what} never came; the daemon's log:\n{log}"
        );
        std::thread::sleep(Duration::from_millis(100));
    }
}

/// A process stopped (SIGSTOP) until this is dropped (SIGCONT), so it
/// does nothing meanwhile: a test's failing assertion lets it go on too.
struct Frozen(u64);

impl Frozen {
    fn stop(pid: u64) -> Frozen {
        send(pid, "-STOP");
        Frozen(pid)
    }
}

impl Drop for Frozen {
    fn drop(&mut self) {
        send(self.0, "-CONT");
    }
}

/// Whether `pid` is there and not a zombie.
fn alive(pid: u64) -> bool {
    procs::alive(pid)
}

fn finished(child: Child) -> super::Ran {
    super::Ran::of(child.wait_with_output().expect("ctxloom finishes"))
}

// THE FORCING TEST of U5's start-report race (owner 2026-11-14): the daemon
// is killed between the frontend's plan request and its start report; the
// next daemon reads the run lock the frontend took before it asked as its
// owner alive, so nothing is orphaned, and the TUI's exit ends its
// incarnation `runner-exit` through the new daemon's adoption.
#[test]
fn test_a_daemon_killed_between_the_plan_and_the_start_report_orphans_nothing() {
    let Some(rig) = rig() else {
        return;
    };
    let gate = rig.home_file("test-frontend-gate");
    files::put(&gate, "wait");
    let session = rig.start_session(&[]);
    let harp = rig.harp();
    // The daemon answered the plan; the frontend waits at the gate.
    let killed = rig.kill_daemon();
    // The next command starts a new daemon, whose start sweeps.
    let listed = rig.run(&["session", "list"]);
    assert_eq!(listed.code, 0, "{}", listed.said());
    let now = rig.daemon_pid().expect("a new daemon serves");
    assert_ne!(now, killed, "another daemon");
    assert!(
        !rig.types(&harp, true).iter().any(|t| t == "RunEnded"),
        "the new daemon's start orphaned the run: {:?}\n{}",
        rig.events(&harp, true),
        rig.log()
    );
    files::remove(&gate);
    wait(&rig.log(), "the TUI's start", || {
        files::exists(rig.bin.join("tui-started"))
    });
    rig.release_tui();
    let ran = finished(session);
    assert_eq!(ran.code, 0, "{}", ran.said());
    assert_eq!(rig.ended(&harp), "runner-exit", "{}", rig.log());
    let run = rig.events(&harp, true);
    let ends: Vec<&(String, String)> = run.iter().filter(|(t, _)| t == "RunEnded").collect();
    assert_eq!(ends.len(), 1, "{run:?}");
    assert_eq!(ends[0].1, "RunnerExit", "{run:?}");
    assert_eq!(
        run.iter()
            .filter(|(t, _)| t == "RunnerStartedRecorded")
            .count(),
        1,
        "{run:?}"
    );
    rig.stop();
}

// A daemon killed under a live TUI (owner 2026-11-14, (1)): the next
// daemon adopts the session at its start, and the TUI's exit ends its
// incarnation and lets its checkout go, as the daemon that prepared it
// would have.
#[test]
fn test_a_daemon_killed_under_a_live_tui_is_adopted_and_the_exit_releases_the_checkout() {
    let Some(rig) = rig() else {
        return;
    };
    let session = rig.start_session(&["--workspace", "worktree"]);
    let harp = rig.harp();
    wait(&rig.log(), "the TUI's start", || {
        files::exists(rig.bin.join("tui-started"))
    });
    wait(&rig.log(), "the start recorded", || {
        rig.types(&harp, true)
            .iter()
            .any(|t| t == "RunnerStartedRecorded")
    });
    let checkout = files::read(rig.bin.join("tui-cwd"))
        .expect("its directory")
        .trim()
        .to_string();
    assert!(
        checkout.contains("/.ctxloom/worktrees/"),
        "the TUI works in its checkout: {checkout}"
    );
    let killed = rig.kill_daemon();
    let listed = rig.run(&["session", "list", "--format", "json"]);
    assert_eq!(listed.code, 0, "{}", listed.said());
    assert_ne!(rig.daemon_pid(), Some(killed), "another daemon");
    assert!(rig.incarnations_ended(&harp).is_empty(), "still live");
    assert!(
        rig.log().contains("adopted"),
        "the new daemon adopted it: {}",
        rig.log()
    );
    rig.release_tui();
    let ran = finished(session);
    assert_eq!(ran.code, 0, "{}", ran.said());
    assert_eq!(rig.ended(&harp), "runner-exit", "{}", rig.log());
    wait(&rig.log(), "the checkout let go", || {
        !files::exists(&checkout)
    });
    assert!(
        !rig.git(&["worktree", "list", "--porcelain"])
            .contains(&checkout),
        "its worktree record is gone"
    );
    let run = rig.types(&harp, true);
    assert_eq!(
        run.iter().filter(|t| *t == "RunEnded").count(),
        1,
        "{run:?}"
    );
    assert!(!run.iter().any(|t| t == "RunnerLostRecorded"), "{run:?}");
    rig.stop();
}

// The frontend's reports held (owner 2026-11-14, (2)): with the daemon
// killed in the gap and no daemon able to start, the start report is HELD
// in the session's hold (no 60 s retry), the run's own lock kept; once a
// daemon can start, the exit's attempt reaches it, which adopts the run (its
// lock held), and delivers the held start, then the exit, in order.
#[test]
fn test_the_frontends_reports_are_held_and_delivered_in_order() {
    let Some(rig) = rig() else {
        return;
    };
    let gate = rig.home_file("test-frontend-gate");
    files::put(&gate, "wait");
    let session = rig.start_session(&[]);
    let harp = rig.harp();
    // No daemon can start: its single-instance lock is held by a blocker,
    // and the frontend, held at its gate, asks for none until it is. The
    // blocker comes as late as it can, once the daemon is dead (FORCED: a
    // blocker taking the lock in a race with the daemon the frontend's
    // start report starts lost it under load, 2026-11-17), so the gate,
    // never the blocker's speed, is what orders it.
    rig.kill_daemon();
    let mut blocking = rig.block_daemons();
    let unblock = rig.bin.join("unblock");
    rig.daemons_blocked();
    files::remove(&gate);
    wait(&rig.log(), "the TUI's start", || {
        files::exists(rig.bin.join("tui-started"))
    });
    let held = rig.home_file("held");
    // Its hold, not the hold's lock (taken before the hold is written).
    wait(&rig.log(), "the start held", || {
        files::children(&held)
            .iter()
            .any(|path| path.extension().is_some_and(|e| e == "held"))
    });
    files::put(&unblock, "go");
    let _ = blocking.wait();
    rig.release_tui();
    let ran = finished(session);
    assert_eq!(ran.code, 0, "{}", ran.said());
    assert!(
        ran.stderr.contains("held the session's start"),
        "{}",
        ran.said()
    );
    assert_eq!(rig.ended(&harp), "runner-exit", "{}", rig.log());
    let run = rig.events(&harp, true);
    let order: Vec<&str> = run
        .iter()
        .map(|(t, _)| t.as_str())
        .filter(|t| matches!(*t, "RunnerStartedRecorded" | "RunEnded"))
        .collect();
    assert_eq!(order, ["RunnerStartedRecorded", "RunEnded"], "{run:?}");
    assert!(
        run.iter()
            .any(|(t, cause)| t == "RunEnded" && cause == "RunnerExit"),
        "{run:?}"
    );
    rig.stop();
}

// OD-59 (2), owner 2026-11-16 ("ALLOW the check"), FORCED: the daemon is
// killed under a live TUI and no daemon can start (a blocker holds its
// single-instance lock, waiting on it BEFORE the kill), so the TUI's exit
// report is HELD in the session's hold and the frontend exits, its locks let
// go. A daemon then started (by the next command) finds the run's owner gone
// at its start: its sweep delivers the session's hold first, so the run ends
// as the report says, with the TUI's own exit code, `runner-exit`, and never
// `runner-lost`.
#[test]
fn test_a_tui_exit_held_while_no_daemon_serves_ends_its_run_with_its_exit_code() {
    let Some(rig) = rig() else {
        return;
    };
    let session = rig.start_session(&[]);
    let harp = rig.harp();
    wait(&rig.log(), "the TUI's start", || {
        files::exists(rig.bin.join("tui-started"))
    });
    wait(&rig.log(), "the start recorded", || {
        rig.types(&harp, true)
            .iter()
            .any(|t| t == "RunnerStartedRecorded")
    });
    // The frontend is FROZEN while its daemon is killed and the blocker
    // takes the single-instance lock: its broken watch, opened again on the
    // next daemon, would otherwise start one in a race with the blocker
    // (lost under load, 2026-11-17). The blocker comes as late as it can,
    // once the daemon is dead, so the freeze, never its speed, orders it.
    let frozen = Frozen::stop(u64::from(session.id()));
    let killed = rig.kill_daemon();
    let mut blocking = rig.block_daemons();
    let unblock = rig.bin.join("unblock");
    rig.daemons_blocked();
    drop(frozen);
    // The TUI exits 3 while no daemon can serve: its exit report is held.
    files::put(rig.bin.join("tui-exit"), "3");
    rig.release_tui();
    let ran = finished(session);
    let held = rig.home_file("held");
    assert!(
        files::children(&held)
            .iter()
            .any(|path| path.extension().is_some_and(|e| e == "held")),
        "the exit report is held: {}",
        ran.said()
    );
    assert!(
        rig.events(&harp, true).iter().all(|(t, _)| t != "RunEnded"),
        "nothing ended the run yet: {:?}",
        rig.events(&harp, true)
    );
    files::put(&unblock, "go");
    let _ = blocking.wait();
    // The next command starts a new daemon, whose start judges the run.
    let listed = rig.run(&["session", "list"]);
    assert_eq!(listed.code, 0, "{}", listed.said());
    assert_ne!(rig.daemon_pid(), Some(killed), "another daemon");
    assert_eq!(rig.ended(&harp), "runner-exit", "{}", rig.log());
    let run = rig.run_rows(&harp);
    let ends: Vec<&Value> = run.iter().filter(|e| e["type"] == "RunEnded").collect();
    assert_eq!(ends.len(), 1, "{run:?}");
    assert_eq!(ends[0]["cause"], "RunnerExit", "{run:?}\n{}", rig.log());
    assert_eq!(ends[0]["exit_code"], 3, "the TUI's own exit code: {run:?}");
    assert!(
        !run.iter().any(|e| e["type"] == "RunnerLostRecorded"),
        "{run:?}"
    );
    rig.stop();
}

// Owner, 2026-11-15 (OD-62): `ctxloom shutdown now` asks a live TUI's
// frontend to stop (a frontend outlives its daemon, so the daemon alone is
// not enough), then the daemon the plain stop: the TUI is stopped as a
// SIGTERM to its frontend would stop it (ruling 114), its end is recorded,
// and nothing of ctxloom runs after.
#[test]
fn test_a_shutdown_now_stops_a_live_tuis_frontend_and_the_daemon() {
    let Some(rig) = rig() else {
        return;
    };
    let session = rig.start_session(&[]);
    let harp = rig.harp();
    wait(&rig.log(), "the TUI's start", || {
        files::exists(rig.bin.join("tui-started"))
    });
    wait(&rig.log(), "the start recorded", || {
        rig.types(&harp, true)
            .iter()
            .any(|t| t == "RunnerStartedRecorded")
    });
    let daemon = rig.daemon_pid().expect("a daemon serves");
    let shut = rig.run(&["shutdown", "now"]);
    assert_eq!(shut.code, 0, "{}\n{}", shut.said(), rig.log());
    assert!(
        shut.stdout.contains("asked the frontend pid"),
        "{}",
        shut.said()
    );
    assert!(
        shut.stdout
            .contains("stopped: nothing of ctxloom is running"),
        "{}",
        shut.said()
    );
    let _ = finished(session);
    wait(&rig.log(), "the stopped daemon's exit", || !alive(daemon));
    assert_eq!(rig.ended(&harp), "runner-exit", "{}", rig.log());
    rig.stop();
}

// A drain leaves a TUI to its human for the drain's whole timeout (no TUI
// is handed over): `ctxloom shutdown 2s` waits the two seconds, says the
// frontend still runs, then asks it to stop.
#[test]
fn test_a_shutdown_with_a_span_waits_it_out_for_a_live_tui_then_stops_it() {
    let Some(rig) = rig() else {
        return;
    };
    let session = rig.start_session(&[]);
    wait(&rig.log(), "the TUI's start", || {
        files::exists(rig.bin.join("tui-started"))
    });
    let shut = rig.run(&["shutdown", "2s"]);
    assert_eq!(shut.code, 0, "{}\n{}", shut.said(), rig.log());
    assert!(
        shut.stdout.contains("after the drain's 2s, still running")
            && shut.stdout.contains("asked the frontend pid"),
        "{}",
        shut.said()
    );
    let told = shut
        .stdout
        .find("told the frontend pid")
        .unwrap_or_else(|| panic!("the frontend was told at once: {}", shut.said()));
    let after = shut
        .stdout
        .find("after the drain's 2s")
        .expect("said above");
    assert!(told < after, "told at the drain's start: {}", shut.said());
    let ran = finished(session);
    assert!(
        ran.stderr.contains(
            "ctxloom: ctxloom is shutting down: this session will be stopped in 2s; finish and exit"
        ),
        "its terminal showed the notice: {}",
        ran.said()
    );
    rig.stop();
}

/// What the daemon's drain says in its log when it hosts nothing and stays
/// for a live frontend it told (owner, 2026-11-16, PREVENT the early exit).
const STAYS_FOR_FRONTENDS: &str = "draining: hosting nothing, it stays for the live frontends";

impl Rig {
    /// The home config with `daemon.drain_timeout: <timeout>`.
    fn drain_timeout(&self, timeout: &str) {
        self.project.write_home(
            ".ctxloom/config.yaml",
            &format!(
                "schema_version: 9\neditor:\n  command: \"true\"\ndaemon:\n  drain_timeout: {timeout}\n"
            ),
        );
    }

    /// A live TUI's session, started and its start recorded: the command,
    /// its harp and the daemon serving it.
    fn live_tui(&self) -> (Child, String, u64) {
        let session = self.start_session(&[]);
        let harp = self.harp();
        wait(&self.log(), "the TUI's start", || {
            files::exists(self.bin.join("tui-started"))
        });
        wait(&self.log(), "the start recorded", || {
            self.types(&harp, true)
                .iter()
                .any(|t| t == "RunnerStartedRecorded")
        });
        let daemon = self.daemon_pid().expect("a daemon serves");
        (session, harp, daemon)
    }

    /// The daemon `daemon`, sent SIGTERM (a drain), seen at the round that
    /// would have exited it: hosting nothing, it says it stays for the
    /// live frontends, or it has exited (the early exit, which fails).
    fn drained_staying(&self, daemon: u64) {
        send(daemon, "-TERM");
        wait(&self.log(), "the drain's round with nothing hosted", || {
            self.log().contains(STAYS_FOR_FRONTENDS) || !alive(daemon)
        });
        assert!(
            alive(daemon) && self.log().contains(STAYS_FOR_FRONTENDS),
            "the draining daemon exited before its live frontend ended: {}",
            self.log()
        );
    }

    /// The holds of the home (their `.held` files).
    fn holds(&self) -> Vec<PathBuf> {
        files::children(self.home_file("held"))
            .into_iter()
            .filter(|path| path.extension().is_some_and(|e| e == "held"))
            .collect()
    }
}

// The owner's ruling of 2026-11-16, PREVENT the early drain exit, FORCED:
// a draining daemon that hosts nothing does not exit while a live frontend
// it told of remains (the round that would have exited it says so), so the
// frontend's last report, its TUI's exit, reaches THIS daemon: nothing is
// held, no second daemon starts, the run ends `runner-exit` with the TUI's
// own code, and the daemon exits once the frontend has ended.
#[test]
fn test_a_draining_daemon_stays_for_a_live_frontend_until_its_last_report() {
    let Some(rig) = rig() else {
        return;
    };
    // The drain's time outlasts the test: only the frontend's end lets the
    // daemon go.
    rig.drain_timeout("1h");
    let (session, harp, daemon) = rig.live_tui();
    rig.drained_staying(daemon);
    // Its human exits the TUI, code 3: the frontend reports, and ends.
    files::put(rig.bin.join("tui-exit"), "3");
    rig.release_tui();
    let ran = finished(session);
    assert!(
        !ran.stderr.contains("for the next daemon"),
        "the exit report reached the daemon: {}",
        ran.said()
    );
    wait(&rig.log(), "the drained daemon's exit", || !alive(daemon));
    assert_eq!(rig.holds(), Vec::<PathBuf>::new(), "nothing is held");
    assert_eq!(
        rig.log().matches(" serving at ").count(),
        1,
        "no second daemon: {}",
        rig.log()
    );
    let run = rig.run_rows(&harp);
    let ends: Vec<&Value> = run.iter().filter(|e| e["type"] == "RunEnded").collect();
    assert_eq!(ends.len(), 1, "{run:?}");
    assert_eq!(ends[0]["cause"], "RunnerExit", "{run:?}\n{}", rig.log());
    assert_eq!(ends[0]["exit_code"], 3, "the TUI's own exit code: {run:?}");
    rig.stop();
}

// A frontend that dies without reporting does not hold a draining daemon
// for the drain's whole time: its lock reads free (the kernel let it go
// with the process), so the daemon, hosting nothing, exits.
#[test]
fn test_a_draining_daemon_does_not_stay_for_a_frontend_that_died() {
    let Some(rig) = rig() else {
        return;
    };
    rig.drain_timeout("1h");
    let (mut session, _harp, daemon) = rig.live_tui();
    rig.drained_staying(daemon);
    // The frontend killed outright: it reports nothing.
    let _ = session.kill();
    let _ = session.wait();
    wait(&rig.log(), "the drained daemon's exit", || !alive(daemon));
    assert!(
        rig.log().contains("exited"),
        "it exited as a daemon exits: {}",
        rig.log()
    );
    rig.stop();
}

// A failing test leaves nothing of ctxloom running (2026-11-15, U8r: two
// test daemons a panicked TUI test left blocked the next commit's gate for
// half an hour). A rig dropped under a live TUI, as a panic drops it, ends
// its frontend (which would otherwise keep, or start again, a daemon) and so
// the daemon too, before and once its home is removed.
#[test]
fn test_a_rig_dropped_under_a_live_tui_leaves_no_frontend_or_daemon() {
    let Some(rig) = rig() else {
        return;
    };
    let session = rig.start_session(&[]);
    let harp = rig.harp();
    wait(&rig.log(), "the TUI's start", || {
        files::exists(rig.bin.join("tui-started"))
    });
    wait(&rig.log(), "the start recorded", || {
        rig.types(&harp, true)
            .iter()
            .any(|t| t == "RunnerStartedRecorded")
    });
    let frontend = u64::from(session.id());
    let daemon = rig.daemon_pid().expect("a daemon serves");
    drop(rig);
    let deadline = Instant::now() + Duration::from_secs(30);
    while (alive(frontend) || alive(daemon)) && Instant::now() < deadline {
        std::thread::sleep(Duration::from_millis(100));
    }
    assert!(!alive(frontend), "the frontend outlived its test");
    assert!(!alive(daemon), "the daemon outlived its test");
    drop(session);
}

/// Send `signal` to `pid`, whatever comes of it (it may have exited).
fn send(pid: u64, signal: &str) {
    let _ = std::process::Command::new("kill")
        .args([signal, &pid.to_string()])
        .status();
}

/// A live TUI's daemon sent SIGTERM twice, its ctxloom home removed first
/// when `home_removed`: the daemon exits within a bound (ruling 114).
fn two_sigterms_under_a_live_tui(home_removed: bool) {
    let Some(rig) = rig() else {
        return;
    };
    let session = rig.start_session(&[]);
    let harp = rig.harp();
    wait(&rig.log(), "the TUI's start", || {
        files::exists(rig.bin.join("tui-started"))
    });
    wait(&rig.log(), "the start recorded", || {
        rig.types(&harp, true)
            .iter()
            .any(|t| t == "RunnerStartedRecorded")
    });
    let daemon = rig.daemon_pid().expect("a daemon serves");
    if home_removed {
        files::remove_tree_settling(rig.project.home().join(".ctxloom"));
    }
    send(daemon, "-TERM");
    // The second once the first was heard, where its log can say so: two
    // sent at once may be one (a signal pending is not queued again). A
    // daemon whose home is gone keeps no log, and exits for its home.
    if !home_removed {
        wait(&rig.log(), "the first SIGTERM heard", || {
            rig.log().contains("SIGTERM: asked to drain")
        });
    }
    send(daemon, "-TERM");
    let deadline = Instant::now() + Duration::from_secs(30);
    while alive(daemon) && Instant::now() < deadline {
        std::thread::sleep(Duration::from_millis(100));
    }
    let outlived = alive(daemon);
    if outlived {
        send(daemon, "-KILL");
    }
    rig.release_tui();
    let _ = finished(session);
    assert!(!outlived, "the daemon outlived two SIGTERMs by 30s");
}

// Ruling 114, FORCED (2026-11-15: a test's daemon hosting a leaked
// frontend's session ignored two SIGTERMs): the daemon of a live TUI
// exits on two SIGTERMs.
#[test]
fn test_two_sigterms_stop_the_daemon_of_a_live_tui() {
    two_sigterms_under_a_live_tui(false);
}

// The same with the ctxloom home removed first, as a test's temporary
// home is.
#[test]
fn test_two_sigterms_stop_the_daemon_of_a_live_tui_whose_home_was_removed() {
    two_sigterms_under_a_live_tui(true);
}
