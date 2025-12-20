package storage

import (
	"context"
	"fmt"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	fragmentsCollection = "fragments"
)

// FirestoreStore implements Store using Google Cloud Firestore.
type FirestoreStore struct {
	client     *firestore.Client
	collection string
}

// FirestoreConfig contains configuration for Firestore connection.
type FirestoreConfig struct {
	ProjectID  string
	Collection string // Optional: defaults to "fragments"
}

// NewFirestoreStore creates a new Firestore-backed store.
func NewFirestoreStore(ctx context.Context, cfg FirestoreConfig) (*FirestoreStore, error) {
	client, err := firestore.NewClient(ctx, cfg.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("failed to create firestore client: %w", err)
	}

	collection := cfg.Collection
	if collection == "" {
		collection = fragmentsCollection
	}

	return &FirestoreStore{
		client:     client,
		collection: collection,
	}, nil
}

// firestoreFragment is the Firestore document structure.
type firestoreFragment struct {
	Name      string    `firestore:"name"`
	Version   string    `firestore:"version"`
	Author    string    `firestore:"author"`
	Tags      []string  `firestore:"tags"`
	Variables []string  `firestore:"variables"`
	Content   string    `firestore:"content"`
	CreatedAt time.Time `firestore:"created_at"`
	UpdatedAt time.Time `firestore:"updated_at"`
	Downloads int64     `firestore:"downloads"`
	Likes     int64     `firestore:"likes"`
	Dislikes  int64     `firestore:"dislikes"`
}

// firestoreDislike is the Firestore document structure for dislikes.
type firestoreDislike struct {
	FragmentID string    `firestore:"fragment_id"`
	Reason     string    `firestore:"reason"`
	CreatedAt  time.Time `firestore:"created_at"`
}

func (s *FirestoreStore) col() *firestore.CollectionRef {
	return s.client.Collection(s.collection)
}

// Create stores a new fragment.
func (s *FirestoreStore) Create(ctx context.Context, frag *Fragment) (*Fragment, error) {
	now := time.Now().UTC()

	doc := firestoreFragment{
		Name:      frag.Name,
		Version:   frag.Version,
		Author:    frag.Author,
		Tags:      frag.Tags,
		Variables: frag.Variables,
		Content:   frag.Content,
		CreatedAt: now,
		UpdatedAt: now,
	}

	ref, _, err := s.col().Add(ctx, doc)
	if err != nil {
		return nil, fmt.Errorf("failed to create fragment: %w", err)
	}

	return &Fragment{
		ID:        ref.ID,
		Name:      frag.Name,
		Version:   frag.Version,
		Author:    frag.Author,
		Tags:      frag.Tags,
		Variables: frag.Variables,
		Content:   frag.Content,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// Get retrieves a fragment by ID.
func (s *FirestoreStore) Get(ctx context.Context, id string) (*Fragment, error) {
	if id == "" {
		return nil, ErrInvalidID
	}

	doc, err := s.col().Doc(id).Get(ctx)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to get fragment: %w", err)
	}

	return docToFragment(doc)
}

// GetByName retrieves a fragment by author, name, and optional version.
func (s *FirestoreStore) GetByName(ctx context.Context, author, name, version string) (*Fragment, error) {
	query := s.col().Where("author", "==", author).Where("name", "==", name)

	if version != "" {
		query = query.Where("version", "==", version)
	} else {
		// Get latest by updated_at
		query = query.OrderBy("updated_at", firestore.Desc).Limit(1)
	}

	iter := query.Documents(ctx)
	doc, err := iter.Next()
	if err == iterator.Done {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get fragment by name: %w", err)
	}

	return docToFragment(doc)
}

// Update updates an existing fragment.
func (s *FirestoreStore) Update(ctx context.Context, frag *Fragment) (*Fragment, error) {
	if frag.ID == "" {
		return nil, ErrInvalidID
	}

	now := time.Now().UTC()

	updates := []firestore.Update{
		{Path: "name", Value: frag.Name},
		{Path: "version", Value: frag.Version},
		{Path: "author", Value: frag.Author},
		{Path: "tags", Value: frag.Tags},
		{Path: "variables", Value: frag.Variables},
		{Path: "content", Value: frag.Content},
		{Path: "updated_at", Value: now},
	}

	_, err := s.col().Doc(frag.ID).Update(ctx, updates)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to update fragment: %w", err)
	}

	frag.UpdatedAt = now
	return frag, nil
}

// Delete removes a fragment by ID.
func (s *FirestoreStore) Delete(ctx context.Context, id string) error {
	if id == "" {
		return ErrInvalidID
	}

	_, err := s.col().Doc(id).Delete(ctx)
	if err != nil {
		return fmt.Errorf("failed to delete fragment: %w", err)
	}

	return nil
}

// List returns fragments matching the given options.
func (s *FirestoreStore) List(ctx context.Context, opts ListOptions) (*ListResult, error) {
	query := s.col().Query

	// Apply filters
	if len(opts.Tags) > 0 {
		// Firestore array-contains-any for OR logic on tags
		query = query.Where("tags", "array-contains-any", opts.Tags)
	}

	if opts.Author != "" {
		query = query.Where("author", "==", opts.Author)
	}

	if opts.NamePrefix != "" {
		// Firestore range query for prefix matching
		query = query.Where("name", ">=", opts.NamePrefix).
			Where("name", "<", opts.NamePrefix+"\uf8ff")
	}

	// Order by name for consistent pagination
	query = query.OrderBy("name", firestore.Asc)

	// Apply pagination
	pageSize := opts.PageSize
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 100 {
		pageSize = 100
	}
	query = query.Limit(pageSize + 1) // Fetch one extra to detect next page

	if opts.PageToken != "" {
		// PageToken is the last document ID
		startDoc, err := s.col().Doc(opts.PageToken).Get(ctx)
		if err == nil {
			query = query.StartAfter(startDoc)
		}
	}

	iter := query.Documents(ctx)
	var fragments []*Fragment
	var lastID string

	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to list fragments: %w", err)
		}

		frag, err := docToFragment(doc)
		if err != nil {
			continue
		}

		fragments = append(fragments, frag)
		lastID = frag.ID

		if len(fragments) > pageSize {
			break
		}
	}

	result := &ListResult{Fragments: fragments}

	// If we got more than pageSize, there's a next page
	if len(fragments) > pageSize {
		result.Fragments = fragments[:pageSize]
		result.NextPageToken = lastID
	}

	return result, nil
}

// Search performs full-text search on fragment content.
// Note: Firestore doesn't have native full-text search.
// This implementation uses simple substring matching.
// For production, consider using Algolia, Elasticsearch, or Cloud Search.
func (s *FirestoreStore) Search(ctx context.Context, opts SearchOptions) (*ListResult, error) {
	// Start with base query
	query := s.col().Query

	// Apply tag filter if specified
	if len(opts.Tags) > 0 {
		query = query.Where("tags", "array-contains-any", opts.Tags)
	}

	// Fetch documents and filter in memory for content search
	iter := query.Documents(ctx)
	var fragments []*Fragment

	queryLower := strings.ToLower(opts.Query)
	pageSize := opts.PageSize
	if pageSize <= 0 {
		pageSize = 50
	}

	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to search fragments: %w", err)
		}

		frag, err := docToFragment(doc)
		if err != nil {
			continue
		}

		// Simple content matching
		if strings.Contains(strings.ToLower(frag.Content), queryLower) ||
			strings.Contains(strings.ToLower(frag.Name), queryLower) {
			fragments = append(fragments, frag)
		}

		if len(fragments) >= pageSize {
			break
		}
	}

	return &ListResult{Fragments: fragments}, nil
}

// Close closes the Firestore client.
func (s *FirestoreStore) Close() error {
	return s.client.Close()
}

func docToFragment(doc *firestore.DocumentSnapshot) (*Fragment, error) {
	var f firestoreFragment
	if err := doc.DataTo(&f); err != nil {
		return nil, err
	}

	return &Fragment{
		ID:        doc.Ref.ID,
		Name:      f.Name,
		Version:   f.Version,
		Author:    f.Author,
		Tags:      f.Tags,
		Variables: f.Variables,
		Content:   f.Content,
		CreatedAt: f.CreatedAt,
		UpdatedAt: f.UpdatedAt,
		Downloads: f.Downloads,
		Likes:     f.Likes,
		Dislikes:  f.Dislikes,
	}, nil
}

// IncrementDownloads increments the download count atomically.
func (s *FirestoreStore) IncrementDownloads(ctx context.Context, id string) (*Fragment, error) {
	if id == "" {
		return nil, ErrInvalidID
	}

	ref := s.col().Doc(id)
	_, err := ref.Update(ctx, []firestore.Update{
		{Path: "downloads", Value: firestore.Increment(1)},
	})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to increment downloads: %w", err)
	}

	return s.Get(ctx, id)
}

// IncrementLikes increments the like count atomically.
func (s *FirestoreStore) IncrementLikes(ctx context.Context, id string) (*Fragment, error) {
	if id == "" {
		return nil, ErrInvalidID
	}

	ref := s.col().Doc(id)
	_, err := ref.Update(ctx, []firestore.Update{
		{Path: "likes", Value: firestore.Increment(1)},
	})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to increment likes: %w", err)
	}

	return s.Get(ctx, id)
}

// AddDislike adds a dislike with reason and increments the dislike count.
func (s *FirestoreStore) AddDislike(ctx context.Context, id string, reason string) (*Fragment, error) {
	if id == "" {
		return nil, ErrInvalidID
	}

	// Add dislike record to subcollection
	dislike := firestoreDislike{
		FragmentID: id,
		Reason:     reason,
		CreatedAt:  time.Now().UTC(),
	}

	_, _, err := s.col().Doc(id).Collection("dislikes").Add(ctx, dislike)
	if err != nil {
		return nil, fmt.Errorf("failed to add dislike: %w", err)
	}

	// Increment dislike count
	ref := s.col().Doc(id)
	_, err = ref.Update(ctx, []firestore.Update{
		{Path: "dislikes", Value: firestore.Increment(1)},
	})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to increment dislikes: %w", err)
	}

	return s.Get(ctx, id)
}

// ListDislikes returns all dislikes for a fragment.
func (s *FirestoreStore) ListDislikes(ctx context.Context, fragmentID string) ([]*Dislike, error) {
	if fragmentID == "" {
		return nil, ErrInvalidID
	}

	iter := s.col().Doc(fragmentID).Collection("dislikes").
		OrderBy("created_at", firestore.Desc).
		Documents(ctx)

	var dislikes []*Dislike
	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to list dislikes: %w", err)
		}

		var d firestoreDislike
		if err := doc.DataTo(&d); err != nil {
			continue
		}

		dislikes = append(dislikes, &Dislike{
			FragmentID: d.FragmentID,
			Reason:     d.Reason,
			CreatedAt:  d.CreatedAt,
		})
	}

	return dislikes, nil
}

// === Persona Operations ===
// TODO: Implement Firestore persona operations

// CreatePersona creates a new persona.
func (s *FirestoreStore) CreatePersona(ctx context.Context, persona *Persona) (*Persona, error) {
	return nil, fmt.Errorf("persona operations not yet implemented for Firestore")
}

// GetPersona retrieves a persona by ID.
func (s *FirestoreStore) GetPersona(ctx context.Context, id string) (*Persona, error) {
	return nil, fmt.Errorf("persona operations not yet implemented for Firestore")
}

// GetPersonaByName retrieves a persona by author, name, and optional version.
func (s *FirestoreStore) GetPersonaByName(ctx context.Context, author, name, version string) (*Persona, error) {
	return nil, fmt.Errorf("persona operations not yet implemented for Firestore")
}

// UpdatePersona updates an existing persona.
func (s *FirestoreStore) UpdatePersona(ctx context.Context, persona *Persona) (*Persona, error) {
	return nil, fmt.Errorf("persona operations not yet implemented for Firestore")
}

// DeletePersona removes a persona by ID.
func (s *FirestoreStore) DeletePersona(ctx context.Context, id string) error {
	return fmt.Errorf("persona operations not yet implemented for Firestore")
}

// ListPersonas returns personas matching the given options.
func (s *FirestoreStore) ListPersonas(ctx context.Context, opts PersonaListOptions) (*PersonaListResult, error) {
	return nil, fmt.Errorf("persona operations not yet implemented for Firestore")
}

// IncrementPersonaDownloads increments the download count.
func (s *FirestoreStore) IncrementPersonaDownloads(ctx context.Context, id string) (*Persona, error) {
	return nil, fmt.Errorf("persona operations not yet implemented for Firestore")
}

// IncrementPersonaLikes increments the like count.
func (s *FirestoreStore) IncrementPersonaLikes(ctx context.Context, id string) (*Persona, error) {
	return nil, fmt.Errorf("persona operations not yet implemented for Firestore")
}

// AddPersonaDislike adds a dislike with reason.
func (s *FirestoreStore) AddPersonaDislike(ctx context.Context, id string, reason string) (*Persona, error) {
	return nil, fmt.Errorf("persona operations not yet implemented for Firestore")
}
