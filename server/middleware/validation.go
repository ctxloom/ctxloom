package middleware

import (
	"context"
	"unicode/utf8"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "mlcm/server/proto/fragmentspb"
)

// ValidationConfig configures input validation limits.
type ValidationConfig struct {
	// MaxNameLength is the maximum length for fragment names
	MaxNameLength int
	// MaxContentLength is the maximum length for fragment content
	MaxContentLength int
	// MaxTagLength is the maximum length for individual tags
	MaxTagLength int
	// MaxTagCount is the maximum number of tags per fragment
	MaxTagCount int
	// MaxVariableCount is the maximum number of variables per fragment
	MaxVariableCount int
	// MaxReasonLength is the maximum length for dislike reasons
	MaxReasonLength int
	// MaxQueryLength is the maximum length for search queries
	MaxQueryLength int
}

// DefaultValidationConfig returns sensible defaults for validation.
func DefaultValidationConfig() ValidationConfig {
	return ValidationConfig{
		MaxNameLength:    256,
		MaxContentLength: 100 * 1024, // 100KB
		MaxTagLength:     64,
		MaxTagCount:      20,
		MaxVariableCount: 50,
		MaxReasonLength:  1024,
		MaxQueryLength:   512,
	}
}

// Validator provides input validation for fragment operations.
type Validator struct {
	config ValidationConfig
}

// NewValidator creates a new validator with the given configuration.
func NewValidator(config ValidationConfig) *Validator {
	return &Validator{config: config}
}

// GRPCUnaryInterceptor returns a gRPC unary interceptor for input validation.
func (v *Validator) GRPCUnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		if err := v.validate(req); err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

func (v *Validator) validate(req interface{}) error {
	switch r := req.(type) {
	case *pb.CreateFragmentRequest:
		return v.validateCreateFragment(r)
	case *pb.UpdateFragmentRequest:
		return v.validateUpdateFragment(r)
	case *pb.DislikeFragmentRequest:
		return v.validateDislike(r)
	case *pb.SearchFragmentsRequest:
		return v.validateSearch(r)
	}
	return nil
}

func (v *Validator) validateCreateFragment(req *pb.CreateFragmentRequest) error {
	if req.Name == "" {
		return status.Error(codes.InvalidArgument, "name is required")
	}
	if utf8.RuneCountInString(req.Name) > v.config.MaxNameLength {
		return status.Errorf(codes.InvalidArgument, "name exceeds maximum length of %d", v.config.MaxNameLength)
	}
	if utf8.RuneCountInString(req.Content) > v.config.MaxContentLength {
		return status.Errorf(codes.InvalidArgument, "content exceeds maximum length of %d", v.config.MaxContentLength)
	}
	if len(req.Tags) > v.config.MaxTagCount {
		return status.Errorf(codes.InvalidArgument, "too many tags (max %d)", v.config.MaxTagCount)
	}
	for _, tag := range req.Tags {
		if utf8.RuneCountInString(tag) > v.config.MaxTagLength {
			return status.Errorf(codes.InvalidArgument, "tag exceeds maximum length of %d", v.config.MaxTagLength)
		}
	}
	if len(req.Variables) > v.config.MaxVariableCount {
		return status.Errorf(codes.InvalidArgument, "too many variables (max %d)", v.config.MaxVariableCount)
	}
	return nil
}

func (v *Validator) validateUpdateFragment(req *pb.UpdateFragmentRequest) error {
	if req.Id == "" {
		return status.Error(codes.InvalidArgument, "id is required")
	}
	if utf8.RuneCountInString(req.Name) > v.config.MaxNameLength {
		return status.Errorf(codes.InvalidArgument, "name exceeds maximum length of %d", v.config.MaxNameLength)
	}
	if utf8.RuneCountInString(req.Content) > v.config.MaxContentLength {
		return status.Errorf(codes.InvalidArgument, "content exceeds maximum length of %d", v.config.MaxContentLength)
	}
	if len(req.Tags) > v.config.MaxTagCount {
		return status.Errorf(codes.InvalidArgument, "too many tags (max %d)", v.config.MaxTagCount)
	}
	for _, tag := range req.Tags {
		if utf8.RuneCountInString(tag) > v.config.MaxTagLength {
			return status.Errorf(codes.InvalidArgument, "tag exceeds maximum length of %d", v.config.MaxTagLength)
		}
	}
	if len(req.Variables) > v.config.MaxVariableCount {
		return status.Errorf(codes.InvalidArgument, "too many variables (max %d)", v.config.MaxVariableCount)
	}
	return nil
}

func (v *Validator) validateDislike(req *pb.DislikeFragmentRequest) error {
	if req.Id == "" {
		return status.Error(codes.InvalidArgument, "id is required")
	}
	if req.Reason == "" {
		return status.Error(codes.InvalidArgument, "reason is required for dislike")
	}
	if utf8.RuneCountInString(req.Reason) > v.config.MaxReasonLength {
		return status.Errorf(codes.InvalidArgument, "reason exceeds maximum length of %d", v.config.MaxReasonLength)
	}
	return nil
}

func (v *Validator) validateSearch(req *pb.SearchFragmentsRequest) error {
	if utf8.RuneCountInString(req.Query) > v.config.MaxQueryLength {
		return status.Errorf(codes.InvalidArgument, "query exceeds maximum length of %d", v.config.MaxQueryLength)
	}
	if len(req.Tags) > v.config.MaxTagCount {
		return status.Errorf(codes.InvalidArgument, "too many tags (max %d)", v.config.MaxTagCount)
	}
	return nil
}
