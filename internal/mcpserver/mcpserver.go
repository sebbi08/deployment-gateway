// Package mcpserver exposes the agent tools over MCP streamable HTTP.
package mcpserver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sebbi08/deployment-gateway/internal/agentauth"
	"github.com/sebbi08/deployment-gateway/internal/service"
)

const instructions = `deploy-gateway deploys containerized apps into your own workspace on a shared server.
Workflow: whoami (limits, port range, naming) -> list_templates (how services can be exposed) -> validate -> deploy -> list / logs.
Images must already be pushed to a registry with a pinned tag. You never supply compose files, volumes paths, labels or security options; the gateway renders a hardened configuration from the stack you send.`

func New(svc *service.Service, version string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "deploy-gateway", Version: version}, &mcp.ServerOptions{Instructions: instructions})
	s.AddReceivingMiddleware(auditRejected(svc))
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
		if ran, ok := ctx.Value(ranKey{}).(*bool); ok {
			*ran = true // the service audits the call from here on
		}
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

type ranKey struct{}

// auditRejected writes an audit row for tool calls the SDK rejects before
// the tool handler runs (input schema violations, malformed arguments,
// unknown tools); calls that reach the handler are audited by the service.
func auditRejected(svc *service.Service) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method != "tools/call" {
				return next(ctx, method, req)
			}
			ran := new(bool)
			res, err := next(context.WithValue(ctx, ranKey{}, ran), method, req)
			call, ok := req.(*mcp.CallToolRequest)
			if *ran || !ok || call.Params == nil {
				return res, err
			}
			p, perr := principal(call)
			if perr != nil {
				return res, err
			}
			reason := ""
			if err != nil {
				reason = err.Error()
			} else if r, ok := res.(*mcp.CallToolResult); ok && r.IsError {
				for _, c := range r.Content {
					if tc, ok := c.(*mcp.TextContent); ok {
						reason = tc.Text
						break
					}
				}
			} else {
				return res, err
			}
			svc.AuditRejected(ctx, p, call.Params.Name, call.Params.Arguments, "rejected before the tool ran: "+reason)
			return res, err
		}
	}
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

const maxBody = agentauth.MaxBodyBytes

// Handler serves stateless streamable HTTP behind bearer-token auth, with a
// per-token cap on concurrent calls.
func Handler(srv *mcp.Server, a *agentauth.Authenticator) http.Handler {
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, MaxRequestBodyBytes: maxBody,
			// Every request needs a bearer token (checked first), which a
			// DNS-rebinding browser cannot send; the guard would only break
			// reverse proxies that reach the gateway over loopback.
			DisableLocalhostProtection: true})
	return sdkauth.RequireBearerToken(a.MCPVerifier(), &sdkauth.RequireBearerTokenOptions{AllowMissingExpiration: true})(
		a.LimitMCP(rejectBatches(h)))
}

// rejectBatches refuses JSON-RPC batches for every protocol version. The SDK
// accepts them before 2025-06-18 and runs all calls of a batch concurrently,
// holding every result in memory until the last one finishes.
func rejectBatches(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			next.ServeHTTP(w, r)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				http.Error(w, fmt.Sprintf("request body exceeds %d bytes", maxBody), http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "cannot read request body", http.StatusBadRequest)
			return
		}
		if trimmed := bytes.TrimLeft(body, " \t\r\n"); len(trimmed) > 0 && trimmed[0] == '[' {
			http.Error(w, "JSON-RPC batches are not supported", http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		next.ServeHTTP(w, r)
	})
}
