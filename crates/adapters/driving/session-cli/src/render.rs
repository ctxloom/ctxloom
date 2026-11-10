//! The rows and documents the commands answer with, in both formats. The
//! JSON keys are D16's: Go 0.7's `harp, summary, state, start, end` plus the
//! plan's additions, each left out when empty as Go leaves its `omitempty`
//! ones. Times are RFC 3339 in json and `2006-01-02 15:04:05` (UTC) in text.

use std::io::{self, Write};

use angzarr_diagnostic::{Arg, Diagnostic};
use angzarr_presentation::text;
use serde::Serialize;
use sessions_application::{
    stamp, Class, FilePlan, ImportReport, PlanItem, Removal, Renamed, Scope, TranscriptRow, View,
    NO_SUMMARY,
};

/// One session as `session list` and `search` row it.
#[derive(Serialize)]
pub(crate) struct Row {
    harp: String,
    summary: String,
    state: &'static str,
    start: String,
    /// When its last run ended; left out while it is live.
    #[serde(skip_serializing_if = "Option::is_none")]
    end: Option<String>,
    #[serde(skip_serializing_if = "is_false")]
    purged: bool,
    #[serde(skip_serializing_if = "is_false")]
    kept: bool,
    engine: String,
}

fn is_false(value: &bool) -> bool {
    !value
}

impl Row {
    pub(crate) fn of(view: &View) -> Row {
        Row {
            harp: view.record.harp.clone(),
            summary: view.summary(),
            state: view.state.as_str(),
            start: stamp::rfc3339(view.record.started_at),
            end: view.end().map(stamp::rfc3339),
            purged: view.record.purged_at.is_some(),
            kept: view.record.kept,
            engine: view.record.engine.clone(),
        }
    }
}

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

/// The listing as text: the placeholder, or a table.
pub(crate) fn rows_text(views: &[View], out: &mut dyn Write) -> io::Result<()> {
    if views.is_empty() {
        return writeln!(out, "(no sessions)");
    }
    let rows: Vec<Vec<String>> = views
        .iter()
        .map(|v| {
            vec![
                v.record.harp.clone(),
                v.summary(),
                v.state.as_str().to_string(),
                stamp::text(v.record.started_at),
                v.end().map(stamp::text).unwrap_or_default(),
            ]
        })
        .collect();
    write!(
        out,
        "{}",
        table(&["HARP", "SUMMARY", "STATE", "START", "END"], &rows)
    )
}

/// `session show`'s document in json.
#[derive(Serialize)]
pub(crate) struct ShowDoc {
    id: String,
    harp: String,
    project: String,
    project_id: String,
    state: &'static str,
    engine: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    native_session: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    title: Option<String>,
    started_at: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    ended_at: Option<String>,
    #[serde(skip_serializing_if = "is_false")]
    purged: bool,
    #[serde(skip_serializing_if = "is_false")]
    kept: bool,
    output_dir: String,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    rotations: Vec<RotationDoc>,
}

#[derive(Serialize)]
struct RotationDoc {
    native_id: String,
    transcript_path: String,
    rotated_at: String,
}

impl ShowDoc {
    pub(crate) fn of(view: &View) -> ShowDoc {
        let record = &view.record;
        ShowDoc {
            id: record.id.clone(),
            harp: record.harp.clone(),
            project: record.project_dir.clone(),
            project_id: record.project_id.clone(),
            state: view.state.as_str(),
            engine: record.engine.clone(),
            native_session: record.native_id.clone(),
            title: view.title.clone(),
            started_at: stamp::rfc3339(record.started_at),
            ended_at: view.end().map(stamp::rfc3339),
            purged: record.purged_at.is_some(),
            kept: record.kept,
            output_dir: record.output_dir.clone(),
            rotations: record
                .rotations
                .iter()
                .map(|r| RotationDoc {
                    native_id: r.native_id.clone(),
                    transcript_path: r.transcript_path.clone(),
                    rotated_at: r.rotated_at.map(stamp::rfc3339).unwrap_or_default(),
                })
                .collect(),
        }
    }
}

/// One fact per line, absent facts left out rather than printed blank (Go:
/// `renderSessionShow`).
pub(crate) fn show_text(view: &View, out: &mut dyn Write) -> io::Result<()> {
    let record = &view.record;
    let title = view.title.as_deref().unwrap_or(NO_SUMMARY);
    writeln!(out, "{}\n{title}\n", record.harp)?;
    writeln!(out, "state:    {}", view.state.as_str())?;
    writeln!(out, "engine:   {}", record.engine)?;
    if !record.native_id.is_empty() {
        writeln!(out, "native:   {}", record.native_id)?;
    }
    writeln!(out, "project:  {}", record.project_dir)?;
    writeln!(out, "started:  {}", stamp::text(record.started_at))?;
    if let Some(end) = view.end() {
        writeln!(out, "ended:    {}", stamp::text(end))?;
    }
    if record.purged_at.is_some() {
        writeln!(out, "purged:   its transcript was destroyed")?;
    }
    if record.kept {
        writeln!(
            out,
            "kept:     its transcript is kept at any age (`ctxloom session keep --undo` releases it)"
        )?;
    }
    Ok(())
}

/// `session edit`'s answer: the session under both spellings.
#[derive(Serialize)]
pub(crate) struct EditDoc {
    harp: String,
    previous_harp: String,
    fields: Vec<&'static str>,
    output_dir: String,
}

impl EditDoc {
    pub(crate) fn of(renamed: &Renamed) -> EditDoc {
        EditDoc {
            harp: renamed.to.clone(),
            previous_harp: renamed.from.clone(),
            fields: vec!["name"],
            output_dir: renamed.output_dir.clone(),
        }
    }
}

/// `session keep`'s answer.
#[derive(Serialize)]
pub(crate) struct KeepDoc {
    pub(crate) harp: String,
    pub(crate) kept: bool,
}

/// One file in a removal's plan, as json.
#[derive(Serialize)]
struct ItemDoc {
    path: String,
    rel: String,
    class: &'static str,
    bytes: u64,
    action: &'static str,
    #[serde(skip_serializing_if = "Option::is_none")]
    reason: Option<String>,
}

impl ItemDoc {
    fn of(item: &PlanItem) -> ItemDoc {
        ItemDoc {
            path: item.path.clone(),
            rel: item.rel.clone(),
            class: class(item.class),
            bytes: item.bytes,
            action: item.action,
            reason: item.reason.clone(),
        }
    }
}

fn class(class: Class) -> &'static str {
    match class {
        Class::Machine => "machine",
        Class::Authored => "authored",
    }
}

/// The file half of a removal, as Go's `PurgeSessionResult`.
#[derive(Serialize)]
struct FilesDoc {
    harp: String,
    applied: bool,
    destroy: Vec<ItemDoc>,
    keep: Vec<ItemDoc>,
    bytes_freed: u64,
    populations: [&'static str; 1],
}

/// `session remove`'s answer, naming each artifact separately: a single
/// "removed: true" could not tell that the index entry went and the files did
/// not.
#[derive(Serialize)]
pub(crate) struct RemovalDoc {
    harp: String,
    applied: bool,
    index_entry_removed: bool,
    files: FilesDoc,
    /// The scratch worktrees a purge reached, when it reached any.
    #[serde(skip_serializing_if = "Option::is_none")]
    worktrees: Option<isolation_application::Report>,
}

impl RemovalDoc {
    pub(crate) fn of(
        removal: &Removal,
        worktrees: Option<&isolation_application::Report>,
    ) -> RemovalDoc {
        let FilePlan {
            destroy,
            keep,
            bytes_freed,
        } = &removal.files;
        RemovalDoc {
            harp: removal.harp.clone(),
            applied: removal.applied,
            index_entry_removed: removal.index_entry_removed,
            files: FilesDoc {
                harp: removal.harp.clone(),
                applied: removal.applied,
                destroy: destroy.iter().map(ItemDoc::of).collect(),
                keep: keep.iter().map(ItemDoc::of).collect(),
                bytes_freed: *bytes_freed,
                populations: ["transcript"],
            },
            worktrees: worktrees.cloned(),
        }
    }
}

/// A summary line, then a table of every file looked at (the destroys first,
/// then every kept file with its reason), then one line for the index entry.
pub(crate) fn removal_text(removal: &Removal, out: &mut dyn Write) -> io::Result<()> {
    let files = &removal.files;
    let verb = if removal.applied {
        "destroyed"
    } else {
        "would destroy"
    };
    writeln!(
        out,
        "session {} (transcript) \u{2014} {verb} {} file(s) ({} byte(s) freed)",
        removal.harp,
        files.destroy.len(),
        files.bytes_freed
    )?;
    let rows: Vec<Vec<String>> = files
        .destroy
        .iter()
        .chain(&files.keep)
        .map(|i| {
            vec![
                i.rel.clone(),
                class(i.class).to_string(),
                i.action.to_string(),
                i.bytes.to_string(),
                i.reason.clone().unwrap_or_default(),
            ]
        })
        .collect();
    if rows.is_empty() {
        writeln!(out, "(nothing found in this session's directory)")?;
    } else {
        write!(
            out,
            "{}",
            table(&["PATH", "CLASS", "ACTION", "BYTES", "REASON"], &rows)
        )?;
    }
    match (removal.scope, removal.index_entry_removed) {
        (Scope::Remove, true) => writeln!(out, "index entry {}: removed", removal.harp),
        (Scope::Remove, false) => writeln!(out, "index entry {}: would be removed", removal.harp),
        _ => writeln!(
            out,
            "index entry {}: kept (the session stays listed)",
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
/// each entry; the records that are not the conversation (the session's id, a
/// completion, a tool call) say nothing a reader wants. The assistant's words
/// are coalesced here: a message's `assistant_delta` lines are joined by their
/// `index`, and the message is one `assistant:` line.
///
/// Arrival order is not turn order: the engine's Stop and MessageDisplay hooks
/// are separate processes, so a turn's words can follow its `turn_end` and
/// even the next prompt. A message is placed by its `prompt_id`: the user
/// entries are the prompts in order, and the prompt ids, in the order each
/// first appears on any record, are the same prompts, so a message follows
/// the entry of its own prompt. (The entry line carries no `prompt_id` of its
/// own.) A message whose prompt is not matched to an entry stands where its
/// first piece arrived.
pub(crate) fn conversation_text(transcript: &str, out: &mut dyn Write) -> io::Result<()> {
    let records: Vec<serde_json::Value> = transcript
        .lines()
        .filter(|l| !l.trim().is_empty())
        .filter_map(|line| serde_json::from_str(line).ok())
        .collect();
    let prompt_of = |record: &serde_json::Value| -> String {
        record["prompt_id"].as_str().unwrap_or_default().to_string()
    };
    // The prompts, in the order each first appears.
    let mut prompts: Vec<String> = Vec::new();
    for record in &records {
        let prompt = prompt_of(record);
        if record["kind"] != "entry" && !prompt.is_empty() && !prompts.contains(&prompt) {
            prompts.push(prompt);
        }
    }
    let users = records
        .iter()
        .filter(|r| r["kind"] == "entry" && r["entry"]["type"] == "user")
        .count();
    // The prompts an entry stands for: a message of one is printed after it.
    let matched = &prompts[..users.min(prompts.len())];
    // The messages, in order of first arrival: (message id, prompt id, text).
    let mut messages: Vec<(String, String, String)> = Vec::new();
    for (at, record) in records.iter().enumerate() {
        if record["kind"] != "assistant_delta" {
            continue;
        }
        let message = record["delta"]["message_id"].as_str().unwrap_or_default();
        if messages.iter().any(|(m, _, _)| m == message) {
            continue;
        }
        let mut pieces: Vec<(u64, &str)> = records[at..]
            .iter()
            .filter(|r| {
                r["kind"] == "assistant_delta" && r["delta"]["message_id"].as_str() == Some(message)
            })
            .map(|r| {
                (
                    r["delta"]["index"].as_u64().unwrap_or_default(),
                    r["delta"]["text"].as_str().unwrap_or_default(),
                )
            })
            .collect();
        pieces.sort_by_key(|(index, _)| *index);
        pieces.dedup_by_key(|(index, _)| *index);
        let text: String = pieces.iter().map(|(_, text)| *text).collect();
        messages.push((message.to_string(), prompt_of(record), text));
    }
    let mut said: Vec<&str> = Vec::new();
    let mut user = 0;
    for record in &records {
        match record["kind"].as_str() {
            Some("entry") => {
                let entry = &record["entry"];
                let kind = entry["type"].as_str().unwrap_or_default();
                let content = entry["content"].as_str().unwrap_or_default();
                writeln!(out, "{kind}: {content}")?;
                if kind == "user" {
                    let own = matched.get(user);
                    user += 1;
                    for (message, prompt, text) in &messages {
                        if own == Some(prompt) && !said.contains(&message.as_str()) {
                            said.push(message);
                            writeln!(out, "assistant: {text}")?;
                        }
                    }
                }
            }
            Some("assistant_delta") => {
                let message = record["delta"]["message_id"].as_str().unwrap_or_default();
                if said.contains(&message) {
                    continue;
                }
                let held = messages
                    .iter()
                    .find(|(m, prompt, _)| m == message && matched.contains(prompt));
                if held.is_some() {
                    // It is printed under its own prompt's entry.
                    continue;
                }
                if let Some((_, _, text)) = messages.iter().find(|(m, _, _)| m == message) {
                    said.push(message);
                    writeln!(out, "assistant: {text}")?;
                }
            }
            _ => {}
        }
    }
    Ok(())
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
    pub(crate) fn of(report: &ImportReport) -> ImportDoc {
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
                    code: l.why.code().to_string(),
                    args: l
                        .why
                        .args()
                        .into_iter()
                        .filter_map(|(name, arg)| match arg {
                            Arg::Str(text) => Some((name.to_string(), text)),
                            Arg::Uint(n) => Some((name.to_string(), n.to_string())),
                            Arg::Int(n) => Some((name.to_string(), n.to_string())),
                            Arg::Nested(_) => None,
                        })
                        .collect(),
                })
                .collect(),
        }
    }
}

/// A count line, then each session left and its reason's code.
pub(crate) fn import_text(report: &ImportReport, out: &mut dyn Write) -> io::Result<()> {
    writeln!(
        out,
        "imported {}, already held {}, left {}",
        report.imported.len(),
        report.already.len(),
        report.left.len()
    )?;
    for left in &report.left {
        writeln!(out, "left {}: {}", left.harp, left.why.code())?;
    }
    Ok(())
}

/// Go's sweep report: a line per session, the reason under it, and a count.
#[cfg(feature = "session-destroy")]
pub(crate) fn sweep_text(
    report: &reclaim_application::SweepReport,
    out: &mut dyn Write,
) -> io::Result<()> {
    let purge = report
        .purge_cutoff
        .as_deref()
        .map_or("not set (purges are held)".to_string(), str::to_string);
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
            r.harp,
            r.action,
            format!("{:?}", r.verdict).to_lowercase(),
            size
        )?;
        if !r.reason.is_empty() {
            writeln!(out, "  {:<28} {}", "", r.reason)?;
        }
        if !r.command.is_empty() {
            writeln!(out, "  {:<28} run: {}", "", r.command)?;
        }
    }
    let count = |k: &str| report.counts.get(k).copied().unwrap_or(0);
    writeln!(
        out,
        "\n{} done, {} planned, {} held, {} left, {} skipped, {} failed; {} session(s) had nothing to do.",
        count("done"), count("planned"), count("held"), count("left"), count("skipped"), count("failed"), report.untouched
    )
}
