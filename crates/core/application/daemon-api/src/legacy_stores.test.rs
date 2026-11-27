//! A project with a store of its own from before the one store is refused at
//! its resolution, naming the file; one without registers as ever.

use std::sync::Arc;

use daemon_proto::v1;

use super::LegacyStores;
use crate::server::tests::{fixture, refused, resolve, COMMIT};

struct Old(Option<&'static str>);

impl LegacyStores for Old {
    fn held(&self, _: &str) -> Option<String> {
        self.0.map(str::to_string)
    }
}

#[tokio::test]
async fn a_project_with_a_store_of_its_own_is_refused_naming_it() {
    let f = fixture(vec!["calm-quiet-heron"]);
    let api = f
        .api
        .refusing_legacy_stores(Arc::new(Old(Some("/h/projects/p/ctxloom.db"))));
    let (_, refusal): (_, v1::PerProjectStore) =
        refused(api.resolve_project(resolve(COMMIT, "")).await);
    assert_eq!(refusal.path, "/h/projects/p/ctxloom.db");
}

#[tokio::test]
async fn a_project_with_none_is_resolved() {
    let f = fixture(vec!["calm-quiet-heron"]);
    let api = f.api.refusing_legacy_stores(Arc::new(Old(None)));
    api.resolve_project(resolve(COMMIT, "")).await.unwrap();
}
