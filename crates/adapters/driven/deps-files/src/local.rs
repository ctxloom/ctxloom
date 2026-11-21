//! The bundles the project authors: read and removed.

use std::collections::BTreeMap;
use std::path::{Component, Path, PathBuf};

use deps_ports::Why;
use file_system::Kind;

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
