package agent

// PreviousSessionByListing resolves the session before the current one at READ
// time. The caller's list func is expected to return metadata sorted
// most-recent-first, so the current (actively written) session is index 0 and
// the previous is index 1.
//
// When myHarp is set and harpOf is provided, it first scopes to transcripts
// whose embedded marker matches myHarp: with /clear forking a new transcript per
// session (all under the same harp), the harp's own current and previous
// transcripts are both marked, so scoped[1] is the true previous session for
// this harp — immune to a second terminal interleaving transcripts in the same
// project directory. Resolution falls back to the positional previous (metas[1])
// when the harp is unknown, the harp has fewer than two marked transcripts
// (e.g. pre-marker history), or the backend embeds no marker — preserving the
// datetime-ordered behavior as a floor. Returns nil when fewer than two
// sessions exist.
//
// This is engine-agnostic — both the claude and gemini session-history readers
// drive it with their own list/byPath/harpOf closures.
func PreviousSessionByListing(
	workDir, myHarp string,
	list func(string) ([]SessionMeta, error),
	byPath func(string) (*Session, error),
	harpOf func(string) string,
) (*Session, error) {
	metas, err := list(workDir)
	if err != nil {
		return nil, err
	}
	if myHarp != "" && harpOf != nil {
		var scoped []SessionMeta
		for _, m := range metas {
			if harpOf(m.Path) == myHarp {
				scoped = append(scoped, m)
			}
		}
		if len(scoped) >= 2 {
			return byPath(scoped[1].Path)
		}
	}
	if len(metas) < 2 {
		return nil, nil
	}
	return byPath(metas[1].Path)
}
