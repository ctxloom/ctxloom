//! The user's settings, folded from their events.

use std::collections::BTreeMap;

use crate::{SettingsKind, SettingsObserved};

/// The domain's name: its stream's `Cover.domain`.
pub const SETTINGS_DOMAIN: &str = "user-settings";

/// One settings file as the stream last observed it.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct ObservedFile {
    /// The stream's sequence of the event that recorded it.
    pub sequence: u64,
    pub path: String,
    pub present: bool,
    pub content_id: String,
    /// The bytes, secrets sealed.
    pub content: Vec<u8>,
}

/// The settings, by file kind.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct UserSettings {
    pub files: BTreeMap<SettingsKind, ObservedFile>,
}

impl UserSettings {
    /// Fold `event`, recorded at `sequence`.
    pub fn apply(&mut self, event: SettingsObserved, sequence: u64) {
        self.files.insert(
            event.kind,
            ObservedFile {
                sequence,
                path: event.path,
                present: event.present,
                content_id: event.content_id,
                content: event.content,
            },
        );
    }

    /// Whether observing `kind` as (`present`, `content_id`) says something
    /// the stream does not hold: a file never seen and absent is nothing.
    pub fn differs(&self, kind: SettingsKind, present: bool, content_id: &str) -> bool {
        match self.files.get(&kind) {
            Some(held) => held.present != present || held.content_id != content_id,
            None => present,
        }
    }
}
