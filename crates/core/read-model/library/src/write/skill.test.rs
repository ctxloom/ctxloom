use super::*;
use angzarr_yaml::Value;

// owner T27: a skill is one directory, its metadata inside it.
#[test]
fn test_the_package_holds_its_metadata() {
    assert_eq!(package_dir("reviewer"), "skills/reviewer");
    assert_eq!(meta_path("reviewer"), "skills/reviewer/.meta.yaml");
}

// owner T27 (2026-11-18, evening): a new skill's metadata is the empty mapping.
#[test]
fn test_a_new_skills_metadata_is_the_empty_mapping() {
    assert_eq!(EMPTY_META, "{}\n");
    assert_eq!(
        angzarr_yaml::parse(EMPTY_META).unwrap(),
        Value::Map(Vec::new())
    );
}

// captured from a Go run: skill create
#[test]
fn test_a_new_skill_md_is_the_bytes_go_writes() {
    assert_eq!(
        skill_md("reviewer", "SKILL-MARKER-reviewer-9f3c21"),
        "---\nname: reviewer\ndescription: SKILL-MARKER-reviewer-9f3c21\n---\n\n# reviewer\n\nTODO: describe what this skill does and how to use it.\n"
    );
    // The default description has a colon in it, so it is quoted.
    let md = skill_md("nodesc", DEFAULT_DESCRIPTION);
    assert!(md.starts_with("---\nname: nodesc\ndescription: \"TODO: describe when an agent should use this skill.\"\n---\n\n# nodesc\n"), "{md}");
    // It reads: the frontmatter holds the name and the description.
    let split = crate::frontmatter::split_front_matter(&md).expect("front matter");
    let meta = angzarr_yaml::parse(split.meta).unwrap();
    assert_eq!(
        meta.get("name").and_then(angzarr_yaml::Value::as_str),
        Some("nodesc")
    );
    assert_eq!(
        meta.get("description")
            .and_then(angzarr_yaml::Value::as_str),
        Some(DEFAULT_DESCRIPTION)
    );
}

#[test]
fn test_the_executable_files_are_what_the_sidecar_lists() {
    assert_eq!(executable_files(None), Vec::<String>::new());
    assert_eq!(executable_files(Some("")), Vec::<String>::new());
    assert_eq!(executable_files(Some("notes: x\n")), Vec::<String>::new());
    assert_eq!(
        executable_files(Some("executable:\n  - scripts/run.sh\n  - bin/x\n")),
        ["scripts/run.sh", "bin/x"]
    );
    // A sidecar that is no YAML declares nothing here (reading refuses it).
    assert_eq!(
        executable_files(Some("executable: [unclosed\n")),
        Vec::<String>::new()
    );
}

#[test]
fn test_setting_the_executables_keeps_every_other_key() {
    let files = vec!["scripts/run.sh".to_string(), "bin/a".to_string()];
    assert_eq!(
        with_executable(None, &files).unwrap(),
        "executable:\n  - bin/a\n  - scripts/run.sh\n"
    );
    // A new skill's empty mapping takes the list in its place.
    assert_eq!(
        with_executable(Some(EMPTY_META), &files).unwrap(),
        "executable:\n  - bin/a\n  - scripts/run.sh\n"
    );
    assert_eq!(
        with_executable(Some("tags:\n  - t\nnotes: n # mine\n"), &files).unwrap(),
        "tags:\n  - t\nnotes: n # mine\nexecutable:\n  - bin/a\n  - scripts/run.sh\n"
    );
    assert_eq!(
        with_executable(Some("executable:\n  - old\nnotes: n\n"), &files).unwrap(),
        "executable:\n  - bin/a\n  - scripts/run.sh\nnotes: n\n"
    );
}

#[test]
fn test_an_empty_list_takes_the_key_and_leaves_the_empty_mapping() {
    assert_eq!(with_executable(None, &[]).unwrap(), EMPTY_META);
    assert_eq!(with_executable(Some(EMPTY_META), &[]).unwrap(), EMPTY_META);
    assert_eq!(
        with_executable(Some("executable:\n  - a\n"), &[]).unwrap(),
        EMPTY_META
    );
    assert_eq!(
        with_executable(Some("notes: n\nexecutable:\n  - a\n"), &[]).unwrap(),
        "notes: n\n"
    );
    assert!(with_executable(Some("executable: [unclosed\n"), &["a".to_string()]).is_err());
}

// Owner ruling 2026-11-12: a file's executability comes ONLY from its skill's
// metadata's `executable:` list (`skills/<n>/.meta.yaml`, owner T27), on
// every OS; no mode is read.
#[test]
fn test_a_bundles_executables_are_what_its_skills_metadata_declares() {
    let files: crate::tree::Files = [
        ("bundle.yaml", "schema_version: 4\n"),
        ("skills/reviewer/SKILL.md", "---\nname: reviewer\n---\n"),
        ("skills/reviewer/scripts/run.sh", "echo\n"),
        ("skills/reviewer/notes.md", "n\n"),
        (
            "skills/reviewer/.meta.yaml",
            "executable:\n  - scripts/run.sh\n  - gone.sh\n",
        ),
        ("skills/plain/SKILL.md", "---\nname: plain\n---\n"),
        ("skills/plain/run.sh", "echo\n"),
        // Metadata that does not parse declares nothing.
        ("skills/plain/.meta.yaml", "executable: [\n"),
        // Only a skill's metadata says: a fragment's sidecar says nothing.
        ("fragments/.f.meta.yaml", "executable:\n  - x\n"),
        ("fragments/x", "x"),
        // The old layout's sidecar beside the package declares nothing
        // (reading refuses the bundle).
        ("skills/.old.meta.yaml", "executable:\n  - run.sh\n"),
        ("skills/old/run.sh", "echo\n"),
        // A `.meta.yaml` deeper in a package is no skill's metadata.
        ("skills/reviewer/sub/.meta.yaml", "executable:\n  - x.sh\n"),
        ("skills/reviewer/sub/x.sh", "echo\n"),
        ("skills/reviewer/x.sh", "echo\n"),
    ]
    .into_iter()
    .map(|(path, text)| (path.to_string(), text.as_bytes().to_vec()))
    .collect();
    let declared: Vec<String> = declared_executables(&files).into_iter().collect();
    // A listed file not in the tree is no file of it.
    assert_eq!(declared, ["skills/reviewer/scripts/run.sh"]);
}
