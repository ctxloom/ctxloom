//! `context`'s tests, beside its verbs.

mod read {
    use crate::tests::*;

    // context.feature. OD-40 (design §5.9): the json is `AssembledContext`,
    // its profiles named, each fragment it used named with the form it used.
    #[test]
    fn test_context_assemble_takes_profiles_fragments_and_tags() {
        let by_profile = json_of(&["context", "assemble", "-p", "dev"]);
        assert_eq!(
            by_profile["profiles"],
            serde_json::json!(["projects/calm-otter/bundles/project/profiles/dev"])
        );
        assert_eq!(
            json_of(&["context", "assemble", "--profile", "dev"]),
            by_profile
        );
        let by_fragment = json_of(&["context", "assemble", "-f", "demo#fragments/golang"]);
        assert_eq!(by_fragment["context"], "GO-RULES");
        let several = json_of(&[
            "context",
            "assemble",
            "-f",
            "demo#fragments/golang,demo#fragments/testing",
        ]);
        assert_eq!(several["fragments"].as_array().unwrap().len(), 2);
        let by_tag = go(
            &[
                "context",
                "assemble",
                "-f",
                "demo#fragments/golang",
                "-t",
                "nosuch",
            ],
            JSON,
            BARE,
        );
        assert_eq!(by_tag.refused(), "library-application-no-fragments:20");
        assert_eq!(json_of(&["context"]), by_profile);
        // On demand (config.premised) the held-back `golang` is offered in the
        // premise index, which ends the context.
        let text = text_of(&["context", "assemble", "-p", "dev"]);
        assert!(text.contains("short form\n"), "{text}");
        assert!(
            text.ends_with("demo#fragments/golang: when editing Go code\n"),
            "{text}"
        );
    }

    // OD-40: the whole message: the context, its token estimate (a number,
    // OQ-12), each fragment named and the form it was used in, nothing left
    // missing (an empty list is left out).
    #[test]
    fn test_context_assemble_json_is_the_assembled_context() {
        assert_eq!(
            json_of(&["context", "assemble", "-f", "demo#fragments/golang"]),
            serde_json::json!({
                "context": "GO-RULES",
                "tokens": 2,
                "fragments": [{
                    "fragment": "projects/calm-otter/bundles/demo/fragments/golang",
                    "form": "original",
                }],
            })
        );
        let distilled = json_of(&["context", "assemble", "-f", "demo#fragments/dist"]);
        assert_eq!(
            distilled["fragments"],
            serde_json::json!([{
                "fragment": "projects/calm-otter/bundles/demo/fragments/dist",
                "form": "distilled",
            }])
        );
        assert_eq!(distilled["context"], "short form");
    }

    // A fragment asked for and not found keeps the text it was asked by
    // (OQ-16: it names nothing), beside the one that loaded.
    #[test]
    fn test_a_missing_fragment_keeps_the_text_it_was_asked_by() {
        let partly = json_of(&[
            "context",
            "assemble",
            "-f",
            "demo#fragments/golang",
            "-f",
            "demo#fragments/nope",
        ]);
        assert_eq!(
            partly["missing_fragments"],
            serde_json::json!(["ctxloom+local:demo#fragments/nope"])
        );
        assert_eq!(
            partly["fragments"][0]["fragment"],
            "projects/calm-otter/bundles/demo/fragments/golang"
        );
        assert!(partly.get("fragments_loaded").is_none(), "{partly}");
    }

    // OQ-3: outside a project the names root at `users/me`.
    #[test]
    fn test_context_names_root_at_the_parent_the_daemon_gives() {
        let home = go_in(&["context", "assemble", "-p", "dev"], JSON, BARE, &Home);
        assert_eq!(
            home.json()["profiles"],
            serde_json::json!(["users/me/bundles/project/profiles/dev"])
        );
        let text = go_in(
            &["context", "assemble", "-p", "dev"],
            TEXT,
            BARE,
            &Unreachable,
        );
        assert_eq!(text.code(), 0);
    }
}
