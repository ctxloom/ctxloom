//! What `config create` leaves in a new project (Go's
//! `operations.InitializeProject`, `BuildInitialConfig`): the config, the
//! default remotes registry and the seeded default profile. The two texts are
//! Go's `resources/` at release/0.7 ca14d6046, byte for byte.

use angzarr_yaml::Value;

use super::section::{render_sorted, sorted};

/// `remotes.yaml` as a new project starts with it.
pub const DEFAULT_REMOTES: &str = include_str!("../../resources/default-remotes.yaml");

/// The `default` profile a new project is seeded with.
pub const SEED_PROFILE: &str = include_str!("../../resources/seed-profile.yaml");

/// The name of the seeded profile, and of the agent bound to it.
pub const SEED_NAME: &str = "default";

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
        ("config", map(vec![("use_distilled", text("true"))])),
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
