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

/// A message's fields, each left out at its default.
#[derive(Default)]
struct Fields(Map<String, Value>);

impl Fields {
    fn text(mut self, name: &str, value: &str) -> Fields {
        if !value.is_empty() {
            self.0.insert(name.to_string(), Value::from(value));
        }
        self
    }

    fn flag(mut self, name: &str, value: bool) -> Fields {
        if value {
            self.0.insert(name.to_string(), Value::Bool(true));
        }
        self
    }

    fn u32(mut self, name: &str, value: u32) -> Fields {
        if value != 0 {
            self.0.insert(name.to_string(), Value::from(value));
        }
        self
    }

    /// A 64-bit integer, as its decimal string.
    fn u64(mut self, name: &str, value: u64) -> Fields {
        if value != 0 {
            self.0
                .insert(name.to_string(), Value::from(value.to_string()));
        }
        self
    }

    fn time(mut self, name: &str, value: Option<Timestamp>) -> Fields {
        if let Some(at) = value {
            self.0
                .insert(name.to_string(), Value::from(stamp::proto3(at)));
        }
        self
    }

    /// An enum's value, by its name (its number where it has none).
    fn enumeration(mut self, name: &str, number: i32, named: Option<&str>) -> Fields {
        if number != 0 {
            let value = named.map_or_else(|| Value::from(number), Value::from);
            self.0.insert(name.to_string(), value);
        }
        self
    }

    /// A message field: left out unset, written whole (even empty) when set.
    fn message(mut self, name: &str, value: Option<Value>) -> Fields {
        if let Some(value) = value {
            self.0.insert(name.to_string(), value);
        }
        self
    }

    fn list(mut self, name: &str, values: Vec<Value>) -> Fields {
        if !values.is_empty() {
            self.0.insert(name.to_string(), Value::Array(values));
        }
        self
    }

    fn done(self) -> Value {
        Value::Object(self.0)
    }
}

/// A session (`GetSession`'s answer, a listing's entry).
pub fn session(session: &v1::Session) -> Value {
    let state = v1::SessionState::try_from(session.state).ok();
    Fields::default()
        .text("name", &session.name)
        .text("uid", &session.uid)
        .enumeration("state", session.state, state.map(|s| s.as_str_name()))
        .text("engine", &session.engine)
        .text("summary", &session.summary)
        .time("start_time", session.start_time)
        .time("end_time", session.end_time)
        .flag("kept", session.kept)
        .text("owner", &session.owner)
        .flag("closed", session.closed)
        .list(
            "children",
            session
                .children
                .iter()
                .map(|c| Value::from(c.as_str()))
                .collect(),
        )
        .list("pending", session.pending.iter().map(pending).collect())
        .u32("incarnation", session.incarnation)
        .text("etag", &session.etag)
        .text("title", &session.title)
        .text("native_session", &session.native_session)
        .text("project_root", &session.project_root)
        .text("output_dir", &session.output_dir)
        .list(
            "rotations",
            session.rotations.iter().map(rotation).collect(),
        )
        .done()
}

fn rotation(rotation: &v1::SessionRotation) -> Value {
    Fields::default()
        .text("native_id", &rotation.native_id)
        .text("transcript_path", &rotation.transcript_path)
        .time("rotated_at", rotation.rotated_at)
        .done()
}

fn pending(message: &v1::PendingMessage) -> Value {
    Fields::default()
        .text("message_id", &message.message_id)
        .text("urgency", &message.urgency)
        .text("text", &message.text)
        .text("sender", &message.sender)
        .done()
}

/// A listing's or a search's answer.
pub fn sessions(listed: &v1::ListSessionsResponse) -> Value {
    Fields::default()
        .list("sessions", listed.sessions.iter().map(session).collect())
        .text("next_page_token", &listed.next_page_token)
        .done()
}

/// The daemon (`GetDaemon`'s and `StopDaemon`'s answer).
pub fn daemon(daemon: &v1::Daemon) -> Value {
    Fields::default()
        .text("name", &daemon.name)
        .u32("pid", daemon.pid)
        .text("endpoint", &daemon.endpoint)
        .time("start_time", daemon.start_time)
        .text("version", &daemon.version)
        .list("runs", daemon.runs.iter().map(hosted).collect())
        .u32("clients", daemon.clients)
        .u64("idle_seconds", daemon.idle_seconds)
        .u64("idle_limit_seconds", daemon.idle_limit_seconds)
        .done()
}

fn hosted(run: &v1::HostedRun) -> Value {
    let kind = v1::RunKind::try_from(run.kind).ok();
    Fields::default()
        .text("session", &run.session)
        .text("harp", &run.harp)
        .text("run", &run.run)
        .enumeration("kind", run.kind, kind.map(|k| k.as_str_name()))
        .done()
}

/// A removal (`DeleteSession`'s answer, and the plan a refusal carries).
pub fn deletion(removal: &v1::DeleteSessionResponse) -> Value {
    Fields::default()
        .text("name", &removal.name)
        .list(
            "authored",
            removal
                .authored
                .iter()
                .map(|rel| Value::from(rel.as_str()))
                .collect(),
        )
        .text("harp", &removal.harp)
        .flag("applied", removal.applied)
        .list("files", removal.files.iter().map(removal_file).collect())
        .u64("bytes_freed", removal.bytes_freed)
        .u64("events_destroyed", removal.events_destroyed)
        .message("worktrees", removal.worktrees.as_ref().map(worktrees))
        .text("etag", &removal.etag)
        .done()
}

fn removal_file(file: &v1::RemovalFile) -> Value {
    let class = v1::FileClass::try_from(file.class).ok();
    Fields::default()
        .text("path", &file.path)
        .text("rel", &file.rel)
        .enumeration("class", file.class, class.map(|c| c.as_str_name()))
        .u64("bytes", file.bytes)
        .done()
}

fn worktrees(report: &v1::WorktreeReport) -> Value {
    Fields::default()
        .list("worktrees", report.worktrees.iter().map(worktree).collect())
        .u64("reaped", report.reaped)
        .u64("spared", report.spared)
        .u64("skipped", report.skipped)
        .flag("applied", report.applied)
        .done()
}

fn worktree(row: &v1::ScratchWorktree) -> Value {
    Fields::default()
        .text("harp", &row.harp)
        .text("worktree", &row.worktree)
        .text("path", &row.path)
        .text("owner_state", &row.owner_state)
        .text("verdict", &row.verdict)
        .text("reason", &row.reason)
        .done()
}

/// A sweep (`SweepSessions`' answer).
pub fn sweep(report: &v1::SweepSessionsResponse) -> Value {
    Fields::default()
        .list("sessions", report.sessions.iter().map(deletion).collect())
        .list("rows", report.rows.iter().map(sweep_row).collect())
        .text("purge_cutoff", &report.purge_cutoff)
        .message("counts", report.counts.as_ref().map(counts))
        .text("etag", &report.etag)
        .done()
}

fn sweep_row(row: &v1::SweepRow) -> Value {
    Fields::default()
        .text("harp", &row.harp)
        .text("action", &row.action)
        .text("verdict", &row.verdict)
        .text("reason", &row.reason)
        .text("command", &row.command)
        .u64("bytes", row.bytes)
        .list(
            "authored",
            row.authored
                .iter()
                .map(|rel| Value::from(rel.as_str()))
                .collect(),
        )
        .done()
}

fn counts(counts: &v1::SweepCounts) -> Value {
    Fields::default()
        .u64("done", counts.done)
        .u64("planned", counts.planned)
        .u64("held", counts.held)
        .u64("left", counts.left)
        .u64("skipped", counts.skipped)
        .u64("failed", counts.failed)
        .done()
}

#[cfg(test)]
#[path = "proto_json.test.rs"]
mod tests;
