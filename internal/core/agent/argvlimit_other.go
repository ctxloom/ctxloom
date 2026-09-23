//go:build !linux

package agent

// perArgCapped: off Linux only the TOTAL argv is limited.
const perArgCapped = false
