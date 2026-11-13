//! The registered companions' loadouts, joined to the catalog an assembly
//! reads (Go's `companions.Prober.ProbeCompanionLoadouts` and the companion
//! reader): each enabled registered name's program found and asked through
//! the `CompanionProbe` port at the project's probe site (D1: the caller's
//! environment, the project's root), its `run:` bundle seeded under the
//! companion's reference. A disabled companion is not asked (OD-9).
//!
//! Never fatal: an absent, failing or unparseable companion costs its own
//! contribution and is said as a warning (an absent one names the program
//! that would bring it, j000800). `--no-companions` and
//! `CTXLOOM_NO_COMPANIONS` ask none.

use ctxloom_ports::CompanionLoadout;
use library::catalog::Catalog;
use library::companion::State;
use library::loadout::{parse_loadout, InitLoadout, Loadout};
use library::model::Bundle;

use crate::error::Notice;
use crate::project::{Library, Outcome, Project};
use crate::Flags;

/// Whether the environment's switch `name` is on: `1`, `t`, `true`, `y`,
/// `yes` or `on`, in any case, around any space (Go's `envswitch.On`). Any
/// other value is off, so a typo never turns a mode on.
pub(crate) fn switch_on(environ: &[(String, String)], name: &str) -> bool {
    environ
        .iter()
        .find(|(variable, _)| variable == name)
        .is_some_and(|(_, value)| {
            matches!(
                value.trim().to_ascii_lowercase().as_str(),
                "1" | "t" | "true" | "y" | "yes" | "on"
            )
        })
}

/// One enabled companion's setup-time section (`init:`), as its program
/// answered it at the probe site.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct CompanionInit {
    /// The name it is registered under.
    pub name: String,
    pub init: InitLoadout,
}

impl Library {
    /// Each enabled registered companion's `init:` section, asked through the
    /// one probe at the caller's site (OD-8, D1): what init composes into the
    /// interview (`setup_guidance`) and `container tooling` declares
    /// (`tooling`). A disabled companion is not asked (OD-9); one that is
    /// absent, fails or does not parse is said, as an assembly says it, and
    /// gives nothing.
    pub fn companion_inits(&self, flags: &Flags) -> Outcome<Vec<CompanionInit>> {
        self.run(flags, |p| Ok(p.companion_inits()))
    }
}

impl Project {
    fn companion_inits(&mut self) -> Vec<CompanionInit> {
        self.ask_companions()
            .into_iter()
            .map(|(name, loadout)| CompanionInit {
                name,
                init: loadout.init,
            })
            .collect()
    }

    /// The project's catalog with the registered companions' loadouts in it.
    /// Where there are none, it is the catalog as it is.
    pub(crate) fn catalog_with_companions(&mut self) -> Catalog {
        let loadouts = self.companion_loadouts();
        if loadouts.is_empty() {
            return self.catalog.clone();
        }
        self.catalog.clone().with_companions(loadouts)
    }

    /// The `run:` bundles of the registered companions, `(name, bundle)`,
    /// asked for the first time this is called and kept for the command, so a
    /// companion is run once and its failure said once.
    fn companion_loadouts(&mut self) -> Vec<(String, Bundle)> {
        if let Some(asked) = &self.companion_bundles {
            return asked.clone();
        }
        let asked: Vec<(String, Bundle)> = self
            .ask_companions()
            .into_iter()
            .map(|(name, loadout)| (name, loadout.run))
            .collect();
        self.companion_bundles = Some(asked.clone());
        asked
    }

    /// Each enabled companion asked once for its whole loadout, by name.
    fn ask_companions(&mut self) -> Vec<(String, Loadout)> {
        let Some(port) = self.companions.clone().filter(|_| !self.no_companions) else {
            return Vec::new();
        };
        let mut loaded = Vec::new();
        for companion in self.config.companions() {
            if companion.state == State::Disabled {
                continue;
            }
            let name = companion.name.clone();
            // A hand-written name that is no plain name is said, not run.
            if !crate::write::companions::valid_name(&name) {
                self.notices.push(Notice::LoadoutProbeFailed {
                    name,
                    detail: "not a plain companion name".to_string(),
                });
                continue;
            }
            let program = companion.program();
            match port.loadout(&program, &self.probe_site) {
                CompanionLoadout::Loaded(text) => match parse_loadout(&text) {
                    Ok(loadout) => loaded.push((name, loadout)),
                    Err(cause) => self
                        .notices
                        .push(Notice::LoadoutUnparseable { name, cause }),
                },
                CompanionLoadout::Absent => {
                    self.notices
                        .push(Notice::CompanionNotOnPath { name, program });
                }
                CompanionLoadout::NoLoadout(_) => {}
                CompanionLoadout::Silent => {
                    self.notices.push(Notice::LoadoutProbeFailed {
                        name,
                        detail: "it printed no loadout".to_string(),
                    });
                }
                CompanionLoadout::ProbeFailed(detail) => {
                    self.notices
                        .push(Notice::LoadoutProbeFailed { name, detail });
                }
            }
        }
        loaded
    }
}
