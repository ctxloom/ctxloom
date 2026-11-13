//! The workspaces adapter, over real git in temporary directories. Ported
//! from Go's `worktree_test.go`, `worktree_integration_test.go`,
//! `worktree_reap_test.go` and j002200_isolation.feature's workspace rows
//! (release/0.7, f36e8dfb21).

use std::path::{Path, PathBuf};
use std::process::Command;

use isolation_ports::{
    session_worktrees, worktrees_root, Prepared, Removal, Request, Safety, Standing, Workspaces,
};

use super::{
    branch_of, checkout_name, is_name, nested_under, same_path, sanitize, GitWorkspaces, Listed,
    EXCLUDE_HEADER,
};
use crate::fixtures::{
    canonical, exists, fs, is_dir, link, mkdirs, put, root, runs_scripts, slurp,
};

const PATTERNS: [&str; 5] = [
    ".ctxloom/cache/",
    ".ctxloom/project-id",
    ".claude/",
    ".mcp.json",
    "CLAUDE.md",
];

/// git with a clean identity and none of the caller's environment.
fn git(dir: &Path, args: &[&str]) -> String {
    let output = Command::new("git")
        .args(["-c", "user.name=Test", "-c", "user.email=test@example.com"])
        .args([
            "-c",
            "commit.gpgsign=false",
            "-c",
            "init.defaultBranch=main",
        ])
        .args(args)
        .current_dir(dir)
        .env_clear()
        .env(
            "PATH",
            crate::fixtures::env().var("PATH").unwrap_or_default(),
        )
        .env("HOME", dir)
        .output()
        .expect("git runs");
    assert!(
        output.status.success(),
        "git {args:?}: {}",
        String::from_utf8_lossy(&output.stderr)
    );
    String::from_utf8_lossy(&output.stdout).into_owned()
}

/// The project's id, which names its checkouts' directory under the home.
const PROJECT_ID: &str = "calm-otter";

/// A ctxloom home and a git-backed project beside it.
struct Fixture {
    _root: file_system::Rooted,
    home: PathBuf,
    project: PathBuf,
}

impl Fixture {
    fn new() -> Fixture {
        let root = root();
        let home = root.root().join("home");
        let project = root.root().join("project");
        mkdirs(&home);
        mkdirs(&project);
        git(&project, &["init", "-q"]);
        put(project.join("README.md"), "hello\n");
        git(&project, &["add", "-A"]);
        git(&project, &["commit", "-q", "-m", "initial"]);
        Fixture {
            _root: root,
            home,
            project,
        }
    }

    fn adapter(&self) -> GitWorkspaces {
        GitWorkspaces::new(
            fs(),
            &self.project,
            Some(worktrees_root(&self.home, PROJECT_ID)),
            crate::fixtures::env(),
        )
    }

    /// The session's checkouts directory, under the home.
    fn work_dir(&self, harp: &str) -> PathBuf {
        session_worktrees(&self.home, PROJECT_ID, harp)
    }

    /// The project's own sessions directory, where a session's directory
    /// and its lock are.
    fn sessions(&self) -> PathBuf {
        self.project.join(".ctxloom").join("sessions")
    }

    fn request(&self, harp: &str, agent: &str) -> Request {
        Request {
            project_dir: self.project.display().to_string(),
            work_dir: self.work_dir(harp).display().to_string(),
            agent: agent.to_string(),
            artifact_patterns: PATTERNS.iter().map(ToString::to_string).collect(),
        }
    }

    fn prepared(&self, harp: &str, agent: &str) -> isolation_ports::Checkout {
        match self.adapter().prepare(&self.request(harp, agent)) {
            Ok(Prepared::Checkout(checkout)) => checkout,
            other => panic!("no checkout: {other:?}"),
        }
    }
}

// Go: worktree_test.go sanitizeAgentID.
#[test]
fn an_agent_id_becomes_one_safe_path_segment() {
    assert_eq!(sanitize("member-1"), "member-1");
    assert_eq!(sanitize("a/b c"), "a-b-c");
    assert_eq!(sanitize("--x.."), "x");
    assert_eq!(sanitize(""), "agent");
    assert_eq!(sanitize("///"), "agent");
}

// The checkout is named by the agent alone, and its branch is the session's
// and the agent's, `ctxloom/<harp>/<agent>` (F1): a pure function of the
// checkout's path (`<harp>/<agent>`), so whoever holds it can name the
// branch with no side table.
#[test]
fn the_branch_derives_from_the_checkouts_session_and_agent() {
    assert_eq!(checkout_name("reviewer"), "reviewer");
    assert_eq!(checkout_name("a/b c"), "a-b-c");
    assert_eq!(
        branch_of("/home/.ctxloom/worktrees/calm-otter/amber-swift-owl/reviewer"),
        "ctxloom/amber-swift-owl/reviewer"
    );
}

// j002200 "The same run lands in the workspace its axis dictates": a
// worktree is a checkout of its own, on its session's branch for its agent,
// under ctxloom's home (owner ruling 2026-11-12, Q1) and never under the
// project.
#[test]
fn a_worktree_run_gets_its_own_branch_under_the_ctxloom_home() {
    let f = Fixture::new();
    let checkout = f.prepared("amber-quiet-heron", "reviewer");
    let expected = canonical(&f.home)
        .join(".ctxloom/worktrees")
        .join(PROJECT_ID)
        .join("amber-quiet-heron/reviewer");
    assert_eq!(Path::new(&checkout.path), expected);
    assert_eq!(checkout.branch, "ctxloom/amber-quiet-heron/reviewer");
    assert!(!checkout.reused);
    assert!(!Path::new(&checkout.path).starts_with(&f.project));
    let branch = git(
        Path::new(&checkout.path),
        &["rev-parse", "--abbrev-ref", "HEAD"],
    );
    assert_eq!(branch.trim(), "ctxloom/amber-quiet-heron/reviewer");
    assert!(exists(Path::new(&checkout.path).join("README.md")));
}

// F1: two sessions of one agent each get a checkout and a branch of their
// own; git would refuse the second a branch the first has checked out.
#[test]
fn two_sessions_of_one_agent_get_a_checkout_and_a_branch_each() {
    let f = Fixture::new();
    let one = f.prepared("amber-quiet-heron", "reviewer");
    let two = f.prepared("brisk-copper-moth", "reviewer");
    assert_ne!(one.path, two.path);
    assert_ne!(one.branch, two.branch);
    assert_eq!(two.branch, "ctxloom/brisk-copper-moth/reviewer");
    assert!(!two.reused);
}

// macOS's temporary directories are `/var/...`, which is `/private/var/...`
// once resolved, and an engine knows its working directory by the resolved
// one (claude keys its trust by it): the checkout's path is canonical.
#[test]
fn the_checkout_path_is_the_resolved_one() {
    let f = Fixture::new();
    let linked = f.home.parent().unwrap().join("linked-home");
    link(&f.home, &linked);
    let mut request = f.request("amber-quiet-heron", "reviewer");
    request.work_dir = session_worktrees(&linked, PROJECT_ID, "amber-quiet-heron")
        .display()
        .to_string();
    let Ok(Prepared::Checkout(checkout)) = f.adapter().prepare(&request) else {
        panic!("no checkout");
    };
    assert_eq!(
        Path::new(&checkout.path),
        canonical(&f.work_dir("amber-quiet-heron").join("reviewer"))
    );
}

// j002200 "Two isolated runs share no workspace between them".
#[test]
fn two_isolated_runs_share_no_workspace() {
    let f = Fixture::new();
    let one = f.prepared("amber-quiet-heron", "task-one");
    let two = f.prepared("brisk-copper-moth", "task-two");
    assert_ne!(one.path, two.path);
    for checkout in [&one, &two] {
        assert!(!Path::new(&checkout.path).starts_with(&f.project));
    }
}

// j002200 "Asking for a worktree outside git runs in the live project
// directory instead of refusing".
#[test]
fn a_worktree_outside_git_is_unavailable_not_a_refusal() {
    let f = Fixture::new();
    let plain = f.home.join("plain");
    mkdirs(&plain);
    let mut request = f.request("amber-quiet-heron", "reviewer");
    request.project_dir = plain.display().to_string();
    match f.adapter().prepare(&request) {
        Ok(Prepared::Unavailable(why)) => {
            assert!(why.contains("not a git repository"), "{why}");
        }
        other => panic!("{other:?}"),
    }
}

// j002200 "A worktree run leaves the project tree clean": the config the run
// writes is hidden by the shared exclude, in the project and in the checkout.
#[test]
fn the_per_agent_config_never_appears_in_the_projects_git_status() {
    let f = Fixture::new();
    let checkout = f.prepared("amber-quiet-heron", "reviewer");
    let wt = Path::new(&checkout.path);
    mkdirs(wt.join(".claude"));
    put(wt.join(".claude/settings.json"), "{}");
    put(wt.join("CLAUDE.md"), "context");
    put(wt.join(".mcp.json"), "{}");
    mkdirs(wt.join(".ctxloom/cache"));
    put(wt.join(".ctxloom/cache/x"), "x");
    assert_eq!(git(&f.project, &["status", "--porcelain"]).trim(), "");
    assert_eq!(git(wt, &["status", "--porcelain"]).trim(), "");
    let exclude = slurp(f.project.join(".git/info/exclude"));
    assert!(exclude.contains(EXCLUDE_HEADER), "{exclude}");
    for pattern in PATTERNS {
        assert!(exclude.contains(pattern), "{pattern} in {exclude}");
    }
}

// Go: excludeConfigFromMerge is idempotent and shared-safe.
#[test]
fn preparing_twice_writes_the_exclude_block_once() {
    let f = Fixture::new();
    f.prepared("amber-quiet-heron", "one");
    f.prepared("amber-quiet-heron", "two");
    let exclude = slurp(f.project.join(".git/info/exclude"));
    assert_eq!(exclude.matches(EXCLUDE_HEADER).count(), 1, "{exclude}");
    assert_eq!(exclude.matches("CLAUDE.md").count(), 1, "{exclude}");
}

// Go: reuseExistingCheckout (sizable-antler): an earlier run's own checkout,
// still registered on the agent's branch, is re-entered in place.
#[test]
fn a_resume_re_enters_the_agents_own_checkout() {
    let f = Fixture::new();
    let first = f.prepared("amber-quiet-heron", "reviewer");
    put(Path::new(&first.path).join("wip.txt"), "in flight");
    let again = f.prepared("amber-quiet-heron", "reviewer");
    assert!(again.reused);
    assert_eq!(again.path, first.path);
    assert!(exists(Path::new(&again.path).join("wip.txt")));
}

// Go: a path collision is not proof of ownership: anything else at the path
// is a refusal, not a degrade, and is left exactly as it was.
#[test]
fn something_else_at_the_agents_path_is_refused_and_left_alone() {
    let f = Fixture::new();
    let path = f.work_dir("amber-quiet-heron").join("reviewer");
    mkdirs(&path);
    put(path.join("mine.txt"), "mine");
    let refused = f
        .adapter()
        .prepare(&f.request("amber-quiet-heron", "reviewer"))
        .unwrap_err();
    assert_eq!(refused.code(), "worktree-git-unadoptable");
    assert_eq!(slurp(path.join("mine.txt")), "mine");
}

// Go: findWorkWorktrees: only the directories directly under a session's
// checkouts directory (the whole tree is ctxloom's); a file there and her
// own trees elsewhere are not candidates.
#[test]
fn scratch_lists_only_the_checkouts_under_the_sessions_directories() {
    let f = Fixture::new();
    let a = f.prepared("amber-quiet-heron", "a");
    let b = f.prepared("brisk-copper-moth", "b");
    put(f.work_dir("amber-quiet-heron").join("a-file"), "x");
    let foreign = f.home.join("workspace/worktrees/proj--stale");
    git(
        &f.project,
        &[
            "worktree",
            "add",
            "-q",
            "-b",
            "stale",
            &foreign.display().to_string(),
        ],
    );
    let ws = f.adapter();
    let all = ws.scratch(None).unwrap();
    let paths: Vec<&str> = all.iter().map(|s| s.path.as_str()).collect();
    assert_eq!(paths.len(), 2, "{paths:?}");
    assert_eq!(all[0].harp, "amber-quiet-heron");
    assert!(paths.contains(&a.path.as_str()) && paths.contains(&b.path.as_str()));
    assert!(!paths.iter().any(|p| p.contains("stale")));
    let one = ws.scratch(Some("amber-quiet-heron")).unwrap();
    assert_eq!(one.len(), 1);
    assert_eq!(one[0].harp, "amber-quiet-heron");
}

#[test]
fn scratch_of_a_home_where_nothing_ever_ran_is_empty() {
    let f = Fixture::new();
    assert!(f.adapter().scratch(None).unwrap().is_empty());
    assert!(f.adapter().scratch(Some("nobody")).unwrap().is_empty());
}

#[test]
fn a_session_is_known_by_its_directory() {
    let f = Fixture::new();
    mkdirs(f.sessions().join("amber-swift-owl"));
    let ws = f.adapter();
    assert!(ws.session_known("amber-swift-owl").unwrap());
    assert!(!ws.session_known("nobody").unwrap());
    assert!(!ws.session_known("../escape").unwrap());
}

// A session whose directory was never written (one that wrote nothing in
// it, as the test seed's) is still known by the lock it left beside it: the
// file its state is read from.
#[test]
fn a_session_is_known_by_its_lock_alone() {
    let f = Fixture::new();
    mkdirs(f.sessions());
    put(f.sessions().join("amber-swift-owl.lock"), "");
    let ws = f.adapter();
    assert!(ws.session_known("amber-swift-owl").unwrap());
    assert!(!ws.session_known("nobody").unwrap());
}

// A session whose checkouts outlive its directory in the project is still
// known by them.
#[test]
fn a_session_is_known_by_its_checkouts_alone() {
    let f = Fixture::new();
    f.prepared("amber-swift-owl", "reviewer");
    assert!(f.adapter().session_known("amber-swift-owl").unwrap());
}

// A wiring with no project has no checkouts directory: nothing is listed.
#[test]
fn workspaces_with_no_checkouts_directory_list_none() {
    let f = Fixture::new();
    f.prepared("amber-swift-owl", "reviewer");
    let ws = GitWorkspaces::new(fs(), &f.project, None, crate::fixtures::env());
    assert!(ws.scratch(None).unwrap().is_empty());
    assert!(ws.scratch(Some("amber-swift-owl")).unwrap().is_empty());
}

// Go: unsafeToRemove: dirty, untracked and ignored-only content all make a
// tree unsafe; an unreadable state is never clean.
#[test]
fn inspect_reads_clean_dirty_and_ignored_only_trees() {
    let f = Fixture::new();
    let clean = f.prepared("amber-quiet-heron", "clean");
    let dirty = f.prepared("amber-quiet-heron", "dirty");
    let ignored = f.prepared("amber-quiet-heron", "ignored");
    put(Path::new(&dirty.path).join("in-flight.go"), "// wip\n");
    put(Path::new(&ignored.path).join("CLAUDE.md"), "ignored\n");
    let ws = f.adapter();
    assert_eq!(
        ws.inspect(&clean.path),
        Standing::Resolved {
            repo_dir: canonical(&f.project).display().to_string(),
            safety: Safety::Clean
        }
    );
    match ws.inspect(&dirty.path) {
        Standing::Resolved {
            safety: Safety::Unsafe(why),
            ..
        } => assert!(why.contains("uncommitted changes"), "{why}"),
        other => panic!("{other:?}"),
    }
    match ws.inspect(&ignored.path) {
        Standing::Resolved {
            safety: Safety::Unsafe(why),
            ..
        } => assert!(why.contains("gitignored"), "{why}"),
        other => panic!("{other:?}"),
    }
}

#[test]
fn inspect_of_a_directory_in_no_repository_is_unresolved() {
    let f = Fixture::new();
    let plain = f.home.join("plain");
    mkdirs(&plain);
    assert!(matches!(
        f.adapter().inspect(&plain.display().to_string()),
        Standing::Unresolved(_)
    ));
}

// Go: ReapWorktrees: a clean checkout is removed, its registration pruned,
// and its branch deleted when merged.
#[test]
fn removing_a_clean_checkout_removes_it_its_registration_and_its_merged_branch() {
    let f = Fixture::new();
    let checkout = f.prepared("amber-quiet-heron", "clean");
    let removal = f.adapter().remove(&checkout.repo_dir, &checkout.path);
    assert_eq!(removal, Removal::Removed);
    assert!(!exists(&checkout.path));
    let listed = git(&f.project, &["worktree", "list", "--porcelain"]);
    assert!(!listed.contains(&checkout.path), "{listed}");
    let branches = git(
        &f.project,
        &["branch", "--list", "ctxloom/amber-quiet-heron/clean"],
    );
    assert_eq!(
        branches.trim(),
        "",
        "a merged branch holds nothing the repository does not"
    );
}

// Go: an unmerged branch holds commits nothing else has, and is kept.
#[test]
fn an_unmerged_branch_is_kept() {
    let f = Fixture::new();
    let checkout = f.prepared("amber-quiet-heron", "work");
    let wt = Path::new(&checkout.path);
    put(wt.join("feature.txt"), "feature");
    git(wt, &["add", "-A"]);
    git(wt, &["commit", "-q", "-m", "feature"]);
    assert_eq!(
        f.adapter().remove(&checkout.repo_dir, &checkout.path),
        Removal::Removed
    );
    let branches = git(
        &f.project,
        &["branch", "--list", "ctxloom/amber-quiet-heron/work"],
    );
    assert!(
        branches.contains("ctxloom/amber-quiet-heron/work"),
        "{branches}"
    );
}

// j001300 "THE SAFETY SCENARIO": uncommitted work is spared IN PLACE, its
// bytes still on disk and still registered with git.
#[test]
fn removing_a_checkout_with_uncommitted_work_leaves_it_in_place() {
    let f = Fixture::new();
    let checkout = f.prepared("amber-quiet-heron", "wip");
    let wip = Path::new(&checkout.path).join("in-flight.go");
    put(&wip, "// WIP-MARKER\n");
    let removal = f.adapter().remove(&checkout.repo_dir, &checkout.path);
    assert!(matches!(removal, Removal::Left(_)), "{removal:?}");
    assert_eq!(slurp(&wip), "// WIP-MARKER\n");
    let listed = git(&f.project, &["worktree", "list", "--porcelain"]);
    assert!(listed.contains(&checkout.path), "{listed}");
}

// Go: teardownWorktree: a dirty nested worktree aborts the whole teardown.
#[test]
fn a_dirty_nested_worktree_keeps_its_parent_in_place() {
    let f = Fixture::new();
    let parent = f.prepared("amber-quiet-heron", "parent");
    let inner = Path::new(&parent.path).join("inner");
    git(
        Path::new(&parent.path),
        &[
            "worktree",
            "add",
            "-q",
            "-b",
            "inner-branch",
            &inner.display().to_string(),
        ],
    );
    put(inner.join("nested-wip.txt"), "nested");
    let removal = f.adapter().remove(&parent.repo_dir, &parent.path);
    assert!(matches!(removal, Removal::Left(_)), "{removal:?}");
    assert!(exists(inner.join("nested-wip.txt")));
}

fn exclude_of(f: &Fixture) -> String {
    slurp(f.project.join(".git/info/exclude"))
}

#[test]
fn a_directory_outside_git_says_it_is_not_a_repository() {
    let f = Fixture::new();
    let plain = f.home.join("plain");
    mkdirs(&plain);
    let mut request = f.request("amber-quiet-heron", "reviewer");
    request.project_dir = plain.display().to_string();
    let why = match f.adapter().prepare(&request) {
        Ok(Prepared::Unavailable(why)) => why,
        other => panic!("{other:?}"),
    };
    assert!(why.starts_with("worktree isolation: "), "{why}");
}

#[test]
fn a_branch_an_earlier_run_left_is_re_entered() {
    let f = Fixture::new();
    git(
        &f.project,
        &["branch", "ctxloom/amber-quiet-heron/reviewer"],
    );
    let checkout = f.prepared("amber-quiet-heron", "reviewer");
    assert_eq!(checkout.branch, "ctxloom/amber-quiet-heron/reviewer");
    assert!(!checkout.reused, "the checkout is new; the branch is not");
}

#[test]
fn a_checkout_registered_on_another_branch_is_refused() {
    let f = Fixture::new();
    let path = f.work_dir("amber-quiet-heron").join("reviewer");
    git(
        &f.project,
        &[
            "worktree",
            "add",
            "-q",
            "-b",
            "someone-elses",
            &path.display().to_string(),
        ],
    );
    let refused = f
        .adapter()
        .prepare(&f.request("amber-quiet-heron", "reviewer"))
        .unwrap_err();
    assert_eq!(refused.code(), "worktree-git-unadoptable");
    assert!(refused.to_string().contains("someone-elses"), "{refused}");
}

#[test]
fn the_exclude_block_starts_a_fresh_file_without_a_blank_line() {
    let f = Fixture::new();
    put(f.project.join(".git/info/exclude"), "");
    f.prepared("amber-quiet-heron", "one");
    assert!(
        exclude_of(&f).starts_with(EXCLUDE_HEADER),
        "{}",
        exclude_of(&f)
    );
}

#[test]
fn the_exclude_block_follows_an_unterminated_line_on_a_line_of_its_own() {
    let f = Fixture::new();
    put(f.project.join(".git/info/exclude"), "mine");
    f.prepared("amber-quiet-heron", "one");
    assert!(
        exclude_of(&f).starts_with(&format!("mine\n{EXCLUDE_HEADER}\n")),
        "{}",
        exclude_of(&f)
    );
}

#[test]
fn the_exclude_block_follows_a_terminated_line_with_no_blank_between() {
    let f = Fixture::new();
    put(f.project.join(".git/info/exclude"), "mine\n");
    f.prepared("amber-quiet-heron", "one");
    assert!(
        exclude_of(&f).starts_with(&format!("mine\n{EXCLUDE_HEADER}\n")),
        "{}",
        exclude_of(&f)
    );
}

// A path the repository tracks that the run will overwrite cannot merge back.
#[test]
fn a_tracked_config_path_is_marked_skip_worktree() {
    let f = Fixture::new();
    put(f.project.join("CLAUDE.md"), "tracked");
    git(&f.project, &["add", "-A"]);
    git(&f.project, &["commit", "-q", "-m", "tracked config"]);
    let checkout = f.prepared("amber-quiet-heron", "reviewer");
    let wt = Path::new(&checkout.path);
    let flags = git(wt, &["ls-files", "-v", "CLAUDE.md"]);
    assert!(flags.starts_with("S "), "{flags}");
    put(wt.join("CLAUDE.md"), "overwritten by the run");
    assert_eq!(git(wt, &["status", "--porcelain"]).trim(), "");
}

#[test]
fn a_work_dir_that_cannot_be_read_is_an_error_for_its_own_session() {
    let f = Fixture::new();
    let work = f.work_dir("amber-quiet-heron");
    mkdirs(work.parent().unwrap());
    put(&work, "not a directory");
    let error = f.adapter().scratch(Some("amber-quiet-heron")).unwrap_err();
    assert_eq!(error.code(), "worktree-git-unreadable");
}

#[test]
fn only_a_single_path_segment_names_a_session() {
    for bad in ["", ".", "..", "a/b", "a\\b"] {
        assert!(!is_name(bad), "{bad:?}");
    }
    assert!(is_name("amber-quiet-heron"));
    let f = Fixture::new();
    assert!(!f.adapter().session_known("..").unwrap());
    assert!(f.adapter().scratch(Some("..")).unwrap().is_empty());
}

#[test]
fn paths_are_the_same_when_equal_or_when_they_resolve_alike() {
    let dir = root();
    let real = dir.root().join("real");
    mkdirs(&real);
    let link_path = dir.root().join("link");
    link(&real, &link_path);
    let gone = dir.root().join("gone");
    assert!(
        same_path(&*fs(), &gone, &gone),
        "equal, though neither exists"
    );
    assert!(same_path(&*fs(), &real, &link_path));
    assert!(!same_path(&*fs(), &real, &gone));
}

#[test]
fn worktrees_nested_under_a_symlinked_target_are_found_by_their_resolved_paths() {
    let dir = root();
    let real = dir.root().join("real");
    mkdirs(&real);
    let link_path = dir.root().join("link");
    link(&real, &link_path);
    let listed = vec![
        Listed {
            path: real.clone(),
            branch: None,
        },
        Listed {
            path: real.join("inner"),
            branch: None,
        },
        Listed {
            path: dir.root().join("elsewhere"),
            branch: None,
        },
    ];
    assert_eq!(
        nested_under(&*fs(), &listed, &link_path),
        vec![real.join("inner")]
    );
}

#[test]
fn a_clean_nested_worktree_is_removed_with_its_parent() {
    let f = Fixture::new();
    let parent = f.prepared("amber-quiet-heron", "parent");
    let inner = Path::new(&parent.path).join("inner");
    git(
        Path::new(&parent.path),
        &[
            "worktree",
            "add",
            "-q",
            "-b",
            "inner-branch",
            &inner.display().to_string(),
        ],
    );
    assert_eq!(
        f.adapter().remove(&parent.repo_dir, &parent.path),
        Removal::Removed
    );
    assert!(!exists(&inner));
    let listed = git(&f.project, &["worktree", "list", "--porcelain"]);
    assert!(!listed.contains("inner"), "{listed}");
}

// S11 hardening: a hung git stops the run with a diagnostic, and never reads
// as "not a repository", "clean" or "removed".
mod hung_git {
    use std::time::Duration;

    use super::*;
    use crate::Git;

    /// A workspace whose git is a script that never finishes, with a tiny
    /// limit, over the fixture's home.
    fn hung(fixture: &Fixture) -> (file_system::Rooted, GitWorkspaces) {
        let dir = root();
        let path = dir.root().join("git");
        fixture_program::install_program(&*fs(), &path, "#!/bin/sh\nexec sleep 30\n").unwrap();
        let git = Git::new(
            fs(),
            path,
            Duration::from_millis(20),
            crate::fixtures::env(),
        );
        (dir, fixture.adapter().with_git(git))
    }

    #[test]
    fn preparing_with_a_hung_git_fails_with_the_timeout_not_a_degrade() {
        if !runs_scripts() {
            return;
        }
        let fixture = Fixture::new();
        let (_stub, workspaces) = hung(&fixture);
        let request = fixture.request("amber-quiet-heron", "member-1");
        let Err(failure) = workspaces.prepare(&request) else {
            panic!("a hung git was read as an answer");
        };
        assert_eq!(failure.code(), "worktree-git-git-timed-out");
        assert!(!exists(
            fixture
                .work_dir("amber-quiet-heron")
                .join(checkout_name("member-1"))
        ));
    }

    #[test]
    fn inspecting_with_a_hung_git_resolves_nothing() {
        if !runs_scripts() {
            return;
        }
        let fixture = Fixture::new();
        let (_stub, workspaces) = hung(&fixture);
        let standing = workspaces.inspect(&fixture.project.display().to_string());
        let Standing::Unresolved(why) = standing else {
            panic!("a hung git resolved a repository: {standing:?}");
        };
        assert!(why.contains("did not finish within 20ms"), "{why}");
    }

    #[test]
    fn removing_with_a_hung_git_leaves_the_checkout_in_place() {
        if !runs_scripts() {
            return;
        }
        let fixture = Fixture::new();
        let checkout = fixture.prepared("amber-quiet-heron", "member-1");
        let (_stub, workspaces) = hung(&fixture);
        let Removal::Left(why) = workspaces.remove(&checkout.repo_dir, &checkout.path) else {
            panic!("a hung git removed a checkout");
        };
        assert!(why.contains("did not finish within 20ms"), "{why}");
        assert!(is_dir(&checkout.path));
    }
}

// Owner ruling 2026-11-12 (Q3): what a launch carries that its engine reads
// only from where it works is placed in the checkout, hidden from git, and
// taken out again before the checkout is removed: excluded, it would read as
// work and keep the checkout (an ignored file is never safe).
#[test]
fn files_placed_in_a_checkout_are_hidden_and_taken_out_before_its_removal() {
    let f = Fixture::new();
    let mut request = f.request("amber-quiet-heron", "reviewer");
    request.artifact_patterns = vec![".claude/commands/".to_string()];
    let Ok(Prepared::Checkout(checkout)) = f.adapter().prepare(&request) else {
        panic!("no checkout");
    };
    let ws = f.adapter();
    let rel = ".claude/commands/demo-example.md".to_string();
    ws.place_files(&checkout.path, &[(rel.clone(), b"BODY".to_vec())])
        .unwrap();
    let placed = Path::new(&checkout.path).join(&rel);
    assert_eq!(slurp(&placed), "BODY");
    assert_eq!(
        git(Path::new(&checkout.path), &["status", "--porcelain"]).trim(),
        ""
    );
    assert!(matches!(
        ws.remove(&checkout.repo_dir, &checkout.path),
        Removal::Left(_)
    ));
    ws.remove_files(&checkout.path, &[rel]);
    assert!(!exists(&placed));
    assert_eq!(
        ws.remove(&checkout.repo_dir, &checkout.path),
        Removal::Removed
    );
}

// A path that is not plainly relative is refused, and nothing written.
#[test]
fn a_placed_file_may_not_climb_out_of_its_checkout() {
    let f = Fixture::new();
    let checkout = f.prepared("amber-quiet-heron", "reviewer");
    let refused = f.adapter().place_files(
        &checkout.path,
        &[("../escape.md".to_string(), b"x".to_vec())],
    );
    assert!(refused.is_err());
    assert!(!exists(Path::new(&checkout.path).join("../escape.md")));
}
