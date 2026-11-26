//! `ImportSessions` where the daemon stands (ONE WRITER): Go's session
//! directories of this account read here, and every command that makes each
//! a session sent through the core's own edge, so the daemon's coordinator
//! handles them (and a test's `handled <CommandType>` line says each) and no
//! other process opens the store.

use std::sync::Arc;

use angzarr_embedded::v1::{command_page, CommandBook, Cover, DeliveryOutcome, EventPage, Uuid};
use angzarr_embedded::{Failure, StoreError};
use daemon_api::Imports;
use daemon_proto::v1;
use harp_names::HarpNames;
use project_registry::Environment;
use session_files::GoDirs;
use sessions_application::{
    dispatch_failed, refused, Dispatch, ImportRequest, ImportService, Importer, Sending, Sent,
};

use file_system::Fs;
use std::path::PathBuf;

/// The importer's way out: the core's edge, each command under the id the
/// importer derived for it.
pub(crate) struct EdgeDispatch<E> {
    pub(crate) edge: Arc<E>,
}

impl<E: run_ports::Edge> Dispatch for EdgeDispatch<E> {
    async fn send(
        &mut self,
        _project: &ports::Project,
        mut book: CommandBook,
        id: &Uuid,
    ) -> Sending {
        // A command is applied under the id its page names; a fact's id is
        // its origin's.
        if let Some(page) = book.page.as_mut() {
            if matches!(page.body, Some(command_page::Body::Command(_))) {
                page.command_id = Some(id.clone());
            }
        }
        match self.edge.handle(book).await {
            Ok(answered) => Ok(Ok(Sent {
                duplicate: answered.delivery == DeliveryOutcome::Duplicate,
            })),
            Err(Failure::Rejected(rejected)) => Ok(Err(refused(&rejected))),
            Err(failure) => Err(dispatch_failed(&failure, 1)),
        }
    }

    async fn events(
        &mut self,
        _project: &ports::Project,
        cover: &Cover,
    ) -> Result<Vec<EventPage>, StoreError> {
        self.edge.events(cover, 0).await
    }

    fn dead_letters(&mut self) -> Vec<angzarr_embedded::DeadLetter> {
        Vec::new()
    }
}

/// What imports for the daemon: the account's registry and Go's directories
/// under `home`, the commands through `edge`.
pub(crate) struct Importing<E> {
    pub(crate) fs: Arc<dyn Fs>,
    pub(crate) home: PathBuf,
    pub(crate) edge: Arc<E>,
}

impl<E: run_ports::Edge + 'static> Imports for Importing<E> {
    fn import(&self) -> daemon_api::Answer<'_, v1::ImportSessionsResponse> {
        Box::pin(async move {
            let environment = Environment {
                home: Some(self.home.clone()),
                root: None,
                project_id: None,
            };
            let mut importer = Importer::new(
                crate::registry(Arc::clone(&self.fs), environment, HarpNames),
                EdgeDispatch {
                    edge: Arc::clone(&self.edge),
                },
                GoDirs::new(Arc::clone(&self.fs), self.home.clone()),
            );
            let request = ImportRequest {
                project_harps: crate::fresh_project_harps(),
            };
            let report = importer
                .import(request)
                .await
                .map_err(|error| daemon_api::ApiError::diagnosed(&error))?;
            Ok(response_of(&report))
        })
    }
}

/// The report as the wire says it: each left session's reason by code and
/// args, as the CLI has no catalog to render it from.
fn response_of(report: &sessions_application::ImportReport) -> v1::ImportSessionsResponse {
    use coded_diagnostic::{Arg, Diagnostic};
    v1::ImportSessionsResponse {
        imported: report
            .imported
            .iter()
            .map(|i| v1::import_sessions_response::Imported {
                harp: i.harp.clone(),
                project: i.project.clone(),
                id: i.id.clone(),
            })
            .collect(),
        already: report.already.clone(),
        left: report
            .left
            .iter()
            .map(|l| v1::import_sessions_response::Left {
                harp: l.harp.clone(),
                code: l.why.code().to_string(),
                args: l
                    .why
                    .args()
                    .into_iter()
                    .filter_map(|(name, arg)| match arg {
                        Arg::Str(text) => Some((name.to_string(), text)),
                        Arg::Uint(n) => Some((name.to_string(), n.to_string())),
                        Arg::Int(n) => Some((name.to_string(), n.to_string())),
                        Arg::Nested(_) => None,
                    })
                    .collect(),
            })
            .collect(),
    }
}
