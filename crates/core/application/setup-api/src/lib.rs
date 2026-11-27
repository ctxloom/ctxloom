//! The setup reads as the API says them (step 10c U5). The daemon's answers
//! and the CLI's read-back both go through [`wire`], which depends on the use
//! cases and the API's messages and on no frontend: a frontend depends on a
//! backend, never the reverse (owner ruling 2026-11-27).

pub mod wire;
