//! Reading the engine's settings. Go: `cli/doctor.feature` "A hook entry an
//! older ctxloom left behind is found, and --fix removes only it".

use crate::hooks::{launches_ctxloom_mcp, registered, stale, strip};

const SETTINGS: &str = r#"{
  "permissions": {"allow": ["Bash(ls)"]},
  "hooks": {
    "SessionStart": [
      {"matcher": "", "hooks": [
        {"type": "command", "command": "'ctxloom' hook inject-context --part 1 --of 2 abc123"},
        {"type": "command", "command": "ctxloom hook claude-code execute SessionStart"}
      ]}
    ],
    "Stop": [
      {"hooks": [{"type": "command", "command": "my-tool hook inject-context"}]}
    ]
  }
}"#;

#[test]
fn only_ctxloom_hooks_that_are_gone_are_stale() {
    let found = stale(SETTINGS, "ctxloom");
    assert_eq!(found.len(), 1);
    assert_eq!(
        (found[0].event.as_str(), found[0].sub.as_str()),
        ("SessionStart", "inject-context")
    );
}

#[test]
fn a_live_hook_and_a_foreign_program_are_not_stale() {
    let live = r#"{"hooks":{"SessionStart":[{"hooks":[
        {"type":"command","command":"ctxloom hook claude-code execute SessionStart"},
        {"type":"command","command":"my-tool hook inject-context"},
        {"type":"command","command":"/usr/bin/ctxloom hook claude-code execute statusline"}]}]}}"#;
    assert!(stale(live, "ctxloom").is_empty());
    assert!(strip(live, "ctxloom").is_none());
}

#[test]
fn strip_takes_exactly_the_stale_entry() {
    let (fixed, gone) = strip(SETTINGS, "ctxloom").expect("something to strip");
    assert_eq!(gone.len(), 1);
    assert!(!fixed.contains("inject-context --part"));
    assert_eq!(fixed.matches("my-tool hook inject-context").count(), 1);
    assert_eq!(fixed.matches("Bash(ls)").count(), 1);
    assert!(fixed.contains("ctxloom hook claude-code execute SessionStart"));
    assert!(fixed.ends_with('\n'));
}

#[test]
fn a_group_and_an_event_left_empty_go() {
    let only =
        r#"{"hooks":{"SessionStart":[{"hooks":[{"command":"ctxloom hook inject-context abc"}]}]}}"#;
    let (fixed, _) = strip(only, "ctxloom").unwrap();
    let doc: serde_json::Value = serde_json::from_str(&fixed).unwrap();
    assert_eq!(doc["hooks"], serde_json::json!({}));
}

#[test]
fn registered_means_any_ctxloom_hook_entry() {
    assert!(registered(SETTINGS, "ctxloom"));
    assert!(!registered(
        r#"{"hooks":{"Stop":[{"hooks":[{"command":"my-tool hook x"}]}]}}"#,
        "ctxloom"
    ));
    assert!(!registered("not json", "ctxloom"));
}

#[test]
fn only_a_launched_ctxloom_mcp_is_a_launch() {
    assert!(launches_ctxloom_mcp(
        r#"{"mcpServers":{"ctxloom":{"command":"ctxloom","args":["mcp","serve"]}}}"#,
        "ctxloom"
    ));
    assert!(!launches_ctxloom_mcp(
        r#"{"mcpServers":{"ctxloom":{"command":"ctxloom","args":["run"]}}}"#,
        "ctxloom"
    ));
    assert!(!launches_ctxloom_mcp(
        r#"{"mcpServers":{"other":{"command":"ctxloom","args":["mcp"]}}}"#,
        "ctxloom"
    ));
}

// The engine-neutral verbs became `hook <engine> execute <name>`: an entry
// still running one of them fails at every event.
#[test]
fn the_retired_neutral_verbs_are_stale() {
    let old = r#"{"hooks":{"SessionStart":[{"hooks":[
        {"type":"command","command":"ctxloom hook session-start"},
        {"type":"command","command":"ctxloom hook skill-mates --engine claude-code"},
        {"type":"command","command":"ctxloom hook hud"}]}]}}"#;
    let subs: Vec<String> = stale(old, "ctxloom").into_iter().map(|s| s.sub).collect();
    assert_eq!(subs, ["session-start", "skill-mates", "hud"]);
}

// Owner ruling 163: the running binary's own name is ctxloom too, for every
// reading of its entries; `ctxloom` stays ctxloom's.
#[test]
fn an_entry_running_the_binarys_own_name_is_ctxloom() {
    let settings = r#"{"hooks":{"SessionStart":[{"hooks":[
        {"type":"command","command":"ctxloom-other hook inject-context"},
        {"type":"command","command":"/home/a/.local/lib/x/ctxloom-other hook claude-code execute SessionStart"},
        {"type":"command","command":"ctxloom hook hud"}]}]}}"#;
    let subs: Vec<String> = stale(settings, "ctxloom-other")
        .into_iter()
        .map(|s| s.sub)
        .collect();
    assert_eq!(subs, ["inject-context", "hud"]);
    let (fixed, gone) = strip(settings, "ctxloom-other").unwrap();
    assert_eq!(gone.len(), 2);
    assert!(fixed.contains("ctxloom-other hook claude-code execute SessionStart"));
    let live = r#"{"hooks":{"Stop":[{"hooks":[{"command":"ctxloom-other hook claude-code execute Stop"}]}]}}"#;
    assert!(registered(live, "ctxloom-other"));
    assert!(!registered(live, "ctxloom"), "another program's entry");
    assert!(launches_ctxloom_mcp(
        r#"{"mcpServers":{"ctxloom":{"command":"ctxloom-other","args":["mcp"]}}}"#,
        "ctxloom-other"
    ));
}
