//! The user-settings domain (P3, owner ruling 2026-11-25): the user's own
//! engine settings files (claude's `settings.json`, opencode's config and
//! `model.json`) are one stream of their own, `users/me/settings`. A scan
//! is a FACT (`RecordSettingsObserved`, keyed by kind and content id), and
//! the aggregate records a `SettingsObserved` only when a file's content
//! differs from what the stream holds, so a launch reads the fold and says
//! the sequence it read. A fact is never refused: this domain has no
//! command and no refusal.

#[allow(dead_code)]
mod generated {
    include!(concat!(env!("OUT_DIR"), "/ctxloom.settings.v1.domain.rs"));
}
mod aggregate;
mod name;
mod state;

/// The aggregate's events, one variant per event type.
pub type SettingsEvent = UserSettingsStateEvent;

pub use aggregate::SettingsAggregate;
pub use generated::*;
pub use name::{settings_root, SETTINGS_NAME};
pub use state::{ObservedFile, UserSettings, SETTINGS_DOMAIN};
