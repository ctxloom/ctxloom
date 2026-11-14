//! `fragment` and `command` (list, show), `fragment premises`, and `skill`
//! (list, show).

use library::catalog::ContentInfo;
use library::distill::DistilledState;
use library::frontmatter::split_front_matter;
use library::model::Skill;
use library::refs::{self, ItemKind};
use library::sha256;
use library::{premise, CatalogError};

use crate::error::{AppError, Notice};
use crate::project::{Library, Outcome, Project};
use crate::views::{
    ItemList, ItemRow, ItemShow, PremiseRow, Premises, SkillFileRow, SkillList, SkillRow, SkillShow,
};
use crate::Flags;

/// A kind of item the listing and show commands serve.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Kind {
    Fragment,
    Command,
}

impl Kind {
    pub(crate) fn word(self) -> &'static str {
        match self {
            Kind::Fragment => "fragment",
            Kind::Command => "command",
        }
    }

    pub(crate) fn dir(self) -> &'static str {
        match self {
            Kind::Fragment => "fragments",
            Kind::Command => "commands",
        }
    }

    /// The directory of the kind's files in a bundle's tree (a command's are
    /// in `prompts`).
    pub(crate) fn tree_dir(self) -> &'static str {
        match self {
            Kind::Fragment => library::write::item::FRAGMENTS,
            Kind::Command => library::write::item::PROMPTS,
        }
    }

    fn item(self) -> ItemKind {
        match self {
            Kind::Fragment => ItemKind::Fragment,
            Kind::Command => ItemKind::Prompt,
        }
    }
}

impl Library {
    /// `fragment list [--bundle B]` and `command list [--bundle B]`.
    pub fn item_list(&self, flags: &Flags, kind: Kind, bundle: &str) -> Outcome<ItemList> {
        self.run(flags, |p| p.item_list(kind, bundle))
    }

    /// `fragment show <ref> [--distilled]` and `command show`.
    pub fn item_show(
        &self,
        flags: &Flags,
        kind: Kind,
        reference: &str,
        distilled: bool,
    ) -> Outcome<ItemShow> {
        self.run(flags, |p| p.item_show(kind, reference, distilled))
    }

    /// The prompt `name` of ctxloom's operational bundle
    /// ([`library::builtin::BUNDLE`]) as the project's catalog holds it
    /// (owner ruling 128): what init and distill open with.
    ///
    /// A project bundle of that name overrides the built-in one whole: its
    /// prompt is the one, and a prompt it lacks is refused, never taken from
    /// the built-in bundle. With no project there is nothing to override it.
    pub fn operational_prompt(&self, flags: &Flags, name: &str) -> Outcome<String> {
        let builtins = self.builtins;
        self.run(flags, |p| p.operational_prompt(name, builtins))
    }

    /// Every fragment and command whose distilled form is stale (T30, S51):
    /// doctor's row. Each by its reference, `<bundle>#fragments/<name>` or
    /// `<bundle>#commands/<name>`, in catalog order; one marked `no_distill`
    /// is never served distilled and is not said.
    pub fn stale_distilled(&self, flags: &Flags) -> Outcome<Vec<String>> {
        self.run(flags, |p| Ok(p.stale_distilled()))
    }

    /// `fragment premises`.
    pub fn premises(&self, flags: &Flags) -> Outcome<Premises> {
        self.run(flags, |p| Ok(p.premises()))
    }

    /// `skill list [--bundle B]`.
    pub fn skill_list(&self, flags: &Flags, bundle: &str) -> Outcome<SkillList> {
        self.run(flags, |p| p.skill_list(bundle))
    }

    /// `skill show <ref>`.
    pub fn skill_show(&self, flags: &Flags, reference: &str) -> Outcome<SkillShow> {
        self.run(flags, |p| p.skill_show(reference))
    }
}

impl Project {
    pub(crate) fn operational_prompt(
        &self,
        name: &str,
        builtins: bool,
    ) -> Result<String, AppError> {
        let bundle = library::builtin::BUNDLE;
        let shipped;
        let entry = if self.no_project() && builtins {
            shipped = library::builtin::entries(&|_| false);
            shipped.iter().find(|e| e.name == bundle).ok_or_else(|| {
                AppError::cause(CatalogError::BundleNotFound {
                    ask: bundle.to_string(),
                })
            })?
        } else {
            self.bundle(bundle)?
        };
        match entry.bundle.commands.get(name) {
            Some(command) => Ok(command.body.content.clone()),
            None => Err(AppError::OperationalPromptMissing {
                bundle: bundle.to_string(),
                prompt: name.to_string(),
                dir: self.content.bundle_dir(bundle),
            }),
        }
    }

    fn item_list(&self, kind: Kind, bundle: &str) -> Result<ItemList, AppError> {
        let infos: Vec<ContentInfo> = match kind {
            Kind::Fragment => self.catalog.list_fragments(),
            Kind::Command => self.catalog.list_commands(),
        };
        let all: Vec<ItemRow> = infos
            .into_iter()
            .map(|info| ItemRow {
                reference: format!("{}#{}/{}", info.bundle, kind.dir(), info.name),
                bundle_label: info.bundle.clone(),
                bundle: info.bundle,
                name: info.name,
                tags: info.tags,
                remote: String::new(),
            })
            .collect();
        let total = all.len();
        let rows: Vec<ItemRow> = if bundle.is_empty() {
            all
        } else {
            all.into_iter().filter(|r| r.bundle == bundle).collect()
        };
        if rows.is_empty() && !bundle.is_empty() {
            let known: Vec<&str> = self
                .catalog
                .entries()
                .iter()
                .map(|e| e.name.as_str())
                .collect();
            if !known.is_empty() && !known.contains(&bundle) {
                return Err(AppError::NoBundleNamed {
                    name: bundle.to_string(),
                    known: known.join(", "),
                });
            }
        }
        Ok(ItemList {
            rows,
            kind: kind.word().to_string(),
            bundle_filter: bundle.to_string(),
            total,
            hint: self.listing_hint(),
        })
    }

    /// The bundle and item a reference names, for a command serving `kind`.
    pub(crate) fn item_target(
        &self,
        kind: Kind,
        reference: &str,
    ) -> Result<(String, String), AppError> {
        let expected = format!("bundle#{}/name", kind.dir());
        let Some((bundle, selector)) = reference.split_once('#') else {
            return Err(AppError::BadReference {
                expected,
                got: reference.to_string(),
            });
        };
        let (got, item) = refs::parse_selector(selector).map_err(|cause| {
            AppError::cause(CatalogError::ItemSyntax {
                ask: reference.to_string(),
                cause,
            })
        })?;
        if got != kind.item() {
            return Err(AppError::WrongKind {
                ask: reference.to_string(),
                got: selector_dir(got).to_string(),
                want: kind.dir().to_string(),
                expected,
            });
        }
        if bundle.is_empty() {
            return Err(AppError::BadReference {
                expected: format!("a bundle name in {reference:?}"),
                got: reference.to_string(),
            });
        }
        Ok((bundle.to_string(), item))
    }

    fn item_show(
        &mut self,
        kind: Kind,
        reference: &str,
        distilled: bool,
    ) -> Result<ItemShow, AppError> {
        let (bundle, name) = self.item_target(kind, reference)?;
        // A companion's bundle is in the catalog only with the companions'
        // loadouts, which are asked for only when one is named (the premise
        // index names a companion's fragments, for `fragment show`).
        let with_companions;
        let entry = if is_companion(&bundle) {
            with_companions = self.catalog_with_companions();
            with_companions.lookup(&bundle).map_err(AppError::cause)?
        } else {
            self.bundle(&bundle)?
        };
        let body = match kind {
            Kind::Fragment => entry.bundle.fragments.get(&name).map(|f| &f.body),
            Kind::Command => entry.bundle.commands.get(&name).map(|c| &c.body),
        };
        let Some(body) = body else {
            let available: Vec<&str> = match kind {
                Kind::Fragment => entry.bundle.fragments.keys().map(String::as_str).collect(),
                Kind::Command => entry.bundle.commands.keys().map(String::as_str).collect(),
            };
            return Err(AppError::ItemMissing {
                kind: kind.word().to_string(),
                name,
                available: available.join(", "),
            });
        };
        // The distilled form is served only while it matches its source; a
        // stale one asked for is said, and the original served (T30, S51).
        let state = body.distilled_state();
        let use_distilled = distilled && state == DistilledState::Fresh;
        let shown = ItemShow {
            name,
            bundle: entry.name.clone(),
            reference: reference.to_string(),
            content: if use_distilled {
                body.distilled.clone()
            } else {
                body.content.clone()
            },
            distilled: use_distilled,
            state,
        };
        if distilled && state == DistilledState::Stale {
            self.notices.push(Notice::StaleDistilled {
                kind: kind.word().to_string(),
                reference: reference.to_string(),
            });
        }
        Ok(shown)
    }

    fn stale_distilled(&self) -> Vec<String> {
        let stale = |body: &library::model::ItemBody| {
            !body.no_distill && body.distilled_state() == DistilledState::Stale
        };
        let mut out = Vec::new();
        for entry in self.catalog.entries() {
            let bundle = &entry.bundle;
            for (name, fragment) in &bundle.fragments {
                if stale(&fragment.body) {
                    out.push(format!("{}#fragments/{name}", entry.name));
                }
            }
            for (name, command) in &bundle.commands {
                if stale(&command.body) {
                    out.push(format!("{}#commands/{name}", entry.name));
                }
            }
        }
        out
    }

    fn premises(&mut self) -> Premises {
        let entries = premise::index(&self.catalog_with_companions());
        if entries.is_empty() {
            return Premises {
                instruction: premise::NONE.to_string(),
                fragments: Vec::new(),
            };
        }
        Premises {
            instruction: premise::selection_instruction(),
            fragments: entries
                .into_iter()
                .map(|e| PremiseRow {
                    name: e.name,
                    premise: e.premise,
                })
                .collect(),
        }
    }

    /// Every skill of every bundle that loads, by bundle then name.
    fn skills(&mut self) -> Result<Vec<LoadedSkill>, AppError> {
        let mut out = Vec::new();
        let mut withheld = Vec::new();
        for entry in self.catalog.entries() {
            for (name, skill) in &entry.bundle.skills {
                match load_skill(&entry.name, &entry.bundle.tags, name, skill) {
                    Ok(loaded) => out.push(loaded),
                    Err(why) => {
                        withheld.push(Notice::text(format!("skill \"{name}\" withheld: {why}")))
                    }
                }
            }
        }
        self.notices.extend(withheld);
        Ok(out)
    }

    /// Every skill as a listing row, by bundle then name.
    pub(crate) fn skill_rows(&mut self) -> Result<Vec<SkillRow>, AppError> {
        let mut rows: Vec<SkillRow> = self
            .skills()?
            .into_iter()
            .map(|s| SkillRow {
                name: format!("{}/{}", s.bundle, s.item),
                description: s.description,
                tags: s.tags,
                source: s.bundle,
                file_count: s.files.len() as u64,
            })
            .collect();
        rows.sort_by(|a, b| a.source.cmp(&b.source).then_with(|| a.name.cmp(&b.name)));
        Ok(rows)
    }

    fn skill_list(&mut self, bundle: &str) -> Result<SkillList, AppError> {
        let mut rows = self.skill_rows()?;
        let total = rows.len();
        if !bundle.is_empty() {
            rows.retain(|r| r.source == bundle);
        }
        Ok(SkillList {
            rows,
            bundle_filter: bundle.to_string(),
            total,
        })
    }

    fn skill_show(&mut self, reference: &str) -> Result<SkillShow, AppError> {
        let (bundle, item) = match reference.split_once('#') {
            None => (None, reference.to_string()),
            Some((bundle, selector)) => {
                let (kind, item) = refs::parse_selector(selector).map_err(|cause| {
                    AppError::cause(CatalogError::ItemSyntax {
                        ask: reference.to_string(),
                        cause,
                    })
                })?;
                if kind != ItemKind::Skill {
                    return Err(AppError::cause(CatalogError::BadItemRef {
                        ask: reference.to_string(),
                        kind: kind.label().to_string(),
                        wanted: ItemKind::Skill.label().to_string(),
                    }));
                }
                (Some(self.bundle(bundle)?.name.clone()), item)
            }
        };
        let all = self.skills()?;
        let found = all
            .into_iter()
            .find(|s| s.item == item && bundle.as_ref().is_none_or(|b| *b == s.bundle));
        match (found, bundle) {
            (Some(skill), _) => Ok(show_of(skill)),
            (None, Some(bundle)) => Err(AppError::SkillNotFound { name: item, bundle }),
            (None, None) => Err(AppError::SkillNameNotFound { name: item }),
        }
    }
}

fn selector_dir(kind: ItemKind) -> &'static str {
    match kind {
        ItemKind::Prompt => "commands",
        other => other.dir(),
    }
}

/// A skill package, loaded.
struct LoadedSkill {
    bundle: String,
    item: String,
    name: String,
    description: String,
    license: String,
    compatibility: String,
    allowed_tools: Vec<String>,
    body: String,
    tags: Vec<String>,
    files: Vec<(String, Vec<u8>, bool)>,
}

fn load_skill(
    bundle: &str,
    bundle_tags: &[String],
    name: &str,
    skill: &Skill,
) -> Result<LoadedSkill, String> {
    let descriptor = skill
        .files
        .iter()
        .find(|f| f.path == "SKILL.md")
        .ok_or_else(|| "SKILL.md is required".to_string())?;
    let text = String::from_utf8_lossy(&descriptor.bytes).into_owned();
    let parts =
        split_front_matter(&text).ok_or_else(|| "SKILL.md has no frontmatter".to_string())?;
    let meta = library_yaml(parts.meta)?;
    let field = |key: &str| -> String {
        meta.get(key)
            .and_then(angzarr_yaml::Value::as_str)
            .unwrap_or_default()
            .to_string()
    };
    let allowed_tools = meta
        .get("allowed-tools")
        .and_then(angzarr_yaml::Value::as_seq)
        .map(|items| {
            items
                .iter()
                .filter_map(|i| i.as_str().map(ToString::to_string))
                .collect()
        })
        .unwrap_or_default();
    let mut tags: Vec<String> = Vec::new();
    for tag in bundle_tags.iter().chain(&skill.tags) {
        if !tags.contains(tag) {
            tags.push(tag.clone());
        }
    }
    Ok(LoadedSkill {
        bundle: bundle.to_string(),
        item: name.to_string(),
        name: name.to_string(),
        description: field("description"),
        license: field("license"),
        compatibility: field("compatibility"),
        allowed_tools,
        body: parts.body.to_string(),
        tags,
        files: skill
            .files
            .iter()
            // The sidecar's declaration, on every OS (owner ruling
            // 2026-11-12): no mode is read.
            .map(|f| (f.path.clone(), f.bytes.clone(), f.executable))
            .collect(),
    })
}

fn library_yaml(text: &str) -> Result<angzarr_yaml::Value, String> {
    if text.trim().is_empty() {
        return Ok(angzarr_yaml::Value::Null);
    }
    angzarr_yaml::parse(text).map_err(|e| format!("invalid SKILL.md frontmatter: {e}"))
}

/// Whether `bundle` is a companion's reference (`ctxloom+companion:<bin>`).
fn is_companion(bundle: &str) -> bool {
    refs::Parts::parse(bundle).is_ok_and(|parts| parts.class == refs::SourceClass::Companion)
}

fn show_of(skill: LoadedSkill) -> SkillShow {
    SkillShow {
        name: skill.name,
        bundle: skill.bundle,
        description: skill.description,
        license: skill.license,
        compatibility: skill.compatibility,
        allowed_tools: skill.allowed_tools,
        body: skill.body,
        files: skill
            .files
            .into_iter()
            .map(|(path, bytes, executable)| SkillFileRow {
                path,
                sha256: format!("sha256:{}", sha256::hex(&bytes)),
                mode: if executable { "0755" } else { "0644" }.to_string(),
            })
            .collect(),
    }
}
