//! ctxloom's own loadout (Go's `cmd/ctxloom/loadout.yaml`): what ctxloom
//! delivers into an engine on its own behalf, as its own companion. The
//! daemon answers it as `GetLoadout` (step 10a U2, owner ruling 2026-11-17)
//! and `ctxloom loadout --format yaml` prints it as the loadout document
//! ([`document`]).
//!
//! The document's prose is the owner's (D24) and names MCP tools whose cut
//! list is open (D8): until the owner rules, this carries its structure and
//! none of the text (the description, the server's notes, the `isolation-axes`
//! fragment).

use angzarr_yaml::Value;

/// A loadout's `run:` bundle as ctxloom declares its own: each key it has.
#[derive(Clone, PartialEq, Eq, Default, Debug)]
pub struct OwnLoadout {
    /// `version`.
    pub version: String,
    /// `tags`, in order.
    pub tags: Vec<String>,
    /// `author`.
    pub author: String,
    /// `mcp`, each server by its key, in order.
    pub mcp: Vec<OwnServer>,
}

/// One server of the `mcp:` map.
#[derive(Clone, PartialEq, Eq, Default, Debug)]
pub struct OwnServer {
    /// Its key in the map.
    pub id: String,
    /// What serves it (`session-endpoint`: nothing is launched).
    pub served_by: String,
}

/// ctxloom's own loadout.
pub fn own() -> OwnLoadout {
    OwnLoadout {
        version: "1.0.0".to_string(),
        tags: ["core", "mcp", "isolation"].map(String::from).to_vec(),
        author: "ctxloom".to_string(),
        mcp: vec![OwnServer {
            id: "ctxloom".to_string(),
            served_by: "session-endpoint".to_string(),
        }],
    }
}

/// `loadout` as the loadout document a companion probe reads: its `run:`
/// bundle, the keys in the order Go's document gives them, a key with
/// nothing in it left out.
pub fn document(loadout: &OwnLoadout) -> String {
    let mut run = vec![("version".to_string(), Value::scalar(&loadout.version))];
    if !loadout.tags.is_empty() {
        let tags = loadout.tags.iter().map(Value::scalar).collect();
        run.push(("tags".to_string(), Value::Seq(tags)));
    }
    run.push(("author".to_string(), Value::scalar(&loadout.author)));
    if !loadout.mcp.is_empty() {
        let servers = loadout
            .mcp
            .iter()
            .map(|server| {
                let served_by = ("served_by".to_string(), Value::scalar(&server.served_by));
                (server.id.clone(), Value::Map(vec![served_by]))
            })
            .collect();
        run.push(("mcp".to_string(), Value::Map(servers)));
    }
    angzarr_yaml::write(
        &Value::Map(vec![("run".to_string(), Value::Map(run))]),
        usize::MAX,
    )
}

#[cfg(test)]
#[path = "loadout.test.rs"]
mod tests;
