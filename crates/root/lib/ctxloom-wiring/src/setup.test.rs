//! The setup use cases, wired.

// doctor.feature: "A bundle in the old metadata layout is a warning naming
// the git mv" (step 10c U5, D15): each old layout is said in the library's
// words, a project's own with the move that migrates it.
#[test]
fn an_old_layout_is_said_in_the_librarys_words_with_its_move() {
    let said = super::said_old_layouts(&[library::BundleError::OldLayout {
        bundle: "demo".to_string(),
        kind: "skill".to_string(),
        old: ".ctxloom/a/skills/.review.meta.yaml".to_string(),
        new: ".ctxloom/a/skills/review/.meta.yaml".to_string(),
        remote: String::new(),
    }]);
    assert_eq!(said.len(), 1);
    assert!(
        said[0]
            .0
            .starts_with("demo keeps its skill metadata at .ctxloom/a/skills/.review.meta.yaml"),
        "{said:?}"
    );
    assert_eq!(
        said[0].1,
        "git mv .ctxloom/a/skills/.review.meta.yaml .ctxloom/a/skills/review/.meta.yaml"
    );
}
