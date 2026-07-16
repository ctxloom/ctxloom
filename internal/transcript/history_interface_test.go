// Package transcript_test is a black-box (external) test package specifically
// so this file can import BOTH internal/transcript and internal/lm/grpc
// (aliased pb) to prove CanonicalHistory structurally satisfies
// pb.SessionSource — without internal/transcript itself ever importing
// internal/lm/grpc. That asymmetry is deliberate: S2 (tough-cloud, running in
// parallel on this same release) wires internal/lm/grpc/chat.go to import
// internal/transcript for Tee, so a transcript -> grpc import here would
// create transcript -> grpc -> transcript. An external test package compiles
// as its own unit and carries no such cycle risk (see history.go's package
// doc for the full rationale).
package transcript_test

import (
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/transcript"
)

var _ pb.SessionSource = (*transcript.CanonicalHistory)(nil)
