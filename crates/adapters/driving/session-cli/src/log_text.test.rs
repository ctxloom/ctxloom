//! A log entry in plain words, the way the screen's log pane and its text
//! seam say it: no JSON, no envelope, no colour.

use daemon_proto::v1::{self, log_entry::Entry as Arm};

use super::{text_of as words_of, Of};

fn text_of(entry: &v1::LogEntry) -> String {
    words_of(entry, &Of::default())
}

fn entry(arm: Arm) -> v1::LogEntry {
    v1::LogEntry {
        entry: Some(arm),
        ..v1::LogEntry::default()
    }
}

fn lifecycle(kind: v1::log_lifecycle::Kind, run: u32, cause: &str, engine: &str) -> v1::LogEntry {
    entry(Arm::Lifecycle(v1::LogLifecycle {
        kind: kind as i32,
        run,
        cause: cause.to_string(),
        engine: engine.to_string(),
    }))
}

#[test]
fn the_transcripts_entries_read_as_who_said_what() {
    let said = |arm| text_of(&entry(arm));
    assert_eq!(
        said(Arm::Prompt(v1::LogPrompt {
            prompt_id: "p".to_string(),
            text: "review the migration".to_string()
        })),
        "user: review the migration"
    );
    assert_eq!(
        said(Arm::Words(v1::LogWords {
            text: "it is reversible".to_string(),
            ..v1::LogWords::default()
        })),
        "assistant: it is reversible"
    );
    assert_eq!(
        said(Arm::Tool(v1::LogTool {
            name: "Bash".to_string(),
            input_bytes: 120,
            response_bytes: 2100,
            ..v1::LogTool::default()
        })),
        "tool Bash, input 120 B, result 2.1 KB"
    );
    assert_eq!(
        said(Arm::Tool(v1::LogTool {
            name: "bash".to_string(),
            input_bytes: 20,
            response_bytes: 3,
            status: Some(v1::log_tool::Status::Error as i32),
            ..v1::LogTool::default()
        })),
        "tool bash, input 20 B, result 3 B, error"
    );
    assert_eq!(said(Arm::TurnEnd(v1::LogTurnEnd::default())), "turn ended");
    assert_eq!(
        said(Arm::Compaction(v1::LogCompaction {
            phase: v1::log_compaction::Phase::After as i32,
            trigger: "auto".to_string(),
            ..v1::LogCompaction::default()
        })),
        "compacted (after, auto)"
    );
    assert_eq!(
        said(Arm::Clear(v1::LogClear::default())),
        "conversation cleared"
    );
    assert_eq!(
        said(Arm::Gap(v1::LogGap {
            dropped: "hook:turn-end:s:p1".to_string(),
            why: "bound".to_string(),
            dropped_at: None,
        })),
        "gap: hook:turn-end:s:p1 not recorded (bound)"
    );
}

#[test]
fn the_daemons_entries_read_as_what_it_did() {
    use v1::log_lifecycle::Kind;
    let said = |kind, run, cause, engine| text_of(&lifecycle(kind, run, cause, engine));
    assert_eq!(said(Kind::Started, 0, "", "claude-code"), "started");
    assert_eq!(
        said(Kind::Exited, 1, "runner-exit", ""),
        "run 1 ended: runner-exit"
    );
    assert_eq!(said(Kind::Rehosted, 2, "", ""), "re-hosted, run 2");
    assert_eq!(said(Kind::Restarted, 2, "", ""), "restarted, run 2");
    assert_eq!(said(Kind::Resumed, 2, "", ""), "resumed, run 2");
    assert_eq!(
        said(Kind::RestartOwed, 2, "", ""),
        "a restart is owed for run 2"
    );
    assert_eq!(said(Kind::Closed, 0, "user", ""), "closed: user");
    assert_eq!(said(Kind::Reopened, 0, "resume", ""), "reopened: resume");
    assert_eq!(
        said(Kind::LaunchFailed, 0, "no credential", ""),
        "launch failed: no credential"
    );
}

#[test]
fn an_ask_reads_as_asked_then_how_it_ended() {
    use v1::log_ask::State;
    let said = |state, option: &str| {
        text_of(&entry(Arm::Ask(v1::LogAsk {
            state: state as i32,
            ask: "ask-a".to_string(),
            tool: "Bash".to_string(),
            input_excerpt: "make test".to_string(),
            option: option.to_string(),
        })))
    };
    assert_eq!(said(State::Asked, ""), "ask-a: asked Bash make test");
    assert_eq!(
        said(State::Answered, "allow-once"),
        "ask-a: answered allow-once"
    );
    assert_eq!(said(State::Expired, ""), "ask-a: expired");
    assert_eq!(said(State::Withdrawn, ""), "ask-a: withdrawn");
}

// An entry of a kind this ctxloom does not know says so, never its JSON.
#[test]
fn an_entry_of_no_known_arm_says_so() {
    assert_eq!(
        text_of(&v1::LogEntry::default()),
        "(an entry this ctxloom cannot read)"
    );
}

// The engine's own data is not in the plain words.
#[test]
fn an_engines_extra_is_not_in_the_words() {
    let mut with = entry(Arm::TurnEnd(v1::LogTurnEnd::default()));
    with.extra
        .insert("opencode.part_id".to_string(), "prt_1".to_string());
    assert_eq!(text_of(&with), "turn ended");
}

/// A tool entry of `tool` with the path its engine's adapter attached.
fn tool_with_path(tool: &str, key: &str, path: &str, input: u64, result: u64) -> v1::LogEntry {
    let mut with = entry(Arm::Tool(v1::LogTool {
        name: tool.to_string(),
        input_bytes: input,
        response_bytes: result,
        ..v1::LogTool::default()
    }));
    with.extra.insert(key.to_string(), path.to_string());
    with
}

// The owner's ruling of 2026-11-24: a file tool's entry shows the path its
// adapter attached (`<engine>.path`), relative to the session's project root
// when it is under it, else as given; sizes are said as people read them.
#[test]
fn a_file_tools_path_is_shown_relative_to_the_project_root() {
    let of = Of {
        project_root: "/work/proj",
        engine: "claude-code",
    };
    let said = |path: &str| {
        words_of(
            &tool_with_path("Edit", "claude-code.path", path, 4200, 12),
            &of,
        )
    };
    assert_eq!(
        said("/work/proj/src/lib.rs"),
        "tool Edit, src/lib.rs, input 4.1 KB, result 12 B"
    );
    assert_eq!(
        said("/etc/hosts"),
        "tool Edit, /etc/hosts, input 4.1 KB, result 12 B",
        "outside the root, as given"
    );
    // A sibling that merely shares the root's prefix is not under it.
    assert_eq!(
        said("/work/proj-other/a.rs"),
        "tool Edit, /work/proj-other/a.rs, input 4.1 KB, result 12 B"
    );
    assert_eq!(
        said("src/a.rs"),
        "tool Edit, src/a.rs, input 4.1 KB, result 12 B",
        "a relative path is as given"
    );
}

// Only the session's own engine's key is read; another's, or none, shows no
// path, and the tool is still named.
#[test]
fn only_the_sessions_engines_path_is_shown() {
    let of = Of {
        project_root: "/work/proj",
        engine: "opencode",
    };
    assert_eq!(
        words_of(
            &tool_with_path("read", "opencode.path", "/work/proj/a.rs", 20, 3),
            &of
        ),
        "tool read, a.rs, input 20 B, result 3 B"
    );
    assert_eq!(
        words_of(
            &tool_with_path("read", "claude-code.path", "/work/proj/a.rs", 20, 3),
            &of
        ),
        "tool read, input 20 B, result 3 B"
    );
}

#[test]
fn sizes_are_said_in_bytes_kilobytes_and_megabytes() {
    let of = Of::default();
    let said = |bytes| {
        words_of(
            &entry(Arm::Tool(v1::LogTool {
                name: "Bash".to_string(),
                response_bytes: bytes,
                ..v1::LogTool::default()
            })),
            &of,
        )
    };
    assert_eq!(said(0), "tool Bash, input 0 B, result 0 B");
    assert_eq!(said(1023), "tool Bash, input 0 B, result 1023 B");
    assert_eq!(said(1024), "tool Bash, input 0 B, result 1.0 KB");
    assert_eq!(said(1_572_864), "tool Bash, input 0 B, result 1.5 MB");
}

// Data yes, display no: the started entry carries its engine, and the words
// do not say it.
#[test]
fn a_started_entry_keeps_its_engine_in_the_data_and_not_in_the_words() {
    let started = lifecycle(v1::log_lifecycle::Kind::Started, 0, "", "claude-code");
    assert_eq!(text_of(&started), "started");
    let Some(Arm::Lifecycle(data)) = &started.entry else {
        panic!("lifecycle")
    };
    assert_eq!(data.engine, "claude-code");
}
