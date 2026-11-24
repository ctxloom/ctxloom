//! The joiner and placer the screen and `session transcript show` share.

use daemon_proto::v1::{
    log_entry::Entry as Arm, LogClear, LogEntry, LogPrompt, LogTurnEnd, LogWords,
};

use super::*;

fn entry(arm: Arm) -> LogEntry {
    LogEntry {
        entry: Some(arm),
        ..LogEntry::default()
    }
}

fn prompt(prompt_id: &str, text: &str) -> LogEntry {
    entry(Arm::Prompt(LogPrompt {
        prompt_id: prompt_id.to_string(),
        text: text.to_string(),
    }))
}

fn words(prompt_id: &str, message: &str, index: u32, text: &str) -> LogEntry {
    entry(Arm::Words(LogWords {
        prompt_id: prompt_id.to_string(),
        turn_id: "t".to_string(),
        message_id: message.to_string(),
        index,
        is_final: false,
        text: text.to_string(),
    }))
}

fn turn_end(prompt_id: &str) -> LogEntry {
    entry(Arm::TurnEnd(LogTurnEnd {
        prompt_id: prompt_id.to_string(),
    }))
}

/// What a reader sees: a prompt's text, a message's, or the entry's kind.
fn shown(said: &[Said<'_>]) -> Vec<String> {
    said.iter()
        .map(|s| match s {
            Said::Message(message) => format!("message: {}", message.text),
            Said::Entry(entry) => match &entry.entry {
                Some(Arm::Prompt(p)) => format!("prompt: {}", p.text),
                Some(Arm::TurnEnd(_)) => "turn end".to_string(),
                Some(Arm::Clear(_)) => "clear".to_string(),
                _ => "other".to_string(),
            },
        })
        .collect()
}

// The pieces of one message are one message, joined by their index however
// they arrived, each index once.
#[test]
fn the_pieces_of_a_message_are_joined_by_index() {
    let entries = [
        words("p1", "m1", 1, "ran "),
        words("p1", "m1", 0, "It "),
        words("p1", "m1", 1, "ran "),
        words("p1", "m1", 2, "fine."),
    ];
    assert_eq!(shown(&join(&entries)), ["message: It ran fine."]);
}

// The message stands where its first piece arrived; every other entry is as
// it is, in order.
#[test]
fn a_message_stands_where_its_first_piece_arrived() {
    let entries = [
        prompt("p1", "go"),
        words("p1", "m1", 0, "a"),
        turn_end("p1"),
        words("p1", "m1", 1, "b"),
        words("p1", "m2", 0, "c"),
    ];
    assert_eq!(
        shown(&join(&entries)),
        ["prompt: go", "message: ab", "turn end", "message: c"]
    );
}

#[test]
fn a_message_names_its_prompt_and_its_first_entry() {
    let entries = [words("p9", "m1", 0, "a"), words("p9", "m1", 1, "b")];
    let said = join(&entries);
    let [Said::Message(message)] = &said[..] else {
        panic!("one message")
    };
    assert_eq!(message.message_id, "m1");
    assert_eq!(message.prompt_id, "p9");
    assert!(std::ptr::eq(message.first, &entries[0]));
}

#[test]
fn an_empty_log_joins_to_nothing() {
    assert!(join(&[]).is_empty());
}

// Arrival order is not turn order: Stop and MessageDisplay are separate hook
// processes, and a turn's words can follow its turn end and even the next
// prompt. A message is placed under the prompt of its own turn.
#[test]
fn a_late_message_is_placed_under_the_prompt_of_its_own_turn() {
    let entries = [
        prompt("p1", "first"),
        turn_end("p1"),
        prompt("p2", "second"),
        words("p1", "m1", 0, "answer to the first"),
        words("p2", "m2", 0, "answer to the second"),
    ];
    assert_eq!(
        shown(&place(join(&entries))),
        [
            "prompt: first",
            "message: answer to the first",
            "turn end",
            "prompt: second",
            "message: answer to the second",
        ]
    );
}

// More than one message of a turn keep their order of arrival.
#[test]
fn messages_of_one_turn_keep_their_order() {
    let entries = [
        prompt("p1", "go"),
        words("p1", "m2", 0, "two"),
        words("p1", "m1", 0, "one"),
    ];
    assert_eq!(
        shown(&place(join(&entries))),
        ["prompt: go", "message: two", "message: one"]
    );
}

// A message no prompt entry matches (the prompt was dropped, or the turn
// names none) stays where its first piece arrived.
#[test]
fn a_message_of_no_known_prompt_stays_where_it_arrived() {
    let entries = [
        words("", "m0", 0, "orphan"),
        prompt("p1", "go"),
        words("p7", "m1", 0, "unmatched"),
        entry(Arm::Clear(LogClear::default())),
    ];
    assert_eq!(
        shown(&place(join(&entries))),
        [
            "message: orphan",
            "prompt: go",
            "message: unmatched",
            "clear"
        ]
    );
}

// Placing twice changes nothing more than placing once.
#[test]
fn placing_is_idempotent() {
    let entries = [
        prompt("p1", "first"),
        prompt("p2", "second"),
        words("p1", "m1", 0, "a"),
    ];
    let once = shown(&place(join(&entries)));
    let twice = shown(&place(place(join(&entries))));
    assert_eq!(once, twice);
}
