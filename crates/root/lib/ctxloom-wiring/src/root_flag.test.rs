use super::*;

fn env() -> Arc<EnvSnapshot> {
    Arc::new(
        EnvSnapshot::of([("HOME", "/home/u"), ("CTXLOOM_ROOT", "/other")]).with_cwd("/work/here"),
    )
}

#[test]
fn test_the_flag_omitted_leaves_the_environment_as_it_is() {
    let applied = applied(&env(), None).expect("applied");
    assert_eq!(applied.utf8("CTXLOOM_ROOT"), Some("/other"));
}

#[test]
fn test_a_given_directory_wins_over_ctxloom_root() {
    let applied = applied(&env(), Some("/proj")).expect("applied");
    assert_eq!(applied.utf8("CTXLOOM_ROOT"), Some("/proj"));
    assert_eq!(applied.utf8("HOME"), Some("/home/u"));
}

#[test]
fn test_a_relative_directory_is_resolved_against_the_working_directory() {
    let applied = applied(&env(), Some("sub")).expect("applied");
    assert_eq!(applied.utf8("CTXLOOM_ROOT"), Some("/work/here/sub"));
}

#[test]
fn test_a_bare_flag_names_the_users_home() {
    let applied = applied(&env(), Some("")).expect("applied");
    assert_eq!(applied.utf8("CTXLOOM_ROOT"), Some("/home/u"));
}

#[test]
fn test_a_bare_flag_with_no_home_is_refused() {
    let none = Arc::new(EnvSnapshot::of::<&str, &str>([]));
    assert!(matches!(
        applied(&none, Some("")),
        Err(LibError::NoHome { .. })
    ));
}
