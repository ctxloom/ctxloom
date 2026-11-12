//! A read model held in memory on angzarr-embedded's projector port, run
//! BOTH ways at once (S32, T16): registered in the kernel's registry
//! (`Registry::projector_handler`), so a `HandleSync` waits for it and
//! answers its view, and caught up from the store by a `CatchUp`, which
//! hands it what another process committed. Both drive ONE instance
//! ([`Held`] is a shared handle), so there is one fold per read model, and
//! each page reaches the fold once:
//!
//! - a page at or past where the fold is on its stream is folded; one before
//!   it was folded already (the other driver handed it) and is passed over;
//! - a book from sequence 0 whose first event is not the one the fold
//!   started the stream with is a stream purged and written again: it is
//!   folded afresh, as the projector port says (and so is one whose first
//!   event carries no cause and no instant, which tells nothing);
//! - a book with no page at `next_sequence` 0 is a stream gone: the fold
//!   forgets it;
//! - a book that starts past where the fold is (the kernel's inline book of
//!   a stream another process wrote meanwhile) is a gap: the pages between
//!   are read through [`Fill`], when one is given, and folded first; with
//!   none, or a read that fails, the book waits for the catch-up, which
//!   hands the stream in order.
//!
//! What a [`Model`] answers for a book's stream ([`Model::view`]) is the
//! projection (`Projection.projection`): the resource message a `Get` of it
//! returns (daemon.proto's header).

use std::collections::BTreeMap;
use std::ops::Deref;
use std::sync::{Arc, Mutex, MutexGuard, PoisonError};

use angzarr_embedded::angzarr_ports::BoxFuture;
use angzarr_embedded::angzarr_proto::v1::event_meta::Causation;
use angzarr_embedded::angzarr_router::error::CodedError;
use angzarr_embedded::angzarr_router::hpb::{ProjectRequest, ProjectResponse};
use angzarr_embedded::v1::{Cover, EventBook, EventPage};
use angzarr_embedded::{Any, PayloadError, ProjectorHandler, StoreError, Timestamp};

/// A read model a [`Held`] folds: its fold, one stream's book at a time as
/// the projector port hands it, and its view of a stream.
pub trait Model: Default + Send + 'static {
    /// The projector's name: what a `HandleSync` names it by.
    const NAME: &'static str;

    /// Fold `book`, one stream's events after the last folded of it: a book
    /// from sequence 0 starts the stream afresh, a book with no page at
    /// `next_sequence` 0 says it is gone.
    fn project(&mut self, book: &EventBook) -> Result<(), PayloadError>;

    /// What the model answers for `cover`'s stream as it stands: the
    /// resource message, packed; `None` where it has none for it.
    fn view(&self, cover: &Cover) -> Option<Any>;
}

/// The pages of a stream between two sequences, read from the store: what a
/// [`Held`] folds first when a book starts past where it is.
pub trait Fill: Send + Sync {
    /// `cover`'s pages from `from` up to, not including, `to`, oldest first.
    fn pages<'a>(
        &'a self,
        cover: &'a Cover,
        from: u64,
        to: u64,
    ) -> BoxFuture<'a, Result<Vec<EventPage>, StoreError>>;
}

/// A stream as the fold tells streams apart: domain, root and edition.
type Key = (String, Vec<u8>, Option<String>);

fn key(cover: &Cover) -> Key {
    (
        cover.domain.clone(),
        cover
            .root
            .as_ref()
            .map(|root| root.value.to_vec())
            .unwrap_or_default(),
        cover.edition.clone(),
    )
}

/// What tells a stream from one written again on its cover: its first
/// event's cause and instant (the sequence cannot: each starts at 0).
#[derive(Clone, Debug, PartialEq)]
struct Fingerprint {
    cause: Option<Causation>,
    recorded_at: Option<Timestamp>,
}

impl Fingerprint {
    /// A page with no cause and no instant (one no store recorded) tells
    /// nothing: a book from 0 of such pages is always folded afresh.
    fn unknown(&self) -> bool {
        self.cause.is_none() && self.recorded_at.is_none()
    }

    fn of(page: &EventPage) -> Fingerprint {
        let meta = page.meta.as_ref();
        Fingerprint {
            cause: meta.and_then(|meta| meta.causation.clone()),
            recorded_at: meta.and_then(|meta| meta.recorded_at),
        }
    }
}

/// Where the fold is on one stream.
#[derive(Clone, Debug)]
struct Seen {
    first: Fingerprint,
    /// The next sequence it is owed.
    next: u64,
}

/// What admitting a book came to.
#[derive(Debug, PartialEq)]
enum Admitted {
    /// Folded (all, some or none of it, being owed nothing).
    Folded,
    /// It starts past where the fold is: the pages `from..to` are owed first.
    Gap { from: u64, to: u64 },
}

/// The model and where it is on each stream.
#[derive(Default)]
struct Guarded<M> {
    model: M,
    seen: BTreeMap<Key, Seen>,
}

impl<M: Model> Guarded<M> {
    /// Fold what of `book` the model is owed, as the module says.
    fn admit(&mut self, book: &EventBook) -> Result<Admitted, PayloadError> {
        let cover = book.cover.clone().unwrap_or_default();
        let key = key(&cover);
        let Some(first) = book.pages.first() else {
            if book.next_sequence == 0 {
                self.seen.remove(&key);
                self.model.project(book)?;
            }
            return Ok(Admitted::Folded);
        };
        let fingerprint = Fingerprint::of(first);
        let afresh = first.sequence == 0
            && (fingerprint.unknown()
                || self
                    .seen
                    .get(&key)
                    .is_none_or(|seen| seen.first != fingerprint));
        // RED: no page is passed over yet.
        let _ = (afresh, self.seen.get(&key).map(|seen| seen.next));
        let next = first.sequence;
        if first.sequence > next {
            return Ok(Admitted::Gap {
                from: next,
                to: first.sequence,
            });
        }
        let owed: Vec<EventPage> = book
            .pages
            .iter()
            .filter(|page| page.sequence >= next)
            .cloned()
            .collect();
        let Some(last) = owed.last().map(|page| page.sequence) else {
            return Ok(Admitted::Folded);
        };
        let trimmed = EventBook {
            cover: book.cover.clone(),
            pages: owed,
            snapshot: None,
            next_sequence: book.next_sequence,
        };
        self.model.project(&trimmed)?;
        let fingerprint = match afresh {
            true => Fingerprint::of(first),
            false => self
                .seen
                .get(&key)
                .map_or_else(|| Fingerprint::of(first), |seen| seen.first.clone()),
        };
        self.seen.insert(
            key,
            Seen {
                first: fingerprint,
                next: last + 1,
            },
        );
        Ok(Admitted::Folded)
    }
}

/// The shared model: every clone is the same fold.
pub struct Held<M> {
    inner: Arc<Inner<M>>,
}

struct Inner<M> {
    guarded: Mutex<Guarded<M>>,
    fill: Option<Arc<dyn Fill>>,
}

impl<M> Clone for Held<M> {
    fn clone(&self) -> Self {
        Held {
            inner: Arc::clone(&self.inner),
        }
    }
}

impl<M: Model> Default for Held<M> {
    fn default() -> Self {
        Held::new(None)
    }
}

/// The model, locked: read as the model.
pub struct Lock<'h, M>(MutexGuard<'h, Guarded<M>>);

impl<M> Deref for Lock<'_, M> {
    type Target = M;

    fn deref(&self) -> &M {
        &self.0.model
    }
}

impl<M: Model> Held<M> {
    /// An empty model; `fill` reads what a gap is owed, where given.
    pub fn new(fill: Option<Arc<dyn Fill>>) -> Held<M> {
        Held {
            inner: Arc::new(Inner {
                guarded: Mutex::new(Guarded::default()),
                fill,
            }),
        }
    }

    fn guarded(&self) -> MutexGuard<'_, Guarded<M>> {
        self.inner
            .guarded
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
    }

    /// The model, locked.
    pub fn lock(&self) -> Lock<'_, M> {
        Lock(self.guarded())
    }
}

impl<M: Model> ProjectorHandler for Held<M> {
    fn name(&self) -> &str {
        M::NAME
    }

    async fn dispatch(&self, request: &ProjectRequest) -> Result<ProjectResponse, CodedError> {
        let book = request.events.clone().unwrap_or_default();
        let cover = book.cover.clone().unwrap_or_default();
        let admitted = self.guarded().admit(&book).map_err(|e| bug(&e))?;
        if let (Admitted::Gap { from, to }, Some(fill)) = (admitted, &self.inner.fill) {
            // A gap the store cannot fill now waits for the catch-up, which
            // hands the stream in order: nothing is folded out of order.
            if let Ok(mut pages) = fill.pages(&cover, from, to).await {
                pages.extend(book.pages.iter().cloned());
                let whole = EventBook { pages, ..book };
                self.guarded().admit(&whole).map_err(|e| bug(&e))?;
            }
        }
        // RED: no view yet.
        let _ = cover;
        Ok(ProjectResponse { projection: None })
    }
}

/// `error`, an event the model cannot read, as the projector port's bug: its
/// code, and its text args (the type URL it names).
pub fn bug(error: &PayloadError) -> CodedError {
    use angzarr_embedded::angzarr_diagnostic::{Arg, Diagnostic as _};
    let args = error
        .args()
        .into_iter()
        .filter_map(|(name, arg)| match arg {
            Arg::Str(value) => Some((name.to_string(), value)),
            _ => None,
        });
    CodedError::bug(error.code(), args)
}

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
