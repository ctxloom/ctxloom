//! What a recorded plan writes, as the daemon writes it.

use std::collections::BTreeSet;
use std::sync::Arc;

use daemon_api::{DepsAnswer, DepsPlanned};
use daemon_proto::v1;
use deps_files::{ProjectFiles, RecordingDepsFiles};
use deps_ports::{BundleSlot, CachePath, DepsFiles, RepoTree, Repository, Why};

/// Remotes that answer one tree id, and fail every other question.
struct TreeOf;

impl deps_ports::Remotes for TreeOf {
    fn local_repository(&self, _: &str) -> Result<String, Why> {
        Err("unasked".to_string())
    }
    fn ensure(&self, _: &Repository) -> Result<(), Why> {
        Err("unasked".to_string())
    }
    fn refresh(&self, _: &Repository) -> Result<(), Why> {
        Err("unasked".to_string())
    }
    fn tags(&self, _: &Repository) -> Result<Vec<String>, Why> {
        Err("unasked".to_string())
    }
    fn resolve_ref(&self, _: &Repository, _: &str) -> Result<String, Why> {
        Err("unasked".to_string())
    }
    fn resolve_tag(&self, _: &Repository, _: &str) -> Result<String, Why> {
        Err("unasked".to_string())
    }
    fn default_branch(&self, _: &Repository) -> Result<String, Why> {
        Err("unasked".to_string())
    }
    fn read_bundle(&self, _: &Repository, _: &str, _: &str) -> Result<Option<RepoTree>, Why> {
        Err("unasked".to_string())
    }
    fn bundle_tree(&self, repo: &Repository, sha: &str, bundle: &str) -> Result<String, Why> {
        Ok(format!(
            "{} {} {sha} {bundle}",
            repo.url,
            repo.cache.segments().join("/")
        ))
    }
    fn list_bundles(&self, _: &Repository) -> Result<Vec<String>, Why> {
        Err("unasked".to_string())
    }
    fn has_bundle(&self, _: &Repository, _: &str) -> Result<bool, Why> {
        Err("unasked".to_string())
    }
    fn publish(
        &self,
        _: &Repository,
        _: &std::collections::BTreeMap<String, Vec<u8>>,
        _: &BTreeSet<String>,
        _: &str,
    ) -> Result<String, Why> {
        Err("unasked".to_string())
    }
}

fn files() -> Arc<ProjectFiles> {
    let fs = Arc::new(file_system::Rooted::temp().expect("a temporary root"));
    Arc::new(ProjectFiles::new(
        fs,
        std::path::PathBuf::from(os::host().flavor().from_top("/proj/.ctxloom")),
    ))
}

fn answer() -> DepsAnswer {
    DepsAnswer::Pulled(v1::PullDependenciesResponse::default())
}

// Step 10c U4: a recorded install is written as its tree, its files with
// their executable bit, and git's own id of it, asked of the remotes at
// the repository the identity names (its url) and cached where the slot
// says; the registry and the lock are written only where they differ from
// the files as they are.
#[test]
fn a_recorded_install_is_written_with_gits_tree_id_and_an_unchanged_file_not_at_all() {
    let files = files();
    files.write_lock("pinned\n").expect("a lock");
    let store = RecordingDepsFiles::over(Arc::clone(&files));
    let identity = "ctxloom+file:///srv/r.git//bundles/demo";
    let slot = BundleSlot {
        identity: identity.to_string(),
        repository: CachePath::new(vec!["srv".to_string(), "r.git".to_string()]).expect("a path"),
        bundle: CachePath::new(vec!["demo".to_string()]).expect("a path"),
    };
    let tree: RepoTree = [("bundle.yaml".to_string(), b"version: 1\n".to_vec())].into();
    let executable: BTreeSet<String> = ["bundle.yaml".to_string()].into();
    store
        .install(&slot, "abc", &tree, &executable)
        .expect("recorded");
    store.write_lock("pinned\n").expect("recorded");
    store.write_registry("remotes: {}\n").expect("recorded");
    let planned: DepsPlanned = super::planned(
        answer(),
        (&store.take(), files.as_ref()),
        &TreeOf,
        Vec::new(),
    )
    .expect("planned");
    assert_eq!(planned.lock, None, "an unchanged lock is not written");
    assert_eq!(planned.registry.as_deref(), Some(&b"remotes: {}\n"[..]));
    assert_eq!(planned.installs.len(), 1);
    let install = &planned.installs[0];
    assert_eq!(install.path, "cache/bundles/srv/r.git/demo/abc");
    assert_eq!(
        (install.identity.as_str(), install.sha.as_str()),
        (identity, "abc")
    );
    assert_eq!(install.tree, "file:///srv/r.git srv/r.git abc demo");
    assert_eq!(
        install
            .files
            .iter()
            .map(|put| (put.path.as_str(), put.executable))
            .collect::<Vec<_>>(),
        [("bundle.yaml", true), (deps_files::IDENTITY_MARKER, false)]
    );
    let changed = RecordingDepsFiles::over(Arc::clone(&files));
    changed.write_lock("moved\n").expect("recorded");
    let planned = super::planned(
        answer(),
        (&changed.take(), files.as_ref()),
        &TreeOf,
        Vec::new(),
    )
    .expect("planned");
    assert_eq!(planned.lock.as_deref(), Some(&b"moved\n"[..]));
    assert_eq!(planned.registry, None);
}
