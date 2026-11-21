//! The engines' callbacks (cli/session_hooks.feature, cli/post_tool_hooks.feature).

use super::*;
use crate::fakes::{world, CONFIG};

// session_hooks.feature: the hook answers through the codec of the engine it
// was registered under; an engine it does not know is refused by name.
#[test]
fn test_a_hook_under_an_unknown_engine_is_refused_by_name() {
    let w = world(CONFIG);
    let err = w.setup.session_start("nosuch", "{}").unwrap_err();
    let SetupError::UnknownHookEngine { engine, known } = err else {
        panic!()
    };
    assert_eq!(
        (engine.as_str(), known.as_str()),
        ("nosuch", "claude-code, mock")
    );
}

// Go's session-start: a payload it cannot read is an error (an engine shows a
// failed hook, which is the truth).
#[test]
fn test_a_payload_that_does_not_read_fails_the_hook() {
    let w = world(CONFIG);
    assert!(w.setup.session_start("claude-code", "not json").is_err());
    assert!(w.setup.skill_mates("claude-code", "not json").is_err());
}

// session_hooks.feature: Scenario: The SessionStart hook never hands the
// engine the project's context - what it hands is a notice, and only when the
// project has profiles and no agents.
#[test]
fn test_session_start_nudges_only_a_project_with_profiles_and_no_agents() {
    let none = world("schema_version: 8\n");
    let out = none.setup.session_start("claude-code", "{}").unwrap();
    assert!(
        out.starts_with("context=;notice=ctxloom: this project has profiles but no agents"),
        "{out}"
    );
    assert!(!out.contains("FRAGMENT-BODY"));
    let agents = world(CONFIG);
    assert_eq!(
        agents.setup.session_start("claude-code", "{}").unwrap(),
        "context=;notice=\n"
    );
}

// post_tool_hooks.feature: Scenario: Completing one skill of a linked pair
// names the other
#[test]
fn test_completing_one_skill_names_the_linked_skill_not_yet_invoked() {
    let w = world(CONFIG);
    let out = w
        .setup
        .skill_mates("claude-code", r#"{"skill":"admit"}"#)
        .unwrap();
    assert_eq!(
        out,
        "context=Skill admit completed; linked skills not yet invoked this session: unattended;notice=\n"
    );
}

// post_tool_hooks.feature: the transcript is where "already invoked" comes
// from, so nothing is persisted to answer it.
#[test]
fn test_a_mate_the_transcript_shows_invoked_is_not_named() {
    let w = world(CONFIG);
    w.store
        .state()
        .files
        .insert("/t/turn.jsonl".to_string(), b"unattended\n".to_vec());
    let out = w
        .setup
        .skill_mates(
            "claude-code",
            r#"{"skill":"admit","transcript":"/t/turn.jsonl"}"#,
        )
        .unwrap();
    assert_eq!(out, "context=;notice=\n");
}

// post_tool_hooks.feature: Scenario: A tool call that is not a skill is passed
// over in silence
#[test]
fn test_a_call_that_ran_no_skill_is_silent() {
    let w = world(CONFIG);
    assert_eq!(
        w.setup.skill_mates("claude-code", "{}").unwrap(),
        "context=;notice=\n"
    );
}

// A skill in no group, or a skill the profile does not deliver, has no mates.
#[test]
fn test_a_skill_in_no_group_has_no_mates() {
    let w = world(CONFIG);
    assert_eq!(
        w.setup
            .skill_mates("claude-code", r#"{"skill":"nosuch"}"#)
            .unwrap(),
        "context=;notice=\n"
    );
}

// The hooks run degraded: a config the strict load refuses (an unknown key)
// must not silence the nudge or the mates.
#[test]
fn test_the_hooks_answer_from_a_config_that_a_strict_load_would_refuse() {
    let nudged = world("schema_version: 8\nbogus_key: 1\n");
    let out = nudged.setup.session_start("claude-code", "{}").unwrap();
    assert!(
        out.contains("notice=ctxloom: this project has profiles"),
        "{out}"
    );

    let strict_refused: &'static str =
        Box::leak(format!("{CONFIG}bogus_key: 1\n").into_boxed_str());
    let mates = world(strict_refused);
    let out = mates
        .setup
        .skill_mates("claude-code", r#"{"skill":"admit"}"#)
        .unwrap();
    assert!(
        out.contains("linked skills not yet invoked this session: unattended"),
        "{out}"
    );
}

// ---------------------------------------------------------------------------
// `hook <engine> execute <native hook name>`.

const MARKER: &str = r#"<ctxloom name="brisk-copper-moth" kind="harp" />"#;

#[test]
fn test_executing_a_hook_of_an_unknown_engine_is_refused_listing_the_known_ones() {
    let w = world(CONFIG);
    let err = w
        .setup
        .execute_hook("nosuch", "Stop", "{}", "brisk-copper-moth")
        .unwrap_err();
    let SetupError::UnknownHookEngine { engine, known } = err else {
        panic!()
    };
    assert_eq!(
        (engine.as_str(), known.as_str()),
        ("nosuch", "claude-code, mock")
    );
}

#[test]
fn test_a_session_start_answers_the_marker_and_the_notice_and_carries_its_event() {
    let w = world("schema_version: 8\n");
    let answer = w
        .setup
        .execute_hook(
            "claude-code",
            "SessionStart",
            r#"{"session":"vendor-42","transcript":"/t/a.jsonl"}"#,
            "brisk-copper-moth",
        )
        .unwrap();
    // The fake wire spells an answer `context=..;notice=..`.
    assert!(
        answer.stdout.starts_with(&format!(
            "context={MARKER};notice=ctxloom: this project has profiles but no agents"
        )),
        "{}",
        answer.stdout
    );
    // Its bind is an observation of the event, recorded with the rest.
    let event = answer.event.expect("the decoded event");
    assert_eq!(
        (event.native_session.as_str(), event.transcript.as_str()),
        ("vendor-42", "/t/a.jsonl")
    );
    assert!(!answer.harp_missing);
}

#[test]
fn test_a_session_start_with_no_usable_harp_says_nothing() {
    let w = world(CONFIG);
    for harp in ["", "has\"quote", "has>angle"] {
        let answer = w
            .setup
            .execute_hook("claude-code", "SessionStart", "{}", harp)
            .unwrap();
        assert!(!answer.stdout.contains("kind=\"harp\""), "{harp:?}");
        assert!(answer.harp_missing, "{harp:?}");
    }
}

#[test]
fn test_a_post_tool_call_answers_what_skill_mates_does() {
    let w = world(CONFIG);
    let answer = w
        .setup
        .execute_hook("claude-code", "PostToolUse", r#"{"skill":"admit"}"#, "")
        .unwrap();
    assert_eq!(
        answer.stdout,
        w.setup
            .skill_mates("claude-code", r#"{"skill":"admit"}"#)
            .unwrap()
    );
}

// The primary agent's data events are recorded elsewhere (M2); here they are
// silence on the wire and no bind.
#[test]
fn test_the_events_that_carry_data_answer_silence() {
    let w = world(CONFIG);
    for native in ["Stop", "MessageDisplay", "PostToolBatch", "SessionEnd"] {
        let answer = w
            .setup
            .execute_hook("claude-code", native, "{}", "brisk-copper-moth")
            .unwrap();
        assert_eq!(answer.stdout, "context=;notice=\n", "{native}");
        assert!(!answer.harp_missing, "{native}");
    }
}

#[test]
fn test_a_payload_that_does_not_read_fails_the_executed_hook() {
    let w = world(CONFIG);
    assert!(w
        .setup
        .execute_hook("claude-code", "Stop", "not json", "brisk-copper-moth")
        .is_err());
}

// Go: harpmarker TestFormat.
#[test]
fn test_the_marker_names_the_harp() {
    assert_eq!(
        harp_marker("plump-loose-sash").as_deref(),
        Some(r#"<ctxloom name="plump-loose-sash" kind="harp" />"#)
    );
    assert_eq!(harp_marker(""), None);
}

// Go: "A '\"' closes the name attribute early, so the element reads back as a
// DIFFERENT, possibly real harp; a '>' closes the element before kind=\"harp\"
// is reached ... a marker that names the wrong harp is worse than no marker".
#[test]
fn test_a_harp_the_marker_cannot_carry_has_no_marker() {
    assert_eq!(harp_marker("a\"b"), None);
    assert_eq!(harp_marker("a>b"), None);
}

// Hook capture (PROPOSAL, design/hook-capture): the answer carries the event
// the codec decoded, so the caller that records it decodes nothing itself.
#[test]
fn test_the_answer_carries_the_decoded_event() {
    let w = world(CONFIG);
    let answer = w
        .setup
        .execute_hook(
            "claude-code",
            "Stop",
            r#"{"session":"native-1"}"#,
            "brisk-copper-moth",
        )
        .unwrap();
    assert_eq!(
        answer.event,
        Some(HookEvent {
            event: "turn_end".to_string(),
            native_session: "native-1".to_string(),
            ..HookEvent::default()
        })
    );
}

#[test]
fn test_the_decoded_event_rides_every_answer_kind() {
    let w = world(CONFIG);
    for native in ["SessionStart", "PostToolUse", "Stop", "UserPromptSubmit"] {
        let answer = w
            .setup
            .execute_hook("claude-code", native, "{}", "brisk-copper-moth")
            .unwrap();
        assert!(answer.event.is_some(), "{native}");
    }
}

// O3 (owner 2026-11-17): the hooks read the library through the daemon
// alone; one that did not answer in time leaves the engine's answer well
// formed and empty of the library, with no read in process.
struct NoDaemon;

impl crate::HookLibrary for NoDaemon {
    fn agents_and_profiles(&self) -> Option<(bool, bool)> {
        None
    }
    fn skills(&self) -> Option<Vec<crate::HookSkill>> {
        None
    }
}

#[test]
fn test_with_no_daemon_in_time_the_hooks_say_nothing_of_the_library() {
    let w = crate::fakes::world("schema_version: 8\n");
    let setup = w.setup.with_hook_library(Box::new(NoDaemon));
    assert_eq!(
        setup.session_start("claude-code", "{}").unwrap(),
        "context=;notice=\n"
    );
    assert_eq!(
        setup
            .skill_mates("claude-code", r#"{"skill":"admit"}"#)
            .unwrap(),
        "context=;notice=\n"
    );
}

#[test]
fn test_a_listed_skill_s_bundle_is_its_name_before_skills() {
    let listed = daemon_proto::v1::ListSkillsResponse {
        skills: vec![
            daemon_proto::v1::Skill {
                name: "projects/p/remotes/acme/bundles/rules/skills/admit".to_string(),
                tags: vec!["ctxloom:link_id=x".to_string()],
                ..daemon_proto::v1::Skill::default()
            },
            daemon_proto::v1::Skill {
                name: "not a skill's name".to_string(),
                ..daemon_proto::v1::Skill::default()
            },
        ],
        next_page_token: String::new(),
    };
    assert_eq!(
        crate::hook_skills(&listed),
        vec![crate::HookSkill {
            name: "admit".to_string(),
            bundle: "projects/p/remotes/acme/bundles/rules".to_string(),
            tags: vec!["ctxloom:link_id=x".to_string()],
        }]
    );
}

// Owner ruling 2026-11-17 (U2 part 2's escalations): a skill's mates are
// matched on its SKILL.md frontmatter `name:` (the `Skill`'s
// `display_name`), as before the hooks read through the daemon, so a skill
// whose directory and frontmatter name differ is still matched by the name
// the engine reports.
#[test]
fn test_mates_are_matched_by_the_frontmatter_name_not_the_directory() {
    let mut owned = crate::fakes::bundles();
    owned[0].1.extend([
        (
            "skills/admit-dir/SKILL.md",
            "---\nname: admit-renamed\ndescription: Admit.\n---\nA\n",
        ),
        (
            "skills/admit-dir/.meta.yaml",
            "tags: [ctxloom:link_id=pair]\n",
        ),
        (
            "skills/mate-dir/SKILL.md",
            "---\nname: mate-renamed\ndescription: Mate.\n---\nM\n",
        ),
        (
            "skills/mate-dir/.meta.yaml",
            "tags: [ctxloom:link_id=pair]\n",
        ),
    ]);
    let w = crate::fakes::world_of(
        CONFIG,
        owned,
        vec![Box::new(crate::fakes::Engine::all("claude-code"))],
    );
    let out = w
        .setup
        .skill_mates("claude-code", r#"{"skill":"admit-renamed"}"#)
        .unwrap();
    assert_eq!(
        out,
        "context=Skill admit-renamed completed; linked skills not yet invoked this session: mate-renamed;notice=\n"
    );
}

#[test]
fn test_a_listed_skill_is_judged_by_its_display_name_else_its_id() {
    let listed = daemon_proto::v1::ListSkillsResponse {
        skills: vec![
            daemon_proto::v1::Skill {
                name: "projects/p/bundles/demo/skills/admit-dir".to_string(),
                display_name: "admit".to_string(),
                ..daemon_proto::v1::Skill::default()
            },
            daemon_proto::v1::Skill {
                name: "projects/p/bundles/demo/skills/plain".to_string(),
                ..daemon_proto::v1::Skill::default()
            },
        ],
        next_page_token: String::new(),
    };
    let names: Vec<String> = crate::hook_skills(&listed)
        .into_iter()
        .map(|skill| skill.name)
        .collect();
    assert_eq!(names, ["admit", "plain"]);
}

/// The daemon owing a re-assembly (step 10c U13): what each hook's one
/// call asked it, `<hook> <budget>`, and the context it answers.
#[derive(Default)]
struct Owes(std::sync::Mutex<Vec<String>>);

impl crate::HookContexts for Owes {
    fn hook_context(
        &self,
        hook: &str,
        _: &[&str],
        _: &str,
        budget: usize,
    ) -> Option<crate::HookCarried> {
        self.0.lock().unwrap().push(format!("{hook} {budget}"));
        Some(crate::HookCarried {
            context: "re-assembled".to_string(),
            ..crate::HookCarried::default()
        })
    }
}

// Step 10c U13 (G6/G7): at a prompt submitted or a tool's result, the hook
// asks the daemon once (the lean hook RPC) what its session carries, by its
// engine's own hook name, and carries it as its context; no other hook
// asks.
#[test]
fn test_a_prompt_or_a_tool_result_carries_the_context_its_session_is_owed() {
    let owes = std::sync::Arc::new(Owes::default());
    struct Shared(std::sync::Arc<Owes>);
    impl crate::HookContexts for Shared {
        fn hook_context(
            &self,
            hook: &str,
            urgencies: &[&str],
            wake: &str,
            budget: usize,
        ) -> Option<crate::HookCarried> {
            self.0.hook_context(hook, urgencies, wake, budget)
        }
    }
    let w = crate::fakes::world(CONFIG);
    let setup = w
        .setup
        .with_hook_contexts(Box::new(Shared(std::sync::Arc::clone(&owes))));
    for native in ["UserPromptSubmit", "PostToolUse"] {
        let answer = setup
            .execute_hook("claude-code", native, "{}", "brisk-copper-moth")
            .unwrap();
        assert_eq!(answer.stdout, "context=re-assembled;notice=\n", "{native}");
    }
    for native in ["SessionStart", "Stop"] {
        setup
            .execute_hook("claude-code", native, "{}", "brisk-copper-moth")
            .unwrap();
    }
    assert_eq!(
        *owes.0.lock().unwrap(),
        ["UserPromptSubmit 10000", "PostToolUse 10000"],
        "nothing else in the answer: the whole budget"
    );
}

// A tool's result that completed a skill carries its mates' line and the
// owed context both, the line first, the call given what the line left.
#[test]
fn test_a_skills_mates_and_an_owed_context_are_carried_together() {
    struct Mates;
    impl crate::HookLibrary for Mates {
        fn agents_and_profiles(&self) -> Option<(bool, bool)> {
            None
        }
        fn skills(&self) -> Option<Vec<crate::HookSkill>> {
            let skill = |name: &str| crate::HookSkill {
                name: name.to_string(),
                bundle: "rules".to_string(),
                tags: vec!["ctxloom:link_id=x".to_string()],
            };
            Some(vec![skill("admit"), skill("unattended")])
        }
    }
    struct Left;
    impl crate::HookContexts for Left {
        fn hook_context(
            &self,
            _: &str,
            _: &[&str],
            _: &str,
            budget: usize,
        ) -> Option<crate::HookCarried> {
            assert!(budget < 10_000, "what the mates line left: {budget}");
            Some(crate::HookCarried {
                context: "re-assembled".to_string(),
                ..crate::HookCarried::default()
            })
        }
    }
    let w = crate::fakes::world(CONFIG);
    let setup = w
        .setup
        .with_hook_library(Box::new(Mates))
        .with_hook_contexts(Box::new(Left));
    let answer = setup
        .execute_hook(
            "claude-code",
            "PostToolUse",
            r#"{"skill":"admit"}"#,
            "brisk-copper-moth",
        )
        .unwrap();
    assert!(
        answer.stdout.starts_with("context=Skill admit completed;")
            && answer.stdout.contains("\n\nre-assembled;notice="),
        "{}",
        answer.stdout
    );
}
