// Package mcpserver exposes the agent tools over MCP streamable HTTP.
package mcpserver

import (
	"context"
	"errors"
	"net/http"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sebmahr/deploy-gateway/internal/agentauth"
	"github.com/sebmahr/deploy-gateway/internal/service"
)

const instructions = `deploy-gateway deploys containerized apps into your own workspace on a shared server.
Workflow: whoami (limits, port range, naming) -> list_templates (how services can be exposed) -> validate -> deploy -> list / logs.
Images must already be pushed to a registry with a pinned tag. You never supply compose files, volumes paths, labels or security options; the gateway renders a hardened configuration from the stack you send.`

func New(svc *service.Service, version string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "deploy-gateway", Version: version}, &mcp.ServerOptions{Instructions: instructions})
	destructive := true
	addTool(s, &mcp.Tool{Name: "whoami", Description: "Show your workspace, quotas, current usage, published port range and the container naming scheme.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
		func(ctx context.Context, p service.Principal, _ service.NoInput) (service.WhoamiResult, error) {
			return svc.Whoami(ctx, p)
		})
	addTool(s, &mcp.Tool{Name: "list_templates", Description: "List the exposure templates granted to your workspace. Reference one per service via stack.services.<name>.expose.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
		func(ctx context.Context, p service.Principal, _ service.NoInput) (service.TemplatesResult, error) {
			return svc.ListTemplates(ctx, p)
		})
	addTool(s, &mcp.Tool{Name: "validate", Description: "Dry-run a deploy: validates the stack, checks quotas and ports, and returns hostnames, URLs and resource totals. Starts nothing.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
		func(ctx context.Context, p service.Principal, in service.DeployInput) (service.ValidateResult, error) {
			return svc.Validate(ctx, p, in)
		})
	addTool(s, &mcp.Tool{Name: "deploy", Description: "Create or update an app from prebuilt images. Pulls images, starts the services and waits for health checks."},
		func(ctx context.Context, p service.Principal, in service.DeployInput) (service.AppStatus, error) {
			return svc.Deploy(ctx, p, in)
		})
	addTool(s, &mcp.Tool{Name: "list", Description: "List your apps with service state, health, URLs, published ports and disk usage.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
		func(ctx context.Context, p service.Principal, _ service.NoInput) (service.ListResult, error) {
			return svc.List(ctx, p)
		})
	addTool(s, &mcp.Tool{Name: "logs", Description: "Recent logs of one service of one of your apps.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
		func(ctx context.Context, p service.Principal, in service.LogsInput) (service.LogsResult, error) {
			return svc.Logs(ctx, p, in)
		})
	addTool(s, &mcp.Tool{Name: "restart", Description: "Restart one service, or all services of an app when service is empty."},
		func(ctx context.Context, p service.Principal, in service.ServiceInput) (service.AppStatus, error) {
			return svc.Restart(ctx, p, in)
		})
	addTool(s, &mcp.Tool{Name: "stop", Description: "Stop one service, or all services of an app when service is empty. Data is kept."},
		func(ctx context.Context, p service.Principal, in service.ServiceInput) (service.AppStatus, error) {
			return svc.Stop(ctx, p, in)
		})
	addTool(s, &mcp.Tool{Name: "delete", Description: "Delete an app including its persistent data. confirm must equal the app name.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &destructive}},
		func(ctx context.Context, p service.Principal, in service.DeleteInput) (service.DeleteResult, error) {
			return svc.Delete(ctx, p, in)
		})
	return s
}

func addTool[In, Out any](s *mcp.Server, t *mcp.Tool, fn func(context.Context, service.Principal, In) (Out, error)) {
	mcp.AddTool(s, t, func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		var zero Out
		p, err := principal(req)
		if err != nil {
			return nil, zero, err
		}
		out, err := fn(ctx, p, in)
		if err != nil {
			return nil, zero, toolError(err)
		}
		return nil, out, nil
	})
}

// toolError passes *service.Error through (its Error() is JSON, so the agent
// receives structured text) and masks everything else as an internal error.
func toolError(err error) error {
	var se *service.Error
	if !errors.As(err, &se) {
		se = &service.Error{Code: service.CodeInternal, Message: "internal error"}
	}
	return se
}

func principal(req *mcp.CallToolRequest) (service.Principal, error) {
	if req.Extra == nil || req.Extra.TokenInfo == nil {
		return service.Principal{}, errors.New(`{"code":"unauthorized","message":"unauthorized"}`)
	}
	p, ok := req.Extra.TokenInfo.Extra["principal"].(service.Principal)
	if !ok {
		return service.Principal{}, errors.New(`{"code":"unauthorized","message":"unauthorized"}`)
	}
	return p, nil
}

// Handler serves stateless streamable HTTP behind bearer-token auth.
func Handler(srv *mcp.Server, a *agentauth.Authenticator) http.Handler {
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	return sdkauth.RequireBearerToken(a.MCPVerifier(), &sdkauth.RequireBearerTokenOptions{AllowMissingExpiration: true})(h)
}
