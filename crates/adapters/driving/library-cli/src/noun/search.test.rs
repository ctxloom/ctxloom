//! `search`'s tests, beside its verbs.

mod read {
    use crate::tests::*;

    // search.feature: query, --tag, --type, --local; --remote is refused here.
    #[test]
    fn test_search_filters_by_tag_and_type_and_refuses_remote() {
        let hits = json_of(&["search", "golang"]);
        assert_eq!(hits["count"], 1);
        assert_eq!(hits["local"][0]["name"], "golang");
        assert_eq!(json_of(&["search", "-t", "go", "--local"])["count"], 1);
        assert_eq!(
            json_of(&["search", "--tag", "nosuch", "golang"])["count"],
            0
        );
        assert_eq!(
            json_of(&["search", "--type", "skill", "rev"])["local"][0]["type"],
            "skill"
        );
        assert_eq!(json_of(&["search", "--type", "command", "rev"])["count"], 1);
        assert_eq!(
            json_of(&["search", "--type", "fragment", "rev"])["count"],
            0
        );
        assert_eq!(
            go(&["search", "--remote", "x"], JSON, BARE).refused(),
            "library-application-search-remote:20"
        );
        assert!(text_of(&["search", "golang"]).starts_with("Results (1):\n"));
    }
}
