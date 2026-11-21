//! The trust domain layer, generated into `OUT_DIR` from the protos.
//! Generated code is never committed.

fn main() -> Result<(), angzarr_build::BuildError> {
    angzarr_build::markup_domain(
        &["ctxloom/trust/v1/trust.proto"],
        &["../../../../proto"],
        &[("ctxloom.trust.v1", "::trust_proto::v1")],
    )
}
