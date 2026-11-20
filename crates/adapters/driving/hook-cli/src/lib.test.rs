//! The `hook` noun as mounted.

use super::*;

// the noun is `hook <engine> execute <hook-name>`,
// hidden; `session-bind`, `hud`, `session-start` and `skill-mates` are no
// longer verbs (they became `hook claude-code execute SessionStart`,
// `... statusline`, `... SessionStart` and `... PostToolUse`).
#[test]
fn test_the_hook_noun_is_hidden_and_executes_an_engines_hook() {
    let noun = noun();
    assert_eq!(noun.get_name(), "hook");
    assert!(noun.is_hide_set());
    assert!(noun
        .get_about()
        .is_some_and(|about| about.to_string().contains("not for people")));
    let parsed = noun
        .clone()
        .try_get_matches_from(["hook", "claude-code", "execute", "Stop"])
        .unwrap();
    assert_eq!(
        parsed.get_one::<String>("engine").map(String::as_str),
        Some("claude-code")
    );
    let execute = parsed.subcommand_matches("execute").unwrap();
    assert_eq!(
        execute.get_one::<String>("hook_name").map(String::as_str),
        Some("Stop")
    );
}

#[test]
fn test_the_engine_is_any_name_the_registry_will_judge_and_the_old_verbs_are_gone() {
    // The CLI validates nothing about the engine: the registry refuses an
    // unknown one, listing the known (hook-cli holds no engine knowledge).
    assert!(noun()
        .try_get_matches_from(["hook", "no-such-engine", "execute", "Stop"])
        .is_ok());
    for old in ["session-bind", "hud", "session-start", "skill-mates"] {
        let line = ["hook", old];
        let parsed = noun().try_get_matches_from(line);
        assert!(
            parsed.is_err(),
            "`hook {old}` is not a command any more: it parsed as an engine named {old} with no `execute`"
        );
    }
}
