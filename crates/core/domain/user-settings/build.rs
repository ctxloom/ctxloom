//! The user-settings domain layer, generated into `OUT_DIR` from the protos.
//! Generated code is never committed.

fn main() -> Result<(), angzarr_build::BuildError> {
    angzarr_build::markup_domain(
        &["ctxloom/settings/v1/settings.proto"],
        &["../../../../proto"],
        &[("ctxloom.settings.v1", "::user_settings_proto::v1")],
    )
}
