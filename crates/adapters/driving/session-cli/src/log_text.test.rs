//! A log entry in plain words, the way the screen's log pane and its text
//! seam say it: no JSON, no envelope, no colour.

use daemon_proto::v1::{self, log_entry::Entry as Arm};

use super::text_of;

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
            response_bytes: 2100,
            ..v1::LogTool::default()
        })),
        "tool Bash, 2100 bytes"
    );
    assert_eq!(
        said(Arm::Tool(v1::LogTool {
            name: "bash".to_string(),
            response_bytes: 3,
            status: Some(v1::log_tool::Status::Error as i32),
            ..v1::LogTool::default()
        })),
        "tool bash, 3 bytes, error"
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
    assert_eq!(
        said(Kind::Started, 0, "", "claude-code"),
        "started on claude-code"
    );
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
