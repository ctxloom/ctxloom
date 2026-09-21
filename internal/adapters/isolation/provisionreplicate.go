package isolation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/sessions"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/internal/shared/watch"
)

// Replication: shared BY REPLICATION, for material that cannot be mounted.
//
// Two files kept in step by a directory watcher and a write-back, under a
// cross-process lock. It has the rotation window provisioner.go describes,
// and it is the ONE delivery that can serve a projected material: the
// instance holds Material.Project of the host's bytes, re-applied on every
// host change, and never writes back (a projected material is read-only by
// construction — validateMaterial).
//
// THREE CHOICES HERE ARE LOAD-BEARING AND EACH ONE IS AN INODE-VERSUS-NAME
// TRAP:
//
//  1. The lock is a SEPARATE FILE, never the credential. A credential is
//     replaced BY RENAME on refresh, so a lock taken on its inode is orphaned
//     the instant a refresh lands — every holder would then be locking a file
//     nothing reads. agent.WithFileLock already does exactly this, against a
//     sidecar under ~/.ctxloom rather than beside a file ctxloom does not own.
//  2. The lock is CROSS-PROCESS (flock), never a mutex. Several ctxloom
//     processes run concurrently; an in-process lock would protect nothing at
//     all and would look like it protected something.
//  3. The watch is on the DIRECTORY, filtered by name — same trap. A watch
//     registered against the file's inode stops hearing about the name the
//     moment a rename replaces it, which is precisely the event worth hearing.
//
// Loop suppression comes free from hashing before writing: a write this
// replicator just made hashes equal to what it recorded, so it does not bounce
// back across.

const replicationMechanism = "replication"

// replicationDebounce coalesces the several filesystem events one logical
// write produces. Short, because the cost of a late propagation is a rejected
// refresh on another instance.
const replicationDebounce = 150 * time.Millisecond

// replicationProvisioner places material and then keeps it in step.
type replicationProvisioner struct{}

// newReplicationProvisioner probes by standing up a real watcher and tearing
// it down. That is not ceremony: fsnotify fails for reasons that have nothing
// to do with the platform — an exhausted inotify instance limit is the common
// one — and discovering that at first use means discovering it with a live
// engine already running on material nothing is replicating.
func newReplicationProvisioner(ctx context.Context, cfg *provisionConfig) (Provisioner, error) {
	if err := cfg.replicationProbe(ctx, cfg.scratch); err != nil {
		return nil, err
	}
	return &replicationProvisioner{}, nil
}

// defaultReplicationProbe is the real capability check. Select replaces it
// only for a negative control — see provisionConfig.replicationProbe.
func defaultReplicationProbe(_ context.Context, scratch string) error {
	if scratch == "" {
		return errors.New("no scratch directory to probe a filesystem watcher in")
	}
	w, err := watch.New(scratch, false, func(string) bool { return false })
	if err != nil {
		return fmt.Errorf("this host cannot start a filesystem watcher, which replication depends on: %w", err)
	}
	return w.Close()
}

// Mechanism names this implementation.
func (p *replicationProvisioner) Mechanism() string { return replicationMechanism }

// Delivery reports shared-by-replication, NOT shared-by-identity. The
// difference is the rotation window, and a consumer debugging a rejected
// refresh needs to be able to see which one it got.
func (p *replicationProvisioner) Delivery() Delivery { return DeliveryReplicated }

// Can reports that replication delivers shared material and nothing else. A
// replicated file is by definition not private: its writes reach the host.
func (p *replicationProvisioner) Can(s Sharing) bool { return s == SharingShared }

// Provision places each material and starts the replicator that keeps it in
// step. The returned Result's Close stops every watcher and waits for the
// goroutines to finish — unlike both mount implementations, which leave
// nothing running and so have nothing to leak.
func (p *replicationProvisioner) Provision(instanceHome string, materials []Material) (Result, error) {
	if instanceHome == "" {
		return Result{}, errors.New("replication provisioning: no instance home to place material in")
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &replicator{cancel: cancel}
	for _, m := range materials {
		if err := validateMaterial(m, SharingShared); err != nil {
			cancel()
			_ = r.Close()
			return Result{}, err
		}
		pair := &replicaPair{
			host:     m.Host,
			instance: filepath.Join(instanceHome, filepath.FromSlash(m.DestRel)),
			readOnly: m.ReadOnly,
			project:  m.Project,
		}
		if err := r.start(ctx, pair); err != nil {
			cancel()
			_ = r.Close()
			return Result{}, err
		}
	}
	return Result{
		Delivery:  DeliveryReplicated,
		Mechanism: replicationMechanism,
		stop:      r.Close,
	}, nil
}

// replicaPair is one host file and its instance-side twin, plus the last
// content each side was seen holding — which is how a write this replicator
// itself made is told apart from one the engine made. lastHost hashes the
// host's RAW bytes and lastInst the instance's (projected) bytes: under a
// projection the two legitimately differ, so each side is compared only
// with what it was last seen holding.
type replicaPair struct {
	host     string
	instance string
	readOnly bool
	project  func(host []byte) ([]byte, error)

	mu       sync.Mutex
	lastHost [sha256.Size]byte
	lastInst [sha256.Size]byte
}

// view is what the instance holds for host bytes: the projection when one
// is declared, the bytes themselves otherwise.
func (p *replicaPair) view(host []byte) ([]byte, error) {
	if p.project == nil {
		return host, nil
	}
	out, err := p.project(host)
	if err != nil {
		return nil, fmt.Errorf("replication: project %s for the instance: %w", p.host, err)
	}
	return out, nil
}

// replicator owns every watcher started for one Provision call.
type replicator struct {
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	closeOnce sync.Once
	closeErr  error

	mu       sync.Mutex
	watchers []*watch.Watcher
}

// start places the pair's initial content and begins watching both sides.
//
// The initial placement is BOOTSTRAP, not a copy mode: it is how the instance
// gets a file at all, after which the two are kept in step — the instance
// holding the host's view (projected when the material declares it) after
// every host change.
func (r *replicator) start(ctx context.Context, pair *replicaPair) error {
	if err := os.MkdirAll(filepath.Dir(pair.instance), 0o700); err != nil {
		return fmt.Errorf("replication provisioning: prepare %s: %w", filepath.Dir(pair.instance), err)
	}
	if err := pair.bootstrap(); err != nil {
		return err
	}
	for _, side := range []struct {
		dir  string
		file string
	}{
		{filepath.Dir(pair.host), pair.host},
		{filepath.Dir(pair.instance), pair.instance},
	} {
		// The DIRECTORY, filtered by name — see this file's header on why a
		// watch bound to the file's inode goes deaf at the first rename.
		w, err := watch.New(side.dir, false, func(p string) bool { return p == side.file })
		if err != nil {
			return fmt.Errorf("replication provisioning: watch %s: %w", side.dir, err)
		}
		r.mu.Lock()
		r.watchers = append(r.watchers, w)
		r.mu.Unlock()
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			err := watch.Stream(ctx, w, replicationDebounce, func() error {
				if err := pair.reconcile(); err != nil {
					// A failed reconcile must not stop the stream: the next
					// change is still worth trying, and a replicator that
					// exits on one transient error stops silently.
					clidiag.Warn("ctxloom", "credential replication: %v", err)
				}
				return nil
			})
			if err != nil && ctx.Err() == nil {
				clidiag.Warn("ctxloom",
					"credential replication for %s stopped and is no longer propagating refreshes: %v", pair.host, err)
			}
		}()
	}
	return nil
}

// Close stops every watcher and waits for its goroutine. Idempotent.
func (r *replicator) Close() error {
	r.closeOnce.Do(func() {
		if r.cancel != nil {
			r.cancel()
		}
		r.mu.Lock()
		watchers := r.watchers
		r.mu.Unlock()
		for _, w := range watchers {
			if err := w.Close(); err != nil && r.closeErr == nil {
				r.closeErr = err
			}
		}
		r.wg.Wait()
	})
	return r.closeErr
}

// bootstrap gives the instance the host material, under the lock so it
// cannot read a refresh half-written by another process.
//
// An instance already holding the host's bytes is left alone. The instance
// is shared by every run of one session, so this bootstrap can land on a
// file another run's engine is reading, and an in-place rewrite of identical
// bytes is a truncate-and-refill window under that reader for nothing. A
// stale instance — a resumed run's copy of a token the host has since
// rotated — differs and is replaced.
func (p *replicaPair) bootstrap() error {
	return p.locked(func() error {
		data, err := os.ReadFile(p.host)
		if err != nil {
			return fmt.Errorf("replication provisioning: read %s: %w", p.host, err)
		}
		want, err := p.view(data)
		if err != nil {
			return err
		}
		if !p.instanceHolds(want) {
			if err := p.write(p.instance, want); err != nil {
				return err
			}
		}
		p.lastHost = sha256.Sum256(data)
		p.lastInst = sha256.Sum256(want)
		return nil
	})
}

// instanceHolds reports whether the instance is a REGULAR file already
// holding data. Decided by Lstat first, so a symlinked instance path is never
// "already identical": it is handed to the write, whose open refuses it at
// the syscall — the same refusal the engine's own open would make, surfaced
// at placement rather than as an engine that starts logged out.
func (p *replicaPair) instanceHolds(data []byte) bool {
	info, err := os.Lstat(p.instance)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	have, err := os.ReadFile(p.instance)
	return err == nil && bytes.Equal(have, data)
}

// reconcile propagates whichever side changed, under the lock.
func (p *replicaPair) reconcile() error {
	return p.locked(func() error {
		hostData, hostErr := os.ReadFile(p.host)
		instData, instErr := os.ReadFile(p.instance)
		if hostErr != nil && instErr != nil {
			return fmt.Errorf("neither side of %s is readable (%v; %v)", p.host, hostErr, instErr)
		}
		var want []byte
		if hostErr == nil {
			if want, err = p.view(hostData); err != nil {
				return err
			}
		}
		if hostErr == nil && instErr == nil && bytes.Equal(want, instData) {
			// Identical: nothing to do, and recording the hashes here is what
			// stops a write this replicator just made from bouncing back.
			p.lastHost = sha256.Sum256(hostData)
			p.lastInst = sha256.Sum256(instData)
			return nil
		}
		hostHash := hashOrZero(hostData, hostErr)
		instHash := hashOrZero(instData, instErr)
		hostChanged := hostErr == nil && hostHash != p.lastHost
		instChanged := instErr == nil && instHash != p.lastInst

		switch {
		case instChanged && hostChanged:
			// BOTH sides changed since the last reconcile — the rotation
			// window, arrived. One of these tokens is already dead, and no
			// local decision can revive it. The host wins because it is what
			// every OTHER instance also reads, so preferring it loses one
			// instance rather than all of them, and it is said out loud
			// because a silently discarded refresh is indistinguishable from
			// a broken replicator.
			clidiag.Warn("ctxloom",
				"credential replication: %s and its instance copy both changed since the last sync; keeping the host's version and discarding the instance's. "+
					"This is the rotation window inherent to replicated sharing: a mounted delivery has no such window.", p.host)
			return p.toInstance(hostData, want)
		case instChanged:
			if p.readOnly {
				// Read-only material: the instance is not permitted to write
				// back, so the host's version is restored over it rather than
				// propagated from it.
				return p.toInstance(hostData, want)
			}
			return p.toHost(instData)
		case hostChanged:
			return p.toInstance(hostData, want)
		default:
			// The two differ but neither matches a change we can attribute —
			// a first reconcile after a restart, say. The host is the source
			// of truth for the same reason as the conflict case.
			if hostErr != nil {
				return fmt.Errorf("host material %s is unreadable: %w", p.host, hostErr)
			}
			return p.toInstance(hostData, want)
		}
	})
}

// toInstance writes the host's view into the instance and records both
// sides as now holding what they hold — the host its raw bytes, the
// instance the view — so the write it just made is not read back as a change
// on the next event. That recording is the whole of the loop suppression:
// without it each sync would fire the other side's watcher, which would
// sync back, forever.
func (p *replicaPair) toInstance(host, want []byte) error {
	if err := p.write(p.instance, want); err != nil {
		return err
	}
	p.lastHost, p.lastInst = sha256.Sum256(host), sha256.Sum256(want)
	return nil
}

// toHost writes the instance's bytes over the host, for an unprojected,
// writable material only (a projected one is read-only by construction and
// never reaches here). Both sides then hold the same bytes.
func (p *replicaPair) toHost(inst []byte) error {
	if err := p.write(p.host, inst); err != nil {
		return err
	}
	sum := sha256.Sum256(inst)
	p.lastHost, p.lastInst = sum, sum
	return nil
}

// write replaces dst's contents IN PLACE rather than by rename.
//
// Renaming is wrong on both sides here, for two different reasons. The host
// file may itself be a bind-mount target for some other run, where a rename
// returns EBUSY. And a rename swaps the inode under the directory watcher's
// nose on every single sync, turning the steady state into a stream of
// create-and-replace events. In-place writing is the arm that reaches through
// a mount and leaves the inode alone. The open refuses a symlinked
// destination at the syscall, which is the defense that must survive anywhere
// credential bytes are placed.
func (p *replicaPair) write(dst string, data []byte) error {
	if err := iox.WriteFileInPlace(dst, iox.TruncateInPlace, data, 0o600); err != nil {
		return fmt.Errorf("replication: write %s: %w", dst, err)
	}
	return nil
}

// locked runs fn holding the CROSS-PROCESS lock for this pair's host file. The
// in-process mutex is held too, and is not a substitute for it: the flock
// excludes other ctxloom processes, the mutex excludes this pair's own two
// watcher goroutines.
func (p *replicaPair) locked(fn func() error) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return sessions.WithFileLock(afero.NewOsFs(), p.host, fn)
}

// hashOrZero hashes data, or reports the zero hash for a side that could not
// be read — which can never equal a recorded sha256 of real content, so an
// unreadable side is never mistaken for an unchanged one.
func hashOrZero(data []byte, err error) [sha256.Size]byte {
	if err != nil {
		return [sha256.Size]byte{}
	}
	return sha256.Sum256(data)
}
