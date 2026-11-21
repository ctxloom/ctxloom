//! The host's processes and advisory locks as the acceptance tests ask after
//! them, the same way on every unix: neither through `/proc` (Linux's alone)
//! nor `flock(1)` (util-linux's, which macOS has not), so a test states on
//! macOS what it states on Linux.

use std::fs::TryLockError;
use std::path::Path;

use crate::tests::files;

/// A program body (for [`files::put_program`]) that takes the advisory lock
/// on the file `$1` (made if missing), waiting at most `wait_seconds` for it
/// (none: it must be free now), and holds it until a file `$2` is there or
/// `tries` tenths of a second have passed; it exits 1, holding nothing, when
/// the lock was not had. The lock is `flock(2)`, the platform's
/// (`file-lock`'s), taken by perl, which macOS and Linux both have. The
/// process started is the holder: killed, it lets the lock go.
pub(crate) fn holds_lock(wait_seconds: u32, tries: u32) -> String {
    r#"exec perl -MFcntl=:flock -e '
my ($lock, $until, $wait, $tries) = @ARGV;
open(my $held, ">>", $lock) or die "$lock: $!\n";
my $end = time + $wait;
until (flock($held, LOCK_EX | LOCK_NB)) {
  exit 1 if time >= $end;
  select(undef, undef, undef, 0.05);
}
for (1 .. $tries) { last if -e $until; select(undef, undef, undef, 0.1); }
' "$1" "$2" WAIT TRIES
"#
    .replace("WAIT", &wait_seconds.to_string())
    .replace("TRIES", &tries.to_string())
}

/// A shell condition, for a fake program, true while the lock on the file
/// named by the shell word `lock` is free (made if missing): taken and let go
/// at once, as `flock -n <lock> true` would.
pub(crate) fn lock_free_condition(lock: &str) -> String {
    format!(
        r#"perl -MFcntl=:flock -e 'open(my $f, ">>", $ARGV[0]) or exit 2; exit(flock($f, LOCK_EX | LOCK_NB) ? 0 : 1)' {lock}"#
    )
}

/// Whether the advisory lock on `lock` (made if missing) is held by another
/// process now: this one cannot take it.
pub(crate) fn lock_held(lock: &Path) -> bool {
    let file = files::open_append(lock).expect("the lock file opens");
    match file.try_lock() {
        Ok(()) => false,
        Err(TryLockError::WouldBlock) => true,
        Err(TryLockError::Error(e)) => panic!("{}: {e}", lock.display()),
    }
}

/// Whether process `pid` is there and not a zombie (exited, not yet reaped
/// by its parent): `ps`'s state, which macOS and Linux both report, where
/// `kill -0` answers yes for a zombie.
pub(crate) fn alive(pid: u64) -> bool {
    let ps = std::process::Command::new("ps")
        .args(["-o", "stat=", "-p", &pid.to_string()])
        .stderr(std::process::Stdio::null())
        .output()
        .expect("ps runs");
    let state = String::from_utf8_lossy(&ps.stdout);
    let state = state.trim();
    ps.status.success() && !state.is_empty() && !state.starts_with('Z')
}
