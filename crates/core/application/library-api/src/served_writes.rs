//! The library's writes as the daemon answers them (step 10b U3): each the
//! use case it plans with, and its answer as the API's message. The write
//! itself (observed, intended, made, recorded) is the daemon's; here is
//! what each verb asks of the library and how what it did is said, so
//! the CLI renders its text from the message alone (D10).
//!
//! Each verb is `plan(library, flags, request, apply)`, its use case over
//! the recording library (`apply` false: its preview), and
//! `respond(request, wrote)`, its response; one table,
//! [`served_writes!`](crate::served_writes), lists what the daemon serves
//! with the verb its intent is recorded under.

use std::sync::{Arc, Mutex};

use ctxloom_ports::{Editor, EditorCommand, Failure};
use daemon_proto::v1;
use daemon_proto::Carries as _;
use library_application::{
    BundleCreated, Flags, ItemCreated, ItemEdited, ItemKind, ItemRemoved, Library, Outcome, Removal,
};

use crate::names;

/// What a write came to (the daemon's `library_write`).
#[derive(Clone, Debug, PartialEq)]
pub struct Wrote<T> {
    /// The use case's answer: a preview's, or a write's planned in this
    /// call; none for the retry of a write already applied.
    pub answer: Option<T>,
    /// Whether the write is applied (by this call or the one it repeats).
    pub applied: bool,
    /// The token of the path the write changes (or, on a preview, would):
    /// the etag a confirmed apply sends back (AIP-154).
    pub etag: String,
    /// The path the write changes, where it is on disk; empty when nothing
    /// was to change.
    pub at: String,
    /// The parent its resources are named under (`projects/{harp}`,
    /// `users/me`).
    pub parent: String,
    /// What the observation and the use case said, in order.
    pub notices: Vec<v1::Notice>,
    /// Where a write done before read from, when it was an export (its
    /// retry's answer, AIP-155: the record's); empty otherwise.
    pub source: String,
}

/// The library writes the daemon serves (step 10b U3, D12), in the shape
/// of `daemon_proto::library_writes!`'s rows with each one's verb, handed
/// to `$each`: `served_writes::$method::{plan, respond}` answer it.
#[macro_export]
macro_rules! served_writes {
    ($each:ident) => {
        $each! {
            create_bundle (CreateBundle) CreateBundleRequest => CreateBundleResponse, "bundle create";
            update_bundle (UpdateBundle) UpdateBundleRequest => UpdateBundleResponse, "bundle edit";
            create_fragment (CreateFragment) CreateFragmentRequest => CreateFragmentResponse, "fragment create";
            update_fragment (UpdateFragment) UpdateFragmentRequest => UpdateFragmentResponse, "fragment edit";
            create_command (CreateCommand) CreateCommandRequest => CreateCommandResponse, "command create";
            update_command (UpdateCommand) UpdateCommandRequest => UpdateCommandResponse, "command edit";
            update_config (UpdateConfig) UpdateConfigRequest => UpdateConfigResponse, "config set";
            edit_config (EditConfig) EditConfigRequest => EditConfigResponse, "config edit";
            create_skill (CreateSkill) CreateSkillRequest => CreateSkillResponse, "skill create";
            update_mcp_server (UpdateMcpServer) UpdateMcpServerRequest => UpdateMcpServerResponse, "mcp server set";
            create_profile (CreateProfile) CreateProfileRequest => CreateProfileResponse, "profile create";
            update_profile (UpdateProfile) UpdateProfileRequest => UpdateProfileResponse, "profile edit";
            create_companion (CreateCompanion) CreateCompanionRequest => CreateCompanionResponse, "companion add";
            enable_companion (EnableCompanion) EnableCompanionRequest => EnableCompanionResponse, "companion enable";
            disable_companion (DisableCompanion) DisableCompanionRequest => DisableCompanionResponse, "companion disable";
            set_default_agent (SetDefaultAgent) SetDefaultAgentRequest => SetDefaultAgentResponse, "agent default";
            create_agent (CreateAgent) CreateAgentRequest => CreateAgentResponse, "agent create";
            update_agent (UpdateAgent) UpdateAgentRequest => UpdateAgentResponse, "agent edit";
            export_bundle (ExportBundle) ExportBundleRequest => ExportBundleResponse, "bundle export";
            move_bundle (MoveBundle) MoveBundleRequest => MoveBundleResponse, "bundle move";
            import_bundle (ImportBundle) ImportBundleRequest => ImportBundleResponse, "bundle import";
            delete_bundle (DeleteBundle) DeleteBundleRequest => DeleteBundleResponse, "bundle remove";
            delete_fragment (DeleteFragment) DeleteFragmentRequest => DeleteFragmentResponse, "fragment remove";
            delete_command (DeleteCommand) DeleteCommandRequest => DeleteCommandResponse, "command remove";
            delete_skill (DeleteSkill) DeleteSkillRequest => DeleteSkillResponse, "skill remove";
            delete_profile (DeleteProfile) DeleteProfileRequest => DeleteProfileResponse, "profile remove";
            delete_agent (DeleteAgent) DeleteAgentRequest => DeleteAgentResponse, "agent remove";
            delete_companion (DeleteCompanion) DeleteCompanionRequest => DeleteCompanionResponse, "companion remove";
        }
    };
}

/// The id of an editor round trip's need: a write asks one at most.
pub const EDIT_NEED: &str = "edit";

/// What the editor is asked of a write the daemon plans (step 10b U3): the
/// document's text, answered by the text the request gave (`--body`, or
/// the answer to the preview's Edit need), else handed back unchanged and
/// the ask kept, as the preview's Edit need. It launches nothing: the CLI
/// launches the editor, from the need (the client-side round trip is
/// gone).
#[derive(Default)]
pub struct AnsweredEditor {
    given: Option<String>,
    /// A preview's editor with no text to answer: it answers the document
    /// changed, so the preview plans the write its apply would make and
    /// answers that path's etag (never performed: a preview writes
    /// nothing), and still keeps the ask as the Edit need.
    probing: bool,
    asked: Mutex<Option<(EditorCommand, String, String)>>,
}

impl AnsweredEditor {
    /// The editor that answers `given`, when the request gave it.
    pub fn answering(given: Option<String>) -> Arc<AnsweredEditor> {
        Arc::new(AnsweredEditor {
            given,
            probing: false,
            asked: Mutex::new(None),
        })
    }

    /// The editor of a preview that is given no text: it probes (see
    /// [`AnsweredEditor::probing`]).
    pub fn probing() -> Arc<AnsweredEditor> {
        Arc::new(AnsweredEditor {
            given: None,
            probing: true,
            asked: Mutex::new(None),
        })
    }

    /// The Edit need of what it was asked and could not answer, if any:
    /// the document's text always, the EDITOR launch when the caller can
    /// launch one (`can_launch`).
    pub fn need(&self, can_launch: bool) -> Option<v1::Need> {
        if self.given.is_some() {
            return None;
        }
        let asked = self.asked.lock().ok()?.clone()?;
        let (command, file_name, text) = asked;
        let launch = can_launch.then(|| v1::Launch {
            kind: v1::launch::Kind::Editor as i32,
            program: command.program,
            args: command.args,
            ..v1::Launch::default()
        });
        Some(v1::Need {
            optional: false,
            id: EDIT_NEED.to_string(),
            kind: Some(v1::need::Kind::Edit(v1::Edit {
                file_name,
                text,
                launch,
            })),
        })
    }
}

impl Editor for AnsweredEditor {
    fn edit(
        &self,
        command: &EditorCommand,
        file_name: &str,
        content: &str,
    ) -> Result<String, Failure> {
        if let Some(given) = &self.given {
            return Ok(given.clone());
        }
        if let Ok(mut asked) = self.asked.lock() {
            *asked = Some((command.clone(), file_name.to_string(), content.to_string()));
        }
        if self.probing {
            return Ok(format!("{content}\n"));
        }
        Ok(content.to_string())
    }

    /// Nothing is opened in the daemon (`config edit` opens the file in
    /// place, which only the CLI's editor can).
    fn open(&self, _: &EditorCommand, _: &str) -> Result<(), Failure> {
        Err(Box::new(library_application::AppError::from(
            library_application::WriteError::NotWired {},
        )))
    }
}

/// A write's answer and the needs its preview asks (an Edit need, at most).
#[derive(Clone, Debug, PartialEq)]
pub struct Asked<T> {
    pub done: T,
    pub needs: Vec<v1::Need>,
}

/// The edited text a write's answers give its Edit need, if any.
fn edited(answers: &[v1::Answer]) -> Option<String> {
    answers
        .iter()
        .find(|answer| answer.need_id == EDIT_NEED)
        .and_then(|answer| match &answer.answer {
            Some(v1::answer::Answer::Edited(text)) => Some(text.clone()),
            _ => None,
        })
}

/// `use_case` run over `library` with its editor answering `given`, its
/// answer with the Edit need it could not answer.
fn editing<T>(
    library: &Library,
    (given, preview): (Option<String>, bool),
    can_launch: bool,
    use_case: impl FnOnce(&Library) -> Outcome<T>,
) -> Outcome<Asked<T>> {
    let editor = match given {
        None if preview => AnsweredEditor::probing(),
        given => AnsweredEditor::answering(given),
    };
    let editing = library
        .clone()
        .editing_with(Arc::clone(&editor) as Arc<dyn Editor>);
    let outcome = use_case(&editing);
    let needs = editor.need(can_launch).into_iter().collect();
    Outcome {
        result: outcome.result.map(|done| Asked { done, needs }),
        notices: outcome.notices,
    }
}

/// The bundle a parent or a resource name is under (`.../bundles/{b}`).
/// Go's two-word create (`placeholder`, step 10b): the item written with
/// Go's placeholder body under the bundle `parent` names, no editor asked; a
/// bundle nobody has is refused (`item_create`).
fn placeholder(
    library: &Library,
    flags: &Flags,
    kind: ItemKind,
    parent: &str,
    id: &str,
) -> Outcome<Asked<ItemCreated>> {
    let made = library.item_create(flags, kind, bundle_in(parent), id);
    Outcome {
        result: made.result.map(|done| Asked {
            done,
            needs: Vec::new(),
        }),
        notices: made.notices,
    }
}

fn bundle_in(name: &str) -> &str {
    name.split_once("/bundles/")
        .map_or("", |(_, rest)| rest.split('/').next().unwrap_or(""))
}

/// The item a resource name names (`.../bundles/{b}/{dir}/{id}`), as its
/// reference (`{b}#{dir}/{id}`).
fn reference_of(name: &str) -> String {
    let rest = name.split_once("/bundles/").map_or(name, |(_, rest)| rest);
    match rest.split_once('/') {
        Some((bundle, item)) => format!("{bundle}#{item}"),
        None => rest.to_string(),
    }
}

/// A body given (`--body`), or the Edit need's answer.
fn given(body: &str, answers: &[v1::Answer]) -> Option<String> {
    (!body.is_empty())
        .then(|| body.to_string())
        .or_else(|| edited(answers))
}

/// Whether the caller can launch the EDITOR.
fn can_launch(presentable: Option<&v1::Presentable>) -> bool {
    presentable.is_some_and(|p| p.can_launch)
}

/// The bundle its catalog name names, by its resource name alone.
fn just_named(parent: &str, bundle: &str) -> Option<v1::Bundle> {
    Some(v1::Bundle {
        name: names::bundle_name(parent, bundle),
        ..v1::Bundle::default()
    })
}

/// `bundle create <name> [-d description]`: `CreateBundle`.
pub mod create_bundle {
    use super::*;

    pub fn plan(
        library: &Library,
        flags: &Flags,
        request: &v1::CreateBundleRequest,
        _apply: bool,
    ) -> Outcome<BundleCreated> {
        let description = request
            .bundle
            .as_ref()
            .map_or("", |bundle| bundle.description.as_str());
        library.bundle_create(flags, &request.bundle_id, description)
    }

    pub fn respond(
        request: &v1::CreateBundleRequest,
        wrote: Wrote<BundleCreated>,
    ) -> v1::CreateBundleResponse {
        let path = match &wrote.answer {
            Some(made) => made.path.clone(),
            None => format!("{}/bundle.yaml", wrote.at),
        };
        v1::CreateBundleResponse {
            bundle: just_named(&wrote.parent, &request.bundle_id),
            path,
            applied: Some(wrote.applied),
            etag: wrote.etag,
            notices: wrote.notices,
            ..v1::CreateBundleResponse::default()
        }
    }
}

/// `bundle edit <bundle> [flags]`: `UpdateBundle`, the fields its mask
/// names (`description`, `version`) and its add and remove lists, planned
/// as one edit (owner ruling 2026-11-18: several mask paths, one step). One
/// file changed, or refused `OneChangePerCall` by the shape of its plan;
/// nothing to change is applied as nothing.
pub mod update_bundle {
    use super::*;
    use library_application::{BundleEditRequest, BundleEdited};

    /// Whether the request's mask names `path`.
    fn masked(request: &v1::UpdateBundleRequest, path: &str) -> bool {
        request
            .update_mask
            .as_ref()
            .is_some_and(|mask| mask.paths.iter().any(|p| p == path))
    }

    pub fn plan(
        library: &Library,
        flags: &Flags,
        request: &v1::UpdateBundleRequest,
        _apply: bool,
    ) -> Outcome<BundleEdited> {
        let bundle = request.bundle.clone().unwrap_or_default();
        let edit = BundleEditRequest {
            name: names::bundle_under(parent_of(&bundle.name), &bundle.name)
                .unwrap_or_else(|| bundle.name.clone()),
            description: masked(request, "description").then(|| bundle.description.clone()),
            version: masked(request, "version").then(|| bundle.version.clone()),
            add_tags: request.add_tags.clone(),
            remove_tags: request.remove_tags.clone(),
            add_fragments: request.add_fragments.clone(),
            remove_fragments: request.remove_fragments.clone(),
            add_prompts: request.add_commands.clone(),
            remove_prompts: request.remove_commands.clone(),
            add_mcp: request.add_mcp_servers.clone(),
            remove_mcp: request.remove_mcp_servers.clone(),
        };
        library.bundle_edit(flags, &edit)
    }

    pub fn respond(
        request: &v1::UpdateBundleRequest,
        wrote: Wrote<BundleEdited>,
    ) -> v1::UpdateBundleResponse {
        let name = request
            .bundle
            .as_ref()
            .map(|b| b.name.clone())
            .unwrap_or_default();
        let (path, changes) = match &wrote.answer {
            Some(edited) => (edited.path.clone(), edited.changes.clone()),
            None => (wrote.at.clone(), Vec::new()),
        };
        v1::UpdateBundleResponse {
            bundle: Some(v1::Bundle {
                name,
                ..v1::Bundle::default()
            }),
            path,
            applied: Some(wrote.applied),
            changes,
            etag: wrote.etag,
            notices: wrote.notices,
            ..v1::UpdateBundleResponse::default()
        }
    }
}

/// `profile create <name> [--parent P]... [--include B]... [--add-skill
/// S]... [-d D] [--bundle B]`: `CreateProfile`, the profile's one file in
/// the bundle its parent names (the project's own, `project`, when it
/// names none, made with it the first time: one new directory).
pub mod create_profile {
    use super::*;
    use library_application::{ProfileCreateRequest, ProfileCreated};

    pub fn plan(
        library: &Library,
        flags: &Flags,
        request: &v1::CreateProfileRequest,
        _apply: bool,
    ) -> Outcome<ProfileCreated> {
        let profile = request.profile.clone().unwrap_or_default();
        let asked = ProfileCreateRequest {
            name: request.profile_id.clone(),
            description: profile.description,
            parents: profile.parents,
            bundles: profile.bundles,
            skills: profile.skills,
            target: bundle_in(&request.parent).to_string(),
        };
        library.profile_create(flags, &asked)
    }

    pub fn respond(
        request: &v1::CreateProfileRequest,
        wrote: Wrote<ProfileCreated>,
    ) -> v1::CreateProfileResponse {
        let name = crate::resources::profile_name(
            &wrote.parent,
            bundle_in(&request.parent),
            &request.profile_id,
        );
        let profile = match &wrote.answer {
            Some(made) => v1::Profile {
                name,
                description: request
                    .profile
                    .as_ref()
                    .map(|p| p.description.clone())
                    .unwrap_or_default(),
                parents: crate::resources::profile_parents(&wrote.parent, &made.parents),
                bundles: crate::resources::profile_bundles(&wrote.parent, &made.bundles),
                path: made.path.clone(),
                ..<v1::Profile as Default>::default()
            },
            None => v1::Profile {
                name,
                ..<v1::Profile as Default>::default()
            },
        };
        v1::CreateProfileResponse {
            path: wrote
                .answer
                .as_ref()
                .map_or_else(|| wrote.at.clone(), |made| made.path.clone()),
            profile: Some(profile),
            applied: Some(wrote.applied),
            etag: wrote.etag,
            notices: wrote.notices,
            ..v1::CreateProfileResponse::default()
        }
    }
}

/// What a profile update came to: a patch's changes, or the editor's
/// round trip.
#[derive(Clone, Debug, PartialEq)]
pub enum ProfileUpdated {
    Patched(library_application::ProfileModified),
    Edited(library_application::ProfileEdited),
}

/// `profile edit <name> [flags]` (and `modify`): `UpdateProfile`. Its lists
/// and the fields its mask names (`description`) patch the profile's one
/// file; with none of them it is the editor's round trip (the preview's
/// Edit need, the apply its answer), the file written whole. Nothing to
/// change is applied as nothing.
pub mod update_profile {
    use super::*;
    use library_application::{ProfileEdit, ProfileEdited};

    /// The profile its resource name names, as the library takes it.
    fn asked(request: &v1::UpdateProfileRequest) -> String {
        let name = request
            .profile
            .as_ref()
            .map(|p| p.name.clone())
            .unwrap_or_default();
        names::profile_under(parent_of(&name), &name).unwrap_or(name)
    }

    /// The patch the request asks, none when it asks none (the editor's).
    fn patch(request: &v1::UpdateProfileRequest) -> Option<ProfileEdit> {
        let masked = |path: &str| {
            request
                .update_mask
                .as_ref()
                .is_some_and(|mask| mask.paths.iter().any(|p| p == path))
        };
        let lists = [
            &request.add_parents,
            &request.remove_parents,
            &request.add_bundles,
            &request.remove_bundles,
            &request.exclude_fragments,
            &request.include_fragments,
            &request.exclude_mcp_servers,
            &request.include_mcp_servers,
        ];
        // A mask sent, even one naming no path, asks a patch (`profile
        // modify <name>` alone changes nothing); none and no list, the
        // editor.
        if request.update_mask.is_none() && lists.iter().all(|list| list.is_empty()) {
            return None;
        }
        Some(ProfileEdit {
            description: masked("description").then(|| {
                request
                    .profile
                    .as_ref()
                    .map(|p| p.description.clone())
                    .unwrap_or_default()
            }),
            add_parents: request.add_parents.clone(),
            remove_parents: request.remove_parents.clone(),
            add_bundles: request.add_bundles.clone(),
            remove_bundles: request.remove_bundles.clone(),
            exclude_fragments: request.exclude_fragments.clone(),
            include_fragments: request.include_fragments.clone(),
            exclude_mcp: request.exclude_mcp_servers.clone(),
            include_mcp: request.include_mcp_servers.clone(),
        })
    }

    pub fn plan(
        library: &Library,
        flags: &Flags,
        request: &v1::UpdateProfileRequest,
        _apply: bool,
    ) -> Outcome<Asked<ProfileUpdated>> {
        let name = asked(request);
        if let Some(edit) = patch(request) {
            let outcome = library.profile_modify(flags, &name, &edit);
            return Outcome {
                result: outcome.result.map(|done| Asked {
                    done: ProfileUpdated::Patched(done),
                    needs: Vec::new(),
                }),
                notices: outcome.notices,
            };
        }
        let outcome = editing(
            library,
            (edited(&request.answers), request.validate_only),
            can_launch(request.caller_presentable()),
            |library| library.profile_edit(flags, &name),
        );
        Outcome {
            result: outcome.result.map(|asked| Asked {
                done: ProfileUpdated::Edited(asked.done),
                needs: asked.needs,
            }),
            notices: outcome.notices,
        }
    }

    pub fn respond(
        request: &v1::UpdateProfileRequest,
        wrote: Wrote<Asked<ProfileUpdated>>,
    ) -> v1::UpdateProfileResponse {
        let (path, changes, changed, needs) = match &wrote.answer {
            Some(Asked {
                done: ProfileUpdated::Patched(done),
                needs,
            }) => (
                wrote.at.clone(),
                done.changes.clone(),
                !done.changes.is_empty(),
                needs.clone(),
            ),
            Some(Asked {
                done: ProfileUpdated::Edited(ProfileEdited::Updated { path }),
                needs,
            }) => (path.clone(), Vec::new(), true, needs.clone()),
            Some(Asked {
                done: ProfileUpdated::Edited(ProfileEdited::Unchanged),
                needs,
            }) => (String::new(), Vec::new(), false, needs.clone()),
            None => (wrote.at.clone(), Vec::new(), true, Vec::new()),
        };
        v1::UpdateProfileResponse {
            profile: Some(v1::Profile {
                name: names::profile_ref(&wrote.parent, &asked(request)),
                ..<v1::Profile as Default>::default()
            }),
            path,
            applied: Some(wrote.applied && changed),
            changes,
            needs,
            etag: wrote.etag,
            notices: wrote.notices,
            ..v1::UpdateProfileResponse::default()
        }
    }
}

/// `companion add <name>`: `CreateCompanion`, the program the name stands
/// for probed (in the caller's environment, at her project), then the name
/// registered in the HOME config, one layer; one registered already is
/// left as it was, and applies nothing.
pub mod create_companion {
    use super::*;
    use library_application::CompanionAdded;

    pub fn plan(
        library: &Library,
        flags: &Flags,
        request: &v1::CreateCompanionRequest,
        _apply: bool,
    ) -> Outcome<CompanionAdded> {
        library.companion_add(flags, &request.companion_id)
    }

    pub fn respond(
        request: &v1::CreateCompanionRequest,
        wrote: Wrote<CompanionAdded>,
    ) -> v1::CreateCompanionResponse {
        let name = crate::resources::companion_name(&wrote.parent, &request.companion_id);
        let companion = match &wrote.answer {
            Some(done) => v1::Companion {
                name,
                bin: done.program.clone(),
                resolves: Some(true),
                state: v1::companion::State::Enabled.into(),
            },
            None => v1::Companion {
                name,
                ..v1::Companion::default()
            },
        };
        let added = wrote.answer.as_ref().is_none_or(|done| done.added);
        v1::CreateCompanionResponse {
            companion: Some(companion),
            path: wrote.at.clone(),
            applied: Some(wrote.applied && added),
            etag: wrote.etag,
            notices: wrote.notices,
            ..v1::CreateCompanionResponse::default()
        }
    }
}

/// `companion enable|disable <name>` (step 10b, owner 2026-11-18, night):
/// the companion's state written to the HOME config, one layer, answered
/// as the write leaves it; applied only when the state changed.
macro_rules! companion_state_write {
    ($module:ident, $request:ident, $response:ident, $state:ident) => {
        pub mod $module {
            use super::*;
            use library_application::CompanionStateSet;

            pub fn plan(
                library: &Library,
                flags: &Flags,
                request: &v1::$request,
                _apply: bool,
            ) -> Outcome<CompanionStateSet> {
                let asked =
                    names::named_under(parent_of(&request.name), "companions", &request.name)
                        .unwrap_or_else(|| request.name.clone());
                library.companion_set_state(
                    flags,
                    &asked,
                    library_application::CompanionState::$state,
                )
            }

            pub fn respond(
                request: &v1::$request,
                wrote: Wrote<CompanionStateSet>,
            ) -> v1::$response {
                let companion = match &wrote.answer {
                    Some(done) => v1::Companion {
                        name: request.name.clone(),
                        bin: done.program.clone(),
                        resolves: None,
                        state: crate::resources::companion_state(done.state).into(),
                    },
                    None => v1::Companion {
                        name: request.name.clone(),
                        ..v1::Companion::default()
                    },
                };
                let changed = wrote.answer.as_ref().is_none_or(|done| done.changed);
                v1::$response {
                    companion: Some(companion),
                    path: wrote.at.clone(),
                    applied: Some(wrote.applied && changed),
                    etag: wrote.etag,
                    notices: wrote.notices,
                }
            }
        }
    };
}

companion_state_write!(
    enable_companion,
    EnableCompanionRequest,
    EnableCompanionResponse,
    Enabled
);
companion_state_write!(
    disable_companion,
    DisableCompanionRequest,
    DisableCompanionResponse,
    Disabled
);

/// `agent default <name>`: `SetDefaultAgent`, the agent a bare run binds
/// written to the config, one layer; an agent not defined yet is set all
/// the same, and said so (`defined`).
pub mod set_default_agent {
    use super::*;
    use library_application::DefaultAgent;

    pub fn plan(
        library: &Library,
        flags: &Flags,
        request: &v1::SetDefaultAgentRequest,
        _apply: bool,
    ) -> Outcome<DefaultAgent> {
        library.agent_default(flags, Some(&agent_in(&request.agent)))
    }

    pub fn respond(
        request: &v1::SetDefaultAgentRequest,
        wrote: Wrote<DefaultAgent>,
    ) -> v1::SetDefaultAgentResponse {
        let (agent, defined) = match &wrote.answer {
            Some(DefaultAgent::Set { name, defined }) => (name.clone(), *defined),
            Some(DefaultAgent::Current(name)) => (name.clone(), true),
            None => (agent_in(&request.agent), true),
        };
        v1::SetDefaultAgentResponse {
            agent,
            defined,
            applied: Some(wrote.applied),
            etag: wrote.etag,
            notices: wrote.notices,
            ..v1::SetDefaultAgentResponse::default()
        }
    }
}

/// What an agent write came to (`agent create|edit`): the binding written,
/// or an engine swap's preview, and the agent a bare run binds (OQ-7), so
/// the answer says whether this one is it.
#[derive(Clone, Debug)]
pub struct AgentWrote {
    pub written: library_application::AgentWritten,
    pub default: String,
}

/// The agent a request's resource name names (its last segment).
fn agent_in(name: &str) -> String {
    names::named_under(parent_of(name), "agents", name).unwrap_or_else(|| name.to_string())
}

/// An engine as the binding says it: its id, when given as the resource
/// name a read answers (`.../engines/{e}`).
fn engine_word(engine: &str) -> String {
    engine
        .rsplit_once("/engines/")
        .map_or(engine, |(_, id)| id)
        .to_string()
}

/// The agent's fields as a write's request: each one `given` says is given
/// (a create's are its non-empty fields, an edit's those its mask names).
fn agent_request(
    agent: &v1::Agent,
    home: bool,
    given: impl Fn(&str, bool) -> bool,
) -> library_application::AgentRequest {
    let text = |path: &str, value: &str| given(path, value.is_empty()).then(|| value.to_string());
    let pairs = |map: &std::collections::BTreeMap<String, String>| {
        map.iter()
            .map(|(k, v)| (k.clone(), v.clone()))
            .collect::<Vec<_>>()
    };
    library_application::AgentRequest {
        engine: given("engine", agent.engine.is_empty()).then(|| engine_word(&agent.engine)),
        model: text("model", &agent.model),
        effort: text("effort", &agent.effort),
        profiles: given("profiles", agent.profiles.is_empty()).then(|| agent.profiles.clone()),
        runtime: text("runtime", &agent.runtime),
        surfaces: given("surfaces", agent.surfaces.is_empty()).then(|| pairs(&agent.surfaces)),
        roots: given("roots", agent.roots.is_empty()).then(|| pairs(&agent.roots)),
        env: given("env", agent.env.is_empty()).then(|| agent.env.clone()),
        session_home: text("session_home", &agent.session_home),
        home,
    }
}

/// The agent a bare run binds, empty when none (OQ-7).
fn default_agent(library: &Library, flags: &Flags) -> String {
    match library.agent_default(flags, None).result {
        Ok(library_application::DefaultAgent::Current(name)) => name,
        _ => String::new(),
    }
}

/// An agent write's outcome, with the default agent beside it.
fn agent_wrote(
    library: &Library,
    flags: &Flags,
    outcome: Outcome<library_application::AgentWritten>,
) -> Outcome<AgentWrote> {
    Outcome {
        result: outcome.result.map(|written| AgentWrote {
            written,
            default: default_agent(library, flags),
        }),
        notices: outcome.notices,
    }
}

/// An agent written, as its response's `agent`: the binding as the config
/// holds it now, or (a swap's preview, a retry) named alone.
fn agent_answered(
    asked: &str,
    wrote: &Wrote<AgentWrote>,
) -> (v1::Agent, Option<library_application::SwapPreview>) {
    let named = v1::Agent {
        name: asked.to_string(),
        ..<v1::Agent as Default>::default()
    };
    match &wrote.answer {
        Some(AgentWrote {
            written: library_application::AgentWritten::Done(row),
            default,
        }) => (crate::resources::agent(&wrote.parent, row, default), None),
        Some(AgentWrote {
            written: library_application::AgentWritten::Preview(preview),
            ..
        }) => (named, Some(preview.clone())),
        None => (named, None),
    }
}

/// `agent create <name> [flags] [--home]`: `CreateAgent`, the binding its
/// fields give (a field left empty is not given), one config layer
/// written; a name taken is refused (never an upsert).
pub mod create_agent {
    use super::*;

    pub fn plan(
        library: &Library,
        flags: &Flags,
        request: &v1::CreateAgentRequest,
        _apply: bool,
    ) -> Outcome<AgentWrote> {
        let agent = request.agent.clone().unwrap_or_default();
        let asked = agent_request(&agent, request.home, |_, empty| !empty);
        let outcome = library.agent_write(flags, &request.agent_id, &asked, false);
        let outcome = Outcome {
            result: outcome.result.map(library_application::AgentWritten::Done),
            notices: outcome.notices,
        };
        agent_wrote(library, flags, outcome)
    }

    pub fn respond(
        request: &v1::CreateAgentRequest,
        wrote: Wrote<AgentWrote>,
    ) -> v1::CreateAgentResponse {
        let asked = crate::resources::agent_name(&request.parent, &request.agent_id);
        let (agent, _) = agent_answered(&asked, &wrote);
        v1::CreateAgentResponse {
            agent: Some(agent),
            path: wrote.at.clone(),
            applied: Some(wrote.applied),
            etag: wrote.etag,
            notices: wrote.notices,
            ..v1::CreateAgentResponse::default()
        }
    }
}

/// `agent edit <name> [flags] [--home] [--yes]`: `UpdateAgent`, the fields
/// its mask names (one given empty is cleared), one config layer written.
/// An engine swap whose new engine cannot carry what the old one did is,
/// on a preview, the swap's preview (from, to, what is given up and the
/// command that applies it), planning nothing; an apply is the `--yes`.
pub mod update_agent {
    use super::*;

    pub fn plan(
        library: &Library,
        flags: &Flags,
        request: &v1::UpdateAgentRequest,
        apply: bool,
    ) -> Outcome<AgentWrote> {
        let agent = request.agent.clone().unwrap_or_default();
        let mask = request
            .update_mask
            .as_ref()
            .map_or(&[][..], |mask| mask.paths.as_slice());
        let asked = agent_request(&agent, request.home, |path, _| {
            mask.iter().any(|p| p == path)
        });
        let outcome = library.agent_edit(flags, &agent_in(&agent.name), &asked, apply);
        agent_wrote(library, flags, outcome)
    }

    pub fn respond(
        request: &v1::UpdateAgentRequest,
        wrote: Wrote<AgentWrote>,
    ) -> v1::UpdateAgentResponse {
        let asked = request
            .agent
            .as_ref()
            .map(|agent| agent.name.clone())
            .unwrap_or_default();
        let (agent, preview) = agent_answered(&asked, &wrote);
        let mut answered = v1::UpdateAgentResponse {
            agent: Some(agent),
            path: wrote.at.clone(),
            applied: Some(wrote.applied),
            etag: wrote.etag,
            notices: wrote.notices,
            ..v1::UpdateAgentResponse::default()
        };
        if let Some(preview) = preview {
            answered.needs = vec![swap_confirm(&preview)];
            answered.applied = Some(false);
            answered.apply_command = preview.apply;
            answered.from = preview.from;
            answered.to = preview.to;
            answered.not_carried = preview
                .not_carried
                .iter()
                .map(crate::resources::not_carried)
                .collect();
        }
        answered
    }
}

/// A swap preview's Confirm need (owner ruling 2026-11-18, evening: an
/// `agent edit` whose new engine gives something up is confirmed at the
/// terminal, as a removal is): its question, drafted in the 10b spec's
/// texts.md (`library-api-confirm-swap`), and its args.
fn swap_confirm(preview: &library_application::SwapPreview) -> v1::Need {
    let arg = |name: &str, value: &str| v1::NeedArg {
        name: name.to_string(),
        value: value.to_string(),
    };
    v1::Need {
        optional: false,
        id: CONFIRM_NEED.to_string(),
        kind: Some(v1::need::Kind::Confirm(v1::Confirm {
            text: format!(
                "Apply the move of agent \"{}\" from {} to {}, giving up what {} cannot carry?",
                preview.agent, preview.from, preview.to, preview.to
            ),
            code: String::new(),
            args: vec![
                arg("agent", &preview.agent),
                arg("from", &preview.from),
                arg("to", &preview.to),
            ],
        })),
    }
}

/// An item created's resource name.
fn item_name(parent: &str, made: &ItemCreated, dir: &str) -> String {
    format!(
        "{}/{dir}/{}",
        names::bundle_name(parent, &made.bundle),
        made.name
    )
}

/// `fragment create <bundle>#fragments/<id> [--body]`: `CreateFragment`,
/// its editor round trip the preview's Edit need.
pub mod create_fragment {
    use super::*;

    pub fn plan(
        library: &Library,
        flags: &Flags,
        request: &v1::CreateFragmentRequest,
        _apply: bool,
    ) -> Outcome<Asked<ItemCreated>> {
        if request.placeholder {
            return placeholder(
                library,
                flags,
                ItemKind::Fragment,
                &request.parent,
                &request.fragment_id,
            );
        }
        let body = request.fragment.as_ref().map_or("", |f| f.body.as_str());
        let reference = format!(
            "{}#fragments/{}",
            bundle_in(&request.parent),
            request.fragment_id
        );
        editing(
            library,
            (given(body, &request.answers), false),
            can_launch(request.caller_presentable()),
            |library| library.item_create_at(flags, ItemKind::Fragment, &reference),
        )
    }

    pub fn respond(
        request: &v1::CreateFragmentRequest,
        wrote: Wrote<Asked<ItemCreated>>,
    ) -> v1::CreateFragmentResponse {
        let (name, path, bundle_created, needs) = match &wrote.answer {
            Some(asked) => (
                item_name(&wrote.parent, &asked.done, "fragments"),
                asked.done.path.clone(),
                asked.done.bundle_created,
                asked.needs.clone(),
            ),
            None => (
                format!(
                    "{}/fragments/{}",
                    names::bundle_name(&wrote.parent, bundle_in(&request.parent)),
                    request.fragment_id
                ),
                String::new(),
                false,
                Vec::new(),
            ),
        };
        v1::CreateFragmentResponse {
            fragment: Some(v1::Fragment {
                name,
                ..v1::Fragment::default()
            }),
            path,
            applied: Some(wrote.applied),
            bundle_created,
            needs,
            etag: wrote.etag,
            notices: wrote.notices,
            ..v1::CreateFragmentResponse::default()
        }
    }
}

/// `command create <bundle>#commands/<id> [--body]`: `CreateCommand`.
pub mod create_command {
    use super::*;

    pub fn plan(
        library: &Library,
        flags: &Flags,
        request: &v1::CreateCommandRequest,
        _apply: bool,
    ) -> Outcome<Asked<ItemCreated>> {
        if request.placeholder {
            return placeholder(
                library,
                flags,
                ItemKind::Command,
                &request.parent,
                &request.command_id,
            );
        }
        let body = request.command.as_ref().map_or("", |c| c.body.as_str());
        let reference = format!(
            "{}#commands/{}",
            bundle_in(&request.parent),
            request.command_id
        );
        editing(
            library,
            (given(body, &request.answers), false),
            can_launch(request.caller_presentable()),
            |library| library.item_create_at(flags, ItemKind::Command, &reference),
        )
    }

    pub fn respond(
        request: &v1::CreateCommandRequest,
        wrote: Wrote<Asked<ItemCreated>>,
    ) -> v1::CreateCommandResponse {
        let (name, path, bundle_created, needs) = match &wrote.answer {
            Some(asked) => (
                item_name(&wrote.parent, &asked.done, "commands"),
                asked.done.path.clone(),
                asked.done.bundle_created,
                asked.needs.clone(),
            ),
            None => (
                format!(
                    "{}/commands/{}",
                    names::bundle_name(&wrote.parent, bundle_in(&request.parent)),
                    request.command_id
                ),
                String::new(),
                false,
                Vec::new(),
            ),
        };
        v1::CreateCommandResponse {
            command: Some(v1::Command {
                name,
                ..v1::Command::default()
            }),
            path,
            applied: Some(wrote.applied),
            bundle_created,
            needs,
            etag: wrote.etag,
            notices: wrote.notices,
            ..v1::CreateCommandResponse::default()
        }
    }
}

/// An edit's distilled state, as its message says it: `FRESH` once
/// refreshed, else no claim.
fn distilled_of(edited: &ItemEdited) -> i32 {
    if edited.distilled {
        v1::DistilledState::Fresh as i32
    } else {
        v1::DistilledState::Unspecified as i32
    }
}

/// `fragment edit <ref> [--body] [--no-distill]`: `UpdateFragment`, its
/// editor round trip the preview's Edit need. Only an edit that changed
/// the file is applied.
pub mod update_fragment {
    use super::*;

    pub fn plan(
        library: &Library,
        flags: &Flags,
        request: &v1::UpdateFragmentRequest,
        _apply: bool,
    ) -> Outcome<Asked<ItemEdited>> {
        let fragment = request.fragment.clone().unwrap_or_default();
        editing(
            library,
            (
                given(&fragment.body, &request.answers),
                request.validate_only,
            ),
            can_launch(request.caller_presentable()),
            |library| {
                library.item_edit(
                    flags,
                    ItemKind::Fragment,
                    &reference_of(&fragment.name),
                    fragment.no_distill.unwrap_or(false),
                )
            },
        )
    }

    pub fn respond(
        request: &v1::UpdateFragmentRequest,
        wrote: Wrote<Asked<ItemEdited>>,
    ) -> v1::UpdateFragmentResponse {
        let name = request
            .fragment
            .as_ref()
            .map(|f| f.name.clone())
            .unwrap_or_default();
        let (path, distilled, needs, changed) = match &wrote.answer {
            Some(asked) => (
                asked.done.path.clone(),
                distilled_of(&asked.done),
                asked.needs.clone(),
                asked.done.status != "unchanged",
            ),
            None => (String::new(), 0, Vec::new(), true),
        };
        v1::UpdateFragmentResponse {
            fragment: Some(v1::Fragment {
                name,
                distilled,
                ..v1::Fragment::default()
            }),
            path,
            applied: Some(wrote.applied && changed),
            needs,
            etag: wrote.etag,
            notices: wrote.notices,
            ..v1::UpdateFragmentResponse::default()
        }
    }
}

/// `command edit <ref> [--body] [--no-distill]`: `UpdateCommand`.
pub mod update_command {
    use super::*;

    pub fn plan(
        library: &Library,
        flags: &Flags,
        request: &v1::UpdateCommandRequest,
        _apply: bool,
    ) -> Outcome<Asked<ItemEdited>> {
        let command = request.command.clone().unwrap_or_default();
        editing(
            library,
            (
                given(&command.body, &request.answers),
                request.validate_only,
            ),
            can_launch(request.caller_presentable()),
            |library| {
                library.item_edit(
                    flags,
                    ItemKind::Command,
                    &reference_of(&command.name),
                    command.no_distill.unwrap_or(false),
                )
            },
        )
    }

    pub fn respond(
        request: &v1::UpdateCommandRequest,
        wrote: Wrote<Asked<ItemEdited>>,
    ) -> v1::UpdateCommandResponse {
        let name = request
            .command
            .as_ref()
            .map(|c| c.name.clone())
            .unwrap_or_default();
        let (path, distilled, needs, changed) = match &wrote.answer {
            Some(asked) => (
                asked.done.path.clone(),
                distilled_of(&asked.done),
                asked.needs.clone(),
                asked.done.status != "unchanged",
            ),
            None => (String::new(), 0, Vec::new(), true),
        };
        v1::UpdateCommandResponse {
            command: Some(v1::Command {
                name,
                distilled,
                ..v1::Command::default()
            }),
            path,
            applied: Some(wrote.applied && changed),
            needs,
            etag: wrote.etag,
            notices: wrote.notices,
            ..v1::UpdateCommandResponse::default()
        }
    }
}

/// `config set <key> <value> [--home]`: `UpdateConfig` (T29: one key per
/// call).
pub mod update_config {
    use super::*;
    use library_application::ConfigSet;

    pub fn plan(
        library: &Library,
        flags: &Flags,
        request: &v1::UpdateConfigRequest,
        _apply: bool,
    ) -> Outcome<ConfigSet> {
        library.config_set(flags, &request.path, &request.value, request.home)
    }

    pub fn respond(
        request: &v1::UpdateConfigRequest,
        wrote: Wrote<ConfigSet>,
    ) -> v1::UpdateConfigResponse {
        let path = match &wrote.answer {
            Some(set) => set.path.clone(),
            None => wrote.at.clone(),
        };
        v1::UpdateConfigResponse {
            config: Some(v1::Config {
                name: format!("{}/config", wrote.parent),
                ..v1::Config::default()
            }),
            path,
            applied: Some(wrote.applied),
            changes: vec![format!("{} = {}", request.path, request.value)],
            etag: wrote.etag,
            notices: wrote.notices,
            ..v1::UpdateConfigResponse::default()
        }
    }
}

/// `config edit`: `EditConfig`, the config of where it is run through the
/// editor (its preview's Edit need, its apply that need's answer), one
/// layer written whole; left as it was, nothing is applied.
pub mod edit_config {
    use super::*;
    use library_application::ConfigEdited;

    pub fn plan(
        library: &Library,
        _flags: &Flags,
        request: &v1::EditConfigRequest,
        _apply: bool,
    ) -> Outcome<Asked<ConfigEdited>> {
        editing(
            library,
            (edited(&request.answers), request.validate_only),
            can_launch(request.caller_presentable()),
            |library| library.config_edited(),
        )
    }

    pub fn respond(
        request: &v1::EditConfigRequest,
        wrote: Wrote<Asked<ConfigEdited>>,
    ) -> v1::EditConfigResponse {
        let (path, changed, needs) = match &wrote.answer {
            Some(asked) => (
                asked.done.path.clone(),
                asked.done.changed,
                asked.needs.clone(),
            ),
            None => (wrote.at.clone(), true, Vec::new()),
        };
        v1::EditConfigResponse {
            config: Some(v1::Config {
                name: request.name.clone(),
                ..v1::Config::default()
            }),
            path,
            applied: Some(wrote.applied && changed),
            needs,
            etag: wrote.etag,
            notices: wrote.notices,
        }
    }
}

/// `skill create <bundle>#skills/<name> [-d description]`: `CreateSkill`,
/// the skill one new directory, its metadata inside it.
pub mod create_skill {
    use super::*;
    use library_application::SkillCreated;

    pub fn plan(
        library: &Library,
        flags: &Flags,
        request: &v1::CreateSkillRequest,
        _apply: bool,
    ) -> Outcome<SkillCreated> {
        let description = request
            .skill
            .as_ref()
            .map_or("", |s| s.description.as_str());
        library.skill_create(
            flags,
            bundle_in(&request.parent),
            &request.skill_id,
            description,
        )
    }

    pub fn respond(
        request: &v1::CreateSkillRequest,
        wrote: Wrote<SkillCreated>,
    ) -> v1::CreateSkillResponse {
        let (bundle, id, dir) = match &wrote.answer {
            Some(made) => (made.bundle.clone(), made.name.clone(), made.dir.clone()),
            None => (
                bundle_in(&request.parent).to_string(),
                request.skill_id.clone(),
                wrote.at.clone(),
            ),
        };
        v1::CreateSkillResponse {
            skill: Some(v1::Skill {
                name: format!("{}/skills/{id}", names::bundle_name(&wrote.parent, &bundle)),
                path: dir.clone(),
                ..v1::Skill::default()
            }),
            path: dir,
            applied: Some(wrote.applied),
            etag: wrote.etag,
            notices: wrote.notices,
            ..v1::CreateSkillResponse::default()
        }
    }
}

/// What an `UpdateMcpServer` came to: a `set`'s fields, or an `edit`'s
/// round trip through the editor (the preview's Edit need).
#[derive(Clone, Debug)]
pub enum McpWritten {
    Set(library_application::McpSet),
    Edited(Asked<library_application::McpEdited>),
}

/// `mcp server set <ref> [flags]` and `mcp server edit <ref>`:
/// `UpdateMcpServer`. A mask names the fields a set writes (`command`,
/// `args`, `env`, `url`, `notes`, `installation`; a field named and given
/// empty is cleared); no mask is the whole server through the editor, the
/// preview's Edit need. A set creates a server its bundle lacks only when
/// `allow_missing` (AIP-134; the CLI sends true), else it is refused
/// `LibraryMcpNotFound`.
pub mod update_mcp_server {
    use super::*;
    use library_application::{McpEdited, McpPatch};

    /// The mask's paths, none when it names none (the editor's round trip).
    fn mask(request: &v1::UpdateMcpServerRequest) -> &[String] {
        request
            .update_mask
            .as_ref()
            .map_or(&[], |mask| mask.paths.as_slice())
    }

    /// The `<bundle>#mcp/<id>` reference the request's server name names.
    fn reference(request: &v1::UpdateMcpServerRequest) -> String {
        let name = request
            .mcp_server
            .as_ref()
            .map_or("", |server| server.name.as_str());
        let reference =
            names::reference_under(parent_of(name), name).unwrap_or_else(|| name.to_string());
        reference.replacen("#mcpServers/", "#mcp/", 1)
    }

    /// The patch a set's mask names: each named field as given (empty, a
    /// clear), the rest left as they are.
    fn patch(request: &v1::UpdateMcpServerRequest) -> McpPatch {
        let server = request.mcp_server.clone().unwrap_or_default();
        let named = |path: &str| mask(request).iter().any(|p| p == path);
        McpPatch {
            command: named("command").then(|| server.command.clone()),
            args: named("args").then(|| server.args.clone()),
            env: named("env").then(|| server.env.clone().into_iter().collect()),
            url: named("url").then(|| server.url.clone()),
            notes: named("notes").then(|| server.notes.clone()),
            installation: named("installation").then(|| server.installation.clone()),
            ..McpPatch::default()
        }
    }

    pub fn plan(
        library: &Library,
        flags: &Flags,
        request: &v1::UpdateMcpServerRequest,
        _apply: bool,
    ) -> Outcome<McpWritten> {
        let reference = reference(request);
        if !mask(request).is_empty() {
            let set = library.mcp_update(flags, &reference, &patch(request), request.allow_missing);
            return Outcome {
                result: set.result.map(McpWritten::Set),
                notices: set.notices,
            };
        }
        let edited = editing(
            library,
            (edited(&request.answers), request.validate_only),
            can_launch(request.caller_presentable()),
            |library| library.mcp_edit(flags, &reference),
        );
        Outcome {
            result: edited.result.map(McpWritten::Edited),
            notices: edited.notices,
        }
    }

    pub fn respond(
        request: &v1::UpdateMcpServerRequest,
        wrote: Wrote<McpWritten>,
    ) -> v1::UpdateMcpServerResponse {
        let asked = request
            .mcp_server
            .as_ref()
            .map(|server| server.name.clone())
            .unwrap_or_default();
        let (name, path, created, needs) = match &wrote.answer {
            Some(McpWritten::Set(set)) => (
                names::item_name(&wrote.parent, &set.bundle, "mcp", &set.name),
                set.path.clone(),
                set.status == "created",
                Vec::new(),
            ),
            Some(McpWritten::Edited(asked_of)) => {
                let name = match &asked_of.done {
                    McpEdited::Updated { name, bundle } => {
                        names::item_name(&wrote.parent, bundle, "mcp", name)
                    }
                    McpEdited::Unchanged => {
                        names::reference_name(&wrote.parent, &reference(request))
                            .unwrap_or_else(|| asked.clone())
                    }
                };
                (name, String::new(), false, asked_of.needs.clone())
            }
            None => (asked, wrote.at.clone(), false, Vec::new()),
        };
        // An edit the editor left as it was applied nothing.
        let unchanged = matches!(
            &wrote.answer,
            Some(McpWritten::Edited(Asked {
                done: McpEdited::Unchanged,
                ..
            }))
        );
        v1::UpdateMcpServerResponse {
            mcp_server: Some(v1::McpServer {
                name,
                ..v1::McpServer::default()
            }),
            path,
            applied: Some(wrote.applied && !unchanged),
            created,
            needs,
            etag: wrote.etag,
            notices: wrote.notices,
            ..v1::UpdateMcpServerResponse::default()
        }
    }
}

/// The id of a removal preview's Confirm need (step 10b U3).
pub const CONFIRM_NEED: &str = "confirm";

/// The parent a resource name is under: its first two segments
/// (`projects/{harp}`, `users/me`).
fn parent_of(name: &str) -> &str {
    match name.match_indices('/').nth(1) {
        Some((offset, _)) => &name[..offset],
        None => name,
    }
}

/// A removal as its response says it (step 10b U3): whether it applied,
/// the preview's detail and the command that applies it, and its Confirm
/// need (the preview's own target, `Would remove <target>`, as its arg
/// `target`, so a client renders the preview's words and asks before it
/// applies). A retry of a removal already applied (no answer) is applied.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Removed {
    pub applied: bool,
    pub detail: Vec<String>,
    pub apply_command: String,
    pub needs: Vec<v1::Need>,
}

/// [`Removed`] of a use case's removal.
pub fn removed_of<T>(answer: Option<&Removal<T>>, applied: bool) -> Removed {
    match answer {
        Some(Removal::Preview(preview)) => Removed {
            applied: false,
            detail: preview.detail.clone(),
            apply_command: preview.apply.clone(),
            needs: vec![v1::Need {
                optional: false,
                id: CONFIRM_NEED.to_string(),
                kind: Some(v1::need::Kind::Confirm(v1::Confirm {
                    text: format!("Remove {}?", preview.target),
                    code: String::new(),
                    args: vec![v1::NeedArg {
                        name: "target".to_string(),
                        value: preview.target.clone(),
                    }],
                })),
            }],
        },
        Some(Removal::Done(_)) => Removed {
            applied: true,
            ..Removed::default()
        },
        None => Removed {
            applied,
            ..Removed::default()
        },
    }
}

/// `bundle import <path> [--force] [--yes]`: `ImportBundle`. A new bundle
/// is one new directory; a forced one over a bundle there is the sequence
/// (T28, step 10b U9): the old tree into the trash, the new one renamed in,
/// then the trash removed. Its source is the caller's path, read from her
/// working directory when it is relative (never the daemon's).
/// `path` as the caller means it: below her working directory `cwd` when
/// relative.
fn below_cwd(cwd: &str, path: &str) -> String {
    let given = std::path::Path::new(path);
    if path.is_empty() || given.is_absolute() || cwd.is_empty() {
        return path.to_string();
    }
    std::path::Path::new(cwd).join(given).display().to_string()
}

/// `path` as she typed it: her working directory `cwd` taken off again.
fn as_typed(cwd: &str, path: &str) -> String {
    if cwd.is_empty() {
        return path.to_string();
    }
    let cwd = cwd.trim_end_matches(['/', '\\']);
    path.strip_prefix(cwd)
        .and_then(|rest| rest.strip_prefix(['/', '\\']))
        .map_or_else(|| path.to_string(), str::to_string)
}

/// A local `bundle move <bundle> --to <dir>` (step 10b U9, QUESTIONS
/// 1(a)): `MoveBundle`, the project's own bundle moved whole into the
/// project at her `dir` (below her working directory when relative) as ONE
/// Move out of this tree, its stream naming the other; that tree's next
/// read records the arrival. Never over a bundle there (`--force` too). A
/// move to a remote is not served here (the CLI publishes it, U4).
pub mod move_bundle {
    use super::*;
    use library_application::BundleMoved;

    pub fn plan(
        library: &Library,
        flags: &Flags,
        request: &v1::MoveBundleRequest,
        _apply: bool,
    ) -> Outcome<BundleMoved> {
        let bundle = names::bundle_under(parent_of(&request.name), &request.name)
            .unwrap_or_else(|| request.name.clone());
        library.bundle_move_into(
            flags,
            &bundle,
            &below_cwd(request.caller_cwd(), &request.destination),
        )
    }

    pub fn respond(
        request: &v1::MoveBundleRequest,
        wrote: Wrote<BundleMoved>,
    ) -> v1::MoveBundleResponse {
        let (source, dest) = match &wrote.answer {
            Some(done) => (done.source.clone(), done.dest.clone()),
            // A retry of a move done: where it landed, by the layout.
            None => {
                let bundle = names::bundle_under(parent_of(&request.name), &request.name)
                    .unwrap_or_else(|| request.name.clone());
                let dir = below_cwd(request.caller_cwd(), &request.destination);
                let dir = dir.trim_end_matches(['/', '\\']);
                let at = format!("{dir}/.ctxloom/{}/{bundle}", crate::writes::BUNDLES);
                (String::new(), at)
            }
        };
        v1::MoveBundleResponse {
            path: as_typed(request.caller_cwd(), &dest),
            source,
            applied: Some(wrote.applied),
            etag: wrote.etag,
            notices: wrote.notices,
            ..v1::MoveBundleResponse::default()
        }
    }
}

/// `bundle export <bundle> <dest-dir>` (or `-o <path>`): `ExportBundle`,
/// the bundle's tree put in outside ctxloom's trees, whole or not at all,
/// never over what is there (D2's EXCEPTION: no step, no intent; the
/// daemon makes the use case's recorded tree on an apply). Its paths are
/// the caller's, below her working directory when relative.
pub mod export_bundle {
    use super::*;
    use library_application::BundleExported;

    pub fn plan(
        library: &Library,
        flags: &Flags,
        request: &v1::ExportBundleRequest,
        _apply: bool,
    ) -> Outcome<BundleExported> {
        let bundle = names::bundle_under(parent_of(&request.name), &request.name)
            .unwrap_or_else(|| request.name.clone());
        library.bundle_export(
            flags,
            &bundle,
            &below_cwd(request.caller_cwd(), &request.destination),
            &below_cwd(request.caller_cwd(), &request.output),
        )
    }

    pub fn respond(
        request: &v1::ExportBundleRequest,
        wrote: Wrote<BundleExported>,
    ) -> v1::ExportBundleResponse {
        let (source, destination) = match &wrote.answer {
            Some(done) => (
                done.source.clone(),
                as_typed(request.caller_cwd(), &done.dest),
            ),
            // A retry of an export made before: what it answered then.
            None => (
                wrote.source.clone(),
                as_typed(request.caller_cwd(), &wrote.at),
            ),
        };
        v1::ExportBundleResponse {
            bundle: request.name.clone(),
            source,
            destination,
            applied: Some(wrote.applied),
            notices: wrote.notices,
            ..v1::ExportBundleResponse::default()
        }
    }
}

pub mod import_bundle {
    use super::*;
    use library_application::BundleImported;

    /// The source as the caller means it: below her working directory
    /// when relative.
    fn source(request: &v1::ImportBundleRequest) -> String {
        let given = std::path::Path::new(&request.source);
        if given.is_absolute() || request.caller_cwd().is_empty() {
            return request.source.clone();
        }
        std::path::Path::new(request.caller_cwd())
            .join(given)
            .display()
            .to_string()
    }

    /// The last segment of a path, either separator.
    fn last(path: &str) -> &str {
        path.trim_end_matches(['/', '\\'])
            .rsplit(['/', '\\'])
            .next()
            .unwrap_or_default()
    }

    pub fn plan(
        library: &Library,
        flags: &Flags,
        request: &v1::ImportBundleRequest,
        _apply: bool,
    ) -> Outcome<BundleImported> {
        library.bundle_import(flags, &source(request), request.force)
    }

    pub fn respond(
        _request: &v1::ImportBundleRequest,
        wrote: Wrote<BundleImported>,
    ) -> v1::ImportBundleResponse {
        let count = |n: u64| u32::try_from(n).unwrap_or(u32::MAX);
        let (bundle, path) = match &wrote.answer {
            Some(done) => (
                Some(v1::Bundle {
                    name: names::bundle_name(&wrote.parent, last(&done.dest)),
                    version: done.version.clone(),
                    fragment_count: count(done.fragments),
                    command_count: count(done.commands),
                    mcp_server_count: count(done.mcp),
                    ..v1::Bundle::default()
                }),
                done.dest.clone(),
            ),
            None => (just_named(&wrote.parent, last(&wrote.at)), wrote.at.clone()),
        };
        v1::ImportBundleResponse {
            bundle,
            path,
            applied: Some(wrote.applied),
            etag: wrote.etag,
            notices: wrote.notices,
            ..v1::ImportBundleResponse::default()
        }
    }
}

/// `bundle remove <name> [--yes]`: `DeleteBundle`, the bundle's directory
/// taken whole (one trash).
pub mod delete_bundle {
    use super::*;
    use library_application::BundleRemoved;

    fn asked(request: &v1::DeleteBundleRequest) -> String {
        names::bundle_under(parent_of(&request.name), &request.name)
            .unwrap_or_else(|| request.name.clone())
    }

    pub fn plan(
        library: &Library,
        flags: &Flags,
        request: &v1::DeleteBundleRequest,
        apply: bool,
    ) -> Outcome<Removal<BundleRemoved>> {
        library.bundle_remove(flags, &asked(request), apply)
    }

    pub fn respond(
        request: &v1::DeleteBundleRequest,
        wrote: Wrote<Removal<BundleRemoved>>,
    ) -> v1::DeleteBundleResponse {
        let removed = removed_of(wrote.answer.as_ref(), wrote.applied);
        let path = match &wrote.answer {
            Some(Removal::Done(done)) => done.path.clone(),
            _ if removed.applied => wrote.at.clone(),
            _ => String::new(),
        };
        v1::DeleteBundleResponse {
            bundle: just_named(&wrote.parent, &asked(request)),
            path,
            applied: Some(removed.applied),
            detail: removed.detail,
            apply_command: removed.apply_command,
            needs: removed.needs,
            etag: wrote.etag,
            notices: wrote.notices,
            dependents: match &wrote.answer {
                Some(Removal::Preview(preview)) => preview
                    .dependents
                    .iter()
                    .map(|dependent| dependent_name(&wrote.parent, dependent))
                    .collect(),
                _ => Vec::new(),
            },
        }
    }

    /// A dependent's resource name: a profile as a profile's `parents`
    /// names it, an agent by its name.
    fn dependent_name(parent: &str, dependent: &library_application::Dependent) -> String {
        match dependent.kind {
            library_application::DependentKind::Profile => {
                names::profile_ref(parent, &dependent.name)
            }
            library_application::DependentKind::Agent => {
                crate::resources::agent_name(parent, &dependent.name)
            }
        }
    }
}

/// An item removal's use case and its answer (`fragment|command remove`).
fn item_removal(
    library: &Library,
    flags: &Flags,
    kind: ItemKind,
    name: &str,
    apply: bool,
) -> Outcome<Removal<ItemRemoved>> {
    let reference =
        names::reference_under(parent_of(name), name).unwrap_or_else(|| name.to_string());
    library.item_remove(flags, kind, &reference, apply)
}

/// The resource name and the path an item removal answers.
fn item_removed(name: &str, wrote: &Wrote<Removal<ItemRemoved>>, dir: &str) -> (String, String) {
    match &wrote.answer {
        Some(Removal::Done(done)) => (
            names::item_name(&wrote.parent, &done.bundle, dir, &done.name),
            done.path.clone(),
        ),
        _ if wrote.applied => (name.to_string(), wrote.at.clone()),
        _ => (name.to_string(), String::new()),
    }
}

/// `fragment remove <ref> [--yes]`: `DeleteFragment`.
pub mod delete_fragment {
    use super::*;

    pub fn plan(
        library: &Library,
        flags: &Flags,
        request: &v1::DeleteFragmentRequest,
        apply: bool,
    ) -> Outcome<Removal<ItemRemoved>> {
        item_removal(library, flags, ItemKind::Fragment, &request.name, apply)
    }

    pub fn respond(
        request: &v1::DeleteFragmentRequest,
        wrote: Wrote<Removal<ItemRemoved>>,
    ) -> v1::DeleteFragmentResponse {
        let (name, path) = item_removed(&request.name, &wrote, "fragments");
        let removed = removed_of(wrote.answer.as_ref(), wrote.applied);
        v1::DeleteFragmentResponse {
            fragment: Some(v1::Fragment {
                name,
                ..v1::Fragment::default()
            }),
            path,
            applied: Some(removed.applied),
            detail: removed.detail,
            apply_command: removed.apply_command,
            needs: removed.needs,
            etag: wrote.etag,
            notices: wrote.notices,
        }
    }
}

/// `command remove <ref> [--yes]`: `DeleteCommand`.
pub mod delete_command {
    use super::*;

    pub fn plan(
        library: &Library,
        flags: &Flags,
        request: &v1::DeleteCommandRequest,
        apply: bool,
    ) -> Outcome<Removal<ItemRemoved>> {
        item_removal(library, flags, ItemKind::Command, &request.name, apply)
    }

    pub fn respond(
        request: &v1::DeleteCommandRequest,
        wrote: Wrote<Removal<ItemRemoved>>,
    ) -> v1::DeleteCommandResponse {
        let (name, path) = item_removed(&request.name, &wrote, "commands");
        let removed = removed_of(wrote.answer.as_ref(), wrote.applied);
        v1::DeleteCommandResponse {
            command: Some(v1::Command {
                name,
                ..v1::Command::default()
            }),
            path,
            applied: Some(removed.applied),
            detail: removed.detail,
            apply_command: removed.apply_command,
            needs: removed.needs,
            etag: wrote.etag,
            notices: wrote.notices,
        }
    }
}

/// `skill remove <ref> [--yes]`: `DeleteSkill`, the skill's one directory.
pub mod delete_skill {
    use super::*;
    use library_application::SkillRemoved;

    pub fn plan(
        library: &Library,
        flags: &Flags,
        request: &v1::DeleteSkillRequest,
        apply: bool,
    ) -> Outcome<Removal<SkillRemoved>> {
        let reference = names::reference_under(parent_of(&request.name), &request.name)
            .unwrap_or_else(|| request.name.clone());
        library.skill_remove(flags, &reference, apply)
    }

    pub fn respond(
        request: &v1::DeleteSkillRequest,
        wrote: Wrote<Removal<SkillRemoved>>,
    ) -> v1::DeleteSkillResponse {
        let removed = removed_of(wrote.answer.as_ref(), wrote.applied);
        let (name, dir) = match &wrote.answer {
            Some(Removal::Done(done)) => (
                format!(
                    "{}/skills/{}",
                    names::bundle_name(&wrote.parent, &done.bundle),
                    done.name
                ),
                done.dir.clone(),
            ),
            _ if removed.applied => (request.name.clone(), wrote.at.clone()),
            _ => (request.name.clone(), String::new()),
        };
        v1::DeleteSkillResponse {
            skill: Some(v1::Skill {
                name,
                path: dir.clone(),
                ..v1::Skill::default()
            }),
            path: dir,
            applied: Some(removed.applied),
            detail: removed.detail,
            apply_command: removed.apply_command,
            needs: removed.needs,
            etag: wrote.etag,
            notices: wrote.notices,
        }
    }
}

/// `profile remove <name> [--yes]`: `DeleteProfile`.
pub mod delete_profile {
    use super::*;
    use library_application::ProfileRemoved;

    pub fn plan(
        library: &Library,
        flags: &Flags,
        request: &v1::DeleteProfileRequest,
        apply: bool,
    ) -> Outcome<Removal<ProfileRemoved>> {
        let asked = names::profile_under(parent_of(&request.name), &request.name)
            .unwrap_or_else(|| request.name.clone());
        library.profile_remove(flags, &asked, apply)
    }

    pub fn respond(
        request: &v1::DeleteProfileRequest,
        wrote: Wrote<Removal<ProfileRemoved>>,
    ) -> v1::DeleteProfileResponse {
        let removed = removed_of(wrote.answer.as_ref(), wrote.applied);
        let path = if removed.applied {
            wrote.at.clone()
        } else {
            String::new()
        };
        let asked = names::profile_under(parent_of(&request.name), &request.name)
            .unwrap_or_else(|| request.name.clone());
        v1::DeleteProfileResponse {
            profile: Some(v1::Profile {
                name: names::profile_ref(&wrote.parent, &asked),
                ..<v1::Profile as Default>::default()
            }),
            path,
            applied: Some(removed.applied),
            detail: removed.detail,
            apply_command: removed.apply_command,
            needs: removed.needs,
            etag: wrote.etag,
            notices: wrote.notices,
        }
    }
}

/// `agent remove <name> [--yes]`: `DeleteAgent`, its config layer
/// written.
pub mod delete_agent {
    use super::*;
    use library_application::AgentRemoved;

    pub fn plan(
        library: &Library,
        flags: &Flags,
        request: &v1::DeleteAgentRequest,
        apply: bool,
    ) -> Outcome<Removal<AgentRemoved>> {
        let asked = names::named_under(parent_of(&request.name), "agents", &request.name)
            .unwrap_or_else(|| request.name.clone());
        library.agent_remove(flags, &asked, apply)
    }

    pub fn respond(
        request: &v1::DeleteAgentRequest,
        wrote: Wrote<Removal<AgentRemoved>>,
    ) -> v1::DeleteAgentResponse {
        let removed = removed_of(wrote.answer.as_ref(), wrote.applied);
        let path = if removed.applied {
            wrote.at.clone()
        } else {
            String::new()
        };
        v1::DeleteAgentResponse {
            agent: Some(v1::Agent {
                name: request.name.clone(),
                ..<v1::Agent as Default>::default()
            }),
            path,
            applied: Some(removed.applied),
            detail: removed.detail,
            apply_command: removed.apply_command,
            needs: removed.needs,
            etag: wrote.etag,
            notices: wrote.notices,
        }
    }
}

/// `companion remove <name> [--yes]`: `DeleteCompanion`, its config layer
/// written. A companion nobody registered is removed as nothing (applied
/// false).
pub mod delete_companion {
    use super::*;
    use library_application::CompanionRemoved;

    pub fn plan(
        library: &Library,
        flags: &Flags,
        request: &v1::DeleteCompanionRequest,
        apply: bool,
    ) -> Outcome<Removal<CompanionRemoved>> {
        let asked = names::named_under(parent_of(&request.name), "companions", &request.name)
            .unwrap_or_else(|| request.name.clone());
        library.companion_remove(flags, &asked, apply)
    }

    pub fn respond(
        request: &v1::DeleteCompanionRequest,
        wrote: Wrote<Removal<CompanionRemoved>>,
    ) -> v1::DeleteCompanionResponse {
        let mut removed = removed_of(wrote.answer.as_ref(), wrote.applied);
        if let Some(Removal::Done(done)) = &wrote.answer {
            removed.applied = done.removed;
        }
        let path = if removed.applied {
            wrote.at.clone()
        } else {
            String::new()
        };
        v1::DeleteCompanionResponse {
            companion: Some(v1::Companion {
                name: request.name.clone(),
                ..v1::Companion::default()
            }),
            path,
            applied: Some(removed.applied),
            detail: removed.detail,
            apply_command: removed.apply_command,
            needs: removed.needs,
            etag: wrote.etag,
            notices: wrote.notices,
        }
    }
}

#[cfg(test)]
#[path = "served_writes.test.rs"]
mod tests;
