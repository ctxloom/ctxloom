use super::*;

const PARENT: &str = "projects/calm-otter";

#[test]
fn test_a_remote_listing_reads_back_from_its_message() {
    let listing = RemoteListing {
        remotes: vec![
            RemoteRow {
                name: "acme".to_string(),
                url: "https://example.com/acme.git".to_string(),
                forge: String::new(),
            },
            RemoteRow {
                name: "origin".to_string(),
                url: "file:///srv/origin.git".to_string(),
                forge: String::new(),
            },
        ],
        count: 2,
        default: "origin".to_string(),
    };
    let message = library_api::deps::remotes(PARENT, &listing);
    assert_eq!(remotes(&message), listing);
    let none = library_api::deps::remotes(
        PARENT,
        &RemoteListing {
            default: String::new(),
            ..listing.clone()
        },
    );
    assert_eq!(remotes(&none).default, "");
}

#[test]
fn test_a_dependency_listing_reads_back_from_its_message() {
    let row = |origin: &str, name: &str| DepRow {
        name: name.to_string(),
        reference: format!("example.com/{name}.git@{name}"),
        sha: "0123456789abcdef".to_string(),
        constraint: "v1".to_string(),
        held: origin.is_empty(),
        origin: origin.to_string(),
        url: format!("https://example.com/{name}.git"),
    };
    let listing = DepsListing {
        deps: vec![row("origin", "demo"), row("", "stray")],
        count: 2,
    };
    let message = library_api::deps::dependencies(PARENT, &listing);
    assert_eq!(dependencies(&message), listing);
}
