use std::collections::BTreeMap;

use run_ports::{
    SessionSurfaces, SurfaceCommand, SurfaceFile, SurfaceHook, SurfaceMcp, SurfaceSkill,
};
use surface_ports::Json;

use super::*;

pub(crate) fn offered() -> SessionSurfaces {
    SessionSurfaces {
        commands: vec![SurfaceCommand {
            name: "standards/conventional-commits".to_string(),
            description: "Commit messages".to_string(),
            body: "COMMITS-BODY\n".to_string(),
        }],
        skills: vec![SurfaceSkill {
            name: "review-checklist".to_string(),
            description: "Reviews a change".to_string(),
            files: vec![
                SurfaceFile {
                    path: "SKILL.md".to_string(),
                    bytes: b"---\nname: review-checklist\n---\nSKILL-BODY\n".to_vec(),
                    executable: false,
                },
                SurfaceFile {
                    path: "scripts/check.sh".to_string(),
                    bytes: b"echo check\n".to_vec(),
                    executable: true,
                },
            ],
        }],
        mcp: vec![SurfaceMcp {
            name: "ledger".to_string(),
            command: "ledger-server".to_string(),
            args: vec!["--serve".to_string()],
            env: BTreeMap::from([("LEDGER_HOME".to_string(), "/l".to_string())]),
            ..SurfaceMcp::default()
        }],
        hooks: vec![SurfaceHook {
            event: "post_file_edit".to_string(),
            command: "stamp-edits".to_string(),
            ..SurfaceHook::default()
        }],
    }
}

#[test]
fn test_commands_and_skills_are_files_of_the_config_dir_itself() {
    let (files, skipped) = home_files(&offered());
    let rels: Vec<&str> = files.iter().map(|f| f.rel.as_str()).collect();
    assert_eq!(
        rels,
        [
            "commands/standards-conventional-commits.md",
            "skills/review-checklist/SKILL.md",
            "skills/review-checklist/scripts/check.sh",
        ]
    );
    assert_eq!(
        files[0].bytes,
        b"---\ndescription: Commit messages\n---\n\nCOMMITS-BODY\n"
    );
    assert_eq!(files[2].bytes, b"echo check\n");
    assert!(skipped.is_empty(), "{skipped:?}");
}

#[test]
fn test_a_skill_claude_would_refuse_is_left_out_and_said() {
    let mut surfaces = offered();
    surfaces.skills[0].description.clear();
    let (files, skipped) = home_files(&surfaces);
    assert!(
        files.iter().all(|f| !f.rel.starts_with("skills/")),
        "{files:?}"
    );
    assert_eq!(skipped.len(), 1, "{skipped:?}");
    assert!(skipped[0].contains("review-checklist"), "{skipped:?}");
}

#[test]
fn test_hooks_keep_their_event_and_order() {
    let mut surfaces = offered();
    surfaces.hooks.push(SurfaceHook {
        event: "post_file_edit".to_string(),
        command: "second".to_string(),
        timeout: 5,
        ..SurfaceHook::default()
    });
    let hooks = hooks(&surfaces);
    let commands: Vec<&str> = hooks["post_file_edit"]
        .iter()
        .map(|h| h.command.as_str())
        .collect();
    assert_eq!(commands, ["stamp-edits", "second"]);
    assert_eq!(hooks["post_file_edit"][1].timeout, 5);
}

#[test]
fn test_mcp_servers_are_registered_at_user_scope_over_what_the_state_holds() {
    let mut state = Json::parse(
        r#"{"hasCompletedOnboarding":true,"mcpServers":{"ledger":{"command":"old"},"hers":{"command":"h"}}}"#,
    )
    .unwrap();
    register_mcp(&mut state, &offered());
    let servers = state.get("mcpServers").unwrap();
    assert_eq!(
        servers.get("ledger").unwrap().pretty(),
        Json::parse(r#"{"command":"ledger-server","args":["--serve"],"env":{"LEDGER_HOME":"/l"}}"#)
            .unwrap()
            .pretty()
    );
    assert!(servers.get("hers").is_some());
    assert_eq!(state.get("hasCompletedOnboarding"), Some(&Json::Bool(true)));
    // None offered: the file is left as it was.
    let mut untouched = Json::parse(r#"{"a":1}"#).unwrap();
    register_mcp(&mut untouched, &SessionSurfaces::default());
    assert_eq!(untouched, Json::parse(r#"{"a":1}"#).unwrap());
    // A state whose `mcpServers` is not an object gets one.
    let mut odd = Json::parse(r#"{"mcpServers":[1]}"#).unwrap();
    register_mcp(&mut odd, &offered());
    assert!(odd.get("mcpServers").unwrap().get("ledger").is_some());
}
