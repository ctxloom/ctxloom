//! `profile`'s tests, beside its verbs.

mod read {
    use crate::tests::*;

    // profile.feature (moved from the four-noun test of lib.test.rs, unchanged)
    #[test]
    fn test_profile_list_and_show() {
        let profiles = json_of(&["profile", "list"]);
        assert_eq!(profiles[1]["name"], "dev");
        assert_eq!(json_of(&["profile"]), profiles);
        assert_eq!(
            json_of(&["profile", "show", "dev"])["description"],
            "dev profile"
        );
        assert!(text_of(&["profile", "show", "dev"]).starts_with("Profile: dev\n"));
        assert!(text_of(&["profile"]).starts_with("Profiles (2):\n"));
    }
}

mod write {
    use crate::tests::write_verbs::*;
    use crate::tests::TEXT;

    // profile.feature
    #[test]
    fn test_profile_verbs_answer_in_text_and_refuse_json() {
        let (library, mem) = world();
        let said = text(
            &library,
            &["profile", "create", "base", "-i", "demo", "-d", "d"],
        );
        assert!(
            said.starts_with("Created profile \"base\" with bundles: demo\nSaved to: "),
            "{said}"
        );
        let said = text(
            &library,
            &["profile", "create", "kid", "--parent", "base", "-i", "demo"],
        );
        assert!(
            said.starts_with("Created profile \"kid\" with parents: base; bundles: demo\n"),
            "{said}"
        );
        let said = text(
            &library,
            &["profile", "create", "bare-kid", "--parent", "base"],
        );
        assert!(
            said.starts_with("Created profile \"bare-kid\" with parents: base\nSaved to: "),
            "{said}"
        );
        let said = text(
            &library,
            &[
                "profile",
                "modify",
                "kid",
                "--add-parent",
                "bare-kid",
                "--add-bundle",
                "project",
                "-d",
                "words",
                "--exclude-fragment",
                "f",
                "--exclude-mcp",
                "m",
            ],
        );
        assert!(
            said.contains("added parent: bare-kid\nadded bundle: project\n")
                && said.ends_with("Modified profile \"kid\"\n"),
            "{said}"
        );
        let said = text(
            &library,
            &[
                "profile",
                "modify",
                "kid",
                "--remove-parent",
                "bare-kid",
                "--remove-bundle",
                "project",
                "--include-fragment",
                "f",
                "--include-mcp",
                "m",
            ],
        );
        assert!(
            said.contains("removed parent: bare-kid\nremoved bundle: project\n"),
            "{said}"
        );
        assert_eq!(
            text(&library, &["profile", "modify", "kid"]),
            "No changes made.\n"
        );
        assert_eq!(
            text(&library, &["profile", "edit", "kid"]),
            "No changes made.\n"
        );
        mem.set_editor(|_, content| Ok(content.replace("words", "other")));
        let said = text(&library, &["profile", "edit", "kid"]);
        assert!(said.starts_with("Updated profile"), "{said}");
    }

    // profile.feature: Export, remove, and re-import
    #[test]
    fn test_profile_export_import_and_remove() {
        let (library, _mem) = world();
        let _ = text(&library, &["profile", "create", "base", "-i", "demo"]);
        let _ = text(&library, &["profile", "create", "kid", "--parent", "base"]);
        let said = text(&library, &["profile", "export", "kid", "pout"]);
        assert!(
            said.starts_with("Exported: ") && said.ends_with(" -> pout/kid.yaml\n"),
            "{said}"
        );
        let _ = text(&library, &["profile", "remove", "kid", "--yes"]);
        let said = text(&library, &["profile", "import", "pout/kid.yaml"]);
        assert!(said.starts_with("Imported: pout/kid.yaml -> "), "{said}");
        let refused = refused_code(&library, &["profile", "import", "pout/kid.yaml"], TEXT);
        assert!(refused.contains("exists"), "{refused}");
        let said = text(
            &library,
            &[
                "profile",
                "import",
                "pout/kid.yaml",
                "--force",
                "--bundle",
                "demo",
            ],
        );
        assert!(said.starts_with("Imported: "), "{said}");
        let said = text(&library, &["profile", "remove", "base"]);
        assert!(said.starts_with("Would remove profile \"base\""), "{said}");
        assert_eq!(
            text(&library, &["profile", "remove", "base", "--yes"]),
            "Removed profile \"base\"\n"
        );
        assert_eq!(
            json(&library, &["profile", "remove", "kid", "-y"])["status"],
            "deleted"
        );
    }

    // vocabulary review 2026-11-06: `profile edit` given a flag patches the file
    // and never opens the editor (without one it opens it, above).
    #[test]
    fn test_profile_edit_given_a_flag_patches_instead_of_opening_the_editor() {
        let (library, _mem) = world();
        let said = text(&library, &["profile", "edit", "dev", "-d", "words"]);
        assert!(said.ends_with("Modified profile \"dev\"\n"), "{said}");
    }
}
