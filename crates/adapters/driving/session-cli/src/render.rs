//! The rows and documents the commands answer with, in both formats. The
//! JSON keys are D16's: Go 0.7's `harp, summary, state, start, end` plus the
//! plan's additions (the resource `name`, slice 2 B3), each left out when
//! empty as Go leaves its `omitempty` ones. Times are RFC 3339 in json and `2006-01-02 15:04:05` (UTC) in text.

use std::io::{self, Write};

use daemon_proto::v1;
use presentation::text;
use serde::Serialize;
use sessions_application::{stamp, TranscriptRow, NO_SUMMARY};
#[cfg(feature = "purge")]
use sessions_application::{Class, FilePlan, PlanItem, Removal};

/// Columns by display width, two spaces between, no trailing spaces.
pub(crate) fn table(headers: &[&str], rows: &[Vec<String>]) -> String {
    let widths: Vec<usize> = (0..headers.len())
        .map(|c| {
            rows.iter()
                .map(|row| text::width(&row[c]))
                .chain(std::iter::once(text::width(headers[c])))
                .max()
                .unwrap_or(0)
        })
        .collect();
    let line = |cells: Vec<&str>| {
        let padded: Vec<String> = cells
            .iter()
            .zip(&widths)
            .map(|(cell, width)| text::pad(cell, *width))
            .collect();
        format!("{}\n", padded.join("  ").trim_end())
    };
    let mut out = line(headers.to_vec());
    for row in rows {
        out.push_str(&line(row.iter().map(String::as_str).collect()));
    }
    out
}

/// A session's harp: the last segment of its resource name.
fn harp_of(name: &str) -> &str {
    name.rsplit('/').next().unwrap_or(name)
}

/// A session's state, as the rows say it.
fn state_word(state: i32) -> &'static str {
    match v1::session::State::try_from(state) {
        Ok(v1::session::State::Live) => "live",
        Ok(v1::session::State::Ended) => "ended",
        _ => "unknown",
    }
}

/// Its title: the one set, else its first prompt (S55).
fn title_of(session: &v1::Session) -> Option<&str> {
    [session.title.as_str(), session.summary.as_str()]
        .into_iter()
        .find(|text| !text.is_empty())
}

/// The row's summary: the title, or [`NO_SUMMARY`], with a badge for a kept
/// one.
fn summary_of(session: &v1::Session) -> String {
    let mut summary = title_of(session).unwrap_or(NO_SUMMARY).to_string();
    if session.kept {
        summary.push_str("  \u{1f4cc} kept (transcript kept at any age)");
    }
    summary
}

/// A time as the text rows show it; blank when unset.
fn when(at: Option<daemon_proto::google::protobuf::Timestamp>) -> String {
    at.map(|at| stamp::text(at.into())).unwrap_or_default()
}

/// The listing the daemon answered, as text: the placeholder, or a table.
pub(crate) fn listing_text(sessions: &[v1::Session], out: &mut dyn Write) -> io::Result<()> {
    if sessions.is_empty() {
        return writeln!(out, "(no sessions)");
    }
    let rows: Vec<Vec<String>> = sessions
        .iter()
        .map(|s| {
            vec![
                harp_of(&s.name).to_string(),
                summary_of(s),
                state_word(s.state).to_string(),
                when(s.start_time),
                when(s.end_time),
            ]
        })
        .collect();
    write!(
        out,
        "{}",
        table(&["HARP", "SUMMARY", "STATE", "START", "END"], &rows)
    )
}

/// One fact per line, absent facts left out rather than printed blank (Go:
/// `renderSessionShow`).
pub(crate) fn session_text(session: &v1::Session, out: &mut dyn Write) -> io::Result<()> {
    let title = title_of(session).unwrap_or(NO_SUMMARY);
    writeln!(out, "{}\n{title}\n", harp_of(&session.name))?;
    writeln!(out, "state:    {}", state_word(session.state))?;
    writeln!(out, "engine:   {}", session.engine)?;
    if !session.native_session.is_empty() {
        writeln!(out, "native:   {}", session.native_session)?;
    }
    writeln!(out, "project:  {}", session.project_root)?;
    writeln!(out, "started:  {}", when(session.start_time))?;
    if session.end_time.is_some() {
        writeln!(out, "ended:    {}", when(session.end_time))?;
    }
    if session.kept {
        writeln!(
            out,
            "kept:     its transcript is kept at any age (`ctxloom session keep --undo` releases it)"
        )?;
    }
    // The owner link, the close and the children (D-N35), as the json has
    // them: each left out when unset.
    if !session.owner.is_empty() {
        writeln!(out, "owner:    {}", session.owner)?;
    }
    if session.closed {
        writeln!(out, "closed:   yes")?;
    }
    if !session.children.is_empty() {
        writeln!(out, "children: {} held", session.children.len())?;
        for child in &session.children {
            writeln!(out, "  {child}")?;
        }
    }
    if !session.pending.is_empty() {
        let count = session.pending.len();
        let plural = if count == 1 { "" } else { "s" };
        writeln!(
            out,
            "pending:  {count} message{plural} no turn has carried yet"
        )?;
        for message in &session.pending {
            let first = message.text.lines().next().unwrap_or_default();
            writeln!(
                out,
                "  {} ({}): {first}",
                message.message_id, message.urgency
            )?;
        }
    }
    // What its latest run was launched with, and how its last turn failed
    // (step 9): each left out when its run said none.
    if let Some(grant) = &session.grant {
        writeln!(
            out,
            "granted:  MCP servers {}",
            grant.mcp_servers.join(", ")
        )?;
        if !grant.permissions.is_empty() {
            writeln!(out, "          permissions {}", grant.permissions)?;
        }
    }
    // The terminal wake: one sent and not answered (disarmed), or one
    // still awaiting its turn. DRAFT words.
    match session.wake.as_str() {
        "not answered" => writeln!(out, "wake:     sent, not answered")?,
        "outstanding" => writeln!(out, "wake:     sent, awaiting its turn")?,
        _ => {}
    }
    if let Some(failure) = &session.last_failure {
        let said = failure.notice.as_ref().map_or("", |n| n.text.as_str());
        writeln!(out, "failed:   {}: {said}", failure.kind)?;
        if !failure.detail.is_empty() {
            writeln!(out, "          {}", failure.detail)?;
        }
    }
    Ok(())
}

/// `session rename`'s answer: the session, its title and whether it changed.
#[derive(Serialize)]
pub(crate) struct TitleDoc {
    harp: String,
    title: String,
    changed: bool,
}

impl TitleDoc {
    /// The answer of a title set through the daemon.
    pub(crate) fn new(harp: &str, title: &str, changed: bool) -> TitleDoc {
        TitleDoc {
            harp: harp.to_string(),
            title: title.to_string(),
            changed,
        }
    }
}

/// `session keep`'s answer.
#[derive(Serialize)]
pub(crate) struct KeepDoc {
    pub(crate) harp: String,
    pub(crate) kept: bool,
}

#[cfg(feature = "purge")]
fn class(class: Class) -> &'static str {
    match class {
        Class::Machine => "machine",
        Class::Authored => "authored",
    }
}

/// The removal the daemon answered (its plan, or its destroy), as the
/// report renders it.
#[cfg(feature = "purge")]
pub(crate) fn removal_of(plan: &v1::DeleteSessionResponse) -> Removal {
    Removal {
        harp: plan.harp.clone(),
        applied: plan.applied.unwrap_or(false),
        files: FilePlan {
            destroy: plan
                .files
                .iter()
                .map(|file| PlanItem {
                    path: file.path.clone(),
                    rel: file.rel.clone(),
                    class: match v1::removal_file::Class::try_from(file.class) {
                        Ok(v1::removal_file::Class::Machine) => Class::Machine,
                        _ => Class::Authored,
                    },
                    bytes: file.bytes,
                })
                .collect(),
            bytes_freed: plan.bytes_freed,
        },
        events_destroyed: plan.events_destroyed,
        refusal: None,
        dead_letters: Vec::new(),
    }
}

/// The worktrees the daemon answered, as `session worktrees` renders them.
#[cfg(feature = "purge")]
pub(crate) fn report_of(report: &v1::WorktreeReport) -> isolation_application::Report {
    use isolation_application::{Row, Verdict};
    isolation_application::Report {
        worktrees: report
            .worktrees
            .iter()
            .map(|row| Row {
                harp: row.harp.clone(),
                worktree: row.worktree.clone(),
                path: row.path.clone(),
                owner_state: match row.owner_state.as_str() {
                    "alive" => "alive",
                    "dead" => "dead",
                    _ => "indeterminate",
                },
                verdict: match row.verdict.as_str() {
                    "reaped" => Verdict::Reaped,
                    "reapable" => Verdict::Reapable,
                    "spared" => Verdict::Spared,
                    _ => Verdict::Skipped,
                },
                reason: row.reason.clone(),
            })
            .collect(),
        reaped: report.reaped,
        spared: report.spared,
        skipped: report.skipped,
        applied: report.applied.unwrap_or(false),
    }
}

#[cfg(feature = "purge")]
/// A summary line, then a table of every file the destroy takes, then one
/// line for the session itself.
pub(crate) fn removal_text(removal: &Removal, out: &mut dyn Write) -> io::Result<()> {
    let files = &removal.files;
    let verb = if removal.applied {
        "destroyed"
    } else {
        "would destroy"
    };
    writeln!(
        out,
        "session {} \u{2014} {verb} {} file(s) ({} byte(s) freed)",
        removal.harp,
        files.destroy.len(),
        files.bytes_freed
    )?;
    let rows: Vec<Vec<String>> = files
        .destroy
        .iter()
        .map(|i| {
            vec![
                i.rel.clone(),
                class(i.class).to_string(),
                i.bytes.to_string(),
            ]
        })
        .collect();
    if rows.is_empty() {
        writeln!(out, "(nothing found in this session's directory)")?;
    } else {
        write!(out, "{}", table(&["PATH", "CLASS", "BYTES"], &rows))?;
    }
    match removal.applied {
        true => writeln!(
            out,
            "session {}: destroyed, {} event(s) of it and its runs; its name stays taken",
            removal.harp, removal.events_destroyed
        ),
        false => writeln!(
            out,
            "session {}: would be destroyed, its events and its runs' with it; its name stays taken",
            removal.harp
        ),
    }
}

/// `session transcript list`'s answer.
#[derive(Serialize)]
pub(crate) struct TranscriptsDoc {
    transcripts: Vec<TranscriptDoc>,
}

#[derive(Serialize)]
struct TranscriptDoc {
    harp: String,
    captured: bool,
    bytes: u64,
}

impl TranscriptsDoc {
    pub(crate) fn of(rows: &[TranscriptRow]) -> TranscriptsDoc {
        TranscriptsDoc {
            transcripts: rows
                .iter()
                .map(|r| TranscriptDoc {
                    harp: r.harp.clone(),
                    captured: r.captured,
                    bytes: r.bytes,
                })
                .collect(),
        }
    }
}

pub(crate) fn transcripts_text(rows: &[TranscriptRow], out: &mut dyn Write) -> io::Result<()> {
    if rows.is_empty() {
        return writeln!(out, "(no sessions)");
    }
    let cells: Vec<Vec<String>> = rows
        .iter()
        .map(|r| vec![r.harp.clone(), r.captured.to_string(), r.bytes.to_string()])
        .collect();
    write!(out, "{}", table(&["HARP", "CAPTURED", "BYTES"], &cells))
}

/// Go's `renderSessionWorktrees`: a table when there is anything to show, one
/// summary line naming what happened (or would happen) after it.
pub(crate) fn worktrees_text(
    report: &isolation_application::Report,
    out: &mut dyn Write,
) -> io::Result<()> {
    if report.worktrees.is_empty() {
        return writeln!(out, "no ctxloom-owned scratch worktrees");
    }
    let cells: Vec<Vec<String>> = report
        .worktrees
        .iter()
        .map(|r| {
            vec![
                r.harp.clone(),
                r.worktree.clone(),
                r.path.clone(),
                r.owner_state.to_string(),
                format!("{:?}", r.verdict).to_lowercase(),
                r.reason.clone(),
            ]
        })
        .collect();
    write!(
        out,
        "{}",
        table(
            &[
                "HARP",
                "WORKTREE",
                "PATH",
                "OWNER STATE",
                "VERDICT",
                "REASON"
            ],
            &cells
        )
    )?;
    if report.applied {
        writeln!(
            out,
            "removed {}, spared {}, skipped {}",
            report.reaped, report.spared, report.skipped
        )
    } else {
        writeln!(
            out,
            "{} worktree(s) reviewed; {} spared, {} skipped (read-only - nothing was removed)",
            report.worktrees.len(),
            report.spared,
            report.skipped
        )
    }
}

/// A transcript read as a conversation: `user: ...`, `assistant: ...`, for
/// each prompt and message, and `gap: ...` where a hook dropped a fact
/// (s4-Q14, H3); the entries that are not the conversation (a tool call, a
/// turn's end, a compaction, a clear) say nothing a reader wants.
///
/// The assistant's pieces are joined into messages and each message is
/// placed under the prompt of its own turn, by the one joiner and placer
/// `ctxloom top` shares (`daemon_api_client::conversation`): arrival order is not
/// turn order, since the engine's Stop and MessageDisplay hooks are separate
/// processes.
pub(crate) fn conversation_text(entries: &[v1::LogEntry], out: &mut dyn Write) -> io::Result<()> {
    use daemon_api_client::conversation::{join, place, Said};
    use v1::log_entry::Entry;
    for said in place(join(entries)) {
        match said {
            Said::Message(message) => writeln!(out, "assistant: {}", message.text)?,
            Said::Entry(entry) => match &entry.entry {
                Some(Entry::Prompt(prompt)) => writeln!(out, "user: {}", prompt.text)?,
                Some(Entry::Gap(gap)) => writeln!(
                    out,
                    "gap: 1 fact not recorded ({}, dropped at {}: {})",
                    gap.dropped,
                    gap.dropped_at.as_ref().map_or_else(String::new, instant),
                    gap_why(&gap.why),
                )?,
                _ => {}
            },
        }
    }
    Ok(())
}

/// An instant as RFC 3339 text, as the messages print it.
fn instant(at: &daemon_proto::google::protobuf::Timestamp) -> String {
    serde_json::to_value(at)
        .ok()
        .and_then(|value| value.as_str().map(str::to_string))
        .unwrap_or_default()
}

/// Why a fact was dropped, as a gap line says it (PROPOSAL, s4-Q14 H3: the
/// owner's to word); a word this ctxloom does not know, as it is.
fn gap_why(word: &str) -> &str {
    match word {
        "bound" => "the session's hold was at its bound",
        "disk" => "the disk refused it",
        other => other,
    }
}

/// `session import`'s answer.
#[derive(Serialize)]
pub(crate) struct ImportDoc {
    imported: Vec<ImportedDoc>,
    already: Vec<String>,
    left: Vec<LeftDoc>,
}

#[derive(Serialize)]
struct ImportedDoc {
    harp: String,
    project: String,
    id: String,
}

/// A Go session left where it is, and why: the reason's code and args (the
/// adapter has no catalog to render its English from).
#[derive(Serialize)]
struct LeftDoc {
    harp: String,
    code: String,
    args: std::collections::BTreeMap<String, String>,
}

impl ImportDoc {
    pub(crate) fn of(report: &v1::ImportSessionsResponse) -> ImportDoc {
        ImportDoc {
            imported: report
                .imported
                .iter()
                .map(|i| ImportedDoc {
                    harp: i.harp.clone(),
                    project: i.project.clone(),
                    id: i.id.clone(),
                })
                .collect(),
            already: report.already.clone(),
            left: report
                .left
                .iter()
                .map(|l| LeftDoc {
                    harp: l.harp.clone(),
                    code: l.code.clone(),
                    args: l.args.clone().into_iter().collect(),
                })
                .collect(),
        }
    }
}

/// A count line, then each session left and its reason's code.
pub(crate) fn import_text(
    report: &v1::ImportSessionsResponse,
    out: &mut dyn Write,
) -> io::Result<()> {
    writeln!(
        out,
        "imported {}, already held {}, left {}",
        report.imported.len(),
        report.already.len(),
        report.left.len()
    )?;
    for left in &report.left {
        writeln!(out, "left {}: {}", left.harp, left.code)?;
    }
    Ok(())
}

/// Go's sweep report: a line per session, the reason under it, and a count.
#[cfg(feature = "purge")]
pub(crate) fn sweep_text(
    report: &v1::SweepSessionsResponse,
    out: &mut dyn Write,
) -> io::Result<()> {
    let purge = match report.purge_cutoff.is_empty() {
        true => "not set (purges are held)".to_string(),
        false => report.purge_cutoff.clone(),
    };
    writeln!(out, "Session sweep: purge before {purge}.\n")?;
    for r in &report.rows {
        let size = if r.bytes > 0 {
            format!("{} B", r.bytes)
        } else {
            String::new()
        };
        writeln!(
            out,
            "  {:<28} {:<8} {:<8} {:>10}",
            r.harp, r.action, r.verdict, size
        )?;
        if !r.reason.is_empty() {
            writeln!(out, "  {:<28} {}", "", r.reason)?;
        }
        if !r.command.is_empty() {
            writeln!(out, "  {:<28} run: {}", "", r.command)?;
        }
    }
    let count = report.counts.unwrap_or_default();
    writeln!(
        out,
        "\n{} done, {} planned, {} held, {} left, {} skipped, {} failed.",
        count.done, count.planned, count.held, count.left, count.skipped, count.failed
    )
}

#[cfg(test)]
#[path = "render.test.rs"]
mod tests;
