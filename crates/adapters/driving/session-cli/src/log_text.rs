//! A log entry in plain words (the screen's log pane, and its text seam): who
//! said what, or what the daemon did. Display only: the entry is the data,
//! and nothing of its JSON, its engine data or its session shows here.

use daemon_proto::v1::{
    self, log_ask, log_compaction, log_entry::Entry as Arm, log_lifecycle, log_tool,
};

/// What `entry` says, as one line of plain text.
pub(crate) fn text_of(entry: &v1::LogEntry) -> String {
    match &entry.entry {
        Some(Arm::Prompt(prompt)) => format!("user: {}", prompt.text),
        Some(Arm::Words(words)) => format!("assistant: {}", words.text),
        Some(Arm::Tool(tool)) => tool_text(tool),
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

fn tool_text(tool: &v1::LogTool) -> String {
    let how = match tool.status.map(log_tool::Status::try_from) {
        Some(Ok(log_tool::Status::Error)) => ", error",
        _ => "",
    };
    format!(
        "tool {}, input {} bytes, result {} bytes{how}",
        tool.name, tool.input_bytes, tool.response_bytes
    )
}

fn lifecycle_text(lifecycle: &v1::LogLifecycle) -> String {
    use log_lifecycle::Kind;
    let run = lifecycle.run;
    let cause = &lifecycle.cause;
    match Kind::try_from(lifecycle.kind) {
        Ok(Kind::Started) => format!("started on {}", lifecycle.engine),
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
