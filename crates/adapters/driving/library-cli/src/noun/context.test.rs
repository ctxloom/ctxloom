//! `context`'s tests, beside its verbs.

mod read {
    use crate::tests::*;

    // context.feature
    #[test]
    fn test_context_assemble_takes_profiles_fragments_and_tags() {
        let by_profile = json_of(&["context", "assemble", "-p", "dev"]);
        assert_eq!(by_profile["profiles"], serde_json::json!(["dev"]));
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
        assert_eq!(several["fragments_loaded"].as_array().unwrap().len(), 2);
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
}
