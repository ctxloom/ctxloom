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
        // Text, drawn from the item's message (owner ruling 2026-11-17): a
        // body as served, else its fields as YAML, keys sorted, references
        // short.
        assert_eq!(
            text_of(&["bundle", "view", "demo#fragments/dist"]),
            "long form\n"
        );
        assert_eq!(
            text_of(&["bundle", "view", "demo#fragments/dist", "-d"]),
            "# (distilled version)\nshort form\n"
        );
        assert!(text_of(&["bundle", "view", "demo#mcp/tree-sitter"])
            .starts_with("# MCP Server: tree-sitter\nargs:\n  - --stdio\ncommand: tree-sitter\n"));
        assert_eq!(
            text_of(&["bundle", "view", "project#profiles/dev"]),
            "# Profile: dev\nbundles:\n  - demo\ndescription: dev profile\ntags:\n  - team\n"
        );
        assert!(
            text_of(&["bundle", "view", "demo#skills/reviewer"]).starts_with("# Skill: reviewer\n")
        );
        // A path no item kind serves is the view's own refusal.
        assert_eq!(
            go(&["bundle", "view", "demo#widgets/x"], JSON, BARE).refused(),
            "library-application-view-path:48"
        );
        assert_eq!(
            go(&["bundle", "show", "nosuch"], JSON, BARE).refused(),
            "library-catalog-bundle-not-found:48"
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
    // distinguishing success 17 (said only under `--detailed-exitcode`); the
    // removal done is ok, 16.
    #[test]
    fn test_a_removals_preview_would_change_and_the_removal_done_is_ok() {
        let (library, _) = world();
        assert_eq!(
            run_on(&library, &["bundle", "remove", "demo"], TEXT).code(),
            17
        );
        assert_eq!(
            run_on(&library, &["bundle", "remove", "demo"], JSON).code(),
            17
        );
        assert_eq!(
            run_on(&library, &["bundle", "remove", "demo", "--yes"], TEXT).code(),
            16
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

// Each answer as the API message it is, built from a view whose every field
// is set, so a field the message leaves out is a field it lost.
mod messages {
    use std::collections::BTreeMap;

    use library_application::{
        BundleList, BundleRow, BundleShow, ShownCommand, ShownFragment, ShownMcp, ShownSkill,
    };
    use serde_json::{json, Value};

    use library_api::resources::bundle;
    use library_api::resources::bundles as listed;

    const P: &str = "projects/calm-otter";

    fn as_json(message: impl serde::Serialize) -> Value {
        serde_json::to_value(message).unwrap()
    }

    fn row() -> BundleRow {
        BundleRow {
            name: "acme/rules".to_string(),
            reference: "https://example.com/acme@bundles/rules".to_string(),
            path: "cache/rules/bundle.yaml".to_string(),
            origin: String::new(),
            version: "2.1.0".to_string(),
            description: "house rules".to_string(),
            tags: vec!["team".to_string()],
            fragment_count: 3,
            command_count: 4,
            mcp_count: 5,
            profile_count: 6,
            held: false,
            commit: "abc123".to_string(),
        }
    }

    // A listed bundle carries every field its row has (BASIC).
    #[test]
    fn test_a_listed_bundle_carries_every_field_of_its_row() {
        let expected = json!({
            "name": "projects/calm-otter/remotes/acme/bundles/rules",
            "origin": "REMOTE",
            "reference": "https://example.com/acme@bundles/rules",
            "commit": "abc123",
            "path": "cache/rules/bundle.yaml",
            "version": "2.1.0",
            "description": "house rules",
            "tags": ["team"],
            "fragment_count": 3,
            "command_count": 4,
            "mcp_server_count": 5,
            "profile_count": 6,
        });
        assert_eq!(as_json(bundle(P, &row())), expected);
        let list = BundleList {
            rows: vec![row()],
            hint: String::new(),
        };
        assert_eq!(as_json(listed(P, &list)), json!({ "bundles": [expected] }));
    }

    // `bundle show` (FULL): its own fields, each child by name with its
    // summary, over the listing's, under the show's own name.
    fn show() -> BundleShow {
        BundleShow {
            name: "demo".to_string(),
            path: "bundles/demo/bundle.yaml".to_string(),
            version: "1.2.3".to_string(),
            author: "ada".to_string(),
            description: "the demo".to_string(),
            tags: vec!["go".to_string()],
            notes: "read me".to_string(),
            installation: "install me".to_string(),
            fragments: BTreeMap::from([(
                "golang".to_string(),
                ShownFragment {
                    tags: vec!["lang".to_string()],
                    preview: "GO-RULES".to_string(),
                    distilled: true,
                    no_distill: true,
                    premise: "when editing Go".to_string(),
                    stale: false,
                },
            )]),
            commands: BTreeMap::from([(
                "review".to_string(),
                ShownCommand {
                    tags: vec!["qa".to_string()],
                    description: "Review code".to_string(),
                    preview: "REVIEW".to_string(),
                    distilled: false,
                    no_distill: false,
                    stale: true,
                },
            )]),
            mcp: BTreeMap::from([(
                "tree-sitter".to_string(),
                ShownMcp {
                    command: "tree-sitter".to_string(),
                    args: vec!["--stdio".to_string()],
                    env: BTreeMap::from([("LANG".to_string(), "C".to_string())]),
                    notes: "mcp notes".to_string(),
                    installation: "npm i".to_string(),
                },
            )]),
            skills: BTreeMap::from([(
                "reviewer".to_string(),
                ShownSkill {
                    path: "skills/reviewer".to_string(),
                    tags: vec!["review".to_string()],
                    notes: "skill notes".to_string(),
                },
            )]),
            profiles: vec!["dev".to_string()],
            hooks: vec!["pre_tool".to_string()],
            // A listing named otherwise: the show's own name wins.
            listing: BundleRow {
                name: "other".to_string(),
                reference: "ctxloom+local:demo".to_string(),
                path: "listed/path".to_string(),
                version: "0.0.1".to_string(),
                description: "listed".to_string(),
                tags: vec!["listed".to_string()],
                commit: "def456".to_string(),
                fragment_count: 1,
                command_count: 1,
                mcp_count: 1,
                profile_count: 1,
                ..BundleRow::default()
            },
        }
    }

    #[test]
    fn test_a_shown_bundle_carries_its_fields_its_children_and_its_listing() {
        let b = "projects/calm-otter/bundles/demo";
        assert_eq!(
            as_json(library_api::resources::bundle_shown(P, &show())),
            json!({
                "name": b,
                "origin": "PROJECT",
                "reference": "ctxloom+local:demo",
                "commit": "def456",
                "path": "bundles/demo/bundle.yaml",
                "version": "1.2.3",
                "author": "ada",
                "description": "the demo",
                "tags": ["go"],
                "notes": "read me",
                "installation": "install me",
                "fragment_count": 1,
                "command_count": 1,
                "mcp_server_count": 1,
                "profile_count": 1,
                "skill_count": 1,
                "fragments": [{
                    "item": format!("{b}/fragments/golang"),
                    "tags": ["lang"],
                    "preview": "GO-RULES",
                    "distilled": "FRESH",
                    "no_distill": true,
                    "premise": "when editing Go",
                }],
                "commands": [{
                    "item": format!("{b}/commands/review"),
                    "tags": ["qa"],
                    "description": "Review code",
                    "preview": "REVIEW",
                    "distilled": "STALE",
                    "no_distill": false,
                }],
                "skills": [{
                    "item": format!("{b}/skills/reviewer"),
                    "tags": ["review"],
                    "notes": "skill notes",
                    "path": "skills/reviewer",
                }],
                "mcp_servers": [{
                    "item": format!("{b}/mcpServers/tree-sitter"),
                    "command": "tree-sitter",
                    "args": ["--stdio"],
                    "env": {"LANG": "C"},
                    "notes": "mcp notes",
                    "installation": "npm i",
                }],
                "profiles": [format!("{b}/profiles/dev")],
                "hooks": ["pre_tool"],
            })
        );
    }
}

// `bundle distill`: the bundle, its fragments and its commands each in its
// own list, each with the state distilling left it in.
mod distill {
    use library_application::fakes::{Setup, CONFIG, ENVELOPE};
    use serde_json::json;

    use crate::tests::write_verbs::json;

    const TIDY: &[(&str, &str)] = &[
        ENVELOPE,
        (
            "fragments/dist.md",
            "---\ntags: [distilled]\n---\nlong form\n",
        ),
        (
            "fragments/dist.distilled.md",
            // The source's digest (T30): the form is fresh.
            "---\nsource_sha256: 1e6de09466e07ecb91f3f923292ba979acc739dc0d20700659e433e1859f8be5\n---\nshort form\n",
        ),
        ("fragments/plain.md", "---\nno_distill: true\n---\nPLAIN\n"),
        ("prompts/quiet.md", "---\nno_distill: true\n---\nQUIET\n"),
    ];

    #[test]
    fn test_bundle_distill_lists_each_fragment_and_command_with_its_state() {
        let library = Setup {
            bundles: &[("tidy", TIDY)],
            layers: &[CONFIG],
            ..Setup::default()
        }
        .library();
        let b = "projects/calm-otter/bundles/tidy";
        assert_eq!(
            json(&library, &["bundle", "distill", "tidy"]),
            json!({
                "name": b,
                "fragments": [
                    {"item": format!("{b}/fragments/dist"), "distilled": "FRESH", "no_distill": false},
                    {"item": format!("{b}/fragments/plain"), "distilled": "NONE", "no_distill": true},
                ],
                "commands": [
                    {"item": format!("{b}/commands/quiet"), "distilled": "NONE", "no_distill": true},
                ],
            })
        );
    }
}

// Step 10a U2: each read asks its view, `--distilled` and the command's
// global flags; a whole bundle's view is the bundle's target, an item's
// its reference's.
mod asks {
    use crate::tests::{asked_once, degraded};
    use daemon_proto::v1;
    #[allow(unused_imports)]
    use daemon_proto::Carries as _;

    #[test]
    fn test_bundle_show_list_and_view_ask_their_view_with_the_globals() {
        let (under, show) = asked_once::<v1::GetBundleRequest>(&["bundle", "show", "demo"]);
        assert_eq!(under, library_api::names::bundle_target("demo"));
        assert_eq!(show.view, v1::bundle::View::Full as i32);
        assert_eq!(show.caller_globals().cloned(), degraded());
        let (_, list) = asked_once::<v1::ListBundlesRequest>(&["bundle", "list"]);
        assert_eq!(list.caller_globals().cloned(), degraded());
        let (under, whole) =
            asked_once::<v1::GetBundleRequest>(&["bundle", "view", "--distilled", "demo"]);
        assert_eq!(under, library_api::names::bundle_target("demo"));
        assert_eq!(whole.view, v1::bundle::View::Content as i32);
        assert!(whole.prefer_distilled);
        assert_eq!(whole.caller_globals().cloned(), degraded());
    }

    #[test]
    fn test_bundle_view_of_an_item_asks_its_kinds_read_with_the_globals() {
        let (under, fragment) = asked_once::<v1::GetFragmentRequest>(&[
            "bundle",
            "view",
            "--distilled",
            "demo#fragments/golang",
        ]);
        assert_eq!(
            under,
            library_api::names::reference_target("demo#fragments/golang")
        );
        assert!(fragment.prefer_distilled);
        assert_eq!(fragment.caller_globals().cloned(), degraded());
        let (_, command) = asked_once::<v1::GetCommandRequest>(&[
            "bundle",
            "view",
            "--distilled",
            "demo#commands/review",
        ]);
        assert!(command.prefer_distilled);
        assert_eq!(command.caller_globals().cloned(), degraded());
        let (_, skill) =
            asked_once::<v1::GetSkillRequest>(&["bundle", "view", "demo#skills/reviewer"]);
        assert_eq!(skill.caller_globals().cloned(), degraded());
        let (_, mcp) =
            asked_once::<v1::GetMcpServerRequest>(&["bundle", "view", "demo#mcp/tree-sitter"]);
        assert_eq!(mcp.caller_globals().cloned(), degraded());
        let (_, profile) =
            asked_once::<v1::GetProfileRequest>(&["bundle", "view", "project#profiles/dev"]);
        assert_eq!(profile.caller_globals().cloned(), degraded());
        let (_, other) =
            asked_once::<v1::GetBundleRequest>(&["bundle", "view", "--distilled", "demo#notes/x"]);
        assert_eq!(other.view, v1::bundle::View::Content as i32);
        assert!(other.prefer_distilled);
        assert_eq!(other.caller_globals().cloned(), degraded());
    }
}

// Step 10b U3: `bundle edit` asks the daemon's `UpdateBundle`, its mask
// naming the fields it was given (`description`, `version`) and its add
// and remove lists as given (several mask paths, one step: owner ruling
// 2026-11-18); nothing given asks no mask and no list.
mod writes_asked {
    use crate::tests::{asked_once, degraded};
    use daemon_proto::v1;
    #[allow(unused_imports)]
    use daemon_proto::Carries as _;

    #[test]
    fn test_bundle_edit_asks_update_bundle_with_its_mask_and_lists() {
        let (under, edit) = asked_once::<v1::UpdateBundleRequest>(&[
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
            "f",
            "--remove-fragment",
            "g",
            "--add-prompt",
            "p",
            "--remove-prompt",
            "q",
            "--add-mcp",
            "m",
            "--remove-mcp",
            "n",
        ]);
        assert_eq!(under, library_api::names::bundle_target("demo"));
        assert_eq!(edit.update_mask.unwrap().paths, ["description", "version"]);
        let bundle = edit.bundle.unwrap();
        assert_eq!(bundle.description, "new words");
        assert_eq!(bundle.version, "2.0.0");
        assert_eq!(edit.add_tags, ["a", "b"]);
        assert_eq!(edit.remove_tags, ["zz"]);
        assert_eq!(edit.add_fragments, ["f"]);
        assert_eq!(edit.remove_fragments, ["g"]);
        assert_eq!(edit.add_commands, ["p"]);
        assert_eq!(edit.remove_commands, ["q"]);
        assert_eq!(edit.add_mcp_servers, ["m"]);
        assert_eq!(edit.remove_mcp_servers, ["n"]);
        assert!(!edit.validate_only);
        assert_eq!(
            edit.caller.as_ref().and_then(|c| c.globals.clone()),
            degraded()
        );
        let (_, bare) = asked_once::<v1::UpdateBundleRequest>(&["bundle", "edit", "demo"]);
        assert!(bare.update_mask.is_none_or(|m| m.paths.is_empty()));
        assert!(bare.add_tags.is_empty() && bare.add_fragments.is_empty());
    }
}

// Step 10b (D2's EXCEPTION): `bundle export` asks the daemon's
// `ExportBundle` with her paths as typed (the daemon reads them below her
// working directory), once.
mod export_asked {
    use crate::tests::{asked_once, degraded};
    use daemon_proto::v1;
    #[allow(unused_imports)]
    use daemon_proto::Carries as _;

    #[test]
    fn test_bundle_export_asks_export_bundle_with_its_paths() {
        let (under, asked) =
            asked_once::<v1::ExportBundleRequest>(&["bundle", "export", "demo", "exported"]);
        assert_eq!(under, "/bundles/demo");
        assert_eq!(asked.destination, "exported");
        assert!(asked.output.is_empty());
        assert!(!asked.validate_only);
        assert_eq!(asked.caller_globals().cloned(), degraded());
        let (_, asked) =
            asked_once::<v1::ExportBundleRequest>(&["bundle", "export", "demo", "-o", "handoff"]);
        assert_eq!(asked.output, "handoff");
    }
}
