//! Go's `companions.Verify` (`companions.go`): the probe is
//! `<bin> loadout --format yaml`; it must succeed and print something.

use std::sync::Arc;
use std::time::Instant;

use file_system::{Fs, Rooted};

use super::*;

/// Whether this host runs the tests' fake programs: unix, the one flavor
/// `fixture_program::install_program` does not refuse as `Unsupported`, with
/// the `sh` (and links) they need. Elsewhere the asking test is skipped and
/// says so on stderr, `SKIP <test>: <why>`, never silently; the same test
/// runs unchanged on unix.
pub(crate) fn runs_scripts() -> bool {
    if os::host().flavor() == os::Flavor::Unix {
        return true;
    }
    let test = std::thread::current();
    eprintln!(
        "SKIP {}: its fake programs are sh scripts (unix only)",
        test.name().unwrap_or("a test")
    );
    false
}

/// The host's own filesystem, where a test's programs are.
fn host_fs() -> Arc<dyn Fs> {
    let host = os::host();
    let top = std::path::Path::new(host.flavor().top()).to_path_buf();
    Arc::new(file_system::Os::new(host, &top).unwrap())
}

fn probe_for(limit: Duration) -> ProcessProbe {
    ProcessProbe::new(limit, host_fs(), os::host().flavor())
}

fn probe() -> ProcessProbe {
    probe_for(STANDARD_LIMIT)
}

/// A program `ctxloom-companion-x` that runs `body`, in a `bin` directory of
/// its own, and the site whose `PATH` is that directory (and the system's).
fn script(body: &str) -> (Rooted, ProbeSite) {
    let dir = Rooted::temp().unwrap();
    dir.create_dir_all(&os::fixture("/bin"), os::Mode::DIR)
        .unwrap();
    let program =
        fixture_program::install_program(&dir, &os::fixture("/bin/ctxloom-companion-x"), body)
            .unwrap();
    let bin = program.parent().unwrap().to_str().unwrap().to_string();
    let site = ProbeSite {
        env: vec![
            ("PATH".to_string(), format!("{bin}:/usr/bin:/bin")),
            ("MARK".to_string(), "seen".to_string()),
        ],
        cwd: dir.root().to_str().unwrap().to_string(),
    };
    (dir, site)
}

const X: &str = "ctxloom-companion-x";

#[test]
fn test_a_program_that_prints_a_loadout_is_loaded() {
    if !runs_scripts() {
        return;
    }
    let (_dir, site) = script("#!/bin/sh\nprintf 'run:\\n  version: 1.0.0\\n'\n");
    assert_eq!(
        probe().loadout(X, &site),
        CompanionLoadout::Loaded("run:\n  version: 1.0.0\n".to_string())
    );
}

#[test]
fn test_the_probe_is_loadout_format_yaml() {
    if !runs_scripts() {
        return;
    }
    let (dir, site) = script("#!/bin/sh\necho \"$@\" > \"$(dirname \"$0\")/args\"\necho x\n");
    probe().loadout(X, &site);
    assert_eq!(
        dir.read_to_string(&os::fixture("/bin/args")).unwrap(),
        "loadout --format yaml\n"
    );
}

// OD-10: a companion is found by name on the site's PATH, every time; a
// program of the same name earlier on that PATH is the one that runs.
#[test]
fn test_the_program_is_found_on_the_sites_path_and_the_earlier_one_runs() {
    if !runs_scripts() {
        return;
    }
    let (dir, site) = script("#!/bin/sh\necho later\n");
    dir.create_dir_all(&os::fixture("/early"), os::Mode::DIR)
        .unwrap();
    let early = fixture_program::install_program(
        &dir,
        &os::fixture("/early/ctxloom-companion-x"),
        "#!/bin/sh\necho early\n",
    )
    .unwrap();
    let early_dir = early.parent().unwrap().to_str().unwrap().to_string();
    let mut first = site.clone();
    first.env[0].1 = format!("{early_dir}:{}", site.env[0].1);
    assert_eq!(
        probe().find(X, &first),
        Some(early.to_str().unwrap().to_string())
    );
    assert_eq!(
        probe().loadout(X, &first),
        CompanionLoadout::Loaded("early\n".to_string())
    );
    assert_eq!(
        probe().loadout(X, &site),
        CompanionLoadout::Loaded("later\n".to_string())
    );
}

// OD-10: a registration names a program, never a path.
#[test]
fn test_a_path_is_no_program_name_and_is_not_found() {
    if !runs_scripts() {
        return;
    }
    let (_dir, site) = script("#!/bin/sh\necho x\n");
    let bin = site.env[0].1.split(':').next().unwrap().to_string();
    let path = format!("{bin}/{X}");
    assert_eq!(probe().find(&path, &site), None);
    assert_eq!(probe().loadout(&path, &site), CompanionLoadout::Absent);
    assert_eq!(probe().find("", &site), None);
}

#[test]
fn test_a_program_not_on_the_sites_path_is_absent_and_never_run() {
    let site = ProbeSite {
        env: vec![("PATH".to_string(), "/no/such/dir".to_string())],
        cwd: String::new(),
    };
    assert_eq!(probe().find(X, &site), None);
    assert_eq!(probe().loadout(X, &site), CompanionLoadout::Absent);
}

#[test]
fn test_a_program_that_prints_nothing_is_silent() {
    if !runs_scripts() {
        return;
    }
    let (_dir, site) = script("#!/bin/sh\nexit 0\n");
    assert_eq!(probe().loadout(X, &site), CompanionLoadout::Silent);
    let (_dir, site) = script("#!/bin/sh\necho\n");
    assert_eq!(
        probe().loadout(X, &site),
        CompanionLoadout::Loaded("\n".to_string()),
        "a blank line is output"
    );
}

#[test]
fn test_a_program_that_exits_with_a_status_offers_no_loadout() {
    if !runs_scripts() {
        return;
    }
    let (_dir, site) = script("#!/bin/sh\necho something\nexit 3\n");
    let CompanionLoadout::NoLoadout(detail) = probe().loadout(X, &site) else {
        panic!("should offer none");
    };
    assert!(detail.contains("exit status: 3"), "{detail}");
}

#[test]
fn test_a_program_that_takes_too_long_is_killed_at_the_limit() {
    if !runs_scripts() {
        return;
    }
    let (dir, site) = script("#!/bin/sh\nsleep 20\n");
    let started = Instant::now();
    let CompanionLoadout::ProbeFailed(detail) =
        probe_for(Duration::from_millis(200)).loadout(X, &site)
    else {
        panic!("should fail");
    };
    assert!(detail.contains("did not answer within"), "{detail}");
    assert!(
        started.elapsed() < Duration::from_secs(10),
        "it waited for the program"
    );
    drop(dir);
}

#[test]
fn test_a_program_with_a_great_deal_to_say_does_not_hang_the_probe() {
    if !runs_scripts() {
        return;
    }
    let (_dir, site) = script("#!/bin/sh\nyes | head -c 5000000\n");
    assert!(matches!(
        probe().loadout(X, &site),
        CompanionLoadout::Loaded(_)
    ));
}

#[test]
fn test_a_helper_the_program_started_is_killed_with_it() {
    if !runs_scripts() {
        return;
    }
    // The helper holds the program's pipe, as a real one would; the limit is
    // long enough for the script to have started it.
    let (dir, site) =
        script("#!/bin/sh\nsleep 30 &\necho $! > \"$(dirname \"$0\")/helper\"\nwait\n");
    // A loaded machine may not have started the helper by the first limit:
    // ask again with a longer one until it has.
    let started = Instant::now();
    for secs in [1, 5, 25] {
        let asked = probe_for(Duration::from_secs(secs)).loadout(X, &site);
        assert!(
            matches!(asked, CompanionLoadout::ProbeFailed(_)),
            "{asked:?}"
        );
        if dir.exists(&os::fixture("/bin/helper")).unwrap() {
            break;
        }
    }
    assert!(started.elapsed() < Duration::from_secs(40));
    let pid = dir.read_to_string(&os::fixture("/bin/helper")).unwrap();
    assert!(
        gone_soon(pid.trim()),
        "the helper {pid} outlived the program"
    );
}

/// Whether the killed process `pid` is gone within ten seconds. A run past
/// its limit sends SIGKILL to the program's whole process group and reaps the
/// program, not its descendants: a helper dies when the kernel next runs it,
/// which can be just after the run returned (it was caught in state `R`), so
/// one look at that instant can see a helper that is already doomed. The
/// bounded look is forced in taskloom-cli-client's
/// `a_helper_seen_alive_before_its_kill_lands_is_gone_within_the_bound`.
fn gone_soon(pid: &str) -> bool {
    let until = std::time::Instant::now() + std::time::Duration::from_secs(10);
    while !run_within::process_gone(pid) {
        if std::time::Instant::now() >= until {
            return false;
        }
        std::thread::sleep(std::time::Duration::from_millis(1));
    }
    true
}

/// A helper that holds the program's pipes after it exits, until the file
/// `release` appears beside the script (or 30 seconds, its own bound), its
/// pid in `helper`. A program that exits normally is not killed with what it
/// started (run-within: the descendant may be meant to stay), so the test
/// lets it go itself.
const HELD: &str = "d=$(dirname \"$0\")\n\
    ( i=0; while [ ! -e \"$d/release\" ] && [ \"$i\" -lt 3000 ]; do sleep 0.01; i=$((i+1)); done ) &\n\
    echo $! > \"$d/helper\"\n";

/// Lets a [`HELD`] helper go when dropped, whether the test passed or not.
struct Release<'a>(&'a Rooted);

impl Drop for Release<'_> {
    fn drop(&mut self) {
        let _ = self.0.write_atomic(
            &os::fixture("/bin/release"),
            b"",
            file_system::Perms::Exact(os::Mode::FILE),
        );
    }
}

#[test]
fn test_a_helper_that_outlives_the_program_does_not_hold_the_probe() {
    if !runs_scripts() {
        return;
    }
    let (dir, site) = script(&format!("#!/bin/sh\n{HELD}printf 'a:\\n  b: 1\\n'\n"));
    let release = Release(&dir);
    let probe = probe_for(Duration::from_secs(30)).drain_grace(Duration::from_millis(50));
    let started = Instant::now();
    assert_eq!(
        probe.loadout(X, &site),
        CompanionLoadout::Loaded("a:\n  b: 1\n".to_string())
    );
    assert!(started.elapsed() < Duration::from_secs(4));
    let pid = dir.read_to_string(&os::fixture("/bin/helper")).unwrap();
    assert!(
        !run_within::process_gone(pid.trim()),
        "the helper held the pipes"
    );
    drop(release);
    assert!(gone_soon(pid.trim()), "the helper {pid} outlived the test");
}

// D1: the program runs in exactly the site's environment, given with the
// call: nothing of the probe's own or the test process's.
#[test]
fn test_the_program_runs_in_exactly_the_sites_environment() {
    if !runs_scripts() {
        return;
    }
    let (_dir, site) = script("#!/bin/sh\necho \"${MARK-unset} ${OTHER-unset}\"\n");
    assert_eq!(
        probe().loadout(X, &site),
        CompanionLoadout::Loaded("seen unset\n".to_string())
    );
    let mut other = site.clone();
    other.env[1].1 = "changed".to_string();
    assert_eq!(
        probe().loadout(X, &other),
        CompanionLoadout::Loaded("changed unset\n".to_string()),
        "each call's own environment"
    );
}

// D1 and the verified gap: the program runs in the site's directory, the
// caller's project root, not the probing process's.
#[test]
fn test_the_program_runs_in_the_sites_directory() {
    if !runs_scripts() {
        return;
    }
    let (dir, mut site) = script("#!/bin/sh\npwd -P\n");
    dir.create_dir_all(&os::fixture("/project"), os::Mode::DIR)
        .unwrap();
    let project = dir.real_path(&os::fixture("/project")).unwrap();
    site.cwd = project.to_str().unwrap().to_string();
    assert_eq!(
        probe().loadout(X, &site),
        CompanionLoadout::Loaded(format!("{}\n", project.display()))
    );
}
