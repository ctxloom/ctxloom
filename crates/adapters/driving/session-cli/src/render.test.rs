//! The daemon's answers read back into the use cases' types, as the text
//! renderers print them.

use daemon_proto::v1;
use isolation_application::Verdict;
use sessions_application::Class;

use super::{removal_of, report_of};

// A file the daemon calls machine-written stays machine-written; any other
// class is a person's.
#[test]
fn a_removal_keeps_each_files_class() {
    let file = |rel: &str, class: v1::removal_file::Class| v1::RemovalFile {
        path: format!("/h/{rel}"),
        rel: rel.to_string(),
        class: class as i32,
        bytes: 1,
    };
    let removal = removal_of(&v1::DeleteSessionResponse {
        harp: "amber-swift-owl".to_string(),
        files: vec![
            file("native/history.jsonl", v1::removal_file::Class::Machine),
            file("notes.md", v1::removal_file::Class::Authored),
        ],
        ..v1::DeleteSessionResponse::default()
    });
    let classes: Vec<Class> = removal.files.destroy.iter().map(|f| f.class).collect();
    assert_eq!(classes, [Class::Machine, Class::Authored]);
}

// Each owner state and verdict the daemon names is read as itself; a word
// it does not name is indeterminate, and skipped.
#[test]
fn a_worktree_report_reads_each_owner_state_and_verdict() {
    let row = |owner_state: &str, verdict: &str| v1::ScratchWorktree {
        harp: "amber-swift-owl".to_string(),
        worktree: "ctxloom-wt-x".to_string(),
        path: "/h/wt".to_string(),
        owner_state: owner_state.to_string(),
        verdict: verdict.to_string(),
        reason: String::new(),
    };
    let report = report_of(&v1::WorktreeReport {
        worktrees: vec![
            row("alive", "reaped"),
            row("dead", "reapable"),
            row("gone", "spared"),
            row("alive", "unheard-of"),
        ],
        ..v1::WorktreeReport::default()
    });
    let read: Vec<(&str, Verdict)> = report
        .worktrees
        .iter()
        .map(|r| (r.owner_state, r.verdict))
        .collect();
    assert_eq!(
        read,
        [
            ("alive", Verdict::Reaped),
            ("dead", Verdict::Reapable),
            ("indeterminate", Verdict::Spared),
            ("alive", Verdict::Skipped),
        ]
    );
}

fn entry(arm: v1::log_entry::Entry) -> v1::LogEntry {
    v1::LogEntry {
        entry: Some(arm),
        ..v1::LogEntry::default()
    }
}

fn prompt(prompt_id: &str, text: &str) -> v1::LogEntry {
    entry(v1::log_entry::Entry::Prompt(v1::LogPrompt {
        prompt_id: prompt_id.to_string(),
        text: text.to_string(),
    }))
}

fn words(prompt_id: &str, message: &str, index: u32, text: &str) -> v1::LogEntry {
    entry(v1::log_entry::Entry::Words(v1::LogWords {
        prompt_id: prompt_id.to_string(),
        message_id: message.to_string(),
        index,
        text: text.to_string(),
        ..v1::LogWords::default()
    }))
}

fn gap(dropped: &str, why: &str, seconds: i64) -> v1::LogEntry {
    entry(v1::log_entry::Entry::Gap(v1::LogGap {
        dropped: dropped.to_string(),
        why: why.to_string(),
        dropped_at: Some(daemon_proto::google::protobuf::Timestamp { seconds, nanos: 0 }),
    }))
}

fn read(entries: &[v1::LogEntry]) -> String {
    let mut out = Vec::new();
    super::conversation_text(entries, &mut out).unwrap();
    String::from_utf8(out).unwrap()
}

// A transcript reads as a conversation: each prompt, the assistant's message
// joined from its pieces under the prompt of its own turn even when its words
// came late, and nothing of the tool calls or turn ends.
#[test]
fn a_conversation_reads_prompts_and_joined_messages_under_their_prompts() {
    let turn_end = entry(v1::log_entry::Entry::TurnEnd(v1::LogTurnEnd {
        prompt_id: "p1".to_string(),
    }));
    assert_eq!(
        read(&[
            prompt("p1", "Hi"),
            turn_end,
            prompt("p2", "Again"),
            words("p1", "m1", 1, "there"),
            words("p1", "m1", 0, "Hello "),
        ]),
        "user: Hi\nassistant: Hello there\nuser: Again\n"
    );
}

// s4-Q14, H3 (PROPOSAL, the owner's to word): a gap reads as a line of its
// own, in its place in the conversation, saying that a fact is missing,
// which, when it was dropped and why.
#[test]
fn a_gap_reads_as_a_line_of_its_own_in_its_place() {
    assert_eq!(
        read(&[
            prompt("p1", "Hi"),
            gap("hook:turn-end:s:p1", "bound", 1_700_000_050),
            gap("hook:text:s:m1:0", "disk", 1_700_000_051),
            prompt("p2", "Again"),
        ]),
        "user: Hi\n\
         gap: 1 fact not recorded (hook:turn-end:s:p1, dropped at 2023-11-14T22:14:10Z: the session's hold was at its bound)\n\
         gap: 1 fact not recorded (hook:text:s:m1:0, dropped at 2023-11-14T22:14:11Z: the disk refused it)\n\
         user: Again\n"
    );
}

// A gap whose why this ctxloom does not know (a later word) says it as it is.
#[test]
fn a_gap_of_an_unknown_why_says_the_word() {
    assert_eq!(
        read(&[gap("x", "quota", 1_700_000_050)]),
        "gap: 1 fact not recorded (x, dropped at 2023-11-14T22:14:10Z: quota)\n"
    );
}
