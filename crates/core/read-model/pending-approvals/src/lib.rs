//! The pending approvals (step 8 U4): a read model a projector folds from
//! the approvals streams, one row per ask: what it asks to run, the options
//! it offers, where it stands (`waiting`, `answered`, `expired`,
//! `withdrawn`) and when it was asked. `session approvals` lists the asks
//! of a project (under the ROOT's project, Q-A), the approval tool's wait
//! reads an ask until it is settled, and a `HandleSync` naming
//! `pending-approvals` is answered the stream's ask as a `ctxloom.v1.Approval`.
//!
//! It is HELD in memory, like `pending-messages`: registered in the
//! daemon's kernel ([`PendingApprovals`] on the projector port, S32) and
//! caught up from the store by the daemon's loop ([`caught_up`]), so an ask
//! a daemon before this one recorded is listed too.

use std::collections::BTreeMap;
use std::sync::Arc;

use angzarr_embedded::angzarr_ports::tombstone;
use angzarr_embedded::angzarr_router::error::CodedError;
use angzarr_embedded::angzarr_router::hpb::{ProjectRequest, ProjectResponse};
use angzarr_embedded::v1::{event_page, Cover, EventBook};
use angzarr_embedded::{Any, CatchUp, Payload, PayloadError, ProjectorHandler, Timestamp};
use approvals::{kind_word, ApprovalEvent, ApprovalOption, SessionRef, APPROVALS_DOMAIN};
use held_projection::{Fill, Held, Lock, Model};

/// The projector's name, as a `HandleSync` names it.
pub const PENDING_APPROVALS: &str = "pending-approvals";

/// Where an ask stands.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub enum State {
    #[default]
    Waiting,
    Answered,
    Expired,
    Withdrawn,
}

impl State {
    /// The word the API says it in.
    pub fn word(self) -> &'static str {
        match self {
            State::Waiting => "waiting",
            State::Answered => "answered",
            State::Expired => "expired",
            State::Withdrawn => "withdrawn",
        }
    }
}

/// An ask, as its stream says it.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct ApprovalRow {
    /// Its stream's root, as a UUID's text.
    pub uid: String,
    /// `projects/{project}/approvals/{ask}`.
    pub name: String,
    pub ask_id: String,
    /// The asking session, and its root, by reference and by name.
    pub child: Option<SessionRef>,
    pub child_name: String,
    pub root: Option<SessionRef>,
    pub root_name: String,
    pub run: String,
    pub engine: String,
    /// The agent the asking session was planned for.
    pub agent: String,
    pub tool_name: String,
    pub input_excerpt: String,
    pub engine_request_id: String,
    pub options: Vec<ApprovalOption>,
    pub timeout_seconds: u32,
    /// When it was asked: its event's recorded instant.
    pub ask_time: Option<Timestamp>,
    pub state: State,
    /// When answered: the option's id and kind, and the message.
    pub option: String,
    pub option_kind: String,
    pub message: String,
}

impl ApprovalRow {
    /// When its time runs out: its asking plus its timeout.
    pub fn expire_time(&self) -> Option<Timestamp> {
        self.ask_time.map(|asked| Timestamp {
            seconds: asked.seconds + i64::from(self.timeout_seconds),
            nanos: asked.nanos,
        })
    }

    /// The row as the API's resource message.
    pub fn resource(&self) -> daemon_proto::v1::Approval {
        daemon_proto::v1::Approval {
            name: self.name.clone(),
            child: self.child_name.clone(),
            root: self.root_name.clone(),
            engine: self.engine.clone(),
            tool_name: self.tool_name.clone(),
            input_excerpt: self.input_excerpt.clone(),
            ask_time: self.ask_time.map(Into::into),
            expire_time: self.expire_time().map(Into::into),
            message: self.message.clone(),
            options: self
                .options
                .iter()
                .map(|option| daemon_proto::v1::ApprovalOption {
                    id: option.id.clone(),
                    kind: kind_word(option.kind).to_string(),
                    label: option.label.clone(),
                    rule: option.rule.clone(),
                })
                .collect(),
            state: self.state.word().to_string(),
            option: self.option.clone(),
            uid: self.uid.clone(),
            option_kind: self.option_kind.clone(),
            timeout_seconds: self.timeout_seconds,
            agent: self.agent.clone(),
        }
    }
}

/// The pending approvals.
#[derive(Default)]
pub struct ApprovalIndex {
    /// Each ask stream's row, by its root.
    rows: BTreeMap<Vec<u8>, ApprovalRow>,
}

fn root_of(cover: Option<&Cover>) -> Vec<u8> {
    cover
        .and_then(|cover| cover.root.as_ref())
        .map(|root| root.value.to_vec())
        .unwrap_or_default()
}

fn uid_of(root: &[u8]) -> String {
    uuid::Uuid::from_slice(root)
        .map(|id| id.hyphenated().to_string())
        .unwrap_or_default()
}

/// Oldest first, then by name.
fn ordered(mut rows: Vec<ApprovalRow>) -> Vec<ApprovalRow> {
    rows.sort_by(|a, b| {
        let at = |row: &ApprovalRow| row.ask_time.map(|t| (t.seconds, t.nanos));
        at(a).cmp(&at(b)).then_with(|| a.name.cmp(&b.name))
    });
    rows
}

impl ApprovalIndex {
    /// An empty index.
    pub fn new() -> ApprovalIndex {
        ApprovalIndex::default()
    }

    /// Fold `book`, one stream's events after the last projected of it. A
    /// tombstone (a purge's `StreamClosed`) closes the stream: it leaves the
    /// index. Another domain's book is none of its.
    pub fn project(&mut self, book: &EventBook) -> Result<(), PayloadError> {
        if book.cover.as_ref().map(|c| c.domain.as_str()) != Some(APPROVALS_DOMAIN) {
            return Ok(());
        }
        let root = root_of(book.cover.as_ref());
        for page in &book.pages {
            if tombstone::is(page) {
                self.rows.remove(&root);
                continue;
            }
            let Some(event_page::Body::Event(event)) = &page.body else {
                continue;
            };
            let recorded = page.meta.as_ref().and_then(|meta| meta.recorded_at);
            match ApprovalEvent::from_any(event)? {
                ApprovalEvent::ApprovalAsked(asked) => {
                    self.rows.insert(
                        root.clone(),
                        ApprovalRow {
                            uid: uid_of(&root),
                            name: asked.name,
                            ask_id: asked.ask_id,
                            child: Some(asked.child),
                            child_name: asked.child_name,
                            root: Some(asked.root),
                            root_name: asked.root_name,
                            run: asked.run,
                            engine: asked.engine,
                            agent: asked.agent,
                            tool_name: asked.tool_name,
                            input_excerpt: asked.input_excerpt,
                            engine_request_id: asked.engine_request_id,
                            options: asked.options,
                            timeout_seconds: asked.timeout_seconds,
                            ask_time: recorded,
                            ..ApprovalRow::default()
                        },
                    );
                }
                ApprovalEvent::ApprovalAnswered(answered) => {
                    if let Some(row) = self.rows.get_mut(&root) {
                        row.state = State::Answered;
                        row.option = answered.option;
                        row.option_kind = kind_word(answered.kind).to_string();
                        row.message = answered.message;
                    }
                }
                ApprovalEvent::ApprovalExpired(_) => {
                    if let Some(row) = self.rows.get_mut(&root) {
                        row.state = State::Expired;
                    }
                }
                ApprovalEvent::ApprovalWithdrawn(_) => {
                    if let Some(row) = self.rows.get_mut(&root) {
                        row.state = State::Withdrawn;
                    }
                }
            }
        }
        Ok(())
    }

    /// The ask whose stream's root is `uid`.
    pub fn by_uid(&self, uid: &str) -> Option<ApprovalRow> {
        self.rows.values().find(|row| row.uid == uid).cloned()
    }

    /// The ask named `name` (`projects/{project}/approvals/{ask}`).
    pub fn named(&self, name: &str) -> Option<ApprovalRow> {
        self.rows.values().find(|row| row.name == name).cloned()
    }

    /// The ask the session `session` (its stable id) made under the
    /// engine's request id `request_id`: a retried create's.
    pub fn asked_by(&self, session: &str, request_id: &str) -> Option<ApprovalRow> {
        self.rows
            .values()
            .find(|row| {
                row.engine_request_id == request_id
                    && row.child.as_ref().is_some_and(|c| c.session == session)
            })
            .cloned()
    }

    /// The asks under the project `parent` (`projects/{project}`, the root's
    /// project), oldest first: only those under the root session named
    /// `root` when one is, and only those waiting when `waiting`.
    pub fn listed(&self, parent: &str, root: Option<&str>, waiting: bool) -> Vec<ApprovalRow> {
        let under = format!("{parent}/approvals/");
        ordered(
            self.rows
                .values()
                .filter(|row| row.name.starts_with(&under))
                .filter(|row| root.is_none_or(|root| row.root_name == root))
                .filter(|row| !waiting || row.state == State::Waiting)
                .cloned()
                .collect(),
        )
    }
}

impl Model for ApprovalIndex {
    const NAME: &'static str = PENDING_APPROVALS;

    fn project(&mut self, book: &EventBook) -> Result<(), PayloadError> {
        ApprovalIndex::project(self, book)
    }

    /// The stream's ask, as the resource message.
    fn view(&self, cover: &Cover) -> Option<Any> {
        let row = self.rows.get(&root_of(Some(cover)))?;
        Any::from_msg(&row.resource()).ok()
    }
}

/// The index on angzarr-embedded's projector port: every clone is the same
/// fold ([`Held`]), so one instance can be both a catch-up's and the
/// kernel's.
#[derive(Clone, Default)]
pub struct PendingApprovals(Held<ApprovalIndex>);

impl PendingApprovals {
    /// An empty index whose gaps are read through `fill`.
    pub fn filled(fill: Arc<dyn Fill>) -> PendingApprovals {
        PendingApprovals(Held::new(Some(fill)))
    }

    /// The index, locked.
    pub fn index(&self) -> Lock<'_, ApprovalIndex> {
        self.0.lock()
    }

    /// A watcher of the index: woken each time a fold changes it (a push).
    pub fn watch(&self) -> std::sync::mpsc::Receiver<()> {
        self.0.watch()
    }

    /// As [`Self::watch`], woken through `wake`.
    pub fn watch_into(&self, wake: std::sync::mpsc::Sender<()>) {
        self.0.watch_into(wake);
    }
}

impl ProjectorHandler for PendingApprovals {
    fn name(&self) -> &str {
        self.0.name()
    }

    async fn dispatch(&self, request: &ProjectRequest) -> Result<ProjectResponse, CodedError> {
        self.0.dispatch(request).await
    }
}

/// `model` caught up from a store: its catch-up, sharing its fold (S32).
pub fn caught_up(model: PendingApprovals) -> CatchUp<PendingApprovals> {
    CatchUp::new(&SOURCES, model).expect("the pending approvals read the approvals domain")
}

/// The domains the index folds.
pub const SOURCES: [&str; 1] = [APPROVALS_DOMAIN];

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
