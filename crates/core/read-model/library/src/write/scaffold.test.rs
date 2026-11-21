use super::*;

// Owner ruling 111: the registry a new project starts with seeds no remote;
// it was Go's resources/default-remotes.yaml at ca14d6046, which seeded
// `ctxloom-default`. Its header is a DRAFT (smoke 2026-11-19): under rulings
// 117 and 126 nothing built in is guidance (the built-in bundle is never
// injected), so a fresh project's assistant is given none until a remote is
// added; it said "ctxloom's default content is built in and needs no remote".
#[test]
fn test_the_default_remotes_seed_no_remote() {
    assert_eq!(
        DEFAULT_REMOTES,
        "# ctxloom Remote Sources\n# The repositories this project draws bundles and profiles from, under\n# `remotes:` (`ctxloom remote create <name> <url>` adds one).\n#\n# Registering a remote is the trust act: its content reaches the assistant\n# because the project added it. ctxloom builds in no guidance for the\n# assistant: until a remote is added, or the project writes bundles of its\n# own, the assistant is given none.\n\nschema_version: 1\n"
    );
    assert!(!DEFAULT_REMOTES.contains("ctxloom-default"));
}

// Owner ruling 117 (narrowing 110, 111): the seeded profile is BARE. It
// inherits nothing and names no bundle, built in or a remote's: a fresh
// project starts with no coding guidance until a remote is added.
#[test]
fn test_the_seed_profile_is_bare() {
    let seeded = crate::profile::Profile::decode(SEED_PROFILE).expect("it decodes");
    assert!(seeded.parents.is_empty());
    assert!(seeded.bundles.is_empty());
    assert!(seeded.bundle_items.is_empty());
    assert!(seeded.fragments.is_empty());
    assert!(seeded.commands.is_empty());
    assert!(seeded.select_tags.is_empty());
    assert!(!SEED_PROFILE.contains("ai-developer"));
    assert!(!SEED_PROFILE.contains("ctxloom-default"));
    assert!(!SEED_PROFILE.contains("ctxloom+git"));
    assert_eq!(seeded.tags, ["default"]);
    assert_eq!(SEED_NAME, "default");
}

// owner amendment (2026-11-09): no engine-label registry, no top-level auth; the agent
// names its engine, and the engine block is the user's
#[test]
fn test_the_scaffold_binds_the_default_agent_to_its_engine() {
    for engine in [
        "claude-code",
        "mock",
        "opencode",
        "mock-launch",
        "mock-lossy",
        "mock-noskills",
    ] {
        assert_eq!(
            initial_config(engine),
            format!("agents:\n  default:\n    engine: {engine}\n    profiles:\n      - default\n    runtime: host\ndefault_agent: default\nschema_version: 9\n")
        );
    }
}

#[test]
fn test_what_it_scaffolds_is_a_config_this_build_reads() {
    for engine in crate::config::schema::ENGINES {
        let doc = angzarr_yaml::parse(&initial_config(engine)).unwrap();
        assert_eq!(initial_sections(engine), doc);
        assert!(crate::config::schema::validate(&doc).is_empty(), "{engine}");
        assert_eq!(doc.get("schema_version").and_then(Value::as_str), Some("9"));
    }
}

// Step 10c U6: init's answers are written in the config the scaffold
// creates, so its one create holds them: `output.dir` and
// `config.premised`, each only when answered, the keys sorted as a fresh
// document's are.
#[test]
fn test_init_answers_ride_in_the_scaffolded_config() {
    assert_eq!(
        initial_config_with("claude-code", &Answered::default()),
        initial_config("claude-code")
    );
    let both = initial_config_with(
        "opencode",
        &Answered {
            output_dir: Some("out/".to_string()),
            premised: Some("always".to_string()),
        },
    );
    assert_eq!(
        both,
        "agents:\n  default:\n    engine: opencode\n    profiles:\n      - default\n    runtime: host\nconfig:\n  premised: always\ndefault_agent: default\noutput:\n  dir: out/\nschema_version: 9\n"
    );
    let doc = angzarr_yaml::parse(&both).unwrap();
    let violations = crate::config::schema::validate(&doc);
    assert!(violations.is_empty(), "{violations:?}");
    let dir_only = initial_config_with(
        "mock",
        &Answered {
            output_dir: Some("ctxloom/".to_string()),
            premised: None,
        },
    );
    assert!(
        dir_only.contains("output:\n  dir: ctxloom/\n"),
        "{dir_only}"
    );
    assert!(!dir_only.contains("premised"), "{dir_only}");
}

// The nested `.gitignore`: the comment, then each private-state pattern on a
// line of its own, and nothing it lists is left to git.
#[test]
fn test_the_nested_gitignore_lists_every_private_state_pattern() {
    let text = nested_gitignore();
    assert!(text.starts_with("# ctxloom's private working state"));
    assert!(
        text.ends_with("/cache\n/sessions\n/project-id\n/state\n*.lock\n"),
        "{text}"
    );
    assert_eq!(
        PRIVATE_STATE_PATTERNS,
        ["/cache", "/sessions", "/project-id", "/state", "*.lock"]
    );
    assert!(unignored_private_state(None, Some(&text)).is_empty());
}

// hand-test 2 2026-11-11: the header named Go's `gitignore.PrivateStatePatterns`
// and told the reader to change it; the list is ctxloom's own.
#[test]
fn test_the_nested_gitignore_names_no_go_source() {
    let text = nested_gitignore();
    assert!(!text.contains("PrivateStatePatterns"), "{text}");
    assert!(!text.contains("Change that list"), "{text}");
    assert!(
        text.contains("ctxloom REWRITES THIS FILE WHOLESALE"),
        "{text}"
    );
}

// Hand-test 2026-11-10: with no ignore file every pattern is git's to see.
#[test]
fn test_with_no_ignore_file_all_private_state_is_unignored() {
    assert_eq!(unignored_private_state(None, None), PRIVATE_STATE_PATTERNS);
    assert_eq!(
        unignored_private_state(Some("# /cache\ntarget/\n"), Some("\n# /sessions\n")),
        PRIVATE_STATE_PATTERNS,
        "comments and blank lines ignore nothing"
    );
}

// The nested file's lines, with or without the leading `/` and a trailing `/`.
#[test]
fn test_the_nested_spellings_each_ignore_their_pattern() {
    let nested = "cache/\n/sessions/\nproject-id\n /state \n";
    assert_eq!(unignored_private_state(None, Some(nested)), ["*.lock"]);
}

// The root file's `.ctxloom/` spellings, anchored or not; an unanchored glob
// by itself; a bare name at the root is some other directory's.
#[test]
fn test_the_root_spellings_each_ignore_their_pattern() {
    let root =
        ".ctxloom/cache/\n/.ctxloom/sessions\n.ctxloom/project-id\n/.ctxloom/state/\n*.lock\n";
    assert!(unignored_private_state(Some(root), None).is_empty());
    assert_eq!(
        unignored_private_state(Some("cache/\nsessions\n.ctxloom/*.lock\n"), None),
        ["/cache", "/sessions", "/project-id", "/state"]
    );
    assert_eq!(
        unignored_private_state(Some("/cache\n"), Some("*.lock\n")),
        ["/cache", "/sessions", "/project-id", "/state"],
        "an anchored pattern is ignored at the root by its .ctxloom/ spelling only"
    );
}
