package storage

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/google/uuid"
)

const (
	defaultFragmentsTable = "fragments"
	defaultDislikesTable  = "fragment_dislikes"
)

// DynamoStore implements Store using AWS DynamoDB.
type DynamoStore struct {
	client         *dynamodb.Client
	fragmentsTable string
	dislikesTable  string
}

// DynamoConfig contains configuration for DynamoDB connection.
type DynamoConfig struct {
	Region          string // AWS region
	FragmentsTable  string // Table name for fragments (defaults to "fragments")
	DislikesTable   string // Table name for dislikes (defaults to "fragment_dislikes")
	EndpointURL     string // Optional: custom endpoint (for local development)
}

// NewDynamoStore creates a new DynamoDB-backed store.
func NewDynamoStore(ctx context.Context, cfg DynamoConfig) (*DynamoStore, error) {
	var opts []func(*config.LoadOptions) error

	if cfg.Region != "" {
		opts = append(opts, config.WithRegion(cfg.Region))
	}

	awsCfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	var clientOpts []func(*dynamodb.Options)
	if cfg.EndpointURL != "" {
		clientOpts = append(clientOpts, func(o *dynamodb.Options) {
			o.BaseEndpoint = aws.String(cfg.EndpointURL)
		})
	}

	client := dynamodb.NewFromConfig(awsCfg, clientOpts...)

	fragmentsTable := cfg.FragmentsTable
	if fragmentsTable == "" {
		fragmentsTable = defaultFragmentsTable
	}

	dislikesTable := cfg.DislikesTable
	if dislikesTable == "" {
		dislikesTable = defaultDislikesTable
	}

	return &DynamoStore{
		client:         client,
		fragmentsTable: fragmentsTable,
		dislikesTable:  dislikesTable,
	}, nil
}

// dynamoFragment is the DynamoDB item structure.
type dynamoFragment struct {
	ID        string   `dynamodbav:"id"`
	Name      string   `dynamodbav:"name"`
	Version   string   `dynamodbav:"version"`
	Author    string   `dynamodbav:"author"`
	Tags      []string `dynamodbav:"tags"`
	Variables []string `dynamodbav:"variables"`
	Content   string   `dynamodbav:"content"`
	CreatedAt string   `dynamodbav:"created_at"`
	UpdatedAt string   `dynamodbav:"updated_at"`
	Downloads int64    `dynamodbav:"downloads"`
	Likes     int64    `dynamodbav:"likes"`
	Dislikes  int64    `dynamodbav:"dislikes"`
	// GSI attributes
	NameVersion string `dynamodbav:"name_version"` // For name+version lookups
	Popularity  int64  `dynamodbav:"popularity"`   // downloads + likes - dislikes
}

// dynamoDislike is the DynamoDB item structure for dislikes.
type dynamoDislike struct {
	ID         string `dynamodbav:"id"`
	FragmentID string `dynamodbav:"fragment_id"`
	Reason     string `dynamodbav:"reason"`
	CreatedAt  string `dynamodbav:"created_at"`
}

// Create stores a new fragment.
func (s *DynamoStore) Create(ctx context.Context, frag *Fragment) (*Fragment, error) {
	now := time.Now().UTC()
	id := uuid.New().String()

	item := dynamoFragment{
		ID:          id,
		Name:        frag.Name,
		Version:     frag.Version,
		Author:      frag.Author,
		Tags:        frag.Tags,
		Variables:   frag.Variables,
		Content:     frag.Content,
		CreatedAt:   now.Format(time.RFC3339),
		UpdatedAt:   now.Format(time.RFC3339),
		Downloads:   0,
		Likes:       0,
		Dislikes:    0,
		NameVersion: frag.Name + "#" + frag.Version,
		Popularity:  0,
	}

	av, err := attributevalue.MarshalMap(item)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal fragment: %w", err)
	}

	_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(s.fragmentsTable),
		Item:      av,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create fragment: %w", err)
	}

	return &Fragment{
		ID:        id,
		Name:      frag.Name,
		Version:   frag.Version,
		Author:    frag.Author,
		Tags:      frag.Tags,
		Variables: frag.Variables,
		Content:   frag.Content,
		CreatedAt: now,
		UpdatedAt: now,
		Downloads: 0,
		Likes:     0,
		Dislikes:  0,
	}, nil
}

// Get retrieves a fragment by ID.
func (s *DynamoStore) Get(ctx context.Context, id string) (*Fragment, error) {
	if id == "" {
		return nil, ErrInvalidID
	}

	result, err := s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(s.fragmentsTable),
		Key: map[string]types.AttributeValue{
			"id": &types.AttributeValueMemberS{Value: id},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get fragment: %w", err)
	}

	if result.Item == nil {
		return nil, ErrNotFound
	}

	return dynamoToFragment(result.Item)
}

// GetByName retrieves a fragment by author, name, and optional version.
func (s *DynamoStore) GetByName(ctx context.Context, author, name, version string) (*Fragment, error) {
	// Build composite key for author_name_version GSI
	var keyCondition expression.KeyConditionBuilder
	var indexName *string

	if version != "" {
		// Use author_name_version GSI
		keyCondition = expression.Key("author_name_version").Equal(expression.Value(author + "#" + name + "#" + version))
		indexName = aws.String("author_name_version-index")
	} else {
		// Query by author_name and get latest by updated_at
		keyCondition = expression.Key("author_name").Equal(expression.Value(author + "#" + name))
		indexName = aws.String("author_name-index")
	}

	expr, err := expression.NewBuilder().WithKeyCondition(keyCondition).Build()
	if err != nil {
		return nil, fmt.Errorf("failed to build expression: %w", err)
	}

	result, err := s.client.Query(ctx, &dynamodb.QueryInput{
		TableName:                 aws.String(s.fragmentsTable),
		IndexName:                 indexName,
		KeyConditionExpression:    expr.KeyCondition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
		ScanIndexForward:          aws.Bool(false), // Descending order
		Limit:                     aws.Int32(1),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query fragment: %w", err)
	}

	if len(result.Items) == 0 {
		return nil, ErrNotFound
	}

	return dynamoToFragment(result.Items[0])
}

// Update updates an existing fragment.
func (s *DynamoStore) Update(ctx context.Context, frag *Fragment) (*Fragment, error) {
	if frag.ID == "" {
		return nil, ErrInvalidID
	}

	now := time.Now().UTC()

	update := expression.Set(expression.Name("name"), expression.Value(frag.Name)).
		Set(expression.Name("version"), expression.Value(frag.Version)).
		Set(expression.Name("author"), expression.Value(frag.Author)).
		Set(expression.Name("tags"), expression.Value(frag.Tags)).
		Set(expression.Name("variables"), expression.Value(frag.Variables)).
		Set(expression.Name("content"), expression.Value(frag.Content)).
		Set(expression.Name("updated_at"), expression.Value(now.Format(time.RFC3339))).
		Set(expression.Name("name_version"), expression.Value(frag.Name+"#"+frag.Version))

	expr, err := expression.NewBuilder().WithUpdate(update).Build()
	if err != nil {
		return nil, fmt.Errorf("failed to build expression: %w", err)
	}

	result, err := s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(s.fragmentsTable),
		Key: map[string]types.AttributeValue{
			"id": &types.AttributeValueMemberS{Value: frag.ID},
		},
		UpdateExpression:          expr.Update(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
		ReturnValues:              types.ReturnValueAllNew,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to update fragment: %w", err)
	}

	return dynamoToFragment(result.Attributes)
}

// Delete removes a fragment by ID.
func (s *DynamoStore) Delete(ctx context.Context, id string) error {
	if id == "" {
		return ErrInvalidID
	}

	_, err := s.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(s.fragmentsTable),
		Key: map[string]types.AttributeValue{
			"id": &types.AttributeValueMemberS{Value: id},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to delete fragment: %w", err)
	}

	return nil
}

// List returns fragments matching the given options.
func (s *DynamoStore) List(ctx context.Context, opts ListOptions) (*ListResult, error) {
	// Build filter expression
	var filterExprs []expression.ConditionBuilder

	if len(opts.Tags) > 0 {
		// DynamoDB doesn't have array-contains-any, so we use OR conditions
		var tagConditions []expression.ConditionBuilder
		for _, tag := range opts.Tags {
			tagConditions = append(tagConditions,
				expression.Contains(expression.Name("tags"), tag))
		}
		if len(tagConditions) > 0 {
			combined := tagConditions[0]
			for _, c := range tagConditions[1:] {
				combined = combined.Or(c)
			}
			filterExprs = append(filterExprs, combined)
		}
	}

	if opts.Author != "" {
		filterExprs = append(filterExprs,
			expression.Name("author").Equal(expression.Value(opts.Author)))
	}

	if opts.NamePrefix != "" {
		filterExprs = append(filterExprs,
			expression.Name("name").BeginsWith(opts.NamePrefix))
	}

	pageSize := int32(opts.PageSize)
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 100 {
		pageSize = 100
	}

	input := &dynamodb.ScanInput{
		TableName: aws.String(s.fragmentsTable),
		Limit:     aws.Int32(pageSize),
	}

	if len(filterExprs) > 0 {
		combined := filterExprs[0]
		for _, f := range filterExprs[1:] {
			combined = combined.And(f)
		}
		expr, err := expression.NewBuilder().WithFilter(combined).Build()
		if err != nil {
			return nil, fmt.Errorf("failed to build expression: %w", err)
		}
		input.FilterExpression = expr.Filter()
		input.ExpressionAttributeNames = expr.Names()
		input.ExpressionAttributeValues = expr.Values()
	}

	if opts.PageToken != "" {
		input.ExclusiveStartKey = map[string]types.AttributeValue{
			"id": &types.AttributeValueMemberS{Value: opts.PageToken},
		}
	}

	result, err := s.client.Scan(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("failed to list fragments: %w", err)
	}

	var fragments []*Fragment
	for _, item := range result.Items {
		frag, err := dynamoToFragment(item)
		if err != nil {
			continue
		}
		fragments = append(fragments, frag)
	}

	listResult := &ListResult{Fragments: fragments}

	if result.LastEvaluatedKey != nil {
		if idAttr, ok := result.LastEvaluatedKey["id"].(*types.AttributeValueMemberS); ok {
			listResult.NextPageToken = idAttr.Value
		}
	}

	return listResult, nil
}

// Search performs search on fragment content.
func (s *DynamoStore) Search(ctx context.Context, opts SearchOptions) (*ListResult, error) {
	// DynamoDB doesn't have full-text search, so we scan and filter in memory
	pageSize := int32(opts.PageSize)
	if pageSize <= 0 {
		pageSize = 50
	}

	result, err := s.client.Scan(ctx, &dynamodb.ScanInput{
		TableName: aws.String(s.fragmentsTable),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to search fragments: %w", err)
	}

	var fragments []*Fragment
	queryLower := strings.ToLower(opts.Query)

	for _, item := range result.Items {
		frag, err := dynamoToFragment(item)
		if err != nil {
			continue
		}

		// Simple content/name matching
		if strings.Contains(strings.ToLower(frag.Content), queryLower) ||
			strings.Contains(strings.ToLower(frag.Name), queryLower) {
			// Apply tag filter if specified
			if len(opts.Tags) > 0 {
				hasTag := false
				for _, tag := range opts.Tags {
					for _, fragTag := range frag.Tags {
						if strings.EqualFold(tag, fragTag) {
							hasTag = true
							break
						}
					}
					if hasTag {
						break
					}
				}
				if !hasTag {
					continue
				}
			}
			fragments = append(fragments, frag)
		}

		if len(fragments) >= int(pageSize) {
			break
		}
	}

	return &ListResult{Fragments: fragments}, nil
}

// IncrementDownloads increments the download count atomically.
func (s *DynamoStore) IncrementDownloads(ctx context.Context, id string) (*Fragment, error) {
	if id == "" {
		return nil, ErrInvalidID
	}

	result, err := s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(s.fragmentsTable),
		Key: map[string]types.AttributeValue{
			"id": &types.AttributeValueMemberS{Value: id},
		},
		UpdateExpression: aws.String("SET downloads = downloads + :inc, popularity = popularity + :inc"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":inc": &types.AttributeValueMemberN{Value: "1"},
		},
		ReturnValues: types.ReturnValueAllNew,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to increment downloads: %w", err)
	}

	return dynamoToFragment(result.Attributes)
}

// IncrementLikes increments the like count atomically.
func (s *DynamoStore) IncrementLikes(ctx context.Context, id string) (*Fragment, error) {
	if id == "" {
		return nil, ErrInvalidID
	}

	result, err := s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(s.fragmentsTable),
		Key: map[string]types.AttributeValue{
			"id": &types.AttributeValueMemberS{Value: id},
		},
		UpdateExpression: aws.String("SET likes = likes + :inc, popularity = popularity + :inc"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":inc": &types.AttributeValueMemberN{Value: "1"},
		},
		ReturnValues: types.ReturnValueAllNew,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to increment likes: %w", err)
	}

	return dynamoToFragment(result.Attributes)
}

// AddDislike adds a dislike with reason and increments the dislike count.
func (s *DynamoStore) AddDislike(ctx context.Context, id string, reason string) (*Fragment, error) {
	if id == "" {
		return nil, ErrInvalidID
	}

	now := time.Now().UTC()
	dislikeID := uuid.New().String()

	dislike := dynamoDislike{
		ID:         dislikeID,
		FragmentID: id,
		Reason:     reason,
		CreatedAt:  now.Format(time.RFC3339),
	}

	av, err := attributevalue.MarshalMap(dislike)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal dislike: %w", err)
	}

	_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(s.dislikesTable),
		Item:      av,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to add dislike: %w", err)
	}

	// Increment dislike count and decrement popularity
	result, err := s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(s.fragmentsTable),
		Key: map[string]types.AttributeValue{
			"id": &types.AttributeValueMemberS{Value: id},
		},
		UpdateExpression: aws.String("SET dislikes = dislikes + :inc, popularity = popularity - :inc"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":inc": &types.AttributeValueMemberN{Value: "1"},
		},
		ReturnValues: types.ReturnValueAllNew,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to increment dislikes: %w", err)
	}

	return dynamoToFragment(result.Attributes)
}

// ListDislikes returns all dislikes for a fragment.
func (s *DynamoStore) ListDislikes(ctx context.Context, fragmentID string) ([]*Dislike, error) {
	if fragmentID == "" {
		return nil, ErrInvalidID
	}

	keyCondition := expression.Key("fragment_id").Equal(expression.Value(fragmentID))
	expr, err := expression.NewBuilder().WithKeyCondition(keyCondition).Build()
	if err != nil {
		return nil, fmt.Errorf("failed to build expression: %w", err)
	}

	result, err := s.client.Query(ctx, &dynamodb.QueryInput{
		TableName:                 aws.String(s.dislikesTable),
		IndexName:                 aws.String("fragment_id-index"),
		KeyConditionExpression:    expr.KeyCondition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
		ScanIndexForward:          aws.Bool(false),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list dislikes: %w", err)
	}

	var dislikes []*Dislike
	for _, item := range result.Items {
		var d dynamoDislike
		if err := attributevalue.UnmarshalMap(item, &d); err != nil {
			continue
		}

		createdAt, _ := time.Parse(time.RFC3339, d.CreatedAt)
		dislikes = append(dislikes, &Dislike{
			FragmentID: d.FragmentID,
			Reason:     d.Reason,
			CreatedAt:  createdAt,
		})
	}

	return dislikes, nil
}

// Close closes the DynamoDB client (no-op for DynamoDB).
func (s *DynamoStore) Close() error {
	return nil
}

func dynamoToFragment(item map[string]types.AttributeValue) (*Fragment, error) {
	var f dynamoFragment
	if err := attributevalue.UnmarshalMap(item, &f); err != nil {
		return nil, err
	}

	createdAt, _ := time.Parse(time.RFC3339, f.CreatedAt)
	updatedAt, _ := time.Parse(time.RFC3339, f.UpdatedAt)

	tags := f.Tags
	if tags == nil {
		tags = []string{}
	}
	variables := f.Variables
	if variables == nil {
		variables = []string{}
	}

	return &Fragment{
		ID:        f.ID,
		Name:      f.Name,
		Version:   f.Version,
		Author:    f.Author,
		Tags:      tags,
		Variables: variables,
		Content:   f.Content,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
		Downloads: f.Downloads,
		Likes:     f.Likes,
		Dislikes:  f.Dislikes,
	}, nil
}

// === Persona Operations ===
// TODO: Implement DynamoDB persona operations

// CreatePersona creates a new persona.
func (s *DynamoStore) CreatePersona(ctx context.Context, persona *Persona) (*Persona, error) {
	return nil, fmt.Errorf("persona operations not yet implemented for DynamoDB")
}

// GetPersona retrieves a persona by ID.
func (s *DynamoStore) GetPersona(ctx context.Context, id string) (*Persona, error) {
	return nil, fmt.Errorf("persona operations not yet implemented for DynamoDB")
}

// GetPersonaByName retrieves a persona by author, name, and optional version.
func (s *DynamoStore) GetPersonaByName(ctx context.Context, author, name, version string) (*Persona, error) {
	return nil, fmt.Errorf("persona operations not yet implemented for DynamoDB")
}

// UpdatePersona updates an existing persona.
func (s *DynamoStore) UpdatePersona(ctx context.Context, persona *Persona) (*Persona, error) {
	return nil, fmt.Errorf("persona operations not yet implemented for DynamoDB")
}

// DeletePersona removes a persona by ID.
func (s *DynamoStore) DeletePersona(ctx context.Context, id string) error {
	return fmt.Errorf("persona operations not yet implemented for DynamoDB")
}

// ListPersonas returns personas matching the given options.
func (s *DynamoStore) ListPersonas(ctx context.Context, opts PersonaListOptions) (*PersonaListResult, error) {
	return nil, fmt.Errorf("persona operations not yet implemented for DynamoDB")
}

// IncrementPersonaDownloads increments the download count.
func (s *DynamoStore) IncrementPersonaDownloads(ctx context.Context, id string) (*Persona, error) {
	return nil, fmt.Errorf("persona operations not yet implemented for DynamoDB")
}

// IncrementPersonaLikes increments the like count.
func (s *DynamoStore) IncrementPersonaLikes(ctx context.Context, id string) (*Persona, error) {
	return nil, fmt.Errorf("persona operations not yet implemented for DynamoDB")
}

// AddPersonaDislike adds a dislike with reason.
func (s *DynamoStore) AddPersonaDislike(ctx context.Context, id string, reason string) (*Persona, error) {
	return nil, fmt.Errorf("persona operations not yet implemented for DynamoDB")
}
