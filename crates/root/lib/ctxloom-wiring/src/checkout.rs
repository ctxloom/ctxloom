//! A library tree's checkout harp (step 10a U0; L2, QUESTIONS B(a)): the
//! one name of a checkout, kept in its `.ctxloom/state/checkout`, which is
//! out of git (the scaffold's `.gitignore` holds `/state`), so a git
//! worktree of a project, or a clone, is a checkout of its own. The home's
//! `.ctxloom` keeps one too, so each `CTXLOOM_DAEMON_ROOT` is a stream of its own.
//!
//! It is minted at the tree's first read (U2), never by a write; reading
//! it mints nothing. The file holds the harp and a newline.

use std::io::{ErrorKind, Write};
use std::path::{Path, PathBuf};

use file_system::{Fs, Open, Perms};
use library_tree::{TreeName, CHECKOUT_FILE};
use os::Mode;
use sessions::ProjectName;

use crate::LibError;

/// Where the checkout of the tree at `app_dir` (its `.ctxloom`) is named.
pub fn checkout_file(app_dir: &Path) -> PathBuf {
    CHECKOUT_FILE
        .split('/')
        .fold(app_dir.to_path_buf(), |path, part| path.join(part))
}

fn failed(path: &Path, detail: impl ToString) -> LibError {
    LibError::Checkout {
        path: path.display().to_string(),
        detail: detail.to_string(),
    }
}

/// The harp the file at `path` holds; none when there is no file, or an
/// empty one (a mint cut short).
fn read(fs: &dyn Fs, path: &Path) -> Result<Option<String>, LibError> {
    match fs.read_to_string(path) {
        Ok(text) => {
            let harp = text.trim();
            if harp.is_empty() {
                return Ok(None);
            }
            sessions::validate(harp).map_err(|e| failed(path, e))?;
            Ok(Some(harp.to_string()))
        }
        Err(e) if e.kind() == ErrorKind::NotFound => Ok(None),
        Err(e) => Err(failed(path, e)),
    }
}

/// The checkout harp of the tree at `app_dir`, if one was minted.
pub fn checkout_harp(fs: &dyn Fs, app_dir: &Path) -> Result<Option<String>, LibError> {
    read(fs, &checkout_file(app_dir))
}

/// The checkout harp of the tree at `app_dir`, minted from `fresh` when it
/// has none: claimed by creating the file (`O_EXCL`), so two first reads
/// agree on one; a file left empty by a mint cut short is written again.
pub fn mint_checkout_harp(
    fs: &dyn Fs,
    app_dir: &Path,
    fresh: impl FnOnce() -> String,
) -> Result<String, LibError> {
    let path = checkout_file(app_dir);
    if let Some(harp) = read(fs, &path)? {
        return Ok(harp);
    }
    let harp = fresh();
    sessions::validate(&harp).map_err(|e| failed(&path, e))?;
    let line = format!("{harp}\n");
    if let Some(state) = path.parent() {
        fs.create_dir_all(state, Mode::from_bits(0o755))
            .map_err(|e| failed(state, e))?;
    }
    match fs.open(&path, Open::CreateNew(Mode::from_bits(0o644))) {
        Ok(mut file) => {
            file.write_all(line.as_bytes())
                .map_err(|e| failed(&path, e))?;
            Ok(harp)
        }
        Err(e) if e.kind() == ErrorKind::AlreadyExists => match read(fs, &path)? {
            Some(theirs) => Ok(theirs),
            None => {
                fs.write_atomic(&path, line.as_bytes(), Perms::Exact(Mode::from_bits(0o644)))
                    .map_err(|e| failed(&path, e))?;
                Ok(harp)
            }
        },
        Err(e) => Err(failed(&path, e)),
    }
}

/// The tree of the checkout of `project` whose harp is `checkout`.
pub fn checkout_tree(project: &ProjectName, checkout: &str) -> Result<TreeName, LibError> {
    TreeName::checkout(project.clone(), checkout).map_err(|e| LibError::Checkout {
        path: checkout.to_string(),
        detail: e.to_string(),
    })
}

#[cfg(test)]
#[path = "checkout.test.rs"]
mod tests;
