package grpc

import (
	"context"
	"os"
	"os/exec"

	"github.com/hashicorp/go-plugin"
)

// GeneratorHandshake is the handshake config for generator plugins.
var GeneratorHandshake = plugin.HandshakeConfig{
	ProtocolVersion:  1,
	MagicCookieKey:   "SCM_GENERATOR_PLUGIN",
	MagicCookieValue: "scm-generator-v1",
}

// GeneratorPluginMap is the plugin map for generator plugins.
var GeneratorPluginMap = map[string]plugin.Plugin{
	"generator": &GeneratorGRPCPlugin{},
}

// GeneratorPluginClient wraps the go-plugin client for generators.
type GeneratorPluginClient struct {
	client     *plugin.Client
	rpcClient  plugin.ClientProtocol
	generator  GeneratorImpl
}

// NewGeneratorPluginClient creates a new generator plugin client.
// It spawns the generator process using self-invocation.
func NewGeneratorPluginClient(name string) (*GeneratorPluginClient, error) {
	execPath, err := os.Executable()
	if err != nil {
		return nil, err
	}

	client := plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig: GeneratorHandshake,
		Plugins:         GeneratorPluginMap,
		Cmd:             exec.Command(execPath, "generator", "serve", name),
		AllowedProtocols: []plugin.Protocol{plugin.ProtocolGRPC},
	})

	rpcClient, err := client.Client()
	if err != nil {
		client.Kill()
		return nil, err
	}

	raw, err := rpcClient.Dispense("generator")
	if err != nil {
		client.Kill()
		return nil, err
	}

	return &GeneratorPluginClient{
		client:    client,
		rpcClient: rpcClient,
		generator: raw.(GeneratorImpl),
	}, nil
}

// Generate runs the generator and returns the response.
func (c *GeneratorPluginClient) Generate(ctx context.Context, req *GenerateRequest) (*GenerateResponse, error) {
	return c.generator.Generate(ctx, req)
}

// Info returns the generator info.
func (c *GeneratorPluginClient) Info() (*GeneratorInfo, error) {
	return &GeneratorInfo{
		Name:        c.generator.Name(),
		Version:     c.generator.Version(),
		Description: c.generator.Description(),
	}, nil
}

// Kill terminates the plugin process.
func (c *GeneratorPluginClient) Kill() {
	c.client.Kill()
}

// NewExternalGeneratorClient creates a generator plugin client for an external binary.
// The binaryPath should be the full path to the generator binary.
func NewExternalGeneratorClient(binaryPath string) (*GeneratorPluginClient, error) {
	client := plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig:  GeneratorHandshake,
		Plugins:          GeneratorPluginMap,
		Cmd:              exec.Command(binaryPath),
		AllowedProtocols: []plugin.Protocol{plugin.ProtocolGRPC},
	})

	rpcClient, err := client.Client()
	if err != nil {
		client.Kill()
		return nil, err
	}

	raw, err := rpcClient.Dispense("generator")
	if err != nil {
		client.Kill()
		return nil, err
	}

	return &GeneratorPluginClient{
		client:    client,
		rpcClient: rpcClient,
		generator: raw.(GeneratorImpl),
	}, nil
}
