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
[ -z "${HOME:-}" ] || echo "HOME" >> "$here/engine-had"
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
[ ! -e "$CLAUDE_CONFIG_DIR/../transcripts/transcript.jsonl" ] || echo present > "$here/engine-saw-transcript"
"#;

/// What the scenario keeps.
#[derive(Default)]
pub(super) struct State {
    /// The transcript's lines as they stood when the run ended.
    ended: Vec<Value>,
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

    fn transcript(&self) -> Vec<Value> {
        let path = self
            .common
            .project
            .dir()
            .join(".ctxloom/sessions")
            .join(self.harp())
            .join("transcripts/transcript.jsonl");
        files::read(&path)
            .unwrap_or_else(|e| panic!("no transcript at {}: {e}", path.display()))
            .lines()
            .map(|l| serde_json::from_str(l).unwrap_or_else(|e| panic!("{l:?}: {e}")))
            .collect()
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

fn payload(template: &str, edit: impl FnOnce(&mut Value)) -> String {
    let mut value: Value = serde_json::from_str(template).expect("a fixture");
    edit(&mut value);
    value.to_string()
}

/// The `Alice ...:` step that runs the scenario (the generic `Alice ...:`
/// step is the World's `alice_runs`, which hands it here): whether it is
/// that step.
pub(super) fn fires(w: &mut World, step: &cucumber::gherkin::Step) -> bool {
    if !step.value.contains("fires its session's hooks") {
        return false;
    }
    run_firing_hooks(w, step);
    true
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
    assert_eq!(fired.len(), 14, "seven hooks, twice each: {fired:?}");
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

#[then("the transcript was written when the run ended, not while the engine ran")]
fn written_at_the_end(w: &mut World) {
    assert!(
        !files::exists(w.engine_dir().join("engine-saw-transcript")),
        "the transcript existed while the engine ran: the projector runs at the run's end"
    );
    assert!(!w.capture.ended.is_empty());
}

#[then("a hook fired after the run has ended is refused without failing, and adds nothing to the transcript")]
fn late_hook(w: &mut World) {
    // The environment the engine had, the run's bearer included, as a hook
    // that outlived its engine would still have it.
    let engine_env = files::read(w.engine_dir().join("engine-env")).expect("the engine's env");
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
