//! `item`'s tests, beside its verbs.

mod read {
    use crate::tests::*;

    // fragment.feature, command.feature: list (filtered by --bundle), show
    // (text only), premises; a bare noun lists.
    #[test]
    fn test_fragment_and_command_list_show_and_premises() {
        // OD-40: `ListFragmentsResponse`; an empty list is `{}`.
        let fragments = json_of(&["fragment", "list"]);
        assert_eq!(fragments["fragments"].as_array().unwrap().len(), 4);
        assert_eq!(
            fragments["fragments"][0]["name"],
            "projects/calm-otter/bundles/demo/fragments/ctl"
        );
        assert_eq!(fragments["fragments"][0]["reference"], "demo#fragments/ctl");
        assert_eq!(json_of(&["fragment"]), fragments);
        assert_eq!(
            json_of(&["fragment", "list", "--bundle", "project"]),
            serde_json::json!({})
        );
        assert_eq!(
            go(&["fragment", "list", "--bundle", "nosuch"], JSON, BARE).refused(),
            "library-application-no-bundle-named:20"
        );
        assert!(text_of(&["fragment", "list"]).starts_with("Fragments (4):\n"));
        let commands = json_of(&["command", "list"]);
        assert_eq!(
            commands["commands"][0]["name"],
            "projects/calm-otter/bundles/demo/commands/review"
        );
        assert_eq!(commands["commands"][0]["reference"], "demo#commands/review");
        assert_eq!(json_of(&["command"]), commands);
        assert_eq!(
            text_of(&["fragment", "show", "demo#fragments/golang"]),
            "golang\n\nGO-RULES\n"
        );
        assert_eq!(
            text_of(&["command", "show", "demo#commands/review"]),
            "review\n\nREVIEW-BODY\n"
        );
        assert_eq!(
            text_of(&["fragment", "show", "demo#fragments/dist", "-d"]),
            "# (distilled version)\ndist\n\nshort form\n"
        );
        assert!(
            !go(&["fragment", "show", "demo#commands/review"], TEXT, BARE)
                .refused()
                .is_empty()
        );
        // The `PremiseIndex` singleton, each entry naming its fragment.
        let premises = json_of(&["fragment", "premises"]);
        assert_eq!(premises["name"], "projects/calm-otter/premiseIndex");
        assert_eq!(
            premises["fragments"][0]["fragment"],
            "projects/calm-otter/bundles/demo/fragments/golang"
        );
        assert_eq!(premises["fragments"][0]["premise"], "when editing Go code");
        assert!(text_of(&["fragment", "premises"])
            .ends_with("- demo#fragments/golang: when editing Go code\n"));
    }

    // OD-40 (design §5.2): `show` answers json when it is asked for by name,
    // the item's resource with its body; by default it stays the body's text
    // (an agent's shell runs it off a terminal).
    #[test]
    fn test_an_item_show_answers_an_asked_for_json_and_a_default_text() {
        let asked = go(&["fragment", "show", "demo#fragments/golang"], JSON, BARE);
        let fragment = asked.json();
        assert_eq!(
            fragment["name"],
            "projects/calm-otter/bundles/demo/fragments/golang"
        );
        assert_eq!(fragment["body"], "GO-RULES\n");
        let by_default = How {
            explicit: None,
            terminal: false,
            degraded: false,
        };
        let ran = go(
            &["fragment", "show", "demo#fragments/golang"],
            by_default,
            BARE,
        );
        assert_eq!(ran.code(), 0);
        assert_eq!(ran.out, "golang\n\nGO-RULES\n");
        let command = go(&["command", "show", "demo#commands/review"], JSON, BARE).json();
        assert_eq!(
            command["name"],
            "projects/calm-otter/bundles/demo/commands/review"
        );
        assert_eq!(command["body"], "REVIEW-BODY\n");
        let short = go(
            &["fragment", "show", "demo#fragments/dist", "-d"],
            JSON,
            BARE,
        )
        .json();
        assert_eq!(short["body_is_distilled"], true);
        assert_eq!(short["body"], "short form\n");
        // S51: the distilled form's state against its source, judged now.
        assert_eq!(short["distilled"], "FRESH");
        assert_eq!(fragment["distilled"], "NONE");
    }
}

mod write {
    use crate::tests::write_verbs::*;
    use crate::tests::TEXT;

    // j001900: `fragment create <ref>` writes what the editor wrote, and makes a
    // bundle nobody had, saying so.
    #[test]
    fn test_fragment_create_by_reference_says_it_made_the_bundle() {
        let (library, mem) = world();
        mem.set_editor(|_, content| Ok(format!("{content}\nNOTES")));
        assert_eq!(
            text(&library, &["fragment", "create", "runbook#fragments/deploy"]),
            "Created bundle \"runbook\"\nCreated fragment \"deploy\" in bundle \"runbook\"\nEdit with: ctxloom fragment edit runbook#fragments/deploy\n"
        );
        assert!(mem
            .text("runbook", "fragments/deploy.md")
            .unwrap()
            .ends_with("NOTES"));
        // OD-40: `CreateCommandResponse` and `CreateFragmentResponse`.
        let doc = json(&library, &["command", "create", "demo#commands/third"]);
        assert_eq!(
            doc["command"]["name"],
            "projects/calm-otter/bundles/demo/commands/third"
        );
        assert_eq!(doc["applied"], true);
        assert!(doc.get("bundle_created").is_none(), "{doc}");
        let doc = json(&library, &["fragment", "create", "fresh#fragments/one"]);
        assert_eq!(doc["bundle_created"], true, "{doc}");
        assert_eq!(
            doc["fragment"]["name"],
            "projects/calm-otter/bundles/fresh/fragments/one"
        );
    }

    // fragment.feature / command.feature: create, remove, edit
    #[test]
    fn test_fragment_and_command_create_remove_and_edit() {
        let (library, mem) = world();
        assert_eq!(
            text(&library, &["fragment", "create", "demo", "testing"]),
            "Created fragment \"testing\" in bundle \"demo\"\nEdit with: ctxloom fragment edit demo#fragments/testing\n"
        );
        assert_eq!(
            text(&library, &["command", "create", "demo", "second"]),
            "Created command \"second\" in bundle \"demo\"\nEdit with: ctxloom command edit demo#commands/second\n"
        );
        assert_eq!(
            json(&library, &["fragment", "create", "demo", "third"])["fragment"]["name"],
            "projects/calm-otter/bundles/demo/fragments/third"
        );
        // `DeleteFragmentResponse`: the preview names the fragment.
        let preview = json(&library, &["fragment", "remove", "demo#fragments/testing"]);
        assert_eq!(preview["applied"], false);
        assert_eq!(
            preview["fragment"]["name"],
            "projects/calm-otter/bundles/demo/fragments/testing"
        );
        assert_eq!(
            preview["apply_command"],
            "ctxloom fragment remove demo#fragments/testing --yes"
        );
        assert_eq!(
            text(&library, &["fragment", "remove", "demo#fragments/testing"]),
            "Would remove fragment \"testing\" from bundle \"demo\".\n\nNothing was removed. Re-run with --yes to apply:\n  ctxloom fragment remove demo#fragments/testing --yes\n"
        );
        assert_eq!(
            text(
                &library,
                &["fragment", "rm", "demo#fragments/testing", "--yes"]
            ),
            "Removed fragment \"testing\" from bundle \"demo\"\n"
        );
        assert_eq!(
            text(
                &library,
                &["command", "remove", "demo#commands/second", "-y"]
            ),
            "Removed command \"second\" from bundle \"demo\"\n"
        );
        mem.set_editor(|_, content| Ok(format!("{content}\nMORE")));
        assert_eq!(
            text(&library, &["fragment", "edit", "demo#fragments/example", "--no-distill"]),
            "Updated fragment \"example\" in bundle \"demo\"\n\nBundle modified. To publish changes:\n  ctxloom bundle push demo [remote]\n"
        );
        assert!(mem
            .text("demo", "fragments/example.md")
            .unwrap()
            .contains("MORE"));
        let ran = run_on(
            &library,
            &["command", "edit", "demo#commands/review", "--no-distill"],
            TEXT,
        );
        assert_eq!(ran.code(), 0);
        assert!(
            ran.err
                .contains("distilled form not refreshed (--no-distill)"),
            "{}",
            ran.err
        );
        mem.set_editor(|_, content| Ok(content.to_string()));
        assert_eq!(
            text(&library, &["fragment", "edit", "demo#fragments/example"]),
            "No changes made.\n"
        );
        // `UpdateFragmentResponse`: an edit that changed nothing applied
        // nothing.
        let unchanged = json(&library, &["fragment", "edit", "demo#fragments/example"]);
        assert_eq!(unchanged["applied"], false);
        assert_eq!(
            unchanged["fragment"]["name"],
            "projects/calm-otter/bundles/demo/fragments/example"
        );
        let removed = json(
            &library,
            &["fragment", "remove", "demo#fragments/third", "-y"],
        );
        assert_eq!(removed["applied"], true);
        assert_eq!(
            removed["fragment"]["name"],
            "projects/calm-otter/bundles/demo/fragments/third"
        );
    }
}
