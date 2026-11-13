//! `skill`'s tests, beside its verbs.

mod read {
    use crate::tests::*;

    // skill.feature
    #[test]
    fn test_skill_list_and_show() {
        // OD-40: `ListSkillsResponse` and the `Skill` (FULL).
        let list = json_of(&["skill", "list"]);
        assert_eq!(
            list["skills"][0]["name"],
            "projects/calm-otter/bundles/demo/skills/reviewer"
        );
        assert_eq!(list["skills"][0]["file_count"], 1);
        assert_eq!(list["skills"][0]["description"], "Reviews code");
        assert_eq!(json_of(&["skill"]), list);
        assert_eq!(
            json_of(&["skill", "list", "--bundle", "project"]),
            serde_json::json!({})
        );
        let show = json_of(&["skill", "show", "demo#skills/reviewer"]);
        assert_eq!(show["body"], "SKILL-BODY\n");
        assert_eq!(
            show["name"],
            "projects/calm-otter/bundles/demo/skills/reviewer"
        );
        assert_eq!(show["files"][0]["path"], "SKILL.md");
        assert!(show["files"][0].get("mode").is_none());
        assert!(text_of(&["skill", "show", "demo#skills/reviewer"])
            .starts_with("demo#skills/reviewer\n\ndescription: Reviews code\n"));
        assert!(text_of(&["skill"]).starts_with("Skills (1):\n"));
    }
}

mod write {
    use crate::tests::write_verbs::*;
    use crate::tests::TEXT;

    // skill.feature
    #[test]
    fn test_skill_create_remove_export_and_import() {
        let (library, mem) = world();
        let said = text(
            &library,
            &["skill", "create", "demo", "writer", "-d", "words"],
        );
        assert!(
            said.starts_with("Created skill \"writer\" in bundle \"demo\"\n  "),
            "{said}"
        );
        assert!(
            said.contains("\nEdit ") && said.ends_with("/SKILL.md to describe the skill\n"),
            "{said}"
        );
        assert!(mem
            .text("demo", "skills/writer/SKILL.md")
            .unwrap()
            .contains("words"));
        let said = text(&library, &["skill", "remove", "demo#skills/writer"]);
        assert!(said.starts_with("Would remove skill"), "{said}");
        let said = text(
            &library,
            &["skill", "remove", "demo#skills/writer", "--yes"],
        );
        assert!(
            said.starts_with("Removed skill \"writer\" from bundle \"demo\"\n  "),
            "{said}"
        );
        let said = text(
            &library,
            &["skill", "export", "demo#skills/reviewer", "-o", "r.zip"],
        );
        assert!(
            said.starts_with("Exported skill \"reviewer\" to r.zip (")
                && said.ends_with(" bytes)\n"),
            "{said}"
        );
        let doc = json(
            &library,
            &["skill", "export", "demo#skills/reviewer", "-o", "r2.zip"],
        );
        // `ExportSkillResponse`, its bytes a uint64 (a JSON string, OQ-12).
        assert_eq!(doc["zip_path"], "r2.zip");
        assert_eq!(
            doc["skill"],
            "projects/calm-otter/bundles/demo/skills/reviewer"
        );
        assert!(doc["bytes"].is_string(), "{doc}");
        let made = json(&library, &["skill", "create", "demo", "second"]);
        assert_eq!(
            made["skill"]["name"],
            "projects/calm-otter/bundles/demo/skills/second"
        );
        assert_eq!(made["applied"], true);
        let preview = json(&library, &["skill", "remove", "demo#skills/second"]);
        assert_eq!(preview["applied"], false);
        assert_eq!(
            preview["apply_command"],
            "ctxloom skill remove demo#skills/second --yes"
        );
        let said = text(
            &library,
            &["skill", "import", "r.zip", "--bundle", "project"],
        );
        assert!(
            said.starts_with("Imported skill \"reviewer\" into bundle \"project\" (1 file(s))\n  "),
            "{said}"
        );
        let refused = refused_code(
            &library,
            &["skill", "import", "r.zip", "--bundle", "project"],
            TEXT,
        );
        assert!(refused.contains("exists"), "{refused}");
        let said = text(
            &library,
            &["skill", "import", "r.zip", "--bundle", "project", "--force"],
        );
        assert!(said.starts_with("Imported skill"), "{said}");
        // `ImportSkillResponse`: the skill with its file count.
        let doc = json(
            &library,
            &["skill", "import", "r.zip", "--bundle", "project", "--force"],
        );
        assert_eq!(
            doc["skill"]["name"],
            "projects/calm-otter/bundles/project/skills/reviewer"
        );
        assert_eq!(doc["skill"]["file_count"], 1);
    }
}
