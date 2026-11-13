//! The catalog's resource names (OD-40, B1).

use super::*;

const P: &str = "projects/calm-otter";

// OQ-4: a bundle's origin is in its name.
#[test]
fn test_a_bundles_name_says_its_origin() {
    assert_eq!(bundle_name(P, "demo"), "projects/calm-otter/bundles/demo");
    assert_eq!(
        bundle_name(P, "ctxloom+local:demo"),
        "projects/calm-otter/bundles/demo"
    );
    assert_eq!(
        bundle_name(P, "acme/rules"),
        "projects/calm-otter/remotes/acme/bundles/rules"
    );
    assert_eq!(
        bundle_name(P, "ctxloom+companion:taskloom"),
        "projects/calm-otter/companions/taskloom/bundles/loadout"
    );
    assert_eq!(
        bundle_name("users/me", "ctxloom"),
        "users/me/bundles/ctxloom"
    );
    assert_eq!(origin("demo", false), v1::bundle::Origin::Project);
    assert_eq!(origin("ctxloom", true), v1::bundle::Origin::Builtin);
    assert_eq!(origin("acme/rules", false), v1::bundle::Origin::Remote);
    assert_eq!(
        origin("ctxloom+companion:taskloom", false),
        v1::bundle::Origin::Companion
    );
}

// An item's name is below its bundle's; `prompts` is `commands`, `mcp`
// `mcpServers`.
#[test]
fn test_an_items_name_is_below_its_bundles() {
    assert_eq!(
        reference_name(P, "demo#fragments/x").as_deref(),
        Some("projects/calm-otter/bundles/demo/fragments/x")
    );
    assert_eq!(
        reference_name(P, "demo#prompts/review").as_deref(),
        Some("projects/calm-otter/bundles/demo/commands/review")
    );
    assert_eq!(
        reference_name(P, "ctxloom+companion:acme#fragments/f").as_deref(),
        Some("projects/calm-otter/companions/acme/bundles/loadout/fragments/f")
    );
    assert_eq!(
        item_name(P, "demo", "mcp", "tree-sitter"),
        "projects/calm-otter/bundles/demo/mcpServers/tree-sitter"
    );
    assert_eq!(reference_name(P, "demo"), None);
    assert_eq!(reference_name(P, "demo#widgets/x"), None);
    assert_eq!(reference_name(P, "demo#fragments/a/b"), None);
    assert_eq!(
        reference_name(P, "https://github.com/acme/x@bundles/y#profiles/p"),
        None
    );
}

// A configured reference is a resource name where its text says which, and
// keeps its text where it does not (OQ-16).
#[test]
fn test_a_reference_is_a_name_where_its_text_places_it() {
    assert_eq!(
        profile_ref(P, "base"),
        "projects/calm-otter/bundles/project/profiles/base"
    );
    assert_eq!(
        profile_ref(P, "vendor/kit#profiles/reach"),
        "projects/calm-otter/remotes/vendor/bundles/kit/profiles/reach"
    );
    assert_eq!(profile_ref(P, "acme/base"), "acme/base");
    assert_eq!(bundle_ref(P, "demo"), "projects/calm-otter/bundles/demo");
    assert_eq!(
        bundle_ref(P, "team/runbook"),
        "projects/calm-otter/remotes/team/bundles/runbook"
    );
    assert_eq!(
        bundle_ref(P, "demo#fragments/x"),
        "projects/calm-otter/bundles/demo/fragments/x"
    );
    let url = "https://github.com/acme/x@bundles/y";
    assert_eq!(bundle_ref(P, url), url);
    assert_eq!(distilled(true), v1::DistilledState::Fresh as i32);
    assert_eq!(distilled(false), v1::DistilledState::None as i32);
}

// S51, T30: a distilled form's state is the API's, and `bundle show`'s stale
// one is STALE whatever else it says.
#[test]
fn test_a_distilled_form_s_state_is_the_api_s() {
    use library_application::DistilledState;
    assert_eq!(
        distilled_state(DistilledState::None),
        v1::DistilledState::None as i32
    );
    assert_eq!(
        distilled_state(DistilledState::Fresh),
        v1::DistilledState::Fresh as i32
    );
    assert_eq!(
        distilled_state(DistilledState::Stale),
        v1::DistilledState::Stale as i32
    );
    assert_eq!(
        shown_distilled(true, true),
        v1::DistilledState::Stale as i32
    );
    assert_eq!(
        shown_distilled(true, false),
        v1::DistilledState::Fresh as i32
    );
    assert_eq!(
        shown_distilled(false, false),
        v1::DistilledState::None as i32
    );
}
