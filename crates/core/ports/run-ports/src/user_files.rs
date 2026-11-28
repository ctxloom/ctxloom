//! The user's own engine settings files, as a launch finds them: read by
//! the composition root where the caller stands, recorded by the use case
//! as the user-settings stream's facts.

/// Which of the user's settings files.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum SettingsFileKind {
    /// claude's `settings.json`.
    Claude,
    /// opencode's global config.
    OpencodeConfig,
    /// opencode's `model.json`.
    OpencodeModel,
}

/// One settings file as found: where, and its bytes (none: it is not there
/// or does not read). Its `Debug` names the place alone: a file's `env` may
/// hold a secret.
#[derive(Clone, PartialEq, Eq)]
pub struct SettingsFile {
    pub kind: SettingsFileKind,
    pub path: String,
    pub bytes: Option<Vec<u8>>,
}

impl std::fmt::Debug for SettingsFile {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("SettingsFile")
            .field("kind", &self.kind)
            .field("path", &self.path)
            .field("bytes", &self.bytes.as_ref().map(Vec::len))
            .finish()
    }
}

/// Where the user's settings files are read, in the caller's environment.
pub trait SettingsFiles: Send + Sync {
    /// Each file the caller's places name, found or not.
    fn read(&self) -> Vec<SettingsFile>;
}
