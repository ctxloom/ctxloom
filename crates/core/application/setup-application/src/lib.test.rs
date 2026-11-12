use super::fakes::*;

#[test]
fn test_a_relative_path_is_made_absolute_against_the_working_directory() {
    let w = world(CONFIG);
    assert_eq!(w.setup.absolute("out"), "/p/out");
    assert_eq!(w.setup.absolute("./a/../b/"), "/p/b");
    assert_eq!(w.setup.absolute("/x/y"), "/x/y");
    assert_eq!(w.setup.absolute("../../.."), "/");
}

// On Windows the working directory's top is a drive's root or a share's, and
// either separator splits a path: a relative path joins it, an absolute one
// is itself, `..` never climbs above the top, and `\` joins the result.
#[test]
fn test_a_relative_path_is_made_absolute_the_windows_way_on_windows() {
    let setup = |cwd: &str| {
        let mut setup = world(CONFIG).setup.with_os("windows");
        setup.cwd = cwd.to_string();
        setup
    };
    let c = setup(r"C:\Users\me\project");
    assert_eq!(c.absolute("out"), r"C:\Users\me\project\out");
    assert_eq!(c.absolute(r".\a\..\b/c/"), r"C:\Users\me\project\b\c");
    assert_eq!(c.absolute(r"D:\x/y"), r"D:\x\y");
    assert_eq!(c.absolute("d:/x"), r"d:\x");
    assert_eq!(c.absolute(r"..\..\..\..\.."), r"C:\");
    let share = setup(r"\\srv\share\work");
    assert_eq!(share.absolute(r"..\..\a"), r"\\srv\share\a");
    assert_eq!(share.absolute("b"), r"\\srv\share\work\b");
    assert_eq!(setup("C:/p").absolute("x"), r"C:\p\x");
}

#[test]
fn test_the_engines_composed_in_are_listed() {
    assert_eq!(
        world(CONFIG).setup.engine_names(),
        vec!["claude-code", "mock"]
    );
}
