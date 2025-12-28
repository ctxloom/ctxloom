package grpc

import (
	"context"

	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
)

// GeneratorImpl is the interface that generator implementations must satisfy.
type GeneratorImpl interface {
	Name() string
	Version() string
	Description() string
	Generate(ctx context.Context, req *GenerateRequest) (*GenerateResponse, error)
}

// GeneratorGRPCServer wraps a GeneratorImpl to serve over gRPC.
type GeneratorGRPCServer struct {
	UnimplementedGeneratorServer
	Impl GeneratorImpl
}

// Info returns metadata about the generator.
func (s *GeneratorGRPCServer) Info(ctx context.Context, _ *Empty) (*GeneratorInfo, error) {
	// Note: Empty is from plugin.proto, shared across services
	return &GeneratorInfo{
		Name:        s.Impl.Name(),
		Version:     s.Impl.Version(),
		Description: s.Impl.Description(),
	}, nil
}

// Generate runs the generator and returns a context fragment.
func (s *GeneratorGRPCServer) Generate(ctx context.Context, req *GenerateRequest) (*GenerateResponse, error) {
	return s.Impl.Generate(ctx, req)
}

// GeneratorGRPCPlugin is the plugin.GRPCPlugin implementation for generators.
type GeneratorGRPCPlugin struct {
	plugin.Plugin
	Impl GeneratorImpl
}

// GRPCServer returns the gRPC server for the generator.
func (p *GeneratorGRPCPlugin) GRPCServer(broker *plugin.GRPCBroker, s *grpc.Server) error {
	RegisterGeneratorServer(s, &GeneratorGRPCServer{Impl: p.Impl})
	return nil
}

// GRPCClient returns a client for communicating with the generator.
func (p *GeneratorGRPCPlugin) GRPCClient(ctx context.Context, broker *plugin.GRPCBroker, c *grpc.ClientConn) (interface{}, error) {
	return &GeneratorGRPCClient{client: NewGeneratorClient(c)}, nil
}

// GeneratorGRPCClient wraps a gRPC client to implement GeneratorImpl.
type GeneratorGRPCClient struct {
	client GeneratorClient
}

// Name returns the generator name.
func (c *GeneratorGRPCClient) Name() string {
	info, err := c.client.Info(context.Background(), &Empty{})
	if err != nil {
		return "unknown"
	}
	return info.Name
}

// Version returns the generator version.
func (c *GeneratorGRPCClient) Version() string {
	info, err := c.client.Info(context.Background(), &Empty{})
	if err != nil {
		return "unknown"
	}
	return info.Version
}

// Description returns the generator description.
func (c *GeneratorGRPCClient) Description() string {
	info, err := c.client.Info(context.Background(), &Empty{})
	if err != nil {
		return ""
	}
	return info.Description
}

// Generate runs the generator.
func (c *GeneratorGRPCClient) Generate(ctx context.Context, req *GenerateRequest) (*GenerateResponse, error) {
	return c.client.Generate(ctx, req)
}
