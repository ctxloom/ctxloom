//! The daemon's prost types, generated into `OUT_DIR` with angzarr's
//! settings. Generated code is never committed. The protos are the daemon's
//! drafts (ruling 132); their resource messages are what the read models
//! project for `HandleSync` (S32), so they are compiled from slice 1 on.

fn main() -> Result<(), angzarr_build::BuildError> {
    angzarr_build::wire(
        &[
            "ctxloom/daemon/v1/daemon.proto",
            "ctxloom/daemon/v1/discovery.proto",
            "ctxloom/daemon/v1/launch.proto",
            "ctxloom/daemon/v1/needs.proto",
        ],
        &["../../../../proto"],
    )
}
