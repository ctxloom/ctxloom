//! The stream's name and root.

/// The one stream of the user's settings.
pub const SETTINGS_NAME: &str = "users/me/settings";

/// The stream root: the UUIDv5 of [`SETTINGS_NAME`] in ctxloom's one
/// namespace.
pub fn settings_root() -> angzarr_embedded::v1::Uuid {
    angzarr_embedded::v1::Uuid::from_bytes(*sessions::root_of(SETTINGS_NAME).as_bytes())
}
