//! A log entry in plain words (the screen's log pane, and its text seam): who
//! said what, or what the daemon did. Display only: the entry is the data,
//! and nothing of its JSON, its engine data or its session shows here.

use daemon_proto::v1::{
    self, log_ask, log_compaction, log_entry::Entry as Arm, log_lifecycle, log_tool,
};

/// What a log is the log of, as an entry's text needs it: the session's
/// project root (a path under it is said relative to it) and its engine (the
/// key its engine data is kept under, `<engine>.path`).
#[derive(Clone, Copy, Default)]
pub(crate) struct Of<'a> {
    pub project_root: &'a str,
    pub engine: &'a str,
}

/// What `entry` says, as one line of plain text.
pub(crate) fn text_of(entry: &v1::LogEntry, of: &Of<'_>) -> String {
    match &entry.entry {
        Some(Arm::Prompt(prompt)) => format!("user: {}", prompt.text),
        Some(Arm::Words(words)) => format!("assistant: {}", words.text),
        Some(Arm::Tool(tool)) => tool_text(tool, &entry.extra, of),
        Some(Arm::TurnEnd(_)) => "turn ended".to_string(),
        Some(Arm::Compaction(compaction)) => {
            let phase = match log_compaction::Phase::try_from(compaction.phase) {
                Ok(log_compaction::Phase::After) => "after",
                _ => "before",
            };
            format!("compacted ({phase}, {})", compaction.trigger)
        }
        Some(Arm::Clear(_)) => "conversation cleared".to_string(),
        Some(Arm::Gap(gap)) => format!("gap: {} not recorded ({})", gap.dropped, gap.why),
        Some(Arm::Lifecycle(lifecycle)) => lifecycle_text(lifecycle),
        Some(Arm::Ask(ask)) => ask_text(ask),
        None => "(an entry this ctxloom cannot read)".to_string(),
    }
}

fn tool_text(
    tool: &v1::LogTool,
    extra: &std::collections::BTreeMap<String, String>,
    of: &Of<'_>,
) -> String {
    let how = match tool.status.map(log_tool::Status::try_from) {
        Some(Ok(log_tool::Status::Error)) => ", error",
        _ => "",
    };
    // The file the call touched, which its engine's adapter attached
    // (`<engine>.path`); no other key is read.
    let path = extra
        .get(&format!("{}.path", of.engine))
        .map(|path| format!(", {}", shown_path(path, of.project_root)))
        .unwrap_or_default();
    format!(
        "tool {}{path}, input {}, result {}{how}",
        tool.name,
        size_text(tool.input_bytes),
        size_text(tool.response_bytes),
    )
}

/// `path` relative to the project root when it is under it, else as given.
fn shown_path<'a>(path: &'a str, root: &str) -> &'a str {
    let root = root.trim_end_matches('/');
    match path
        .strip_prefix(root)
        .and_then(|rest| rest.strip_prefix('/'))
    {
        Some(under) if !root.is_empty() && !under.is_empty() => under,
        _ => path,
    }
}

/// A size as people read it: bytes, then kilobytes, then megabytes (1024).
fn size_text(bytes: u64) -> String {
    const KB: u64 = 1024;
    const MB: u64 = KB * KB;
    match bytes {
        0..KB => format!("{bytes} B"),
        KB..MB => format!("{:.1} KB", bytes as f64 / KB as f64),
        _ => format!("{:.1} MB", bytes as f64 / MB as f64),
    }
}

fn lifecycle_text(lifecycle: &v1::LogLifecycle) -> String {
    use log_lifecycle::Kind;
    let run = lifecycle.run;
    let cause = &lifecycle.cause;
    match Kind::try_from(lifecycle.kind) {
        // The entry carries its engine; the words do not say it.
        Ok(Kind::Started) => "started".to_string(),
        Ok(Kind::Rehosted) => format!("re-hosted, run {run}"),
        Ok(Kind::Restarted) => format!("restarted, run {run}"),
        Ok(Kind::Resumed) => format!("resumed, run {run}"),
        Ok(Kind::Exited) => format!("run {run} ended: {cause}"),
        Ok(Kind::RestartOwed) => format!("a restart is owed for run {run}"),
        Ok(Kind::Closed) => format!("closed: {cause}"),
        Ok(Kind::Reopened) => format!("reopened: {cause}"),
        Ok(Kind::LaunchFailed) => format!("launch failed: {cause}"),
        _ => "(a lifecycle event this ctxloom cannot read)".to_string(),
    }
}

fn ask_text(ask: &v1::LogAsk) -> String {
    let name = &ask.ask;
    match log_ask::State::try_from(ask.state) {
        Ok(log_ask::State::Asked) => format!("{name}: asked {} {}", ask.tool, ask.input_excerpt),
        Ok(log_ask::State::Answered) => format!("{name}: answered {}", ask.option),
        Ok(log_ask::State::Expired) => format!("{name}: expired"),
        Ok(log_ask::State::Withdrawn) => format!("{name}: withdrawn"),
        _ => format!("{name}: (an ask state this ctxloom cannot read)"),
    }
}

#[cfg(test)]
#[path = "log_text.test.rs"]
mod tests;
