//! The library's read side for `profile` (S1), ported from Go's acceptance
//! scenarios as library_cli.test.rs describes; one file per noun (design
//! §8.4), its harness library_cli.test.rs's.

use super::library_cli::*;

// ---------------------------------------------------------------- cli/profile.feature

// profile.feature: Scenario Outline: Creating a profile writes its definition
// and it is listed back (the listing half)
#[test]
fn test_a_profile_is_listed_back_with_its_description() {
    for flags in FORMATS {
        let project = Project::initialized().on_gos_catalog();
        project.bundle("demo");
        project.write(
            ".ctxloom/content/bundles/v2/project/bundle.yaml",
            "schema_version: 4\nversion: 1.0.0\n",
        );
        project.write(
            ".ctxloom/content/bundles/v2/project/profiles/dev.yaml",
            "description: day-to-day-work\nbundles:\n  - demo\n",
        );
        let ran = run_with_flags(&project, &["profile", "list"], flags);
        ran.succeeds();
        if is_text(flags) {
            ran.contains("Profiles (1):").contains("day-to-day-work");
        } else {
            let doc = ran.json();
            assert_eq!(reports_as(&doc, "$.0.name"), "dev");
            assert_eq!(reports_as(&doc, "$.0.description"), "day-to-day-work");
        }
    }
}

// profile.feature: Scenario Outline: Showing a profile reads its composition back
#[test]
fn test_showing_a_profile_reads_its_composition_back() {
    for flags in FORMATS {
        let project = Project::initialized().on_gos_catalog();
        project.bundle("demo");
        project.profile("dev", "demo");
        let ran = run_with_flags(&project, &["profile", "show", "dev"], flags);
        ran.succeeds();
        if is_text(flags) {
            ran.contains("Profile: dev").contains("- demo");
        } else {
            let doc = ran.json();
            assert_eq!(reports_as(&doc, "name"), "dev");
            assert!(contains_value(&doc, "bundles", "demo"), "{doc}");
        }
    }
}

// profile.feature: Scenario Outline: Bare profile lists the profiles, and so
// does ls
#[test]
fn test_bare_profile_lists_the_profiles_and_so_does_ls() {
    for flags in FORMATS {
        let project = Project::initialized().on_gos_catalog();
        project.bundle("demo");
        project.profile("dev", "demo");
        for base in [&["profile"][..], &["profile", "ls"]] {
            let ran = run_with_flags(&project, base, flags);
            ran.succeeds().lacks("Available Commands:");
            if is_text(flags) {
                ran.contains("dev");
            } else {
                assert_eq!(reports_as(&ran.json(), "$.0.name"), "dev");
            }
        }
    }
}

// profile.feature: Scenario Outline: A parent profile's content is inherited
// (the read half: show names the parent)
#[test]
fn test_a_profile_show_names_its_parent() {
    for flags in FORMATS {
        let project = Project::initialized().on_gos_catalog();
        project.bundle("demo");
        project.profile("base", "demo");
        project.write(
            ".ctxloom/content/bundles/v2/project/profiles/dev.yaml",
            "description: day-to-day-work\nparents:\n  - base\nbundles:\n  - demo\n",
        );
        let ran = run_with_flags(&project, &["profile", "show", "dev"], flags);
        ran.succeeds();
        if is_text(flags) {
            ran.contains("- base");
        } else {
            assert!(contains_value(&ran.json(), "parents", "base"));
        }
    }
}

// profile.feature: Scenario: Removing a profile with --yes takes the file and
// the listing entry (the empty listing it leaves)
#[test]
fn test_a_project_with_no_profiles_says_so() {
    let project = Project::initialized().on_gos_catalog();
    project
        .run(&["profile", "list", "--format", "text"])
        .succeeds()
        .contains("No profiles defined.");
}
