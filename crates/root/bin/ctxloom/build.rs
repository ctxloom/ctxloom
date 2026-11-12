//! The binary's version stamp (`src/stamp.rs`, the shape and its pieces):
//! versionator's reading of the repository's VERSION and of git, passed to
//! rustc as `CTXLOOM_VERSION_STAMP`. No versionator, no build: the error
//! names it and how to install it. It shells out and reads files only; what
//! it makes of them is `stamp`'s, which the binary's tests hold.
//!
//! It runs again when a commit, a checkout, a staged change or an edit to a
//! tracked file could change the stamp (the git directory's HEAD, index, the
//! branch's ref and packed-refs, each tracked file, VERSION and
//! .versionator.yaml); on a dirty tree, at every build, so each build has a
//! time of its own.

// A build script runs before any crate, the file-system seam included: it
// reads the git directory's files itself.
#![allow(clippy::disallowed_methods)]

use std::path::Path;
use std::process::Command;

#[path = "src/stamp.rs"]
mod stamp;

fn main() {
    if let Err(why) = stamped() {
        eprintln!("error: {why}");
        std::process::exit(1);
    }
}

fn stamped() -> Result<(), String> {
    let manifest = Path::new(env!("CARGO_MANIFEST_DIR"));
    // crates/root/bin/ctxloom: the repository is four up.
    let root = manifest
        .ancestors()
        .nth(4)
        .ok_or("the binary's crate is not at crates/root/bin/ctxloom")?;
    let version = root.join("VERSION");
    watch(&version);
    watch(&root.join(".versionator.yaml"));
    if !version.is_file() {
        return Err(stamp::no_version_file(&version));
    }
    let semver = versionator(root, &["output", "version"])?;
    let fields = versionator(
        root,
        &[
            "output",
            "version",
            "-t",
            stamp::FIELDS_TEMPLATE,
            "--prefix",
        ],
    )?;
    let stamp = stamp::compose(&semver, &fields)?;
    if stamp.dirty {
        // Never there, so cargo runs this again at every build.
        watch(&manifest.join("build.rs.dirty-tree-rebuilds-every-time"));
    } else {
        watch_git(root)?;
    }
    println!("cargo::rustc-env=CTXLOOM_VERSION_STAMP={}", stamp.text);
    Ok(())
}

fn watch(path: &Path) {
    println!("cargo::rerun-if-changed={}", path.display());
}

/// The git files and each tracked file: a clean tree's stamp holds until one
/// of them changes.
fn watch_git(root: &Path) -> Result<(), String> {
    let dot_git = root.join(".git");
    let file = dot_git.is_file().then(|| read(&dot_git)).transpose()?;
    let git_dir = stamp::git_dir(root, file.as_deref())
        .ok_or_else(|| format!("{} names no git directory", dot_git.display()))?;
    let commondir = read_if_there(&git_dir.join("commondir"));
    let common = stamp::common_dir(&git_dir, commondir.as_deref());
    let head = read(&git_dir.join("HEAD"))?;
    for path in stamp::git_watched(&git_dir, &common, stamp::head_ref(&head)) {
        watch(&path);
    }
    let listing = run(root, "git", &["ls-files", "-z"])
        .map_err(|why| format!("`git ls-files` failed: {why}"))?;
    for path in stamp::tracked(root, &listing) {
        watch(&path);
    }
    Ok(())
}

fn read(path: &Path) -> Result<String, String> {
    std::fs::read_to_string(path).map_err(|e| format!("cannot read {}: {e}", path.display()))
}

fn read_if_there(path: &Path) -> Option<String> {
    std::fs::read_to_string(path).ok()
}

/// versionator's answer to `args`, run in the repository.
fn versionator(root: &Path, args: &[&str]) -> Result<String, String> {
    let output = Command::new("versionator")
        .args(args)
        .current_dir(root)
        // git refreshes the index when it may; a build must not write it.
        .env("GIT_OPTIONAL_LOCKS", "0")
        .output()
        .map_err(|e| stamp::versionator_unavailable(&e.to_string()))?;
    if !output.status.success() {
        return Err(stamp::versionator_failed(
            args,
            &output.status.to_string(),
            &String::from_utf8_lossy(&output.stderr),
        ));
    }
    String::from_utf8(output.stdout).map_err(|e| format!("versionator's answer is not UTF-8: {e}"))
}

fn run(root: &Path, program: &str, args: &[&str]) -> Result<Vec<u8>, String> {
    let output = Command::new(program)
        .args(args)
        .current_dir(root)
        .env("GIT_OPTIONAL_LOCKS", "0")
        .output()
        .map_err(|e| e.to_string())?;
    if output.status.success() {
        Ok(output.stdout)
    } else {
        Err(format!(
            "{}: {}",
            output.status,
            String::from_utf8_lossy(&output.stderr).trim()
        ))
    }
}
