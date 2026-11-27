//! A project's store from before the one store (T17, S48): its resolution
//! refuses it, the daemon being the one owner of every store. Nothing is read
//! from it or imported; the refusal names the file to remove.

use std::sync::Arc;

use crate::Api;

/// Where the old per-project stores would be, which only the store adapter
/// knows: the daemon's composition answers.
pub trait LegacyStores: Send + Sync {
    /// The file `project_id` still has of its own, from before the one store;
    /// none where it has none (or it cannot be told).
    fn held(&self, project_id: &str) -> Option<String>;
}

impl<E> Api<E> {
    /// The API, refusing a project that `legacy` says still has a store of
    /// its own.
    #[must_use]
    pub fn refusing_legacy_stores(self, legacy: Arc<dyn LegacyStores>) -> Api<E> {
        Api {
            legacy_stores: Some(legacy),
            ..self
        }
    }
}

#[cfg(test)]
#[path = "legacy_stores.test.rs"]
mod tests;
