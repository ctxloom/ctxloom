use coded_diagnostic::Diagnostic;

use super::{diagnostic_samples, LibraryApiNotice, EN_US};

#[test]
fn every_notice_has_a_message_using_exactly_its_args_and_its_code_is_prefixed() {
    let (name, catalog) = EN_US;
    assert_eq!(name, "library-api");
    let samples = diagnostic_samples();
    assert_eq!(samples.len(), 1);
    for sample in samples {
        let code = sample.code();
        assert!(code.starts_with("library-api-notice-"), "{code}");
        let start = catalog
            .find(&format!("{code} ="))
            .unwrap_or_else(|| panic!("{code} has no message"));
        let rest = &catalog[start..];
        let message = rest.split("\nlibrary-api-").next().unwrap_or_default();
        let mut used: Vec<&str> = message
            .split('$')
            .skip(1)
            .map(|r| {
                r.split(|c: char| !(c.is_ascii_alphanumeric() || c == '_'))
                    .next()
                    .unwrap()
            })
            .collect();
        used.sort_unstable();
        used.dedup();
        let mut names: Vec<&str> = sample.args().into_iter().map(|(n, _)| n).collect();
        names.sort_unstable();
        assert_eq!(names, used, "{code}");
    }
}

#[test]
fn a_notice_displays_as_its_code_and_args() {
    let notice = LibraryApiNotice::Trashed {
        path: "demo".to_string(),
        slot: "trash/r1/".to_string(),
    };
    assert_eq!(
        notice.to_string(),
        "library-api-notice-trashed path=demo slot=trash/r1/"
    );
}

#[test]
fn the_trashed_notice_names_the_path_and_its_slot() {
    let notice = LibraryApiNotice::Trashed {
        path: ".ctxloom/content/bundles/v2/demo".to_string(),
        slot: ".ctxloom/state/trash/r1/".to_string(),
    };
    assert_eq!(notice.code(), "library-api-notice-trashed");
    let args: Vec<String> = notice
        .args()
        .into_iter()
        .map(|(n, v)| format!("{n}={v:?}"))
        .collect();
    assert_eq!(args.len(), 2);
    assert!(
        args[0].starts_with("path=") && args[0].contains("v2/demo"),
        "{args:?}"
    );
    assert!(
        args[1].starts_with("slot=") && args[1].contains("trash/r1/"),
        "{args:?}"
    );
}
