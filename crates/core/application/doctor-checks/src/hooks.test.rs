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
    let found = stale(SETTINGS);
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
    assert!(stale(live).is_empty());
    assert!(strip(live).is_none());
}

#[test]
fn strip_takes_exactly_the_stale_entry() {
    let (fixed, gone) = strip(SETTINGS).expect("something to strip");
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
    let (fixed, _) = strip(only).unwrap();
    let doc: serde_json::Value = serde_json::from_str(&fixed).unwrap();
    assert_eq!(doc["hooks"], serde_json::json!({}));
}

#[test]
fn registered_means_any_ctxloom_hook_entry() {
    assert!(registered(SETTINGS));
    assert!(!registered(
        r#"{"hooks":{"Stop":[{"hooks":[{"command":"my-tool hook x"}]}]}}"#
    ));
    assert!(!registered("not json"));
}

#[test]
fn only_a_launched_ctxloom_mcp_is_a_launch() {
    assert!(launches_ctxloom_mcp(
        r#"{"mcpServers":{"ctxloom":{"command":"ctxloom","args":["mcp","serve"]}}}"#
    ));
    assert!(!launches_ctxloom_mcp(
        r#"{"mcpServers":{"ctxloom":{"command":"ctxloom","args":["run"]}}}"#
    ));
    assert!(!launches_ctxloom_mcp(
        r#"{"mcpServers":{"other":{"command":"ctxloom","args":["mcp"]}}}"#
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
    let subs: Vec<String> = stale(old).into_iter().map(|s| s.sub).collect();
    assert_eq!(subs, ["session-start", "skill-mates", "hud"]);
}

// The rewrite spells its keys sorted, as it always has, whatever serde_json's
// map keeps (its `preserve_order`, which agent-client-protocol turns on).
#[test]
fn a_rewrite_keeps_its_keys_sorted_whatever_the_map_keeps() {
    let text = r#"{"zeta":1,"hooks":{"Stop":[{"hooks":[{"command":"ctxloom hook inject-context abc","type":"command"}]}],"After":[]},"alpha":2}"#;
    let (fixed, _) = strip(text).unwrap();
    let at = |key: &str| fixed.find(key).unwrap();
    assert!(at("\"alpha\"") < at("\"hooks\""), "{fixed}");
    assert!(at("\"hooks\"") < at("\"zeta\""), "{fixed}");
}
