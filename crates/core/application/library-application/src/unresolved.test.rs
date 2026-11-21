//! Doctor's agent check (smoke 2026-11-19): each agent resolved as its run
//! would plan it, and what would refuse that run named.

use crate::fakes::{plain, Setup, ENVELOPE};
use crate::Library;

const HOUSE: &[(&str, &str)] = &[ENVELOPE, ("fragments/guide.md", "GUIDE")];

const PROJECT: &[(&str, &str)] = &[
    ENVELOPE,
    ("profiles/team.yaml", "bundles: [house]\n"),
    ("profiles/dangling.yaml", "bundles: [ghost]\n"),
];

fn world(config: &'static str) -> Library {
    let (library, _) = Setup {
        bundles: &[("house", HOUSE), ("project", PROJECT)],
        layers: &[config],
        ..Setup::default()
    }
    .fixture();
    library
}

#[test]
fn test_an_agent_whose_profile_includes_a_missing_bundle_is_named_with_it() {
    let library = world(
        "schema_version: 9\nagents:\n  dev:\n    engine: claude-code\n    profiles: [team]\n  broken:\n    engine: claude-code\n    profiles: [dangling]\n",
    );
    let found = library.agents_unresolved(&plain()).result.unwrap();
    assert_eq!(found.len(), 1, "{found:?}");
    assert_eq!(found[0].agent, "broken");
    assert_eq!(found[0].problems.len(), 1, "{found:?}");
    assert!(
        found[0].problems[0].contains("bundle not found: ghost"),
        "{found:?}"
    );
    assert!(found[0].fix.contains("deps pull"), "{found:?}");
}

#[test]
fn test_an_agent_naming_a_profile_nobody_has_is_named() {
    let library = world(
        "schema_version: 9\nagents:\n  lost:\n    engine: claude-code\n    profiles: [nowhere]\n",
    );
    let found = library.agents_unresolved(&plain()).result.unwrap();
    assert_eq!(found.len(), 1, "{found:?}");
    assert_eq!(found[0].agent, "lost");
    assert!(found[0].problems[0].contains("nowhere"), "{found:?}");
}

#[test]
fn test_agents_that_resolve_are_not_named() {
    let library = world(
        "schema_version: 9\nagents:\n  dev:\n    engine: claude-code\n    profiles: [team]\n",
    );
    let found = library.agents_unresolved(&plain()).result.unwrap();
    assert!(found.is_empty(), "{found:?}");
}
