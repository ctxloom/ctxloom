//! Delivers an engine's claims on its native files under a directory, and
//! gives them back (S9). The ownership record lives in the home's `records/`
//! (never beside the targets, which are foreign files).
//!
//! The promise, in order of strength:
//!
//! - a file ctxloom made is removed once nothing claims anything in it;
//! - a file that was there comes back byte for byte once nothing claims
//!   anything in it, as long as nobody changed it since ctxloom wrote it;
//! - a file somebody changed since has ctxloom's entries cut out where they
//!   are found, and the rest is theirs;
//! - a claim is replaced, never doubled: delivering an engine's kind again is
//!   releasing it and delivering it.

mod record;
mod render;

use std::io;
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex};

use file_system::{Fs, Kind as FileKind, Perms};
use os::Mode;
use surface_ports::{
    Claim, Delivered, ForeignFiles, Inspected, Kind, Released, SurfaceError, SurfaceStore,
};

use record::{FileRecord, Owned, Record};

/// The store over the process's filesystem.
pub struct SurfaceFiles {
    fs: Arc<dyn Fs>,
    /// The home's `records/` directory.
    records: PathBuf,
    /// A recording store's plan (step 10c U10): what it would have written,
    /// read back by its own later reads; none for a store that writes.
    recorded: Option<Mutex<Vec<Recorded>>>,
}

/// One file a recording store would have written (step 10c U10): another
/// tool's file, or its own record of what it claims, as it would be left.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Recorded {
    /// Its absolute path.
    pub path: String,
    /// Its bytes; none where it would be removed.
    pub bytes: Option<Vec<u8>>,
    pub executable: bool,
    /// The directory a removal empties nothing above (the target, or the
    /// records' directory).
    pub keep: String,
    /// Whether it is the store's own record (the first step of a foreign
    /// writer's sequence, protos.md §1.1).
    pub record: bool,
}

impl SurfaceFiles {
    /// A store over `fs`, keeping its records in `records`.
    pub fn new(fs: Arc<dyn Fs>, records: impl Into<PathBuf>) -> SurfaceFiles {
        SurfaceFiles {
            fs,
            records: records.into(),
            recorded: None,
        }
    }

    /// A store that WRITES NOTHING (step 10c U10): each file it would write
    /// or remove is recorded instead, and its own reads see what it
    /// recorded, so what [`SurfaceFiles::take_recorded`] answers is what a
    /// writing store would have left.
    pub fn recording(fs: Arc<dyn Fs>, records: impl Into<PathBuf>) -> SurfaceFiles {
        SurfaceFiles {
            recorded: Some(Mutex::new(Vec::new())),
            ..SurfaceFiles::new(fs, records)
        }
    }

    /// What this store recorded, once: its records first, then every other
    /// file in the order it was first written; each as it would be left.
    pub fn take_recorded(&self) -> Vec<Recorded> {
        let Some(recorded) = &self.recorded else {
            return Vec::new();
        };
        let taken = std::mem::take(&mut *recorded.lock().unwrap_or_else(|e| e.into_inner()));
        let (mut records, files): (Vec<_>, Vec<_>) = taken.into_iter().partition(|r| r.record);
        records.extend(files);
        records
    }

    /// The bytes at `path`: what this store recorded there, else the disk's.
    fn read_at(&self, path: &Path) -> Result<Option<Vec<u8>>, SurfaceError> {
        if let Some(recorded) = &self.recorded {
            let recorded = recorded.lock().unwrap_or_else(|e| e.into_inner());
            let key = path.display().to_string();
            if let Some(found) = recorded.iter().find(|r| r.path == key) {
                return Ok(found.bytes.clone());
            }
        }
        read(&*self.fs, path)
    }

    /// `bytes` at `path`, or (none) the file removed up to `keep`: written,
    /// or recorded by a recording store.
    fn put_at(
        &self,
        path: &Path,
        bytes: Option<&[u8]>,
        (executable, keep, record): (bool, &Path, bool),
    ) -> Result<(), SurfaceError> {
        let Some(recorded) = &self.recorded else {
            return match bytes {
                Some(bytes) => write(&*self.fs, path, bytes, executable),
                None if record => match self.fs.remove_file(path) {
                    Err(e) if e.kind() != io::ErrorKind::NotFound => Err(io_error(path, &e)),
                    _ => Ok(()),
                },
                None => remove(&*self.fs, &keep.display().to_string(), path),
            };
        };
        let entry = Recorded {
            path: path.display().to_string(),
            bytes: bytes.map(<[u8]>::to_vec),
            executable,
            keep: keep.display().to_string(),
            record,
        };
        let mut recorded = recorded.lock().unwrap_or_else(|e| e.into_inner());
        match recorded.iter_mut().find(|r| r.path == entry.path) {
            Some(there) => *there = entry,
            None => recorded.push(entry),
        }
        Ok(())
    }

    fn record_path(&self, target: &str) -> PathBuf {
        let name: String = target
            .chars()
            .map(|c| {
                if c.is_ascii_alphanumeric() || c == '-' || c == '.' {
                    c
                } else {
                    '_'
                }
            })
            .collect();
        self.records.join(format!("{name}.claims.yaml"))
    }

    fn load(&self, target: &str) -> Result<Record, SurfaceError> {
        let path = self.record_path(target);
        match self.read_at(&path)? {
            Some(bytes) => {
                let text = String::from_utf8(bytes).map_err(|e| SurfaceError::Io {
                    file: path.display().to_string(),
                    detail: e.to_string(),
                })?;
                Record::decode(&path.display().to_string(), &text)
            }
            None => Ok(Record::default()),
        }
    }

    fn save(&self, target: &str, record: &Record) -> Result<(), SurfaceError> {
        let path = self.record_path(target);
        let keep = self.records.clone();
        if record.files.is_empty() {
            return self.put_at(&path, None, (false, &keep, true));
        }
        let text = record.encode(target);
        self.put_at(&path, Some(text.as_bytes()), (false, &keep, true))
    }
}

fn io_error(path: &Path, e: &io::Error) -> SurfaceError {
    SurfaceError::Io {
        file: path.display().to_string(),
        detail: e.to_string(),
    }
}

/// A path below the target, refused where it would leave it.
fn below(target: &str, rel: &str) -> Result<PathBuf, SurfaceError> {
    let path = Path::new(rel);
    let safe = !rel.is_empty()
        && path
            .components()
            .all(|c| matches!(c, std::path::Component::Normal(_)));
    if !safe {
        return Err(SurfaceError::Refused {
            file: rel.to_string(),
            reason: "it is not a path inside the target".to_string(),
        });
    }
    Ok(Path::new(target).join(path))
}

fn read(fs: &dyn Fs, path: &Path) -> Result<Option<Vec<u8>>, SurfaceError> {
    match fs.read(path) {
        Ok(bytes) => Ok(Some(bytes)),
        Err(e) if e.kind() == io::ErrorKind::NotFound => Ok(None),
        Err(e) => Err(io_error(path, &e)),
    }
}

/// `bytes` written whole and atomically, its directories made; an existing
/// file keeps its mode unless `executable` asks for 0755 (a new file is 0644).
fn write(fs: &dyn Fs, path: &Path, bytes: &[u8], executable: bool) -> Result<(), SurfaceError> {
    let fail = |e: io::Error| io_error(path, &e);
    if let Some(dir) = path.parent() {
        fs.create_dir_all(dir, Mode::DIR).map_err(fail)?;
    }
    let perms = if executable {
        Perms::Exact(Mode::from_bits(0o755))
    } else {
        Perms::KeepOr(Mode::FILE)
    };
    fs.write_atomic(path, bytes, perms).map_err(fail)
}

/// A file removed, and the directories it leaves empty (never the target).
fn remove(fs: &dyn Fs, target: &str, path: &Path) -> Result<(), SurfaceError> {
    match fs.remove_file(path) {
        Err(e) if e.kind() != io::ErrorKind::NotFound => return Err(io_error(path, &e)),
        _ => {}
    }
    let mut dir = path.parent();
    while let Some(d) = dir {
        let empty = fs.read_dir(d).is_ok_and(|entries| entries.is_empty());
        if d == Path::new(target) || !empty || fs.remove_dir_all(d).is_err() {
            break;
        }
        dir = d.parent();
    }
    Ok(())
}

/// A file's settled state after its claims changed.
struct Settled {
    /// The claims the file now carries.
    record: Option<FileRecord>,
    changed: bool,
}

impl SurfaceFiles {
    /// Bring the file `rel` to hold exactly `rec.claims`, writing it unless
    /// `dry`. `rec` is the file's record with the claims already changed;
    /// `prior` the claims it held before.
    fn settle(
        &self,
        target: &str,
        rel: &str,
        mut rec: FileRecord,
        prior: &[Owned],
        dry: bool,
        left: &mut Vec<String>,
    ) -> Result<Settled, SurfaceError> {
        // The file is known when the record already held claims in it.
        let known = !prior.is_empty();
        let path = below(target, rel)?;
        let current = self.read_at(&path)?;
        // What the file held before ctxloom's claims, unless somebody has
        // changed it since: then ctxloom's old entries are cut out of what it
        // holds now.
        let unchanged = known && current.as_deref() == Some(rec.written.as_slice());
        let base: Option<Vec<u8>> = if !known {
            rec.existed = current.is_some();
            current.clone()
        } else if unchanged {
            rec.existed.then(|| rec.original.clone())
        } else if let Some(now) = &current {
            let old: Vec<&Claim> = prior.iter().map(|o| &o.claim).collect();
            let cleaned = render::unrender(rel, now, &old)?;
            rec.existed = true;
            Some(cleaned)
        } else {
            rec.existed = false;
            None
        };
        rec.original = base.clone().unwrap_or_default();

        if rec.claims.is_empty() {
            let changed = match (&base, &current) {
                (Some(base), Some(now)) => base != now,
                (None, Some(_)) => true,
                (Some(_), None) => true,
                (None, None) => false,
            };
            if !dry && changed {
                let keep = Path::new(target);
                self.put_at(&path, base.as_deref(), (false, keep, false))?;
            }
            return Ok(Settled {
                record: None,
                changed,
            });
        }

        let claims: Vec<&Claim> = rec.claims.iter().map(|o| &o.claim).collect();
        let bytes = render::render(rel, base.as_deref(), &claims, left)?;
        let executable = claims.iter().any(|c| {
            matches!(
                c,
                Claim::Whole {
                    executable: true,
                    ..
                }
            )
        });
        let changed = current.as_deref() != Some(bytes.as_slice());
        if !dry && changed {
            self.put_at(&path, Some(&bytes), (executable, Path::new(target), false))?;
        }
        rec.written = bytes;
        Ok(Settled {
            record: Some(rec),
            changed,
        })
    }

    /// Change the claims of (`engine`, `kind`) to `claims` in the record, and
    /// settle every file that touches.
    fn change(
        &self,
        target: &str,
        engine: &str,
        kind: Kind,
        claims: &[Claim],
        dry: bool,
    ) -> Result<(Vec<String>, Vec<String>, bool), SurfaceError> {
        let mut record = self.load(target)?;
        let mut touched: Vec<String> = Vec::new();
        let mut prior: std::collections::BTreeMap<String, Vec<Owned>> = Default::default();
        // Where each file held the claims being replaced: they go back in
        // that place, so that delivering a kind again leaves the file as it
        // was, whichever kinds were delivered after it.
        let mut slot: std::collections::BTreeMap<String, usize> = Default::default();
        for (rel, file) in &mut record.files {
            let before = file.claims.clone();
            let first = before
                .iter()
                .position(|o| o.engine == engine && o.kind == kind);
            file.claims
                .retain(|o| !(o.engine == engine && o.kind == kind));
            if file.claims.len() != before.len() {
                touched.push(rel.clone());
                prior.insert(rel.clone(), before);
                slot.extend(first.map(|at| (rel.clone(), at)));
            }
        }
        for claim in claims {
            let rel = claim.path().to_string();
            below(target, &rel)?;
            let known = record.files.contains_key(&rel);
            let file = record.files.entry(rel.clone()).or_default();
            prior.entry(rel.clone()).or_insert_with(|| {
                if known {
                    file.claims.clone()
                } else {
                    Vec::new()
                }
            });
            let at = slot
                .get(&rel)
                .map_or(file.claims.len(), |at| (*at).min(file.claims.len()));
            file.claims.insert(
                at,
                Owned {
                    engine: engine.to_string(),
                    kind,
                    claim: claim.clone(),
                },
            );
            slot.insert(rel.clone(), at + 1);
            if !touched.contains(&rel) {
                touched.push(rel);
            }
        }
        let mut left = Vec::new();
        let mut changed = false;
        let mut next = record.clone();
        for rel in &touched {
            let rec = record.files.get(rel).cloned().unwrap_or_default();
            let before = prior.get(rel).cloned().unwrap_or_default();
            let settled = self.settle(target, rel, rec, &before, dry, &mut left)?;
            changed |= settled.changed;
            match settled.record {
                Some(rec) => {
                    next.files.insert(rel.clone(), rec);
                }
                None => {
                    next.files.remove(rel);
                }
            }
        }
        if !dry {
            self.save(target, &next)?;
        }
        Ok((touched, left, changed))
    }
}

impl SurfaceStore for SurfaceFiles {
    fn deliver(
        &self,
        target: &str,
        engine: &str,
        kind: Kind,
        claims: &[Claim],
        dry: bool,
    ) -> Result<Delivered, SurfaceError> {
        let (mut files, left, changed) = self.change(target, engine, kind, claims, dry)?;
        files.retain(|f| claims.iter().any(|c| c.path() == f));
        Ok(Delivered {
            files,
            left,
            changed,
        })
    }

    fn release(
        &self,
        target: &str,
        engine: &str,
        kind: Kind,
        dry: bool,
    ) -> Result<Released, SurfaceError> {
        let (files, _, changed) = self.change(target, engine, kind, &[], dry)?;
        Ok(Released { files, changed })
    }

    fn claimed(&self, target: &str, engine: &str) -> Result<Vec<Kind>, SurfaceError> {
        let record = self.load(target)?;
        let mut kinds: Vec<Kind> = record
            .files
            .values()
            .flat_map(|f| f.claims.iter())
            .filter(|o| o.engine == engine)
            .map(|o| o.kind)
            .collect();
        kinds.sort();
        kinds.dedup();
        Ok(kinds)
    }

    fn read(&self, target: &str, rel: &str) -> Result<Option<Vec<u8>>, SurfaceError> {
        self.read_at(&below(target, rel)?)
    }

    fn read_absolute(&self, path: &str) -> Result<Option<Vec<u8>>, SurfaceError> {
        read(&*self.fs, Path::new(path))
    }

    fn inspect(&self, target: &str) -> Result<Inspected, SurfaceError> {
        match self.fs.metadata(Path::new(target)) {
            Ok(meta) if meta.kind == FileKind::Dir => Ok(Inspected::Directory),
            Ok(_) => Ok(Inspected::Other),
            Err(e) if e.kind() == io::ErrorKind::NotFound => Ok(Inspected::Missing),
            Err(e) => Err(io_error(Path::new(target), &e)),
        }
    }
}

impl ForeignFiles for SurfaceFiles {
    fn read(&self, path: &str) -> Result<Option<Vec<u8>>, SurfaceError> {
        read(&*self.fs, Path::new(path))
    }

    fn write(&self, path: &str, bytes: &[u8]) -> Result<(), SurfaceError> {
        write(&*self.fs, Path::new(path), bytes, false)
    }

    fn record(&self, path: &str) -> Result<Option<String>, SurfaceError> {
        let at = self.foreign_record(path);
        match self.fs.read_to_string(&at) {
            Ok(text) => Ok(Some(text)),
            Err(e) if e.kind() == io::ErrorKind::NotFound => Ok(None),
            Err(e) => Err(io_error(&at, &e)),
        }
    }

    fn write_record(&self, path: &str, text: &str) -> Result<String, SurfaceError> {
        let at = self.foreign_record(path);
        write(&*self.fs, &at, text.as_bytes(), false)?;
        Ok(at.display().to_string())
    }
}

impl SurfaceFiles {
    fn foreign_record(&self, path: &str) -> PathBuf {
        let name: String = path
            .chars()
            .map(|c| {
                if c.is_ascii_alphanumeric() || c == '-' || c == '.' {
                    c
                } else {
                    '_'
                }
            })
            .collect();
        self.records.join(format!("{name}.hew-record.yaml"))
    }
}

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
