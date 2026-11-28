//! The wire messages carry the names the kernel routes by: a stored event's
//! type URL must never change.

use super::v1;
use prost::Name;

#[test]
fn every_message_is_in_the_settings_package() {
    for url in [
        v1::RecordSettingsObserved::type_url(),
        v1::SettingsObserved::type_url(),
        v1::UserSettingsState::type_url(),
    ] {
        assert!(url.starts_with("/ctxloom.settings.v1."), "{url}");
    }
}
