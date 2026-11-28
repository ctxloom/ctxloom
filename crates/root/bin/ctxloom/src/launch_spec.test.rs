//! The launch spec as events (P3, owner rulings 2026-11-25): what a plan
//! decided is recorded on the event the decide emits, so no in-memory table,
//! file or callback hands a launch its inputs, and no component reads another
//! component's memory for them. These are the structural half of the spec
//! (what is gone, what exists); the behaviour is in the crates' own tests and
//! in `features/cli/launch_spec.feature`. Each fails today.
//!
//! Names are spelled in pieces so this file does not hold them itself and the
//! scan needs no exclusion of its own.

use crate::tests::files;
use std::path::Path;

/// Every Rust source under `crates/`, as (path below the repository, text).
fn rust_sources() -> Vec<(String, String)> {
    fn walk(dir: &Path, found: &mut Vec<std::path::PathBuf>) {
        for path in files::children(dir) {
            let name = path.file_name().expect("a name");
            if name == "target" || name == ".git" {
                continue;
            }
            if files::is_dir(&path) {
                walk(&path, found);
            } else if path.extension().and_then(|e| e.to_str()) == Some("rs") {
                found.push(path);
            }
        }
    }
    let root = super::repo_root();
    let mut all = Vec::new();
    walk(&root.join("crates"), &mut all);
    let sources: Vec<(String, String)> = all
        .into_iter()
        .filter_map(|path| {
            let rel = path
                .strip_prefix(&root)
                .expect("under the root")
                .to_string_lossy()
                .replace('\\', "/");
            files::read(&path).ok().map(|text| (rel, text))
        })
        .collect();
    assert!(sources.len() > 500, "scanned only {} files", sources.len());
    sources
}

/// The sources that name `word` as a whole word.
fn naming(word: &str) -> Vec<String> {
    let is_word = |c: char| c.is_alphanumeric() || c == '_';
    rust_sources()
        .into_iter()
        .filter(|(_, text)| {
            text.match_indices(word).any(|(at, _)| {
                let before = text[..at].chars().next_back().is_none_or(|c| !is_word(c));
                let after = text[at + word.len()..]
                    .chars()
                    .next()
                    .is_none_or(|c| !is_word(c));
                before && after
            })
        })
        .map(|(rel, _)| rel)
        .collect()
}

// Row 4, pattern 1: the launcher acts on `pending-launches` alone (the
// run's enqueue, and the session's mint and loadout), never on a payload the
// enqueuing caller hands it through a mutex map.
#[test]
fn test_the_launcher_has_no_orders_handed_to_it() {
    let table = concat!("Ord", "ers");
    let hits = naming(table);
    assert!(
        hits.is_empty(),
        "the in-memory {table} remain in: {hits:#?}"
    );
    let root = super::repo_root();
    assert!(
        !files::exists(root.join("crates/core/application/run-launcher/src/orders.rs")),
        "run-launcher still has its orders.rs"
    );
}

// Rows 7, 8, 22: a child's launch inputs ride its `LaunchChild` and
// `ChildLaunched`, so the hand-off file, the client program kept in the
// daemon's memory and the daemon's environment cache are gone.
#[test]
fn test_no_hand_off_file_no_handed_programs_and_no_environment_cache() {
    for word in [concat!("Handed", "Programs"), concat!("Hand", "Offs")] {
        let hits = naming(word);
        assert!(hits.is_empty(), "{word} remains in: {hits:#?}");
    }
    let daemon = files::read(
        super::repo_root().join("crates/core/application/daemon-application/src/daemon.rs"),
    )
    .expect("the daemon loop's source");
    assert!(
        !daemon.contains(concat!("envs", ": BTreeMap<String, HandOff>")),
        "the daemon still caches its children's hand-offs in memory"
    );
}

// Row 3: the distill reaction is a policy on the turn's recorded answer, not
// a callback held in memory (lost with the daemon).
#[test]
fn test_no_reaction_callback_is_held_in_memory() {
    let word = concat!("Re", "action");
    let hits: Vec<String> = naming(word)
        .into_iter()
        .filter(|rel| !rel.ends_with(".test.rs"))
        .collect();
    assert!(hits.is_empty(), "the {word} port remains in: {hits:#?}");
}

// Row 4: a hosted child's asks take their run, engine, agent and timeout from
// the stream, so the loop keeps no asker for the API to read.
#[test]
fn test_the_loop_keeps_no_asker_for_the_api_to_read() {
    let word = concat!("As", "ker");
    let hits: Vec<String> = naming(word)
        .into_iter()
        .filter(|rel| !rel.ends_with(".test.rs"))
        .collect();
    assert!(hits.is_empty(), "the {word} remains in: {hits:#?}");
}

// Item 7: the user's engine settings are a stream of their own: the proto is
// compiled by a wire crate, and a domain crate and its scanner exist.
#[test]
fn test_the_user_settings_stream_has_a_domain_and_a_wire_crate() {
    let root = super::repo_root();
    let proto = "ctxloom/settings/v1/settings.proto";
    assert!(
        files::exists(root.join("proto").join(proto)),
        "the proto is the spec"
    );
    let builds = rust_build_scripts();
    assert!(
        builds.iter().any(|(_, text)| text.contains(proto)),
        "no wire crate's build.rs compiles {proto}"
    );
}

/// Every `build.rs` under `crates/`, as (path, text).
fn rust_build_scripts() -> Vec<(String, String)> {
    fn walk(dir: &Path, found: &mut Vec<std::path::PathBuf>) {
        for path in files::children(dir) {
            let name = path.file_name().expect("a name");
            if name == "target" || name == ".git" {
                continue;
            }
            if files::is_dir(&path) {
                walk(&path, found);
            } else if name == "build.rs" {
                found.push(path);
            }
        }
    }
    let root = super::repo_root();
    let mut all = Vec::new();
    walk(&root.join("crates"), &mut all);
    all.into_iter()
        .filter_map(|path| {
            let rel = path
                .strip_prefix(&root)
                .expect("under the root")
                .to_string_lossy()
                .replace('\\', "/");
            files::read(&path).ok().map(|text| (rel, text))
        })
        .collect()
}

// Item 6 and the Sealed reference: the proto's secret-bearing fields name the
// Sealed message of angzarr-sealed once the crate is in the lock. While it is
// not, each is a `bytes` field carrying its encoding, marked with a TODO; this
// test fails when the crate is locked and a TODO stays.
#[test]
fn test_no_sealed_todo_is_left_once_angzarr_sealed_is_locked() {
    let root = super::repo_root();
    let lock = files::read(root.join("Cargo.lock")).expect("the lock");
    if !lock.contains("name = \"angzarr-sealed\"") {
        return;
    }
    let mut left = Vec::new();
    for proto in [
        "proto/ctxloom/runs/v1/runs.proto",
        "proto/ctxloom/sessions/v1/daemon.proto",
        "proto/ctxloom/library/v1/library.proto",
        "proto/ctxloom/settings/v1/settings.proto",
    ] {
        let text = files::read(root.join(proto)).expect("a proto");
        if text.contains("TODO(angzarr-sealed)") {
            left.push(proto);
        }
    }
    assert!(left.is_empty(), "the Sealed TODO stays in: {left:?}");
}
