package agent

// DeliveryStatus is a route's currency verdict.
type DeliveryStatus string

const (
	// StatusDelivered: what the route carries matches what was composed.
	StatusDelivered DeliveryStatus = "delivered"
	// StatusStale: the route carries ctxloom content that no longer matches.
	StatusStale DeliveryStatus = "stale"
	// StatusMissing: the route exists and carries nothing.
	StatusMissing DeliveryStatus = "missing"
)

// Currency is one route's answer about what it carries.
type Currency struct {
	Status DeliveryStatus
	// Detail explains any status that is not plainly good, in the route's own
	// terms — a path for a file, a hash for a per-run append.
	Detail string
}
