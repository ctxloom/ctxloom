// Package v2 is the session sidecar's step to generation 2.
package v2

import "gopkg.in/yaml.v3"

// Step is generation 1 -> 2: the sidecar gains `keep`, the mark
// `ctxloom session keep` sets so a session's transcript is never reclaimed
// at any age. A marker generation — a generation-1 sidecar carries no mark
// and needs no edit — whose bump is what makes an older binary REFUSE a
// sidecar it would otherwise read with the mark silently dropped, and then
// destroy the transcript the mark exists to keep.
type Step struct{}

// To is the generation this step migrates to.
func (Step) To() int { return 2 }

// Name describes the step.
func (Step) Name() string { return "add keep (transcript retention mark)" }

// Apply edits nothing: a generation-1 sidecar is a valid generation-2 one
// that carries no keep mark.
func (Step) Apply(*yaml.Node) {}
