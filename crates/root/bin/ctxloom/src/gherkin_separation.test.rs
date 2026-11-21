//! The steps of the feature runner's own forcing test
//! (`gherkin_features::two_clusters_at_once_never_see_each_others_daemon_or_files`):
//! a scenario records the daemon its home is served by in a witness
//! directory the test names, and holds it serving until every other
//! scenario the test runs at once has recorded its own, asking its daemon
//! all the while which one answers. No feature file under `features/` uses
//! them.

use std::path::Path;
use std::time::{Duration, Instant};

use cucumber::{given, then};

use super::World;
use crate::tests::files;

/// The variable a client gives the daemon it spawns as its idle time.
const IDLE_VARIABLE: &str = "CTXLOOM_DAEMON_IDLE_SECONDS";

/// How long a witness waits for the others before failing: the scenarios
/// it waits for are started together, so this is only a bound, inside the
/// daemon's idle time (a minute).
const WAIT: Duration = Duration::from_secs(45);

#[given("the daemon a command starts idles for a minute")]
fn idles_long(w: &mut World) {
    w.common.set_env(IDLE_VARIABLE, "60".to_string());
}

/// `ctxloom args` now, with the scenario's environment.
fn run_now(w: &World, args: &[&str]) -> crate::tests::Ran {
    let env: Vec<(&str, &str)> = w
        .common
        .env
        .iter()
        .map(|(k, v)| (k.as_str(), v.as_str()))
        .collect();
    w.common.project.run_with(&env, args)
}

/// The daemon `daemon status` says serves this home, by its pid; none when
/// none does.
fn answering(w: &World) -> Option<u64> {
    let status = run_now(w, &["daemon", "status", "--format", "json"]);
    assert_eq!(status.code, 0, "{}", status.said());
    status.json()["daemons"][0]
        .get("pid")
        .and_then(serde_json::Value::as_u64)
}

/// The witnesses in `dir`: one file per daemon, named by its pid.
fn witnesses(dir: &Path) -> usize {
    files::children(dir)
        .iter()
        .filter(|p| p.extension().is_none())
        .count()
}

/// Record `<dir>/<pid>` (the home's discovery: its home and endpoint) for
/// the daemon serving this home, then keep asking the daemon which one
/// answers until `n` witnesses are in `dir` (every scenario of the test is
/// then serving at once), and record `<dir>/<pid>.seen` (`<pid of each
/// answer>`, one a line). The daemon is stopped last.
#[then(regex = r#"^the daemon serving this home is witnessed in "([^"]+)", until "(\d+)" are$"#)]
fn witnessed(w: &mut World, dir: String, n: usize) {
    let dir = Path::new(&dir).to_path_buf();
    let path = w
        .common
        .project
        .home()
        .join(".ctxloom/daemon/daemon.discovery");
    let bytes = files::read_bytes(&path).expect("a daemon advertises itself");
    let found = run_ports::Discovery::decode(&bytes).expect("a discovery file it can read");
    files::put(
        dir.join(found.pid.to_string()),
        format!(
            "{}\n{}\n{}\n",
            found.home,
            found.endpoint,
            w.common.project.home().display()
        ),
    );
    let mut seen = Vec::new();
    let deadline = Instant::now() + WAIT;
    loop {
        let answered = answering(w);
        seen.push(answered.map_or("none".to_string(), |pid| pid.to_string()));
        assert_eq!(
            answered,
            Some(u64::from(found.pid)),
            "this home's daemon answers it"
        );
        if witnesses(&dir) >= n {
            break;
        }
        assert!(
            Instant::now() < deadline,
            "the other scenarios never served at once with this one"
        );
        std::thread::sleep(Duration::from_millis(100));
    }
    // Once more, every daemon serving.
    let answered = answering(w);
    seen.push(answered.map_or("none".to_string(), |pid| pid.to_string()));
    files::put(
        dir.join(format!("{}.seen", found.pid)),
        seen.join("\n") + "\n",
    );
    let stopped = run_now(w, &["daemon", "stop"]);
    assert_eq!(stopped.code, 0, "{}", stopped.said());
}
