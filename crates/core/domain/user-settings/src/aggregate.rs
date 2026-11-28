//! The user-settings aggregate on angzarr-embedded's markup: facts in, the
//! change recorded.

use angzarr_embedded::markup::{pack_message, Bug, Context, FactEmit, PageContext, Snapshots};
use angzarr_embedded_macros::{aggregate, applies, handles_fact};

use crate::{RecordSettingsObserved, SettingsEvent, SettingsObserved, UserSettings};

#[derive(Clone, Debug, Default)]
pub struct SettingsAggregate {
    settings: UserSettings,
}

impl SettingsAggregate {
    /// The folded settings.
    pub fn state(&self) -> &UserSettings {
        &self.settings
    }
}

/// Names the anchor, and takes no snapshots: the stream is folded whole.
impl Snapshots for SettingsAggregate {
    type Anchor = crate::generated::UserSettingsState;
}

#[aggregate(domain = "user-settings", command = crate::UserSettingsStateCommand, event = SettingsEvent)]
impl SettingsAggregate {
    /// A scan of one settings file: recorded when it says something the
    /// stream does not hold (new content, the file gone or back), else
    /// nothing, so a re-read of an unchanged file adds no event.
    #[handles_fact(RecordSettingsObserved)]
    fn observed(&self, fact: RecordSettingsObserved, ctx: &Context) -> Result<FactEmit<Self>, Bug> {
        let _ = ctx;
        let event = self
            .settings
            .differs(fact.kind, fact.present, &fact.content_id)
            .then_some(SettingsObserved {
                kind: fact.kind,
                path: fact.path,
                present: fact.present,
                content_id: fact.content_id,
                content: fact.content,
                sealed: fact.sealed,
            });
        Ok(FactEmit::of(event.map(|event| pack_message(&event))))
    }

    #[applies(SettingsObserved)]
    fn settings_observed(&mut self, event: SettingsObserved, page: &PageContext) {
        self.settings.apply(event, page.sequence);
    }
}

#[cfg(test)]
#[path = "aggregate.test.rs"]
mod tests;
