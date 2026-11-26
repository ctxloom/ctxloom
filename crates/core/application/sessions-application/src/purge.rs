//! Purging a stream the daemon's destroy removes.

use angzarr_embedded::{EventStore, Purge, StoreError, StreamRef};

use crate::{failed, AppError};

/// Purge `stream` at the head just read, re-read and tried again when another
/// writer moved it in between (head-guarded, angzarr's 14.43); how many
/// events it removed.
pub async fn purge<S: EventStore + Purge>(
    store: &S,
    stream: StreamRef<'_>,
) -> Result<u64, AppError> {
    let mut tries = 0;
    loop {
        let head = store.head(stream).await.map_err(failed)?.next_sequence;
        match store.purge(stream, head).await {
            Err(StoreError::Conflict { .. }) if tries < 3 => tries += 1,
            other => return other.map_err(failed),
        }
    }
}
