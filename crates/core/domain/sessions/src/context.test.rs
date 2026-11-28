//! What a library change owes a session, as the pure functions judge it.

fn paths(paths: &[&str]) -> Vec<String> {
    paths.iter().map(|path| path.to_string()).collect()
}

// A change touches a loadout where one of its paths is in a bundle the
// loadout names, or is the tree's config (in every loadout): named
// sorted, each once; a change of another bundle touches nothing.
#[test]
fn a_change_touches_the_bundles_a_loadout_names_and_the_config() {
    let dev = vec!["demo".to_string()];
    let demo = "content/bundles/v2/demo/fragments/example.md";
    assert_eq!(super::touched(&dev, &paths(&[demo])), ["demo"]);
    assert_eq!(
        super::touched(&dev, &paths(&["config.yaml", demo, demo])),
        ["config", "demo"]
    );
    assert!(super::touched(&dev, &paths(&["content/bundles/v2/other/bundle.yaml"])).is_empty());
    assert!(
        super::touched(&dev, &paths(&["content/bundles/v2/demox/bundle.yaml"])).is_empty(),
        "a bundle named alike is another"
    );
    assert!(super::touched(&dev, &paths(&["remotes.yaml"])).is_empty());
}

// A path a loadout draws on is kept, sorted, each once; the rest is not.
#[test]
fn a_loadout_draws_on_the_paths_in_its_bundles_and_the_config() {
    let dev = vec!["demo".to_string()];
    let demo = "content/bundles/v2/demo/fragments/example.md";
    assert_eq!(
        super::drawn_on(
            &dev,
            &paths(&[demo, "config.yaml", "content/bundles/v2/other/a.md", demo])
        ),
        ["config.yaml", demo]
    );
}

// A change is owed past the covered position; with none covered, when it was
// recorded after the loadout.
#[test]
fn a_change_is_owed_past_the_covered_position_or_after_the_loadout() {
    let at = |seconds| angzarr_embedded::Timestamp { seconds, nanos: 0 };
    assert!(super::owes(Some(3), at(10), 4, at(1)));
    assert!(!super::owes(Some(3), at(10), 3, at(99)));
    assert!(super::owes(None, at(10), 1, at(11)));
    assert!(!super::owes(None, at(10), 9, at(10)));
}
