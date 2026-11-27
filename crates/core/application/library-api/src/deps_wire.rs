//! The deps writes and checks as the API says them (OD-40, B4; design §5.10):
//! the Dependency resource and the custom methods pull, check and upgrade, a
//! remote's writes and the project refresh a pull skipped, as their
//! `ctxloom.v1` messages and back. The daemon says its answers in these and
//! the CLI reads them back and prints them: a backend crate, naming no
//! frontend (owner ruling 2026-11-27).

pub mod api;
pub mod dependency;
pub mod refresh;
pub mod remote;
