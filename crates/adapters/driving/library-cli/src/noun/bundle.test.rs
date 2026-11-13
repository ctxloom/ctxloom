//! `bundle`'s tests, beside its verbs.

mod read {
    use crate::tests::*;

    // bundle.feature: list, show and view; the bare noun lists.
    #[test]
    fn test_bundle_list_show_and_view() {
        let list = json_of(&["bundle", "list"]);
        assert_eq!(list[0]["name"], "demo");
        assert_eq!(list[0]["fragment_count"], 4);
        assert_eq!(json_of(&["bundle"]), list);
        assert_eq!(json_of(&["bundle", "ls"]), list);
        assert!(text_of(&["bundle"]).starts_with("Installed bundles (2):\n"));
        let show = json_of(&["bundle", "show", "demo"]);
        assert_eq!(show["name"], "demo");
        assert_eq!(
            show["fragments"]["golang"]["premise"],
            "when editing Go code"
        );
        let text = text_of(&["bundle", "show", "demo"]);
        assert!(text.starts_with("Bundle: demo\nVersion: 1.0.0\n"));
        let view = json_of(&["bundle", "view", "demo"]);
        assert!(view["content"]
            .as_str()
            .unwrap()
            .contains("==> fragments/golang.md <=="));
        assert!(text_of(&["bundle", "view", "demo"]).starts_with("==> bundle.yaml <==\n"));
        let long = json_of(&["bundle", "view", "demo#fragments/dist"]);
        assert_eq!(long["content"], "long form\n");
        let short = json_of(&["bundle", "view", "demo#fragments/dist", "-d"]);
        assert_eq!(short["distilled"], true);
        assert_eq!(short["content"], "# (distilled version)\nshort form\n");
        assert_eq!(
            go(&["bundle", "show", "nosuch"], JSON, BARE).refused(),
            "library-catalog-bundle-not-found:20"
        );
    }
}

mod write {
    use crate::tests::write_verbs::*;
    use crate::tests::{JSON, TEXT};

    // bundle.feature: Creating a bundle writes a manifest carrying its seeded content
    #[test]
    fn test_bundle_create_says_where_it_wrote() {
        let (library, mem) = world();
        let said = text(&library, &["bundle", "create", "fresh", "-d", "my bundle"]);
        assert!(said.starts_with("Created bundle: "), "{said}");
        assert!(
            said.ends_with("Edit its item files to add your fragments and prompts.\n"),
            "{said}"
        );
        assert!(mem.has_bundle("fresh"));
        assert!(mem
            .text("fresh", "bundle.yaml")
            .unwrap()
            .contains("my bundle"));
        let doc = json(&library, &["bundle", "create", "second"]);
        assert_eq!(doc["status"], "created");
        assert_eq!(doc["name"], "second");
    }

    // bundle.feature: Metadata edits land in the file
    #[test]
    fn test_bundle_edit_names_each_change_and_says_when_there_is_none() {
        let (library, mem) = world();
        let said = text(
            &library,
            &[
                "bundle",
                "edit",
                "demo",
                "-d",
                "new words",
                "--version",
                "2.0.0",
                "--add-tag",
                "a,b",
                "--remove-tag",
                "zz",
                "--add-fragment",
                "example",
                "--add-prompt",
                "review",
                "--add-mcp",
                "tools",
            ],
        );
        assert!(said.contains("updated description\n"), "{said}");
        assert!(said.contains("updated version\n"), "{said}");
        assert!(said.contains("added tag: a\nadded tag: b\n"), "{said}");
        assert!(
            said.ends_with("\n") && said.contains("Updated bundle: "),
            "{said}"
        );
        let file = mem.text("demo", "bundle.yaml").unwrap();
        assert!(
            file.contains("version: 2.0.0") && file.contains("new words"),
            "{file}"
        );
        let said = text(
            &library,
            &[
                "bundle",
                "edit",
                "demo",
                "--remove-tag",
                "a",
                "--remove-fragment",
                "example",
                "--remove-prompt",
                "review",
                "--remove-mcp",
                "tools",
            ],
        );
        assert!(said.contains("removed tag: a\n"), "{said}");
        let before = mem.writes();
        assert_eq!(
            text(&library, &["bundle", "edit", "demo"]),
            "No changes made.\n"
        );
        assert_eq!(mem.writes(), before);
        assert_eq!(
            json(&library, &["bundle", "edit", "demo"])["status"],
            "no_changes"
        );
    }

    // bundle.feature: Bare bundle remove reports what would go and destroys nothing
    #[test]
    fn test_bundle_remove_previews_until_yes() {
        let (library, mem) = world();
        let said = text(&library, &["bundle", "remove", "demo"]);
        assert!(
            said.starts_with("Would remove bundle \"demo\":\n  "),
            "{said}"
        );
        assert!(
            said.ends_with(
                "\n\nNothing was removed. Re-run with --yes to apply:\n  ctxloom bundle remove demo --yes\n"
            ),
            "{said}"
        );
        assert!(mem.has_bundle("demo"));
        assert_eq!(json(&library, &["bundle", "rm", "demo"])["applied"], false);
        let said = text(&library, &["bundle", "remove", "demo", "--yes"]);
        assert!(said.starts_with("Removed bundle: "), "{said}");
        assert!(!mem.has_bundle("demo"));
        let doc = json(&library, &["bundle", "del", "project", "-y"]);
        assert_eq!(doc["status"], "deleted");
    }

    // Ruling 153: a removal's preview would change something, the
    // distinguishing success 1 (said only under `--detailed-exitcode`); the
    // removal done is ok, 0.
    #[test]
    fn test_a_removals_preview_would_change_and_the_removal_done_is_ok() {
        let (library, _) = world();
        assert_eq!(
            run_on(&library, &["bundle", "remove", "demo"], TEXT).code(),
            1
        );
        assert_eq!(
            run_on(&library, &["bundle", "remove", "demo"], JSON).code(),
            1
        );
        assert_eq!(
            run_on(&library, &["bundle", "remove", "demo", "--yes"], TEXT).code(),
            0
        );
    }

    // A preview without detail is a sentence, not a list.
    #[test]
    fn test_a_preview_without_detail_is_one_sentence() {
        let (library, _) = world();
        let said = text(
            &library,
            &[
                "agent",
                "create",
                "dev",
                "--engine",
                "claude-code",
                "--profiles",
                "dev",
            ],
        );
        assert!(said.starts_with("Created agent"), "{said}");
        let said = text(&library, &["agent", "remove", "dev"]);
        assert!(
            said.starts_with("Would remove agent \"dev\".\n\nNothing"),
            "{said}"
        );
    }

    // bundle.feature: Export, remove, and re-import a bundle
    #[test]
    fn test_bundle_export_and_import_say_where_they_went() {
        let (library, mem) = world();
        let said = text(&library, &["bundle", "export", "demo", "out"]);
        assert!(said.starts_with("Exported: "), "{said}");
        assert!(said.ends_with(" -> out/demo\n"), "{said}");
        assert!(mem.user_paths().iter().any(|p| p.starts_with("out/demo/")));
        let doc = json(&library, &["bundle", "export", "demo", "-o", "elsewhere"]);
        assert_eq!(doc["dest"], "elsewhere");
        let _ = text(&library, &["bundle", "remove", "demo", "--yes"]);
        let said = text(&library, &["bundle", "import", "out/demo"]);
        assert!(said.starts_with("Imported: out/demo -> "), "{said}");
        assert!(
            said.ends_with("\n  Version: 1.0.0\n  Fragments: 1, Commands: 1, MCP: 1\n"),
            "{said}"
        );
        let refused = refused_code(&library, &["bundle", "import", "out/demo"], TEXT);
        assert!(refused.contains("exists"), "{refused}");
        let said = text(&library, &["bundle", "import", "out/demo", "--force"]);
        assert!(said.starts_with("Imported: "), "{said}");
    }
}
