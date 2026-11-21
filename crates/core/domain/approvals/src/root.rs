//! An ask's key, its stream's root, its name and its name's claim root.

use angzarr_embedded::markup::DomainMessage;
use angzarr_embedded::v1::{command_page, CommandBook, CommandPage, Cover, Fact, FactOrigin};

/// Every ask's name begins with it (Q7): no session's name may
/// (`sessions::validate_session_name`).
pub const ASK_PREFIX: &str = "ask-";

/// The fact key of the ask the engine made under its request id
/// `request_id` in the run `run` (claude's `tool_use_id`, ACP's JSON-RPC
/// request id): `approval:<run>:<request id>`, so a redelivered ask is one
/// ask.
pub fn ask_key(run: &str, request_id: &str) -> String {
    format!("approval:{run}:{request_id}")
}

/// The ask stream's root: UUIDv5(the asking session's stable id, its key).
/// A session id that is no UUID roots under the nil namespace.
pub fn ask_root(session: &str, run: &str, request_id: &str) -> uuid::Uuid {
    let namespace = uuid::Uuid::parse_str(session).unwrap_or(uuid::Uuid::nil());
    uuid::Uuid::new_v5(&namespace, ask_key(run, request_id).as_bytes())
}

/// The ask's name from a two-word harp-names mint (`tidy-wren`):
/// `ask-tidy-wren`.
pub fn ask_name(words: &str) -> String {
    format!("{ASK_PREFIX}{words}")
}

/// Whether `word` names an ask (it begins `ask-`), not a session.
pub fn is_ask_name(word: &str) -> bool {
    word.starts_with(ASK_PREFIX)
}

/// An approval's resource name, `projects/{project}/approvals/{ask}`.
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct ApprovalName {
    /// The project's harp.
    pub project: String,
    /// The ask's name.
    pub ask: String,
}

impl ApprovalName {
    /// `name` read as `projects/{project}/approvals/{ask}`; none otherwise.
    pub fn parse(name: &str) -> Option<ApprovalName> {
        let rest = name.strip_prefix("projects/")?;
        let (project, ask) = rest.split_once("/approvals/")?;
        let plain = |s: &str| !s.is_empty() && !s.contains('/');
        (plain(project) && plain(ask)).then(|| ApprovalName {
            project: project.to_string(),
            ask: ask.to_string(),
        })
    }
}

impl std::fmt::Display for ApprovalName {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "projects/{}/approvals/{}", self.project, self.ask)
    }
}

/// The root of the claim stream of the ask name `name` (its resource name):
/// the UUIDv5 of the name, as every ctxloom name's root is
/// (`sessions::root_of`).
pub fn name_root(name: &ApprovalName) -> uuid::Uuid {
    sessions::root_of(&name.to_string())
}

/// The book that records `fact` on the stream `cover` names, keyed
/// `external_id`: a redelivery is a DUPLICATE.
pub fn fact_book<M: DomainMessage>(cover: Cover, fact: &M, external_id: &str) -> CommandBook {
    CommandBook {
        cover: Some(cover),
        page: Some(CommandPage {
            body: Some(command_page::Body::Fact(Fact {
                payload: Some(
                    angzarr_embedded::Any::from_msg(&fact.to_wire()).expect("a fact encodes"),
                ),
                origin: Some(FactOrigin {
                    external_id: external_id.to_string(),
                    description: String::new(),
                }),
            })),
            ..CommandPage::default()
        }),
    }
}

#[cfg(test)]
#[path = "root.test.rs"]
mod tests;
