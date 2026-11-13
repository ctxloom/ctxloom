//! Ported from Go's `internal/core/bundles` tests (`item_tags_test.go`,
//! `item_ask_test.go`, `listing_order_test.go`, `catalog_ask_test.go`) at
//! 860336c4e, on the corpus of `internal/core/composite/corpus_test.go`.

use super::*;
use crate::distill::{distilled_file, source_digest, DistilledState};

angzarr_presentation::english_for!(&[crate::EN_US.1]);

use crate::testdata::{catalog_of, corpus, ALPHA, BETA};

fn names(infos: &[ContentInfo]) -> Vec<&str> {
    infos.iter().map(|i| i.name.as_str()).collect()
}

fn strings(of: &[&str]) -> Vec<String> {
    of.iter().map(ToString::to_string).collect()
}

// item_tags_test.go: TestListAllFragments_TagsAreEachListedOnce
#[test]
fn test_an_items_tags_are_the_bundles_then_its_own_each_once() {
    let cat = catalog_of(&[(
        "test",
        "version: \"1.0\"\ntags: [testing, tdd]\nfragments:\n  frag:\n    tags: [testing, mutation]\n    content: c\n",
    )]);
    let infos = cat.list_fragments();
    assert_eq!(infos.len(), 1);
    assert_eq!(infos[0].tags, strings(&["testing", "tdd", "mutation"]));
    assert_eq!(
        item_tags(&strings(&["a", "b"]), &strings(&["b", "c", "a"])),
        strings(&["a", "b", "c"])
    );
}

// listing_order_test.go: TestCatalog_ListAll_ItemsListByNameWithinABundle
#[test]
fn test_items_list_by_name_within_a_bundle() {
    let items = "  zulu: {content: z}\n  alpha: {content: a}\n  mike: {content: m}\n  echo: {content: e}\n  kilo: {content: k}\n  bravo: {content: b}\n  yankee: {content: y}\n  charlie: {content: c}\n";
    let cat = catalog_of(&[(
        "order",
        &format!("version: \"1.0\"\nfragments:\n{items}commands:\n{items}"),
    )]);
    let want = [
        "alpha", "bravo", "charlie", "echo", "kilo", "mike", "yankee", "zulu",
    ];
    assert_eq!(names(&cat.list_fragments()), want);
    assert_eq!(names(&cat.list_commands()), want);
}

// catalog.go sortReads: bundles list by name, so a listing is a function of
// the corpus whatever order the entries came in.
#[test]
fn test_bundles_list_by_name_whatever_order_they_arrive_in() {
    let cat = catalog_of(&[("beta", BETA), ("alpha", ALPHA)]);
    let bundles: Vec<&str> = cat.entries().iter().map(|e| e.name.as_str()).collect();
    assert_eq!(bundles, ["alpha", "beta"]);
    let listed: Vec<(String, String)> = cat
        .list_fragments()
        .into_iter()
        .map(|f| (f.bundle, f.name))
        .collect();
    assert_eq!(listed[0], ("alpha".to_string(), "maybe".to_string()));
    assert_eq!(
        listed.last().unwrap(),
        &("beta".to_string(), "tagged".to_string())
    );
}

// catalog.go Lookup: a bundle is its name, ctxloom+local:<name> or the older
// ctxloom:local@bundles/<name>; a miss is "bundle not found: <ask>".
#[test]
fn test_a_bundle_is_found_by_every_spelling_of_its_name() {
    let cat = corpus();
    for ask in [
        "alpha",
        "ctxloom+local:alpha",
        "ctxloom:local@bundles/alpha",
    ] {
        assert_eq!(cat.lookup(ask).unwrap().name, "alpha", "{ask}");
    }
    let err = cat.lookup("nosuch").expect_err("not there");
    assert_eq!(err.english(), "bundle not found: nosuch");
    assert!(matches!(
        cat.lookup("ctxloom+local:nosuch"),
        Err(CatalogError::BundleNotFound { .. })
    ));
}

// item_ask_test.go: TestReadCommand_PromptsAliasReachesTheSameItem
#[test]
fn test_the_prompts_alias_reaches_the_same_command() {
    let cat = catalog_of(&[(
        "kit",
        "version: \"1.0\"\ncommands:\n  deploy:\n    content: run the deploy script\n",
    )]);
    let via_commands = cat.read_command("kit#commands/deploy").unwrap();
    let via_prompts = cat.read_command("kit#prompts/deploy").unwrap();
    assert_eq!(via_commands.len(), 1);
    assert_eq!(via_prompts.len(), 1);
    assert_eq!(via_commands[0].item_ref, via_prompts[0].item_ref);
    assert_eq!(via_commands[0].item_ref, "ctxloom+local:kit#prompts/deploy");
    assert_eq!(via_prompts[0].item, "deploy");
}

// item_ask_test.go: TestReadFragment_CommandSelectorIsRefusedByKindNotByHash
#[test]
fn test_a_command_selector_given_to_a_fragment_read_is_refused_by_kind() {
    let cat = catalog_of(&[(
        "kit",
        "version: \"1.0\"\ncommands:\n  deploy:\n    content: run the deploy script\n",
    )]);
    for ask in ["kit#commands/deploy", "kit#prompts/deploy"] {
        let err = cat.read_fragment(ask).expect_err(ask);
        assert!(
            matches!(err, CatalogError::BadItemRef { .. }),
            "{ask}: {}",
            err.english()
        );
        assert!(
            err.english().contains("selects a prompt, not a fragment"),
            "{}",
            err.english()
        );
    }
}

// bundles.go ReadFragment: a missing fragment names it and its bundle; a bare
// name no bundle has says so.
#[test]
fn test_a_fragment_that_is_not_there_is_named() {
    let cat = corpus();
    let err = cat.read_fragment("alpha#fragments/nope").expect_err("nope");
    assert_eq!(
        err.english(),
        "fragment not found: \"nope\" in bundle \"alpha\""
    );
    let err = cat.read_fragment("nope").expect_err("nope");
    assert_eq!(err.english(), "fragment not found: nope");
    let read = cat.read_fragment("alpha#fragments/rules").unwrap();
    assert_eq!(read[0].name, "alpha/rules");
    assert_eq!(read[0].item_ref, "ctxloom+local:alpha#fragments/rules");
    assert_eq!(read[0].tags, strings(&["house"]));
}

// bundles.go ByTags: the fragments carrying any of the tags, in listing order
#[test]
fn test_by_tags_selects_any_tag_in_listing_order() {
    let cat = corpus();
    assert_eq!(
        names(&cat.by_tags(&strings(&["house"]))),
        ["rules", "tagged"]
    );
    assert!(cat.by_tags(&strings(&["nothing"])).is_empty());
}

// bundles.go ResolveFragmentAsk (select_test.go: TestSelect_ExplicitAndTagAsksFoldIn):
// a bare name resolves to the qualified reference of the first bundle holding
// it; one no bundle holds stays as written.
#[test]
fn test_a_bare_fragment_ask_resolves_to_its_qualified_reference() {
    let cat = corpus();
    assert_eq!(
        cat.resolve_fragment_ask("maybe"),
        "ctxloom+local:alpha#fragments/maybe"
    );
    assert_eq!(cat.resolve_fragment_ask("nowhere"), "nowhere");
    assert_eq!(
        cat.resolve_fragment_ask("beta#fragments/tagged"),
        "ctxloom+local:beta#fragments/tagged"
    );
}

// T30: an inline document's distilled form records no source digest, so it
// is stale and never delivered (hard migrate); its original is.
#[test]
fn test_an_inline_distilled_form_records_no_digest_and_is_never_delivered() {
    let cat = catalog_of(&[(
        "kit",
        "version: \"1.0\"\nfragments:\n  both:\n    content: long\n    distilled: short\n  raw:\n    content: only\n",
    )]);
    let both = &cat.read_fragment("kit#fragments/both").unwrap()[0];
    assert_eq!(both.distilled_state, DistilledState::Stale);
    assert_eq!(both.resolve(true), ("long", false));
    assert_eq!(both.resolve(false), ("long", false));
    assert!(both.stale_for(true));
    assert!(!both.stale_for(false));
    let raw = &cat.read_fragment("kit#fragments/raw").unwrap()[0];
    assert_eq!(raw.distilled_state, DistilledState::None);
    assert_eq!(raw.resolve(true), ("only", false));
    assert!(!raw.stale_for(true));
}

/// A catalog of the one tree `kit`, of `files`.
fn tree_catalog(files: &[(&str, &str)]) -> Catalog {
    let mut all = vec![("bundle.yaml", "version: 1.0.0\n")];
    all.extend_from_slice(files);
    let bundle = crate::tree::read_tree("kit", &crate::testdata::files(&all))
        .unwrap_or_else(|e| panic!("{}", e.english()));
    Catalog::new(vec![Entry {
        name: "kit".to_string(),
        manifest_path: "/app/.ctxloom/content/bundles/v2/kit/bundle.yaml".to_string(),
        bundle,
    }])
}

// T30: a tree's distilled form is delivered, when preferred, only while the
// digest it records is its source's now; a mismatch or none is stale, and
// the original is served.
#[test]
fn test_a_distilled_form_is_delivered_only_while_it_matches_its_source() {
    let fresh = distilled_file(&source_digest(b"long a\n"), "short a");
    let other = distilled_file(&source_digest(b"what it was\n"), "short b");
    let cat = tree_catalog(&[
        ("fragments/a.md", "long a\n"),
        ("fragments/a.distilled.md", &fresh),
        ("fragments/b.md", "long b\n"),
        ("fragments/b.distilled.md", &other),
        ("fragments/c.md", "long c\n"),
        ("fragments/c.distilled.md", "short c\n"),
        ("fragments/d.md", "---\nno_distill: true\n---\nlong d\n"),
        ("fragments/d.distilled.md", "short d\n"),
    ]);
    let read = |name: &str| cat.read_fragment(&format!("kit#fragments/{name}")).unwrap()[0].clone();
    let a = read("a");
    assert_eq!(a.distilled_state, DistilledState::Fresh);
    assert_eq!(a.resolve(true), ("short a\n", true));
    assert_eq!(a.resolve(false), ("long a\n", false));
    assert!(!a.stale_for(true));
    for stale in ["b", "c"] {
        let item = read(stale);
        assert_eq!(item.distilled_state, DistilledState::Stale, "{stale}");
        assert!(!item.resolve(true).1, "{stale}");
        assert!(item.resolve(true).0.starts_with("long"), "{stale}");
        assert!(item.stale_for(true), "{stale}");
        assert!(!item.stale_for(false), "{stale}");
    }
    // An item marked no_distill is served raw and is never stale.
    let d = read("d");
    assert!(!d.resolve(true).1);
    assert!(!d.stale_for(true));
}

// catalog.go Lookup: a name the listing shows wins, then the name a bundle
// declares for itself; two bundles declaring one name are ambiguous.
#[test]
fn test_a_bundle_is_found_by_its_declared_name_and_two_of_them_are_ambiguous() {
    let cat = catalog_of(&[
        ("one", "version: \"1.0\"\nname: shared\n"),
        ("two", "version: \"1.0\"\nname: shared\n"),
        ("three", "version: \"1.0\"\nname: solo\n"),
        ("four", "version: \"1.0\"\n"),
    ]);
    assert_eq!(cat.lookup("solo").unwrap().name, "three");
    assert_eq!(cat.lookup("four").unwrap().name, "four");
    match cat.lookup("shared") {
        Err(CatalogError::Ambiguous { ask, candidates }) => {
            assert_eq!(ask, "shared");
            assert_eq!(candidates, "ctxloom+local:one, ctxloom+local:two");
        }
        other => panic!(
            "expected ambiguity, got {:?}",
            other.map(|e| e.name.clone())
        ),
    }
    for ask in ["", "missing"] {
        assert!(
            matches!(cat.lookup(ask), Err(CatalogError::BundleNotFound { .. })),
            "{ask:?}"
        );
    }
}

// catalog.go Lookup: an ask that only looks like a scheme but is not a
// reference is still a name.
#[test]
fn test_a_listing_name_that_looks_like_a_scheme_is_still_found() {
    let cat = catalog_of(&[("ctxloom+odd", "version: \"1.0\"\n")]);
    assert_eq!(cat.lookup("ctxloom+odd").unwrap().name, "ctxloom+odd");
}

fn origin(reference: &str) -> RemoteOrigin {
    RemoteOrigin {
        reference: reference.to_string(),
        url: "file:///srv/o.git".to_string(),
        sha: "a".repeat(40),
        files: std::collections::BTreeMap::new(),
    }
}

const KIT: &str = "ctxloom+file:///srv/o.git//bundles/kit";
const OTHER: &str = "ctxloom+file:///srv/o.git//bundles/other";

fn installed_catalog() -> Catalog {
    catalog_of(&[
        ("alpha", ALPHA),
        ("origin/kit", BETA),
        ("origin/other", BETA),
    ])
    .with_origins(
        [
            ("origin/kit".to_string(), origin(KIT)),
            ("origin/other".to_string(), origin(OTHER)),
        ]
        .into(),
    )
}

// A bundle installed from a remote is told apart from the project's own.
#[test]
fn test_an_installed_bundle_has_an_origin_and_is_not_local() {
    let cat = installed_catalog();
    assert_eq!(
        cat.origin("origin/kit").map(|o| o.reference.as_str()),
        Some(KIT)
    );
    assert!(cat.origin("alpha").is_none());
    assert!(cat.origin("ghost").is_none());
    assert!(cat.is_local("alpha"));
    assert!(!cat.is_local("origin/kit"));
    assert!(!cat.is_local("ghost"));
    assert!(catalog_of(&[("alpha", ALPHA)]).origin("alpha").is_none());
}

// An installed bundle is found by its canonical reference, whatever version
// the reference asks for, and by no other.
#[test]
fn test_an_installed_bundle_is_found_by_its_canonical_reference() {
    let cat = installed_catalog();
    assert_eq!(cat.lookup(KIT).expect("kit").name, "origin/kit");
    assert_eq!(cat.lookup(OTHER).expect("other").name, "origin/other");
    assert_eq!(
        cat.lookup("ctxloom+file:///srv/o.git//bundles/kit@v1.0.0")
            .expect("kit at a version")
            .name,
        "origin/kit"
    );
    assert!(cat
        .lookup("ctxloom+file:///srv/o.git//bundles/ghost")
        .is_err());
    assert!(catalog_of(&[("alpha", ALPHA)]).lookup(KIT).is_err());
}

// ---------------------------------------------------------------- companions

const LOADOUT_RUN: &str = "version: 1.0.0\nfragments:\n  guide:\n    tags: [house]\n    content: \"Companion guide.\"\n  zeta:\n    premise: \"about to zeta\"\n    content: \"Zeta.\"\n";

fn with_reprise() -> Catalog {
    corpus().with_companions(vec![(
        "reprise".to_string(),
        crate::document::parse_document(LOADOUT_RUN).unwrap(),
    )])
}

#[test]
fn test_a_companion_is_a_bundle_under_its_companion_reference() {
    let cat = with_reprise();
    let entry = cat.lookup("ctxloom+companion:reprise").unwrap();
    assert_eq!(entry.name, "ctxloom+companion:reprise");
    assert_eq!(cat.companion_of(&entry.name), Some("reprise"));
    // A companion no one registered is no bundle.
    assert!(cat.lookup("ctxloom+companion:ltk").is_err());
}

#[test]
fn test_a_project_bundle_is_no_companion() {
    let cat = with_reprise();
    assert_eq!(cat.companion_of("alpha"), None);
    assert_eq!(cat.companion_of("ghost"), None);
    assert_eq!(corpus().companion_of("ctxloom+companion:reprise"), None);
}

#[test]
fn test_the_companion_entries_are_the_companions_only_by_name() {
    let cat = corpus().with_companions(vec![
        (
            "reprise".to_string(),
            crate::document::parse_document(LOADOUT_RUN).unwrap(),
        ),
        (
            "ltk".to_string(),
            crate::document::parse_document(LOADOUT_RUN).unwrap(),
        ),
    ]);
    let names: Vec<(&str, &str)> = cat
        .companion_entries()
        .iter()
        .map(|(name, e)| (*name, e.name.as_str()))
        .collect();
    assert_eq!(
        names,
        [
            ("ltk", "ctxloom+companion:ltk"),
            ("reprise", "ctxloom+companion:reprise")
        ]
    );
    assert!(corpus().companion_entries().is_empty());
}

#[test]
fn test_an_item_is_credited_to_its_companion_or_its_local_bundle() {
    let cat = with_reprise();
    let companion = cat.lookup("ctxloom+companion:reprise").unwrap();
    assert_eq!(cat.source_of(companion), "ctxloom+companion:reprise");
    let alpha = cat.lookup("alpha").unwrap();
    assert_eq!(cat.source_of(alpha), "ctxloom+local:alpha");
}

#[test]
fn test_a_companions_fragment_is_read_under_its_companion_reference() {
    let cat = with_reprise();
    let read = cat
        .read_fragment("ctxloom+companion:reprise#fragments/guide")
        .unwrap();
    assert_eq!(read.len(), 1);
    assert_eq!(
        read[0].item_ref,
        "ctxloom+companion:reprise#fragments/guide"
    );
    assert_eq!(read[0].content, "Companion guide.");
    assert_eq!(read[0].tags, strings(&["house"]));
    // A project bundle's item keeps its local reference.
    let alpha = cat.read_fragment("alpha#fragments/rules").unwrap();
    assert_eq!(alpha[0].item_ref, "ctxloom+local:alpha#fragments/rules");
}

#[test]
fn test_the_companions_asks_follow_the_reference_then_the_fragment_name() {
    let mut cat = corpus().with_companions(vec![
        (
            "reprise".to_string(),
            crate::document::parse_document(LOADOUT_RUN).unwrap(),
        ),
        (
            "ltk".to_string(),
            crate::document::parse_document(
                "version: 1\nfragments:\n  b: {content: b}\n  a: {content: a}\n",
            )
            .unwrap(),
        ),
    ]);
    let asks: Vec<String> = cat.companion_fragments();
    assert_eq!(
        asks,
        [
            "ctxloom+companion:ltk#fragments/a",
            "ctxloom+companion:ltk#fragments/b",
            "ctxloom+companion:reprise#fragments/guide",
            "ctxloom+companion:reprise#fragments/zeta",
        ]
    );
    assert!(corpus().companion_fragments().is_empty());
    cat = corpus();
    assert!(cat.companion_fragments().is_empty());
}

// Owner rulings 110, 111 (narrowed by 117): ctxloom's built-in bundle joins
// the catalog beside the project's, under its own name, and a local name
// reaches it.
#[test]
fn test_the_built_in_bundle_joins_the_catalog_and_a_local_name_finds_it() {
    let cat = corpus().with_builtins(&|_| false);
    let listed: Vec<&str> = cat.entries().iter().map(|e| e.name.as_str()).collect();
    assert_eq!(listed, ["alpha", "beta", "ctxloom"]);
    assert!(cat.is_builtin("ctxloom"));
    assert!(cat.origin("ctxloom").is_none(), "it is no remote's");
    assert!(!cat.is_builtin("alpha"));
    assert_eq!(
        cat.lookup("ctxloom+local:ctxloom").map(|e| e.name.as_str()),
        Ok("ctxloom")
    );
    let read = cat
        .read_command("ctxloom#prompts/setup-interview")
        .expect("a built-in prompt");
    assert_eq!(
        read[0].item_ref,
        "ctxloom+local:ctxloom#prompts/setup-interview"
    );
    assert_eq!(read[0].content, crate::builtin::SETUP_INTERVIEW);
}

// A project bundle of the built-in one's name overrides it: one entry of the
// name, the project's, and it is not built in.
#[test]
fn test_a_project_bundle_of_the_built_in_name_overrides_it() {
    let cat = catalog_of(&[(
        "ctxloom",
        "version: 9.9.9\nfragments:\n  ours:\n    content: Our own rules.\n",
    )])
    .with_builtins(&|_| false);
    let named: Vec<&Entry> = cat
        .entries()
        .iter()
        .filter(|e| e.name == "ctxloom")
        .collect();
    assert_eq!(named.len(), 1);
    assert!(!cat.is_builtin("ctxloom"));
    let found = cat.lookup("ctxloom").expect("the project's");
    assert_eq!(found.bundle.version, "9.9.9");
    assert!(cat.read_command("ctxloom#prompts/setup-interview").is_err());
}

// One that is shadowed (a project bundle there that cannot be read) is not
// added either.
#[test]
fn test_a_shadowed_built_in_name_is_not_added() {
    let cat = corpus().with_builtins(&|name| name == "ctxloom");
    assert!(!cat.is_builtin("ctxloom"));
    assert!(cat.lookup("ctxloom").is_err());
}

// A reference that selects an item (`#<kind>/<name>`) names no bundle:
// `lookup` refuses it in every spelling (it once dropped the selector of a
// canonical reference and answered the whole bundle, the trap behind handtest
// D1), saying it is an item reference; `lookup_item` is its own path.
#[test]
fn test_lookup_refuses_an_item_reference_in_every_spelling() {
    let cat = installed_catalog();
    for ask in [
        "alpha#fragments/rules",
        "ctxloom+local:alpha#fragments/rules",
        "ctxloom+file:///srv/o.git//bundles/kit#fragments/x",
        "ctxloom+file:///srv/o.git//bundles/kit@v1.0.0#mcp/db",
    ] {
        let err = cat.lookup(ask).expect_err(ask);
        assert_eq!(
            angzarr_diagnostic::Diagnostic::code(&err),
            "library-catalog-item-not-bundle",
            "{ask}"
        );
    }
}

// An item reference's own path: the bundle it reaches, its kind and name, in
// every spelling; a reference that selects nothing is no item reference.
#[test]
fn test_lookup_item_reaches_the_items_bundle_kind_and_name() {
    let cat = installed_catalog();
    for (ask, bundle) in [
        ("alpha#fragments/rules", "alpha"),
        ("ctxloom+local:alpha#fragments/rules", "alpha"),
        (
            "ctxloom+file:///srv/o.git//bundles/kit@v1.0.0#fragments/rules",
            "origin/kit",
        ),
    ] {
        let (entry, kind, name) = cat.lookup_item(ask).expect(ask);
        assert_eq!(
            (entry.name.as_str(), kind, name.as_str()),
            (bundle, ItemKind::Fragment, "rules"),
            "{ask}"
        );
    }
    let (_, kind, name) = cat.lookup_item("alpha#mcp/db").unwrap();
    assert_eq!((kind, name.as_str()), (ItemKind::Mcp, "db"));
    for ask in ["alpha", "alpha#widgets/x"] {
        assert!(
            matches!(cat.lookup_item(ask), Err(CatalogError::ItemSyntax { .. })),
            "{ask}"
        );
    }
    assert!(matches!(
        cat.lookup_item("ghost#fragments/x"),
        Err(CatalogError::BundleNotFound { .. })
    ));
}
