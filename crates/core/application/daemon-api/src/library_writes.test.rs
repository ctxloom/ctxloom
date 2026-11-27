//! A write's landing among the trees observed, and its command id.

use library_api::writes::{Lands, Scope};
use library_api::{Observed, Site};
use library_tree::TreeName;
use sessions::ProjectName;

use super::{answered_before, export_source, landing_of, request_bytes, step_path};

fn observed(tree: TreeName) -> Observed {
    Observed {
        tree,
        notices: Vec::new(),
    }
}

fn seen(with_project: bool) -> Vec<(Site, Observed)> {
    let project = ProjectName::parse("projects/amber-quiet-heron").unwrap();
    let mut seen = Vec::new();
    if with_project {
        seen.push((
            Site::checkout(project.clone(), "/work/demo"),
            observed(TreeName::checkout(project, "brisk-copper-moth").unwrap()),
        ));
    }
    seen.push((
        Site::Home {
            tree: "/home/alice/.ctxloom".to_string(),
        },
        observed(TreeName::home("calm-otter-run").unwrap()),
    ));
    seen
}

#[test]
fn test_content_lands_in_the_checkout_else_the_home_and_a_layer_in_its_scope() {
    let both = seen(true);
    let at = |lands| landing_of(&both, lands).map(|l| l.site.tree().to_string());
    assert_eq!(at(Lands::Content).as_deref(), Some("/work/demo/.ctxloom"));
    assert_eq!(
        at(Lands::Layer(Scope::Project)).as_deref(),
        Some("/work/demo/.ctxloom")
    );
    assert_eq!(
        at(Lands::Layer(Scope::Home)).as_deref(),
        Some("/home/alice/.ctxloom")
    );
    let home = seen(false);
    assert_eq!(
        landing_of(&home, Lands::Content).map(|l| l.site.tree().to_string()),
        Some("/home/alice/.ctxloom".to_string())
    );
    assert!(landing_of(&home, Lands::Layer(Scope::Project)).is_none());
}

#[test]
fn test_the_command_id_is_the_requests_own_uuid() {
    let id = "0190a4c2-7d1e-7c3a-9b5f-0123456789ab";
    assert_eq!(
        uuid::Uuid::from_bytes(request_bytes(id)).to_string(),
        id,
        "a retry under the same id is a duplicate"
    );
    assert_ne!(request_bytes("not a uuid"), request_bytes("not a uuid"));
}

#[test]
fn test_a_steps_path_is_its_kinds_own() {
    let step = library_tree::Step {
        replace: None,
        create: None,
        trash: Some(library_tree::TrashPath {
            name: String::new(),
            verb: String::new(),
            path: "content/bundles/v2/demo".to_string(),
            seen_sequence: 0,
            evidence: library_tree::Evidence {
                tree_reads: true,
                plan_rejection: None,
                builtin: false,
            },
            keep: true,
        }),
        r#move: None,
        purge: None,
    };
    assert_eq!(step_path(&step), "content/bundles/v2/demo");
    let purge = library_tree::Step {
        trash: None,
        purge: Some(library_tree::PurgeSlot {
            request_id: "r-0".to_string(),
        }),
        ..step
    };
    assert_eq!(step_path(&purge), "state/trash/r-0");
}

// E1, extended to the writes: a write's directory is the one its caller
// carries, set and read back through the request it is, as its environment
// is (owner, 2026-11-18: one `Caller`).
#[test]
fn test_a_writes_directory_is_its_requests_own() {
    use daemon_proto::{Carries as _, CarriesMut as _};
    let mut request = daemon_proto::v1::UpdateConfigRequest::default();
    assert_eq!(request.caller_cwd(), "");
    request.set_caller_cwd("/work/demo".to_string());
    assert_eq!(request.caller_cwd(), "/work/demo");
    assert_eq!(request.caller.as_ref().unwrap().cwd, "/work/demo");
}

// Step 10b U3 (G4 superseded): a write is planned against the etag its
// caller's preview answered, the path's sequence then, so `decide` judges
// it stale when the path has moved since; one call that plans and applies
// (`--yes`) is planned against the path as it is now.
#[test]
fn test_a_writes_seen_sequence_is_its_callers_etag_else_the_paths_now() {
    use super::seen_sequence;
    assert_eq!(seen_sequence("", 9).unwrap(), 9);
    assert_eq!(seen_sequence("4", 9).unwrap(), 4);
    assert_eq!(seen_sequence("0", 9).unwrap(), 0);
    assert!(seen_sequence("abc", 9).is_err());
}

// Every write that names an existing resource carries the etag its
// caller sends back; a create carries none.
#[test]
fn test_an_update_or_a_removal_carries_its_etag_and_a_create_none() {
    use super::LibraryWriteRequest;
    let update = daemon_proto::v1::UpdateFragmentRequest {
        etag: "7".to_string(),
        ..Default::default()
    };
    assert_eq!(update.etag(), "7");
    let removal = daemon_proto::v1::DeleteBundleRequest {
        etag: "3".to_string(),
        ..Default::default()
    };
    assert_eq!(removal.etag(), "3");
    assert_eq!(daemon_proto::v1::CreateBundleRequest::default().etag(), "");
}

// `bundle edit` writes the bundle its request names, against the etag it
// carries.
#[test]
fn test_a_bundle_edit_writes_the_bundle_it_names_against_its_etag() {
    use super::LibraryWriteRequest;
    let mut edit = daemon_proto::v1::UpdateBundleRequest {
        etag: "5".to_string(),
        ..Default::default()
    };
    assert_eq!(edit.target(), "");
    edit.set_target("projects/p/bundles/demo".to_string());
    assert_eq!(edit.target(), "projects/p/bundles/demo");
    assert_eq!(edit.bundle.unwrap().name, "projects/p/bundles/demo");
    assert_eq!(edit.etag, "5");
}

// An operation that made nothing is the domain's refusal: something at the
// path it creates is `ResourceExists` (D4, NOREPLACE), nothing at the path
// it takes `NoSuchResource`; one that made its change is no refusal.
#[test]
fn test_an_operation_that_made_nothing_is_the_domain_s_refusal() {
    use ctxloom_ports::Operated;
    assert!(super::not_made(Operated::Done).is_none());
    let code = |operated| match super::not_made(operated) {
        Some(daemon_api_contract::ApiError::Refused(refusal)) => refusal
            .refusal
            .type_url
            .rsplit('.')
            .next()
            .unwrap_or_default()
            .to_string(),
        other => panic!("{other:?}"),
    };
    assert_eq!(
        code(Operated::Exists {
            path: "content/bundles/v2/notes".to_string()
        }),
        "ResourceExists"
    );
    assert_eq!(
        code(Operated::Missing {
            path: "content/bundles/v2/demo".to_string()
        }),
        "NoSuchResource"
    );
}

// An etag that is not a number (none a preview answered) is the request's
// own bad argument (protos.md §1.1): the typed `EtagInvalid`, naming the
// etag as sent, never a failure (exit 32).
#[test]
fn test_an_etag_that_is_not_a_number_is_refused_as_an_invalid_argument() {
    use super::seen_sequence;
    use daemon_proto::Refusal as _;
    let Err(daemon_api_contract::ApiError::Refused(refusal)) = seen_sequence("abc", 9) else {
        panic!("not refused")
    };
    assert_eq!(refusal.code, daemon_proto::v1::EtagInvalid::REFUSAL_STATUS);
    let invalid: daemon_proto::v1::EtagInvalid = refusal.refusal.to_msg().unwrap();
    assert_eq!(invalid.etag, "abc");
    assert!(matches!(
        seen_sequence("-1", 9),
        Err(daemon_api_contract::ApiError::Refused(_))
    ));
}

// Step 10b texts.md §3 (G12): a write that took a path into the trash says
// where it is kept, both as the tree shows them (its directory's own name
// first); any other step says nothing.
#[test]
fn test_a_trash_step_says_where_its_path_is_kept() {
    let trash = library_api::writes::Shape::Trash {
        path: "content/bundles/v2/demo".to_string(),
        files: Vec::new(),
    };
    let [said] = &super::trashed_notice("/work/demo/.ctxloom", &trash, "r-1")[..] else {
        panic!("one notice")
    };
    assert_eq!(said.code, "library-api-notice-trashed");
    assert!(
        said.text.contains(".ctxloom/content/bundles/v2/demo")
            && said.text.contains(".ctxloom/state/trash/r-1/"),
        "{}",
        said.text
    );
    let replace = library_api::writes::Shape::Replace {
        path: "config.yaml".to_string(),
        bytes: Vec::new(),
        executable: false,
    };
    assert!(super::trashed_notice("/work/demo/.ctxloom", &replace, "r-1").is_empty());
    let windows = super::trashed_notice(r"C:\work\.ctxloom", &trash, "r-2")
        .pop()
        .unwrap();
    assert!(
        windows.text.contains(".ctxloom/state/trash/r-2/"),
        "{}",
        windows.text
    );
}

// Step 10b U9 (G12): a sequence that keeps its trash (`clean`'s) says where
// each path is kept; one that purges its own slot (a forced import's) says
// nothing, as it keeps none.
#[test]
fn test_a_sequence_says_where_each_kept_path_is_and_a_purging_one_nothing() {
    use library_api::writes::Shape;
    let trash = |path: &str| Shape::Trash {
        path: path.to_string(),
        files: Vec::new(),
    };
    let clean = Shape::Sequence(vec![trash("cache/repos"), trash("cache/context")]);
    let said = super::trashed_notice("/work/demo/.ctxloom", &clean, "r-1");
    assert_eq!(said.len(), 2);
    assert!(
        said[1].text.contains(".ctxloom/cache/context"),
        "{}",
        said[1].text
    );
    let import = Shape::Sequence(vec![trash("content/bundles/v2/demo"), Shape::Purge]);
    assert!(super::trashed_notice("/work/demo/.ctxloom", &import, "r-1").is_empty());
}

// Step 10b (D2's EXCEPTION): a tree written outside ctxloom's trees is
// taken aside, the write itself, no step of any tree; beside a tree's
// change it is more than one change, refused.
#[test]
fn test_a_tree_outside_is_taken_aside_alone_and_refused_beside_a_change() {
    use library_application::write::recording::Planned;
    let outside = Planned::Outside {
        dir: "/work/exported/demo".to_string(),
        files: vec![ctxloom_ports::Put::file("bundle.yaml", "x")],
    };
    let (rest, taken) = super::outside_of(vec![outside.clone()]);
    assert!(rest.is_empty());
    let (dir, files) = taken.unwrap().expect("taken aside");
    assert_eq!(dir, "/work/exported/demo");
    assert_eq!(files.len(), 1);
    let layer = Planned::Layer {
        scope: ctxloom_ports::Scope::Project,
        text: "a: b\n".to_string(),
    };
    let (rest, taken) = super::outside_of(vec![layer.clone()]);
    assert_eq!(rest, std::slice::from_ref(&layer));
    assert_eq!(taken.unwrap(), None);
    let (_, taken) = super::outside_of(vec![outside, layer]);
    assert!(matches!(
        taken,
        Err(library_tree::LibraryTreeRejection::OneChangePerCall { .. })
    ));
}

fn evidence() -> library_tree::Evidence {
    library_tree::Evidence {
        tree_reads: true,
        plan_rejection: None,
        builtin: false,
    }
}

fn bare() -> library_tree::Step {
    library_tree::Step {
        replace: None,
        create: None,
        trash: None,
        r#move: None,
        purge: None,
    }
}

fn replaced(path: &str) -> library_tree::Step {
    library_tree::Step {
        replace: Some(library_tree::ReplaceFile {
            foreign: false,
            name: String::new(),
            verb: String::new(),
            path: path.to_string(),
            seen_sequence: 0,
            new_oid: String::new(),
            evidence: evidence(),
        }),
        ..bare()
    }
}

// AIP-155 (owner 2026-11-18, night): an export made before under a request
// id is answered from its record, applied, with the source and the
// destination it answered then; a request id nothing recorded is planned.
#[tokio::test]
async fn test_an_export_made_before_is_answered_from_its_record() {
    use std::sync::Arc;
    let f = crate::server::tests::fixture(vec![]);
    let gits = Arc::new(crate::server::tests::Gits::default());
    let api = f
        .api
        .observing(crate::server::tests::observing(&f.world, &gits));
    let (observations, _) = api.observing.as_ref().expect("observing");
    let trees = library_api::Trees {
        seen: Vec::new(),
        notices: Vec::new(),
    };
    let mut said = Vec::new();
    let fresh = answered_before::<()>(
        (&trees, observations),
        ("r-1", None),
        "projects/p",
        &mut said,
    );
    assert!(fresh.is_none(), "nothing recorded: planned");
    let fact = library_tree::RecordBundleExported {
        request_id: "r-1".to_string(),
        bundle: "demo".to_string(),
        source: "/w/.ctxloom/content/bundles/v2/demo/bundle.yaml".to_string(),
        destination: "/w/exported/demo".to_string(),
    };
    let tree = TreeName::home("brisk-copper-moth").unwrap();
    observations
        .send(library_api::writes::step_book(&tree, &fact, "export:r-1"))
        .await
        .expect("recorded");
    let retried = answered_before::<()>(
        (&trees, observations),
        ("r-1", None),
        "projects/p",
        &mut said,
    )
    .expect("answered")
    .expect("applied");
    assert!(retried.applied);
    assert!(retried.answer.is_none());
    assert_eq!(retried.at, "/w/exported/demo");
    assert_eq!(
        retried.source,
        "/w/.ctxloom/content/bundles/v2/demo/bundle.yaml"
    );
    assert_eq!(retried.parent, "projects/p");
    assert!(answered_before::<()>(
        (&trees, observations),
        ("r-2", None),
        "projects/p",
        &mut said
    )
    .is_none());
}

// Step 10c U13 (protos.md §1.3): a retry of a write whose intent an outside
// change superseded is refused stale (`PlanStale`, ABORTED, exit 48),
// naming its first step's path, the token the intent was decided on and
// the path's token now; a write still pending is planned again.
#[tokio::test]
async fn test_a_retry_of_a_superseded_write_is_refused_stale() {
    use std::sync::Arc;
    let f = crate::server::tests::fixture(vec![]);
    let gits = Arc::new(crate::server::tests::Gits::default());
    let api = f
        .api
        .observing(crate::server::tests::observing(&f.world, &gits));
    let (observations, _) = api.observing.as_ref().expect("observing");
    let seen = seen(true);
    let root = seen[0].1.tree.root().value.to_vec();
    let trees = library_api::Trees {
        seen,
        notices: Vec::new(),
    };
    let path = "content/bundles/v2/demo/fragments/example.md";
    let mut step = replaced(path);
    if let Some(replace) = step.replace.as_mut() {
        replace.seen_sequence = 3;
    }
    let known = |end| library_checkout::Known {
        root: root.clone(),
        intent: library_tree::Intent {
            foreign: Vec::new(),
            request_id: "r-1".to_string(),
            verb: "fragment edit".to_string(),
            steps: vec![step.clone()],
            base_cause: String::new(),
            done: 0,
        },
        end,
    };
    // The path's token now is the edit's sequence, 1 (the baseline's 0).
    let tree = trees.seen[0].1.tree.clone();
    let sight = |baseline: bool, base_cause: &str, changes: Vec<library_tree::Change>| {
        library_tree::RecordTreeObserved {
            name: tree.to_string(),
            kind: library_tree::TreeKind::Checkout,
            base_cause: base_cause.to_string(),
            head_before: "h-0".to_string(),
            head_after: "h-0".to_string(),
            tree_after: "t-0".to_string(),
            dirty_after: changes
                .iter()
                .map(|change| library_tree::DirtyPath {
                    path: change.path.clone(),
                    oid: change.to_oid.clone(),
                })
                .collect(),
            changes,
            baseline,
        }
    };
    observations
        .send(library_api::writes::step_book(
            &tree,
            &sight(true, "", vec![]),
            "obs-0",
        ))
        .await
        .expect("baselined");
    let edit = library_tree::Change {
        path: path.to_string(),
        status: library_tree::ChangeStatus::Modified,
        from_oid: "a".to_string(),
        to_oid: "by-hand".to_string(),
        from_path: String::new(),
    };
    observations
        .send(library_api::writes::step_book(
            &tree,
            &sight(false, "obs-0", vec![edit]),
            "obs-1",
        ))
        .await
        .expect("edited");
    let mut said = Vec::new();
    let superseded = known(library_checkout::End::Superseded);
    let refused = answered_before::<()>(
        (&trees, observations),
        ("r-1", Some(&superseded)),
        "projects/p",
        &mut said,
    )
    .expect("answered");
    let expected = crate::library_refusal(&library_tree::LibraryTreeRejection::PlanStale {
        path: path.to_string(),
        seen: 3,
        now: 1,
    });
    match refused {
        Err(crate::ApiError::Refused(refusal)) => assert_eq!(refusal, expected),
        other => panic!("not refused stale: {:?}", other.map(|_| ())),
    }
    let pending = known(library_checkout::End::Pending { done: 0 });
    assert!(answered_before::<()>(
        (&trees, observations),
        ("r-1", Some(&pending)),
        "projects/p",
        &mut said
    )
    .is_none());
}

// An export's record names its source as the export's own answer named it
// (AIP-155: a retry answers what the export answered): content-fs's
// manifest path, the tree's directory joined segment by segment, so the
// platform's separator throughout (`\` on Windows, never a `/` after the
// tree's own).
#[test]
fn test_an_export_s_recorded_source_is_spelled_as_its_answer_spells_it() {
    let sep = std::path::MAIN_SEPARATOR_STR;
    let tree = ["", "w", ".ctxloom"].join(sep);
    let want = [
        "",
        "w",
        ".ctxloom",
        "content",
        "bundles",
        "v2",
        "demo",
        "bundle.yaml",
    ]
    .join(sep);
    assert_eq!(
        export_source(&tree, "content/bundles/v2/demo/bundle.yaml"),
        want
    );
    // A directory spelled with its separator at the end is joined as
    // content-fs joins it: one separator, never two.
    assert_eq!(
        export_source(
            &format!("{tree}{sep}"),
            "content/bundles/v2/demo/bundle.yaml"
        ),
        want
    );
}
