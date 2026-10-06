package fsstatic

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/afero"
	yaml "gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// The claimant index answers Targets and Writers without decoding a single
// claims record: the store holds a record for every file ctxloom ever
// delivered, in every project, and one delivery needs only its own writer's.
//
// It is one small marker per writer and file a record names, written by the
// file's seal under the file's lock: a writer's marker BEFORE the record
// names it, removed only AFTER the record no longer does. The markers are
// therefore always a SUPERSET of the records' writers — a seal that did not
// finish leaves a marker too many, never one too few — and an extra marker
// costs a release that changes nothing, which also removes it.
const (
	claimantsDir = "claimants"
	// indexedName marks the index complete: every record written before the
	// index existed has its markers. A store without it is indexed from its
	// records once (indexRecords).
	indexedName = ".indexed"
	// targetDigestLen is the hex digest of the target that ends a marker's
	// name, after the writer's flat name and paths' separator.
	targetDigestLen = 16
	markerSep       = "__"
)

// claimant is one marker's content.
type claimant struct {
	Writer string `yaml:"writer"`
	Target string `yaml:"target"`
}

func (c *Records) claimantsPath() string { return filepath.Join(c.dir, claimantsDir) }

// writerPrefix starts the name of every marker of w. paths.FlatName ends in
// a digest of the whole writer, so no other writer's markers share it.
func writerPrefix(w string) string { return paths.FlatName(w) + markerSep }

func (c *Records) markerPath(w, target string) string {
	sum := sha256.Sum256([]byte(target))
	return filepath.Join(c.claimantsPath(), writerPrefix(w)+hex.EncodeToString(sum[:])[:targetDigestLen])
}

// mark records that w may claim in target; a marker already there stays.
func (c *Records) mark(w, target string) error {
	path := c.markerPath(w, target)
	if ok, err := afero.Exists(c.fs, path); err != nil || ok {
		return err
	}
	data, err := yaml.Marshal(claimant{Writer: w, Target: target})
	if err != nil {
		return err
	}
	existed, err := afero.DirExists(c.fs, c.dir)
	if err != nil {
		return err
	}
	if err := c.fs.MkdirAll(c.claimantsPath(), safefs.PrivateDirMode); err != nil {
		return err
	}
	if !existed {
		// The store is created here, so no record predates the index.
		if err := c.markIndexed(); err != nil {
			return err
		}
	}
	return safefs.WriteFile(c.fs, path, data, safefs.PrivateFileMode, safefs.Durable())
}

func (c *Records) markIndexed() error {
	return safefs.WriteFile(c.fs, filepath.Join(c.claimantsPath(), indexedName), nil, safefs.PrivateFileMode, safefs.Durable())
}

func (c *Records) unmark(w, target string) error {
	if err := c.fs.Remove(c.markerPath(w, target)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("fsstatic: remove the claimant marker of %s in %s: %w", w, target, err)
	}
	return nil
}

// writers is every writer rec names.
func (rec *claimsRecord) writers() []string {
	var out []string
	for _, entries := range rec.Paths {
		for _, e := range entries {
			if !slices.Contains(out, e.Writer) {
				out = append(out, e.Writer)
			}
		}
	}
	return out
}

// claimants reads the markers, grouped by writer prefix: those of the groups
// want admits, or with onePerWriter the first of each. The index is completed
// from the records first when it is not.
func (c *Records) claimants(want func(prefix string) bool, onePerWriter bool) (map[string][]claimant, error) {
	if err := c.ensureIndexed(); err != nil {
		return nil, err
	}
	entries, err := afero.ReadDir(c.fs, c.claimantsPath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string][]claimant{}
	for _, e := range entries {
		prefix, ok := markerPrefix(e)
		if _, seen := out[prefix]; !ok || !want(prefix) || (seen && onePerWriter) {
			continue
		}
		m, err := c.readClaimant(e.Name())
		if err != nil {
			return nil, err
		}
		out[prefix] = append(out[prefix], m)
	}
	return out, nil
}

// markerPrefix is the writer prefix of a marker's name; ok is false for
// anything in the index that is not a marker (the completion mark, a write's
// temp file).
func markerPrefix(e os.FileInfo) (string, bool) {
	name := e.Name()
	cut := len(name) - targetDigestLen
	if e.IsDir() || strings.HasPrefix(name, ".") || cut <= 0 {
		return "", false
	}
	return name[:cut], true
}

func (c *Records) readClaimant(name string) (claimant, error) {
	var m claimant
	path := filepath.Join(c.claimantsPath(), name)
	data, err := afero.ReadFile(c.fs, path)
	if err != nil {
		return m, err
	}
	if err := yaml.Unmarshal(data, &m); err != nil {
		return m, fmt.Errorf("fsstatic: claimant marker %s: %w", path, err)
	}
	return m, nil
}

// ensureIndexed indexes a store whose records predate the index: every
// writer of every record is marked, then the index is marked complete. A
// seal marks its own writers whether or not the index is complete, so a
// record written while this runs is never missed; one it read before a seal
// changed it can only leave a marker too many.
func (c *Records) ensureIndexed() error {
	if ok, err := afero.Exists(c.fs, filepath.Join(c.claimantsPath(), indexedName)); err != nil || ok {
		return err
	}
	if ok, err := afero.DirExists(c.fs, c.dir); err != nil || !ok {
		return err // no store: nothing to index, and a read creates nothing
	}
	err := c.eachRecord(func(rec claimsRecord) error {
		for _, w := range rec.writers() {
			if err := c.mark(w, rec.Target); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := c.fs.MkdirAll(c.claimantsPath(), safefs.PrivateDirMode); err != nil {
		return err
	}
	return c.markIndexed()
}

// eachRecord decodes every claims record in the store.
func (c *Records) eachRecord(visit func(claimsRecord) error) error {
	entries, err := afero.ReadDir(c.fs, c.dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), claimsSuffix) {
			continue
		}
		path := filepath.Join(c.dir, e.Name())
		data, err := afero.ReadFile(c.fs, path)
		if err != nil {
			return err
		}
		rec, err := decodeClaims(path, data)
		if err != nil {
			return err
		}
		if err := visit(rec); err != nil {
			return err
		}
	}
	return nil
}

// Targets lists the files w may claim anything in, sorted, from the claimant
// index. A file whose last write did not finish may be listed though w no
// longer claims anything there; releasing w there changes nothing.
func (c *Records) Targets(w delivery.Writer) ([]string, error) {
	prefix := writerPrefix(string(w))
	groups, err := c.claimants(func(p string) bool { return p == prefix }, false)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, m := range groups[prefix] {
		out = append(out, m.Target)
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

// Writers lists every writer that may claim anything in any file, sorted, as
// Targets does: one whose last release did not finish may be listed.
func (c *Records) Writers() ([]delivery.Writer, error) {
	groups, err := c.claimants(func(string) bool { return true }, true)
	if err != nil {
		return nil, err
	}
	out := make([]delivery.Writer, 0, len(groups))
	for _, ms := range groups {
		out = append(out, delivery.Writer(ms[0].Writer))
	}
	slices.Sort(out)
	return out, nil
}
