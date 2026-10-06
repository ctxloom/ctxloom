package operations

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/adapters/companions"
)

// CompanionAllowRequest names the companion binary to allow — a path, or a
// bare name resolved through PATH — and whether to record it (Apply) or only
// report what would be recorded.
type CompanionAllowRequest struct {
	PathOrName string
	Apply      bool
}

// CompanionAllowResult is what an allow recorded, or would record: the
// resolved path and the hash of the bytes there now. Previous is the record
// already held for that path when its hash differs — the "hash changed: old ->
// new" a human reads before re-allowing a rebuilt binary.
type CompanionAllowResult struct {
	Key      companions.CompanionKey  `json:"key"`
	Previous *companions.CompanionKey `json:"previous,omitempty"`
	Applied  bool                     `json:"applied"`
}

// AllowCompanion records that ctxloom may execute the binary req names, as its
// bytes are now. Without Apply it writes nothing.
func AllowCompanion(ctx context.Context, fs afero.Fs, req CompanionAllowRequest) (CompanionAllowResult, error) {
	if err := ctx.Err(); err != nil {
		return CompanionAllowResult{}, err
	}
	key, err := companions.ResolveCompanion(req.PathOrName)
	if err != nil {
		return CompanionAllowResult{}, err
	}
	store, err := companions.NewAllowStore(fs)
	if err != nil {
		return CompanionAllowResult{}, err
	}
	recs, err := store.List()
	if err != nil {
		return CompanionAllowResult{}, err
	}
	res := CompanionAllowResult{Key: key}
	for _, r := range recs {
		if r.Approved && r.Key.Path == key.Path && r.Key.SHA256 != key.SHA256 {
			prev := r.Key
			res.Previous = &prev
		}
	}
	if !req.Apply {
		return res, nil
	}
	if _, err := store.Set(key, true); err != nil {
		return CompanionAllowResult{}, err
	}
	res.Applied = true
	return res, nil
}

// CompanionForgetResult is what a forget removed, or would remove.
type CompanionForgetResult struct {
	Target  string                    `json:"target"`
	Records []companions.CompanionKey `json:"records"`
	Applied bool                      `json:"applied"`
}

// ForgetCompanion drops the allow records pathOrName matches: by resolved
// path when it is a path, by the recorded name when it is a bare name — so a
// binary that is already gone can still be forgotten. Without apply it writes
// nothing. Matching no record is ErrCompanionNotAllowed.
func ForgetCompanion(ctx context.Context, fs afero.Fs, pathOrName string, apply bool) (CompanionForgetResult, error) {
	if err := ctx.Err(); err != nil {
		return CompanionForgetResult{}, err
	}
	store, err := companions.NewAllowStore(fs)
	if err != nil {
		return CompanionForgetResult{}, err
	}
	recs, err := store.List()
	if err != nil {
		return CompanionForgetResult{}, err
	}
	match := forgetMatcher(pathOrName)
	res := CompanionForgetResult{Target: pathOrName}
	for _, r := range recs {
		if match(r.Key) {
			res.Records = append(res.Records, r.Key)
		}
	}
	if len(res.Records) == 0 {
		return CompanionForgetResult{}, fmt.Errorf("%w: no allow record for %s", ErrCompanionNotAllowed, pathOrName)
	}
	if !apply {
		return res, nil
	}
	for _, k := range res.Records {
		if _, err := store.Forget(k); err != nil {
			return CompanionForgetResult{}, err
		}
	}
	res.Applied = true
	return res, nil
}

// forgetMatcher selects records by path (made absolute, symlinks followed
// where the file still exists) or, for a bare name, by the recorded name.
func forgetMatcher(pathOrName string) func(companions.CompanionKey) bool {
	if !strings.ContainsRune(pathOrName, filepath.Separator) && !strings.ContainsRune(pathOrName, '/') {
		return func(k companions.CompanionKey) bool { return k.Bin == pathOrName }
	}
	want, err := filepath.Abs(pathOrName)
	if err != nil {
		want = filepath.Clean(pathOrName)
	}
	if resolved, rerr := filepath.EvalSymlinks(want); rerr == nil {
		want = resolved
	}
	return func(k companions.CompanionKey) bool { return k.Path == want }
}
