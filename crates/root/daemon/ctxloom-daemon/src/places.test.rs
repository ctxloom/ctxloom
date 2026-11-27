//! A project's places as the daemon's destroy reaches them, under a
//! temporary root (never the real home).

use daemon_api::Places;

use super::ProjectPlaces;
use crate::fixtures;

fn places(root: &file_system::Rooted) -> ProjectPlaces {
    ProjectPlaces {
        fs: fixtures::fs(),
        home: root.root().join("home"),
        temp: root.root().join("tmp"),
    }
}

// The configuration is read from the project's registered root (owner,
// 2026-11-12), whatever the daemon's own directory: its purge age there.
#[test]
fn the_purge_age_is_read_from_the_registered_root() {
    let root = fixtures::root();
    let project = root.root().join("work/calm");
    fixtures::mkdirs(project.join(".ctxloom"));
    fixtures::put_mode(
        project.join(".ctxloom/config.yaml"),
        "schema_version: 8\nsession_purge_age: 45d\n",
        0o644,
    );
    fixtures::mkdirs(root.root().join("home"));
    let mut settings = places(&root).settings(&project.display().to_string(), &[]);
    assert_eq!(settings.purge_age().unwrap(), Some("45d".to_string()));
}

// A session's files are its project's session directory.
#[test]
fn a_sessions_files_are_its_projects() {
    let root = fixtures::root();
    let project = root.root().join("work/calm");
    let dir = project.join(".ctxloom/sessions/amber-swift-owl");
    fixtures::mkdirs(dir.join("native"));
    fixtures::put_mode(dir.join("design-notes.md"), "notes", 0o644);
    let mut files = places(&root).files(&project.display().to_string());
    let rels: Vec<String> = files
        .survey("amber-swift-owl")
        .unwrap()
        .into_iter()
        .map(|item| item.rel)
        .collect();
    assert_eq!(rels, ["design-notes.md"]);
}

// Now is the clock's: after the epoch, and not a fixed instant.
#[test]
fn now_is_the_clocks() {
    let root = fixtures::root();
    assert!(places(&root).now().seconds > 1_700_000_000);
}

/// git in `dir` with a clean identity and none of the caller's environment.
fn git(dir: &std::path::Path, args: &[&str]) {
    let output = std::process::Command::new("git")
        .args(["-c", "user.name=Test", "-c", "user.email=test@example.com"])
        .args([
            "-c",
            "commit.gpgsign=false",
            "-c",
            "init.defaultBranch=main",
        ])
        .args(args)
        .current_dir(dir)
        .env_clear()
        .env("PATH", path())
        .env("HOME", dir)
        .output()
        .expect("git runs");
    assert!(
        output.status.success(),
        "git {args:?}: {}",
        String::from_utf8_lossy(&output.stderr)
    );
}

/// The host's `PATH`, which finds git.
fn path() -> String {
    ctxloom_compose::fixtures::host_snapshot()
        .utf8("PATH")
        .unwrap_or_default()
        .to_string()
}

// Merge item 1 (main into the daemon line, 2026-11-13): a session's
// checkouts are under the account's ctxloom home, by its project's id
// (owner ruling 2026-11-12, Q1), so the daemon's destroy finds them there: a
// checkout holding uncommitted work is spared, and keeps its session.
#[test]
fn a_sessions_checkout_under_the_account_home_is_found_and_spared() {
    let root = fixtures::root();
    let home = root.root().join("home");
    let project = root.root().join("work/calm");
    fixtures::mkdirs(project.join(".ctxloom/sessions/amber-swift-owl"));
    fixtures::put_mode(
        project.join(".ctxloom/sessions/amber-swift-owl/session.lock"),
        "",
        0o644,
    );
    fixtures::put_mode(project.join("README.md"), "calm", 0o644);
    git(&project, &["init", "-q"]);
    git(&project, &["add", "README.md"]);
    git(&project, &["commit", "-q", "-m", "first"]);
    let checkout =
        isolation_ports::session_worktrees(&home, "calm-otter", "amber-swift-owl").join("dev");
    fixtures::mkdirs(checkout.parent().unwrap());
    git(
        &project,
        &[
            "worktree",
            "add",
            "-q",
            "-b",
            "ctxloom/amber-swift-owl/dev",
            &checkout.display().to_string(),
        ],
    );
    fixtures::put_mode(checkout.join("wip.txt"), "unsaved", 0o644);
    let mut worktrees = places(&root).worktrees(
        &project.display().to_string(),
        "calm-otter",
        &[("PATH".to_string(), path())],
    );
    let report = worktrees.worktrees(Some("amber-swift-owl"), false).unwrap();
    assert_eq!(report.worktrees.len(), 1, "{report:?}");
    assert_eq!(report.spared, 1, "{report:?}");
}

// E2 (owner 2026-11-17): the daemon's assembly holds a premised fragment
// back and answers the index as data; the MCP tool (a launch's edge) and
// the CLI word it at their presentation (library_api::premise). Step 10a
// U2: the assembly is a library read, over the library a process at the
// project opens.
#[test]
fn an_assembly_answers_what_its_premises_held_back_as_data() {
    use daemon_api::Libraries;
    let root = fixtures::root();
    let project = root.root().join("work/calm");
    let bundle = project.join(".ctxloom/content/bundles/v2/demo");
    fixtures::mkdirs(bundle.join("fragments"));
    fixtures::put_mode(
        bundle.join("bundle.yaml"),
        "schema_version: 4\nversion: 1.0.0\n",
        0o644,
    );
    fixtures::put_mode(
        bundle.join("fragments/later.md"),
        "---\ndescription: about to do the later thing\n---\nLATER-BODY\n",
        0o644,
    );
    fixtures::put_mode(bundle.join("fragments/rules.md"), "RULES-BODY\n", 0o644);
    let profiles = project.join(".ctxloom/content/bundles/v2/project/profiles");
    fixtures::mkdirs(&profiles);
    fixtures::put_mode(
        project.join(".ctxloom/content/bundles/v2/project/bundle.yaml"),
        "schema_version: 4\nversion: 1.0.0\n",
        0o644,
    );
    fixtures::put_mode(profiles.join("dev.yaml"), "bundles: [demo]\n", 0o644);
    fixtures::put_mode(
        project.join(".ctxloom/config.yaml"),
        "schema_version: 9\n",
        0o644,
    );
    fixtures::mkdirs(root.root().join("home"));
    fixtures::mkdirs(root.root().join("tmp"));
    let libraries = super::ProjectLibraries {
        home: root.root().join("home"),
        temp: root.root().join("tmp"),
    };
    let request = daemon_proto::v1::AssembleContextRequest {
        profiles: vec!["dev".to_string()],
        ..daemon_proto::v1::AssembleContextRequest::default()
    };
    let mut answered = None;
    libraries
        .read(&project.display().to_string(), &[], &mut |library| {
            let flags = library_application::Flags::default();
            let read = library_api::reads::assemble_context(library, &flags, "users/me", &request);
            answered = Some(read.result.expect("assembled"));
        })
        .expect("read");
    let assembled = answered.expect("read");
    // E2 (owner 2026-11-17): the index is answered as data, never
    // rendered here; the edge that shows the context renders it.
    let premised = assembled.premised.clone();
    let context = assembled.context;
    assert!(context.contains("RULES-BODY"), "{context}");
    assert!(!context.contains("LATER-BODY"), "held back: {context}");
    assert!(!context.contains("Conditional guidance"), "{context}");
    assert_eq!(premised.len(), 1, "{premised:?}");
    assert!(premised[0].fragment.ends_with("later"), "{premised:?}");
    assert_eq!(premised[0].premise, "about to do the later thing");
}

// Step 10a U2: a read's checkout is named once, in its tree's
// `state/checkout`, and read back after.
#[test]
fn a_checkouts_harp_is_minted_once_into_its_tree() {
    use library_api::CheckoutHarps;
    let root = fixtures::root();
    let tree = root.root().join("work/calm/.ctxloom");
    fixtures::mkdirs(&tree);
    let harps = super::TreeHarps(fixtures::fs());
    let first = harps.harp(&tree.display().to_string()).unwrap();
    let again = harps.harp(&tree.display().to_string()).unwrap();
    assert_eq!(first, again);
    let kept = fixtures::fs()
        .read_to_string(&tree.join("state/checkout"))
        .unwrap();
    assert_eq!(kept.trim(), first);
}

// Step 10a U2: the home's library is observed where the home has one.
#[test]
fn a_reads_home_is_ctxloom_s_own() {
    use library_api::Observers;
    let root = fixtures::root();
    let home = root.root().join("home");
    fixtures::mkdirs(&home);
    let gits = super::CallerGits {
        fs: fixtures::fs(),
        home: home.clone(),
        scanner: std::sync::Arc::new(super::TreeScanner::new(fixtures::fs())),
    };
    assert_eq!(gits.home_tree(), None);
    fixtures::mkdirs(home.join(".ctxloom"));
    assert_eq!(
        gits.home_tree(),
        Some(home.join(".ctxloom").display().to_string())
    );
}

// Step 10a U2 (E1): a library read is opened where the caller is, as a
// process there would open it: the nearest `.ctxloom` above its directory
// (a worktree of the project is its own checkout), none outside a project.
#[test]
fn a_reads_library_is_the_one_its_callers_directory_finds() {
    use daemon_api::Libraries;
    let root = fixtures::root();
    let project = root.root().join("work/calm");
    fixtures::mkdirs(project.join(".ctxloom/content/bundles/v2/project/profiles"));
    fixtures::mkdirs(project.join("src/deep"));
    fixtures::put_mode(
        project.join(".ctxloom/config.yaml"),
        "schema_version: 9\n",
        0o644,
    );
    fixtures::put_mode(
        project.join(".ctxloom/content/bundles/v2/project/bundle.yaml"),
        "schema_version: 4\nversion: 1.0.0\n",
        0o644,
    );
    fixtures::put_mode(
        project.join(".ctxloom/content/bundles/v2/project/profiles/dev.yaml"),
        "description: the dev one\n",
        0o644,
    );
    fixtures::mkdirs(root.root().join("home"));
    fixtures::mkdirs(root.root().join("tmp"));
    fixtures::mkdirs(root.root().join("elsewhere"));
    let libraries = super::ProjectLibraries {
        home: root.root().join("home"),
        temp: root.root().join("tmp"),
    };
    let deep = project.join("src/deep").display().to_string();
    assert_eq!(
        libraries.root(&deep, &[]),
        Some(project.display().to_string())
    );
    let elsewhere = root.root().join("elsewhere").display().to_string();
    assert_eq!(libraries.root(&elsewhere, &[]), None);
    assert_eq!(libraries.root("", &[]), None);
    let mut profiles = Vec::new();
    libraries
        .read(&deep, &[], &mut |library| {
            let listed = library
                .profile_list(&library_application::Flags::default())
                .result
                .unwrap();
            profiles = listed.rows.iter().map(|r| r.display_name.clone()).collect();
        })
        .unwrap();
    assert_eq!(profiles, ["dev"]);
}
