package storage

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const (
	defaultDatabase          = "mlcm"
	defaultCollection        = "fragments"
	defaultPersonaCollection = "personas"
)

// MongoStore implements Store using MongoDB.
type MongoStore struct {
	client     *mongo.Client
	db         *mongo.Database
	collection *mongo.Collection
	personas   *mongo.Collection
}

// MongoConfig contains configuration for MongoDB connection.
type MongoConfig struct {
	URI        string // MongoDB connection URI
	Database   string // Database name (defaults to "mlcm")
	Collection string // Collection name (defaults to "fragments")
}

// NewMongoStore creates a new MongoDB-backed store.
func NewMongoStore(ctx context.Context, cfg MongoConfig) (*MongoStore, error) {
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(cfg.URI))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to mongodb: %w", err)
	}

	// Verify connection
	if err := client.Ping(ctx, nil); err != nil {
		return nil, fmt.Errorf("failed to ping mongodb: %w", err)
	}

	database := cfg.Database
	if database == "" {
		database = defaultDatabase
	}

	collection := cfg.Collection
	if collection == "" {
		collection = defaultCollection
	}

	db := client.Database(database)
	col := db.Collection(collection)
	personasCol := db.Collection(defaultPersonaCollection)

	// Create indexes
	if err := createIndexes(ctx, col); err != nil {
		return nil, fmt.Errorf("failed to create fragment indexes: %w", err)
	}
	if err := createPersonaIndexes(ctx, personasCol); err != nil {
		return nil, fmt.Errorf("failed to create persona indexes: %w", err)
	}

	return &MongoStore{
		client:     client,
		db:         db,
		collection: col,
		personas:   personasCol,
	}, nil
}

func createIndexes(ctx context.Context, col *mongo.Collection) error {
	indexes := []mongo.IndexModel{
		{
			Keys: bson.D{{Key: "name", Value: 1}},
		},
		{
			Keys: bson.D{{Key: "author", Value: 1}},
		},
		{
			Keys: bson.D{{Key: "tags", Value: 1}},
		},
		{
			Keys: bson.D{{Key: "author", Value: 1}, {Key: "name", Value: 1}, {Key: "version", Value: 1}},
		},
		{
			Keys: bson.D{
				{Key: "name", Value: "text"},
				{Key: "content", Value: "text"},
			},
		},
	}

	_, err := col.Indexes().CreateMany(ctx, indexes)
	return err
}

func createPersonaIndexes(ctx context.Context, col *mongo.Collection) error {
	indexes := []mongo.IndexModel{
		{
			Keys: bson.D{{Key: "name", Value: 1}},
		},
		{
			Keys: bson.D{{Key: "author", Value: 1}},
		},
		{
			Keys: bson.D{{Key: "author", Value: 1}, {Key: "name", Value: 1}, {Key: "version", Value: 1}},
		},
	}

	_, err := col.Indexes().CreateMany(ctx, indexes)
	return err
}

// mongoFragment is the MongoDB document structure.
type mongoFragment struct {
	ID             primitive.ObjectID `bson:"_id,omitempty"`
	Name           string             `bson:"name"`
	Version        string             `bson:"version"`
	Author         string             `bson:"author"`
	Tags           []string           `bson:"tags"`
	Variables      []string           `bson:"variables"`
	Content        string             `bson:"content"`
	CreatedAt      time.Time          `bson:"created_at"`
	UpdatedAt      time.Time          `bson:"updated_at"`
	Downloads      int64              `bson:"downloads"`
	Likes          int64              `bson:"likes"`
	Dislikes       int64              `bson:"dislikes"`
	UsedByPersonas int64              `bson:"used_by_personas"`
}

// mongoDislike is the MongoDB document structure for dislikes.
type mongoDislike struct {
	ID         primitive.ObjectID `bson:"_id,omitempty"`
	FragmentID primitive.ObjectID `bson:"fragment_id"`
	Reason     string             `bson:"reason"`
	CreatedAt  time.Time          `bson:"created_at"`
}

// mongoFragmentRef is a reference to a fragment.
type mongoFragmentRef struct {
	Name    string `bson:"name"`
	Author  string `bson:"author"`
	Version string `bson:"version"`
}

// mongoPersona is the MongoDB document structure for personas.
type mongoPersona struct {
	ID          primitive.ObjectID `bson:"_id,omitempty"`
	Name        string             `bson:"name"`
	Author      string             `bson:"author"`
	Version     string             `bson:"version"`
	Description string             `bson:"description"`
	Fragments   []mongoFragmentRef `bson:"fragments"`
	CreatedAt   time.Time          `bson:"created_at"`
	UpdatedAt   time.Time          `bson:"updated_at"`
	Downloads   int64              `bson:"downloads"`
	Likes       int64              `bson:"likes"`
	Dislikes    int64              `bson:"dislikes"`
}

// mongoPersonaDislike is the MongoDB document structure for persona dislikes.
type mongoPersonaDislike struct {
	ID        primitive.ObjectID `bson:"_id,omitempty"`
	PersonaID primitive.ObjectID `bson:"persona_id"`
	Reason    string             `bson:"reason"`
	CreatedAt time.Time          `bson:"created_at"`
}

// Create stores a new fragment.
func (s *MongoStore) Create(ctx context.Context, frag *Fragment) (*Fragment, error) {
	now := time.Now().UTC()

	doc := mongoFragment{
		Name:      frag.Name,
		Version:   frag.Version,
		Author:    frag.Author,
		Tags:      frag.Tags,
		Variables: frag.Variables,
		Content:   frag.Content,
		CreatedAt: now,
		UpdatedAt: now,
	}

	result, err := s.collection.InsertOne(ctx, doc)
	if err != nil {
		return nil, fmt.Errorf("failed to create fragment: %w", err)
	}

	id := result.InsertedID.(primitive.ObjectID)

	return &Fragment{
		ID:        id.Hex(),
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
func (s *MongoStore) Get(ctx context.Context, id string) (*Fragment, error) {
	if id == "" {
		return nil, ErrInvalidID
	}

	objectID, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return nil, ErrInvalidID
	}

	var doc mongoFragment
	err = s.collection.FindOne(ctx, bson.M{"_id": objectID}).Decode(&doc)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to get fragment: %w", err)
	}

	return mongoToFragment(&doc), nil
}

// GetByName retrieves a fragment by name and optional version.
func (s *MongoStore) GetByName(ctx context.Context, author, name, version string) (*Fragment, error) {
	filter := bson.M{"name": name}
	if author != "" {
		filter["author"] = author
	}
	if version != "" {
		filter["version"] = version
	}

	opts := options.FindOne().SetSort(bson.D{{Key: "updated_at", Value: -1}})

	var doc mongoFragment
	err := s.collection.FindOne(ctx, filter, opts).Decode(&doc)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to get fragment by name: %w", err)
	}

	return mongoToFragment(&doc), nil
}

// Update updates an existing fragment.
func (s *MongoStore) Update(ctx context.Context, frag *Fragment) (*Fragment, error) {
	if frag.ID == "" {
		return nil, ErrInvalidID
	}

	objectID, err := primitive.ObjectIDFromHex(frag.ID)
	if err != nil {
		return nil, ErrInvalidID
	}

	now := time.Now().UTC()

	update := bson.M{
		"$set": bson.M{
			"name":       frag.Name,
			"version":    frag.Version,
			"author":     frag.Author,
			"tags":       frag.Tags,
			"variables":  frag.Variables,
			"content":    frag.Content,
			"updated_at": now,
		},
	}

	result, err := s.collection.UpdateByID(ctx, objectID, update)
	if err != nil {
		return nil, fmt.Errorf("failed to update fragment: %w", err)
	}

	if result.MatchedCount == 0 {
		return nil, ErrNotFound
	}

	frag.UpdatedAt = now
	return frag, nil
}

// Delete removes a fragment by ID.
func (s *MongoStore) Delete(ctx context.Context, id string) error {
	if id == "" {
		return ErrInvalidID
	}

	objectID, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return ErrInvalidID
	}

	result, err := s.collection.DeleteOne(ctx, bson.M{"_id": objectID})
	if err != nil {
		return fmt.Errorf("failed to delete fragment: %w", err)
	}

	if result.DeletedCount == 0 {
		return ErrNotFound
	}

	return nil
}

// List returns fragments matching the given options.
func (s *MongoStore) List(ctx context.Context, opts ListOptions) (*ListResult, error) {
	filter := bson.M{}

	// Apply filters
	if len(opts.Tags) > 0 {
		filter["tags"] = bson.M{"$in": opts.Tags}
	}

	if opts.Author != "" {
		filter["author"] = opts.Author
	}

	if opts.NamePrefix != "" {
		filter["name"] = bson.M{"$regex": "^" + opts.NamePrefix}
	}

	// Pagination
	pageSize := int64(opts.PageSize)
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 100 {
		pageSize = 100
	}

	findOpts := options.Find().
		SetSort(bson.D{{Key: "name", Value: 1}}).
		SetLimit(pageSize + 1)

	if opts.PageToken != "" {
		// PageToken is the last document ID
		objectID, err := primitive.ObjectIDFromHex(opts.PageToken)
		if err == nil {
			filter["_id"] = bson.M{"$gt": objectID}
		}
	}

	cursor, err := s.collection.Find(ctx, filter, findOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to list fragments: %w", err)
	}
	defer cursor.Close(ctx)

	var fragments []*Fragment
	var lastID string

	for cursor.Next(ctx) {
		var doc mongoFragment
		if err := cursor.Decode(&doc); err != nil {
			continue
		}

		frag := mongoToFragment(&doc)
		fragments = append(fragments, frag)
		lastID = frag.ID

		if int64(len(fragments)) > pageSize {
			break
		}
	}

	result := &ListResult{Fragments: fragments}

	if int64(len(fragments)) > pageSize {
		result.Fragments = fragments[:pageSize]
		result.NextPageToken = lastID
	}

	return result, nil
}

// Search performs full-text search on fragment content.
func (s *MongoStore) Search(ctx context.Context, opts SearchOptions) (*ListResult, error) {
	filter := bson.M{}

	// Use MongoDB text search
	if opts.Query != "" {
		filter["$text"] = bson.M{"$search": opts.Query}
	}

	// Apply tag filter
	if len(opts.Tags) > 0 {
		filter["tags"] = bson.M{"$in": opts.Tags}
	}

	pageSize := int64(opts.PageSize)
	if pageSize <= 0 {
		pageSize = 50
	}

	findOpts := options.Find().SetLimit(pageSize)

	// Add text score if searching
	if opts.Query != "" {
		findOpts.SetProjection(bson.M{"score": bson.M{"$meta": "textScore"}})
		findOpts.SetSort(bson.D{{Key: "score", Value: bson.M{"$meta": "textScore"}}})
	}

	cursor, err := s.collection.Find(ctx, filter, findOpts)
	if err != nil {
		// Fall back to simple regex search if text search fails
		return s.fallbackSearch(ctx, opts)
	}
	defer cursor.Close(ctx)

	var fragments []*Fragment
	for cursor.Next(ctx) {
		var doc mongoFragment
		if err := cursor.Decode(&doc); err != nil {
			continue
		}
		fragments = append(fragments, mongoToFragment(&doc))
	}

	return &ListResult{Fragments: fragments}, nil
}

// fallbackSearch uses regex for search when text index isn't available.
func (s *MongoStore) fallbackSearch(ctx context.Context, opts SearchOptions) (*ListResult, error) {
	filter := bson.M{
		"$or": []bson.M{
			{"name": bson.M{"$regex": opts.Query, "$options": "i"}},
			{"content": bson.M{"$regex": opts.Query, "$options": "i"}},
		},
	}

	if len(opts.Tags) > 0 {
		filter["tags"] = bson.M{"$in": opts.Tags}
	}

	pageSize := int64(opts.PageSize)
	if pageSize <= 0 {
		pageSize = 50
	}

	cursor, err := s.collection.Find(ctx, filter, options.Find().SetLimit(pageSize))
	if err != nil {
		return nil, fmt.Errorf("failed to search fragments: %w", err)
	}
	defer cursor.Close(ctx)

	var fragments []*Fragment
	for cursor.Next(ctx) {
		var doc mongoFragment
		if err := cursor.Decode(&doc); err != nil {
			continue
		}
		fragments = append(fragments, mongoToFragment(&doc))
	}

	return &ListResult{Fragments: fragments}, nil
}

// Close closes the MongoDB client.
func (s *MongoStore) Close() error {
	return s.client.Disconnect(context.Background())
}

func mongoToFragment(doc *mongoFragment) *Fragment {
	tags := doc.Tags
	if tags == nil {
		tags = []string{}
	}
	variables := doc.Variables
	if variables == nil {
		variables = []string{}
	}

	return &Fragment{
		ID:             doc.ID.Hex(),
		Name:           doc.Name,
		Version:        doc.Version,
		Author:         doc.Author,
		Tags:           tags,
		Variables:      variables,
		Content:        doc.Content,
		CreatedAt:      doc.CreatedAt,
		UpdatedAt:      doc.UpdatedAt,
		Downloads:      doc.Downloads,
		Likes:          doc.Likes,
		Dislikes:       doc.Dislikes,
		UsedByPersonas: doc.UsedByPersonas,
	}
}

func mongoToPersona(doc *mongoPersona) *Persona {
	fragments := make([]FragmentRef, len(doc.Fragments))
	for i, f := range doc.Fragments {
		fragments[i] = FragmentRef{
			Name:    f.Name,
			Author:  f.Author,
			Version: f.Version,
		}
	}

	return &Persona{
		ID:          doc.ID.Hex(),
		Name:        doc.Name,
		Author:      doc.Author,
		Version:     doc.Version,
		Description: doc.Description,
		Fragments:   fragments,
		CreatedAt:   doc.CreatedAt,
		UpdatedAt:   doc.UpdatedAt,
		Downloads:   doc.Downloads,
		Likes:       doc.Likes,
		Dislikes:    doc.Dislikes,
	}
}

// IncrementDownloads increments the download count atomically.
func (s *MongoStore) IncrementDownloads(ctx context.Context, id string) (*Fragment, error) {
	if id == "" {
		return nil, ErrInvalidID
	}

	objectID, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return nil, ErrInvalidID
	}

	result := s.collection.FindOneAndUpdate(
		ctx,
		bson.M{"_id": objectID},
		bson.M{"$inc": bson.M{"downloads": 1}},
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	)

	var doc mongoFragment
	if err := result.Decode(&doc); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to increment downloads: %w", err)
	}

	return mongoToFragment(&doc), nil
}

// IncrementLikes increments the like count atomically.
func (s *MongoStore) IncrementLikes(ctx context.Context, id string) (*Fragment, error) {
	if id == "" {
		return nil, ErrInvalidID
	}

	objectID, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return nil, ErrInvalidID
	}

	result := s.collection.FindOneAndUpdate(
		ctx,
		bson.M{"_id": objectID},
		bson.M{"$inc": bson.M{"likes": 1}},
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	)

	var doc mongoFragment
	if err := result.Decode(&doc); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to increment likes: %w", err)
	}

	return mongoToFragment(&doc), nil
}

// AddDislike adds a dislike with reason and increments the dislike count.
func (s *MongoStore) AddDislike(ctx context.Context, id string, reason string) (*Fragment, error) {
	if id == "" {
		return nil, ErrInvalidID
	}

	objectID, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return nil, ErrInvalidID
	}

	// Add dislike record to dislikes collection
	dislikesCol := s.collection.Database().Collection("dislikes")
	dislike := mongoDislike{
		FragmentID: objectID,
		Reason:     reason,
		CreatedAt:  time.Now().UTC(),
	}

	_, err = dislikesCol.InsertOne(ctx, dislike)
	if err != nil {
		return nil, fmt.Errorf("failed to add dislike: %w", err)
	}

	// Increment dislike count
	result := s.collection.FindOneAndUpdate(
		ctx,
		bson.M{"_id": objectID},
		bson.M{"$inc": bson.M{"dislikes": 1}},
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	)

	var doc mongoFragment
	if err := result.Decode(&doc); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to increment dislikes: %w", err)
	}

	return mongoToFragment(&doc), nil
}

// ListDislikes returns all dislikes for a fragment.
func (s *MongoStore) ListDislikes(ctx context.Context, fragmentID string) ([]*Dislike, error) {
	if fragmentID == "" {
		return nil, ErrInvalidID
	}

	objectID, err := primitive.ObjectIDFromHex(fragmentID)
	if err != nil {
		return nil, ErrInvalidID
	}

	dislikesCol := s.collection.Database().Collection("dislikes")
	cursor, err := dislikesCol.Find(
		ctx,
		bson.M{"fragment_id": objectID},
		options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list dislikes: %w", err)
	}
	defer cursor.Close(ctx)

	var dislikes []*Dislike
	for cursor.Next(ctx) {
		var doc mongoDislike
		if err := cursor.Decode(&doc); err != nil {
			continue
		}

		dislikes = append(dislikes, &Dislike{
			FragmentID: doc.FragmentID.Hex(),
			Reason:     doc.Reason,
			CreatedAt:  doc.CreatedAt,
		})
	}

	return dislikes, nil
}

// === Persona Operations ===

// CreatePersona stores a new persona.
func (s *MongoStore) CreatePersona(ctx context.Context, persona *Persona) (*Persona, error) {
	now := time.Now().UTC()

	fragments := make([]mongoFragmentRef, len(persona.Fragments))
	for i, f := range persona.Fragments {
		fragments[i] = mongoFragmentRef{
			Name:    f.Name,
			Author:  f.Author,
			Version: f.Version,
		}
	}

	doc := mongoPersona{
		Name:        persona.Name,
		Author:      persona.Author,
		Version:     persona.Version,
		Description: persona.Description,
		Fragments:   fragments,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	result, err := s.personas.InsertOne(ctx, doc)
	if err != nil {
		return nil, fmt.Errorf("failed to create persona: %w", err)
	}

	id := result.InsertedID.(primitive.ObjectID)

	// Increment UsedByPersonas for each referenced fragment
	for _, ref := range persona.Fragments {
		s.incrementFragmentUsage(ctx, ref.Author, ref.Name, ref.Version, 1)
	}

	return &Persona{
		ID:          id.Hex(),
		Name:        persona.Name,
		Author:      persona.Author,
		Version:     persona.Version,
		Description: persona.Description,
		Fragments:   persona.Fragments,
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

// incrementFragmentUsage updates the UsedByPersonas count for a fragment.
func (s *MongoStore) incrementFragmentUsage(ctx context.Context, author, name, version string, delta int) {
	filter := bson.M{"author": author, "name": name}
	if version != "" {
		filter["version"] = version
	}
	s.collection.UpdateMany(ctx, filter, bson.M{"$inc": bson.M{"used_by_personas": delta}})
}

// GetPersona retrieves a persona by ID.
func (s *MongoStore) GetPersona(ctx context.Context, id string) (*Persona, error) {
	if id == "" {
		return nil, ErrInvalidID
	}

	objectID, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return nil, ErrInvalidID
	}

	var doc mongoPersona
	err = s.personas.FindOne(ctx, bson.M{"_id": objectID}).Decode(&doc)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrPersonaNotFound
		}
		return nil, fmt.Errorf("failed to get persona: %w", err)
	}

	return mongoToPersona(&doc), nil
}

// GetPersonaByName retrieves a persona by author, name, and optional version.
func (s *MongoStore) GetPersonaByName(ctx context.Context, author, name, version string) (*Persona, error) {
	filter := bson.M{"author": author, "name": name}
	if version != "" {
		filter["version"] = version
	}

	opts := options.FindOne().SetSort(bson.D{{Key: "updated_at", Value: -1}})

	var doc mongoPersona
	err := s.personas.FindOne(ctx, filter, opts).Decode(&doc)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrPersonaNotFound
		}
		return nil, fmt.Errorf("failed to get persona by name: %w", err)
	}

	return mongoToPersona(&doc), nil
}

// UpdatePersona updates an existing persona.
func (s *MongoStore) UpdatePersona(ctx context.Context, persona *Persona) (*Persona, error) {
	if persona.ID == "" {
		return nil, ErrInvalidID
	}

	objectID, err := primitive.ObjectIDFromHex(persona.ID)
	if err != nil {
		return nil, ErrInvalidID
	}

	// Get old persona to update fragment usage counts
	var oldDoc mongoPersona
	err = s.personas.FindOne(ctx, bson.M{"_id": objectID}).Decode(&oldDoc)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrPersonaNotFound
		}
		return nil, fmt.Errorf("failed to get persona for update: %w", err)
	}

	// Decrement usage for old fragments
	for _, ref := range oldDoc.Fragments {
		s.incrementFragmentUsage(ctx, ref.Author, ref.Name, ref.Version, -1)
	}

	now := time.Now().UTC()
	fragments := make([]mongoFragmentRef, len(persona.Fragments))
	for i, f := range persona.Fragments {
		fragments[i] = mongoFragmentRef{
			Name:    f.Name,
			Author:  f.Author,
			Version: f.Version,
		}
	}

	update := bson.M{
		"$set": bson.M{
			"name":        persona.Name,
			"author":      persona.Author,
			"version":     persona.Version,
			"description": persona.Description,
			"fragments":   fragments,
			"updated_at":  now,
		},
	}

	result, err := s.personas.UpdateByID(ctx, objectID, update)
	if err != nil {
		return nil, fmt.Errorf("failed to update persona: %w", err)
	}

	if result.MatchedCount == 0 {
		return nil, ErrPersonaNotFound
	}

	// Increment usage for new fragments
	for _, ref := range persona.Fragments {
		s.incrementFragmentUsage(ctx, ref.Author, ref.Name, ref.Version, 1)
	}

	persona.UpdatedAt = now
	return persona, nil
}

// DeletePersona removes a persona by ID.
func (s *MongoStore) DeletePersona(ctx context.Context, id string) error {
	if id == "" {
		return ErrInvalidID
	}

	objectID, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return ErrInvalidID
	}

	// Get persona to decrement fragment usage
	var doc mongoPersona
	err = s.personas.FindOne(ctx, bson.M{"_id": objectID}).Decode(&doc)
	if err == nil {
		for _, ref := range doc.Fragments {
			s.incrementFragmentUsage(ctx, ref.Author, ref.Name, ref.Version, -1)
		}
	}

	result, err := s.personas.DeleteOne(ctx, bson.M{"_id": objectID})
	if err != nil {
		return fmt.Errorf("failed to delete persona: %w", err)
	}

	if result.DeletedCount == 0 {
		return ErrPersonaNotFound
	}

	return nil
}

// ListPersonas returns personas matching the given options.
func (s *MongoStore) ListPersonas(ctx context.Context, opts PersonaListOptions) (*PersonaListResult, error) {
	filter := bson.M{}

	if opts.Author != "" {
		filter["author"] = opts.Author
	}

	if opts.NamePrefix != "" {
		filter["name"] = bson.M{"$regex": "^" + opts.NamePrefix}
	}

	pageSize := int64(opts.PageSize)
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 100 {
		pageSize = 100
	}

	findOpts := options.Find().
		SetSort(bson.D{{Key: "name", Value: 1}}).
		SetLimit(pageSize + 1)

	if opts.PageToken != "" {
		objectID, err := primitive.ObjectIDFromHex(opts.PageToken)
		if err == nil {
			filter["_id"] = bson.M{"$gt": objectID}
		}
	}

	cursor, err := s.personas.Find(ctx, filter, findOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to list personas: %w", err)
	}
	defer cursor.Close(ctx)

	var personas []*Persona
	var lastID string

	for cursor.Next(ctx) {
		var doc mongoPersona
		if err := cursor.Decode(&doc); err != nil {
			continue
		}

		p := mongoToPersona(&doc)
		personas = append(personas, p)
		lastID = p.ID

		if int64(len(personas)) > pageSize {
			break
		}
	}

	result := &PersonaListResult{Personas: personas}

	if int64(len(personas)) > pageSize {
		result.Personas = personas[:pageSize]
		result.NextPageToken = lastID
	}

	return result, nil
}

// IncrementPersonaDownloads increments the download count atomically.
func (s *MongoStore) IncrementPersonaDownloads(ctx context.Context, id string) (*Persona, error) {
	if id == "" {
		return nil, ErrInvalidID
	}

	objectID, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return nil, ErrInvalidID
	}

	result := s.personas.FindOneAndUpdate(
		ctx,
		bson.M{"_id": objectID},
		bson.M{"$inc": bson.M{"downloads": 1}},
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	)

	var doc mongoPersona
	if err := result.Decode(&doc); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrPersonaNotFound
		}
		return nil, fmt.Errorf("failed to increment persona downloads: %w", err)
	}

	return mongoToPersona(&doc), nil
}

// IncrementPersonaLikes increments the like count atomically.
func (s *MongoStore) IncrementPersonaLikes(ctx context.Context, id string) (*Persona, error) {
	if id == "" {
		return nil, ErrInvalidID
	}

	objectID, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return nil, ErrInvalidID
	}

	result := s.personas.FindOneAndUpdate(
		ctx,
		bson.M{"_id": objectID},
		bson.M{"$inc": bson.M{"likes": 1}},
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	)

	var doc mongoPersona
	if err := result.Decode(&doc); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrPersonaNotFound
		}
		return nil, fmt.Errorf("failed to increment persona likes: %w", err)
	}

	return mongoToPersona(&doc), nil
}

// AddPersonaDislike adds a dislike with reason and increments the dislike count.
func (s *MongoStore) AddPersonaDislike(ctx context.Context, id string, reason string) (*Persona, error) {
	if id == "" {
		return nil, ErrInvalidID
	}

	objectID, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return nil, ErrInvalidID
	}

	// Add dislike record
	dislikesCol := s.db.Collection("persona_dislikes")
	dislike := mongoPersonaDislike{
		PersonaID: objectID,
		Reason:    reason,
		CreatedAt: time.Now().UTC(),
	}

	_, err = dislikesCol.InsertOne(ctx, dislike)
	if err != nil {
		return nil, fmt.Errorf("failed to add persona dislike: %w", err)
	}

	// Increment dislike count
	result := s.personas.FindOneAndUpdate(
		ctx,
		bson.M{"_id": objectID},
		bson.M{"$inc": bson.M{"dislikes": 1}},
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	)

	var doc mongoPersona
	if err := result.Decode(&doc); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrPersonaNotFound
		}
		return nil, fmt.Errorf("failed to increment persona dislikes: %w", err)
	}

	return mongoToPersona(&doc), nil
}

// Ensure unused import doesn't cause error
var _ = strings.Contains
