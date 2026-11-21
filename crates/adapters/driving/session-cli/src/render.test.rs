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

// s4-Q14, H3 (PROPOSAL, the owner's to word): a gap line reads as a line of
// its own, in its place in the conversation, saying how many facts are
// missing, which, when they were dropped and why.
#[test]
fn a_gap_reads_as_a_line_of_its_own_in_its_place() {
    let transcript = [
        r#"{"kind":"entry","entry":{"type":"user","content":"Hi"}}"#,
        r#"{"kind":"gap","gap":{"facts":1,"dropped":"hook:turn-end:s:p1","why":"bound","dropped_at":"2023-11-14T22:14:10Z"}}"#,
        r#"{"kind":"gap","gap":{"facts":1,"dropped":"hook:text:s:m1:0","why":"disk","dropped_at":"2023-11-14T22:14:11Z"}}"#,
        r#"{"kind":"entry","entry":{"type":"user","content":"Again"}}"#,
    ]
    .join("\n");
    let mut out = Vec::new();
    super::conversation_text(&transcript, &mut out).unwrap();
    assert_eq!(
        String::from_utf8(out).unwrap(),
        "user: Hi\n\
         gap: 1 fact not recorded (hook:turn-end:s:p1, dropped at 2023-11-14T22:14:10Z: the session's hold was at its bound)\n\
         gap: 1 fact not recorded (hook:text:s:m1:0, dropped at 2023-11-14T22:14:11Z: the disk refused it)\n\
         user: Again\n"
    );
}

// A gap whose why this ctxloom does not know (a later word) says it as it
// is, and one that counts no facts is still one dropped fact.
#[test]
fn a_gap_of_an_unknown_why_says_the_word() {
    let transcript =
        r#"{"kind":"gap","gap":{"dropped":"x","why":"quota","dropped_at":"2023-11-14T22:14:10Z"}}"#;
    let mut out = Vec::new();
    super::conversation_text(transcript, &mut out).unwrap();
    assert_eq!(
        String::from_utf8(out).unwrap(),
        "gap: 1 fact not recorded (x, dropped at 2023-11-14T22:14:10Z: quota)\n"
    );
}
