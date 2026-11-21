//! A hold's file format, pure (held.proto's header): the magic
//! `CTXHELD\x01`, then records, each a little-endian u32 length, a
//! little-endian u32 CRC-32 of the payload, and the payload, one
//! `ctxloom.v1.HeldRecord`.
//!
//! [`scan`] reads a file's bytes into its VALID PREFIX and says what
//! follows it:
//!
//! - nothing ([`Tail::Clean`]);
//! - a TORN tail ([`Tail::Torn`]): fewer bytes than a header, or a header
//!   whose length (within the bound) runs past the end: an append that
//!   crashed, cut by the next taker, never set aside. Its residual: a
//!   corrupted length that still fits the bound is read as torn too;
//! - CORRUPTION ([`Tail::Corrupt`]): a length past the bound, a CRC that
//!   does not match, or a payload that is not a record: the rest is set
//!   aside, never parsed again.

use std::ops::Range;

use daemon_proto::google::protobuf::Timestamp;
use daemon_proto::v1::{self, held_record::Kind};
use prost::Message;
use run_ports::{HeldEntry, HeldFact, HeldGap};

/// The file's magic: its last byte is the format.
pub(crate) const MAGIC: &[u8; 8] = b"CTXHELD\x01";

/// A record's header: its length and its CRC.
pub(crate) const HEADER: usize = 8;

/// `entry`, framed.
pub(crate) fn frame(entry: &HeldEntry) -> Vec<u8> {
    let payload = record_of(entry).encode_to_vec();
    let mut out = Vec::with_capacity(HEADER + payload.len());
    // A payload past u32 is never framed: the bound is far below it.
    let len = u32::try_from(payload.len()).unwrap_or(u32::MAX);
    out.extend_from_slice(&len.to_le_bytes());
    out.extend_from_slice(&crc32fast::hash(&payload).to_le_bytes());
    out.extend_from_slice(&payload);
    out
}

fn record_of(entry: &HeldEntry) -> v1::HeldRecord {
    let kind = match entry {
        HeldEntry::Fact(fact) => Kind::Fact(v1::HeldFact {
            stamp: fact.stamp.clone(),
            session: fact.session.clone(),
            external_id: fact.external_id.clone(),
            book: fact.book.clone(),
            held_at: fact.held_at,
            incarnation: fact.incarnation,
        }),
        HeldEntry::Gap(gap) => Kind::Gap(v1::HeldGap {
            stamp: gap.stamp.clone(),
            external_id: gap.external_id.clone(),
            held_at: gap.held_at,
            why: gap.why.clone(),
        }),
    };
    v1::HeldRecord { kind: Some(kind) }
}

fn entry_of(record: v1::HeldRecord) -> Option<HeldEntry> {
    Some(match record.kind? {
        Kind::Fact(fact) => HeldEntry::Fact(HeldFact {
            stamp: fact.stamp,
            session: fact.session,
            external_id: fact.external_id,
            book: fact.book,
            held_at: fact.held_at,
            incarnation: fact.incarnation,
        }),
        Kind::Gap(gap) => HeldEntry::Gap(HeldGap {
            stamp: gap.stamp,
            external_id: gap.external_id,
            held_at: gap.held_at,
            why: gap.why,
        }),
    })
}

/// When a record was held.
pub(crate) fn held_at(entry: &HeldEntry) -> Option<Timestamp> {
    match entry {
        HeldEntry::Fact(fact) => fact.held_at,
        HeldEntry::Gap(gap) => gap.held_at,
    }
}

/// What follows a file's valid prefix.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum Tail {
    Clean,
    Torn,
    Corrupt,
}

/// A file read: its valid records, each with its frame's bytes in the
/// file, where the valid prefix ends, and what follows.
#[derive(Debug)]
pub(crate) struct Scan {
    pub(crate) entries: Vec<(HeldEntry, Range<usize>)>,
    pub(crate) valid_end: usize,
    pub(crate) tail: Tail,
}

/// The file is no hold this ctxloom reads: another file, or a format it
/// does not know.
#[derive(Debug, PartialEq, Eq)]
pub(crate) struct Unreadable;

/// `bytes`, a hold's whole file, read; a record longer than `max` is
/// corruption. A file shorter than the magic whose bytes begin it is an
/// empty hold whose creation tore.
pub(crate) fn scan(bytes: &[u8], max: u64) -> Result<Scan, Unreadable> {
    let empty = |tail| Scan {
        entries: Vec::new(),
        valid_end: 0,
        tail,
    };
    if bytes.len() < MAGIC.len() {
        return if MAGIC.starts_with(bytes) {
            Ok(empty(if bytes.is_empty() {
                Tail::Clean
            } else {
                Tail::Torn
            }))
        } else {
            Err(Unreadable)
        };
    }
    if &bytes[..MAGIC.len()] != MAGIC {
        return Err(Unreadable);
    }
    let mut entries = Vec::new();
    let mut at = MAGIC.len();
    let tail = loop {
        let rest = &bytes[at..];
        if rest.is_empty() {
            break Tail::Clean;
        }
        if rest.len() < HEADER {
            break Tail::Torn;
        }
        let len = u32::from_le_bytes([rest[0], rest[1], rest[2], rest[3]]);
        let crc = u32::from_le_bytes([rest[4], rest[5], rest[6], rest[7]]);
        if u64::from(len) > max {
            break Tail::Corrupt;
        }
        // Within u32, so within usize on every platform ctxloom runs on.
        let len = len as usize;
        if rest.len() - HEADER < len {
            break Tail::Torn;
        }
        let payload = &rest[HEADER..HEADER + len];
        if crc32fast::hash(payload) != crc {
            break Tail::Corrupt;
        }
        let Some(entry) = v1::HeldRecord::decode(payload).ok().and_then(entry_of) else {
            break Tail::Corrupt;
        };
        let end = at + HEADER + len;
        entries.push((entry, at..end));
        at = end;
    };
    Ok(Scan {
        entries,
        valid_end: at,
        tail,
    })
}

#[cfg(test)]
#[path = "frame.test.rs"]
mod tests;
