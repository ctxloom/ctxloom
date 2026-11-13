//! A daemon's answer as `--format json` prints it (ruling 146): the
//! message's standard proto3 JSON, field names as in the proto (snake_case,
//! `preserving_proto_field_names`). As proto3 JSON writes them: a field at
//! its default (empty text, zero, false, an empty list, an unset message) is
//! left out; an enum is its value's name; a 64-bit integer is a decimal
//! string; a `google.protobuf.Timestamp` is RFC 3339 in UTC (`Z`, a fraction
//! of 0, 3, 6 or 9 digits).

use daemon_proto::v1;
use serde_json::{Map, Value};
use sessions_application::{stamp, Timestamp};

/// A session (`GetSession`'s answer, a listing's entry).
pub fn session(session: &v1::Session) -> Value {
    let _ = (
        session,
        Map::<String, Value>::new(),
        stamp::proto3,
        Timestamp::default(),
    );
    todo!("RED: slice 2 step 6")
}

/// A listing's or a search's answer.
pub fn sessions(listed: &v1::ListSessionsResponse) -> Value {
    let _ = listed;
    todo!("RED: slice 2 step 6")
}

/// The daemon (`GetDaemon`'s and `StopDaemon`'s answer).
pub fn daemon(daemon: &v1::Daemon) -> Value {
    let _ = daemon;
    todo!("RED: slice 2 step 6")
}

#[cfg(test)]
#[path = "proto_json.test.rs"]
mod tests;
