//! What a session is offered beyond its context: the commands, MCP servers
//! and hooks its profiles carry, handed to the engine with the context
//! ([`crate::PrepareRequest::items`]), which writes each where it reads it in
//! the session home. ctxloom's own callbacks are not among them: the engine
//! installs those itself.

use std::collections::BTreeMap;
use std::fmt;

/// The session's items, each kind in the order the profiles carry them.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct SessionItems {
    pub commands: Vec<SessionCommand>,
    pub mcp: Vec<SessionMcp>,
    pub hooks: Vec<SessionHook>,
}

impl SessionItems {
    /// Whether there is nothing to offer.
    pub fn is_empty(&self) -> bool {
        self.commands.is_empty() && self.mcp.is_empty() && self.hooks.is_empty()
    }
}

/// A command: its name (`<bundle>/<name>`), description and body.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct SessionCommand {
    pub name: String,
    pub description: String,
    pub body: String,
}

/// An MCP server: a program to spawn, or a URL to reach.
#[derive(Clone, Default, PartialEq, Eq)]
pub struct SessionMcp {
    pub name: String,
    pub command: String,
    pub args: Vec<String>,
    pub env: BTreeMap<String, String>,
    pub url: String,
    pub headers: BTreeMap<String, String>,
}

impl fmt::Debug for SessionMcp {
    /// The variables' and headers' names alone: a server's env or header may
    /// hold a secret.
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("SessionMcp")
            .field("name", &self.name)
            .field("command", &self.command)
            .field("args", &self.args)
            .field("env", &self.env.keys().collect::<Vec<_>>())
            .field("url", &self.url)
            .field("headers", &self.headers.keys().collect::<Vec<_>>())
            .finish()
    }
}

/// A hook on a unified event (`session_start`, `pre_tool`, ...).
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct SessionHook {
    pub event: String,
    /// `command` (empty is `command`), `prompt`, ...
    pub kind: String,
    pub command: String,
    pub args: Vec<String>,
    pub prompt: String,
    pub timeout: i64,
    pub is_async: bool,
    /// The tool class it is narrowed to; empty for every tool.
    pub tool: String,
    /// The engine's own matcher, which wins over a class.
    pub matcher: String,
}

#[cfg(test)]
#[path = "items.test.rs"]
mod tests;
