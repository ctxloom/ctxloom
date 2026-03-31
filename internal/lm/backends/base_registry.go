package backends

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/filelock"
)

// BaseSessionRegistry provides shared session registry logic for backends.
// It tracks sessions across /clear by associating transcript paths with ctxloom wrapper PIDs.
type BaseSessionRegistry struct {
	fs           afero.Fs
	registryFile string // e.g., "claude-session-registry.json"
	maxAge       time.Duration
	maxEntries   int
}

// RegistryOption configures a BaseSessionRegistry.
type RegistryOption func(*BaseSessionRegistry)

// WithRegistryFS sets a custom filesystem for the registry.
func WithRegistryFS(fs afero.Fs) RegistryOption {
	return func(r *BaseSessionRegistry) {
		r.fs = fs
	}
}

// NewBaseSessionRegistry creates a new registry with the given filename.
func NewBaseSessionRegistry(registryFile string, opts ...RegistryOption) *BaseSessionRegistry {
	r := &BaseSessionRegistry{
		fs:           afero.NewOsFs(),
		registryFile: registryFile,
		maxAge:       7 * 24 * time.Hour, // Prune entries older than 7 days
		maxEntries:   100,                // Maximum PIDs to track
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// RegistryData is the on-disk format for tracking sessions across /clear.
type RegistryData struct {
	Runs map[int]*RunInfo `json:"runs"`
}

// RunInfo contains information about an ctxloom run instance and its sessions.
type RunInfo struct {
	Started  time.Time `json:"started"`
	Sessions []string  `json:"sessions"` // Transcript file paths in chronological order
}

// RegisterSession records a session transcript path for the given ctxloom run (by PID).
// Uses file locking to prevent race conditions from concurrent hook invocations.
func (r *BaseSessionRegistry) RegisterSession(workDir string, pid int, transcriptPath string) error {
	appDir := filepath.Join(workDir, ".ctxloom")
	registryPath := filepath.Join(appDir, r.registryFile)

	// Acquire exclusive lock for read-modify-write
	unlock, err := r.lockFile(registryPath)
	if err != nil {
		return fmt.Errorf("acquire lock: %w", err)
	}
	defer unlock()

	registry, err := r.loadRegistryLocked(appDir)
	if err != nil {
		return err
	}

	run, ok := registry.Runs[pid]
	if !ok {
		run = &RunInfo{
			Started:  time.Now().UTC(),
			Sessions: []string{},
		}
		registry.Runs[pid] = run
	}

	// Check if path already registered (idempotent)
	for _, s := range run.Sessions {
		if s == transcriptPath {
			return nil // Already registered
		}
	}

	// Append new path
	run.Sessions = append(run.Sessions, transcriptPath)

	// Prune old entries before saving
	r.pruneRegistry(registry)

	return r.saveRegistryLocked(appDir, registry)
}

// GetPreviousSession returns the session before the current one for /clear recovery.
func (r *BaseSessionRegistry) GetPreviousSession(workDir string, pid int, parseFunc func(path string) (*Session, error)) (*Session, error) {
	appDir := filepath.Join(workDir, ".ctxloom")
	registryPath := filepath.Join(appDir, r.registryFile)

	// Use shared lock for reading
	unlock, err := r.lockFileShared(registryPath)
	if err != nil {
		// If we can't lock, try reading anyway (best effort)
		registry, loadErr := r.loadRegistryLocked(appDir)
		if loadErr != nil {
			return nil, loadErr
		}
		return r.getPreviousFromRegistry(registry, pid, parseFunc)
	}
	defer unlock()

	registry, err := r.loadRegistryLocked(appDir)
	if err != nil {
		return nil, err
	}

	return r.getPreviousFromRegistry(registry, pid, parseFunc)
}

func (r *BaseSessionRegistry) getPreviousFromRegistry(registry *RegistryData, pid int, parseFunc func(path string) (*Session, error)) (*Session, error) {
	run, ok := registry.Runs[pid]
	if !ok || len(run.Sessions) < 2 {
		return nil, nil // No previous session
	}

	// Get second-to-last path and load it directly
	prevPath := run.Sessions[len(run.Sessions)-2]
	return parseFunc(prevPath)
}

// loadRegistryLocked loads the session registry from disk (caller must hold lock).
func (r *BaseSessionRegistry) loadRegistryLocked(appDir string) (*RegistryData, error) {
	path := filepath.Join(appDir, r.registryFile)

	data, err := afero.ReadFile(r.fs, path)
	if err != nil {
		if os.IsNotExist(err) {
			return &RegistryData{Runs: make(map[int]*RunInfo)}, nil
		}
		return nil, fmt.Errorf("read session registry: %w", err)
	}

	var registry RegistryData
	if err := json.Unmarshal(data, &registry); err != nil {
		// Return empty registry on parse error (resilient)
		return &RegistryData{Runs: make(map[int]*RunInfo)}, nil
	}

	if registry.Runs == nil {
		registry.Runs = make(map[int]*RunInfo)
	}

	return &registry, nil
}

// saveRegistryLocked writes the registry to disk (caller must hold lock).
func (r *BaseSessionRegistry) saveRegistryLocked(appDir string, registry *RegistryData) error {
	path := filepath.Join(appDir, r.registryFile)

	// Ensure directory exists
	if err := r.fs.MkdirAll(appDir, 0755); err != nil {
		return fmt.Errorf("create ctxloom directory: %w", err)
	}

	data, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal session registry: %w", err)
	}

	// Write atomically using temp file + rename
	tmpPath := path + ".tmp"
	if err := afero.WriteFile(r.fs, tmpPath, data, 0644); err != nil {
		return fmt.Errorf("write session registry: %w", err)
	}

	if err := r.fs.Rename(tmpPath, path); err != nil {
		_ = r.fs.Remove(tmpPath) // Clean up on failure
		return fmt.Errorf("rename session registry: %w", err)
	}

	return nil
}

// pruneRegistry removes old entries to prevent unbounded growth.
func (r *BaseSessionRegistry) pruneRegistry(registry *RegistryData) {
	cutoff := time.Now().Add(-r.maxAge)

	// Remove entries older than maxAge
	for pid, run := range registry.Runs {
		if run.Started.Before(cutoff) {
			delete(registry.Runs, pid)
		}
	}

	// If still over maxEntries, remove oldest
	for len(registry.Runs) > r.maxEntries {
		var oldestPID int
		var oldestTime time.Time
		first := true

		for pid, run := range registry.Runs {
			if first || run.Started.Before(oldestTime) {
				oldestPID = pid
				oldestTime = run.Started
				first = false
			}
		}

		if !first {
			delete(registry.Runs, oldestPID)
		}
	}
}

// lockFile acquires an exclusive lock on the registry file.
// Creates a .lock file adjacent to the registry.
// For non-OsFs (e.g., MemMapFs in tests), returns a no-op unlock.
func (r *BaseSessionRegistry) lockFile(registryPath string) (func(), error) {
	// Only use locking for real filesystem - tests don't need locking
	if _, ok := r.fs.(*afero.OsFs); !ok {
		return func() {}, nil
	}

	lockPath := registryPath + ".lock"
	return filelock.Lock(lockPath)
}

// lockFileShared acquires a shared (read) lock on the registry file.
// For non-OsFs (e.g., MemMapFs in tests), returns a no-op unlock.
func (r *BaseSessionRegistry) lockFileShared(registryPath string) (func(), error) {
	// Only use locking for real filesystem - tests don't need locking
	if _, ok := r.fs.(*afero.OsFs); !ok {
		return func() {}, nil
	}

	lockPath := registryPath + ".lock"
	return filelock.LockShared(lockPath)
}
