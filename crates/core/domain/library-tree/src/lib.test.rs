use std::collections::BTreeMap;

use coded_diagnostic::Diagnostic;

use crate::{diagnostic_samples, LibraryTreeRejection, EN_US, LIBRARY_DOMAIN};

#[test]
fn the_domain_name_is_library() {
    assert_eq!(LIBRARY_DOMAIN, "library");
}

#[test]
fn every_diagnostic_has_a_message_and_its_code_is_prefixed() {
    let (name, catalog) = EN_US;
    assert_eq!(name, "library-tree");
    for sample in diagnostic_samples() {
        let code = sample.code();
        assert!(code.starts_with("library-tree-"), "{code} is prefixed");
        assert!(
            catalog.contains(&format!("{code} =")),
            "{code} has no message"
        );
        for (arg, _) in sample.args() {
            assert!(catalog.contains(&format!("${arg}")), "{code} uses {arg}");
        }
    }
    assert_eq!(diagnostic_samples().len(), 7);
}

#[test]
fn a_plans_rejection_speaks_as_the_plans_own_diagnostic() {
    let rejected = LibraryTreeRejection::PlanRejected {
        code: "library-application-write-unknown-kind".to_string(),
        args: BTreeMap::from([
            ("kind".to_string(), "widget".to_string()),
            ("bundle".to_string(), "demo".to_string()),
        ]),
    };
    assert_eq!(rejected.code(), "library-application-write-unknown-kind");
    let args: Vec<String> = rejected
        .args()
        .into_iter()
        .map(|(name, value)| format!("{name}={value:?}"))
        .collect();
    assert_eq!(args.len(), 2);
    assert!(args[0].starts_with("bundle="), "{args:?}");
    assert!(args[1].starts_with("kind="), "{args:?}");
}

#[test]
fn each_rejection_names_its_args() {
    let names = |r: LibraryTreeRejection| -> Vec<String> {
        r.args().into_iter().map(|(n, _)| n.to_string()).collect()
    };
    let path = || "p".to_string();
    assert_eq!(
        names(LibraryTreeRejection::PlanStale {
            path: path(),
            seen: 1,
            now: 2
        }),
        ["path", "seen", "now"]
    );
    assert_eq!(
        names(LibraryTreeRejection::ResourceExists { path: path() }),
        ["path"]
    );
    assert_eq!(
        names(LibraryTreeRejection::NoSuchResource { path: path() }),
        ["path"]
    );
    assert_eq!(
        names(LibraryTreeRejection::BuiltinReadOnly { bundle: path() }),
        ["bundle"]
    );
}

#[test]
fn rejections_display_their_code() {
    assert_eq!(
        LibraryTreeRejection::ResourceExists {
            path: "x".to_string()
        }
        .code(),
        "library-tree-resource-exists"
    );
    assert!(LibraryTreeRejection::ResourceExists {
        path: "x".to_string()
    }
    .to_string()
    .starts_with("library-tree-resource-exists"));
}
