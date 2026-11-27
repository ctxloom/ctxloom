//! The CLI's half of the daemon commands, beside the shared half
//! (`ctxloom-compose`'s `daemon`, re-exported here so every path
//! `ctxloom_wiring::daemon::...` the commands name is still one).

pub mod cli;
pub mod shutdown;
pub mod watch;

pub use ctxloom_compose::daemon::*;
