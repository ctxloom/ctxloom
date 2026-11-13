//! j000400: what the launch carries reaches opencode's session home, never
//! the project: commands in its config dir's `commands/`, servers under `mcp`
//! of its `opencode.json`.

use std::collections::BTreeMap;

use run_ports::{Engine, LaunchSurfaces, PrepareRequest, Prepared};
use surface_ports::{Command, Inputs, Json, McpServer};

use crate::{Opencode, CONFIG_FILE};

fn carried() -> Inputs {
    Inputs {
        commands: vec![
            Command {
                name: "team/onboarding".to_string(),
                description: "the team's \"onboarding\"".to_string(),
                body: "COMMAND-BODY".to_string(),
            },
            Command {
                name: "../escape".to_string(),
                description: String::new(),
                body: "NO".to_string(),
            },
        ],
        mcp: vec![
            McpServer {
                name: "toolserver".to_string(),
                command: "tool-cmd".to_string(),
                args: vec!["--flag".to_string()],
                env: BTreeMap::from([("K".to_string(), "V".to_string())]),
                ..McpServer::default()
            },
            McpServer {
                name: "remote".to_string(),
                url: "https://mcp.example/".to_string(),
                headers: BTreeMap::from([("X".to_string(), "Y".to_string())]),
                ..McpServer::default()
            },
        ],
        ..Inputs::default()
    }
}

fn prepare(surfaces: Inputs) -> Prepared {
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
            surfaces: LaunchSurfaces(surfaces),
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
    let prepared = prepare(carried());
    assert_eq!(
        file(&prepared, "commands/team-onboarding.md").as_deref(),
        Some("---\ndescription: \"the team's \\\"onboarding\\\"\"\n---\n\nCOMMAND-BODY")
    );
    assert!(
        !prepared.files.iter().any(|f| f.rel.contains("escape")),
        "a name that climbs is no file"
    );
    // A command without a description is its body alone.
    let bare = super::command_files(&[Command {
        name: "plain".to_string(),
        description: String::new(),
        body: "BODY".to_string(),
    }]);
    assert_eq!(bare.len(), 1);
    assert_eq!(bare[0].rel, "commands/plain.md");
    assert_eq!(bare[0].bytes, b"BODY");
}

#[test]
fn test_a_name_that_is_empty_rooted_or_holds_a_separator_of_another_os_is_no_file() {
    for name in ["", "/abs", "a\\b", "c:d"] {
        let files = super::command_files(&[Command {
            name: name.to_string(),
            ..Command::default()
        }]);
        assert!(files.is_empty(), "{name:?}: {files:?}");
    }
}

#[test]
fn test_each_server_is_set_under_mcp_of_the_session_config() {
    let config = Json::parse(&file(&prepare(carried()), CONFIG_FILE).unwrap()).unwrap();
    assert_eq!(
        config,
        Json::parse(
            r#"{"mcp":{"toolserver":{"type":"local","command":["tool-cmd","--flag"],"environment":{"K":"V"}},"remote":{"type":"remote","url":"https://mcp.example/","headers":{"X":"Y"}},"ctxloom":{"type":"local","command":["ctxloom","mcp","serve"],"enabled":true}},"permission":{"edit":"ask"}}"#
        )
        .unwrap()
    );
    // Slice 3 step 3 (T20): ctxloom's own server is every session's, the
    // running program's `mcp serve`, no identity in the entry.
    let bare = Json::parse(&file(&prepare(Inputs::default()), CONFIG_FILE).unwrap()).unwrap();
    assert_eq!(
        bare.get("mcp").unwrap(),
        &Json::parse(
            r#"{"ctxloom":{"type":"local","command":["ctxloom","mcp","serve"],"enabled":true}}"#
        )
        .unwrap()
    );
    // Hers of the same name is replaced (OD-6: ctxloom's own keeps it).
    let replaced = super::with_ctxloom(
        Json::parse(r#"{"mcp":{"ctxloom":{"type":"remote","url":"https://x/"}}}"#).unwrap(),
        "ctxloom",
    );
    assert_eq!(
        replaced
            .get("mcp")
            .unwrap()
            .get("ctxloom")
            .unwrap()
            .get("type"),
        Some(&Json::text("local"))
    );
    // Hers of another name stays.
    let hers = super::with_servers(
        Json::parse(r#"{"mcp":{"mine":{"type":"local","command":["own"]}}}"#).unwrap(),
        &carried().mcp,
    );
    assert!(hers.get("mcp").unwrap().get("mine").is_some());
    // A member that is not an object is made one.
    let odd = super::with_servers(Json::parse(r#"{"mcp":1}"#).unwrap(), &carried().mcp);
    assert!(odd.get("mcp").unwrap().get("toolserver").is_some());
}
