//! `skill`'s tests, beside its verbs.

mod read {
    use crate::tests::*;

    // skill.feature
    #[test]
    fn test_skill_list_and_show() {
        let list = json_of(&["skill", "list"]);
        assert_eq!(list[0]["name"], "demo/reviewer");
        assert_eq!(json_of(&["skill"]), list);
        assert_eq!(
            json_of(&["skill", "list", "--bundle", "project"]),
            serde_json::json!([])
        );
        let show = json_of(&["skill", "show", "demo#skills/reviewer"]);
        assert_eq!(show["body"], "SKILL-BODY\n");
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
        assert_eq!(doc["zip_path"], "r2.zip");
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
    }
}
