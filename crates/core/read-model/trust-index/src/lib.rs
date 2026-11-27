//! The trust index (step 8 U1): a read model a projector folds from the
//! trust streams, one row per project's trust (`projects/{harp}/trust`):
//! whether it is trusted now, who granted or took it back last, and when it
//! was granted. `trust list` lists the projects trusted NOW (a revoked trust
//! is not listed), and a `HandleSync` naming `trust-index` is answered the
//! stream's trust as a `ctxloom.v1.Trust`.
//!
//! It is HELD in memory, like `pending-messages`: registered in the daemon's
//! kernel ([`TrustIndexes`] on the projector port, S32) and caught up from
//! the store by the daemon's loop ([`caught_up`]), so a trust a daemon
//! before this one recorded is listed too.

use std::collections::BTreeMap;
use std::sync::Arc;

use angzarr_embedded::angzarr_ports::tombstone;
use angzarr_embedded::angzarr_router::error::CodedError;
use angzarr_embedded::angzarr_router::hpb::{ProjectRequest, ProjectResponse};
use angzarr_embedded::v1::{event_page, Cover, EventBook};
use angzarr_embedded::{Any, CatchUp, Payload, PayloadError, ProjectorHandler, Timestamp};
use held_projection::{Fill, Held, Lock, Model};
use trust::{TrustEvent, TRUST_DOMAIN};

/// A project's trust, as its stream says it.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct TrustRow {
    /// `projects/{harp}/trust`.
    pub name: String,
    pub project_id: String,
    pub trusted: bool,
    /// Who granted it, or took it back, last.
    pub user: String,
    /// When it was granted, while it is trusted.
    pub grant_time: Option<Timestamp>,
}

impl TrustRow {
    /// The row as the API's resource message.
    pub fn resource(&self) -> daemon_proto::v1::Trust {
        daemon_proto::v1::Trust {
            name: self.name.clone(),
            trusted: self.trusted,
            actor: self.user.clone(),
            grant_time: self.grant_time.map(Into::into),
            ..daemon_proto::v1::Trust::default()
        }
    }
}

/// The trust index.
#[derive(Default)]
pub struct TrustIndex {
    /// Each trust stream's row, by its root.
    rows: BTreeMap<Vec<u8>, TrustRow>,
}

fn root_of(cover: Option<&Cover>) -> Vec<u8> {
    cover
        .and_then(|cover| cover.root.as_ref())
        .map(|root| root.value.to_vec())
        .unwrap_or_default()
}

impl TrustIndex {
    /// An empty index.
    pub fn new() -> TrustIndex {
        TrustIndex::default()
    }

    /// Fold `book`, one stream's events after the last projected of it. A
    /// tombstone (a purge's `StreamClosed`) closes the stream: it leaves the
    /// index. Another domain's book is none of its.
    pub fn project(&mut self, book: &EventBook) -> Result<(), PayloadError> {
        if book.cover.as_ref().map(|c| c.domain.as_str()) != Some(TRUST_DOMAIN) {
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
            let row = self.rows.entry(root.clone()).or_default();
            match TrustEvent::from_any(event)? {
                TrustEvent::TrustGranted(granted) => {
                    *row = TrustRow {
                        name: granted.name,
                        project_id: granted.project_id,
                        trusted: true,
                        user: granted.actor.user,
                        grant_time: recorded,
                    };
                }
                TrustEvent::TrustRevoked(revoked) => {
                    *row = TrustRow {
                        name: revoked.name,
                        project_id: revoked.project_id,
                        trusted: false,
                        user: revoked.actor.user,
                        grant_time: None,
                    };
                }
            }
        }
        Ok(())
    }

    /// The trust named `name` (`projects/{harp}/trust`), if any was ever
    /// granted.
    pub fn named(&self, name: &str) -> Option<TrustRow> {
        self.rows.values().find(|row| row.name == name).cloned()
    }

    /// Whether the project `project_id` is trusted now (step 8 U2: what a
    /// launch in it is judged by). An empty id is no project's.
    pub fn trusts(&self, project_id: &str) -> bool {
        !project_id.is_empty()
            && self
                .rows
                .values()
                .any(|row| row.trusted && row.project_id == project_id)
    }

    /// The trusts given now, by name: a revoked one is not among them.
    pub fn trusted_now(&self) -> Vec<TrustRow> {
        let mut rows: Vec<TrustRow> = self
            .rows
            .values()
            .filter(|row| row.trusted)
            .cloned()
            .collect();
        rows.sort_by(|a, b| a.name.cmp(&b.name));
        rows
    }
}

impl Model for TrustIndex {
    const NAME: &'static str = "trust-index";

    fn project(&mut self, book: &EventBook) -> Result<(), PayloadError> {
        TrustIndex::project(self, book)
    }

    /// The stream's trust, as the resource message.
    fn view(&self, cover: &Cover) -> Option<Any> {
        let row = self.rows.get(&root_of(Some(cover)))?;
        Any::from_msg(&row.resource()).ok()
    }
}

/// The index on angzarr-embedded's projector port: every clone is the same
/// fold ([`Held`]), so one instance can be both a catch-up's and the
/// kernel's.
#[derive(Clone, Default)]
pub struct TrustIndexes(Held<TrustIndex>);

impl TrustIndexes {
    /// An empty index whose gaps are read through `fill`.
    pub fn filled(fill: Arc<dyn Fill>) -> TrustIndexes {
        TrustIndexes(Held::new(Some(fill)))
    }

    /// The index, locked.
    pub fn index(&self) -> Lock<'_, TrustIndex> {
        self.0.lock()
    }
}

impl ProjectorHandler for TrustIndexes {
    fn name(&self) -> &str {
        self.0.name()
    }

    async fn dispatch(&self, request: &ProjectRequest) -> Result<ProjectResponse, CodedError> {
        self.0.dispatch(request).await
    }
}

/// `model` caught up from a store: its catch-up, sharing its fold (S32).
pub fn caught_up(model: TrustIndexes) -> CatchUp<TrustIndexes> {
    CatchUp::new(&SOURCES, model).expect("the trust index reads the trust domain")
}

/// The domains the index folds.
pub const SOURCES: [&str; 1] = [TRUST_DOMAIN];

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
