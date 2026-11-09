use std::path::{Path, PathBuf};
use tokio::net::{UnixListener, UnixStream};

use file_system::Fs;

use super::clear_stale;

/// A real directory for sockets, under a short temporary path.
fn dir(name: &str) -> (file_system::Os, PathBuf) {
    let fs = file_system::Os::new(os::host(), Path::new("/")).unwrap();
    let dir = PathBuf::from(format!("/tmp/ctxloom-ss-{}-{name}", std::process::id()));
    fs.create_dir_all(&dir, os::Mode::from_bits(0o700)).unwrap();
    (fs, dir)
}

// Live probe (2026-11-09): runs killed by SIGHUP or a timeout left their
// sockets behind (four of them under /run/user/1000/ctxloom).
#[tokio::test]
async fn every_socket_nothing_accepts_on_is_removed_from_the_runs_directory() {
    let (fs, dir) = dir("stale");
    let (one, two) = (dir.join("a.sock"), dir.join("b.sock"));
    drop(UnixListener::bind(&one).unwrap());
    drop(UnixListener::bind(&two).unwrap());
    assert!(fs.exists(&one).unwrap(), "the run left it");
    // The run's own path, a socket that is not there yet.
    assert_eq!(clear_stale(&fs, &dir.join("own.sock")).await.unwrap(), 2);
    assert!(!fs.exists(&one).unwrap() && !fs.exists(&two).unwrap());
    fs.remove_dir_all(&dir).unwrap();
}

#[tokio::test]
async fn a_socket_a_run_holds_is_never_removed() {
    let (fs, dir) = dir("live");
    let path = dir.join("a.sock");
    let listener = UnixListener::bind(&path).unwrap();
    assert_eq!(clear_stale(&fs, &dir.join("own.sock")).await.unwrap(), 0);
    assert!(fs.exists(&path).unwrap());
    assert!(UnixStream::connect(&path).await.is_ok(), "still serving");
    drop(listener);
    fs.remove_dir_all(&dir).unwrap();
}

#[tokio::test]
async fn only_sockets_named_sock_are_taken_and_a_missing_directory_has_none() {
    let (fs, dir) = dir("other");
    let perms = file_system::Perms::Exact(os::Mode::from_bits(0o600));
    fs.write_atomic(&dir.join("plain.sock"), b"mine", perms)
        .unwrap();
    drop(UnixListener::bind(dir.join("kept.txt")).unwrap());
    assert_eq!(clear_stale(&fs, &dir.join("own.sock")).await.unwrap(), 0);
    assert_eq!(fs.read(&dir.join("plain.sock")).unwrap(), b"mine");
    assert!(fs.exists(&dir.join("kept.txt")).unwrap());
    assert_eq!(
        clear_stale(&fs, &dir.join("gone/own.sock")).await.unwrap(),
        0
    );
    fs.remove_dir_all(&dir).unwrap();
}
