use std::path::{Path, PathBuf};

use super::*;

const CLEAN: &str = "v|abc1234|20261111000000|";

#[test]
fn test_a_clean_tree_is_the_version_and_the_short_hash() {
    let stamp = compose("0.1.0", "v|abc1234|20261111043015|").unwrap();
    assert_eq!(stamp.text, "v0.1.0-abc1234");
    assert!(!stamp.dirty);
}

#[test]
fn test_a_dirty_tree_adds_the_build_time_with_its_t_and_dirty() {
    let stamp = compose("0.1.0", "v|abc1234|20261111043015|dirty").unwrap();
    assert_eq!(stamp.text, "v0.1.0-abc1234-20261111T043015-dirty");
    assert!(stamp.dirty);
}

#[test]
fn test_a_pre_release_stays_between_the_version_and_the_hash() {
    let clean = compose("1.2.3-pre1", "v|0f0f0f0|20261111043015|").unwrap();
    assert_eq!(clean.text, "v1.2.3-pre1-0f0f0f0");
    let dirty = compose("1.2.3-rc.1", "v|0f0f0f0|20261111043015|dirty").unwrap();
    assert_eq!(dirty.text, "v1.2.3-rc.1-0f0f0f0-20261111T043015-dirty");
}

#[test]
fn test_the_versionator_lines_are_trimmed() {
    let stamp = compose(" 0.1.0\n", "v|abc1234|20261111043015|\n").unwrap();
    assert_eq!(stamp.text, "v0.1.0-abc1234");
}

#[test]
fn test_a_build_time_not_fourteen_digits_is_refused() {
    for time in ["2026111104301", "202611110430155", "2026111104301x", ""] {
        let fields = format!("v|abc1234|{time}|dirty");
        let refused = compose("0.1.0", &fields).unwrap_err();
        assert!(refused.contains(&fields), "{refused}");
        assert!(refused.contains("versionator"), "{refused}");
    }
}

#[test]
fn test_a_clean_tree_needs_no_build_time_but_a_garbled_one_is_refused() {
    assert!(compose("0.1.0", "v|abc1234|garbage|").is_err());
}

#[test]
fn test_fields_not_four_are_refused() {
    for fields in [
        "v|abc1234|20261111043015",
        "v|abc1234|20261111043015||x",
        "",
    ] {
        let refused = compose("0.1.0", fields).unwrap_err();
        assert!(refused.contains("4 fields"), "{refused}");
    }
}

#[test]
fn test_a_dirty_word_not_dirty_or_empty_is_refused() {
    let refused = compose("0.1.0", "v|abc1234|20261111043015|clean").unwrap_err();
    assert!(refused.contains("clean"), "{refused}");
}

#[test]
fn test_a_composed_stamp_not_of_the_shape_is_refused() {
    // No commit (an unborn branch): versionator gives no hash.
    let refused = compose("0.1.0", "v||20261111043015|").unwrap_err();
    assert!(refused.contains("v0.1.0-"), "{refused}");
    assert!(refused.contains(INSTALL), "{refused}");
    assert!(compose("0.1.0", "|abc1234|20261111043015|").is_err());
    assert!(compose("0.1", CLEAN).is_err());
    assert!(compose("0.1.0+build.5", CLEAN).is_err());
}

#[test]
fn test_the_shape_accepts_a_clean_and_a_dirty_stamp() {
    for stamp in [
        "v0.1.0-abc1234",
        "v10.20.30-0123456789abcdef",
        "v0.1.0-pre1-abc1234",
        "v0.1.0-rc.1-x-y-abc1234",
        "v0.1.0-abc1234-20261111T043015-dirty",
        "v0.1.0-pre1-abc1234-20261111T043015-dirty",
    ] {
        assert!(valid(stamp), "{stamp}");
    }
}

#[test]
fn test_the_shape_refuses_what_is_not_a_stamp() {
    for stamp in [
        "",
        "0.1.0",
        "0.1.0-abc1234",
        "v0.1.0",
        "v0.1.0-",
        "v0.1.0--abc1234",
        "v0.1-abc1234",
        "v0.1.0.4-abc1234",
        "v0..0-abc1234",
        "v0.x.0-abc1234",
        "v0.1.0-ABC1234",
        "v0.1.0-abcxyz1",
        "v0.1.0+meta-abc1234",
        "v0.1.0-pre..1-abc1234",
        "v0.1.0-pre_1-abc1234",
        "v0.1.0-abc1234-dirty",
        "v0.1.0-abc1234-20261111T043015",
        "v0.1.0-abc1234-20261111043015-dirty",
        "v0.1.0-abc1234-2026111T0430150-dirty",
        "v0.1.0-abc1234-20261111T04301x-dirty",
        "v0.1.0-abc1234-20261111X043015-dirty",
        "v0.1.0-20261111T043015-dirty",
        "V0.1.0-abc1234",
    ] {
        assert!(!valid(stamp), "{stamp}");
    }
}

#[test]
fn test_the_missing_versionator_message_names_it_and_its_install() {
    let said = versionator_unavailable("No such file or directory (os error 2)");
    assert!(said.contains("versionator"), "{said}");
    assert!(said.contains(INSTALL), "{said}");
    assert!(
        said.contains("No such file or directory (os error 2)"),
        "{said}"
    );
    assert!(INSTALL.contains("go install github.com/benjaminabbitt/versionator@v0.3.0"));
}

#[test]
fn test_a_failed_versionator_names_its_arguments_status_and_stderr() {
    let said = versionator_failed(&["output", "version"], "exit status: 1", "  boom\n");
    assert!(said.contains("versionator output version"), "{said}");
    assert!(said.contains("exit status: 1"), "{said}");
    assert!(said.contains("boom"), "{said}");
    assert!(said.contains(INSTALL), "{said}");
}

#[test]
fn test_a_missing_version_file_is_named() {
    let said = no_version_file(Path::new("/repo/VERSION"));
    assert!(said.contains("/repo/VERSION"), "{said}");
    assert!(said.contains("versionator"), "{said}");
}

#[test]
fn test_a_git_directory_is_the_dot_git_directory() {
    assert_eq!(
        git_dir(Path::new("/repo"), None),
        Some(PathBuf::from("/repo/.git"))
    );
}

#[test]
fn test_a_worktree_dot_git_file_names_its_git_directory() {
    assert_eq!(
        git_dir(
            Path::new("/w/tree"),
            Some("gitdir: /main/.git/worktrees/tree\n")
        ),
        Some(PathBuf::from("/main/.git/worktrees/tree"))
    );
    assert_eq!(
        git_dir(
            Path::new("/w/tree"),
            Some("gitdir: ../main/.git/worktrees/t")
        ),
        Some(PathBuf::from("/w/tree/../main/.git/worktrees/t"))
    );
    assert_eq!(git_dir(Path::new("/w/tree"), Some("nonsense")), None);
    assert_eq!(git_dir(Path::new("/w/tree"), Some("gitdir:  ")), None);
}

#[test]
fn test_the_common_directory_is_commondir_or_the_git_directory() {
    let git = Path::new("/main/.git/worktrees/tree");
    assert_eq!(
        common_dir(git, Some("../..\n")),
        PathBuf::from("/main/.git/worktrees/tree/../..")
    );
    assert_eq!(
        common_dir(git, Some("/abs/.git")),
        PathBuf::from("/abs/.git")
    );
    assert_eq!(common_dir(git, None), git.to_path_buf());
    assert_eq!(common_dir(git, Some("  \n")), git.to_path_buf());
}

#[test]
fn test_an_empty_commondir_is_the_git_directory_exactly_not_joined() {
    // `Path` equality ignores a trailing separator; joining "" adds one.
    let git = Path::new("/main/.git/worktrees/tree");
    for blank in ["", "  \n"] {
        assert_eq!(common_dir(git, Some(blank)).as_os_str(), git.as_os_str());
    }
}

#[test]
fn test_head_names_its_branch_ref_or_none_when_detached() {
    assert_eq!(
        head_ref("ref: refs/heads/version/stamp\n"),
        Some("refs/heads/version/stamp")
    );
    assert_eq!(head_ref("1ec2efa9502dfa0b283a1935438a3678bb936500\n"), None);
}

#[test]
fn test_the_watched_git_files_are_head_index_the_ref_and_packed_refs() {
    let watched = git_watched(Path::new("/g/wt"), Path::new("/g"), Some("refs/heads/main"));
    assert_eq!(
        watched,
        [
            PathBuf::from("/g/wt/HEAD"),
            PathBuf::from("/g/wt/index"),
            PathBuf::from("/g/packed-refs"),
            PathBuf::from("/g/refs/heads/main"),
        ]
    );
    assert_eq!(git_watched(Path::new("/g"), Path::new("/g"), None).len(), 3);
}

#[test]
fn test_tracked_files_are_the_nul_separated_listing_under_the_root() {
    assert_eq!(
        tracked(Path::new("/r"), b"a.rs\0dir/b c.rs\0"),
        [PathBuf::from("/r/a.rs"), PathBuf::from("/r/dir/b c.rs")]
    );
    assert!(tracked(Path::new("/r"), b"").is_empty());
}
