//! The proto3 JSON of the well-known types the package uses, written by
//! hand (pbjson-build leaves them to the caller): a `Timestamp` is RFC 3339
//! in UTC, `Z` and a fraction of 0, 3, 6 or 9 digits (what
//! `session_cli::proto_json` wrote before, OQ-21); a `Struct` is the object
//! itself, its keys in order (the map is a `BTreeMap`); a `Value` is the
//! JSON value it holds, a whole number without a fraction; a `FieldMask` is
//! its paths in lowerCamelCase, joined by commas.

use std::collections::BTreeMap;
use std::fmt;

use serde::de::{self, MapAccess, SeqAccess, Visitor};
use serde::ser::{SerializeMap, SerializeSeq};
use serde::{Deserialize, Deserializer, Serialize, Serializer};
use time::format_description::well_known::Rfc3339;
use time::OffsetDateTime;

use crate::google::protobuf::{
    value::Kind, FieldMask, ListValue, NullValue, Struct, Timestamp, Value,
};

impl From<prost_types::Timestamp> for Timestamp {
    fn from(at: prost_types::Timestamp) -> Timestamp {
        Timestamp {
            seconds: at.seconds,
            nanos: at.nanos,
        }
    }
}

impl From<Timestamp> for prost_types::Timestamp {
    fn from(at: Timestamp) -> prost_types::Timestamp {
        prost_types::Timestamp {
            seconds: at.seconds,
            nanos: at.nanos,
        }
    }
}

/// `at` as proto3 JSON writes it: `2026-07-17T17:27:32Z`, `...32.481Z`.
/// An instant no calendar holds is written as the epoch.
pub(crate) fn rfc3339(at: &Timestamp) -> String {
    let whole =
        OffsetDateTime::from_unix_timestamp(at.seconds).unwrap_or(OffsetDateTime::UNIX_EPOCH);
    let date = whole.date();
    let clock = whole.time();
    let nanos = u32::try_from(at.nanos).unwrap_or(0);
    let fraction = match nanos {
        0 => String::new(),
        n if n % 1_000_000 == 0 => format!(".{:03}", n / 1_000_000),
        n if n % 1_000 == 0 => format!(".{:06}", n / 1_000),
        n => format!(".{n:09}"),
    };
    format!(
        "{:04}-{:02}-{:02}T{:02}:{:02}:{:02}{fraction}Z",
        date.year(),
        u8::from(date.month()),
        date.day(),
        clock.hour(),
        clock.minute(),
        clock.second(),
    )
}

impl Serialize for Timestamp {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        serializer.serialize_str(&rfc3339(self))
    }
}

impl<'de> Deserialize<'de> for Timestamp {
    fn deserialize<D: Deserializer<'de>>(deserializer: D) -> Result<Timestamp, D::Error> {
        let text = String::deserialize(deserializer)?;
        let at = OffsetDateTime::parse(&text, &Rfc3339).map_err(de::Error::custom)?;
        Ok(Timestamp {
            seconds: at.unix_timestamp(),
            nanos: i32::try_from(at.nanosecond()).map_err(de::Error::custom)?,
        })
    }
}

/// `snake_case` as `snakeCase`.
fn camel(path: &str) -> String {
    let mut out = String::with_capacity(path.len());
    let mut upper = false;
    for c in path.chars() {
        if c == '_' {
            upper = true;
        } else if upper {
            out.extend(c.to_uppercase());
            upper = false;
        } else {
            out.push(c);
        }
    }
    out
}

/// `snakeCase` as `snake_case`.
fn snake(path: &str) -> String {
    let mut out = String::with_capacity(path.len() + 4);
    for c in path.chars() {
        if c.is_ascii_uppercase() {
            out.push('_');
            out.push(c.to_ascii_lowercase());
        } else {
            out.push(c);
        }
    }
    out
}

impl Serialize for FieldMask {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        let paths: Vec<String> = self.paths.iter().map(|p| camel(p)).collect();
        serializer.serialize_str(&paths.join(","))
    }
}

impl<'de> Deserialize<'de> for FieldMask {
    fn deserialize<D: Deserializer<'de>>(deserializer: D) -> Result<FieldMask, D::Error> {
        let text = String::deserialize(deserializer)?;
        Ok(FieldMask {
            paths: text
                .split(',')
                .filter(|p| !p.is_empty())
                .map(snake)
                .collect(),
        })
    }
}

impl Serialize for Struct {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        let mut map = serializer.serialize_map(Some(self.fields.len()))?;
        for (key, value) in &self.fields {
            map.serialize_entry(key, value)?;
        }
        map.end()
    }
}

impl Serialize for ListValue {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        let mut seq = serializer.serialize_seq(Some(self.values.len()))?;
        for value in &self.values {
            seq.serialize_element(value)?;
        }
        seq.end()
    }
}

/// The largest whole number a double holds exactly (2^53).
const EXACT: f64 = 9_007_199_254_740_992.0;

impl Serialize for Value {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        match &self.kind {
            None | Some(Kind::NullValue(_)) => serializer.serialize_unit(),
            Some(Kind::NumberValue(n)) if n.is_nan() => serializer.serialize_str("NaN"),
            Some(Kind::NumberValue(n)) if n.is_infinite() && *n > 0.0 => {
                serializer.serialize_str("Infinity")
            }
            Some(Kind::NumberValue(n)) if n.is_infinite() => serializer.serialize_str("-Infinity"),
            // A whole number is written without a fraction, as proto3 JSON
            // writes one (`1`, not `1.0`).
            #[allow(clippy::cast_possible_truncation, clippy::float_cmp)]
            Some(Kind::NumberValue(n)) if n.fract() == 0.0 && n.abs() <= EXACT => {
                serializer.serialize_i64(*n as i64)
            }
            Some(Kind::NumberValue(n)) => serializer.serialize_f64(*n),
            Some(Kind::StringValue(s)) => serializer.serialize_str(s),
            Some(Kind::BoolValue(b)) => serializer.serialize_bool(*b),
            Some(Kind::StructValue(s)) => s.serialize(serializer),
            Some(Kind::ListValue(l)) => l.serialize(serializer),
        }
    }
}

/// Any JSON value, as a `Value`.
struct ValueVisitor;

impl<'de> Visitor<'de> for ValueVisitor {
    type Value = Value;

    fn expecting(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str("a JSON value")
    }

    fn visit_unit<E: de::Error>(self) -> Result<Value, E> {
        Ok(kind(Kind::NullValue(NullValue::NullValue.into())))
    }

    fn visit_none<E: de::Error>(self) -> Result<Value, E> {
        self.visit_unit()
    }

    fn visit_bool<E: de::Error>(self, b: bool) -> Result<Value, E> {
        Ok(kind(Kind::BoolValue(b)))
    }

    #[allow(clippy::cast_precision_loss)]
    fn visit_i64<E: de::Error>(self, n: i64) -> Result<Value, E> {
        Ok(kind(Kind::NumberValue(n as f64)))
    }

    #[allow(clippy::cast_precision_loss)]
    fn visit_u64<E: de::Error>(self, n: u64) -> Result<Value, E> {
        Ok(kind(Kind::NumberValue(n as f64)))
    }

    fn visit_f64<E: de::Error>(self, n: f64) -> Result<Value, E> {
        Ok(kind(Kind::NumberValue(n)))
    }

    fn visit_str<E: de::Error>(self, s: &str) -> Result<Value, E> {
        Ok(kind(Kind::StringValue(s.to_string())))
    }

    fn visit_seq<A: SeqAccess<'de>>(self, mut seq: A) -> Result<Value, A::Error> {
        let mut values = Vec::new();
        while let Some(value) = seq.next_element::<Value>()? {
            values.push(value);
        }
        Ok(kind(Kind::ListValue(ListValue { values })))
    }

    fn visit_map<A: MapAccess<'de>>(self, mut map: A) -> Result<Value, A::Error> {
        let mut fields = BTreeMap::new();
        while let Some((key, value)) = map.next_entry::<String, Value>()? {
            fields.insert(key, value);
        }
        Ok(kind(Kind::StructValue(Struct { fields })))
    }
}

fn kind(kind: Kind) -> Value {
    Value { kind: Some(kind) }
}

impl<'de> Deserialize<'de> for Value {
    fn deserialize<D: Deserializer<'de>>(deserializer: D) -> Result<Value, D::Error> {
        deserializer.deserialize_any(ValueVisitor)
    }
}

impl<'de> Deserialize<'de> for Struct {
    fn deserialize<D: Deserializer<'de>>(deserializer: D) -> Result<Struct, D::Error> {
        match Value::deserialize(deserializer)?.kind {
            Some(Kind::StructValue(s)) => Ok(s),
            _ => Err(de::Error::custom(
                "a google.protobuf.Struct is a JSON object",
            )),
        }
    }
}

impl<'de> Deserialize<'de> for ListValue {
    fn deserialize<D: Deserializer<'de>>(deserializer: D) -> Result<ListValue, D::Error> {
        match Value::deserialize(deserializer)?.kind {
            Some(Kind::ListValue(l)) => Ok(l),
            _ => Err(de::Error::custom(
                "a google.protobuf.ListValue is a JSON array",
            )),
        }
    }
}

#[cfg(test)]
#[path = "wkt.test.rs"]
mod tests;
