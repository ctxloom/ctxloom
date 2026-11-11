//! Owner rulings 110, 111: the built-in bundles, compiled in.

use super::*;

/// `resources/builtin/SHA256SUMS`: each file's digest and its path below
/// `resources/builtin/`.
const SUMS: &str = include_str!("../resources/builtin/SHA256SUMS");

fn compiled() -> BTreeMap<String, String> {
    FILES
        .iter()
        .flat_map(|(bundle, files)| {
            files
                .iter()
                .map(move |(path, bytes)| (format!("{bundle}/{path}"), crate::sha256::hex(bytes)))
        })
        .collect()
}

// The compiled table is the copy of the source the manifest lists: every file
// of it, each with the bytes it was taken with (the text is the owner's).
#[test]
fn test_the_compiled_files_are_the_ones_the_manifest_lists_byte_for_byte() {
    let listed: BTreeMap<String, String> = SUMS
        .lines()
        .map(|line| {
            let (digest, path) = line.split_once("  ").expect("sha256sum lines");
            (path.to_string(), digest.to_string())
        })
        .collect();
    assert_eq!(listed.len(), 31);
    assert_eq!(compiled(), listed);
}

#[test]
fn test_the_source_is_named_by_its_repository_and_full_commit() {
    assert_eq!(ORIGIN, "ctxloom/default");
    assert_eq!(
        SOURCE_REPOSITORY,
        "https://github.com/ctxloom/ctxloom-default"
    );
    assert_eq!(SOURCE_COMMIT.len(), 40);
    assert!(SOURCE_COMMIT.bytes().all(|b| b.is_ascii_hexdigit()));
}

// What the seeded profile resolves through, and nothing more.
#[test]
fn test_the_built_in_bundles_are_the_ones_the_developer_profile_names() {
    assert_eq!(
        names(),
        ["ai-coding-general", "ai-developer", "git", "testing"]
    );
    let developer = &profiles(&|_| false)["ctxloom+local:ai-developer#profiles/developer"];
    assert_eq!(
        developer.bundles,
        ["ai-coding-general", "ai-developer", "testing", "git"]
    );
    assert_eq!(
        developer.name,
        "ctxloom+local:ai-developer#profiles/developer"
    );
}

#[test]
fn test_every_built_in_bundle_reads_with_its_items() {
    let read = entries(&|_| false);
    let counts: Vec<(&str, usize, usize)> = read
        .iter()
        .map(|e| {
            (
                e.name.as_str(),
                e.bundle.fragments.len(),
                e.bundle.profiles.len(),
            )
        })
        .collect();
    assert_eq!(
        counts,
        [
            ("ai-coding-general", 9, 0),
            ("ai-developer", 0, 1),
            ("git", 1, 0),
            ("testing", 3, 0),
        ]
    );
    let tdd = &read[3].bundle.fragments["tdd"];
    assert!(tdd
        .body
        .content
        .starts_with("# Test-Driven Development (TDD)"));
    assert!(!tdd.body.distilled.is_empty());
    assert_eq!(
        read[1].manifest_path,
        "<built-in ctxloom/default>/ai-developer/bundle.yaml"
    );
}

// A project bundle of the same name overrides one: the built-in one is not
// read at all, and its profiles go with it.
#[test]
fn test_a_name_the_project_has_leaves_that_built_in_bundle_out() {
    let taken = |name: &str| name == "testing" || name == "ai-developer";
    let left: Vec<String> = entries(&taken).into_iter().map(|e| e.name).collect();
    assert_eq!(left, ["ai-coding-general", "git"]);
    assert!(profiles(&taken).is_empty());
}

#[test]
fn test_the_files_of_a_built_in_bundle_and_of_no_other_name() {
    let git = files("git").expect("git is built in");
    assert_eq!(
        git.keys().collect::<Vec<_>>(),
        [
            "bundle.yaml",
            "fragments/git.distilled.md",
            "fragments/git.md"
        ]
    );
    assert!(files("project").is_none());
}
