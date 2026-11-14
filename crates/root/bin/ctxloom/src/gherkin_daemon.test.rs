//! The steps of the user daemon's spec (ruling 132, the target T1-T12):
//! `journeys/j002700_user_daemon.feature`, `cli/daemon.feature`,
//! `cli/session_children.feature` and the daemon Rule of `cli/run.feature`,
//! as the owner's rulings on the spec calls (S1-S20, 2026-11-11) shaped them;
//! and trust and approvals (ruling 142): `journeys/j002800_trusting_a_project.feature`,
//! `cli/trust.feature` and `cli/session_approvals.feature`; and the
//! architecture rulings T13-T30: `cli/mcp_serve.feature` and
//! `cli/atomic_writes.feature`.
//!
//! RED BY DESIGN. The spec is committed before the daemon is reworked to
//! it, so every step here that the existing harness cannot already take is a
//! stub that FAILS, naming itself: nothing is implemented. Each stub becomes
//! a real step with the milestone that builds what it asserts.

use std::path::PathBuf;
use std::process::Child;
use std::time::{Duration, Instant};

use cucumber::{given, then, when};
use serde_json::{json, Value};

use super::World;
use crate::tests::files;

/// The variable a client gives the daemon it spawns as its idle time.
const IDLE_VARIABLE: &str = "CTXLOOM_DAEMON_IDLE_SECONDS";

/// The idle time of a daemon a scenario starts, in seconds: short, so a
/// scenario's wait for it is.
const IDLE_SECONDS: u64 = 2;

/// The idle time of a daemon a scenario's command starts: long enough to
/// outlive the scenario's steps, short enough to leave nothing for long.
const SPAWNED_IDLE_SECONDS: u64 = 15;

/// What a scenario started: the daemon, and the run that keeps it busy. A
/// World drops this FIRST (its first field), before its home is removed:
/// the run is released and waited for, the daemon killed outright (T3: it
/// is kill-tolerant) and reaped.
#[derive(Default)]
pub(super) struct State {
    pub(super) daemon: Option<Child>,
    run: Option<Child>,
    /// The file whose presence lets the stub engine end.
    release: Option<PathBuf>,
    /// What a client's call to the daemon came to.
    called: Option<Result<(), daemon_ports::Called>>,
    /// The discovery of a daemon since stopped.
    old: Option<run_ports::Discovery>,
    /// The daemon a command was seen to start.
    first: Option<run_ports::Discovery>,
    /// What two commands run at once came to.
    both: Vec<crate::tests::Ran>,
    /// The engine a registration scenario's session ran on, and where its
    /// stub recorded what it was handed.
    registered: Option<(String, PathBuf)>,
    /// What an engine's call of a ctxloom tool was answered.
    tool: Option<Value>,
    /// The daemon a tool's call found before it was killed.
    before_kill: Option<run_ports::Discovery>,
}

impl Drop for State {
    fn drop(&mut self) {
        if let Some(release) = &self.release {
            files::put(release, "go");
        }
        if let Some(mut run) = self.run.take() {
            let deadline = Instant::now() + Duration::from_secs(10);
            while Instant::now() < deadline && matches!(run.try_wait(), Ok(None)) {
                std::thread::sleep(Duration::from_millis(50));
            }
            let _ = run.kill();
            let _ = run.wait();
        }
        if let Some(mut daemon) = self.daemon.take() {
            let _ = daemon.kill();
            let _ = daemon.wait();
        }
    }
}

/// The discovery file of the scenario's home.
fn discovery_path(w: &World) -> PathBuf {
    w.common
        .project
        .home()
        .join(".ctxloom/daemon/daemon.discovery")
}

fn daemon_log_text(w: &World) -> String {
    files::read(w.common.project.home().join(".ctxloom/daemon/daemon.log")).unwrap_or_default()
}

/// The scenario's environment, as `(name, value)` pairs.
fn env_of(w: &World) -> Vec<(String, String)> {
    w.common.env.clone()
}

/// `ctxloom args` in the project, now, with the scenario's environment.
fn run_now(w: &World, args: &[&str]) -> crate::tests::Ran {
    let env = env_of(w);
    let env: Vec<(&str, &str)> = env.iter().map(|(k, v)| (k.as_str(), v.as_str())).collect();
    w.common.project.run_with(&env, args)
}

/// Whether `daemon status` says a daemon serves (its health check): its
/// answer is the daemon, which has a pid; none serving, the empty message.
fn serving_now(w: &World) -> bool {
    let status = run_now(w, &["daemon", "status", "--format", "json"]);
    assert_eq!(status.code, 0, "{}", status.said());
    status.json().get("pid").is_some()
}

/// Start `ctxloom daemon serve` with the scenario's short idle time, and
/// wait for it to answer.
fn start_daemon(w: &mut World) {
    start_daemon_idling(w, IDLE_SECONDS);
}

/// Start `ctxloom daemon serve` with the idle time `idle` (seconds), and
/// wait for it to answer.
fn start_daemon_idling(w: &mut World, idle: u64) {
    let env = env_of(w);
    let env: Vec<(&str, &str)> = env.iter().map(|(k, v)| (k.as_str(), v.as_str())).collect();
    let idle = idle.to_string();
    let daemon = w
        .common
        .project
        .start_with(&env, &["daemon", "serve", "--idle-seconds", &idle]);
    w.daemon_spec.daemon = Some(daemon);
    let deadline = Instant::now() + Duration::from_secs(30);
    while !serving_now(w) {
        assert!(
            Instant::now() < deadline,
            "the daemon never served; its log:\n{}",
            daemon_log_text(w)
        );
        std::thread::sleep(Duration::from_millis(50));
    }
}

/// The project's config: one agent on claude-code, the default.
const PROJECT_CONFIG: &str =
    "schema_version: 9\nagents:\n  dev:\n    engine: claude-code\ndefault_agent: dev\n";

/// Point claude-code at the stub `body` in the scenario's home (it runs
/// with `$here` its own directory); the stub's directory.
fn stub_claude(w: &mut World, body: &str) -> PathBuf {
    let dir = w.common.project.home().join("fake-claude");
    files::put_program(dir.join("claude"), body);
    w.common
        .project
        .write(".ctxloom/config.yaml", PROJECT_CONFIG);
    w.common.project.write_home(
        ".ctxloom/config.yaml",
        &format!(
            "schema_version: 9\neditor:\n  command: \"true\"\nengines:\n  claude-code:\n    command: {}\n",
            dir.join("claude").display()
        ),
    );
    dir
}

/// The interactive session's stub: it says it started, then runs until
/// released (bounded, so a lost scenario ends anyway).
const INTERACTIVE_STUB: &str = r#"here=$(dirname "$0")
: > "$here/started"
i=0
while [ ! -e "$here/release" ] && [ "$i" -lt 600 ]; do sleep 0.1; i=$((i+1)); done
"#;

/// An interactive session the CLI runs (`ctxloom run` at a terminal, the
/// test's stand-in for one), started and not ended: its run is in the one
/// store, and its locks are held by the command (M9).
fn interactive_session(w: &mut World) {
    let dir = stub_claude(w, INTERACTIVE_STUB);
    w.daemon_spec.release = Some(dir.join("release"));
    let mut env = env_of(w);
    env.push(("CTXLOOM_TEST_TERMINAL".to_string(), "1".to_string()));
    let env: Vec<(&str, &str)> = env.iter().map(|(k, v)| (k.as_str(), v.as_str())).collect();
    w.daemon_spec.run = Some(w.common.project.start_with(&env, &["run"]));
    let deadline = Instant::now() + Duration::from_secs(30);
    while !files::exists(dir.join("started")) {
        assert!(
            Instant::now() < deadline,
            "the session's engine never started"
        );
        std::thread::sleep(Duration::from_millis(50));
    }
}

/// A hosted child's stub (engine-claude's streaming child): an init, a
/// result for each message, until its input ends.
const CHILD_STUB: &str = r#"here=$(dirname "$0")
printf '{"type":"system","subtype":"init","cwd":"/work","session_id":"S1","tools":[]}\n'
while IFS= read -r line; do
  : > "$here/child-turn"
  printf '{"type":"result","subtype":"success","is_error":false,"result":"ok","session_id":"S1"}\n'
done
: > "$here/child-exited"
"#;

/// A child the daemon hosts: launched by the user for a seeded owner, and
/// seen to take its first turn.
fn hosted_child(w: &mut World) {
    let dir = stub_claude(w, CHILD_STUB);
    let seeded = run_now(w, &["test", "seed-session", "amber-quiet-heron"]);
    assert_eq!(seeded.code, 0, "{}", seeded.said());
    let mut env = env_of(w);
    env.push(("ANTHROPIC_API_KEY".to_string(), "a-key".to_string())); // gitleaks:allow
    let env: Vec<(&str, &str)> = env.iter().map(|(k, v)| (k.as_str(), v.as_str())).collect();
    let launched = w.common.project.run_with(
        &env,
        &[
            "daemon",
            "launch",
            "--owner",
            "amber-quiet-heron",
            "--name",
            "brisk-copper-moth",
            "--prompt",
            "a task",
        ],
    );
    assert_eq!(launched.code, 0, "{}", launched.said());
    let deadline = Instant::now() + Duration::from_secs(30);
    while !files::exists(dir.join("child-turn")) {
        assert!(
            Instant::now() < deadline,
            "the child never took its turn; the daemon's log:\n{}",
            daemon_log_text(w)
        );
        std::thread::sleep(Duration::from_millis(50));
    }
}

/// A step of the daemon's spec that nothing implements yet.
fn red(step: &str) -> ! {
    panic!("RED (ruling 132, the daemon spec): not built yet: {step}")
}

/// The `Alice ...:` steps of the spec that need a harness not built yet
/// (the generic `Alice ...:` step is the World's `alice_runs`, which hands
/// it here): whether it is one. The ones the existing harness can run (her
/// own commands, typed) are left to it.
pub(super) fn claims(w: &mut World, step: &cucumber::gherkin::Step) -> bool {
    let _ = w;
    let text = step.value.as_str();
    let stubbed = text.contains("presses Ctrl-C while it works")
        || text.contains("in both projects at once")
        || text.contains("which her daemon runs")
        || text.contains("and ctxloom is sent a ")
        || text.contains("through her daemon")
        || text.contains("its daemon is killed outright while it works")
        || text.contains("at her terminal, and answers yes when asked")
        || text.contains("says yes at her terminal");
    if stubbed {
        red(text);
    }
    false
}

// ------------------------------------------------------------------ given

#[given("no ctxloom daemon is serving for Alice")]
fn none_serving_for_alice(w: &mut World) {
    none_serving(w);
}

#[given(regex = r#"^Alice has a second project, "([^"]+)", set up with ctxloom$"#)]
fn second_project(_: &mut World, _name: String) {
    red("Alice has a second project, set up with ctxloom")
}

#[given(regex = r#"^Alice's session "([^"]+)" is running$"#)]
fn alices_session_running(_: &mut World, _harp: String) {
    red("Alice's session is running")
}

#[given(regex = r#"^Alice's session "([^"]+)" is running, with a child "([^"]+)" working for it$"#)]
fn alices_session_with_child(_: &mut World, _owner: String, _child: String) {
    red("Alice's session is running, with a child working for it")
}

#[given(
    "Alice's daemon has nothing to do: no session runs, no child is hosted and no command is open"
)]
fn alices_daemon_idle(w: &mut World) {
    start_daemon(w);
    w.daemon_spec.old = Some(discovery_now(w));
    // The next command's daemon idles out at once too.
    w.common
        .set_env(IDLE_VARIABLE, SPAWNED_IDLE_SECONDS.to_string());
}

#[given("no ctxloom daemon is serving for this home")]
fn none_serving(w: &mut World) {
    // A fresh home: no daemon has advertised itself there, and one a
    // command starts idles out at once.
    assert!(!files::exists(discovery_path(w)), "a fresh home");
    w.common
        .set_env(IDLE_VARIABLE, SPAWNED_IDLE_SECONDS.to_string());
}

#[given("the discovery file names a daemon that no longer runs")]
fn stale_discovery(w: &mut World) {
    start_daemon(w);
    let old = discovery_now(w);
    // Killed outright (T3): its discovery file is left as it was.
    let mut daemon = w.daemon_spec.daemon.take().expect("the daemon started");
    let _ = daemon.kill();
    let _ = daemon.wait();
    assert_eq!(
        discovery_now(w),
        old,
        "the file names the daemon that is gone"
    );
    w.daemon_spec.old = Some(old);
    w.common
        .set_env(IDLE_VARIABLE, SPAWNED_IDLE_SECONDS.to_string());
}

/// The daemon directory of the scenario's home.
fn daemon_dir(w: &World) -> PathBuf {
    w.common.project.home().join(".ctxloom/daemon")
}

#[given("no ctxloom daemon can be started for this home")]
fn unstartable(w: &mut World) {
    // A file where the daemon's directory goes: no daemon can take its
    // single-instance lock there, advertise itself or log.
    assert!(!files::exists(daemon_dir(w)), "a fresh home");
    files::put(daemon_dir(w), "not a directory");
}

#[given("a ctxloom daemon is serving for this home")]
fn serving(w: &mut World) {
    start_daemon(w);
}

#[given(expr = "a ctxloom daemon is serving for this home, hosting a child {string}")]
fn serving_hosting(w: &mut World, child: String) {
    assert_eq!(child, "brisk-copper-moth", "the child the harness hosts");
    // Its default idle time: a daemon that hosts a child never idles out.
    start_daemon_idling(w, DEFAULT_IDLE_SECONDS);
    hosted_child(w);
}

/// The daemon's idle time when nothing says (`daemon serve`'s default).
const DEFAULT_IDLE_SECONDS: u64 = 600;

/// The discovery file the serving daemon wrote.
fn discovery_now(w: &World) -> run_ports::Discovery {
    let bytes = files::read_bytes(discovery_path(w)).expect("a discovery file");
    run_ports::Discovery::decode(&bytes).expect("a discovery file it can read")
}

/// GetDaemon of the daemon `found` names, presenting `found`'s bearer.
fn get_daemon(found: &run_ports::Discovery) -> Result<(), daemon_ports::Called> {
    use daemon_ports::DaemonApi;
    let client = daemon_client::DaemonClient::new(found, Duration::from_secs(5));
    tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()
        .unwrap()
        .block_on(client.get_daemon(daemon_proto::v1::GetDaemonRequest::default()))
        .map(|_| ())
}

#[given("a ctxloom daemon is serving for this home, and has nothing running, hosted or open")]
fn serving_idle(w: &mut World) {
    start_daemon(w);
}

#[given(regex = r"^a ctxloom daemon is serving for this home, with (.+)$")]
fn serving_busy(w: &mut World, busy: String) {
    match busy.as_str() {
        "an interactive session running" => {
            interactive_session(w);
            start_daemon(w);
        }
        "a child it hosts" => {
            start_daemon(w);
            hosted_child(w);
        }
        _ => red("a ctxloom daemon is serving for this home, with something to do"),
    }
}

#[given(regex = r"^the daemon will be killed outright (.+)$")]
fn killed_at(w: &mut World, when: String) {
    if when != "after the write is applied, before it is answered" {
        red("the daemon will be killed outright at a step")
    }
    // The test-support switch (slice 3 step 4): the daemon that finds it
    // takes it, applies the next write it is sent through its coordinator,
    // and is killed before it answers; the daemon started after it finds
    // none.
    w.daemon_spec.before_kill = Some(discovery_now(w));
    files::put(daemon_dir(w).join(DIE_AFTER_APPLY), "once");
}

/// The daemon directory's switch that kills a test-support daemon once a
/// write it was sent is applied, before it is answered.
const DIE_AFTER_APPLY: &str = "test-die-after-apply";

#[given(expr = "a child {string} that is open and idle")]
fn child_idle(_: &mut World, _child: String) {
    red("a child that is open and idle")
}

#[given(expr = "a child {string} that is in the middle of a turn")]
fn child_mid_turn(_: &mut World, _child: String) {
    red("a child that is in the middle of a turn")
}

#[given(expr = "a child {string} that has been closed")]
fn child_closed(_: &mut World, _child: String) {
    red("a child that has been closed")
}

#[given(expr = "a child {string} of {string} that is open and idle")]
fn child_of_idle(w: &mut World, child: String, owner: String) {
    assert_eq!(
        (child.as_str(), owner.as_str()),
        ("brisk-copper-moth", "amber-quiet-heron"),
        "the child and owner the harness hosts"
    );
    // Its default idle time: a daemon that hosts a child never idles out.
    start_daemon_idling(w, DEFAULT_IDLE_SECONDS);
    hosted_child(w);
}

#[given("a hand-off file is left for a child that is no longer open")]
fn stale_hand_off(_: &mut World) {
    red("a hand-off file is left for a child that is no longer open")
}

// ------------------------------------------------------------------- when

#[when(
    "Alice's daemon is killed outright while her assistant works, and her assistant goes on firing its hooks"
)]
fn alices_daemon_killed(_: &mut World) {
    red("Alice's daemon is killed outright while her assistant works")
}

#[when("her daemon's idle time passes")]
fn her_idle_passes(w: &mut World) {
    idle_passes(w);
}

#[when("the daemon's idle time passes")]
fn idle_passes(_: &mut World) {
    // Its idle time, and a margin for its poll and its last round.
    std::thread::sleep(Duration::from_secs(IDLE_SECONDS + 2));
}

#[when(expr = "two commands are run at once, {string} and {string}")]
fn two_at_once(w: &mut World, first: String, second: String) {
    let env = env_of(w);
    let env: Vec<(&str, &str)> = env.iter().map(|(k, v)| (k.as_str(), v.as_str())).collect();
    let started: Vec<Child> = [first, second]
        .iter()
        .map(|line| {
            let args: Vec<&str> = line.split_whitespace().skip(1).collect();
            w.common.project.start_with(&env, &args)
        })
        .collect();
    w.daemon_spec.both = started
        .into_iter()
        .map(|child| crate::tests::Ran::of(child.wait_with_output().expect("it ran")))
        .collect();
}

#[when("a client calls the daemon with a bearer it did not mint")]
fn foreign_bearer(w: &mut World) {
    let found = run_ports::Discovery {
        bearer: run_ports::Token::from_bytes([0x5a; 32]),
        ..discovery_now(w)
    };
    w.daemon_spec.called = Some(get_daemon(&found));
}

#[when("the daemon is stopped and the next command starts another")]
fn restarted(w: &mut World) {
    let old = discovery_now(w);
    let stopped = run_now(w, &["daemon", "stop"]);
    assert_eq!(stopped.code, 0, "{}", stopped.said());
    if let Some(mut daemon) = w.daemon_spec.daemon.take() {
        let _ = daemon.wait();
    }
    // The next command's daemon idles out at once.
    w.common.set_env(IDLE_VARIABLE, IDLE_SECONDS.to_string());
    let next = run_now(w, &["daemon", "resolve"]);
    assert_eq!(next.code, 0, "{}\n{}", next.said(), daemon_log_text(w));
    w.daemon_spec.old = Some(old);
}

#[when("the session's assistant fires its UserPromptSubmit hook")]
fn hook_fires(_: &mut World) {
    red("the session's assistant fires its UserPromptSubmit hook")
}

#[when("Alice asks her assistant one thing, which the killed daemon leaves unfinished")]
fn unfinished(_: &mut World) {
    red("Alice asks her assistant one thing, which the killed daemon leaves unfinished")
}

// ------------------------------------------------------- then: the journey

/// How many daemons logged that they served, in the scenario's home.
fn daemons_logged(w: &World) -> usize {
    daemon_log_text(w).matches(" serving at ").count()
}

#[then("a ctxloom daemon is serving for Alice, started by that command")]
fn started_by_command(w: &mut World) {
    started_detached(w);
    // The one it started, beside the one an earlier step had started.
    let before = usize::from(w.daemon_spec.old.is_some());
    assert_eq!(daemons_logged(w), before + 1, "{}", daemon_log_text(w));
    assert!(serving_now(w), "{}", daemon_log_text(w));
    w.daemon_spec.first = Some(discovery_now(w));
}

#[then("that command used the same daemon, and started none")]
fn same_daemon(w: &mut World) {
    let first = w.daemon_spec.first.clone().expect("a daemon was started");
    assert_eq!(discovery_now(w), first, "the same daemon advertises itself");
    assert_eq!(daemons_logged(w), 1, "{}", daemon_log_text(w));
}

#[then("both sessions were served by the one daemon")]
fn one_daemon(_: &mut World) {
    red("both sessions were served by the one daemon")
}

#[then("each session's record is its own project's, in the one store her daemon keeps")]
fn records_in_one_store(_: &mut World) {
    red("each session's record is its own project's, in the one store")
}

#[then("each project lists its own session, and not the other's")]
fn own_listing(_: &mut World) {
    red("each project lists its own session, and not the other's")
}

#[then("her assistant ran on her terminal exactly as her daemon prepared it")]
fn ran_as_prepared(_: &mut World) {
    red("her assistant ran on her terminal exactly as her daemon prepared it")
}

#[then(
    "the command she typed recorded nothing itself: her daemon recorded the session and prepared its session home"
)]
fn frontend_recorded_nothing(_: &mut World) {
    red("the command she typed recorded nothing itself")
}

#[then(
    "the session's transcript holds what her assistant's hooks delivered to her daemon, each once"
)]
fn transcript_via_daemon(_: &mut World) {
    red("the session's transcript holds what the hooks delivered to her daemon")
}

#[then("her assistant ran under her daemon, not under the command she typed")]
fn ran_under_daemon(_: &mut World) {
    red("her assistant ran under her daemon")
}

#[then("her daemon stopped her assistant, and it was gone before the command exited")]
fn daemon_stopped_it(_: &mut World) {
    red("her daemon stopped her assistant")
}

#[then("ctxloom exited 130, saying the run was stopped by SIGINT")]
fn exited_sigint(_: &mut World) {
    red("ctxloom exited 130, saying the run was stopped by SIGINT")
}

#[then(regex = r#"^"([^"]+)" is listed as a child of "([^"]+)", run by her daemon$"#)]
fn listed_child(_: &mut World, _child: String, _owner: String) {
    red("the child is listed under its owner, run by her daemon")
}

#[then(regex = r#"^"([^"]+)"'s first turn was "([^"]*)"$"#)]
fn first_turn(_: &mut World, _child: String, _text: String) {
    red("the child's first turn")
}

#[then(regex = r#"^"([^"]+)"'s next turn was "([^"]*)"$"#)]
fn next_turn(_: &mut World, _child: String, _text: String) {
    red("the child's next turn")
}

#[then(regex = r#"^"([^"]+)" is listed as closed, and its assistant has exited$"#)]
fn listed_closed(_: &mut World, _child: String) {
    red("the child is listed as closed, and its assistant has exited")
}

#[then(regex = r#"^nothing "([^"]+)" was launched with is left at rest$"#)]
fn nothing_at_rest(_: &mut World, _child: String) {
    red("nothing the child was launched with is left at rest")
}

#[then("a new ctxloom daemon is serving for Alice")]
fn new_daemon(_: &mut World) {
    red("a new ctxloom daemon is serving for Alice")
}

#[then(regex = r#"^"([^"]+)" is listed as live, and its assistant never stopped$"#)]
fn still_live(_: &mut World, _harp: String) {
    red("the session is listed as live, and its assistant never stopped")
}

#[then(
    regex = r#"^"([^"]+)" found the new daemon and delivered again what the old one had not answered, and the record holds each once$"#
)]
fn redelivered(_: &mut World, _harp: String) {
    red("the session redelivered to the new daemon, each once")
}

#[then(
    regex = r#"^"([^"]+)" is working again in its own conversation, with what it was launched with$"#
)]
fn rehosted(_: &mut World, _child: String) {
    red("the child is re-hosted from its hand-off file")
}

#[then("her daemon has exited, and nothing says it serves")]
fn her_daemon_exited(w: &mut World) {
    daemon_exited(w);
}

#[then("it proves itself with a new certificate, and knows her by a new credential")]
fn new_credentials(w: &mut World) {
    rotated(w);
}

// ---------------------------------------------------- then: cli/daemon

#[then("a ctxloom daemon serves for this home, started detached in a session of its own")]
fn started_detached(w: &mut World) {
    let bytes = files::read_bytes(discovery_path(w)).unwrap_or_default();
    let found = run_ports::Discovery::decode(&bytes).expect("a daemon advertises itself");
    // T3: detached, it leads a session of its own (its session id is its
    // pid). Seen through procfs, so on Linux alone; elsewhere its discovery
    // file is what is seen.
    if let Ok(stat) = files::read(format!("/proc/{}/stat", found.pid)) {
        let after = &stat[stat.rfind(')').expect("a stat line") + 2..];
        let session: u32 = after
            .split(' ')
            .nth(3)
            .and_then(|s| s.parse().ok())
            .expect("a session id");
        assert_eq!(session, found.pid, "it leads a session of its own");
    }
}

#[then(expr = "the daemon's output goes to {string}")]
fn daemon_log(w: &mut World, path: String) {
    let rel = path.strip_prefix("~/").expect("a path under the home");
    let log = files::read(w.common.project.home().join(rel)).unwrap_or_default();
    assert!(
        log.contains(" serving at "),
        "the daemon logs there: {log:?}"
    );
}

#[then("both commands succeed")]
fn both_succeed(w: &mut World) {
    assert_eq!(w.daemon_spec.both.len(), 2, "two commands ran");
    for ran in &w.daemon_spec.both {
        assert_eq!(ran.code, 0, "{}", ran.said());
    }
}

#[then("exactly one daemon served them both")]
fn exactly_one(w: &mut World) {
    // One daemon took the single-instance lock and served; any other the
    // two started found it held, and never served.
    assert_eq!(daemons_logged(w), 1, "{}", daemon_log_text(w));
    assert!(serving_now(w), "{}", daemon_log_text(w));
}

#[then("a new daemon serves, and the discovery file names it")]
fn replaced(w: &mut World) {
    let old = w.daemon_spec.old.clone().expect("a daemon was killed");
    let new = discovery_now(w);
    assert_ne!(new.pid, old.pid, "another daemon advertises itself");
    let status = run_now(w, &["daemon", "status", "--format", "json"]);
    assert_eq!(
        status.json()["pid"],
        Value::from(new.pid),
        "{}",
        status.said()
    );
}

#[then("no project store was opened, and no engine was started")]
fn nothing_opened(w: &mut World) {
    let home = w.common.project.home();
    assert!(
        !files::exists(home.join(".ctxloom/ctxloom.db")),
        "the one store was not opened"
    );
    assert_eq!(per_project_stores(home), Vec::<PathBuf>::new());
    assert!(
        !files::exists(w.common.project.dir().join(".ctxloom/sessions")),
        "no session was set up, so no engine was started"
    );
}

#[then("no ctxloom daemon was started")]
fn none_started(w: &mut World) {
    let dir = w.common.project.home().join(".ctxloom/daemon");
    assert!(!files::exists(discovery_path(w)), "nothing advertised");
    assert!(!files::exists(dir.join("daemon.log")), "no daemon logged");
}

#[then(
    "the discovery file is readable by its owner alone, in a directory only its owner can enter"
)]
fn discovery_private(w: &mut World) {
    // Modes are unix's; on Windows the profile's ACL stands (M8).
    if let Some(mode) = files::mode_of(discovery_path(w)) {
        assert_eq!(mode & 0o777, 0o600, "the file is 0600");
        let dir = files::mode_of(daemon_dir(w)).expect("the directory");
        assert_eq!(dir & 0o777, 0o700, "the directory is 0700");
    }
}

#[then(
    "the discovery file names the daemon's endpoint, its certificate, a user bearer and its pid"
)]
fn discovery_holds(w: &mut World) {
    let found = discovery_now(w);
    assert!(
        found.endpoint.starts_with("https://127.0.0.1:"),
        "{}",
        found.endpoint
    );
    assert!(!found.cert.to_pem().is_empty(), "a certificate");
    assert_eq!(found.bearer.to_text().len(), 64, "a 256-bit bearer");
    assert!(found.pid > 0, "a pid");
    // What it names is what serves: the daemon answers its bearer.
    assert_eq!(get_daemon(&found), Ok(()));
}

#[then(regex = r#"^the output reports "([^"]+)" as the daemon's pid$"#)]
fn reports_pid(w: &mut World, path: String) {
    let doc = w.common.json();
    let pid = crate::tests::library::reports_as(&doc, &path);
    assert_eq!(pid, discovery_now(w).pid.to_string(), "{doc}");
}

#[then(regex = r#"^the output reports "([^"]+)" as the discovery file's endpoint$"#)]
fn reports_endpoint(w: &mut World, path: String) {
    let doc = w.common.json();
    let endpoint = crate::tests::library::reports_as(&doc, &path);
    assert_eq!(endpoint, discovery_now(w).endpoint, "{doc}");
}

#[then("neither the output, the daemon's log nor the project's store holds the user bearer")]
fn bearer_unseen(w: &mut World) {
    let bearer = discovery_now(w).bearer.to_text();
    let ran = w.common.ran();
    assert!(!ran.said().contains(&bearer), "the output holds the bearer");
    assert!(
        !daemon_log_text(w).contains(&bearer),
        "the daemon's log holds the bearer"
    );
    let home = w.common.project.home().join(".ctxloom");
    for name in ["ctxloom.db", "ctxloom.db-wal"] {
        let bytes = files::read_bytes(home.join(name)).unwrap_or_default();
        let text = String::from_utf8_lossy(&bytes);
        assert!(!text.contains(&bearer), "{name} holds the bearer");
    }
}

#[then("the call is refused as unauthenticated, and the refusal names no reason")]
fn unauthenticated(w: &mut World) {
    // `Called::Unauthenticated` carries nothing: the daemon said no reason a
    // typed refusal would hold.
    assert_eq!(
        w.daemon_spec.called.take(),
        Some(Err(daemon_ports::Called::Unauthenticated))
    );
}

#[then("the new discovery file holds another certificate and another bearer")]
fn rotated(w: &mut World) {
    let old = w.daemon_spec.old.as_ref().expect("a daemon was stopped");
    let new = discovery_now(w);
    assert_ne!(new.pid, old.pid, "another daemon");
    assert_ne!(new.cert, old.cert, "another certificate");
    assert_ne!(new.bearer, old.bearer, "another bearer");
}

#[then("a client still holding the old bearer is refused")]
fn old_bearer_refused(w: &mut World) {
    let old = w.daemon_spec.old.clone().expect("a daemon was stopped");
    let stale = run_ports::Discovery {
        bearer: old.bearer,
        ..discovery_now(w)
    };
    assert_eq!(
        get_daemon(&stale),
        Err(daemon_ports::Called::Unauthenticated)
    );
}

#[then("the daemon has exited, and nothing says it serves")]
fn daemon_exited(w: &mut World) {
    let Some(daemon) = w.daemon_spec.daemon.as_mut() else {
        red("the daemon has exited (a daemon this scenario did not start)")
    };
    let deadline = Instant::now() + Duration::from_secs(20);
    let code = loop {
        if let Some(status) = daemon.try_wait().expect("the daemon is waited for") {
            break status.code();
        }
        assert!(
            Instant::now() < deadline,
            "the daemon still runs; its log:\n{}",
            daemon_log_text(w)
        );
        std::thread::sleep(Duration::from_millis(50));
    };
    assert_eq!(code, Some(0), "{}", daemon_log_text(w));
    assert!(!serving_now(w), "nothing says it serves");
}

#[then("the daemon still serves")]
fn still_serves(w: &mut World) {
    let daemon = w
        .daemon_spec
        .daemon
        .as_mut()
        .expect("the scenario started a daemon");
    assert_eq!(
        daemon.try_wait().expect("the daemon is waited for"),
        None,
        "it exited; its log:\n{}",
        daemon_log_text(w)
    );
    assert!(serving_now(w), "{}", daemon_log_text(w));
}

#[then("the hook exits 0, and says nothing was left unrecorded")]
fn hook_delivered(_: &mut World) {
    red("the hook exits 0, and says nothing was left unrecorded")
}

#[then("a ctxloom daemon serves for this home, started by that hook")]
fn started_by_hook(_: &mut World) {
    red("a ctxloom daemon serves, started by that hook")
}

#[then("the session's transcript holds the prompt, once")]
fn prompt_once(_: &mut World) {
    red("the session's transcript holds the prompt, once")
}

#[then("the hook exits 0, and its answer to the engine is unchanged")]
fn hook_fails_open(_: &mut World) {
    red("the hook exits 0, and its answer to the engine is unchanged")
}

#[then(expr = "the hook warns {string} for the prompt, within its bound")]
fn hook_warns(_: &mut World, _code: String) {
    red("the hook warns that the prompt was not recorded, within its bound")
}

#[then("no run is left owed a runner, no incarnation is left open and no fact is recorded twice")]
fn record_whole(_: &mut World) {
    red("the record is whole after the daemon was killed")
}

// ------------------------------------------- then: cli/session_children

#[then(expr = "the child {string} of {string} ran its first turn on {string}")]
fn child_first_turn(_: &mut World, _child: String, _owner: String, _text: String) {
    red("the child ran its first turn on its task")
}

#[then("the child's hand-off file is readable by its owner alone while the child is open")]
fn hand_off_private(_: &mut World) {
    red("the child's hand-off file is 0600 while the child is open")
}

#[then("no hand-off file was left behind")]
fn no_hand_off(_: &mut World) {
    red("no hand-off file was left behind")
}

#[then(regex = r#"^the child ran a turn on "([^"]*)"$"#)]
fn child_ran_turn(_: &mut World, _text: String) {
    red("the child ran a turn on the message")
}

#[then(regex = r#"^the child ran no turn, and lists "([^"]*)" as waiting for one$"#)]
fn child_waits(_: &mut World, _text: String) {
    red("the child ran no turn, and lists the message as pending")
}

#[then(expr = "the child's turn in flight was interrupted, and its next turn was {string}")]
fn child_steered(_: &mut World, _text: String) {
    red("the child's turn in flight was interrupted by the steer")
}

#[then(expr = "the child ran one turn on {string}")]
fn child_one_turn(_: &mut World, _text: String) {
    red("the child ran one turn on the message")
}

#[then("the child's next turn runs in a new conversation of the same session")]
fn child_cleared(_: &mut World) {
    red("the child's next turn runs in a new conversation of the same session")
}

#[then(
    expr = "the child {string} is listed as closed, its engine has exited and its hand-off file is gone"
)]
fn child_ended(_: &mut World, _child: String) {
    red("the child is closed, its engine exited and its hand-off file gone")
}

// ------------------------------------------------- then: cli/run's Rule

#[then("the assistant was a child of the daemon, and its session's lock was the daemon's")]
fn one_shot_hosted(_: &mut World) {
    red("the one-shot's assistant was the daemon's child")
}

#[then("the daemon asked the assistant to stop, and it was gone before the command exited")]
fn daemon_asked_stop(_: &mut World) {
    red("the daemon asked the assistant to stop")
}

#[then(
    regex = r"^the command exits 128 plus the signal's number, (\d+), saying the run was stopped by (SIG[A-Z]+)$"
)]
fn exits_by_signal(_: &mut World, _code: String, _signal: String) {
    red("the command exits 128 plus the signal's number")
}

#[then(
    "the assistant was started with the program, arguments, environment and directory of the daemon's launch, and nothing else"
)]
fn started_as_launched(_: &mut World) {
    red("the TUI was started exactly as the daemon's launch says")
}

#[then(
    "the command took the session's lock before the assistant started and let it go after it exited"
)]
fn frontend_lock(_: &mut World) {
    red("the command held the session's lock around the TUI")
}

#[then("the daemon recorded the assistant's start and its exit, each once")]
fn start_and_exit(_: &mut World) {
    red("the daemon recorded the TUI's start and exit, each once")
}

#[then("no session was minted")]
fn no_mint(_: &mut World) {
    red("no session was minted")
}

#[then("the assistant was never stopped")]
fn never_stopped(_: &mut World) {
    red("the assistant was never stopped")
}

#[then("the command started a new daemon, and reported the assistant's exit to it once")]
fn reported_to_new(_: &mut World) {
    red("the command reported the TUI's exit to a new daemon, once")
}

// ---------------------------------------- the owner's rulings S8 to S20

#[then("the hosted engines were ended cleanly before the daemon exited")]
fn ended_cleanly(w: &mut World) {
    // The stub's input was closed and it ran to its end, which the daemon
    // waits for (its end grace) before it exits.
    let exited = w.common.project.home().join("fake-claude/child-exited");
    let deadline = Instant::now() + Duration::from_secs(20);
    while !files::exists(&exited) {
        assert!(
            Instant::now() < deadline,
            "the child never ended; the daemon's log:\n{}",
            daemon_log_text(w)
        );
        std::thread::sleep(Duration::from_millis(50));
    }
}

#[then(
    "the run the killed daemon hosted ended lost, and its turn in flight ended failed, with its owner told"
)]
fn lost_and_told(_: &mut World) {
    red("the lost run's turn in flight ended failed, and its owner was told")
}

#[then(
    regex = r#"^"([^"]+)" runs again as its session's next incarnation, in its own conversation$"#
)]
fn next_incarnation(_: &mut World, _child: String) {
    red("the child runs again as its session's next incarnation")
}

#[then("the command started a new daemon and sent the write once more, under the same id")]
fn retried_once(_: &mut World) {
    red("the command retried its write once on a new daemon, under the same id")
}

#[then("the write was applied once, and its second delivery was a duplicate")]
fn applied_once(_: &mut World) {
    red("the write was applied once, and its retry was a duplicate")
}

#[then("the daemon's confirmation was asked at her terminal, and the session is gone")]
fn asked_at_terminal(_: &mut World) {
    red("the daemon's confirmation was asked at her terminal")
}

#[when("its orchestrator calls a ctxloom tool whose daemon call asks for confirmation")]
fn tool_asks(_: &mut World) {
    red("its orchestrator calls a ctxloom tool whose daemon call asks for confirmation")
}

#[then(
    "the question reached its engine as an MCP elicitation, and the engine's answer reached the daemon"
)]
fn elicited(_: &mut World) {
    red("the question reached its engine as an MCP elicitation")
}

#[given(expr = "an interactive session {string} whose assistant is running on its terminal")]
fn primary_running(_: &mut World, _harp: String) {
    red("an interactive session whose assistant is running on its terminal")
}

#[then("the assistant on its terminal was asked to stop, then killed once its grace had passed")]
fn terminal_stopped(_: &mut World) {
    red("the assistant on its terminal was stopped, gracefully, then killed")
}

#[then(regex = r#"^"([^"]+)" is listed as closed$"#)]
fn closed_listed(_: &mut World, _harp: String) {
    red("the session is listed as closed")
}

#[given(
    expr = "a session {string} whose engine environment names ANTHROPIC_API_KEY through its engine block"
)]
fn owner_curated_env(_: &mut World, _harp: String) {
    red("a session whose engine environment names ANTHROPIC_API_KEY")
}

#[given("the daemon was started from a shell holding CLAUDE_CODE_OAUTH_TOKEN")]
fn daemon_env_token(_: &mut World) {
    red("the daemon was started from a shell holding CLAUDE_CODE_OAUTH_TOKEN")
}

#[when(expr = "its orchestrator launches a child {string}")]
fn orchestrator_launches(_: &mut World, _child: String) {
    red("its orchestrator launches a child")
}

#[then(expr = "the child {string} was given ANTHROPIC_API_KEY, and not CLAUDE_CODE_OAUTH_TOKEN")]
fn child_given_curated(_: &mut World, _child: String) {
    red("the child was given the owner's curated credential only")
}

// ------------------------------------------------------ trust (ruling 142)

#[given(
    "Alice has cloned a project whose own ctxloom config names the program that runs as her assistant"
)]
fn cloned_project(_: &mut World) {
    red("Alice has cloned a project whose config names its own assistant program")
}

#[then(
    regex = r#"^she is told the project's own "([^"]+)" waits for her trust, and that `ctxloom trust` is how to give it$"#
)]
fn told_untrusted(_: &mut World, _path: String) {
    red("she is told the project-only setting waits for her trust")
}

#[then("the project is listed as trusted by her, just now")]
fn trusted_by_her(_: &mut World) {
    red("the project is listed as trusted by her, just now")
}

#[then("her assistant ran as the program the project names")]
fn ran_projects_program(_: &mut World) {
    red("her assistant ran as the program the project names")
}

#[given("Alice has trusted the cloned project")]
fn has_trusted(_: &mut World) {
    red("Alice has trusted the cloned project")
}

#[given("a pull changes the program the project names")]
fn pull_changes(_: &mut World) {
    red("a pull changes the program the project names")
}

#[then("her assistant ran as the program the project names now")]
fn ran_projects_program_now(_: &mut World) {
    red("her assistant ran as the program the project names now")
}

#[given(expr = "an initialized ctxloom project whose project config says {string}")]
fn project_says(_: &mut World, _setting: String) {
    red("an initialized ctxloom project whose project config says a project-only setting")
}

#[given(
    expr = "an initialized ctxloom project whose own {string} bundle replaces the setup prompt"
)]
fn project_overrides_setup(_: &mut World, _bundle: String) {
    red("a project whose own ctxloom bundle replaces the setup prompt")
}

#[then("the setup used the built-in prompt, and said the project's own waits for trust")]
fn builtin_setup(_: &mut World) {
    red("the setup used the built-in prompt, and said the project's waits for trust")
}

#[then("the project is not trusted")]
fn not_trusted(_: &mut World) {
    red("the project is not trusted")
}

#[then("the project is listed as trusted by the user running ctxloom, with the time it was given")]
fn listed_trusted(_: &mut World) {
    red("the project is listed as trusted, with its actor and time")
}

#[given("the project's settings change after the preview and before the answer")]
fn changed_mid_trust(_: &mut World) {
    red("the project's settings change after the preview and before the answer")
}

#[given("an initialized ctxloom project that the user has trusted")]
fn project_trusted(_: &mut World) {
    red("an initialized ctxloom project that the user has trusted")
}

#[given(expr = "an initialized ctxloom project with a remote {string}")]
fn project_with_remote(_: &mut World, _remote: String) {
    red("an initialized ctxloom project with a remote")
}

#[then(expr = "the remote {string} is listed as trusted by the user running ctxloom")]
fn remote_trusted(_: &mut World, _remote: String) {
    red("the remote is listed as trusted")
}

// -------------------------------------------------- approvals (ruling 142)

#[given(
    regex = r#"^a child "([^"]+)" of "([^"]+)" whose engine has asked to run "([^"]+)" with "([^"]*)"$"#
)]
fn child_asked(_: &mut World, _child: String, _owner: String, _tool: String, _input: String) {
    red("a child whose engine has asked to run a tool")
}

#[then(regex = r"^the child's engine was told (.+)$")]
fn engine_told(_: &mut World, _what: String) {
    red("the child's engine was told the answer")
}

#[then(expr = "no ask is waiting under {string}")]
fn none_waiting(_: &mut World, _root: String) {
    red("no ask is waiting under the root")
}

#[given(expr = "a child {string} of {string} whose ask has been answered")]
fn ask_answered(_: &mut World, _child: String, _owner: String) {
    red("a child whose ask has been answered")
}

#[when("the ask's time runs out")]
fn ask_expires(_: &mut World) {
    red("the ask's time runs out")
}

#[then(regex = r#"^the child's claude was launched with "([^"]+)" "([^"]+)"$"#)]
fn claude_flag(_: &mut World, _flag: String, _value: String) {
    red("the child's claude was launched with the permission prompt tool")
}

#[then("the child's session home names ctxloom's MCP server")]
fn home_names_mcp(_: &mut World) {
    red("the child's session home names ctxloom's MCP server")
}

#[given(
    expr = "an opencode child {string} of {string} that sends a permission request for {string}"
)]
fn opencode_asks(_: &mut World, _child: String, _owner: String, _tool: String) {
    red("an opencode child that sends a permission request")
}

#[then("the child's opencode was answered with its request's allow-once option")]
fn opencode_allowed(_: &mut World) {
    red("the child's opencode was answered allow-once")
}

#[given(regex = r#"^"([^"]+)" asks to run "([^"]+)" with "([^"]*)"$"#)]
fn journey_child_asks(_: &mut World, _child: String, _tool: String, _input: String) {
    red("the child asks to run a tool")
}

#[then(regex = r#"^she sees "([^"]+)" asking to run "([^"]+)" with "([^"]*)"$"#)]
fn sees_ask(_: &mut World, _child: String, _tool: String, _input: String) {
    red("she sees the child's ask")
}

#[then(regex = r#"^"([^"]+)" ran "([^"]*)", and nothing waits for her any more$"#)]
fn ran_after_allow(_: &mut World, _child: String, _input: String) {
    red("the child ran the allowed tool, and nothing waits")
}

// ------------------------------------- the architecture rulings T13-T30

#[then("the removal was previewed first, and applied with her answer and the preview's tag")]
fn previewed_then_applied(_: &mut World) {
    red("the removal was previewed, then applied with the answer and tag")
}

#[given(
    expr = "the bundle {string} changes after its removal is previewed and before Alice answers"
)]
fn bundle_changes_mid(_: &mut World, _b: String) {
    red("the bundle changes between preview and answer")
}

#[then("the apply was refused as stale, and the CLI previewed the removal again")]
fn refused_stale(_: &mut World) {
    red("the apply was refused as stale")
}

#[given(expr = "an initialized ctxloom project with a recorded session {string}")]
fn project_with_session(w: &mut World, harp: String) {
    w.common.project = crate::tests::library::Project::initialized();
    w.plant(&harp, "finished");
}

/// A second project beside the first, under the same home: its directory.
fn second_dir(w: &World) -> std::path::PathBuf {
    w.common
        .project
        .dir()
        .parent()
        .expect("the project has a parent")
        .join("second")
}

#[given(expr = "a second initialized project with a recorded session {string}")]
fn second_project_session(w: &mut World, harp: String) {
    let dir = second_dir(w);
    crate::tests::files::put(
        dir.join(".ctxloom/config.yaml"),
        crate::tests::library::MINIMAL_CONFIG,
    );
    let ran = w.common.project.run_in(
        &dir,
        &[
            "test",
            "seed-session",
            &harp,
            "--transcript",
            "--lock",
            "free",
        ],
    );
    assert_eq!(ran.code, 0, "seeding {harp}: {}", ran.said());
}

/// Every project's own store under the home, from before the one store.
fn per_project_stores(home: &std::path::Path) -> Vec<std::path::PathBuf> {
    crate::tests::files::children(home.join(".ctxloom/projects"))
        .into_iter()
        .map(|dir| dir.join("ctxloom.db"))
        .filter(|db| crate::tests::files::exists(db))
        .collect()
}

#[then("both sessions are in the one store under ctxloom's home, and no project has a store of its own")]
fn one_store(w: &mut World) {
    let home = w.common.project.home().to_path_buf();
    assert!(
        crate::tests::files::exists(home.join(".ctxloom/ctxloom.db")),
        "the one store"
    );
    assert_eq!(per_project_stores(&home), Vec::<std::path::PathBuf>::new());
    let all = w
        .common
        .project
        .run(&["session", "list", "--all", "--format", "json"]);
    assert_eq!(all.code, 0, "{}", all.said());
    for harp in ["amber-swift-owl", "brisk-copper-moth"] {
        assert!(all.stdout.contains(harp), "{harp}: {}", all.said());
    }
}

/// The project id the per-project store step gives the project.
const OLD_PROJECT: &str = "old-store-project";

/// What the per-project store held when it was planted.
const OLD_BYTES: &[u8] = b"a store of the project's own, from before the one store";

#[given("the project has a store of its own from before the one store")]
fn per_project_store(w: &mut World) {
    let project = &w.common.project;
    project.write(".ctxloom/project-id", &format!("{OLD_PROJECT}\n"));
    crate::tests::files::put(
        project
            .home()
            .join(".ctxloom/projects")
            .join(OLD_PROJECT)
            .join("ctxloom.db"),
        OLD_BYTES,
    );
}

#[then("nothing was imported from it")]
fn nothing_imported(w: &mut World) {
    let home = w.common.project.home();
    let old = home
        .join(".ctxloom/projects")
        .join(OLD_PROJECT)
        .join("ctxloom.db");
    assert_eq!(
        crate::tests::files::read_bytes(&old).expect("still there"),
        OLD_BYTES,
        "left as it was"
    );
    assert!(
        !crate::tests::files::exists(home.join(".ctxloom/ctxloom.db")),
        "no store was made from it"
    );
}

#[then(regex = r#"^its incarnation was recorded with the cause "([^"]+)"$"#)]
fn incarnation_cause(_: &mut World, _c: String) {
    red("its incarnation was recorded with the cause")
}

#[given(expr = "a child {string} of {string} that has been re-hosted {int} times in the last hour")]
fn rehosted_n(_: &mut World, _c: String, _o: String, _n: i64) {
    red("a child re-hosted n times in the last hour")
}

#[then(regex = r#"^"([^"]+)" was not re-hosted again, and ended runner-lost$"#)]
fn past_budget(_: &mut World, _c: String) {
    red("the child was not re-hosted past its budget")
}

#[then(regex = r#"^"([^"]+)" was told that its child ended$"#)]
fn owner_told(_: &mut World, _o: String) {
    red("the owner was told its child ended")
}

#[given(expr = "a child {string} whose session settings its agent has edited")]
fn settings_edited(_: &mut World, _c: String) {
    red("a child whose session settings its agent edited")
}

#[then("the re-hosted child's session settings are the ones the user's config makes, and its agent's edit is gone")]
fn settings_regenerated(_: &mut World) {
    red("the re-hosted child's settings were regenerated")
}

/// A stub engine of a registration scenario: it records the directory its
/// config is read from and its arguments, then answers as a one-shot turn
/// answers (claude's stream-json result, opencode's text), or, at the
/// terminal, just exits.
const REGISTERED_STUB: &str = r#"here=$(dirname "$0")
printf '%s\n' "${CLAUDE_CONFIG_DIR:-$OPENCODE_CONFIG_DIR}" > "$here/config-dir"
: > "$here/args"
for a in "$@"; do printf '%s\n' "$a" >> "$here/args"; done
case " $* " in
  *" -p "*)
    cat > /dev/null
    printf '{"type":"system","subtype":"init","cwd":"/work","session_id":"S1","tools":[]}\n'
    printf '{"type":"result","subtype":"success","is_error":false,"result":"ok","session_id":"S1"}\n'
    ;;
  *" run "*)
    cat > /dev/null
    printf '{"type":"text","sessionID":"ses_1","part":{"type":"text","text":"ok","time":{"end":1}}}\n'
    ;;
esac
"#;

#[given(regex = r"^a (child|interactive) session on (claude-code|opencode)$")]
fn session_on_engine(w: &mut World, kind: String, engine: String) {
    let program = match engine.as_str() {
        "claude-code" => "claude",
        _ => "opencode",
    };
    let dir = w.common.project.home().join("registered");
    files::put_program(dir.join(program), REGISTERED_STUB);
    w.common.project.write(
        ".ctxloom/config.yaml",
        &format!("schema_version: 9\nagents:\n  dev:\n    engine: {engine}\ndefault_agent: dev\n"),
    );
    w.common.project.write_home(
        ".ctxloom/config.yaml",
        &format!(
            "schema_version: 9\neditor:\n  command: \"true\"\nengines:\n  {engine}:\n    command: {}\n",
            dir.join(program).display()
        ),
    );
    let mut env = env_of(w);
    let args: Vec<&str> = match kind.as_str() {
        "child" => vec!["run", "--one-shot", "a task"],
        _ => {
            env.push(("CTXLOOM_TEST_TERMINAL".to_string(), "1".to_string()));
            vec!["run"]
        }
    };
    let env: Vec<(&str, &str)> = env.iter().map(|(k, v)| (k.as_str(), v.as_str())).collect();
    let ran = w.common.project.run_with(&env, &args);
    assert!(
        files::exists(dir.join("config-dir")),
        "the {engine} session never started: {}",
        ran.said()
    );
    w.daemon_spec.registered = Some((engine, dir));
}

#[then(expr = "its session home registers {string} as the MCP server {string}")]
fn registers_mcp(w: &mut World, command: String, name: String) {
    let (engine, dir) = w
        .daemon_spec
        .registered
        .clone()
        .expect("a session ran on an engine");
    let config_dir = PathBuf::from(
        files::read(dir.join("config-dir"))
            .expect("the engine recorded its config dir")
            .trim(),
    );
    let registered: Vec<String> = match engine.as_str() {
        "claude-code" => {
            // The servers the launch carries: the file `--mcp-config` names.
            let args = files::read(dir.join("args")).expect("the engine recorded its arguments");
            let args: Vec<&str> = args.lines().collect();
            let file = args
                .windows(2)
                .find(|pair| pair[0] == "--mcp-config")
                .map(|pair| pair[1].to_string())
                .unwrap_or_else(|| panic!("claude was given no --mcp-config: {args:?}"));
            let config: Value =
                serde_json::from_str(&files::read(&file).expect("the MCP config")).expect("JSON");
            let entry = &config["mcpServers"][&name];
            assert!(entry.is_object(), "no server {name}: {config}");
            assert!(
                entry.get("env").is_none(),
                "no identity in the entry: {entry}"
            );
            let mut words = vec![entry["command"].as_str().unwrap_or_default().to_string()];
            words.extend(
                entry["args"]
                    .as_array()
                    .into_iter()
                    .flatten()
                    .map(|a| a.as_str().unwrap_or_default().to_string()),
            );
            words
        }
        _ => {
            // opencode's session config: its `mcp` entry, a local one.
            let config: Value = serde_json::from_str(
                &files::read(config_dir.join("opencode.json")).expect("opencode's config"),
            )
            .expect("JSON");
            let entry = &config["mcp"][&name];
            assert_eq!(entry["type"], "local", "{config}");
            assert!(
                entry.get("environment").is_none(),
                "no identity in the entry: {entry}"
            );
            entry["command"]
                .as_array()
                .unwrap_or_else(|| panic!("no server {name}: {config}"))
                .iter()
                .map(|a| a.as_str().unwrap_or_default().to_string())
                .collect()
        }
    };
    assert_eq!(registered.join(" "), command, "{engine}");
}

#[when("its engine lists the tools of ctxloom's MCP server")]
fn lists_tools(_: &mut World) {
    red("its engine lists ctxloom's MCP tools")
}

#[then(regex = r"^the tools are (.+)$")]
fn tools_are(_: &mut World, _t: String) {
    red("the MCP tools are the spec's")
}

/// The resource name of the session `harp` of the scenario's project.
fn session_name(w: &World, harp: &str) -> String {
    w.common
        .named(&format!("projects/{{project}}/sessions/{harp}"))
}

/// The engine of `owner` calls ctxloom's tool `tool` with `arguments`,
/// through `ctxloom mcp serve` started as its engine starts it (the
/// session's name in its environment): what it was answered.
fn engine_calls(w: &mut World, owner: &str, tool: &str, arguments: Value) {
    let me = session_name(w, owner);
    let env = env_of(w);
    let env: Vec<(&str, &str)> = env.iter().map(|(k, v)| (k.as_str(), v.as_str())).collect();
    let mut mcp = crate::tests::mcp_serve::Mcp::for_session(&w.common.project, &me, &env);
    let answered = mcp.call(tool, arguments);
    let (code, err) = mcp.finish();
    assert_eq!(code, 0, "the server failed: {err}");
    w.daemon_spec.tool = Some(answered);
}

/// The events of the session `harp`'s stream, as `ctxloom test
/// session-events` lists them: each one's type, and its message id when it
/// has one.
fn session_events(w: &World, harp: &str) -> Vec<Value> {
    let ran = run_now(w, &["test", "session-events", harp]);
    assert_eq!(ran.code, 0, "{}", ran.said());
    ran.json().as_array().cloned().unwrap_or_default()
}

/// How many of `events` are of the type `name` (its message's name).
fn count_of(events: &[Value], name: &str) -> usize {
    events.iter().filter(|e| e["type"] == name).count()
}

/// The tool's answer: not an error, its first text the session it answered.
fn tool_session(w: &World) -> Value {
    let answered = w.daemon_spec.tool.clone().expect("a tool was called");
    assert_ne!(
        answered["isError"],
        true,
        "the tool failed: {answered}; the daemon's log:\n{}",
        daemon_log_text(w)
    );
    crate::tests::mcp_serve::answered_json(&answered)
}

#[when(
    regex = r#"^the engine of "([^"]+)" calls "([^"]+)" for "([^"]+)" with (inform|wake|steer) "([^"]*)"$"#
)]
fn tool_send(
    w: &mut World,
    owner: String,
    tool: String,
    child: String,
    urgency: String,
    text: String,
) {
    engine_calls(
        w,
        &owner,
        &tool,
        json!({"child": child, "urgency": urgency, "text": text}),
    );
}

#[then("the tool answered with the child's session as the daemon's read model has it after the message")]
fn tool_answered_view(w: &mut World) {
    let session = tool_session(w);
    let child = session_name(w, "brisk-copper-moth");
    assert_eq!(session["name"], child, "{session}");
    let shown = run_now(
        w,
        &["session", "show", "brisk-copper-moth", "--format", "json"],
    );
    assert_eq!(shown.code, 0, "{}", shown.said());
    let shown = shown.json();
    for field in ["uid", "owner", "engine"] {
        assert_eq!(
            session[field], shown[field],
            "{field}: {session} vs {shown}"
        );
    }
}

#[then("the message was sent once, under the id the server chose")]
fn sent_once(w: &mut World) {
    let answered = w.daemon_spec.tool.clone().expect("a tool was called");
    let id: Value = serde_json::from_str(
        answered["content"][1]["text"]
            .as_str()
            .unwrap_or_else(|| panic!("the message's id is not answered: {answered}")),
    )
    .expect("JSON");
    let id = id["message_id"].as_str().expect("a message id").to_string();
    let events = session_events(w, "brisk-copper-moth");
    let sent: Vec<&Value> = events
        .iter()
        .filter(|e| e["type"] == "MessageSent")
        .filter(|e| e["text"] == "check the tests")
        .collect();
    assert_eq!(sent.len(), 1, "{events:?}");
    assert_eq!(sent[0]["message_id"], id.as_str(), "{events:?}");
}

#[when(regex = r#"^the engine of "([^"]+)" calls "([^"]+)" for "([^"]+)"$"#)]
fn tool_call(w: &mut World, owner: String, tool: String, child: String) {
    engine_calls(w, &owner, &tool, json!({"child": child}));
}

#[then("the tool answered once the new daemon had the close, and the close was applied once")]
fn tool_retried(w: &mut World) {
    let session = tool_session(w);
    assert_eq!(session["closed"], true, "{session}");
    let old = w
        .daemon_spec
        .before_kill
        .clone()
        .expect("a daemon was killed");
    let now = discovery_now(w);
    assert_ne!(
        now.pid, old.pid,
        "the tool's retry was answered by a new daemon"
    );
    assert!(
        !files::exists(daemon_dir(w).join(DIE_AFTER_APPLY)),
        "the switch was taken"
    );
    let events = session_events(w, "brisk-copper-moth");
    assert_eq!(count_of(&events, "SessionClosed"), 1, "{events:?}");
}

#[when("its engine calls a tool whose change asks for an edited document, and answers the elicitation with new text")]
fn tool_edit(_: &mut World) {
    red("an engine answers an edit need with text")
}

#[then("the daemon applied the new text, and no editor was started")]
fn applied_text(_: &mut World) {
    red("the daemon applied the new text without an editor")
}

#[given(
    regex = r#"^a claude child "([^"]+)" of "([^"]+)" whose engine calls "approval_prompt" for "([^"]+)" with "([^"]*)"$"#
)]
fn approval_prompt_called(_: &mut World, _c: String, _o: String, _t: String, _i: String) {
    red("a claude child calls approval_prompt")
}

#[when(regex = r#"^(Alice runs "[^"]+"|nobody answers within the ask's time)$"#)]
fn approval_answered_by(_: &mut World, _w: String) {
    red("the ask is answered, or times out")
}

#[then(regex = r"^the tool answered the engine (.+)$")]
fn tool_answered_engine(_: &mut World, _a: String) {
    red("the approval tool answered the engine")
}

#[given(expr = "a child {string} whose engine runs in a container")]
fn child_in_container(_: &mut World, _c: String) {
    red("a child whose engine runs in a container")
}

#[then("the container holds no ctxloom binary of the host's, and its MCP server and hooks reach the daemon over TCP and TLS")]
fn container_path(_: &mut World) {
    red("the container reaches the daemon through its MCP server")
}

#[then("the change is in the tree whole, once, and every bundle still reads")]
fn change_whole(_: &mut World) {
    red("the change is in the tree whole, once")
}

#[given(expr = "another process creates the bundle {string} while its creation is staged")]
fn concurrent_create(_: &mut World, _b: String) {
    red("another process creates the bundle meanwhile")
}

#[then(
    regex = r#"^the other process's "([^"]+)" is untouched, and nothing staged is left behind$"#
)]
fn other_untouched(_: &mut World, _b: String) {
    red("the other process's bundle is untouched")
}

#[then(
    regex = r#"^the skill "([^"]+)" is the one directory "([^"]+)" of the bundle "([^"]+)", holding its "\.meta\.yaml"$"#
)]
fn skill_one_dir(_: &mut World, _s: String, _d: String, _b: String) {
    red("the skill is one directory with its metadata inside")
}

#[then(regex = r#"^only "([^"]+)" of the bundle "([^"]+)" changed$"#)]
fn only_changed(_: &mut World, _p: String, _b: String) {
    red("only the source file changed")
}

#[then(
    regex = r#"^the session's directory, its lock inside it, and its output folder are under "([^"]+)"$"#
)]
fn renamed_whole(_: &mut World, _h: String) {
    red("the session's directory, lock and output moved")
}

#[then(
    regex = r#"^the directory, lock and output folder are under "([^"]+)", and its name is claimed once$"#
)]
fn rename_converged(_: &mut World, _h: String) {
    red("the rename converged")
}

#[then(regex = r#"^nothing lists "([^"]+)", its streams are purged and no trash is left$"#)]
fn remove_converged(_: &mut World, _h: String) {
    red("the remove converged")
}

#[then("the files it writes are as one uninterrupted run would leave them")]
fn foreign_converged(_: &mut World) {
    red("the foreign writer converged")
}

// ------------------------------------- the rulings after T30 (S42, S53)

#[then("nothing was applied, and the daemon answered the preview")]
fn preview_only(_: &mut World) {
    red("nothing was applied, and the daemon answered the preview")
}

/// S55: a title moves nothing. Every planted session's lock is under its
/// harp, and nothing under the sessions or the output root is named for
/// anything but a planted harp.
#[then("the session's directory, lock and output folder were not moved")]
fn not_moved(w: &mut World) {
    let harps = w.lock_free_dead.clone();
    assert!(!harps.is_empty(), "a session was planted");
    let project = w.common.project.dir().to_path_buf();
    for harp in &harps {
        assert!(
            crate::tests::files::exists(
                project
                    .join(".ctxloom/sessions")
                    .join(format!("{harp}.lock"))
            ),
            "{harp}'s lock is where it was"
        );
    }
    for root in [project.join(".ctxloom/sessions"), project.join("ctxloom")] {
        for entry in crate::tests::files::children(&root) {
            let name = entry.file_name().unwrap().to_string_lossy().to_string();
            assert!(
                harps
                    .iter()
                    .any(|h| name == *h || name.strip_suffix(".lock") == Some(h.as_str())),
                "{} is named for no planted session",
                entry.display()
            );
        }
    }
}

#[then(
    regex = r#"^the output reports "([^"]+)" as the project's harp and the session's, "([^"]+)"$"#
)]
fn six_word_name(w: &mut World, path: String, shape: String) {
    // Slice 2 B3: `projects/<project harp>/sessions/<harp>`, the project's
    // harp being one minted for it (a harp: no `/`, never empty).
    let ran = w.common.ran();
    let doc: serde_json::Value = serde_json::from_str(&ran.stdout)
        .unwrap_or_else(|e| panic!("not json ({e}): {}", ran.said()));
    let name = crate::tests::library::reports_as(&doc, &path);
    let (before, after) = shape
        .split_once("<project harp>")
        .expect("the shape names the project harp");
    let project = name
        .strip_prefix(before)
        .and_then(|rest| rest.strip_suffix(after))
        .unwrap_or_else(|| panic!("{path} is {name:?}, not {shape:?}: {}", ran.said()));
    assert!(
        !project.is_empty() && !project.contains('/'),
        "{project:?} is no harp"
    );
}

#[then(regex = r#"^its lock is "([^"]+)", inside its directory$"#)]
fn lock_inside(_: &mut World, _path: String) {
    red("the session's lock is inside its directory")
}

#[then(regex = r#"^the bundle "([^"]+)" is the imported one, whole, and no trash is left$"#)]
fn import_converged(_: &mut World, _bundle: String) {
    red("bundle import --force converged")
}
