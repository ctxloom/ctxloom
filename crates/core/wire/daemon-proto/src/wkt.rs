//! The proto3 JSON of the well-known types the package uses, written by
//! hand (pbjson-build leaves them to the caller): a `Timestamp` is RFC 3339
//! in UTC, `Z` and a fraction of 0, 3, 6 or 9 digits (what
//! `session_cli::proto_json` wrote before, OQ-21); a `FieldMask` is its
//! paths in lowerCamelCase, joined by commas.

use serde::de;
use serde::{Deserialize, Deserializer, Serialize, Serializer};
use time::format_description::well_known::Rfc3339;
use time::OffsetDateTime;

use crate::google::protobuf::{FieldMask, Timestamp};

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

#[cfg(test)]
#[path = "wkt.test.rs"]
mod tests;
