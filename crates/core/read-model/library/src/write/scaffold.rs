//! What `config create` leaves in a new project (Go's
//! `operations.InitializeProject`, `BuildInitialConfig`): the config, the
//! remotes registry and the seeded default profile. The two texts were Go's
//! `resources/` at release/0.7 ca14d6046; they diverge from Go by owner
//! rulings 110 and 111: the registry seeds no remote (Go's seeded
//! `ctxloom-default`), and the profile's parent is the built-in
//! `ai-developer#profiles/developer` ([`crate::builtin`]). And the nested
//! `.ctxloom/.gitignore` of ctxloom's private state, which `manage gitignore
//! install` writes too, and which doctor checks for.

use angzarr_yaml::Value;

use super::section::{render_sorted, sorted};

/// `remotes.yaml` as a new project starts with it.
pub const DEFAULT_REMOTES: &str = include_str!("../../resources/default-remotes.yaml");

/// The `default` profile a new project is seeded with.
pub const SEED_PROFILE: &str = include_str!("../../resources/seed-profile.yaml");

/// The name of the seeded profile, and of the agent bound to it.
pub const SEED_NAME: &str = "default";

/// The comment at the head of `.ctxloom/.gitignore` (Go's
/// `gitignore.NestedComment`).
const NESTED_GITIGNORE_COMMENT: &str = "# ctxloom's private working state: rebuildable or purely local, and so must\n# never ride a distributable tree. Everything else under .ctxloom/ - config.yaml,\n# remotes.yaml, lock.yaml, content/ - is committed by omission, because it is\n# content or config the project depends on.\n#\n# GENERATED from gitignore.PrivateStatePatterns, relativized to this directory.\n# ctxloom REWRITES THIS FILE WHOLESALE; hand edits are lost. Change that list.\n#\n# COMMIT THIS FILE. Tracking it is what carries the rules with the directory -\n# into every clone and, crucially, into every linked worktree, which is where a\n# rule living only in the superproject's root .gitignore would not reach.";

/// Go's `gitignore.NestedPatterns()`: ctxloom's private state, relative to
/// `.ctxloom/`.
pub const PRIVATE_STATE_PATTERNS: [&str; 5] =
    ["/cache", "/sessions", "/project-id", "/state", "*.lock"];

/// `.ctxloom/.gitignore` as ctxloom writes it: the comment, then each of
/// [`PRIVATE_STATE_PATTERNS`] on a line.
pub fn nested_gitignore() -> String {
    let mut out = String::from(NESTED_GITIGNORE_COMMENT);
    out.push('\n');
    for pattern in PRIVATE_STATE_PATTERNS {
        out.push_str(pattern);
        out.push('\n');
    }
    out
}

/// The patterns of [`PRIVATE_STATE_PATTERNS`] that neither the nested
/// `.ctxloom/.gitignore` (`nested`) nor the project's root `.gitignore`
/// (`root`) ignores, in that list's order. A pattern is ignored by its own
/// line in the nested file (with or without its leading `/`, and a trailing
/// `/`), or by its `.ctxloom/` spelling in the root one; an unanchored glob
/// (`*.lock`) is ignored by itself in either.
pub fn unignored_private_state(root: Option<&str>, nested: Option<&str>) -> Vec<&'static str> {
    // A blank line or a comment equals no pattern, so it ignores nothing
    // without being filtered out.
    let lines = |text: Option<&str>| -> Vec<String> {
        text.unwrap_or_default()
            .lines()
            .map(|line| line.trim().trim_end_matches('/').to_string())
            .collect()
    };
    let (root, nested) = (lines(root), lines(nested));
    PRIVATE_STATE_PATTERNS
        .into_iter()
        .filter(|pattern| {
            let bare = pattern.trim_start_matches('/');
            let in_nested = nested.iter().any(|l| l.trim_start_matches('/') == bare);
            let in_root = root.iter().any(|l| {
                l.trim_start_matches('/').strip_prefix(".ctxloom/") == Some(bare)
                    || (!pattern.starts_with('/') && l == pattern)
            });
            !in_nested && !in_root
        })
        .collect()
}

fn text(value: &str) -> Value {
    Value::Scalar(value.to_string())
}

fn map(entries: Vec<(&str, Value)>) -> Value {
    Value::Map(
        entries
            .into_iter()
            .map(|(key, value)| (key.to_string(), value))
            .collect(),
    )
}

/// The config a new project starts as, as a value: keys sorted. The default
/// agent names its engine and the seeded profile; the engine block is the
/// user's to write (an engine on its built-in settings needs none).
pub fn initial_sections(engine: &str) -> Value {
    sorted(&map(vec![
        (
            "agents",
            map(vec![(
                SEED_NAME,
                map(vec![
                    ("engine", text(engine)),
                    ("profiles", Value::Seq(vec![text(SEED_NAME)])),
                    ("runtime", text("host")),
                ]),
            )]),
        ),
        ("default_agent", text(SEED_NAME)),
        ("schema_version", text(&crate::config::CURRENT.to_string())),
    ]))
}

/// The config a new project starts as, for `engine` (a name the caller has
/// checked): the default agent bound to that engine and the seeded profile. Keys sorted, as a fresh document is.
pub fn initial_config(engine: &str) -> String {
    render_sorted(&initial_sections(engine))
}

#[cfg(test)]
#[path = "scaffold.test.rs"]
mod tests;
