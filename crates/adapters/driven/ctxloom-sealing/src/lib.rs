//! The driven adapters of per-project seal keys (owner ruling 2026-11-27):
//! [`DotenvSecrets`], which reads a TRUSTED project's keys from its `.env`
//! (the live project root, read only, `CTXLOOM_SEAL_KEY` and
//! `CTXLOOM_SEAL_KEY_PREVIOUS`) into an opaque [`run_ports::KeySet`], and
//! [`AngzarrSealer`], which seals and opens with one over angzarr-sealed
//! (RFC 9180 HPKE to the key's own public half, the OS random source). All
//! key material and crypto are here and nowhere else.
//!
//! A key is 64 hexadecimal digits, an X25519 private key. Earlier keys kept
//! for opening what they sealed ride beside the current one: sealing always
//! uses the current (rotation is a KeySet's, `angzarr_sealed::KeySet`). A
//! key's id is derived from its public half, so the id on a sealed value
//! names the key that opens it.

use std::collections::BTreeMap;
use std::path::Path;
use std::sync::Arc;

use angzarr_sealed::{open_with, KeyId, KeySet, OpenError, RecipientPrivateKey, Sealed};
use file_system::Fs;
use prost::Message;
use run_ports::{KeyInfo, ProjectEvidence, ProjectSecrets, Sealer, SealingError};
use sha2::{Digest, Sha256};

/// The name a project's `.env` gives its current seal key.
pub const SEAL_KEY_VARIABLE: &str = "CTXLOOM_SEAL_KEY";

/// The name a project's `.env` gives its earlier seal keys, comma separated.
pub const PREVIOUS_KEYS_VARIABLE: &str = "CTXLOOM_SEAL_KEY_PREVIOUS";

/// The file a project's keys are in, said in a refusal.
pub const KEY_FILE: &str = ".env";

/// A key's id: the first eight hex digits of the SHA-256 of its public half.
fn id_of(key: &RecipientPrivateKey) -> KeyId {
    let digest = Sha256::digest(key.public_key().as_bytes());
    KeyId::new(format!("k-{}", hex::encode(&digest[..4])))
}

/// A key's fingerprint: the first sixteen bytes of the SHA-256 of its public
/// half, in hex: enough to tell keys apart, nothing of the key.
fn fingerprint_of(key: &RecipientPrivateKey) -> String {
    hex::encode(&Sha256::digest(key.public_key().as_bytes())[..16])
}

fn key_of(project: &str, text: &str) -> Result<RecipientPrivateKey, SealingError> {
    let bad = || SealingError::BadKey {
        project: project.to_string(),
        file: KEY_FILE.to_string(),
    };
    let bytes = hex::decode(text.trim()).map_err(|_| bad())?;
    let bytes: [u8; 32] = bytes.try_into().map_err(|_| bad())?;
    Ok(RecipientPrivateKey::from_bytes(bytes))
}

fn info_of(key: &RecipientPrivateKey) -> KeyInfo {
    KeyInfo {
        id: id_of(key).as_str().to_string(),
        fingerprint: fingerprint_of(key),
    }
}

/// The values of the names asked of `.env`'s text: `NAME=value` lines, with
/// an optional `export `, `#` comments and one pair of quotes around a value.
pub fn values_of(text: &str, names: &[&str]) -> BTreeMap<String, String> {
    text.lines()
        .filter_map(|line| {
            let line = line.trim();
            let line = line.strip_prefix("export ").unwrap_or(line).trim_start();
            if line.starts_with('#') {
                return None;
            }
            let (name, value) = line.split_once('=')?;
            let name = name.trim();
            names.contains(&name).then(|| {
                let value = value.trim();
                let unquoted = ['"', '\'']
                    .iter()
                    .find_map(|quote| {
                        value
                            .strip_prefix(*quote)
                            .and_then(|rest| rest.strip_suffix(*quote))
                    })
                    .unwrap_or(value);
                (name.to_string(), unquoted.to_string())
            })
        })
        .collect()
}

/// A project's seal keys from its `.env`.
pub struct DotenvSecrets {
    fs: Arc<dyn Fs>,
    /// The key text of a project with no `.env` at all (the test build's
    /// acceptance projects have none); a project WITH one is judged by it.
    fallback: Option<String>,
}

impl DotenvSecrets {
    pub fn new(fs: Arc<dyn Fs>) -> DotenvSecrets {
        DotenvSecrets { fs, fallback: None }
    }

    /// These secrets, a project with no `.env` given `key` (test-support).
    #[must_use]
    pub fn with_fallback(self, key: Option<String>) -> DotenvSecrets {
        DotenvSecrets {
            fallback: key,
            ..self
        }
    }
}

fn set_of(
    project: &str,
    current: &str,
    previous: &[String],
) -> Result<run_ports::KeySet, SealingError> {
    let current = key_of(project, current)?;
    let current_info = info_of(&current);
    let mut earlier = Vec::new();
    let mut set = KeySet::new(id_of(&current), current);
    for text in previous.iter().filter(|t| !t.trim().is_empty()) {
        let key = key_of(project, text)?;
        let info = info_of(&key);
        // The same key twice is the same id: refused by the set, and
        // harmless to leave out.
        if set.insert(id_of(&key), key).is_ok() {
            earlier.push(info);
        }
    }
    Ok(run_ports::KeySet::new(current_info, earlier, Arc::new(set)))
}

impl ProjectSecrets for DotenvSecrets {
    fn key_set(
        &self,
        project: &str,
        evidence: &ProjectEvidence,
    ) -> Result<run_ports::KeySet, SealingError> {
        let path = Path::new(&evidence.root).join(KEY_FILE);
        let no_key = || SealingError::NoKey {
            project: project.to_string(),
            file: path.display().to_string(),
        };
        let Ok(bytes) = self.fs.read(&path) else {
            return match &self.fallback {
                Some(key) => set_of(project, key, &[]),
                None => Err(no_key()),
            };
        };
        if !evidence.trusted {
            return Err(no_key());
        }
        let found = values_of(
            &String::from_utf8_lossy(&bytes),
            &[SEAL_KEY_VARIABLE, PREVIOUS_KEYS_VARIABLE],
        );
        let current = found
            .get(SEAL_KEY_VARIABLE)
            .filter(|key| !key.is_empty())
            .ok_or_else(no_key)?;
        let previous: Vec<String> = found
            .get(PREVIOUS_KEYS_VARIABLE)
            .map(|keys| keys.split(',').map(|k| k.trim().to_string()).collect())
            .unwrap_or_default();
        set_of(project, current, &previous)
    }
}

/// Seals and opens over angzarr-sealed.
#[derive(Clone, Copy, Debug, Default)]
pub struct AngzarrSealer;

fn angzarr_set(keys: &run_ports::KeySet) -> Result<&KeySet, SealingError> {
    keys.material()
        .downcast_ref::<KeySet>()
        .ok_or_else(|| SealingError::Seal {
            field: String::new(),
            ext_err_txt: "the key set was not made by this adapter".to_string(),
        })
}

impl Sealer for AngzarrSealer {
    fn seal(
        &self,
        keys: &run_ports::KeySet,
        field: &str,
        plain: &[u8],
    ) -> Result<Vec<u8>, SealingError> {
        let sealed = angzarr_set(keys)?
            .seal(field.as_bytes(), plain)
            .map_err(|e| SealingError::Seal {
                field: field.to_string(),
                ext_err_txt: e.to_string(),
            })?;
        Ok(sealed.encode_to_vec())
    }

    fn open(
        &self,
        keys: &run_ports::KeySet,
        field: &str,
        sealed: &[u8],
    ) -> Result<Vec<u8>, SealingError> {
        let failed = |e: &dyn std::fmt::Display| SealingError::Open {
            field: field.to_string(),
            ext_err_txt: e.to_string(),
        };
        let sealed = Sealed::decode(sealed).map_err(|e| failed(&e))?;
        match open_with(angzarr_set(keys)?, &sealed, field.as_bytes()) {
            Ok(plain) => Ok(plain.into_vec()),
            Err(OpenError::UnknownKey { key_id }) => Err(SealingError::UnknownKey {
                project: String::new(),
                key_id,
            }),
            Err(e) => Err(failed(&e)),
        }
    }
}

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
