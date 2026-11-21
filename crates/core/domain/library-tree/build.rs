//! The library domain layer, generated into `OUT_DIR` from the protos.
//! Generated code is never committed.

fn main() -> Result<(), angzarr_build::BuildError> {
    angzarr_build::markup_domain(
        &["ctxloom/library/v1/library.proto"],
        &["../../../../proto"],
        &[("ctxloom.library.v1", "::library_domain_proto::v1")],
    )
}
