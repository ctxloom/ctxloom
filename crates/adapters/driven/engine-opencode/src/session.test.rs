//! j000400: a session's profiles reach opencode's session home, never the
//! project: commands in its config dir's `commands/`, servers under `mcp` of
//! its `opencode.json`, and the session's start hooks run by a plugin of its
//! own, run here under node.

use std::collections::BTreeMap;
use std::process::Command;

use file_system::{Fs, Perms, Rooted};
use os::Mode;
use run_ports::{
    Engine, PrepareRequest, Prepared, SessionCommand, SessionHook, SessionItems, SessionMcp,
};
use surface_ports::Json;

use super::{session_hooks, SESSION_HOOKS, SESSION_HOOKS_FILE};
use crate::{Opencode, CONFIG_FILE};

/// Whether this host runs node with `sh` behind it as the tests do: unix.
/// Elsewhere the asking test is skipped and says so, never silently.
fn runs_scripts() -> bool {
    if os::host().flavor() == os::Flavor::Unix {
        return true;
    }
    let test = std::thread::current();
    eprintln!(
        "SKIP {}: it runs a shell's command line under node (unix only)",
        test.name().unwrap_or("a test")
    );
    false
}

fn hook(event: &str, command: &str, args: &[&str]) -> SessionHook {
    SessionHook {
        event: event.to_string(),
        command: command.to_string(),
        args: args.iter().map(ToString::to_string).collect(),
        ..SessionHook::default()
    }
}

fn items() -> SessionItems {
    SessionItems {
        commands: vec![
            SessionCommand {
                name: "team/onboarding".to_string(),
                description: "the team's \"onboarding\"".to_string(),
                body: "COMMAND-BODY".to_string(),
            },
            SessionCommand {
                name: "../escape".to_string(),
                description: String::new(),
                body: "NO".to_string(),
            },
        ],
        mcp: vec![
            SessionMcp {
                name: "toolserver".to_string(),
                command: "tool-cmd".to_string(),
                args: vec!["--flag".to_string()],
                env: BTreeMap::from([("K".to_string(), "V".to_string())]),
                ..SessionMcp::default()
            },
            SessionMcp {
                name: "remote".to_string(),
                url: "https://mcp.example/".to_string(),
                headers: BTreeMap::from([("X".to_string(), "Y".to_string())]),
                ..SessionMcp::default()
            },
        ],
        hooks: vec![
            hook("session_start", "echo START", &[]),
            hook("session_end", "echo END", &[]),
            SessionHook {
                kind: "prompt".to_string(),
                prompt: "say hi".to_string(),
                ..hook("session_start", "", &[])
            },
        ],
    }
}

fn prepare(items: SessionItems) -> Prepared {
    Opencode::new()
        .prepare(&PrepareRequest {
            home: "/h".to_string(),
            context: "ctx".to_string(),
            offered: Vec::new(),
            interactive: true,
            cwd: None,
            existing: Default::default(),
            permissions: Some(r#"{"edit":"ask"}"#.to_string()),
            session_home: run_ports::SessionHome::Session,
            items,
        })
        .unwrap()
}

fn file(prepared: &Prepared, rel: &str) -> Option<String> {
    prepared
        .files
        .iter()
        .find(|f| f.rel == rel)
        .map(|f| String::from_utf8(f.bytes.clone()).unwrap())
}

#[test]
fn test_a_command_is_a_file_of_the_config_dir_s_commands_and_a_climbing_name_none() {
    let prepared = prepare(items());
    assert_eq!(
        file(&prepared, "commands/team-onboarding.md").as_deref(),
        Some("---\ndescription: \"the team's \\\"onboarding\\\"\"\n---\n\nCOMMAND-BODY")
    );
    assert!(
        !prepared.files.iter().any(|f| f.rel.contains("escape")),
        "a name that climbs is no file"
    );
}

#[test]
fn test_each_server_is_set_under_mcp_of_the_session_config() {
    let config = Json::parse(&file(&prepare(items()), CONFIG_FILE).unwrap()).unwrap();
    assert_eq!(
        config,
        Json::parse(
            r#"{"mcp":{"toolserver":{"type":"local","command":["tool-cmd","--flag"],"environment":{"K":"V"}},"remote":{"type":"remote","url":"https://mcp.example/","headers":{"X":"Y"}}},"permission":{"edit":"ask"}}"#
        )
        .unwrap()
    );
    // None to register: no member is made.
    let bare = Json::parse(&file(&prepare(SessionItems::default()), CONFIG_FILE).unwrap()).unwrap();
    assert!(bare.get("mcp").is_none());
    // Hers of another name stays.
    let hers = super::with_servers(
        Json::parse(r#"{"mcp":{"mine":{"type":"local","command":["own"]}}}"#).unwrap(),
        &items(),
    );
    assert!(hers.get("mcp").unwrap().get("mine").is_some());
}

#[test]
fn test_the_session_hooks_plugin_lists_the_command_hooks_of_the_session_s_start_alone() {
    let prepared = prepare(items());
    let written = file(&prepared, SESSION_HOOKS_FILE).expect("the plugin, always");
    assert_eq!(written, session_hooks(&items()));
    assert!(written.contains("\"echo START\""), "{written}");
    assert!(
        !written.contains("echo END"),
        "a session's end is not carried"
    );
    assert!(
        !written.contains("say hi"),
        "a prompt hook has nothing to run it"
    );
    // With none, it is written running nothing, so none outlives a launch.
    assert_eq!(
        file(&prepare(SessionItems::default()), SESSION_HOOKS_FILE).as_deref(),
        Some(SESSION_HOOKS)
    );
}

/// What opencode does with the plugin: one instance, the session created
/// (twice, and a sub-agent's), then `dispose`.
const DRIVER: &str = r#"import { pathToFileURL } from "node:url"
const [plugin] = process.argv.slice(2)
const { CtxloomSessionHooks } = await import(pathToFileURL(plugin).href)
const hooks = await CtxloomSessionHooks({})
const created = (info) => ({ event: { id: "e", type: "session.created", properties: { info } } })
await hooks.event(created({ id: "ses_1" }))
await hooks.event(created({ id: "ses_1" }))
await hooks.event(created({ id: "ses_2", parentID: "ses_1" }))
await hooks.event({ event: { id: "e", type: "session.idle", properties: { sessionID: "ses_1" } } })
await hooks.dispose()
"#;

// Needs node on PATH (opencode runs its plugins under Bun; the plugin uses
// node's builtins alone, which both have).
#[test]
fn test_the_plugin_runs_each_start_hook_once_in_order_into_the_hooks_log() {
    if !runs_scripts() {
        return;
    }
    let dir = Rooted::temp().unwrap();
    let put = |path: &str, text: &str| {
        let path = os::fixture(path);
        dir.create_dir_all(path.parent().unwrap(), Mode::DIR)
            .unwrap();
        dir.write_atomic(&path, text.as_bytes(), Perms::Exact(Mode::FILE))
            .unwrap();
        dir.real_path(&path).unwrap()
    };
    let items = SessionItems {
        hooks: vec![
            hook("session_start", "echo FIRST; echo SHELL-LINE", &[]),
            hook(
                "session_start",
                "printf",
                &["%s-%s\\n", "two words", "SECOND"],
            ),
            hook("session_start", "no-such-program-j000400", &["x"]),
            hook("session_start", "echo THIRD", &[]),
        ],
        ..SessionItems::default()
    };
    let plugin = put(
        "/home/plugins/ctxloom-session-hooks.js",
        &session_hooks(&items),
    );
    let driver = put("/driver.mjs", DRIVER);
    let home = plugin.parent().unwrap().parent().unwrap().to_path_buf();
    let out = Command::new("node")
        .args([&driver, &plugin])
        .env("OPENCODE_CONFIG_DIR", &home)
        .output()
        .expect("node runs (is it on PATH?)");
    assert!(
        out.status.success(),
        "{}",
        String::from_utf8_lossy(&out.stderr)
    );
    let log = dir
        .read_to_string(&os::fixture("/home/ctxloom-hooks.log"))
        .unwrap();
    assert_eq!(
        log.lines()
            .filter(|l| !l.contains("no-such-program"))
            .collect::<Vec<_>>(),
        ["FIRST", "SHELL-LINE", "two words-SECOND", "THIRD"],
        "each once, in order, a missing program stopping nothing: {log}"
    );
}
