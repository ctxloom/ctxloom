//! `config`'s tests, beside its verbs.

mod read {
    use crate::tests::*;

    // config.feature: `config show` prints the effective configuration, one
    // section, or only what the files set (--raw); a bare `config` is `show`.
    // OD-40 (design §3.9, §5.8): its json is the `Config` singleton, named
    // under the project the daemon names, the document what was printed
    // before, under `document`.
    #[test]
    fn test_config_show_prints_the_effective_configuration_a_section_or_the_raw_layers() {
        let all = json_of(&["config", "show"]);
        assert_eq!(all["name"], "projects/calm-otter/config");
        assert_eq!(all["document"]["default_agent"], "dev");
        assert_eq!(all["document"]["agents"]["dev"]["engine"], "claude-code");
        assert_eq!(all["document"]["schema_version"], 9);
        assert_eq!(json_of(&["config"]), all);
        assert_eq!(
            json_of(&["config", "show", "companions"]),
            serde_json::json!({
                "name": "projects/calm-otter/config",
                "document": {"acme": {"command": ["acme-bin", "--as", "acme"]}},
            })
        );
        let raw = json_of(&["config", "show", "--raw"]);
        assert_eq!(
            raw["document"]["companions"],
            serde_json::json!({"acme": {"command": ["acme-bin", "--as", "acme"]}})
        );
        // A section is the document whole, as the layers give it; outside a
        // project the singleton is the home's (OQ-3).
        let set = go(
            &["config", "show", "sync"],
            JSON,
            &["schema_version: 9\nsync:\n  on_run: none\n"],
        );
        assert_eq!(set.json()["document"]["on_run"], "none");
        let home = go_named(&["config", "show", "agents"], JSON, &Home);
        assert_eq!(home.json()["name"], "users/me/config");
        assert!(text_of(&["config", "show", "companions"]).starts_with("acme:\n  command:\n"));
        assert!(text_of(&["config"]).contains("default_agent: dev\n"));
        assert_eq!(
            go(&["config", "show", "nosuch"], JSON, BARE).refused(),
            "library-application-unknown-section:20"
        );
    }
}

mod write {
    use crate::tests::write_verbs::*;
    use crate::tests::TEXT;
    use library_application::fakes::Setup;

    // config.feature
    #[test]
    fn test_config_create_and_edit() {
        let (library, mem) = world();
        assert!(refused_code(&library, &["config", "edit"], TEXT).contains("config-missing"));
        mem.set_scaffold_for_test("config.yaml", "schema_version: 9\n");
        let said = text(&library, &["config", "edit"]);
        assert_eq!(said, "");
        assert!(!mem.opened().is_empty());
        let (library, _) = Setup::default().fixture();
        let said = text(&library, &["config", "create", "--engine", "mock"]);
        assert!(said.starts_with("Wrote "), "{said}");
    }
}
