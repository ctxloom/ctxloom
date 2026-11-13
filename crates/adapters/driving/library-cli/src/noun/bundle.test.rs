//! `bundle`'s tests, beside its verbs.

mod read {
    use crate::tests::*;

    // bundle.feature: list, show and view; the bare noun lists.
    #[test]
    fn test_bundle_list_names_each_bundle_under_the_project() {
        // OD-40: `ListBundlesResponse`, each bundle named under the project.
        let list = json_of(&["bundle", "list"]);
        let demo = &list["bundles"][0];
        assert_eq!(demo["name"], "projects/calm-otter/bundles/demo");
        assert_eq!(demo["origin"], "PROJECT");
        assert_eq!(demo["reference"], "ctxloom+local:demo");
        assert_eq!(demo["fragment_count"], 4);
        assert_eq!(demo["mcp_server_count"], 1);
        assert!(demo.get("profile_count").is_none(), "a zero is left out");
        assert_eq!(json_of(&["bundle"]), list);
        assert_eq!(json_of(&["bundle", "ls"]), list);
        assert!(text_of(&["bundle"]).starts_with("Installed bundles (2):\n"));
    }

    // bundle.feature: show names each child (OQ-14).
    #[test]
    fn test_bundle_show_summarises_each_child_by_name() {
        // The `Bundle` (FULL): each child summarised by its name (OQ-14).
        let show = json_of(&["bundle", "show", "demo"]);
        assert_eq!(show["name"], "projects/calm-otter/bundles/demo");
        assert_eq!(show["origin"], "PROJECT");
        let golang = show["fragments"]
            .as_array()
            .unwrap()
            .iter()
            .find(|f| f["item"] == "projects/calm-otter/bundles/demo/fragments/golang")
            .expect("golang summarised");
        assert_eq!(golang["premise"], "when editing Go code");
        assert_eq!(golang["no_distill"], false);
        assert_eq!(golang["distilled"], "NONE");
        assert_eq!(
            show["mcp_servers"][0]["item"],
            "projects/calm-otter/bundles/demo/mcpServers/tree-sitter"
        );
        assert_eq!(
            show["skills"][0]["item"],
            "projects/calm-otter/bundles/demo/skills/reviewer"
        );
        let text = text_of(&["bundle", "show", "demo"]);
        assert!(text.starts_with("Bundle: demo\nVersion: 1.0.0\n"));
    }

    // bundle.feature: view hands back bytes, the bundle's or one item's.
    #[test]
    fn test_bundle_view_is_the_bundles_files_or_the_items_resource() {
        // `bundle view <bundle>`: the `Bundle` (CONTENT), file by file.
        let view = json_of(&["bundle", "view", "demo"]);
        assert_eq!(view["name"], "projects/calm-otter/bundles/demo");
        assert_eq!(view["files"][0]["path"], "bundle.yaml");
        assert!(view["files"]
            .as_array()
            .unwrap()
            .iter()
            .any(|f| f["path"] == "fragments/golang.md"));
        assert!(text_of(&["bundle", "view", "demo"]).starts_with("==> bundle.yaml <==\n"));
        // An item's path: the item's own resource, its body the bytes served.
        let long = json_of(&["bundle", "view", "demo#fragments/dist"]);
        assert_eq!(
            long["name"],
            "projects/calm-otter/bundles/demo/fragments/dist"
        );
        assert_eq!(long["body"], "long form\n");
        assert!(long.get("body_is_distilled").is_none());
        let short = json_of(&["bundle", "view", "demo#fragments/dist", "-d"]);
        assert_eq!(short["body_is_distilled"], true);
        assert_eq!(short["body"], "short form\n");
        let command = json_of(&["bundle", "view", "demo#prompts/review"]);
        assert_eq!(
            command["name"],
            "projects/calm-otter/bundles/demo/commands/review"
        );
        let server = json_of(&["bundle", "view", "demo#mcp/tree-sitter"]);
        assert_eq!(
            server["name"],
            "projects/calm-otter/bundles/demo/mcpServers/tree-sitter"
        );
        assert_eq!(server["command"], "tree-sitter");
        assert_eq!(server["args"][0], "--stdio");
        let skill = json_of(&["bundle", "view", "demo#skills/reviewer"]);
        assert_eq!(
            skill["name"],
            "projects/calm-otter/bundles/demo/skills/reviewer"
        );
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
        // OD-40: `CreateBundleResponse`, the bundle by its name.
        let doc = json(&library, &["bundle", "create", "second"]);
        assert_eq!(doc["bundle"]["name"], "projects/calm-otter/bundles/second");
        assert_eq!(doc["applied"], true);
        assert!(
            doc["path"]
                .as_str()
                .unwrap()
                .ends_with("second/bundle.yaml"),
            "{doc}"
        );
        assert!(doc.get("status").is_none());
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
        // `UpdateBundleResponse`: no change listed, nothing applied.
        let unchanged = json(&library, &["bundle", "edit", "demo"]);
        assert_eq!(unchanged["applied"], false);
        assert!(unchanged.get("changes").is_none(), "{unchanged}");
        assert_eq!(
            unchanged["bundle"]["name"],
            "projects/calm-otter/bundles/demo"
        );
        let changed = json(&library, &["bundle", "edit", "demo", "--version", "3.0.0"]);
        assert_eq!(changed["applied"], true);
        assert_eq!(changed["changes"][0], "updated version");
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
        // `DeleteBundleResponse`: the preview names the bundle, what would go
        // and the command that applies it.
        let preview = json(&library, &["bundle", "rm", "demo"]);
        assert_eq!(preview["applied"], false);
        assert_eq!(
            preview["bundle"]["name"],
            "projects/calm-otter/bundles/demo"
        );
        assert_eq!(preview["apply_command"], "ctxloom bundle remove demo --yes");
        assert!(preview["detail"].as_array().is_some_and(|d| !d.is_empty()));
        let said = text(&library, &["bundle", "remove", "demo", "--yes"]);
        assert!(said.starts_with("Removed bundle: "), "{said}");
        assert!(!mem.has_bundle("demo"));
        let doc = json(&library, &["bundle", "del", "project", "-y"]);
        assert_eq!(doc["applied"], true);
        assert_eq!(doc["bundle"]["name"], "projects/calm-otter/bundles/project");
        assert!(doc.get("status").is_none());
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
        // `ExportBundleResponse`.
        let doc = json(&library, &["bundle", "export", "demo", "-o", "elsewhere"]);
        assert_eq!(doc["destination"], "elsewhere");
        assert_eq!(doc["bundle"], "projects/calm-otter/bundles/demo");
        assert_eq!(doc["applied"], true);
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
        // `ImportBundleResponse`: the bundle with its version and counts.
        let doc = json(&library, &["bundle", "import", "out/demo", "--force"]);
        assert_eq!(doc["bundle"]["name"], "projects/calm-otter/bundles/demo");
        assert_eq!(doc["bundle"]["version"], "1.0.0");
        assert_eq!(doc["bundle"]["fragment_count"], 1);
        assert_eq!(doc["bundle"]["mcp_server_count"], 1);
        assert_eq!(doc["applied"], true);
    }
}
