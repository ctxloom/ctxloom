//! The version stamp, the ctxloom family's standard (Go's `version :=`):
//!
//! - a clean tree: `v<MAJOR.MINOR.PATCH[-pre]>-<short hash>`, so clean builds
//!   of one commit are the same binary and cache;
//! - a dirty tree (a TRACKED file changed; `.versionator.yaml` sets
//!   `dirty.ignoreUntracked`): that, then `-<YYYYMMDDTHHMMSS>-dirty`, the
//!   build's UTC time, so the builds before a commit are told apart.
//!
//! The build script (`build.rs`) runs versionator and composes the stamp
//! here; the program checks it with [`valid`] before it runs anything. The
//! file is std alone, because the build script includes it too.

use std::path::{Path, PathBuf};

/// How to install versionator, said whenever the build cannot run it.
pub const INSTALL: &str =
    "install it with `go install github.com/benjaminabbitt/versionator@v0.3.0` \
     (or from its repository, github.com/benjaminabbitt/versionator) and put it on PATH";

/// What the build asks versionator for besides the version itself: the
/// prefix, the short hash, the build's UTC time and whether the tree is dirty.
pub const FIELDS_TEMPLATE: &str = "{{Prefix}}|{{ShortHash}}|{{BuildDateTimeCompact}}|{{Dirty}}";

/// A composed stamp, and whether the tree it names was dirty.
#[derive(Debug, PartialEq, Eq)]
pub struct Stamp {
    pub text: String,
    pub dirty: bool,
}

/// The stamp of `semver` (`versionator output version`) and `fields`
/// ([`FIELDS_TEMPLATE`] rendered), or why there is none.
pub fn compose(semver: &str, fields: &str) -> Result<Stamp, String> {
    let (semver, fields) = (semver.trim(), fields.trim());
    let parts: Vec<&str> = fields.split('|').collect();
    let [prefix, hash, time, dirty] = parts[..] else {
        return Err(format!(
            "versionator's fields `{fields}` are not the 4 fields of `{FIELDS_TEMPLATE}`"
        ));
    };
    let dirty = match dirty {
        "" => false,
        "dirty" => true,
        other => {
            return Err(format!(
                "versionator said the tree is `{other}`, neither `dirty` nor nothing"
            ))
        }
    };
    let text = if dirty {
        format!(
            "{prefix}{semver}-{hash}-{}-dirty",
            build_time(time, fields)?
        )
    } else {
        build_time(time, fields)?;
        format!("{prefix}{semver}-{hash}")
    };
    if !valid(&text) {
        return Err(format!(
            "the version stamp `{text}` is not of the shape \
             v<MAJOR.MINOR.PATCH[-pre]>-<short hash>[-<YYYYMMDDTHHMMSS>-dirty] \
             (no hash: no commit yet, or a versionator older than v0.3.0, \
             which finds none in a git worktree; {INSTALL})"
        ));
    }
    Ok(Stamp { text, dirty })
}

/// `YYYYMMDDHHMMSS` as `YYYYMMDDTHHMMSS`.
fn build_time(compact: &str, fields: &str) -> Result<String, String> {
    if compact.len() != 14 || !digits(compact) {
        return Err(format!(
            "versionator's build time in `{fields}` is not 14 digits (YYYYMMDDHHMMSS)"
        ));
    }
    Ok(format!("{}T{}", &compact[..8], &compact[8..]))
}

/// Whether `stamp` is a whole stamp: every field present, of its shape.
pub fn valid(stamp: &str) -> bool {
    let Some(rest) = stamp.strip_prefix('v') else {
        return false;
    };
    let rest = match rest.strip_suffix("-dirty") {
        Some(dirty) => match dirty.rsplit_once('-') {
            Some((rest, time)) if is_time(time) => rest,
            _ => return false,
        },
        None => rest,
    };
    let Some((version, hash)) = rest.rsplit_once('-') else {
        return false;
    };
    is_hash(hash) && is_version(version)
}

fn digits(text: &str) -> bool {
    !text.is_empty() && text.bytes().all(|b| b.is_ascii_digit())
}

fn is_time(text: &str) -> bool {
    match text.split_once('T') {
        Some((date, time)) => date.len() == 8 && time.len() == 6 && digits(date) && digits(time),
        None => false,
    }
}

fn is_hash(text: &str) -> bool {
    !text.is_empty() && text.bytes().all(|b| matches!(b, b'0'..=b'9' | b'a'..=b'f'))
}

/// `MAJOR.MINOR.PATCH`, then an optional `-pre` of dot-separated
/// identifiers of letters, digits and dashes. No build metadata.
fn is_version(text: &str) -> bool {
    let (core, pre) = match text.split_once('-') {
        Some((core, pre)) => (core, Some(pre)),
        None => (text, None),
    };
    let numbers: Vec<&str> = core.split('.').collect();
    numbers.len() == 3
        && numbers.iter().all(|n| digits(n))
        && pre.is_none_or(|pre| {
            pre.split('.').all(|id| {
                !id.is_empty() && id.bytes().all(|b| b.is_ascii_alphanumeric() || b == b'-')
            })
        })
}

/// Why the build cannot run versionator at all.
pub fn versionator_unavailable(why: &str) -> String {
    format!("ctxloom's version stamp needs versionator, which could not be run ({why}): {INSTALL}")
}

/// Why versionator, run with `args`, did not answer.
pub fn versionator_failed(args: &[&str], status: &str, stderr: &str) -> String {
    format!(
        "`versionator {}` failed ({status}): {}; ctxloom's version stamp needs it: {INSTALL}",
        args.join(" "),
        stderr.trim()
    )
}

/// Why there is no version to stamp: the VERSION file is missing (and
/// versionator would write one rather than say so).
pub fn no_version_file(path: &Path) -> String {
    format!(
        "{} is missing: ctxloom's version stamp is versionator's reading of it",
        path.display()
    )
}

/// The git directory of the checkout at `root`: `.git` itself, or the one a
/// worktree's `.git` FILE (`dot_git_file`, its text) names.
pub fn git_dir(root: &Path, dot_git_file: Option<&str>) -> Option<PathBuf> {
    match dot_git_file {
        None => Some(root.join(".git")),
        Some(text) => {
            let named = text.trim().strip_prefix("gitdir:")?.trim();
            (!named.is_empty()).then(|| root.join(named))
        }
    }
}

/// The directory a worktree shares with its main checkout (refs,
/// packed-refs): what its `commondir` file names, else the git directory.
pub fn common_dir(git_dir: &Path, commondir: Option<&str>) -> PathBuf {
    match commondir.map(str::trim) {
        Some(named) if !named.is_empty() => git_dir.join(named),
        _ => git_dir.to_path_buf(),
    }
}

/// The ref `HEAD` names (`ref: refs/heads/<branch>`), none when detached.
pub fn head_ref(head: &str) -> Option<&str> {
    head.trim().strip_prefix("ref:").map(str::trim)
}

/// The git files whose change is a commit, a checkout or a staged edit.
pub fn git_watched(git_dir: &Path, common_dir: &Path, head_ref: Option<&str>) -> Vec<PathBuf> {
    let mut watched = vec![
        git_dir.join("HEAD"),
        git_dir.join("index"),
        common_dir.join("packed-refs"),
    ];
    watched.extend(head_ref.map(|r| common_dir.join(r)));
    watched
}

/// The files of `git ls-files -z`'s `listing`, under `root`.
pub fn tracked(root: &Path, listing: &[u8]) -> Vec<PathBuf> {
    listing
        .split(|&b| b == 0)
        .filter(|name| !name.is_empty())
        .map(|name| root.join(String::from_utf8_lossy(name).as_ref()))
        .collect()
}

#[cfg(test)]
#[path = "stamp.test.rs"]
mod tests;
