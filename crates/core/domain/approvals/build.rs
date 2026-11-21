//! The approvals domain layer, generated into `OUT_DIR` from the protos.
//! Generated code is never committed.

fn main() -> Result<(), angzarr_build::BuildError> {
    angzarr_build::markup_domain(
        &["ctxloom/approvals/v1/approvals.proto"],
        &["../../../../proto"],
        &[("ctxloom.approvals.v1", "::approvals_proto::v1")],
    )
}
