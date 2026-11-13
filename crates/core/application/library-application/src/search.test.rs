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
