//! The router over two in-process cores: each stream reaches the store that
//! holds it, and one no store holds is nowhere.

use std::sync::Arc;

use angzarr_embedded::testing::MemoryStore;
use angzarr_embedded::v1::{CommandBook, Cover, Uuid};
use angzarr_embedded::CommandBookExt;
use embedded_edge::EmbeddedEdge;
use run_ports::{Arrival, Edge, Unserved};

use super::{Routes, RoutingEdge};
use crate::InProcessCore;

type TheEdge = EmbeddedEdge<crate::CoreStores<MemoryStore>>;

fn cover(n: u8) -> Cover {
    Cover {
        domain: sessions::SESSION_DOMAIN.to_string(),
        root: Some(Uuid::from_bytes([n; 16])),
        edition: None,
    }
}

fn mint(n: u8, project: &str) -> CommandBook {
    CommandBook::command_of(
        cover(n),
        &sessions::SessionCommand::from(sessions::MintSession {
            harp: format!("harp-{n}"),
            project_id: project.to_string(),
            project_dir: "/w".to_string(),
            output_dir: "/o".to_string(),
            engine: "claude-code".to_string(),
            origin: sessions::Origin::Session,
            started_at: None,
            depth: 0,
            owner: None,
        }),
    )
}

fn keep(n: u8) -> CommandBook {
    CommandBook::command_of(
        cover(n),
        &sessions::SessionCommand::from(sessions::SetKept { kept: true }),
    )
}

#[tokio::test]
async fn each_stream_reaches_the_store_that_holds_it() {
    let a = InProcessCore::start(MemoryStore::default()).await;
    let b = InProcessCore::start(MemoryStore::default()).await;
    a.edge().handle(mint(1, "alpha")).await.unwrap();
    b.edge().handle(mint(2, "beta")).await.unwrap();
    let routes: Arc<Routes<TheEdge>> = Arc::default();
    routes.add("alpha", a.edge());
    routes.add("beta", b.edge());
    assert!(routes.project("beta").is_some());
    assert!(routes.project("gamma").is_none());
    let edge = RoutingEdge(Arc::clone(&routes));
    assert_eq!(edge.events(&cover(1), 0).await.unwrap().len(), 2);
    assert_eq!(edge.events(&cover(2), 0).await.unwrap().len(), 2);
    // Twice: the second is routed by what the first found.
    edge.handle(keep(2)).await.unwrap();
    edge.serve(keep(2), Arrival::Loopback).await.unwrap();
    assert_eq!(b.edge().events(&cover(2), 0).await.unwrap().len(), 3);
    assert_eq!(a.edge().events(&cover(2), 0).await.unwrap().len(), 0);
}

#[tokio::test]
async fn a_stream_no_store_holds_is_nowhere() {
    let a = InProcessCore::start(MemoryStore::default()).await;
    let routes: Arc<Routes<TheEdge>> = Arc::default();
    routes.add("alpha", a.edge());
    let edge = RoutingEdge(routes);
    assert!(edge.events(&cover(9), 0).await.unwrap().is_empty());
    assert!(edge.handle(keep(9)).await.is_err());
    assert!(matches!(
        edge.serve(keep(9), Arrival::Loopback).await,
        Err(Unserved::Hidden { domain }) if domain == sessions::SESSION_DOMAIN
    ));
    let mut rootless = keep(9);
    rootless.cover = None;
    assert!(edge.handle(rootless).await.is_err());
}
