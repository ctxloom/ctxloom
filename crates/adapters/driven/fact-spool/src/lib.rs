//! Each session's HOLD on disk (s4-Q14, H1): run-ports' [`HeldFacts`] over
//! the file-system seam, `<home>/.ctxloom/held/<session root>.held`, in
//! `frame`'s format, behind two `file-lock` locks per session.
//!
//! - `<root>.lock`, the APPEND lock, short: taken by an append, an
//!   emptiness check, a take's snapshot, a settle's rewrite and a discard,
//!   each for its own call. A hook waits for it at most [`APPEND_WAIT`]
//!   (the wait, or the pause it is made of, is the constructor's: a test
//!   forces a held lock through it, never by sleeping).
//! - `<root>.flush.lock`, the FLUSH lock: a take holds it from its snapshot
//!   until its batch is settled or dropped, and it is NEVER waited for: of
//!   several processes taking one hold at once (the session's next hook, a
//!   CLI command, the frontend: any of an up-to-date version) exactly one
//!   gets the batch, the others `None`, and nothing cuts the hold twice.
//!
//! A settle rewrites the hold (a 0600 temporary renamed over it) to the
//! records kept, then whatever was appended after the snapshot, and only
//! when the hold still begins with the bytes snapshotted: a hold discarded
//! (and held anew) meanwhile is left as it is. A crash between delivering
//! and settling leaves the snapshot's records to be delivered again, each
//! a DUPLICATE by its external id.
//!
//! A torn tail (an append that crashed) is cut by the next append or take;
//! corruption has its valid prefix kept and the rest set aside as
//! `<root>.held.corrupt-<n>` (0600, never parsed again), so later appends
//! start clean. A settle's records the flusher sets aside (`Fate::SetAside`:
//! ones it can never deliver, s4-Q14 H3) go there too, their frames as
//! they were, one part a settle. A hold in a format this ctxloom does not know is never
//! touched. No record is fsync'd: the hold's durability is the store's, a
//! process's crash and not the machine's.
//!
//! Modes: the `held` directory is made and tightened 0700, a hold created
//! 0600 and tightened 0600, at every use, before any lock is taken, as the
//! store's opener tightens; a mode that cannot be set fails the call
//! (`run-ports-hold-restrict`). On Windows the seam sets no mode: the
//! profile's ACL holds them (M8).

mod frame;

use std::collections::BTreeMap;
use std::io::{self, Write};
use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::time::Duration;

use file_lock::{LockError, Locked};
use file_system::{Fs, Open, Perms};
use os::Mode;
use run_ports::{
    Batch, Bound, Fate, GapWhy, Held, HeldEntry, HeldFact, HeldFacts, HeldGap, HoldError,
    HoldSummary, Settle, Settled, APPEND_WAIT, GAP_SLACK, HELD_DIR, HOME_BOUND, SESSION_BOUND,
};
use uuid::Uuid;

use frame::{Scan, Tail, MAGIC};

/// The holds' directory's mode: its owner's alone (os's own constant is
/// test support only, so the store's opener spells it too).
const PRIVATE_DIR: Mode = Mode::from_bits(0o700);

/// A lock's pause between tries while another holds it: `Ok` to try again,
/// an error to give up.
pub type Pause = Box<dyn FnMut() -> Result<(), LockError> + Send>;

/// What makes the append lock's pause for a lock path.
type Pauses = Arc<dyn Fn(&Path) -> Pause + Send + Sync>;

/// The holds of one ctxloom home.
#[derive(Clone)]
pub struct Spool {
    fs: Arc<dyn Fs>,
    dir: PathBuf,
    session_bound: u64,
    home_bound: u64,
    pauses: Pauses,
}

impl std::fmt::Debug for Spool {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Spool")
            .field("dir", &self.dir)
            .finish_non_exhaustive()
    }
}

impl Spool {
    /// The holds of the ctxloom home `home` (absolute), at [`HELD_DIR`],
    /// with the rulings' bounds, waiting [`APPEND_WAIT`] for an append lock.
    pub fn at_home(fs: Arc<dyn Fs>, home: &Path) -> Spool {
        Spool {
            fs,
            dir: home.join(HELD_DIR),
            session_bound: SESSION_BOUND,
            home_bound: HOME_BOUND,
            pauses: Arc::new(|path| Box::new(file_lock::until(path, APPEND_WAIT))),
        }
    }

    /// Waiting up to `wait` for an append lock (a daemon's longer wait).
    pub fn waiting(self, wait: Duration) -> Spool {
        self.pausing(move |path| Box::new(file_lock::until(path, wait)))
    }

    /// Pausing as `pauses` makes each append lock's pause: the seam a test
    /// forces a held lock through.
    pub fn pausing(self, pauses: impl Fn(&Path) -> Pause + Send + Sync + 'static) -> Spool {
        Spool {
            pauses: Arc::new(pauses),
            ..self
        }
    }

    /// Bounded at `session` bytes a hold and `home` bytes in all.
    pub fn bounded(self, session: u64, home: u64) -> Spool {
        Spool {
            session_bound: session,
            home_bound: home,
            ..self
        }
    }

    /// The longest record a hold can have: none is framed past the
    /// session's bound and its gaps' slack, so a longer length is
    /// corruption.
    fn longest(&self) -> u64 {
        self.session_bound.saturating_add(GAP_SLACK)
    }

    /// The holds' directory.
    pub fn dir(&self) -> &Path {
        &self.dir
    }

    /// `session`'s hold.
    pub fn hold_path(&self, session: &Uuid) -> PathBuf {
        self.dir.join(format!("{session}.held"))
    }

    /// `session`'s append lock.
    pub fn append_lock_path(&self, session: &Uuid) -> PathBuf {
        self.dir.join(format!("{session}.lock"))
    }

    /// `session`'s flush lock.
    pub fn flush_lock_path(&self, session: &Uuid) -> PathBuf {
        self.dir.join(format!("{session}.flush.lock"))
    }

    /// The directory, made and tightened 0700.
    fn private_dir(&self) -> Result<(), HoldError> {
        self.fs
            .create_dir_all(&self.dir, PRIVATE_DIR)
            .map_err(|e| io_error(&self.dir, &e))?;
        self.fs
            .set_permissions(&self.dir, PRIVATE_DIR)
            .map_err(|e| restrict_error(&self.dir, &e))
    }

    fn append_lock(&self, session: &Uuid) -> Result<Locked, HoldError> {
        let path = self.append_lock_path(session);
        let mut pause = (self.pauses)(&path);
        file_lock::lock(&*self.fs, &path, &mut pause).map_err(|e| lock_error(&path, &e))
    }

    /// The flush lock, at once; `None` when another holds it.
    fn flush_lock(&self, session: &Uuid) -> Result<Option<Locked>, HoldError> {
        let path = self.flush_lock_path(session);
        let shown = path.display().to_string();
        let mut at_once = || {
            Err(LockError::Timeout {
                path: shown.clone(),
                waited_ms: 0,
            })
        };
        match file_lock::lock(&*self.fs, &path, &mut at_once) {
            Ok(locked) => Ok(Some(locked)),
            Err(LockError::Timeout { .. }) => Ok(None),
            Err(e) => Err(lock_error(&path, &e)),
        }
    }

    /// The hold's bytes; none when there is none. An existing hold is
    /// tightened 0600 first.
    fn read(&self, path: &Path) -> Result<Option<Vec<u8>>, HoldError> {
        match self.fs.read(path) {
            Ok(bytes) => {
                self.fs
                    .set_permissions(path, Mode::PRIVATE_FILE)
                    .map_err(|e| restrict_error(path, &e))?;
                Ok(Some(bytes))
            }
            Err(e) if e.kind() == io::ErrorKind::NotFound => Ok(None),
            Err(e) => Err(io_error(path, &e)),
        }
    }

    fn scan(&self, path: &Path, bytes: &[u8]) -> Result<Scan, HoldError> {
        frame::scan(bytes, self.longest()).map_err(|_| HoldError::Format {
            path: path.display().to_string(),
        })
    }

    /// Under the append lock: the hold read, its torn tail cut and its
    /// corruption set aside, so what is on disk is its valid prefix.
    /// `None` when there is no hold.
    fn repaired(&self, session: &Uuid) -> Result<Option<(Vec<u8>, Scan)>, HoldError> {
        let path = self.hold_path(session);
        let Some(mut bytes) = self.read(&path)? else {
            return Ok(None);
        };
        let scan = self.scan(&path, &bytes)?;
        if scan.tail == Tail::Corrupt {
            self.set_aside(session, &bytes[scan.valid_end..])?;
        }
        if scan.tail != Tail::Clean {
            // The magic, then the whole frames: a valid prefix past the
            // magic starts with it, and one that tore before its magic was
            // whole (`valid_end` 0) has no frames, so it is the magic alone.
            let frames = bytes.get(MAGIC.len()..scan.valid_end).unwrap_or_default();
            bytes = [&MAGIC[..], frames].concat();
            self.write(&path, &bytes)?;
        }
        Ok(Some((bytes, scan)))
    }

    /// `rest`, the bytes past a hold's valid prefix, kept as the first free
    /// `<root>.held.corrupt-<n>`.
    fn set_aside(&self, session: &Uuid, rest: &[u8]) -> Result<(), HoldError> {
        for n in 1u32.. {
            let path = self.dir.join(format!("{session}.held.corrupt-{n}"));
            match self.fs.open(&path, Open::CreateNew(Mode::PRIVATE_FILE)) {
                Ok(mut file) => {
                    return file.write_all(rest).map_err(|e| io_error(&path, &e));
                }
                Err(e) if e.kind() == io::ErrorKind::AlreadyExists => continue,
                Err(e) => return Err(io_error(&path, &e)),
            }
        }
        unreachable!("u32 set-aside files")
    }

    fn write(&self, path: &Path, bytes: &[u8]) -> Result<(), HoldError> {
        self.fs
            .write_atomic(path, bytes, Perms::Exact(Mode::PRIVATE_FILE))
            .map_err(|e| io_error(path, &e))
    }

    /// The bytes every hold of the home takes but `session`'s.
    fn others_bytes(&self, session: &Uuid) -> Result<u64, HoldError> {
        let own = format!("{session}.held");
        let mut total = 0;
        for entry in self.entries()? {
            let name = entry.to_string_lossy();
            if name.ends_with(".held") && *name != own {
                match self.fs.metadata(&self.dir.join(&entry)) {
                    Ok(meta) => total += meta.len,
                    Err(e) if e.kind() == io::ErrorKind::NotFound => {}
                    Err(e) => return Err(io_error(&self.dir.join(&entry), &e)),
                }
            }
        }
        Ok(total)
    }

    /// The names in the holds' directory; none when there is none.
    fn entries(&self) -> Result<Vec<std::ffi::OsString>, HoldError> {
        match self.fs.read_dir(&self.dir) {
            Ok(entries) => Ok(entries.into_iter().map(|e| e.name).collect()),
            Err(e) if e.kind() == io::ErrorKind::NotFound => Ok(Vec::new()),
            Err(e) => Err(io_error(&self.dir, &e)),
        }
    }

    /// `framed` added to the end of `path`, which holds `held` bytes
    /// (none: it is made, its magic first).
    fn append(&self, path: &Path, held: usize, framed: &[u8]) -> Result<(), HoldError> {
        if held == 0 {
            let mut bytes = MAGIC.to_vec();
            bytes.extend_from_slice(framed);
            return self.write(path, &bytes);
        }
        let mut file = self
            .fs
            .open(path, Open::Append(Mode::PRIVATE_FILE))
            .map_err(|e| io_error(path, &e))?;
        file.write_all(framed).map_err(|e| io_error(path, &e))
    }
}

impl HeldFacts for Spool {
    fn hold(&self, session: &Uuid, fact: HeldFact) -> Result<Held, HoldError> {
        self.private_dir()?;
        let _append = self.append_lock(session)?;
        let path = self.hold_path(session);
        let held = self.repaired(session)?.map_or(0, |(bytes, _)| bytes.len());
        let framed = frame::frame(&HeldEntry::Fact(fact.clone()));
        let size = held.max(MAGIC.len()) as u64 + framed.len() as u64;
        let bound = if size > self.session_bound {
            Some(Bound::Session(self.session_bound))
        } else if self.others_bytes(session)? + size > self.home_bound {
            Some(Bound::Home(self.home_bound))
        } else {
            None
        };
        let Some(bound) = bound else {
            self.append(&path, held, &framed)?;
            return Ok(Held::Kept);
        };
        let gap = frame::frame(&HeldEntry::Gap(HeldGap {
            stamp: fact.stamp,
            external_id: fact.external_id,
            held_at: fact.held_at,
            why: GapWhy::Bound.word().to_string(),
        }));
        let room = held.max(MAGIC.len()) as u64 + gap.len() as u64 <= self.longest();
        if room {
            self.append(&path, held, &gap)?;
        }
        Ok(Held::Full { bound, gap: room })
    }

    fn is_empty(&self, session: &Uuid) -> Result<bool, HoldError> {
        self.private_dir()?;
        let _append = self.append_lock(session)?;
        let path = self.hold_path(session);
        match self.read(&path)? {
            None => Ok(true),
            Some(bytes) => Ok(self.scan(&path, &bytes)?.entries.is_empty()),
        }
    }

    fn sessions(&self) -> Result<Vec<Uuid>, HoldError> {
        let mut roots: Vec<Uuid> = self
            .entries()?
            .iter()
            .filter_map(|name| {
                let name = name.to_str()?;
                Uuid::parse_str(name.strip_suffix(".held")?).ok()
            })
            .collect();
        roots.sort();
        Ok(roots)
    }

    fn take(&self, session: &Uuid) -> Result<Option<Batch>, HoldError> {
        self.private_dir()?;
        let Some(flush) = self.flush_lock(session)? else {
            return Ok(None);
        };
        let (snapshot, scan) = {
            let _append = self.append_lock(session)?;
            match self.repaired(session)? {
                Some((bytes, scan)) => (bytes, scan),
                None => (
                    Vec::new(),
                    Scan {
                        entries: Vec::new(),
                        valid_end: 0,
                        tail: Tail::Clean,
                    },
                ),
            }
        };
        let (entries, frames) = scan.entries.into_iter().unzip();
        Ok(Some(Batch::new(
            entries,
            Box::new(Settling {
                spool: self.clone(),
                session: *session,
                snapshot,
                frames,
                _flush: flush,
            }),
        )))
    }

    fn discard(&self, session: &Uuid) -> Result<u64, HoldError> {
        self.private_dir()?;
        let _append = self.append_lock(session)?;
        let path = self.hold_path(session);
        let facts = match self.read(&path)? {
            None => 0,
            Some(bytes) => match frame::scan(&bytes, self.longest()) {
                Ok(scan) => count_facts(&scan),
                Err(_) => 0,
            },
        };
        let corrupt = format!("{session}.held.corrupt-");
        for name in self.entries()? {
            let name = name.to_string_lossy();
            if name.starts_with(&corrupt) {
                remove(&*self.fs, &self.dir.join(&*name))?;
            }
        }
        remove(&*self.fs, &path)?;
        Ok(facts)
    }

    fn survey(&self) -> Result<Vec<HoldSummary>, HoldError> {
        let mut found: BTreeMap<Uuid, HoldSummary> = BTreeMap::new();
        let blank = |root| HoldSummary {
            root,
            session: None,
            facts: 0,
            gaps: 0,
            bytes: 0,
            stamps: Vec::new(),
            oldest: None,
            set_aside: 0,
            unreadable: false,
            bad_tail: false,
        };
        for name in self.entries()? {
            let Some(name) = name.to_str() else { continue };
            if let Some((root, _)) = name.split_once(".held.corrupt-") {
                if let Ok(root) = Uuid::parse_str(root) {
                    found.entry(root).or_insert_with(|| blank(root)).set_aside += 1;
                }
                continue;
            }
            let Some(root) = name
                .strip_suffix(".held")
                .and_then(|r| Uuid::parse_str(r).ok())
            else {
                continue;
            };
            let path = self.dir.join(name);
            let bytes = match self.fs.read(&path) {
                Ok(bytes) => bytes,
                Err(e) if e.kind() == io::ErrorKind::NotFound => continue,
                Err(e) => return Err(io_error(&path, &e)),
            };
            let summary = found.entry(root).or_insert_with(|| blank(root));
            summary.bytes = bytes.len() as u64;
            let Ok(scan) = frame::scan(&bytes, self.longest()) else {
                summary.unreadable = true;
                continue;
            };
            summary.bad_tail = scan.tail != Tail::Clean;
            for (entry, _) in &scan.entries {
                let at = frame::held_at(entry);
                if at.is_some_and(|at| summary.oldest.is_none_or(|old| earlier(&at, &old))) {
                    summary.oldest = at;
                }
                match entry {
                    HeldEntry::Fact(fact) => {
                        summary.facts += 1;
                        summary.session.get_or_insert_with(|| fact.session.clone());
                        if !summary.stamps.contains(&fact.stamp) {
                            summary.stamps.push(fact.stamp.clone());
                        }
                    }
                    HeldEntry::Gap(_) => summary.gaps += 1,
                }
            }
            summary.stamps.sort();
        }
        Ok(found.into_values().collect())
    }
}

/// A taken hold, waiting to be told what to keep; it holds the flush lock.
struct Settling {
    spool: Spool,
    session: Uuid,
    /// The hold's bytes when taken (none: there was no hold).
    snapshot: Vec<u8>,
    /// Each record's frame in the snapshot.
    frames: Vec<std::ops::Range<usize>>,
    _flush: Locked,
}

impl Settle for Settling {
    fn settle(self: Box<Self>, fates: &[Fate]) -> Result<Settled, HoldError> {
        let spool = &self.spool;
        let _append = spool.append_lock(&self.session)?;
        let path = spool.hold_path(&self.session);
        let Some(now) = spool.read(&path)? else {
            return Ok(if self.snapshot.is_empty() {
                Settled::Emptied
            } else {
                Settled::Replaced
            });
        };
        if self.snapshot.is_empty() || !now.starts_with(&self.snapshot) {
            return Ok(Settled::Replaced);
        }
        let mut kept = 0;
        let mut rewritten = MAGIC.to_vec();
        let mut aside = Vec::new();
        for (frame, fate) in self.frames.iter().zip(fates) {
            match fate {
                Fate::Keep => {
                    rewritten.extend_from_slice(&self.snapshot[frame.clone()]);
                    kept += 1;
                }
                // Its frame as it was, beside the hold, as corruption is
                // (s4-Q14, H3: no silent drop).
                Fate::SetAside => aside.extend_from_slice(&self.snapshot[frame.clone()]),
                Fate::Cut => {}
            }
        }
        if !aside.is_empty() {
            spool.set_aside(&self.session, &aside)?;
        }
        rewritten.extend_from_slice(&now[self.snapshot.len()..]);
        if rewritten.len() == MAGIC.len() {
            remove(&*spool.fs, &path)?;
            return Ok(Settled::Emptied);
        }
        spool.write(&path, &rewritten)?;
        Ok(Settled::Cut { kept })
    }
}

fn count_facts(scan: &Scan) -> u64 {
    scan.entries
        .iter()
        .filter(|(entry, _)| matches!(entry, HeldEntry::Fact(_)))
        .count() as u64
}

fn earlier(
    a: &daemon_proto::google::protobuf::Timestamp,
    b: &daemon_proto::google::protobuf::Timestamp,
) -> bool {
    (a.seconds, a.nanos) < (b.seconds, b.nanos)
}

fn remove(fs: &dyn Fs, path: &Path) -> Result<(), HoldError> {
    match fs.remove_file(path) {
        Err(e) if e.kind() != io::ErrorKind::NotFound => Err(io_error(path, &e)),
        _ => Ok(()),
    }
}

fn io_error(path: &Path, error: &io::Error) -> HoldError {
    HoldError::Io {
        path: path.display().to_string(),
        ext_err_txt: error.to_string(),
    }
}

fn restrict_error(path: &Path, error: &io::Error) -> HoldError {
    HoldError::Restrict {
        path: path.display().to_string(),
        ext_err_txt: error.to_string(),
    }
}

fn lock_error(path: &Path, error: &LockError) -> HoldError {
    HoldError::Lock {
        path: path.display().to_string(),
        ext_err_txt: error.to_string(),
    }
}

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
