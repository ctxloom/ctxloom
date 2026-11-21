//! `profile list` and `profile show`, and the profiles of the project's
//! bundles as a source for resolution.

use std::collections::{BTreeMap, BTreeSet};

use angzarr_diagnostic::Diagnostic;
use library::findings::Finding;
use library::profile::{self, LoadError, Profile, ProfileSource, ResolvedProfile};
use library::ProfileError;

use crate::error::AppError;
use crate::project::{Library, Outcome, Project};
use crate::views::{ProfileList, ProfileRow, ProfileShow};
use crate::write::views::{Dependent, DependentKind};
use crate::Flags;

/// The bundle every project's own profiles are items of.
const PROJECT_BUNDLE: &str = "ctxloom+local:project";

impl Library {
    /// `profile list`.
    pub fn profile_list(&self, flags: &Flags) -> Outcome<ProfileList> {
        self.run(flags, |p| Ok(p.profile_list()))
    }

    /// `profile show <name>`.
    pub fn profile_show(&self, flags: &Flags, name: &str) -> Outcome<ProfileShow> {
        self.run(flags, |p| p.profile_show(name))
    }
}

/// The profiles the project's local bundles ship, by their canonical key.
pub(crate) struct Profiles {
    pub(crate) by_key: BTreeMap<String, (Profile, String)>,
    /// The catalog name of the bundle that ships each, by its key.
    entries: BTreeMap<String, String>,
    /// The repository a remote profile was shipped in, by its key: it may
    /// name only that repository's content.
    sources: BTreeMap<String, String>,
}

impl ProfileSource for Profiles {
    fn load(&self, key: &str) -> Result<Profile, LoadError> {
        match self.by_key.get(key) {
            Some((profile, _)) => {
                self.own_repository(key, profile)?;
                Ok(profile.clone())
            }
            None if key.starts_with("ctxloom+local:") => Err(LoadError::LocalNotFound {
                name: key.to_string(),
            }),
            None => Err(LoadError::NotFound {
                name: key.to_string(),
            }),
        }
    }

    fn keys(&self) -> Vec<String> {
        self.by_key.keys().cloned().collect()
    }
}

impl Profiles {
    /// A remote profile that names content outside its own repository is
    /// refused: registering a remote admits that repository's content, never
    /// whatever its profiles name.
    fn own_repository(&self, key: &str, profile: &Profile) -> Result<(), LoadError> {
        let Some(url) = self.sources.get(key) else {
            return Ok(());
        };
        profile
            .check_own_repo(url)
            .map_err(|cause| LoadError::Unreadable {
                name: key.to_string(),
                detail: match cause {
                    ProfileError::CrossRepo { reference, .. } => format!(
                    "a remote profile may refer only to bundles in its own repository: {reference}"
                ),
                    other => other.code().to_string(),
                },
            })
    }
}

/// The name a user types to reach a profile: the bare name for the project
/// bundle's, else its full key.
fn ask_name(key: &str) -> String {
    match key.split_once("#profiles/") {
        Some((bundle, name)) if bundle == PROJECT_BUNDLE => name.to_string(),
        _ => key.to_string(),
    }
}

/// The bare profile name of a key.
fn display_name(key: &str) -> String {
    key.split_once("#profiles/")
        .map_or_else(|| key.to_string(), |(_, name)| name.to_string())
}

impl Project {
    pub(crate) fn profiles(&self) -> Profiles {
        let mut by_key = BTreeMap::new();
        let mut sources = BTreeMap::new();
        let mut entries = BTreeMap::new();
        for entry in self.catalog.entries() {
            let dir = crate::shown::parent(&entry.manifest_path);
            let origin = self.catalog.origin(&entry.name);
            let bundle_key = origin.map_or_else(
                || format!("ctxloom+local:{}", entry.name),
                |origin| origin.reference.clone(),
            );
            for (name, profile) in &entry.bundle.profiles {
                let key = format!("{bundle_key}#profiles/{name}");
                let mut profile = profile.clone();
                profile.name = key.clone();
                if let (Some(origin), Some(remotes)) = (origin, &self.remotes) {
                    // A remote profile's short references are its own
                    // repository's, at the commit it was read at.
                    let within = |r: &String| remotes.within(r, &origin.url, &origin.sha);
                    profile.bundles = profile.bundles.iter().map(within).collect();
                    profile.parents = profile.parents.iter().map(within).collect();
                    profile.commands = profile.commands.iter().map(within).collect();
                    profile.skills = profile.skills.iter().map(within).collect();
                    profile.bundle_items = profile.bundle_items.iter().map(within).collect();
                    for fragment in &mut profile.fragments {
                        fragment.name = within(&fragment.name);
                    }
                    sources.insert(key.clone(), origin.url.clone());
                }
                entries.insert(key.clone(), entry.name.clone());
                by_key.insert(
                    key,
                    (
                        profile,
                        crate::shown::below(dir, &format!("profiles/{name}.yaml")),
                    ),
                );
            }
        }
        Profiles {
            by_key,
            entries,
            sources,
        }
    }

    /// The canonical keys of the profiles the default agent composes.
    fn default_keys(&self, profiles: &Profiles) -> Vec<String> {
        self.config
            .default_agent_profiles()
            .iter()
            .filter_map(|name| profile::canonical_key(name).ok())
            .filter(|key| profiles.by_key.contains_key(key))
            .collect()
    }

    pub(crate) fn profile_list(&self) -> ProfileList {
        let profiles = self.profiles();
        let defaults = self.default_keys(&profiles);
        let mut rows: Vec<ProfileRow> = profiles
            .by_key
            .iter()
            .map(|(key, (p, path))| ProfileRow {
                name: ask_name(key),
                display_name: display_name(key),
                description: p.description.clone(),
                parents: p.parents.clone(),
                tags: p.tags.clone(),
                bundles: p.bundles.clone(),
                default: defaults.contains(key),
                path: path.clone(),
                is_remote: profiles.sources.contains_key(key),
                bundle: bundle_of(key),
                entry: profiles.entries.get(key).cloned().unwrap_or_default(),
            })
            .collect();
        rows.sort_by_key(|r| r.name.to_lowercase());
        ProfileList {
            rows,
            hint: if self.no_project() {
                crate::NO_PROJECT_HINT.to_string()
            } else {
                crate::PROFILE_HINT.to_string()
            },
        }
    }

    fn profile_show(&self, ask: &str) -> Result<ProfileShow, AppError> {
        let profiles = self.profiles();
        let key = profile::canonical_key(&self.spelled_out(ask)).map_err(AppError::cause)?;
        let Some((found, path)) = profiles.by_key.get(&key) else {
            return Err(AppError::cause(ProfileError::NotFound {
                name: ask.to_string(),
            }));
        };
        if let Some(url) = profiles.sources.get(&key) {
            found.check_own_repo(url).map_err(AppError::cause)?;
        }
        let defaults = self.default_keys(&profiles);
        Ok(ProfileShow {
            name: ask_name(&key),
            description: found.description.clone(),
            parents: found.parents.clone(),
            tags: found.tags.clone(),
            bundles: found.bundles.clone(),
            variables: found.variables.clone(),
            exclude_fragments: found.exclude_fragments.clone(),
            exclude_mcp: found.exclude_mcp.clone(),
            select_tags: found.select_tags.clone(),
            fragments: found.fragments.clone(),
            hooks: found.hooks.read(),
            deny_tools: found.deny_tools.clone(),
            path: path.clone(),
            bundle: bundle_of(&key),
            default: defaults.contains(&key),
            entry: profiles.entries.get(&key).cloned().unwrap_or_default(),
        })
    }

    /// The named profiles, each resolved with its parents, in order. A name
    /// that does not resolve is an error naming it.
    pub(crate) fn resolve_profiles(
        &self,
        names: &[String],
        findings: &mut Vec<Finding>,
    ) -> Result<Vec<ResolvedProfile>, AppError> {
        let profiles = self.profiles();
        let mut out = Vec::with_capacity(names.len());
        for name in names {
            let mut resolved = profile::resolve(&profiles, &self.spelled_out(name), findings)
                .map_err(|cause| AppError::Compose {
                    profile: name.clone(),
                    cause: Box::new(cause),
                })?;
            resolved.name.clone_from(name);
            out.push(resolved);
        }
        Ok(out)
    }
}

impl Project {
    /// The profiles and agents that include the local bundle `name` (owner
    /// ruling 2026-11-19): a profile naming it (a whole-bundle or an item
    /// reference), or one of whose parents does, or lives in it; an agent
    /// one of whose profiles does, or lives in it. A profile that lives in
    /// the bundle goes with it and is not named. Profiles first, then
    /// agents, each in name order.
    pub(crate) fn bundle_dependents(&self, name: &str) -> Vec<Dependent> {
        let profiles = self.profiles();
        let target = format!("ctxloom+local:{name}");
        let mut walk = Walk {
            profiles: &profiles,
            target: target.clone(),
            spell: &|reference| self.spelled_out(reference),
            memo: BTreeMap::new(),
            visiting: BTreeSet::new(),
        };
        let mut named: Vec<String> = profiles
            .by_key
            .keys()
            .filter(|key| bundle_of(key) != target)
            .filter(|key| walk.includes(key))
            .map(|key| shown_name(key))
            .collect();
        named.sort();
        let mut out: Vec<Dependent> = named
            .into_iter()
            .map(|name| Dependent {
                kind: DependentKind::Profile,
                name,
            })
            .collect();
        for agent in self.config.agents() {
            let on_it = agent.profiles.iter().any(|profile| {
                profile::canonical_key(&self.spelled_out(profile))
                    .is_ok_and(|key| walk.includes(&key))
            });
            if on_it {
                out.push(Dependent {
                    kind: DependentKind::Agent,
                    name: agent.name,
                });
            }
        }
        out
    }
}

/// Which profiles reach one bundle, each judged once.
struct Walk<'a> {
    profiles: &'a Profiles,
    /// The bundle's canonical reference.
    target: String,
    spell: &'a dyn Fn(&str) -> String,
    memo: BTreeMap<String, bool>,
    /// The chain being judged, so a cycle (which resolution refuses) ends.
    visiting: BTreeSet<String>,
}

impl Walk<'_> {
    /// Whether the profile of `key` lives in the bundle, names it, or has
    /// a parent that does.
    fn includes(&mut self, key: &str) -> bool {
        if bundle_of(key) == self.target {
            return true;
        }
        if let Some(known) = self.memo.get(key) {
            return *known;
        }
        let Some((found, _)) = self.profiles.by_key.get(key) else {
            return false;
        };
        if !self.visiting.insert(key.to_string()) {
            return false;
        }
        let found = found.clone();
        // `bundles` and `bundle_items` are bundle references; a command,
        // skill or fragment names its bundle only as `<bundle>#...`.
        let scoped = found
            .commands
            .iter()
            .chain(&found.skills)
            .chain(found.fragments.iter().map(|f| &f.name))
            .filter(|reference| reference.contains('#'));
        let named = found
            .bundles
            .iter()
            .chain(&found.bundle_items)
            .chain(scoped)
            .any(|reference| self.names_target(reference));
        let reached = named
            || found.parents.iter().any(|parent| {
                profile::canonical_key(&(self.spell)(parent)).is_ok_and(|key| self.includes(&key))
            });
        self.visiting.remove(key);
        self.memo.insert(key.to_string(), reached);
        reached
    }

    /// Whether `reference` (`<bundle>` or `<bundle>#<item>`) is the bundle's.
    fn names_target(&self, reference: &str) -> bool {
        let base = reference.split('#').next().unwrap_or(reference);
        library::refs::canonical_bundle_ref(&(self.spell)(base)).is_ok_and(|c| c == self.target)
    }
}

/// A profile's key as a profile's `parents` names it: a project profile
/// bare, a local bundle's `<bundle>#profiles/<name>`.
fn shown_name(key: &str) -> String {
    let asked = ask_name(key);
    asked
        .strip_prefix("ctxloom+local:")
        .map_or(asked.clone(), ToString::to_string)
}

/// The bundle of a profile's key (`<bundle>#profiles/<name>`).
fn bundle_of(key: &str) -> String {
    key.split_once("#profiles/")
        .map(|(b, _)| b.to_string())
        .unwrap_or_default()
}
