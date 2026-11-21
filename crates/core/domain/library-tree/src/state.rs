//! A library tree's state, folded from its events: what was last observed,
//! the intents not yet done, and each resource's `{last sequence, oid}`
//! (G4 superseded). ONE fold, the aggregate's and `library-checkout`'s
//! alike, so the read side answers the same token `decide` judges.

use std::collections::BTreeMap;

use angzarr_embedded::v1::event_meta::Causation;
use angzarr_embedded::v1::EventMeta;
use library_domain_proto::v1 as wire;

use crate::{
    ChangeFailed, ChangeIntended, ChangeStatus, Intent, IntentSuperseded, LibraryEvent,
    StepApplied, TreeKind, TreeObserved,
};

/// The library domain's name: every tree stream's `Cover.domain`.
pub const LIBRARY_DOMAIN: &str = "library";

/// A resource's place in the record: the sequence of the last event that
/// named the path, and the git id that event gave it (empty when it deleted
/// the path or moved it away). The sequence is the path's etag.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Resource {
    pub sequence: u64,
    pub oid: String,
}

/// How long a kept trash slot is kept before the daemon purges it (G12,
/// N = 7 days).
pub const KEEP_DAYS: u32 = 7;

/// A trash slot (L12): the paths a removal took into
/// `state/trash/<request id>/`, when its intent was recorded (seconds since
/// the epoch, the page's own time; decide reads no clock), and whether it
/// is kept ([`KEEP_DAYS`], then purged) or a sequence's own (its last step
/// removes it).
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Slot {
    pub paths: Vec<String>,
    pub trashed_at: i64,
    pub kept: bool,
}

/// A library tree as its stream holds it.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Library {
    /// `projects/{p}/checkouts/{c}/library` or `users/me/library`.
    pub name: String,
    /// None until the first observation.
    pub kind: Option<TreeKind>,
    /// The commit last seen; empty for a repository with none yet.
    pub head: String,
    /// The library's tree id at `head`; empty when none.
    pub tree: String,
    /// What git saw differing from `head`, by path: its blob id now, empty
    /// when deleted.
    pub dirty: BTreeMap<String, String>,
    /// The cause last recorded: a fact's external id, or a command's id.
    pub last_cause: String,
    /// The intents not yet done, by request id (10b, U3).
    pub pending: BTreeMap<String, Intent>,
    /// Trash slots, by request id (10b, U8, G12).
    pub trashed: BTreeMap<String, Slot>,
    /// Per path, the last event that named it.
    pub resources: BTreeMap<String, Resource>,
    /// The sequence of the tree's last baseline.
    pub baseline_sequence: u64,
    /// Whether a baseline was ever recorded.
    pub baselined: bool,
    /// Each foreign writer's file (step 10c U10, L15), project-relative,
    /// at the sequence of the step that wrote it: a record only, never
    /// observed and never a token.
    pub foreign: BTreeMap<String, Resource>,
}

/// The cause an event's page names: a fact's external id, else the
/// producing command's id as UUID text; empty when the page says neither.
pub fn cause_of(meta: Option<&EventMeta>) -> String {
    match meta.and_then(|meta| meta.causation.as_ref()) {
        Some(Causation::Fact(fact)) => fact
            .origin
            .as_ref()
            .map(|origin| origin.external_id.clone())
            .unwrap_or_default(),
        Some(Causation::Command(command)) => command
            .command_id
            .as_ref()
            .and_then(|id| uuid::Uuid::from_slice(&id.value).ok())
            .map(|id| id.to_string())
            .unwrap_or_default(),
        None => String::new(),
    }
}

impl Library {
    /// The token (etag) of `path`: the greatest sequence among the entries
    /// at `path` or under its directory, else the baseline's. A file's is
    /// its own entry's; a bundle's the latest of its files'.
    pub fn token(&self, path: &str) -> u64 {
        let under = format!("{}/", path.trim_end_matches('/'));
        self.resources
            .iter()
            .filter(|(key, _)| key.as_str() == path || key.starts_with(&under))
            .map(|(_, resource)| resource.sequence)
            .fold(self.baseline_sequence, u64::max)
    }

    /// Whether the record knows `path` (or something under it) to be there
    /// now: named by an event with an oid. A path no event named since the
    /// baseline is not known either way ([`Library::known_absent`]).
    pub fn known_present(&self, path: &str) -> bool {
        let under = format!("{}/", path.trim_end_matches('/'));
        self.resources.iter().any(|(key, resource)| {
            (key.as_str() == path || key.starts_with(&under)) && !resource.oid.is_empty()
        })
    }

    /// Whether the record knows `path` to be gone: named, and every entry
    /// at or under it emptied.
    pub fn known_absent(&self, path: &str) -> bool {
        let under = format!("{}/", path.trim_end_matches('/'));
        let mut named = self
            .resources
            .iter()
            .filter(|(key, _)| key.as_str() == path || key.starts_with(&under))
            .peekable();
        named.peek().is_some() && named.all(|(_, resource)| resource.oid.is_empty())
    }

    /// Fold `event`, recorded on a page at `sequence` with `meta`: its
    /// cause ([`cause_of`]) and its time (a trash slot's).
    pub fn apply_page(&mut self, event: &LibraryEvent, sequence: u64, meta: Option<&EventMeta>) {
        let at = meta
            .and_then(|meta| meta.recorded_at.as_ref())
            .map_or(0, |at| at.seconds);
        self.fold(event, sequence, cause_of(meta), at);
    }

    /// Fold `event`, recorded at `sequence` and caused by `cause`, at no
    /// time: a test's.
    #[cfg(test)]
    pub(crate) fn apply(&mut self, event: &LibraryEvent, sequence: u64, cause: String) {
        self.fold(event, sequence, cause, 0);
    }

    fn fold(&mut self, event: &LibraryEvent, sequence: u64, cause: String, at: i64) {
        match event {
            LibraryEvent::TreeObserved(observed) => self.observed(observed, sequence),
            LibraryEvent::ChangeIntended(intended) => self.intended(intended, at),
            LibraryEvent::StepApplied(applied) => self.applied(applied, sequence),
            LibraryEvent::ChangeFailed(failed) => self.failed(failed),
            LibraryEvent::IntentSuperseded(superseded) => self.superseded(superseded),
            // Never emitted since the git-free ruling: a 10b stream's is
            // nothing to fold (its cause still chains).
            LibraryEvent::HomeRepositoryMade(_) => {}
            // The read model keeps it; the tree is as it was.
            LibraryEvent::BundleExported(_) => {}
            LibraryEvent::TreeInstalled(installed) => self.installed(installed, sequence),
        }
        self.last_cause = cause;
    }

    /// The tree's listing as its record holds it (step 10c U13 rework):
    /// every path at its content id, one a step took away (its id empty)
    /// left out.
    pub fn listed(&self) -> BTreeMap<String, String> {
        self.dirty
            .iter()
            .filter(|(_, oid)| !oid.is_empty())
            .map(|(path, oid)| (path.clone(), oid.clone()))
            .collect()
    }

    /// The dirty set once `left` (paths and the ids a step left there,
    /// empty when it took them away) is laid over this one: what a step
    /// done makes it. ctxloom never commits in a project, so a path it
    /// writes differs from HEAD, and one it takes away is gone from it.
    pub fn dirty_after(&self, left: &[crate::DirtyPath]) -> BTreeMap<String, String> {
        let mut dirty = self.dirty.clone();
        for path in left {
            dirty.insert(path.path.clone(), path.oid.clone());
        }
        dirty
    }

    fn observed(&mut self, observed: &TreeObserved, sequence: u64) {
        if let Some(request) = &observed.intended {
            // The intent's own change, seen: its step is done.
            if let Some(intent) = self.pending.get_mut(request) {
                intent.done += 1;
                if intent.done as usize >= intent.steps.len() {
                    self.pending.remove(request);
                }
            }
        }
        if self.name.is_empty() {
            self.name = observed.name.clone();
            self.kind = Some(crate::kind_of(&observed.name));
        }
        if observed.baseline {
            self.resources.clear();
            self.baseline_sequence = sequence;
            self.baselined = true;
        }
        if let Some(external) = &observed.external {
            self.head = external.head_after.clone();
        }
        self.tree = observed.tree_after.clone();
        self.dirty = observed
            .dirty_after
            .iter()
            .map(|dirty| (dirty.path.clone(), dirty.oid.clone()))
            .collect();
        for change in &observed.changes {
            if change.status == ChangeStatus::Renamed && !change.from_path.is_empty() {
                self.resources.insert(
                    change.from_path.clone(),
                    Resource {
                        sequence,
                        oid: String::new(),
                    },
                );
            }
            self.resources.insert(
                change.path.clone(),
                Resource {
                    sequence,
                    oid: change.to_oid.clone(),
                },
            );
        }
    }

    fn intended(&mut self, intended: &ChangeIntended, at: i64) {
        let intent = &intended.intent;
        for trash in intent.steps.iter().filter_map(|step| step.trash.as_ref()) {
            let slot = self
                .trashed
                .entry(intent.request_id.clone())
                .or_insert_with(|| Slot {
                    paths: Vec::new(),
                    trashed_at: at,
                    kept: trash.keep,
                });
            slot.paths.push(trash.path.clone());
        }
        self.pending
            .insert(intent.request_id.clone(), intent.clone());
    }

    fn applied(&mut self, applied: &StepApplied, sequence: u64) {
        let foreign = self
            .pending
            .get(&applied.request_id)
            .and_then(|intent| intent.steps.get(applied.step as usize))
            .and_then(|step| step.replace.as_ref())
            .is_some_and(|replace| replace.foreign);
        if foreign {
            // Another tool's file: recorded apart, and the library (its
            // dirty set and its tokens) is as it was.
            for observed in &applied.observed {
                self.foreign.insert(
                    observed.path.clone(),
                    Resource {
                        sequence,
                        oid: observed.oid.clone(),
                    },
                );
            }
        } else {
            self.dirty = self.dirty_after(&applied.observed);
        }
        for observed in applied.observed.iter().filter(|_| !foreign) {
            self.resources.insert(
                observed.path.clone(),
                Resource {
                    sequence,
                    oid: observed.oid.clone(),
                },
            );
        }
        let Some(intent) = self.pending.get_mut(&applied.request_id) else {
            return;
        };
        if let Some(purged) = intent
            .steps
            .get(applied.step as usize)
            .and_then(|step| step.purge.as_ref())
        {
            self.trashed.remove(&purged.request_id);
        }
        intent.done = intent.done.max(applied.step + 1);
        if intent.done as usize >= intent.steps.len() {
            self.pending.remove(&applied.request_id);
        }
    }

    /// A tree installed into the cache (step 10c U4): its create step done,
    /// and its directory's path tied to this sequence and the tree's id.
    fn installed(&mut self, installed: &crate::TreeInstalled, sequence: u64) {
        let Some(intent) = self.pending.get_mut(&installed.request_id) else {
            return;
        };
        if let Some(create) = intent
            .steps
            .get(installed.step as usize)
            .and_then(|step| step.create.as_ref())
        {
            self.resources.insert(
                create.path.clone(),
                Resource {
                    sequence,
                    oid: installed.tree.clone(),
                },
            );
        }
        intent.done = intent.done.max(installed.step + 1);
        if intent.done as usize >= intent.steps.len() {
            self.pending.remove(&installed.request_id);
        }
    }

    fn failed(&mut self, failed: &ChangeFailed) {
        // A trash that failed moved nothing: its slot holds nothing.
        let trashing = self.pending.get(&failed.request_id).is_some_and(|intent| {
            intent
                .steps
                .get(failed.step as usize)
                .is_some_and(|step| step.trash.is_some())
        });
        if trashing && failed.step == 0 {
            self.trashed.remove(&failed.request_id);
        }
        self.pending.remove(&failed.request_id);
    }

    fn superseded(&mut self, superseded: &IntentSuperseded) {
        self.pending.remove(&superseded.request_id);
    }

    /// The state as its wire message (`ctxloom.library.v1.LibraryState`):
    /// what `library-checkout` answers a `HandleSync` with.
    pub fn wire(&self) -> wire::LibraryState {
        wire::LibraryState {
            name: self.name.clone(),
            kind: self.kind.map(i32::from).unwrap_or_default(),
            head: self.head.clone(),
            tree: self.tree.clone(),
            dirty: self.dirty.clone(),
            last_cause: self.last_cause.clone(),
            pending: self
                .pending
                .iter()
                .map(|(id, intent)| (id.clone(), wire::Intent::from(intent.clone())))
                .collect(),
            trashed: self
                .trashed
                .iter()
                .map(|(id, slot)| {
                    (
                        id.clone(),
                        wire::Trashed {
                            request_id: id.clone(),
                            paths: slot.paths.clone(),
                            trashed_at: Some(angzarr_embedded::Timestamp {
                                seconds: slot.trashed_at,
                                nanos: 0,
                            }),
                            kept: slot.kept,
                        },
                    )
                })
                .collect(),
            resources: self
                .resources
                .iter()
                .map(|(path, resource)| {
                    (
                        path.clone(),
                        wire::Resource {
                            sequence: resource.sequence,
                            oid: resource.oid.clone(),
                        },
                    )
                })
                .collect(),
            baseline_sequence: self.baseline_sequence,
            foreign: self
                .foreign
                .iter()
                .map(|(path, resource)| {
                    (
                        path.clone(),
                        wire::Resource {
                            sequence: resource.sequence,
                            oid: resource.oid.clone(),
                        },
                    )
                })
                .collect(),
        }
    }
}

#[cfg(test)]
#[path = "state.test.rs"]
mod tests;
