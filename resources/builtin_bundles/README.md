# Embedded built-in bundles

This directory is intentionally empty of `.yaml` bundles as of the companion
loadout protocol (signature-envelope spec §4.3/§6). `ltk.yaml` and
`taskloom.yaml` used to live here, vendored into the ctxloom binary; they
have been deleted. That content now ships from the companions' own
binaries via `<bin> loadout --format json` — see `cmd/ltk/loadout.yaml` and
`cmd/taskloom/loadout.yaml`, and `internal/config/companions.go`
(`DiscoverCompanions`, `ProbeCompanionLoadouts`).

The mechanism this directory feeds (`resources.ListBuiltinBundles`,
`resources.GetBuiltinBundle`, and their consumers in
`internal/config/config_bundles.go`: `resolveBuiltinBundleHooks`,
`resolveBuiltinBundleMCPServers`, `Config.ResolveBuiltinBundleFragments`) is
kept for a FUTURE bundle that genuinely needs to ship compiled into the
binary itself — core ctxloom functionality with no companion binary of its
own — rather than as a discoverable companion's loadout. Builtin content is
exempt from review (signing-design.md: "builtins are not signed, and must
not be" — signing bytes embedded in the binary that verifies them is
circular); a companion loadout is third-party content and is always gated.

This `README.md` exists only so the `//go:embed all:builtin_bundles`
directive in `resources/embed.go` has at least one file to embed — Go's
embed package errors at compile time on a pattern matching zero files.
`ListBuiltinBundles` filters by `.yaml` suffix, so this file is never listed
as a bundle.
