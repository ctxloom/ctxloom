//! Run sockets that no run holds: what a run killed by SIGHUP or a timeout
//! leaves behind in the sockets' directory.
//!
//! A run's socket path is made from the run's own id, so a later run never
//! lands on an earlier one's path: the leftovers are cleared from the
//! directory the new run's socket goes in, which is the one place a run
//! knows to look.

use std::io;
use std::path::Path;

use file_system::{Fs, Kind};
use tokio::net::UnixStream;

/// Remove, from the directory of `endpoint`, every `*.sock` socket nothing
/// accepts on. How many were removed. Never a live socket (a connection is
/// made), never a file that is not a socket or is not named `*.sock`, nothing
/// outside that directory; a directory that is not there has none.
pub(crate) async fn clear_stale(fs: &dyn Fs, endpoint: &Path) -> io::Result<usize> {
    let Some(dir) = endpoint.parent() else {
        return Ok(0);
    };
    let entries = match fs.read_dir(dir) {
        Ok(entries) => entries,
        Err(e) if e.kind() == io::ErrorKind::NotFound => return Ok(0),
        Err(e) => return Err(e),
    };
    let mut removed = 0;
    for entry in entries {
        let path = dir.join(&entry.name);
        let named = path.extension().is_some_and(|ext| ext == "sock");
        if entry.kind != Kind::Other || !named || path == endpoint {
            continue;
        }
        // Nothing accepting is a refused connection; anything else (a
        // connection, a permission, a file that is no socket) leaves it.
        let refused = matches!(
            UnixStream::connect(&path).await,
            Err(e) if e.kind() == io::ErrorKind::ConnectionRefused
        );
        if refused {
            fs.remove_file(&path)?;
            removed += 1;
        }
    }
    Ok(removed)
}

#[cfg(test)]
#[path = "stale_socket.test.rs"]
mod tests;
