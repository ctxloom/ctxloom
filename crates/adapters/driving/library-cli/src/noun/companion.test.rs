//! `companion`'s tests, beside its verbs.

mod read {
    use crate::tests::*;

    // companion.feature; OD-40 (design §3.9, §5.7): a list is
    // `ListCompanionsResponse`, each companion named under the project the
    // daemon names, whether its program is there `resolves`, printed true or
    // false (OQ-7).
    #[test]
    fn test_companion_list() {
        let companions = json_of(&["companion", "list"]);
        assert_eq!(
            companions,
            serde_json::json!({"companions": [{
                "name": "projects/calm-otter/companions/acme",
                "command": ["acme-bin", "--as", "acme"],
                "resolves": false,
            }]})
        );
        assert_eq!(json_of(&["companion"]), companions);
        assert!(text_of(&["companion"]).contains("NOT FOUND"));
        let none = go(&["companion", "list"], JSON, &["schema_version: 9\n"]);
        assert_eq!(none.out, "{}\n");
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
        // OD-40: a removal's preview is `DeleteCompanionResponse`, `applied`
        // false and the command that applies.
        assert_eq!(
            json(&library, &["companion", "remove", "acme"]),
            serde_json::json!({
                "companion": {"name": "projects/calm-otter/companions/acme"},
                "applied": false,
                "apply_command": "ctxloom companion remove acme --yes",
            })
        );
        assert_eq!(
            text(&library, &["companion", "rm", "acme", "--yes"]),
            "Removed companion 'acme'\n"
        );
        // An add is `CreateCompanionResponse`: the companion as registered (its
        // program answered the probe, so it resolves), applied when the
        // registration is new or changed; the same again changes nothing.
        let added = serde_json::json!({
            "companion": {
                "name": "projects/calm-otter/companions/acme",
                "command": ["acme-bin"],
                "resolves": true,
            },
            "applied": true,
        });
        let add = ["companion", "add", "acme", "--command", "acme-bin"];
        assert_eq!(json(&library, &add), added);
        let mut again = added.clone();
        again["applied"] = serde_json::json!(false);
        assert_eq!(json(&library, &add), again);
        assert_eq!(
            json(&library, &["companion", "rm", "acme", "--yes"]),
            serde_json::json!({
                "companion": {"name": "projects/calm-otter/companions/acme"},
                "applied": true,
            })
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
