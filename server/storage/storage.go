// Package storage provides the storage interface and implementations for fragments.
package storage

import (
	"context"
	"errors"
	"time"
)

// Common errors returned by storage implementations.
var (
	ErrNotFound         = errors.New("not found")
	ErrAlreadyExists    = errors.New("already exists")
	ErrInvalidID        = errors.New("invalid ID")
	ErrPersonaNotFound  = errors.New("persona not found")
	ErrFragmentNotFound = errors.New("fragment not found")
)

// Fragment represents a stored context fragment.
type Fragment struct {
	ID        string
	Name      string
	Version   string
	Author    string
	Tags      []string
	Variables []string
	Content   string
	CreatedAt time.Time
	UpdatedAt time.Time

	// Engagement metrics
	Downloads      int64
	Likes          int64
	Dislikes       int64
	UsedByPersonas int64 // Count of personas that reference this fragment
	// Popularity = Downloads + Likes - Dislikes + UsedByPersonas
}

// Dislike represents a dislike with its reason.
type Dislike struct {
	FragmentID string
	Reason     string
	CreatedAt  time.Time
}

// FragmentRef is a reference to a fragment by name, author, and version.
type FragmentRef struct {
	Name    string
	Author  string
	Version string
}

// Persona represents a stored persona (collection of fragment references).
type Persona struct {
	ID          string
	Name        string
	Author      string
	Version     string
	Description string
	Fragments   []FragmentRef
	CreatedAt   time.Time
	UpdatedAt   time.Time

	// Engagement metrics
	Downloads int64
	Likes     int64
	Dislikes  int64
}

// PersonaDislike represents a dislike on a persona with its reason.
type PersonaDislike struct {
	PersonaID string
	Reason    string
	CreatedAt time.Time
}

// PersonaListOptions contains options for listing personas.
type PersonaListOptions struct {
	Author     string
	NamePrefix string
	PageSize   int
	PageToken  string
	Sort       SortOrder
}

// PersonaListResult contains the result of a persona list operation.
type PersonaListResult struct {
	Personas      []*Persona
	NextPageToken string
}

// SortOrder defines how to sort results.
type SortOrder int

const (
	SortDefault    SortOrder = iota // Popularity (downloads + likes - dislikes)
	SortPopularity                  // Same as default
	SortDownloads                   // By download count
	SortRecent                      // By updated_at
	SortName                        // Alphabetical
)

// ListOptions contains options for listing fragments.
type ListOptions struct {
	Tags       []string  // Filter by tags (OR logic)
	Author     string    // Filter by author
	NamePrefix string    // Filter by name prefix
	PageSize   int       // Max results per page
	PageToken  string    // Pagination token
	Sort       SortOrder // Sort order (default: popularity)
}

// ListResult contains the result of a list operation.
type ListResult struct {
	Fragments     []*Fragment
	NextPageToken string
}

// SearchOptions contains options for searching fragments.
type SearchOptions struct {
	Query     string    // Full-text search query
	Tags      []string  // Optional tag filter
	PageSize  int
	PageToken string
	Sort      SortOrder // Sort order (default: popularity)
}

// Store defines the interface for fragment storage backends.
type Store interface {
	// === Fragment Operations ===

	// Create stores a new fragment and returns it with generated ID.
	Create(ctx context.Context, frag *Fragment) (*Fragment, error)

	// Get retrieves a fragment by ID.
	Get(ctx context.Context, id string) (*Fragment, error)

	// GetByName retrieves a fragment by author, name, and optional version.
	// If version is empty, returns the latest version.
	GetByName(ctx context.Context, author, name, version string) (*Fragment, error)

	// Update updates an existing fragment.
	Update(ctx context.Context, frag *Fragment) (*Fragment, error)

	// Delete removes a fragment by ID.
	Delete(ctx context.Context, id string) error

	// List returns fragments matching the given options.
	List(ctx context.Context, opts ListOptions) (*ListResult, error)

	// Search performs full-text search on fragment content.
	Search(ctx context.Context, opts SearchOptions) (*ListResult, error)

	// IncrementDownloads increments the download count and returns the updated fragment.
	IncrementDownloads(ctx context.Context, id string) (*Fragment, error)

	// IncrementLikes increments the like count and returns the updated fragment.
	IncrementLikes(ctx context.Context, id string) (*Fragment, error)

	// AddDislike adds a dislike with reason and returns the updated fragment.
	AddDislike(ctx context.Context, id string, reason string) (*Fragment, error)

	// ListDislikes returns all dislikes for a fragment.
	ListDislikes(ctx context.Context, fragmentID string) ([]*Dislike, error)

	// === Persona Operations ===

	// CreatePersona stores a new persona and returns it with generated ID.
	CreatePersona(ctx context.Context, persona *Persona) (*Persona, error)

	// GetPersona retrieves a persona by ID.
	GetPersona(ctx context.Context, id string) (*Persona, error)

	// GetPersonaByName retrieves a persona by author, name, and optional version.
	// If version is empty, returns the latest version.
	GetPersonaByName(ctx context.Context, author, name, version string) (*Persona, error)

	// UpdatePersona updates an existing persona.
	UpdatePersona(ctx context.Context, persona *Persona) (*Persona, error)

	// DeletePersona removes a persona by ID.
	DeletePersona(ctx context.Context, id string) error

	// ListPersonas returns personas matching the given options.
	ListPersonas(ctx context.Context, opts PersonaListOptions) (*PersonaListResult, error)

	// IncrementPersonaDownloads increments the download count and returns the updated persona.
	IncrementPersonaDownloads(ctx context.Context, id string) (*Persona, error)

	// IncrementPersonaLikes increments the like count and returns the updated persona.
	IncrementPersonaLikes(ctx context.Context, id string) (*Persona, error)

	// AddPersonaDislike adds a dislike with reason and returns the updated persona.
	AddPersonaDislike(ctx context.Context, id string, reason string) (*Persona, error)

	// Close closes the storage connection.
	Close() error
}
