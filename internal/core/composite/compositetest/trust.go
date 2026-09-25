// Package compositetest builds a composite.Trust over FAKE ports, so a test
// can state what a human decided, what a publisher withdrew, or that a
// store is unreadable, without a countersign store or a lockfile on disk.
// The gate underneath is the real one (composite.NewTrust): the cascade a
// test exercises here is the cascade production decides with.
package compositetest

import (
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/trust"
)

// Option shapes the fake ports a Trust decides with.
type Option func(*ports)

type ports struct {
	rejected  func(trust.Ref, []byte) bool
	approved  func(trust.Ref, []byte, bundles.ContentForm) bool
	retracted func(trust.BundleRef) (bool, string)
	observe   func(trust.Ref, []byte)
	fault     error
}

// Trust is a gate over fake ports: with no options it trusts no signer,
// holds no review record and no retraction, so it admits by locality alone
// and withholds everything that travelled.
func Trust(opts ...Option) composite.Trust {
	tr, err := composite.NewTrust(Ports(opts...))
	if err != nil {
		panic(err) // every port is supplied above
	}
	return tr
}

// Ports are the three fake ports Trust is built over, for a test's
// config.Sources to hand the Owner.
func Ports(opts ...Option) (composite.TrustRoot, composite.ReviewRecords, composite.RetractionRecords) {
	p := &ports{}
	for _, o := range opts {
		o(p)
	}
	return Root(), records{p}, retraction{p}
}

// Root is a trust root that trusts no key.
func Root() composite.TrustRoot { return trust.NoSigners{} }

// RejectWhen records a human rejection for every (ref, bytes) fn accepts.
func RejectWhen(fn func(ref trust.Ref, payload []byte) bool) Option {
	return func(p *ports) { p.rejected = fn }
}

// RejectAll records a human rejection of everything: the deny-everything gate.
func RejectAll() Option { return RejectWhen(func(trust.Ref, []byte) bool { return true }) }

// ApproveWhen records a human approval for every (ref, bytes, form) fn accepts.
func ApproveWhen(fn func(ref trust.Ref, payload []byte, form bundles.ContentForm) bool) Option {
	return func(p *ports) { p.approved = fn }
}

// ApproveAll records a human approval of everything reviewable.
func ApproveAll() Option {
	return ApproveWhen(func(trust.Ref, []byte, bundles.ContentForm) bool { return true })
}

// RetractWhen records a publisher retraction, with its reason, for every ref
// fn accepts.
func RetractWhen(fn func(ref trust.BundleRef) (retracted bool, reason string)) Option {
	return func(p *ports) { p.retracted = fn }
}

// Observe is called with every claimed exposure the gate decides about, so a
// test can assert the ref shape and the bytes a choke feeds the gate.
func Observe(fn func(ref trust.Ref, payload []byte)) Option {
	return func(p *ports) { p.observe = fn }
}

// Faulted makes the review records unreadable: the gate withholds everything
// and names err.
func Faulted(err error) Option { return func(p *ports) { p.fault = err } }

type records struct{ p *ports }

func (r records) Rejected(ref trust.Ref, payload []byte) bool {
	if r.p.observe != nil {
		r.p.observe(ref, payload)
	}
	return r.p.rejected != nil && r.p.rejected(ref, payload)
}

func (r records) Approved(ref trust.Ref, payload []byte, form bundles.ContentForm) bool {
	return r.p.approved != nil && r.p.approved(ref, payload, form)
}

func (r records) Fault() error { return r.p.fault }

type retraction struct{ p *ports }

func (r retraction) Retracted(ref trust.BundleRef) (bool, string) {
	if r.p.retracted == nil {
		return false, ""
	}
	return r.p.retracted(ref)
}
