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
            "library-application-no-bundle-named:48"
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
        assert_eq!(ran.code(), 16);
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

mod distill {
    use crate::tests::*;
    use daemon_proto::v1;
    #[allow(unused_imports)]
    use daemon_proto::Carries as _;

    /// The daemon's answer to a distill that asks its model: its run.
    fn asking(run: &str) -> v1::DistillFragmentResponse {
        v1::DistillFragmentResponse {
            fragment: Some(v1::Fragment {
                name: "projects/calm-otter/bundles/demo/fragments/golang".to_string(),
                distilled: v1::DistilledState::Fresh as i32,
                ..v1::Fragment::default()
            }),
            path: "fragments/golang.distilled.md".to_string(),
            applied: Some(false),
            run: run.to_string(),
            notices: vec![v1::Notice {
                code: "library-application-stale-distilled".to_string(),
                text: "said before".to_string(),
                ..v1::Notice::default()
            }],
            ..v1::DistillFragmentResponse::default()
        }
    }

    // Step 10b: the daemon asks the model as a run; the command follows
    // that run to its end, and its form is written once it ends answered.
    #[test]
    fn test_a_distill_that_asks_follows_its_run_and_is_applied_once_it_ends() {
        let run = "projects/calm-otter/runs/r1";
        let asks = Asks::answering(vec![Box::new(asking(run)), Box::new(())], None);
        let ran = go_served(
            &["fragment", "distill", "demo#fragments/golang", "-f"],
            JSON,
            BARE,
            &Project,
            Some(&asks),
        );
        assert_eq!(ran.code(), 16, "{}", ran.err);
        let asked = asks.requests::<v1::DistillFragmentRequest>();
        assert_eq!(asked.len(), 1);
        assert!(asked[0].force, "-f asks again");
        assert_eq!(
            asks.requests::<v1::WatchRunRequest>(),
            [v1::WatchRunRequest {
                name: run.to_string()
            }],
            "its run followed"
        );
        let json = ran.json();
        assert_eq!(json["applied"], true, "{json}");
        assert_eq!(json["run"], run);
        assert!(ran.err.contains("stale-distilled"), "{}", ran.err);
        let asks = Asks::answering(vec![Box::new(asking(run)), Box::new(())], None);
        let ran = go_served(
            &["fragment", "distill", "demo#fragments/golang"],
            TEXT,
            BARE,
            &Project,
            Some(&asks),
        );
        assert_eq!(
            ran.out,
            "Distilled fragment \"golang\" in bundle \"demo\" (fragments/golang.distilled.md)\n"
        );
        assert!(!asks.requests::<v1::DistillFragmentRequest>()[0].force);
    }

    // Ruling 114: the command hears its stops BEFORE it asks the daemon to
    // distill (whose answer is a run the daemon has started already), so a
    // stop that arrives before the follow begins is heard, never taken by
    // its default.
    #[test]
    fn test_a_distill_hears_its_stops_before_it_asks_the_daemon() {
        let run = "projects/calm-otter/runs/r1";
        let asks = Asks::answering(vec![Box::new(asking(run)), Box::new(())], None);
        let ran = go_served(
            &["fragment", "distill", "demo#fragments/golang"],
            JSON,
            BARE,
            &Project,
            Some(&asks),
        );
        assert_eq!(ran.code(), 16, "{}", ran.err);
        let order: Vec<&str> = asks
            .asked
            .borrow()
            .iter()
            .map(|(_, asked)| {
                if asked.is::<crate::tests::HearStops>() {
                    "hear"
                } else if asked.is::<v1::DistillFragmentRequest>() {
                    "distill"
                } else if asked.is::<v1::WatchRunRequest>() {
                    "follow"
                } else {
                    "other"
                }
            })
            .collect();
        assert_eq!(order, ["hear", "distill", "follow"]);
    }

    // A run that ends in a failure fails the command in its end's words,
    // what was said before it said first; nothing is applied.
    #[test]
    fn test_a_distill_whose_run_fails_fails_saying_what_was_said() {
        let asks = Asks::answering(vec![Box::new(asking("projects/calm-otter/runs/r2"))], None);
        let ran = go_served(
            &["fragment", "distill", "demo#fragments/golang"],
            JSON,
            BARE,
            &Project,
            Some(&asks),
        );
        assert_eq!(ran.exit, Err("daemon-api-unreached:32".to_string()));
        assert!(ran.out.is_empty(), "{}", ran.out);
        assert!(ran.err.contains("stale-distilled"), "{}", ran.err);
    }

    // A distill that asks nothing (its form fresh) follows no run.
    #[test]
    fn test_a_distill_that_asks_nothing_follows_no_run() {
        let asks = Asks::answering(vec![Box::new(asking(""))], None);
        let ran = go_served(
            &["fragment", "distill", "demo#fragments/golang"],
            JSON,
            BARE,
            &Project,
            Some(&asks),
        );
        assert_eq!(ran.code(), 16, "{}", ran.err);
        assert!(asks.requests::<v1::WatchRunRequest>().is_empty());
        assert_eq!(ran.json()["applied"], false);
    }

    // Plan step 5: a form that matches its source is kept without asking
    // (applied false, saying why); one forced reads the distill prompt
    // through the catalog (ruling 128), and this one holds no built-in
    // `ctxloom` bundle, so it is refused, as `bundle distill` is.
    #[test]
    fn test_distill_keeps_a_fresh_form_and_fails_without_a_distiller() {
        let kept = json_of(&["fragment", "distill", "demo#fragments/dist"]);
        assert_eq!(
            kept["fragment"]["name"],
            "projects/calm-otter/bundles/demo/fragments/dist"
        );
        assert_eq!(kept["applied"], false);
        assert_eq!(kept["detail"], serde_json::json!(["fresh"]));
        assert_eq!(kept["fragment"]["distilled"], "FRESH");
        assert!(text_of(&["fragment", "distill", "demo#fragments/dist"])
            .contains("its distilled form matches its source"));
        assert_eq!(
            go(
                &["fragment", "distill", "demo#fragments/dist", "-f"],
                JSON,
                BARE
            )
            .refused(),
            "library-catalog-bundle-not-found:48"
        );
        assert_eq!(
            go(&["command", "distill", "demo#commands/review"], JSON, BARE).refused(),
            "library-catalog-bundle-not-found:48"
        );
        assert_eq!(
            go(&["bundle", "distill", "demo", "-f"], JSON, BARE).refused(),
            "library-catalog-bundle-not-found:48"
        );
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
        assert_eq!(ran.code(), 16);
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

// Each view's mapping to its message, field by field (OD-40): what the
// binary's tests alone reached.
mod mapping {
    use super::super::*;

    fn done(status: &str, kind: &str) -> ItemDistilled {
        ItemDistilled {
            status: status.to_string(),
            kind: kind.to_string(),
            bundle: "demo".to_string(),
            name: "review".to_string(),
            path: "x/review.distilled.md".to_string(),
        }
    }

    #[test]
    fn test_a_no_distill_item_says_nothing_was_distilled() {
        let mut out = Vec::new();
        distilled_text(&done("no_distill", "command"), &mut out).unwrap();
        assert_eq!(
            String::from_utf8(out).unwrap(),
            "command \"review\" in bundle \"demo\" is marked as no_distill: nothing distilled\n"
        );
    }
}

// Step 10a U2: each read's request carries `--distilled` and the
// command's global flags.
mod asks {
    use crate::tests::{asked, asked_once, degraded};
    use daemon_proto::v1;
    #[allow(unused_imports)]
    use daemon_proto::Carries as _;

    // Step 10b: Go's two-word create is the daemon's create, asked once
    // (no preview, no editor), Go's placeholder asked for.
    #[test]
    fn test_a_two_word_create_asks_the_daemon_once_for_gos_placeholder() {
        let (under, fragment) =
            asked_once::<v1::CreateFragmentRequest>(&["fragment", "create", "demo", "testing"]);
        assert_eq!(under, library_api::names::bundle_target("demo"));
        assert_eq!(fragment.fragment_id, "testing");
        assert!(fragment.placeholder);
        assert!(!fragment.validate_only);
        assert_eq!(fragment.caller_globals().cloned(), degraded());
        let (_, command) =
            asked_once::<v1::CreateCommandRequest>(&["command", "create", "demo", "second"]);
        assert_eq!(command.command_id, "second");
        assert!(command.placeholder);
        assert!(!command.validate_only);
    }

    #[test]
    fn test_item_reads_carry_distilled_and_the_globals() {
        let (_, command) = asked_once::<v1::GetCommandRequest>(&[
            "command",
            "show",
            "--distilled",
            "demo#commands/review",
        ]);
        assert!(command.prefer_distilled);
        assert_eq!(command.caller_globals().cloned(), degraded());
        let (_, fragment) = asked_once::<v1::GetFragmentRequest>(&[
            "fragment",
            "show",
            "--distilled",
            "demo#fragments/golang",
        ]);
        assert!(fragment.prefer_distilled);
        assert_eq!(fragment.caller_globals().cloned(), degraded());
        let (under, index) = asked_once::<v1::GetPremiseIndexRequest>(&["fragment", "premises"]);
        assert_eq!(under, "/premiseIndex");
        assert_eq!(index.caller_globals().cloned(), degraded());
        let commands = asked::<v1::ListCommandsRequest>(&["command", "list"]);
        assert!(!commands.is_empty());
        assert!(commands
            .iter()
            .all(|(_, r)| r.caller_globals().cloned() == degraded()));
        let fragments = asked::<v1::ListFragmentsRequest>(&["fragment", "list"]);
        assert!(!fragments.is_empty());
        assert!(fragments
            .iter()
            .all(|(_, r)| r.caller_globals().cloned() == degraded()));
    }
}
