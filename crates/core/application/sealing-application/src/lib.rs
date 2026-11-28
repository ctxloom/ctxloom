//! Sealing as the launch use cases ask for it (owner ruling 2026-11-27, per
//! project): a project's secret is sealed with ITS key set, which the
//! secrets port gives only for evidence this layer gathers first (where the
//! project's live `.env` is, and whether the user trusts it). The first time
//! a key set is used its key ids are told (`KeysSeen`, a fact to the
//! projects aggregate: ids and fingerprints, never keys). A value sealed to a
//! key the project's set no longer holds is a typed diagnostic naming the key
//! and the project.

use std::sync::Arc;

use run_ports::{KeySet, KeysSeen, ProjectFacts, ProjectSecrets, Sealer, Sealing, SealingError};

/// [`Sealing`] over a project's facts, secrets and sealer.
pub struct ProjectSealing {
    facts: Arc<dyn ProjectFacts>,
    secrets: Arc<dyn ProjectSecrets>,
    sealer: Arc<dyn Sealer>,
    seen: Arc<dyn KeysSeen>,
}

/// The file a project with no known root names in a refusal.
const KEY_FILE: &str = ".env";

impl ProjectSealing {
    pub fn new(
        facts: Arc<dyn ProjectFacts>,
        secrets: Arc<dyn ProjectSecrets>,
        sealer: Arc<dyn Sealer>,
        seen: Arc<dyn KeysSeen>,
    ) -> ProjectSealing {
        ProjectSealing {
            facts,
            secrets,
            sealer,
            seen,
        }
    }

    /// `project`'s keys, announced the first time they are used.
    fn keys(&self, project: &str) -> Result<KeySet, SealingError> {
        let evidence = self
            .facts
            .evidence(project)
            .ok_or_else(|| SealingError::NoKey {
                project: project.to_string(),
                file: KEY_FILE.to_string(),
            })?;
        let keys = self.secrets.key_set(project, &evidence)?;
        if keys.announce() {
            self.seen.seen(project, &keys);
        }
        Ok(keys)
    }
}

impl Sealing for ProjectSealing {
    fn seal(&self, project: &str, field: &str, plain: &[u8]) -> Result<Vec<u8>, SealingError> {
        self.sealer.seal(&self.keys(project)?, field, plain)
    }

    fn open(&self, project: &str, field: &str, sealed: &[u8]) -> Result<Vec<u8>, SealingError> {
        self.sealer
            .open(&self.keys(project)?, field, sealed)
            .map_err(|error| match error {
                SealingError::UnknownKey { key_id, .. } => SealingError::UnknownKey {
                    project: project.to_string(),
                    key_id,
                },
                other => other,
            })
    }
}

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
