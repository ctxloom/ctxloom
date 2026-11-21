//! `ctxloom test library-events [--home]` (step 10a, QUESTIONS G; built
//! with `test-support` only): the cwd checkout's library tree stream (or,
//! with `--home`, the home's), read from the ONE store as the daemon
//! recorded it, printed as library_changes.feature's description has it:
//!
//! ```text
//! {"tree": "projects/<p>/checkouts/<c>/library",
//!  "events": [{"type": "TreeObserved", "sequence": n, "baseline": bool,
//!              "head_before": "<oid>", "head_after": "<oid>",
//!              "changes": [{"path", "status", "from_path", "oid"}]}]}
//! ```
//!
//! A change's `from_path` is there for a rename alone, and its `oid` (the
//! content id of the path after it) is absent when it was deleted. A tree no read
//! has named yet (no checkout harp) is `"tree": null` with no event. It
//! reads the record; it never observes, mints or records anything.

use angzarr_embedded::v1::{event_page, Cover, EventPage};
use angzarr_embedded::{EventStore, Payload, StreamRef};
use library_tree::{Change, ChangeStatus, LibraryEvent, TreeName, LIBRARY_DOMAIN};
use serde_json::{json, Value};

use crate::{TestError, Wiring};

fn read_error(detail: impl ToString) -> TestError {
    TestError::Seed {
        detail: detail.to_string(),
    }
}

/// A change's status as the record prints it.
fn status_word(status: ChangeStatus) -> &'static str {
    match status {
        ChangeStatus::Added => "added",
        ChangeStatus::Modified => "modified",
        ChangeStatus::Deleted => "deleted",
        ChangeStatus::Renamed => "renamed",
        ChangeStatus::TypeChanged => "type-changed",
    }
}

fn change_row(change: &Change) -> Value {
    let mut row = json!({"path": change.path, "status": status_word(change.status)});
    if !change.from_path.is_empty() {
        row["from_path"] = change.from_path.clone().into();
    }
    if !change.to_oid.is_empty() {
        row["oid"] = change.to_oid.clone().into();
    }
    row
}

/// An intent's step as the reader prints it (protos.md §1.4): its kind
/// (the oneof's case) and its paths.
fn step_row(step: &library_tree::Step) -> Value {
    if let Some(replace) = &step.replace {
        return json!({"kind": "replace", "path": replace.path});
    }
    if let Some(create) = &step.create {
        return json!({"kind": "create", "path": create.path});
    }
    if let Some(trash) = &step.trash {
        return json!({"kind": "trash", "path": trash.path});
    }
    if let Some(moved) = &step.r#move {
        return json!({"kind": "move", "path": moved.from, "from": moved.from, "to": moved.to});
    }
    if let Some(purged) = &step.purge {
        let slot = format!("{}/{}", ctxloom_ports::TRASH_DIR, purged.request_id);
        return json!({"kind": "purge", "path": slot});
    }
    json!({"kind": ""})
}

/// One page as the reader prints it: the event's type and its sequence,
/// and what each kind names (never content: there is none to name).
pub(crate) fn event_row(page: &EventPage) -> Option<Value> {
    let Some(event_page::Body::Event(any)) = &page.body else {
        return None;
    };
    let kind = any.type_url.rsplit('.').next().unwrap_or_default();
    let mut row = json!({"type": kind, "sequence": page.sequence});
    match LibraryEvent::from_any(any) {
        Ok(LibraryEvent::TreeObserved(observed)) => {
            let (before, after) = observed
                .external
                .map(|external| (external.head_before, external.head_after))
                .unwrap_or_default();
            row["baseline"] = observed.baseline.into();
            row["head_before"] = before.into();
            row["head_after"] = after.into();
            if let Some(intended) = observed.intended {
                row["intended"] = intended.into();
            }
            row["changes"] = observed.changes.iter().map(change_row).collect();
        }
        Ok(LibraryEvent::ChangeIntended(intended)) => {
            row["request_id"] = intended.intent.request_id.into();
            row["verb"] = intended.intent.verb.into();
            row["steps"] = intended.intent.steps.iter().map(step_row).collect();
            if !intended.intent.foreign.is_empty() {
                row["foreign"] = intended
                    .intent
                    .foreign
                    .iter()
                    .map(|file| json!({"path": file.path}))
                    .collect();
            }
        }
        Ok(LibraryEvent::StepApplied(applied)) => {
            row["request_id"] = applied.request_id.into();
            row["step"] = applied.step.into();
            row["observed"] = applied
                .observed
                .iter()
                .map(|seen| json!({"path": seen.path, "oid": seen.oid}))
                .collect();
        }
        Ok(LibraryEvent::ChangeFailed(failed)) => {
            row["request_id"] = failed.request_id.into();
            row["step"] = failed.step.into();
        }
        Ok(LibraryEvent::IntentSuperseded(superseded)) => {
            row["request_id"] = superseded.request_id.into();
        }
        Ok(LibraryEvent::HomeRepositoryMade(_)) => {}
        Ok(LibraryEvent::TreeInstalled(installed)) => {
            row["request_id"] = installed.request_id.into();
            row["step"] = installed.step.into();
            row["identity"] = installed.identity.into();
            row["sha"] = installed.sha.into();
            row["tree"] = installed.tree.into();
        }
        Ok(LibraryEvent::BundleExported(exported)) => {
            row["request_id"] = exported.request_id.into();
        }
        Err(_) => {}
    }
    Some(row)
}

/// The reader's document for `tree`'s `pages`; `"tree": null` and no
/// event for a tree not named yet.
pub(crate) fn document(tree: Option<&TreeName>, pages: &[EventPage]) -> Value {
    json!({
        "tree": tree.map(ToString::to_string),
        "events": pages.iter().filter_map(event_row).collect::<Vec<_>>(),
    })
}

impl Wiring {
    /// The tree the reader reads: the home's under `home`, else the cwd's
    /// project checkout's, its project as the daemon names it (asked, never
    /// minted); none where no checkout harp was minted, or the project has
    /// no name yet. A cwd in no project reads the home's.
    fn events_tree(&self, home: bool, version: &str) -> Result<Option<TreeName>, TestError> {
        let (_, app) = self.locate();
        let at_home = home || app.source == ctxloom_ports::Source::Home;
        let app_dir = if at_home {
            self.home().join(config_files::APP_DIR_NAME)
        } else {
            app.path
        };
        let Some(checkout) = crate::checkout_harp(self.fs().as_ref(), &app_dir)
            .map_err(|e| read_error(angzarr_diagnostic::Diagnostic::code(&e)))?
        else {
            return Ok(None);
        };
        if at_home {
            return TreeName::home(&checkout).map(Some).map_err(read_error);
        }
        let parent = crate::daemon::library_parent_if_named(self, version, crate::Minting::Never)
            .map_err(|failure| read_error(format!("{failure:?}")))?;
        let Some(project) = parent.and_then(|p| sessions::ProjectName::parse(&p).ok()) else {
            return Ok(None);
        };
        crate::checkout_tree(&project, &checkout)
            .map(Some)
            .map_err(|e| read_error(angzarr_diagnostic::Diagnostic::code(&e)))
    }

    /// The events of the cwd checkout's (or the home's) library stream, as
    /// `ctxloom test library-events` prints them.
    pub fn library_events(&self, home: bool, version: &str) -> Result<Value, TestError> {
        let tree = self.events_tree(home, version)?;
        let Some(name) = tree.as_ref() else {
            return Ok(document(None, &[]));
        };
        let runtime = tokio::runtime::Builder::new_current_thread()
            .enable_all()
            .build()
            .map_err(read_error)?;
        runtime.block_on(async {
            let stores = ctxloom_store_sqlite::Stores::new(self.fs(), self.home().to_path_buf());
            let Some(store) = stores.open_existing().await.map_err(read_error)? else {
                return Ok(document(tree.as_ref(), &[]));
            };
            let read = async {
                let cover = Cover {
                    domain: LIBRARY_DOMAIN.to_string(),
                    root: Some(name.root()),
                    edition: None,
                };
                let stream = StreamRef::try_from(&cover).map_err(read_error)?;
                let slice = store.read(stream, 0, None).await.map_err(read_error)?;
                Ok::<_, TestError>(document(tree.as_ref(), &slice.pages))
            };
            let read = read.await;
            let closed = store.close().await.map_err(read_error);
            let read = read?;
            closed?;
            Ok(read)
        })
    }
}

#[cfg(test)]
#[path = "library_events.test.rs"]
mod tests;
