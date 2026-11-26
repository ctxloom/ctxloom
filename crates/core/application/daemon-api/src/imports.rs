//! `ImportSessions` (ONE WRITER): Go's sessions of this account, imported by
//! the daemon. It reads Go's directories and sends the commands that make
//! each a session, through its own core's edge, so the store is written by
//! the daemon alone; the importer itself (the planning, the ids derived from
//! each session so a rerun adds nothing) is `sessions-application`'s.

use std::sync::Arc;

use daemon_proto::v1;

use crate::{Answer, Api};

/// What runs the import where the daemon stands: the root's, which holds the
/// account's project registry, Go's session directories and the core's edge.
pub trait Imports: Send + Sync {
    /// Import every Go session of this account, once.
    fn import(&self) -> Answer<'_, v1::ImportSessionsResponse>;
}

impl<E> Api<E> {
    /// The API, `ImportSessions` served by `imports`.
    #[must_use]
    pub fn importing(self, imports: Arc<dyn Imports>) -> Api<E> {
        Api {
            imports: Some(imports),
            ..self
        }
    }

    /// `ImportSessions`.
    pub async fn import_sessions(
        &self,
        request: v1::ImportSessionsRequest,
    ) -> Result<v1::ImportSessionsResponse, crate::ApiError> {
        let _ = request;
        let imports = self
            .imports
            .as_ref()
            .ok_or_else(|| crate::ApiError::failed("this daemon imports no sessions"))?;
        imports.import().await
    }
}
