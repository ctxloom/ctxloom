//! What a session is offered beside its context (S9): the commands, skills,
//! MCP servers and hooks its profiles carry, in no engine's words. The plan
//! carries them in the launch order, the launcher hands them to
//! [`crate::Engine::prepare`], and the engine puts each where it reads it in
//! the session home (claude: its config dir's `commands/`, `skills/`, the
//! `mcpServers` of its state file, the hooks of its `--settings` file).
//!
//! Pure data. Every value an MCP server's environment or headers hold may be
//! a secret, so their `Debug` names the keys alone.

use std::collections::BTreeMap;
use std::fmt;

/// A command (a slash command): `<bundle>/<name>`, its description and body.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct SurfaceCommand {
    pub name: String,
    pub description: String,
    pub body: String,
}

/// One file of a skill package: its path inside the package, its bytes and
/// whether the publisher declared it executable.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct SurfaceFile {
    pub path: String,
    pub bytes: Vec<u8>,
    pub executable: bool,
}

/// An Agent Skill package, under the name its `SKILL.md` declares.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct SurfaceSkill {
    pub name: String,
    pub description: String,
    pub files: Vec<SurfaceFile>,
}

/// An MCP server: a command to spawn, or a URL to reach.
#[derive(Clone, Default, PartialEq, Eq)]
pub struct SurfaceMcp {
    pub name: String,
    pub command: String,
    pub args: Vec<String>,
    pub env: BTreeMap<String, String>,
    pub url: String,
    pub headers: BTreeMap<String, String>,
}

impl fmt::Debug for SurfaceMcp {
    /// The environment's and the headers' keys alone: either may carry a
    /// credential.
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("SurfaceMcp")
            .field("name", &self.name)
            .field("command", &self.command)
            .field("args", &self.args)
            .field("env", &self.env.keys().collect::<Vec<_>>())
            .field("url", &self.url)
            .field("headers", &self.headers.keys().collect::<Vec<_>>())
            .finish()
    }
}

/// A hook, on its unified event (`pre_tool`, `post_file_edit`, ...), in the
/// order its event fires them.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct SurfaceHook {
    pub event: String,
    /// `command` (the default), `prompt`, ...
    pub kind: String,
    pub command: String,
    pub args: Vec<String>,
    pub prompt: String,
    pub timeout: i64,
    pub is_async: bool,
    /// The tool class it is narrowed to (`shell`, `file_edit`, `skill`);
    /// empty for every tool.
    pub tool: String,
    /// The engine's own matcher, which wins over a class.
    pub matcher: String,
}

/// What a session is offered beside its context.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct SessionSurfaces {
    pub commands: Vec<SurfaceCommand>,
    pub skills: Vec<SurfaceSkill>,
    pub mcp: Vec<SurfaceMcp>,
    pub hooks: Vec<SurfaceHook>,
}

impl SessionSurfaces {
    /// Whether it offers nothing.
    pub fn is_empty(&self) -> bool {
        self.commands.is_empty()
            && self.skills.is_empty()
            && self.mcp.is_empty()
            && self.hooks.is_empty()
    }
}

#[cfg(test)]
#[path = "surfaces.test.rs"]
mod tests;
