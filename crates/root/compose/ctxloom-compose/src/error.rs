//! Why the wiring could not do what it was asked.

use std::fmt;

use coded_diagnostic::{code, Arg, Diagnostic, Report};

/// This crate's en-US messages.
pub const EN_US: (&str, &str) = (
    env!("CARGO_PKG_NAME"),
    include_str!("../locales/en-US/messages.ftl"),
);

/// Open for extension: a new variant is not a breaking change.
#[derive(Clone, PartialEq, Eq, Debug)]
#[non_exhaustive]
pub enum LibError {
    /// The home variable is not set (or is empty).
    NoHome { variable: String },
    /// A session id that is not a UUID.
    BadSession { session: String, detail: String },
    /// No runtime could be started to open the store.
    NoRuntime { detail: String },
    /// An answer that could not be written out.
    Output { detail: String },
    /// The project could not be named (S54).
    Project(Report),
    /// The user's one daemon serves another ctxloom home than this
    /// command's: one daemon per OS user, never a second.
    DaemonOtherHome { home: String, daemon_home: String },
    /// A dry run's names in a ctxloom project nothing has named yet: a dry
    /// run never mints a project id (owner ruling 2026-11-14), so it is
    /// refused and writes nothing. (A command that must name its project
    /// mints the id at its root instead.)
    ProjectUnnamed { root: String },
    /// No project root: no `.ctxloom` of its own, no git root, and no
    /// `--root` or `CTXLOOM_ROOT` (the user's home is never taken for one).
    ProjectRootNeeded { cwd: String },
    /// `daemon serve` found another daemon holding the account's
    /// single-instance lock: that one serves, and this one does not.
    DaemonInstanceHeld { lock: String },
    /// A daemon holds the single-instance lock and does not answer its
    /// health check (hung): no other can serve until it is stopped. `pid` is
    /// the discovery file's. A decline (48), as the lock
    /// held is: the state forbids a daemon, nothing a retry changes.
    DaemonHung { pid: u32, lock: String },
    /// A library tree's checkout harp (`.ctxloom/state/checkout`, L2) could
    /// not be read, minted or written.
    Checkout { path: String, detail: String },
    /// A library tree could not be walked or a file of it read (step 10c
    /// U13 rework, the scanner).
    Scan { path: String, detail: String },
}

impl cli_support::ExitClass for LibError {
    /// A read in a project nothing has named, and a daemon that found
    /// another holding the lock, are declines (48); every other wiring error
    /// failed (32).
    fn exit(&self) -> cli_support::Exit {
        match self {
            LibError::ProjectUnnamed { .. }
            | LibError::ProjectRootNeeded { .. }
            | LibError::DaemonInstanceHeld { .. }
            | LibError::DaemonHung { .. } => cli_support::Exit::REFUSED,
            _ => cli_support::Exit::FAILED,
        }
    }
}

impl fmt::Display for LibError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        coded_diagnostic::display(self, f)
    }
}

impl std::error::Error for LibError {}

impl Diagnostic for LibError {
    fn code(&self) -> &str {
        match self {
            LibError::NoHome { .. } => code!("no-home"),
            LibError::BadSession { .. } => code!("bad-session"),
            LibError::NoRuntime { .. } => code!("no-runtime"),
            LibError::Output { .. } => code!("output"),
            LibError::Project(cause) => cause.code(),
            LibError::DaemonOtherHome { .. } => code!("daemon-other-home"),
            LibError::ProjectUnnamed { .. } => code!("project-unnamed"),
            LibError::ProjectRootNeeded { .. } => code!("project-root-needed"),
            LibError::Checkout { .. } => code!("checkout"),
            LibError::Scan { .. } => code!("scan"),
            LibError::DaemonInstanceHeld { .. } => code!("daemon-instance-held"),
            LibError::DaemonHung { .. } => code!("daemon-hung"),
        }
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        match self {
            LibError::NoHome { variable } => vec![("variable", variable.as_str().into())],
            LibError::BadSession { session, detail } => vec![
                ("session", session.as_str().into()),
                ("ext_err_txt", detail.as_str().into()),
            ],
            LibError::NoRuntime { detail } => vec![("ext_err_txt", detail.as_str().into())],
            LibError::Output { detail } => vec![("ext_err_txt", detail.as_str().into())],
            LibError::Project(cause) => cause.args(),
            LibError::DaemonOtherHome { home, daemon_home } => vec![
                ("home", home.as_str().into()),
                ("daemon_home", daemon_home.as_str().into()),
            ],
            LibError::ProjectUnnamed { root } => vec![("root", root.as_str().into())],
            LibError::ProjectRootNeeded { cwd } => vec![("cwd", cwd.as_str().into())],
            LibError::Checkout { path, detail } | LibError::Scan { path, detail } => vec![
                ("path", path.as_str().into()),
                ("ext_err_txt", detail.as_str().into()),
            ],
            LibError::DaemonInstanceHeld { lock } => vec![("lock", lock.as_str().into())],
            LibError::DaemonHung { pid, lock } => vec![
                ("pid", pid.to_string().into()),
                ("lock", lock.as_str().into()),
            ],
        }
    }
}

/// One value of every error here, for a catalog test.
#[cfg(any(test, feature = "test-support"))]
pub fn diagnostic_samples() -> Vec<Box<dyn Diagnostic>> {
    let samples: Vec<Box<dyn Diagnostic>> = vec![
        Box::new(LibError::NoHome {
            variable: "HOME".to_string(),
        }),
        Box::new(LibError::BadSession {
            session: "x".to_string(),
            detail: "invalid length".to_string(),
        }),
        Box::new(LibError::Output {
            detail: "broken pipe".to_string(),
        }),
        Box::new(LibError::NoRuntime {
            detail: "too many open files".to_string(),
        }),
        Box::new(LibError::DaemonOtherHome {
            home: "/home/u/.ctxloom-other".to_string(),
            daemon_home: "/home/u".to_string(),
        }),
        Box::new(LibError::ProjectRootNeeded {
            cwd: "/home/u/newproj".to_string(),
        }),
        Box::new(LibError::ProjectUnnamed {
            root: "/work/app".to_string(),
        }),
        Box::new(LibError::DaemonInstanceHeld {
            lock: "/home/u/.ctxloom/daemon/daemon.lock".to_string(),
        }),
        Box::new(LibError::DaemonHung {
            pid: 4242,
            lock: "/home/u/.ctxloom/daemon/daemon.lock".to_string(),
        }),
        Box::new(LibError::Checkout {
            path: "/work/app/.ctxloom/state/checkout".to_string(),
            detail: "permission denied".to_string(),
        }),
        Box::new(LibError::Scan {
            path: "/work/app/.ctxloom".to_string(),
            detail: "permission denied".to_string(),
        }),
        Box::new(crate::daemon::flush::HeldSetAside {
            session: "brisk-copper-moth".to_string(),
            count: 2,
            why: "the daemon refused it".to_string(),
        }),
    ];
    samples
}

#[cfg(test)]
#[path = "error.test.rs"]
mod tests;
