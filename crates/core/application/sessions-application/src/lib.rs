//! The sessions application: what the daemon's session use cases share. A
//! session's writes are commands SENT to the owner of the store
//! ([`Dispatch`]); the importer plans Go's sessions into such commands
//! ([`Importer`]) and a project is named through them ([`name_through`]); a
//! remove is the daemon's (daemon-api's `DeleteSession`, over the types here
//! and [`purge`]). The ports are the only way out: this crate does no I/O,
//! keeps no clock and starts no runtime, and opens no store.

mod dispatch;
mod error;
mod go_sidecar;
mod import;
mod projects;
mod purge;
mod registry;
mod types;
mod urgency;

use coded_diagnostic::Report;

/// A dead letter, as the kernel answers it and the store keeps it. A use case
/// returns every one its kernel calls answered, beside its result or on its
/// error, for its driving adapter to say loudly: each almost certainly marks a
/// bug.
pub use angzarr_embedded::{dead_letter_line, DeadLetter};

pub use dispatch::{failed as dispatch_failed, refused, Dispatch, Sending, Sent};
#[cfg(any(test, feature = "test-support"))]
pub use error::diagnostic_samples;
pub use error::{
    AppError, DestroysAuthored, DestroysHeld, Refusal, RemovedNothing, CATALOGS, EN_US,
};
pub use go_sidecar::{
    decode as decode_go_sidecar, GoEndReason, GoEntry, GoRotation, GoRunEnd, GoSidecarError,
};
pub use import::{ImportReport, ImportRequest, ImportService, Imported, Importer, Left, Why};
pub use ports::{Project, Timestamp};
pub use projects::{name_through, ProjectHarp};
pub use purge::purge;
pub use registry::{registry, DEFAULT_RETENTION_DAYS};
pub use session_index::{
    stamp, Captured, PendingMessage, SessionRecord, State, View, NO_SUMMARY, STATES,
};
/// A pending message's urgency, and its word (D12), for a renderer.
pub use sessions::{urgency_word, Urgency};
/// A record's owner link and the children it holds (D26), for a renderer.
pub use sessions::{Child, SessionRef};
pub use types::{Class, FilePlan, PlanItem, Removal, RemoveRequest, TranscriptRow, REMOVE_VERB};
pub use urgency::urgencies_of;

/// A store's failure, as the use cases fail with it.
pub(crate) fn failed(error: angzarr_embedded::StoreError) -> AppError {
    AppError::Failed(Report::of(&angzarr_embedded::KernelError::Store(error)))
}

/// A dead letter as the kernel answers one, from `component`: for a test that
/// hands a use case, or a driving adapter, a letter a kernel call answered.
#[cfg(any(test, feature = "test-support"))]
pub fn dead_letter_sample(component: &str) -> DeadLetter {
    use angzarr_embedded::v1::{angzarr_dead_letter::Why, AngzarrDeadLetter, Cover, Diagnostic};
    DeadLetter {
        stream: Cover {
            domain: sessions::SESSION_DOMAIN.to_string(),
            root: Some(angzarr_embedded::v1::Uuid::from_bytes([7; 16])),
            edition: None,
        },
        at: angzarr_embedded::DeadLetterAt::Sequence(0),
        letter: AngzarrDeadLetter {
            source_component: component.to_string(),
            why: Some(Why::Reason(Diagnostic {
                code: "ROUTER_REASON_UNHANDLED_HANDLER_ERROR".to_string(),
                typed_args: Vec::new(),
            })),
            ..AngzarrDeadLetter::default()
        },
    }
}

#[cfg(test)]
#[path = "fakes.test.rs"]
mod fakes;
