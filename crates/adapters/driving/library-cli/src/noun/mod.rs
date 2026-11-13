//! One module per noun (design §8.4): its read verbs, its write verbs and,
//! once its JSON is converted (OD-40), the mapping of its views to their API
//! messages, so the branches that convert the nouns touch no shared file.
//! B1 owns `bundle`, `item` (fragment, command, `fragment premises`),
//! `skill`, `mcp` and `profile`; B2 `agent`, `config`, `companion` and `run`
//! (`run --dry-run`); B3 `search` and `context`. `engine` is the spine's.

pub(crate) mod agent;
pub(crate) mod bundle;
pub(crate) mod companion;
pub(crate) mod config;
pub(crate) mod context;
pub(crate) mod engine;
pub(crate) mod item;
pub(crate) mod mcp;
pub(crate) mod profile;
pub(crate) mod run;
pub(crate) mod search;
pub(crate) mod skill;
