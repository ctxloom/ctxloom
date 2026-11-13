//! What each command answers, as data: the shapes are the JSON the commands
//! print (Go's `--format json`, field for field, in its order), and the text
//! forms are written from the same values.

use std::collections::BTreeMap;

use serde::Serialize;

fn no_text(s: &str) -> bool {
    s.is_empty()
}

fn no_items<T>(items: &[T]) -> bool {
    items.is_empty()
}

fn no_entries<K, V>(map: &BTreeMap<K, V>) -> bool {
    map.is_empty()
}

fn is_false(b: &bool) -> bool {
    !*b
}

fn is_zero(n: &u64) -> bool {
    *n == 0
}

/// One row of `bundle list`.
#[derive(Clone, Debug, Default, Serialize)]
pub struct BundleRow {
    pub name: String,
    #[serde(rename = "ref")]
    pub reference: String,
    #[serde(skip_serializing_if = "no_text")]
    pub path: String,
    /// The built-in origin a bundle ctxloom ships is listed under
    /// (`ctxloom/default`, rulings 110, 111); empty for any other.
    #[serde(skip_serializing_if = "no_text")]
    pub origin: String,
    #[serde(skip_serializing_if = "no_text")]
    pub version: String,
    #[serde(skip_serializing_if = "no_text")]
    pub description: String,
    #[serde(skip_serializing_if = "no_items")]
    pub tags: Vec<String>,
    pub fragment_count: u64,
    pub command_count: u64,
    pub mcp_count: u64,
    pub profile_count: u64,
    pub held: bool,
    /// The commit an installed remote bundle is pinned at; empty for any
    /// other (its API resource's `commit`, OD-40).
    #[serde(skip)]
    pub commit: String,
}

/// What `bundle list` answers.
#[derive(Clone, Debug)]
pub struct BundleList {
    pub rows: Vec<BundleRow>,
    /// The project has no bundles directory at all.
    pub no_root: bool,
    /// The hint for an empty listing.
    pub hint: String,
}

/// A fragment as `bundle show` shows it.
#[derive(Clone, Debug, Serialize)]
pub struct ShownFragment {
    #[serde(skip_serializing_if = "no_items")]
    pub tags: Vec<String>,
    pub preview: String,
    pub distilled: bool,
    pub no_distill: bool,
    #[serde(skip_serializing_if = "no_text")]
    pub premise: String,
    /// Its distilled form no longer matches its source (T30).
    #[serde(skip)]
    pub stale: bool,
}

/// A command as `bundle show` shows it.
#[derive(Clone, Debug, Serialize)]
pub struct ShownCommand {
    #[serde(skip_serializing_if = "no_items")]
    pub tags: Vec<String>,
    #[serde(skip_serializing_if = "no_text")]
    pub description: String,
    pub preview: String,
    pub distilled: bool,
    pub no_distill: bool,
    /// Its distilled form no longer matches its source (T30).
    #[serde(skip)]
    pub stale: bool,
}

/// An MCP server as `bundle show` shows it.
#[derive(Clone, Debug, Serialize)]
pub struct ShownMcp {
    pub command: String,
    #[serde(skip_serializing_if = "no_items")]
    pub args: Vec<String>,
    #[serde(skip_serializing_if = "no_entries")]
    pub env: BTreeMap<String, String>,
    #[serde(skip_serializing_if = "no_text")]
    pub notes: String,
    #[serde(skip_serializing_if = "no_text")]
    pub installation: String,
}

/// A skill as `bundle show` shows it.
#[derive(Clone, Debug, Serialize)]
pub struct ShownSkill {
    #[serde(skip_serializing_if = "no_text")]
    pub path: String,
    #[serde(skip_serializing_if = "no_items")]
    pub tags: Vec<String>,
    #[serde(skip_serializing_if = "no_text")]
    pub notes: String,
}

/// What `bundle show` answers.
#[derive(Clone, Debug, Serialize)]
pub struct BundleShow {
    pub name: String,
    #[serde(skip_serializing_if = "no_text")]
    pub path: String,
    #[serde(skip_serializing_if = "no_text")]
    pub version: String,
    #[serde(skip_serializing_if = "no_text")]
    pub author: String,
    #[serde(skip_serializing_if = "no_text")]
    pub description: String,
    #[serde(skip_serializing_if = "no_items")]
    pub tags: Vec<String>,
    #[serde(skip_serializing_if = "no_text")]
    pub notes: String,
    #[serde(skip_serializing_if = "no_text")]
    pub installation: String,
    #[serde(skip_serializing_if = "no_entries")]
    pub fragments: BTreeMap<String, ShownFragment>,
    #[serde(skip_serializing_if = "no_entries")]
    pub commands: BTreeMap<String, ShownCommand>,
    #[serde(skip_serializing_if = "no_entries")]
    pub mcp: BTreeMap<String, ShownMcp>,
    #[serde(skip_serializing_if = "no_entries")]
    pub skills: BTreeMap<String, ShownSkill>,
    #[serde(skip_serializing_if = "no_items")]
    pub profiles: Vec<String>,
    #[serde(skip_serializing_if = "no_items")]
    pub hooks: Vec<String>,
    /// The bundle as `bundle list` lists it: its origin, reference and
    /// counts (its API resource's, OD-40).
    #[serde(skip)]
    pub listing: BundleRow,
}

/// What `bundle view` read, as data (its API resource, OD-40): the
/// bundle's files, or the one item the path selects.
#[derive(Clone, Debug, Default)]
pub enum Viewed {
    #[default]
    Nothing,
    /// The whole bundle: each file, by path, as text.
    Files(Vec<(String, String)>),
    /// A fragment or a command (`kind` its directory, `fragments` or
    /// `commands`): the body served, and whether it is the distilled form.
    Body {
        kind: String,
        name: String,
        body: String,
        distilled: bool,
    },
    Skill {
        name: String,
        skill: ShownSkill,
    },
    Mcp(McpRow),
    Profile(ProfileShow),
}

/// What `bundle view` answers.
#[derive(Clone, Debug, Serialize)]
pub struct BundleView {
    pub bundle: String,
    #[serde(skip_serializing_if = "no_text")]
    pub path: String,
    #[serde(skip_serializing_if = "is_false")]
    pub distilled: bool,
    pub content: String,
    /// Whether the content is one item's (its blank runs collapse in text).
    #[serde(skip)]
    pub item: bool,
    /// What was read, as data.
    #[serde(skip)]
    pub viewed: Viewed,
}

/// One row of `fragment list` and `command list`.
#[derive(Clone, Debug, Serialize)]
pub struct ItemRow {
    pub name: String,
    pub tags: Vec<String>,
    pub bundle: String,
    #[serde(rename = "ref")]
    pub reference: String,
    pub remote: String,
    pub bundle_label: String,
}

/// What `fragment list` and `command list` answer.
#[derive(Clone, Debug)]
pub struct ItemList {
    pub rows: Vec<ItemRow>,
    /// The kind, `fragment` or `command`.
    pub kind: String,
    /// The `--bundle` asked for, empty for none.
    pub bundle_filter: String,
    /// How many there are in all bundles.
    pub total: usize,
    pub hint: String,
}

/// What `fragment show` and `command show` answer.
#[derive(Clone, Debug)]
pub struct ItemShow {
    pub name: String,
    /// The bundle the item is in, by its catalog name.
    pub bundle: String,
    pub reference: String,
    pub content: String,
    /// The content is the distilled form (asked for, and fresh).
    pub distilled: bool,
    /// Its distilled form against its source, judged now (T30, S51).
    pub state: library::distill::DistilledState,
}

/// One entry of `fragment premises`.
#[derive(Clone, Debug, Serialize)]
pub struct PremiseRow {
    pub name: String,
    pub premise: String,
}

/// What `fragment premises` answers.
#[derive(Clone, Debug, Serialize)]
pub struct Premises {
    pub instruction: String,
    pub fragments: Vec<PremiseRow>,
}

/// One row of `skill list`.
#[derive(Clone, Debug, Serialize)]
pub struct SkillRow {
    pub name: String,
    #[serde(skip_serializing_if = "no_text")]
    pub description: String,
    #[serde(skip_serializing_if = "no_items")]
    pub tags: Vec<String>,
    pub source: String,
    pub file_count: u64,
}

/// What `skill list` answers.
#[derive(Clone, Debug)]
pub struct SkillList {
    pub rows: Vec<SkillRow>,
    pub bundle_filter: String,
    pub total: usize,
}

/// One file of a skill package.
#[derive(Clone, Debug, Serialize)]
pub struct SkillFileRow {
    pub path: String,
    pub sha256: String,
    pub mode: String,
}

/// What `skill show` answers.
#[derive(Clone, Debug, Serialize)]
pub struct SkillShow {
    pub name: String,
    pub bundle: String,
    pub description: String,
    #[serde(skip_serializing_if = "no_text")]
    pub license: String,
    #[serde(skip_serializing_if = "no_text")]
    pub compatibility: String,
    #[serde(skip_serializing_if = "no_items")]
    pub allowed_tools: Vec<String>,
    pub body: String,
    pub files: Vec<SkillFileRow>,
}

/// One row of `profile list`.
#[derive(Clone, Debug, Serialize)]
pub struct ProfileRow {
    pub name: String,
    pub display_name: String,
    #[serde(skip_serializing_if = "no_text")]
    pub description: String,
    #[serde(skip_serializing_if = "no_items")]
    pub parents: Vec<String>,
    #[serde(skip_serializing_if = "no_items")]
    pub tags: Vec<String>,
    #[serde(skip_serializing_if = "no_items")]
    pub bundles: Vec<String>,
    #[serde(skip_serializing_if = "is_false")]
    pub default: bool,
    #[serde(skip_serializing_if = "no_text")]
    pub path: String,
    pub is_remote: bool,
    #[serde(skip_serializing_if = "no_text")]
    pub bundle: String,
    /// The catalog name of the bundle that ships it (its API resource's
    /// parent, OD-40).
    #[serde(skip)]
    pub entry: String,
}

/// What `profile list` answers.
#[derive(Clone, Debug)]
pub struct ProfileList {
    pub rows: Vec<ProfileRow>,
    pub hint: String,
}

/// What `profile show` answers.
#[derive(Clone, Debug, Default, Serialize)]
pub struct ProfileShow {
    pub name: String,
    #[serde(skip_serializing_if = "no_text")]
    pub description: String,
    #[serde(skip_serializing_if = "no_items")]
    pub parents: Vec<String>,
    #[serde(skip_serializing_if = "no_items")]
    pub tags: Vec<String>,
    #[serde(skip_serializing_if = "no_items")]
    pub bundles: Vec<String>,
    #[serde(skip_serializing_if = "no_entries")]
    pub variables: BTreeMap<String, String>,
    #[serde(skip_serializing_if = "no_items")]
    pub exclude_fragments: Vec<String>,
    #[serde(skip_serializing_if = "no_items")]
    pub exclude_mcp: Vec<String>,
    #[serde(skip_serializing_if = "no_text")]
    pub path: String,
    #[serde(skip_serializing_if = "no_text")]
    pub bundle: String,
    pub default: bool,
    /// The catalog name of the bundle that ships it (OD-40).
    #[serde(skip)]
    pub entry: String,
}

/// An agent as its binding declares it.
#[derive(Clone, Debug, Serialize)]
pub struct AgentRow {
    pub name: String,
    #[serde(skip_serializing_if = "no_text")]
    pub engine: String,
    #[serde(skip_serializing_if = "no_items")]
    pub profiles: Vec<String>,
    #[serde(skip_serializing_if = "no_text")]
    pub runtime: String,
    #[serde(skip_serializing_if = "no_text")]
    pub driving: String,
    #[serde(skip_serializing_if = "no_items")]
    pub env: Vec<String>,
    /// The agent's per-engine blocks as the config holds them
    /// (`agents.<n>.engines`, owner rulings 57 and 67); absent when none.
    #[serde(skip_serializing_if = "no_blocks")]
    pub engines: serde_json::Value,
    /// Each engine block on one line, `engines.<e>: {...}`, for the listing,
    /// with the engine it is for and whether it holds a `permissions`
    /// document (passed to that engine verbatim, owner ruling 139).
    #[serde(skip)]
    pub engine_lines: Vec<EngineLine>,
}

/// One engine block of an agent, on one line.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct EngineLine {
    pub engine: String,
    /// `engines.<e>: {...}`, in YAML's flow form.
    pub line: String,
    /// The block holds a `permissions` document.
    pub permissions: bool,
}

/// What every display of an engine's permissions document says of it (owner
/// ruling 139): ctxloom passes it on as written and checks nothing in it.
pub fn passed_verbatim(engine: &str) -> String {
    format!("passed to {engine} verbatim; ctxloom does not check it")
}

fn no_blocks(value: &serde_json::Value) -> bool {
    value.as_object().is_none_or(serde_json::Map::is_empty)
}

/// The engine and context an agent resolves to.
#[derive(Clone, Debug, Serialize)]
pub struct ResolvedAgent {
    pub name: String,
    pub profiles: Vec<String>,
    pub engine: String,
    /// Model, effort, permissions, runtime and env, each with the
    /// layer that said it.
    #[serde(flatten)]
    pub settings: crate::AgentSettings,
    #[serde(skip_serializing_if = "no_text")]
    pub context: String,
    #[serde(skip_serializing_if = "no_items")]
    pub fragments: Vec<String>,
    #[serde(skip_serializing_if = "no_text")]
    pub driving: String,
    /// The engine block's `session_home`.
    #[serde(skip_serializing_if = "no_text")]
    pub session_home: String,
    /// The program the engine block (else the engine's own name) runs.
    #[serde(skip_serializing_if = "no_text")]
    pub command: String,
}

/// A command an agent needs on its PATH and who asks for it.
#[derive(Clone, Debug, Serialize)]
pub struct Requirement {
    pub command: String,
    pub sources: Vec<RequirementSource>,
}

/// One source of a requirement.
#[derive(Clone, Debug, Serialize)]
pub struct RequirementSource {
    pub kind: String,
    #[serde(rename = "ref")]
    pub reference: String,
}

/// What `agent show` answers.
#[derive(Clone, Debug, Serialize)]
pub struct AgentShow {
    pub definition: AgentRow,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub resolved: Option<ResolvedAgent>,
    #[serde(skip_serializing_if = "no_text")]
    pub resolution_error: String,
    #[serde(skip_serializing_if = "no_items")]
    pub requirements: Vec<Requirement>,
    #[serde(skip_serializing_if = "no_text")]
    pub requirements_error: String,
    /// What the agent's engine cannot carry of what its profiles configure
    /// (the capability-loss report); absent when nothing.
    #[serde(skip_serializing_if = "no_items")]
    pub not_carried: Vec<crate::NotCarried>,
}

/// One row of `companion list`.
#[derive(Clone, Debug, Serialize)]
pub struct CompanionRow {
    pub name: String,
    /// The program, then its arguments.
    pub command: Vec<String>,
    /// Whether the program is there; it is not run to find out.
    pub exists: bool,
}

/// A registered MCP server.
#[derive(Clone, Debug, Serialize)]
pub struct McpRow {
    pub name: String,
    #[serde(skip_serializing_if = "no_text")]
    pub command: String,
    #[serde(skip_serializing_if = "no_items")]
    pub args: Vec<String>,
    #[serde(skip_serializing_if = "no_entries")]
    pub env: BTreeMap<String, String>,
    #[serde(skip_serializing_if = "no_text")]
    pub url: String,
    #[serde(skip_serializing_if = "no_text")]
    pub served_by: String,
    pub source: String,
    #[serde(skip_serializing_if = "no_text")]
    pub notes: String,
    #[serde(skip_serializing_if = "no_text")]
    pub installation: String,
}

/// What `mcp server list` answers.
#[derive(Clone, Debug, Serialize)]
pub struct McpList {
    pub servers: Vec<McpListRow>,
}

/// One row of `mcp server list` (no environment, notes or installation).
#[derive(Clone, Debug, Serialize)]
pub struct McpListRow {
    pub name: String,
    #[serde(skip_serializing_if = "no_text")]
    pub command: String,
    #[serde(skip_serializing_if = "no_items")]
    pub args: Vec<String>,
    #[serde(skip_serializing_if = "no_text")]
    pub url: String,
    #[serde(skip_serializing_if = "no_text")]
    pub served_by: String,
    pub source: String,
}

/// What `mcp server show` answers.
#[derive(Clone, Debug, Serialize)]
pub struct McpShow {
    pub name: String,
    pub found: bool,
    pub entries: Vec<McpRow>,
}

/// Where an item is in the catalog (OD-40): its bundle's catalog name
/// (`demo`; `<remote>/<bundle>` for one installed from a remote;
/// `ctxloom+companion:<bin>` for a companion's loadout) and the item's own
/// name, from which a driving adapter names its resource (design §1.2).
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Located {
    pub bundle: String,
    pub item: String,
}

/// One result of `search`.
#[derive(Clone, Debug, Serialize)]
pub struct SearchHit {
    #[serde(rename = "type")]
    pub kind: String,
    pub name: String,
    #[serde(skip_serializing_if = "no_items")]
    pub tags: Vec<String>,
    #[serde(skip_serializing_if = "no_text")]
    pub source: String,
    #[serde(skip_serializing_if = "no_text")]
    pub r#match: String,
    /// Where the hit is; none when its bundle is none the catalog holds.
    #[serde(skip)]
    pub located: Option<Located>,
}

/// One bundle a remote publishes, as a search found it.
#[derive(Clone, Debug, Serialize)]
pub struct RemoteFound {
    pub remote: String,
    pub name: String,
    #[serde(rename = "type")]
    pub kind: String,
    #[serde(skip_serializing_if = "no_items")]
    pub tags: Vec<String>,
    #[serde(skip_serializing_if = "no_text")]
    pub description: String,
    #[serde(skip_serializing_if = "no_text")]
    pub author: String,
    pub pull_ref: String,
}

/// What `search` answers.
#[derive(Clone, Debug, Serialize)]
pub struct SearchResults {
    pub query: String,
    pub local: Vec<SearchHit>,
    pub remote: Vec<RemoteFound>,
    pub count: u64,
    #[serde(skip_serializing_if = "is_zero")]
    pub hidden_local: u64,
}

/// A finding, on the wire.
#[derive(Clone, Debug, Serialize)]
pub struct FindingRow {
    pub kind: String,
    pub text: String,
    #[serde(skip_serializing_if = "no_text")]
    pub remedy: String,
    pub fatal: bool,
}

/// What `run --dry-run` answers: the launch a run would carry. The pieces
/// that print the engines' surfaces and isolation (`resolved`, `environment`,
/// `delivery`, `engine_home`) arrive with those slices.
#[derive(Clone, Debug, Serialize)]
pub struct DryRun {
    #[serde(skip_serializing_if = "no_text")]
    pub agent: String,
    #[serde(skip_serializing_if = "no_text")]
    pub workspace: String,
    #[serde(skip_serializing_if = "no_text")]
    pub runtime: String,
    pub engine: String,
    pub profiles: Vec<String>,
    pub fragments: Vec<String>,
    pub context: String,
    pub tokens: u64,
    #[serde(skip_serializing_if = "no_text")]
    pub prompt: String,
    pub findings: Vec<FindingRow>,
    /// The workspace and runtime a launch settles on (shown beside an agent).
    #[serde(skip)]
    pub axes: (String, String),
    /// The agent's model, empty when it names none: handed to the engine as
    /// `--model`, not shown by a dry run.
    #[serde(skip)]
    pub model: String,
    /// The agent's effort, empty when it names none: handed to the engine,
    /// not shown by a dry run.
    #[serde(skip)]
    pub effort: String,
    /// The agent's permissions document for the run's engine, as written
    /// (its block's `permissions`, else the engine's default document; owner
    /// rulings 135, 137), as JSON text in the order it was written; none when
    /// there is none: handed to the engine verbatim, shown by a dry run's
    /// settings.
    #[serde(skip)]
    pub permissions: Option<String>,
    /// How the engine is run (its block's program, session home and
    /// credential source, the passthrough the agent settled on): handed to the
    /// engine, not shown by a dry run.
    #[serde(skip)]
    pub drive: library::config::engines::Drive,
    /// The agent's settings on the engine the run is on (its block for that
    /// engine, owner ruling 57), each with the layer that said it; none for
    /// a launch with no agent.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub settings: Option<crate::AgentSettings>,
    /// The agent `settings` are of: `agent`, or the default agent a run with
    /// no `--agent` takes; empty with no settings. Not shown by the JSON,
    /// whose `settings` say their places by word.
    #[serde(skip)]
    pub settings_agent: String,
    /// What the launch carries for the engine besides the context, from the
    /// RUN's selection (the agent's profiles, not the default agent's, owner
    /// ruling 2026-11-12): ctxloom's own callbacks and the bundles' authored
    /// hooks, the statusline, `deny_tools`, the MCP servers not served by the
    /// session, the commands and the skills. The context is `context`; this
    /// one's is empty. Not shown by a dry run.
    #[serde(skip)]
    pub surfaces: surface_ports::Inputs,
}

/// What `context assemble` answers (Go's `assemble_context` result).
#[derive(Clone, Debug, Serialize)]
pub struct Assembled {
    pub profiles: Vec<String>,
    pub context: String,
    pub fragments_loaded: Vec<String>,
    #[serde(skip_serializing_if = "no_items")]
    pub missing_fragments: Vec<String>,
    #[serde(skip_serializing_if = "no_items")]
    pub missing_tags: Vec<String>,
    /// Where each of `profiles` is, in its order; none for one the catalog
    /// does not hold (OD-40).
    #[serde(skip)]
    pub profiles_located: Vec<Option<Located>>,
    /// Each of `fragments_loaded`, in its order, located, with the form it
    /// was used in (OD-40).
    #[serde(skip)]
    pub used: Vec<UsedFragment>,
    /// The context's token estimate.
    #[serde(skip)]
    pub tokens: u64,
}

/// A fragment an assembly used: where it is (none when its bundle is none
/// the catalog holds) and whether its distilled form was used.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct UsedFragment {
    pub located: Option<Located>,
    pub distilled: bool,
}

#[cfg(test)]
#[path = "views.test.rs"]
mod tests;
