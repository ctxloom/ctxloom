// Package mcp is the runner's implementation of delivery.Dynamic: the
// session's ONE MCP endpoint, bound at the address the Launch carries and
// served as Streamable HTTP under delivery.ServePolicy — a bearer on every
// request, an Origin allowlist with 403 on a miss. The cell-local tools and
// the ctxloom:// resources serve off the Loadout's Package and Index; the
// coordination tools, the host-relayed tools and artifact fetch ride the
// runner's reach-back Home. It holds no config.
package mcp

import (
	"context"
	"errors"

	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// Endpoint is one runner's Dynamic port: Serve binds the Loadout's endpoint.
type Endpoint struct {
	// Home is the runner's reach-back link: the coordination frames, the
	// host relays and artifact fetch ride it.
	Home *coord.Home
	// Reporter receives the endpoint's diagnostics; nil discards.
	Reporter report.Sink
}

var errUnimplemented = errors.New("runner mcp: not implemented")

// Serve implements delivery.Dynamic.
func (e Endpoint) Serve(ctx context.Context, lo delivery.Loadout, policy delivery.ServePolicy) (delivery.Served, error) {
	return delivery.Served{}, errUnimplemented
}

var _ delivery.Dynamic = Endpoint{}
