//! The format: a round trip, the CRC, a torn tail and corruption, each
//! also as a property over generated holds (a seeded generator of the
//! test's own: no property crate is in the lock).

use super::*;

/// A small deterministic generator (xorshift64*).
pub(crate) struct Gen(u64);

impl Gen {
    pub(crate) fn new(seed: u64) -> Gen {
        Gen(seed.wrapping_mul(0x9E37_79B9_7F4A_7C15) | 1)
    }

    pub(crate) fn next(&mut self) -> u64 {
        self.0 ^= self.0 >> 12;
        self.0 ^= self.0 << 25;
        self.0 ^= self.0 >> 27;
        self.0.wrapping_mul(0x2545_F491_4F6C_DD1D)
    }

    pub(crate) fn below(&mut self, n: usize) -> usize {
        (self.next() % n as u64) as usize
    }

    fn bytes(&mut self, max: usize) -> Vec<u8> {
        let n = self.below(max + 1);
        (0..n).map(|_| self.next() as u8).collect()
    }

    pub(crate) fn entry(&mut self) -> HeldEntry {
        let held_at = (self.below(2) == 0).then(|| Timestamp {
            seconds: (self.next() % 2_000_000_000) as i64,
            nanos: self.below(1_000_000_000) as i32,
        });
        if self.below(5) == 0 {
            HeldEntry::Gap(HeldGap {
                stamp: format!("v0.{}.0-abcdef0", self.below(9)),
                external_id: format!("hook:text:s:{}:{}", self.next(), self.below(4)),
                held_at,
                why: ["bound", "disk"][self.below(2)].to_string(),
            })
        } else {
            HeldEntry::Fact(HeldFact {
                stamp: format!("v0.{}.0-abcdef0", self.below(9)),
                session: "projects/p/sessions/brisk-copper-moth".to_string(),
                external_id: format!("hook:prompt:s:{}", self.next()),
                book: self.bytes(300),
                held_at,
                incarnation: u32::try_from(self.below(3)).unwrap_or(0),
            })
        }
    }
}

pub(crate) fn file_of(entries: &[HeldEntry]) -> Vec<u8> {
    let mut bytes = MAGIC.to_vec();
    for entry in entries {
        bytes.extend(frame(entry));
    }
    bytes
}

fn read(bytes: &[u8]) -> Scan {
    scan(bytes, 1 << 20).expect("a hold")
}

fn entries(scan: &Scan) -> Vec<HeldEntry> {
    scan.entries.iter().map(|(e, _)| e.clone()).collect()
}

#[test]
fn a_hold_reads_back_what_was_framed_in_order() {
    let mut gen = Gen::new(1);
    let held: Vec<HeldEntry> = (0..20).map(|_| gen.entry()).collect();
    let bytes = file_of(&held);
    let scan = read(&bytes);
    assert_eq!(entries(&scan), held);
    assert_eq!(scan.tail, Tail::Clean);
    assert_eq!(scan.valid_end, bytes.len());
    // Each record's range is its frame, end to end.
    let mut at = MAGIC.len();
    for ((entry, range), want) in scan.entries.iter().zip(&held) {
        assert_eq!(entry, want);
        assert_eq!(range.start, at);
        assert_eq!(&bytes[range.clone()], frame(want).as_slice());
        at = range.end;
    }
}

#[test]
fn a_frame_is_its_length_its_crc_and_its_payload() {
    let entry = Gen::new(2).entry();
    let framed = frame(&entry);
    let len = u32::from_le_bytes(framed[..4].try_into().unwrap()) as usize;
    let crc = u32::from_le_bytes(framed[4..8].try_into().unwrap());
    assert_eq!(len, framed.len() - HEADER);
    assert_eq!(crc, crc32fast::hash(&framed[HEADER..]));
    assert_eq!(&MAGIC[..7], b"CTXHELD");
    assert_eq!(MAGIC[7], 1, "the format");
}

#[test]
fn an_empty_hold_is_its_magic_or_nothing() {
    for bytes in [&b""[..], &MAGIC[..]] {
        let scan = read(bytes);
        assert!(scan.entries.is_empty());
        assert_eq!(scan.tail, Tail::Clean);
        assert_eq!(scan.valid_end, bytes.len());
    }
    // A creation that tore before its magic was whole.
    let scan = read(&MAGIC[..5]);
    assert!(scan.entries.is_empty());
    assert_eq!((scan.tail, scan.valid_end), (Tail::Torn, 0));
}

#[test]
fn a_file_that_is_no_hold_or_of_another_format_is_unreadable() {
    let mut future = MAGIC.to_vec();
    future[7] = 2;
    for bytes in [&b"{\"json\": 1}"[..], &future[..], &b"XYZ"[..]] {
        assert_eq!(scan(bytes, 1 << 20).unwrap_err(), Unreadable);
    }
}

#[test]
fn a_bad_crc_is_corruption_after_the_records_before_it() {
    let mut gen = Gen::new(3);
    let held: Vec<HeldEntry> = (0..3).map(|_| gen.entry()).collect();
    let mut bytes = file_of(&held);
    let third = read(&bytes).entries[2].1.clone();
    bytes[third.start + 4] ^= 0x01;
    let scan = read(&bytes);
    assert_eq!(entries(&scan), held[..2].to_vec());
    assert_eq!((scan.tail, scan.valid_end), (Tail::Corrupt, third.start));
}

#[test]
fn a_length_past_the_bound_is_corruption_and_one_within_it_a_torn_tail() {
    let entry = Gen::new(4).entry();
    let mut bytes = file_of(std::slice::from_ref(&entry));
    let good = bytes.len();
    bytes.extend_from_slice(&(2_000_000u32).to_le_bytes());
    bytes.extend_from_slice(&[0; 4]);
    let scan = scan(&bytes, 1 << 20).unwrap();
    assert_eq!((scan.tail, scan.valid_end), (Tail::Corrupt, good));
    let within = super::scan(&bytes, 4_000_000).unwrap();
    assert_eq!((within.tail, within.valid_end), (Tail::Torn, good));
}

// The bound is the longest record a hold has: a record exactly that long
// is read, one byte longer is corruption.
#[test]
fn a_record_exactly_at_the_bound_is_read_and_one_past_it_is_corruption() {
    let entry = Gen::new(5).entry();
    let bytes = file_of(std::slice::from_ref(&entry));
    let len = (bytes.len() - MAGIC.len() - HEADER) as u64;
    let at = scan(&bytes, len).unwrap();
    assert_eq!(entries(&at), [entry]);
    assert_eq!((at.tail, at.valid_end), (Tail::Clean, bytes.len()));
    let past = scan(&bytes, len - 1).unwrap();
    assert!(past.entries.is_empty());
    assert_eq!((past.tail, past.valid_end), (Tail::Corrupt, MAGIC.len()));
}

#[test]
fn a_payload_that_is_no_record_is_corruption() {
    for payload in [vec![0xff, 0xff, 0xff], Vec::new()] {
        let mut bytes = MAGIC.to_vec();
        bytes.extend_from_slice(&(payload.len() as u32).to_le_bytes());
        bytes.extend_from_slice(&crc32fast::hash(&payload).to_le_bytes());
        bytes.extend_from_slice(&payload);
        let scan = read(&bytes);
        assert!(scan.entries.is_empty(), "{payload:?}");
        assert_eq!((scan.tail, scan.valid_end), (Tail::Corrupt, MAGIC.len()));
    }
}

// Property: a hold cut at ANY byte (an append that crashed) reads back
// exactly the records wholly before the cut, its tail torn (or clean on a
// record's boundary), never corrupt.
#[test]
fn property_a_hold_cut_anywhere_is_its_whole_records_and_a_torn_tail() {
    for seed in 0..40 {
        let mut gen = Gen::new(seed);
        let held: Vec<HeldEntry> = (0..1 + gen.below(8)).map(|_| gen.entry()).collect();
        let bytes = file_of(&held);
        let ends: Vec<usize> = read(&bytes).entries.iter().map(|(_, r)| r.end).collect();
        for cut in MAGIC.len()..=bytes.len() {
            let scan = read(&bytes[..cut]);
            let whole = ends.iter().filter(|end| **end <= cut).count();
            assert_eq!(
                entries(&scan),
                held[..whole].to_vec(),
                "seed {seed} cut {cut}"
            );
            let boundary = cut == MAGIC.len() || ends.contains(&cut);
            let want = if boundary { Tail::Clean } else { Tail::Torn };
            assert_eq!(scan.tail, want, "seed {seed} cut {cut}");
            assert_eq!(
                scan.valid_end,
                ends.get(whole.wrapping_sub(1))
                    .copied()
                    .unwrap_or(MAGIC.len())
            );
        }
    }
}

// Property: a byte flipped anywhere in a record keeps every record before
// it, and reads that record as no record (corrupt, or torn when the flip
// made its length run past the end).
#[test]
fn property_a_flipped_byte_keeps_the_records_before_it() {
    for seed in 0..60 {
        let mut gen = Gen::new(1000 + seed);
        let held: Vec<HeldEntry> = (0..1 + gen.below(6)).map(|_| gen.entry()).collect();
        let mut bytes = file_of(&held);
        let ranges: Vec<Range<usize>> = read(&bytes)
            .entries
            .iter()
            .map(|(_, r)| r.clone())
            .collect();
        let at = MAGIC.len() + gen.below(bytes.len() - MAGIC.len());
        let hit = ranges.iter().position(|r| r.contains(&at)).unwrap();
        bytes[at] ^= 1 << gen.below(8);
        let scan = read(&bytes);
        assert_eq!(entries(&scan), held[..hit].to_vec(), "seed {seed} at {at}");
        assert_eq!(scan.valid_end, ranges[hit].start, "seed {seed}");
        assert_ne!(scan.tail, Tail::Clean, "seed {seed}");
    }
}

#[test]
fn held_at_is_the_records_own() {
    let at = Some(Timestamp {
        seconds: 7,
        nanos: 0,
    });
    let mut gen = Gen::new(5);
    for _ in 0..10 {
        let mut entry = gen.entry();
        match &mut entry {
            HeldEntry::Fact(f) => f.held_at = at,
            HeldEntry::Gap(g) => g.held_at = at,
        }
        assert_eq!(held_at(&entry), at);
    }
}
