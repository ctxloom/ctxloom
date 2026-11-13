//! ctxloom's own MCP server, `ctxloom mcp serve` (slice 3: T20, S45, S46,
//! OD-2), end to end: the real binary in a temporary home, spoken to over
//! stdio in JSON-RPC lines written here by hand (never an MCP SDK's client),
//! so the wire is checked on its own. The four tools no scenario of
//! `cli/mcp_serve.feature` covers are held here: `session_list`,
//! `session_show`, `transcript_show` and `context_assemble`. A tool's call
//! is a client of the user's daemon (started by the first call that needs
//! it), in the project the session's resource name names.

use std::io::{BufRead, BufReader, Write};
use std::process::{Child, ChildStdin};
use std::sync::mpsc::{channel, Receiver};
use std::time::{Duration, Instant};

use serde_json::{json, Value};

use super::library::Project;

/// The tools the server offers before approvals are built (OD-1: the
/// approval prompt is not listed until it is).
const TOOLS: [&str; 8] = [
    "child_clear",
    "child_close",
    "child_launch",
    "child_send",
    "context_assemble",
    "session_list",
    "session_show",
    "transcript_show",
];

/// A short idle for the daemon a call spawns: one left behind exits.
const IDLE: (&str, &str) = ("CTXLOOM_DAEMON_IDLE_SECONDS", "60");

/// How long one answer may take: a call that spawns the daemon waits for
/// it (S16's bound) before it is answered.
const ANSWER_WAIT: Duration = Duration::from_secs(60);

/// A running `ctxloom mcp serve`, its stdout read line by line.
pub(crate) struct Mcp {
    child: Child,
    stdin: Option<ChildStdin>,
    lines: Receiver<String>,
    /// Every line it wrote to stdout, in order.
    pub(crate) said: Vec<String>,
    next: u64,
}

impl Mcp {
    /// `ctxloom mcp serve` in `project` with `env`, NOT yet initialized.
    pub(crate) fn spawn(project: &Project, env: &[(&str, &str)]) -> Mcp {
        let mut child = project.start_talking(env, &["mcp", "serve"]);
        let stdin = child.stdin.take();
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
        Mcp {
            child,
            stdin,
            lines,
            said: Vec::new(),
            next: 0,
        }
    }

    /// `ctxloom mcp serve` for the session `session` (its resource name, as
    /// the engine's environment carries it), initialized.
    pub(crate) fn for_session(project: &Project, session: &str, env: &[(&str, &str)]) -> Mcp {
        let mut all = vec![("CTXLOOM_SESSION", session), IDLE];
        all.extend_from_slice(env);
        let mut mcp = Mcp::spawn(project, &all);
        let init = mcp.request(
            "initialize",
            json!({
                "protocolVersion": "2025-06-18",
                "capabilities": {},
                "clientInfo": {"name": "acceptance", "version": "0"}
            }),
        );
        assert!(init.get("result").is_some(), "initialize: {init}");
        mcp.send(json!({"jsonrpc": "2.0", "method": "notifications/initialized"}));
        mcp
    }

    fn send(&mut self, message: Value) {
        let stdin = self.stdin.as_mut().expect("stdin is open");
        writeln!(stdin, "{message}").expect("the server reads its stdin");
        stdin.flush().expect("flushed");
    }

    /// The answer to the request `method` with `params`: the whole response.
    pub(crate) fn request(&mut self, method: &str, params: Value) -> Value {
        self.next += 1;
        let id = self.next;
        self.send(json!({"jsonrpc": "2.0", "id": id, "method": method, "params": params}));
        let deadline = Instant::now() + ANSWER_WAIT;
        loop {
            let left = deadline.saturating_duration_since(Instant::now());
            let line = self.lines.recv_timeout(left).unwrap_or_else(|_| {
                panic!(
                    "no answer to {method} within {ANSWER_WAIT:?}; it said {:?}",
                    self.said
                )
            });
            self.said.push(line.clone());
            let message: Value = serde_json::from_str(&line)
                .unwrap_or_else(|e| panic!("stdout is JSON-RPC only, not {line:?}: {e}"));
            if message.get("id") == Some(&json!(id)) {
                return message;
            }
        }
    }

    /// `tools/call` of `tool` with `arguments`: its result.
    pub(crate) fn call(&mut self, tool: &str, arguments: Value) -> Value {
        let answer = self.request("tools/call", json!({"name": tool, "arguments": arguments}));
        answer
            .get("result")
            .cloned()
            .unwrap_or_else(|| panic!("{tool} was not answered with a result: {answer}"))
    }

    /// Close its stdin and wait for it (bounded): its exit code, and what it
    /// wrote to stderr. What it wrote to stdout after is in `said`.
    pub(crate) fn finish(mut self) -> (i32, String) {
        drop(self.stdin.take());
        let deadline = Instant::now() + Duration::from_secs(20);
        let status = loop {
            if let Some(status) = self.child.try_wait().expect("waitable") {
                break status;
            }
            if Instant::now() > deadline {
                let _ = self.child.kill();
                panic!("the server did not exit at the end of its input");
            }
            std::thread::sleep(Duration::from_millis(20));
        };
        while let Ok(line) = self.lines.try_recv() {
            self.said.push(line);
        }
        let mut err = String::new();
        if let Some(mut stderr) = self.child.stderr.take() {
            let _ = std::io::Read::read_to_string(&mut stderr, &mut err);
        }
        (status.code().unwrap_or(-1), err)
    }
}

impl Drop for Mcp {
    fn drop(&mut self) {
        let _ = self.child.kill();
        let _ = self.child.wait();
    }
}

/// The first text content of a tool's result, as JSON.
pub(crate) fn answered_json(result: &Value) -> Value {
    let text = result["content"][0]["text"]
        .as_str()
        .unwrap_or_else(|| panic!("no text content: {result}"));
    serde_json::from_str(text).unwrap_or_else(|e| panic!("not JSON: {text}: {e}"))
}

/// A project with two sessions recorded, and the daemon stopped when the
/// test ends.
struct World {
    project: Project,
}

impl World {
    fn new() -> World {
        World {
            project: Project::initialized(),
        }
    }

    fn seed(&self, harp: &str, flags: &[&str]) {
        let mut args = vec!["test", "seed-session", harp];
        args.extend_from_slice(flags);
        let ran = self.project.run_with(&[IDLE], &args);
        assert_eq!(ran.code, 0, "seed {harp}: {}", ran.said());
    }

    /// The project's resource name, as the daemon names it.
    fn project_name(&self) -> String {
        let ran = self.project.run_with(&[IDLE], &["test", "project-name"]);
        assert_eq!(ran.code, 0, "{}", ran.said());
        ran.stdout.trim().to_string()
    }

    fn session(&self, harp: &str) -> String {
        format!("{}/sessions/{harp}", self.project_name())
    }
}

impl Drop for World {
    fn drop(&mut self) {
        let _ = self.project.run(&["daemon", "stop", "--even-if-live"]);
    }
}

// S45, OD-2: started with no session to speak as, the server refuses rather
// than act as "the user": a decline, its code its crate's.
#[test]
fn test_mcp_serve_without_a_session_name_is_refused() {
    let project = Project::initialized();
    let ran = project.run(&["mcp", "serve", "--format", "json"]);
    assert_eq!(ran.code, 20, "{}", ran.said());
    assert_eq!(
        ran.err_json()["code"],
        "mcp-server-no-session",
        "{}",
        ran.said()
    );
    assert!(
        ran.stdout.is_empty(),
        "nothing on the protocol's channel: {}",
        ran.stdout
    );
}

// S45 (startup): `initialize` and `tools/list` are answered from a static
// list, reaching no daemon (claude must not time the server out while one
// starts); stdout carries JSON-RPC and nothing else; the end of its input
// ends it, exit 0.
#[test]
fn test_mcp_serve_offers_its_tools_without_reaching_a_daemon() {
    let project = Project::initialized();
    let mut mcp = Mcp::for_session(
        &project,
        "projects/amber-quiet-heron/sessions/brisk-copper-moth",
        &[],
    );
    let listed = mcp.request("tools/list", json!({}));
    let mut names: Vec<String> = listed["result"]["tools"]
        .as_array()
        .unwrap_or_else(|| panic!("no tools: {listed}"))
        .iter()
        .map(|tool| tool["name"].as_str().unwrap_or_default().to_string())
        .collect();
    names.sort();
    assert_eq!(names, TOOLS, "{listed}");
    for tool in listed["result"]["tools"].as_array().unwrap() {
        assert_eq!(tool["inputSchema"]["type"], "object", "{tool}");
    }
    let (code, err) = mcp.finish();
    assert_eq!(code, 0, "{err}");
    assert!(
        !super::files::exists(project.home().join(".ctxloom/daemon/daemon.discovery")),
        "no daemon was reached to list the tools"
    );
}

// S46: `session_list` and `session_show` answer the daemon's `ListSessions`
// and `GetSession`, as their proto3 JSON (ruling 146), in the project the
// session's name names (OD-2); `session_show` takes a harp of that project
// or a full name.
#[test]
fn test_session_list_and_show_answer_the_daemons_sessions() {
    let world = World::new();
    world.seed("amber-quiet-heron", &[]);
    world.seed("brisk-copper-moth", &[]);
    let me = world.session("amber-quiet-heron");
    let mut mcp = Mcp::for_session(&world.project, &me, &[]);
    let listed = answered_json(&mcp.call("session_list", json!({})));
    let mut names: Vec<String> = listed["sessions"]
        .as_array()
        .unwrap_or_else(|| panic!("no sessions: {listed}"))
        .iter()
        .map(|s| s["name"].as_str().unwrap().to_string())
        .collect();
    names.sort();
    assert_eq!(
        names,
        [
            world.session("amber-quiet-heron"),
            world.session("brisk-copper-moth")
        ],
        "{listed}"
    );
    for asked in [
        "brisk-copper-moth".to_string(),
        world.session("brisk-copper-moth"),
    ] {
        let shown = answered_json(&mcp.call("session_show", json!({"session": asked})));
        assert_eq!(shown["name"], world.session("brisk-copper-moth"), "{shown}");
        assert!(
            shown["uid"].as_str().is_some_and(|uid| !uid.is_empty()),
            "{shown}"
        );
    }
    let missing = mcp.call("session_show", json!({"session": "no-such-harp"}));
    assert_eq!(missing["isError"], true, "{missing}");
    let (code, err) = mcp.finish();
    assert_eq!(code, 0, "{err}");
}

// S46, slice 3 step 5: `transcript_show` answers the daemon's
// `GetTranscript`, a session's D27 lines as delivered so far; a session
// with none captured is a typed refusal, said as the tool's error.
#[test]
fn test_transcript_show_answers_the_lines_delivered_so_far() {
    let world = World::new();
    world.seed("amber-quiet-heron", &["--transcript"]);
    world.seed("brisk-copper-moth", &[]);
    let me = world.session("amber-quiet-heron");
    let mut mcp = Mcp::for_session(&world.project, &me, &[]);
    let shown =
        answered_json(&mcp.call("transcript_show", json!({"session": "amber-quiet-heron"})));
    assert_eq!(shown["name"], format!("{me}/transcript"), "{shown}");
    let lines = shown["lines"]
        .as_array()
        .unwrap_or_else(|| panic!("no lines: {shown}"));
    assert!(!lines.is_empty(), "{shown}");
    for line in lines {
        let entry: Value = serde_json::from_str(line["json"].as_str().unwrap()).unwrap();
        assert_eq!(entry["harp"], "amber-quiet-heron", "{entry}");
    }
    let none = mcp.call("transcript_show", json!({"session": "brisk-copper-moth"}));
    assert_eq!(none["isError"], true, "{none}");
    assert!(
        none["content"][0]["text"]
            .as_str()
            .is_some_and(|text| text.contains("daemon-api-transcript-empty")),
        "{none}"
    );
    let (code, err) = mcp.finish();
    assert_eq!(code, 0, "{err}");
}

// S46, slice 3 step 6: `context_assemble` loads the premised fragments the
// agent chose (its `bundles` argument, the names its premise index lists)
// through the daemon's `AssembleContext`, from the project's registered
// root; what it names alone, no default profile; a name nobody has is
// said, never a refusal.
#[test]
fn test_context_assemble_loads_the_fragments_the_agent_chose() {
    let world = World::new();
    world.project.bundle("demo");
    world.project.write(
        ".ctxloom/content/bundles/v2/demo/fragments/golang.md",
        "---\ndescription: when editing Go code\n---\nGO-RULES-BODY\n",
    );
    world.project.fragment("demo", "testing");
    world.project.profile("dev", "demo");
    world.seed("amber-quiet-heron", &[]);
    let me = world.session("amber-quiet-heron");
    let mut mcp = Mcp::for_session(&world.project, &me, &[]);
    let assembled = answered_json(&mcp.call(
        "context_assemble",
        json!({"bundles": ["demo#fragments/golang", "demo#fragments/nobody"]}),
    ));
    let context = assembled["context"].as_str().unwrap_or_default();
    assert!(context.contains("GO-RULES-BODY"), "{assembled}");
    assert!(
        !context.contains("FRAGMENT-BODY-testing"),
        "only what it named: {assembled}"
    );
    let said = assembled.to_string();
    assert!(
        said.contains("demo#fragments/nobody"),
        "the missing one is said: {assembled}"
    );
    let (code, err) = mcp.finish();
    assert_eq!(code, 0, "{err}");
}
