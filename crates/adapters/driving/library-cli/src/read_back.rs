//! A served read's message read back as the view its text is drawn from
//! (step 10a U2, D10: the text is rendered from the message alone): each
//! function takes what the daemon answered and gives the view the text
//! renderer in [`text`](crate::text) prints, line for line as it printed
//! the view the use case answered in process.

use daemon_proto::google::protobuf::{value::Kind, Value};
use std::collections::BTreeMap;

use daemon_proto::v1;
use library_application::{
    AgentRow, AgentSettings, AgentShow, AuthSetting, EngineAgent, EngineLine, EngineRow, EnvKey,
    EnvSetting, NotCarried, PermissionsSetting, RequirementSource, ResolvedAgent, RuntimeSetting,
    SourcedValue,
};
use library_application::{
    BundleRow, BundleShow, BundleView, McpList, McpListRow, McpRow, McpShow, RemoteFound,
    SearchHit, SearchResults, ShownCommand, ShownFragment, ShownMcp, ShownSkill,
};
use library_application::{
    CompanionRow, CompanionState, DistilledState, FragmentRef, Hook, ItemList, ItemRow, ItemShow,
    PremiseRow, Premises, ProfileHooks, ProfileList, ProfileRow, ProfileShow, SkillFileRow,
    SkillList, SkillRow, SkillShow,
};

/// The parent a resource name is under: its first two segments,
/// `projects/{p}` or `users/me`.
pub(crate) fn parent_of(name: &str) -> &str {
    match name.match_indices('/').nth(1) {
        Some((offset, _)) => &name[..offset],
        None => name,
    }
}

/// An item's bundle and its own name, as its reference
/// (`<bundle>#<kind>/<name>`) says them.
fn reference_parts(reference: &str) -> (String, String) {
    let (bundle, path) = reference.split_once('#').unwrap_or((reference, ""));
    let id = path.rsplit_once('/').map_or(path, |(_, id)| id);
    (bundle.to_string(), id.to_string())
}

/// A distilled form's state as the message says it.
fn state_of(state: i32) -> DistilledState {
    match v1::DistilledState::try_from(state) {
        Ok(v1::DistilledState::Fresh) => DistilledState::Fresh,
        Ok(v1::DistilledState::Stale) => DistilledState::Stale,
        _ => DistilledState::None,
    }
}

/// The hint an empty listing ends with: a project's, or none's.
pub(crate) fn hint(in_project: bool) -> String {
    let hint = if in_project {
        library_application::LISTING_HINT
    } else {
        library_application::NO_PROJECT_HINT
    };
    hint.to_string()
}

/// What a `fragment|command list` asked and was answered: each item's
/// reference and tags, the bundle it was filtered to, how many items the
/// catalog holds of every bundle, and whether it is a project's.
pub(crate) struct Listed<'a> {
    pub(crate) kind: &'a str,
    pub(crate) items: Vec<(&'a str, &'a [String])>,
    pub(crate) bundle_filter: &'a str,
    pub(crate) total: usize,
    pub(crate) in_project: bool,
}

/// A `fragment|command list` as its view.
pub(crate) fn item_list(listed: &Listed<'_>) -> ItemList {
    let rows = listed
        .items
        .iter()
        .map(|(reference, tags)| {
            let (bundle, name) = reference_parts(reference);
            ItemRow {
                name,
                tags: tags.to_vec(),
                bundle_label: bundle.clone(),
                bundle,
                reference: (*reference).to_string(),
                remote: String::new(),
            }
        })
        .collect();
    ItemList {
        rows,
        kind: listed.kind.to_string(),
        bundle_filter: listed.bundle_filter.to_string(),
        total: listed.total,
        hint: hint(listed.in_project),
    }
}

/// A `fragment|command show` as its view.
pub(crate) fn item_show(reference: &str, body: &str, distilled: bool, state: i32) -> ItemShow {
    let (bundle, name) = reference_parts(reference);
    ItemShow {
        name,
        bundle,
        reference: reference.to_string(),
        content: body.to_string(),
        distilled,
        state: state_of(state),
    }
}

/// The short references (owner ruling 2026-11-17, step 10a U2) the
/// resource names a message lists stand for, each as
/// [`short_reference`](library_api::names::short_reference) reads it.
fn shorts(names: &[String]) -> Vec<String> {
    names
        .iter()
        .map(|name| library_api::names::short_reference(name))
        .collect()
}

/// `fragment premises` as its view, each fragment by its reference.
pub(crate) fn premises(index: &v1::PremiseIndex) -> Premises {
    Premises {
        instruction: index.instruction.clone(),
        fragments: index
            .fragments
            .iter()
            .map(|entry| PremiseRow {
                name: library_api::names::short_reference(&entry.fragment),
                premise: entry.premise.clone(),
            })
            .collect(),
    }
}

/// A skill's bundle and its own name, as its resource name says them.
fn skill_parts(name: &str) -> (String, String) {
    let parent = parent_of(name);
    let reference =
        library_api::names::reference_under(parent, name).unwrap_or_else(|| name.to_string());
    reference_parts(&reference)
}

/// A `skill list` as its view: each skill below its bundle, filtered to
/// `bundle_filter`, the catalog holding `total` of every bundle.
pub(crate) fn skill_list(
    listed: &v1::ListSkillsResponse,
    bundle_filter: &str,
    total: usize,
) -> SkillList {
    let rows = listed
        .skills
        .iter()
        .map(|skill| {
            let (bundle, id) = skill_parts(&skill.name);
            SkillRow {
                name: format!("{bundle}/{id}"),
                display_name: skill.display_name.clone(),
                description: skill.description.clone(),
                tags: skill.tags.clone(),
                source: bundle,
                file_count: u64::from(skill.file_count),
            }
        })
        .collect();
    SkillList {
        rows,
        bundle_filter: bundle_filter.to_string(),
        total,
    }
}

/// A `skill show` as its view, each file's mode its executability.
pub(crate) fn skill_show(skill: &v1::Skill) -> SkillShow {
    let (bundle, name) = skill_parts(&skill.name);
    SkillShow {
        name,
        bundle,
        description: skill.description.clone(),
        license: skill.license.clone(),
        compatibility: skill.compatibility.clone(),
        allowed_tools: skill.allowed_tools.clone(),
        body: skill.body.clone(),
        files: skill
            .files
            .iter()
            .map(|file| SkillFileRow {
                path: file.path.clone(),
                sha256: file.sha256.clone(),
                mode: if file.executable { "0755" } else { "0644" }.to_string(),
            })
            .collect(),
    }
}

/// A `companion list` as its rows: each by its own name, its program, and
/// whether it was found on the `PATH` where it was asked.
pub(crate) fn companions(listed: &v1::ListCompanionsResponse) -> Vec<CompanionRow> {
    listed
        .companions
        .iter()
        .map(|companion| CompanionRow {
            name: companion
                .name
                .rsplit_once("/companions/")
                .map_or(companion.name.as_str(), |(_, name)| name)
                .to_string(),
            program: companion.bin.clone(),
            exists: companion.resolves == Some(true),
            state: match v1::companion::State::try_from(companion.state) {
                Ok(v1::companion::State::Disabled) => CompanionState::Disabled,
                _ => CompanionState::Enabled,
            },
        })
        .collect()
}

/// A profile's bundle's resource name and its own id, as its resource name
/// (`<bundle>/profiles/<id>`) says them.
fn profile_parts(name: &str) -> (&str, &str) {
    name.rsplit_once("/profiles/").unwrap_or(("", name))
}

/// The name a person types to reach a profile, read back from its resource
/// name: a project profile's bare id, a project bundle's
/// `ctxloom+local:<bundle>#profiles/<id>`, else (a remote's, a companion's)
/// its short reference (owner ruling 2026-11-17).
pub(crate) fn profile_ask(name: &str) -> String {
    let parent = parent_of(name);
    let (bundle, id) = profile_parts(name);
    let catalog = library_api::names::catalog_name(parent, bundle);
    if catalog == library_api::resources::PROJECT_BUNDLE {
        return id.to_string();
    }
    if !catalog.contains(['/', ':']) && catalog != bundle {
        return format!("ctxloom+local:{catalog}#profiles/{id}");
    }
    library_api::names::short_reference(name)
}

/// A `profile list` as its view: each profile by the name a person types,
/// its bundle, parents and bundles by their short references (text prints
/// canonical forms, the short reference; owner ruling 2026-11-17).
pub(crate) fn profile_list(listed: &v1::ListProfilesResponse, in_project: bool) -> ProfileList {
    let rows = listed
        .profiles
        .iter()
        .map(|profile| {
            let (bundle, id) = profile_parts(&profile.name);
            ProfileRow {
                name: profile_ask(&profile.name),
                display_name: id.to_string(),
                description: profile.description.clone(),
                parents: shorts(&profile.parents),
                tags: profile.tags.clone(),
                bundles: shorts(&profile.bundles),
                default: profile.default == Some(true),
                path: profile.path.clone(),
                is_remote: false,
                bundle: library_api::names::short_reference(bundle),
                entry: String::new(),
            }
        })
        .collect();
    let hint = if in_project {
        library_application::PROFILE_HINT
    } else {
        library_application::NO_PROJECT_HINT
    };
    ProfileList {
        rows,
        hint: hint.to_string(),
    }
}

/// A `profile show` as its view, as [`profile_list`] reads each profile.
pub(crate) fn profile_show(profile: &v1::Profile) -> ProfileShow {
    let (bundle, _) = profile_parts(&profile.name);
    ProfileShow {
        name: profile_ask(&profile.name),
        description: profile.description.clone(),
        parents: shorts(&profile.parents),
        tags: profile.tags.clone(),
        bundles: shorts(&profile.bundles),
        variables: profile.variables.clone(),
        exclude_fragments: profile.exclude_fragments.clone(),
        exclude_mcp: profile.exclude_mcp_servers.clone(),
        select_tags: profile.select_tags.clone(),
        fragments: profile
            .fragments
            .iter()
            .map(|fragment| FragmentRef {
                name: library_api::names::short_reference(&fragment.fragment),
                priority: fragment.priority,
            })
            .collect(),
        hooks: profile
            .hooks
            .as_ref()
            .map(profile_hooks)
            .unwrap_or_default(),
        deny_tools: profile.deny_tools.clone(),
        path: profile.path.clone(),
        bundle: library_api::names::short_reference(bundle),
        default: profile.default == Some(true),
        entry: String::new(),
    }
}

/// A profile's hooks as the message says them, field for field.
fn profile_hooks(hooks: &v1::ProfileHooks) -> ProfileHooks {
    let events = |events: &BTreeMap<String, v1::ProfileHookList>| -> BTreeMap<String, Vec<Hook>> {
        events
            .iter()
            .map(|(event, list)| (event.clone(), list.hooks.iter().map(hook).collect()))
            .collect()
    };
    ProfileHooks {
        unified: events(&hooks.unified),
        ext: hooks
            .ext
            .iter()
            .map(|(engine, native)| (engine.clone(), events(&native.events)))
            .collect(),
    }
}

/// One hook as the message says it.
fn hook(hook: &v1::ProfileHook) -> Hook {
    Hook {
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

/// A layer's word as the message says it (`agent`, `engine`, `project`,
/// `default`); any other reads as `default`.
fn layer(word: &str) -> &'static str {
    match word {
        "agent" => "agent",
        "engine" => "engine",
        "project" => "project",
        _ => "default",
    }
}

/// How a runtime differs from the one it replaced, as the message says it;
/// none where nothing was replaced.
fn change(word: &str) -> Option<&'static str> {
    match word {
        "weakens" => Some("weakens"),
        "strengthens" => Some("strengthens"),
        "ownership-swap" => Some("ownership-swap"),
        _ => None,
    }
}

/// A free-form document on one line in YAML's flow form, every map's keys
/// sorted (text prints canonical forms, owner ruling 2026-11-17, E3): a
/// word bare, a whole number without a fraction.
pub(crate) fn flow(value: &Value) -> String {
    match &value.kind {
        None | Some(Kind::NullValue(_)) => "null".to_string(),
        Some(Kind::BoolValue(b)) => b.to_string(),
        // A float prints a whole number without its fraction (and never
        // with an exponent) already: only a negative zero is written `0`.
        Some(Kind::NumberValue(n)) if *n == 0.0 => "0".to_string(),
        Some(Kind::NumberValue(n)) => n.to_string(),
        Some(Kind::StringValue(text)) => text.clone(),
        Some(Kind::ListValue(list)) => format!(
            "[{}]",
            list.values.iter().map(flow).collect::<Vec<_>>().join(", ")
        ),
        Some(Kind::StructValue(fields)) => {
            let inner: Vec<String> = fields
                .fields
                .iter()
                .map(|(key, field)| format!("{key}: {}", flow(field)))
                .collect();
            format!("{{{}}}", inner.join(", "))
        }
    }
}

/// A free-form document as JSON.
fn json_of(value: &Value) -> serde_json::Value {
    match &value.kind {
        None | Some(Kind::NullValue(_)) => serde_json::Value::Null,
        Some(Kind::BoolValue(b)) => serde_json::Value::Bool(*b),
        Some(Kind::NumberValue(n)) => serde_json::Number::from_f64(*n)
            .map_or(serde_json::Value::Null, serde_json::Value::Number),
        Some(Kind::StringValue(text)) => serde_json::Value::String(text.clone()),
        Some(Kind::ListValue(list)) => {
            serde_json::Value::Array(list.values.iter().map(json_of).collect())
        }
        Some(Kind::StructValue(fields)) => serde_json::Value::Object(
            fields
                .fields
                .iter()
                .map(|(key, field)| (key.clone(), json_of(field)))
                .collect(),
        ),
    }
}

/// The last segment of a resource name after `/<collection>/`, or the name.
fn id_in<'a>(name: &'a str, collection: &str) -> &'a str {
    name.rsplit_once(&format!("/{collection}/"))
        .map_or(name, |(_, id)| id)
}

/// An agent's binding as its row: its engine by its id, its profiles by
/// their short references (owner ruling 2026-11-17), each
/// engine block on one line in YAML's flow form, keys sorted.
pub(crate) fn agent_row(agent: &v1::Agent) -> AgentRow {
    let engine_lines = agent
        .engines
        .iter()
        .map(|(engine, block)| {
            let mut fields = Vec::new();
            if !block.effort.is_empty() {
                fields.push(format!("effort: {}", block.effort));
            }
            if !block.model.is_empty() {
                fields.push(format!("model: {}", block.model));
            }
            if let Some(permissions) = &block.permissions {
                fields.push(format!("permissions: {}", flow(permissions)));
            }
            EngineLine {
                engine: engine.clone(),
                line: format!("engines.{engine}: {{{}}}", fields.join(", ")),
                permissions: block.permissions.is_some(),
            }
        })
        .collect();
    AgentRow {
        name: id_in(&agent.name, "agents").to_string(),
        engine: id_in(&agent.engine, "engines").to_string(),
        profiles: shorts(&agent.profiles),
        runtime: agent.runtime.clone(),
        driving: agent.driving.clone(),
        env: agent.env.clone(),
        session_home: agent.session_home.clone(),
        engines: serde_json::Value::Object(serde_json::Map::new()),
        engine_lines,
    }
}

/// An `agent list` as its rows.
pub(crate) fn agent_rows(listed: &v1::ListAgentsResponse) -> Vec<AgentRow> {
    listed.agents.iter().map(agent_row).collect()
}

fn sourced_text(text: Option<&v1::SourcedText>) -> SourcedValue<String> {
    SourcedValue {
        value: text.map(|t| t.value.clone()).unwrap_or_default(),
        source: layer(text.map_or("", |t| t.source.as_str())),
    }
}

fn sourced_optional(text: Option<&v1::SourcedText>) -> SourcedValue<Option<String>> {
    let read = sourced_text(text);
    SourcedValue {
        value: (!read.value.is_empty()).then_some(read.value),
        source: read.source,
    }
}

/// An agent's settings as the message says them (the parts `settings`,
/// `ResolvedAgent` and `EngineAgent` each carry).
#[allow(clippy::too_many_arguments)]
fn settings_of(
    model: Option<&v1::SourcedText>,
    effort: Option<&v1::SourcedText>,
    permissions: Option<&v1::SourcedDocument>,
    runtime: Option<&v1::RuntimeSetting>,
    env: Option<&v1::EnvSetting>,
    session_home: Option<&v1::SourcedText>,
    approval_timeout: Option<&v1::SourcedText>,
) -> AgentSettings {
    let document = permissions.and_then(|p| p.value.as_ref());
    let runtime = runtime.cloned().unwrap_or_default();
    let env = env.cloned().unwrap_or_default();
    AgentSettings {
        model: sourced_optional(model),
        effort: sourced_optional(effort),
        permissions: PermissionsSetting {
            value: document.map(json_of),
            source: layer(permissions.map_or("", |p| p.source.as_str())),
            line: document.map(flow).unwrap_or_default(),
        },
        runtime: RuntimeSetting {
            value: runtime.value.clone(),
            source: layer(&runtime.source),
            overrides: (!runtime.overrides.is_empty()).then(|| runtime.overrides.clone()),
            overrides_source: (!runtime.overrides_source.is_empty())
                .then(|| layer(&runtime.overrides_source)),
            change: change(&runtime.change),
        },
        env: EnvSetting {
            value: env.value.clone(),
            source: layer(&env.source),
            keys: env
                .keys
                .iter()
                .map(|key| EnvKey {
                    name: key.variable.clone(),
                    source: layer(&key.source),
                })
                .collect(),
        },
        session_home: sourced_text(session_home),
        approval_timeout: sourced_text(approval_timeout),
    }
}

/// An agent's settings on an engine as the message says them.
pub(crate) fn agent_settings(settings: &v1::AgentSettings) -> AgentSettings {
    settings_of(
        settings.model.as_ref(),
        settings.effort.as_ref(),
        settings.permissions.as_ref(),
        settings.runtime.as_ref(),
        settings.env.as_ref(),
        settings.session_home.as_ref(),
        settings.approval_timeout.as_ref(),
    )
}

/// What an engine cannot carry, as the message says it.
pub(crate) fn not_carried(lost: &v1::NotCarried) -> NotCarried {
    NotCarried {
        engine: lost.engine.clone(),
        surface: lost.surface.clone(),
        event: lost.event.clone(),
        reason: lost.reason.clone(),
    }
}

/// An `agent show` as its view.
pub(crate) fn agent_show(agent: &v1::Agent) -> AgentShow {
    let definition = agent_row(agent);
    AgentShow {
        resolved: agent.resolved.as_ref().map(|resolved| ResolvedAgent {
            name: definition.name.clone(),
            profiles: shorts(&resolved.profiles),
            engine: id_in(&resolved.engine, "engines").to_string(),
            settings: settings_of(
                resolved.model.as_ref(),
                resolved.effort.as_ref(),
                resolved.permissions.as_ref(),
                resolved.runtime.as_ref(),
                resolved.env.as_ref(),
                resolved.session_home.as_ref(),
                None,
            ),
            context: resolved.context.clone(),
            fragments: resolved.fragments.clone(),
            driving: resolved.driving.clone(),
            command: resolved.command.clone(),
        }),
        definition,
        resolution_error: agent.resolution_error.clone(),
        requirements: agent
            .requirements
            .iter()
            .map(|needed| library_application::Requirement {
                command: needed.command.clone(),
                sources: needed
                    .sources
                    .iter()
                    .map(|source| RequirementSource {
                        kind: source.kind.clone(),
                        reference: source.reference.clone(),
                    })
                    .collect(),
            })
            .collect(),
        requirements_error: agent.requirements_error.clone(),
        not_carried: agent.not_carried.iter().map(not_carried).collect(),
    }
}

/// An engine's auth, as the message says it.
fn auth_of(auth: Option<&v1::EngineAuth>) -> AuthSetting {
    let auth = auth.cloned().unwrap_or_default();
    let value = auth.value.unwrap_or_default();
    let value = if value.word.is_empty() {
        let mut source = serde_json::Map::new();
        if !value.env.is_empty() {
            source.insert("env".to_string(), serde_json::json!(value.env));
        }
        if !value.file.is_empty() {
            source.insert("file".to_string(), serde_json::json!(value.file));
        }
        serde_json::Value::Object(source)
    } else {
        serde_json::Value::String(value.word)
    };
    let state = match v1::engine_auth::State::try_from(auth.state) {
        Ok(v1::engine_auth::State::Present) => "present",
        Ok(v1::engine_auth::State::Missing) => "missing",
        Ok(v1::engine_auth::State::NotNeeded) => "not-needed",
        _ => "",
    };
    AuthSetting {
        value,
        source: layer(&auth.source),
        state,
        via: (!auth.via.is_empty()).then_some(auth.via),
    }
}

/// An `engine show` as its row.
pub(crate) fn engine_row(engine: &v1::Engine) -> EngineRow {
    let env = engine.env.clone().unwrap_or_default();
    EngineRow {
        name: id_in(&engine.name, "engines").to_string(),
        command: sourced_text(engine.command.as_ref()),
        session_home: sourced_text(engine.session_home.as_ref()),
        runtime: sourced_text(engine.runtime.as_ref()),
        env: SourcedValue {
            value: env.value,
            source: layer(&env.source),
        },
        auth: auth_of(engine.auth.as_ref()),
        approval_timeout: sourced_text(engine.approval_timeout.as_ref()),
        agents: engine
            .agents
            .iter()
            .map(|agent| EngineAgent {
                name: agent.agent.clone(),
                settings: settings_of(
                    agent.model.as_ref(),
                    agent.effort.as_ref(),
                    agent.permissions.as_ref(),
                    agent.runtime.as_ref(),
                    agent.env.as_ref(),
                    agent.session_home.as_ref(),
                    None,
                ),
            })
            .collect(),
    }
}

/// An `engine list` as its rows.
pub(crate) fn engine_rows(listed: &v1::ListEnginesResponse) -> Vec<EngineRow> {
    listed.engines.iter().map(engine_row).collect()
}

/// A server's own ID and its bundle's short reference, as its resource
/// name (`<bundle>/mcpServers/<id>`) says them (owner ruling 2026-11-17:
/// text prints the short reference).
fn server_parts(name: &str) -> (String, String) {
    let (bundle, id) = name.rsplit_once("/mcpServers/").unwrap_or(("", name));
    (id.to_string(), library_api::names::short_reference(bundle))
}

/// An `mcp server list` as its view.
pub(crate) fn mcp_list(listed: &v1::ListMcpServersResponse) -> McpList {
    McpList {
        servers: listed
            .mcp_servers
            .iter()
            .map(|server| {
                let (name, source) = server_parts(&server.name);
                McpListRow {
                    name,
                    command: server.command.clone(),
                    args: server.args.clone(),
                    url: server.url.clone(),
                    served_by: server.served_by.clone(),
                    source,
                }
            })
            .collect(),
    }
}

/// An `mcp server show` (the listing of one ID) as its view.
pub(crate) fn mcp_show(listed: &v1::ListMcpServersResponse) -> McpShow {
    let entries: Vec<McpRow> = listed
        .mcp_servers
        .iter()
        .map(|server| {
            let (name, source) = server_parts(&server.name);
            McpRow {
                name,
                command: server.command.clone(),
                args: server.args.clone(),
                env: server.env.clone(),
                url: server.url.clone(),
                served_by: server.served_by.clone(),
                source,
                notes: server.notes.clone(),
                installation: server.installation.clone(),
            }
        })
        .collect();
    McpShow {
        name: entries.first().map(|e| e.name.clone()).unwrap_or_default(),
        found: !entries.is_empty(),
        entries,
    }
}

/// A `search` as its view: each local hit by its own name (its resource
/// name's last segment) and its bundle as found.
pub(crate) fn search(found: &v1::SearchLibraryResponse) -> SearchResults {
    use library_api::resources::kind_word;
    SearchResults {
        query: found.query.clone(),
        local: found
            .local
            .iter()
            .map(|hit| SearchHit {
                kind: kind_word(hit.kind).to_string(),
                name: hit
                    .resource
                    .rsplit_once('/')
                    .map_or(hit.resource.as_str(), |(_, id)| id)
                    .to_string(),
                tags: hit.tags.clone(),
                source: hit.source.clone(),
                r#match: hit.r#match.clone(),
                located: None,
            })
            .collect(),
        remote: found
            .remote
            .iter()
            .map(|hit| RemoteFound {
                remote: hit.remote.clone(),
                name: hit.item.clone(),
                kind: kind_word(hit.kind).to_string(),
                tags: hit.tags.clone(),
                description: hit.description.clone(),
                author: hit.author.clone(),
                pull_ref: hit.pull_reference.clone(),
            })
            .collect(),
        count: u64::from(found.count),
        hidden_local: u64::from(found.hidden_local),
    }
}

/// A `Loadout` as ctxloom's own loadout, its `run:` bundle field for
/// field.
pub(crate) fn loadout(loadout: &v1::Loadout) -> library_application::loadout::OwnLoadout {
    let run = loadout.run.clone().unwrap_or_default();
    library_application::loadout::OwnLoadout {
        version: run.version,
        tags: run.tags,
        author: run.author,
        mcp: run
            .mcp_servers
            .into_iter()
            .map(|server| library_application::loadout::OwnServer {
                id: server.id,
                served_by: server.served_by,
            })
            .collect(),
    }
}

/// A `config show` as its text: the document as YAML, keys sorted.
pub(crate) fn config_text(config: &v1::Config) -> String {
    let document = config
        .document
        .as_ref()
        .map_or(serde_json::Value::Null, json_of);
    library_application::config_yaml(&document)
}

/// An item's own ID, its resource name's last segment.
fn item_id(name: &str) -> String {
    name.rsplit_once('/').map_or(name, |(_, id)| id).to_string()
}

/// A `bundle show` as its view: the bundle by its catalog name, each item
/// by its ID.
pub(crate) fn bundle_show(bundle: &v1::Bundle) -> BundleShow {
    let parent = parent_of(&bundle.name);
    let state = |item: &v1::BundleItem| state_of(item.distilled);
    BundleShow {
        name: library_api::names::catalog_name(parent, &bundle.name),
        path: bundle.path.clone(),
        version: bundle.version.clone(),
        author: bundle.author.clone(),
        description: bundle.description.clone(),
        tags: bundle.tags.clone(),
        notes: bundle.notes.clone(),
        installation: bundle.installation.clone(),
        fragments: bundle
            .fragments
            .iter()
            .map(|f| {
                (
                    item_id(&f.item),
                    ShownFragment {
                        tags: f.tags.clone(),
                        preview: f.preview.clone(),
                        distilled: state(f) != DistilledState::None,
                        no_distill: f.no_distill == Some(true),
                        premise: f.premise.clone(),
                        stale: state(f) == DistilledState::Stale,
                    },
                )
            })
            .collect(),
        commands: bundle
            .commands
            .iter()
            .map(|c| {
                (
                    item_id(&c.item),
                    ShownCommand {
                        tags: c.tags.clone(),
                        description: c.description.clone(),
                        preview: c.preview.clone(),
                        distilled: state(c) != DistilledState::None,
                        no_distill: c.no_distill == Some(true),
                        stale: state(c) == DistilledState::Stale,
                    },
                )
            })
            .collect(),
        mcp: bundle
            .mcp_servers
            .iter()
            .map(|m| {
                (
                    item_id(&m.item),
                    ShownMcp {
                        command: m.command.clone(),
                        args: m.args.clone(),
                        env: m.env.clone(),
                        notes: m.notes.clone(),
                        installation: m.installation.clone(),
                    },
                )
            })
            .collect(),
        skills: bundle
            .skills
            .iter()
            .map(|s| {
                (
                    item_id(&s.item),
                    ShownSkill {
                        path: s.path.clone(),
                        tags: s.tags.clone(),
                        notes: s.notes.clone(),
                    },
                )
            })
            .collect(),
        profiles: bundle.profiles.iter().map(|p| item_id(p)).collect(),
        hooks: bundle.hooks.clone(),
        listing: BundleRow {
            name: library_api::names::catalog_name(parent, &bundle.name),
            reference: bundle.reference.clone(),
            path: bundle.path.clone(),
            origin: String::new(),
            version: bundle.version.clone(),
            description: bundle.description.clone(),
            tags: bundle.tags.clone(),
            fragment_count: u64::from(bundle.fragment_count),
            command_count: u64::from(bundle.command_count),
            mcp_count: u64::from(bundle.mcp_server_count),
            profile_count: u64::from(bundle.profile_count),
            held: bundle.held == Some(true),
            commit: bundle.commit.clone(),
        },
    }
}

/// A `bundle view <bundle>` as its view: its files as one text, each under
/// its `==> path <==` line, its manifest first, as they were answered.
pub(crate) fn bundle_view(bundle: &v1::Bundle) -> BundleView {
    let parent = parent_of(&bundle.name);
    let blocks: Vec<String> = bundle
        .files
        .iter()
        .map(|file| {
            let tail = if file.content.ends_with('\n') {
                ""
            } else {
                "\n"
            };
            format!("==> {} <==\n{}{tail}", file.path, file.content)
        })
        .collect();
    BundleView {
        bundle: library_api::names::catalog_name(parent, &bundle.name),
        path: String::new(),
        distilled: bundle.distilled == Some(true),
        content: blocks.join("\n"),
        item: false,
        viewed: library_application::Viewed::Nothing,
    }
}

/// One item's `bundle view` (`<bundle>#<path>`, as asked), its content
/// as text prints it (step 10a U2, owner ruling 2026-11-17).
fn item_view(ask: &str, distilled: bool, content: String) -> BundleView {
    let (bundle, path) = ask.split_once('#').unwrap_or((ask, ""));
    BundleView {
        bundle: bundle.to_string(),
        path: path.to_string(),
        distilled,
        content,
        item: true,
        viewed: library_application::Viewed::Nothing,
    }
}

/// A fragment's or a command's `bundle view`: its body from the message,
/// headed `# (distilled version)` where the body is the distilled form.
pub(crate) fn body_view(ask: &str, body: &str, distilled: bool) -> BundleView {
    let header = if distilled {
        "# (distilled version)\n"
    } else {
        ""
    };
    let tail = if body.ends_with('\n') { "" } else { "\n" };
    item_view(ask, distilled, format!("{header}{body}{tail}"))
}

/// An item's fields as YAML under its `# <kind>: <id>` heading, every
/// key sorted and an empty field left out (owner ruling 2026-11-17: the
/// view is drawn from the item's message, not its file byte for byte).
fn fields_view(ask: &str, heading: &str, fields: Vec<(&str, serde_json::Value)>) -> BundleView {
    let set: serde_json::Map<String, serde_json::Value> = fields
        .into_iter()
        .filter(|(_, value)| match value {
            serde_json::Value::String(text) => !text.is_empty(),
            serde_json::Value::Array(items) => !items.is_empty(),
            serde_json::Value::Object(entries) => !entries.is_empty(),
            _ => true,
        })
        .map(|(key, value)| (key.to_string(), value))
        .collect();
    let yaml = if set.is_empty() {
        String::new()
    } else {
        library_application::config_yaml(&serde_json::Value::Object(set))
    };
    item_view(ask, false, format!("# {heading}\n{yaml}"))
}

/// A skill's `bundle view`: its bundle's entry for it (path, tags, notes).
pub(crate) fn skill_view(ask: &str, skill: &v1::Skill) -> BundleView {
    let (_, id) = skill_parts(&skill.name);
    fields_view(
        ask,
        &format!("Skill: {id}"),
        vec![
            ("notes", serde_json::json!(skill.notes)),
            ("path", serde_json::json!(skill.path)),
            ("tags", serde_json::json!(skill.tags)),
        ],
    )
}

/// An MCP server's `bundle view`: its configuration as the message says it.
pub(crate) fn mcp_view(ask: &str, server: &v1::McpServer) -> BundleView {
    fields_view(
        ask,
        &format!("MCP Server: {}", id_in(&server.name, "mcpServers")),
        vec![
            ("args", serde_json::json!(server.args)),
            ("command", serde_json::json!(server.command)),
            ("env", serde_json::json!(server.env)),
            ("installation", serde_json::json!(server.installation)),
            ("notes", serde_json::json!(server.notes)),
            ("served_by", serde_json::json!(server.served_by)),
            ("url", serde_json::json!(server.url)),
        ],
    )
}

/// A profile's `bundle view`: its composition, its parents, bundles and
/// fragments by their short references, its fragments and hooks in the
/// document's shape (a fragment of no priority by its name alone).
pub(crate) fn profile_view(ask: &str, profile: &v1::Profile) -> BundleView {
    let shown = profile_show(profile);
    let fragments: Vec<serde_json::Value> = shown
        .fragments
        .iter()
        .map(|fragment| match fragment.priority {
            0 => serde_json::json!(fragment.name),
            priority => serde_json::json!({"name": fragment.name, "priority": priority}),
        })
        .collect();
    let events = |events: &BTreeMap<String, Vec<Hook>>| -> serde_json::Value {
        events
            .iter()
            .map(|(event, list)| {
                let hooks: Vec<serde_json::Value> =
                    list.iter().map(crate::text::hook_document).collect();
                (event.clone(), serde_json::Value::Array(hooks))
            })
            .collect::<serde_json::Map<_, _>>()
            .into()
    };
    let mut hooks = serde_json::Map::new();
    if !shown.hooks.unified.is_empty() {
        hooks.insert("unified".to_string(), events(&shown.hooks.unified));
    }
    if !shown.hooks.ext.is_empty() {
        let ext: serde_json::Map<String, serde_json::Value> = shown
            .hooks
            .ext
            .iter()
            .map(|(engine, native)| (engine.clone(), events(native)))
            .collect();
        hooks.insert("ext".to_string(), ext.into());
    }
    fields_view(
        ask,
        &format!("Profile: {}", id_in(&profile.name, "profiles")),
        vec![
            ("bundles", serde_json::json!(shorts(&profile.bundles))),
            ("description", serde_json::json!(profile.description)),
            (
                "exclude_fragments",
                serde_json::json!(profile.exclude_fragments),
            ),
            (
                "exclude_mcp",
                serde_json::json!(profile.exclude_mcp_servers),
            ),
            ("deny_tools", serde_json::json!(profile.deny_tools)),
            ("fragments", serde_json::Value::Array(fragments)),
            ("hooks", serde_json::Value::Object(hooks)),
            ("parents", serde_json::json!(shorts(&profile.parents))),
            ("select_tags", serde_json::json!(profile.select_tags)),
            ("tags", serde_json::json!(profile.tags)),
            ("variables", serde_json::json!(profile.variables)),
        ],
    )
}

/// A plan's findings as the message says them.
pub(crate) fn findings(run: &v1::Run) -> Vec<library_application::FindingRow> {
    run.findings
        .iter()
        .map(|found| library_application::FindingRow {
            kind: found.kind.clone(),
            text: found.text.clone(),
            remedy: found.remedy.clone(),
            fatal: found.fatal,
        })
        .collect()
}

/// A `run --dry-run`'s `Run` as the plan its text is drawn from: its agent
/// and engine by their IDs, its profiles by their short references (owner
/// ruling 2026-11-17; configured text in a project nothing has named), the
/// workspace axis and runtime it
/// settles on, and the agent's settings with the agent they are of.
pub(crate) fn dry_run(run: &v1::Run) -> library_application::DryRun {
    library_application::DryRun {
        agent: id_in(&run.agent, "agents").to_string(),
        workspace: run.workspace.clone(),
        runtime: run.runtime.clone(),
        engine: id_in(&run.engine, "engines").to_string(),
        profiles: shorts(&run.profiles),
        fragments: run.fragments.clone(),
        context: run.context.clone(),
        tokens: u64::from(run.tokens),
        prompt: run.prompt.clone(),
        findings: findings(run),
        axes: (run.settled_workspace.clone(), run.settled_runtime.clone()),
        model: String::new(),
        effort: String::new(),
        permissions: None,
        drive: Default::default(),
        settings: run.settings.as_ref().map(agent_settings),
        settings_agent: run.settings_agent.clone(),
        surfaces: Default::default(),
        untrusted: Vec::new(),
    }
}

#[cfg(test)]
#[path = "read_back.test.rs"]
mod tests;
