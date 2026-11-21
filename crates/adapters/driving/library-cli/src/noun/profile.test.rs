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
        // Off a terminal a create is json, as every verb (smoke
        // 2026-11-19); a modify stays text unless json is asked for by name
        // (profile.feature: "only an explicit --format json is json").
        let said = run_on(
            &library,
            &["profile", "create", "other", "-i", "demo"],
            unasked,
        );
        assert_eq!(
            said.json()["profile"]["name"],
            "projects/calm-otter/bundles/project/profiles/other",
            "{}",
            said.out
        );
        let said = run_on(
            &library,
            &["profile", "modify", "other", "-d", "x"],
            unasked,
        );
        assert!(
            said.out.contains("Modified profile \"other\"\n"),
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

// Each view's mapping to its message, field by field (OD-40): what the
// binary's tests alone reached.
mod mapping {
    use super::super::*;
    use library_application::ProfileShow;
    use serde_json::json;

    const PARENT: &str = "projects/p";

    fn json_of<M: serde::Serialize>(message: &M) -> serde_json::Value {
        let mut out = Vec::new();
        crate::api::print(&mut out, message).unwrap();
        serde_json::from_slice(&out).unwrap()
    }

    fn strings(words: &[&str]) -> Vec<String> {
        words.iter().map(|word| (*word).to_string()).collect()
    }

    #[test]
    fn test_a_listed_profile_is_its_whole_row() {
        let list = library_application::ProfileList {
            rows: vec![library_application::ProfileRow {
                name: "dev".to_string(),
                display_name: "dev".to_string(),
                description: "the dev one".to_string(),
                parents: strings(&["base", "lean"]),
                tags: strings(&["team"]),
                bundles: strings(&["demo"]),
                default: true,
                path: "profiles/dev.yaml".to_string(),
                is_remote: false,
                bundle: String::new(),
                entry: String::new(),
            }],
            hint: String::new(),
        };
        assert_eq!(
            json_of(&library_api::resources::profiles(PARENT, &list)),
            json!({"profiles": [{
                "name": "projects/p/bundles/project/profiles/dev",
                "description": "the dev one",
                "parents": [
                    "projects/p/bundles/project/profiles/base",
                    "projects/p/bundles/project/profiles/lean",
                ],
                "tags": ["team"],
                "bundles": ["projects/p/bundles/demo"],
                "default": true,
                "path": "profiles/dev.yaml",
            }]})
        );
    }

    fn show() -> ProfileShow {
        ProfileShow {
            name: "dev".to_string(),
            description: "the dev one".to_string(),
            parents: strings(&["base"]),
            tags: strings(&["team"]),
            bundles: strings(&["demo"]),
            variables: [("lang".to_string(), "go".to_string())].into(),
            exclude_fragments: strings(&["demo#fragments/ctl"]),
            exclude_mcp: strings(&["tree-sitter"]),
            path: "profiles/dev.yaml".to_string(),
            default: false,
            ..ProfileShow::default()
        }
    }

    #[test]
    fn test_a_shown_profile_is_its_whole_resource() {
        assert_eq!(
            json_of(&library_api::resources::profile_shown(PARENT, &show())),
            json!({
                "name": "projects/p/bundles/project/profiles/dev",
                "description": "the dev one",
                "parents": ["projects/p/bundles/project/profiles/base"],
                "tags": ["team"],
                "bundles": ["projects/p/bundles/demo"],
                "variables": {"lang": "go"},
                "exclude_fragments": ["demo#fragments/ctl"],
                "exclude_mcp_servers": ["tree-sitter"],
                "path": "profiles/dev.yaml",
                "default": false,
            })
        );
    }

    #[test]
    fn test_an_exported_or_imported_profile_answers_its_files() {
        let out = ProfileExported {
            source: "profiles/dev.yaml".to_string(),
            dest: "/tmp/dev.yaml".to_string(),
        };
        assert_eq!(
            json_of(&exported(PARENT, "dev", &out)),
            json!({
                "profile": "projects/p/bundles/project/profiles/dev",
                "source": "profiles/dev.yaml",
                "destination": "/tmp/dev.yaml",
                "applied": true,
            })
        );
        let came = ProfileImported {
            source: "/tmp/dev.yaml".to_string(),
            dest: "profiles/dev.yaml".to_string(),
        };
        assert_eq!(
            json_of(&imported(PARENT, "", &came)),
            json!({
                "profile": {"name": "projects/p/bundles/project/profiles/dev"},
                "path": "profiles/dev.yaml",
                "applied": true,
            })
        );
    }
}

// Step 10b U3: `profile create` asks `CreateProfile` under the bundle its
// `--bundle` names (none: the project), its fields as given; `profile
// edit|modify` with flags asks `UpdateProfile` once, a patch (its lists,
// `description` by mask); `profile edit` alone previews its Edit need and
// applies the answer with the preview's etag.
mod writes_asked {
    use crate::tests::{asked_once, degraded};
    use daemon_proto::v1;
    #[allow(unused_imports)]
    use daemon_proto::Carries as _;

    #[test]
    fn test_profile_create_asks_create_profile_with_its_fields() {
        let (under, made) = asked_once::<v1::CreateProfileRequest>(&[
            "profile",
            "create",
            "clinic",
            "--parent",
            "base",
            "--include",
            "demo",
            "--add-bundle",
            "ops",
            "--add-skill",
            "ops#skills/doctor",
            "-d",
            "day-to-day",
        ]);
        assert_eq!(under, "");
        assert_eq!(made.profile_id, "clinic");
        assert!(!made.validate_only);
        assert_eq!(made.caller_globals().cloned(), degraded());
        let profile = made.profile.unwrap();
        assert_eq!(profile.description, "day-to-day");
        assert_eq!(profile.parents, ["base"]);
        assert_eq!(profile.bundles, ["demo", "ops"]);
        assert_eq!(profile.skills, ["ops#skills/doctor"]);
        let (under, _) = asked_once::<v1::CreateProfileRequest>(&[
            "profile", "create", "clinic", "-i", "demo", "--bundle", "ops",
        ]);
        assert_eq!(under, "/bundles/ops");
    }

    #[test]
    fn test_profile_edit_given_flags_asks_one_patch_with_its_lists() {
        let (under, patch) = asked_once::<v1::UpdateProfileRequest>(&[
            "profile",
            "edit",
            "dev",
            "-d",
            "words",
            "--add-parent",
            "a",
            "--remove-parent",
            "b",
            "--add-bundle",
            "c",
            "--remove-bundle",
            "d",
            "--exclude-fragment",
            "e",
            "--include-fragment",
            "f",
            "--exclude-mcp",
            "g",
            "--include-mcp",
            "h",
        ]);
        assert_eq!(under, "/profiles/dev");
        assert!(!patch.validate_only);
        assert_eq!(patch.caller_globals().cloned(), degraded());
        assert_eq!(patch.update_mask.unwrap().paths, ["description"]);
        assert_eq!(patch.profile.unwrap().description, "words");
        assert_eq!(
            [
                patch.add_parents,
                patch.remove_parents,
                patch.add_bundles,
                patch.remove_bundles,
                patch.exclude_fragments,
                patch.include_fragments,
                patch.exclude_mcp_servers,
                patch.include_mcp_servers,
            ],
            [["a"], ["b"], ["c"], ["d"], ["e"], ["f"], ["g"], ["h"]]
        );
        // `modify` alone is a patch of nothing: its mask sent, naming none.
        let (_, bare) = asked_once::<v1::UpdateProfileRequest>(&["profile", "modify", "dev"]);
        assert_eq!(bare.update_mask.map(|m| m.paths.len()), Some(0));
    }

    #[test]
    fn test_profile_edit_alone_previews_its_edit_need_and_applies_with_its_etag() {
        use crate::tests::{go_served, Asks, Project, BARE, TEXT};
        let previewed = v1::UpdateProfileResponse {
            applied: Some(false),
            etag: "7".to_string(),
            needs: vec![v1::Need {
                optional: false,
                id: "edit".to_string(),
                kind: Some(v1::need::Kind::Edit(v1::Edit {
                    file_name: "dev.yaml".to_string(),
                    text: "bundles:\n  - demo\n".to_string(),
                    launch: Some(v1::Launch {
                        kind: v1::launch::Kind::Editor as i32,
                        program: "ed".to_string(),
                        ..v1::Launch::default()
                    }),
                })),
            }],
            ..v1::UpdateProfileResponse::default()
        };
        let asks = Asks::answering(
            vec![
                Box::new(previewed),
                Box::new("bundles:\n  - ops\n".to_string()),
            ],
            None,
        );
        let ran = go_served(
            &["profile", "edit", "dev"],
            TEXT,
            BARE,
            &Project,
            Some(&asks),
        );
        let sent = asks.requests::<v1::UpdateProfileRequest>();
        assert_eq!(sent.len(), 2, "{}", ran.err);
        assert!(sent[0].validate_only && sent[0].update_mask.is_none());
        assert!(sent[0].caller_presentable().is_some_and(|p| p.can_launch));
        assert!(!sent[1].validate_only && sent[1].update_mask.is_none());
        assert_eq!(sent[1].etag, "7");
        assert_eq!(
            sent[1].answers,
            [v1::Answer {
                need_id: "edit".to_string(),
                answer: Some(v1::answer::Answer::Edited(
                    "bundles:\n  - ops\n".to_string()
                )),
            }]
        );
    }
}

// Step 10a U2: each read's request carries the command's global flags.
mod asks {
    use crate::tests::{asked_once, degraded};
    use daemon_proto::v1;
    #[allow(unused_imports)]
    use daemon_proto::Carries as _;

    #[test]
    fn test_profile_reads_carry_the_globals() {
        let (_, show) = asked_once::<v1::GetProfileRequest>(&["profile", "show", "dev"]);
        assert_eq!(show.caller_globals().cloned(), degraded());
        let (_, list) = asked_once::<v1::ListProfilesRequest>(&["profile", "list"]);
        assert_eq!(list.caller_globals().cloned(), degraded());
    }
}
