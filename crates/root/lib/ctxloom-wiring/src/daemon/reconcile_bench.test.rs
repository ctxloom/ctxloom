//! The observation's bench (step 10a U2; plan O7; step 10c U13 rework: the
//! scanner): a read's scan, as the daemon makes it (`CallerGits`' scanner,
//! its memo warm, then compared with the record's listing), on a repository
//! of 100 000 files whose `.ctxloom` holds 500, its p95 held to the budget,
//! 50 ms per observation, for a read that sees nothing new and for one that
//! sees a hand edit.
//!
//! It builds its repository (about half a minute), so it runs only when
//! asked: `just bench-reconcile` (`LIBRARY_BENCH=reconcile`); any other
//! run says SKIP and passes. It prints each case's p50, p95 and max.

use std::path::Path;
use std::time::{Duration, Instant};

use library_api::Observers;
use library_tree::Library;

use crate::fixtures;

/// The budget (O7).
const BUDGET: Duration = Duration::from_millis(50);
/// The repository's files outside the library, and in it.
const OUTSIDE: usize = 100_000;
const LIBRARY: usize = 500;
/// The reconciles timed per case.
const ROUNDS: usize = 40;

fn path() -> String {
    crate::tests::host_snapshot()
        .utf8("PATH")
        .unwrap_or_default()
        .to_string()
}

fn git(dir: &Path, args: &[&str]) {
    let output = std::process::Command::new("git")
        .args([
            "-c",
            "user.name=Bench",
            "-c",
            "user.email=bench@example.invalid",
        ])
        .args([
            "-c",
            "commit.gpgsign=false",
            "-c",
            "init.defaultBranch=main",
        ])
        .args(args)
        .current_dir(dir)
        .env_clear()
        .env("PATH", path())
        .env("HOME", dir)
        .output()
        .expect("git runs");
    assert!(
        output.status.success(),
        "git {args:?}: {}",
        String::from_utf8_lossy(&output.stderr)
    );
}

/// `count` files under `dir`, a hundred to a directory.
fn files(dir: &Path, count: usize, body: &str) {
    for n in 0..count {
        let sub = dir.join(format!("d{:04}", n / 100));
        if n % 100 == 0 {
            fixtures::mkdirs(&sub);
        }
        fixtures::put_mode(
            sub.join(format!("f{n}.md")),
            &format!("{body} {n}\n"),
            0o644,
        );
    }
}

/// The record's state after `fact` (what the aggregate folds of a sight).
fn held_after(listed: std::collections::BTreeMap<String, String>) -> Library {
    Library {
        dirty: listed,
        last_cause: "bench".to_string(),
        baselined: true,
        ..Library::default()
    }
}

/// p50, p95 and the max of `times`.
fn quantiles(mut times: Vec<Duration>) -> (Duration, Duration, Duration) {
    times.sort();
    let at = |q: f64| times[((times.len() as f64 * q).ceil() as usize).saturating_sub(1)];
    (at(0.50), at(0.95), *times.last().expect("timed"))
}

#[test]
fn a_reconcile_on_a_large_repository_is_within_its_budget() {
    let asked = crate::tests::host_snapshot()
        .utf8("LIBRARY_BENCH")
        .map(str::to_string);
    if asked.as_deref() != Some("reconcile") {
        eprintln!("SKIP a_reconcile_on_a_large_repository_is_within_its_budget: run it with `just bench-reconcile`");
        return;
    }
    let root = fixtures::root();
    let repo = root.root().join("repo");
    files(&repo.join("src"), OUTSIDE, "outside");
    let bundles = repo.join(".ctxloom/content/bundles/v2/big");
    files(&bundles.join("fragments"), LIBRARY - 1, "fragment");
    fixtures::put_mode(
        repo.join(".ctxloom/config.yaml"),
        "schema_version: 9\n",
        0o644,
    );
    git(&repo, &["init", "-q"]);
    git(&repo, &["add", "-A"]);
    git(&repo, &["commit", "-qm", "bench"]);
    let gits = super::CallerGits {
        fs: fixtures::fs(),
        home: root.root().join("home"),
        scanner: std::sync::Arc::new(super::TreeScanner::new(fixtures::fs())),
    };
    let scanner = gits.scanner();
    let tree = repo.join(".ctxloom").display().to_string();
    let held = held_after(scanner.scan(&tree).unwrap());
    let time = |held: &Library, expect_change: bool| {
        (0..ROUNDS)
            .map(|_| {
                let started = Instant::now();
                let found = scanner.scan(&tree).unwrap();
                let seen = !library_api::listed_as_held(held, &found);
                let took = started.elapsed();
                assert_eq!(seen, expect_change);
                took
            })
            .collect::<Vec<_>>()
    };
    let quiet = quantiles(time(&held, false));
    fixtures::put_mode(
        bundles.join("fragments/d0000/f7.md"),
        "edited by hand\n",
        0o644,
    );
    let edited = quantiles(time(&held, true));
    for (case, (p50, p95, max)) in [("nothing new", quiet), ("a hand edit", edited)] {
        eprintln!(
            "bench reconcile ({OUTSIDE} files, {LIBRARY} in .ctxloom), {case}: p50 {p50:?}, p95 {p95:?}, max {max:?} over {ROUNDS}"
        );
        assert!(
            p95 < BUDGET,
            "{case}: p95 {p95:?} is over the budget {BUDGET:?}"
        );
    }
}
