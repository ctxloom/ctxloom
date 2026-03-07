package steps

import (
	"fmt"

	"github.com/cucumber/godog"
)

// mcpRequestID tracks the current request ID for MCP calls
var mcpRequestID = 0

// RegisterMCPSteps registers steps for MCP server operations.
func RegisterMCPSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^I send MCP initialize request$`, iSendMCPInitializeRequest)
	ctx.Step(`^I send MCP tools/list request$`, iSendMCPToolsListRequest)
	ctx.Step(`^I send MCP tools/call "([^"]*)" with:$`, iSendMCPToolsCallWith)
	ctx.Step(`^I send MCP tools/call "([^"]*)"$`, iSendMCPToolsCallNoArgs)
	ctx.Step(`^the MCP response should contain "([^"]*)"$`, theMCPResponseShouldContain)
	ctx.Step(`^the MCP response should not contain "([^"]*)"$`, theMCPResponseShouldNotContain)
	ctx.Step(`^the MCP response should have no error$`, theMCPResponseShouldHaveNoError)
	ctx.Step(`^the MCP response should have error containing "([^"]*)"$`, theMCPResponseShouldHaveErrorContaining)
}

func buildMCPRequest(method string, params string) string {
	mcpRequestID++
	if params == "" {
		return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"%s"}`, mcpRequestID, method)
	}
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"%s","params":%s}`, mcpRequestID, method, params)
}

func iSendMCPInitializeRequest() error {
	req := buildMCPRequest("initialize", `{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`)
	_ = TestEnv.RunSCMWithStdin(req+"\n", "mcp")
	return nil
}

func iSendMCPToolsListRequest() error {
	// First initialize, then list tools
	initReq := buildMCPRequest("initialize", `{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`)
	listReq := buildMCPRequest("tools/list", "")
	input := initReq + "\n" + listReq + "\n"
	_ = TestEnv.RunSCMWithStdin(input, "mcp")
	return nil
}

func iSendMCPToolsCallWith(toolName string, args *godog.DocString) error {
	initReq := buildMCPRequest("initialize", `{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`)
	callParams := fmt.Sprintf(`{"name":"%s","arguments":%s}`, toolName, args.Content)
	callReq := buildMCPRequest("tools/call", callParams)
	input := initReq + "\n" + callReq + "\n"
	_ = TestEnv.RunSCMWithStdin(input, "mcp")
	return nil
}

func iSendMCPToolsCallNoArgs(toolName string) error {
	initReq := buildMCPRequest("initialize", `{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`)
	callParams := fmt.Sprintf(`{"name":"%s","arguments":{}}`, toolName)
	callReq := buildMCPRequest("tools/call", callParams)
	input := initReq + "\n" + callReq + "\n"
	_ = TestEnv.RunSCMWithStdin(input, "mcp")
	return nil
}

func theMCPResponseShouldContain(expected string) error {
	output := TestEnv.LastOutput()
	if !contains(output, expected) {
		return fmt.Errorf("expected MCP response to contain %q\nActual output:\n%s", expected, output)
	}
	return nil
}

func theMCPResponseShouldNotContain(unexpected string) error {
	output := TestEnv.LastOutput()
	if contains(output, unexpected) {
		return fmt.Errorf("expected MCP response NOT to contain %q\nActual output:\n%s", unexpected, output)
	}
	return nil
}

func theMCPResponseShouldHaveNoError() error {
	output := TestEnv.LastOutput()
	if contains(output, `"error"`) {
		return fmt.Errorf("expected MCP response to have no error\nActual output:\n%s", output)
	}
	return nil
}

func theMCPResponseShouldHaveErrorContaining(expected string) error {
	output := TestEnv.LastOutput()
	if !contains(output, `"error"`) {
		return fmt.Errorf("expected MCP response to have an error\nActual output:\n%s", output)
	}
	if !contains(output, expected) {
		return fmt.Errorf("expected MCP error to contain %q\nActual output:\n%s", expected, output)
	}
	return nil
}
