//! The library's reads as the daemon answers them (step 10a U2): each a
//! use case of the library opened for the read, its view mapped to the
//! API's message ([`crate::resources`]) under the parent it was asked
//! under. What the use case noticed is answered beside it; what refused it
//! is the use case's own error, which the daemon answers as its typed
//! refusal (E5).
//!
//! Each read is `fn(library, flags, parent, request)`, the parent the
//! target is under (`projects/{harp}` or `users/me`), so one table,
//! [`served!`](crate::served), lists what the daemon serves, and each layer
//! generates its part from it (D12).

use daemon_proto::v1;
use library_application::{
    AssembleRequest, DefaultAgent, Flags, ItemKind, Library, Outcome, SearchRequest,
};

use crate::{names, resources};

/// A notice a read said, as the command says it: its code and args,
/// rendered by the command's own catalog.
pub fn said(notice: &v1::Notice) -> library_application::Notice {
    run_application::client::noticed(notice)
}

/// The diagnostic a daemon's notice said (its code and flattened args,
/// rebuilt), for a client to say in its own catalog's words or end a
/// command with.
pub fn reported(notice: &v1::Notice) -> coded_diagnostic::Report {
    run_application::client::report_of(&notice.code, &notice.args)
}

/// A notice a read said, as the daemon answers it: its code, flattened
/// args and text.
pub fn notice_of(notice: &library_application::Notice) -> v1::Notice {
    run_application::client::notice_of(notice)
}

/// A command's global flags as a read's request carries them (E1).
pub fn globals_of(flags: &Flags) -> v1::Globals {
    v1::Globals {
        degraded: flags.degraded,
        no_companions: flags.no_companions,
        config_set: flags.config_set.clone(),
    }
}

/// `outcome`'s answer as `f` maps it, its notices kept.
fn mapped<T, U>(outcome: Outcome<T>, f: impl FnOnce(T) -> U) -> Outcome<U> {
    Outcome {
        result: outcome.result.map(f),
        notices: outcome.notices,
    }
}

/// The library reads the daemon serves (step 10a U2, D12), in the shape
/// of `daemon_proto::library_reads!`'s rows, handed to `$each`: each row's
/// `reads::$method` answers it.
#[macro_export]
macro_rules! served {
    ($each:ident) => {
        $each! {
            list_bundles (ListBundles) ListBundlesRequest.parent => ListBundlesResponse,
            list_agents (ListAgents) ListAgentsRequest.parent => ListAgentsResponse,
            list_profiles (ListProfiles) ListProfilesRequest.parent => ListProfilesResponse,
            list_skills (ListSkills) ListSkillsRequest.parent => ListSkillsResponse,
            list_fragments (ListFragments) ListFragmentsRequest.parent => ListFragmentsResponse,
            list_commands (ListCommands) ListCommandsRequest.parent => ListCommandsResponse,
            get_fragment (GetFragment) GetFragmentRequest.name => Fragment,
            get_command (GetCommand) GetCommandRequest.name => Command,
            get_premise_index (GetPremiseIndex) GetPremiseIndexRequest.name => PremiseIndex,
            get_skill (GetSkill) GetSkillRequest.name => Skill,
            list_companions (ListCompanions) ListCompanionsRequest.parent => ListCompanionsResponse,
            get_profile (GetProfile) GetProfileRequest.name => Profile,
            get_agent (GetAgent) GetAgentRequest.name => Agent,
            list_engines (ListEngines) ListEnginesRequest.parent => ListEnginesResponse,
            get_engine (GetEngine) GetEngineRequest.name => Engine,
            list_mcp_servers (ListMcpServers) ListMcpServersRequest.parent => ListMcpServersResponse,
            get_mcp_server (GetMcpServer) GetMcpServerRequest.name => McpServer,
            search_library (SearchLibrary) SearchLibraryRequest.parent => SearchLibraryResponse,
            get_config (GetConfig) GetConfigRequest.name => Config,
            get_bundle (GetBundle) GetBundleRequest.name => Bundle,
            get_loadout (GetLoadout) GetLoadoutRequest.name => Loadout,
        }
    };
}

/// `ListBundles`: the whole catalog, every origin.
pub fn list_bundles(
    library: &Library,
    flags: &Flags,
    parent: &str,
    _: &v1::ListBundlesRequest,
) -> Outcome<v1::ListBundlesResponse> {
    mapped(library.bundle_list(flags), |list| {
        resources::bundles(parent, &list)
    })
}

/// `ListAgents`: every agent under `parent`, the one a bare run binds
/// marked `default`.
pub fn list_agents(
    library: &Library,
    flags: &Flags,
    parent: &str,
    _: &v1::ListAgentsRequest,
) -> Outcome<v1::ListAgentsResponse> {
    let read = library.agent_list(flags);
    // Asked only once the listing was had, so its notices are the
    // listing's alone.
    let default = default_agent(library, flags);
    mapped(read, |rows| resources::agents(parent, &rows, &default))
}

/// `ListProfiles`: every profile of the catalog.
pub fn list_profiles(
    library: &Library,
    flags: &Flags,
    parent: &str,
    _: &v1::ListProfilesRequest,
) -> Outcome<v1::ListProfilesResponse> {
    let read = library.profile_list(flags);
    mapped(read, |list| resources::profiles(parent, &list))
}

/// `ListSkills`: every skill of the catalog, or of the one bundle the
/// request's parent names (`.../bundles/{b}`).
pub fn list_skills(
    library: &Library,
    flags: &Flags,
    parent: &str,
    request: &v1::ListSkillsRequest,
) -> Outcome<v1::ListSkillsResponse> {
    let bundle = names::bundle_under(parent, &request.parent).unwrap_or_default();
    let read = library.skill_list(flags, &bundle);
    mapped(read, |list| resources::skills(parent, &list))
}

/// The reference a Get's `name` reads back to under `parent`
/// (`names::reference_target`'s), or the name's own text where it names
/// no bundle: the use case then refuses it in its own words.
fn reference_of(parent: &str, name: &str) -> String {
    names::reference_under(parent, name).unwrap_or_else(|| name.to_string())
}

/// `ListFragments`: every fragment of the catalog, or of the one bundle the
/// request's parent names.
pub fn list_fragments(
    library: &Library,
    flags: &Flags,
    parent: &str,
    request: &v1::ListFragmentsRequest,
) -> Outcome<v1::ListFragmentsResponse> {
    let bundle = names::bundle_under(parent, &request.parent).unwrap_or_default();
    let read = library.item_list(flags, ItemKind::Fragment, &bundle);
    mapped(read, |list| resources::fragments(parent, &list))
}

/// `ListCommands`: as [`list_fragments`], the commands.
pub fn list_commands(
    library: &Library,
    flags: &Flags,
    parent: &str,
    request: &v1::ListCommandsRequest,
) -> Outcome<v1::ListCommandsResponse> {
    let bundle = names::bundle_under(parent, &request.parent).unwrap_or_default();
    let read = library.item_list(flags, ItemKind::Command, &bundle);
    mapped(read, |list| resources::commands(parent, &list))
}

/// `GetFragment`: the fragment its name references, its distilled form
/// where one is preferred and fresh.
pub fn get_fragment(
    library: &Library,
    flags: &Flags,
    parent: &str,
    request: &v1::GetFragmentRequest,
) -> Outcome<v1::Fragment> {
    let reference = reference_of(parent, &request.name);
    let read = library.item_show(
        flags,
        ItemKind::Fragment,
        &reference,
        request.prefer_distilled,
    );
    mapped(read, |show| resources::fragment(parent, &show))
}

/// `GetCommand`: as [`get_fragment`], a command.
pub fn get_command(
    library: &Library,
    flags: &Flags,
    parent: &str,
    request: &v1::GetCommandRequest,
) -> Outcome<v1::Command> {
    let reference = reference_of(parent, &request.name);
    let read = library.item_show(
        flags,
        ItemKind::Command,
        &reference,
        request.prefer_distilled,
    );
    mapped(read, |show| resources::command(parent, &show))
}

/// `GetPremiseIndex`: what a premise holds back of the default profile's
/// assembly, and the instruction that offers it.
pub fn get_premise_index(
    library: &Library,
    flags: &Flags,
    parent: &str,
    _: &v1::GetPremiseIndexRequest,
) -> Outcome<v1::PremiseIndex> {
    mapped(library.premises(flags), |premises| {
        resources::premise_index(parent, &premises)
    })
}

/// `GetLoadout`: ctxloom's own loadout, what it contributes to a session
/// as its own companion; the same in every project, so nothing of the
/// library is read.
pub fn get_loadout(
    _: &Library,
    _: &Flags,
    parent: &str,
    _: &v1::GetLoadoutRequest,
) -> Outcome<v1::Loadout> {
    Outcome {
        result: Ok(resources::loadout(
            parent,
            &library_application::loadout::own(),
        )),
        notices: Vec::new(),
    }
}

/// `GetSkill`: the skill its name references, its files and their
/// executability.
pub fn get_skill(
    library: &Library,
    flags: &Flags,
    parent: &str,
    request: &v1::GetSkillRequest,
) -> Outcome<v1::Skill> {
    let reference = reference_of(parent, &request.name);
    mapped(library.skill_show(flags, &reference), |show| {
        resources::skill(parent, &show)
    })
}

/// `ListCompanions`: every registered companion, its program looked for on
/// the caller's `PATH` (never run).
pub fn list_companions(
    library: &Library,
    flags: &Flags,
    parent: &str,
    _: &v1::ListCompanionsRequest,
) -> Outcome<v1::ListCompanionsResponse> {
    mapped(library.companion_list(flags), |rows| {
        resources::companions(parent, &rows)
    })
}

/// `GetProfile`: the profile its name references (a project profile's
/// bare name, or `<bundle>#profiles/<name>`), read back as typed.
pub fn get_profile(
    library: &Library,
    flags: &Flags,
    parent: &str,
    request: &v1::GetProfileRequest,
) -> Outcome<v1::Profile> {
    let ask = names::profile_under(parent, &request.name).unwrap_or_else(|| request.name.clone());
    mapped(library.profile_show(flags, &ask), |show| {
        resources::profile_shown(parent, &show)
    })
}

/// The agent a bare run binds, empty when none.
fn default_agent(library: &Library, flags: &Flags) -> String {
    match library.agent_default(flags, None).result {
        Ok(DefaultAgent::Current(name)) => name,
        _ => String::new(),
    }
}

/// `GetAgent`: the agent its name names, by the text typed, what it
/// resolves to beside its binding (a resolution error in the library's
/// words).
pub fn get_agent(
    library: &Library,
    flags: &Flags,
    parent: &str,
    request: &v1::GetAgentRequest,
) -> Outcome<v1::Agent> {
    let name =
        names::named_under(parent, "agents", &request.name).unwrap_or_else(|| request.name.clone());
    let say = |d: &dyn coded_diagnostic::Diagnostic| library.say(d);
    let read = library.agent_show(flags, &name, &say);
    let default = default_agent(library, flags);
    mapped(read, |show| resources::agent_shown(parent, &show, &default))
}

/// `ListEngines`: every engine, with its agents.
pub fn list_engines(
    library: &Library,
    flags: &Flags,
    parent: &str,
    _: &v1::ListEnginesRequest,
) -> Outcome<v1::ListEnginesResponse> {
    mapped(library.engine_list(flags), |rows| {
        resources::engines(parent, &rows)
    })
}

/// `GetEngine`: the engine its name names, by the text typed.
pub fn get_engine(
    library: &Library,
    flags: &Flags,
    parent: &str,
    request: &v1::GetEngineRequest,
) -> Outcome<v1::Engine> {
    let name = names::named_under(parent, "engines", &request.name)
        .unwrap_or_else(|| request.name.clone());
    mapped(library.engine_show(flags, &name), |row| {
        resources::engine(parent, &row)
    })
}

/// `ListMcpServers`: every server the catalog's bundles ship, or (an
/// `mcp_server_id` asked, `mcp server show`) every bundle's server of that
/// ID, whole.
pub fn list_mcp_servers(
    library: &Library,
    flags: &Flags,
    parent: &str,
    request: &v1::ListMcpServersRequest,
) -> Outcome<v1::ListMcpServersResponse> {
    if request.mcp_server_id.is_empty() {
        return mapped(library.mcp_list(flags), |list| {
            resources::servers_listed(parent, &list)
        });
    }
    mapped(library.mcp_show(flags, &request.mcp_server_id), |show| {
        resources::servers_shown(parent, &show)
    })
}

/// `GetMcpServer`: one bundle's server, as `bundle view
/// <bundle>#mcp/<id>` reads it (a bundle of one server answers it
/// whatever the ID, as Go's view did), named by the server it found. A
/// name of no bundle is refused as the view refuses it.
pub fn get_mcp_server(
    library: &Library,
    flags: &Flags,
    parent: &str,
    request: &v1::GetMcpServerRequest,
) -> Outcome<v1::McpServer> {
    let reference = reference_of(parent, &request.name);
    let (bundle, path) = reference.split_once('#').unwrap_or((&reference, ""));
    let id = path.rsplit_once('/').map_or(path, |(_, id)| id);
    let read = library.bundle_view(flags, &format!("{bundle}#mcp/{id}"), false);
    let bundle = bundle.to_string();
    Outcome {
        result: read.result.and_then(|view| match view.viewed {
            library_application::Viewed::Mcp(row) => Ok(resources::server(
                &resources::server_name(parent, &bundle, &row.name),
                &row,
            )),
            _ => Err(library_application::AppError::ViewMissing {
                kind: "mcp server".to_string(),
                name: id.to_string(),
            }),
        }),
        notices: read.notices,
    }
}

/// `SearchLibrary`: the catalog's items (and the remotes' bundles) that
/// match the query or the tags, of the kind asked (none: every kind).
pub fn search_library(
    library: &Library,
    flags: &Flags,
    parent: &str,
    request: &v1::SearchLibraryRequest,
) -> Outcome<v1::SearchLibraryResponse> {
    let asked = SearchRequest {
        query: request.query.clone(),
        tags: request.tags.clone(),
        kind: resources::kind_word(request.kind).to_string(),
        local: request.local,
        remote: request.remote,
    };
    mapped(library.search(flags, &asked), |results| {
        resources::searched(parent, &results)
    })
}

/// `GetConfig`: the configuration the layers give (`EFFECTIVE`, the
/// default) or only what they set (`SET`, `--raw`), whole or one section.
pub fn get_config(
    library: &Library,
    flags: &Flags,
    parent: &str,
    request: &v1::GetConfigRequest,
) -> Outcome<v1::Config> {
    let raw = only_set(request);
    let section = (!request.section.is_empty()).then_some(request.section.as_str());
    mapped(library.config_show(flags, section, raw), |shown| {
        resources::config(parent, &shown)
    })
}

/// Whether `GetConfig` asks only for what the layers set (`SET`, `--raw`).
fn only_set(request: &v1::GetConfigRequest) -> bool {
    request.view == v1::config::View::Set as i32
}

/// `GetBundle`: the bundle its name names, by the text typed: what it
/// holds (FULL, `bundle show`), or its files (CONTENT, `bundle view`).
pub fn get_bundle(
    library: &Library,
    flags: &Flags,
    parent: &str,
    request: &v1::GetBundleRequest,
) -> Outcome<v1::Bundle> {
    if request.view == v1::bundle::View::Content as i32 {
        // An item's path read back with it (`<bundle>#<path>`), so a
        // path no item kind serves is refused as the view refuses it.
        let ask = reference_of(parent, &request.name);
        return mapped(
            library.bundle_view(flags, &ask, request.prefer_distilled),
            |view| resources::bundle_viewed(parent, &view),
        );
    }
    let ask = names::bundle_under(parent, &request.name).unwrap_or_else(|| request.name.clone());
    mapped(library.bundle_show(flags, &ask), |show| {
        resources::bundle_shown(parent, &show)
    })
}

/// `AssembleContext`: the context of the profiles, fragments and tags
/// asked (none: the default profile's), without its premise index, which
/// is answered as data for the edge that shows it to render (E2).
pub fn assemble_context(
    library: &Library,
    flags: &Flags,
    parent: &str,
    request: &v1::AssembleContextRequest,
) -> Outcome<v1::AssembledContext> {
    let asked = AssembleRequest {
        profiles: request.profiles.clone(),
        fragments: request.fragments.clone(),
        tags: request.tags.clone(),
        surface: None,
    };
    mapped(library.assemble(flags, &asked), |assembled| {
        resources::assembled(parent, &assembled)
    })
}

#[cfg(test)]
#[path = "reads.test.rs"]
mod tests;
