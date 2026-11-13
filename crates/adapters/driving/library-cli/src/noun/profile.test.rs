//! `profile`'s tests, beside its verbs.

mod read {
    use crate::tests::*;

    // profile.feature (moved from the four-noun test of lib.test.rs, unchanged)
    #[test]
    fn test_profile_list_and_show() {
        // OD-40: `ListProfilesResponse`; a project's own profiles are in its
        // bundle `project`, a reference a resource name.
        let profiles = json_of(&["profile", "list"]);
        let dev = &profiles["profiles"][1];
        assert_eq!(
            dev["name"],
            "projects/calm-otter/bundles/project/profiles/dev"
        );
        assert_eq!(dev["bundles"][0], "projects/calm-otter/bundles/demo");
        assert_eq!(dev["default"], true);
        assert_eq!(profiles["profiles"][0]["default"], false);
        assert!(dev.get("display_name").is_none() && dev.get("is_remote").is_none());
        assert_eq!(json_of(&["profile"]), profiles);
        let show = json_of(&["profile", "show", "dev"]);
        assert_eq!(show["description"], "dev profile");
        assert_eq!(
            show["name"],
            "projects/calm-otter/bundles/project/profiles/dev"
        );
        assert_eq!(show["tags"][0], "team");
        assert!(text_of(&["profile", "show", "dev"]).starts_with("Profile: dev\n"));
        assert!(text_of(&["profile"]).starts_with("Profiles (2):\n"));
    }
}

mod write {
    use crate::tests::write_verbs::*;
    use crate::tests::TEXT;

    // profile.feature
    #[test]
    fn test_profile_verbs_answer_in_text() {
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
        // `DeleteProfileResponse`, the preview's and the removal's.
        let preview = json(&library, &["profile", "remove", "kid"]);
        assert_eq!(preview["applied"], false);
        assert_eq!(
            preview["profile"]["name"],
            "projects/calm-otter/bundles/project/profiles/kid"
        );
        assert_eq!(preview["apply_command"], "ctxloom profile remove kid --yes");
        let done = json(&library, &["profile", "remove", "kid", "-y"]);
        assert_eq!(done["applied"], true);
        assert_eq!(
            done["profile"]["name"],
            "projects/calm-otter/bundles/project/profiles/kid"
        );
    }

    // OD-40 (design §5.5): the verbs that answered in text only answer
    // their method's response when json is asked for by name, and stay text
    // by default.
    #[test]
    fn test_profile_writes_answer_their_response_when_json_is_asked_for() {
        let (library, mem) = world();
        let made = json(
            &library,
            &["profile", "create", "base", "-i", "demo", "-d", "d"],
        );
        assert_eq!(
            made["profile"]["name"],
            "projects/calm-otter/bundles/project/profiles/base"
        );
        assert_eq!(
            made["profile"]["bundles"][0],
            "projects/calm-otter/bundles/demo"
        );
        assert_eq!(made["applied"], true);
        let modified = json(&library, &["profile", "modify", "base", "-d", "other"]);
        assert_eq!(modified["applied"], true);
        assert_eq!(modified["changes"][0], "updated description");
        let unchanged = json(&library, &["profile", "modify", "base"]);
        assert_eq!(unchanged["applied"], false);
        mem.set_editor(|_, content| Ok(content.to_string()));
        let edited = json(&library, &["profile", "edit", "base"]);
        assert_eq!(edited["applied"], false);
        assert_eq!(
            edited["profile"]["name"],
            "projects/calm-otter/bundles/project/profiles/base"
        );
        let exported = json(&library, &["profile", "export", "base", "pout"]);
        assert_eq!(exported["destination"], "pout/base.yaml");
        assert_eq!(
            exported["profile"],
            "projects/calm-otter/bundles/project/profiles/base"
        );
        let imported = json(
            &library,
            &["profile", "import", "pout/base.yaml", "--bundle", "demo"],
        );
        assert_eq!(
            imported["profile"]["name"],
            "projects/calm-otter/bundles/demo/profiles/base"
        );
        assert_eq!(imported["applied"], true);
        let unasked = crate::tests::How {
            explicit: None,
            terminal: false,
            degraded: false,
        };
        let said = run_on(
            &library,
            &["profile", "create", "other", "-i", "demo"],
            unasked,
        );
        assert!(
            said.out.starts_with("Created profile \"other\""),
            "{}",
            said.out
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
