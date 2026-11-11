//! The session home (ruling 58 and the live probe): the context, the plugin,
//! and the npm pre-seed in every config directory opencode reads; and the
//! plugin itself, run under node with a fake `ctxloom` on its PATH.

use std::path::{Path, PathBuf};
use std::process::Command;

use file_system::{Fs, Perms, Rooted};
use os::Mode;
use run_ports::{Engine, PrepareRequest};
use surface_ports::{HookCodec, Json};

use crate::{Codec, Opencode, CONFIG_FILE, CONTEXT_FILE, PLUGIN, PLUGIN_FILE, PRESEED_LOCK};

const HOME: &str = "/p/.ctxloom/sessions/h/home";

fn prepared(interactive: bool) -> run_ports::Prepared {
    prepared_with(interactive, None).unwrap()
}

fn prepared_with(
    interactive: bool,
    permissions: Option<&str>,
) -> Result<run_ports::Prepared, run_ports::EngineError> {
    Opencode::new().prepare(&PrepareRequest {
        home: HOME.to_string(),
        context: "the project's context".to_string(),
        offered: vec!["auth.json".to_string(), "opencode.json".to_string()],
        interactive,
        cwd: None,
        existing: Default::default(),
        permissions: permissions.map(String::from),
    })
}

/// The session home's `opencode.json`, as written.
fn config_of(prepared: &run_ports::Prepared) -> String {
    let file = prepared
        .files
        .iter()
        .find(|f| f.rel == CONFIG_FILE)
        .expect("opencode.json in the home");
    String::from_utf8(file.bytes.clone()).unwrap()
}

// Owner rulings 135, 137: the agent's permissions document is opencode's
// config `permission` object, exactly as handed: its words (unchecked) and its
// order (opencode's last matching rule wins), for a child's turn and the
// session alike; the document is that one key.
#[test]
fn test_the_permissions_document_is_the_configs_permission_object_as_handed() {
    const DOCUMENT: &str = r#"{"edit":"ask","bash":{"*":"ask","git status *":"allow","git push *":"deny"},"webfetch":"deny","mode":"plan"}"#;
    for interactive in [false, true] {
        let text = config_of(&prepared_with(interactive, Some(DOCUMENT)).unwrap());
        let doc = Json::parse(&text).unwrap();
        assert_eq!(
            doc,
            Json::object([("permission", Json::parse(DOCUMENT).unwrap())]),
            "{text}"
        );
        let Some(Json::Object(rules)) = doc.get("permission").and_then(|p| p.get("bash")) else {
            panic!("{text}");
        };
        let order: Vec<&str> = rules.iter().map(|(k, _)| k.as_str()).collect();
        assert_eq!(order, ["*", "git status *", "git push *"]);
    }
}

// Owner T21: opencode.json is written whole at every launch from what the
// launch is handed, never merged into what the home holds, so an agent's edit
// of its own config does not survive into its next launch.
#[test]
fn test_the_config_is_regenerated_whatever_the_home_holds() {
    assert!(Opencode::new().merges().is_empty());
    let prepared = Opencode::new()
        .prepare(&PrepareRequest {
            home: HOME.to_string(),
            context: "c".to_string(),
            offered: Vec::new(),
            interactive: true,
            cwd: None,
            existing: std::collections::BTreeMap::from([(
                CONFIG_FILE.to_string(),
                br#"{"permission":"allow"}"#.to_vec(),
            )]),
            permissions: Some(r#"{"edit":"ask"}"#.to_string()),
        })
        .unwrap();
    assert_eq!(
        Json::parse(&config_of(&prepared)).unwrap(),
        Json::object([("permission", Json::object([("edit", Json::text("ask"))]))])
    );
}

// No document: the config is empty, so nothing of an earlier run's lingers.
#[test]
fn test_with_no_document_the_config_is_empty() {
    assert_eq!(config_of(&prepared(false)), "{}\n");
}

// A document that is not JSON is refused before anything is written.
#[test]
fn test_a_document_that_is_not_json_is_refused() {
    let refused = prepared_with(false, Some("[nope"));
    assert!(
        matches!(&refused, Err(run_ports::EngineError::BadPermissions { engine, .. }) if engine == "opencode"),
        "{refused:?}"
    );
}

#[test]
fn test_a_session_home_holds_the_context_the_plugin_and_the_preseed_and_nothing_of_the_users() {
    for interactive in [false, true] {
        let home = prepared(interactive);
        let mut files: Vec<(&str, &[u8])> = home
            .files
            .iter()
            .map(|f| (f.rel.as_str(), f.bytes.as_slice()))
            .collect();
        files.sort();
        assert_eq!(
            files,
            [
                ("AGENTS.md", b"the project's context".as_slice()),
                ("node_modules/.ctxloom", b"".as_slice()),
                ("opencode.json", b"{}\n".as_slice()),
                ("package-lock.json", PRESEED_LOCK.as_bytes()),
                ("plugins/ctxloom.js", PLUGIN.as_bytes()),
                ("xdg/config/opencode/node_modules/.ctxloom", b"".as_slice()),
                (
                    "xdg/config/opencode/package-lock.json",
                    PRESEED_LOCK.as_bytes()
                ),
            ],
            "interactive: {interactive}"
        );
        assert_eq!(home.context_path, format!("{HOME}/{CONTEXT_FILE}"));
        assert!(home.copy_in.is_empty(), "nothing of the user's crosses");
    }
}

// The probe: opencode installs `@opencode-ai/plugin` into a config directory
// unless it has a node_modules/ and a lock naming the package (the version is
// not compared).
#[test]
fn test_the_preseed_lock_names_the_plugin_package_as_installed() {
    let lock = Json::parse(PRESEED_LOCK).unwrap();
    let deps = lock
        .get("packages")
        .and_then(|p| p.get(""))
        .and_then(|p| p.get("dependencies"))
        .expect("packages[\"\"].dependencies");
    assert!(deps.get("@opencode-ai/plugin").is_some(), "{lock:?}");
    assert_eq!(
        lock.get("lockfileVersion").map(Json::pretty).as_deref(),
        Some("3")
    );
}

#[test]
fn test_the_plugin_imports_node_builtins_alone_and_runs_the_hook_verb() {
    let imports: Vec<&str> = PLUGIN
        .lines()
        .filter(|l| l.starts_with("import "))
        .collect();
    assert!(!imports.is_empty());
    for line in imports {
        assert!(line.contains("from \"node:"), "{line}");
    }
    assert!(PLUGIN.contains(r#"const COMMAND = "ctxloom""#));
    assert!(PLUGIN.contains(r#"const ARGS = ["hook", "opencode", "execute"]"#));
    assert!(PLUGIN_FILE.starts_with("plugins/") && PLUGIN_FILE.ends_with(".js"));
}

// --------------------------------------------------------------- the plugin

/// The fake `ctxloom`: each delivery a numbered file holding its arguments,
/// whether it had a HOME, and its stdin; slow, so a delivery the plugin did
/// not wait for would still be a `.tmp`.
const FAKE_CTXLOOM: &str = r#"#!/bin/sh
d="$(dirname "$0")/../got"
mkdir -p "$d"
n=$(ls "$d" | wc -l | tr -d ' ')
f="$d/$(printf %03d "$n")"
{ echo "$*"; echo "HOME=${HOME:-}"; cat; } > "$f.tmp"
sleep 0.2
mv "$f.tmp" "$f"
"#;

/// The driver: loads the plugin TWICE (two evaluations of the module, as
/// opencode may), feeds each step to the instance it names, disposes the
/// first, and prints what had been delivered by then.
const DRIVER: &str = r#"import { readFileSync, readdirSync } from "node:fs"
import { pathToFileURL } from "node:url"
const [plugin, steps, got] = process.argv.slice(2)
process.env.HOME = "/the/session/home"
const first = await import(pathToFileURL(plugin).href)
const second = await import(pathToFileURL(plugin).href + "?again")
const instances = [await first.CtxloomPlugin({}), await second.CtxloomPlugin({})]
for (const step of JSON.parse(readFileSync(steps, "utf8"))) {
  const at = instances[step.instance || 0]
  if (step.prompt) await at["chat.message"](step.prompt.input, step.prompt.output)
  else await at.event({ event: step.event })
}
await instances[0].dispose()
console.log(readdirSync(got).sort().join(" "))
"#;

const S: &str = "ses_e3c648397ffesrvA00TD1hAwdC";
const U: &str = "msg_1c39b7ca0001Vu5HlXKcT90BCg";

fn fixture(name: &str) -> String {
    let text = match name {
        "chat_message" => include_str!("fixtures/chat_message.json"),
        "assistant_open" => include_str!("fixtures/assistant_open.json"),
        "assistant_completed" => include_str!("fixtures/assistant_completed.json"),
        "text_part_open" => include_str!("fixtures/text_part_open.json"),
        "text_part_end" => include_str!("fixtures/text_part_end.json"),
        "reasoning_part_end" => include_str!("fixtures/reasoning_part_end.json"),
        "tool_running" => include_str!("fixtures/tool_running.json"),
        "tool_completed" => include_str!("fixtures/tool_completed.json"),
        "session_idle" => include_str!("fixtures/session_idle.json"),
        "compaction_part" => include_str!("fixtures/compaction_part.json"),
        "summary_completed" => include_str!("fixtures/summary_completed.json"),
        other => panic!("no fixture {other}"),
    };
    text.trim().to_string()
}

/// The event a fixture holds, as opencode hands it to the plugin: without
/// what the plugin adds.
fn event(json: &str) -> String {
    let mut doc = Json::parse(json).unwrap();
    doc.remove("ctxloom");
    format!(r#"{{"event":{}}}"#, doc.pretty())
}

fn steps() -> String {
    let summary_open = fixture("summary_completed").replace(r#","completed":1794283498100"#, "");
    let summary_text = fixture("text_part_end")
        .replace(S, "ses_e3c5eb4e1ffeQ9uRk2Wm7Tx4Lb")
        .replace(
            "msg_1c39b7cc8001hR2pQx7Lm4Nb9V",
            "msg_1c3a14b25001Cz5iP2lMg0OjHu",
        )
        .replace(
            "prt_1c39b7d0e001Ls1aH8dEy6GbZm",
            "prt_1c3a14c00001SummaryText00",
        )
        .replace(r#""text":"ok""#, r#""text":"The summary.""#);
    let prompt = Json::parse(&fixture("chat_message")).unwrap();
    let props = prompt.get("properties").unwrap();
    let steps = [
        format!(r#"{{"event":{{"id":"evt_1","type":"session.created","properties":{{"info":{{"id":"{S}"}}}}}}}}"#),
        r#"{"event":{"id":"evt_2","type":"session.created","properties":{"info":{"id":"ses_child","parentID":"ses_e3c648397ffesrvA00TD1hAwdC"}}}}"#.to_string(),
        format!(
            r#"{{"prompt":{{"input":{{"sessionID":"{S}"}},"output":{{"message":{},"parts":{}}}}}}}"#,
            props.get("message").unwrap().pretty(),
            props.get("parts").unwrap().pretty()
        ),
        // the second instance learns the step message's prompt; the first uses it
        event(&fixture("assistant_open")).replacen('{', r#"{"instance":1,"#, 1),
        event(&fixture("text_part_open")),
        event(&fixture("reasoning_part_end")),
        event(&fixture("text_part_end")),
        event(&fixture("text_part_end")).replacen('{', r#"{"instance":1,"#, 1),
        event(&fixture("text_part_end").replace(S, "ses_child")),
        event(&fixture("assistant_completed")),
        event(&fixture("assistant_completed")),
        event(&fixture("session_idle")),
        event(&fixture("tool_running")),
        event(&fixture("tool_completed")),
        event(&fixture("compaction_part")),
        event(&summary_open),
        event(&summary_text),
        event(&fixture("summary_completed")),
    ];
    format!("[{}]", steps.join(","))
}

fn put(dir: &Rooted, path: &str, text: &str) -> PathBuf {
    let path = os::fixture(path);
    dir.create_dir_all(path.parent().unwrap(), Mode::DIR)
        .unwrap();
    dir.write_atomic(&path, text.as_bytes(), Perms::Exact(Mode::FILE))
        .unwrap();
    dir.real_path(&path).unwrap()
}

fn delivered(dir: &Rooted, n: usize) -> (String, String, String) {
    let text = dir
        .read_to_string(&os::fixture(&format!("/got/{n:03}")))
        .unwrap_or_else(|e| panic!("delivery {n}: {e}"));
    let mut lines = text.splitn(3, '\n');
    (
        lines.next().unwrap_or_default().to_string(),
        lines.next().unwrap_or_default().to_string(),
        lines.next().unwrap_or_default().to_string(),
    )
}

// Needs node on PATH (opencode runs its plugins under Bun; the plugin uses
// node's builtins alone, which both have).
#[test]
fn test_the_plugin_forwards_each_event_that_records_once_in_order_and_drains_on_dispose() {
    let dir = Rooted::temp().unwrap();
    let plugin = put(&dir, "/home/plugins/ctxloom.js", PLUGIN);
    let steps = put(&dir, "/steps.json", &steps());
    let driver = put(&dir, "/driver.mjs", DRIVER);
    dir.create_dir_all(&os::fixture("/bin"), Mode::DIR).unwrap();
    let fake =
        fixture_program::install_program(&dir, &os::fixture("/bin/ctxloom"), FAKE_CTXLOOM).unwrap();
    dir.create_dir_all(&os::fixture("/got"), Mode::DIR).unwrap();
    let got = dir.real_path(&os::fixture("/got")).unwrap();
    let bin = fake.parent().unwrap();
    let out = Command::new("sh")
        .arg("-c")
        .arg(r#"PATH="$FAKE_BIN:$PATH" exec node "$@""#)
        .arg("sh")
        .args([&driver, &plugin, &steps, &got])
        .env("FAKE_BIN", bin)
        .env_remove("OPENCODE_CONFIG_DIR")
        .output()
        .expect("sh runs");
    let stderr = String::from_utf8_lossy(&out.stderr);
    assert!(
        out.status.success(),
        "node ran the plugin (is node on PATH?): {stderr}"
    );
    let natives = [
        "chat.message",
        "message.part.updated",
        "message.updated",
        "session.idle",
        "message.part.updated",
        "message.part.updated",
        "message.updated",
    ];
    let names: Vec<String> = (0..natives.len()).map(|n| format!("{n:03}")).collect();
    assert_eq!(
        String::from_utf8_lossy(&out.stdout).trim(),
        names.join(" "),
        "every delivery done, each once, before dispose returned"
    );
    let codec = Codec;
    let mut events = Vec::new();
    for (n, native) in natives.iter().enumerate() {
        let (args, home, payload) = delivered(&dir, n);
        assert_eq!(args, format!("hook opencode execute {native}"), "{n}");
        assert_eq!(home, "HOME=", "{n}: a hook runs without opencode's HOME");
        let event = codec
            .decode(&codec.unified_event(native), &payload)
            .unwrap_or_else(|e| panic!("{n}: {e:?}: {payload}"));
        events.push((event.event.clone(), event.prompt_id.clone()));
        if n == 6 {
            assert_eq!(event.trigger, "manual");
            assert_eq!(event.compact_summary, "The summary.");
        }
    }
    let compaction = "msg_1c3a14b1f001Bx3gN0jKe8MhFs";
    assert_eq!(
        events,
        [
            ("turn_start".to_string(), U.to_string()),
            ("message_display".to_string(), U.to_string()),
            ("turn_end".to_string(), U.to_string()),
            ("turn_end".to_string(), U.to_string()),
            ("post_tool_batch".to_string(), U.to_string()),
            ("compaction_before".to_string(), compaction.to_string()),
            ("compaction_after".to_string(), compaction.to_string()),
        ],
        "the prompt of a part is the plugin's: its step message's parent, else the session's last prompt"
    );
    let _ = Path::new(HOME);
}
