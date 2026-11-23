//! `search`'s tests, beside its verbs.

mod read {
    use crate::tests::*;

    // search.feature: query, --tag, --type, --local; --remote is refused here.
    // OD-40 (design §5.9): the json is `SearchLibraryResponse`, each local
    // hit named by `resource` and its kind the `ItemKind` value, a zero
    // count left out.
    #[test]
    fn test_search_filters_by_tag_and_type_and_refuses_remote() {
        let hits = json_of(&["search", "golang"]);
        assert_eq!(hits["count"], 1);
        assert_eq!(
            hits["local"][0]["resource"],
            "projects/calm-otter/bundles/demo/fragments/golang"
        );
        assert_eq!(hits["local"][0]["kind"], "FRAGMENT");
        assert_eq!(json_of(&["search", "-t", "go", "--local"])["count"], 1);
        assert!(json_of(&["search", "--tag", "nosuch", "golang"])
            .get("count")
            .is_none());
        let skill = json_of(&["search", "--type", "skill", "rev"]);
        assert_eq!(skill["local"][0]["kind"], "SKILL");
        assert_eq!(
            skill["local"][0]["resource"],
            "projects/calm-otter/bundles/demo/skills/reviewer"
        );
        let command = json_of(&["search", "--type", "command", "rev"]);
        assert_eq!(command["count"], 1);
        assert_eq!(command["local"][0]["kind"], "COMMAND");
        assert_eq!(
            command["local"][0]["resource"],
            "projects/calm-otter/bundles/demo/commands/review"
        );
        assert!(json_of(&["search", "--type", "fragment", "rev"])
            .get("count")
            .is_none());
        assert_eq!(
            go(&["search", "--remote", "x"], JSON, BARE).refused(),
            "library-application-search-remote:48"
        );
        assert!(text_of(&["search", "golang"]).starts_with("Results (1):\n"));
    }

    // OD-40: one hit is the `SearchHit` message, whole: no `name` (AIP-122
    // reserves it), no `type`, the remote half and every default left out.
    #[test]
    fn test_a_search_json_is_the_search_library_response() {
        assert_eq!(
            json_of(&["search", "--local", "golang"]),
            serde_json::json!({
                "query": "golang",
                "local": [{
                    "resource": "projects/calm-otter/bundles/demo/fragments/golang",
                    "kind": "FRAGMENT",
                    "tags": ["go"],
                    "source": "demo",
                    "match": "name",
                }],
                "count": 1,
            })
        );
        // Nothing matched: the query alone.
        assert_eq!(
            json_of(&["search", "--local", "no-such-thing"]),
            serde_json::json!({"query": "no-such-thing"})
        );
    }

    // Every local kind is named under its bundle: a profile of the project's
    // own under the bundle `project`, an MCP server under `mcpServers`.
    #[test]
    fn test_each_local_kind_is_named_under_its_bundle() {
        let profile = json_of(&["search", "--type", "profile", "dev"]);
        assert_eq!(profile["local"][0]["kind"], "PROFILE");
        assert_eq!(
            profile["local"][0]["resource"],
            "projects/calm-otter/bundles/project/profiles/dev"
        );
        let server = json_of(&["search", "--type", "mcp_server", "tree"]);
        assert_eq!(server["local"][0]["kind"], "MCP_SERVER");
        assert_eq!(
            server["local"][0]["resource"],
            "projects/calm-otter/bundles/demo/mcpServers/tree-sitter"
        );
    }

    // OQ-3: outside a project the names root at `users/me`; a text answer
    // asks no daemon for a name, and a json one fails when none answers.
    #[test]
    fn test_search_names_root_at_the_parent_the_daemon_gives() {
        let home = go_in(&["search", "golang"], JSON, BARE, &Home);
        assert_eq!(
            home.json()["local"][0]["resource"],
            "users/me/bundles/demo/fragments/golang"
        );
        // The daemon's read (step 10a U2): with none, text fails as json does.
        assert_eq!(
            go_in(&["search", "golang"], TEXT, BARE, &Unreachable).refused(),
            "daemon-api-unreached:32"
        );
        assert_eq!(
            go_in(&["search", "golang"], JSON, BARE, &Unreachable).refused(),
            "daemon-api-unreached:32"
        );
    }
}

// OD-40 (design §1.2, OQ-4): a bundle's origin is part of its name; its
// items are named below it, each in its collection.
mod names {
    use library_api::names::item_name;

    // A hit's bundle is named by catalog's names (B1's, which search
    // shares): a remote's item below its remote's bundle.
    #[test]
    fn test_a_hit_is_named_below_its_bundle_by_its_origin() {
        assert_eq!(
            item_name("projects/calm-otter", "acme/rules", "fragments", "go"),
            "projects/calm-otter/remotes/acme/bundles/rules/fragments/go"
        );
        assert_eq!(
            item_name("projects/calm-otter", "demo", "mcpServers", "tree"),
            "projects/calm-otter/bundles/demo/mcpServers/tree"
        );
    }

    // OQ-12: a count is a uint32, saturating.
    #[test]
    fn test_a_count_is_a_uint32_saturating() {
        use library_api::resources::count;
        assert_eq!(count(7), 7);
        assert_eq!(count(u64::from(u32::MAX)), u32::MAX);
        assert_eq!(count(u64::from(u32::MAX) + 1), u32::MAX);
    }
}

// The remote hit's and the kinds' mapping to the message (OD-40): what the
// binary's tests alone reached.
mod mapping {
    use super::super::*;
    use serde_json::json;

    #[test]
    fn test_a_remote_bundle_found_is_its_whole_message() {
        let found = library_application::RemoteFound {
            remote: "acme".to_string(),
            name: "rules".to_string(),
            kind: "bundle".to_string(),
            tags: vec!["go".to_string()],
            description: "Go rules".to_string(),
            author: "someone".to_string(),
            pull_ref: "acme/rules".to_string(),
        };
        let mut out = Vec::new();
        let results = library_application::SearchResults {
            query: "rules".to_string(),
            local: Vec::new(),
            remote: vec![found],
            count: 1,
            hidden_local: 0,
        };
        let searched = library_api::resources::searched("projects/p", &results);
        crate::api::print(&mut out, &searched.remote[0]).unwrap();
        assert_eq!(
            serde_json::from_slice::<serde_json::Value>(&out).unwrap(),
            json!({
                "remote": "acme",
                "item": "rules",
                "kind": "BUNDLE",
                "tags": ["go"],
                "description": "Go rules",
                "author": "someone",
                "pull_reference": "acme/rules",
            })
        );
    }

    #[test]
    fn test_a_bundle_is_its_kind_in_its_collection() {
        use library_api::resources::{item_kind, kind_word};
        assert_eq!(item_kind("bundle"), (v1::ItemKind::Bundle, "bundles"));
        for word in [
            "fragment",
            "command",
            "skill",
            "profile",
            "mcp_server",
            "bundle",
        ] {
            assert_eq!(kind_word(item_kind(word).0 as i32), word);
        }
        assert_eq!(item_kind("widget").0, v1::ItemKind::Unspecified);
        assert_eq!(kind_word(0), "");
    }
}

// Step 10a U2: the search's request carries the command's global flags.
mod asks {
    use crate::tests::{asked_once, degraded};
    use daemon_proto::v1;
    #[allow(unused_imports)]
    use daemon_proto::Carries as _;

    #[test]
    fn test_search_carries_the_globals() {
        let (_, request) = asked_once::<v1::SearchLibraryRequest>(&["search", "go"]);
        assert_eq!(request.query, "go");
        assert_eq!(request.caller_globals().cloned(), degraded());
    }
}
