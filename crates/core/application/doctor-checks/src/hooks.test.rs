//! Reading the engine's settings. Go: `cli/doctor.feature` "A hook entry an
//! older ctxloom left behind is found, and --fix removes only it".

use crate::hooks::{go_era, launches_ctxloom_mcp, registered, strip};

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
fn only_ctxloom_hooks_that_are_gone_are_go_era() {
    let found = go_era(SETTINGS);
    assert_eq!(found.len(), 1);
    assert_eq!(
        (found[0].event.as_str(), found[0].sub.as_str()),
        ("SessionStart", "inject-context")
    );
}

#[test]
fn a_live_hook_and_a_foreign_program_are_not_go_era() {
    let live = r#"{"hooks":{"SessionStart":[{"hooks":[
        {"type":"command","command":"ctxloom hook claude-code execute SessionStart"},
        {"type":"command","command":"my-tool hook inject-context"},
        {"type":"command","command":"/usr/bin/ctxloom hook claude-code execute statusline"}]}]}}"#;
    assert!(go_era(live).is_empty());
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
fn the_retired_neutral_verbs_are_go_era() {
    let old = r#"{"hooks":{"SessionStart":[{"hooks":[
        {"type":"command","command":"ctxloom hook session-start"},
        {"type":"command","command":"ctxloom hook skill-mates --engine claude-code"},
        {"type":"command","command":"ctxloom hook hud"}]}]}}"#;
    let subs: Vec<String> = go_era(old).into_iter().map(|s| s.sub).collect();
    assert_eq!(subs, ["session-start", "skill-mates", "hud"]);
}

// Owner ruling 162: a Rust-era entry is EXACTLY `ctxloom hook <engine>
// execute <Event>` (what `prepare` writes, as one command line or as
// `command: ctxloom` with the words in `args`); every other `ctxloom hook ...`
// entry is Go-era.
#[test]
fn a_rust_era_entry_is_exactly_the_execute_form() {
    let rust = r#"{"hooks":{"Stop":[{"hooks":[
        {"command":"ctxloom hook claude-code execute Stop"},
        {"command":"'/opt/bin/ctxloom' hook opencode execute Stop"},
        {"command":"ctxloom","args":["hook","claude-code","execute","Stop"]}]}]}}"#;
    assert!(go_era(rust).is_empty());
    assert!(strip(rust).is_none());
    let go = r#"{"hooks":{"Stop":[{"hooks":[
        {"command":"ctxloom hook stamp-plan"},
        {"command":"ctxloom hook claude-code execute"},
        {"command":"ctxloom hook claude-code execute Stop --more"},
        {"command":"ctxloom hook claude-code other Stop"},
        {"command":"ctxloom","args":["hook","session-start","--engine","claude-code"]}]}]}}"#;
    let subs: Vec<String> = go_era(go).into_iter().map(|s| s.sub).collect();
    assert_eq!(
        subs,
        [
            "stamp-plan",
            "claude-code",
            "claude-code",
            "claude-code",
            "session-start"
        ]
    );
}

// An entry with its words in `args` is a ctxloom hook too: registered, and
// stripped when Go-era.
#[test]
fn an_entry_in_args_form_is_read_and_stripped() {
    let args = r#"{"hooks":{"Stop":[{"hooks":[
        {"command":"ctxloom","args":["hook","claude-code","execute","Stop"]},
        {"command":"ctxloom","args":["hook","hud"]}]}]}}"#;
    assert!(registered(args));
    let (fixed, gone) = strip(args).expect("the Go-era entry");
    assert_eq!(gone.len(), 1);
    assert_eq!(gone[0].sub, "hud");
    assert!(fixed.contains("\"execute\""), "{fixed}");
    assert!(!fixed.contains("\"hud\""), "{fixed}");
    // a command that is not ctxloom is not ours, whatever its args
    assert!(!registered(
        r#"{"hooks":{"Stop":[{"hooks":[{"command":"my-tool","args":["hook","x"]}]}]}}"#
    ));
}
