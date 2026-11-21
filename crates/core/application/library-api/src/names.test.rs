//! The library's resource names (OD-40, B1).

use super::*;
use daemon_proto::v1;

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
}

// A reference whose bundle is empty names nothing, whatever its path; a
// bundle text that is empty, or carries a version (`@`), or another scheme
// (`host:x`) keeps its text; a companion's loadout is named.
#[test]
fn test_a_reference_that_names_no_bundle_keeps_its_text() {
    assert_eq!(reference_name(P, "#fragments/x"), None);
    assert_eq!(bundle_ref(P, "#fragments/x"), "#fragments/x");
    assert_eq!(bundle_ref(P, ""), "");
    assert_eq!(bundle_ref(P, "kit@v1"), "kit@v1");
    assert_eq!(bundle_ref(P, "host:kit"), "host:kit");
    assert_eq!(
        bundle_ref(P, "ctxloom+companion:taskloom"),
        "projects/calm-otter/companions/taskloom/bundles/loadout"
    );
    assert_eq!(
        bundle_ref(P, "ctxloom+local:demo"),
        "projects/calm-otter/bundles/demo"
    );
}

// A bundle's resource name reads back as the catalog name it was made of.
#[test]
fn a_bundle_s_name_reads_back_as_its_catalog_name() {
    for catalog in ["demo", "acme/rules", "ctxloom+companion:acme-tool"] {
        let name = super::bundle_name("projects/p", catalog);
        assert_eq!(super::catalog_name("projects/p", &name), catalog, "{name}");
    }
    assert_eq!(super::catalog_name("users/me", "users/me/bundles/x"), "x");
    assert_eq!(
        super::catalog_name("projects/p", "other/thing"),
        "other/thing"
    );
    assert_eq!(
        super::catalog_name("projects/p", "projects/p/odd/x"),
        "projects/p/odd/x"
    );
}

// Step 10a U2: a command names what it reads by its text as typed, and the
// daemon reads that text back exactly, so its refusals say what was typed.
#[test]
fn every_bundle_text_a_command_takes_reads_back_as_typed() {
    let parent = "projects/calm-otter";
    for (text, target) in [
        ("demo", "/bundles/demo"),
        ("vendor/kit", "/remotes/vendor/bundles/kit"),
        ("ctxloom+companion:acme", "/companions/acme/bundles/loadout"),
        ("ctxloom+local:demo", "/bundles/ctxloom+local:demo"),
        (
            "demo#fragments/testing",
            "/bundles/demo#fragments%2Ftesting",
        ),
        (
            "github.com/o/r.git/kit",
            "/bundles/github.com%2Fo%2Fr.git%2Fkit",
        ),
        ("50%/x", "/bundles/50%25%2Fx"),
    ] {
        assert_eq!(super::bundle_target(text), target, "{text}");
        let name = format!("{parent}{target}");
        assert_eq!(
            super::bundle_under(parent, &name).as_deref(),
            Some(text),
            "{name}"
        );
    }
    assert_eq!(super::bundle_under(parent, "users/me/bundles/demo"), None);
    assert_eq!(super::bundle_under(parent, parent), None);
}

#[test]
fn every_item_reference_a_command_takes_reads_back_as_typed() {
    let parent = "users/me";
    for (reference, target) in [
        ("demo#fragments/testing", "/bundles/demo/fragments/testing"),
        (
            "ctxloom#prompts/distill",
            "/bundles/ctxloom/prompts/distill",
        ),
        (
            "vendor/kit#profiles/reach",
            "/remotes/vendor/bundles/kit/profiles/reach",
        ),
        ("not-a-real-reference", "/bundles/not-a-real-reference"),
        ("demo#fragments", "/bundles/demo/-/fragments"),
        ("demo#a/b/c", "/bundles/demo/-/a%2Fb%2Fc"),
        ("demo#-/x", "/bundles/demo/-/-%2Fx"),
        ("demo#", "/bundles/demo/-/"),
        (
            "ctxloom+companion:acme#fragments/rules",
            "/companions/acme/bundles/loadout/fragments/rules",
        ),
    ] {
        assert_eq!(super::reference_target(reference), target, "{reference}");
        let name = format!("{parent}{target}");
        assert_eq!(
            super::reference_under(parent, &name).as_deref(),
            Some(reference),
            "{name}"
        );
    }
}

#[test]
fn every_profile_text_a_command_takes_reads_back_as_typed() {
    let parent = "projects/calm-otter";
    for (text, target) in [
        ("dev", "/profiles/dev"),
        ("a/b", "/profiles/a%2Fb"),
        ("50%", "/profiles/50%25"),
        ("", "/profiles/"),
        ("demo#profiles/x", "/bundles/demo/profiles/x"),
        (
            "ctxloom+local:project#profiles/dev",
            "/bundles/ctxloom+local:project/profiles/dev",
        ),
        (
            "vendor/kit#profiles/reach",
            "/remotes/vendor/bundles/kit/profiles/reach",
        ),
    ] {
        assert_eq!(super::profile_target(text), target, "{text}");
        let name = format!("{parent}{target}");
        assert_eq!(
            super::profile_under(parent, &name).as_deref(),
            Some(text),
            "{name}"
        );
    }
    assert_eq!(super::profile_under(parent, "users/me/profiles/x"), None);
}

#[test]
fn a_named_resource_text_reads_back_as_typed() {
    let parent = "users/me";
    for (text, target) in [
        ("dev", "/agents/dev"),
        ("a/b", "/agents/a%2Fb"),
        ("", "/agents/"),
    ] {
        assert_eq!(super::named_target("agents", text), target, "{text}");
        let name = format!("{parent}{target}");
        assert_eq!(
            super::named_under(parent, "agents", &name).as_deref(),
            Some(text)
        );
    }
    assert_eq!(
        super::named_under(parent, "agents", "users/me/engines/x"),
        None
    );
}

#[test]
fn test_a_resource_name_reads_as_its_short_reference() {
    // Owner ruling 2026-11-17 (U2): text prints the short, harp-free
    // reference a person types; json keeps the resource name.
    for (name, short) in [
        ("projects/able-baker/bundles/demo", "demo"),
        ("users/me/bundles/demo", "demo"),
        (
            "projects/able-baker/remotes/vendor/bundles/kit",
            "vendor/kit",
        ),
        (
            "projects/able-baker/companions/tl/bundles/loadout",
            "ctxloom+companion:tl",
        ),
        (
            "projects/able-baker/bundles/project/profiles/dev",
            "project#profiles/dev",
        ),
        (
            "projects/able-baker/remotes/vendor/bundles/kit/profiles/reach",
            "vendor/kit#profiles/reach",
        ),
        (
            "projects/able-baker/bundles/demo/fragments/style",
            "demo#fragments/style",
        ),
        (
            "projects/able-baker/bundles/demo/commands/review",
            "demo#commands/review",
        ),
        (
            "projects/able-baker/bundles/demo/mcpServers/tree-sitter",
            "demo#mcp/tree-sitter",
        ),
        ("projects/able-baker/bundles/demo/skills/s", "demo#skills/s"),
        // A reference that names no place keeps its text (OQ-16).
        ("https://example.com/x.git", "https://example.com/x.git"),
        ("dev", "dev"),
        (
            "projects/able-baker/engines/claude-code",
            "projects/able-baker/engines/claude-code",
        ),
    ] {
        assert_eq!(super::short_reference(name), short, "{name}");
    }
}

#[test]
fn a_skill_reference_names_its_skill() {
    assert_eq!(
        super::reference_name("projects/p", "demo#skills/reviewer").as_deref(),
        Some("projects/p/bundles/demo/skills/reviewer")
    );
}
