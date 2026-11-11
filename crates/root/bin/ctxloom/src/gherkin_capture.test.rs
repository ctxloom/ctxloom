//! The steps of j001000's stub-engine scenario: hook capture, end to end,
//! through the real binary.
//!
//! The assistant is a stub `claude` on `PATH` (an interactive run, the
//! terminal stood in by `CTXLOOM_TEST_TERMINAL` as j000200 does). It does what
//! claude does with a session home: it reads the hook commands the home's
//! `settings.json` registers and runs each as a separate process, with the
//! environment it was itself given (the engine's: `CTXLOOM_*`,
//! `CLAUDE_CONFIG_DIR`, the `PATH`; the commands are never hard-coded here),
//! feeding each the payload of the real shapes the engine-claude fixtures
//! hold. Each hook is fired twice, so a redelivery shows end to end. A
//! `ctxloom` shim on the stub's `PATH` is the binary under test.
//!
//! After the run the same command is run once more with the environment the
//! stub recorded (the run's bearer, after the run is over), as a hook that
//! outlived its engine would.

use std::path::PathBuf;

use std::process::{Command, Stdio};

use cucumber::then;
use serde_json::{json, Value};

use super::World;
use crate::tests::gherkin_common::CommonWorld;
use crate::tests::{files, Ran};

const USER_PROMPT_SUBMIT: &str =
    include_str!("../../../../adapters/driven/engine-claude/src/fixtures/user_prompt_submit.json");
const MESSAGE_DISPLAY: &str =
    include_str!("../../../../adapters/driven/engine-claude/src/fixtures/message_display.json");
const POST_TOOL_BATCH: &str =
    include_str!("../../../../adapters/driven/engine-claude/src/fixtures/post_tool_batch.json");
const STOP: &str = include_str!("../../../../adapters/driven/engine-claude/src/fixtures/stop.json");
const SESSION_END: &str =
    include_str!("../../../../adapters/driven/engine-claude/src/fixtures/session_end.json");

/// What the prompt says.
const PROMPT: &str = "Run the shell command: echo hi";
/// The assistant's words, in the deltas it said them.
const DELTAS: [&str; 3] = ["The command `echo hi` ", "ran and ", "printed `hi`."];
/// The tool's response: a body no record may keep.
const TOOL_BODY: &str = "TOOL-RESPONSE-BODY-THAT-MUST-NOT-BE-KEPT";

/// The digest of [`TOOL_BODY`] (`sha256sum`).
const TOOL_DIGEST: &str = "71ca0ef886ae43a96de885a15d913cc2cbe0553f553638a28601890879ed97e3";

/// The stub engine.
const STUB: &str = r#"here="$(dirname "$0")"
fired="$here/fired"
mkdir -p "$fired"
for v in CTXLOOM_ENDPOINT CTXLOOM_BEARER CTXLOOM_RUN CTXLOOM_HOME CLAUDE_CONFIG_DIR; do
  eval "[ -n \"\${$v:-}\" ]" || echo "$v" >> "$here/engine-missing"
done
[ "${HOME:-}" = "$CLAUDE_CONFIG_DIR" ] || echo "HOME" >> "$here/engine-had"
env | grep -E '^(CTXLOOM_|CLAUDE_CONFIG_DIR=|PATH=)' > "$here/engine-env"
pwd > "$here/engine-cwd"
n=0
for payload in "$here"/payloads/*.json; do
  event="$(basename "$payload" .json)"
  event="${event#*-}"
  cmd="$(jq -r --arg e "$event" '.hooks[$e][0].hooks[0] | ([.command] + .args) | join(" ")' "$CLAUDE_CONFIG_DIR/settings.json")"
  for again in 1 2; do
    n=$((n + 1))
    id="$(printf %03d "$n")"
    echo "$event" > "$fired/$id.event"
    echo "$cmd" > "$fired/$id.cmd"
    sh -c "$cmd" < "$payload" > "$fired/$id.out" 2> "$fired/$id.err"
    echo "$?" > "$fired/$id.code"
  done
done
ctxloom session transcript show "$CTXLOOM_SESSION_HARP" --format json > "$here/live-transcript" 2> "$here/live-transcript.err" || true
"#;

/// The stub child: a `claude -p` turn. It reads the ONE `--settings` file its
/// command line names (the session dir's, as claude does), records what it
/// registers, reads the prompt from stdin, runs each registered hook command
/// for its payloads (the prompt the one it was given) as a separate process
/// with its own environment, twice, and answers as claude's stream-json does.
const CHILD_STUB: &str = r#"here="$(dirname "$0")"
fired="$here/fired"
mkdir -p "$fired"
settings=""
seen=0
while [ $# -gt 0 ]; do
  if [ "$1" = "--settings" ]; then settings="$2"; seen=$((seen + 1)); shift; fi
  shift
done
echo "$seen" > "$here/settings-flags"
jq -r '.hooks | keys[]' "$settings" > "$here/registered"
prompt="$(cat)"
for v in CTXLOOM_ENDPOINT CTXLOOM_BEARER CTXLOOM_RUN CTXLOOM_HOME CTXLOOM_SESSION_HARP CTXLOOM_TLS_CERT CLAUDE_CONFIG_DIR; do
  eval "[ -n \"\${$v:-}\" ]" || echo "$v" >> "$here/engine-missing"
done
[ "${HOME:-}" = "$CLAUDE_CONFIG_DIR" ] || echo "HOME" >> "$here/engine-had"
[ -z "${ANTHROPIC_AUTH_TOKEN:-}" ] || echo "ANTHROPIC_AUTH_TOKEN" >> "$here/engine-had"
env | grep -E '^(CTXLOOM_|CLAUDE_CONFIG_DIR=|PATH=)' > "$here/engine-env"
n=0
for payload in "$here"/payloads/*.json; do
  [ -e "$payload" ] || continue
  event="$(basename "$payload" .json)"
  event="${event#*-}"
  cmd="$(jq -r --arg e "$event" '.hooks[$e][0].hooks[0] | ([.command] + .args) | join(" ")' "$settings")"
  for again in 1 2; do
    n=$((n + 1))
    id="$(printf %03d "$n")"
    echo "$event" > "$fired/$id.event"
    echo "$cmd" > "$fired/$id.cmd"
    jq -c --arg p "$prompt" 'if .hook_event_name == "UserPromptSubmit" then .prompt = $p else . end' "$payload"       | sh -c "$cmd" > "$fired/$id.out" 2> "$fired/$id.err"
    echo "$?" > "$fired/$id.code"
  done
done
echo '{"type":"system","subtype":"init","session_id":"9d764b45-a56a-48d7-ac9d-c2a16c5669f1"}'
echo '{"type":"result","subtype":"success","is_error":false,"result":"J001000-CHILD-REPLY","session_id":"9d764b45-a56a-48d7-ac9d-c2a16c5669f1"}'
"#;

/// The reply the stub child's stream gives.
const CHILD_REPLY: &str = "J001000-CHILD-REPLY";

/// What the scenario keeps.
#[derive(Default)]
pub(super) struct State {
    /// The transcript's lines as they stood when the run ended.
    ended: Vec<Value>,
    /// How many hook processes the stub was to fire.
    firings: usize,
}

impl World {
    fn engine_dir(&self) -> PathBuf {
        self.adopt_scratch().join("bin")
    }

    fn harp(&self) -> String {
        let sessions = self.common.project.dir().join(".ctxloom/sessions");
        let names: Vec<String> = files::children(&sessions)
            .iter()
            .filter(|p| files::is_dir(p))
            .filter_map(|p| p.file_name().map(|n| n.to_string_lossy().into_owned()))
            .collect();
        assert_eq!(names.len(), 1, "one session was started: {names:?}");
        names[0].clone()
    }

    /// The session's transcript as `session transcript show` reads it (owner
    /// ruling 87a: from the session's events, no file); none when nothing
    /// was captured.
    fn transcript(&self) -> Vec<Value> {
        read_transcript(self, &self.harp())
    }

    /// What each firing left: its event, command, answer, stderr and code.
    fn fired(&self) -> Vec<(String, String, String, String, String)> {
        let dir = self.engine_dir().join("fired");
        let mut ids: Vec<String> = files::children(&dir)
            .iter()
            .filter_map(|p| p.file_stem().map(|n| n.to_string_lossy().into_owned()))
            .collect();
        ids.sort();
        ids.dedup();
        let read = |id: &str, ext: &str| {
            files::read(dir.join(format!("{id}.{ext}")))
                .unwrap_or_default()
                .trim()
                .to_string()
        };
        ids.iter()
            .map(|id| {
                (
                    read(id, "event"),
                    read(id, "cmd"),
                    read(id, "out"),
                    files::read(dir.join(format!("{id}.err"))).unwrap_or_default(),
                    read(id, "code"),
                )
            })
            .collect()
    }
}

/// `harp`'s transcript, read by `ctxloom session transcript show --format
/// json` in the project (owner ruling 87a): its lines; none when the session
/// captured nothing.
pub(super) fn read_transcript(w: &World, harp: &str) -> Vec<Value> {
    let env: Vec<(&str, &str)> = w
        .common
        .env
        .iter()
        .map(|(k, v)| (k.as_str(), v.as_str()))
        .collect();
    let ran = w.common.project.run_with(
        &env,
        &["session", "transcript", "show", harp, "--format", "json"],
    );
    if ran.code != 0 {
        assert!(
            ran.stderr.contains("no-captured-transcript"),
            "the transcript could not be read: {}",
            ran.said()
        );
        return Vec::new();
    }
    ran.stdout
        .lines()
        .filter(|l| !l.trim().is_empty())
        .map(|l| serde_json::from_str(l).unwrap_or_else(|e| panic!("{l:?}: {e}")))
        .collect()
}

/// Ruling 87a: what a session's directory may hold, now that nothing of its
/// events is projected there: its session home (and what the engine itself
/// writes in it), never `metadata.json`, `records/` or `transcripts/`.
pub(super) fn holds_only_its_home(dir: &std::path::Path) {
    for gone in ["metadata.json", "records", "transcripts"] {
        assert!(
            !files::exists(dir.join(gone)),
            "{} holds {gone}: ruling 87a writes nothing of the events there",
            dir.display()
        );
    }
    assert!(
        files::is_dir(dir.join("home")),
        "{} holds its session home",
        dir.display()
    );
}

#[then("the session's directory holds its session home and nothing projected from its events")]
fn directory_holds_home(w: &mut World) {
    let dir = w
        .common
        .project
        .dir()
        .join(".ctxloom/sessions")
        .join(w.harp());
    holds_only_its_home(&dir);
}

fn payload(template: &str, edit: impl FnOnce(&mut Value)) -> String {
    let mut value: Value = serde_json::from_str(template).expect("a fixture");
    edit(&mut value);
    value.to_string()
}

/// The `Alice ...:` step that runs the scenario (the generic `Alice ...:`
/// step is the World's `alice_runs`, which hands it here): whether it is
/// that step.
pub(super) fn fires(w: &mut World, step: &cucumber::gherkin::Step) -> bool {
    if step.value.contains("fires its session's hooks") {
        run_firing_hooks(w, step);
    } else if step.value.contains("fires its turn's hooks") {
        run_child(w, step, true);
    } else if step.value.contains("fires no hooks") {
        run_child(w, step, false);
    } else if step
        .value
        .contains("another terminal lists, renames and removes")
    {
        run_watched(w, step);
    } else if step.value.contains("stops ctxloom with a SIGTERM") {
        run_stopped(w, step);
    } else {
        return false;
    }
    true
}

/// A one-shot whose stub child fires the turn's hooks (`fire`), or none.
fn run_child(w: &mut World, step: &cucumber::gherkin::Step, fire: bool) {
    let bin = w.engine_dir();
    let ctxloom = crate::tests::binary().display().to_string();
    files::put_program(bin.join("ctxloom"), &format!("exec '{ctxloom}' \"$@\"\n"));
    if fire {
        // The prompt is the one the turn was given on stdin (the stub sets it).
        let mut payloads = vec![(USER_PROMPT_SUBMIT.to_string(), "UserPromptSubmit")];
        for (index, delta) in DELTAS.iter().enumerate() {
            payloads.push((
                payload(MESSAGE_DISPLAY, |v| {
                    v["index"] = json!(index);
                    v["final"] = json!(index == DELTAS.len() - 1);
                    v["delta"] = json!(delta);
                }),
                "MessageDisplay",
            ));
        }
        payloads.push((
            payload(POST_TOOL_BATCH, |v| {
                v["tool_calls"][0]["tool_response"] = json!(TOOL_BODY);
            }),
            "PostToolBatch",
        ));
        payloads.push((
            payload(STOP, |v| {
                v["last_assistant_message"] = json!(DELTAS.concat());
            }),
            "Stop",
        ));
        for (n, (text, event)) in payloads.iter().enumerate() {
            files::put(bin.join(format!("payloads/{n:02}-{event}.json")), text);
        }
        w.capture.firings = 2 * payloads.len();
    }
    files::put_program(bin.join("claude"), CHILD_STUB);
    // The primary's own token is set, and must not reach the child.
    w.common
        .set_env("ANTHROPIC_AUTH_TOKEN", "primary-only-not-real".to_string()); // gitleaks:allow
    w.before_run();
    let lines = crate::tests::gherkin_common::doc(step);
    w.common_mut().run_lines(&lines, "");
}

#[then("the assistant's reply is printed")]
fn reply_printed(w: &mut World) {
    w.ran().succeeds().contains(CHILD_REPLY);
}

#[then(
    "the assistant's turn held what its hooks need to reach ctxloom, and not the primary's token"
)]
fn child_environment(w: &mut World) {
    let dir = w.engine_dir();
    assert_eq!(
        files::read(dir.join("engine-missing")).unwrap_or_default(),
        "",
        "the turn's environment lacked variables"
    );
    assert_eq!(
        files::read(dir.join("engine-had")).unwrap_or_default(),
        "",
        "the turn's environment held what it must not"
    );
    let engine_env = files::read(dir.join("engine-env")).expect("the turn ran");
    assert!(
        engine_env.contains("CTXLOOM_ENDPOINT=https://127.0.0.1:"),
        "{engine_env}"
    );
    assert!(
        !engine_env.contains("PRIVATE KEY"),
        "no key in the environment"
    );
}

#[then("the assistant's turn registered the hooks of a turn and not those of a session")]
fn child_registered(w: &mut World) {
    let dir = w.engine_dir();
    assert_eq!(
        files::read(dir.join("settings-flags"))
            .unwrap_or_default()
            .trim(),
        "1",
        "one --settings"
    );
    let mut registered: Vec<String> = files::read(dir.join("registered"))
        .expect("the settings were read")
        .lines()
        .map(String::from)
        .collect();
    registered.sort();
    assert_eq!(
        registered,
        [
            "MessageDisplay",
            "PostCompact",
            "PostToolBatch",
            "PreCompact",
            "Stop",
            "UserPromptSubmit"
        ]
    );
}

#[then("the session's transcript holds her prompt, the assistant's words in order, the tool call as a digest and the turn's end, each once")]
fn child_transcript(w: &mut World) {
    let lines = w.transcript();
    let kinds: Vec<&str> = lines.iter().map(|l| l["kind"].as_str().unwrap()).collect();
    assert_eq!(
        kinds,
        [
            "entry",
            "assistant_delta",
            "assistant_delta",
            "assistant_delta",
            "tool",
            "turn_end"
        ],
        "each observation once, from the hooks alone: {lines:#?}"
    );
    assert_eq!(lines[0]["entry"]["type"], "user");
    assert_eq!(lines[0]["entry"]["content"], PROMPT);
    for (n, delta) in DELTAS.iter().enumerate() {
        assert_eq!(lines[1 + n]["delta"]["text"], *delta, "{n}");
    }
    let whole = serde_json::to_string(&lines).unwrap();
    assert!(
        !whole.contains(TOOL_BODY),
        "the tool's response body was kept"
    );
    assert!(
        !whole.contains(CHILD_REPLY),
        "the stream's reply is not a second copy of the words"
    );
    for (n, line) in lines.iter().enumerate() {
        assert_eq!(line["seq"], n, "{line}");
    }
}

#[then("the session's transcript holds no prompt and no words of the assistant's")]
fn no_conversation(w: &mut World) {
    let lines = w.transcript();
    let text = serde_json::to_string(&lines).unwrap();
    assert!(
        !text.contains("\"entry\"") && !text.contains("assistant_delta"),
        "{text}"
    );
    assert!(!text.contains(CHILD_REPLY), "{text}");
}

fn run_firing_hooks(w: &mut World, step: &cucumber::gherkin::Step) {
    let bin = w.engine_dir();
    let ctxloom = crate::tests::binary().display().to_string();
    files::put_program(bin.join("ctxloom"), &format!("exec '{ctxloom}' \"$@\"\n"));
    let mut payloads = vec![payload(USER_PROMPT_SUBMIT, |v| v["prompt"] = json!(PROMPT))];
    for (index, delta) in DELTAS.iter().enumerate() {
        payloads.push(payload(MESSAGE_DISPLAY, |v| {
            v["index"] = json!(index);
            v["final"] = json!(index == DELTAS.len() - 1);
            v["delta"] = json!(delta);
        }));
    }
    payloads.push(payload(POST_TOOL_BATCH, |v| {
        v["tool_calls"][0]["tool_response"] = json!(TOOL_BODY);
    }));
    payloads.push(payload(STOP, |v| {
        v["last_assistant_message"] = json!(DELTAS.concat());
    }));
    payloads.push(payload(SESSION_END, |_| {}));
    let events = [
        "UserPromptSubmit",
        "MessageDisplay",
        "MessageDisplay",
        "MessageDisplay",
        "PostToolBatch",
        "Stop",
        "SessionEnd",
    ];
    for (n, (text, event)) in payloads.iter().zip(events).enumerate() {
        files::put(bin.join(format!("payloads/{n:02}-{event}.json")), text);
    }
    files::put_program(bin.join("claude"), STUB);
    w.common.set_env("CTXLOOM_TEST_TERMINAL", "1".to_string());
    w.before_run();
    let lines = crate::tests::gherkin_common::doc(step);
    w.common_mut().run_lines(&lines, "");
}

#[then("the assistant's environment held what its hooks need to reach ctxloom")]
fn environment_reached(w: &mut World) {
    w.ran().succeeds();
    let dir = w.engine_dir();
    assert_eq!(
        files::read(dir.join("engine-missing")).unwrap_or_default(),
        "",
        "the engine's environment lacked variables"
    );
    assert!(
        files::exists(dir.join("engine-env")),
        "the engine never ran"
    );
}

#[then("every hook it fired answered, said nothing was left unrecorded, and exited 0")]
fn hooks_delivered(w: &mut World) {
    let fired = w.fired();
    let firings = match w.capture.firings {
        0 => 14,
        n => n,
    };
    assert_eq!(fired.len(), firings, "each hook twice: {fired:?}");
    for (event, cmd, out, err, code) in &fired {
        assert!(
            cmd.contains(event),
            "{event}: the registered command {cmd:?}"
        );
        assert_eq!(code, "0", "{event}: {err}");
        assert!(
            !err.contains("hook-cli-not-recorded"),
            "{event} was not recorded: {err} (answered {out})"
        );
    }
}

#[then("the session's transcript holds her prompt, the assistant's words in order, the tool call as a digest, the turn's end and the session's end, each once")]
fn transcript_holds(w: &mut World) {
    let lines = w.transcript();
    let kinds: Vec<&str> = lines.iter().map(|l| l["kind"].as_str().unwrap()).collect();
    assert_eq!(
        kinds,
        [
            "entry",
            "assistant_delta",
            "assistant_delta",
            "assistant_delta",
            "tool",
            "turn_end",
            "session_end"
        ],
        "each observation once, though every hook fired twice: {lines:#?}"
    );
    assert_eq!(lines[0]["entry"]["type"], "user");
    assert_eq!(lines[0]["entry"]["content"], PROMPT);
    for (n, delta) in DELTAS.iter().enumerate() {
        let line = &lines[1 + n]["delta"];
        assert_eq!(line["text"], *delta, "{n}");
        assert_eq!(line["index"], n);
        assert_eq!(line["final"], n == DELTAS.len() - 1);
        assert!(!line["message_id"].as_str().unwrap_or_default().is_empty());
    }
    let tool = &lines[4]["tool"];
    assert_eq!(tool["name"], "Bash");
    assert_eq!(tool["response_bytes"], TOOL_BODY.len());
    assert_eq!(tool["response_sha256"], TOOL_DIGEST);
    let turn_end = lines[5].as_object().unwrap();
    assert!(
        !turn_end.contains_key("text") && !turn_end.contains_key("delta"),
        "the turn's end carries no text: {turn_end:?}"
    );
    let whole = serde_json::to_string(&lines).unwrap();
    assert!(
        !whole.contains(TOOL_BODY),
        "the tool's response body was kept"
    );
    for (n, line) in lines.iter().enumerate() {
        assert_eq!(line["seq"], n, "{line}");
    }
    w.capture.ended = lines;
}

// Ruling 87a: the store is live (each hook's fact is an event when it is
// delivered), so another process reading the session while its engine runs
// sees what was delivered so far. The stub reads it after firing every hook
// and before it exits: no wait, the order is the stub's own.
#[then("while the engine ran, its session's transcript already held what its hooks had delivered")]
fn readable_while_running(w: &mut World) {
    let dir = w.engine_dir();
    let live = files::read(dir.join("live-transcript")).unwrap_or_default();
    let lines: Vec<Value> = live
        .lines()
        .filter(|l| !l.trim().is_empty())
        .map(|l| serde_json::from_str(l).unwrap_or_else(|e| panic!("{l:?}: {e}")))
        .collect();
    assert_eq!(
        lines,
        w.capture.ended,
        "read while the engine ran ({}), the transcript held every delivery",
        files::read(dir.join("live-transcript.err")).unwrap_or_default()
    );
}

#[then("a hook fired after the run has ended is refused without failing, and adds nothing to the transcript")]
fn late_hook(w: &mut World) {
    // The environment the engine had, the run's bearer included, as a hook
    // that outlived its engine would still have it.
    let engine_env = files::read(w.engine_dir().join("engine-env")).expect("the engine's env");
    let endpoint = engine_env
        .lines()
        .find_map(|l| l.strip_prefix("CTXLOOM_ENDPOINT="))
        .expect("the engine was told an endpoint");
    assert!(
        endpoint.starts_with("https://127.0.0.1:")
            && endpoint["https://127.0.0.1:".len()..]
                .parse::<u16>()
                .is_ok(),
        "the endpoint is the loopback https URL of the listener's port: {endpoint}"
    );
    // The engine was told the listener's certificate (public, one line of
    // base64 of a DER SEQUENCE) and nothing of its key.
    let cert = engine_env
        .lines()
        .find_map(|l| l.strip_prefix("CTXLOOM_TLS_CERT="))
        .expect("the engine was told the listener's certificate");
    assert!(
        cert.len() > 100 && cert.starts_with("MI") && !cert.contains(char::is_whitespace),
        "a base64 DER certificate: {cert}"
    );
    assert!(
        !engine_env.contains("PRIVATE KEY"),
        "no key in the environment"
    );
    let mut command = Command::new(crate::tests::binary());
    command
        .args(["hook", "claude-code", "execute", "Stop"])
        .env_clear()
        .env("TMPDIR", crate::tests::temp_root())
        .current_dir(w.common.project.dir())
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());
    for line in engine_env.lines() {
        let (name, value) = line.split_once('=').expect("NAME=value");
        command.env(name, value);
    }
    let mut child = command.spawn().expect("ctxloom runs");
    crate::tests::feed(child.stdin.take().expect("stdin"), STOP);
    let ran = Ran::of(child.wait_with_output().expect("ctxloom finishes"));
    assert_eq!(ran.code, 0, "{}", ran.said());
    assert_eq!(ran.stdout.trim(), "{}", "the engine is answered as usual");
    assert!(
        ran.stderr.contains("hook-cli-not-recorded"),
        "the hook says it recorded nothing: {}",
        ran.said()
    );
    assert_eq!(w.transcript(), w.capture.ended, "nothing was appended");
}

// ------------------------------------------- a live session (defects 1, 2)

/// The stub engine of a session another terminal watches: it runs that
/// terminal's commands itself, while it runs (so no wait is needed: the
/// order is the stub's own), and records what each said.
const WATCHED_STUB: &str = r#"here="$(dirname "$0")"
harp="$CTXLOOM_SESSION_HARP"
echo "$harp" > "$here/watched-harp"
ctxloom session list --format json > "$here/live-list" 2> "$here/live-list.err"
ctxloom session edit "$harp" --name renamed-while-live --format json > "$here/live-rename" 2>&1
echo "$?" > "$here/live-rename.code"
ctxloom session remove "$harp" --yes --format json > "$here/live-remove" 2>&1
echo "$?" > "$here/live-remove.code"
"#;

fn run_watched(w: &mut World, step: &cucumber::gherkin::Step) {
    let bin = w.engine_dir();
    let ctxloom = crate::tests::binary().display().to_string();
    files::put_program(bin.join("ctxloom"), &format!("exec '{ctxloom}' \"$@\"\n"));
    files::put_program(bin.join("claude"), WATCHED_STUB);
    w.common.set_env("CTXLOOM_TEST_TERMINAL", "1".to_string());
    w.before_run();
    let lines = crate::tests::gherkin_common::doc(step);
    w.common_mut().run_lines(&lines, "");
}

impl World {
    /// The watched session's name, as its engine was told it.
    fn watched_harp(&self) -> String {
        let harp = files::read(self.engine_dir().join("watched-harp")).expect("the engine ran");
        harp.trim().to_string()
    }

    fn watched(&self, name: &str) -> String {
        files::read(self.engine_dir().join(name)).unwrap_or_default()
    }

    /// `ctxloom <args>` in the project, now.
    fn run_now(&self, args: &[&str]) -> Ran {
        let env: Vec<(&str, &str)> = self
            .common
            .env
            .iter()
            .map(|(k, v)| (k.as_str(), v.as_str()))
            .collect();
        self.common.project.run_with(&env, args)
    }
}

/// The state a listing (JSON) gives the session `harp`, wherever in the
/// document its row is.
fn listed_state(listing: &str, harp: &str) -> Option<String> {
    fn find(value: &Value, harp: &str) -> Option<String> {
        match value {
            Value::Object(map) if map.get("harp").and_then(Value::as_str) == Some(harp) => {
                map.get("state").and_then(Value::as_str).map(str::to_string)
            }
            Value::Object(map) => map.values().find_map(|v| find(v, harp)),
            Value::Array(items) => items.iter().find_map(|v| find(v, harp)),
            _ => None,
        }
    }
    let value: Value = serde_json::from_str(listing).ok()?;
    find(&value, harp)
}

#[then("while it ran, the listing showed its session as live")]
fn listed_live(w: &mut World) {
    w.ran().succeeds();
    let harp = w.watched_harp();
    let listing = w.watched("live-list");
    assert_eq!(
        listed_state(&listing, &harp).as_deref(),
        Some("live"),
        "the run holds its lock (defect 1): {listing} {}",
        w.watched("live-list.err")
    );
}

#[then("while it ran, renaming its session was refused until the session ends")]
fn rename_refused_live(w: &mut World) {
    let said = w.watched("live-rename");
    assert_ne!(w.watched("live-rename.code").trim(), "0", "{said}");
    // PROPOSAL: the typed refusal `SessionLive` (its status is the proto's), code
    // `sessions-session-live`, its fix saying to wait for the session's end.
    assert!(said.contains("sessions-session-live"), "{said}");
    assert!(
        said.contains("\"fix\""),
        "the envelope carries the fix: {said}"
    );
    assert!(
        !files::exists(
            w.common
                .project
                .dir()
                .join(".ctxloom/sessions/renamed-while-live")
        ),
        "nothing was moved"
    );
}

#[then("while it ran, removing its session was refused because its owner is alive")]
fn remove_refused_live(w: &mut World) {
    let said = w.watched("live-remove");
    assert!(
        said.contains("the owner is alive"),
        "refused for its held lock, not for having none: {said}"
    );
}

#[then("after it ended, its session is listed as ended under its own name")]
fn listed_ended(w: &mut World) {
    let harp = w.watched_harp();
    let ran = w.run_now(&["session", "list", "--format", "json"]);
    assert_eq!(ran.code, 0, "{}", ran.said());
    assert_eq!(
        listed_state(&ran.stdout, &harp).as_deref(),
        Some("ended"),
        "{}",
        ran.stdout
    );
}

#[then("after it ended, renaming it moves its session's directory and lock with its name")]
fn rename_moves_directory(w: &mut World) {
    let harp = w.watched_harp();
    let sessions = w.common.project.dir().join(".ctxloom/sessions");
    let ran = w.run_now(&["session", "edit", &harp, "--name", "renamed-after-end"]);
    assert_eq!(ran.code, 0, "{}", ran.said());
    assert!(
        !files::exists(sessions.join(&harp)),
        "the old directory is gone (defect 2): a later session could claim it"
    );
    assert!(
        files::is_dir(sessions.join("renamed-after-end/home")),
        "the directory moved with the name"
    );
    let listed = w.run_now(&["session", "list", "--format", "json"]);
    assert_eq!(
        listed_state(&listed.stdout, "renamed-after-end").as_deref(),
        Some("ended"),
        "its lock moved with it, so it still reads ended: {}",
        listed.stdout
    );
}

// ------------------------------------------- a run asked to stop (ruling 114)

/// The stub child of a run that is stopped: it says its session and its pid
/// once its trap is set, then works until it is asked to stop, and says it
/// was asked.
const STOPPED_STUB: &str = r#"here="$(dirname "$0")"
trap 'echo asked > "$here/asked"; exit 0' TERM
echo "$CTXLOOM_SESSION_HARP" > "$here/watched-harp"
echo "$$" > "$here/pid.tmp" && mv "$here/pid.tmp" "$here/pid"
sleep 30 &
wait
"#;

/// The one-shot started, a SIGTERM sent to the ctxloom process (never to the
/// test's own) once the stub says it works, and the run waited for.
fn run_stopped(w: &mut World, step: &cucumber::gherkin::Step) {
    let bin = w.engine_dir();
    files::put_program(bin.join("claude"), STOPPED_STUB);
    let host = crate::tests::host_var("PATH").unwrap_or_default();
    w.common.set_env(
        "PATH",
        format!("{}:{}", bin.display(), host.to_string_lossy()),
    );
    w.before_run();
    let lines = crate::tests::gherkin_common::doc(step);
    let args = shell_words(
        lines
            .trim()
            .strip_prefix("ctxloom ")
            .expect("a ctxloom line"),
    );
    let args: Vec<&str> = args.iter().map(String::as_str).collect();
    let env: Vec<(&str, &str)> = w
        .common
        .env
        .iter()
        .map(|(k, v)| (k.as_str(), v.as_str()))
        .collect();
    let child = w.common.project.start_with(&env, &args);
    let deadline = std::time::Instant::now() + std::time::Duration::from_secs(60);
    while !files::exists(bin.join("pid")) {
        assert!(
            std::time::Instant::now() < deadline,
            "the stub never said it works"
        );
        std::thread::yield_now();
    }
    let sent = Command::new("kill")
        .args(["-TERM", &child.id().to_string()])
        .status()
        .expect("kill runs");
    assert!(sent.success(), "the SIGTERM was sent");
    w.common.ran = Some(Ran::of(child.wait_with_output().expect("ctxloom finishes")));
}

/// The words of a command line, as the scenario writes it: double quotes
/// group.
fn shell_words(line: &str) -> Vec<String> {
    let mut words = Vec::new();
    let mut quoted = false;
    let mut word = String::new();
    for c in line.chars() {
        match c {
            '"' => quoted = !quoted,
            ' ' if !quoted => words.extend((!word.is_empty()).then(|| std::mem::take(&mut word))),
            _ => word.push(c),
        }
    }
    words.extend((!word.is_empty()).then_some(word));
    words
}

#[then("the assistant was asked to stop, and was gone before ctxloom exited")]
fn assistant_stopped(w: &mut World) {
    let dir = w.engine_dir();
    assert!(
        files::exists(dir.join("asked")),
        "the assistant was not asked to stop: {}",
        w.ran().said()
    );
    let pid = w.watched("pid");
    let alive = Command::new("kill")
        .args(["-0", pid.trim()])
        .stderr(Stdio::null())
        .status()
        .expect("kill runs");
    assert!(!alive.success(), "the assistant ({pid}) outlived ctxloom");
}

#[then("ctxloom exited 143, saying the run was stopped by SIGTERM")]
fn exited_as_signalled(w: &mut World) {
    let ran = w.ran();
    assert_eq!(ran.code, 143, "{}", ran.said());
    assert!(ran.stderr.contains("stopped by SIGTERM"), "{}", ran.said());
}
