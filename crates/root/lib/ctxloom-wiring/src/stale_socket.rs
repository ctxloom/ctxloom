//! Run sockets that no run holds: what a run killed by SIGHUP or a timeout
//! leaves behind in the sockets' directory.
//!
//! A run's socket path is made from the run's own id, so a later run never
//! lands on an earlier one's path: the leftovers are cleared from the
//! directory the new run's socket goes in, which is the one place a run
//! knows to look.

use std::io;
use std::path::Path;

use file_system::Fs;

/// Remove, from the directory of `endpoint`, every `*.sock` socket nothing
/// accepts on. How many were removed. Never a live socket (a connection is
/// made), never a file that is not a socket or is not named `*.sock`, nothing
/// outside that directory; a directory that is not there has none.
pub(crate) async fn clear_stale(_fs: &dyn Fs, _endpoint: &Path) -> io::Result<usize> {
    Ok(0)
}

#[cfg(test)]
#[path = "stale_socket.test.rs"]
mod tests;
