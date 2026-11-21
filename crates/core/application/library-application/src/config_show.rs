//! `config show [section] [--raw]`.

use library::config::schema::SECTIONS;
use library::config::typed::{to_yaml, typed, Order, Typed};

use crate::error::AppError;
use crate::project::{Library, Outcome, Project};
use crate::Flags;

/// A configuration, or a section of it: as JSON (keys sorted) and as the
/// YAML text (sorted for the whole document, in the order of its record's
/// fields for a section).
pub struct ConfigShown {
    pub json: serde_json::Value,
    pub text: String,
}

impl Library {
    /// `config show [section] [--raw]`.
    pub fn config_show(
        &self,
        flags: &Flags,
        section: Option<&str>,
        raw: bool,
    ) -> Outcome<ConfigShown> {
        self.run(flags, |p| p.config_show(section, raw))
    }

    /// `config show <section>` names every section when it is not one.
    pub fn config_sections() -> &'static [&'static str] {
        &SECTIONS
    }
}

impl Project {
    fn config_show(&self, section: Option<&str>, raw: bool) -> Result<ConfigShown, AppError> {
        let value = if raw {
            &self.config.raw
        } else {
            &self.config.value
        };
        let Some(section) = section else {
            let sorted = typed(value, &[], Order::Sorted);
            return Ok(ConfigShown {
                json: json(&sorted),
                text: to_yaml(&sorted),
            });
        };
        if !SECTIONS.contains(&section) {
            return Err(AppError::UnknownSection {
                section: section.to_string(),
                available: SECTIONS.join(", "),
            });
        }
        let path = [section.to_string()];
        let present = value.get(section).is_some();
        let (json_form, text_form) = if present {
            (
                typed(value, &path, Order::Sorted),
                typed(value, &path, Order::Fields),
            )
        } else {
            (empty_section(section), empty_section(section))
        };
        Ok(ConfigShown {
            json: json(&json_form),
            text: to_yaml(&text_form),
        })
    }
}

/// A configuration document (or a section of it) as `config show` prints
/// it from its JSON: YAML, every mapping's keys sorted (step 10a U2: the
/// text is drawn from the read's message, which holds no written order;
/// owner ruling 2026-11-17, text prints canonical forms).
pub fn config_yaml(json: &serde_json::Value) -> String {
    to_yaml(&typed_of(json))
}

/// A JSON value as the typed value it prints as, keys sorted.
fn typed_of(json: &serde_json::Value) -> Typed {
    match json {
        serde_json::Value::Null => Typed::Null,
        serde_json::Value::Bool(b) => Typed::Bool(*b),
        serde_json::Value::Number(n) => match n.as_i64() {
            Some(whole) => Typed::Int(whole),
            None => match n.as_f64() {
                Some(x) if x.fract() == 0.0 && x.abs() < 9.0e15 => Typed::Int(x as i64),
                Some(x) => Typed::Float(x),
                None => Typed::Str(n.to_string()),
            },
        },
        serde_json::Value::String(text) => Typed::Str(text.clone()),
        serde_json::Value::Array(items) => Typed::Seq(items.iter().map(typed_of).collect()),
        serde_json::Value::Object(fields) => {
            let mut entries: Vec<(String, Typed)> = fields
                .iter()
                .map(|(key, field)| (key.clone(), typed_of(field)))
                .collect();
            entries.sort_by(|a, b| a.0.cmp(&b.0));
            Typed::Map(entries)
        }
    }
}

/// What a section that is not set shows: its record's zero.
fn empty_section(section: &str) -> Typed {
    match section {
        "isolation_engines" | "companions" => Typed::Seq(Vec::new()),
        "editor" | "config" | "sync" | "agents" | "permissions" | "delegation"
        | "shell_timeout" | "isolation_images" | "ui" => Typed::Map(Vec::new()),
        _ => Typed::Str(String::new()),
    }
}

/// A typed value as JSON.
fn json(value: &Typed) -> serde_json::Value {
    match value {
        Typed::Null => serde_json::Value::Null,
        Typed::Bool(b) => serde_json::Value::Bool(*b),
        Typed::Int(n) => serde_json::Value::from(*n),
        Typed::Float(x) => serde_json::Number::from_f64(*x)
            .map_or(serde_json::Value::Null, serde_json::Value::Number),
        Typed::Str(s) => serde_json::Value::String(s.clone()),
        Typed::Seq(items) => serde_json::Value::Array(items.iter().map(json).collect()),
        // Sorted by key, whatever serde_json's map keeps (its
        // `preserve_order` would keep the document's order).
        Typed::Map(entries) => serde_json::Value::Object(
            entries
                .iter()
                .map(|(k, v)| (k.clone(), json(v)))
                .collect::<std::collections::BTreeMap<_, _>>()
                .into_iter()
                .collect(),
        ),
    }
}
