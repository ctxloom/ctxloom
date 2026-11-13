//! Where a search's hits, and an assembly's profiles and fragments, are in
//! the catalog (OD-40): what a driving adapter names their resources by.

use crate::fakes::{plain, Setup, ENVELOPE};
use crate::{AssembleRequest, Assembled, Library, Located, SearchRequest, UsedFragment};

const DEMO: &[(&str, &str)] = &[
    ENVELOPE,
    ("fragments/golang.md", "---\ntags: [go]\n---\nGO-RULES\n"),
    ("fragments/dist.md", "long form\n"),
    (
        "fragments/dist.distilled.md",
        // The source's digest (T30): the form is fresh.
        "---\nsource_sha256: 83c7e86d47059f22626abe2311dc3ee5759867bc3dbe0bde68824e09cae5a5eb\n---\nshort form\n",
    ),
    ("prompts/review.md", "REVIEW-BODY\n"),
    (
        "skills/reviewer/SKILL.md",
        "---\nname: reviewer\ndescription: Reviews code\n---\nSKILL-BODY\n",
    ),
    ("mcp/tree-sitter.yaml", "command: tree-sitter\n"),
];

const PROJECT: &[(&str, &str)] = &[ENVELOPE, ("profiles/dev.yaml", "bundles: [demo]\n")];

const AGENTS: &str = "schema_version: 9\nagents:\n  dev:\n    engine: claude-code\n    profiles: [dev]\ndefault_agent: dev\n";

fn library() -> Library {
    Setup {
        bundles: &[("demo", DEMO), ("project", PROJECT)],
        layers: &[AGENTS],
        ..Setup::default()
    }
    .library()
}

fn at(bundle: &str, item: &str) -> Option<Located> {
    Some(Located {
        bundle: bundle.to_string(),
        item: item.to_string(),
    })
}

/// Each local hit of `query` narrowed to `kind`, with where it is.
fn located(kind: &str, query: &str) -> Vec<(String, Option<Located>)> {
    let request = SearchRequest {
        query: query.to_string(),
        kind: kind.to_string(),
        local: true,
        ..SearchRequest::default()
    };
    library()
        .search(&plain(), &request)
        .result
        .expect("results")
        .local
        .into_iter()
        .map(|hit| (hit.name, hit.located))
        .collect()
}

// Each kind is located by its bundle's catalog name and its own name: a
// skill without its row's `<bundle>/` prefix, a project profile in the bundle
// `project`, an MCP server in the bundle that ships it.
#[test]
fn test_each_local_hit_is_located_in_its_bundle() {
    assert_eq!(
        located("fragment", "golang"),
        [("golang".to_string(), at("demo", "golang"))]
    );
    assert_eq!(
        located("command", "review"),
        [("review".to_string(), at("demo", "review"))]
    );
    assert_eq!(
        located("skill", "reviewer"),
        [("demo/reviewer".to_string(), at("demo", "reviewer"))]
    );
    assert_eq!(
        located("profile", "dev"),
        [("dev".to_string(), at("project", "dev"))]
    );
    assert_eq!(
        located("mcp_server", "tree"),
        [("tree-sitter".to_string(), at("demo", "tree-sitter"))]
    );
}

fn assembled(request: AssembleRequest) -> Assembled {
    library()
        .assemble(&plain(), &request)
        .result
        .expect("assembled")
}

// An assembly's profiles are located by the key each ask resolved to, each
// fragment it used by the reference it loaded by, with the form it used; and
// its token estimate is the context's.
#[test]
fn test_an_assembly_locates_its_profiles_and_fragments_and_their_forms() {
    let by_profile = assembled(AssembleRequest {
        profiles: vec!["dev".to_string()],
        ..AssembleRequest::default()
    });
    assert_eq!(by_profile.profiles_located, [at("project", "dev")]);
    let named = assembled(AssembleRequest {
        fragments: vec![
            "demo#fragments/golang".to_string(),
            "demo#fragments/dist".to_string(),
        ],
        ..AssembleRequest::default()
    });
    assert_eq!(
        named.used,
        [
            UsedFragment {
                located: at("demo", "golang"),
                distilled: false,
            },
            UsedFragment {
                located: at("demo", "dist"),
                distilled: true,
            },
        ]
    );
    assert_eq!(
        named.tokens,
        library::tokens::estimate(&named.context) as u64
    );
    assert!(named.tokens > 0);
}

// A reference is located by its bundle as the catalog names it, however it
// is spelled; a companion's by its own reference; one whose bundle the
// catalog does not hold, or that names no item, nowhere.
#[test]
fn test_a_reference_is_located_by_the_catalog_or_nowhere() {
    let found = library()
        .run(&plain(), |p| {
            Ok([
                "ctxloom+local:demo#fragments/golang",
                "demo#fragments/golang",
                "ctxloom+companion:reprise#fragments/guide",
                "nosuch#fragments/golang",
                "demo",
                "demo#fragments",
            ]
            .map(|reference| p.locate(reference)))
        })
        .result
        .expect("located");
    assert_eq!(
        found,
        [
            at("demo", "golang"),
            at("demo", "golang"),
            at("ctxloom+companion:reprise", "guide"),
            None,
            None,
            None,
        ]
    );
}

/// The library over `demo` with `files` added to it (a file of the same
/// path replaced).
fn library_with(files: &[(&str, &str)]) -> Library {
    let mut demo: Vec<(&str, &str)> = DEMO
        .iter()
        .filter(|(path, _)| !files.iter().any(|(p, _)| p == path))
        .copied()
        .collect();
    demo.extend_from_slice(files);
    Setup {
        bundles: &[("demo", &demo), ("project", PROJECT)],
        layers: &[AGENTS],
        ..Setup::default()
    }
    .library()
}

/// The notices, each as `<kind> <reference>` if it is the stale-distilled
/// one, and its code otherwise.
fn said(notices: &[crate::Notice]) -> Vec<String> {
    use angzarr_diagnostic::Diagnostic;
    notices
        .iter()
        .map(|n| match n {
            crate::Notice::StaleDistilled { kind, reference } => format!("{kind} {reference}"),
            other => other.code().to_string(),
        })
        .collect()
}

fn stale_notice(reference: &str) -> String {
    format!("fragment {reference}")
}

// atomic_writes.feature, "A distilled form is used only while it matches its
// source" (T30, S51): a distilled form whose source changed since is not
// used, its original is, and the assembly says it is stale; a fresh one is
// used and nothing is said.
#[test]
fn test_an_assembly_uses_a_stale_distilled_form_s_original_and_says_so() {
    let ask = AssembleRequest {
        fragments: vec!["demo#fragments/dist".to_string()],
        ..AssembleRequest::default()
    };
    let fresh = library().assemble(&plain(), &ask);
    assert_eq!(fresh.result.as_ref().unwrap().context.trim(), "short form");
    assert!(fresh.notices.is_empty(), "{:?}", fresh.notices);
    let edited = library_with(&[("fragments/dist.md", "long form, edited by hand\n")]);
    let stale = edited.assemble(&plain(), &ask);
    let assembled = stale.result.as_ref().unwrap();
    assert_eq!(assembled.context.trim(), "long form, edited by hand");
    assert!(!assembled.used[0].distilled);
    let reference = assembled.fragments_loaded[0].clone();
    assert_eq!(said(&stale.notices), [stale_notice(&reference)]);
    // A form that records no digest is stale too.
    let undigested = library_with(&[("fragments/dist.distilled.md", "short form\n")]);
    let none = undigested.assemble(&plain(), &ask);
    assert_eq!(none.result.unwrap().context.trim(), "long form");
    assert_eq!(said(&none.notices), [stale_notice(&reference)]);
}

// A dry run says it as the assembly does, and preferring the original says
// nothing of a stale form, since none was going to be used.
#[test]
fn test_a_dry_run_says_a_stale_distilled_form_and_the_original_preferred_says_nothing() {
    let edited = || library_with(&[("fragments/dist.md", "long form, edited by hand\n")]);
    let request = crate::RunRequest {
        fragments: vec!["demo#fragments/dist".to_string()],
        ..crate::RunRequest::default()
    };
    let planned = edited().dry_run(&plain(), &request);
    let plan = &planned.result.as_ref().unwrap().plan;
    assert!(plan.context.contains("edited by hand"), "{}", plan.context);
    assert!(
        planned
            .notices
            .iter()
            .any(|n| matches!(n, crate::Notice::StaleDistilled { .. })),
        "{:?}",
        planned.notices
    );
    let original = crate::Flags {
        config_set: vec!["config.content_form=original".to_string()],
        ..plain()
    };
    let quiet = edited().assemble(
        &original,
        &AssembleRequest {
            fragments: vec!["demo#fragments/dist".to_string()],
            ..AssembleRequest::default()
        },
    );
    assert!(quiet.result.is_ok());
    assert!(quiet.notices.is_empty(), "{:?}", quiet.notices);
}

// S51: `fragment show` judges the distilled form now: fresh is served when
// asked for, stale is not (its original is, and it is said), and none is
// none; doctor's listing names each stale form by its reference.
#[test]
fn test_show_and_the_stale_listing_judge_the_distilled_form_now() {
    use library::distill::DistilledState;
    let show = |library: &Library, distilled: bool| {
        library.item_show(
            &plain(),
            crate::ItemKind::Fragment,
            "demo#fragments/dist",
            distilled,
        )
    };
    let fresh = show(&library(), true);
    let shown = fresh.result.unwrap();
    assert_eq!(shown.state, DistilledState::Fresh);
    assert!(shown.distilled);
    assert_eq!(shown.content.trim(), "short form");
    assert!(fresh.notices.is_empty());
    let edited = library_with(&[("fragments/dist.md", "edited\n")]);
    let stale = show(&edited, true);
    let shown = stale.result.unwrap();
    assert_eq!(shown.state, DistilledState::Stale);
    assert!(!shown.distilled);
    assert_eq!(shown.content.trim(), "edited");
    assert_eq!(said(&stale.notices), ["fragment demo#fragments/dist"]);
    // Not asked for the distilled form: its state is told, nothing said.
    let plain_show = show(&edited, false);
    assert_eq!(plain_show.result.unwrap().state, DistilledState::Stale);
    assert!(plain_show.notices.is_empty());
    let none = library().item_show(
        &plain(),
        crate::ItemKind::Fragment,
        "demo#fragments/golang",
        true,
    );
    assert_eq!(none.result.unwrap().state, DistilledState::None);
    assert!(library()
        .stale_distilled(&plain())
        .result
        .unwrap()
        .is_empty());
    let marked = library_with(&[
        ("fragments/dist.md", "edited\n"),
        ("prompts/review.md", "---\nno_distill: true\n---\nREVIEW\n"),
        ("prompts/review.distilled.md", "old\n"),
        ("prompts/other.md", "OTHER\n"),
        ("prompts/other.distilled.md", "old\n"),
    ]);
    assert_eq!(
        marked.stale_distilled(&plain()).result.unwrap(),
        ["demo#fragments/dist", "demo#commands/other"]
    );
}

// bundle show and bundle view: a stale form is said stale, and never viewed
// as the distilled form.
#[test]
fn test_bundle_show_says_a_stale_form_and_view_serves_its_original() {
    let edited = library_with(&[("fragments/dist.md", "edited\n")]);
    let shown = edited.bundle_show(&plain(), "demo").result.unwrap();
    assert!(shown.fragments["dist"].stale);
    assert!(!shown.fragments["golang"].stale);
    assert!(
        !library()
            .bundle_show(&plain(), "demo")
            .result
            .unwrap()
            .fragments["dist"]
            .stale
    );
    let viewed = edited
        .bundle_view(&plain(), "demo#fragments/dist", true)
        .result
        .unwrap();
    assert_eq!(viewed.content, "edited\n");
    let fresh = library()
        .bundle_view(&plain(), "demo#fragments/dist", true)
        .result
        .unwrap();
    assert_eq!(fresh.content, "# (distilled version)\nshort form\n");
}
