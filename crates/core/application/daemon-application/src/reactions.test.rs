//! Step 10c U13: the pure, keyed reaction to a library change.

use daemon_proto::v1;

use super::{fragment_name, loadout_of_assembled};

fn paths(paths: &[&str]) -> Vec<String> {
    paths.iter().map(|p| p.to_string()).collect()
}

// An assembly's loadout is the same, its fragments named as resources
// (`.../bundles/<b>/fragments/<f>`), those a premise held back included.
#[test]
fn an_assemblys_loadout_names_its_fragments_bundles_held_back_ones_too() {
    let assembled = v1::AssembledContext {
        fragments: vec![v1::AssembledFragment {
            fragment: "projects/p/bundles/demo/fragments/example".to_string(),
            ..v1::AssembledFragment::default()
        }],
        premised: vec![v1::PremiseEntry {
            fragment: "projects/p/bundles/held/fragments/later".to_string(),
            ..v1::PremiseEntry::default()
        }],
        ..v1::AssembledContext::default()
    };
    let got = loadout_of_assembled("", &paths(&["dev"]), &assembled);
    assert_eq!(got.bundles, ["demo", "held"]);
    assert_eq!(got.profiles, ["dev"]);
}

// A changed path names its fragment under its tree's parent: a checkout's
// project's, or the home's; a path that is no fragment's names none.
#[test]
fn a_fragment_s_path_names_its_resource_under_its_tree() {
    let at = "content/bundles/v2/demo/fragments/example.md";
    assert_eq!(
        fragment_name("projects/calm/checkouts/c/library", at).as_deref(),
        Some("projects/calm/bundles/demo/fragments/example")
    );
    assert_eq!(
        fragment_name("users/me/library", at).as_deref(),
        Some("users/me/bundles/demo/fragments/example")
    );
    assert_eq!(fragment_name("users/me/library", "config.yaml"), None);
    assert_eq!(
        fragment_name("users/me/library", "content/bundles/v2/demo/bundle.yaml"),
        None
    );
    assert_eq!(
        fragment_name(
            "users/me/library",
            "content/bundles/v2/demo/fragments/x.txt"
        ),
        None
    );
    assert_eq!(fragment_name("elsewhere", at), None);
}
