use super::{said, Finding};

// hand-test 2 2026-11-11: a missing bundle was said by a failing finding and
// again by a warning about the same bundle.
#[test]
fn test_a_warning_about_what_a_failure_is_about_is_not_said() {
    let failed = Finding::fail("bundle", "failed to load bundle x", "pull").about("x");
    let echo = Finding::warn("bundle", "skipping unresolved bundle x").about("x");
    let other = Finding::warn("bundle", "skipping unresolved bundle y").about("y");
    let plain = Finding::warn("bundle", "something");
    let elsewhere = Finding::warn("ref", "x of another kind").about("x");
    let findings = [
        echo,
        failed.clone(),
        other.clone(),
        plain.clone(),
        elsewhere.clone(),
    ];
    assert_eq!(said(&findings), [&failed, &other, &plain, &elsewhere]);
}

#[test]
fn test_a_warning_alone_is_said() {
    let echo = Finding::warn("bundle", "skipping unresolved bundle x").about("x");
    assert_eq!(said(std::slice::from_ref(&echo)), [&echo]);
}
