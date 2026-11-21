//! The runs domain's prost types, generated into `OUT_DIR` with
//! angzarr's settings. Generated code is never committed.

fn main() -> Result<(), angzarr_build::BuildError> {
    angzarr_build::wire(&["ctxloom/runs/v1/runs.proto"], &["../../../../proto"])
}
