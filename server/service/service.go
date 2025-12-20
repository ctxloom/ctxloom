// Package service implements the gRPC FragmentService.
package service

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "mlcm/server/proto/fragmentspb"
	"mlcm/server/storage"
)

// FragmentService implements the gRPC FragmentService.
type FragmentService struct {
	pb.UnimplementedFragmentServiceServer
	store storage.Store
}

// NewFragmentService creates a new FragmentService with the given storage backend.
func NewFragmentService(store storage.Store) *FragmentService {
	return &FragmentService{store: store}
}

// CreateFragment creates a new fragment.
func (s *FragmentService) CreateFragment(ctx context.Context, req *pb.CreateFragmentRequest) (*pb.Fragment, error) {
	frag := &storage.Fragment{
		Name:      req.Name,
		Version:   req.Version,
		Author:    req.Author,
		Tags:      req.Tags,
		Variables: req.Variables,
		Content:   req.Content,
	}

	created, err := s.store.Create(ctx, frag)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create fragment: %v", err)
	}

	return fragmentToProto(created), nil
}

// GetFragment retrieves a fragment by ID.
func (s *FragmentService) GetFragment(ctx context.Context, req *pb.GetFragmentRequest) (*pb.Fragment, error) {
	frag, err := s.store.Get(ctx, req.Id)
	if err != nil {
		if err == storage.ErrNotFound {
			return nil, status.Error(codes.NotFound, "fragment not found")
		}
		if err == storage.ErrInvalidID {
			return nil, status.Error(codes.InvalidArgument, "invalid fragment ID")
		}
		return nil, status.Errorf(codes.Internal, "failed to get fragment: %v", err)
	}

	return fragmentToProto(frag), nil
}

// GetFragmentByName retrieves a fragment by author, name, and optional version.
func (s *FragmentService) GetFragmentByName(ctx context.Context, req *pb.GetFragmentByNameRequest) (*pb.Fragment, error) {
	frag, err := s.store.GetByName(ctx, req.Author, req.Name, req.Version)
	if err != nil {
		if err == storage.ErrNotFound {
			return nil, status.Error(codes.NotFound, "fragment not found")
		}
		return nil, status.Errorf(codes.Internal, "failed to get fragment: %v", err)
	}

	return fragmentToProto(frag), nil
}

// UpdateFragment updates an existing fragment.
func (s *FragmentService) UpdateFragment(ctx context.Context, req *pb.UpdateFragmentRequest) (*pb.Fragment, error) {
	frag := &storage.Fragment{
		ID:        req.Id,
		Name:      req.Name,
		Version:   req.Version,
		Author:    req.Author,
		Tags:      req.Tags,
		Variables: req.Variables,
		Content:   req.Content,
	}

	updated, err := s.store.Update(ctx, frag)
	if err != nil {
		if err == storage.ErrNotFound {
			return nil, status.Error(codes.NotFound, "fragment not found")
		}
		if err == storage.ErrInvalidID {
			return nil, status.Error(codes.InvalidArgument, "invalid fragment ID")
		}
		return nil, status.Errorf(codes.Internal, "failed to update fragment: %v", err)
	}

	return fragmentToProto(updated), nil
}

// DeleteFragment deletes a fragment.
func (s *FragmentService) DeleteFragment(ctx context.Context, req *pb.DeleteFragmentRequest) (*emptypb.Empty, error) {
	err := s.store.Delete(ctx, req.Id)
	if err != nil {
		if err == storage.ErrNotFound {
			return nil, status.Error(codes.NotFound, "fragment not found")
		}
		if err == storage.ErrInvalidID {
			return nil, status.Error(codes.InvalidArgument, "invalid fragment ID")
		}
		return nil, status.Errorf(codes.Internal, "failed to delete fragment: %v", err)
	}

	return &emptypb.Empty{}, nil
}

// ListFragments lists fragments with optional filtering.
func (s *FragmentService) ListFragments(ctx context.Context, req *pb.ListFragmentsRequest) (*pb.ListFragmentsResponse, error) {
	opts := storage.ListOptions{
		Tags:       req.Tags,
		Author:     req.Author,
		NamePrefix: req.NamePrefix,
		PageSize:   int(req.PageSize),
		PageToken:  req.PageToken,
	}

	result, err := s.store.List(ctx, opts)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list fragments: %v", err)
	}

	resp := &pb.ListFragmentsResponse{
		NextPageToken: result.NextPageToken,
	}

	for _, frag := range result.Fragments {
		resp.Fragments = append(resp.Fragments, fragmentToProto(frag))
	}

	return resp, nil
}

// SearchFragments searches fragments by content.
func (s *FragmentService) SearchFragments(ctx context.Context, req *pb.SearchFragmentsRequest) (*pb.SearchFragmentsResponse, error) {
	opts := storage.SearchOptions{
		Query:     req.Query,
		Tags:      req.Tags,
		PageSize:  int(req.PageSize),
		PageToken: req.PageToken,
	}

	result, err := s.store.Search(ctx, opts)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to search fragments: %v", err)
	}

	resp := &pb.SearchFragmentsResponse{
		NextPageToken: result.NextPageToken,
	}

	for _, frag := range result.Fragments {
		resp.Fragments = append(resp.Fragments, fragmentToProto(frag))
	}

	return resp, nil
}

// DownloadFragment increments download count and returns the fragment.
func (s *FragmentService) DownloadFragment(ctx context.Context, req *pb.DownloadFragmentRequest) (*pb.Fragment, error) {
	frag, err := s.store.IncrementDownloads(ctx, req.Id)
	if err != nil {
		if err == storage.ErrNotFound {
			return nil, status.Error(codes.NotFound, "fragment not found")
		}
		if err == storage.ErrInvalidID {
			return nil, status.Error(codes.InvalidArgument, "invalid fragment ID")
		}
		return nil, status.Errorf(codes.Internal, "failed to increment downloads: %v", err)
	}

	return fragmentToProto(frag), nil
}

// LikeFragment adds a like to a fragment.
func (s *FragmentService) LikeFragment(ctx context.Context, req *pb.LikeFragmentRequest) (*pb.Fragment, error) {
	frag, err := s.store.IncrementLikes(ctx, req.Id)
	if err != nil {
		if err == storage.ErrNotFound {
			return nil, status.Error(codes.NotFound, "fragment not found")
		}
		if err == storage.ErrInvalidID {
			return nil, status.Error(codes.InvalidArgument, "invalid fragment ID")
		}
		return nil, status.Errorf(codes.Internal, "failed to like fragment: %v", err)
	}

	return fragmentToProto(frag), nil
}

// DislikeFragment adds a dislike with reason to a fragment.
func (s *FragmentService) DislikeFragment(ctx context.Context, req *pb.DislikeFragmentRequest) (*pb.Fragment, error) {
	if req.Reason == "" {
		return nil, status.Error(codes.InvalidArgument, "reason is required for dislike")
	}

	frag, err := s.store.AddDislike(ctx, req.Id, req.Reason)
	if err != nil {
		if err == storage.ErrNotFound {
			return nil, status.Error(codes.NotFound, "fragment not found")
		}
		if err == storage.ErrInvalidID {
			return nil, status.Error(codes.InvalidArgument, "invalid fragment ID")
		}
		return nil, status.Errorf(codes.Internal, "failed to dislike fragment: %v", err)
	}

	return fragmentToProto(frag), nil
}

// ListDislikes returns dislikes with reasons for a fragment.
func (s *FragmentService) ListDislikes(ctx context.Context, req *pb.ListDislikesRequest) (*pb.ListDislikesResponse, error) {
	dislikes, err := s.store.ListDislikes(ctx, req.FragmentId)
	if err != nil {
		if err == storage.ErrInvalidID {
			return nil, status.Error(codes.InvalidArgument, "invalid fragment ID")
		}
		return nil, status.Errorf(codes.Internal, "failed to list dislikes: %v", err)
	}

	resp := &pb.ListDislikesResponse{}
	for _, d := range dislikes {
		resp.Dislikes = append(resp.Dislikes, &pb.Dislike{
			FragmentId: d.FragmentID,
			Reason:     d.Reason,
			CreatedAt:  timestamppb.New(d.CreatedAt),
		})
	}

	return resp, nil
}

func fragmentToProto(frag *storage.Fragment) *pb.Fragment {
	tags := frag.Tags
	if tags == nil {
		tags = []string{}
	}
	variables := frag.Variables
	if variables == nil {
		variables = []string{}
	}

	return &pb.Fragment{
		Id:             frag.ID,
		Name:           frag.Name,
		Version:        frag.Version,
		Author:         frag.Author,
		Tags:           tags,
		Variables:      variables,
		Content:        frag.Content,
		CreatedAt:      timestamppb.New(frag.CreatedAt),
		UpdatedAt:      timestamppb.New(frag.UpdatedAt),
		Downloads:      frag.Downloads,
		Likes:          frag.Likes,
		Dislikes:       frag.Dislikes,
		UsedByPersonas: frag.UsedByPersonas,
	}
}

// === Persona Operations ===

// CreatePersona creates a new persona.
func (s *FragmentService) CreatePersona(ctx context.Context, req *pb.CreatePersonaRequest) (*pb.Persona, error) {
	persona := &storage.Persona{
		Name:        req.Name,
		Author:      req.Author,
		Version:     req.Version,
		Description: req.Description,
		Fragments:   fragmentRefsFromProto(req.Fragments),
	}

	created, err := s.store.CreatePersona(ctx, persona)
	if err != nil {
		if err == storage.ErrAlreadyExists {
			return nil, status.Error(codes.AlreadyExists, "persona already exists")
		}
		return nil, status.Errorf(codes.Internal, "failed to create persona: %v", err)
	}

	return personaToProto(created), nil
}

// GetPersona retrieves a persona by ID.
func (s *FragmentService) GetPersona(ctx context.Context, req *pb.GetPersonaRequest) (*pb.Persona, error) {
	persona, err := s.store.GetPersona(ctx, req.Id)
	if err != nil {
		if err == storage.ErrPersonaNotFound {
			return nil, status.Error(codes.NotFound, "persona not found")
		}
		if err == storage.ErrInvalidID {
			return nil, status.Error(codes.InvalidArgument, "invalid persona ID")
		}
		return nil, status.Errorf(codes.Internal, "failed to get persona: %v", err)
	}

	return personaToProto(persona), nil
}

// GetPersonaByName retrieves a persona by author, name, and optional version.
func (s *FragmentService) GetPersonaByName(ctx context.Context, req *pb.GetPersonaByNameRequest) (*pb.Persona, error) {
	persona, err := s.store.GetPersonaByName(ctx, req.Author, req.Name, req.Version)
	if err != nil {
		if err == storage.ErrPersonaNotFound {
			return nil, status.Error(codes.NotFound, "persona not found")
		}
		return nil, status.Errorf(codes.Internal, "failed to get persona: %v", err)
	}

	return personaToProto(persona), nil
}

// UpdatePersona updates an existing persona.
func (s *FragmentService) UpdatePersona(ctx context.Context, req *pb.UpdatePersonaRequest) (*pb.Persona, error) {
	persona := &storage.Persona{
		ID:          req.Id,
		Name:        req.Name,
		Author:      req.Author,
		Version:     req.Version,
		Description: req.Description,
		Fragments:   fragmentRefsFromProto(req.Fragments),
	}

	updated, err := s.store.UpdatePersona(ctx, persona)
	if err != nil {
		if err == storage.ErrPersonaNotFound {
			return nil, status.Error(codes.NotFound, "persona not found")
		}
		if err == storage.ErrInvalidID {
			return nil, status.Error(codes.InvalidArgument, "invalid persona ID")
		}
		return nil, status.Errorf(codes.Internal, "failed to update persona: %v", err)
	}

	return personaToProto(updated), nil
}

// DeletePersona deletes a persona.
func (s *FragmentService) DeletePersona(ctx context.Context, req *pb.DeletePersonaRequest) (*emptypb.Empty, error) {
	err := s.store.DeletePersona(ctx, req.Id)
	if err != nil {
		if err == storage.ErrPersonaNotFound {
			return nil, status.Error(codes.NotFound, "persona not found")
		}
		if err == storage.ErrInvalidID {
			return nil, status.Error(codes.InvalidArgument, "invalid persona ID")
		}
		return nil, status.Errorf(codes.Internal, "failed to delete persona: %v", err)
	}

	return &emptypb.Empty{}, nil
}

// ListPersonas lists personas with optional filtering.
func (s *FragmentService) ListPersonas(ctx context.Context, req *pb.ListPersonasRequest) (*pb.ListPersonasResponse, error) {
	opts := storage.PersonaListOptions{
		Author:     req.Author,
		NamePrefix: req.NamePrefix,
		PageSize:   int(req.PageSize),
		PageToken:  req.PageToken,
		Sort:       storage.SortOrder(req.Sort),
	}

	result, err := s.store.ListPersonas(ctx, opts)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list personas: %v", err)
	}

	resp := &pb.ListPersonasResponse{
		NextPageToken: result.NextPageToken,
	}

	for _, persona := range result.Personas {
		resp.Personas = append(resp.Personas, personaToProto(persona))
	}

	return resp, nil
}

// DownloadPersona increments download count and returns persona with resolved fragments.
func (s *FragmentService) DownloadPersona(ctx context.Context, req *pb.DownloadPersonaRequest) (*pb.DownloadPersonaResponse, error) {
	persona, err := s.store.IncrementPersonaDownloads(ctx, req.Id)
	if err != nil {
		if err == storage.ErrPersonaNotFound {
			return nil, status.Error(codes.NotFound, "persona not found")
		}
		if err == storage.ErrInvalidID {
			return nil, status.Error(codes.InvalidArgument, "invalid persona ID")
		}
		return nil, status.Errorf(codes.Internal, "failed to increment downloads: %v", err)
	}

	// Resolve all fragment references
	var fragments []*pb.Fragment
	for _, ref := range persona.Fragments {
		frag, err := s.store.GetByName(ctx, ref.Author, ref.Name, ref.Version)
		if err != nil {
			// Skip missing fragments but continue
			continue
		}
		fragments = append(fragments, fragmentToProto(frag))
	}

	return &pb.DownloadPersonaResponse{
		Persona:   personaToProto(persona),
		Fragments: fragments,
	}, nil
}

// LikePersona adds a like to a persona.
func (s *FragmentService) LikePersona(ctx context.Context, req *pb.LikePersonaRequest) (*pb.Persona, error) {
	persona, err := s.store.IncrementPersonaLikes(ctx, req.Id)
	if err != nil {
		if err == storage.ErrPersonaNotFound {
			return nil, status.Error(codes.NotFound, "persona not found")
		}
		if err == storage.ErrInvalidID {
			return nil, status.Error(codes.InvalidArgument, "invalid persona ID")
		}
		return nil, status.Errorf(codes.Internal, "failed to like persona: %v", err)
	}

	return personaToProto(persona), nil
}

// DislikePersona adds a dislike with reason to a persona.
func (s *FragmentService) DislikePersona(ctx context.Context, req *pb.DislikePersonaRequest) (*pb.Persona, error) {
	if req.Reason == "" {
		return nil, status.Error(codes.InvalidArgument, "reason is required for dislike")
	}

	persona, err := s.store.AddPersonaDislike(ctx, req.Id, req.Reason)
	if err != nil {
		if err == storage.ErrPersonaNotFound {
			return nil, status.Error(codes.NotFound, "persona not found")
		}
		if err == storage.ErrInvalidID {
			return nil, status.Error(codes.InvalidArgument, "invalid persona ID")
		}
		return nil, status.Errorf(codes.Internal, "failed to dislike persona: %v", err)
	}

	return personaToProto(persona), nil
}

func personaToProto(persona *storage.Persona) *pb.Persona {
	return &pb.Persona{
		Id:          persona.ID,
		Name:        persona.Name,
		Author:      persona.Author,
		Version:     persona.Version,
		Description: persona.Description,
		Fragments:   fragmentRefsToProto(persona.Fragments),
		CreatedAt:   timestamppb.New(persona.CreatedAt),
		UpdatedAt:   timestamppb.New(persona.UpdatedAt),
		Downloads:   persona.Downloads,
		Likes:       persona.Likes,
		Dislikes:    persona.Dislikes,
	}
}

func fragmentRefsToProto(refs []storage.FragmentRef) []*pb.FragmentRef {
	result := make([]*pb.FragmentRef, len(refs))
	for i, ref := range refs {
		result[i] = &pb.FragmentRef{
			Name:    ref.Name,
			Author:  ref.Author,
			Version: ref.Version,
		}
	}
	return result
}

func fragmentRefsFromProto(refs []*pb.FragmentRef) []storage.FragmentRef {
	result := make([]storage.FragmentRef, len(refs))
	for i, ref := range refs {
		result[i] = storage.FragmentRef{
			Name:    ref.Name,
			Author:  ref.Author,
			Version: ref.Version,
		}
	}
	return result
}
