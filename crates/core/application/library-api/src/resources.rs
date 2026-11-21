//! A library view as its API message (OD-40; design §6), the ONE mapping
//! (step 10a U2): what the daemon answers a read with, and what the CLI
//! prints for a read still answered in process, named under the parent the
//! read was asked under (`projects/{harp}`, or `users/me` outside a
//! project).
//!
//! Moved here from library-cli (U2) for the reads the daemon serves: the
//! agents, the profiles and the skills (the hooks' reads, O3).

use std::collections::BTreeMap;

use daemon_proto::google::protobuf::{value::Kind, ListValue, Struct, Value};
use daemon_proto::v1;
use library_application::{
    AgentRow, Assembled, BundleList, BundleRow, BundleShow, BundleView, CompanionRow,
    CompanionState, DistilledState, ItemList, ItemShow, Located, McpList, McpRow, McpShow,
    Premises, ProfileList, ProfileRow, ProfileShow, RemoteFound, SearchHit, SearchResults,
    SkillList, SkillShow,
};
use library_application::{
    AgentSettings, AgentShow, AuthSetting, EngineAgent, EngineRow, FragmentRef, Hook, NotCarried,
    ProfileHooks, Requirement, ResolvedAgent,
};

use crate::names::{bundle_name, bundle_ref, item_name, origin, profile_ref, reference_name};

/// The bundle a project's own profiles are named under.
pub const PROJECT_BUNDLE: &str = "project";

/// A free-form document as a `google.protobuf.Value` (OQ-11, owner ruling
/// 2026-11-13): whatever was written, an object or not, for display.
pub fn document(json: &serde_json::Value) -> Value {
    let kind = match json {
        serde_json::Value::Null => Kind::NullValue(0),
        serde_json::Value::Bool(b) => Kind::BoolValue(*b),
        serde_json::Value::Number(n) => Kind::NumberValue(n.as_f64().unwrap_or_default()),
        serde_json::Value::String(s) => Kind::StringValue(s.clone()),
        serde_json::Value::Array(items) => Kind::ListValue(ListValue {
            values: items.iter().map(document).collect(),
        }),
        serde_json::Value::Object(fields) => Kind::StructValue(Struct {
            fields: fields
                .iter()
                .map(|(key, field)| (key.clone(), document(field)))
                .collect::<BTreeMap<_, _>>(),
        }),
    };
    Value { kind: Some(kind) }
}

/// `bundle list`: `ListBundlesResponse`, the whole catalog (design §1.2).
pub fn bundles(parent: &str, list: &BundleList) -> v1::ListBundlesResponse {
    v1::ListBundlesResponse {
        bundles: list.rows.iter().map(|row| bundle(parent, row)).collect(),
        next_page_token: String::new(),
    }
}

/// A `bundle list` row as its `Bundle` (BASIC).
pub fn bundle(parent: &str, row: &BundleRow) -> v1::Bundle {
    v1::Bundle {
        name: bundle_name(parent, &row.name),
        origin: origin(&row.name, row.origin == "ctxloom/builtin") as i32,
        reference: row.reference.clone(),
        commit: row.commit.clone(),
        path: row.path.clone(),
        version: row.version.clone(),
        description: row.description.clone(),
        tags: row.tags.clone(),
        fragment_count: count(row.fragment_count),
        command_count: count(row.command_count),
        mcp_server_count: count(row.mcp_count),
        profile_count: count(row.profile_count),
        held: row.held.then_some(true),
        ..v1::Bundle::default()
    }
}

/// An engine's resource name under `parent`.
pub fn engine_name(parent: &str, engine: &str) -> String {
    format!("{parent}/engines/{engine}")
}

/// An agent's resource name under `parent`.
pub fn agent_name(parent: &str, agent: &str) -> String {
    format!("{parent}/agents/{agent}")
}

/// `agent list`: `ListAgentsResponse`, every agent under `parent`, the one
/// a bare run binds being `default`.
pub fn agents(parent: &str, rows: &[AgentRow], default: &str) -> v1::ListAgentsResponse {
    v1::ListAgentsResponse {
        agents: rows.iter().map(|row| agent(parent, row, default)).collect(),
        next_page_token: String::new(),
    }
}

/// The engine an agent references, by its resource name (design §2); an
/// agent that names none names none.
pub fn engine_ref(parent: &str, engine: &str) -> String {
    if engine.is_empty() {
        return String::new();
    }
    engine_name(parent, engine)
}

/// An agent as its binding declares it (the BASIC view): its engine blocks
/// keyed by engine, so `engines.<e>.model` keeps its path; its engine and
/// profiles by their resource names (design §2: one that names no place
/// keeps its configured text, OQ-16).
pub fn agent(parent: &str, row: &AgentRow, default: &str) -> v1::Agent {
    v1::Agent {
        name: agent_name(parent, &row.name),
        engine: engine_ref(parent, &row.engine),
        profiles: row
            .profiles
            .iter()
            .map(|profile| profile_ref(parent, profile))
            .collect(),
        runtime: row.runtime.clone(),
        driving: row.driving.clone(),
        env: row.env.clone(),
        session_home: row.session_home.clone(),
        engines: blocks(&row.engines),
        default: Some(row.name == default),
        ..Default::default()
    }
}

/// `agents.<n>.engines` as the config holds it: each engine's model, effort
/// and permissions document, the document as written (rulings 133-139).
fn blocks(engines: &serde_json::Value) -> BTreeMap<String, v1::AgentEngine> {
    engines
        .as_object()
        .into_iter()
        .flatten()
        .map(|(engine, block)| {
            let word = |key: &str| {
                block
                    .get(key)
                    .and_then(serde_json::Value::as_str)
                    .unwrap_or_default()
                    .to_string()
            };
            let lined = v1::AgentEngine {
                model: word("model"),
                effort: word("effort"),
                permissions: block.get("permissions").map(document),
            };
            (engine.clone(), lined)
        })
        .collect()
}

/// A value and the layer that said it.
pub fn text(value: &str, source: &str) -> Option<v1::SourcedText> {
    Some(v1::SourcedText {
        value: value.to_string(),
        source: source.to_string(),
    })
}

/// What an agent resolves to on an engine. A model or effort nobody set is
/// no value (the engine's own default), its source still said.
pub fn settings(settings: &AgentSettings) -> v1::AgentSettings {
    let optional =
        |value: &Option<String>, source: &str| text(value.as_deref().unwrap_or_default(), source);
    let runtime = &settings.runtime;
    v1::AgentSettings {
        model: optional(&settings.model.value, settings.model.source),
        effort: optional(&settings.effort.value, settings.effort.source),
        permissions: Some(v1::SourcedDocument {
            value: settings.permissions.value.as_ref().map(document),
            source: settings.permissions.source.to_string(),
        }),
        runtime: Some(v1::RuntimeSetting {
            value: runtime.value.clone(),
            source: runtime.source.to_string(),
            overrides: runtime.overrides.clone().unwrap_or_default(),
            overrides_source: runtime.overrides_source.unwrap_or_default().to_string(),
            change: runtime.change.unwrap_or_default().to_string(),
        }),
        env: Some(v1::EnvSetting {
            value: settings.env.value.clone(),
            source: settings.env.source.to_string(),
            keys: settings
                .env
                .keys
                .iter()
                .map(|key| v1::EnvKey {
                    variable: key.name.clone(),
                    source: key.source.to_string(),
                })
                .collect(),
        }),
        session_home: text(&settings.session_home.value, settings.session_home.source),
        approval_timeout: text(
            &settings.approval_timeout.value,
            settings.approval_timeout.source,
        ),
    }
}

/// What an engine cannot carry of what an agent's profiles configure.
pub fn not_carried(lost: &NotCarried) -> v1::NotCarried {
    v1::NotCarried {
        engine: lost.engine.clone(),
        surface: lost.surface.clone(),
        event: lost.event.clone(),
        reason: lost.reason.clone(),
    }
}

/// `agent show <agent>`: the `Agent` resource (the FULL view), its
/// definition the resource and what it resolves to beside it.
pub fn agent_shown(parent: &str, show: &AgentShow, default: &str) -> v1::Agent {
    v1::Agent {
        resolved: show.resolved.as_ref().map(|r| resolved(parent, r)),
        resolution_error: show.resolution_error.clone(),
        requirements: show.requirements.iter().map(requirement).collect(),
        requirements_error: show.requirements_error.clone(),
        not_carried: show.not_carried.iter().map(not_carried).collect(),
        ..agent(parent, &show.definition, default)
    }
}

/// What an agent resolves to, its settings flat (as today), its engine by
/// its resource name.
fn resolved(parent: &str, agent: &ResolvedAgent) -> v1::ResolvedAgent {
    let settings = settings(&agent.settings);
    v1::ResolvedAgent {
        profiles: agent.profiles.clone(),
        engine: engine_ref(parent, &agent.engine),
        model: settings.model,
        effort: settings.effort,
        permissions: settings.permissions,
        runtime: settings.runtime,
        env: settings.env,
        context: agent.context.clone(),
        fragments: agent.fragments.clone(),
        driving: agent.driving.clone(),
        session_home: settings.session_home,
        command: agent.command.clone(),
    }
}

fn requirement(needed: &Requirement) -> v1::Requirement {
    v1::Requirement {
        command: needed.command.clone(),
        sources: needed
            .sources
            .iter()
            .map(|source| v1::RequirementSource {
                kind: source.kind.clone(),
                reference: source.reference.clone(),
            })
            .collect(),
    }
}

/// `engine list`: `ListEnginesResponse`, every engine under `parent`.
pub fn engines(parent: &str, rows: &[EngineRow]) -> v1::ListEnginesResponse {
    v1::ListEnginesResponse {
        engines: rows.iter().map(|row| engine(parent, row)).collect(),
        next_page_token: String::new(),
    }
}

/// `engine show <engine>`: the `Engine` resource.
pub fn engine(parent: &str, row: &EngineRow) -> v1::Engine {
    v1::Engine {
        name: engine_name(parent, &row.name),
        command: text(&row.command.value, row.command.source),
        session_home: text(&row.session_home.value, row.session_home.source),
        runtime: text(&row.runtime.value, row.runtime.source),
        env: Some(v1::SourcedList {
            value: row.env.value.clone(),
            source: row.env.source.to_string(),
        }),
        auth: Some(engine_auth(&row.auth)),
        agents: row.agents.iter().map(engine_agent).collect(),
        approval_timeout: text(&row.approval_timeout.value, row.approval_timeout.source),
    }
}

/// The auth source: its word, or its variables and file; its state the
/// enum's value.
pub fn engine_auth(setting: &AuthSetting) -> v1::EngineAuth {
    let value = match &setting.value {
        serde_json::Value::String(word) => v1::AuthValue {
            word: word.clone(),
            ..v1::AuthValue::default()
        },
        serde_json::Value::Object(source) => v1::AuthValue {
            word: String::new(),
            env: source
                .get("env")
                .and_then(serde_json::Value::as_array)
                .map(|names| {
                    names
                        .iter()
                        .filter_map(|n| n.as_str().map(String::from))
                        .collect()
                })
                .unwrap_or_default(),
            file: source
                .get("file")
                .and_then(serde_json::Value::as_str)
                .unwrap_or_default()
                .to_string(),
        },
        _ => v1::AuthValue::default(),
    };
    let state = match setting.state {
        "present" => v1::engine_auth::State::Present,
        "missing" => v1::engine_auth::State::Missing,
        "not-needed" => v1::engine_auth::State::NotNeeded,
        _ => v1::engine_auth::State::Unspecified,
    };
    v1::EngineAuth {
        value: Some(value),
        source: setting.source.to_string(),
        state: state as i32,
        via: setting.via.clone().unwrap_or_default(),
    }
}

/// One agent of an engine, its settings flat (design §3.8).
fn engine_agent(agent: &EngineAgent) -> v1::EngineAgent {
    let settings = settings(&agent.settings);
    v1::EngineAgent {
        agent: agent.name.clone(),
        model: settings.model,
        effort: settings.effort,
        permissions: settings.permissions,
        runtime: settings.runtime,
        env: settings.env,
        session_home: settings.session_home,
    }
}

/// A server's resource name below the bundle that serves it (`source`, its
/// canonical reference).
pub fn server_name(parent: &str, source: &str, id: &str) -> String {
    item_name(parent, source, "mcp", id)
}

/// A server as its `McpServer`, named `name`.
pub fn server(name: &str, row: &McpRow) -> v1::McpServer {
    v1::McpServer {
        name: name.to_string(),
        command: row.command.clone(),
        args: row.args.clone(),
        env: row.env.clone(),
        url: row.url.clone(),
        served_by: row.served_by.clone(),
        notes: row.notes.clone(),
        installation: row.installation.clone(),
        ..v1::McpServer::default()
    }
}

/// `mcp server list`: `ListMcpServersResponse`, each server below its
/// bundle (no environment, notes or installation, as listed).
pub fn servers_listed(parent: &str, list: &McpList) -> v1::ListMcpServersResponse {
    v1::ListMcpServersResponse {
        mcp_servers: list
            .servers
            .iter()
            .map(|row| v1::McpServer {
                name: server_name(parent, &row.source, &row.name),
                command: row.command.clone(),
                args: row.args.clone(),
                url: row.url.clone(),
                served_by: row.served_by.clone(),
                ..v1::McpServer::default()
            })
            .collect(),
        next_page_token: String::new(),
    }
}

/// `mcp server show <name>`: `ListMcpServersResponse` filtered by the ID
/// (design §3.5), every bundle's server of that ID.
pub fn servers_shown(parent: &str, show: &McpShow) -> v1::ListMcpServersResponse {
    v1::ListMcpServersResponse {
        mcp_servers: show
            .entries
            .iter()
            .map(|row| server(&server_name(parent, &row.source, &row.name), row))
            .collect(),
        next_page_token: String::new(),
    }
}

/// `search`: `SearchLibraryResponse`, each local hit named under `parent`.
pub fn searched(parent: &str, results: &SearchResults) -> v1::SearchLibraryResponse {
    v1::SearchLibraryResponse {
        query: results.query.clone(),
        local: results
            .local
            .iter()
            .map(|hit| local_hit(parent, hit))
            .collect(),
        remote: results.remote.iter().map(remote_found).collect(),
        count: count(results.count),
        hidden_local: count(results.hidden_local),
        next_page_token: String::new(),
    }
}

/// A local hit, named by its resource; one whose bundle the catalog does
/// not hold keeps the name it was found by (OQ-16).
fn local_hit(parent: &str, hit: &SearchHit) -> v1::SearchHit {
    let (kind, collection) = item_kind(&hit.kind);
    v1::SearchHit {
        resource: hit.located.as_ref().map_or_else(
            || hit.name.clone(),
            |at| item_name(parent, &at.bundle, collection, &at.item),
        ),
        kind: kind as i32,
        tags: hit.tags.clone(),
        source: hit.source.clone(),
        r#match: hit.r#match.clone(),
    }
}

/// A remote's bundle, not installed: no resource name, its `item` the name
/// it has in the remote.
fn remote_found(found: &RemoteFound) -> v1::RemoteFound {
    v1::RemoteFound {
        remote: found.remote.clone(),
        item: found.name.clone(),
        kind: item_kind(&found.kind).0 as i32,
        tags: found.tags.clone(),
        description: found.description.clone(),
        author: found.author.clone(),
        pull_reference: found.pull_ref.clone(),
    }
}

/// A search's kind word as its `ItemKind`, and the collection its items are
/// named in.
pub fn item_kind(word: &str) -> (v1::ItemKind, &'static str) {
    match word {
        "fragment" => (v1::ItemKind::Fragment, "fragments"),
        "command" => (v1::ItemKind::Command, "commands"),
        "skill" => (v1::ItemKind::Skill, "skills"),
        "profile" => (v1::ItemKind::Profile, "profiles"),
        "mcp_server" => (v1::ItemKind::McpServer, "mcpServers"),
        "bundle" => (v1::ItemKind::Bundle, "bundles"),
        _ => (v1::ItemKind::Unspecified, ""),
    }
}

/// An `ItemKind` as a search's kind word; unspecified is none.
pub fn kind_word(kind: i32) -> &'static str {
    match v1::ItemKind::try_from(kind) {
        Ok(v1::ItemKind::Fragment) => "fragment",
        Ok(v1::ItemKind::Command) => "command",
        Ok(v1::ItemKind::Skill) => "skill",
        Ok(v1::ItemKind::Profile) => "profile",
        Ok(v1::ItemKind::McpServer) => "mcp_server",
        Ok(v1::ItemKind::Bundle) => "bundle",
        _ => "",
    }
}

/// `config show [section] [--raw]`: the `Config` singleton under `parent`,
/// its document what the layers give (one section of it, or only what they
/// set).
pub fn config(parent: &str, shown: &library_application::ConfigShown) -> v1::Config {
    v1::Config {
        name: format!("{parent}/config"),
        document: Some(document(&shown.json)),
        etag: String::new(),
    }
}

/// A distilled form there or not.
fn distilled_there(there: bool) -> i32 {
    if there {
        v1::DistilledState::Fresh as i32
    } else {
        v1::DistilledState::None as i32
    }
}

/// A `bundle show` entry's distilled form: stale where it no longer matches
/// its source, else there or not.
pub fn shown_distilled(there: bool, stale: bool) -> i32 {
    if stale {
        v1::DistilledState::Stale as i32
    } else {
        distilled_there(there)
    }
}

/// `bundle show`: the `Bundle` (FULL), each child summarised by its name
/// (OQ-14).
pub fn bundle_shown(parent: &str, show: &BundleShow) -> v1::Bundle {
    let item = |dir: &str, id: &str| item_name(parent, &show.name, dir, id);
    v1::Bundle {
        author: show.author.clone(),
        notes: show.notes.clone(),
        installation: show.installation.clone(),
        description: show.description.clone(),
        version: show.version.clone(),
        tags: show.tags.clone(),
        path: show.path.clone(),
        skill_count: count(show.skills.len() as u64),
        fragments: show
            .fragments
            .iter()
            .map(|(id, f)| v1::BundleItem {
                item: item("fragments", id),
                tags: f.tags.clone(),
                preview: f.preview.clone(),
                distilled: shown_distilled(f.distilled, f.stale),
                no_distill: Some(f.no_distill),
                premise: f.premise.clone(),
                ..v1::BundleItem::default()
            })
            .collect(),
        commands: show
            .commands
            .iter()
            .map(|(id, c)| v1::BundleItem {
                item: item("commands", id),
                tags: c.tags.clone(),
                description: c.description.clone(),
                preview: c.preview.clone(),
                distilled: shown_distilled(c.distilled, c.stale),
                no_distill: Some(c.no_distill),
                ..v1::BundleItem::default()
            })
            .collect(),
        skills: show
            .skills
            .iter()
            .map(|(id, s)| v1::BundleItem {
                item: item("skills", id),
                tags: s.tags.clone(),
                notes: s.notes.clone(),
                path: s.path.clone(),
                ..v1::BundleItem::default()
            })
            .collect(),
        mcp_servers: show
            .mcp
            .iter()
            .map(|(id, m)| v1::BundleItem {
                item: item("mcp", id),
                command: m.command.clone(),
                args: m.args.clone(),
                env: m.env.clone(),
                notes: m.notes.clone(),
                installation: m.installation.clone(),
                ..v1::BundleItem::default()
            })
            .collect(),
        profiles: show
            .profiles
            .iter()
            .map(|id| item("profiles", id))
            .collect(),
        hooks: show.hooks.clone(),
        ..bundle(
            parent,
            &BundleRow {
                name: show.name.clone(),
                ..show.listing.clone()
            },
        )
    }
}

/// `bundle view <bundle>`: the `Bundle` (CONTENT), its files in the order
/// they are viewed (its manifest first, then the rest by path).
pub fn bundle_viewed(parent: &str, view: &BundleView) -> v1::Bundle {
    let files = match &view.viewed {
        library_application::Viewed::Files(files) => files
            .iter()
            .map(|(path, content)| v1::BundleFile {
                path: path.clone(),
                content: content.clone(),
            })
            .collect(),
        _ => Vec::new(),
    };
    v1::Bundle {
        name: bundle_name(parent, &view.bundle),
        files,
        distilled: Some(view.distilled),
        ..v1::Bundle::default()
    }
}

/// The `Run` a dry run would create (design §5.9, `CreateRun` with
/// `validate_only`), under `parent` (none: the project is not named, owner
/// ruling 2026-11-14): the plan's agent, engine, profiles, fragments,
/// context and its token estimate, the prompt, the findings, the agent's
/// settings on the engine and the agent they are of, and the workspace axis
/// and runtime the launch settles on (step 10a U2, owner ruling
/// 2026-11-17). Its name is `<parent>/runs/-`, as nothing is created; its
/// agent, engine and profiles are resource names (design §2), their
/// configured text where the project has no name.
pub fn planned(parent: Option<&str>, plan: &library_application::DryRun) -> v1::Run {
    let named = |name: &dyn Fn(&str, &str) -> String, text: &str| match parent {
        Some(parent) if !text.is_empty() => name(parent, text),
        _ => text.to_string(),
    };
    let (workspace, runtime) = &plan.axes;
    v1::Run {
        name: parent.map_or_else(String::new, |parent| format!("{parent}/runs/-")),
        agent: named(&agent_name, &plan.agent),
        engine: named(&engine_name, &plan.engine),
        profiles: plan
            .profiles
            .iter()
            .map(|profile| named(&profile_ref, profile))
            .collect(),
        context: plan.context.clone(),
        // A count (OQ-12); no context holds 2^32 tokens.
        tokens: u32::try_from(plan.tokens).unwrap_or(u32::MAX),
        findings: plan.findings.iter().map(finding).collect(),
        settings: plan.settings.as_ref().map(settings),
        settings_agent: plan.settings_agent.clone(),
        workspace: plan.workspace.clone(),
        runtime: plan.runtime.clone(),
        settled_workspace: workspace.clone(),
        settled_runtime: runtime.clone(),
        prompt: plan.prompt.clone(),
        fragments: plan.fragments.clone(),
        ..v1::Run::default()
    }
}

/// A plan's finding.
pub fn finding(found: &library_application::FindingRow) -> v1::Finding {
    v1::Finding {
        kind: found.kind.clone(),
        text: found.text.clone(),
        remedy: found.remedy.clone(),
        fatal: found.fatal,
    }
}

/// The profile's own name within its key (`<bundle>#profiles/<name>`).
pub fn profile_id(name: &str) -> &str {
    name.split_once("#profiles/").map_or(name, |(_, id)| id)
}

/// A profile's resource name, below the bundle that ships it (the
/// project's own, `project`, for none).
pub fn profile_name(parent: &str, entry: &str, id: &str) -> String {
    let entry = if entry.is_empty() {
        PROJECT_BUNDLE
    } else {
        entry
    };
    item_name(parent, entry, "profiles", id)
}

/// `profile list`: `ListProfilesResponse`.
pub fn profiles(parent: &str, list: &ProfileList) -> v1::ListProfilesResponse {
    v1::ListProfilesResponse {
        profiles: list
            .rows
            .iter()
            .map(|row| profile_row(parent, row))
            .collect(),
        next_page_token: String::new(),
    }
}

fn profile_row(parent: &str, row: &ProfileRow) -> v1::Profile {
    v1::Profile {
        name: profile_name(parent, &row.entry, &row.display_name),
        description: row.description.clone(),
        parents: profile_parents(parent, &row.parents),
        tags: row.tags.clone(),
        bundles: profile_bundles(parent, &row.bundles),
        default: Some(row.default),
        path: row.path.clone(),
        ..<v1::Profile as Default>::default()
    }
}

/// `profile show`: the `Profile` (FULL).
pub fn profile_shown(parent: &str, show: &ProfileShow) -> v1::Profile {
    v1::Profile {
        default: Some(show.default),
        ..profile_of(
            parent,
            &profile_name(parent, &show.entry, profile_id(&show.name)),
            show,
        )
    }
}

/// A profile as its `Profile`, named `name`.
pub fn profile_of(parent: &str, name: &str, show: &ProfileShow) -> v1::Profile {
    v1::Profile {
        name: name.to_string(),
        description: show.description.clone(),
        parents: profile_parents(parent, &show.parents),
        tags: show.tags.clone(),
        bundles: profile_bundles(parent, &show.bundles),
        variables: show.variables.clone(),
        exclude_fragments: show.exclude_fragments.clone(),
        exclude_mcp_servers: show.exclude_mcp.clone(),
        path: show.path.clone(),
        select_tags: show.select_tags.clone(),
        fragments: show
            .fragments
            .iter()
            .map(|fragment| profile_fragment(parent, fragment))
            .collect(),
        hooks: profile_hooks(&show.hooks),
        deny_tools: show.deny_tools.clone(),
        ..<v1::Profile as Default>::default()
    }
}

/// A fragment a profile names directly: an item reference by its
/// resource name, any other text (a bare name) as written (OQ-16).
pub fn profile_fragment(parent: &str, fragment: &FragmentRef) -> v1::ProfileFragment {
    v1::ProfileFragment {
        fragment: reference_name(parent, &fragment.name).unwrap_or_else(|| fragment.name.clone()),
        priority: fragment.priority,
    }
}

/// A profile's hooks, field for field (none when it declares none).
pub fn profile_hooks(hooks: &ProfileHooks) -> Option<v1::ProfileHooks> {
    if hooks.unified.is_empty() && hooks.ext.is_empty() {
        return None;
    }
    let events = |events: &BTreeMap<String, Vec<Hook>>| -> BTreeMap<String, v1::ProfileHookList> {
        events
            .iter()
            .map(|(event, hooks)| {
                let hooks = hooks.iter().map(profile_hook).collect();
                (event.clone(), v1::ProfileHookList { hooks })
            })
            .collect()
    };
    Some(v1::ProfileHooks {
        unified: events(&hooks.unified),
        ext: hooks
            .ext
            .iter()
            .map(|(engine, native)| {
                let events = events(native);
                (engine.clone(), v1::ProfileEngineHooks { events })
            })
            .collect(),
    })
}

/// One hook, field for field.
pub fn profile_hook(hook: &Hook) -> v1::ProfileHook {
    v1::ProfileHook {
        event: hook.event.clone(),
        name: hook.name.clone(),
        order: hook.order,
        tool: hook.tool.clone(),
        kind: hook.kind.clone(),
        command: hook.command.clone(),
        args: hook.args.clone(),
        prompt: hook.prompt.clone(),
        timeout: hook.timeout,
        is_async: hook.is_async,
        pre_tool_fallback: hook.pre_tool_fallback,
        tags: hook.tags.clone(),
    }
}

/// A profile's parents as Profile names (OQ-16: one with no place keeps
/// its text).
pub fn profile_parents(parent: &str, asks: &[String]) -> Vec<String> {
    asks.iter().map(|ask| profile_ref(parent, ask)).collect()
}

/// A profile's bundles as Bundle (or item) names.
pub fn profile_bundles(parent: &str, asks: &[String]) -> Vec<String> {
    asks.iter().map(|ask| bundle_ref(parent, ask)).collect()
}

/// A skill's resource name.
pub fn skill_name(parent: &str, bundle: &str, id: &str) -> String {
    item_name(parent, bundle, "skills", id)
}

/// A count as the API's uint32 (OQ-12), saturating.
pub fn count(n: u64) -> u32 {
    u32::try_from(n).unwrap_or(u32::MAX)
}

/// `skill list`: `ListSkillsResponse`.
pub fn skills(parent: &str, list: &SkillList) -> v1::ListSkillsResponse {
    v1::ListSkillsResponse {
        skills: list
            .rows
            .iter()
            .map(|row| {
                // A row's name is `<bundle>/<skill>`, its source the bundle.
                let id = row
                    .name
                    .strip_prefix(&format!("{}/", row.source))
                    .unwrap_or(&row.name);
                v1::Skill {
                    name: skill_name(parent, &row.source, id),
                    display_name: row.display_name.clone(),
                    description: row.description.clone(),
                    tags: row.tags.clone(),
                    file_count: count(row.file_count),
                    ..v1::Skill::default()
                }
            })
            .collect(),
        next_page_token: String::new(),
    }
}

/// `skill show`: the `Skill` (FULL), each file's executability its
/// sidecar's.
pub fn skill(parent: &str, show: &SkillShow) -> v1::Skill {
    v1::Skill {
        name: skill_name(parent, &show.bundle, &show.name),
        description: show.description.clone(),
        license: show.license.clone(),
        compatibility: show.compatibility.clone(),
        allowed_tools: show.allowed_tools.clone(),
        body: show.body.clone(),
        file_count: count(show.files.len() as u64),
        files: show
            .files
            .iter()
            .map(|file| v1::SkillFile {
                path: file.path.clone(),
                sha256: file.sha256.clone(),
                executable: file.mode == "0755",
            })
            .collect(),
        ..v1::Skill::default()
    }
}

/// A companion's resource name under `parent`.
pub fn companion_name(parent: &str, companion: &str) -> String {
    format!("{parent}/companions/{companion}")
}

/// A companion's state as the API says it.
pub fn companion_state(state: CompanionState) -> v1::companion::State {
    match state {
        CompanionState::Enabled => v1::companion::State::Enabled,
        CompanionState::Disabled => v1::companion::State::Disabled,
    }
}

/// `companion list`: `ListCompanionsResponse`, every registered companion
/// under `parent`, whether its program is there `resolves` (looked for,
/// never run).
pub fn companions(parent: &str, rows: &[CompanionRow]) -> v1::ListCompanionsResponse {
    v1::ListCompanionsResponse {
        companions: rows
            .iter()
            .map(|row| v1::Companion {
                name: companion_name(parent, &row.name),
                bin: row.program.clone(),
                resolves: Some(row.exists),
                state: companion_state(row.state).into(),
            })
            .collect(),
        next_page_token: String::new(),
    }
}

/// A distilled form as judged against its source now (T30, S51).
pub fn distilled_state(state: DistilledState) -> i32 {
    let api = match state {
        DistilledState::None => v1::DistilledState::None,
        DistilledState::Fresh => v1::DistilledState::Fresh,
        DistilledState::Stale => v1::DistilledState::Stale,
    };
    api as i32
}

/// `fragment list`: `ListFragmentsResponse`, each fragment below its
/// bundle.
pub fn fragments(parent: &str, list: &ItemList) -> v1::ListFragmentsResponse {
    v1::ListFragmentsResponse {
        fragments: list
            .rows
            .iter()
            .map(|row| v1::Fragment {
                name: item_name(parent, &row.bundle, "fragments", &row.name),
                reference: row.reference.clone(),
                tags: row.tags.clone(),
                ..v1::Fragment::default()
            })
            .collect(),
        next_page_token: String::new(),
    }
}

/// `command list`: `ListCommandsResponse`, each command below its bundle.
pub fn commands(parent: &str, list: &ItemList) -> v1::ListCommandsResponse {
    v1::ListCommandsResponse {
        commands: list
            .rows
            .iter()
            .map(|row| v1::Command {
                name: item_name(parent, &row.bundle, "commands", &row.name),
                reference: row.reference.clone(),
                tags: row.tags.clone(),
                ..v1::Command::default()
            })
            .collect(),
        next_page_token: String::new(),
    }
}

/// `fragment show`: the `Fragment` (FULL), its body the form served.
pub fn fragment(parent: &str, show: &ItemShow) -> v1::Fragment {
    v1::Fragment {
        name: item_name(parent, &show.bundle, "fragments", &show.name),
        reference: show.reference.clone(),
        body: show.content.clone(),
        body_is_distilled: show.distilled,
        distilled: distilled_state(show.state),
        ..v1::Fragment::default()
    }
}

/// `command show`: the `Command` (FULL), its body the form served.
pub fn command(parent: &str, show: &ItemShow) -> v1::Command {
    v1::Command {
        name: item_name(parent, &show.bundle, "commands", &show.name),
        reference: show.reference.clone(),
        body: show.content.clone(),
        body_is_distilled: show.distilled,
        distilled: distilled_state(show.state),
        ..v1::Command::default()
    }
}

/// `fragment premises`: the `PremiseIndex`, each fragment by its name.
pub fn premise_index(parent: &str, premises: &Premises) -> v1::PremiseIndex {
    v1::PremiseIndex {
        name: format!("{parent}/premiseIndex"),
        instruction: premises.instruction.clone(),
        fragments: premises
            .fragments
            .iter()
            .map(|row| v1::PremiseEntry {
                fragment: reference_name(parent, &row.name).unwrap_or_else(|| row.name.clone()),
                premise: row.premise.clone(),
            })
            .collect(),
    }
}

/// `loadout`: ctxloom's own `Loadout` under `parent`, its `run:` bundle
/// field for field.
pub fn loadout(parent: &str, own: &library_application::loadout::OwnLoadout) -> v1::Loadout {
    v1::Loadout {
        name: format!("{parent}/loadout"),
        run: Some(v1::LoadoutBundle {
            version: own.version.clone(),
            tags: own.tags.clone(),
            author: own.author.clone(),
            mcp_servers: own
                .mcp
                .iter()
                .map(|server| v1::LoadoutMcpServer {
                    id: server.id.clone(),
                    served_by: server.served_by.clone(),
                })
                .collect(),
        }),
    }
}

/// `context assemble` (and the MCP server's `context_assemble`):
/// `AssembledContext`, its profiles and fragments named under `parent`; one
/// the catalog does not hold keeps the text it was asked by, as a fragment
/// asked for and not found does (OQ-16). What a premise held back is
/// answered as data, by its reference, for the edge to render (E2).
pub fn assembled(parent: &str, assembled: &Assembled) -> v1::AssembledContext {
    let named = |text: &String, located: Option<&Option<Located>>, collection: &str| {
        located.and_then(Option::as_ref).map_or_else(
            || text.clone(),
            |at| item_name(parent, &at.bundle, collection, &at.item),
        )
    };
    v1::AssembledContext {
        context: assembled.context.clone(),
        tokens: count(assembled.tokens),
        fragments: assembled
            .fragments_loaded
            .iter()
            .enumerate()
            .map(|(at, reference)| {
                let used = assembled.used.get(at);
                v1::AssembledFragment {
                    fragment: named(reference, used.map(|u| &u.located), "fragments"),
                    form: if used.is_some_and(|u| u.distilled) {
                        v1::assembled_fragment::Form::Distilled
                    } else {
                        v1::assembled_fragment::Form::Original
                    } as i32,
                }
            })
            .collect(),
        profiles: assembled
            .profiles
            .iter()
            .enumerate()
            .map(|(at, ask)| named(ask, assembled.profiles_located.get(at), "profiles"))
            .collect(),
        missing_fragments: assembled.missing_fragments.clone(),
        missing_tags: assembled.missing_tags.clone(),
        premised: assembled
            .premised
            .iter()
            .map(|entry| v1::PremiseEntry {
                fragment: entry.name.clone(),
                premise: entry.premise.clone(),
            })
            .collect(),
        notices: Vec::new(),
    }
}

#[cfg(test)]
#[path = "resources.test.rs"]
mod tests;
