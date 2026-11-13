//! The library's read side for `skill` (S1), ported from Go's acceptance
//! scenarios as library_cli.test.rs describes; one file per noun (design
//! §8.4), its harness library_cli.test.rs's.

use super::library_cli::*;

// ---------------------------------------------------------------- cli/skill.feature

const SKILL_ROOT: &str = ".ctxloom/content/bundles/v2/vault";

/// Go's "Alice's project has a directory-form bundle "vault"" with the skill
/// `skill create vault reviewer -d SKILL-MARKER-reviewer-9f3c21` makes, and
/// "Alice adds an executable scripts/run.sh" (`steps_skill.go` wrote it 0755
/// on disk, which Go read; here the sidecar's `executable:` is what `skill
/// show` reports, on every OS, and no mode is read: owner ruling 2026-11-12).
fn vault_with_reviewer() -> Project {
    let project = Project::initialized().on_gos_catalog();
    project.write(
        &format!("{SKILL_ROOT}/bundle.yaml"),
        "schema_version: 4\nversion: 1.0.0\n",
    );
    project.write(
        &format!("{SKILL_ROOT}/skills/reviewer/SKILL.md"),
        "---\nname: reviewer\ndescription: SKILL-MARKER-reviewer-9f3c21\n---\n\n# reviewer\n\nTODO: describe what this skill does and how to use it.\n",
    );
    let script = format!("{SKILL_ROOT}/skills/reviewer/scripts/run.sh");
    project.write(&script, "#!/bin/sh\n# SCRIPT-MARKER-reviewer-2b7e40\n");
    files::set_mode(project.dir().join(&script), 0o755);
    project.write(
        &format!("{SKILL_ROOT}/skills/.reviewer.meta.yaml"),
        "executable:\n  - scripts/run.sh\n",
    );
    project
}

// skill.feature's dropped Scenario: Alice authors a skill package and its
// listing and show reflect the real tree (its rule named the mode on disk the
// truth, which owner ruling 2026-11-12 contradicts: the sidecar says)
#[test]
fn test_a_skills_listing_and_show_reflect_the_real_tree() {
    let project = vault_with_reviewer();
    let shown = project.run(&["skill", "show", "vault#skills/reviewer", "--format", "json"]);
    shown.succeeds();
    let doc = shown.json();
    let files = doc["files"].as_array().expect("a files list");
    let file = |path: &str| files.iter().find(|f| f["path"] == path);
    assert!(file("SKILL.md").is_some(), "{doc}");
    let script = file("scripts/run.sh").expect("the script is listed");
    assert_eq!(script["mode"], "0755", "{doc}");
    assert!(script["sha256"]
        .as_str()
        .is_some_and(|s| s.starts_with("sha256:")));
    let list = project.run(&["skill", "list"]);
    list.succeeds().contains("reviewer").contains("vault");
    project
        .run(&["skill", "show", "vault#skills/reviewer"])
        .succeeds()
        .contains("SKILL-MARKER-reviewer-9f3c21");
}

// skill.feature: Scenario: Removing a skill nobody created is refused, not
// silently reported as done (the read half: showing one is refused)
#[test]
fn test_showing_a_skill_nobody_created_is_refused() {
    let project = vault_with_reviewer();
    project
        .run(&["skill", "show", "vault#skills/never-created"])
        .fails();
}
