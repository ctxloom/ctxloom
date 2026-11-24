//! A log read as a conversation: the one joiner and placer the screen and
//! `session transcript show` share.
//!
//! The words stay pieces on the wire, one entry each. A reader joins the
//! pieces of one message ([`join`]) and, for a transcript read in order,
//! places each message under the prompt that opened its turn ([`place`]).

use daemon_proto::v1::{log_entry::Entry as Arm, LogEntry};

/// One thing a log says, its words joined.
#[derive(Clone, Debug, PartialEq)]
pub enum Said<'a> {
    /// An entry, as it is.
    Entry(&'a LogEntry),
    /// One message of the assistant: its pieces joined.
    Message(Message<'a>),
}

/// The pieces of one message, joined by their index.
#[derive(Clone, Debug, PartialEq)]
pub struct Message<'a> {
    /// The entry of the message's first piece to arrive: where it stands in
    /// the log, and its instant.
    pub first: &'a LogEntry,
    pub message_id: &'a str,
    /// The turn the message belongs to; the prompt it answers.
    pub prompt_id: &'a str,
    pub text: String,
}

/// `entries` with each message's pieces joined where its first piece
/// arrived, every other entry as it is, in order.
pub fn join(entries: &[LogEntry]) -> Vec<Said<'_>> {
    let mut said = Vec::new();
    let mut joined: Vec<&str> = Vec::new();
    for entry in entries {
        let Some(Arm::Words(piece)) = &entry.entry else {
            said.push(Said::Entry(entry));
            continue;
        };
        if joined.contains(&piece.message_id.as_str()) {
            continue;
        }
        joined.push(&piece.message_id);
        let mut pieces: Vec<(u32, &str)> = entries
            .iter()
            .filter_map(|other| match &other.entry {
                Some(Arm::Words(w)) if w.message_id == piece.message_id => {
                    Some((w.index, w.text.as_str()))
                }
                _ => None,
            })
            .collect();
        pieces.sort_by_key(|(index, _)| *index);
        pieces.dedup_by_key(|(index, _)| *index);
        said.push(Said::Message(Message {
            first: entry,
            message_id: &piece.message_id,
            prompt_id: &piece.prompt_id,
            text: pieces.into_iter().map(|(_, text)| text).collect(),
        }));
    }
    said
}

/// `said` with each message moved under the prompt entry of its own turn
/// (the one with its `prompt_id`), after the messages already there; a
/// message no prompt entry matches stays where it arrived.
pub fn place(said: Vec<Said<'_>>) -> Vec<Said<'_>> {
    let prompt_of = |s: &Said<'_>| match s {
        Said::Entry(LogEntry {
            entry: Some(Arm::Prompt(prompt)),
            ..
        }) if !prompt.prompt_id.is_empty() => Some(prompt.prompt_id.clone()),
        _ => None,
    };
    let opened: Vec<String> = said.iter().filter_map(prompt_of).collect();
    let owned = |s: &Said<'_>| match s {
        Said::Message(message) => opened.iter().any(|id| id == message.prompt_id),
        Said::Entry(_) => false,
    };
    let mut placed = Vec::with_capacity(said.len());
    let mut under_its_prompt: Vec<&Said<'_>> = Vec::new();
    for s in &said {
        if owned(s) {
            under_its_prompt.push(s);
        }
    }
    let mut taken: Vec<String> = Vec::new();
    for s in &said {
        if owned(s) {
            continue;
        }
        placed.push(s.clone());
        if let Some(id) = prompt_of(s) {
            if taken.contains(&id) {
                continue;
            }
            taken.push(id.clone());
            placed.extend(
                under_its_prompt
                    .iter()
                    .filter(|m| matches!(m, Said::Message(message) if message.prompt_id == id))
                    .map(|m| (*m).clone()),
            );
        }
    }
    placed
}

#[cfg(test)]
#[path = "conversation.test.rs"]
mod tests;
