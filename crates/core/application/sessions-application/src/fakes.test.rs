//! Fakes for the ports, for the use cases' tests. Every future they return is
//! ready at once, so [`ready`] runs a use case to its end without a runtime.

use std::collections::BTreeMap;
use std::path::PathBuf;

use angzarr_embedded::angzarr_ports::memory::MemoryStore;
use angzarr_embedded::v1::{command_page, Command, CommandBook, CommandPage, Cover, Uuid};
use angzarr_embedded::{Kernel, OnRetryable, Payload, Sampled, StoreError, Timestamp};
use coded_diagnostic::Report;
use ports::{Project, ProjectIdentity};
use session_ports::{GoSession, GoSessions, ProjectStores};
use sessions::{MintSession, Origin, SessionAggregate, SessionCommand, SESSION_DOMAIN};

/// Run a future whose every await is ready at once: the test support's.
pub use angzarr_embedded::testing::block_on_ready as ready;
use ports::testing::FakeClock;

pub fn project(id: &str) -> Project {
    Project {
        id: id.to_string(),
        root: Some(format!("/work/{id}")),
        store: PathBuf::from(format!("/home/.ctxloom/projects/{id}/state.db")),
    }
}

/// A work root with a project (`bound`), or none; a pin resolves, as an
/// explicit id always does; every project is `every`.
#[derive(Default)]
pub struct FakeIdentity {
    pub bound: Option<Project>,
    pub every: Vec<Project>,
    pub fails: Option<Report>,
}

impl ProjectIdentity for FakeIdentity {
    fn for_write(&mut self, pinned: Option<&str>) -> Result<Project, Report> {
        if let Some(report) = &self.fails {
            return Err(report.clone());
        }
        Ok(match pinned {
            Some(id) => project(id),
            None => self
                .bound
                .get_or_insert_with(|| project("jolly-mute-magma"))
                .clone(),
        })
    }

    fn for_read(&mut self, pinned: Option<&str>) -> Result<Option<Project>, Report> {
        if let Some(report) = &self.fails {
            return Err(report.clone());
        }
        Ok(match pinned {
            Some(id) => Some(project(id)),
            None => self.bound.clone(),
        })
    }

    fn every_project(&mut self) -> Result<Vec<Project>, Report> {
        if let Some(report) = &self.fails {
            return Err(report.clone());
        }
        Ok(self.every.clone())
    }

    fn rootless_directory(&mut self) -> Result<Option<String>, Report> {
        Ok(None)
    }
}

/// One in-memory store per project, absent until a write opens it. The
/// projects opened to write (`opened`) are recorded, so a test can say a read
/// created nothing.
///
/// `one` makes it the one store (T14): every project's streams in one
/// in-memory store, keyed [`ONE`].
#[derive(Default)]
pub struct FakeStores {
    pub stores: BTreeMap<String, MemoryStore>,
    pub opened: Vec<String>,
    pub one: bool,
}

/// The key of the one store, where [`FakeStores::one`].
pub const ONE: &str = "the-one-store";

impl FakeStores {
    /// The key `project`'s streams are kept under.
    pub fn key(&self, project: &str) -> String {
        match self.one {
            true => ONE.to_string(),
            false => project.to_string(),
        }
    }
}

impl ProjectStores for FakeStores {
    type Store = MemoryStore;

    async fn open(&mut self, project: &Project) -> Result<&MemoryStore, StoreError> {
        self.opened.push(project.id.clone());
        let key = self.key(&project.id);
        Ok(self.stores.entry(key).or_default())
    }

    async fn open_existing(
        &mut self,
        project: &Project,
    ) -> Result<Option<&MemoryStore>, StoreError> {
        Ok(self.stores.get(&self.key(&project.id)))
    }
}

/// The stream of the session with stable id `id`.
pub fn cover(id: u8) -> Cover {
    Cover {
        domain: SESSION_DOMAIN.to_string(),
        root: Some(Uuid::from_bytes([id; 16])),
        edition: None,
    }
}

/// Run `command` on session `id`'s stream in `store` directly, as another
/// process would, sampled at `seconds`.
pub fn run(store: &MemoryStore, id: u8, seconds: i64, command: impl Into<SessionCommand>) {
    let book = CommandBook {
        cover: Some(cover(id)),
        page: Some(CommandPage {
            body: Some(command_page::Body::Command(Command {
                payload: Some(command.into().to_any()),
                handling: None,
            })),
            ..CommandPage::default()
        }),
    };
    run_book(store, seconds, book);
}

/// Send `book` to its session stream directly, sampled at `seconds`.
#[allow(
    clippy::disallowed_methods,
    reason = "a test kernel or store of its own"
)]
fn run_book(store: &MemoryStore, seconds: i64, book: CommandBook) {
    let registry = crate::registry();
    let sampled = Sampled {
        now: Timestamp { seconds, nanos: 0 },
        command_id: Uuid::from_bytes([(seconds % 251) as u8; 16]),
    };
    ready(
        Kernel::new(&registry)
            .on_retryable(OnRetryable {
                max_retries: 0,
                ..OnRetryable::default()
            })
            .handle::<SessionAggregate, _>(store, book, &sampled),
    )
    .unwrap_or_else(|failure| panic!("{failure:?}"));
}

/// A session minted in `store` at `started`, with an engine and (when
/// `native_id` is not empty) bound.
pub fn minted(store: &MemoryStore, id: u8, harp: &str, project_dir: &str, started: i64) {
    minted_in(store, "jolly-mute-magma", id, harp, project_dir, started);
}

/// [`minted`], in `project`.
pub fn minted_in(
    store: &MemoryStore,
    project: &str,
    id: u8,
    harp: &str,
    project_dir: &str,
    started: i64,
) {
    run(
        store,
        id,
        started,
        MintSession {
            harp: harp.to_string(),
            project_id: project.to_string(),
            project_dir: project_dir.to_string(),
            output_dir: format!("/home/Documents/ctxloom/proj/{harp}"),
            engine: "claude-code".to_string(),
            origin: Origin::Session,
            started_at: None,
            depth: 0,
            owner: None,
            workspace: None,
        },
    );
}

/// Go's session directories, as a test lays them out.
#[derive(Default)]
pub struct FakeGo(pub Vec<GoSession>);

impl GoSessions for FakeGo {
    fn go_sessions(&mut self) -> Result<Vec<GoSession>, Report> {
        Ok(self.0.clone())
    }
}

/// A project whose work root is `root`.
pub fn project_at(id: &str, root: &str) -> Project {
    Project {
        root: Some(root.to_string()),
        ..project(id)
    }
}

/// The importer's way out, over in-memory stores: each command handled by a
/// kernel of the test's own, as the daemon's core handles it for the real
/// one. `stores` is where the streams are kept.
#[derive(Default)]
pub struct StoreDispatch {
    pub stores: FakeStores,
    pub clock: FakeClock,
    pub sent: Vec<String>,
}

impl crate::Dispatch for StoreDispatch {
    #[allow(
        clippy::disallowed_methods,
        reason = "a test kernel or store of its own"
    )]
    async fn send(&mut self, project: &Project, book: CommandBook, id: &Uuid) -> crate::Sending {
        use angzarr_embedded::Handled;
        use ports::Clock;
        let store = self.stores.open(project).await.expect("a store");
        let registry = crate::registry();
        let sampled = Sampled {
            now: self.clock.now(),
            command_id: id.clone(),
        };
        let domain = book.cover.as_ref().map(|c| c.domain.clone());
        self.sent.extend(domain);
        let handled = Kernel::new(&registry)
            .on_retryable(OnRetryable::default())
            .handle_any(store, book, &sampled)
            .await;
        match handled {
            Ok(handled) => Ok(Ok(crate::Sent {
                duplicate: matches!(handled, Handled::Duplicate(..)),
            })),
            Err(angzarr_embedded::Failure::Rejected(rejected)) => {
                Ok(Err(crate::refused(&rejected)))
            }
            Err(failure) => Err(crate::dispatch::failed_unrun(&failure)),
        }
    }

    #[allow(
        clippy::disallowed_methods,
        reason = "a test kernel or store of its own"
    )]
    async fn events(
        &mut self,
        project: &Project,
        cover: &Cover,
    ) -> Result<Vec<angzarr_embedded::v1::EventPage>, StoreError> {
        use angzarr_embedded::{EventStore, StreamRef};
        let store = self.stores.open(project).await?;
        let stream = StreamRef::try_from(cover).expect("a stream");
        Ok(store.read(stream, 0, None).await?.pages)
    }

    fn dead_letters(&mut self) -> Vec<crate::DeadLetter> {
        Vec::new()
    }
}
