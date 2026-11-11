use super::*;

// Owner ruling 111: the registry a new project starts with seeds no remote;
// it was Go's resources/default-remotes.yaml at ca14d6046, which seeded
// `ctxloom-default`.
#[test]
fn test_the_default_remotes_seed_no_remote() {
    assert_eq!(
        DEFAULT_REMOTES,
        "# ctxloom Remote Sources\n# The repositories this project draws bundles and profiles from, under\n# `remotes:` (`ctxloom remote create <name> <url>` adds one).\n#\n# Registering a remote is the trust act: its content reaches the assistant\n# because the project added it. ctxloom's default content is built in and\n# needs no remote.\n\nschema_version: 1\n"
    );
    assert!(!DEFAULT_REMOTES.contains("ctxloom-default"));
}

// Owner rulings 110, 111: the seeded profile (Go's resources/profiles/default.yaml
// at ca14d6046) inherits the built-in developer profile, not the remote's.
#[test]
fn test_the_seed_profile_inherits_the_built_in_developer_profile() {
    let seeded = crate::profile::Profile::decode(SEED_PROFILE).expect("it decodes");
    assert_eq!(seeded.parents, ["ai-developer#profiles/developer"]);
    assert!(!SEED_PROFILE.contains("ctxloom-default"));
    assert!(!SEED_PROFILE.contains("ctxloom+git"));
    assert!(crate::builtin::profiles(&|_| false)
        .contains_key(&crate::profile::canonical_key(&seeded.parents[0]).expect("a key")));
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
