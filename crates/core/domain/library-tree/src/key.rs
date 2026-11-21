//! An observation's fact key (L4, unchanged by G4's supersession): a key of
//! ids alone, chained on the last cause so that A→B→A→B never repeats one.
//!
//! `obs:{tree}:{H(last_cause)}:{head}:{H(sorted dirty)}`, H the SHA-256 in
//! hex, `sorted dirty` each `path NUL oid NL` line in path order. Two reads
//! that see the same thing against the same state give one key, so the
//! second is a DUPLICATE. No sequence enters it: a fact carries none.

use angzarr_embedded::markup::DomainMessage;
use angzarr_embedded::v1::{command_page, CommandBook, CommandPage, Cover, Fact, FactOrigin};
use sha2::{Digest, Sha256};

use crate::{DirtyPath, RecordTreeObserved, TreeName, LIBRARY_DOMAIN};

/// The external id of an observation of `tree` (its name) made against the
/// state whose last cause is `last_cause`, seeing `head` and `dirty`.
pub fn observation_key(tree: &str, last_cause: &str, head: &str, dirty: &[DirtyPath]) -> String {
    let mut sorted: Vec<&DirtyPath> = dirty.iter().collect();
    sorted.sort_by(|a, b| a.path.cmp(&b.path));
    let mut lines = Sha256::new();
    for path in sorted {
        lines.update(path.path.as_bytes());
        lines.update([0]);
        lines.update(path.oid.as_bytes());
        lines.update([b'\n']);
    }
    format!(
        "obs:{tree}:{}:{head}:{}",
        hex::encode(Sha256::digest(last_cause.as_bytes())),
        hex::encode(lines.finalize()),
    )
}

/// A file's content id (step 10c U13 rework, owner 2026-11-18): the
/// SHA-256 of its bytes in hex, ctxloom's digest; never git's.
pub fn content_id(bytes: &[u8]) -> String {
    hex::encode(Sha256::digest(bytes))
}

/// The external id of a listing of `tree` (its name) made against the
/// state whose last cause is `last_cause` (step 10c U13 rework):
/// `listed:{tree}:{H(last_cause)}:{H(sorted files)}`, so two scans that
/// find the same against the same state are one, and A to B to A is three.
pub fn listing_key(tree: &str, last_cause: &str, files: &[DirtyPath]) -> String {
    let mut sorted: Vec<&DirtyPath> = files.iter().collect();
    sorted.sort_by(|a, b| a.path.cmp(&b.path));
    let mut lines = Sha256::new();
    for path in sorted {
        lines.update(path.path.as_bytes());
        lines.update([0]);
        lines.update(path.oid.as_bytes());
        lines.update([b'\n']);
    }
    format!(
        "listed:{tree}:{}:{}",
        hex::encode(Sha256::digest(last_cause.as_bytes())),
        hex::encode(lines.finalize()),
    )
}

/// The book that records `fact`, every file a scan of `tree` found: a
/// Fact on the tree's stream keyed by [`listing_key`].
pub fn listing_book(tree: &TreeName, fact: &crate::RecordTreeListed) -> CommandBook {
    let external_id = listing_key(&tree.to_string(), &fact.base_cause, &fact.files);
    CommandBook {
        cover: Some(Cover {
            domain: LIBRARY_DOMAIN.to_string(),
            root: Some(tree.root()),
            edition: None,
        }),
        page: Some(CommandPage {
            body: Some(command_page::Body::Fact(Fact {
                payload: Some(
                    angzarr_embedded::Any::from_msg(&fact.clone().to_wire())
                        .expect("a listing encodes"),
                ),
                origin: Some(FactOrigin {
                    external_id,
                    description: "a scan of the library tree".to_string(),
                }),
            })),
            ..CommandPage::default()
        }),
    }
}

/// The book that records `fact`, an observation of `tree`: a Fact on the
/// tree's stream, its origin keyed by [`observation_key`] over the cause
/// the observer read (`base_cause`), the head and the dirty set it saw.
pub fn observation_book(tree: &TreeName, fact: &RecordTreeObserved) -> CommandBook {
    let external_id = observation_key(
        &tree.to_string(),
        &fact.base_cause,
        &fact.head_after,
        &fact.dirty_after,
    );
    CommandBook {
        cover: Some(Cover {
            domain: LIBRARY_DOMAIN.to_string(),
            root: Some(tree.root()),
            edition: None,
        }),
        page: Some(CommandPage {
            body: Some(command_page::Body::Fact(Fact {
                payload: Some(
                    angzarr_embedded::Any::from_msg(&fact.clone().to_wire())
                        .expect("an observation encodes"),
                ),
                origin: Some(FactOrigin {
                    external_id,
                    description: "a read of the library tree".to_string(),
                }),
            })),
            ..CommandPage::default()
        }),
    }
}

#[cfg(test)]
#[path = "key.test.rs"]
mod tests;
