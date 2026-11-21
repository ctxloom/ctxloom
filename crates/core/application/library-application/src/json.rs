//! Values of the configuration as JSON and on one line in YAML's flow form.

use angzarr_yaml::Value;
use serde_json::{Map, Number};

/// A scalar as the value its text reads as.
fn scalar(text: &str) -> serde_json::Value {
    if text == "true" {
        return serde_json::Value::Bool(true);
    }
    if text == "false" {
        return serde_json::Value::Bool(false);
    }
    match text.parse::<i64>() {
        Ok(n) => serde_json::Value::Number(Number::from(n)),
        Err(_) => serde_json::Value::String(text.to_string()),
    }
}

fn convert(value: &Value) -> serde_json::Value {
    match value {
        Value::Null => serde_json::Value::Null,
        Value::Scalar(text) => scalar(text),
        Value::Seq(items) => serde_json::Value::Array(items.iter().map(convert).collect()),
        // Sorted by key, whatever serde_json's map keeps (its
        // `preserve_order`, which agent-client-protocol turns on, would keep
        // the document's order).
        Value::Map(entries) => serde_json::Value::Object(
            entries
                .iter()
                .map(|(key, inner)| (key.clone(), convert(inner)))
                .collect::<std::collections::BTreeMap<_, _>>()
                .into_iter()
                .collect(),
        ),
    }
}

/// A value of the configuration as JSON, whatever its shape: an engine's
/// permissions document as written (owner rulings 135, 137). A plain
/// scalar reads as what its text reads as (`true`, a whole number), the YAML
/// reader keeping no quotes.
pub fn document(value: &Value) -> serde_json::Value {
    convert(value)
}

/// A value of the configuration as JSON TEXT on one line, every mapping's keys
/// in the order they were written (owner rulings 135, 137: what an engine is
/// handed is a verbatim copy, and an engine may read its rules in order). A
/// scalar reads as [`document`] reads it.
pub fn document_text(value: &Value) -> String {
    let quoted = |text: &str| serde_json::to_string(text).unwrap_or_else(|_| format!("{:?}", text));
    match value {
        Value::Map(entries) => {
            let inner: Vec<String> = entries
                .iter()
                .map(|(key, inner)| format!("{}:{}", quoted(key), document_text(inner)))
                .collect();
            format!("{{{}}}", inner.join(","))
        }
        Value::Seq(items) => format!(
            "[{}]",
            items
                .iter()
                .map(document_text)
                .collect::<Vec<_>>()
                .join(",")
        ),
        other => convert(other).to_string(),
    }
}

/// A block of the configuration (an agent's `engines`) as JSON: an object,
/// `{}` for none.
pub fn permissions(block: &Value) -> serde_json::Value {
    match convert(block) {
        object @ serde_json::Value::Object(_) => object,
        _ => serde_json::Value::Object(Map::new()),
    }
}

/// A value on one line in YAML's flow form (`{k: v, ...}`, `[a, b]`), keys
/// sorted in every map, as the config writer writes them.
pub fn flow(value: &Value) -> String {
    match value {
        Value::Null => "null".to_string(),
        Value::Scalar(text) => text.clone(),
        Value::Seq(items) => format!(
            "[{}]",
            items.iter().map(flow).collect::<Vec<_>>().join(", ")
        ),
        Value::Map(entries) => {
            let mut sorted: Vec<&(String, Value)> = entries.iter().collect();
            sorted.sort_by(|a, b| a.0.cmp(&b.0));
            let inner: Vec<String> = sorted
                .iter()
                .map(|(k, v)| format!("{k}: {}", flow(v)))
                .collect();
            format!("{{{}}}", inner.join(", "))
        }
    }
}

#[cfg(test)]
#[path = "json.test.rs"]
mod tests;
