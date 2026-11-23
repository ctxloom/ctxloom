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
                "document": {"acme": {}},
            })
        );
        let raw = json_of(&["config", "show", "--raw"]);
        assert_eq!(
            raw["document"]["companions"],
            serde_json::json!({"acme": {}})
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
        assert!(text_of(&["config", "show", "companions"]).starts_with("acme: {}"));
        assert!(text_of(&["config"]).contains("default_agent: dev\n"));
        assert_eq!(
            go(&["config", "show", "nosuch"], JSON, BARE).refused(),
            "library-application-unknown-section:48"
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
        let (library, mem) = Setup::default().fixture();
        assert!(refused_code(&library, &["config", "edit"], TEXT).contains("config-missing"));
        assert!(mem.edits().is_empty(), "no editor before a config");
        // Step 10b U3: the config through the editor, round trip, written
        // whole; nothing said on success.
        let (library, mem) = Setup {
            layers: &["schema_version: 9\n"],
            ..Setup::default()
        }
        .fixture();
        mem.set_editor(|_, text| Ok(format!("{text}# mended\n")));
        let said = text(&library, &["config", "edit"]);
        assert_eq!(said, "");
        assert!(mem.opened().is_empty(), "nothing opened in place");
        assert_eq!(
            mem.config(library_application::fakes::ConfigScope::Project)
                .unwrap(),
            "schema_version: 9\n# mended\n"
        );
        let (library, _) = Setup::default().fixture();
        let said = text(&library, &["config", "create", "--engine", "mock"]);
        assert!(said.starts_with("Wrote "), "{said}");
    }
}

// Step 10a U2: the read asks the view the flag names, with the command's
// global flags.
mod asks {
    use crate::tests::{asked_once, degraded};
    use daemon_proto::v1;
    #[allow(unused_imports)]
    use daemon_proto::Carries as _;

    #[test]
    fn test_config_show_asks_its_view_with_the_globals() {
        let (under, effective) = asked_once::<v1::GetConfigRequest>(&["config", "show"]);
        assert_eq!(under, "/config");
        assert_eq!(effective.view, v1::config::View::Effective as i32);
        assert_eq!(effective.caller_globals().cloned(), degraded());
        let (_, set) = asked_once::<v1::GetConfigRequest>(&["config", "show", "--raw"]);
        assert_eq!(set.view, v1::config::View::Set as i32);
    }
}

// Step 10b U3: `config edit` asks the daemon's `EditConfig`: its preview
// (whose Edit need the CLI presents), then the apply with the editor's
// text and the preview's etag.
mod writes_asked {
    use crate::tests::{go_served, Asks, Project, BARE, TEXT};
    use daemon_proto::v1;
    #[allow(unused_imports)]
    use daemon_proto::Carries as _;

    #[test]
    fn test_config_edit_previews_presents_its_edit_need_and_applies_the_answer() {
        let previewed = v1::EditConfigResponse {
            applied: Some(false),
            etag: "7".to_string(),
            needs: vec![v1::Need {
                optional: false,
                id: "edit".to_string(),
                kind: Some(v1::need::Kind::Edit(v1::Edit {
                    file_name: "config.yaml".to_string(),
                    text: "schema_version: 9\n".to_string(),
                    launch: Some(v1::Launch {
                        kind: v1::launch::Kind::Editor as i32,
                        program: "ed".to_string(),
                        ..v1::Launch::default()
                    }),
                })),
            }],
            ..v1::EditConfigResponse::default()
        };
        let asks = Asks::answering(
            vec![
                Box::new(previewed),
                Box::new("schema_version: 9\n# mended\n".to_string()),
            ],
            None,
        );
        let ran = go_served(&["config", "edit"], TEXT, BARE, &Project, Some(&asks));
        let sent = asks.requests::<v1::EditConfigRequest>();
        assert_eq!(sent.len(), 2, "{}", ran.err);
        assert!(sent[0].validate_only);
        assert!(sent[0].caller_presentable().is_some_and(|p| p.can_launch));
        assert_eq!(sent[1].etag, "7");
        assert!(!sent[1].validate_only);
        assert_eq!(
            sent[1].answers,
            [v1::Answer {
                need_id: "edit".to_string(),
                answer: Some(v1::answer::Answer::Edited(
                    "schema_version: 9\n# mended\n".to_string()
                )),
            }]
        );
        assert_eq!(
            asks.requests::<v1::Edit>().len(),
            1,
            "the editor launched once"
        );
    }

    fn previewed_edit(launch: Option<v1::Launch>) -> v1::EditConfigResponse {
        v1::EditConfigResponse {
            applied: Some(false),
            etag: "7".to_string(),
            needs: vec![v1::Need {
                optional: false,
                id: "edit".to_string(),
                kind: Some(v1::need::Kind::Edit(v1::Edit {
                    file_name: "config.yaml".to_string(),
                    text: "schema_version: 9\n".to_string(),
                    launch,
                })),
            }],
            ..v1::EditConfigResponse::default()
        }
    }

    // The Caller ruling (kept 2026-11-19): the CLI says `can_launch` when
    // it is at a terminal, and only then (stdin and stdout, as it asks).
    #[test]
    fn test_a_write_says_it_can_launch_only_at_a_terminal() {
        for at_terminal in [true, false] {
            let asks = Asks::answering(vec![Box::new(previewed_edit(None))], None);
            let asks = if at_terminal {
                asks
            } else {
                asks.off_terminal()
            };
            go_served(&["config", "edit"], TEXT, BARE, &Project, Some(&asks));
            let sent = asks.requests::<v1::EditConfigRequest>();
            assert!(!sent.is_empty());
            for request in &sent {
                let presentable = request.caller_presentable();
                assert_eq!(
                    presentable.map(|p| p.can_launch),
                    Some(at_terminal),
                    "at a terminal: {at_terminal}"
                );
            }
        }
    }

    // Off a terminal the preview carries no launch, so no editor is run
    // (an editor on no terminal hangs or garbles): the apply goes with no
    // answer, and the daemon answers the preview again, applying nothing.
    #[test]
    fn test_an_edit_need_with_no_launch_runs_no_editor_and_sends_no_answer() {
        let asks = Asks::answering(vec![Box::new(previewed_edit(None))], None).off_terminal();
        go_served(&["config", "edit"], TEXT, BARE, &Project, Some(&asks));
        assert!(asks.requests::<v1::Edit>().is_empty(), "no editor launched");
        let sent = asks.requests::<v1::EditConfigRequest>();
        assert_eq!(sent.len(), 2);
        assert!(sent[1].answers.is_empty());
    }
}
