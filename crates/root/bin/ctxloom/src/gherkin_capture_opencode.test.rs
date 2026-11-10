//! The steps of j001000's opencode scenario: capture through opencode's
//! plugin, end to end, through the real binary (ruling 58).
//!
//! The assistant is a stub `opencode` on `PATH` (an interactive run, the
//! terminal stood in by `CTXLOOM_TEST_TERMINAL`). It does what opencode does
//! with a session home: it loads the plugin `$OPENCODE_CONFIG_DIR/plugins/
//! ctxloom.js` (under node; opencode runs it under Bun) and hands it the bus
//! events and the `chat.message` call a real opencode makes, in the shapes the
//! live probe saw (engine-opencode's fixtures), then disposes it. The plugin
//! runs `ctxloom hook opencode execute <Event>` for each event that records,
//! with the environment the stub was given, so the hooks reach the run's real
//! listener. Needs node on the host's `PATH`.

use std::path::PathBuf;

use serde_json::{json, Value};

use super::World;
use crate::tests::files;
use crate::tests::gherkin_common::CommonWorld;

const CHAT_MESSAGE: &str =
    include_str!("../../../../adapters/driven/engine-opencode/src/fixtures/chat_message.json");
const ASSISTANT_OPEN: &str =
    include_str!("../../../../adapters/driven/engine-opencode/src/fixtures/assistant_open.json");
const ASSISTANT_COMPLETED: &str = include_str!(
    "../../../../adapters/driven/engine-opencode/src/fixtures/assistant_completed.json"
);
const TOOL_STEP_COMPLETED: &str = include_str!(
    "../../../../adapters/driven/engine-opencode/src/fixtures/tool_steps_completed.json"
);
const TOOL_COMPLETED: &str =
    include_str!("../../../../adapters/driven/engine-opencode/src/fixtures/tool_completed.json");
const TEXT_PART_END: &str =
    include_str!("../../../../adapters/driven/engine-opencode/src/fixtures/text_part_end.json");
const REASONING_PART_END: &str = include_str!(
    "../../../../adapters/driven/engine-opencode/src/fixtures/reasoning_part_end.json"
);
const SESSION_IDLE: &str =
    include_str!("../../../../adapters/driven/engine-opencode/src/fixtures/session_idle.json");

/// The prompt's user message, `chat.message`'s `output.message.id`.
const PROMPT_ID: &str = "msg_1c39b7ca0001Vu5HlXKcT90BCg";
/// The tool step's assistant message.
const TOOL_STEP: &str = "msg_1c39f7419001Rd3mT0pQk8SnLy";
/// The tool part's id: the call's id.
const TOOL_PART: &str = "prt_1c39f745b001c5IdBJ9I7M5n07";
/// The reply's text part's id: the words' message id.
const TEXT_PART: &str = "prt_1c39b7d0e001Ls1aH8dEy6GbZm";
/// The assistant's words.
const WORDS: &str = "The command `echo hi` ran and printed `hi`.";
/// The tool's output: a body no record may keep.
const TOOL_BODY: &str = "TOOL-RESPONSE-BODY-THAT-MUST-NOT-BE-KEPT";
/// The digest of [`TOOL_BODY`] (`sha256sum`).
const TOOL_DIGEST: &str = "71ca0ef886ae43a96de885a15d913cc2cbe0553f553638a28601890879ed97e3";

/// The stub opencode: records its environment, then runs the driver over the
/// plugin its session home holds.
const STUB: &str = r#"here="$(dirname "$0")"
for v in CTXLOOM_ENDPOINT CTXLOOM_BEARER CTXLOOM_RUN CTXLOOM_HOME CTXLOOM_TLS_CERT CTXLOOM_SESSION_HARP OPENCODE_CONFIG_DIR; do
  eval "[ -n \"\${$v:-}\" ]" || echo "$v" >> "$here/opencode-missing"
done
env | grep -E '^(CTXLOOM_|OPENCODE_|XDG_|HOME=)' > "$here/opencode-env"
node "$here/driver.mjs" "$OPENCODE_CONFIG_DIR/plugins/ctxloom.js" "$here/steps.json" > "$here/driver-out" 2>&1
echo "$?" > "$here/driver-code"
"#;

/// What opencode does with the plugin: one instance, each step handed to it
/// in order, then `dispose`, which `run` and the TUI await before they exit.
const DRIVER: &str = r#"import { readFileSync } from "node:fs"
import { pathToFileURL } from "node:url"
const [plugin, steps] = process.argv.slice(2)
const { CtxloomPlugin } = await import(pathToFileURL(plugin).href)
const hooks = await CtxloomPlugin({})
for (const step of JSON.parse(readFileSync(steps, "utf8"))) {
  if (step.prompt) await hooks["chat.message"](step.prompt.input, step.prompt.output)
  else await hooks.event({ event: step.event })
}
await hooks.dispose()
"#;

impl World {
    fn opencode_dir(&self) -> PathBuf {
        self.adopt_scratch().join("bin")
    }

    fn opencode_harp(&self) -> String {
        let sessions = self.common.project.dir().join(".ctxloom/sessions");
        let names: Vec<String> = files::children(&sessions)
            .iter()
            .filter(|p| files::is_dir(p))
            .filter_map(|p| p.file_name().map(|n| n.to_string_lossy().into_owned()))
            .collect();
        assert_eq!(names.len(), 1, "one session was started: {names:?}");
        names[0].clone()
    }

    fn opencode_session_dir(&self) -> PathBuf {
        self.common
            .project
            .dir()
            .join(".ctxloom/sessions")
            .join(self.opencode_harp())
    }
}

/// A fixture as opencode hands it to the plugin: the bus event alone, without
/// what the plugin adds, `edit`ed.
fn event(fixture: &str, edit: impl FnOnce(&mut Value)) -> Value {
    let mut value: Value = serde_json::from_str(fixture).expect("a fixture");
    if let Some(object) = value.as_object_mut() {
        object.remove("ctxloom");
    }
    edit(&mut value);
    json!({ "event": value })
}

/// The session's events, in the order opencode emits them: the prompt; a
/// step that calls a tool; a reasoning part (never forwarded); the reply's
/// step, its text part, its completion; then the idle the turn ends with.
fn steps() -> Value {
    let prompt: Value = serde_json::from_str(CHAT_MESSAGE).expect("a fixture");
    let props = &prompt["properties"];
    json!([
        {"prompt": {
            "input": {"sessionID": props["sessionID"]},
            "output": {"message": props["message"], "parts": props["parts"]},
        }},
        event(ASSISTANT_OPEN, |v| {
            v["properties"]["info"]["id"] = json!(TOOL_STEP);
            v["properties"]["info"]["finish"] = json!("tool-calls");
        }),
        event(TOOL_COMPLETED, |v| {
            v["properties"]["part"]["state"]["output"] = json!(TOOL_BODY);
        }),
        event(TOOL_STEP_COMPLETED, |v| {
            v["properties"]["info"]["parentID"] = json!(PROMPT_ID);
        }),
        event(ASSISTANT_OPEN, |_| {}),
        event(REASONING_PART_END, |_| {}),
        event(TEXT_PART_END, |v| {
            v["properties"]["part"]["text"] = json!(WORDS);
        }),
        event(ASSISTANT_COMPLETED, |_| {}),
        event(SESSION_IDLE, |_| {}),
    ])
}

/// The `Alice ...:` step that runs the scenario (the generic `Alice ...:` step
/// is the World's `alice_runs`, which hands it here): whether it is that step.
pub(super) fn forwards(w: &mut World, step: &cucumber::gherkin::Step) -> bool {
    if !step
        .value
        .contains("its plugin forwards the session's events")
    {
        return false;
    }
    let bin = w.opencode_dir();
    files::put(bin.join("driver.mjs"), DRIVER);
    files::put(bin.join("steps.json"), steps().to_string());
    files::put_program(bin.join("opencode"), STUB);
    let host = crate::tests::host_var("PATH").unwrap_or_default();
    w.common.set_env(
        "PATH",
        format!("{}:{}", bin.display(), host.to_string_lossy()),
    );
    w.common.set_env("CTXLOOM_TEST_TERMINAL", "1".to_string());
    // Set where Alice runs ctxloom: it would make opencode skip the plugin.
    w.common.set_env("OPENCODE_PURE", "1".to_string());
    w.before_run();
    let lines = crate::tests::gherkin_common::doc(step);
    w.common_mut().run_lines(&lines, "");
    true
}

/// The stub opencode of ruling 69's scenario: says what its data directory
/// holds where opencode reads its login (`$XDG_DATA_HOME/opencode/auth.json`):
/// a link and where it points, a file of its own, or nothing.
const LOGIN_STUB: &str = r#"here="$(dirname "$0")"
f="$XDG_DATA_HOME/opencode/auth.json"
if [ -L "$f" ]; then echo "link $(readlink "$f")" > "$here/opencode-login"
elif [ -e "$f" ]; then echo "file" > "$here/opencode-login"
else echo "none" > "$here/opencode-login"
fi
"#;

/// The `Alice ...:` step of ruling 69's scenario: an interactive opencode
/// that reports its login, then exits.
pub(super) fn reports_login(w: &mut World, step: &cucumber::gherkin::Step) -> bool {
    if !step.value.contains("and it reports its login") {
        return false;
    }
    let bin = w.opencode_dir();
    files::put_program(bin.join("opencode"), LOGIN_STUB);
    let host = crate::tests::host_var("PATH").unwrap_or_default();
    w.common.set_env(
        "PATH",
        format!("{}:{}", bin.display(), host.to_string_lossy()),
    );
    w.common.set_env("CTXLOOM_TEST_TERMINAL", "1".to_string());
    w.before_run();
    let lines = crate::tests::gherkin_common::doc(step);
    w.common_mut().run_lines(&lines, "");
    true
}

#[cucumber::then("opencode's data directory held a link to her opencode login, not a copy")]
fn opencode_login_linked(w: &mut World) {
    let login = w
        .common
        .project
        .home()
        .join(".local/share/opencode/auth.json");
    assert_eq!(
        files::read(w.opencode_dir().join("opencode-login"))
            .expect("opencode ran")
            .trim(),
        format!("link {}", login.display()),
        "{}",
        w.ran().said()
    );
}

#[cucumber::then(
    "opencode ran in its session home, with what its plugin's hooks need to reach ctxloom and without OPENCODE_PURE"
)]
fn opencode_environment(w: &mut World) {
    w.ran().succeeds();
    let dir = w.opencode_dir();
    assert_eq!(
        files::read(dir.join("opencode-missing")).unwrap_or_default(),
        "",
        "opencode's environment lacked variables"
    );
    let env = files::read(dir.join("opencode-env")).expect("opencode ran");
    let var = |name: &str| {
        env.lines()
            .find_map(|l| l.strip_prefix(&format!("{name}=")))
            .map(str::to_string)
    };
    let home = w.opencode_session_dir().join("home");
    let home = home.to_string_lossy();
    assert_eq!(var("OPENCODE_CONFIG_DIR").as_deref(), Some(&*home));
    assert_eq!(var("HOME").as_deref(), Some(&*home));
    assert_eq!(
        var("XDG_CONFIG_HOME"),
        Some(format!("{home}/xdg/config")),
        "{env}"
    );
    assert_eq!(var("OPENCODE_PURE"), None, "{env}");
    for file in [
        "plugins/ctxloom.js",
        "AGENTS.md",
        "package-lock.json",
        "xdg/config/opencode/package-lock.json",
    ] {
        assert!(
            files::exists(w.opencode_session_dir().join("home").join(file)),
            "{file} in the session home"
        );
    }
}

#[cucumber::then("every hook the plugin ran left nothing unrecorded")]
fn hooks_recorded(w: &mut World) {
    let dir = w.opencode_dir();
    assert_eq!(
        files::read(dir.join("driver-code"))
            .unwrap_or_default()
            .trim(),
        "0",
        "{}",
        files::read(dir.join("driver-out")).unwrap_or_default()
    );
    let log = w.opencode_session_dir().join("home/ctxloom-hooks.log");
    let said = files::read(&log).expect("the plugin ran its hooks, which write here");
    assert!(
        !said.contains("hook-cli-not-recorded") && !said.contains("ctxloom:"),
        "a hook said: {said}"
    );
}

#[cucumber::then(
    "the session's transcript holds her prompt, the tool call with its status, the assistant's words, the turn's end and the session's end at the exit, each once"
)]
fn transcript_holds(w: &mut World) {
    let path = w
        .opencode_session_dir()
        .join("transcripts/transcript.jsonl");
    let lines: Vec<Value> = files::read(&path)
        .unwrap_or_else(|e| panic!("no transcript at {}: {e}", path.display()))
        .lines()
        .map(|l| serde_json::from_str(l).unwrap_or_else(|e| panic!("{l:?}: {e}")))
        .collect();
    let kinds: Vec<&str> = lines.iter().map(|l| l["kind"].as_str().unwrap()).collect();
    assert_eq!(
        kinds,
        [
            "entry",
            "tool",
            "assistant_delta",
            "turn_end",
            "session_end"
        ],
        "each once, the turn ended twice (its completion and its idle): {lines:#?}"
    );
    for line in &lines {
        assert_eq!(line["engine"], "opencode", "{line}");
    }
    assert_eq!(lines[0]["entry"]["type"], "user");
    assert_eq!(lines[0]["entry"]["content"], "Reply with exactly: ok");
    let tool = &lines[1]["tool"];
    assert_eq!(lines[1]["prompt_id"], PROMPT_ID);
    assert_eq!(tool["id"], TOOL_PART);
    assert_eq!(tool["name"], "bash");
    assert_eq!(tool["status"], "completed");
    assert_eq!(tool["input"], r#"{"command":"echo hi"}"#);
    assert_eq!(tool["response_bytes"], TOOL_BODY.len());
    assert_eq!(tool["response_sha256"], TOOL_DIGEST);
    let words = &lines[2];
    assert_eq!(words["prompt_id"], PROMPT_ID);
    assert_eq!(words["delta"]["message_id"], TEXT_PART);
    assert_eq!(words["delta"]["text"], WORDS);
    assert_eq!(words["delta"]["final"], true);
    assert_eq!(lines[3]["prompt_id"], PROMPT_ID);
    assert_eq!(lines[4]["reason"], "exit");
    let whole = serde_json::to_string(&lines).unwrap();
    assert!(!whole.contains(TOOL_BODY), "the tool's output was kept");
    assert!(
        !whole.contains("The user wants ok."),
        "the reasoning was kept"
    );
}
