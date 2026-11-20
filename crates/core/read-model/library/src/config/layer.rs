//! One config file as a layer, and layers merged.

use angzarr_yaml::Value;

use crate::config::{CURRENT, OLDEST};
use crate::error::ConfigError;

/// What a layer said about its own generation, and its contents.
#[derive(Clone, PartialEq, Eq, Debug)]
pub enum Note {
    /// Written by a newer format than this build reads.
    Newer { found: u64, current: u64 },
    /// Older than the oldest this build migrates (an unstamped file is 0).
    Older { found: u64, oldest: u64 },
    /// A `schema_version` that is not an integer.
    NotInteger,
    /// A file with no configuration keys at all (empty, or all comments).
    Keyless,
    /// Companions registered by bare name, as generation 8 wrote them: a
    /// registration is a name and the command that runs it from generation 9,
    /// and a name alone is not migrated into one.
    LegacyCompanions { found: u64, names: Vec<String> },
}

/// A layer, read.
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct Read {
    /// The mapping: migrated in memory when its generation is one this build
    /// migrates, as written otherwise.
    pub value: Value,
    pub notes: Vec<Note>,
}

/// Read a layer's text. A generation this build cannot migrate is a note, not
/// a refusal: the layer is read as written. Text that is not YAML the reader
/// takes is a refusal.
pub fn read(text: &str) -> Result<Read, ConfigError> {
    let parsed = crate::yamlx::parse(text).map_err(|cause| ConfigError::Syntax { cause })?;
    let mut value = match parsed {
        Value::Null => Value::Map(Vec::new()),
        Value::Map(_) => parsed,
        _ => return Err(ConfigError::NotAMapping),
    };
    let mut notes = Vec::new();
    if value.as_map().is_some_and(<[_]>::is_empty) {
        notes.push(Note::Keyless);
        return Ok(Read { value, notes });
    }
    match generation(&value) {
        Err(()) => notes.push(Note::NotInteger),
        Ok(found) if found > u64::from(CURRENT) => notes.push(Note::Newer {
            found,
            current: u64::from(CURRENT),
        }),
        Ok(found) if found < u64::from(OLDEST) => notes.push(Note::Older {
            found,
            oldest: u64::from(OLDEST),
        }),
        Ok(found) => match bare_companions(&value, found) {
            Some(names) => notes.push(Note::LegacyCompanions { found, names }),
            None => migrate(&mut value, found),
        },
    }
    Ok(Read { value, notes })
}

/// The names a generation before 9 registered companions by, when it has any.
fn bare_companions(value: &Value, found: u64) -> Option<Vec<String>> {
    if found >= 9 {
        return None;
    }
    let names: Vec<String> = value
        .get("companions")?
        .as_seq()?
        .iter()
        .filter_map(|n| n.as_str().map(ToString::to_string))
        .collect();
    (!names.is_empty()).then_some(names)
}

/// The generation a layer declares: none is 0.
fn generation(value: &Value) -> Result<u64, ()> {
    match value.get("schema_version") {
        None => Ok(0),
        Some(Value::Scalar(s)) => s.trim().parse().map_err(|_| ()),
        Some(_) => Err(()),
    }
}

/// The in-memory migrations from `found` up to [`CURRENT`], each step by the
/// generation it reaches; the stamp follows.
fn migrate(value: &mut Value, found: u64) {
    if found >= u64::from(CURRENT) {
        return;
    }
    // 7 to 8: `config.essence_max_chars` is retired; a section the key alone
    // filled leaves with it. A `config` that is not a mapping is left for the
    // strict decode to refuse.
    if found < 8 {
        if let Some(Value::Map(entries)) = value.get_mut("config") {
            entries.retain(|(k, _)| k != "essence_max_chars");
            if entries.is_empty() {
                value.remove("config");
            }
        }
    }
    // 8 to 9: companions are registered with their command. A list with names
    // in it was refused before this; an empty one registers nothing.
    if value
        .get("companions")
        .is_some_and(|c| !matches!(c, Value::Map(_)))
    {
        value.remove("companions");
    }
    *value.entry("schema_version") = Value::Scalar(CURRENT.to_string());
}

/// The layers merged, lowest first. Mappings merge key by key; anything else
/// the higher layer replaces; an agent binding replaces the lower layer's of
/// the same name wholesale; `companions` names union (a project cannot
/// unregister a home companion), a name's command the highest layer's that
/// registers it (both are what the mappings' key-by-key merge gives, and a
/// list replaces).
pub fn merge_layers(layers: &[Value]) -> Value {
    let mut merged = Value::Map(Vec::new());
    for layer in layers {
        merge_one(&mut merged, layer);
    }
    merged
}

fn merge_one(into: &mut Value, layer: &Value) {
    let Value::Map(entries) = layer else { return };
    for (key, value) in entries {
        match key.as_str() {
            "agents" => {
                if let Value::Map(agents) = value {
                    let mine = into.entry("agents");
                    if !matches!(mine, Value::Map(_)) {
                        *mine = Value::Map(Vec::new());
                    }
                    for (name, binding) in agents {
                        *mine.entry(name) = binding.clone();
                    }
                } else {
                    *into.entry(key) = value.clone();
                }
            }
            _ if matches!(value, Value::Null) => {}
            _ => {
                let mine = into.entry(key);
                if matches!(mine, Value::Null) {
                    *mine = value.clone();
                } else {
                    super::deep_merge(mine, value);
                }
            }
        }
    }
}

#[cfg(test)]
#[path = "layer.test.rs"]
mod tests;
