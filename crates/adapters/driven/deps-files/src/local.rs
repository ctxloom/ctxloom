//! The bundles the project authors: read, removed, and copied elsewhere.

use std::collections::BTreeMap;
use std::path::{Component, Path, PathBuf};

use deps_ports::{CopyOutcome, Why};
use file_system::{Kind, Perms};
use os::Mode;

use crate::ProjectFiles;

/// Where bundles are below an app dir.
const BUNDLES: &str = "content/bundles/v2";

/// Whether `name` is no bundle's path below the bundles directory: empty,
/// with a `.` step, or anything but plain names once the host has read it as
/// a path (a `..` step, a root: `/x`, and on Windows `\x` or `C:x`, which a
/// join would put outside the directory, as it would an absolute path).
fn climbs(name: &str) -> bool {
    name.is_empty()
        || name.split('/').any(|part| part == ".")
        || Path::new(name)
            .components()
            .any(|part| !matches!(part, Component::Normal(_) | Component::CurDir))
}

/// The directory of the authored bundle `name`; why it is none if the name
/// climbs out of the bundles directory.
fn tree_of(files: &ProjectFiles, name: &str) -> Result<PathBuf, Why> {
    if climbs(name) {
        return Err(format!("{name:?} is not the name of a bundle"));
    }
    Ok(crate::below(&crate::below(&files.app_dir, BUNDLES), name))
}

pub(crate) fn manifest(files: &ProjectFiles, name: &str) -> Result<Option<String>, Why> {
    let manifest = tree_of(files, name)?.join("bundle.yaml");
    Ok((files.kind_of(&manifest) == Some(Kind::File)).then(|| manifest.display().to_string()))
}

pub(crate) fn read(files: &ProjectFiles, name: &str) -> Result<BTreeMap<String, Vec<u8>>, Why> {
    let top = tree_of(files, name)?;
    let mut out = BTreeMap::new();
    collect(files, &top, &top, &mut out)?;
    Ok(out)
}

fn collect(
    files: &ProjectFiles,
    top: &Path,
    dir: &Path,
    out: &mut BTreeMap<String, Vec<u8>>,
) -> Result<(), Why> {
    for entry in files.fs.read_dir(dir).map_err(|e| e.to_string())? {
        let path = dir.join(&entry.name);
        match files.kind_of(&path) {
            Some(Kind::Dir) => collect(files, top, &path, out)?,
            Some(Kind::File) => {
                let relative = path
                    .strip_prefix(top)
                    .unwrap_or(&path)
                    .components()
                    .map(|c| c.as_os_str().to_string_lossy().into_owned())
                    .collect::<Vec<_>>()
                    .join("/");
                out.insert(relative, files.fs.read(&path).map_err(|e| e.to_string())?);
            }
            _ => {}
        }
    }
    Ok(())
}

pub(crate) fn remove(files: &ProjectFiles, name: &str) -> Result<(), Why> {
    files
        .fs
        .remove_dir_all(&tree_of(files, name)?)
        .map_err(|e| e.to_string())
}

/// The bundles directory a destination `dir` takes a bundle into: a
/// `.ctxloom` directory itself, a project checkout holding one, or any other
/// directory as it is (a plain directory has no layout).
fn destination(files: &ProjectFiles, dir: &str) -> PathBuf {
    let dir = Path::new(dir);
    if dir.file_name().is_some_and(|n| n == ".ctxloom") {
        return crate::below(dir, BUNDLES);
    }
    let app = dir.join(".ctxloom");
    if files.kind_of(&app) == Some(Kind::Dir) {
        return crate::below(&app, BUNDLES);
    }
    dir.to_path_buf()
}

pub(crate) fn copy(
    files: &ProjectFiles,
    dir: &str,
    name: &str,
    contents: &BTreeMap<String, Vec<u8>>,
    force: bool,
) -> Result<CopyOutcome, Why> {
    let target = crate::below(&destination(files, dir), name);
    let shown = target.display().to_string();
    let own = tree_of(files, name)?;
    let same = |a: &Path, b: &Path| match (files.fs.canonicalize(a), files.fs.canonicalize(b)) {
        (Ok(a), Ok(b)) => a == b,
        _ => a == b,
    };
    if same(&target, &own) {
        return Ok(CopyOutcome::Itself(shown));
    }
    if files.fs.exists(&target).unwrap_or(false) {
        if !force {
            return Ok(CopyOutcome::Exists(shown));
        }
        files
            .fs
            .remove_dir_all(&target)
            .map_err(|e| e.to_string())?;
    }
    for (path, data) in contents {
        let at = target.join(path);
        if let Some(parent) = at.parent() {
            files
                .fs
                .create_dir_all(parent, Mode::from_bits(0o755))
                .map_err(|e| e.to_string())?;
        }
        files
            .fs
            .write_atomic(&at, data, Perms::Exact(Mode::from_bits(0o644)))
            .map_err(|e| e.to_string())?;
    }
    Ok(CopyOutcome::Copied(shown))
}
