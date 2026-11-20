use super::*;

#[test]
fn test_a_path_below_a_shown_directory_is_spelled_as_the_directory_is() {
    assert_eq!(
        below("/p/v2/demo", "profiles/dev.yaml"),
        "/p/v2/demo/profiles/dev.yaml"
    );
    assert_eq!(
        below(r"C:\p\v2\demo", "profiles/dev.yaml"),
        r"C:\p\v2\demo\profiles\dev.yaml"
    );
    assert_eq!(below(r"\\srv\share\demo", "a/b"), r"\\srv\share\demo\a\b");
    // A unix name may hold a `\`, which is no separator there.
    assert_eq!(below(r"/p/a\b", "c"), r"/p/a\b/c");
}

#[test]
fn test_a_shown_file_s_directory_is_up_to_its_last_separator() {
    assert_eq!(parent("/p/v2/demo/bundle.yaml"), "/p/v2/demo");
    assert_eq!(parent(r"C:\p\v2\demo\bundle.yaml"), r"C:\p\v2\demo");
    assert_eq!(parent(r"\\srv\share\demo\bundle.yaml"), r"\\srv\share\demo");
    assert_eq!(parent(r"/p/a\bundle.yaml"), "/p");
    assert_eq!(parent("bundle.yaml"), "bundle.yaml");
}
