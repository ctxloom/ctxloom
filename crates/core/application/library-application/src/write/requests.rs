//! What each write is asked, as data: the flags a command was given, with
//! `None` for a flag not named (it keeps what is stored) and `Some` empty for
//! a flag named empty (it clears).

/// `bundle edit`.
#[derive(Clone, Default, Debug)]
pub struct BundleEditRequest {
    pub name: String,
    pub description: Option<String>,
    pub version: Option<String>,
    pub add_tags: Vec<String>,
    pub remove_tags: Vec<String>,
    pub add_fragments: Vec<String>,
    pub remove_fragments: Vec<String>,
    pub add_prompts: Vec<String>,
    pub remove_prompts: Vec<String>,
    pub add_mcp: Vec<String>,
    pub remove_mcp: Vec<String>,
}

/// `profile create`.
#[derive(Clone, Default, Debug)]
pub struct ProfileCreateRequest {
    pub name: String,
    pub description: String,
    pub parents: Vec<String>,
    pub bundles: Vec<String>,
    /// The skills it curates (`--add-skill`).
    pub skills: Vec<String>,
    /// The local bundle the profile is written into; the project's own when
    /// empty.
    pub target: String,
}

/// `agent create` and `agent edit`.
#[derive(Clone, Default, Debug)]
pub struct AgentRequest {
    pub engine: Option<String>,
    pub model: Option<String>,
    pub effort: Option<String>,
    pub profiles: Option<Vec<String>>,
    pub runtime: Option<String>,
    /// `kind=approach` pairs; an empty list clears them.
    pub surfaces: Option<Vec<(String, String)>>,
    /// `kind=root` pairs; an empty list clears them.
    pub roots: Option<Vec<(String, String)>>,
    pub env: Option<Vec<String>>,
    /// `session` | `host` (owner ruling of 2026-11-13); empty clears it.
    /// `host` only on a home write (owner ruling of 2026-11-14).
    pub session_home: Option<String>,
    /// `--home`: the write goes to the home config, not the project's
    /// (vocabulary §4: every write defaults to the project).
    pub home: bool,
}
