//! The setup writes as the API says them (step 10c U10). The daemon's answers
//! and the CLI's read-back both go through [`wire`], which depends on the use
//! cases and the API's messages and on no frontend (owner ruling 2026-11-27:
//! a backend never depends on a frontend crate).

pub mod wire;
