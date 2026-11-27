//! ctxloom's wiring for the command line: the CLI's composition, a CLIENT of
//! the daemon. What it shares with the daemon (the [`Wiring`], the ports over
//! their default adapters, the daemon contract) is `ctxloom-compose`'s, below
//! both halves and re-exported here, so a path through `ctxloom_wiring` still
//! names it; what is the command line's (the nouns, their services, `top`,
//! `shutdown`) is here.

pub mod commands;
pub mod daemon;
pub mod interview;
pub mod isolation;
pub mod library;
pub mod mcp;
pub mod sessions;
#[cfg(feature = "test-support")]
pub mod test_commands;
pub mod top;

pub use ctxloom_compose::{
    capture, checkout, compose, error, frontend, frontend_asks, hook_asks, hook_library,
    hook_messages, library_build, oneshot, process_env, program, rebind, root_flag, setup, signals,
    sweep_config,
};
pub use ctxloom_compose::{
    checkout_file, checkout_harp, checkout_tree, daemon_sink, engines, fresh_project_harps,
    mint_checkout_harp, registry, run_id, session_name, Deadline, EdgeSink, HoldingRecording,
    HookReach, HookRecording, HookSink, LibError, Minting, ProcessEnv, RunIds, SessionIds,
    Sessions, SweepSettings, Unreachable, Wiring, PROJECT_MARKER,
};
#[cfg(feature = "test-support")]
pub use ctxloom_compose::{store_verb, test_error};

/// One value of every diagnostic of the shared composition and of this
/// crate's `ctxloom shutdown`.
#[cfg(any(test, feature = "test-support"))]
pub fn diagnostic_samples() -> Vec<Box<dyn coded_diagnostic::Diagnostic>> {
    let mut all = ctxloom_compose::diagnostic_samples();
    all.extend(daemon::shutdown::samples());
    all
}
pub use commands::{Commands, Hooks};
#[cfg(feature = "test-support")]
pub use ctxloom_compose::{SeedArgs, StoreTest, StoreVerb, TestError};
pub use library::LibraryCommands;
#[cfg(feature = "test-support")]
pub use test_commands::Samples;

/// This crate's en-US messages (`ctxloom shutdown`'s).
pub const EN_US: (&str, &str) = (
    env!("CARGO_PKG_NAME"),
    include_str!("../locales/en-US/messages.ftl"),
);

/// Every catalog the binary's diagnostics are rendered from: the shared
/// composition's (angzarr's, the platform's, the adapters' and use cases'),
/// the CLI's own and its frontends'.
pub fn catalog_sources() -> Vec<presentation::Source> {
    let mut sources = ctxloom_compose::catalog_sources();
    sources.extend([
        EN_US,
        mcp_server::EN_US,
        container_cli::EN_US,
        run_cli::EN_US,
    ]);
    sources
}

#[cfg(any(test, feature = "test-support"))]
#[path = "fixtures.test.rs"]
pub mod fixtures;
