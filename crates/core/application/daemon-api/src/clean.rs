//! `clean` served (step 10b U9; protos.md §3): the project's regenerable
//! cache, its three trees (`reclaim_application::clean::TARGETS`), each one
//! present taken into the checkout's trash and KEPT there 7 days (G12),
//! then purged by the daemon. A clean is one write: its trashes one T28
//! sequence (one alone is a lone trash), intended under the request id,
//! made one at a time and resumed on a retry, so a retry of a clean done
//! takes nothing again.

use std::sync::Arc;

use daemon_proto::v1;
use library_api::writes::{Lands, Shape, Shaped};
use library_checkout::End;
use reclaim_application::{CleanReport, Cleaner};
use run_ports::Edge;

use crate::library_writes::{landing_of, notice_of};
use crate::server::Api;
use daemon_api_contract::ApiError;

/// A project tree's regenerable cache, by the tree's directory (its
/// `.ctxloom`).
pub trait Caches: Send + Sync {
    fn cache(&self, tree: &str) -> Arc<dyn ctxloom_ports::ProjectCache>;
}

/// The command a clean's preview names.
pub(crate) const APPLY_COMMAND: &str = "ctxloom clean --yes";

/// The verb a clean's intent is recorded under.
const VERB: &str = "clean";

/// What a clean of `report`'s present targets is, each relative to its
/// tree: none present, nothing; one, a lone trash; several, the sequence of
/// their trashes (kept: it purges no slot of its own).
pub(crate) fn shape_of(report: &CleanReport) -> Option<Shape> {
    let mut trashes: Vec<Shape> = report
        .targets
        .iter()
        .filter(|target| target.present)
        .map(|target| Shape::Trash {
            path: target
                .rel
                .strip_prefix(".ctxloom/")
                .unwrap_or(&target.rel)
                .to_string(),
            files: Vec::new(),
        })
        .collect();
    match trashes.len() {
        0 => None,
        1 => trashes.pop(),
        _ => Some(Shape::Sequence(trashes)),
    }
}

/// `report` as `Clean` answers it: its targets present, each target as the
/// report says it, `removed` where `applied` took it; a preview names the
/// command that applies it.
pub(crate) fn response(report: &CleanReport, applied: bool) -> v1::CleanResponse {
    v1::CleanResponse {
        targets: report
            .targets
            .iter()
            .filter(|target| target.present)
            .map(|target| target.rel.clone())
            .collect(),
        applied,
        apply_command: if applied {
            String::new()
        } else {
            APPLY_COMMAND.to_string()
        },
        items: report
            .targets
            .iter()
            .map(|target| v1::CleanTarget {
                rel: target.rel.clone(),
                path: target.path.clone(),
                present: target.present,
                bytes: target.bytes,
                rebuild: target.rebuild.clone(),
                removed: applied && target.present,
            })
            .collect(),
        bytes: report.bytes,
        ..v1::CleanResponse::default()
    }
}

impl<E: Edge> Api<E> {
    /// `Clean` (step 10b U9): the trees observed as a write's are, the
    /// cache measured, then (unless a preview) its present trees taken into
    /// the trash, one write.
    pub(crate) async fn serve_clean(
        &self,
        request: v1::CleanRequest,
    ) -> Result<v1::CleanResponse, ApiError> {
        self.open()?;
        let (_, trees) = self.write_site(&request).await?;
        let (observations, _) = self.observing.as_ref().ok_or_else(|| ApiError::Failed {
            detail: "this daemon observes no library".to_string(),
        })?;
        let mut notices: Vec<v1::Notice> = trees.notices.iter().map(notice_of).collect();
        let landing = landing_of(&trees.seen, Lands::Content).ok_or_else(|| ApiError::Failed {
            detail: "the tree a clean takes from was not observed".to_string(),
        })?;
        let caches = self.caches.as_ref().ok_or_else(|| ApiError::Failed {
            detail: "this daemon cleans no cache".to_string(),
        })?;
        let report = Cleaner::new(caches.cache(landing.site.tree()))
            .clean(false)
            .map_err(|error| ApiError::diagnosed(&error))?;
        let known = (!request.request_id.is_empty())
            .then(|| observations.known(&request.request_id))
            .flatten();
        if known.as_ref().is_some_and(|known| known.end == End::Done) {
            // A retry of a clean done: it took what it took.
            return Ok(v1::CleanResponse {
                notices,
                ..response(&report, true)
            });
        }
        let shape = shape_of(&report);
        if request.validate_only || shape.is_none() {
            return Ok(v1::CleanResponse {
                notices,
                ..response(&report, !request.validate_only)
            });
        }
        let pending = known.and_then(|known| match known.end {
            End::Pending { done } => Some((done, known.intent.steps)),
            _ => None,
        });
        let shaped = Shaped {
            lands: Lands::Content,
            shape: shape.unwrap_or(Shape::Purge),
        };
        let (_, trashed) = self
            .carry_out(
                &landing,
                (VERB, ""),
                &shaped,
                (&request.request_id, pending),
            )
            .await?;
        notices.extend(trashed);
        Ok(v1::CleanResponse {
            notices,
            ..response(&report, true)
        })
    }
}

#[cfg(test)]
#[path = "clean.test.rs"]
mod tests;
