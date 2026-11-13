//! j000400: a session's profiles reach claude's session home, never the
//! project: commands in its config dir's `commands/`, servers at user scope
//! in its state file, hooks beside ctxloom's own in the `--settings` file.

use std::collections::BTreeMap;

use run_ports::{
    Engine, EngineError, PrepareRequest, Prepared, SessionCommand, SessionHook, SessionItems,
    SessionMcp,
};
use surface_ports::Json;

use crate::drive::{HOOKS_FILE, STATE_FILE};
use crate::Claude;

fn items() -> SessionItems {
    SessionItems {
        commands: vec![SessionCommand {
            name: "team/onboarding".to_string(),
            description: "the team's onboarding".to_string(),
            body: "COMMAND-BODY".to_string(),
        }],
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
                ..SessionMcp::default()
            },
        ],
        hooks: vec![
            SessionHook {
                event: "session_start".to_string(),
                command: "echo START".to_string(),
                ..SessionHook::default()
            },
            SessionHook {
                event: "pre_tool".to_string(),
                command: "check".to_string(),
                tool: "shell".to_string(),
                ..SessionHook::default()
            },
        ],
    }
}

fn prepare(items: SessionItems, existing: Option<&str>) -> Result<Prepared, EngineError> {
    Claude::calling("ctxloom-rs").prepare(&PrepareRequest {
        home: "/h".to_string(),
        context: "the context".to_string(),
        offered: Vec::new(),
        interactive: true,
        cwd: Some("/work".to_string()),
        existing: existing
            .map(|text| BTreeMap::from([(STATE_FILE.to_string(), text.as_bytes().to_vec())]))
            .unwrap_or_default(),
        permissions: None,
        session_home: run_ports::SessionHome::Session,
        items,
    })
}

fn file(prepared: &Prepared, rel: &str) -> String {
    let file = prepared
        .files
        .iter()
        .find(|f| f.rel == rel)
        .unwrap_or_else(|| {
            panic!(
                "no {rel} in {:?}",
                prepared.files.iter().map(|f| &f.rel).collect::<Vec<_>>()
            )
        });
    String::from_utf8(file.bytes.clone()).unwrap()
}

fn json(prepared: &Prepared, rel: &str) -> Json {
    Json::parse(&file(prepared, rel)).unwrap()
}

#[test]
fn test_a_command_is_a_file_of_the_config_dir_s_commands() {
    let prepared = prepare(items(), None).unwrap();
    assert_eq!(
        file(&prepared, "commands/team-onboarding.md"),
        "---\ndescription: \"the team's onboarding\"\n---\n\nCOMMAND-BODY"
    );
    assert!(!prepared.files.iter().any(|f| f.rel.starts_with(".claude/")));
}

#[test]
fn test_each_server_is_registered_at_user_scope_beside_what_the_state_holds() {
    let existing = r#"{"theme":"dark","mcpServers":{"mine":{"command":"own"},"toolserver":{"command":"stale"}}}"#;
    let state = json(&prepare(items(), Some(existing)).unwrap(), STATE_FILE);
    let servers = state.get("mcpServers").unwrap();
    assert_eq!(
        servers.get("toolserver").unwrap().pretty(),
        Json::parse(r#"{"command":"tool-cmd","args":["--flag"],"env":{"K":"V"}}"#)
            .unwrap()
            .pretty()
    );
    assert_eq!(
        servers
            .get("remote")
            .unwrap()
            .get("url")
            .and_then(Json::as_str),
        Some("https://mcp.example/")
    );
    // The session's own is left, and so is the rest of claude's state.
    assert_eq!(
        servers
            .get("mine")
            .unwrap()
            .get("command")
            .and_then(Json::as_str),
        Some("own")
    );
    assert_eq!(state.get("theme").and_then(Json::as_str), Some("dark"));
    assert_eq!(state.get("hasCompletedOnboarding"), Some(&Json::Bool(true)));
    // None to register: no member is made.
    let bare = json(&prepare(SessionItems::default(), None).unwrap(), STATE_FILE);
    assert!(bare.get("mcpServers").is_none());
}

#[test]
fn test_the_profile_s_hooks_follow_ctxloom_s_own_in_the_settings_file() {
    let settings = json(&prepare(items(), None).unwrap(), HOOKS_FILE);
    let hooks = settings.get("hooks").unwrap();
    let start = hooks.get("SessionStart").and_then(Json::as_array).unwrap();
    assert_eq!(start.len(), 2, "ctxloom's callback, then the profile's");
    let command = |group: &Json| {
        group.get("hooks").and_then(Json::as_array).unwrap()[0]
            .get("command")
            .and_then(Json::as_str)
            .unwrap()
            .to_string()
    };
    assert_eq!(command(&start[0]), "ctxloom-rs");
    assert_eq!(command(&start[1]), "echo START");
    // A tool class is claude's matcher.
    let pre = hooks.get("PreToolUse").and_then(Json::as_array).unwrap();
    assert_eq!(pre.len(), 1);
    assert_eq!(
        pre[0].get("matcher").and_then(Json::as_str),
        Some("Bash|PowerShell")
    );
    assert_eq!(command(&pre[0]), "check");
    // No item: the file is ctxloom's alone, as before.
    let bare = file(&prepare(SessionItems::default(), None).unwrap(), HOOKS_FILE);
    assert_eq!(bare, crate::hooks::home_settings(true, "ctxloom-rs"));
}

#[test]
fn test_a_hook_claude_cannot_narrow_refuses_the_launch() {
    let mut items = items();
    items.hooks.push(SessionHook {
        event: "pre_tool".to_string(),
        command: "x".to_string(),
        tool: "browser".to_string(),
        ..SessionHook::default()
    });
    let refused = prepare(items, None).unwrap_err();
    assert!(
        matches!(&refused, EngineError::CannotCarry { engine, ext_err_txt }
            if engine == "claude-code" && ext_err_txt.contains("\"browser\"")),
        "{refused:?}"
    );
}
