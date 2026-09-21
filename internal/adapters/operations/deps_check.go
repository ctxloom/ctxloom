package operations

import (
	"context"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/config"
)

// CheckDependenciesRequest scopes one check: the whole lockfile, or the one
// canonical reference named.
type CheckDependenciesRequest struct {
	Ref string
}

// DependencyUpdate is one entry a newer commit satisfies the manifest
// constraint for.
type DependencyUpdate struct {
	Type             remote.ItemType
	Ref              string
	CurrentSHA       string
	LatestSHA        string
	RequestedVersion string
	Kind             remote.SelectorKind
	Version          string
}

// UncheckedReason says why an entry's currency could not be established.
type UncheckedReason int

const (
	UncheckedUnparseable UncheckedReason = iota
	UncheckedNoRepositoryURL
	UncheckedUnreachable
	UncheckedUnresolvable
)

// UncheckedDependency is one entry that was neither verified current nor
// found outdated.
type UncheckedDependency struct {
	Ref        string
	URL        string
	Constraint string
	Reason     UncheckedReason
	Err        error
}

// RefreshFailure is one repository whose clone could not be refreshed before
// the check; its entries were checked against the stale clone.
type RefreshFailure struct {
	URL string
	Err error
}

// DependencyStatus is a single-reference check's answer.
type DependencyStatus struct {
	Ref        string
	CurrentSHA string
	LatestSHA  string
	Update     DependencyUpdate
}

// CheckDependenciesResult is what the check found.
type CheckDependenciesResult struct {
	Entries            int
	Refresh            []RefreshFailure
	Updates            []DependencyUpdate
	Unchecked          []UncheckedDependency
	SkippedEmpty       int
	MissingDefaults    []string
	MissingDefaultsErr error
	Single             *DependencyStatus
}

// CheckDependencies reports which installed dependencies have a newer commit
// available within their constraint. It changes nothing.
func CheckDependencies(ctx context.Context, app *App, req CheckDependenciesRequest) (CheckDependenciesResult, error) {
	return CheckDependenciesResult{}, nil
}

// ReconcilePlan is what a reconcile decided, split by whether it is authority.
type ReconcilePlan struct {
	Gone        []string
	Unreachable []UncheckedRemote
}

// UncheckedRemote is one repository the reconcile could not establish the
// state of, and what that cost.
type UncheckedRemote struct {
	URL    string
	Refs   []string
	Reason string
}

// ReconcileResult is the plan a reconcile applied and what it could not do
// while applying it.
type ReconcileResult struct {
	Plan     ReconcilePlan
	Warnings []string
}

// ReconcileInstalled removes installed dependencies upstream demonstrably no
// longer serves.
func ReconcileInstalled(ctx context.Context, cfg *config.Config) (ReconcileResult, error) {
	return ReconcileResult{}, nil
}
