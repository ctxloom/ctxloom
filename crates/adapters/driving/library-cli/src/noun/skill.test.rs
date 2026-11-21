//! `skill`'s tests, beside its verbs.

mod read {
    use crate::tests::*;

    // skill.feature
    #[test]
    fn test_skill_list_and_show() {
        // OD-40: `ListSkillsResponse` and the `Skill` (FULL).
        let list = json_of(&["skill", "list"]);
        assert_eq!(
            list["skills"][0]["name"],
            "projects/calm-otter/bundles/demo/skills/reviewer"
        );
        assert_eq!(list["skills"][0]["file_count"], 1);
        assert_eq!(list["skills"][0]["description"], "Reviews code");
        assert_eq!(json_of(&["skill"]), list);
        assert_eq!(
            json_of(&["skill", "list", "--bundle", "project"]),
            serde_json::json!({})
        );
        let show = json_of(&["skill", "show", "demo#skills/reviewer"]);
        assert_eq!(show["body"], "SKILL-BODY\n");
        assert_eq!(
            show["name"],
            "projects/calm-otter/bundles/demo/skills/reviewer"
        );
        assert_eq!(show["files"][0]["path"], "SKILL.md");
        assert!(show["files"][0].get("mode").is_none());
        assert!(text_of(&["skill", "show", "demo#skills/reviewer"])
            .starts_with("demo#skills/reviewer\n\ndescription: Reviews code\n"));
        assert!(text_of(&["skill"]).starts_with("Skills (1):\n"));
    }
}

mod write {
    use crate::tests::write_verbs::*;
    use crate::tests::TEXT;

    // skill.feature
    #[test]
    fn test_skill_create_remove_export_and_import() {
        let (library, mem) = world();
        let said = text(
            &library,
            &["skill", "create", "demo", "writer", "-d", "words"],
        );
        assert!(
            said.starts_with("Created skill \"writer\" in bundle \"demo\"\n  "),
            "{said}"
        );
        assert!(
            said.contains("\nEdit ") && said.ends_with("/SKILL.md to describe the skill\n"),
            "{said}"
        );
        assert!(mem
            .text("demo", "skills/writer/SKILL.md")
            .unwrap()
            .contains("words"));
        let said = text(&library, &["skill", "remove", "demo#skills/writer"]);
        assert!(said.starts_with("Would remove skill"), "{said}");
        let said = text(
            &library,
            &["skill", "remove", "demo#skills/writer", "--yes"],
        );
        assert!(
            said.starts_with("Removed skill \"writer\" from bundle \"demo\"\n  "),
            "{said}"
        );
        let said = text(
            &library,
            &["skill", "export", "demo#skills/reviewer", "-o", "r.zip"],
        );
        assert!(
            said.starts_with("Exported skill \"reviewer\" to r.zip (")
                && said.ends_with(" bytes)\n"),
            "{said}"
        );
        let doc = json(
            &library,
            &["skill", "export", "demo#skills/reviewer", "-o", "r2.zip"],
        );
        // `ExportSkillResponse`, its bytes a uint64 (a JSON string, OQ-12).
        assert_eq!(doc["zip_path"], "r2.zip");
        assert_eq!(
            doc["skill"],
            "projects/calm-otter/bundles/demo/skills/reviewer"
        );
        assert!(doc["bytes"].is_string(), "{doc}");
        let made = json(&library, &["skill", "create", "demo", "second"]);
        assert_eq!(
            made["skill"]["name"],
            "projects/calm-otter/bundles/demo/skills/second"
        );
        assert_eq!(made["applied"], true);
        let preview = json(&library, &["skill", "remove", "demo#skills/second"]);
        assert_eq!(preview["applied"], false);
        assert_eq!(
            preview["apply_command"],
            "ctxloom skill remove demo#skills/second --yes"
        );
        let said = text(
            &library,
            &["skill", "import", "r.zip", "--bundle", "project"],
        );
        assert!(
            said.starts_with("Imported skill \"reviewer\" into bundle \"project\" (1 file(s))\n  "),
            "{said}"
        );
        let refused = refused_code(
            &library,
            &["skill", "import", "r.zip", "--bundle", "project"],
            TEXT,
        );
        assert!(refused.contains("exists"), "{refused}");
        let said = text(
            &library,
            &["skill", "import", "r.zip", "--bundle", "project", "--force"],
        );
        assert!(said.starts_with("Imported skill"), "{said}");
        // `ImportSkillResponse`: the skill with its file count.
        let doc = json(
            &library,
            &["skill", "import", "r.zip", "--bundle", "project", "--force"],
        );
        assert_eq!(
            doc["skill"]["name"],
            "projects/calm-otter/bundles/project/skills/reviewer"
        );
        assert_eq!(doc["skill"]["file_count"], 1);
    }
}

// Each view's mapping to its message, field by field (OD-40): what the
// binary's tests alone reached.
mod mapping {
    use super::super::*;
    use library_application::{SkillFileRow, SkillRow, SkillShow};
    use serde_json::json;

    const PARENT: &str = "projects/p";

    fn json_of<M: serde::Serialize>(message: &M) -> serde_json::Value {
        let mut out = Vec::new();
        crate::api::print(&mut out, message).unwrap();
        serde_json::from_slice(&out).unwrap()
    }

    #[test]
    fn test_a_count_is_the_number_it_counts() {
        assert_eq!(count(0), 0);
        assert_eq!(count(3), 3);
    }

    #[test]
    fn test_a_listed_skill_keeps_its_tags_and_file_count() {
        let list = library_application::SkillList {
            rows: vec![SkillRow {
                name: "demo/reviewer".to_string(),
                display_name: String::new(),
                description: "Reviews".to_string(),
                tags: vec!["review".to_string()],
                source: "demo".to_string(),
                file_count: 3,
            }],
            bundle_filter: String::new(),
            total: 1,
        };
        assert_eq!(
            json_of(&library_api::resources::skills(PARENT, &list)),
            json!({"skills": [{
                "name": "projects/p/bundles/demo/skills/reviewer",
                "description": "Reviews",
                "tags": ["review"],
                "file_count": 3,
            }]})
        );
    }

    #[test]
    fn test_a_shown_skill_is_its_whole_resource() {
        let file = |path: &str, mode: &str| SkillFileRow {
            path: path.to_string(),
            sha256: "ab".to_string(),
            mode: mode.to_string(),
        };
        let show = SkillShow {
            name: "reviewer".to_string(),
            bundle: "demo".to_string(),
            description: "Reviews".to_string(),
            license: "MIT".to_string(),
            compatibility: "claude".to_string(),
            allowed_tools: vec!["Read".to_string()],
            body: "BODY".to_string(),
            files: vec![file("SKILL.md", "0644"), file("run.sh", "0755")],
        };
        assert_eq!(
            json_of(&library_api::resources::skill(PARENT, &show)),
            json!({
                "name": "projects/p/bundles/demo/skills/reviewer",
                "description": "Reviews",
                "license": "MIT",
                "compatibility": "claude",
                "allowed_tools": ["Read"],
                "body": "BODY",
                "file_count": 2,
                "files": [
                    {"path": "SKILL.md", "sha256": "ab"},
                    {"path": "run.sh", "sha256": "ab", "executable": true},
                ],
            })
        );
    }

    #[test]
    fn test_an_exported_or_imported_skill_answers_its_files() {
        let out = SkillExported {
            name: "reviewer".to_string(),
            zip_path: "/tmp/reviewer.zip".to_string(),
            bytes: 12,
        };
        let sent = json_of(&exported(PARENT, "demo#skills/reviewer", &out));
        assert_eq!(sent["applied"], true, "{sent}");
        assert_eq!(sent["skill"], "projects/p/bundles/demo/skills/reviewer");
        let came = SkillImported {
            status: "imported".to_string(),
            bundle: "demo".to_string(),
            name: "reviewer".to_string(),
            dir: "skills/reviewer".to_string(),
            file_count: 3,
        };
        assert_eq!(
            json_of(&imported(PARENT, &came)),
            json!({
                "skill": {
                    "name": "projects/p/bundles/demo/skills/reviewer",
                    "path": "skills/reviewer",
                    "file_count": 3,
                },
                "path": "skills/reviewer",
                "applied": true,
            })
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
    fn test_skill_reads_carry_the_globals() {
        let (_, show) =
            asked_once::<v1::GetSkillRequest>(&["skill", "show", "demo#skills/reviewer"]);
        assert_eq!(show.caller_globals().cloned(), degraded());
        let (_, list) = asked_once::<v1::ListSkillsRequest>(&["skill", "list"]);
        assert_eq!(list.caller_globals().cloned(), degraded());
    }
}
