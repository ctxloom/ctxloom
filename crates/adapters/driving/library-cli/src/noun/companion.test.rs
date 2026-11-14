//! `companion`'s tests, beside its verbs.

mod read {
    use crate::tests::*;

    // companion.feature; OD-40 (design §3.9, §5.7): a list is
    // `ListCompanionsResponse`, each companion named under the project the
    // daemon names, the program its name stands for `bin` (OD-10), whether
    // that is on the PATH `resolves`, printed true or false (OQ-7), and its
    // state (OD-9).
    #[test]
    fn test_companion_list() {
        let companions = json_of(&["companion", "list"]);
        assert_eq!(
            companions,
            serde_json::json!({"companions": [{
                "name": "projects/calm-otter/companions/acme",
                "bin": "ctxloom-companion-acme",
                "resolves": true,
                "state": "ENABLED",
            }]})
        );
        assert_eq!(json_of(&["companion"]), companions);
        assert!(text_of(&["companion"]).contains("found"));
        let none = go(&["companion", "list"], JSON, &["schema_version: 9\n"]);
        assert_eq!(none.out, "{}\n");
    }
}

mod write {
    use crate::tests::write_verbs::*;
    use crate::tests::{How, TEXT};

    // companion.feature: a companion is added by name alone (OD-10).
    #[test]
    fn test_companion_verbs() {
        let (library, mem) = world();
        mem.set_companion_loadout("acme", "run:\n  version: 1.0.0\n");
        assert_eq!(
            text(&library, &["companion", "add", "acme"]),
            "Registered companion 'acme' (its program ctxloom-companion-acme answers)\n"
        );
        assert_eq!(
            text(&library, &["companion", "add", "acme"]),
            "Companion 'acme' is already registered (ctxloom-companion-acme)\n"
        );
        assert!(refused_code(&library, &["companion", "add", "a/b"], TEXT).contains("companion"));
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
        // OD-9: disable and enable answer the companion as it is now (AIP-136).
        assert_eq!(
            text(&library, &["companion", "disable", "acme"]),
            "Companion 'acme' is disabled\n"
        );
        assert_eq!(
            text(&library, &["companion", "disable", "acme"]),
            "Companion 'acme' is already disabled\n"
        );
        assert_eq!(
            json(&library, &["companion", "enable", "acme"]),
            serde_json::json!({
                "name": "projects/calm-otter/companions/acme",
                "bin": "ctxloom-companion-acme",
                "state": "ENABLED",
            })
        );
        assert_eq!(
            text(&library, &["companion", "rm", "acme", "--yes"]),
            "Removed companion 'acme'\n"
        );
        assert!(
            refused_code(&library, &["companion", "enable", "acme"], TEXT)
                .contains("companion-not-registered")
        );
        // An add is `CreateCompanionResponse`: the companion as registered (its
        // program answered the probe, so it resolves), applied when the
        // registration is new; the same again changes nothing.
        let added = serde_json::json!({
            "companion": {
                "name": "projects/calm-otter/companions/acme",
                "bin": "ctxloom-companion-acme",
                "resolves": true,
                "state": "ENABLED",
            },
            "applied": true,
        });
        let add = ["companion", "add", "acme"];
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
