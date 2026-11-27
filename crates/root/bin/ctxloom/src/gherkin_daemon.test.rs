//! The steps of the user daemon's spec (ruling 132, the target T1-T12):
//! `journeys/j002700_user_daemon.feature`, `cli/daemon.feature`,
//! `cli/session_children.feature` and the daemon Rule of `cli/run.feature`,
//! as the owner's rulings on the spec calls (S1-S20, 2026-11-11) shaped them;
//! and trust and approvals (ruling 142): `journeys/j002800_trusting_a_project.feature`,
//! `cli/trust.feature` and `cli/session_approvals.feature`; and the
//! architecture rulings T13-T30: `cli/mcp_serve.feature` and
//! `cli/atomic_writes.feature`; and the shutdown (OD-62, OD-63):
//! `cli/shutdown.feature`; and step 8 (approvals and trust), whose harness
//! (notes, started commands, other projects, a terminal acted at) is real.
//!
//! RED BY DESIGN. The spec is committed before the daemon is reworked to
//! it, so every step here that the existing harness cannot already take is a
//! stub that FAILS, naming itself: nothing is implemented. Each stub becomes
//! a real step with the milestone that builds what it asserts.

use std::path::{Path, PathBuf};
use std::process::{Child, Command, Stdio};
use std::time::{Duration, Instant};

use cucumber::{given, then, when};
use serde_json::{json, Value};

use super::capture::{assert_no_envelope, count, kind_of};
use super::{writes, World};
use crate::tests::gherkin_common::CommonWorld;
use crate::tests::{files, procs};

/// The variable a client gives the daemon it spawns as its idle time.
const IDLE_VARIABLE: &str = "CTXLOOM_DAEMON_IDLE_SECONDS";

/// The idle time of a daemon a scenario starts, in seconds: short, so a
/// scenario's wait for it is.
const IDLE_SECONDS: u64 = 2;

/// The idle time of a daemon a busy scenario starts ("a ctxloom daemon is
/// serving for this home, with <busy>"), which it must outlive: long enough
/// for the steps that make it busy (a session seeded and launched, a few
/// commands) to finish within it on a loaded host. Its daemon's idle clock
/// runs from its start, and a request does not reset it, so at
/// [`IDLE_SECONDS`] a busy step that took over 2 s found it already gone
/// (land of 2026-11-18, load 20 and more). "The daemon's idle time passes"
/// waits this one out, so "it still serves" is still asked after it.
const BUSY_IDLE_SECONDS: u64 = 8;

/// The idle time of a daemon a scenario's command starts: long enough to
/// outlive the scenario's steps, short enough to leave nothing for long.
pub(super) const SPAWNED_IDLE_SECONDS: u64 = 15;

/// What a scenario started: the daemon, and the run that keeps it busy. A
/// World drops this FIRST (its first field), before its home is removed:
/// the run is released and waited for, the daemon killed outright (T3: it
/// is kill-tolerant) and reaped.
#[derive(Default)]
pub(super) struct State {
    /// A step changed a tracked file by hand since the last hook fired: the
    /// daemon owes the session's next hook the change only once a round of
    /// its own has recorded it ([`settled`]).
    pub(crate) edited_by_hand: bool,
    /// The bundle changed between a preview and Alice's answer, if any.
    changes_before_answer: Option<String>,
    /// The stores' evidence as it was before the scenario's command ran.
    store_before: Option<StoreEvidence>,
    pub(super) daemon: Option<Child>,
    /// The process holding the scenario's own lifeline, when a step gave
    /// its commands one (the lifeline's forcing test).
    holder: Option<Child>,
    /// The idle time "the daemon's idle time passes" waits out, when not
    /// [`IDLE_SECONDS`]: a busy daemon's [`BUSY_IDLE_SECONDS`].
    idle_wait: Option<u64>,
    run: Option<Child>,
    /// The file whose presence lets the stub engine end.
    release: Option<PathBuf>,
    /// What a client's call to the daemon came to.
    called: Option<Result<(), daemon_ports::Called>>,
    /// The discovery of a daemon since stopped.
    old: Option<run_ports::Discovery>,
    /// The discovery of the daemon started after it, read while it served.
    new: Option<run_ports::Discovery>,
    /// The daemon a command was seen to start.
    first: Option<run_ports::Discovery>,
    /// What two commands run at once came to.
    both: Vec<crate::tests::Ran>,
    /// The engine a registration scenario's session ran on, and where its
    /// stub recorded what it was handed.
    registered: Option<(String, PathBuf)>,
    /// What an engine's call of a ctxloom tool was answered.
    tool: Option<Value>,
    /// Each elicitation the server asked of the last tool call (step 10c
    /// U11), and whether the engine accepts the next ones.
    elicited: Vec<Value>,
    declines: bool,
    /// The daemon a tool's call found before it was killed.
    before_kill: Option<run_ports::Discovery>,
    /// A second project's directory (U5: two sessions, two projects).
    second: Option<PathBuf>,
    /// The daemon's round period, in milliseconds, when a step set one.
    poll_ms: Option<u64>,
    /// A stand-in `opencode`, when a scenario hosts a child on it.
    opencode_stub: Option<PathBuf>,
    /// Processes that hold sessions' locks until the scenario ends.
    holders: Vec<Child>,
    /// The daemon a step killed outright, by its pid.
    killed_pid: Option<u32>,
    /// Where the TUI stub records itself, when not on the scenario's PATH.
    tui_dir: Option<PathBuf>,
    /// The hook the session's assistant fired last.
    fired: Option<Fired>,
    /// The daemon that hosted the scenario's child was killed already (U6:
    /// a re-host budget's scenario plants its past re-hosts with no daemon).
    killed: bool,
    /// What the child's session settings said before its agent edited them.
    settings: Option<String>,
    /// The hooks the session's assistant fired across its daemon's kill.
    hooks: Vec<crate::tests::Ran>,
    /// No daemon can start while the session's assistant starts (OD-59).
    start_blocked: bool,
    /// The home config's `daemon.drain_timeout`, as a step said it
    /// (shutdown.feature): every home config the steps write carries it.
    drain_timeout: Option<String>,
    /// The stub claude, once one is set: the one the home config names,
    /// or, when [`State::stub_on_path`], the one the default `claude` is.
    stub: Option<PathBuf>,
    /// The stub is reached on the scenario's PATH as the default command
    /// `claude`, and no config names it ([`project_says`]: an untrusted
    /// read reports claude-code's command as the default's, trust.feature).
    stub_on_path: bool,
    /// What the interactive session's command came to, once it ended.
    session_ran: Option<crate::tests::Ran>,
    /// What the claude statusline printed when it was run by a step.
    statusline: Option<String>,
    /// A foreign writer killed after one of its steps (step 10c U10): its
    /// command, and the file its run must leave as it says.
    foreign_run: Option<(String, Leaves)>,
    /// A command a step started and left running (`I start ...`), for a
    /// later step to wait for.
    started: Option<Child>,
    /// What the project's own config says besides its agent (trust.feature:
    /// `<dotted key>: <value>`), kept when a step rewrites that config.
    project_says: Vec<String>,
    /// The turns a hosted child had taken when its Given was done (step 8
    /// U2b): a later step's turns are those after them.
    turns_before: Option<usize>,
    /// The project's config defines no agent: the home's is the one a
    /// launch uses (session_approvals.feature's allow-always).
    no_project_agents: bool,
    /// The asks the scenario's scripted claude made (step 8 U4): each
    /// through `ctxloom mcp serve`, as claude calls its approval prompt.
    asks: Vec<Ask>,
    /// The engine side of an MCP server a step talks to as claude does.
    engine: Option<crate::tests::mcp_serve::Mcp>,
    /// What a step's discovery probe was answered.
    discovered: Option<Value>,
    /// The arguments a step scripted the stub claude's ask with (step 8 U6).
    scripted: Option<Value>,
    /// How many daemons the log said served when a step said none did.
    logged_before: Option<usize>,
    /// What holds the daemon's single-instance lock, so no daemon can start,
    /// and the file that lets it go.
    blocker: Option<(Child, PathBuf)>,
    /// The harp of the interactive session a run.feature step started.
    tui: Option<String>,
    /// The project's config defines agents of its own (step 9's journeys):
    /// the stub claude is named in the home config alone, the project's
    /// config left as the step wrote it.
    own_agents: bool,
    /// The session the scenario's orchestrator is (step 9: Alice's).
    orchestrator: Option<String>,
    /// The turns each hosted child had taken when its Given was done (step
    /// 9: several children at once), by harp.
    child_turns_before: std::collections::BTreeMap<String, usize>,
    /// The child the last tool call named.
    tool_child: Option<String>,
    /// The part of a listed tool a step read (its input schema, or its
    /// description as a JSON string): the coordination contract.
    read_tool: Option<Value>,
    /// The children a step had write a file at once, and the file's name.
    writers: Vec<String>,
    written: Option<String>,
    /// The terminal wake: what stands in for the orchestrator's engine's
    /// messaging socket (a listener that records each post), and the file
    /// it records them in.
    wake_socket: Option<(Child, PathBuf)>,
}

/// An ask the scripted claude made: its server, the call's id, and the
/// tool and input it asked with.
pub(super) struct Ask {
    mcp: crate::tests::mcp_serve::Mcp,
    id: u64,
    tool: String,
    input: Value,
    /// What the server answered the call, once read.
    answered: Option<Value>,
}

/// A hook the session's assistant fired (s4-Q14, H2): what it came to, how
/// long it took, its session's events before it, and what the same hook
/// answers the engine when it records nothing.
pub(super) struct Fired {
    ran: crate::tests::Ran,
    took: Duration,
    harp: String,
    events_before: usize,
    unrecorded_answer: String,
}

impl Drop for State {
    fn drop(&mut self) {
        if let Some(mut holder) = self.holder.take() {
            let _ = holder.kill();
            let _ = holder.wait();
        }
        if let Some(release) = &self.release {
            files::put(release, "go");
        }
        if let Some((mut blocker, unblock)) = self.blocker.take() {
            files::put(&unblock, "go");
            let _ = blocker.kill();
            let _ = blocker.wait();
        }
        if let Some(mut run) = self.run.take() {
            let deadline = Instant::now() + Duration::from_secs(10);
            while Instant::now() < deadline && matches!(run.try_wait(), Ok(None)) {
                std::thread::sleep(Duration::from_millis(50));
            }
            let _ = run.kill();
            let _ = run.wait();
        }
        if let Some(mut started) = self.started.take() {
            let _ = started.kill();
            let _ = started.wait();
        }
        if let Some((mut socket, _)) = self.wake_socket.take() {
            let _ = socket.kill();
            let _ = socket.wait();
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

pub(super) fn daemon_log_text(w: &World) -> String {
    files::read(w.common.project.home().join(".ctxloom/daemon/daemon.log")).unwrap_or_default()
}

/// The scenario's environment, as `(name, value)` pairs.
pub(super) fn env_of(w: &World) -> Vec<(String, String)> {
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
    status.json()["daemons"][0].get("pid").is_some()
}

/// Start `ctxloom daemon serve` with the scenario's short idle time, and
/// wait for it to answer.
fn start_daemon(w: &mut World) {
    start_daemon_idling(w, IDLE_SECONDS);
}

/// Start `ctxloom daemon serve` with the idle time `idle` (seconds), and
/// wait for it to answer.
pub(super) fn start_daemon_idling(w: &mut World, idle: u64) {
    start_daemon_serving(w, idle, &[]);
}

/// Start `ctxloom daemon serve` with the idle time `idle` (seconds) and
/// `more` of its (hidden) options, and wait for it to answer.
fn start_daemon_serving(w: &mut World, idle: u64, more: &[&str]) {
    let env = env_of(w);
    let env: Vec<(&str, &str)> = env.iter().map(|(k, v)| (k.as_str(), v.as_str())).collect();
    let idle = idle.to_string();
    let poll = w.daemon_spec.poll_ms.map(|ms| ms.to_string());
    let mut args = vec!["daemon", "serve", "--idle-seconds", &idle];
    if let Some(poll) = &poll {
        args.extend(["--poll-ms", poll]);
    }
    args.extend_from_slice(more);
    let daemon = w.common.project.start_with(&env, &args);
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

/// [`PROJECT_CONFIG`]'s agent over what the project config already says
/// (a Given's `engines.claude-code.approval_timeout`, say), so the stub's
/// agent never takes a setting a step wrote away; none said, none but the
/// schema, or agents of its own, [`PROJECT_CONFIG`] itself.
fn project_config_over(said: Option<String>) -> String {
    let said = said.unwrap_or_default();
    let own = said
        .lines()
        .filter(|line| !line.starts_with("schema_version:"))
        .any(|line| !line.trim().is_empty());
    // One that names agents of its own is the stub's agent alone, as ever.
    let agents = said.lines().any(|line| line.starts_with("agents:"));
    if !own || agents {
        return PROJECT_CONFIG.to_string();
    }
    let agent = PROJECT_CONFIG.split_once('\n').map_or("", |(_, rest)| rest);
    format!("{said}{agent}")
}

/// Point claude-code at the stub `body` in the scenario's home (it runs
/// with `$here` its own directory); the stub's directory.
fn stub_claude(w: &mut World, body: &str) -> PathBuf {
    let dir = w.common.project.home().join("fake-claude");
    files::put_program(dir.join("claude"), body);
    write_project_config(w);
    w.daemon_spec.stub = Some(dir.join("claude"));
    w.daemon_spec.stub_on_path = false;
    write_home_config(w);
    dir
}

/// The project's config as the stub's agent and what the project says make
/// it (not written when the project defines its own agents).
fn write_project_config(w: &mut World) {
    let mut entries = vec![
        "agents.default.engine: claude-code".to_string(),
        "default_agent: default".to_string(),
    ];
    entries.extend(w.daemon_spec.project_says.iter().cloned());
    if w.daemon_spec.no_project_agents {
        entries.retain(|entry| !entry.starts_with("agents."));
    }
    // A Given that set what the project says ([`project_says`]) is its
    // config; otherwise the stub's agent over what the config already says.
    let config = match w.daemon_spec.project_says.is_empty() {
        true => project_config_over(w.common.project.read(".ctxloom/config.yaml")),
        false => config_of(&entries),
    };
    // A project that defines its own agents keeps its config as written.
    if !w.daemon_spec.own_agents {
        w.common.project.write(".ctxloom/config.yaml", &config);
    }
}

/// The daemon the scenario starts looks at the store every `ms`: a change
/// reaches a watch only by being pushed.
pub(super) fn set_round_period(w: &mut World, ms: u64) {
    w.daemon_spec.poll_ms = Some(ms);
}

/// A session `harp`, ended long ago and kept by nothing, in this project.
pub(super) fn ended_session(w: &mut World, harp: &str) {
    hosting_daemon(w);
    let seeded = run_now(
        w,
        &[
            "test",
            "seed-session",
            harp,
            "--lock",
            "free",
            "--transcript",
            "--started-ago",
            "6048000",
            "--ended-ago",
            "5184000",
        ],
    );
    assert_eq!(seeded.code, 0, "{}", seeded.said());
    let listed = run_now(
        w,
        &["session", "list", "--state", "ended", "--format", "json"],
    );
    assert!(
        listed.stdout.contains(harp),
        "the ended session is not listed as ended: {}",
        listed.said()
    );
}

/// The harp of the project `name` beside this one, as `projects/<harp>`
/// names it.
pub(super) fn beside_name(w: &World, name: &str) -> String {
    let ran = run_in_beside(w, name, &["test", "project-name"]);
    assert_eq!(ran.code, 0, "{}", ran.said());
    ran.stdout.trim().to_string()
}

/// The project `name` beside this one, with a session `harp` that is
/// running there: its lock held until the scenario ends.
pub(super) fn second_project_running(w: &mut World, name: &str, harp: &str) {
    second_project_beside(w, name.to_string());
    hosting_daemon(w);
    let seeded = run_in_beside(w, name, &["test", "seed-session", harp, "--lock", "free"]);
    assert_eq!(seeded.code, 0, "{}", seeded.said());
    let dir = w.common.project.home().join(format!("lock-{harp}"));
    let lock = beside(w, name).join(format!(".ctxloom/sessions/{harp}/session.lock"));
    let holder = dir.join("holds-lock");
    files::put_program(&holder, &holds_lock());
    let release = dir.join("release");
    let assistant = std::process::Command::new(&holder)
        .args([lock.as_os_str(), release.as_os_str()])
        .stdin(std::process::Stdio::null())
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::null())
        .spawn()
        .expect("her assistant starts");
    let _ = release;
    w.daemon_spec.holders.push(assistant);
    // Live to her daemon's reads before a scenario asks.
    until(w, "the second project's session live", DAEMON_WAIT, |w| {
        let ran = run_in_beside(w, name, &["session", "list", "--format", "json"]);
        (ran.code == 0 && ran.stdout.contains(harp) && ran.stdout.contains("LIVE")).then_some(())
    });
}

/// Alice sets ctxloom up in `dir` and a session `harp` of hers is running
/// there: its lock held until the scenario ends.
pub(super) fn set_up_and_running(w: &mut World, dir: &Path, harp: &str) {
    let git = std::process::Command::new("git")
        .args(["init", "-q"])
        .current_dir(dir)
        .status()
        .expect("git runs");
    assert!(git.success(), "the directory is a repository");
    let env = env_of(w);
    let env = env_pairs(&env);
    let made = w.common.project.run_in_with(dir, &env, &["init"]);
    assert_eq!(made.code, 0, "{}", made.said());
    let seeded =
        w.common
            .project
            .run_in_with(dir, &env, &["test", "seed-session", harp, "--lock", "free"]);
    assert_eq!(seeded.code, 0, "{}", seeded.said());
    let held = w.common.project.home().join(format!("lock-{harp}"));
    let lock = dir.join(format!(".ctxloom/sessions/{harp}/session.lock"));
    let holder = held.join("holds-lock");
    files::put_program(&holder, &holds_lock());
    let assistant = std::process::Command::new(&holder)
        .args([lock.as_os_str(), held.join("release").as_os_str()])
        .stdin(std::process::Stdio::null())
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::null())
        .spawn()
        .expect("her assistant starts");
    w.daemon_spec.holders.push(assistant);
}

/// A daemon serving, draining, and hosting `child`, which does not end its
/// turn.
pub(super) fn serving_draining(w: &mut World, child: String) {
    serving_hosting_deaf(w, child);
    let drained = run_now(w, &["daemon", "stop", "--drain"]);
    assert_eq!(drained.code, 0, "{}", drained.said());
    until(w, "the daemon draining", DAEMON_WAIT, |w| {
        let status = run_now(w, &["daemon", "status", "--format", "json"]);
        (status.json()["daemons"][0]["draining"] == true).then_some(())
    });
}

/// The daemon the scenario started is killed outright.
pub(super) fn kill_daemon(w: &mut World) {
    if let Some(daemon) = w.daemon_spec.daemon.as_mut() {
        let _ = daemon.kill();
    }
    daemon_gone(w);
}

/// The daemon the scenario started has drained and exited.
pub(super) fn drained_daemon_gone(w: &mut World) {
    exit_of_daemon(w);
    w.daemon_spec.daemon = None;
}

/// An ask waits under the harness's owner: the child's engine asks to run
/// `make test` when none does.
pub(super) fn ensure_an_ask(w: &mut World, child: &str) {
    if waiting_asks(w, Some("amber-quiet-heron")).is_empty() {
        ask(w, child, "Bash", "make test");
    }
}

/// Whether a step of the scenario started a daemon.
pub(super) fn started_a_daemon(w: &World) -> bool {
    w.daemon_spec.daemon.is_some()
}

/// Nothing has started a daemon in this scenario's home.
pub(super) fn nothing_started(w: &World) {
    let dir = w.common.project.home().join(".ctxloom/daemon");
    assert!(
        !files::exists(discovery_path(w)),
        "a daemon advertised itself"
    );
    assert!(!files::exists(dir.join("daemon.log")), "a daemon logged");
}

/// A daemon serves for this home, hosting nothing.
pub(super) fn serve_again(w: &mut World) {
    w.common
        .set_env(IDLE_VARIABLE, SPAWNED_IDLE_SECONDS.to_string());
    start_daemon_idling(w, DEFAULT_IDLE_SECONDS);
}

/// The session `owner`, seeded, is live: a process holds its lock until the
/// scenario ends, as her assistant on her terminal does. Done once; nothing
/// when no such session was made.
pub(super) fn hold_owner_live(w: &mut World, owner: &str) {
    let session_dir = w
        .common
        .project
        .dir()
        .join(format!(".ctxloom/sessions/{owner}"));
    // Only where a daemon the scenario started serves: asking another one
    // would start it (and a draining or killed one answers nothing).
    if w.daemon_spec.run.is_some() || w.daemon_spec.daemon.is_none() || w.daemon_spec.killed {
        return;
    }
    let ran = run_now(w, &["session", "list", "--format", "json"]);
    if ran.code != 0 || listed_state(w, owner).is_none() {
        return;
    }
    files::mkdirs(&session_dir);
    let dir = w.common.project.home().join("owner-lock");
    let lock = session_dir.join("session.lock");
    let release = dir.join("release");
    let holder = dir.join("holds-lock");
    files::put_program(&holder, &holds_lock());
    let assistant = std::process::Command::new(&holder)
        .args([lock.as_os_str(), release.as_os_str()])
        .stdin(std::process::Stdio::null())
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::null())
        .spawn()
        .expect("her assistant starts");
    w.daemon_spec.run = Some(assistant);
    w.daemon_spec.release = Some(release);
    until(w, "her session's lock held", DAEMON_WAIT, |w| {
        listed_state(w, owner).filter(|s| s == "LIVE")
    });
}

/// `opencode acp`, as an ACP agent: it answers the client's opening, takes
/// each prompt with one reply and ends the turn, and exits when its input
/// ends. (A hosted opencode child is this one long-lived process.)
const OPENCODE_CHILD_STUB: &str = r#"exec python3 -I -c '
import json, sys

def send(message):
    sys.stdout.write(json.dumps(message) + "\n")
    sys.stdout.flush()

for line in sys.stdin:
    try:
        message = json.loads(line)
    except ValueError:
        continue
    method, ident = message.get("method"), message.get("id")
    if method == "initialize":
        send({"jsonrpc": "2.0", "id": ident, "result": {"protocolVersion": 1, "agentCapabilities": {}}})
    elif method == "session/new":
        send({"jsonrpc": "2.0", "id": ident, "result": {"sessionId": "ses_stub"}})
    elif method == "session/prompt":
        send({"jsonrpc": "2.0", "method": "session/update", "params": {"sessionId": "ses_stub", "update": {"sessionUpdate": "agent_message_chunk", "content": {"type": "text", "text": "ok"}}}})
        send({"jsonrpc": "2.0", "id": ident, "result": {"stopReason": "end_turn"}})
    elif ident is not None:
        send({"jsonrpc": "2.0", "id": ident, "result": {}})
'
"#;

/// A child of `owner` launched on `agent`, which the project says is on
/// claude-code; its first turn ended and told. The project's config is made
/// again, so a later child names an agent the daemon reads at its launch.
pub(super) fn child_on_agent(
    w: &mut World,
    child: &str,
    owner: &str,
    agent: &str,
    profile: Option<(&str, &str)>,
    engine: &str,
) {
    // A profile of the project whose loadout loads `bundle`, the agent's.
    if let Some((profile, bundle)) = profile {
        bundle_profile(w, bundle, profile, "fragments/rules.md", "the rules");
        let said = format!("agents.{agent}.profiles: [{profile}]");
        if !w.daemon_spec.project_says.contains(&said) {
            w.daemon_spec.project_says.push(said);
        }
    }
    if engine == "opencode" && w.daemon_spec.opencode_stub.is_none() {
        // `opencode run` answers one turn and exits; the daemon keeps the
        // child hosted between turns.
        let stub = w
            .common
            .project
            .home()
            .join("fake-opencode")
            .join("opencode");
        files::put_program(&stub, OPENCODE_CHILD_STUB);
        w.daemon_spec.opencode_stub = Some(stub);
        write_home_config(w);
    }
    let said = format!("agents.{agent}.engine: {engine}");
    if !w.daemon_spec.project_says.contains(&said) {
        w.daemon_spec.project_says.push(said);
    }
    // The harness's own child, before any daemon is started, is hosted on
    // the project's default agent: so the agent asked for is the default.
    if w.daemon_spec.daemon.is_none()
        && (child, owner) == ("brisk-copper-moth", "amber-quiet-heron")
    {
        w.daemon_spec
            .project_says
            .push(format!("default_agent: {agent}"));
        return idle_child(w);
    }
    if w.daemon_spec.stub.is_some() {
        write_project_config(w);
    }
    // A child of a hosted child: its first turn is told through its owner's
    // own conversation, so it is the run's record that says it ended.
    if owner != "amber-quiet-heron" {
        hosting_daemon(w);
        grandchild_of(w, owner, child, Some(agent));
        return;
    }
    user_launches(w, owner, child, Some(agent));
    first_turn_told(w, owner, child);
}

/// The home config the steps write: the stub claude's (when one is set),
/// and the drain timeout a step said (shutdown.feature).
fn write_home_config(w: &World) {
    let mut config = "schema_version: 9\neditor:\n  command: \"true\"\n".to_string();
    // The agents a Given wrote into the home config are kept (step 8 U4:
    // the layer an allow-always writes into).
    config.push_str(&agents_block(
        &w.common
            .project
            .read_home(".ctxloom/config.yaml")
            .unwrap_or_default(),
    ));
    if let Some(stub) = w
        .daemon_spec
        .stub
        .as_ref()
        .filter(|_| !w.daemon_spec.stub_on_path)
    {
        config.push_str(&format!(
            "engines:\n  claude-code:\n    command: {}\n",
            stub.display()
        ));
        if let Some(opencode) = &w.daemon_spec.opencode_stub {
            config.push_str(&format!(
                "  opencode:\n    command: {}\n",
                opencode.display()
            ));
        }
    }
    if let Some(timeout) = &w.daemon_spec.drain_timeout {
        config.push_str(&format!("daemon:\n  drain_timeout: {timeout}\n"));
    }
    w.common.project.write_home(".ctxloom/config.yaml", &config);
}

/// The `agents:` block of the config `text`, its lines up to the next
/// top-level key; none, empty.
fn agents_block(text: &str) -> String {
    let mut block = String::new();
    let mut inside = false;
    for line in text.lines() {
        let top = !line.starts_with(' ') && !line.is_empty();
        if top {
            inside = line.starts_with("agents:");
        }
        if inside {
            block.push_str(line);
            block.push('\n');
        }
    }
    block
}

/// The interactive session's stub: it says it started, then runs until
/// released (bounded, so a lost scenario ends anyway).
const INTERACTIVE_STUB: &str = r#"here=$(dirname "$0")
: > "$here/started"
i=0
while [ ! -e "$here/release" ] && [ "$i" -lt 600 ]; do sleep 0.1; i=$((i+1)); done
"#;

/// An interactive session the CLI runs (`ctxloom run` at a terminal, the
/// test's stand-in for one), started and not ended: prepared by the daemon
/// serving (U5), its run in the one store, its session's lock held by the
/// command.
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

/// A hosted child's stub (engine-claude's streaming child): its
/// environment written once, an init, then for each message given a line
/// of `turns` (its conversation's id, then the message) and a result; a
/// turn begun while `hold` is there is left in flight until the interrupt
/// (`interrupted`), which ends it aborted (unless `deaf` is there: then the
/// interrupt is ignored, and the turn stays in flight); `/clear` starts a new
/// conversation (a new id), as claude's own does. It ends when its input
/// does. Its model asks (step 8 U6) when a step scripts it, the arguments of
/// the ask in `ask` beside it ([`script_ask`]): a turn begun with one there
/// asks at once, and a turn in flight asks once one is put there; either
/// asks through ctxloom's approval prompt as claude does
/// (`daemon::PROMPT_CLIENT`), then ends saying what came of it.
fn child_stub() -> String {
    [
        CHILD_STUB_HEAD,
        crate::tests::daemon::PROMPT_CLIENT,
        CHILD_STUB_TURNS,
    ]
    .concat()
}

const CHILD_STUB_HEAD: &str = r#"here=$(dirname "$0")
pwd >> "$here/child-cwds"
printf '%s\n' "$*" >> "$here/child-args"
env > "$here/child-env"
me="${CTXLOOM_SESSION##*/}"
rec="$here/by/${me:-none}"
mkdir -p "$rec"
pwd >> "$rec/child-cwds"
printf '%s\n' "$*" >> "$rec/child-args"
env > "$rec/child-env"
"#;

const CHILD_STUB_TURNS: &str = r#"
n=1
sid=S1
busy=
watcher=
over="$here/asked.$$"
# claim: the scripted ask taken, if there is one (one taker).
claim() { mv "$here/ask" "$here/asking.$$" 2>/dev/null; }
printf '{"type":"system","subtype":"init","cwd":"/work","session_id":"%s","tools":[]}\n' "$sid"
while IFS= read -r line; do
  case "$line" in
    *'"control_request"'*)
      [ -e "$here/deaf" ] && continue
      : > "$here/interrupted"
      printf '{"type":"control_response","response":{"subtype":"success"}}\n'
      if [ -n "$busy" ]; then
        busy=
        [ -n "$watcher" ] && kill "$watcher" 2>/dev/null
        watcher=
        [ -e "$over" ] || printf '{"type":"result","subtype":"error_during_execution","is_error":false,"terminal_reason":"aborted_streaming","result":"","session_id":"%s"}\n' "$sid"
        rm -f "$over"
      fi
      continue ;;
    *'"/clear"'*)
      n=$((n+1))
      sid="S$n"
      printf '{"type":"conversation_reset","session_id":"%s"}\n' "$sid"
      printf '{"type":"result","subtype":"success","is_error":false,"result":"","session_id":"%s"}\n' "$sid"
      continue ;;
  esac
  # The prompt submitted, echoed as claude echoes every user message it
  # submits (`--replay-user-messages`: the recorded streams in
  # engine-claude's fixtures, a first turn's too), before its turn's work.
  printf '%s\n' "$line" | sed 's/}[[:space:]]*$/,"session_id":"'"$sid"'","isReplay":true}/'
  printf '%s %s\n' "$sid" "$line" >> "$here/turns"
  printf '%s %s\n' "$sid" "$line" >> "$rec/turns"
  : > "$here/child-turn"
  : > "$rec/child-turn"
  # Its hooks, as claude fires them, from the payloads a step left.
  if [ -d "$rec/fire" ]; then
    for f in "$rec/fire"/*; do
      ev=${f##*/}; ev=${ev#*-}; ev=${ev%.json}
      ctxloom hook claude-code execute "$ev" < "$f" > /dev/null 2>> "$rec/hook-errors"
    done
    rm -rf "$rec/fire"
  fi
  if [ -e "$rec/rate-limit" ]; then
    rm -f "$rec/rate-limit"
    printf '{"type":"result","subtype":"success","is_error":true,"api_error_status":429,"result":"API Error: 429 rate limited","session_id":"%s"}\n' "$sid"
    continue
  fi
  # A file written in the turn, then held until every writer has written
  # (the turns run at the same time).
  if [ -e "$rec/write" ]; then
    name=$(cat "$rec/write")
    rm -f "$rec/write"
    printf '%s\n' "$me" > "$name"
    : > "$rec/wrote"
    i=0
    while [ "$(ls "$here"/by/*/wrote 2> /dev/null | wc -l)" -lt "$(cat "$here/together" 2> /dev/null || echo 1)" ] && [ "$i" -lt 600 ]; do
      sleep 0.1
      i=$((i+1))
    done
  fi
  if claim; then
    asks "$here/asking.$$"
    continue
  fi
  if [ -e "$here/hold" ] || [ -e "$rec/hold" ]; then
    rm -f "$here/hold" "$rec/hold"
    busy=1
    rm -f "$over"
    (
      i=0
      while [ "$i" -lt 600 ]; do
        if claim; then
          asks "$here/asking.$$"
          : > "$over"
          exit 0
        fi
        sleep 0.1
        i=$((i+1))
      done
    ) &
    watcher=$!
    continue
  fi
  answer=ok
  [ -e "$rec/answer" ] && answer=$(cat "$rec/answer")
  printf '{"type":"result","subtype":"success","is_error":false,"result":"%s","session_id":"%s"}\n' "$answer" "$sid"
done
[ -n "$watcher" ] && kill "$watcher" 2>/dev/null
: > "$here/child-exited"
: > "$rec/child-exited"
"#;

/// The stub child's directory.
fn stub_dir(w: &World) -> PathBuf {
    w.common.project.home().join("fake-claude")
}

/// The lines of `turns` the stub child wrote: `(conversation, message)`.
fn turns(w: &World) -> Vec<(String, String)> {
    files::read(stub_dir(w).join("turns"))
        .unwrap_or_default()
        .lines()
        .filter_map(|line| line.split_once(' '))
        .map(|(sid, rest)| (sid.to_string(), rest.to_string()))
        .collect()
}

/// Wait, at most `limit`, for `ready` to answer.
fn until<T>(
    w: &World,
    what: &str,
    limit: Duration,
    mut ready: impl FnMut(&World) -> Option<T>,
) -> T {
    let deadline = Instant::now() + limit;
    loop {
        if let Some(found) = ready(w) {
            return found;
        }
        assert!(
            Instant::now() < deadline,
            "timed out waiting for {what}; the daemon's log:\n{}",
            daemon_log_text(w)
        );
        std::thread::sleep(Duration::from_millis(50));
    }
}

/// How long a scenario waits for what its daemon does.
const DAEMON_WAIT: Duration = Duration::from_secs(30);

/// The turn whose message holds `text`, once the stub has taken one.
fn turn_on(w: &World, text: &str) -> (String, String) {
    until(w, &format!("a turn on {text:?}"), DAEMON_WAIT, |w| {
        turns(w).into_iter().find(|(_, line)| line.contains(text))
    })
}

/// The session `harp` as `session show` has it.
fn shown(w: &World, harp: &str) -> Value {
    let ran = run_now(w, &["session", "show", harp, "--format", "json"]);
    assert_eq!(ran.code, 0, "{}", ran.said());
    ran.json()
}

/// The daemon's hand-off directory of the scenario's home.
fn launch_dir(w: &World) -> PathBuf {
    daemon_dir(w).join("launch")
}

/// The hand-off files there are.
fn hand_offs(w: &World) -> Vec<PathBuf> {
    files::children(launch_dir(w))
}

/// The child stub's claude and a daemon this scenario owns (it hosts the
/// children for the scenario's life, so its default idle time), for a
/// scenario whose own command launches its first child; every daemon a
/// command spawns idles out soon.
fn hosting_daemon(w: &mut World) {
    if w.daemon_spec.daemon.is_some() {
        return;
    }
    stub_claude(w, &child_stub());
    w.common
        .set_env(IDLE_VARIABLE, SPAWNED_IDLE_SECONDS.to_string());
    start_daemon_idling(w, DEFAULT_IDLE_SECONDS);
}

/// Before a scenario's command: one that launches a child is given the
/// stub child and a daemon the scenario owns, when its steps set up none
/// (no engine on the host's `PATH` is ever started).
pub(super) fn before_line(w: &mut World, line: &str) {
    let line = line.trim_start();
    if line.starts_with("ctxloom session launch") {
        hosting_daemon(w);
    } else if line.starts_with("ctxloom run --one-shot") && !line.contains("--engine") && bare(w) {
        // A one-shot on a project whose steps named no engine runs claude:
        // the one-shot stub, never the host's.
        stub_claude(w, &one_shot_stub());
        w.common
            .set_env(IDLE_VARIABLE, SPAWNED_IDLE_SECONDS.to_string());
    }
}

/// Whether nothing the scenario set up says how its engine is run: no stub
/// is named, its project names no engine and its home config no engine
/// block, so a launch would start the host's own claude.
fn bare(w: &World) -> bool {
    let project = w
        .common
        .project
        .read(".ctxloom/config.yaml")
        .unwrap_or_default();
    let home = w
        .common
        .project
        .read_home(".ctxloom/config.yaml")
        .unwrap_or_default();
    w.daemon_spec.stub.is_none() && !project.contains("engine:") && !home.contains("engines:")
}

/// A one-shot's stub turn (engine-claude's `claude -p`): its arguments, one
/// a line, in `one-shot-args` and its environment in `one-shot-env`; its
/// prompt read from its input; then its reply or, when a step scripted an
/// ask ([`script_ask`]), the ask through ctxloom's approval prompt as claude
/// asks (`daemon::PROMPT_CLIENT`) and what came of it.
fn one_shot_stub() -> String {
    [
        ONE_SHOT_HEAD,
        crate::tests::daemon::PROMPT_CLIENT,
        ONE_SHOT_TURN,
    ]
    .concat()
}

const ONE_SHOT_HEAD: &str = r#"here=$(dirname "$0")
printf '%s\n' "$@" > "$here/one-shot-args"
env > "$here/one-shot-env"
sid=9d764b45-a56a-48d7-ac9d-c2a16c5669f1
"#;

const ONE_SHOT_TURN: &str = r#"
cat > /dev/null
printf '{"type":"system","subtype":"init","session_id":"%s"}\n' "$sid"
if mv "$here/ask" "$here/asking.$$" 2>/dev/null; then
  asks "$here/asking.$$"
else
  printf '{"type":"result","subtype":"success","is_error":false,"result":"ONE-SHOT-REPLY","session_id":"%s"}\n' "$sid"
fi
"#;

/// A child the daemon hosts: launched by the user for a seeded owner
/// (`session launch`), and seen to take its first turn.
fn hosted_child(w: &mut World) {
    let dir = stub_claude(w, &child_stub());
    let seeded = run_now(w, &["test", "seed-session", "amber-quiet-heron"]);
    assert_eq!(seeded.code, 0, "{}", seeded.said());
    let mut env = env_of(w);
    env.push(("ANTHROPIC_API_KEY".to_string(), "a-key".to_string())); // gitleaks:allow
    let env: Vec<(&str, &str)> = env.iter().map(|(k, v)| (k.as_str(), v.as_str())).collect();
    let launched = w.common.project.run_with(
        &env,
        &[
            "session",
            "launch",
            "amber-quiet-heron",
            "--name",
            "brisk-copper-moth",
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
    conversation_bound(w);
}

/// The hosted child's conversation is bound: its first turn's `init` was
/// heard by the daemon and recorded as the session's native conversation.
/// A scenario that kills the daemon "in the middle of a turn" and expects
/// the child re-hosted in its own conversation must wait for this first: the
/// stub's `child-turn` file says the child printed its `init`, not that the
/// daemon has heard and recorded it, and a daemon killed in between leaves a
/// child with no conversation to resume, which is not re-hosted
/// (`session_policies::plan`'s `rehosts`; CI on macOS, 2026-11-19 and
/// 2026-11-20: "timed out waiting for the child started again").
fn conversation_bound(w: &World) {
    until(w, "the child's conversation bound", DAEMON_WAIT, |w| {
        shown(w, CHILD)["native_session"]
            .as_str()
            .is_some_and(|native| !native.is_empty())
            .then_some(())
    });
}

/// A hosted child whose first turn has ended: its owner was told so.
pub(super) fn idle_child(w: &mut World) {
    w.common
        .set_env(IDLE_VARIABLE, SPAWNED_IDLE_SECONDS.to_string());
    start_daemon_idling(w, DEFAULT_IDLE_SECONDS);
    hosted_child(w);
    first_turn_ended(w);
}

/// The hosted child's first turn has ended: its owner was told so.
fn first_turn_ended(w: &World) {
    until(w, "the child's first turn to end", DAEMON_WAIT, |w| {
        let owner = shown(w, "amber-quiet-heron");
        owner["pending"]
            .as_array()
            .is_some_and(|pending| {
                pending.iter().any(|m| {
                    m["text"]
                        .as_str()
                        .is_some_and(|t| t.contains("finished its turn"))
                })
            })
            .then_some(())
    });
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
    let text = step.value.as_str();
    if text.contains("which her daemon runs") {
        hosted_one_shot(w, step);
        return true;
    }
    if text.contains("presses Ctrl-C while it works") {
        stopped_one_shot(w, step, "INT");
        return true;
    }
    if let Some(signal) = sent_signal(text) {
        stopped_one_shot(w, step, signal);
        return true;
    }
    if text.contains("through her daemon") {
        tui_session(w, step, false);
        return true;
    }
    if text.contains("in a worktree, and its daemon is killed outright while it works") {
        w.git_backed();
        tui_session(w, step, true);
        return true;
    }
    if text.contains("its daemon is killed outright while it works") {
        tui_session(w, step, true);
        return true;
    }
    if text.contains("its daemon is killed outright before the assistant starts") {
        tui_gated(w, step, false);
        return true;
    }
    if text == "Alice runs her assistant interactively:" && w.daemon_spec.start_blocked {
        tui_gated(w, step, true);
        return true;
    }
    if text.contains("in both projects at once") {
        tui_in_both(w, step);
        return true;
    }
    if text.contains("says yes at her terminal") {
        // Her terminal, stood in for: she says yes when asked (U1).
        w.before_run();
        let mut env = env_of(w);
        env.push(("CTXLOOM_TEST_TERMINAL".to_string(), "1".to_string()));
        // Her login, as a command at her terminal has it: the trust's actor.
        for name in ["USER", "LOGNAME", "USERNAME"] {
            if let Some(value) = crate::tests::host_var(name) {
                env.push((name.to_string(), value.to_string_lossy().into_owned()));
            }
        }
        let words = line_words(step);
        let args: Vec<&str> = words.iter().map(String::as_str).collect();
        w.common.ran = Some(w.common.project.run_input(&args, "yes\n", &env_pairs(&env)));
        return true;
    }
    if text.contains("at her terminal, and answers yes when asked") {
        removes_at_terminal(w, step);
        return true;
    }
    false
}

/// Her terminal, stood in for (`CTXLOOM_TEST_TERMINAL`): the step's
/// command run, and once it asks its `[y/N]` question (read off its
/// standard error) whatever the scenario has to happen between the
/// preview and her answer (`changes_before_answer`), then her yes. The
/// change waits for the question, so it falls between the two, always.
fn removes_at_terminal(w: &mut World, step: &cucumber::gherkin::Step) {
    use std::io::{Read as _, Write as _};
    w.before_run();
    let words = line_words(step);
    let project = &w.common.project;
    let mut command = Command::new(crate::tests::binary());
    command
        .args(&words)
        .env_clear()
        .env("HOME", project.home())
        .env("USERPROFILE", project.home())
        .env("TMPDIR", crate::tests::temp_root())
        .env(crate::tests::LIFELINE, crate::tests::lifeline())
        .env("CTXLOOM_ROOT", project.dir())
        .env("CTXLOOM_TEST_TERMINAL", "1")
        .current_dir(project.dir())
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());
    for name in ["PATH", "SystemRoot"] {
        if let Some(value) = crate::tests::host_var(name) {
            command.env(name, value);
        }
    }
    for (name, value) in env_of(w) {
        command.env(name, value);
    }
    let mut child = command.spawn().expect("ctxloom runs");
    let mut stdout = child.stdout.take().expect("stdout");
    let mut stderr = child.stderr.take().expect("stderr");
    let (asked, asking) = std::sync::mpsc::channel();
    let errors = std::thread::spawn(move || {
        let (mut said, mut byte, mut told) = (Vec::new(), [0_u8], false);
        while stderr.read(&mut byte).is_ok_and(|n| n == 1) {
            said.push(byte[0]);
            if !told && said.ends_with(b"[y/N] ") {
                told = true;
                let _ = asked.send(());
            }
        }
        said
    });
    let outputs = std::thread::spawn(move || {
        let mut said = Vec::new();
        let _ = stdout.read_to_end(&mut said);
        said
    });
    let mut stdin = child.stdin.take().expect("stdin");
    if asking.recv_timeout(Duration::from_secs(60)).is_ok() {
        if let Some(bundle) = w.daemon_spec.changes_before_answer.clone() {
            files::put(
                w.common.project.dir().join(format!(
                    ".ctxloom/content/bundles/v2/{bundle}/fragments/meanwhile.md"
                )),
                "written while Alice read the preview\n",
            );
        }
        let _ = stdin.write_all(b"yes\n");
    }
    drop(stdin);
    let status = child.wait().expect("ctxloom finishes");
    w.common.ran = Some(crate::tests::Ran {
        code: status.code().unwrap_or(-1),
        stdout: String::from_utf8_lossy(&outputs.join().expect("stdout read")).into_owned(),
        stderr: String::from_utf8_lossy(&errors.join().expect("stderr read")).into_owned(),
    });
}

/// The signal a `... ctxloom is sent a <SIG> while the assistant works:`
/// step names, as `kill` names it.
fn sent_signal(text: &str) -> Option<&'static str> {
    let rest = text.split("and ctxloom is sent a ").nth(1)?;
    ["HUP", "INT", "TERM"]
        .into_iter()
        .find(|signal| rest.starts_with(&format!("SIG{signal} ")))
}

/// Where the scenario's stub assistant is, and writes what it saw: the
/// directory on the scenario's PATH ("Alice's assistant is Claude Code").
fn assistant_dir(w: &World) -> PathBuf {
    w.adopt_scratch().join("bin")
}

/// What the stub assistant of a hosted one-shot records of itself before
/// it answers or works: its parent (the process that started it), and
/// whether its session's lock was held by another process while it ran
/// (`held` or `free`).
fn records_itself() -> String {
    let free = procs::lock_free_condition(r#""$lock""#);
    format!(
        r#"here="$(dirname "$0")"
echo "$PPID" > "$here/ppid"
echo "${{CTXLOOM_SESSION##*/}}" > "$here/watched-harp"
lock="${{CTXLOOM_ROOT}}/.ctxloom/sessions/${{CTXLOOM_SESSION##*/}}/session.lock"
if {free}; then echo free > "$here/lock-probe"; else echo held > "$here/lock-probe"; fi
"#
    )
}

/// A hosted one-shot's stub turn: it records itself, takes its prompt from
/// its input, and answers (engine-claude's stream).
fn answering_stub() -> String {
    format!(
        r#"{records}cat > /dev/null
echo '{{"type":"system","subtype":"init","session_id":"9d764b45-a56a-48d7-ac9d-c2a16c5669f1"}}'
echo '{{"type":"result","subtype":"success","is_error":false,"result":"J001000-CHILD-REPLY","session_id":"9d764b45-a56a-48d7-ac9d-c2a16c5669f1"}}'
"#,
        records = records_itself()
    )
}

/// A hosted one-shot's stub turn that works until it is asked to stop: it
/// records itself and its pid (once it works), and on a SIGTERM says it was
/// asked and exits.
fn working_stub() -> String {
    format!(
        r#"{records}trap 'echo asked > "$here/asked"; exit 0' TERM
echo "$$" > "$here/pid.tmp" && mv "$here/pid.tmp" "$here/pid"
sleep 30 &
wait
"#,
        records = records_itself()
    )
}

/// `w`'s environment as `run_with` takes it.
fn env_pairs(env: &[(String, String)]) -> Vec<(&str, &str)> {
    env.iter().map(|(k, v)| (k.as_str(), v.as_str())).collect()
}

/// The ctxloom line of a step's doc string, as its words.
fn line_words(step: &cucumber::gherkin::Step) -> Vec<String> {
    let lines = crate::tests::gherkin_common::doc(step);
    crate::tests::gherkin_common::split(
        lines
            .trim()
            .strip_prefix("ctxloom ")
            .expect("a ctxloom line"),
    )
}

/// The one-shot of the step, its assistant the answering stub, run to its
/// end: the daemon the command starts hosts it.
fn hosted_one_shot(w: &mut World, step: &cucumber::gherkin::Step) {
    files::put_program(assistant_dir(w).join("claude"), &answering_stub());
    w.common
        .set_env(IDLE_VARIABLE, SPAWNED_IDLE_SECONDS.to_string());
    w.before_run();
    let words = line_words(step);
    let args: Vec<&str> = words.iter().map(String::as_str).collect();
    let env = env_of(w);
    w.common.ran = Some(w.common.project.run_with(&env_pairs(&env), &args));
}

/// The one-shot of the step, its assistant the working stub: once the stub
/// says it works (forced: its pid file, never a wait for a race), the
/// command is sent `signal`, and waited for.
fn stopped_one_shot(w: &mut World, step: &cucumber::gherkin::Step, signal: &str) {
    let dir = assistant_dir(w);
    files::put_program(dir.join("claude"), &working_stub());
    w.common
        .set_env(IDLE_VARIABLE, SPAWNED_IDLE_SECONDS.to_string());
    w.before_run();
    let words = line_words(step);
    let args: Vec<&str> = words.iter().map(String::as_str).collect();
    let env = env_of(w);
    let child = w.common.project.start_with(&env_pairs(&env), &args);
    let deadline = Instant::now() + Duration::from_secs(60);
    while !files::exists(dir.join("pid")) {
        assert!(
            Instant::now() < deadline,
            "the stub never said it works; the daemon's log:\n{}",
            daemon_log_text(w)
        );
        std::thread::sleep(Duration::from_millis(20));
    }
    let sent = std::process::Command::new("kill")
        .args([&format!("-{signal}"), &child.id().to_string()])
        .status()
        .expect("kill runs");
    assert!(sent.success(), "the SIG{signal} was sent");
    w.common.ran = Some(crate::tests::Ran::of(
        child.wait_with_output().expect("ctxloom finishes"),
    ));
}

/// What the stub assistant wrote of itself, by name.
fn recorded(w: &World, name: &str) -> String {
    files::read(assistant_dir(w).join(name))
        .unwrap_or_default()
        .trim()
        .to_string()
}

/// The daemon that logged it serves last, by its pid (a daemon that has
/// idled out since is still the one that ran it).
fn daemon_pid(w: &World) -> u32 {
    let log = daemon_log_text(w);
    let serving = log
        .lines()
        .filter(|line| line.contains(" serving at "))
        .count();
    assert!(serving > 0, "no daemon logged that it served:\n{log}");
    match files::read_bytes(discovery_path(w)) {
        Ok(bytes) => {
            run_ports::Discovery::decode(&bytes)
                .expect("a discovery file it can read")
                .pid
        }
        Err(e) => panic!("no discovery file ({e}); the daemon's log:\n{log}"),
    }
}

/// The stub assistant was started by the daemon (its parent the daemon's
/// pid), not by the command typed.
fn ran_under_the_daemon(w: &World) {
    let parent = recorded(w, "ppid");
    assert!(!parent.is_empty(), "the assistant ran: {}", w.ran().said());
    assert_eq!(
        parent,
        daemon_pid(w).to_string(),
        "the assistant's parent is the daemon; its log:\n{}",
        daemon_log_text(w)
    );
}

/// The stub assistant was asked to stop (its trap ran) and is gone.
fn asked_and_gone(w: &World) {
    assert_eq!(
        recorded(w, "asked"),
        "asked",
        "the assistant was not asked to stop: {}\n{}",
        w.ran().said(),
        daemon_log_text(w)
    );
    let pid = recorded(w, "pid");
    assert!(
        !procs::alive(pid.parse().expect("a pid")),
        "the assistant ({pid}) outlived the command"
    );
    ran_under_the_daemon(w);
}

/// The command exited `code`, saying the run was stopped by `signal`.
fn exited_stopped(w: &World, code: i32, signal: &str) {
    let ran = w.ran();
    assert_eq!(ran.code, code, "{}", ran.said());
    assert!(
        ran.stderr.contains(&format!("stopped by {signal}")),
        "{}",
        ran.said()
    );
}

/// A one-shot the daemon hosts and that works until the scenario ends: its
/// stub released by the World's end.
fn hosted_working_one_shot(w: &mut World) {
    let dir = stub_claude(w, INTERACTIVE_STUB);
    w.daemon_spec.release = Some(dir.join("release"));
    let mut env = env_of(w);
    env.push(("ANTHROPIC_API_KEY".to_string(), "a-key".to_string())); // gitleaks:allow
    w.daemon_spec.run = Some(
        w.common
            .project
            .start_with(&env_pairs(&env), &["run", "--one-shot", "work"]),
    );
    let deadline = Instant::now() + Duration::from_secs(30);
    while !files::exists(dir.join("started")) {
        assert!(
            Instant::now() < deadline,
            "the one-shot's engine never started; the daemon's log:\n{}",
            daemon_log_text(w)
        );
        std::thread::sleep(Duration::from_millis(50));
    }
}

// ------------------------------------------------- the TUI frontend (U5)

/// The interactive session's stub assistant: it records itself (its
/// arguments, its whole environment, its directory, whether its session's
/// lock was held), says it started, and runs until released, then exits
/// with the code `tui-exit` beside it says (none: 0); a SIGTERM is
/// said, never obeyed (the scenario that asks for a stop wants the kill).
/// Its records are named by its project's directory, so two projects'
/// sessions keep their own.
fn tui_stub() -> String {
    format!(
        r#"{records}p="$here/$(basename "$CTXLOOM_ROOT")"
mkdir -p "$p"
printf '%s\n' "$@" > "$p/tui-args"
env -0 > "$p/tui-env"
pwd -P > "$p/tui-cwd"
echo "${{CTXLOOM_SESSION##*/}}" > "$p/tui-harp"
trap 'echo asked > "$p/asked"' TERM
: > "$p/tui-started"
i=0
while [ ! -e "$here/release" ] && [ "$i" -lt 600 ]; do sleep 0.1; i=$((i+1)); done
exit "$(cat "$here/tui-exit" 2>/dev/null || echo 0)"
"#,
        records = records_itself()
    )
}

/// Where the stub assistant recorded itself for `project`'s session.
fn tui_record(w: &World, project: &std::path::Path, name: &str) -> String {
    let dir = project.file_name().expect("a project directory");
    let stub = w
        .daemon_spec
        .tui_dir
        .clone()
        .unwrap_or_else(|| assistant_dir(w));
    files::read(stub.join(dir).join(name)).unwrap_or_default()
}

/// The scenario's environment for a session on a stood-in terminal.
fn tui_env(w: &mut World) -> Vec<(String, String)> {
    w.common
        .set_env(IDLE_VARIABLE, SPAWNED_IDLE_SECONDS.to_string());
    w.common.set_env("CTXLOOM_TEST_TERMINAL", "1".to_string());
    env_of(w)
}

/// Wait, bounded, for `ready`; `what` names it when it never comes.
fn wait_for(w: &World, what: &str, ready: impl Fn() -> bool) {
    let deadline = Instant::now() + Duration::from_secs(60);
    while !ready() {
        assert!(
            Instant::now() < deadline,
            "{what} never came; the daemon's log:\n{}",
            daemon_log_text(w)
        );
        std::thread::sleep(Duration::from_millis(50));
    }
}

/// The session of the step (`ctxloom run` on a stood-in terminal), its
/// assistant the TUI stub. Unless `kill`, the stub is released at once and
/// the command waited for; with `kill`, once the daemon has recorded the
/// TUI's start and let its own run lock go (forced: its run's stream and
/// its lock file, never a wait for a race), the daemon is killed outright,
/// THEN the TUI is released, and the command waited for.
fn tui_session(w: &mut World, step: &cucumber::gherkin::Step, kill: bool) {
    files::put_program(assistant_dir(w).join("claude"), &tui_stub());
    let release = assistant_dir(w).join("release");
    if !kill {
        files::put(&release, "go");
    }
    let env = tui_env(w);
    w.before_run();
    let words = line_words(step);
    let args: Vec<&str> = words.iter().map(String::as_str).collect();
    let child = w.common.project.start_with(&env_pairs(&env), &args);
    if kill {
        let project = w.common.project.dir().to_path_buf();
        wait_for(w, "the TUI's start", || {
            files::exists(
                assistant_dir(w)
                    .join(project.file_name().unwrap())
                    .join("tui-started"),
            )
        });
        let harp = tui_record(w, &project, "tui-harp").trim().to_string();
        wait_for(w, "the daemon's record of the start", || {
            run_events(w, &harp).contains(&"RunnerStartedRecorded".to_string())
                && files::children(w.common.project.home().join(".ctxloom/runs")).is_empty()
        });
        let pid = discovery_now(w).pid;
        let killed = std::process::Command::new("kill")
            .args(["-KILL", &pid.to_string()])
            .status()
            .expect("kill runs");
        assert!(killed.success(), "the daemon was killed");
        wait_for(w, "the killed daemon's end", || !alive(pid));
        w.daemon_spec.killed_pid = Some(pid);
        files::put(&release, "go");
    }
    w.common.ran = Some(crate::tests::Ran::of(
        child.wait_with_output().expect("ctxloom finishes"),
    ));
}

/// Whether the process `pid` is there, and not a zombie (a daemon the
/// command typed spawned is its child until that command exits, and is
/// reaped only then).
fn alive(pid: u32) -> bool {
    procs::alive(u64::from(pid))
}

/// The types of the events of `harp`'s first run, as the store holds them.
fn run_events(w: &World, harp: &str) -> Vec<String> {
    let ran = run_now(w, &["test", "session-events", harp, "--run", "1"]);
    if ran.code != 0 {
        return Vec::new();
    }
    ran.json()["events"]
        .as_array()
        .map(|events| {
            events
                .iter()
                .filter_map(|e| e["type"].as_str().map(str::to_string))
                .collect()
        })
        .unwrap_or_default()
}

/// The launch the frontend was handed (test-support's record of it).
fn frontend_launch(w: &World) -> Value {
    let path = w
        .common
        .project
        .home()
        .join(".ctxloom/test-frontend-launch.json");
    let text = files::read(&path)
        .unwrap_or_else(|e| panic!("no launch was recorded ({e}): {}", w.ran().said()));
    serde_json::from_str(&text).expect("a launch record")
}

/// The variables a shell sets of its own, which no launch names.
const SHELL_OWN: [&str; 4] = ["PWD", "OLDPWD", "SHLVL", "_"];

/// The TUI of `project` was started exactly as the frontend's launch says:
/// its arguments, its whole environment (nothing else, but what the shell
/// sets of its own) and its directory.
fn started_exactly_as_launched(w: &World, project: &std::path::Path) {
    let launch = frontend_launch(w);
    let args: Vec<String> = tui_record(w, project, "tui-args")
        .lines()
        .map(str::to_string)
        .collect();
    let launched: Vec<String> = launch["args"]
        .as_array()
        .expect("its args")
        .iter()
        .map(|a| a.as_str().unwrap_or_default().to_string())
        .collect();
    assert_eq!(args, launched, "the TUI's arguments are the launch's");
    let env: std::collections::BTreeMap<String, String> = tui_record(w, project, "tui-env")
        .split('\0')
        .filter_map(|pair| pair.split_once('='))
        .filter(|(name, _)| !SHELL_OWN.contains(name))
        .map(|(name, value)| (name.to_string(), value.to_string()))
        .collect();
    let launched: std::collections::BTreeMap<String, String> = launch["env"]
        .as_object()
        .expect("its env")
        .iter()
        .map(|(name, value)| (name.clone(), value.as_str().unwrap_or_default().to_string()))
        .collect();
    let extra: Vec<&String> = env.keys().filter(|k| !launched.contains_key(*k)).collect();
    let missing: Vec<&String> = launched.keys().filter(|k| !env.contains_key(*k)).collect();
    assert!(
        extra.is_empty() && missing.is_empty(),
        "the TUI's environment is the launch's: extra {extra:?}, missing {missing:?}"
    );
    for (name, value) in &launched {
        assert_eq!(env.get(name), Some(value), "{name} is the launch's");
    }
    let cwd = tui_record(w, project, "tui-cwd");
    let want = launch["cwd"].as_str().expect("its directory");
    assert_eq!(
        files::canonical(cwd.trim()),
        files::canonical(want),
        "the TUI's directory"
    );
}

/// Two sessions at once, one in each project, each its TUI stub's: both
/// seen started (forced, never waited for a race), then released.
fn tui_in_both(w: &mut World, step: &cucumber::gherkin::Step) {
    files::put_program(assistant_dir(w).join("claude"), &tui_stub());
    let env = tui_env(w);
    w.before_run();
    let words = line_words(step);
    let args: Vec<&str> = words.iter().map(String::as_str).collect();
    let first = w.common.project.dir().to_path_buf();
    let second = w.daemon_spec.second.clone().expect("a second project");
    let started: Vec<Child> = [&first, &second]
        .iter()
        .map(|dir| {
            let mut env = env.clone();
            env.push(("CTXLOOM_ROOT".to_string(), dir.display().to_string()));
            w.common.project.start_in_with(dir, &env_pairs(&env), &args)
        })
        .collect();
    for dir in [&first, &second] {
        wait_for(w, "both TUIs' start", || {
            files::exists(
                assistant_dir(w)
                    .join(dir.file_name().unwrap())
                    .join("tui-started"),
            )
        });
    }
    files::put(assistant_dir(w).join("release"), "go");
    w.daemon_spec.both = started
        .into_iter()
        .map(|child| crate::tests::Ran::of(child.wait_with_output().expect("it ran")))
        .collect();
}

/// Whether no project of the home keeps a store of its own (T14: the one
/// store is the daemon's, `~/.ctxloom/ctxloom.db`).
fn no_project_store(w: &World) -> bool {
    files::children(w.common.project.home().join(".ctxloom/projects"))
        .iter()
        .all(|project| !files::exists(project.join("ctxloom.db")))
}

/// The sessions `dir`'s project lists, by their names.
fn listed_in(w: &World, dir: &std::path::Path) -> Vec<String> {
    let env = env_of(w);
    let ran = w.common.project.run_in_with(
        dir,
        &env_pairs(&env),
        &["session", "list", "--format", "json"],
    );
    assert_eq!(ran.code, 0, "{}", ran.said());
    let listed = ran.json();
    let rows = listed
        .get("sessions")
        .and_then(Value::as_array)
        .or_else(|| listed.as_array())
        .cloned()
        .unwrap_or_default();
    rows.iter()
        .filter_map(|row| {
            row.get("name")
                .or_else(|| row.get("harp"))
                .and_then(Value::as_str)
                .map(str::to_string)
        })
        .collect()
}

// ------------------------------------------------------------------ given

#[given("no ctxloom daemon is serving for Alice")]
fn none_serving_for_alice(w: &mut World) {
    super::top::forget_pending(w);
    none_serving(w);
}

#[given(regex = r#"^Alice has a second project, "([^"]+)", set up with ctxloom$"#)]
fn second_project(w: &mut World, name: String) {
    let dir = w.adopt_scratch().join(&name);
    files::mkdirs(&dir);
    let env = env_of(w);
    let git = std::process::Command::new("git")
        .args(["init", "-q"])
        .current_dir(&dir)
        .status()
        .expect("git runs");
    assert!(git.success(), "the second project is a repository");
    let ran = w
        .common
        .project
        .run_in_with(&dir, &env_pairs(&env), &["init"]);
    assert_eq!(ran.code, 0, "{}", ran.said());
    w.daemon_spec.second = Some(dir);
}

#[given(regex = r#"^Alice's session "([^"]+)" is running$"#)]
fn alices_session_running(w: &mut World, harp: String) {
    // Her session: recorded and running; her daemon serves, and her
    // environment holds a credential a child of claude takes.
    hosting_daemon(w);
    let seeded = run_now(w, &["test", "seed-session", &harp]);
    assert_eq!(seeded.code, 0, "{}", seeded.said());
    w.common.set_env("ANTHROPIC_API_KEY", "a-key".to_string()); // gitleaks:allow
    w.daemon_spec.orchestrator = Some(harp);
}

/// Alice's assistant on her terminal, as far as its session's liveness
/// goes: a process that holds the session's lock (free now) until released.
fn holds_lock() -> String {
    procs::holds_lock(0, 900)
}

#[given(regex = r#"^Alice's session "([^"]+)" is running, with a child "([^"]+)" working for it$"#)]
fn alices_session_with_child(w: &mut World, owner: String, child: String) {
    assert_eq!(
        (child.as_str(), owner.as_str()),
        (CHILD, OWNER),
        "the child and owner the harness hosts"
    );
    // Her session, recorded, its lock held by her assistant.
    let dir = stub_claude(w, &child_stub());
    w.common
        .set_env(IDLE_VARIABLE, SPAWNED_IDLE_SECONDS.to_string());
    start_daemon_idling(w, DEFAULT_IDLE_SECONDS);
    let seeded = run_now(w, &["test", "seed-session", &owner, "--lock", "free"]);
    assert_eq!(seeded.code, 0, "{}", seeded.said());
    let lock = w
        .common
        .project
        .dir()
        .join(format!(".ctxloom/sessions/{owner}/session.lock"));
    let release = dir.join("release");
    let holder = dir.join("holds-lock");
    files::put_program(&holder, &holds_lock());
    let assistant = std::process::Command::new(&holder)
        .args([lock.as_os_str(), release.as_os_str()])
        .spawn()
        .expect("her assistant starts");
    w.daemon_spec.run = Some(assistant);
    w.daemon_spec.release = Some(release);
    until(w, "her session's lock held", DAEMON_WAIT, |w| {
        listed_state(w, &owner).filter(|s| s == "LIVE")
    });
    // Its child, in the middle of a turn.
    w.common.set_env("ANTHROPIC_API_KEY", "a-key".to_string()); // gitleaks:allow
    files::put(stub_dir(w).join("hold"), "once");
    let launched = run_now(
        w,
        &["session", "launch", &owner, "--name", &child, "a task"],
    );
    assert_eq!(launched.code, 0, "{}", launched.said());
    until(w, "the child's first turn", DAEMON_WAIT, |_| {
        files::exists(dir.join("child-turn")).then_some(())
    });
    conversation_bound(w);
}

/// The state `session list` gives `harp`, if it lists it.
fn listed_state(w: &World, harp: &str) -> Option<String> {
    let ran = run_now(w, &["session", "list", "--format", "json"]);
    let listed = ran.json();
    let rows = listed
        .as_array()
        .cloned()
        .or_else(|| listed["sessions"].as_array().cloned())
        .unwrap_or_default();
    rows.iter()
        .find(|row| {
            row["name"]
                .as_str()
                .is_some_and(|n| n.ends_with(&format!("/{harp}")))
        })
        .and_then(|row| row["state"].as_str().map(str::to_string))
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
    // A fresh home: no daemon has advertised itself there; or one an
    // earlier step started is gone (its discovery file left, stale). One a
    // command starts idles out soon.
    if files::exists(discovery_path(w)) {
        assert!(!serving_now(w), "no daemon serves: {}", daemon_log_text(w));
    }
    w.daemon_spec.logged_before = Some(daemons_logged(w));
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
    // A daemon an earlier step's command started (step 10c U6: init is the
    // daemon's) is stopped, and its directory taken away.
    if files::exists(daemon_dir(w)) {
        let stopped = run_now(w, &["daemon", "stop"]);
        assert_eq!(stopped.code, 0, "daemon stop: {}", stopped.said());
        files::remove_tree(daemon_dir(w));
    }
    // A file where the daemon's directory goes: no daemon can take its
    // single-instance lock there, advertise itself or log.
    files::put(daemon_dir(w), "not a directory");
}

#[given("a ctxloom daemon is serving for this home")]
fn serving(w: &mut World) {
    // It outlives the scenario's steps: a later step that says it still
    // serves must not find it idled out (shutdown.feature).
    start_daemon_idling(w, SPAWNED_IDLE_SECONDS);
}

/// Start `ctxloom daemon serve` of the stamp `stamp` (test-support), with
/// the scenario's short idle time; one a later command starts idles out too.
fn start_daemon_stamped(w: &mut World, stamp: &str) {
    let mut env = env_of(w);
    env.push((STAMP_VARIABLE.to_string(), stamp.to_string()));
    let env: Vec<(&str, &str)> = env.iter().map(|(k, v)| (k.as_str(), v.as_str())).collect();
    let idle = SPAWNED_IDLE_SECONDS.to_string();
    let daemon = w
        .common
        .project
        .start_with(&env, &["daemon", "serve", "--idle-seconds", &idle]);
    w.daemon_spec.daemon = Some(daemon);
    let deadline = Instant::now() + Duration::from_secs(30);
    while !files::exists(discovery_path(w)) || discovery_now(w).stamp != stamp {
        assert!(
            Instant::now() < deadline,
            "the daemon never served; its log:\n{}",
            daemon_log_text(w)
        );
        std::thread::sleep(Duration::from_millis(50));
    }
    while !serving_now(w) {
        assert!(Instant::now() < deadline, "{}", daemon_log_text(w));
        std::thread::sleep(Duration::from_millis(50));
    }
    w.common
        .set_env(IDLE_VARIABLE, SPAWNED_IDLE_SECONDS.to_string());
}

#[given("a ctxloom daemon of an older version is serving for this home")]
fn serving_older(w: &mut World) {
    start_daemon_stamped(w, OLDER_STAMP);
}

#[given("a ctxloom daemon of a newer version is serving for this home")]
fn serving_newer(w: &mut World) {
    start_daemon_stamped(w, NEWER_STAMP);
}

#[given("a ctxloom daemon of this version is serving for this home")]
fn serving_this(w: &mut World) {
    // It outlives the scenario's steps: a shutdown must find it serving.
    start_daemon_idling(w, SPAWNED_IDLE_SECONDS);
    w.common
        .set_env(IDLE_VARIABLE, SPAWNED_IDLE_SECONDS.to_string());
}

#[given("the session holds a prompt its hook held while an older daemon served")]
fn holds_a_prompt(w: &mut World) {
    start_daemon_stamped(w, OLDER_STAMP);
    fire(w, "UserPromptSubmit", USER_PROMPT_SUBMIT);
    let harp = fired(w).harp.clone();
    warned_for_prompt(w, "hook-cli-held");
    prompt_held(w, harp);
    // The older daemon goes (killed outright: its discovery file is left,
    // stale, for the next daemon to publish over).
    let mut daemon = w.daemon_spec.daemon.take().expect("the daemon started");
    let _ = daemon.kill();
    let _ = daemon.wait();
}

/// The stem of the hold that stands for every other session's of a full
/// home: a session root no session has.
const FULL_HOME_HOLD: &str = "00000000-0000-0000-0000-000000000000";

#[given("the holds of this home are at their bound")]
fn holds_at_bound(w: &mut World) {
    // Another session's hold of the home's whole bound (256 MiB, H-Q4),
    // sparse: the bound counts its size, and nothing reads it.
    let path = held_dir(w).join(format!("{FULL_HOME_HOLD}.held"));
    files::mkdirs(held_dir(w));
    let file = files::fs()
        .open(
            &path,
            file_system::Open::CreateNew(os::Mode::from_bits(0o600)),
        )
        .expect("a hold");
    file.set_len(256 << 20).expect("a sparse hold");
}

/// The older daemon still serving for a second hook: a hook never touches
/// a daemon's life (OD-61, OD-62: H-Q6's drain nudge is gone), so the one
/// the first hook held for serves on. (This step used to wait for it to
/// exit and start it again; it exited only at its idle time, 15 s a
/// scenario, the daemon cluster's cap, owner ruling 2026-11-18.)
fn older_still_serving(w: &mut World) {
    let alive = w
        .daemon_spec
        .daemon
        .as_mut()
        .is_some_and(|daemon| matches!(daemon.try_wait(), Ok(None)));
    assert!(alive, "the older daemon exited: {}", daemon_log_text(w));
    assert_eq!(discovery_now(w).stamp, OLDER_STAMP, "the older one serves");
    assert!(serving_now(w), "the older daemon answers");
}

#[given("the session's hooks held its prompt and its turn's end while an older daemon served")]
fn held_prompt_and_turn_end(w: &mut World) {
    start_daemon_stamped(w, OLDER_STAMP);
    fire(w, "UserPromptSubmit", USER_PROMPT_SUBMIT);
    warned_for_prompt(w, "hook-cli-held");
    older_still_serving(w);
    fire(w, "Stop", STOP);
    let harp = fired(w).harp.clone();
    let held = holds(w);
    assert_eq!(held.len(), 1, "the session's hold: {held:?}");
    let text = String::from_utf8_lossy(&files::read_bytes(&held[0]).expect("the hold")).to_string();
    assert!(
        text.contains("hook:prompt:") && text.contains("hook:turn-end:"),
        "{harp}'s prompt and turn's end are held"
    );
}

#[given(
    "the session's hooks held its prompt, then dropped its turn's end past the hold's bound, while an older daemon served"
)]
fn held_then_dropped(w: &mut World) {
    start_daemon_stamped(w, OLDER_STAMP);
    fire(w, "UserPromptSubmit", USER_PROMPT_SUBMIT);
    warned_for_prompt(w, "hook-cli-held");
    holds_at_bound(w);
    older_still_serving(w);
    fire(w, "Stop", STOP);
    let warnings = hook_warnings(w);
    assert!(
        warnings.iter().any(|w| w["code"] == "hook-cli-not-recorded"
            && w["args"]["why"]
                .as_str()
                .is_some_and(|why| why.contains("is at its bound"))),
        "the turn's end was dropped at the bound: {warnings:#?}"
    );
    // The home is no longer at its bound: only the session's hold is left,
    // its prompt and the gap where its turn's end was dropped.
    files::remove(held_dir(w).join(format!("{FULL_HOME_HOLD}.held")));
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
    w.daemon_spec.idle_wait = Some(BUSY_IDLE_SECONDS);
    let start_daemon = |w: &mut World| start_daemon_idling(w, BUSY_IDLE_SECONDS);
    match busy.as_str() {
        // The session's daemon is the one serving (U5: a `run` prepares
        // its session through it), so it is started first.
        "an interactive session running" => {
            start_daemon(w);
            interactive_session(w);
        }
        "a child it hosts" => {
            start_daemon(w);
            hosted_child(w);
        }
        "a one-shot it hosts" => {
            start_daemon(w);
            hosted_working_one_shot(w);
        }
        "a session's frontend subscribed" => {
            start_daemon(w);
            interactive_session(w);
        }
        _ => red("a ctxloom daemon is serving for this home, with something to do"),
    }
}

/// The kill switch at a step (U6): a daemon that finds it names the step
/// it is killed outright at, takes it, and is killed; the next finds none.
const DIE_AT: &str = "test-die-at";

/// The step the daemon spec's `when` names, as the switch says it.
fn die_at(when: &str) -> Option<&'static str> {
    Some(match when {
        "after the run is enqueued" => "enqueued",
        "after the session is minted" => "minted",
        "after the engine is started" => "started",
        "while a turn is in flight" => "turn",
        "after a hook's fact is recorded, before it is answered" => "hook",
        "while the discovery file is written" => "discovery",
        // A library write's points (step 10b U3): after its intent is
        // recorded, after it is staged, after the rename, after its done
        // fact. "In the middle" of one is between its staging and its rename.
        "after the write's intent is recorded, before it is staged" => "intended",
        "after the write is staged, before it is renamed in" | "in the middle of the write" => {
            "staged"
        }
        "after the write is renamed in, before it is recorded done" => "renamed",
        "after the write is recorded done, before it is answered" => "done",
        // A sequence's (T28): after its first step, the trash, is applied.
        "after the session's directory moved to trash, before the purge"
        | "after the old bundle moved to trash, before the new one moved in" => "trashed",
        // A pull's (step 10c U4, O6.3): its sequence's trees, then its lock.
        "after the first tree is installed, before the second" => "tree-installed",
        "after every tree is installed, before the lock is written" => "trees-installed",
        "after the lock is written, before the pull is answered" => "done",
        _ => return None,
    })
}

/// Wait, bounded, for the daemon the scenario started to have exited (it
/// was killed outright: its discovery file is left).
fn daemon_gone(w: &mut World) {
    let Some(mut daemon) = w.daemon_spec.daemon.take() else {
        return;
    };
    let deadline = Instant::now() + DAEMON_WAIT;
    while matches!(daemon.try_wait(), Ok(None)) {
        assert!(
            Instant::now() < deadline,
            "the daemon was never killed; its log:\n{}",
            daemon_log_text(w)
        );
        std::thread::sleep(Duration::from_millis(50));
    }
    w.daemon_spec.killed = true;
}

/// What the import's kill point presupposes ("the OLD bundle moved to
/// trash, before the NEW one moved in"): a bundle `notes`, and its export
/// at `exported/notes` holding a file the bundle does not, the new copy a
/// forced import replaces it with.
fn old_and_new_notes(w: &mut World) {
    for args in [
        &["bundle", "create", "notes", "--description", "my notes"][..],
        &["bundle", "export", "notes", "exported"][..],
    ] {
        let ran = run_now(w, args);
        assert_eq!(ran.code, 0, "{args:?}: {}", ran.said());
    }
    files::put(
        w.common
            .project
            .dir()
            .join("exported/notes/fragments/shared.md"),
        "only the new copy holds this\n",
    );
}

/// What a foreign writer's run leaves (step 10c U10), project-relative: a
/// file holding a text, or a file gone, or a file without a text.
#[derive(Clone, Debug)]
pub(super) enum Leaves {
    Holding(&'static str, &'static str),
    Without(&'static str, &'static str),
    Gone(&'static str),
}

/// `after step <n> of '<command>'`: the kill point after a foreign
/// writer's `n`th step, and the command.
fn foreign_step(when: &str) -> Option<(usize, String)> {
    let rest = when.strip_prefix("after step ")?;
    let (n, command) = rest.split_once(" of '")?;
    Some((n.parse().ok()?, command.strip_suffix('\'')?.to_string()))
}

/// A Go-era hook entry `doctor --fix` takes out (once Go is retired).
const STALE_HOOK: &str = "ctxloom hook inject-context abc";

/// What `command` presupposes, planted (a stale hook to take out, claims to
/// give back), and what its run must leave.
fn foreign_presupposed(w: &mut World, command: &str) -> Leaves {
    match command {
        "ctxloom doctor --fix" => {
            // Its report passes, so the run's exit is the fix's: an agent
            // credential is exported (doctor's credentials row).
            w.common
                .set_env("CLAUDE_CODE_OAUTH_TOKEN", "not-a-real-token".to_string());
            let home_config = w.common.project.home().join(".ctxloom/config.yaml");
            let said = files::read(&home_config).unwrap_or_default();
            files::put(&home_config, format!("{said}legacy:\n  go: retired\n"));
            files::put(
                w.common.project.dir().join(".claude/settings.json"),
                format!(
                    r#"{{"hooks":{{"SessionStart":[{{"hooks":[{{"type":"command","command":"{STALE_HOOK}"}}]}}]}}}}"#
                ),
            );
            Leaves::Without(".claude/settings.json", STALE_HOOK)
        }
        "ctxloom manage uninstall --yes" | "ctxloom manage uninstall" => {
            let ran = run_now(w, &["materialize", "--yes", "--backend", "claude-code"]);
            assert_eq!(ran.code, 0, "the claims to give back: {}", ran.said());
            assert!(files::exists(w.common.project.dir().join("CLAUDE.md")));
            Leaves::Gone("CLAUDE.md")
        }
        "ctxloom materialize --target out --yes" => Leaves::Holding("out/CLAUDE.md", "ctxloom"),
        other => panic!("no foreign writer the harness knows: {other}"),
    }
}

#[given(regex = r"^the daemon will be killed outright (.+)$")]
fn killed_at(w: &mut World, step: &cucumber::gherkin::Step, when: String) {
    if let Some((n, command)) = foreign_step(&when) {
        // Killed once its `n`th step is recorded done (step 10c U10); the
        // CLI's one retry finishes it on a new daemon.
        let leaves = foreign_presupposed(w, &command);
        w.daemon_spec.foreign_run = Some((command, leaves));
        w.daemon_spec.before_kill = discovery_if_serving(w);
        files::put(daemon_dir(w).join(DIE_AT), format!("step-{n}"));
        return;
    }
    if let Some(rel) = superseded_file(&when) {
        // Killed once its intent is recorded, and the file changed by hand
        // before the CLI's retry reaches a new daemon (step 10c U13).
        w.daemon_spec.before_kill = discovery_if_serving(w);
        files::put(daemon_dir(w).join(DIE_AT), "intended");
        edit_before_retry(w, &rel, &crate::tests::gherkin_common::doc(step));
        return;
    }
    if let Some(step) = die_at(&when) {
        // Killed already (a scenario that planted what its daemon left).
        if w.daemon_spec.killed {
            return;
        }
        if when == "after the old bundle moved to trash, before the new one moved in" {
            old_and_new_notes(w);
        }
        w.daemon_spec.before_kill = discovery_if_serving(w);
        files::put(daemon_dir(w).join(DIE_AT), step);
        // A daemon serving with the step already reached (a child's turn in
        // flight) takes the switch on its next round.
        if w.daemon_spec.daemon.is_some() {
            daemon_gone(w);
        }
        return;
    }
    if when != "after the write is applied, before it is answered" {
        red("the daemon will be killed outright at a step")
    }
    // The test-support switch (slice 3 step 4): the daemon that finds it
    // takes it, applies the next write it is sent through its coordinator,
    // and is killed before it answers; the daemon started after it finds
    // none.
    w.daemon_spec.before_kill = discovery_if_serving(w);
    files::put(daemon_dir(w).join(DIE_AFTER_APPLY), "once");
}

/// The file a kill's `when` says is changed by hand before the retry
/// (`after the write's intent is recorded, and the file "<rel>" is changed
/// by hand before the retry, to:`), project-relative; none for any other.
fn superseded_file(when: &str) -> Option<String> {
    let rest = when.strip_prefix("after the write's intent is recorded, and the file \"")?;
    let (rel, tail) = rest.split_once('"')?;
    (tail == " is changed by hand before the retry, to:").then(|| rel.to_string())
}

/// The test-support program the CLI runs before its one retry (step 10c
/// U13's harness), so a test stands for a person between the daemon's loss
/// and the write sent again: the race forced, never waited for.
const BEFORE_RETRY: &str = "CTXLOOM_TEST_BEFORE_RETRY";

/// The CLI's retry runs a program that writes `text` over `rel` first.
fn edit_before_retry(w: &mut World, rel: &str, text: &str) {
    let dir = w.common.project.home().join("..").join("before-retry");
    let body = dir.join("hand-edit.txt");
    files::put(&body, format!("{text}\n"));
    let target = rel
        .split('/')
        .fold(w.common.project.dir().to_path_buf(), |path, part| {
            path.join(part)
        });
    files::put_program(
        dir.join("edit"),
        &format!("cp '{}' '{}'\n", body.display(), target.display()),
    );
    w.common.set_env(
        BEFORE_RETRY,
        dir.join("edit").to_str().expect("utf-8").to_string(),
    );
}

/// The discovery file of the daemon serving now, when one has written one
/// (a daemon a command started, or the scenario's own).
fn discovery_if_serving(w: &World) -> Option<run_ports::Discovery> {
    files::exists(discovery_path(w)).then(|| discovery_now(w))
}

/// The daemon directory's switch that kills a test-support daemon once a
/// write it was sent is applied, before it is answered.
const DIE_AFTER_APPLY: &str = "test-die-after-apply";

#[given(expr = "a child {string} that is open and idle")]
fn child_idle(w: &mut World, child: String) {
    assert_eq!(child, "brisk-copper-moth", "the child the harness hosts");
    idle_child(w);
}

#[given(expr = "a child {string} that is in the middle of a turn")]
fn child_mid_turn(w: &mut World, child: String) {
    assert_eq!(child, "brisk-copper-moth", "the child the harness hosts");
    w.common
        .set_env(IDLE_VARIABLE, SPAWNED_IDLE_SECONDS.to_string());
    start_daemon_idling(w, DEFAULT_IDLE_SECONDS);
    // Its first turn is taken and left in flight.
    files::put(stub_dir(w).join("hold"), "once");
    hosted_child(w);
}

#[given(expr = "a child {string} that has been closed")]
fn child_closed(w: &mut World, child: String) {
    assert_eq!(child, "brisk-copper-moth", "the child the harness hosts");
    idle_child(w);
    let closed = run_now(w, &["session", "close", &child]);
    assert_eq!(closed.code, 0, "{}", closed.said());
    until(w, "the child closed", DAEMON_WAIT, |w| {
        (shown(w, "brisk-copper-moth")["closed"] == Value::Bool(true)).then_some(())
    });
}

#[given(expr = "a child {string} of {string} that is open and idle")]
pub(super) fn child_of_idle(w: &mut World, child: String, owner: String) {
    if (child.as_str(), owner.as_str()) == ("olive-quiet-finch", "brisk-copper-moth") {
        grandchild_idle(w);
        return;
    }
    // A child of a hosted child: launched by it, as its engine would.
    if owner != "amber-quiet-heron" {
        grandchild_of(w, &owner, &child, None);
        return;
    }
    if (child.as_str(), owner.as_str()) == ("brisk-copper-moth", "amber-quiet-heron") {
        return idle_child(w);
    }
    // Another child of a running owner (step 9: a hosted orchestrator),
    // launched by Alice and idle once its first turn ended.
    user_launches(w, &owner, &child, None);
    first_turn_told(w, &owner, &child);
}

/// The hosted claude child, its agent planned on `profile` (step 10c U13:
/// its loadout names that profile's bundles), open and idle; the session
/// a later hook fires in.
#[given(expr = "a claude child {string} of {string} that is open and idle on the profile {string}")]
fn claude_child_on(w: &mut World, child: String, owner: String, profile: String) {
    assert_eq!(
        (child.as_str(), owner.as_str()),
        ("brisk-copper-moth", "amber-quiet-heron"),
        "the child and owner the harness hosts"
    );
    w.daemon_spec
        .project_says
        .push(format!("agents.default.profiles: [{profile}]"));
    idle_child(w);
    w.seeded_harps.push(child);
}

/// The hosted child's own child (step 10c D16): launched by the child,
/// `olive-quiet-finch`, its first turn ended and told to it.
fn grandchild_idle(w: &mut World) {
    grandchild_of(w, "brisk-copper-moth", "olive-quiet-finch", None);
}

/// `child`, launched under the hosted child `owner`, its first turn ended.
fn grandchild_of(w: &mut World, owner: &str, child: &str, agent: Option<&str>) {
    let mut env = env_of(w);
    env.push(("ANTHROPIC_API_KEY".to_string(), "a-key".to_string())); // gitleaks:allow
    let env: Vec<(&str, &str)> = env.iter().map(|(k, v)| (k.as_str(), v.as_str())).collect();
    let mut args = vec!["session", "launch", owner, "--name", child];
    if let Some(agent) = agent {
        args.extend(["--agent", agent]);
    }
    args.push("a task");
    let launched = w.common.project.run_with(&env, &args);
    assert_eq!(launched.code, 0, "{}", launched.said());
    // Its first turn's end is its run's record (its owner, itself a
    // hosted child, is told it through its own conversation).
    until(w, "the grandchild's first turn to end", DAEMON_WAIT, |w| {
        run_events(w, child)
            .iter()
            .any(|kind| kind == "TurnCompletedRecorded")
            .then_some(())
    });
}

/// A child no session holds open: one nothing launched.
const STALE_CHILD: &str = "0b0b0b0b-0b0b-4b0b-8b0b-0b0b0b0b0b0b";

#[given("a hand-off file is left for a child that is no longer open")]
fn stale_hand_off(w: &mut World) {
    // Left by a launch whose child was never recorded, longer ago than a
    // launch takes to record its child.
    let path = launch_dir(w).join(STALE_CHILD);
    files::put_mode(
        &path,
        "ctxloom-launch-env 3\nANTHROPIC_API_KEY=left\n",
        0o600,
    ); // gitleaks:allow
    let old = std::time::SystemTime::now() - Duration::from_secs(3_600);
    files::open_append(&path)
        .and_then(|file| file.set_modified(old))
        .expect("the hand-off is made old");
    w.common
        .set_env(IDLE_VARIABLE, SPAWNED_IDLE_SECONDS.to_string());
}

/// The test-support switch that fails the daemon's start-up catch-up
/// (OD-52), its one line the why.
const START_UP_FAILS: &str = "test-start-up-fails";

#[given(expr = "the daemon's start-up catch-up will fail with {string}")]
fn start_up_will_fail(w: &mut World, why: String) {
    files::put(daemon_dir(w).join(START_UP_FAILS), format!("{why}\n"));
    w.common
        .set_env(IDLE_VARIABLE, SPAWNED_IDLE_SECONDS.to_string());
}

// ------------------------------------------------------------------- when

#[when("the cause of the failure is gone")]
fn start_up_cause_gone(w: &mut World) {
    files::remove(daemon_dir(w).join(START_UP_FAILS));
}

#[when(
    "Alice's daemon is killed outright while her assistant works, and her assistant goes on firing its hooks"
)]
fn alices_daemon_killed(w: &mut World) {
    let named = run_now(w, &["test", "session-name", OWNER]);
    assert_eq!(named.code, 0, "{}", named.said());
    let name = named.stdout.trim().to_string();
    w.daemon_spec.before_kill = Some(discovery_now(w));
    // Killed once her prompt's fact is recorded, before it is answered.
    files::put(daemon_dir(w).join(DIE_AT), "hook");
    let (prompt, _) = hook_run(w, "UserPromptSubmit", USER_PROMPT_SUBMIT, Some(&name));
    daemon_gone(w);
    // She goes on: her next hook finds no daemon, starts one, and delivers.
    let (stop, _) = hook_run(w, "Stop", STOP, Some(&name));
    w.daemon_spec.hooks = vec![prompt, stop];
}

/// The lifeline's forcing test (gherkin_features.test.rs): the scenario's
/// commands are given a lifeline (`CTXLOOM_TEST_LIFELINE`) that a process
/// of its own holds, not the test process, so a step can kill its holder
/// outright as nextest's timeout kills a test. The holder is the binary's
/// `test hold-lock`, which takes it through the platform's file lock, as
/// the daemon does (macOS has no `flock(1)`, CI 2026-11-19).
#[given("the scenario's commands have a lifeline a process of their own holds")]
fn own_lifeline(w: &mut World) {
    let path = w.common.project.dir().join("lifeline.lock");
    let home = w.common.project.home();
    let mut holder = Command::new(crate::tests::binary());
    holder
        .args(["test", "hold-lock"])
        .arg(&path)
        .env_clear()
        .env("HOME", home)
        .env("USERPROFILE", home)
        .env("TMPDIR", crate::tests::temp_root());
    for name in ["PATH", "SystemRoot"] {
        if let Some(value) = crate::tests::host_var(name) {
            holder.env(name, value);
        }
    }
    let holder = holder
        .current_dir(w.common.project.dir())
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .spawn()
        .expect("the lifeline's holder starts");
    w.daemon_spec.holder = Some(holder);
    let deadline = Instant::now() + Duration::from_secs(10);
    while file_lock::lock_within(&*files::fs(), &path, Duration::ZERO).is_ok() {
        assert!(
            Instant::now() < deadline,
            "the holder never took the lifeline"
        );
        std::thread::sleep(Duration::from_millis(20));
    }
    w.common
        .set_env(crate::tests::LIFELINE, path.display().to_string());
}

#[when("the lifeline's holder is killed outright")]
fn holder_killed(w: &mut World) {
    let mut holder = w.daemon_spec.holder.take().expect("a lifeline's holder");
    holder.kill().expect("the holder is killed");
    holder.wait().expect("the holder is reaped");
}

/// Whether the process `pid` is still there (`kill -0`).
fn process_is_there(pid: &str) -> bool {
    Command::new("kill")
        .args(["-0", pid])
        .stderr(Stdio::null())
        .status()
        .is_ok_and(|status| status.success())
}

#[then("the daemon and the child it hosts are gone within 30 seconds")]
fn daemon_and_child_gone(w: &mut World) {
    let log = daemon_log_text(w);
    let child = log
        .lines()
        .find_map(|line| {
            line.split_once("started, pid ")
                .map(|(_, pid)| pid.trim().to_string())
        })
        .unwrap_or_else(|| panic!("no hosted child in the daemon's log:\n{log}"));
    let deadline = Instant::now() + Duration::from_secs(30);
    loop {
        let daemon = w
            .daemon_spec
            .daemon
            .as_mut()
            .expect("a daemon the scenario started");
        let exited = matches!(daemon.try_wait(), Ok(Some(_)));
        if exited && !process_is_there(&child) {
            return;
        }
        assert!(
            Instant::now() < deadline,
            "after 30 s the daemon {} and its child {child} {}; its log:\n{}",
            if exited { "has exited" } else { "still runs" },
            if process_is_there(&child) {
                "still runs"
            } else {
                "is gone"
            },
            daemon_log_text(w)
        );
        std::thread::sleep(Duration::from_millis(100));
    }
}

#[when("her daemon's idle time passes")]
fn her_idle_passes(w: &mut World) {
    idle_passes(w);
}

#[when("the daemon's idle time passes")]
fn idle_passes(w: &mut World) {
    // Its idle time, and a margin for its poll and its last round.
    let idle = w.daemon_spec.idle_wait.unwrap_or(IDLE_SECONDS);
    std::thread::sleep(Duration::from_secs(idle + 2));
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
    // The next command's daemon idles as a spawned one does, so it outlives
    // the steps that call it; its discovery is read here, while `daemon
    // resolve` has just found it serving, never left to a later step's
    // timing (under load a short idle time had it gone by then).
    w.common
        .set_env(IDLE_VARIABLE, SPAWNED_IDLE_SECONDS.to_string());
    let next = run_now(w, &["daemon", "resolve"]);
    assert_eq!(next.code, 0, "{}\n{}", next.said(), daemon_log_text(w));
    w.daemon_spec.new = Some(discovery_now(w));
    w.daemon_spec.old = Some(old);
}

pub(super) const USER_PROMPT_SUBMIT: &str =
    include_str!("../../../../adapters/driven/engine-claude/src/fixtures/user_prompt_submit.json");
pub(super) const POST_TOOL_BATCH: &str =
    include_str!("../../../../adapters/driven/engine-claude/src/fixtures/post_tool_batch.json");
pub(super) const STOP: &str =
    include_str!("../../../../adapters/driven/engine-claude/src/fixtures/stop.json");
pub(super) const MESSAGE_DISPLAY: &str =
    include_str!("../../../../adapters/driven/engine-claude/src/fixtures/message_display.json");

/// What the fixture's prompt says.
const PROMPT: &str = "Run the shell command: echo hi";

/// The stamps of a daemon older and newer than any build of this one.
const OLDER_STAMP: &str = "v0.0.1-0000000";
const NEWER_STAMP: &str = "v999.0.0-0000000";

/// The variable a test names a ctxloom's stamp by (test-support).
const STAMP_VARIABLE: &str = "CTXLOOM_TEST_STAMP";

/// S16's bound on a hook, and what a process's start and exit add to it.
const HOOK_BOUND: Duration = Duration::from_secs(10);
const PROCESS_SLACK: Duration = Duration::from_secs(1);

/// `ctxloom hook claude-code execute <native>` as the session's engine runs
/// it: the scenario's environment, the session's name when there is one
/// (none: a hook with no session records nothing), `payload` on stdin.
pub(super) fn hook_run(
    w: &World,
    native: &str,
    payload: &str,
    session: Option<&str>,
) -> (crate::tests::Ran, Duration) {
    let mut env = env_of(w);
    if let Some(session) = session {
        env.push(("CTXLOOM_SESSION".to_string(), session.to_string()));
    }
    let env: Vec<(&str, &str)> = env.iter().map(|(k, v)| (k.as_str(), v.as_str())).collect();
    let started = Instant::now();
    let mut child = w
        .common
        .project
        .start_talking(&env, &["hook", "claude-code", "execute", native]);
    crate::tests::feed(child.stdin.take().expect("stdin"), payload);
    let ran = crate::tests::Ran::of(child.wait_with_output().expect("the hook finishes"));
    (ran, started.elapsed())
}

/// The rounds the serving daemon has finished, by `daemon status`.
fn rounds_done(w: &World) -> Option<u64> {
    let ran = run_now(w, &["daemon", "status", "--format", "json"]);
    (ran.code == 0)
        .then(|| ran.json()["daemons"][0]["rounds"].as_u64())
        .flatten()
}

/// The daemon has finished a round that began after this call: what a
/// command committed before it (a read that saw a file changed by hand) is
/// recorded on the sessions it owes by the daemon's next round, and a hook
/// collects it from the projection as it is, no round awaited (the owner's
/// design, `daemon_api::reactions::redeliver_context`): a hook fired before
/// that round answers nothing, by design, and collects the change at a
/// later one. A scenario that says the NEXT hook collects it waits for the
/// round first (CI on macOS, 2026-11-19: a hook answered `{}`). Two rounds
/// are finished: the one running when this is asked may have begun before.
/// No daemon serving: nothing to wait for.
fn settled(w: &World) {
    let Some(before) = rounds_done(w) else {
        return;
    };
    until(w, "the daemon's next round", DAEMON_WAIT, |w| {
        rounds_done(w).filter(|now| *now >= before + 2).map(|_| ())
    });
}

/// The session's assistant fires its `native` hook with `payload`.
fn fire(w: &mut World, native: &str, payload: &str) {
    w.seed();
    let harp = w.seeded_harps.last().cloned().expect("a session");
    // Named in process, as the seed named it: no daemon is reached for it
    // (a command of this version would stop an older daemon, M3).
    let named = run_now(w, &["test", "session-name", &harp]);
    assert_eq!(named.code, 0, "{}", named.said());
    let name = named.stdout.trim().to_string();
    let events_before = session_events(w, &harp).len();
    if std::mem::take(&mut w.daemon_spec.edited_by_hand) {
        settled(w);
    }
    let (unrecorded, _) = hook_run(w, native, payload, None);
    let (ran, took) = hook_run(w, native, payload, Some(&name));
    w.daemon_spec.fired = Some(Fired {
        ran,
        took,
        harp,
        events_before,
        unrecorded_answer: unrecorded.stdout,
    });
}

fn fired(w: &World) -> &Fired {
    w.daemon_spec.fired.as_ref().expect("a hook fired")
}

#[when("the session's assistant fires its UserPromptSubmit hook")]
fn hook_fires(w: &mut World) {
    fire(w, "UserPromptSubmit", USER_PROMPT_SUBMIT);
}

#[when("the session's assistant fires its Stop hook")]
fn stop_hook_fires(w: &mut World) {
    fire(w, "Stop", STOP);
}

const SESSION_START: &str =
    include_str!("../../../../adapters/driven/engine-claude/src/fixtures/session_start.json");

/// A hook that reads the library (step 10 O3): SessionStart (the
/// session-start notices), or PostToolUse after a Skill call (the skill's
/// link-group mates).
#[when(
    regex = r#"^the session's assistant fires its (SessionStart|PostToolUse, after the skill "([^"]+)") hook$"#
)]
fn library_hook_fires(w: &mut World, hook: String, skill: String) {
    if hook == "SessionStart" {
        fire(w, "SessionStart", SESSION_START);
        return;
    }
    let payload = json!({
        "session_id": "9d764b45-a56a-48d7-ac9d-c2a16c5669f1",
        "hook_event_name": "PostToolUse",
        "cwd": w.common.project.dir().to_str().expect("utf-8"),
        "tool_name": "Skill",
        "tool_input": {"skill": skill, "args": ""},
        "tool_response": "ok",
    });
    fire(w, "PostToolUse", &payload.to_string());
}

/// A hook after a plain tool's call (step 10c U13: a tool's result is an
/// event a re-assembly may ride).
#[when(regex = r#"^the session's assistant fires its PostToolUse, after the tool "([^"]+)" hook$"#)]
fn tool_hook_fires(w: &mut World, tool: String) {
    let payload = json!({
        "session_id": "9d764b45-a56a-48d7-ac9d-c2a16c5669f1",
        "hook_event_name": "PostToolUse",
        "cwd": w.common.project.dir().to_str().expect("utf-8"),
        "tool_name": tool,
        "tool_input": {"file_path": "README.md"},
        "tool_response": "ok",
    });
    fire(w, "PostToolUse", &payload.to_string());
}

/// The `additionalContext` of a hook's answer, as claude reads it
/// (`hookSpecificOutput.additionalContext` of its stdout's JSON); none
/// when the answer is no JSON, or has none.
fn additional_context(stdout: &str) -> Option<String> {
    let answer: Value = serde_json::from_str(stdout.trim()).ok()?;
    answer["hookSpecificOutput"]["additionalContext"]
        .as_str()
        .map(str::to_string)
}

#[then(expr = "the hook's answer to the engine carries additionalContext containing {string}")]
fn context_carried(w: &mut World, text: String) {
    let fired = fired(w);
    let context = additional_context(&fired.ran.stdout)
        .unwrap_or_else(|| panic!("no additionalContext: {}", fired.ran.said()));
    assert!(context.contains(&text), "{text:?} is not in {context:?}");
}

#[then("the hook's answer to the engine carries no additionalContext")]
fn no_context(w: &mut World) {
    let fired = fired(w);
    assert_eq!(
        additional_context(&fired.ran.stdout),
        None,
        "{}",
        fired.ran.said()
    );
}

#[then("the hook exits 0, within its bound")]
fn hook_exits_within_bound(w: &mut World) {
    let fired = fired(w);
    assert_eq!(fired.ran.code, 0, "{}", fired.ran.said());
    assert!(
        fired.took <= HOOK_BOUND + PROCESS_SLACK,
        "took {:?}: {}",
        fired.took,
        fired.ran.said()
    );
}

/// What each library part of a hook's answer says, to find it absent:
/// the session-start notice of a project with profiles and no agent, and
/// the mates line of a completed skill (setup-application's hooks).
fn library_part_marker(part: &str) -> &'static str {
    match part {
        "session-start notice" => "has profiles but no agents configured",
        "linked skills not yet invoked" => "linked skills not yet invoked",
        other => panic!("no library part {other:?} of a hook's answer"),
    }
}

/// The engine's stdout is empty or one JSON document, and holds nothing of
/// the library part named.
#[then(regex = r"^the hook's answer to the engine is well formed, and carries no (.+)$")]
fn hook_answer_without(w: &mut World, part: String) {
    let marker = library_part_marker(&part);
    let answer = fired(w).ran.stdout.trim().to_string();
    if !answer.is_empty() {
        let _: Value = serde_json::from_str(&answer)
            .unwrap_or_else(|e| panic!("the answer is not JSON ({e}): {answer}"));
    }
    assert!(
        !answer.contains(marker),
        "the answer carries the {part}: {answer}"
    );
}

/// The fixture's MessageDisplay, as piece `index` of ten of one message.
fn piece(index: u32) -> String {
    let mut payload: Value = serde_json::from_str(MESSAGE_DISPLAY).expect("the fixture");
    payload["index"] = json!(index);
    payload["final"] = json!(index == 9);
    payload["delta"] = json!(format!("piece {index}"));
    payload.to_string()
}

#[when("the session's assistant fires ten MessageDisplay hooks at once")]
fn ten_hooks_at_once(w: &mut World) {
    w.seed();
    let harp = w.seeded_harps.last().cloned().expect("a session");
    let named = run_now(w, &["test", "session-name", &harp]);
    assert_eq!(named.code, 0, "{}", named.said());
    let name = named.stdout.trim().to_string();
    let events_before = session_events(w, &harp).len();
    let mut env = env_of(w);
    env.push(("CTXLOOM_SESSION".to_string(), name));
    let env: Vec<(&str, &str)> = env.iter().map(|(k, v)| (k.as_str(), v.as_str())).collect();
    let started = Instant::now();
    // All ten started before any is waited for.
    let children: Vec<Child> = (0..10)
        .map(|index| {
            let mut child = w
                .common
                .project
                .start_talking(&env, &["hook", "claude-code", "execute", "MessageDisplay"]);
            crate::tests::feed(child.stdin.take().expect("stdin"), &piece(index));
            child
        })
        .collect();
    let mut last = None;
    for child in children {
        let ran = crate::tests::Ran::of(child.wait_with_output().expect("the hook finishes"));
        assert_eq!(ran.code, 0, "{}", ran.said());
        last = Some(ran);
    }
    w.daemon_spec.fired = Some(Fired {
        ran: last.expect("ten hooks"),
        took: started.elapsed(),
        harp,
        events_before,
        unrecorded_answer: String::new(),
    });
}

/// A one-shot's stub turn that fires its UserPromptSubmit hook (the
/// fixture beside it) before it answers.
fn hooking_stub() -> String {
    format!(
        r#"{records}cat > /dev/null
ctxloom hook claude-code execute UserPromptSubmit < "$here/prompt.json" > /dev/null 2>&1 || true
echo '{{"type":"system","subtype":"init","session_id":"9d764b45-a56a-48d7-ac9d-c2a16c5669f1"}}'
echo '{{"type":"result","subtype":"success","is_error":false,"result":"ok","session_id":"9d764b45-a56a-48d7-ac9d-c2a16c5669f1"}}'
"#,
        records = records_itself()
    )
}

#[when("Alice asks her assistant one thing, which the killed daemon leaves unfinished")]
fn unfinished(w: &mut World) {
    let dir = stub_claude(w, &hooking_stub());
    files::put(dir.join("prompt.json"), USER_PROMPT_SUBMIT);
    w.common
        .set_env(IDLE_VARIABLE, SPAWNED_IDLE_SECONDS.to_string());
    let mut env = env_of(w);
    env.push(("ANTHROPIC_API_KEY".to_string(), "a-key".to_string())); // gitleaks:allow
                                                                      // What it comes to is the kill's: the record is what is judged.
    let _ = w
        .common
        .project
        .run_with(&env_pairs(&env), &["run", "--one-shot", "one thing"]);
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
fn one_daemon(w: &mut World) {
    for ran in &w.daemon_spec.both {
        assert_eq!(ran.code, 0, "{}", ran.said());
    }
    assert_eq!(daemons_logged(w), 1, "{}", daemon_log_text(w));
}

#[then("each session's record is its own project's, in the one store her daemon keeps")]
fn records_in_one_store(w: &mut World) {
    let home = w.common.project.home().join(".ctxloom");
    assert!(
        files::exists(home.join("ctxloom.db")),
        "the one store is the daemon's"
    );
    assert!(no_project_store(w), "no project keeps a store of its own");
    let first = w.common.project.dir().to_path_buf();
    let second = w.daemon_spec.second.clone().expect("a second project");
    for dir in [&first, &second] {
        let harp = tui_record(w, dir, "tui-harp").trim().to_string();
        assert!(
            files::is_dir(dir.join(".ctxloom/sessions").join(&harp)),
            "{harp}'s directory is its own project's"
        );
    }
}

#[then("each project lists its own session, and not the other's")]
fn own_listing(w: &mut World) {
    let first = w.common.project.dir().to_path_buf();
    let second = w.daemon_spec.second.clone().expect("a second project");
    let (a, b) = (listed_in(w, &first), listed_in(w, &second));
    let harp = |dir: &std::path::Path| tui_record(w, dir, "tui-harp").trim().to_string();
    let (harp_a, harp_b) = (harp(&first), harp(&second));
    assert!(
        a.iter().any(|n| n.ends_with(&harp_a)),
        "{a:?} lists {harp_a}"
    );
    assert!(
        !a.iter().any(|n| n.ends_with(&harp_b)),
        "{a:?} lists {harp_b}"
    );
    assert!(
        b.iter().any(|n| n.ends_with(&harp_b)),
        "{b:?} lists {harp_b}"
    );
    assert!(
        !b.iter().any(|n| n.ends_with(&harp_a)),
        "{b:?} lists {harp_a}"
    );
}

#[then("her assistant ran on her terminal exactly as her daemon prepared it")]
fn ran_as_prepared(w: &mut World) {
    w.ran().succeeds();
    // The capture stub records what its hooks need: each is the launch's.
    let launch = frontend_launch(w);
    let recorded = files::read(assistant_dir(w).join("engine-env")).unwrap_or_default();
    assert!(
        !recorded.is_empty(),
        "the assistant ran: {}",
        w.ran().said()
    );
    for line in recorded.lines() {
        let (name, value) = line.split_once('=').expect("a variable");
        assert_eq!(
            launch["env"][name].as_str(),
            Some(value),
            "{name} is the launch's"
        );
    }
    let cwd = files::read(assistant_dir(w).join("engine-cwd")).unwrap_or_default();
    assert_eq!(Some(cwd.trim()), launch["cwd"].as_str(), "its directory");
}

#[then(
    "the command she typed recorded nothing itself: her daemon recorded the session and prepared its session home"
)]
fn frontend_recorded_nothing(w: &mut World) {
    let launch = frontend_launch(w);
    let home = launch["session_home"].as_str().expect("its session home");
    assert!(files::is_dir(home), "the daemon prepared {home}");
    assert!(
        files::exists(PathBuf::from(home).join(".claude.json")),
        "its session home holds what the daemon seeded"
    );
    // The session is the daemon's record: listed through it, by its name.
    let harp = w.harp();
    let names = listed_in(w, w.common.project.dir());
    assert!(names.iter().any(|n| n.ends_with(&harp)), "{names:?}");
    assert!(
        no_project_store(w),
        "the command opened no store of its own"
    );
}

#[then(
    "the session's transcript holds what her assistant's hooks delivered to her daemon, each once"
)]
fn transcript_via_daemon(w: &mut World) {
    let harp = w.harp();
    let shown = run_now(
        w,
        &["session", "transcript", "show", &harp, "--format", "json"],
    );
    assert_eq!(shown.code, 0, "{}", shown.said());
    let prompts = shown
        .stdout
        .matches("Run the shell command: echo hi")
        .count();
    assert_eq!(
        prompts, 1,
        "the prompt is in the transcript once: {}",
        shown.stdout
    );
}

#[then("her assistant ran under her daemon, not under the command she typed")]
fn ran_under_daemon(w: &mut World) {
    ran_under_the_daemon(w);
}

#[then("her daemon stopped her assistant, and it was gone before the command exited")]
fn daemon_stopped_it(w: &mut World) {
    asked_and_gone(w);
}

#[then("ctxloom exited 130, saying the run was stopped by SIGINT")]
fn exited_sigint(w: &mut World) {
    exited_stopped(w, 130, "SIGINT");
}

#[then(regex = r#"^"([^"]+)" is listed as a child of "([^"]+)", run by her daemon$"#)]
fn listed_child(w: &mut World, child: String, owner: String) {
    let shown_child = until(w, "the child minted", DAEMON_WAIT, |w| {
        let ran = run_now(w, &["session", "show", &child, "--format", "json"]);
        (ran.code == 0).then(|| ran.json())
    });
    assert_eq!(
        shown_child["owner"],
        shown(w, &owner)["name"],
        "{shown_child}"
    );
    until(w, "the child hosted by her daemon", DAEMON_WAIT, |w| {
        let status = run_now(w, &["daemon", "status", "--format", "json"]);
        status.stdout.contains(&child).then_some(())
    });
}

#[then(regex = r#"^"([^"]+)"'s first turn was "([^"]*)"$"#)]
fn first_turn(w: &mut World, _child: String, text: String) {
    turn_on(w, &text);
    assert!(
        turns(w)[0].1.contains(&text),
        "its first turn: {:?}",
        turns(w)
    );
}

#[then(regex = r#"^"([^"]+)"'s next turn was "([^"]*)"$"#)]
fn next_turn(w: &mut World, child: String, text: String) {
    turn_on(w, &text);
    // Its own turn (step 9: several children at once).
    if files::exists(child_rec(w, &child)) {
        until(
            w,
            &format!("{child}'s turn on {text:?}"),
            DAEMON_WAIT,
            |w| {
                child_turns(w, &child)
                    .iter()
                    .any(|(_, line)| line.contains(&text))
                    .then_some(())
            },
        );
    }
}

#[then(regex = r#"^"([^"]+)" is listed as closed, and its assistant has exited$"#)]
fn listed_closed(w: &mut World, child: String) {
    child_is_gone(w, &child, false);
    if files::exists(child_rec(w, &child)) {
        until(w, &format!("{child}'s engine exited"), DAEMON_WAIT, |w| {
            files::exists(child_rec(w, &child).join("child-exited")).then_some(())
        });
    }
}

#[then(regex = r#"^nothing "([^"]+)" was launched with is left at rest$"#)]
fn nothing_at_rest(w: &mut World, _child: String) {
    until(w, "the child's hand-off gone", DAEMON_WAIT, |w| {
        hand_offs(w).is_empty().then_some(())
    });
}

/// The child `child` is closed and its engine exited; with `hand_off`, its
/// hand-off gone as well.
fn child_is_gone(w: &World, child: &str, hand_off: bool) {
    until(w, "the child closed", DAEMON_WAIT, |w| {
        (shown(w, child)["closed"] == Value::Bool(true)).then_some(())
    });
    until(w, "the child's engine exited", DAEMON_WAIT, |w| {
        files::exists(stub_dir(w).join("child-exited")).then_some(())
    });
    if hand_off {
        until(w, "the child's hand-off gone", DAEMON_WAIT, |w| {
            hand_offs(w).is_empty().then_some(())
        });
    }
}

#[then("a new ctxloom daemon is serving for Alice")]
fn new_daemon(w: &mut World) {
    let old = w
        .daemon_spec
        .before_kill
        .clone()
        .expect("a daemon was killed");
    assert!(serving_now(w), "{}", daemon_log_text(w));
    let now = discovery_now(w);
    assert_ne!(now.pid, old.pid, "another daemon: {}", daemon_log_text(w));
}

#[then(regex = r#"^"([^"]+)" is listed as live, and its assistant never stopped$"#)]
fn still_live(w: &mut World, harp: String) {
    assert_eq!(listed_state(w, &harp).as_deref(), Some("LIVE"));
    let assistant = w.daemon_spec.run.as_mut().expect("her assistant");
    assert!(
        matches!(assistant.try_wait(), Ok(None)),
        "her assistant never stopped"
    );
}

#[then(
    regex = r#"^"([^"]+)" found the new daemon and delivered again what the old one had not answered, and the record holds each once$"#
)]
fn redelivered(w: &mut World, harp: String) {
    for ran in &w.daemon_spec.hooks {
        assert_eq!(ran.code, 0, "{}", ran.said());
    }
    let events = session_events(w, &harp);
    assert_eq!(count_of(&events, "PromptSubmitted"), 1, "{events:?}");
    assert_eq!(count_of(&events, "TurnEnded"), 1, "{events:?}");
}

#[then(
    regex = r#"^"([^"]+)" is working again in its own conversation, with what it was launched with$"#
)]
fn rehosted(w: &mut World, child: String) {
    assert_eq!(child, CHILD, "the child the harness hosts");
    let starts = started_again(w);
    assert!(starts[1].contains("--resume S1"), "{starts:?}");
    let env = until(w, "the child's environment", DAEMON_WAIT, |w| {
        files::read(stub_dir(w).join("child-env"))
            .ok()
            .filter(|env| env.contains("ANTHROPIC_API_KEY=a-key")) // gitleaks:allow
    });
    assert!(env.contains("ANTHROPIC_API_KEY=a-key")); // gitleaks:allow
}

#[then("her daemon has exited, and nothing says it serves")]
fn her_daemon_exited(w: &mut World) {
    daemon_exited(w);
}

#[then("it proves itself with a new certificate, and knows her by a new credential")]
fn new_credentials(w: &mut World) {
    // The daemon that command started, as read while it served (the step
    // before), never re-read here, where it may have idled out.
    w.daemon_spec.new = w.daemon_spec.first.clone();
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
        status.json()["daemons"][0]["pid"],
        Value::from(new.pid),
        "{}",
        status.said()
    );
}

/// What a store's opening leaves behind, as the harness can see it: each
/// store file's size and modification time, whether its `-wal` and `-shm`
/// are there (an open makes them, a read's may stay), and the sessions'
/// directories in the project (a session set up for an engine).
/// One store file: its path, its size and modification time (none when it
/// is not there), and whether its `-wal` and `-shm` are there.
type StoreFile = (PathBuf, Option<(u64, std::time::SystemTime)>, bool, bool);

#[derive(Clone, Debug, PartialEq)]
pub(super) struct StoreEvidence {
    stores: Vec<StoreFile>,
    sessions: Vec<PathBuf>,
}

/// The stores' evidence now: the one store and every per-project one.
fn store_evidence(w: &World) -> StoreEvidence {
    let home = w.common.project.home();
    let mut dbs = vec![home.join(".ctxloom/ctxloom.db")];
    dbs.extend(
        files::children(home.join(".ctxloom/projects"))
            .into_iter()
            .map(|dir| dir.join("ctxloom.db")),
    );
    let side = |db: &PathBuf, suffix: &str| {
        let mut name = db.as_os_str().to_owned();
        name.push(suffix);
        files::exists(PathBuf::from(name))
    };
    let stores = dbs
        .iter()
        .map(|db| {
            (
                db.clone(),
                files::size_and_modified(db),
                side(db, "-wal"),
                side(db, "-shm"),
            )
        })
        .collect();
    let mut sessions = files::children(w.common.project.dir().join(".ctxloom/sessions"));
    sessions.sort();
    StoreEvidence { stores, sessions }
}

/// Before the scenario's command: the stores' evidence, so a later step
/// can tell what the command itself opened from what the Given made.
pub(super) fn before_run(w: &mut World) {
    w.daemon_spec.store_before = Some(store_evidence(w));
}

// The command opened no store and started no engine: what the Given left
// (a seeded session's store, its directory) is as it was, and nothing new
// is there (a store that was not there is still not).
#[then("no project store was opened, and no engine was started")]
fn nothing_opened(w: &mut World) {
    let before = w
        .daemon_spec
        .store_before
        .clone()
        .expect("the stores were looked at before the command ran");
    let now = store_evidence(w);
    assert_eq!(
        now.stores,
        before.stores,
        "the command opened a store: {}",
        w.common.ran().said()
    );
    assert_eq!(
        now.sessions, before.sessions,
        "the command set a session up, so an engine may have started"
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
    let new = w.daemon_spec.new.as_ref().expect("another daemon started");
    assert_ne!(new.pid, old.pid, "another daemon");
    assert_ne!(new.cert, old.cert, "another certificate");
    assert_ne!(new.bearer, old.bearer, "another bearer");
}

#[then("a client still holding the old bearer is refused")]
fn old_bearer_refused(w: &mut World) {
    let old = w.daemon_spec.old.clone().expect("a daemon was stopped");
    let new = w.daemon_spec.new.clone().expect("another daemon started");
    let stale = run_ports::Discovery {
        bearer: old.bearer,
        ..new
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

/// The warnings the hook said on stderr: one JSON envelope each.
fn hook_warnings(w: &World) -> Vec<Value> {
    serde_json::Deserializer::from_str(&fired(w).ran.stderr)
        .into_iter::<Value>()
        .map(|w| w.expect("stderr holds JSON warnings only"))
        .collect()
}

#[then("the hook exits 0, and says nothing was left unrecorded")]
fn hook_delivered(w: &mut World) {
    let fired = fired(w);
    assert_eq!(fired.ran.code, 0, "{}", fired.ran.said());
    assert!(
        hook_warnings(w).is_empty(),
        "{}; the daemon's log:\n{}",
        fired.ran.said(),
        daemon_log_text(w)
    );
}

#[then("a ctxloom daemon serves for this home, started by that hook")]
fn started_by_hook(w: &mut World) {
    assert!(serving_now(w), "{}", daemon_log_text(w));
    assert_eq!(daemons_logged(w), 1, "{}", daemon_log_text(w));
}

/// The session's transcript, as `session transcript show` gives it.
fn transcript_lines(w: &World, harp: &str) -> Vec<Value> {
    let ran = run_now(
        w,
        &["session", "transcript", "show", harp, "--format", "json"],
    );
    assert_eq!(ran.code, 0, "{}", ran.said());
    ran.stdout
        .lines()
        .filter(|l| !l.trim().is_empty())
        .map(|l| serde_json::from_str(l).unwrap_or_else(|e| panic!("{l:?}: {e}")))
        .collect()
}

/// Where in `lines` the fixture's prompt is, each place it is.
fn prompt_lines(lines: &[Value]) -> Vec<usize> {
    lines
        .iter()
        .enumerate()
        .filter(|(_, l)| l["prompt"]["text"] == PROMPT)
        .map(|(n, _)| n)
        .collect()
}

#[then("the session's transcript holds the prompt, once")]
fn prompt_once(w: &mut World) {
    let harp = fired(w).harp.clone();
    let lines = transcript_lines(w, &harp);
    assert_eq!(prompt_lines(&lines).len(), 1, "{lines:#?}");
}

#[then("the session's transcript holds the prompt, then the turn's end, each once")]
fn prompt_then_turn_end(w: &mut World) {
    let harp = fired(w).harp.clone();
    let lines = transcript_lines(w, &harp);
    let prompts = prompt_lines(&lines);
    assert_eq!(prompts.len(), 1, "{lines:#?}");
    let ends = lines[prompts[0]..]
        .iter()
        .filter(|l| kind_of(l) == "turn_end")
        .count();
    assert_eq!(ends, 1, "the turn's end after the prompt: {lines:#?}");
}

#[then("the session's transcript holds each of the ten pieces, once")]
fn ten_pieces_once(w: &mut World) {
    let harp = fired(w).harp.clone();
    let lines = transcript_lines(w, &harp);
    for index in 0..10 {
        let found = lines
            .iter()
            .filter(|l| {
                kind_of(l) == "words"
                    && count(&l["words"]["index"]) == index
                    && l["words"]["text"] == format!("piece {index}")
            })
            .count();
        assert_eq!(found, 1, "piece {index}: {lines:#?}");
    }
}

/// The one gap line of `lines`, after the prompt's.
fn the_gap(lines: &[Value]) -> Value {
    let prompts = prompt_lines(lines);
    assert_eq!(prompts.len(), 1, "the prompt, once: {lines:#?}");
    let gaps: Vec<usize> = (0..lines.len())
        .filter(|n| kind_of(&lines[*n]) == "gap")
        .collect();
    assert_eq!(gaps.len(), 1, "one gap: {lines:#?}");
    assert!(gaps[0] > prompts[0], "the gap after the prompt: {lines:#?}");
    lines[gaps[0]].clone()
}

#[then(
    "the session's transcript holds the prompt, then a gap where the turn's end was dropped, each once"
)]
fn prompt_then_gap(w: &mut World) {
    let harp = fired(w).harp.clone();
    let lines = transcript_lines(w, &harp);
    let gap = the_gap(&lines);
    assert_no_envelope(&gap);
    assert!(
        gap["gap"]["dropped"]
            .as_str()
            .is_some_and(|d| d.starts_with("hook:turn-end:")),
        "the turn's end was dropped: {gap}"
    );
    assert!(
        !lines.iter().any(|l| kind_of(l) == "turn_end"),
        "the turn's end itself was never recorded: {lines:#?}"
    );
}

#[then("the gap says when the turn's end was dropped, and that the hold was at its bound")]
fn gap_says_when_and_why(w: &mut World) {
    let harp = fired(w).harp.clone();
    let gap = the_gap(&transcript_lines(w, &harp));
    assert_eq!(gap["gap"]["why"], "bound", "{gap}");
    let at = gap["gap"]["dropped_at"]
        .as_str()
        .unwrap_or_default()
        .to_string();
    // The hook's clock when it dropped it: an RFC 3339 instant (its fraction
    // kept), never the epoch, no later than the gap was recorded.
    assert!(at.ends_with('Z') && at.len() >= 20, "{at}");
    let year: u64 = at[..4].parse().expect("a year");
    assert!(year >= 2026, "{at}");
    let recorded = gap["time"].as_str().unwrap_or_default();
    let second = |instant: &str| {
        instant
            .trim_end_matches('Z')
            .split('.')
            .next()
            .unwrap_or_default()
            .to_string()
    };
    assert!(
        second(&at) <= second(recorded),
        "dropped {at}, recorded {recorded}"
    );
}

#[then(
    "once a ctxloom daemon can be started and serves, the next command records the prompt in the session's transcript, once"
)]
fn once_startable_next_command_records(w: &mut World) {
    files::remove(daemon_dir(w));
    w.common
        .set_env(IDLE_VARIABLE, SPAWNED_IDLE_SECONDS.to_string());
    // The next command: one that reaches the daemon (the transcript is read
    // in process, and reaches none).
    let next = run_now(w, &["session", "list", "--format", "json"]);
    assert_eq!(next.code, 0, "{}", next.said());
    assert!(holds(w).is_empty(), "{:?}", holds(w));
    let harp = fired(w).harp.clone();
    let lines = transcript_lines(w, &harp);
    assert_eq!(prompt_lines(&lines).len(), 1, "{lines:#?}");
}

#[then("the hook exits 0, and its answer to the engine is unchanged")]
fn hook_fails_open(w: &mut World) {
    let fired = fired(w);
    assert_eq!(fired.ran.code, 0, "{}", fired.ran.said());
    assert_eq!(
        fired.ran.stdout, fired.unrecorded_answer,
        "the same answer as a hook that records nothing"
    );
}

/// The hook warned `code` for the prompt: an envelope of that code whose
/// fact is the prompt's.
fn warned_for_prompt(w: &World, code: &str) -> Value {
    let warnings = hook_warnings(w);
    let found = warnings
        .iter()
        .find(|w| {
            w["code"] == code
                && w["args"]["what"]
                    .as_str()
                    .is_some_and(|what| what.starts_with("hook:prompt:"))
        })
        .unwrap_or_else(|| panic!("no {code} for the prompt: {warnings:#?}"));
    assert_eq!(
        warnings.len(),
        1,
        "one warning, the prompt's: {warnings:#?}"
    );
    found.clone()
}

#[then(expr = "the hook warns {string} for the prompt, within its bound")]
fn hook_warns(w: &mut World, code: String) {
    warned_for_prompt(w, &code);
    let took = fired(w).took;
    assert!(
        took <= HOOK_BOUND + PROCESS_SLACK,
        "the hook took {took:?}, past its bound"
    );
}

#[then(expr = "the hook warns {string} for the prompt")]
fn hook_warns_for_prompt(w: &mut World, code: String) {
    warned_for_prompt(w, &code);
}

#[then(expr = "the hook warns {string} for the prompt, saying the hold is at its bound")]
fn hook_warns_bound(w: &mut World, code: String) {
    let warning = warned_for_prompt(w, &code);
    let why = warning["args"]["why"]
        .as_str()
        .unwrap_or_default()
        .to_string();
    assert!(why.contains("is at its bound"), "{warning}");
}

/// The holds' directory of the scenario's home.
fn held_dir(w: &World) -> PathBuf {
    w.common.project.home().join(".ctxloom/held")
}

/// The holds in the scenario's home, but the placeholder of a full home.
fn holds(w: &World) -> Vec<PathBuf> {
    files::children(held_dir(w))
        .into_iter()
        .filter(|p| p.extension().is_some_and(|e| e == "held"))
        .filter(|p| p.file_stem().is_some_and(|s| s != FULL_HOME_HOLD))
        .collect()
}

#[then(expr = "the prompt is held for {string}, in a file only its owner can read")]
fn prompt_held(w: &mut World, harp: String) {
    assert_eq!(fired(w).harp, harp);
    let holds = holds(w);
    assert_eq!(holds.len(), 1, "one hold, the session's: {holds:?}");
    let bytes = files::read_bytes(&holds[0]).expect("the hold");
    let text = String::from_utf8_lossy(&bytes);
    assert!(
        text.contains("hook:prompt:") && text.contains(PROMPT),
        "the prompt's fact is held"
    );
    if let Some(mode) = files::mode_of(&holds[0]) {
        assert_eq!(mode & 0o777, 0o600, "{:o}", mode);
        assert_eq!(files::mode_of(held_dir(w)).map(|m| m & 0o777), Some(0o700));
    }
}

#[then(expr = "nothing is held for {string}")]
fn nothing_held(w: &mut World, harp: String) {
    assert_eq!(fired(w).harp, harp);
    let left = holds(w);
    // A flush is bounded, and what it could not deliver stays held, unsaid;
    // so a hold left behind is judged with what settles why: how much of it
    // is left, how much the store took, and the command's own words.
    assert!(left.is_empty(), "{}", why_held(w, &harp, &left));
}

/// What a hold left after a command says of itself: each file's size and
/// the facts it still holds, the session's events recorded since the hook,
/// the last command's words and the daemon's log.
fn why_held(w: &World, harp: &str, left: &[PathBuf]) -> String {
    let files: Vec<String> = left
        .iter()
        .map(|p| {
            let text = files::read_bytes(p)
                .map(|b| String::from_utf8_lossy(&b).to_string())
                .unwrap_or_default();
            format!(
                "{}: {} bytes, {} facts",
                p.display(),
                text.len(),
                text.matches("hook:").count()
            )
        })
        .collect();
    format!(
        "held: {files:?}; events recorded since the hooks: {}; the command said: {}; the daemon logged: {}",
        session_events(w, harp).len().saturating_sub(fired(w).events_before),
        w.common.ran().said(),
        daemon_log_text(w)
    )
}

#[then(expr = "nothing was recorded for {string}")]
fn nothing_recorded(w: &mut World, harp: String) {
    let before = fired(w).events_before;
    assert_eq!(
        session_events(w, &harp).len(),
        before,
        "the session's stream is as it was before the hook"
    );
}

/// Every stream of the one store, as `ctxloom test record` prints it.
fn record(w: &World) -> Vec<Value> {
    let ran = run_now(w, &["test", "record"]);
    assert_eq!(ran.code, 0, "{}", ran.said());
    ran.json().as_array().cloned().unwrap_or_default()
}

/// The events of the stream of `domain` and `root` in `record`.
fn stream_events<'a>(record: &'a [Value], domain: &str, root: &str) -> &'a [Value] {
    record
        .iter()
        .find(|s| s["domain"] == domain && s["root"] == root)
        .and_then(|s| s["events"].as_array())
        .map_or(&[], Vec::as_slice)
}

/// The root of the session `harp` (its mint names it) in `record`.
fn session_root(record: &[Value], harp: &str) -> Option<String> {
    record
        .iter()
        .filter(|s| s["domain"] == "sessions")
        .find(|s| {
            s["events"].as_array().is_some_and(|events| {
                events
                    .iter()
                    .any(|e| e["type"] == "SessionMinted" && e["harp"] == harp)
            })
        })
        .and_then(|s| s["root"].as_str().map(str::to_string))
}

/// The events of the run of the session `root`'s `incarnation`.
fn run_events_of(record: &[Value], session: &str, incarnation: u64) -> Vec<Value> {
    record
        .iter()
        .filter(|s| s["domain"] == "runs")
        .find(|s| {
            s["events"].as_array().is_some_and(|events| {
                events.iter().any(|e| {
                    e["type"] == "RunEnqueued"
                        && e["session"] == session
                        && e["incarnation"] == incarnation
                })
            })
        })
        .and_then(|s| s["events"].as_array().cloned())
        .unwrap_or_default()
}

#[then("no run is left owed a runner, no incarnation is left open and no fact is recorded twice")]
fn record_whole(w: &mut World) {
    let record = record(w);
    for stream in &record {
        let events = stream["events"].as_array().cloned().unwrap_or_default();
        let mut seen = std::collections::BTreeSet::new();
        for event in &events {
            assert!(
                seen.insert((event["type"].to_string(), event["payload"].to_string())),
                "recorded twice on {} {}: {event}",
                stream["domain"],
                stream["root"]
            );
        }
        let has = |kind: &str| events.iter().any(|e| e["type"] == kind);
        if stream["domain"] == "runs" && has("RunEnqueued") {
            assert!(has("RunEnded"), "a run left owed or unended: {stream}");
        }
        if stream["domain"] == "sessions" {
            let numbers = |kind: &str| -> std::collections::BTreeSet<u64> {
                events
                    .iter()
                    .filter(|e| e["type"] == kind)
                    .filter_map(|e| e["incarnation"].as_u64())
                    .collect()
            };
            let open: Vec<u64> = numbers("IncarnationOwed")
                .difference(&numbers("IncarnationEnded"))
                .copied()
                .collect();
            assert!(open.is_empty(), "incarnations {open:?} left open: {stream}");
        }
    }
}

// ------------------------------------------- then: cli/session_children

#[then(expr = "the child {string} of {string} ran its first turn on {string}")]
fn child_first_turn(w: &mut World, child: String, owner: String, text: String) {
    turn_on(w, &text);
    let shown_child = until(w, "the child minted", DAEMON_WAIT, |w| {
        let ran = run_now(w, &["session", "show", &child, "--format", "json"]);
        (ran.code == 0).then(|| ran.json())
    });
    assert_eq!(
        shown_child["owner"],
        shown(w, &owner)["name"],
        "{shown_child}"
    );
}

#[then("the child's hand-off file is readable by its owner alone while the child is open")]
fn hand_off_private(w: &mut World) {
    let child = until(w, "the child minted", DAEMON_WAIT, |w| {
        let ran = run_now(
            w,
            &["session", "show", "brisk-copper-moth", "--format", "json"],
        );
        (ran.code == 0).then(|| ran.json())
    });
    assert_ne!(child["closed"], Value::Bool(true), "open: {child}");
    // The hand-off itself, not its mark in launch (claimed by its launch).
    let kept: Vec<PathBuf> = hand_offs(w)
        .into_iter()
        .filter(|path| path.extension().is_none())
        .collect();
    let [path] = kept.as_slice() else {
        panic!("one hand-off: {kept:?}");
    };
    assert_eq!(
        path.file_name().and_then(|n| n.to_str()),
        child["uid"].as_str(),
        "named by the child's stable id"
    );
    if let Some(mode) = files::mode_of(path) {
        assert_eq!(mode, 0o600, "{}", path.display());
    }
}

#[then("no hand-off file was left behind")]
fn no_hand_off(w: &mut World) {
    until(w, "no hand-off left", DAEMON_WAIT, |w| {
        hand_offs(w).is_empty().then_some(())
    });
}

#[then(regex = r#"^the child ran a turn on "([^"]*)"$"#)]
fn child_ran_turn(w: &mut World, text: String) {
    turn_on(w, &text);
}

#[then(regex = r#"^the child ran no turn, and lists "([^"]*)" as waiting for one$"#)]
fn child_waits(w: &mut World, text: String) {
    let child = shown(w, "brisk-copper-moth");
    let pending: Vec<&str> = child["pending"]
        .as_array()
        .map(|pending| pending.iter().filter_map(|m| m["text"].as_str()).collect())
        .unwrap_or_default();
    assert!(pending.contains(&text.as_str()), "pending: {child}");
    assert!(
        !turns(w).iter().any(|(_, line)| line.contains(&text)),
        "no turn ran on it: {:?}",
        turns(w)
    );
}

#[then(expr = "the child's turn in flight was interrupted, and its next turn was {string}")]
fn child_steered(w: &mut World, text: String) {
    until(w, "the turn in flight interrupted", DAEMON_WAIT, |w| {
        files::exists(stub_dir(w).join("interrupted")).then_some(())
    });
    turn_on(w, &text);
    let taken = turns(w);
    assert!(
        taken.last().is_some_and(|(_, line)| line.contains(&text)),
        "its next turn: {taken:?}"
    );
}

#[then(expr = "the child ran one turn on {string}")]
fn child_one_turn(w: &mut World, text: String) {
    turn_on(w, &text);
    let events = session_events(w, "brisk-copper-moth");
    let sent: Vec<&Value> = events
        .iter()
        .filter(|e| e["type"] == "MessageSent" && e["text"] == text.as_str())
        .collect();
    assert_eq!(sent.len(), 1, "one message: {events:?}");
    let on: Vec<_> = turns(w)
        .into_iter()
        .filter(|(_, line)| line.contains(&text))
        .collect();
    assert_eq!(on.len(), 1, "one turn: {on:?}");
}

#[then("the child's next turn runs in a new conversation of the same session")]
fn child_cleared(w: &mut World) {
    let before = turns(w)
        .first()
        .map(|(sid, _)| sid.clone())
        .expect("its first turn");
    let uid = shown(w, "brisk-copper-moth")["uid"].clone();
    until(w, "the clear's new conversation bound", DAEMON_WAIT, |w| {
        let native = shown(w, "brisk-copper-moth")["native_session"].clone();
        (native.as_str().is_some_and(|n| n != before)).then_some(())
    });
    let sent = run_now(
        w,
        &[
            "session",
            "send",
            "brisk-copper-moth",
            "wake",
            "after the clear",
        ],
    );
    assert_eq!(sent.code, 0, "{}", sent.said());
    let (sid, _) = turn_on(w, "after the clear");
    assert_ne!(sid, before, "a new conversation: {:?}", turns(w));
    assert_eq!(
        shown(w, "brisk-copper-moth")["uid"],
        uid,
        "the same session"
    );
}

#[then(
    regex = r#"^the child "([^"]+)" is listed as closed, its engine has exited and its hand-off file is gone$"#
)]
fn child_ended(w: &mut World, child: String) {
    child_is_gone(w, &child, true);
}

#[then("the assistant was a child of the daemon, and its session's lock was the daemon's")]
fn one_shot_hosted(w: &mut World) {
    ran_under_the_daemon(w);
    // The command typed is gone: whoever held the lock while the assistant
    // ran was the daemon, its parent.
    let probe = recorded(w, "lock-probe");
    assert!(
        probe == "held" || probe == "unknown",
        "the session's lock was not held while the assistant ran: {probe}"
    );
}

#[then("the daemon asked the assistant to stop, and it was gone before the command exited")]
fn daemon_asked_stop(w: &mut World) {
    asked_and_gone(w);
}

#[then(
    regex = r"^the command exits 128 plus the signal's number, (\d+), saying the run was stopped by (SIG[A-Z]+)$"
)]
fn exits_by_signal(w: &mut World, code: String, signal: String) {
    exited_stopped(w, code.parse().expect("a code"), &signal);
}

#[then(
    "the assistant was started with the program, arguments, environment and directory of the daemon's launch, and nothing else"
)]
fn started_as_launched(w: &mut World) {
    let project = w.common.project.dir().to_path_buf();
    started_exactly_as_launched(w, &project);
}

#[then(
    "the command took the session's lock before the assistant started and let it go after it exited"
)]
fn frontend_lock(w: &mut World) {
    // The command typed held it while the TUI ran (the stub probed it), and
    // let it go: the session no longer reads live.
    let probe = recorded(w, "lock-probe");
    assert_eq!(
        probe, "held",
        "the session's lock was not held while the TUI ran"
    );
    let launch = frontend_launch(w);
    let lock = launch["session_lock"].as_str().expect("its lock");
    assert!(
        lock.ends_with(&format!("/{}/session.lock", w.harp())),
        "the launch names the session's lock: {lock}"
    );
    assert!(
        !procs::lock_held(std::path::Path::new(lock)),
        "the lock was let go after the TUI exited"
    );
}

#[then("the daemon recorded the assistant's start and its exit, each once")]
fn start_and_exit(w: &mut World) {
    let events = run_events(w, &w.harp());
    let count = |kind: &str| events.iter().filter(|e| *e == kind).count();
    assert_eq!(count("RunnerStartedRecorded"), 1, "{events:?}");
    assert_eq!(count("RunEnded"), 1, "{events:?}");
}

#[then("no session was minted")]
fn no_mint(w: &mut World) {
    assert!(
        !files::exists(w.common.project.dir().join(".ctxloom/sessions")),
        "no session directory was made"
    );
    // The store as the Given left it (step 10c U6: an init before is the
    // daemon's, which opened it) is as it was, so nothing was minted in it.
    let before = w
        .daemon_spec
        .store_before
        .clone()
        .expect("the stores were looked at before the command ran");
    assert_eq!(
        store_evidence(w).stores,
        before.stores,
        "the store was opened, so a session may have been minted in it"
    );
}

#[then("the assistant was never stopped")]
fn never_stopped(w: &mut World) {
    let project = w.common.project.dir().to_path_buf();
    assert_eq!(
        tui_record(w, &project, "asked"),
        "",
        "the TUI was asked to stop"
    );
    assert!(
        files::exists(
            assistant_dir(w)
                .join(project.file_name().unwrap())
                .join("tui-started")
        ),
        "the TUI ran"
    );
}

#[then("the command started a new daemon, and reported the assistant's exit to it once")]
fn reported_to_new(w: &mut World) {
    let killed = w.daemon_spec.killed_pid.expect("a daemon was killed");
    let now = discovery_now(w);
    assert_ne!(now.pid, killed, "another daemon advertises itself");
    let events = run_events(w, &w.harp());
    let ended = events.iter().filter(|e| *e == "RunEnded").count();
    assert_eq!(ended, 1, "the exit was recorded once: {events:?}");
    assert!(
        !events.iter().any(|e| e == "RunnerLostRecorded"),
        "the run was not swept as lost: {events:?}"
    );
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

/// The child the harness hosts, and its owner.
const CHILD: &str = "brisk-copper-moth";
const OWNER: &str = "amber-quiet-heron";

/// The child's session's root in `record`.
fn child_root(record: &[Value]) -> String {
    session_root(record, CHILD).expect("the child is recorded")
}

#[then(
    "the run the killed daemon hosted ended lost, and its turn in flight ended failed, with its owner told"
)]
fn lost_and_told(w: &mut World) {
    let record = record(w);
    let run = run_events_of(&record, &child_root(&record), 1);
    let of = |kind: &str, field: &str| -> Vec<String> {
        run.iter()
            .filter(|e| e["type"] == kind)
            .map(|e| e[field].as_str().unwrap_or_default().to_string())
            .collect()
    };
    assert_eq!(of("RunEnded", "cause"), ["RunnerLoss"], "{run:?}");
    assert_eq!(of("TurnFailedRecorded", "class"), ["RunnerLost"], "{run:?}");
    until(
        w,
        "the owner told its child's turn failed",
        DAEMON_WAIT,
        |w| {
            pending_text(w, OWNER)
                .iter()
                .any(|t| t.contains(&format!("child {CHILD} failed its turn")))
                .then_some(())
        },
    );
}

/// The texts of the messages `harp` holds pending.
fn pending_text(w: &World, harp: &str) -> Vec<String> {
    shown(w, harp)["pending"]
        .as_array()
        .map(|pending| {
            pending
                .iter()
                .filter_map(|m| m["text"].as_str().map(str::to_string))
                .collect()
        })
        .unwrap_or_default()
}

/// The arguments of each start of the stub child, in order.
fn child_starts(w: &World) -> Vec<String> {
    files::read(stub_dir(w).join("child-args"))
        .unwrap_or_default()
        .lines()
        .map(str::to_string)
        .collect()
}

/// Wait for the child to be started again, resuming its conversation.
fn started_again(w: &World) -> Vec<String> {
    until(w, "the child started again", DAEMON_WAIT, |w| {
        let starts = child_starts(w);
        (starts.len() >= 2).then_some(starts)
    })
}

#[then(
    regex = r#"^"([^"]+)" runs again as its session's next incarnation, in its own conversation$"#
)]
fn next_incarnation(w: &mut World, child: String) {
    assert_eq!(child, CHILD, "the child the harness hosts");
    let starts = started_again(w);
    assert!(
        !starts[0].contains("--resume") && starts[1].contains("--resume S1"),
        "its own conversation resumed: {starts:?}"
    );
    until(w, "its next incarnation's run started", DAEMON_WAIT, |w| {
        let record = record(w);
        run_events_of(&record, &child_root(&record), 2)
            .iter()
            .any(|e| e["type"] == "RunnerStartedRecorded")
            .then_some(())
    });
}

/// The write the last command was (the daemon spec's retry rows): a
/// session's, as its event's type, a session's removal, or a library write
/// (step 10b U3), by its verb.
enum Written {
    Session(&'static str),
    Removal(String),
    Library(String),
}

fn session_write(w: &World) -> Written {
    let line = w.common.line.clone();
    if line.contains("session send") {
        Written::Session("MessageSent")
    } else if line.contains("session close") {
        Written::Session("SessionClosed")
    } else if line.contains("session keep") {
        Written::Session("KeptChanged")
    } else if line.contains("session rename") {
        Written::Session("TitleSet")
    } else if let Some(rest) = line.split("session remove ").nth(1) {
        Written::Removal(
            rest.split_whitespace()
                .next()
                .unwrap_or_default()
                .to_string(),
        )
    } else if writes::is_library_write(&line) {
        Written::Library(writes::verb_of(&line))
    } else {
        panic!("no write the harness knows was run: {line}")
    }
}

#[then("the command started a new daemon and sent the write once more, under the same id")]
fn retried_once(w: &mut World) {
    let written = session_write(w);
    for switch in [DIE_AFTER_APPLY, DIE_AT] {
        assert!(
            !files::exists(daemon_dir(w).join(switch)),
            "the daemon was never killed under the write (its switch {switch} is still there): {}",
            w.common.ran().said()
        );
    }
    let now = discovery_now(w);
    match (&written, w.daemon_spec.before_kill.clone()) {
        (_, Some(old)) => assert_ne!(now.pid, old.pid, "the retry was answered by a new daemon"),
        (Written::Session(_), None) => panic!("a daemon was killed"),
        // The command started the daemon that was killed: the switch was
        // taken, and the command succeeded, so another one answered it.
        _ => {}
    }
    if let Written::Library(verb) = written {
        let events = writes::library_events(w, false);
        // An export is recorded as made, under its request id (AIP-155).
        if verb == "bundle export" {
            let made = writes::made(&events, &verb);
            assert_eq!(made.len(), 1, "one export made: {events:?}");
            return;
        }
        let intents = writes::intents(&events, &verb);
        assert_eq!(intents.len(), 1, "one intent of {verb}: {events:?}");
        let request = &intents[0]["request_id"];
        assert!(
            events
                .iter()
                .any(|e| e["type"] == "StepApplied" && &e["request_id"] == request),
            "the retry under {request} finished the write: {events:?}"
        );
    }
}

#[then("the write was applied once, and its second delivery was a duplicate")]
fn applied_once(w: &mut World) {
    let kind = match session_write(w) {
        Written::Session(kind) => kind,
        Written::Removal(harp) => {
            let record = record(w);
            let intended = record
                .iter()
                .flat_map(|s| s["events"].as_array().cloned().unwrap_or_default())
                .filter(|e| e["type"] == "SessionRemovalIntended" && e["harp"] == harp.as_str())
                .count();
            assert_eq!(
                intended, 1,
                "one removal of {harp} was intended: {record:?}"
            );
            return;
        }
        Written::Library(verb) => {
            let events = writes::library_events(w, false);
            if verb == "bundle export" {
                assert_eq!(
                    writes::made(&events, &verb).len(),
                    1,
                    "one export made: {events:?}"
                );
                return;
            }
            assert_eq!(
                writes::intents(&events, &verb).len(),
                1,
                "one intent of {verb}: {events:?}"
            );
            return;
        }
    };
    let events = session_events(w, "brisk-copper-moth");
    // The write the command sent: a send's by its text (the launch's own
    // first message is another).
    let applied = events
        .iter()
        .filter(|e| e["type"] == kind)
        .filter(|e| kind != "MessageSent" || e["text"] == "check the tests")
        .count();
    assert_eq!(applied, 1, "{events:?}");
}

// ONE WRITER (owner ruling 2026-11-25): the daemon is the store's one writer.
// Under test-support its log says each command it takes through its
// coordinator, `handled <CommandType>`, as it says each service call,
// `served <path>`; these steps read those lines. Written first, red: the log
// does not say it yet, and a CLI process still opens the store for the
// session writes.

/// The commands the daemon's log says it handled, by type name, in order.
fn handled_commands(w: &World) -> Vec<String> {
    daemon_log_text(w)
        .lines()
        .filter_map(|line| line.split("handled ").nth(1))
        .map(|rest| {
            rest.split_whitespace()
                .next()
                .unwrap_or_default()
                .to_string()
        })
        .collect()
}

#[then(expr = "the daemon's log says it handled the command {string} once")]
fn handled_once(w: &mut World, command: String) {
    handled_n(w, command, 1);
}

#[then(expr = "the daemon's log says it handled the command {string} {int} times")]
fn handled_n(w: &mut World, command: String, times: usize) {
    let handled = handled_commands(w);
    assert_eq!(
        handled.iter().filter(|c| **c == command).count(),
        times,
        "the daemon handled {command} {times} times; its log says {handled:?}: {}",
        w.common.ran().said()
    );
}

#[then(expr = "the daemon's log says it served {string}")]
fn served_rpc(w: &mut World, rpc: String) {
    let served: Vec<String> = daemon_log_text(w)
        .lines()
        .filter_map(|line| line.split("served /ctxloom.v1.Ctxloom/").nth(1))
        .map(|rest| rest.trim().to_string())
        .collect();
    assert!(
        served.contains(&rpc),
        "the daemon served {rpc}; its log says {served:?}: {}",
        w.common.ran().said()
    );
}

#[then(expr = "the session {string} recorded {int} {string} events")]
fn recorded_events(w: &mut World, harp: String, count: usize, kind: String) {
    let events = session_events(w, &harp);
    assert_eq!(count_of(&events, &kind), count, "{events:?}");
}

/// Go's three session sidecars, as Go lays them out (session.test.rs's).
const GO_SIDECAR: &str = include_str!("../fixtures/go-sidecar/schema1.yaml");

#[given(expr = "Go's ctxloom left a session {string} in its home for this project")]
fn go_left_a_session(w: &mut World, harp: String) {
    assert_eq!(harp, "amber-swift-owl", "the fixture is schema 1's session");
    let work_root = files::canonical(w.common.project.dir());
    // The project is one the registry holds (its index names the id of the
    // directory the seed made, for this root): a project with a marker of
    // its own is bound only when a command binds it.
    w.before_run();
    let registry = w.common.project.home().join(".ctxloom/projects");
    if !files::exists(registry.join("index.yaml")) {
        let id = files::children(&registry)
            .into_iter()
            .filter(|dir| files::is_dir(dir))
            .find_map(|dir| dir.file_name().map(|n| n.to_string_lossy().to_string()))
            .unwrap_or_else(|| {
                panic!(
                    "the seeded session made its project's directory under {}: {:?}",
                    registry.display(),
                    files::children(w.common.project.home().join(".ctxloom"))
                )
            });
        files::put(
            registry.join("index.yaml"),
            format!(
                "schema_version: 1\nprojects:\n  - project_id: {id}\n    path: {}\n    created_at: 2026-01-01T00:00:00Z\n    last_seen_at: 2026-01-01T00:00:00Z\n",
                work_root.display()
            ),
        );
    }
    let body = GO_SIDECAR.replace("/work/proj", &work_root.display().to_string());
    files::put(
        w.common
            .project
            .home()
            .join(".ctxloom/sessions")
            .join(&harp)
            .join("session.yaml"),
        &body,
    );
}

/// The question a session removal's preview asks (step 10c U11), as the
/// daemon words its Confirm need.
fn removal_question(harp: &str) -> String {
    format!("Remove session \"{harp}\"?")
}

/// Whether `session show` still finds `harp` (else it names no session).
fn still_held(w: &World, harp: &str) -> bool {
    let ran = run_now(w, &["session", "show", harp, "--format", "json"]);
    match ran.code {
        0 => true,
        48 => false,
        _ => panic!("session show {harp}: {}", ran.said()),
    }
}

#[then("the daemon's confirmation was asked at her terminal, and the session is gone")]
fn asked_at_terminal(w: &mut World) {
    let ran = w.common.ran();
    assert_eq!(ran.code, 0, "{}", ran.said());
    let question = format!("{} [y/N] ", removal_question("amber-swift-owl"));
    assert!(
        ran.stderr.contains(&question),
        "asked {question:?}: {}",
        ran.said()
    );
    assert!(!still_held(w, "amber-swift-owl"), "the session is gone");
}

#[when("its orchestrator calls a ctxloom tool whose daemon call asks for confirmation")]
fn tool_asks(w: &mut World) {
    // The test build's tool (QUESTIONS.md 7): a session removal, whose
    // preview asks to confirm it, of a session that has ended.
    w.plant("amber-swift-owl", "finished");
    w.before_run();
    let session = json!({"session": "amber-swift-owl"});
    engine_calls(w, "amber-quiet-heron", "test_remove_session", session);
}

#[then(
    "the question reached its engine as an MCP elicitation, and the engine's answer reached the daemon"
)]
fn elicited(w: &mut World) {
    let asked = &w.daemon_spec.elicited;
    assert_eq!(asked.len(), 1, "one question: {asked:?}");
    assert_eq!(asked[0]["message"], removal_question("amber-swift-owl"));
    let answered = crate::tests::mcp_serve::answered_json(w.daemon_spec.tool.as_ref().unwrap());
    assert_eq!(answered["applied"], true, "{answered}");
    assert!(!still_held(w, "amber-swift-owl"), "the session is gone");
}

#[given(expr = "an interactive session {string} whose assistant is running on its terminal")]
fn primary_running(w: &mut World, harp: String) {
    primary_running_on(w, harp, &tui_stub());
}

/// [`primary_running`] over the stub `stub` (the claude of the TUI, and of
/// any child it launches).
fn primary_running_on(w: &mut World, harp: String, stub: &str) {
    // Its session recorded, its engine's own session bound, and resumed on
    // this terminal (`run --session`), the TUI stub left running.
    let dir = stub_claude(w, stub);
    w.daemon_spec.tui_dir = Some(dir.clone());
    let seeded = run_now(
        w,
        &[
            "test",
            "seed-session",
            &harp,
            "--ended-ago",
            "60",
            "--native-id",
            "native-1",
            "--resumable",
        ],
    );
    assert_eq!(seeded.code, 0, "{}", seeded.said());
    let env = tui_env(w);
    let mut child = w
        .common
        .project
        .start_with(&env_pairs(&env), &["run", "--session", &harp]);
    let project = w.common.project.dir().to_path_buf();
    let started = dir.join(project.file_name().unwrap()).join("tui-started");
    let deadline = Instant::now() + Duration::from_secs(60);
    while !files::exists(&started) {
        if let Ok(Some(_)) = child.try_wait() {
            let ran = crate::tests::Ran::of(child.wait_with_output().expect("it ran"));
            panic!("the session ended before its TUI started: {}", ran.said());
        }
        assert!(
            Instant::now() < deadline,
            "the TUI's start never came; the daemon's log:\n{}",
            daemon_log_text(w)
        );
        std::thread::sleep(Duration::from_millis(50));
    }
    w.daemon_spec.run = Some(child);
}

#[then("the assistant on its terminal was asked to stop, then killed once its grace had passed")]
fn terminal_stopped(w: &mut World) {
    w.ran().succeeds();
    // The frontend was told to stop its TUI: it asked (the stub said so,
    // and went on), then killed it once the grace had passed; the command
    // that ran the session then ended.
    let child = w.daemon_spec.run.take().expect("the session's command");
    let ran = crate::tests::Ran::of(child.wait_with_output().expect("it ends"));
    let project = w.common.project.dir().to_path_buf();
    assert_eq!(
        tui_record(w, &project, "asked").trim(),
        "asked",
        "the TUI was asked to stop: {}\n{}",
        ran.said(),
        daemon_log_text(w)
    );
    let stub = w
        .daemon_spec
        .tui_dir
        .clone()
        .unwrap_or_else(|| assistant_dir(w));
    assert!(
        !files::exists(stub.join("release")),
        "nothing released the TUI: it was killed"
    );
}

#[then(regex = r#"^"([^"]+)" is listed as closed$"#)]
fn closed_listed(w: &mut World, harp: String) {
    let shown = run_now(w, &["session", "show", &harp, "--format", "json"]);
    assert_eq!(shown.code, 0, "{}", shown.said());
    assert!(
        shown.stdout.contains("closed"),
        "{harp} is listed as closed: {}",
        shown.stdout
    );
}

#[given(
    expr = "a session {string} whose engine environment names ANTHROPIC_API_KEY through its engine block"
)]
fn owner_curated_env(w: &mut World, harp: String) {
    let dir = stub_claude(w, &child_stub());
    // Its engine block names the key, so its engine's environment holds it
    // (what its MCP server runs in).
    w.common.project.write_home(
        ".ctxloom/config.yaml",
        &format!(
            "schema_version: 9\neditor:\n  command: \"true\"\nengines:\n  claude-code:\n    command: {}\n    env: [ANTHROPIC_API_KEY]\n",
            dir.join("claude").display()
        ),
    );
    let seeded = run_now(w, &["test", "seed-session", &harp]);
    assert_eq!(seeded.code, 0, "{}", seeded.said());
    w.common
        .set_env("ANTHROPIC_API_KEY", "the-owners-key".to_string()); // gitleaks:allow
    w.common
        .set_env(IDLE_VARIABLE, SPAWNED_IDLE_SECONDS.to_string());
}

#[given("the daemon was started from a shell holding CLAUDE_CODE_OAUTH_TOKEN")]
fn daemon_env_token(w: &mut World) {
    let mut env = env_of(w);
    env.retain(|(name, _)| name != "ANTHROPIC_API_KEY");
    env.push((
        "CLAUDE_CODE_OAUTH_TOKEN".to_string(),
        "the-daemons-token".to_string(), // gitleaks:allow
    ));
    let env: Vec<(&str, &str)> = env.iter().map(|(k, v)| (k.as_str(), v.as_str())).collect();
    let idle = DEFAULT_IDLE_SECONDS.to_string();
    let daemon = w
        .common
        .project
        .start_with(&env, &["daemon", "serve", "--idle-seconds", &idle]);
    w.daemon_spec.daemon = Some(daemon);
    until(w, "the daemon to serve", DAEMON_WAIT, |w| {
        serving_now(w).then_some(())
    });
}

#[when(expr = "its orchestrator launches a child {string}")]
fn orchestrator_launches(w: &mut World, child: String) {
    engine_calls(
        w,
        "amber-quiet-heron",
        "child_launch",
        json!({"name": child, "task": "a task"}),
    );
    let launched = tool_session(w);
    assert!(
        launched["name"]
            .as_str()
            .is_some_and(|name| name.ends_with(&format!("/sessions/{child}"))),
        "{launched}"
    );
}

#[then(expr = "the child {string} was given ANTHROPIC_API_KEY, and not CLAUDE_CODE_OAUTH_TOKEN")]
fn child_given_curated(w: &mut World, _child: String) {
    let env = until(w, "the child to start", DAEMON_WAIT, |w| {
        files::read(stub_dir(w).join("child-env")).ok()
    });
    assert!(env.contains("ANTHROPIC_API_KEY=the-owners-key\n"), "{env}"); // gitleaks:allow
    assert!(!env.contains("CLAUDE_CODE_OAUTH_TOKEN"), "{env}");
}

// ------------------------------------------------------ trust (ruling 142)

/// The program a cloned project's own config names as Alice's assistant,
/// the `n`th version of it: a stub that records it ran, and answers.
fn their_program(w: &World, n: u32) -> PathBuf {
    w.adopt_scratch().join(format!("their-{n}")).join("claude")
}

/// The upstream repository Alice cloned her project from.
fn upstream(w: &World) -> PathBuf {
    w.adopt_scratch().join("upstream")
}

/// The cloned project's config, naming the `n`th version of its program.
fn cloned_config(w: &World, n: u32) -> String {
    config_of(&[
        "agents.default.engine: claude-code".to_string(),
        "default_agent: default".to_string(),
        format!(
            "engines.claude-code.command: {}",
            their_program(w, n).display()
        ),
    ])
}

/// Commit `config` upstream as the project's own config.
fn commit_upstream(w: &World, config: &str, message: &str) {
    let up = upstream(w);
    files::put(up.join(".ctxloom/config.yaml"), config);
    w.git(&up, &["add", "-A"]);
    w.git(&up, &["commit", "-q", "-m", message]);
}

#[given(
    "Alice has cloned a project whose own ctxloom config names the program that runs as her assistant"
)]
fn cloned_project(w: &mut World) {
    for n in [1, 2] {
        let program = their_program(w, n);
        files::put_program(
            &program,
            &format!(": > \"$(dirname \"$0\")/ran\"\n{}", answering_stub()),
        );
    }
    let up = upstream(w);
    files::mkdirs(&up);
    w.git(&up, &["init", "-q"]);
    commit_upstream(w, &cloned_config(w, 1), "the project");
    // Her clone: the project's directory, its tree the upstream's.
    let dir = w.common.project.dir().to_path_buf();
    w.git(&dir, &["init", "-q"]);
    w.git(
        &dir,
        &["remote", "add", "origin", &up.display().to_string()],
    );
    w.git(&dir, &["fetch", "-q", "origin"]);
    w.git(&dir, &["checkout", "-q", "-f", "-B", "main", "origin/main"]);
    w.common
        .set_env(IDLE_VARIABLE, SPAWNED_IDLE_SECONDS.to_string());
}

#[then(
    regex = r#"^she is told the project's own "([^"]+)" waits for her trust, and that `ctxloom trust` is how to give it$"#
)]
fn told_untrusted(w: &mut World, path: String) {
    let ran = w.ran().clone();
    assert_eq!(ran.code, 48, "{}", ran.said());
    let envelope = super::adopt::envelope_of(w);
    assert_eq!(
        envelope["code"],
        "runs-untrusted-project-settings",
        "{}",
        ran.said()
    );
    assert!(ran.said().contains(&path), "{}", ran.said());
    assert!(ran.said().contains("ctxloom trust"), "{}", ran.said());
}

#[then("the project is listed as trusted by her, just now")]
fn trusted_by_her(w: &mut World) {
    let listed = run_now(w, &["trust", "list", "--format", "json"]);
    assert_eq!(listed.code, 0, "{}", listed.said());
    let trusts = listed.json()["trusts"].clone();
    assert_eq!(
        trusts.as_array().map(Vec::len),
        Some(1),
        "{}",
        listed.said()
    );
    trusted_by_the_user(&trusts[0], &listed);
    let granted = trusts[0]["grant_time"]
        .as_str()
        .and_then(|t| {
            time::OffsetDateTime::parse(t, &time::format_description::well_known::Rfc3339).ok()
        })
        .unwrap_or_else(|| panic!("no grant time: {}", listed.said()));
    let ago = time::OffsetDateTime::now_utc() - granted;
    assert!(
        ago < time::Duration::minutes(5) && ago > time::Duration::minutes(-1),
        "granted {ago} ago: {}",
        listed.said()
    );
}

/// The login of the user running the test, as the binary is given it.
fn login() -> String {
    ["USER", "LOGNAME", "USERNAME"]
        .iter()
        .find_map(|name| crate::tests::host_var(name).filter(|v| !v.is_empty()))
        .map(|v| v.to_string_lossy().into_owned())
        .expect("the test runs as a user with a login")
}

/// `trust`, a listed trust, was given by the user running the test, at a
/// time it says.
fn trusted_by_the_user(trust: &Value, listed: &crate::tests::Ran) {
    assert_eq!(trust["trusted"], true, "{}", listed.said());
    assert_eq!(
        trust["actor"].as_str(),
        Some(login().as_str()),
        "{}",
        listed.said()
    );
    assert!(trust["grant_time"].is_string(), "{}", listed.said());
}

/// Whether the `n`th version of the cloned project's program ran.
fn their_program_ran(w: &World, n: u32) -> bool {
    files::exists(
        their_program(w, n)
            .parent()
            .expect("in a directory")
            .join("ran"),
    )
}

#[then("her assistant ran as the program the project names")]
fn ran_projects_program(w: &mut World) {
    assert!(their_program_ran(w, 1), "{}", w.ran().said());
}

#[given("Alice has trusted the cloned project")]
fn has_trusted(w: &mut World) {
    w.before_run();
    let env = env_of(w);
    let ran = w
        .common
        .project
        .run_with(&env_pairs(&env), &["trust", "--yes"]);
    assert_eq!(ran.code, 0, "{}", ran.said());
}

#[given("a pull changes the program the project names")]
fn pull_changes(w: &mut World) {
    commit_upstream(w, &cloned_config(w, 2), "another program");
    let dir = w.common.project.dir().to_path_buf();
    w.git(&dir, &["pull", "-q", "--ff-only", "origin", "main"]);
}

#[then("her assistant ran as the program the project names now")]
fn ran_projects_program_now(w: &mut World) {
    assert!(their_program_ran(w, 2), "{}", w.ran().said());
    assert!(!their_program_ran(w, 1), "the old program ran");
}

/// A setting the project config will say once the scenario's children are
/// launched (their agents are written beside it).
pub(super) fn says_in_project(w: &mut World, setting: String) {
    if !w.daemon_spec.project_says.contains(&setting) {
        w.daemon_spec.project_says.push(setting);
    }
}

#[given(expr = "an initialized ctxloom project whose project config says {string}")]
fn project_says(w: &mut World, setting: String) {
    w.common.project = crate::tests::library::Project::initialized();
    w.daemon_spec.project_says = vec![setting.clone()];
    // Its default agent on claude-code, so a launch reads that engine's
    // block and the agent's own.
    w.common.project.write(
        ".ctxloom/config.yaml",
        &config_of(&[
            "agents.default.engine: claude-code".to_string(),
            "default_agent: default".to_string(),
            setting,
        ]),
    );
    // A one-shot's assistant, should the launch run: the stub, which
    // answers, first on the scenario's PATH as `claude`, so the default
    // command runs it (never an engine on the host's PATH) and no config
    // names it: an untrusted read reports the command as the default's.
    let bin = assistant_dir(w);
    let stub = bin.join("claude");
    files::put_program(&stub, &answering_stub());
    w.common.project.put_on_path(&bin);
    w.daemon_spec.stub = Some(stub);
    w.daemon_spec.stub_on_path = true;
    write_home_config(w);
    w.common
        .set_env(IDLE_VARIABLE, SPAWNED_IDLE_SECONDS.to_string());
}

/// A project config at the current schema saying each of `entries`, a
/// dotted key and its value (`engines.claude-code.command: /opt/x`), nested
/// as YAML writes it, keys that share a prefix under one parent.
fn config_of(entries: &[String]) -> String {
    #[derive(Default)]
    struct Node(std::collections::BTreeMap<String, (Option<String>, Node)>);
    fn render(node: &Node, depth: usize, out: &mut String) {
        for (key, (value, children)) in &node.0 {
            let indent = "  ".repeat(depth);
            match value {
                Some(value) => out.push_str(&format!("{indent}{key}: {value}\n")),
                None => {
                    out.push_str(&format!("{indent}{key}:\n"));
                    render(children, depth + 1, out);
                }
            }
        }
    }
    let mut root = Node::default();
    for entry in entries {
        let (path, value) = entry
            .split_once(": ")
            .unwrap_or_else(|| panic!("{entry:?} is `<dotted key>: <value>`"));
        let keys: Vec<&str> = path.split('.').collect();
        let mut node = &mut root;
        for (depth, key) in keys.iter().enumerate() {
            let slot = node.0.entry((*key).to_string()).or_default();
            if depth + 1 == keys.len() {
                slot.0 = Some(value.to_string());
            }
            node = &mut slot.1;
        }
    }
    let mut yaml = crate::tests::library::MINIMAL_CONFIG.to_string();
    render(&root, 0, &mut yaml);
    yaml
}

/// A project config at the current schema that says `setting`, a dotted
/// key and its value (`engines.claude-code.command: /opt/their-claude`),
/// nested as YAML writes it.
fn config_saying(setting: &str) -> String {
    config_of(&[setting.to_string()])
}

#[then("the project is listed as trusted by the user running ctxloom, with the time it was given")]
fn listed_trusted(w: &mut World) {
    let listed = w.ran().clone();
    let trusts = listed.json()["trusts"].clone();
    assert_eq!(
        trusts.as_array().map(Vec::len),
        Some(1),
        "{}",
        listed.said()
    );
    trusted_by_the_user(&trusts[0], &listed);
}

// -------------------------------------------------- approvals (ruling 142)

/// The scenario's asks' time runs out: its `approval_timeout` is short, and
/// the daemon's scheduler ends each ask by its alarm (U5), so this waits,
/// bounded, until none is waiting any more.
#[when("the ask's time runs out")]
fn ask_expires(w: &mut World) {
    // The time passes now: the alarm held since the ask is let go.
    files::remove(alarms_hold(w));
    until(w, "the ask's time to run out", DAEMON_WAIT, |w| {
        waiting_asks(w, None).is_empty().then_some(())
    });
}

/// The arguments the hosted child stub was last started with, once it has
/// been, as its words.
fn child_args(w: &World) -> Vec<String> {
    let args = until(w, "the child's start", DAEMON_WAIT, |w| {
        files::read(stub_dir(w).join("child-args")).ok()
    });
    let last = args.lines().last().unwrap_or_default();
    last.split_whitespace().map(str::to_string).collect()
}

/// The value after `flag` in `args`.
fn flag_value<'a>(args: &'a [String], flag: &str) -> &'a str {
    args.windows(2)
        .find(|pair| pair[0] == flag)
        .map(|pair| pair[1].as_str())
        .unwrap_or_else(|| panic!("no {flag} among {args:?}"))
}

/// The claude was given `flag` `value`, side by side.
fn launched_with(args: &[String], flag: &str, value: &str) {
    assert_eq!(flag_value(args, flag), value, "{args:?}");
}

/// The MCP config claude was named (`--mcp-config`) names ctxloom's own
/// server, `ctxloom mcp serve --no-wake` (a hosted session's) as the running
/// binary's name, with no identity in its entry.
fn names_ctxloom_server(args: &[String]) {
    let file = flag_value(args, "--mcp-config");
    let config: Value =
        serde_json::from_str(&files::read(file).expect("the MCP config")).expect("JSON");
    let entry = &config["mcpServers"]["ctxloom"];
    assert!(
        entry["command"].as_str().is_some_and(|c| !c.is_empty()),
        "{config}"
    );
    // A session the daemon hosts (a child, a one-shot): its server without
    // the terminal wake's poster.
    assert_eq!(
        entry["args"],
        json!(["mcp", "serve", "--no-wake"]),
        "{config}"
    );
    assert!(entry.get("env").is_none(), "{entry}");
}

#[then(regex = r#"^the child's claude was launched with "([^"]+)" "([^"]+)"$"#)]
fn claude_flag(w: &mut World, flag: String, value: String) {
    launched_with(&child_args(w), &flag, &value);
}

#[then("the child's session home names ctxloom's MCP server")]
fn home_names_mcp(w: &mut World) {
    names_ctxloom_server(&child_args(w));
}

/// The scripted claude's model asks, at its next turn or in the one in
/// flight (step 8 U6), to run `tool` with `text`: the arguments claude gives
/// its prompt tool, put whole in `ask` beside the stub (written aside, then
/// renamed, so the stub never reads half of it). The stub asks through
/// ctxloom's approval prompt itself; nothing here records an ask.
fn script_ask(w: &mut World, tool: &str, text: &str) {
    let arguments = json!({
        "tool_name": tool,
        "input": ask_input(tool, text),
        "tool_use_id": "toolu_scripted_1",
    });
    let dir = stub_dir(w);
    files::put(dir.join("ask.tmp"), format!("{arguments}\n"));
    files::rename(dir.join("ask.tmp"), dir.join("ask"));
    w.daemon_spec.scripted = Some(arguments);
}

/// Wait, at most [`DAEMON_WAIT`], until an ask is listed waiting (`root`'s,
/// or the project's): the scripted engine has asked.
fn ask_listed(w: &World, root: Option<&str>) {
    until(w, "the engine's ask listed", DAEMON_WAIT, |w| {
        (!waiting_asks(w, root).is_empty()).then_some(())
    });
}

#[given(regex = r#"^"([^"]+)" asks to run "([^"]+)" with "([^"]*)"$"#)]
fn journey_child_asks(w: &mut World, _child: String, tool: String, input: String) {
    script_ask(w, &tool, &input);
    ask_listed(w, Some(OWNER));
}

/// The asks the last command listed (its JSON's `approvals`).
fn listed_asks(w: &mut World) -> Vec<Value> {
    w.common.json()["approvals"]
        .as_array()
        .cloned()
        .unwrap_or_else(|| panic!("no asks listed: {}", w.ran().said()))
}

#[then(regex = r#"^she sees "([^"]+)" asking to run "([^"]+)" with "([^"]*)"$"#)]
fn sees_ask(w: &mut World, child: String, tool: String, input: String) {
    let asks = listed_asks(w);
    assert!(
        asks.iter().any(|ask| {
            ask["child"]
                .as_str()
                .is_some_and(|c| c.ends_with(&format!("/sessions/{child}")))
                && ask["tool_name"] == tool.as_str()
                && ask["input_excerpt"]
                    .as_str()
                    .is_some_and(|e| e.contains(&input))
        }),
        "{asks:?}"
    );
}

/// What the scripted claudes did with their asks: `ran <what>` or `denied
/// <what>`, a line each.
fn stub_ran(w: &World) -> String {
    files::read(stub_dir(w).join("ran")).unwrap_or_default()
}

#[then(regex = r#"^"([^"]+)" ran "([^"]*)", and nothing waits for her any more$"#)]
fn ran_after_allow(w: &mut World, _child: String, input: String) {
    let ran = format!("ran {input}");
    until(w, "the child to run what it asked", DAEMON_WAIT, |w| {
        stub_ran(w).lines().any(|line| line == ran).then_some(())
    });
    assert_eq!(waiting_asks(w, None), Vec::<Value>::new());
}

// ------------------------------------- step 8: approvals and trust's harness

/// A project beside this one, by its name: its directory.
fn beside(w: &World, name: &str) -> PathBuf {
    w.common
        .project
        .dir()
        .parent()
        .expect("the project has a parent")
        .join(name)
}

#[given(expr = "a second project {string} beside this one, initialized with ctxloom")]
fn second_project_beside(w: &mut World, name: String) {
    let dir = beside(w, &name);
    // Its default agent on claude-code, as `init` seeds one.
    files::put(
        dir.join(".ctxloom/config.yaml"),
        config_of(&[
            "agents.default.engine: claude-code".to_string(),
            "default_agent: default".to_string(),
        ]),
    );
    // An id of its own, as `init` mints one: never this project's.
    files::put(
        dir.join(crate::tests::library::PROJECT_MARKER),
        format!("{name}-fixture-project\n"),
    );
}

#[given(
    expr = "a second project {string} beside this one, initialized with ctxloom, whose config says {string}"
)]
fn second_project_saying(w: &mut World, name: String, says: String) {
    let dir = beside(w, &name);
    files::put(
        dir.join(".ctxloom/config.yaml"),
        config_of(&[
            "agents.default.engine: claude-code".to_string(),
            "default_agent: default".to_string(),
            says,
        ]),
    );
    files::put(
        dir.join(crate::tests::library::PROJECT_MARKER),
        format!("{name}-fixture-project\n"),
    );
}

/// `ctxloom args` in the project `name` beside this one, now, with the
/// scenario's environment.
fn run_in_beside(w: &World, name: &str, args: &[&str]) -> crate::tests::Ran {
    let env = env_of(w);
    let env: Vec<(&str, &str)> = env.iter().map(|(k, v)| (k.as_str(), v.as_str())).collect();
    w.common.project.run_in_with(&beside(w, name), &env, args)
}

/// The session `harp` of the project `name` beside this one, as its
/// `session show` has it.
fn shown_beside(w: &World, name: &str, harp: &str) -> Value {
    let ran = run_in_beside(w, name, &["session", "show", harp, "--format", "json"]);
    assert_eq!(ran.code, 0, "{}", ran.said());
    ran.json()
}

// Step 8 U2b: the owner's child launched into the trusted project `name`
// beside this one, its first turn ended and told to its owner. The child's
// full name, `projects/<{name}>/sessions/<child>` as a command line writes
// it, is that project's harp and the child's.
#[given(expr = "a child {string} of {string} in the project {string} that is open and idle")]
fn child_of_idle_beside(w: &mut World, child: String, owner: String, name: String) {
    assert_eq!(
        (child.as_str(), owner.as_str()),
        ("brisk-copper-moth", "amber-quiet-heron"),
        "the child and owner the harness hosts"
    );
    if !files::exists(
        w.common
            .project
            .dir()
            .join(crate::tests::library::PROJECT_MARKER),
    ) {
        w.common.project = crate::tests::library::Project::initialized();
    }
    second_project_beside(w, name.clone());
    let trusted = run_in_beside(w, &name, &["trust", "--yes"]);
    assert_eq!(trusted.code, 0, "{}", trusted.said());
    w.common
        .set_env(IDLE_VARIABLE, SPAWNED_IDLE_SECONDS.to_string());
    start_daemon_idling(w, DEFAULT_IDLE_SECONDS);
    let dir = stub_claude(w, &child_stub());
    let seeded = run_now(w, &["test", "seed-session", &owner]);
    assert_eq!(seeded.code, 0, "{}", seeded.said());
    let mut env = env_of(w);
    env.push(("ANTHROPIC_API_KEY".to_string(), "a-key".to_string())); // gitleaks:allow
    let env: Vec<(&str, &str)> = env.iter().map(|(k, v)| (k.as_str(), v.as_str())).collect();
    let project = format!("../{name}");
    let launched = w.common.project.run_with(
        &env,
        &[
            "session",
            "launch",
            &owner,
            "--name",
            &child,
            "--project",
            &project,
            "a task",
            "--format",
            "json",
        ],
    );
    assert_eq!(launched.code, 0, "{}", launched.said());
    until(w, "the child's first turn", DAEMON_WAIT, |_| {
        files::exists(dir.join("child-turn")).then_some(())
    });
    first_turn_ended(w);
    // Its full name is its project's harp and its own.
    let named = run_in_beside(w, &name, &["test", "project-name"]);
    assert_eq!(named.code, 0, "{}", named.said());
    let full = format!("{}/sessions/{child}", named.stdout.trim());
    assert_eq!(
        launched.json()["name"],
        Value::String(full.clone()),
        "the launch answered its full name"
    );
    w.common
        .aliases
        .push((format!("projects/<{name}>/sessions/{child}"), full));
    w.daemon_spec.turns_before = Some(turns(w).len());
}

#[given(expr = "an empty directory {string} beside this project")]
fn empty_directory_beside(w: &mut World, name: String) {
    files::mkdirs(beside(w, &name));
}

#[given(expr = "I run {string} in the project {string}")]
#[when(expr = "I run {string} in the project {string}")]
fn run_in_project(w: &mut World, line: String, name: String) {
    let dir = beside(w, &name);
    w.before_line(&line);
    w.before_run();
    w.common.run_lines_at(Some(&dir), &line, "");
    // A child launched there is minted by the daemon after its launch is
    // answered (`session launch` answers it as launched): its `session
    // show` is read once the store has it, bounded, as `the child ... ran
    // its first turn` reads it.
    if line.trim_start().starts_with("ctxloom session show") && w.ran().code != 0 {
        let deadline = Instant::now() + DAEMON_WAIT;
        while w.ran().code != 0 && Instant::now() < deadline {
            std::thread::sleep(Duration::from_millis(50));
            w.common.run_lines_at(Some(&dir), &line, "");
        }
    }
}

#[then(expr = "the output says \"denied at\" then a UTC date and time, then \"unless answered\"")]
fn denied_at_a_date_and_time(w: &mut World) {
    let out = w.common.ran().stdout.clone();
    let after = out
        .split_once("denied at ")
        .unwrap_or_else(|| panic!("no `denied at` in {out:?}"))
        .1;
    let (when, rest) = after.split_at(after.len().min(19));
    let shaped = when.len() == 19
        && when.chars().enumerate().all(|(at, c)| match at {
            4 | 7 => c == '-',
            10 => c == ' ',
            13 | 16 => c == ':',
            _ => c.is_ascii_digit(),
        });
    assert!(shaped, "not `YYYY-MM-DD HH:MM:SS`: {when:?} in {out:?}");
    assert!(rest.starts_with(" unless answered"), "{out:?}");
}

#[given(expr = "I note the ask named by {string} as {string}")]
#[then(expr = "I note the ask named by {string} as {string}")]
fn note_ask(w: &mut World, path: String, label: String) {
    let doc = w.common.json();
    let name = crate::tests::library::at(&doc, &path)
        .and_then(Value::as_str)
        .unwrap_or_else(|| panic!("no name at {path}: {}", w.ran().said()))
        .to_string();
    let ask = name.rsplit('/').next().unwrap_or(&name).to_string();
    w.common.note(&label, &ask);
}

// A path the output reports, as the project `name` beside this one has it:
// that project's directory (as written, or as the platform resolves it),
// then `relative`.
#[then(expr = "the output reports {string} as {string} in the project {string}")]
fn reports_in_project(w: &mut World, path: String, relative: String, name: String) {
    let ran = w.common.ran();
    let doc: Value = serde_json::from_str(&ran.stdout)
        .unwrap_or_else(|e| panic!("not json ({e}): {}", ran.said()));
    let reported = PathBuf::from(crate::tests::library::reports_as(&doc, &path));
    let dir = beside(w, &name);
    assert!(
        [dir.clone(), files::canonical(&dir)]
            .iter()
            .any(|root| reported == root.join(&relative)),
        "{path} is {}, not {relative} of {}: {}",
        reported.display(),
        dir.display(),
        ran.said()
    );
}

// Each turn the child took since its Given ended is told to `owner`, a
// message waiting for its turn that says `text`: one for every turn its
// engine took, and at least one turn taken since.
#[then(expr = "{string} lists {string} as waiting for a turn")]
fn owner_lists_waiting(w: &mut World, owner: String, text: String) {
    // A child named (`child <harp> ...`) is judged by its own turns (step 9:
    // several children at once).
    if let Some(child) = text
        .strip_prefix("child ")
        .and_then(|rest| rest.split_whitespace().next())
        .filter(|child| files::exists(child_rec(w, child)) || text.contains("ended its run"))
        .map(str::to_string)
    {
        return child_told(w, &owner, &child, &text);
    }
    let before = w.daemon_spec.turns_before.unwrap_or(0);
    until(w, &format!("{owner} told of each turn"), DAEMON_WAIT, |w| {
        let taken = turns(w).len();
        let told = shown(w, &owner)["pending"]
            .as_array()
            .map(|pending| {
                pending
                    .iter()
                    .filter(|m| m["text"].as_str().is_some_and(|t| t.contains(&text)))
                    .count()
            })
            .unwrap_or(0);
        (taken > before && told == taken).then_some(())
    });
}

#[then(
    regex = r#"^the child "([^"]+)" of the project "([^"]+)" is listed as closed, its engine has exited and its hand-off file is gone$"#
)]
fn child_ended_beside(w: &mut World, child: String, name: String) {
    until(w, "the child closed", DAEMON_WAIT, |w| {
        (shown_beside(w, &name, &child)["closed"] == Value::Bool(true)).then_some(())
    });
    until(w, "the child's engine exited", DAEMON_WAIT, |w| {
        files::exists(stub_dir(w).join("child-exited")).then_some(())
    });
    until(w, "the child's hand-off gone", DAEMON_WAIT, |w| {
        hand_offs(w).is_empty().then_some(())
    });
}

#[then(expr = "the output reports {string} as the project's harp under {string}, then {string}")]
fn reports_project_harp(w: &mut World, path: String, under: String, then: String) {
    w.common
        .output_reports(&path, &format!("{under}{{project}}{then}"));
}

/// Wait, at most `limit`, for `child` to end, its output drained as it
/// comes (a full pipe never stalls it): what it came to. One that outlives
/// the limit is killed, and the test fails.
fn finished(mut child: Child, limit: Duration) -> crate::tests::Ran {
    use std::io::Read as _;
    let drain = |pipe: Option<Box<dyn std::io::Read + Send>>| {
        std::thread::spawn(move || {
            let mut bytes = Vec::new();
            if let Some(mut pipe) = pipe {
                let _ = pipe.read_to_end(&mut bytes);
            }
            bytes
        })
    };
    let out = drain(child.stdout.take().map(|p| Box::new(p) as _));
    let err = drain(child.stderr.take().map(|p| Box::new(p) as _));
    let deadline = Instant::now() + limit;
    let status = loop {
        if let Some(status) = child.try_wait().expect("the command is waited for") {
            break status;
        }
        if Instant::now() >= deadline {
            let _ = child.kill();
            let _ = child.wait();
            panic!("the command did not end within {limit:?}");
        }
        std::thread::sleep(Duration::from_millis(50));
    };
    crate::tests::Ran::of(std::process::Output {
        status,
        stdout: out.join().expect("stdout drained"),
        stderr: err.join().expect("stderr drained"),
    })
}

/// Start `line` (one ctxloom command, notes expanded) in the project with
/// the scenario's environment, left running: what a later step waits for.
fn start_line(w: &mut World, line: &str) {
    let line = w.common.expanded(line);
    w.before_line(&line);
    w.before_run();
    let words = crate::tests::gherkin_common::split(&line);
    assert_eq!(words.first().map(String::as_str), Some("ctxloom"), "{line}");
    let args: Vec<&str> = words[1..].iter().map(String::as_str).collect();
    let env = env_of(w);
    let child = w.common.project.start_with(&env_pairs(&env), &args);
    w.common.line = line;
    w.daemon_spec.started = Some(child);
}

/// The command a scenario started ([`start_line`]), ended: what it came to.
fn started_ran(w: &mut World) -> crate::tests::Ran {
    let child = w.daemon_spec.started.take().expect("a command was started");
    let ran = finished(child, DAEMON_WAIT);
    w.common.ran = Some(ran.clone());
    ran
}

#[when(regex = r#"^I start '([^']+)', whose engine asks to run "([^"]+)" with "([^"]*)"$"#)]
fn start_asking(w: &mut World, line: String, tool: String, input: String) {
    script_ask(w, &tool, &input);
    start_line(w, &line);
    ask_listed(w, None);
}

#[then("the started command succeeds, its engine told to run the tool")]
fn started_told_to_run(w: &mut World) {
    started_ran(w).succeeds();
    // The prompt's answer, as claude read it: allowed, its input unchanged.
    let answers = files::read(stub_dir(w).join("prompt-answers")).expect("the prompt answered");
    let answer: Value =
        serde_json::from_str(answers.lines().last().unwrap_or_default()).expect("JSON-RPC");
    let said = crate::tests::mcp_serve::answered_json(&answer["result"]);
    assert_eq!(said["behavior"], "allow", "{said}");
    let asked = w.daemon_spec.scripted.clone().expect("a scripted ask");
    assert_eq!(said["updatedInput"], asked["input"], "{said}");
    let what = asked["input"]["command"].as_str().unwrap_or_default();
    assert!(
        stub_ran(w)
            .lines()
            .any(|line| line == format!("ran {what}")),
        "{}",
        stub_ran(w)
    );
}

/// The question a stood-in terminal asks before it reads its answer: a
/// yes/no one, as ctxloom asks it (`[y/N]` or `[Y/n]`), on stderr.
fn asking(said: &str) -> bool {
    said.contains("[y/N]") || said.contains("[Y/n]")
}

#[when(
    expr = "I run {string} at a terminal, and before I answer the project config is changed to say {string}, then I answer {string}"
)]
fn run_at_terminal_changed(w: &mut World, line: String, setting: String, answer: String) {
    use std::io::{Read as _, Write as _};
    let line = w.common.expanded(&line);
    w.before_line(&line);
    w.before_run();
    let words = crate::tests::gherkin_common::split(&line);
    assert_eq!(words.first().map(String::as_str), Some("ctxloom"), "{line}");
    let args: Vec<&str> = words[1..].iter().map(String::as_str).collect();
    let mut env = env_of(w);
    env.push(("CTXLOOM_TEST_TERMINAL".to_string(), "1".to_string()));
    let mut child = w.common.project.start_talking(&env_pairs(&env), &args);
    // Its stderr read as it comes, so the step sees the question asked
    // before it acts: forced, never waited out (the race rule).
    let said = std::sync::Arc::new(std::sync::Mutex::new(Vec::new()));
    let reader = {
        let said = std::sync::Arc::clone(&said);
        let mut pipe = child.stderr.take().expect("stderr is piped");
        std::thread::spawn(move || {
            let mut chunk = [0u8; 512];
            while let Ok(n) = pipe.read(&mut chunk) {
                if n == 0 {
                    break;
                }
                said.lock()
                    .expect("not poisoned")
                    .extend_from_slice(&chunk[..n]);
            }
        })
    };
    let text = |said: &std::sync::Mutex<Vec<u8>>| {
        String::from_utf8_lossy(&said.lock().expect("not poisoned")).to_string()
    };
    let deadline = Instant::now() + DAEMON_WAIT;
    while !asking(&text(&said)) {
        if matches!(child.try_wait(), Ok(Some(_))) || Instant::now() >= deadline {
            let _ = child.kill();
            let _ = child.wait();
            panic!("the command asked nothing at its terminal: {}", text(&said));
        }
        std::thread::sleep(Duration::from_millis(20));
    }
    w.common
        .project
        .write(".ctxloom/config.yaml", &config_saying(&setting));
    let mut stdin = child.stdin.take().expect("stdin is piped");
    stdin
        .write_all(format!("{answer}\n").as_bytes())
        .expect("the answer is typed");
    drop(stdin);
    let mut ran = finished(child, DAEMON_WAIT);
    reader.join().expect("stderr drained");
    // What the command said after its question: the question itself is
    // the terminal's, not the command's answer.
    let said = text(&said);
    ran.stderr = match said.rfind("[y/N] ").or_else(|| said.rfind("[Y/n] ")) {
        Some(at) => said[at + "[y/N] ".len()..].to_string(),
        None => said,
    };
    w.common.line = line;
    w.common.ran = Some(ran);
}

// ------------------------------------- step 8: approvals and trust, red

#[given(
    expr = "the engine of {string} calls {string} for {string} with project {string} and task {string}"
)]
#[when(
    expr = "the engine of {string} calls {string} for {string} with project {string} and task {string}"
)]
fn tool_call_with_project(
    w: &mut World,
    owner: String,
    tool: String,
    child: String,
    project: String,
    task: String,
) {
    hosting_daemon(w);
    w.before_run();
    // The engine's session is told its project's root, as every engine is
    // (`CTXLOOM_ROOT`): a relative `project` is relative to it (Q-C).
    let root = w.common.project.dir().display().to_string();
    w.common.set_env("CTXLOOM_ROOT", root);
    engine_calls(
        w,
        &owner,
        &tool,
        json!({"name": child, "project": project, "task": task}),
    );
}

#[then(expr = "the tool failed with {string}, naming {string}")]
fn tool_failed_naming(w: &mut World, code: String, named: String) {
    let answered = w.daemon_spec.tool.clone().expect("a tool was called");
    assert_eq!(answered["isError"], true, "{answered}");
    let said = answered.to_string();
    assert!(said.contains(&code), "{answered}");
    assert!(said.contains(&named), "{answered}");
}

#[then(expr = "the tool's refusal names the stable id of {string} as the requester")]
fn tool_refusal_names_requester(w: &mut World, harp: String) {
    let answered = w.daemon_spec.tool.clone().expect("a tool was called");
    let uid = shown(w, &harp)["uid"]
        .as_str()
        .unwrap_or_else(|| panic!("{harp} has a stable id"))
        .to_string();
    assert!(!uid.is_empty(), "{harp}'s stable id");
    let said = answered.to_string();
    assert!(
        said.contains(&format!("session {uid} is not this session's owner")),
        "the requester {uid} is named: {answered}"
    );
}

#[then(expr = "the tool answered with the child's session, named under the project {string}")]
fn tool_answered_in_project(w: &mut World, project: String) {
    let session = tool_session(w);
    // The project's name, as its trust's preview names it there.
    let previewed = run_in_dir(w, &beside(w, &project), &["trust", "--format", "json"]);
    let trust = previewed.json()["name"]
        .as_str()
        .unwrap_or_else(|| panic!("a trust's name: {}", previewed.said()))
        .to_string();
    let parent = trust.trim_end_matches("/trust");
    assert!(
        session["name"]
            .as_str()
            .is_some_and(|name| name.starts_with(&format!("{parent}/sessions/"))),
        "{session} is not named under {parent}"
    );
}

/// `args` run in the directory `dir`, in the scenario's environment.
fn run_in_dir(w: &World, dir: &Path, args: &[&str]) -> crate::tests::Ran {
    let env = env_of(w);
    let env: Vec<(&str, &str)> = env
        .iter()
        .filter(|(k, _)| k != "CTXLOOM_ROOT")
        .map(|(k, v)| (k.as_str(), v.as_str()))
        .collect();
    w.common.project.run_in_with(dir, &env, args)
}

#[then(
    expr = "the child {string} of {string} ran its first turn on {string}, in the root of {string}"
)]
fn first_turn_in_project(
    w: &mut World,
    child: String,
    owner: String,
    task: String,
    project: String,
) {
    turn_on(w, &task);
    let dir = beside(w, &project);
    let shown_child = until(w, "the child minted", DAEMON_WAIT, |w| {
        let ran = run_in_dir(w, &dir, &["session", "show", &child, "--format", "json"]);
        (ran.code == 0).then(|| ran.json())
    });
    assert_eq!(
        shown_child["owner"],
        shown(w, &owner)["name"],
        "{shown_child}"
    );
    let stub = w.daemon_spec.stub.clone().expect("the stub child");
    let cwds =
        files::read(stub.parent().expect("a directory").join("child-cwds")).unwrap_or_default();
    assert!(
        cwds.lines()
            .any(|cwd| files::canonical(cwd) == files::canonical(&dir)),
        "the child ran in {cwds:?}, not {}",
        dir.display()
    );
}

/// The asking engine exits where it waits: its MCP server killed with it,
/// so nothing cancels the call and nothing withdraws the ask.
#[when(regex = r#"^"([^"]+)"'s engine exits without cancelling its approval prompt call$"#)]
fn exits_uncancelled(w: &mut World, _child: String) {
    the_ask(w, None).mcp.kill();
}

/// The hosted child stub's environment, once it has written it: each
/// variable's value by its name.
fn child_env(w: &World) -> std::collections::BTreeMap<String, String> {
    let env = until(w, "the child's environment", DAEMON_WAIT, |w| {
        files::read(stub_dir(w).join("child-env")).ok()
    });
    env.lines()
        .filter_map(|line| line.split_once('='))
        .map(|(name, value)| (name.to_string(), value.to_string()))
        .collect()
}

#[then(expr = "the child's claude was not given {string}")]
fn claude_not_given(w: &mut World, name: String) {
    let env = child_env(w);
    assert!(
        !env.contains_key(&name),
        "{name} was given: {:?}",
        env.get(&name)
    );
}

// Q12 (step 8): a claude child's MCP_TOOL_TIMEOUT, in milliseconds, past
// the ask's approval_timeout.
#[then(expr = "the child's claude was given {string} above {string}")]
fn claude_given_above(w: &mut World, name: String, floor: String) {
    let env = child_env(w);
    let given: u64 = env
        .get(&name)
        .unwrap_or_else(|| {
            panic!(
                "{name} was not given: {:?}; the daemon's log:\n{}",
                env.keys(),
                daemon_log_text(w)
            )
        })
        .parse()
        .unwrap_or_else(|e| panic!("{name} is no number: {e}"));
    let floor: u64 = floor.parse().expect("the floor is a number");
    assert!(given > floor, "{name}={given}, not above {floor}");
}

// Probe P0: a PermissionRequest hook fires beside the approval prompt and
// races it, so ctxloom's hooks (the `--settings` file) hold none.
#[then("the child's session home registers no PermissionRequest hook of ctxloom's")]
fn no_permission_request_hook(w: &mut World) {
    let args = child_args(w);
    let file = flag_value(&args, "--settings");
    let settings: Value =
        serde_json::from_str(&files::read(file).expect("ctxloom's hooks file")).expect("JSON");
    let hooks = settings["hooks"].as_object().expect("ctxloom's hooks");
    assert!(hooks.contains_key("Stop"), "{settings}");
    assert!(!hooks.contains_key("PermissionRequest"), "{settings}");
}

/// The arguments the one-shot stub was started with, one a line.
fn one_shot_args(w: &World) -> Vec<String> {
    files::read(stub_dir(w).join("one-shot-args"))
        .unwrap_or_else(|e| {
            panic!(
                "the one-shot's claude never started ({e}): {}",
                w.ran().said()
            )
        })
        .lines()
        .map(str::to_string)
        .collect()
}

#[then(regex = r#"^the one-shot's claude was launched with "([^"]+)" "([^"]+)"$"#)]
fn one_shot_claude_flag(w: &mut World, flag: String, value: String) {
    launched_with(&one_shot_args(w), &flag, &value);
}

#[then("the one-shot's session home names ctxloom's MCP server")]
fn one_shot_home_names_mcp(w: &mut World) {
    names_ctxloom_server(&one_shot_args(w));
}

#[then(
    "she sees she may allow it once, for the rest of the child's session, or always, or deny it"
)]
fn sees_options(w: &mut World) {
    let asks = listed_asks(w);
    let ids: Vec<&str> = asks[0]["options"]
        .as_array()
        .into_iter()
        .flatten()
        .filter_map(|option| option["id"].as_str())
        .collect();
    assert_eq!(
        ids,
        ["allow-once", "allow-session", "allow-always", "deny"],
        "{asks:?}"
    );
}

// ------------------------------------- the architecture rulings T13-T30

#[then("the removal was previewed first, and applied with her answer and the preview's tag")]
fn previewed_then_applied(w: &mut World) {
    // What she was asked is the preview's own question (the daemon's Confirm
    // need: the CLI asks nothing of its own), and the daemon destroys only
    // on an apply that carries the preview's tag and her yes (its own
    // refusals otherwise: `PlanStale`, `NeedsUnanswered`), once.
    let ran = w.common.ran();
    let question = removal_question("amber-swift-owl");
    assert_eq!(ran.stderr.matches(&question).count(), 1, "{}", ran.said());
    let intended = record(w)
        .iter()
        .flat_map(|s| s["events"].as_array().cloned().unwrap_or_default())
        .filter(|e| e["type"] == "SessionRemovalIntended" && e["harp"] == "amber-swift-owl")
        .count();
    assert_eq!(intended, 1, "one removal applied");
}

#[given(
    expr = "the bundle {string} changes after its removal is previewed and before Alice answers"
)]
fn bundle_changes_mid(w: &mut World, bundle: String) {
    w.daemon_spec.changes_before_answer = Some(bundle);
}

#[then("the apply was refused as stale, and the CLI previewed the removal again")]
fn refused_stale(w: &mut World) {
    let ran = w.common.ran();
    assert_eq!(ran.code, 48, "{}", ran.said());
    assert!(
        ran.stderr.contains("library-tree-plan-stale") || ran.stderr.contains("look again"),
        "refused as stale: {}",
        ran.said()
    );
    assert!(
        ran.stdout.contains("Would remove bundle \"notes\""),
        "the removal previewed again: {}",
        ran.said()
    );
    assert!(
        files::exists(
            w.common
                .project
                .dir()
                .join(".ctxloom/content/bundles/v2/notes/bundle.yaml")
        ),
        "nothing was removed"
    );
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

/// The `IncarnationOwed` events of the child's session in `record`, each
/// `(incarnation, reason)`.
fn child_owed(record: &[Value]) -> Vec<(u64, String)> {
    stream_events(record, "sessions", &child_root(record))
        .iter()
        .filter(|e| e["type"] == "IncarnationOwed")
        .map(|e| {
            (
                e["incarnation"].as_u64().unwrap_or_default(),
                e["reason"].as_str().unwrap_or_default().to_string(),
            )
        })
        .collect()
}

#[then(regex = r#"^its incarnation was recorded with the cause "([^"]+)"$"#)]
fn incarnation_cause(w: &mut World, cause: String) {
    let owed = child_owed(&record(w));
    assert_eq!(
        owed.last(),
        Some(&(2, cause)),
        "its next incarnation's cause: {owed:?}"
    );
}

#[given(expr = "a child {string} of {string} that has been re-hosted {int} times in the last hour")]
fn rehosted_n(w: &mut World, child: String, owner: String, times: i64) {
    assert_eq!(
        (child.as_str(), owner.as_str()),
        (CHILD, OWNER),
        "the child and owner the harness hosts"
    );
    child_mid_turn(w, child.clone());
    // Its conversation is bound (`hosted_child` waited for it).
    // Its daemon is killed outright with its turn in flight; then its past
    // re-hosts are planted, as earlier daemons would have recorded them,
    // its host lost again after the last.
    files::put(daemon_dir(w).join(DIE_AT), "turn");
    daemon_gone(w);
    let seeded = run_now(w, &["test", "seed-rehosts", &child, &times.to_string()]);
    assert_eq!(seeded.code, 0, "{}", seeded.said());
}

/// The messages `harp`'s session was sent, by id, in `record`.
fn sent_to(record: &[Value], harp: &str) -> Vec<(String, String)> {
    let Some(root) = session_root(record, harp) else {
        return Vec::new();
    };
    stream_events(record, "sessions", &root)
        .iter()
        .filter(|e| e["type"] == "MessageSent")
        .map(|e| {
            (
                e["message_id"].as_str().unwrap_or_default().to_string(),
                e["text"].as_str().unwrap_or_default().to_string(),
            )
        })
        .collect()
}

#[then(regex = r#"^"([^"]+)" was not re-hosted again, and ended runner-lost$"#)]
fn past_budget(w: &mut World, child: String) {
    assert_eq!(child, CHILD, "the child the harness hosts");
    // Its re-host was decided once its owner's notice is recorded.
    let record = until(w, "the re-host refused for its budget", DAEMON_WAIT, |w| {
        let record = record(w);
        sent_to(&record, OWNER)
            .iter()
            .any(|(id, _)| id.ends_with(":rehost-budget"))
            .then_some(record)
    });
    let owed = child_owed(&record);
    let ended: Vec<u64> = stream_events(&record, "sessions", &child_root(&record))
        .iter()
        .filter(|e| e["type"] == "IncarnationEnded")
        .filter_map(|e| e["incarnation"].as_u64())
        .collect();
    assert_eq!(
        owed.last().map(|(n, _)| *n),
        ended.iter().max().copied(),
        "nothing owed past its last end: {owed:?}"
    );
    let run = run_events_of(&record, &child_root(&record), 1);
    assert!(
        run.iter()
            .any(|e| e["type"] == "RunEnded" && e["cause"] == "RunnerLoss"),
        "{run:?}"
    );
    assert_eq!(child_starts(w).len(), 1, "never started again");
}

#[then(regex = r#"^"([^"]+)" was told that its child ended$"#)]
fn owner_told(w: &mut World, owner: String) {
    let told = until(w, "the owner told", DAEMON_WAIT, |w| {
        let texts = pending_text(w, &owner);
        texts
            .iter()
            .any(|t| t.starts_with(&format!("child {CHILD} ended")))
            .then_some(texts)
    });
    assert!(!told.is_empty());
}

/// The child's session's claude settings: its session home is its
/// `CLAUDE_CONFIG_DIR`, whose user-scope `settings.json` they are.
fn child_settings(w: &World) -> PathBuf {
    w.common
        .project
        .dir()
        .join(".ctxloom/sessions")
        .join(CHILD)
        .join("home/settings.json")
}

#[given(expr = "a child {string} whose session settings its agent has edited")]
fn settings_edited(w: &mut World, child: String) {
    child_mid_turn(w, child);
    let path = child_settings(w);
    let made = files::read(&path).expect("its launch wrote its settings");
    files::put(
        &path,
        "{\"permissions\": {\"allow\": [\"Bash(*)\"]}, \"agent-edit\": 1}\n",
    );
    w.daemon_spec.settings = Some(made);
}

#[then("the re-hosted child's session settings are the ones the user's config makes, and its agent's edit is gone")]
fn settings_regenerated(w: &mut World) {
    started_again(w);
    let now = files::read(child_settings(w)).expect("its settings");
    assert!(!now.contains("agent-edit"), "the edit is gone: {now}");
    assert_eq!(
        Some(now),
        w.daemon_spec.settings.clone(),
        "written again from the user's config, as its launch wrote them"
    );
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

/// The resource name of the session `harp` of the scenario's project.
pub(super) fn session_name(w: &World, harp: &str) -> String {
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
    // A client that can elicit (step 10c U11), so a tool that asks first is
    // seen asking, and one that does not is seen not to.
    let mut mcp = crate::tests::mcp_serve::Mcp::for_session_eliciting(&w.common.project, &me, &env);
    let (answered, elicited) = mcp.call_eliciting(tool, arguments, !w.daemon_spec.declines);
    let (code, err) = mcp.finish();
    assert_eq!(code, 0, "the server failed: {err}");
    w.daemon_spec.tool = Some(answered);
    w.daemon_spec.elicited = elicited;
}

#[when(
    regex = r#"^the engine of "([^"]+)" calls "child_close" for "([^"]+)", and answers its elicitation (yes|no)$"#
)]
fn close_answering(w: &mut World, owner: String, child: String, answer: String) {
    w.daemon_spec.declines = answer == "no";
    engine_calls(w, &owner, "child_close", json!({"child": child}));
}

#[then(regex = r#"^the elicitation asked to confirm closing "([^"]+)" and, with it, "([^"]+)"$"#)]
fn asked_cascade(w: &mut World, child: String, grandchild: String) {
    let asked = &w.daemon_spec.elicited;
    assert_eq!(asked.len(), 1, "one question: {asked:?}");
    let message = asked[0]["message"].as_str().unwrap_or_default();
    assert!(
        message.contains(&child) && message.contains(&grandchild),
        "{message:?} names {child} and {grandchild}"
    );
}

#[then("no elicitation was sent")]
fn none_elicited(w: &mut World) {
    let asked = &w.daemon_spec.elicited;
    assert!(asked.is_empty(), "the server asked: {asked:?}");
}

/// Whether `harp` is closed, as `session show` says.
fn is_closed(w: &World, harp: &str) -> bool {
    shown(w, harp)["closed"] == Value::Bool(true)
}

#[then(regex = r#"^"([^"]+)" is closed, and the tool said so$"#)]
fn closed_said(w: &mut World, harp: String) {
    tool_session(w);
    assert!(is_closed(w, &harp), "{harp} is closed");
}

#[then(regex = r#"^"([^"]+)" and "([^"]+)" are closed, and the tool said so$"#)]
fn both_closed_said(w: &mut World, one: String, two: String) {
    tool_session(w);
    assert!(is_closed(w, &one), "{one} is closed");
    assert!(is_closed(w, &two), "{two} is closed");
}

#[then("neither is closed, and the tool answered that the close was declined")]
fn neither_closed(w: &mut World) {
    let answered = w.daemon_spec.tool.clone().expect("a tool was called");
    assert!(
        answered.to_string().contains("was not closed"),
        "the tool said the close was declined: {answered}"
    );
    for harp in ["brisk-copper-moth", "olive-quiet-finch"] {
        assert!(!is_closed(w, harp), "{harp} is not closed");
    }
}

/// The events of the session `harp`'s stream, as `ctxloom test
/// session-events` lists them: each one's type, and its message id when it
/// has one.
fn session_events(w: &World, harp: &str) -> Vec<Value> {
    let ran = run_now(w, &["test", "session-events", harp]);
    assert_eq!(ran.code, 0, "{}", ran.said());
    ran.json()["events"].as_array().cloned().unwrap_or_default()
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
    w.daemon_spec.tool_child = Some(child.clone());
    // A child that is to answer fires its hooks for the turn (its prompt,
    // its words), as claude does, so its transcript holds the two.
    if let Ok(answer) = files::read(child_rec(w, &child).join("answer")) {
        fire_for_turn(w, &child, &text, &answer);
    }
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
    let harp = w
        .daemon_spec
        .tool_child
        .clone()
        .unwrap_or_else(|| "brisk-copper-moth".to_string());
    let child = session_name(w, &harp);
    assert_eq!(session["name"], child, "{session}");
    let shown = run_now(w, &["session", "show", &harp, "--format", "json"]);
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
    w.daemon_spec.tool_child = Some(child.clone());
    // `transcript_show` names its session (step 9), the child tools a child.
    let key = match tool.as_str() {
        "transcript_show" => "session",
        _ => "child",
    };
    engine_calls(w, &owner, &tool, json!({ key: child }));
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

/// The text an engine answers an Edit elicitation with.
const NEW_TEXT: &str = "# Brief\n\nWritten by the engine, through its MCP elicitation.\n";

/// Where the scenario's editor stub marks that it ran.
fn editor_ran(w: &World) -> PathBuf {
    w.common.project.home().join("editor-ran")
}

#[when("its engine calls a tool whose change asks for an edited document, and answers the elicitation with new text")]
fn tool_edit(w: &mut World) {
    // An EDITOR that marks its run, in the engine's environment: a client
    // that cannot launch one is never handed it.
    let editor = w.common.project.home().join("bin/editor");
    let mark = editor_ran(w);
    files::put_program(&editor, &format!("#!/bin/sh\ntouch '{}'\n", mark.display()));
    w.common
        .env
        .push(("EDITOR".to_string(), editor.display().to_string()));
    w.before_run();
    let me = session_name(w, "amber-quiet-heron");
    let env = env_of(w);
    let env: Vec<(&str, &str)> = env.iter().map(|(k, v)| (k.as_str(), v.as_str())).collect();
    let mut mcp = crate::tests::mcp_serve::Mcp::for_session_eliciting(&w.common.project, &me, &env);
    let answer = |_: &Value| json!({"action": "accept", "content": {"text": NEW_TEXT}});
    let arguments = json!({"bundle": "notes", "fragment": "brief"});
    let (answered, elicited) = mcp.call_answering("test_create_fragment", arguments, &answer);
    let (code, err) = mcp.finish();
    assert_eq!(code, 0, "the server failed: {err}");
    w.daemon_spec.tool = Some(answered);
    w.daemon_spec.elicited = elicited;
}

#[then("the daemon applied the new text, and no editor was started")]
fn applied_text(w: &mut World) {
    let asked = &w.daemon_spec.elicited;
    assert_eq!(asked.len(), 1, "one question: {asked:?}");
    let field = &asked[0]["requestedSchema"]["properties"]["text"];
    assert_eq!(field["type"], "string", "{asked:?}");
    let tool = w.daemon_spec.tool.clone().unwrap();
    assert_ne!(tool["isError"], true, "{tool}");
    let written = files::read(
        w.common
            .project
            .dir()
            .join(".ctxloom/content/bundles/v2/notes/fragments/brief.md"),
    )
    .expect("the fragment was written");
    assert_eq!(written, NEW_TEXT);
    assert!(!files::exists(editor_ran(w)), "no editor was started");
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
fn change_whole(w: &mut World) {
    writes::change_whole(w);
}

#[given(expr = "another process creates the bundle {string} while its creation is staged")]
fn concurrent_create(w: &mut World, bundle: String) {
    writes::other_creates(w, bundle);
}

#[then(
    regex = r#"^the other process's "([^"]+)" is untouched, and nothing staged is left behind$"#
)]
fn other_untouched(w: &mut World, bundle: String) {
    writes::other_untouched(w, bundle);
}

#[then(
    regex = r#"^the skill "([^"]+)" is the one directory "([^"]+)" of the bundle "([^"]+)", holding its "\.meta\.yaml"$"#
)]
fn skill_one_dir(w: &mut World, skill: String, dir: String, bundle: String) {
    writes::skill_one_dir(w, skill, dir, bundle);
}

#[then(regex = r#"^only "([^"]+)" of the bundle "([^"]+)" changed$"#)]
fn only_changed(w: &mut World, path: String, bundle: String) {
    writes::only_changed(w, path, bundle);
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
fn remove_converged(w: &mut World, harp: String) {
    writes::kill_taken(w);
    let listed = run_now(w, &["session", "list", "--all", "--format", "json"]);
    assert_eq!(listed.code, 0, "{}", listed.said());
    assert!(
        !listed.stdout.contains(&harp),
        "{harp} is listed: {}",
        listed.said()
    );
    let record = record(w);
    assert_eq!(
        session_root(&record, &harp),
        None,
        "{harp}'s stream is still in the store"
    );
    let trash = writes::in_project(w, ".ctxloom/state/trash");
    assert!(writes::empty(&trash), "the trash holds {}", trash.display());
}

/// Every file below the project and ctxloom's records, with its bytes,
/// but ctxloom's own state there (its sessions, its staging and trash, its
/// caches, git's directory), which a run changes on its own.
fn foreign_snapshot(w: &World) -> Vec<(PathBuf, Vec<u8>)> {
    fn walk(dir: &Path, out: &mut Vec<(PathBuf, Vec<u8>)>) {
        for entry in files::children(dir) {
            let name = entry.file_name().map(|n| n.to_string_lossy().to_string());
            if matches!(
                name.as_deref(),
                Some(".git" | "state" | "sessions" | "cache" | "daemon")
            ) {
                continue;
            }
            if files::is_dir(&entry) {
                walk(&entry, out);
            } else {
                out.push((entry.clone(), files::read_bytes(&entry).unwrap_or_default()));
            }
        }
    }
    let mut out = Vec::new();
    walk(w.common.project.dir(), &mut out);
    walk(&w.common.project.home().join(".ctxloom/records"), &mut out);
    out.sort();
    out
}

/// The kill was taken after the step the scenario named, the files hold
/// what the command writes, and one more run, uninterrupted, changes
/// nothing: what the killed run and its re-run left is what one run leaves.
#[then("the files it writes are as one uninterrupted run would leave them")]
fn foreign_converged(w: &mut World) {
    let (command, leaves) = w
        .daemon_spec
        .foreign_run
        .clone()
        .expect("a foreign writer was killed after a step");
    assert!(
        !files::exists(daemon_dir(w).join(DIE_AT)),
        "the daemon never took its kill: {:?}",
        files::read(daemon_dir(w).join(DIE_AT)).unwrap_or_default()
    );
    let at = |rel: &str| w.common.project.dir().join(rel);
    match leaves {
        Leaves::Holding(rel, text) => {
            let held = files::read(at(rel)).unwrap_or_else(|_| panic!("{rel} was not written"));
            assert!(held.contains(text), "{rel} holds {held:?}");
        }
        Leaves::Without(rel, text) => {
            let held = files::read(at(rel)).unwrap_or_default();
            assert!(!held.contains(text), "{rel} still holds {text:?}: {held:?}");
        }
        Leaves::Gone(rel) => assert!(!files::exists(at(rel)), "{rel} is still there"),
    }
    let before = foreign_snapshot(w);
    let args: Vec<&str> = command.split(' ').skip(1).collect();
    let again = run_now(w, &args);
    assert_eq!(again.code, 0, "{command} once more: {}", again.said());
    let after = foreign_snapshot(w);
    let changed: Vec<&PathBuf> = after
        .iter()
        .filter(|file| !before.contains(file))
        .chain(before.iter().filter(|file| !after.contains(file)))
        .map(|(path, _)| path)
        .collect();
    assert!(changed.is_empty(), "one more run changed {changed:?}");
}

// ------------------------------------- the rulings after T30 (S42, S53)

#[then("nothing was applied, and the daemon answered the preview")]
fn preview_only(w: &mut World) {
    // Off a terminal the command says the daemon's answer to its preview
    // (json off a terminal): the plan, applied nowhere, or the refusal of a
    // session nobody holds; the session and the bundle are as they were.
    let ran = w.common.ran();
    match ran.code {
        48 => {
            let refused: Value = serde_json::from_str(ran.stderr.trim())
                .unwrap_or_else(|e| panic!("a refusal's envelope: {e}: {}", ran.said()));
            let code = refused["code"].as_str().unwrap_or_default();
            assert!(code.ends_with("not-found"), "{}", ran.said());
        }
        _ => assert_eq!(ran.json()["applied"], false, "{}", ran.said()),
    }
    assert!(still_held(w, "amber-swift-owl"), "the session is kept");
    assert!(
        files::exists(
            w.common
                .project
                .dir()
                .join(".ctxloom/content/bundles/v2/notes/bundle.yaml")
        ),
        "the bundle is kept"
    );
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
            crate::tests::files::exists(super::session_lock(
                &project.join(".ctxloom/sessions"),
                harp
            )),
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

/// The running session's lock is the file `path` (under `.ctxloom`),
/// inside its directory and held; nothing is beside the directory.
#[then(regex = r#"^its lock is "([^"]+)", inside its directory$"#)]
fn lock_inside(w: &mut World, path: String) {
    // The fixture's session is seeded by the first command.
    w.before_run();
    let (dir, file) = path.rsplit_once('/').expect("a path in a directory");
    assert_eq!(file, "session.lock", "the lock's name");
    let lock = writes::in_project(w, &format!(".ctxloom/{path}"));
    assert!(
        files::exists(&lock),
        "no lock at {}: {:?}",
        lock.display(),
        files::children(writes::in_project(w, ".ctxloom/sessions"))
    );
    let beside = writes::in_project(w, &format!(".ctxloom/{dir}.lock"));
    assert!(
        !files::exists(&beside),
        "a lock is beside the directory: {}",
        beside.display()
    );
    let probe = files::open_read(&lock).expect("the lock opens");
    assert!(
        probe.try_lock().is_err(),
        "{} is not held by the running session",
        lock.display()
    );
}

#[then(regex = r#"^the bundle "([^"]+)" is the imported one, whole, and no trash is left$"#)]
fn import_converged(w: &mut World, bundle: String) {
    writes::import_converged(w, bundle);
}

// ------------------------------------------------------------ the drain (U8b)

/// The hand-over's period a scenario's daemon is given, in seconds: what
/// stands for the five minutes (`daemon serve --hand-over-seconds`).
const HAND_OVER_SECONDS: &str = "2";

/// The drain's guidance, as the daemon steers a busy child with it.
const DRAIN_GUIDANCE: &str = "ctxloom is being upgraded: finish the step you are on and end your turn; you will be resumed in this conversation.";

#[given(
    expr = "a ctxloom daemon is serving for this home, hosting a child {string} with a turn in flight"
)]
fn serving_hosting_busy(w: &mut World, child: String) {
    assert_eq!(child, CHILD, "the child the harness hosts");
    w.common
        .set_env(IDLE_VARIABLE, SPAWNED_IDLE_SECONDS.to_string());
    start_daemon_idling(w, DEFAULT_IDLE_SECONDS);
    // Its first turn is taken and left in flight.
    files::put(stub_dir(w).join("hold"), "once");
    hosted_child(w);
}

#[given(
    expr = "a ctxloom daemon is serving for this home, hosting a child {string} that does not end its turn"
)]
fn serving_hosting_deaf(w: &mut World, child: String) {
    assert_eq!(child, CHILD, "the child the harness hosts");
    w.common.project = crate::tests::library::Project::initialized();
    w.common
        .set_env(IDLE_VARIABLE, SPAWNED_IDLE_SECONDS.to_string());
    start_daemon_serving(
        w,
        DEFAULT_IDLE_SECONDS,
        &["--hand-over-seconds", HAND_OVER_SECONDS],
    );
    // Its first turn is taken and left in flight, and it ignores the
    // interrupt that would end it.
    files::put(stub_dir(w).join("hold"), "once");
    files::put(stub_dir(w).join("deaf"), "");
    hosted_child(w);
}

/// Wait, bounded, for the daemon the scenario started to exit; its code.
fn exit_of_daemon(w: &mut World) -> Option<i32> {
    let deadline = Instant::now() + DAEMON_WAIT;
    loop {
        let daemon = w
            .daemon_spec
            .daemon
            .as_mut()
            .expect("the scenario started a daemon");
        if let Some(status) = daemon.try_wait().expect("the daemon is waited for") {
            return status.code();
        }
        assert!(
            Instant::now() < deadline,
            "the daemon still runs; its log:\n{}",
            daemon_log_text(w)
        );
        std::thread::sleep(Duration::from_millis(50));
    }
}

/// The child started again by the next daemon, resuming its own
/// conversation (`--resume S1`), where its first start resumed none.
fn rehosted_resuming(w: &World) {
    let starts = started_again(w);
    assert!(
        !starts[0].contains("--resume") && starts[1].contains("--resume S1"),
        "its own conversation resumed: {starts:?}"
    );
}

#[then(regex = r#"^it warns "([^"]+)", naming "([^"]+)"$"#)]
fn warns_naming(w: &mut World, code: String, harp: String) {
    let ran = w.common.ran();
    for part in [code.as_str(), harp.as_str()] {
        assert!(ran.stderr.contains(part), "{part}: {}", ran.said());
    }
}

#[then(regex = r#"^the child was steered with "([^"]+)"$"#)]
fn steered_with(w: &mut World, guidance: String) {
    assert_eq!(guidance, DRAIN_GUIDANCE, "the approved guidance");
    turn_on(w, &guidance);
    assert!(
        files::exists(stub_dir(w).join("interrupted")),
        "its turn in flight was interrupted"
    );
}

#[then("once its turn ended the daemon has exited, and nothing says it serves")]
fn turn_ended_then_exited(w: &mut World) {
    until(w, "the child ended", DAEMON_WAIT, |w| {
        files::exists(stub_dir(w).join("child-exited")).then_some(())
    });
    daemon_exited(w);
}

#[then("the child is hosted again, resuming its own conversation")]
fn hosted_again(w: &mut World) {
    let ran = w.common.ran();
    assert_eq!(ran.code, 0, "{}", ran.said());
    rehosted_resuming(w);
}

#[then("after five minutes the child's turn in flight was cut and the daemon has exited")]
fn cut_after_the_period(w: &mut World) {
    cut_after(w, HAND_OVER_SECONDS);
}

/// The drain's period, `seconds` long, cut the child's turn in flight and
/// handed it over, and the daemon exited, nothing saying it serves. A
/// command run since may have started the next daemon (any command starts
/// one where none serves): that one is not the daemon that drained.
fn cut_after(w: &mut World, seconds: &str) {
    let drained = w
        .daemon_spec
        .daemon
        .as_ref()
        .expect("the scenario started a daemon")
        .id();
    assert_eq!(exit_of_daemon(w), Some(0), "{}", daemon_log_text(w));
    let log = daemon_log_text(w);
    assert!(
        log.contains(&format!(
            "{CHILD}: still busy after {seconds}s: its turn in flight cut, handed over"
        )),
        "{log}"
    );
    assert!(
        files::exists(stub_dir(w).join("child-exited")),
        "the child ended"
    );
    let status = run_now(w, &["daemon", "status", "--format", "json"]);
    assert_eq!(status.code, 0, "{}", status.said());
    assert_ne!(
        status.json()["daemons"][0]["pid"].as_u64(),
        Some(u64::from(drained)),
        "nothing says the drained daemon serves"
    );
}

#[then("the next daemon hosts the child again, resuming its own conversation")]
fn next_daemon_hosts_again(w: &mut World) {
    files::remove(stub_dir(w).join("deaf"));
    let shown = run_now(w, &["session", "show", CHILD, "--format", "json"]);
    assert_eq!(shown.code, 0, "{}", shown.said());
    rehosted_resuming(w);
}

/// The warnings `daemon status --format json` says the daemon stands by.
fn status_warnings(w: &World) -> Vec<Value> {
    let status = run_now(w, &["daemon", "status", "--format", "json"]);
    assert_eq!(status.code, 0, "{}", status.said());
    status.json()["daemons"][0]["warnings"]
        .as_array()
        .cloned()
        .unwrap_or_default()
}

#[then(expr = "\"ctxloom daemon status --format json\" has a warning with code {string}")]
fn status_warns(w: &mut World, code: String) {
    let warnings = status_warnings(w);
    assert!(
        warnings
            .iter()
            .any(|warning| warning["code"] == code.as_str()),
        "{warnings:?}"
    );
}

#[then("within a few seconds \"ctxloom daemon status\" has no warning")]
fn status_warns_no_more(w: &mut World) {
    until(w, "the daemon's warning cleared", DAEMON_WAIT, |w| {
        status_warnings(w).is_empty().then_some(())
    });
}

#[then("\"ctxloom session list\" says nothing of the start-up")]
fn listed_says_nothing_of_the_start_up(w: &mut World) {
    let listed = run_now(w, &["session", "list"]);
    assert_eq!(listed.code, 0, "{}", listed.said());
    assert!(!listed.stderr.contains("start-up"), "{}", listed.said());
}

// --------------------------------------- `ctxloom shutdown` (OD-62, OD-63)

#[then("after two seconds the child's turn in flight was cut and the daemon has exited")]
fn cut_after_two_seconds(w: &mut World) {
    cut_after(w, "2");
}

#[then("after one second the child's turn in flight was cut and the daemon has exited")]
fn cut_after_one_second(w: &mut World) {
    cut_after(w, "1");
}

#[then("within a few seconds the child's turn in flight was cut and the daemon has exited")]
fn cut_within_a_few_seconds(w: &mut World) {
    // The drain's timeout was changed to 1s while it drained: the period,
    // timed from its start, has long passed.
    cut_after(w, "1");
}

/// The value a `daemon.drain_timeout: <value>` step names.
fn drain_timeout_of(said: &str) -> String {
    said.strip_prefix("daemon.drain_timeout: ")
        .unwrap_or_else(|| panic!("a drain timeout: {said:?}"))
        .to_string()
}

#[given(expr = "the home config says {string}")]
fn home_config_says(w: &mut World, said: String) {
    w.daemon_spec.drain_timeout = Some(drain_timeout_of(&said));
    write_home_config(w);
}

#[when(expr = "the home config is changed to say {string}")]
fn home_config_changed(w: &mut World, said: String) {
    w.daemon_spec.drain_timeout = Some(drain_timeout_of(&said));
    write_home_config(w);
}

#[then(expr = "{string} succeeds and shows the new timeout")]
fn shows_the_new_timeout(w: &mut World, line: String) {
    let words: Vec<&str> = line
        .strip_prefix("ctxloom ")
        .expect("a ctxloom line")
        .split_whitespace()
        .collect();
    let shown = run_now(w, &words);
    assert_eq!(shown.code, 0, "{}", shown.said());
    let timeout = w.daemon_spec.drain_timeout.clone().expect("a new timeout");
    assert!(
        shown.stdout.contains("drain_timeout") && shown.stdout.contains(&timeout),
        "{timeout}: {}",
        shown.said()
    );
}

/// Send the daemon the scenario started `signal`.
fn signal_daemon(w: &World, signal: &str) {
    let pid = w
        .daemon_spec
        .daemon
        .as_ref()
        .expect("the scenario started a daemon")
        .id();
    let sent = std::process::Command::new("kill")
        .args([&format!("-{signal}"), &pid.to_string()])
        .status()
        .expect("kill runs");
    assert!(sent.success(), "the daemon was sent SIG{signal}");
}

/// Wait for the daemon's log to say `line`.
fn logged(w: &World, line: &str) {
    until(w, &format!("the log's {line:?}"), DAEMON_WAIT, |w| {
        daemon_log_text(w).contains(line).then_some(())
    });
}

#[when("the daemon is sent SIGTERM")]
fn sent_sigterm(w: &mut World) {
    signal_daemon(w, "TERM");
    logged(w, "SIGTERM: asked to drain");
}

#[when("the daemon is sent SIGTERM twice")]
fn sent_sigterm_twice(w: &mut World) {
    sent_sigterm(w);
    // The second once the first was heard: two at once may be one.
    signal_daemon(w, "TERM");
    logged(w, "SIGTERM again: asked to stop even if live");
}

#[then(expr = "{string} reports it draining")]
fn reports_draining(w: &mut World, line: String) {
    assert_eq!(line, "ctxloom daemon status --format json");
    until(w, "the daemon to say it drains", DAEMON_WAIT, |w| {
        let status = run_now(w, &["daemon", "status", "--format", "json"]);
        assert_eq!(status.code, 0, "{}", status.said());
        (status.json()["daemons"][0]["draining"] == true).then_some(())
    });
}

#[then(expr = "{string} exits with code {int}, naming {string}")]
fn exits_naming(w: &mut World, line: String, code: i32, name: String) {
    let words: Vec<&str> = line
        .strip_prefix("ctxloom ")
        .expect("a ctxloom line")
        .split_whitespace()
        .collect();
    let ran = run_now(w, &words);
    assert_eq!(ran.code, code, "{}", ran.said());
    assert!(ran.stderr.contains(&name), "{name}: {}", ran.said());
}

#[then(expr = "the error names {string}")]
fn error_names(w: &mut World, name: String) {
    let ran = w.common.ran();
    assert!(ran.stderr.contains(&name), "{name}: {}", ran.said());
}

#[given("a ctxloom daemon is serving for this home that does not exit when asked")]
fn serving_deaf_to_stop(w: &mut World) {
    // It outlives the steps, so the shutdown finds it serving.
    start_daemon_idling(w, SPAWNED_IDLE_SECONDS);
    // It takes a stop and ignores it (test-support), and the shutdown waits
    // for it two seconds, not thirty.
    files::put(daemon_dir(w).join("test-deaf-to-stop"), "");
    files::put(daemon_dir(w).join("test-shutdown-stop-wait"), "2");
}

#[then("the error names the daemon's pid")]
fn error_names_the_pid(w: &mut World) {
    let pid = w
        .daemon_spec
        .daemon
        .as_ref()
        .expect("the scenario started a daemon")
        .id();
    let ran = w.common.ran();
    assert!(
        ran.stderr.contains("ctxloom-wiring-shutdown-incomplete")
            || ran.stderr.contains(&format!("pid {pid}")),
        "{}",
        ran.said()
    );
    assert!(ran.stderr.contains(&format!("pid {pid}")), "{}", ran.said());
}

#[given(expr = "an interactive session {string} is running in a terminal")]
fn session_in_a_terminal(w: &mut World, harp: String) {
    primary_running(w, harp);
}

/// What the interactive session's command came to: waited for once (the
/// shutdown that stopped it has returned).
fn session_ran(w: &mut World) -> crate::tests::Ran {
    if w.daemon_spec.session_ran.is_none() {
        let child = w.daemon_spec.run.take().expect("the session's command");
        w.daemon_spec.session_ran = Some(crate::tests::Ran::of(
            child.wait_with_output().expect("it ends"),
        ));
    }
    w.daemon_spec.session_ran.clone().expect("it ran")
}

#[then(expr = "the session's human was told at once {string}")]
fn human_told(w: &mut World, text: String) {
    let ran = w.common.ran().clone();
    let told = ran
        .stdout
        .find(&format!(": {text}"))
        .filter(|_| ran.stdout.contains("told the frontend pid"))
        .unwrap_or_else(|| panic!("the frontend was told: {}", ran.said()));
    let after = ran
        .stdout
        .find("after the drain's")
        .unwrap_or_else(|| panic!("the drain's end said: {}", ran.said()));
    assert!(told < after, "told at the drain's start: {}", ran.said());
    let session = session_ran(w);
    assert!(
        session.stderr.contains(&format!("ctxloom: {text}")),
        "its terminal showed it: {}",
        session.said()
    );
}

#[then(
    "the output says the session's frontend still ran after the drain's 2s, and was asked to stop"
)]
fn frontend_asked_after_the_drain(w: &mut World) {
    let ran = w.common.ran();
    let still = ran
        .stdout
        .find("after the drain's 2s, still running")
        .unwrap_or_else(|| panic!("{}", ran.said()));
    assert!(
        ran.stdout[still..].contains("frontends pid"),
        "{}",
        ran.said()
    );
    let asked = ran
        .stdout
        .find("asked the frontend pid")
        .unwrap_or_else(|| panic!("{}", ran.said()));
    assert!(
        still < asked,
        "asked once the drain had ended: {}",
        ran.said()
    );
}

#[then("the session's engine was stopped and its end recorded")]
fn engine_stopped_and_recorded(w: &mut World) {
    let session = session_ran(w);
    assert!(
        session
            .stderr
            .contains("asked to stop by `ctxloom shutdown`: the session's engine was stopped"),
        "{}",
        session.said()
    );
    let project = w.common.project.dir().to_path_buf();
    assert_eq!(
        tui_record(w, &project, "asked").trim(),
        "asked",
        "its engine was asked to stop: {}",
        session.said()
    );
    let harp = tui_record(w, &project, "tui-harp").trim().to_string();
    // The incarnation its resume owed has ended (the seeded one had too).
    until(w, "the session's end recorded", DAEMON_WAIT, |w| {
        let ran = run_now(w, &["test", "session-events", &harp]);
        let types: Vec<String> = if ran.code == 0 {
            ran.json()["events"]
                .as_array()
                .cloned()
                .unwrap_or_default()
                .iter()
                .filter_map(|e| e["type"].as_str().map(str::to_string))
                .collect()
        } else {
            Vec::new()
        };
        let owed = types.iter().rposition(|t| t == "IncarnationOwed");
        let ended = types.iter().rposition(|t| t == "IncarnationEnded");
        matches!((owed, ended), (Some(owed), Some(ended)) if ended > owed).then_some(())
    });
}

// ------------------------------------- a daemon lost under a session (OD-59)

/// What waits for `$1` (a lock) and holds it until `$2` is there.
fn waits_for_lock() -> String {
    procs::holds_lock(60, 900)
}

/// Whether `lock` is held by another process now.
fn lock_held(lock: &std::path::Path) -> bool {
    procs::lock_held(lock)
}

/// The session of the step (`ctxloom run` on a stood-in terminal), its
/// frontend held at the test-support gate between the daemon's answer to its
/// plan and its start report, where its daemon is killed outright (forced:
/// the frontend's launch record, never a wait for a race). Unless `block`,
/// the next command starts a new daemon while the frontend still waits; with
/// `block`, no daemon can start (its single-instance lock held) until the
/// start report is held. Then the TUI runs, is released, and the command is
/// waited for.
fn tui_gated(w: &mut World, step: &cucumber::gherkin::Step, block: bool) {
    files::put_program(assistant_dir(w).join("claude"), &tui_stub());
    let release = assistant_dir(w).join("release");
    let env = tui_env(w);
    let gate = w.common.project.home().join(".ctxloom/test-frontend-gate");
    files::put(&gate, "wait");
    w.before_run();
    let words = line_words(step);
    let args: Vec<&str> = words.iter().map(String::as_str).collect();
    let child = w.common.project.start_with(&env_pairs(&env), &args);
    let launch = w
        .common
        .project
        .home()
        .join(".ctxloom/test-frontend-launch.json");
    wait_for(w, "the frontend's launch", || files::exists(&launch));
    let pid = discovery_now(w).pid;
    let unblock = assistant_dir(w).join("unblock");
    // The blocker waits on the daemon's single-instance lock BEFORE the
    // daemon is killed, so it takes the lock as the kill lets it go, ahead
    // of the daemon the frontend's watch starts at once.
    let blocking = block.then(|| {
        let lock = daemon_dir(w).join("daemon.lock");
        let holder = assistant_dir(w).join("blocks-daemons");
        files::put_program(&holder, &waits_for_lock());
        std::process::Command::new(&holder)
            .args([lock.as_os_str(), unblock.as_os_str()])
            .spawn()
            .expect("the blocker starts")
    });
    let killed = std::process::Command::new("kill")
        .args(["-KILL", &pid.to_string()])
        .status()
        .expect("kill runs");
    assert!(killed.success(), "the daemon was killed");
    wait_for(w, "the killed daemon's end", || !alive(pid));
    w.daemon_spec.killed_pid = Some(pid);
    if block {
        let lock = daemon_dir(w).join("daemon.lock");
        wait_for(w, "the daemon's lock held", || lock_held(&lock));
    } else {
        // A new daemon starts, and sweeps, while the frontend waits.
        let listed = run_now(w, &["session", "list"]);
        assert_eq!(listed.code, 0, "{}", listed.said());
    }
    files::remove(&gate);
    let project = w.common.project.dir().to_path_buf();
    let started = assistant_dir(w)
        .join(project.file_name().unwrap())
        .join("tui-started");
    wait_for(w, "the TUI's start", || files::exists(&started));
    if let Some(mut holding) = blocking {
        // Its hold, not the hold's lock (taken before the hold is written).
        wait_for(w, "the session's start held", || {
            files::children(held_dir(w))
                .iter()
                .any(|path| path.extension().is_some_and(|e| e == "held"))
        });
        files::put(&unblock, "go");
        let _ = holding.wait();
    }
    files::put(&release, "go");
    w.common.ran = Some(crate::tests::Ran::of(
        child.wait_with_output().expect("ctxloom finishes"),
    ));
}

#[given("no ctxloom daemon can be started while her assistant starts")]
fn unstartable_at_the_start(w: &mut World) {
    w.daemon_spec.start_blocked = true;
}

/// The events of `harp`'s session (`run`: its first run's), each its type
/// and its cause (an end's; empty otherwise).
fn caused_events(w: &World, harp: &str, run: bool) -> Vec<(String, String)> {
    let mut args = vec!["test", "session-events", harp];
    if run {
        args.extend(["--run", "1"]);
    }
    let ran = run_now(w, &args);
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

/// Her session's incarnation ended, once its end is recorded: each cause.
fn incarnations_ended(w: &World) -> Vec<String> {
    let harp = w.harp();
    let ended = || -> Vec<String> {
        caused_events(w, &harp, false)
            .into_iter()
            .filter(|(t, _)| t == "IncarnationEnded")
            .map(|(_, cause)| cause)
            .collect()
    };
    wait_for(w, "her session's end", || !ended().is_empty());
    ended()
}

/// Her session's exit ended it once: its incarnation `runner-exit`, its run
/// ended once on its exit, never swept as lost.
fn exit_ended_it_once(w: &World) {
    assert_eq!(
        incarnations_ended(w),
        ["runner-exit"],
        "{}",
        daemon_log_text(w)
    );
    let run = caused_events(w, &w.harp(), true);
    let ends: Vec<&str> = run
        .iter()
        .filter(|(t, _)| t == "RunEnded")
        .map(|(_, cause)| cause.as_str())
        .collect();
    assert_eq!(ends, ["RunnerExit"], "{run:?}");
    assert!(
        !run.iter().any(|(t, _)| t == "RunnerLostRecorded"),
        "{run:?}"
    );
}

#[then("the next daemon took her session over, and her session's exit ended it once")]
fn taken_over(w: &mut World) {
    let killed = w.daemon_spec.killed_pid.expect("a daemon was killed");
    assert_ne!(discovery_now(w).pid, killed, "another daemon");
    let log = daemon_log_text(w);
    assert!(log.contains("adopted its run"), "{log}");
    exit_ended_it_once(w);
}

#[then("her session's checkout was let go")]
fn checkout_let_go(w: &mut World) {
    let project = w.common.project.dir().to_path_buf();
    let checkout = tui_record(w, &project, "tui-cwd").trim().to_string();
    assert!(
        checkout.contains("/.ctxloom/worktrees/"),
        "the assistant worked in its checkout: {checkout}"
    );
    wait_for(w, "the checkout let go", || !files::exists(&checkout));
    let listed = w.git(&project, &["worktree", "list", "--porcelain"]);
    assert!(!listed.contains(&checkout), "its worktree record: {listed}");
}

#[then("her session was never ended as orphaned, and its exit ended it once")]
fn never_orphaned(w: &mut World) {
    exit_ended_it_once(w);
    let run = caused_events(w, &w.harp(), true);
    assert_eq!(
        run.iter()
            .filter(|(t, _)| t == "RunnerStartedRecorded")
            .count(),
        1,
        "{run:?}"
    );
}

#[then("the command succeeds, saying her session's start was held")]
fn succeeds_held(w: &mut World) {
    let ran = w.ran();
    assert_eq!(ran.code, 0, "{}", ran.said());
    assert!(
        ran.stderr
            .contains("held the session's start for the next daemon: "),
        "{}\nthe daemon's log:\n{}",
        ran.said(),
        daemon_log_text(w)
    );
}

#[then("the next daemon recorded her session's start, then its exit, in that order")]
fn start_then_exit(w: &mut World) {
    exit_ended_it_once(w);
    let run = caused_events(w, &w.harp(), true);
    let order: Vec<&str> = run
        .iter()
        .map(|(t, _)| t.as_str())
        .filter(|t| matches!(*t, "RunnerStartedRecorded" | "RunEnded"))
        .collect();
    assert_eq!(order, ["RunnerStartedRecorded", "RunEnded"], "{run:?}");
}

// ------------------------------------- step 8 U4: the asks, as claude makes them

/// The input claude gives a tool it asks to run with `text`: a Bash
/// command, a path tool's file, else `text` read as the JSON it is (its
/// `\"` unescaped).
fn ask_input(tool: &str, text: &str) -> Value {
    match tool {
        "Bash" => json!({"command": text}),
        "NotebookEdit" => json!({"notebook_path": text}),
        "Read" | "Write" | "Edit" | "MultiEdit" => json!({"file_path": text}),
        _ => serde_json::from_str(&text.replace("\\\"", "\""))
            .unwrap_or_else(|_| json!({"text": text})),
    }
}

/// The waiting asks `session approvals [<root>]` lists now.
fn waiting_asks(w: &World, root: Option<&str>) -> Vec<Value> {
    let mut args = vec!["session", "approvals"];
    args.extend(root);
    args.extend(["--format", "json"]);
    let ran = run_now(w, &args);
    assert_eq!(ran.code, 0, "{}", ran.said());
    ran.json()["approvals"]
        .as_array()
        .cloned()
        .unwrap_or_default()
}

/// The scripted claude of `child` asks to run `tool` with `text`: its
/// approval prompt called through `ctxloom mcp serve`, started as the
/// engine starts it (the child's session in its environment), and waited
/// for until the ask is listed or, settled at once, answered.
pub(super) fn ask(w: &mut World, child: &str, tool: &str, text: &str) {
    let me = session_name(w, child);
    let env = env_of(w);
    let mut mcp =
        crate::tests::mcp_serve::Mcp::for_session(&w.common.project, &me, &env_pairs(&env));
    let input = ask_input(tool, text);
    let n = w.daemon_spec.asks.len() + 1;
    let before = waiting_asks(w, None).len();
    let id = mcp.start_call(
        "approval_prompt",
        json!({"tool_name": tool, "input": input, "tool_use_id": format!("toolu_{n}")}),
    );
    w.daemon_spec.asks.push(Ask {
        mcp,
        id,
        tool: tool.to_string(),
        input,
        answered: None,
    });
    let deadline = Instant::now() + DAEMON_WAIT;
    loop {
        if waiting_asks(w, None).len() > before {
            return;
        }
        let ask = w.daemon_spec.asks.last_mut().expect("the ask");
        if let Some(answered) = ask
            .mcp
            .answer_to(&json!(ask.id), Duration::from_millis(100))
        {
            ask.answered = Some(answered);
            return;
        }
        assert!(
            Instant::now() < deadline,
            "the ask was never listed; the daemon's log:\n{}",
            daemon_log_text(w)
        );
    }
}

/// The scenario's child, hosted in the middle of a turn (claude asks from
/// inside one), on the project's default agent; then its ask. The
/// harness's own pair is hosted as every daemon scenario hosts it; any
/// other is a child of its own owner, seeded, beside it.
fn child_asking(w: &mut World, child: &str, owner: &str, tool: &str, text: &str) {
    // The project's default agent, as the spec's allow-always names it.
    if w.daemon_spec.project_says.is_empty() {
        w.daemon_spec
            .project_says
            .push("default_agent: default".to_string());
    }
    if w.daemon_spec.daemon.is_none() {
        w.common
            .set_env(IDLE_VARIABLE, SPAWNED_IDLE_SECONDS.to_string());
        start_daemon_idling(w, DEFAULT_IDLE_SECONDS);
        stub_claude(w, &child_stub());
    }
    files::put(stub_dir(w).join("hold"), "once");
    if (child, owner) == ("brisk-copper-moth", "amber-quiet-heron") {
        hosted_child(w);
    } else {
        let taken = turns(w).len();
        // An owner the scenario already has (the harness's, a child hosted)
        // is not made again.
        if listed_state(w, owner).is_none() {
            let seeded = run_now(w, &["test", "seed-session", owner]);
            assert_eq!(seeded.code, 0, "{}", seeded.said());
        }
        let mut env = env_of(w);
        env.push(("ANTHROPIC_API_KEY".to_string(), "a-key".to_string())); // gitleaks:allow
        let launched = w.common.project.run_with(
            &env_pairs(&env),
            &["session", "launch", owner, "--name", child, "a task"],
        );
        assert_eq!(launched.code, 0, "{}", launched.said());
        until(w, "the second child's first turn", DAEMON_WAIT, |w| {
            (turns(w).len() > taken).then_some(())
        });
    }
    // Its alarm held until the scenario says its time runs out (the race
    // rule): an `approval_timeout` of 2s must not end the ask on the wall
    // clock while the steps before that one run on a loaded machine.
    let now = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map_or(0, |since| since.as_secs());
    files::put(alarms_hold(w), now.to_string());
    ask(w, child, tool, text);
}

/// `child`'s engine asks, its alarm held until the scenario says its time
/// runs out (the race rule, as [`child_asking`] holds it).
pub(super) fn ask_holding_alarms(w: &mut World, child: &str, tool: &str, text: &str) {
    let now = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map_or(0, |since| since.as_secs());
    files::put(alarms_hold(w), now.to_string());
    ask(w, child, tool, text);
}

/// The daemon's test-support hold of its scheduler's alarms
/// (`test-hold-alarms`, the instant it holds them at): while it is there
/// no alarm armed since comes due.
fn alarms_hold(w: &World) -> PathBuf {
    daemon_dir(w).join("test-hold-alarms")
}

#[given("the project's config defines no agent of its own")]
fn no_project_agents(w: &mut World) {
    w.daemon_spec.no_project_agents = true;
}

#[given(
    regex = r#"^a child "([^"]+)" of "([^"]+)" whose engine has asked to run "([^"]+)" with "((?:[^"\\]|\\.)*)"$"#
)]
fn child_asked(w: &mut World, child: String, owner: String, tool: String, input: String) {
    child_asking(w, &child, &owner, &tool, &input);
}

#[given(
    regex = r#"^a claude child "([^"]+)" of "([^"]+)" whose engine calls "approval_prompt" for "([^"]+)" with "([^"]*)"$"#
)]
fn approval_prompt_called(
    w: &mut World,
    child: String,
    owner: String,
    tool: String,
    input: String,
) {
    child_asking(w, &child, &owner, &tool, &input);
}

#[given(regex = r#"^"([^"]+)"'s engine has also asked to run "([^"]+)" with "([^"]*)"$"#)]
fn also_asked(w: &mut World, child: String, tool: String, input: String) {
    ask(w, &child, &tool, &input);
}

#[when(regex = r#"^"([^"]+)"'s engine asks to run "([^"]+)" with "([^"]*)"$"#)]
fn engine_asks(w: &mut World, child: String, tool: String, input: String) {
    ask(w, &child, &tool, &input);
}

/// What claude's approval prompt answered `ask`, once it has: the JSON its
/// text says.
fn prompt_answer(ask: &mut Ask) -> Value {
    if ask.answered.is_none() {
        ask.answered = ask.mcp.answer_to(&json!(ask.id), DAEMON_WAIT);
    }
    let answered = ask.answered.clone().unwrap_or_else(|| {
        panic!(
            "the approval prompt never answered; it said {:?}",
            ask.mcp.said
        )
    });
    let result = &answered["result"];
    assert_ne!(result["isError"], true, "the tool failed: {answered}");
    crate::tests::mcp_serve::answered_json(result)
}

/// The scenario's ask an answer names: the one whose command is `command`,
/// else the last made.
fn the_ask<'a>(w: &'a mut World, command: Option<&str>) -> &'a mut Ask {
    let asks = &mut w.daemon_spec.asks;
    let at = match command {
        Some(command) => asks
            .iter()
            .position(|a| a.input["command"] == command)
            .unwrap_or_else(|| panic!("no ask ran {command:?}")),
        None => asks.len().checked_sub(1).expect("an ask was made"),
    };
    &mut asks[at]
}

/// `answer` is claude's allow of `ask`'s input unchanged, with the session
/// rule `rule` (`Tool(content)`) when one is named, else with none.
fn allowed(answer: &Value, ask: &Ask, rule: Option<(&str, &str)>) {
    assert_eq!(answer["behavior"], "allow", "{answer}");
    assert_eq!(answer["updatedInput"], ask.input, "{answer}");
    match rule {
        None => assert!(answer.get("updatedPermissions").is_none(), "{answer}"),
        Some((tool, content)) => {
            let given = &answer["updatedPermissions"][0];
            assert_eq!(given["destination"], "session", "{answer}");
            assert_eq!(given["behavior"], "allow", "{answer}");
            assert_eq!(given["rules"][0]["toolName"], tool, "{answer}");
            assert_eq!(given["rules"][0]["ruleContent"], content, "{answer}");
        }
    }
}

/// `answer` is claude's deny of an ask whose time ran out
/// (`mcp-server-approval-expired`).
fn expired_answer(answer: &Value) {
    assert_eq!(answer["behavior"], "deny", "{answer}");
    let said = answer["message"].as_str().unwrap_or_default();
    assert!(
        said.contains("No one answered this permission request within"),
        "{answer}"
    );
}

/// `rule` (`Bash(rm -rf build/)`) as its tool and its content.
fn rule_parts(rule: &str) -> (&str, &str) {
    rule.strip_suffix(')')
        .and_then(|r| r.split_once('('))
        .unwrap_or_else(|| panic!("{rule:?} is no rule"))
}

#[then(regex = r"^the child's engine was told (.+)$")]
fn engine_told(w: &mut World, what: String) {
    let quoted = |text: &str| -> Option<String> {
        let start = text.find('"')? + 1;
        let end = start + text[start..].find('"')?;
        Some(text[start..end].to_string())
    };
    if what == "not to run the tool" {
        let answer = prompt_answer(the_ask(w, None));
        assert_eq!(answer["behavior"], "deny", "{answer}");
    } else if what == "to run the tool" {
        let ask = the_ask(w, None);
        let answer = prompt_answer(ask);
        allowed(&answer, ask, None);
    } else if let Some(rest) = what.strip_prefix("to run the tool, and to allow ") {
        let rule = quoted(rest).expect("a rule");
        let ask = the_ask(w, None);
        let answer = prompt_answer(ask);
        allowed(&answer, ask, Some(rule_parts(&rule)));
    } else if let Some(rest) = what.strip_prefix("to run ") {
        let command = quoted(rest).expect("a command");
        let ask = the_ask(w, Some(&command));
        let answer = prompt_answer(ask);
        allowed(&answer, ask, None);
    } else if let Some(rest) = what.strip_prefix("not to run it, with the message ") {
        let message = quoted(rest).expect("a message");
        let answer = prompt_answer(the_ask(w, None));
        assert_eq!(answer, json!({"behavior": "deny", "message": message}));
    } else if what == "not to run it, because every ask is denied at once" {
        let answer = prompt_answer(the_ask(w, None));
        assert_eq!(answer["behavior"], "deny", "{answer}");
        let said = answer["message"].as_str().unwrap_or_default();
        assert!(said.contains("at once"), "{answer}");
        assert!(said.contains("approval_timeout is now"), "{answer}");
    } else if what == "not to run it, because no answer came in time" {
        expired_answer(&prompt_answer(the_ask(w, None)));
    } else {
        panic!("no such answer to tell: {what}");
    }
}

/// The last ask was answered, and not to run the tool: another answer
/// reached its engine.
pub(super) fn engine_not_told_to_run(w: &mut World) {
    let answer = prompt_answer(the_ask(w, None));
    assert_eq!(answer["behavior"], "deny", "{answer}");
}

#[then(expr = "no ask is waiting under {string}")]
fn none_waiting(w: &mut World, root: String) {
    until(w, "no ask waiting", DAEMON_WAIT, |w| {
        waiting_asks(w, Some(&root)).is_empty().then_some(())
    });
}

#[then(expr = "an ask is still waiting under {string}")]
#[then(expr = "the ask is still waiting under {string}")]
fn still_waiting(w: &mut World, root: String) {
    if !waiting_asks(w, Some(&root)).is_empty() {
        return;
    }
    // A short `approval_timeout` (2 s) can run out before this step on a
    // loaded machine: an ask gone only once its own recorded expiry has
    // passed left by its time, not by its engine's exit (the scenario's
    // last step reads it settled `expired`, never `withdrawn`).
    let listed = w.common.json();
    let expire = listed["approvals"][0]["expire_time"]
        .as_str()
        .map(epoch_seconds)
        .unwrap_or_else(|| panic!("no ask waits, and none was listed with its expiry: {listed}"));
    let now = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .expect("after the epoch")
        .as_secs();
    assert!(
        i64::try_from(now).expect("a time") >= expire,
        "no ask waits, before its time ran out ({expire}): {listed}"
    );
}

/// Seconds since the epoch of an RFC 3339 instant (`2026-11-16T10:00:00Z`,
/// any fraction dropped), as proto3 JSON writes a Timestamp.
fn epoch_seconds(text: &str) -> i64 {
    let digits = |from: usize, to: usize| -> i64 {
        text.get(from..to)
            .and_then(|d| d.parse().ok())
            .unwrap_or_else(|| panic!("{text:?} is no RFC 3339 instant"))
    };
    let (y, m, d) = (digits(0, 4), digits(5, 7), digits(8, 10));
    // Days from the civil date (Howard Hinnant's algorithm).
    let y = if m <= 2 { y - 1 } else { y };
    let era = y.div_euclid(400);
    let yoe = y - era * 400;
    let doy = (153 * (if m > 2 { m - 3 } else { m + 9 }) + 2) / 5 + d - 1;
    let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
    let days = era * 146_097 + doe - 719_468;
    days * 86_400 + digits(11, 13) * 3600 + digits(14, 16) * 60 + digits(17, 19)
}

#[then(expr = "the output reports the ask's {string} as 300 seconds after its {string}")]
fn expires_after(w: &mut World, expire: String, asked: String) {
    let doc = w.common.json();
    let at = |field: &str| {
        let text = doc["approvals"][0][field]
            .as_str()
            .unwrap_or_else(|| panic!("no {field}: {doc}"))
            .to_string();
        epoch_seconds(&text)
    };
    assert_eq!(at(&expire) - at(&asked), 300, "{doc}");
}

#[then("the child's engine cancelled its approval prompt call")]
fn engine_cancelled(w: &mut World) {
    // The scripted claude: a steer's interrupt reaches the stub, and claude
    // cancels its pending approval prompt call (P0 Q5), as its MCP client.
    until(w, "the child's turn interrupted", DAEMON_WAIT, |w| {
        files::exists(stub_dir(w).join("interrupted")).then_some(())
    });
    cancel_the_ask(w);
}

/// The last ask's call cancelled, as claude cancels it: its
/// `notifications/cancelled`, with claude's reason.
fn cancel_the_ask(w: &mut World) {
    let ask = the_ask(w, None);
    let id = ask.id;
    ask.mcp.send(json!({
        "jsonrpc": "2.0",
        "method": "notifications/cancelled",
        "params": {"requestId": id, "reason": "AbortError: remote-cancel"}
    }));
}

#[when(expr = "the engine sends {string} for that call")]
fn engine_cancels(w: &mut World, notification: String) {
    assert_eq!(notification, "notifications/cancelled");
    cancel_the_ask(w);
}

#[then("the server sent no answer for the cancelled call, and still serves the engine's next call")]
fn cancelled_unanswered(w: &mut World) {
    let ask = the_ask(w, None);
    let listed = ask.mcp.request("tools/list", json!({}));
    assert!(listed["result"]["tools"].is_array(), "{listed}");
    assert_eq!(
        ask.mcp
            .answer_to(&json!(ask.id), Duration::from_millis(500)),
        None,
        "the cancelled call was answered"
    );
}

#[when(regex = r#"^(Alice runs "[^"]+"|Alice runs '[^']+'|nobody answers within the ask's time)$"#)]
fn approval_answered_by(w: &mut World, who: String) {
    let Some(quoted) = who.strip_prefix("Alice runs ") else {
        // Nobody answers: the ask's short time runs out (U5).
        return ask_expires(w);
    };
    let line = quoted[1..quoted.len() - 1].to_string();
    w.before_line(&line);
    w.before_run();
    w.common.run_lines_at(None, &line, "");
}

#[then(regex = r"^the tool answered the engine (.+)$")]
fn tool_answered_engine(w: &mut World, answer: String) {
    let ask = the_ask(w, None);
    let said = prompt_answer(ask);
    if let Some(rest) = answer.strip_prefix(r#"{"behavior":"allow"} with the input unchanged"#) {
        let rule = rest.strip_prefix(", and a session rule for ").map(|r| {
            r.split('"')
                .filter(|p| !p.trim().is_empty())
                .collect::<Vec<_>>()
        });
        match rule.as_deref() {
            None => allowed(&said, ask, None),
            Some([tool, content]) => allowed(&said, ask, Some((tool, content))),
            Some(other) => panic!("no such rule: {other:?}"),
        }
    } else if let Some(rest) = answer.strip_prefix(r#"{"behavior":"deny"} with the message "#) {
        let message = rest.trim_matches('"');
        assert_eq!(said, json!({"behavior": "deny", "message": message}));
    } else if answer == r#"{"behavior":"deny"}, saying the user denied it"# {
        assert_eq!(said["behavior"], "deny", "{said}");
        let message = said["message"].as_str().unwrap_or_default();
        assert!(message.contains("user denied"), "{said}");
    } else if answer == r#"{"behavior":"deny"}, saying no answer came in time"# {
        expired_answer(&said);
    } else {
        panic!("no such answer: {answer}");
    }
    let _ = &ask.tool;
}

#[when("its engine lists the tools of ctxloom's MCP server")]
fn lists_tools(w: &mut World) {
    let me = session_name(w, "amber-quiet-heron");
    let env = env_of(w);
    let mut mcp =
        crate::tests::mcp_serve::Mcp::for_session(&w.common.project, &me, &env_pairs(&env));
    w.daemon_spec.tool = Some(mcp.request("tools/list", json!({})));
    w.daemon_spec.engine = Some(mcp);
}

#[then(regex = r"^the tools are (.+)$")]
fn tools_are(w: &mut World, tools: String) {
    let mut wanted: Vec<String> = tools
        .split('"')
        .skip(1)
        .step_by(2)
        .map(str::to_string)
        .collect();
    wanted.sort();
    let listed = w.daemon_spec.tool.clone().expect("the tools were listed");
    let mut names: Vec<String> = listed["result"]["tools"]
        .as_array()
        .unwrap_or_else(|| panic!("no tools: {listed}"))
        .iter()
        .map(|t| t["name"].as_str().unwrap_or_default().to_string())
        .collect();
    names.sort();
    assert_eq!(names, wanted);
}

/// The tool `name` as ctxloom's MCP server lists it to the engine of
/// "amber-quiet-heron": its whole listing entry.
fn listed_tool(w: &mut World, name: &str) -> Value {
    lists_tools(w);
    let listed = w.daemon_spec.tool.clone().expect("the tools were listed");
    listed["result"]["tools"]
        .as_array()
        .unwrap_or_else(|| panic!("no tools: {listed}"))
        .iter()
        .find(|t| t["name"] == name)
        .cloned()
        .unwrap_or_else(|| panic!("no tool {name}: {listed}"))
}

#[when(regex = r#"^its engine reads the "([^"]+)" tool's (input contract|description)$"#)]
fn reads_tool(w: &mut World, name: String, part: String) {
    let tool = listed_tool(w, &name);
    let read = match part.as_str() {
        "input contract" => tool["inputSchema"].clone(),
        _ => tool["description"].clone(),
    };
    assert!(!read.is_null(), "{name} has no {part}: {tool}");
    w.daemon_spec.read_tool = Some(read);
}

#[then(regex = r#"^its "([^"]+)" argument's vocabulary is exactly:$"#)]
fn vocabulary_exactly(w: &mut World, step: &cucumber::gherkin::Step, argument: String) {
    let schema = w
        .daemon_spec
        .read_tool
        .clone()
        .expect("a contract was read");
    let wanted: Vec<String> = step
        .table
        .as_ref()
        .expect("a table of the words")
        .rows
        .iter()
        .map(|row| row[0].trim().to_string())
        .collect();
    let advertised: Vec<String> = schema["properties"][&argument]["enum"]
        .as_array()
        .unwrap_or_else(|| panic!("{argument} enumerates no vocabulary: {schema}"))
        .iter()
        .map(|word| word.as_str().unwrap_or_default().to_string())
        .collect();
    assert_eq!(advertised, wanted, "{schema}");
}

#[then(regex = r"^it requires the arguments (.+)$")]
fn requires_arguments(w: &mut World, names: String) {
    let schema = w
        .daemon_spec
        .read_tool
        .clone()
        .expect("a contract was read");
    let mut wanted: Vec<String> = names
        .split('"')
        .skip(1)
        .step_by(2)
        .map(str::to_string)
        .collect();
    wanted.sort();
    let mut required: Vec<String> = schema["required"]
        .as_array()
        .unwrap_or_else(|| panic!("nothing required: {schema}"))
        .iter()
        .map(|name| name.as_str().unwrap_or_default().to_string())
        .collect();
    required.sort();
    assert_eq!(required, wanted, "{schema}");
}

#[then(expr = "it says {string}")]
fn description_says(w: &mut World, text: String) {
    let read = w
        .daemon_spec
        .read_tool
        .clone()
        .expect("a description was read");
    let description = read
        .as_str()
        .unwrap_or_else(|| panic!("not a description: {read}"));
    assert!(description.contains(&text), "{description:?}");
}

#[when(
    regex = r#"^the engine of "([^"]+)" calls "child_send" for "([^"]+)" with the urgency "([^"]*)" and "([^"]*)"$"#
)]
fn tool_send_any_urgency(
    w: &mut World,
    owner: String,
    child: String,
    urgency: String,
    text: String,
) {
    w.daemon_spec.tool_child = Some(child.clone());
    engine_calls(
        w,
        &owner,
        "child_send",
        json!({"child": child, "urgency": urgency, "text": text}),
    );
}

#[then(expr = "no message {string} was sent to {string}")]
fn no_message_sent(w: &mut World, text: String, harp: String) {
    let events = session_events(w, &harp);
    assert!(
        !events
            .iter()
            .any(|e| e["type"] == "MessageSent" && e["text"] == text.as_str()),
        "{events:?}"
    );
}

#[then("the tool answered with the child's session, closed")]
fn tool_answered_closed(w: &mut World) {
    let session = tool_session(w);
    let harp = w.daemon_spec.tool_child.clone().expect("a child was named");
    assert_eq!(session["name"], session_name(w, &harp), "{session}");
    assert_eq!(session["closed"], true, "{session}");
}

#[then(expr = "{string} was closed once")]
fn closed_once(w: &mut World, harp: String) {
    let events = session_events(w, &harp);
    assert_eq!(count_of(&events, "SessionClosed"), 1, "{events:?}");
}

#[when(regex = r#"^the engine of "([^"]+)" calls "context_assemble" with the fragments (.+)$"#)]
fn tool_assemble(w: &mut World, owner: String, names: String) {
    w.before_run();
    let bundles: Vec<String> = names
        .split('"')
        .skip(1)
        .step_by(2)
        .map(str::to_string)
        .collect();
    engine_calls(w, &owner, "context_assemble", json!({ "bundles": bundles }));
}

/// The context the last `context_assemble` call answered.
fn assembled_context(w: &World) -> String {
    let answered = w.daemon_spec.tool.clone().expect("a tool was called");
    assert_ne!(answered["isError"], true, "the tool failed: {answered}");
    let assembled = crate::tests::mcp_serve::answered_json(&answered);
    assembled["context"]
        .as_str()
        .unwrap_or_else(|| panic!("no context: {assembled}"))
        .to_string()
}

#[then(expr = "the tool answered with a context holding {string}")]
fn context_holds(w: &mut World, marker: String) {
    let context = assembled_context(w);
    assert!(context.contains(&marker), "{context}");
}

#[then(expr = "the assembled context does not hold {string}")]
fn context_lacks(w: &mut World, marker: String) {
    let context = assembled_context(w);
    assert!(!context.contains(&marker), "{context}");
}

#[when(expr = "its engine sends {string} to ctxloom's MCP server before {string}")]
fn sends_before_initialize(w: &mut World, method: String, before: String) {
    assert_eq!(
        (method.as_str(), before.as_str()),
        ("server/discover", "initialize")
    );
    let me = session_name(w, "amber-quiet-heron");
    let mut env = env_of(w);
    env.push(("CTXLOOM_SESSION".to_string(), me));
    let mut mcp = crate::tests::mcp_serve::Mcp::spawn(&w.common.project, &env_pairs(&env));
    // claude 2.1.322's probe (P0 Q11): a STRING id, before `initialize`.
    let id = json!("server-discover-probe-1");
    mcp.send(json!({
        "jsonrpc": "2.0",
        "id": id,
        "method": "server/discover",
        "params": {"_meta": {"io.modelcontextprotocol/protocolVersion": "2026-07-28"}}
    }));
    w.daemon_spec.discovered = mcp.answer_to(&id, DAEMON_WAIT);
    w.daemon_spec.engine = Some(mcp);
}

#[then("the server answered it with an empty result")]
fn answered_empty(w: &mut World) {
    let discovered = w
        .daemon_spec
        .discovered
        .clone()
        .expect("the probe was answered");
    assert_eq!(discovered["id"], "server-discover-probe-1", "{discovered}");
    assert_eq!(discovered["result"], json!({}), "{discovered}");
}

#[then("the engine went on to initialize the server and list its tools")]
fn initialized_after_discover(w: &mut World) {
    let mcp = w.daemon_spec.engine.as_mut().expect("the engine's server");
    let init = mcp.request(
        "initialize",
        json!({
            "protocolVersion": "2025-11-25",
            "capabilities": {},
            "clientInfo": {"name": "claude-code", "version": "2.1.322"}
        }),
    );
    assert_eq!(init["result"]["serverInfo"]["name"], "ctxloom", "{init}");
    mcp.send(json!({"jsonrpc": "2.0", "method": "notifications/initialized"}));
    let listed = mcp.request("tools/list", json!({}));
    assert_eq!(
        listed["result"]["tools"].as_array().map(Vec::len),
        Some(9),
        "{listed}"
    );
}

// ------------------------- a shutdown is done once the holds are empty

#[given(expr = "a session {string} whose prompt its hook held while an older daemon served")]
fn session_prompt_held_older(w: &mut World, harp: String) {
    w.plant(&harp, "running");
    holds_a_prompt(w);
}

#[given(expr = "a session {string} whose prompt is held")]
fn session_prompt_held(w: &mut World, harp: String) {
    // Held as a hook holds it: fired while only an older daemon served.
    session_prompt_held_older(w, harp);
}

#[given("a session's hold has only a set-aside part")]
fn hold_set_aside_only(w: &mut World) {
    // The set-aside part of a session's hold (never delivered again), and
    // no live part beside it.
    files::mkdirs(held_dir(w));
    files::put(
        held_dir(w).join("5f0c7a52-1111-5111-8111-000000000001.held.corrupt-1"),
        "not a record",
    );
    assert!(holds(w).is_empty(), "no live part: {:?}", holds(w));
}

#[then(expr = "the output says {string}")]
fn output_says(w: &mut World, text: String) {
    let ran = w.common.ran();
    assert!(ran.stdout.contains(&text), "{text}: {}", ran.said());
}

#[then("no daemon was started")]
fn no_daemon_started(w: &mut World) {
    assert!(!serving_now(w), "{}", daemon_log_text(w));
    let before = w
        .daemon_spec
        .logged_before
        .expect("no daemon was said to serve");
    assert_eq!(daemons_logged(w), before, "{}", daemon_log_text(w));
}

// --------------------- a TUI's exit held while no daemon serves (OD-59 (2))

/// The interactive session's command (`ctxloom run` on a stood-in
/// terminal, the TUI stub, of `stamp` when one is named), started, its
/// TUI's start recorded by its daemon: its harp.
fn tui_started(w: &mut World, stamp: Option<&str>) -> String {
    let dir = stub_claude(w, &tui_stub());
    w.daemon_spec.tui_dir = Some(dir.clone());
    w.daemon_spec.release = Some(dir.join("release"));
    let mut env = tui_env(w);
    if let Some(stamp) = stamp {
        env.push((STAMP_VARIABLE.to_string(), stamp.to_string()));
    }
    let mut child = w.common.project.start_with(&env_pairs(&env), &["run"]);
    let project = w.common.project.dir().to_path_buf();
    let started = dir.join(project.file_name().unwrap()).join("tui-started");
    let deadline = Instant::now() + Duration::from_secs(60);
    while !files::exists(&started) {
        if let Ok(Some(_)) = child.try_wait() {
            let ran = crate::tests::Ran::of(child.wait_with_output().expect("it ran"));
            panic!("the session ended before its TUI started: {}", ran.said());
        }
        assert!(
            Instant::now() < deadline,
            "the TUI's start never came; the daemon's log:\n{}",
            daemon_log_text(w)
        );
        std::thread::sleep(Duration::from_millis(50));
    }
    w.daemon_spec.run = Some(child);
    let harp = tui_record(w, &project, "tui-harp").trim().to_string();
    wait_for(w, "the daemon's record of the TUI's start", || {
        run_events(w, &harp).contains(&"RunnerStartedRecorded".to_string())
    });
    w.daemon_spec.tui = Some(harp.clone());
    harp
}

/// The harp of the session a step started.
fn tui_harp(w: &World) -> String {
    w.daemon_spec.tui.clone().expect("an interactive session")
}

#[given(expr = "an interactive session {string} whose engine has started")]
fn tui_engine_started(w: &mut World, _session: String) {
    // `run` mints the session's harp; the step's name is the scenario's
    // handle for that session.
    tui_started(w, None);
}

/// Kill the daemon serving outright, a blocker taking its single-instance
/// lock as the kill lets it go (waiting on it BEFORE the kill, so it is
/// ahead of the daemon the frontend's watch starts at once): no daemon can
/// start until the blocker is let go.
fn kill_and_block(w: &mut World) {
    let dir = w.daemon_spec.tui_dir.clone().expect("the TUI stub's");
    let lock = daemon_dir(w).join("daemon.lock");
    let unblock = dir.join("unblock");
    let holder = dir.join("blocks-daemons");
    files::put_program(&holder, &waits_for_lock());
    let blocker = std::process::Command::new(&holder)
        .args([lock.as_os_str(), unblock.as_os_str()])
        .spawn()
        .expect("the blocker starts");
    w.daemon_spec.blocker = Some((blocker, unblock));
    let pid = discovery_now(w).pid;
    let killed = std::process::Command::new("kill")
        .args(["-KILL", &pid.to_string()])
        .status()
        .expect("kill runs");
    assert!(killed.success(), "the daemon was killed");
    wait_for(w, "the killed daemon's end", || !alive(pid));
    w.daemon_spec.killed_pid = Some(pid);
    wait_for(w, "the daemon's lock held", || lock_held(&lock));
}

/// Let the blocker go: a daemon can start again.
fn unblock(w: &mut World) {
    let (mut blocker, unblock) = w.daemon_spec.blocker.take().expect("a blocker");
    files::put(&unblock, "go");
    let _ = blocker.wait();
}

#[given("the daemon is killed and no daemon can start")]
fn killed_and_unstartable(w: &mut World) {
    kill_and_block(w);
}

/// The TUI exits `code`; the session's command is waited for.
fn tui_exits(w: &mut World, code: i32) -> crate::tests::Ran {
    let dir = w.daemon_spec.tui_dir.clone().expect("the TUI stub's");
    files::put(dir.join("tui-exit"), code.to_string());
    files::put(dir.join("release"), "go");
    session_ran(w)
}

#[when(expr = "the session's engine exits with code {int}")]
fn engine_exits(w: &mut World, code: i32) {
    tui_exits(w, code);
}

/// The session's hold (its `.held` file), once it holds the exit report,
/// with nothing yet ending the run.
fn exit_held(w: &mut World) {
    let ran = session_ran(w);
    assert!(
        ran.stderr
            .contains("held the session's exit for the next daemon"),
        "{}",
        ran.said()
    );
    assert_eq!(holds(w).len(), 1, "the session's hold: {:?}", holds(w));
    let harp = tui_harp(w);
    assert!(
        !run_events(w, &harp).contains(&"RunEnded".to_string()),
        "nothing ended the run yet: {:?}",
        run_events(w, &harp)
    );
}

#[then("the session's exit report is held")]
fn exit_report_held(w: &mut World) {
    exit_held(w);
}

#[when("a ctxloom command starts a new daemon")]
fn command_starts_a_daemon(w: &mut World) {
    unblock(w);
    let listed = run_now(w, &["session", "list"]);
    assert_eq!(listed.code, 0, "{}", listed.said());
    let killed = w.daemon_spec.killed_pid.expect("a daemon was killed");
    assert_ne!(discovery_now(w).pid, killed, "another daemon");
}

/// The cause a run's end is recorded with, as the spec says it.
fn end_cause(said: &str) -> &'static str {
    match said {
        "runner-exit" => "RunnerExit",
        "runner-lost" => "RunnerLoss",
        other => panic!("a run's end the spec says: {other:?}"),
    }
}

/// The events of the session's run, as `test session-events` prints them.
fn run_rows(w: &World) -> Vec<Value> {
    let ran = run_now(w, &["test", "session-events", &tui_harp(w), "--run", "1"]);
    assert_eq!(ran.code, 0, "{}", ran.said());
    ran.json()["events"].as_array().cloned().unwrap_or_default()
}

/// The run's one end, once it is recorded.
fn run_end(w: &World) -> Value {
    wait_for(w, "the run's end", || {
        run_rows(w).iter().any(|e| e["type"] == "RunEnded")
    });
    let ends: Vec<Value> = run_rows(w)
        .into_iter()
        .filter(|e| e["type"] == "RunEnded")
        .collect();
    assert_eq!(ends.len(), 1, "one end: {ends:?}");
    ends[0].clone()
}

#[then(expr = "the session's run ends {string} with exit code {int}")]
fn run_ends_with_code(w: &mut World, cause: String, code: i64) {
    let end = run_end(w);
    assert_eq!(
        end["cause"],
        end_cause(&cause),
        "{end}\n{}",
        daemon_log_text(w)
    );
    assert_eq!(end["exit_code"], code, "the TUI's own exit code: {end}");
}

#[then(expr = "the session's run ends {string}")]
fn run_ends(w: &mut World, cause: String) {
    let end = run_end(w);
    assert_eq!(
        end["cause"],
        end_cause(&cause),
        "{end}\n{}",
        daemon_log_text(w)
    );
}

#[then(expr = "the session's incarnation ends {string}")]
fn incarnation_ends(w: &mut World, cause: String) {
    let harp = tui_harp(w);
    let ended = || -> Vec<String> {
        caused_events(w, &harp, false)
            .into_iter()
            .filter(|(t, _)| t == "IncarnationEnded")
            .map(|(_, cause)| cause)
            .collect()
    };
    wait_for(w, "the incarnation's end", || !ended().is_empty());
    assert_eq!(ended(), [cause], "{}", daemon_log_text(w));
}

#[then(expr = "the run was never ended {string}")]
fn never_ended(w: &mut World, cause: String) {
    assert_eq!(cause, "runner-lost", "the end the spec rules out");
    let rows = run_rows(w);
    assert!(
        !rows.iter().any(|e| e["type"] == "RunnerLostRecorded"
            || (e["type"] == "RunEnded" && e["cause"] == end_cause(&cause))),
        "{rows:?}"
    );
}

#[given(expr = "an interactive session {string} whose exit report was held by a newer ctxloom")]
fn exit_held_by_newer(w: &mut World, _session: String) {
    w.common.project = crate::tests::library::Project::initialized();
    // The session's frontend and its daemon are of a newer ctxloom; the
    // daemon is killed and none can start, so its TUI's exit report is
    // held, in the newer one's records. Its engine is the TUI stub before
    // its daemon starts.
    stub_claude(w, &tui_stub());
    start_daemon_stamped(w, NEWER_STAMP);
    tui_started(w, Some(NEWER_STAMP));
    kill_and_block(w);
    if let Some(mut gone) = w.daemon_spec.daemon.take() {
        let _ = gone.wait();
    }
    tui_exits(w, 0);
    exit_held(w);
}

#[when("an older daemon starts")]
fn older_daemon_starts(w: &mut World) {
    // This version, older than the hold's: it starts as no command of the
    // newer one would start it.
    unblock(w);
    start_daemon_idling(w, SPAWNED_IDLE_SECONDS);
}

#[then("the daemon's log says the run ended runner-lost without what its hold held")]
fn log_says_lost_without_hold(w: &mut World) {
    let log = daemon_log_text(w);
    assert!(
        log.contains("ended runner-lost without what its hold held")
            && log.contains("records held by a newer ctxloom"),
        "{log}"
    );
    assert_eq!(holds(w).len(), 1, "the hold is left: {:?}", holds(w));
}

/// A hook's answer carries additionalContext only as claude reads it: the
/// string under `hookSpecificOutput`; text that is no JSON, or JSON without
/// it, carries none.
#[test]
fn a_hooks_additional_context_is_read_as_claude_reads_it() {
    let answer = r#"{"hookSpecificOutput":{"hookEventName":"UserPromptSubmit","additionalContext":"ctxloom: your context changed"}}"#;
    assert_eq!(
        additional_context(&format!("{answer}\n")).as_deref(),
        Some("ctxloom: your context changed")
    );
    assert_eq!(
        additional_context(r#"{"additionalContext":"top level"}"#),
        None
    );
    assert_eq!(additional_context(r#"{"hookSpecificOutput":{}}"#), None);
    assert_eq!(additional_context("ctxloom: not json"), None);
    assert_eq!(additional_context(""), None);
}

/// Only the superseded-write kill names a file: its path, from the step's
/// own words; every other kill, or one worded otherwise, names none.
#[test]
fn a_kill_that_changes_a_file_before_the_retry_names_the_file() {
    assert_eq!(
        superseded_file(
            "after the write's intent is recorded, and the file \".ctxloom/a.md\" is changed by hand before the retry, to:"
        )
        .as_deref(),
        Some(".ctxloom/a.md")
    );
    assert_eq!(
        superseded_file("after the write's intent is recorded, before it is staged"),
        None
    );
    assert_eq!(
        superseded_file(
            "after the write's intent is recorded, and the file \".ctxloom/a.md\" is changed by hand, to:"
        ),
        None
    );
}

// ------------------------------------------ step 9: delegation, several children

/// The stub's record of the child `harp`, kept by its own session's name:
/// `by/<harp>` beside the stub (several children at once).
fn child_rec(w: &World, harp: &str) -> PathBuf {
    stub_dir(w).join("by").join(harp)
}

/// The turns the child `harp` took: `(conversation, message)`.
fn child_turns(w: &World, harp: &str) -> Vec<(String, String)> {
    files::read(child_rec(w, harp).join("turns"))
        .unwrap_or_default()
        .lines()
        .filter_map(|line| line.split_once(' '))
        .map(|(sid, rest)| (sid.to_string(), rest.to_string()))
        .collect()
}

/// The pending messages of `owner`'s, their texts.
fn pending_texts(w: &World, owner: &str) -> Vec<String> {
    shown(w, owner)["pending"]
        .as_array()
        .map(|pending| {
            pending
                .iter()
                .filter_map(|m| m["text"].as_str().map(str::to_string))
                .collect()
        })
        .unwrap_or_default()
}

/// `owner` was told `text` of its child `child`: for a turn's end, every
/// turn the child took told and one taken since its Given; else (its run's
/// end) once.
fn child_told(w: &World, owner: &str, child: &str, text: &str) {
    let before = w
        .daemon_spec
        .child_turns_before
        .get(child)
        .copied()
        .unwrap_or(0);
    until(w, &format!("{owner} told {text:?}"), DAEMON_WAIT, |w| {
        let pending = pending_texts(w, owner);
        let said = pending.iter().any(|t| t.contains(text));
        if !text.contains("its turn") {
            return said.then_some(());
        }
        let prefix = format!("child {child} ");
        let told = pending
            .iter()
            .filter(|t| t.starts_with(&prefix) && t.contains("its turn"))
            .count();
        let taken = child_turns(w, child).len();
        (said && taken > before && told == taken).then_some(())
    });
}

/// Alice launches `child` of `owner` herself (`session launch`), on
/// `agent` (none: the project's), with a first task.
pub(super) fn user_launches(w: &mut World, owner: &str, child: &str, agent: Option<&str>) {
    hosting_daemon(w);
    let mut args = vec!["session", "launch", owner, "--name", child];
    if let Some(agent) = agent {
        args.extend(["--agent", agent]);
    }
    args.push("a task");
    let mut env = env_of(w);
    env.push(("ANTHROPIC_API_KEY".to_string(), "a-key".to_string())); // gitleaks:allow
    let env: Vec<(&str, &str)> = env.iter().map(|(k, v)| (k.as_str(), v.as_str())).collect();
    let launched = w.common.project.run_with(&env, &args);
    assert_eq!(launched.code, 0, "{}", launched.said());
}

/// The orchestrator: Alice's session.
fn orchestrator(w: &World) -> String {
    w.daemon_spec
        .orchestrator
        .clone()
        .expect("Alice's session is running")
}

/// Her orchestrator's engine launches `child` on `agent` to `task`
/// (`child_launch`), answered with the child's session.
fn launched_by_orchestrator(w: &mut World, child: &str, agent: &str, task: &str) {
    hosting_daemon(w);
    let owner = orchestrator(w);
    engine_calls(
        w,
        &owner,
        "child_launch",
        json!({"name": child, "agent": agent, "task": task}),
    );
    w.daemon_spec.tool_child = Some(child.to_string());
    let answered = w.daemon_spec.tool.clone().expect("a tool was called");
    assert_ne!(
        answered["isError"],
        true,
        "{answered}: {}",
        daemon_log_text(w)
    );
}

/// `child`'s first turn has ended and `owner` was told so; its turns so
/// far noted, so a later step's are those after them.
fn first_turn_told(w: &mut World, owner: &str, child: &str) {
    let text = format!("child {child} finished its turn");
    until(
        w,
        &format!("{child}'s first turn to end"),
        DAEMON_WAIT,
        |w| {
            pending_texts(w, owner)
                .iter()
                .any(|t| t.contains(&text))
                .then_some(())
        },
    );
    let taken = child_turns(w, child).len();
    w.daemon_spec
        .child_turns_before
        .insert(child.to_string(), taken);
}

/// The orchestrator has launched `child` on `agent`, and its first turn
/// ended.
fn launched_idle(w: &mut World, child: &str, agent: &str) {
    launched_by_orchestrator(w, child, agent, "a task");
    let owner = orchestrator(w);
    first_turn_told(w, &owner, child);
}

/// The payloads of the hooks `child` fires in its next turn, as claude
/// fires them: its prompt `prompt`, then its words `answer`.
fn fire_for_turn(w: &World, child: &str, prompt: &str, answer: &str) {
    let dir = child_rec(w, child).join("fire");
    let n = child_turns(w, child).len();
    let prompt_id = format!("7f000000-0000-4000-8000-{n:012}");
    let mut submit: Value = serde_json::from_str(USER_PROMPT_SUBMIT).expect("JSON");
    submit["prompt"] = Value::from(prompt);
    submit["prompt_id"] = Value::from(prompt_id.as_str());
    let mut words: Value = serde_json::from_str(MESSAGE_DISPLAY).expect("JSON");
    words["prompt_id"] = Value::from(prompt_id.as_str());
    words["message_id"] = Value::from(format!("8f000000-0000-4000-8000-{n:012}"));
    words["delta"] = Value::from(answer);
    files::put(dir.join("1-UserPromptSubmit.json"), submit.to_string());
    files::put(dir.join("2-MessageDisplay.json"), words.to_string());
}

/// The words of `child`'s launch: what its stub was started with.
fn args_of_child(w: &World, child: &str) -> Vec<String> {
    files::read(child_rec(w, child).join("child-args"))
        .unwrap_or_default()
        .lines()
        .last()
        .unwrap_or_default()
        .split_whitespace()
        .map(str::to_string)
        .collect()
}

/// The quoted names in `text`, in order.
fn quoted(text: &str) -> Vec<String> {
    text.split('"')
        .enumerate()
        .filter(|(i, _)| i % 2 == 1)
        .map(|(_, name)| name.to_string())
        .collect()
}

/// The marker of the guidance only `agent`'s own profile carries.
fn guidance_of(agent: &str) -> String {
    format!("J002300-GUIDANCE-{}-only", agent.to_uppercase())
}

/// A bundle of the project's, `name`, carrying `rel` with `text`, and a
/// profile `profile` that includes it.
fn bundle_profile(w: &World, name: &str, profile: &str, rel: &str, text: &str) {
    let root = format!(".ctxloom/content/bundles/v2/{name}");
    w.common.project.write(
        &format!("{root}/bundle.yaml"),
        "schema_version: 4\nversion: 1.0.0\ndescription: an acceptance bundle\n",
    );
    w.common.project.write(&format!("{root}/{rel}"), text);
    w.common.project.write(
        ".ctxloom/content/bundles/v2/project/bundle.yaml",
        "schema_version: 4\nversion: 1.0.0\n",
    );
    w.common.project.write(
        &format!(".ctxloom/content/bundles/v2/project/profiles/{profile}.yaml"),
        &format!("description: acceptance fixture profile\nbundles:\n  - {name}\n"),
    );
}

/// The command a bundle's MCP server starts with: never in a grant record.
fn server_command(server: &str) -> String {
    format!("/opt/{server}-START-COMMAND")
}

#[given(
    regex = r#"^Alice's project defines her own agent "lead" and two agents to delegate to, each on a profile of its own that carries an MCP server of its own, and each with claude permissions of its own:$"#
)]
fn delegating_agents(w: &mut World, step: &cucumber::gherkin::Step) {
    let agents = crate::tests::gherkin_common::doc(step);
    for (profile, server) in [
        ("leading", "lead-tools"),
        ("reviewing", "review-tools"),
        ("fixing", "fix-tools"),
    ] {
        bundle_profile(
            w,
            &format!("{profile}-bundle"),
            profile,
            &format!("mcp/{server}.yaml"),
            &format!("command: {}\n", server_command(server)),
        );
    }
    w.common.project.write(
        ".ctxloom/config.yaml",
        &format!("schema_version: 9\n{agents}"),
    );
    w.daemon_spec.own_agents = true;
}

#[given(
    regex = r#"^Alice's orchestrator can delegate to two agents, "([^"]+)" and "([^"]+)", each carrying its own distinct guidance in its own profile$"#
)]
fn guided_agents(w: &mut World, first: String, second: String) {
    let mut config =
        "schema_version: 9\ndefault_agent: lead\nagents:\n  lead:\n    engine: claude-code\n"
            .to_string();
    for agent in [&first, &second] {
        let profile = format!("{agent}-profile");
        bundle_profile(
            w,
            &format!("{agent}-bundle"),
            &profile,
            &format!("fragments/{agent}-guidance.md"),
            &format!("{}\n", guidance_of(agent)),
        );
        config.push_str(&format!(
            "  {agent}:\n    engine: claude-code\n    profiles: [{profile}]\n"
        ));
    }
    w.common.project.write(".ctxloom/config.yaml", &config);
    w.daemon_spec.own_agents = true;
}

#[when(regex = r#"^Alice's orchestrator launches "([^"]+)" on "([^"]+)" to "([^"]*)"$"#)]
fn launches_one(w: &mut World, child: String, agent: String, task: String) {
    launched_by_orchestrator(w, &child, &agent, &task);
}

#[when(
    regex = r#"^Alice's orchestrator launches "([^"]+)" on "([^"]+)" to "([^"]*)", and "([^"]+)" on "([^"]+)" to "([^"]*)"$"#
)]
fn launches_two(
    w: &mut World,
    first: String,
    first_agent: String,
    first_task: String,
    second: String,
    second_agent: String,
    second_task: String,
) {
    launched_by_orchestrator(w, &first, &first_agent, &first_task);
    launched_by_orchestrator(w, &second, &second_agent, &second_task);
}

#[given(
    regex = r#"^Alice's orchestrator has launched "([^"]+)" on "([^"]+)", which is open and idle$"#
)]
fn has_launched_idle(w: &mut World, child: String, agent: String) {
    launched_idle(w, &child, &agent);
}

#[given(
    regex = r#"^Alice's orchestrator has launched "([^"]+)" on "([^"]+)", which is idle and will answer its next message "([^"]*)"$"#
)]
fn has_launched_answering(w: &mut World, child: String, agent: String, answer: String) {
    launched_idle(w, &child, &agent);
    files::put(child_rec(w, &child).join("answer"), &answer);
}

#[given(
    regex = r#"^Alice's orchestrator has launched "([^"]+)" on "([^"]+)", which is idle and whose next turn runs into its rate limit$"#
)]
fn has_launched_rate_limited(w: &mut World, child: String, agent: String) {
    launched_idle(w, &child, &agent);
    files::put(child_rec(w, &child).join("rate-limit"), "once");
}

#[given(
    regex = r#"^Alice's orchestrator has launched "([^"]+)" on "([^"]+)", which is in the middle of a turn$"#
)]
fn has_launched_busy(w: &mut World, child: String, agent: String) {
    // Its first turn is taken and left in flight.
    files::put(child_rec(w, &child).join("hold"), "once");
    launched_by_orchestrator(w, &child, &agent, "a long task");
    until(w, &format!("{child}'s turn in flight"), DAEMON_WAIT, |w| {
        (!child_turns(w, &child).is_empty()).then_some(())
    });
}

#[given(
    regex = r#"^Alice's orchestrator has launched "([^"]+)" on "([^"]+)" and "([^"]+)" on "([^"]+)", both open and idle$"#
)]
fn has_launched_two(
    w: &mut World,
    first: String,
    first_agent: String,
    second: String,
    second_agent: String,
) {
    launched_idle(w, &first, &first_agent);
    launched_idle(w, &second, &second_agent);
}

#[then(
    regex = r#"^"([^"]+)"'s session home registers the MCP servers "([^"]+)" and "([^"]+)", and neither "([^"]+)" nor "([^"]+)"$"#
)]
fn home_registers(w: &mut World, child: String, a: String, b: String, c: String, d: String) {
    until(w, &format!("{child} started"), DAEMON_WAIT, |w| {
        (!args_of_child(w, &child).is_empty()).then_some(())
    });
    let args = args_of_child(w, &child);
    let file = flag_value(&args, "--mcp-config");
    let config: Value =
        serde_json::from_str(&files::read(file).expect("its MCP config")).expect("JSON");
    let servers = config["mcpServers"]
        .as_object()
        .map(|servers| servers.keys().cloned().collect::<Vec<_>>())
        .unwrap_or_default();
    for name in [&a, &b] {
        assert!(servers.contains(name), "{name} among {servers:?}");
    }
    for name in [&c, &d] {
        assert!(!servers.contains(name), "{name} not among {servers:?}");
    }
}

/// The permissions document claude was given in `child`'s session home:
/// the `permissions` of its user-scope settings, with ctxloom's own
/// allowance (OD-4, a stated exception to "verbatim") taken out.
fn given_permissions(w: &World, child: &str) -> Value {
    let env = files::read(child_rec(w, child).join("child-env")).unwrap_or_default();
    let dir = env
        .lines()
        .find_map(|line| line.strip_prefix("CLAUDE_CONFIG_DIR="))
        .expect("its claude config dir");
    let settings: Value = serde_json::from_str(
        &files::read(Path::new(dir).join("settings.json")).expect("its settings"),
    )
    .expect("JSON");
    let mut permissions = settings["permissions"].clone();
    if let Some(allow) = permissions.get_mut("allow").and_then(Value::as_array_mut) {
        allow.retain(|tool| tool != "mcp__ctxloom__*");
        if allow.is_empty() {
            permissions.as_object_mut().map(|doc| doc.remove("allow"));
        }
    }
    permissions
}

#[then(regex = r#"^"([^"]+)"'s claude was given the permissions (\{.*\})$"#)]
fn given_the_permissions(w: &mut World, child: String, document: String) {
    let expected: Value = serde_json::from_str(&document).expect("the step's JSON");
    assert_eq!(given_permissions(w, &child), expected);
}

#[then(
    regex = r#"^the tool answered with the transcript of "([^"]+)", holding "([^"]*)" and then "([^"]*)"$"#
)]
fn tool_answered_transcript(w: &mut World, child: String, first: String, then: String) {
    let answered = w.daemon_spec.tool.clone().expect("a tool was called");
    assert_ne!(answered["isError"], true, "{answered}");
    let said = answered.to_string();
    assert!(said.contains(&child), "{said}");
    let at = |text: &str| {
        said.find(text)
            .unwrap_or_else(|| panic!("{text:?} not in the transcript: {said}"))
    };
    assert!(at(&first) < at(&then), "{first:?} before {then:?}: {said}");
}

#[then(regex = r#"^once its rate limit has reset, "([^"]+)" has still run no turn on its own$"#)]
fn no_turn_on_its_own(w: &mut World, child: String) {
    let taken = child_turns(w, &child).len();
    // Each read waits for a daemon round begun after it was asked: two
    // reads, so a round planned with the failure in the store has passed.
    for _ in 0..2 {
        let shown = shown(w, &child);
        assert!(
            shown["pending"].as_array().is_none_or(Vec::is_empty),
            "nothing owed: {shown}"
        );
    }
    assert_eq!(child_turns(w, &child).len(), taken, "no turn on its own");
}

#[when(regex = r#"^Alice's session dies and she resumes it:$"#)]
fn dies_and_resumes(w: &mut World, step: &cucumber::gherkin::Step) {
    // Her session's engine is gone: its lock is free (the seeded session
    // holds none), and she resumes it at her terminal.
    w.common.set_env("CTXLOOM_TEST_TERMINAL", "1".to_string());
    w.alice_runs(step);
    w.common
        .env
        .retain(|(name, _)| name != "CTXLOOM_TEST_TERMINAL");
}

#[when(
    regex = r#"^the engine of "([^"]+)" calls "child_launch" for "([^"]+)" with agent "([^"]+)" and task "([^"]*)"$"#
)]
fn engine_launches(w: &mut World, owner: String, child: String, agent: String, task: String) {
    w.daemon_spec
        .child_turns_before
        .insert(owner.clone(), child_turns(w, &owner).len());
    engine_calls(
        w,
        &owner,
        "child_launch",
        json!({"name": child, "agent": agent, "task": task}),
    );
    let answered = w.daemon_spec.tool.clone().expect("a tool was called");
    assert_ne!(answered["isError"], true, "{answered}");
}

/// A listener on a unix socket at `$1` that appends what each connection
/// sent to `$2`: the stand-in for claude's messaging socket (the live check
/// is `just validate-wake-claude`).
fn socket_recorder() -> String {
    "#!/bin/sh\nexec node -e 'const net = require(\"net\"), fs = require(\"fs\");\nconst [socket, out] = process.argv.slice(1);\nnet.createServer((c) => { let got = \"\"; c.on(\"data\", (d) => { got += d; }); c.on(\"end\", () => fs.appendFileSync(out, got)); }).listen(socket);' \"$1\" \"$2\"\n".to_string()
}

#[given(
    regex = r#"^Alice's session "([^"]+)" is idle in her terminal, the ctxloom MCP server its engine started watching it$"#
)]
fn idle_in_her_terminal(w: &mut World, harp: String) {
    // Her engine's messaging socket: a listener that records each post.
    let dir = w.common.project.home().join("messaging");
    let socket = dir.join("cc.sock");
    let posted = dir.join("posted.jsonl");
    let recorder = dir.join("recorder");
    files::put_program(&recorder, &socket_recorder());
    let listening = std::process::Command::new(&recorder)
        .args([socket.as_os_str(), posted.as_os_str()])
        .stdin(std::process::Stdio::null())
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::null())
        .spawn()
        .expect("the socket's listener starts");
    w.daemon_spec.wake_socket = Some((listening, posted));
    until(w, "her engine's socket listens", DAEMON_WAIT, |_| {
        files::exists(&socket).then_some(())
    });
    // The ctxloom MCP server her engine started, the socket exported to it
    // (claude exports both to what it starts).
    let me = session_name(w, &harp);
    let socket = socket.display().to_string();
    let mut env = env_of(w);
    env.push(("CLAUDE_CODE_MESSAGING_SOCKET".to_string(), socket));
    env.push((
        "CLAUDE_CODE_MESSAGING_TOKEN".to_string(),
        "a-messaging-token".to_string(),
    )); // gitleaks:allow
    let env: Vec<(&str, &str)> = env.iter().map(|(k, v)| (k.as_str(), v.as_str())).collect();
    let mcp = crate::tests::mcp_serve::Mcp::for_session(&w.common.project, &me, &env);
    w.daemon_spec.engine = Some(mcp);
    // The hold point: nothing goes on until its server watches it.
    until(w, "her session watched", DAEMON_WAIT, |w| {
        (shown(w, &harp)["wakeable"] == true).then_some(())
    });
}

/// The line the stand-in socket was posted, once it was: its auth line
/// first, then the user line, whose content is the wake line.
fn posted_wake(w: &World) -> String {
    let (_, posted) = w.daemon_spec.wake_socket.as_ref().expect("her socket");
    let posted = posted.clone();
    until(w, "a wake posted on her socket", DAEMON_WAIT, |_| {
        let text = files::read(&posted).ok()?;
        let lines: Vec<Value> = text
            .lines()
            .filter_map(|line| serde_json::from_str(line).ok())
            .collect();
        let [auth, user, ..] = lines.as_slice() else {
            return None;
        };
        assert_eq!(auth["type"], "auth", "{text}");
        assert_eq!(auth["token"], "a-messaging-token", "{text}");
        assert_eq!(user["type"], "user", "{text}");
        assert_eq!(user["message"]["role"], "user", "{text}");
        user["message"]["content"].as_str().map(str::to_string)
    })
}

#[then(regex = r#"^"([^"]+)" was woken, and its next turn began with "([^"]*)"$"#)]
fn was_woken(w: &mut World, owner: String, text: String) {
    // The terminal wake: her engine's next prompt is the posted wake line,
    // and its hook hands the session what woke it.
    if w.daemon_spec.wake_socket.is_some() {
        let line = posted_wake(w);
        assert!(line.starts_with("ctxloom: wake "), "{line}");
        let payload = json!({
            "hook_event_name": "UserPromptSubmit",
            "session_id": "native-amber",
            "prompt_id": "prompt-1",
            "prompt": line,
        })
        .to_string();
        let me = session_name(w, &owner);
        let (ran, _) = hook_run(w, "UserPromptSubmit", &payload, Some(&me));
        assert_eq!(ran.code, 0, "{}", ran.said());
        let answered: Value = serde_json::from_str(ran.stdout.trim())
            .unwrap_or_else(|_| panic!("the hook's answer: {}", ran.said()));
        let context = answered["hookSpecificOutput"]["additionalContext"]
            .as_str()
            .unwrap_or_else(|| panic!("no context: {answered}"));
        assert!(context.contains(&text), "{context}");
        assert!(
            answered.get("decision").is_none(),
            "not blocked: {answered}"
        );
        // Her engine's end closes the server's input: it exits, its poster
        // with it.
        let server = w.daemon_spec.engine.take().expect("her server");
        let (code, err) = server.finish();
        assert_eq!(code, 0, "her server at the end of its input: {err}");
        return;
    }
    let before = w
        .daemon_spec
        .child_turns_before
        .get(&owner)
        .copied()
        .unwrap_or(0);
    until(w, &format!("{owner} woken"), DAEMON_WAIT, |w| {
        child_turns(w, &owner)
            .iter()
            .skip(before)
            .any(|(_, line)| line.contains(&text))
            .then_some(())
    });
}

#[then(regex = r#"^"([^"]+)" reports "([^"]+)"'s guidance, not "([^"]+)"'s$"#)]
fn reports_guidance(w: &mut World, child: String, own: String, sibling: String) {
    until(w, &format!("{child}'s turn"), DAEMON_WAIT, |w| {
        (!child_turns(w, &child).is_empty()).then_some(())
    });
    let args = args_of_child(w, &child);
    let context =
        files::read(flag_value(&args, "--append-system-prompt-file")).expect("its context");
    assert!(context.contains(&guidance_of(&own)), "{context}");
    assert!(!context.contains(&guidance_of(&sibling)), "{context}");
}

#[given("the claude program a child runs cannot be found")]
fn claude_not_found(w: &mut World) {
    hosting_daemon(w);
    let missing = w.common.project.home().join("no-such-claude/claude");
    w.daemon_spec.stub = Some(missing);
    w.daemon_spec.stub_on_path = false;
    write_home_config(w);
}

#[then(regex = r#"^"([^"]+)" recorded no turn$"#)]
fn recorded_no_turn(w: &mut World, child: String) {
    assert!(
        child_turns(w, &child).is_empty(),
        "{:?}",
        child_turns(w, &child)
    );
    let shown = shown(w, &child);
    assert!(shown["summary"].is_null(), "no prompt: {shown}");
}

#[given(regex = r#"^Alice's project config says "([^"]+)"$"#)]
fn project_config_says(w: &mut World, line: String) {
    let config = w
        .common
        .project
        .read(".ctxloom/config.yaml")
        .unwrap_or_default();
    w.common
        .project
        .write(".ctxloom/config.yaml", &format!("{config}{line}\n"));
    // A checkout is cut from a repository: her project is one, committed.
    let project = &w.common.project;
    if !files::exists(project.dir().join(".git")) {
        project.git(&["init", "-q"]);
    }
    let committed = project
        .git_output(project.dir(), &["rev-parse", "--verify", "-q", "HEAD"])
        .status
        .success();
    if !committed {
        project.git(&["add", "-A"]);
        project.git(&[
            "-c",
            "user.name=Alice",
            "-c",
            "user.email=alice@example.com",
            "commit",
            "-q",
            "--allow-empty",
            "-m",
            "her project",
        ]);
    }
    let head = project.git(&["rev-parse", "--verify", "HEAD"]);
    assert!(!head.trim().is_empty(), "her project has a commit");
}

#[when(
    regex = r#"^"([^"]+)" and "([^"]+)" each write "([^"]+)" in turns that run at the same time$"#
)]
fn write_at_once(w: &mut World, first: String, second: String, file: String) {
    files::put(stub_dir(w).join("together"), "2");
    for child in [&first, &second] {
        files::put(child_rec(w, child).join("write"), &file);
    }
    for child in [&first, &second] {
        let sent = run_now(
            w,
            &["session", "send", child, "wake", &format!("write {file}")],
        );
        assert_eq!(sent.code, 0, "{}", sent.said());
    }
    for child in [&first, &second] {
        let owner = orchestrator(w);
        let before = w
            .daemon_spec
            .child_turns_before
            .get(child.as_str())
            .copied()
            .unwrap_or(0);
        until(
            w,
            &format!("{child}'s writing turn to end"),
            DAEMON_WAIT,
            |w| {
                let told = pending_texts(w, &owner)
                    .iter()
                    .filter(|t| t.starts_with(&format!("child {child} finished its turn")))
                    .count();
                (files::exists(child_rec(w, child).join("wrote")) && told > before).then_some(())
            },
        );
    }
    w.daemon_spec.writers = vec![first, second];
    w.daemon_spec.written = Some(file);
}

/// Where `child` works: the directory its engine was last started in.
fn child_cwd(w: &World, child: &str) -> PathBuf {
    let cwds = files::read(child_rec(w, child).join("child-cwds")).unwrap_or_default();
    PathBuf::from(cwds.lines().last().expect("it was started"))
}

#[then(regex = r#"^"([^"]+)" works in a checkout of its own, on the branch "([^"]+)"$"#)]
fn works_in_checkout(w: &mut World, child: String, branch: String) {
    let cwd = child_cwd(w, &child);
    let project = files::canonical(w.common.project.dir());
    assert_ne!(
        files::canonical(&cwd),
        project,
        "not the live project; the daemon's log:\n{}",
        daemon_log_text(w)
    );
    let head = std::process::Command::new("git")
        .args(["rev-parse", "--abbrev-ref", "HEAD"])
        .current_dir(&cwd)
        .output()
        .expect("git runs");
    assert_eq!(String::from_utf8_lossy(&head.stdout).trim(), branch);
}

#[then(
    regex = r#"^each child's "([^"]+)" is in its own checkout alone, and not in its sibling's or in Alice's project$"#
)]
fn each_in_its_own(w: &mut World, file: String) {
    let writers = w.daemon_spec.writers.clone();
    assert_eq!(writers.len(), 2, "two children wrote");
    for (me, sibling) in [(&writers[0], &writers[1]), (&writers[1], &writers[0])] {
        let mine = files::read(child_cwd(w, me).join(&file)).expect("its own file");
        assert_eq!(mine.trim(), me.as_str(), "{me}'s {file} is its own");
        assert_ne!(child_cwd(w, me), child_cwd(w, sibling), "two checkouts");
    }
    assert!(
        !files::exists(w.common.project.dir().join(&file)),
        "not in Alice's project"
    );
}

#[then(regex = r#"^"([^"]+)"'s assistant was asked to stop, and has exited$"#)]
fn asked_to_stop_and_exited(w: &mut World, child: String) {
    until(w, &format!("{child}'s engine exited"), DAEMON_WAIT, |w| {
        files::exists(child_rec(w, &child).join("child-exited")).then_some(())
    });
}

/// `cli/session.feature`: a message sent a session, as Alice sends it.
#[given(expr = "the session {string} was sent an inform {string} saying {string}")]
fn session_sent_an_inform(w: &mut World, harp: String, id: String, text: String) {
    w.seed();
    let ran = run_now(w, &["session", "send", &harp, "inform", &text, "--id", &id]);
    assert_eq!(ran.code, 0, "{}", ran.said());
}

/// `cli/session.feature`, `session stop`: once the run is over the session
/// reads ENDED, as `session show` has it (the lock's verdict: nothing holds it).
#[then(expr = "the session {string} is shown ended")]
fn session_shown_ended(w: &mut World, harp: String) {
    until(w, &format!("{harp} shown ended"), DAEMON_WAIT, |w| {
        (shown(w, &harp)["state"] == "ENDED").then_some(())
    });
}

#[then(regex = r#"^the record of what "([^"]+)" was launched with names the MCP servers (.*)$"#)]
fn record_names(w: &mut World, child: String, names: String) {
    let (named, not) = names
        .split_once(", and not ")
        .map_or((names.as_str(), ""), |(named, not)| (named, not));
    let grant = until(w, &format!("{child}'s record"), DAEMON_WAIT, |w| {
        let shown = shown(w, &child);
        shown.get("grant").cloned()
    });
    let mut servers: Vec<String> = grant["mcp_servers"]
        .as_array()
        .map(|names| {
            names
                .iter()
                .filter_map(Value::as_str)
                .map(str::to_string)
                .collect()
        })
        .unwrap_or_default();
    servers.sort();
    let mut expected = quoted(named);
    expected.sort();
    assert_eq!(servers, expected, "{grant}");
    for name in quoted(not) {
        assert!(!servers.contains(&name), "{name}: {grant}");
    }
}

#[then("neither record holds the command that starts an MCP server")]
fn records_hold_no_command(w: &mut World) {
    for child in ["quiet-silver-fern", "steady-lunar-pike"] {
        let shown = shown(w, child).to_string();
        for server in ["lead-tools", "review-tools", "fix-tools"] {
            assert!(!shown.contains(&server_command(server)), "{child}: {shown}");
        }
        assert!(!shown.contains("START-COMMAND"), "{child}: {shown}");
    }
}

// ------------------------------------- the frontend tells its human of an ask

/// What a terminal session's frontend records of its watch for asks under
/// test-support (`test-frontend-asks` in ctxloom's home): a line `look`
/// after each look at the waiting asks, and `told <ask>` after each ask
/// it showed. The scenarios wait on it: the interleavings they are about
/// are forced, never waited for by the clock.
fn frontend_asks_record(w: &World) -> String {
    files::read(w.common.project.home().join(".ctxloom/test-frontend-asks")).unwrap_or_default()
}

fn frontend_lines_starting(w: &World, word: &str) -> usize {
    frontend_asks_record(w)
        .lines()
        .filter(|line| line.split(' ').next() == Some(word))
        .count()
}

/// The claude stub of both the TUI and the children it launches: a call
/// that streams (`claude -p --output-format stream-json`) is the child's
/// turn, any other the TUI.
fn tui_and_child_stub() -> String {
    format!(
        "case \"$*\" in\n  *stream-json*)\n    (\n{child}\n    )\n    exit $? ;;\nesac\n{tui}",
        child = child_stub(),
        tui = tui_stub()
    )
}

#[given(
    expr = "an interactive session {string} is running in a terminal, with a child {string} of it"
)]
fn session_with_a_child(w: &mut World, harp: String, child: String) {
    primary_running_on(w, harp.clone(), &tui_and_child_stub());
    let mut env = env_of(w);
    env.push(("ANTHROPIC_API_KEY".to_string(), "a-key".to_string())); // gitleaks:allow
    let launched = w.common.project.run_with(
        &env_pairs(&env),
        &["session", "launch", &harp, "--name", &child, "a task"],
    );
    assert_eq!(launched.code, 0, "{}", launched.said());
    let stub = w.daemon_spec.tui_dir.clone().expect("the stub's directory");
    until(w, "the child's first turn", DAEMON_WAIT, |_| {
        files::exists(stub.join("child-turn")).then_some(())
    });
}

#[then(regex = r"^the session's frontend has told its human of (\d+) asks?$")]
fn frontend_told(w: &mut World, count: usize) {
    until(w, "the frontend's notice of the ask", DAEMON_WAIT, |w| {
        (frontend_lines_starting(w, "told") >= count).then_some(())
    });
    assert_eq!(frontend_lines_starting(w, "told"), count);
}

#[then(regex = r"^the session's frontend has looked for asks (\d+) more times$")]
fn frontend_looked(w: &mut World, more: usize) {
    let before = frontend_lines_starting(w, "look");
    until(w, "the frontend's looks", DAEMON_WAIT, |w| {
        (frontend_lines_starting(w, "look") >= before + more).then_some(())
    });
}

#[when("the session's terminal is released and the session ends")]
fn terminal_released(w: &mut World) {
    let stub = w.daemon_spec.tui_dir.clone().expect("the stub's directory");
    files::put(stub.join("release"), "go");
    session_ran(w);
}

#[then(regex = r"^the session's terminal rang the bell (\d+) times?$")]
fn terminal_rang(w: &mut World, times: usize) {
    let session = session_ran(w);
    assert_eq!(
        session.stderr.matches('\u{7}').count(),
        times,
        "{}",
        session.said()
    );
}

/// The claude statusline as the session `amber-swift-owl`'s engine runs it
/// (a payload on stdin), what it printed.
fn statusline_of(w: &World) -> String {
    let session = session_name(w, "amber-swift-owl");
    let (ran, _) = hook_run(
        w,
        "statusline",
        r#"{"model":{"display_name":"Opus 5"}}"#,
        Some(&session),
    );
    assert_eq!(ran.code, 0, "{}", ran.said());
    assert_eq!(ran.stderr, "", "a diagnostic would corrupt the bar");
    ran.stdout
}

#[then(expr = "the session's statusline says {string}")]
fn statusline_says(w: &mut World, text: String) {
    let mut last = String::new();
    until(w, "the statusline's count of asks", DAEMON_WAIT, |w| {
        last = statusline_of(w);
        last.contains(&text).then_some(())
    });
    assert!(last.contains(&format!("\u{26a0} {text}")), "{last:?}");
}

#[then("the session's statusline says nothing of asks")]
fn statusline_says_nothing(w: &mut World) {
    until(w, "the statusline's count to clear", DAEMON_WAIT, |w| {
        (!statusline_of(w).contains("waiting")).then_some(())
    });
}

#[when("the claude statusline runs for a session of the project, with no daemon serving")]
fn statusline_without_daemon(w: &mut World) {
    // The project's name is the daemon's to give (init asked it), so the
    // session's name is taken before the daemon is stopped.
    let session = session_name(w, "amber-swift-owl");
    let stopped = run_now(w, &["daemon", "stop"]);
    assert_eq!(stopped.code, 0, "{}", stopped.said());
    until(w, "the daemon to be gone", DAEMON_WAIT, |w| {
        (!serving_now(w)).then_some(())
    });
    let (ran, _) = hook_run(
        w,
        "statusline",
        r#"{"model":{"display_name":"Opus 5"}}"#,
        Some(&session),
    );
    assert_eq!(ran.code, 0, "{}", ran.said());
    w.daemon_spec.statusline = Some(ran.stdout);
}

#[then("the statusline shows nothing of asks")]
fn statusline_shows_nothing(w: &mut World) {
    let shown = w.daemon_spec.statusline.clone().expect("it ran");
    assert!(!shown.contains("waiting"), "{shown:?}");
}

#[then("no daemon is serving")]
fn no_daemon_serving(w: &mut World) {
    assert!(!serving_now(w), "{}", daemon_log_text(w));
}

#[then("the session's terminal was shown no line about an ask")]
fn terminal_shown_no_line(w: &mut World) {
    let session = session_ran(w);
    assert!(
        !session.stderr.contains("session approvals answer")
            && !session.stderr.contains("asks to run"),
        "{}",
        session.said()
    );
}
