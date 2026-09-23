//go:build integration || acceptance

package testenv

import (
	"context"
	"fmt"

	"github.com/Masterminds/semver/v3"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/release"
)

// TreeRelease is the release a fixture signs tree b under, built the way
// `bundle sign` builds it: b's own id, and the version, retractions and
// withdrawal its bundle.yaml declares.
func TreeRelease(ctx context.Context, b content.Bundle) (release.Release, error) {
	raw, err := b.ReadFile(ctx, bundles.DirectoryFormManifest)
	if err != nil {
		return release.Release{}, fmt.Errorf("read %s of %q: %w", bundles.DirectoryFormManifest, b.ID(), err)
	}
	env, err := bundles.ParseBundle(raw)
	if err != nil {
		return release.Release{}, fmt.Errorf("parse %s of %q: %w", bundles.DirectoryFormManifest, b.ID(), err)
	}
	v, err := semver.StrictNewVersion(env.Version)
	if err != nil {
		return release.Release{}, fmt.Errorf("%q declares version %q, which is not strict semver: %w", b.ID(), env.Version, err)
	}
	rel := release.Release{Name: string(b.ID()), Version: v, Withdrawn: env.Withdrawn}
	for _, r := range env.Retracts {
		rv, err := semver.StrictNewVersion(r.Version)
		if err != nil {
			return release.Release{}, fmt.Errorf("%q retracts version %q, which is not strict semver: %w", b.ID(), r.Version, err)
		}
		rel.Retracts = append(rel.Retracts, release.Retraction{Version: rv, Reason: r.Reason})
	}
	return rel, nil
}
