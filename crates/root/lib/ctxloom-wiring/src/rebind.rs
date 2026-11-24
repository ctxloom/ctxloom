//! A re-created repository's root re-bound (owner ruling 2026-11-19, batch
//! 3): where a first `init` names a project at a root whose `.ctxloom` is
//! gone but ctxloom's home still binds the root to another project id (a
//! repository deleted and made again at its place), the home index's old
//! binding is taken out, so the registry binds the root to the id the
//! daemon named. The old project's sessions and store rows stay under the
//! old id, orphaned, until cleaned.
//!
//! The registry's index is taskloom's (`project-registry`), which has no
//! verb for it: this reads and writes the same file as the registry does,
//! under the registry's own lock (`index.yaml.lock`), strictly: a file that
//! does not read, or whose entries are not as the registry writes them, is
//! left as it is and the bind is refused.

use std::path::{Path, PathBuf};

use angzarr_yaml::Value;
use coded_diagnostic::Report;
use file_system::{Fs, Perms};
use os::Mode;
use project_registry::RegistryError;

/// The registry file under `home`.
fn index_path(home: &Path) -> PathBuf {
    home.join(".ctxloom").join("projects").join("index.yaml")
}

/// `path` as the registry compares it: symlinks resolved when it exists,
/// the host's display form, then the flavor's comparison key.
fn key(fs: &dyn Fs, path: &Path) -> String {
    let resolved = fs.canonicalize(path).unwrap_or_else(|_| path.to_path_buf());
    let flavor = fs.os().flavor();
    let text = resolved.to_string_lossy();
    flavor.path_key(&flavor.display_path(&text))
}

/// The registry's text with every entry for `root` that names a project
/// other than `keep` taken out; none when nothing is taken.
fn without_stale(fs: &dyn Fs, text: &str, root: &Path, keep: &str) -> Option<String> {
    let mut document = angzarr_yaml::parse(text).ok()?;
    let Value::Map(fields) = &mut document else {
        return None;
    };
    let wanted = key(fs, root);
    let mut taken = false;
    for (name, value) in fields.iter_mut() {
        let (true, Value::Seq(entries)) = (name == "projects", value) else {
            continue;
        };
        entries.retain(|entry| {
            let Value::Map(entry) = entry else {
                return true;
            };
            let field = |wanted: &str| {
                entry.iter().find_map(|(name, value)| match value {
                    Value::Scalar(text) if name == wanted => Some(text.as_str()),
                    _ => None,
                })
            };
            let stale = field("path").is_some_and(|path| key(fs, Path::new(path)) == wanted)
                && field("project_id") != Some(keep);
            taken |= stale;
            !stale
        });
    }
    taken.then(|| angzarr_yaml::write(&document, usize::MAX))
}

/// The home's index (under `home`) with `root`'s binding to any project but
/// `keep` taken out, under the registry's lock; whether one was.
pub(crate) fn unbind_stale(
    fs: &dyn Fs,
    home: &Path,
    root: &Path,
    keep: &str,
) -> Result<bool, Report> {
    let path = index_path(home);
    let failed = |detail: String| {
        Report::of(&RegistryError::Io {
            path: path.display().to_string(),
            detail,
        })
    };
    match fs.read_to_string(&path) {
        Ok(_) => {}
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(false),
        Err(e) => return Err(failed(e.to_string())),
    }
    let lock = path.with_file_name("index.yaml.lock");
    let _locked =
        file_lock::lock_within(fs, &lock, file_lock::LOCK_WAIT).map_err(|e| Report::of(&e))?;
    // Read again under the lock: another process may have written meanwhile.
    let text = fs
        .read_to_string(&path)
        .map_err(|e| failed(e.to_string()))?;
    let Some(rewritten) = without_stale(fs, &text, root, keep) else {
        return Ok(false);
    };
    fs.write_atomic(
        &path,
        rewritten.as_bytes(),
        Perms::Exact(Mode::PRIVATE_FILE),
    )
    .map_err(|e| failed(e.to_string()))?;
    Ok(true)
}

#[cfg(test)]
#[path = "rebind.test.rs"]
mod tests;
