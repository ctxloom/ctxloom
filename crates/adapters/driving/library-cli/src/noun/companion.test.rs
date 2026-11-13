//! `companion`'s tests, beside its verbs.

mod read {
    use crate::tests::*;

    // companion.feature (moved from the four-noun test of lib.test.rs, unchanged)
    #[test]
    fn test_companion_list() {
        let companions = json_of(&["companion", "list"]);
        assert_eq!(
            companions[0],
            serde_json::json!({"name": "acme", "command": ["acme-bin", "--as", "acme"], "exists": false})
        );
        assert_eq!(json_of(&["companion"]), companions);
        assert!(text_of(&["companion"]).contains("NOT FOUND"));
    }
}

mod write {
    use crate::tests::write_verbs::*;
    use crate::tests::{How, TEXT};

    // companion.feature
    #[test]
    fn test_companion_verbs() {
        let (library, _mem) = world();
        assert_eq!(
            text(
                &library,
                &[
                    "companion",
                    "add",
                    "acme",
                    "--command",
                    "acme-bin",
                    "--as",
                    "-x"
                ]
            ),
            "Registered companion 'acme' (answers: acme-bin --as -x)\n"
        );
        assert_eq!(
            text(
                &library,
                &[
                    "companion",
                    "add",
                    "acme",
                    "--command",
                    "acme-bin",
                    "--as",
                    "-x"
                ]
            ),
            "Companion 'acme' is already registered (acme-bin --as -x)\n"
        );
        assert!(refused_code(
            &library,
            &["companion", "add", "-x", "--command", "acme-bin"],
            TEXT
        )
        .contains("companion"));
        assert_eq!(
            text(&library, &["companion", "remove", "acme"]),
            "Would remove companion \"acme\".\n\nNothing was removed. Re-run with --yes to apply:\n  ctxloom companion remove acme --yes\n"
        );
        assert_eq!(
            text(&library, &["companion", "rm", "acme", "--yes"]),
            "Removed companion 'acme'\n"
        );
    }

    // loadout_cmd.go (documented condition)
    #[test]
    fn test_loadout_is_the_one_yaml_document() {
        let (library, _) = world();
        let said = text(&library, &["loadout"]);
        assert_eq!(said, library_application::LOADOUT);
        let ran = run_on(
            &library,
            &["loadout", "--format", "yaml"],
            How {
                explicit: None,
                ..TEXT
            },
        );
        assert_eq!(ran.out, library_application::LOADOUT);
        let ran = run_on(
            &library,
            &["loadout", "--format", "toml"],
            How {
                explicit: None,
                ..TEXT
            },
        );
        assert!(ran.refused().contains("loadout-format"));
        assert_eq!(ran.out, "");
    }
}
