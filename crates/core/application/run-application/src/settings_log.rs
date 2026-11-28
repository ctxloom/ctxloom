//! The user's own engine settings, recorded as the stream of their own
//! (owner ruling 2026-11-25): a launch scans the files where the caller
//! stands, sends each as a fact (the aggregate records only a change), and
//! names the sequence of the stream it read, so the record says which
//! settings a launch had. A secret inside a file (an `env`, an MCP server's
//! `headers`) is sealed before it is sent, never recorded in the clear.

use angzarr_embedded::v1::Cover;
use run_ports::{Edge, Sealing, SealingError, SettingsFile, SettingsFileKind};
use runs_application::AppError as CoreError;

use crate::error::RunError;

/// The objects of a settings document whose string values are secrets.
const SECRET_OBJECTS: [&str; 3] = ["env", "environment", "headers"];

/// What stands in the content for a sealed value.
const SEALED_IN_CONTENT: &str = "[sealed]";

fn kind_of(kind: SettingsFileKind) -> user_settings::SettingsKind {
    match kind {
        SettingsFileKind::Claude => user_settings::SettingsKind::ClaudeSettings,
        SettingsFileKind::OpencodeConfig => user_settings::SettingsKind::OpencodeConfig,
        SettingsFileKind::OpencodeModel => user_settings::SettingsKind::OpencodeModel,
    }
}

/// `value` with each string of a [`SECRET_OBJECTS`] object replaced by its
/// stand-in and sealed, bound to its document path (`<prefix>.<name>`).
fn sealed_in(
    value: &mut serde_json::Value,
    path: &str,
    (sealing, project): (&dyn Sealing, &str),
    sealed: &mut Vec<user_settings::SealedSetting>,
) -> Result<(), SealingError> {
    let serde_json::Value::Object(object) = value else {
        return Ok(());
    };
    for (name, inner) in object.iter_mut() {
        let here = if path.is_empty() {
            name.clone()
        } else {
            format!("{path}.{name}")
        };
        let secret = SECRET_OBJECTS.contains(&name.as_str());
        match (secret, inner) {
            (true, serde_json::Value::Object(secrets)) => {
                for (secret_name, secret_value) in secrets.iter_mut() {
                    let serde_json::Value::String(plain) = secret_value else {
                        continue;
                    };
                    let field = format!("{here}.{secret_name}");
                    let bound = sealing.seal(project, &field, plain.as_bytes())?;
                    sealed.push(user_settings::SealedSetting {
                        document_path: field,
                        sealed: bound,
                    });
                    *secret_value = serde_json::Value::String(SEALED_IN_CONTENT.to_string());
                }
            }
            (_, inner) => sealed_in(inner, &here, (sealing, project), sealed)?,
        }
    }
    Ok(())
}

/// The fact that records `file`: its bytes with each secret sealed.
fn fact_of(
    file: &SettingsFile,
    keyed: (&dyn Sealing, &str),
) -> Result<user_settings::RecordSettingsObserved, SealingError> {
    let Some(bytes) = &file.bytes else {
        return Ok(user_settings::RecordSettingsObserved {
            kind: kind_of(file.kind),
            path: file.path.clone(),
            present: false,
            content_id: String::new(),
            content: Vec::new(),
            sealed: Vec::new(),
        });
    };
    let mut sealed = Vec::new();
    // A document that is not JSON is recorded by its id alone: its secrets,
    // if any, cannot be told apart, so none of it is kept.
    let content = match serde_json::from_slice::<serde_json::Value>(bytes) {
        Ok(mut value) => {
            sealed_in(&mut value, "", keyed, &mut sealed)?;
            serde_json::to_vec(&value).unwrap_or_default()
        }
        Err(_) => Vec::new(),
    };
    Ok(user_settings::RecordSettingsObserved {
        kind: kind_of(file.kind),
        path: file.path.clone(),
        present: true,
        content_id: library_tree::content_id(bytes),
        content,
        sealed,
    })
}

/// Record what `files` say on the user-settings stream through `edge`, and
/// answer the sequence of its last event: none when there is no file and the
/// stream is empty.
pub(crate) async fn observed<E: Edge>(
    edge: &E,
    files: &[SettingsFile],
    keyed: (&dyn Sealing, &str),
) -> Result<Option<u64>, RunError> {
    let cover = Cover {
        domain: user_settings::SETTINGS_DOMAIN.to_string(),
        root: Some(user_settings::settings_root()),
        edition: None,
    };
    for file in files.iter().filter(|file| file.bytes.is_some()) {
        let fact = fact_of(file, keyed).map_err(|e| RunError::Sealing(Box::new(e)))?;
        let key = format!("settings:{}:{}", fact.kind as i32, fact.content_id);
        edge.handle(sessions::fact_book(cover.clone(), &fact, &key))
            .await
            .map_err(|e| {
                RunError::Session(CoreError::Failed {
                    reason: e.to_string(),
                })
            })?;
    }
    let pages = edge.events(&cover, 0).await.map_err(|e| {
        RunError::Session(CoreError::Failed {
            reason: e.to_string(),
        })
    })?;
    Ok(pages.last().map(|page| page.sequence))
}
