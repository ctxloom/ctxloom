//! What a drain reads and says, shared by the daemon (which times it and
//! tells its frontends) and the CLI's `ctxloom shutdown` (which asks for it).

use std::path::Path;
use std::sync::Arc;
use std::time::Duration;

use file_system::Fs;

/// What a live frontend's human is told when a drain starts (owner,
/// 2026-11-15, OD-63; a DRAFT wording): it will be stopped once `left` has
/// passed.
pub fn notice_text(left: Duration) -> String {
    format!(
        "ctxloom is shutting down: this session will be stopped in {}; finish and exit",
        duration_text::format_exact(left)
    )
}

/// Seconds since the epoch, on this host's clock.
pub fn epoch_seconds() -> u64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map_or(0, |since| since.as_secs())
}

/// The home config's `daemon.drain_timeout` (owner, 2026-11-15), read
/// afresh, as a command reads the home layer: none when it is not said, or
/// the config does not read.
pub fn configured_drain_timeout(fs: &Arc<dyn Fs>, home: &Path) -> Option<Duration> {
    let app = config_files::AppDir {
        path: home.join(".ctxloom"),
        source: ctxloom_ports::Source::Home,
        ignored_root: None,
    };
    let files = config_files::ConfigFilesFs::new(Arc::clone(fs), app, home);
    library_application::load_config(
        &files,
        &[],
        &library_application::Flags::default(),
        &mut Vec::new(),
    )
    .ok()?
    .drain_timeout()
}
